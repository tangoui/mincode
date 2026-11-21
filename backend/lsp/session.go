package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/filewatch"
)

const (
	requestTimeout  = 30 * time.Second
	maxPending      = 128
	maxStderrBytes  = 64 * 1024
	serverEventName = "project-lsp:event"
	// eventCoalesceWindow bounds how long high-frequency notifications
	// ($/progress per token, publishDiagnostics per URI) wait before forwarding.
	eventCoalesceWindow = 50 * time.Millisecond
)

type limitedBuffer struct {
	mu   sync.Mutex
	data []byte
	// max caps the kept tail; 0 means maxStderrBytes.
	max int
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	limit := buffer.max
	if limit <= 0 {
		limit = maxStderrBytes
	}
	buffer.data = append(buffer.data, data...)
	if len(buffer.data) > limit {
		buffer.data = append([]byte(nil), buffer.data[len(buffer.data)-limit:]...)
	}
	return len(data), nil
}

func (buffer *limitedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return strings.TrimSpace(string(buffer.data))
}

type rpcReply struct {
	result json.RawMessage
	err    error
}

type session struct {
	key               string
	root              string
	definition        serverDefinition
	cmd               *exec.Cmd
	stdin             io.Writer
	stderr            *limitedBuffer
	trace             limitedBuffer
	serverRequestMu   sync.Mutex
	serverRequests    map[uint64]json.RawMessage
	nextServerRequest atomic.Uint64
	failureMu         sync.Mutex
	failure           error
	writeMu           sync.Mutex
	pendingMu         sync.Mutex
	pending           map[uint64]chan rpcReply
	nextID            atomic.Uint64
	closed            atomic.Bool
	generation        uint64
	events            eventCoalescer
	// vueHybrid is set for vue-language-server 3.x, which delegates
	// TypeScript to the vue-ts companion through tsserver/request.
	vueHybrid bool
	// watcher serves the server's dynamic workspace/didChangeWatchedFiles
	// registrations (watch.go); created on first registration.
	watchMu      sync.Mutex
	watcher      *fileWatcher
	watchStopped bool
	// external is set for plugin language servers (external.go); nil for
	// built-in servers.
	external *ExternalServerSpec
	// jdtls only (jdtls.go): bundles sent in initialize and the last
	// language/status type, guarded by jdtlsMu; ready is set by ServiceReady.
	jdtlsMu     sync.Mutex
	bundles     []string
	status      string
	ready       atomic.Bool
	initialized atomic.Bool
	// configDir is the built-in jdtls's per-session OSGi configuration
	// area (jdtls_config.go), removed once by removeConfigDir; exited is
	// closed after the process has been waited for and configDir removed.
	configDir  string
	configOnce sync.Once
	exited     chan struct{}
}

// messageLimit is the per-session frame cap in both directions.
func (session *session) messageLimit() int {
	if session.external != nil && session.external.MessageMaxBytes > 0 && session.external.MessageMaxBytes < maxMessageBytes {
		return session.external.MessageMaxBytes
	}
	return maxMessageBytes
}

// eventCoalescer forwards server events to the frontend, keeping only the
// latest $/progress per token and publishDiagnostics per URI within a short
// window. emitMu serialises every emission so an immediate event is never
// delivered ahead of coalesced events received before it.
type eventCoalescer struct {
	emitMu  sync.Mutex
	mu      sync.Mutex
	pending map[string]serverEvent
	order   []string
	timer   *time.Timer
}

// droppedServerNotifications have no frontend consumer (the runtime in
// vue-app only handles publishDiagnostics, window/showMessage, server
// requests and $/serverStopped), so forwarding them only costs frontend event
// traffic.
var droppedServerNotifications = map[string]bool{
	"window/logMessage": true,
	"$/logTrace":        true,
	"telemetry/event":   true,
}

func coalesceKey(event serverEvent) string {
	if event.RequestID != nil {
		return ""
	}
	switch event.Method {
	case "textDocument/publishDiagnostics":
		var params struct {
			URI string `json:"uri"`
		}
		if json.Unmarshal(event.Params, &params) == nil && params.URI != "" {
			return "diagnostics:" + params.URI
		}
	case "$/progress":
		var params struct {
			Token json.RawMessage `json:"token"`
		}
		if json.Unmarshal(event.Params, &params) == nil && len(params.Token) > 0 {
			return "progress:" + string(params.Token)
		}
	}
	return ""
}

// forward routes one server event: dropped, coalesced, or emitted in order.
func (session *session) forward(event serverEvent) {
	if event.RequestID == nil && droppedServerNotifications[event.Method] {
		return
	}
	coalescer := &session.events
	if key := coalesceKey(event); key != "" {
		coalescer.mu.Lock()
		if coalescer.pending == nil {
			coalescer.pending = map[string]serverEvent{}
		}
		if _, exists := coalescer.pending[key]; !exists {
			coalescer.order = append(coalescer.order, key)
		}
		coalescer.pending[key] = event
		if coalescer.timer == nil {
			coalescer.timer = time.AfterFunc(eventCoalesceWindow, coalescer.flush)
		}
		coalescer.mu.Unlock()
		return
	}
	coalescer.emitMu.Lock()
	defer coalescer.emitMu.Unlock()
	coalescer.flushLocked()
	core.Emit(serverEventName, event)
}

func (coalescer *eventCoalescer) flush() {
	coalescer.emitMu.Lock()
	defer coalescer.emitMu.Unlock()
	coalescer.flushLocked()
}

// flushLocked emits pending events in first-arrival order; emitMu must be held.
func (coalescer *eventCoalescer) flushLocked() {
	coalescer.mu.Lock()
	pending, order := coalescer.pending, coalescer.order
	coalescer.pending, coalescer.order = nil, nil
	if coalescer.timer != nil {
		coalescer.timer.Stop()
		coalescer.timer = nil
	}
	coalescer.mu.Unlock()
	for _, key := range order {
		core.Emit(serverEventName, pending[key])
	}
}

func (session *session) log(message string) {
	_, _ = fmt.Fprintf(&session.trace, "%s %s\n", time.Now().UTC().Format(time.RFC3339), message)
}

// Never log params, results, URIs or arbitrary server message text.
func (session *session) logMethod(direction, method string) {
	if len(method) > 128 || strings.IndexFunc(method, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("$/_.-", r))
	}) >= 0 {
		method = "[redacted-method]"
	}
	session.log(direction + " " + method)
}

func (session *session) logs() string {
	output := session.trace.String()
	if session.stderr != nil && session.stderr.String() != "" {
		output += "\n[server stderr; untrusted]\n" + session.stderr.String()
	}
	return output
}

func (session *session) failureText() string {
	session.failureMu.Lock()
	defer session.failureMu.Unlock()
	if session.failure != nil {
		return session.failure.Error()
	}
	return ""
}

func (session *session) fail(reason error) {
	// A failed write can originate on the watcher emitter itself. Request
	// shutdown here; joining that loop synchronously would join ourselves.
	if session.beginClose(reason) {
		session.forward(serverEvent{Root: session.root, ServerID: session.definition.ID,
			Generation: session.generation, Method: "$/serverStopped", Params: json.RawMessage(`{}`)})
	}
}

func (session *session) start() error {
	session.log("lifecycle starting (process start is not LSP initialization)")
	if session.external != nil {
		return session.startExternal()
	}
	commandPath, err := cachedLookPath(session.definition.Command)
	if err != nil {
		session.log("error category=executable-unavailable")
		return fmt.Errorf("%s is not installed or not available on PATH", session.definition.Command)
	}
	if session.definition.ID == "vue-ls" {
		install, err := inspectVueServer(commandPath)
		if err == nil {
			err = install.compatibility()
		}
		if err != nil {
			session.log("error category=vue-version-rejected; reinstall required")
			return validateVueServer(commandPath)
		}
		session.vueHybrid = install.hybrid()
		if findTypeScriptLib(session.root) == "" {
			session.log("error category=typescript-sdk-unavailable stage=start")
			install := vueStandaloneInstall
			if session.vueHybrid {
				install = vueHybridInstall
			}
			return fmt.Errorf("Vue TypeScript SDK missing; reinstall with: %s", install)
		}
	}
	if session.definition.ID == vueCompanionID {
		if _, err := vueHybridCompanion(); err != nil {
			session.log("error category=vue-companion-unavailable stage=start")
			return err
		}
	}
	arguments := session.definition.Arguments
	if session.definition.ExtraArguments != nil {
		arguments = append(append([]string(nil), arguments...), session.definition.ExtraArguments(session.root)...)
	}
	if session.definition.ID == jdtlsServerID {
		dir, err := newJdtlsConfigDir()
		if err != nil {
			session.log("error category=jdtls-configuration-unavailable")
			return err
		}
		session.configDir = dir
		arguments = append(append([]string(nil), arguments...), jdtlsConfigArguments(dir)...)
	}
	session.cmd = exec.Command(commandPath, arguments...)
	session.cmd.Dir = session.root
	session.cmd.Env = builtinServerEnv(session.definition, commandPath, session.log)
	core.PrepareProcessTree(session.cmd)
	stdin, err := session.cmd.StdinPipe()
	if err != nil {
		session.log("error category=stdin-pipe-failed")
		return fmt.Errorf("open language-server stdin: %w", err)
	}
	stdout, err := session.cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		session.log("error category=stdout-pipe-failed")
		return fmt.Errorf("open language-server stdout: %w", err)
	}
	session.stderr = &limitedBuffer{}
	session.cmd.Stderr = session.stderr
	if err := session.cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		session.removeConfigDir()
		session.log("error category=process-exec-failed")
		return fmt.Errorf("start %s: %w", session.definition.Name, err)
	}
	session.stdin = stdin
	session.run(stdout)
	return nil
}

// run starts the read loop and the exit watcher of a started process.
func (session *session) run(stdout io.Reader) {
	session.log("lifecycle process started; awaiting initialize")
	readDone := make(chan struct{})
	session.exited = make(chan struct{})
	go func() {
		defer close(readDone)
		session.readLoop(stdout)
	}()
	go func() {
		// StdoutPipe must be drained before Wait closes it.
		<-readDone
		err := session.cmd.Wait()
		reason := errors.New("language server exited")
		if err != nil {
			reason = fmt.Errorf("language server exited: %w", err)
		}
		session.log("lifecycle process exited")
		session.fail(reason)
		session.removeConfigDir()
		close(session.exited)
		// This existing process owner is outside the emitter and can join it.
		session.stopWatching()
	}()
}

func (session *session) readLoop(stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 32*1024)
	for {
		payload, err := readMessageLimit(reader, session.messageLimit())
		if errors.Is(err, errInvalidPayload) {
			// The frame was read whole; drop it and keep the session.
			session.log("error invalid JSON payload skipped")
			continue
		}
		if err != nil {
			// Deliver any coalesced tail before the stream ends.
			session.events.flush()
			if !errors.Is(err, io.EOF) {
				session.log("error reading protocol frame")
				session.fail(fmt.Errorf("read language-server output: %w", err))
			}
			return
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &message) != nil {
			session.log("error invalid JSON-RPC envelope")
			continue
		}
		hasID := len(message.ID) > 0 && string(message.ID) != "null"
		if hasID && message.Method == "" {
			id, ok := responseID(message.ID)
			if !ok {
				session.log("error invalid response id")
				continue
			}
			if message.Error != nil {
				session.log("in response error")
			} else {
				session.log("in response ok")
			}
			session.deliver(id, rpcReply{result: message.Result, err: rpcError(message.Error)})
			continue
		}
		if message.Method != "" {
			session.logMethod("in", message.Method)
			if !hasID {
				session.observeLanguageStatus(message.Method, message.Params)
			}
			// Notification or server-to-client request.
			event := serverEvent{Root: session.root, ServerID: session.definition.ID, Generation: session.generation, Method: message.Method, Params: message.Params}
			if hasID {
				// Preserve the numeric bridge contract while accepting JSON-RPC string
				// and signed IDs. Restore the original ID when replying.
				var id uint64
				if json.Unmarshal(message.ID, &id) != nil || id > (1<<53)-1 {
					id = (1 << 52) + session.nextServerRequest.Add(1)
				}
				session.serverRequestMu.Lock()
				if session.serverRequests == nil {
					session.serverRequests = map[uint64]json.RawMessage{}
				}
				if len(session.serverRequests) >= maxPending {
					session.serverRequestMu.Unlock()
					session.fail(errors.New("too many pending server requests"))
					return
				}
				for session.serverRequests[id] != nil {
					id = (1 << 52) + session.nextServerRequest.Add(1)
				}
				session.serverRequests[id] = message.ID
				session.serverRequestMu.Unlock()
				event.RequestID = &id
				if err := session.applyWatchRegistration(message.Method, message.Params); err != nil {
					if errors.Is(err, filewatch.ErrBaselineTruncated) {
						// The project is larger than the watch limits. Refusing the
						// registration makes some servers (Intelephense) exit, so let
						// the frontend acknowledge it with nothing watched and tell
						// the user external changes will not reach this server.
						session.log("warn category=file-watcher-limited; external file changes are not watched")
						session.forward(serverEvent{Root: session.root, ServerID: session.definition.ID, Generation: session.generation,
							Method: fileWatchLimitedMethod, Params: json.RawMessage(`{}`)})
						session.forward(event)
						continue
					}
					// Do not let the frontend acknowledge a watcher whose silent
					// baseline was lost, replaced or cancelled.
					if err := session.replyError(id, -32603, "File watcher registration could not establish a complete baseline"); err != nil {
						session.fail(err)
						return
					}
					continue
				}
			}
			session.forward(event)
		}
	}
}

// responseID reads the id of a response to one of our requests. We send
// numeric ids; a server that echoes one as a decimal string still matches it.
func responseID(raw json.RawMessage) (uint64, bool) {
	var id uint64
	if json.Unmarshal(raw, &id) == nil {
		return id, true
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return 0, false
	}
	id, err := strconv.ParseUint(text, 10, 64)
	return id, err == nil
}

func rpcError(responseError *struct {
	Message string `json:"message"`
}) error {
	if responseError == nil {
		return nil
	}
	if responseError.Message == "" {
		return errors.New("language server returned an error")
	}
	return errors.New(responseError.Message)
}

func (session *session) deliver(id uint64, reply rpcReply) {
	session.pendingMu.Lock()
	pending, ok := session.pending[id]
	if ok {
		delete(session.pending, id)
	}
	session.pendingMu.Unlock()
	if ok {
		pending <- reply
	}
}

func (session *session) request(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if session.closed.Load() {
		return nil, errors.New("language server is stopped")
	}
	params, err := session.guardOutgoing(method, params, false)
	if err != nil {
		return nil, err
	}
	if method == "initialize" {
		var err error
		params, err = session.prepareInitialize(params)
		if err != nil {
			return nil, err
		}
	}
	id := session.nextID.Add(1)
	pending := make(chan rpcReply, 1)
	session.pendingMu.Lock()
	if session.closed.Load() {
		session.pendingMu.Unlock()
		return nil, errors.New("language server is stopped")
	}
	if len(session.pending) >= maxPending {
		session.pendingMu.Unlock()
		return nil, errors.New("too many pending language-server requests")
	}
	session.pending[id] = pending
	session.pendingMu.Unlock()
	message := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	// JSON-RPC params must be an object/array when present. Some servers
	// (notably OCaml) reject explicit null for parameterless shutdown.
	if len(params) > 0 && string(params) != "null" {
		message["params"] = params
	}
	if err := session.write(message); err != nil {
		session.removePending(id, pending)
		return nil, err
	}
	select {
	case reply := <-pending:
		if reply.err != nil {
			session.logMethod("error request", method)
		} else if method == "initialize" {
			session.initialized.Store(true)
			session.log("lifecycle initialize response received")
		}
		return reply.result, reply.err
	case <-ctx.Done():
		session.removePending(id, pending)
		session.logMethod("timeout/canceled request", method)
		cancelParams, _ := json.Marshal(map[string]uint64{"id": id})
		_ = session.notify("$/cancelRequest", cancelParams)
		return nil, ctx.Err()
	}
}

func (session *session) notify(method string, params json.RawMessage) error {
	if session.closed.Load() {
		return errors.New("language server is stopped")
	}
	if _, err := session.guardOutgoing(method, params, true); err != nil {
		return err
	}
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if len(params) > 0 && string(params) != "null" {
		message["params"] = params
	}
	return session.write(message)
}

func (session *session) write(message any) error {
	// Marshal JSON before acquiring the lock to minimise lock hold time.
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode LSP message: %w", err)
	}
	if len(payload) > session.messageLimit() {
		return fmt.Errorf("LSP message exceeds size limit")
	}
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if session.closed.Load() {
		return errors.New("language server is stopped")
	}
	if session.stdin == nil {
		return errors.New("language-server stdin is unavailable")
	}
	if err := writeFrameBytes(session.stdin, payload); err != nil {
		session.log("error writing protocol frame")
		session.fail(err)
		return err
	}
	var envelope struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(payload, &envelope)
	if envelope.Method != "" {
		session.logMethod("out", envelope.Method)
	} else {
		session.log("out response")
	}
	return nil
}

func (session *session) reply(id uint64, result json.RawMessage) error {
	if len(result) == 0 {
		result = json.RawMessage("null")
	}
	session.serverRequestMu.Lock()
	original := session.serverRequests[id]
	delete(session.serverRequests, id)
	session.serverRequestMu.Unlock()
	var wireID any = id
	if original != nil {
		wireID = original
	}
	return session.write(map[string]any{"jsonrpc": "2.0", "id": wireID, "result": result})
}

func (session *session) replyError(id uint64, code int, message string) error {
	session.serverRequestMu.Lock()
	original := session.serverRequests[id]
	delete(session.serverRequests, id)
	session.serverRequestMu.Unlock()
	var wireID any = id
	if original != nil {
		wireID = original
	}
	return session.write(map[string]any{"jsonrpc": "2.0", "id": wireID, "error": map[string]any{"code": code, "message": message}})
}

func (session *session) removePending(id uint64, expected chan rpcReply) {
	session.pendingMu.Lock()
	if current, ok := session.pending[id]; ok && current == expected {
		delete(session.pending, id)
	}
	session.pendingMu.Unlock()
}

func (session *session) close(reason error) bool {
	first := session.beginClose(reason)
	// IO/process shutdown has already been requested, even if fail won the
	// transition. Repeated external closes must still join watcher cleanup.
	session.stopWatching()
	return first
}

func (session *session) beginClose(reason error) bool {
	if !session.closed.CompareAndSwap(false, true) {
		return false
	}
	session.failureMu.Lock()
	session.failure = reason
	session.failureMu.Unlock()
	session.log("lifecycle session closed")
	// Close pipe IO and kill the process BEFORE joining the watcher: the
	// emitter may be blocked writing to a server which never consumes stdin.
	// Do not acquire writeMu: the blocked writer can currently own it.
	if stdin, ok := session.stdin.(io.Closer); ok {
		_ = stdin.Close()
	}
	if session.cmd != nil {
		_ = core.KillProcessTree(session.cmd)
	}
	if session.exited == nil {
		// Never started: no exit watcher will remove the directory.
		session.removeConfigDir()
	}
	session.requestStopWatching()
	session.pendingMu.Lock()
	pending := session.pending
	session.pending = map[uint64]chan rpcReply{}
	session.pendingMu.Unlock()
	for _, reply := range pending {
		reply <- rpcReply{err: reason}
	}
	return true
}

func canonicalRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("project root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("project root must be an existing directory")
	}
	return resolved, nil
}
