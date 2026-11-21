package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

const (
	pluginRPCMaxMessage = 1024 * 1024
	// pluginRPCMaxLargeReply is the largest stdout line a generation may send
	// once a negotiated feature raised its cap (runtimeFeature.MaxMessage),
	// and only as the response to a call made with callGenerationLarge.
	pluginRPCMaxLargeReply = 4 * 1024 * 1024
	// pluginRPCMaxStorageMessage is the message cap of a generation that
	// negotiated features.largeStorage: one host.storage.set request or a host
	// reply (host.storage.get) carrying a full pluginKVMaxValueBytes value.
	pluginRPCMaxStorageMessage = pluginKVMaxValueBytes + 64*1024
	// pluginRPCMaxLine is the largest stdout line any negotiated feature allows.
	pluginRPCMaxLine    = max(pluginRPCMaxLargeReply, pluginRPCMaxStorageMessage)
	pluginLogLimit      = 200
	pluginRPCWriteQueue = 8
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// pluginInitializeTimeout bounds plugin.initialize. macOS scans a newly
// installed unsigned binary on its first launch, which can take well over ten
// seconds for large plugins such as duckdb-driver.
const pluginInitializeTimeout = 60 * time.Second

type processRPCError struct {
	Method  string
	Code    int
	Message string
	Data    map[string]any
}

func (err *processRPCError) Error() string {
	return fmt.Sprintf("plugin rpc %s (%d): %s", err.Method, err.Code, err.Message)
}

func safeProcessRPCError(method string, rpcErr *rpcError) error {
	safe := &processRPCError{Method: method, Code: rpcErr.Code, Message: strings.TrimSpace(rpcErr.Message)}
	if safe.Message == "" || len(safe.Message) > 1000 {
		safe.Message = "plugin call failed"
	}
	if raw, ok := rpcErr.Data.(map[string]any); ok {
		data := make(map[string]any, 3)
		for _, key := range []string{"kind", "tool"} {
			if value, ok := raw[key].(string); ok && value != "" && len(value) <= 128 {
				data[key] = value
			}
		}
		if value, ok := raw["retryable"].(bool); ok {
			data["retryable"] = value
		}
		if len(data) > 0 {
			safe.Data = data
		}
	}
	return safe
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	// Host-only request provenance; never accepted from the wire.
	hostGeneration uint64
}

type rpcWriteRequest struct {
	ctx      context.Context
	data     []byte
	result   chan error
	validate func() error
}

type processRuntime struct {
	instance       uint64
	callerMetadata runtimeCallerMetadata
	pluginID       string
	root           string
	manifest       Manifest
	capabilities   map[api.Capability]struct{}
	log            func(level, message string)
	onExit         func(error)
	// Takes precedence over onExit; consumers must check both runtime identity
	// and generation under their state lock before applying it.
	onExitGeneration func(uint64, error)
	// Reports a plugin-declared command context value; wired by the manager so
	// the value lands on the owning plugin record.
	contextSet   func(key string, value any) error
	ownerManager *pluginManager
	ownerRecord  *pluginRecord

	mu      sync.Mutex
	writes  chan rpcWriteRequest
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	pending map[string]chan rpcEnvelope
	// largePending holds the pending ids whose response may exceed
	// pluginRPCMaxMessage (up to the generation's negotiated line cap).
	largePending   map[string]struct{}
	done           chan struct{}
	stopping       bool
	generation     uint64
	runContext     context.Context
	runCancel      context.CancelFunc
	requestID      atomic.Uint64
	heartbeatEnd   chan struct{}
	tasksSupported bool
	// taskEventsSupported: tasks plus features.taskEvents were negotiated, so
	// plugin.task.changed notifications wake the task monitor (tasks.go).
	taskEventsSupported bool
	tasks               map[string]*hostTask
	hostRequests        chan struct{}

	// configurationSupported: the plugin negotiated features.configuration = 1
	// and accepts plugin.configuration.changed requests (configuration_rpc.go).
	configurationSupported bool

	// features is the full outcome of the last plugin.initialize exchange
	// (runtime_features.go); tasksSupported/configurationSupported mirror it.
	features negotiatedRuntimeFeatures
	// eventSubscriptions holds the host events this generation subscribed to
	// with host.events.subscribe; eventsInflight bounds pending deliveries
	// (workspace_host.go).
	eventSubscribeMu       sync.Mutex // serializes subscription replacement and source admission
	eventSubscriptions     map[string]struct{}
	fileEventsSubscribedAt time.Time
	eventsInflight         atomic.Int32
	fileEventLoss          workspaceEventLoss // protected by mu; generation/scope-local
}

func newProcessRuntime(root string, manifest Manifest, logFn func(string, string), onExit func(error)) *processRuntime {
	capabilities := make(map[api.Capability]struct{}, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		capabilities[capability] = struct{}{}
	}
	return &processRuntime{
		instance: allocateRuntimeInstance(), callerMetadata: runtimeCallerMetadata{Source: "unknown"},
		pluginID: manifest.ID, root: root, manifest: manifest, capabilities: capabilities,
		log: logFn, onExit: onExit, hostRequests: make(chan struct{}, 8),
	}
}

func (runtimeHost *processRuntime) Start(ctx context.Context) error {
	if runtimeHost.instance == 0 {
		return errors.New("plugin runtime identity space is exhausted; restart MinCode")
	}
	runtimeHost.mu.Lock()
	if runtimeHost.cmd != nil {
		runtimeHost.mu.Unlock()
		return nil
	}
	if runtimeHost.generation == math.MaxUint64 {
		runtimeHost.mu.Unlock()
		return errors.New("plugin runtime generation space is exhausted; restart MinCode")
	}
	entry, err := runtimeHost.manifest.Runtime.selectedEntry()
	if err != nil {
		runtimeHost.mu.Unlock()
		return err
	}
	entryPath, err := resolvePluginPath(runtimeHost.root, entry)
	if err != nil {
		runtimeHost.mu.Unlock()
		return err
	}
	environment := pluginProcessEnvironment(runtimeHost.pluginID, runtimeHost.root, runtimeHost.manifest.APIVersion)
	command, err := pluginProcessCommand(entryPath, runtimeHost.manifest.Runtime.Args, environment)
	if err != nil {
		runtimeHost.mu.Unlock()
		return err
	}
	core.PrepareProcessTree(command)
	command.Dir = runtimeHost.root
	command.Env = environment
	stdin, err := command.StdinPipe()
	if err != nil {
		runtimeHost.mu.Unlock()
		return fmt.Errorf("open plugin stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		runtimeHost.mu.Unlock()
		return fmt.Errorf("open plugin stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		runtimeHost.mu.Unlock()
		return fmt.Errorf("open plugin stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		runtimeHost.mu.Unlock()
		if mismatch := executableTargetMismatch(entryPath, entry); mismatch != nil {
			return fmt.Errorf("start plugin process: %w: %w", mismatch, err)
		}
		return fmt.Errorf("start plugin process: %w", err)
	}
	runtimeHost.generation++
	generation := runtimeHost.generation
	runContext, runCancel := context.WithCancel(context.Background())
	runtimeHost.cmd = command
	runtimeHost.stdin = stdin
	writes := make(chan rpcWriteRequest, pluginRPCWriteQueue)
	runtimeHost.writes = writes
	runtimeHost.pending = map[string]chan rpcEnvelope{}
	runtimeHost.largePending = map[string]struct{}{}
	runtimeHost.done = make(chan struct{})
	runtimeHost.heartbeatEnd = make(chan struct{})
	runtimeHost.runContext = runContext
	runtimeHost.runCancel = runCancel
	runtimeHost.stopping = false
	runtimeHost.tasksSupported = false
	runtimeHost.taskEventsSupported = false
	runtimeHost.configurationSupported = false
	runtimeHost.features = negotiatedRuntimeFeatures{}
	offer := offeredRuntimeFeatures(runtimeHost.capabilities)
	runtimeHost.eventSubscriptions = nil
	runtimeHost.fileEventsSubscribedAt = time.Time{}
	runtimeHost.fileEventLoss = workspaceEventLoss{}
	runtimeHost.mu.Unlock()

	// The stdout line cap starts at 1 MiB and is raised only by negotiation.
	lineLimit := newRPCLineLimit()
	go writePluginRPC(runContext, stdin, writes)
	go runtimeHost.readStdout(generation, stdout, lineLimit)
	go runtimeHost.readStderr(generation, stderr)
	go func() {
		waitErr := command.Wait()
		runtimeHost.processExited(generation, waitErr)
	}()
	runtimeHost.logMessage("info", fmt.Sprintf("process started (pid %d)", command.Process.Pid))

	initializeCtx, cancel := context.WithTimeout(ctx, pluginInitializeTimeout)
	defer cancel()
	params, _ := json.Marshal(map[string]any{
		"apiVersion":             api.RuntimeProtocolVersion,
		"runtimeProtocolVersion": api.RuntimeProtocolVersion,
		"manifestApiVersion":     runtimeHost.manifest.APIVersion,
		// transport is informational: the plugin must already speak it (plugin.json runtime.transport).
		"transport": runtimeHost.manifest.Runtime.transport(),
		"features":  offer,
		"plugin": map[string]any{
			"id": runtimeHost.manifest.ID, "name": runtimeHost.manifest.Name,
			"version": runtimeHost.manifest.Version, "capabilities": capabilityStrings(runtimeHost.manifest.Capabilities),
		},
		"host": map[string]any{"name": "MinCode", "platform": runtime.GOOS},
	})
	initializeResult, initializeErr := runtimeHost.callGeneration(initializeCtx, generation, "plugin.initialize", params)
	if err := initializeErr; err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		cleanupErr := runtimeHost.ForceStopGeneration(cleanupCtx, generation)
		return fmt.Errorf("initialize plugin process: %w", errors.Join(err, cleanupErr))
	}
	negotiated := negotiateRuntimeFeatures(offer, initializeResult)
	for _, problem := range negotiated.problems {
		runtimeHost.logMessage("warn", problem)
	}
	identified, identityErr := checkInitializeIdentity(initializeResult, runtimeHost.manifest)
	if identityErr != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cleanupCancel()
		cleanupErr := runtimeHost.ForceStopGeneration(cleanupCtx, generation)
		return fmt.Errorf("initialize plugin process: %w", errors.Join(identityErr, cleanupErr))
	}
	if !identified {
		runtimeHost.logMessage("info", "plugin.initialize did not report plugin id/version; update the plugin SDK so the host can verify the entry belongs to this manifest")
	}
	runtimeHost.mu.Lock()
	if runtimeHost.generation != generation || runtimeHost.cmd != command || runtimeHost.stopping {
		runtimeHost.mu.Unlock()
		return errors.New("plugin process exited or generation changed during initialization")
	}
	runtimeHost.features = negotiated
	lineLimit.Store(int64(negotiated.maxMessage()))
	runtimeHost.tasksSupported = negotiated.has("tasks")
	runtimeHost.taskEventsSupported = runtimeHost.tasksSupported && negotiated.has("taskEvents")
	runtimeHost.configurationSupported = negotiated.has("configuration")
	runtimeHost.mu.Unlock()
	go runtimeHost.heartbeat(generation)
	return nil
}

func (runtimeHost *processRuntime) Stop(ctx context.Context) error {
	runtimeHost.mu.Lock()
	if runtimeHost.cmd == nil {
		done := runtimeHost.done
		runtimeHost.mu.Unlock()
		return waitPluginProcessExit(ctx, done)
	}
	runtimeHost.stopping = true
	generation := runtimeHost.generation
	done := runtimeHost.done
	heartbeatEnd := runtimeHost.heartbeatEnd
	runtimeHost.mu.Unlock()
	closeOnce(heartbeatEnd)

	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, shutdownErr := runtimeHost.callGeneration(shutdownCtx, generation, "plugin.shutdown", json.RawMessage(`{}`))
	cancel()
	graceTimer := time.NewTimer(500 * time.Millisecond)
	select {
	case <-done:
		graceTimer.Stop()
		return shutdownErr
	case <-ctx.Done():
		graceTimer.Stop()
		shutdownErr = joinPluginStopError(shutdownErr, ctx.Err())
	case <-graceTimer.C:
	}

	// Cleanup must not inherit a canceled caller context. Once shutdown begins, leaving
	// the external process alive would violate the manager's disabled/stopped state.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cleanupCancel()
	if err := runtimeHost.ForceStopGeneration(cleanupCtx, generation); err != nil {
		shutdownErr = joinPluginStopError(shutdownErr, err)
	}
	select {
	case <-done:
		return shutdownErr
	case <-cleanupCtx.Done():
		return joinPluginStopError(shutdownErr, errors.New("plugin process did not exit"))
	}
}

func joinPluginStopError(current, next error) error {
	if next == nil || errors.Is(current, next) {
		return current
	}
	if current == nil {
		return next
	}
	return errors.Join(current, next)
}

func (runtimeHost *processRuntime) forceStop(ctx context.Context) error {
	return runtimeHost.ForceStopGeneration(ctx, runtimeHost.Generation())
}

// ForceStopGeneration never resolves the target again after capture. A stale
// generation is already retired; it must not terminate its replacement. Success
// confirms Wait and pending-call cleanup, not merely delivery of a kill signal.
func (runtimeHost *processRuntime) ForceStopGeneration(ctx context.Context, generation uint64) error {
	runtimeHost.mu.Lock()
	if generation != runtimeHost.generation {
		runtimeHost.mu.Unlock()
		return nil
	}
	command, done := runtimeHost.cmd, runtimeHost.done
	if command == nil {
		runtimeHost.mu.Unlock()
		return waitPluginProcessExit(ctx, done)
	}
	runtimeHost.stopping = true
	closeOnce(runtimeHost.heartbeatEnd)
	if runtimeHost.runCancel != nil {
		runtimeHost.runCancel()
	}
	runtimeHost.mu.Unlock()
	// Attempt termination even for a canceled caller; only the confirmation wait
	// inherits its deadline. Never send plugin.shutdown through a newer transport.
	if err := core.KillProcessTree(command); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("terminate plugin process generation %d: %w", generation, err)
	}
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("plugin process generation %d termination unconfirmed: %w", generation, ctx.Err())
	}
}

// waitPluginProcessExit also covers the interval after Wait cleared cmd but
// before the exit owner has finished lifecycle hooks. A nil channel means the
// runtime has never started; a closed one is safe even with a canceled context.
func waitPluginProcessExit(ctx context.Context, done <-chan struct{}) error {
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("plugin process teardown unconfirmed: %w", ctx.Err())
	}
}

func (runtimeHost *processRuntime) Invoke(ctx context.Context, action, payload string) (string, error) {
	return runtimeHost.InvokeGeneration(ctx, runtimeHost.Generation(), action, payload)
}

func (runtimeHost *processRuntime) InvokeGeneration(ctx context.Context, generation uint64, action, payload string) (string, error) {
	result, err := runtimeHost.InvokeGenerationRaw(ctx, generation, action, payload)
	if err != nil {
		return "", err
	}
	return pluginInvokeResultJSON(result), nil
}

// InvokeGenerationRaw is InvokeGeneration without the plugin.invoke unwrap:
// service calls apply their own stricter rule (serviceResultJSON).
func (runtimeHost *processRuntime) InvokeGenerationRaw(ctx context.Context, generation uint64, action, payload string) (json.RawMessage, error) {
	params, err := json.Marshal(map[string]string{"action": action, "payload": payload})
	if err != nil {
		return nil, err
	}
	return runtimeHost.callGeneration(ctx, generation, "plugin.invoke", params)
}

// pluginInvokeResultJSON unwraps the plugin.invoke reply: an object with a
// top-level "result" member yields that member, anything else is returned as
// is. The reply already passed json.Unmarshal as part of the JSON-RPC
// envelope (handleStdoutMessage), so it is syntactically valid JSON and the
// returned string is too: callers may pass it on without json.Valid. The
// content stays untrusted.
//
// The common shape is resolved by one allocation-free scan of the top-level
// keys (the same helpers as splitDatabaseDriverPageRows) instead of a second
// full decode. Keys that json.Unmarshal would match
// case-insensitively or through escapes (and anything else unusual) fall back
// to the original decode, so the result is identical to it.
func pluginInvokeResultJSON(result json.RawMessage) string {
	if len(result) == 0 || string(result) == "null" {
		return "null"
	}
	if member, ok := topLevelResultMember(result); ok {
		if member != nil {
			return string(member)
		}
		return string(result)
	}
	var response struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(result, &response) == nil && len(response.Result) > 0 {
		return string(response.Result)
	}
	return string(result)
}

// topLevelResultMember scans syntactically valid JSON for the last top-level
// "result" member of an object. ok=false asks the caller to use json.Unmarshal
// (escaped or case-folded keys, malformed input); ok=true with a nil member
// means the value is not an object or has no such member.
func topLevelResultMember(data []byte) (member []byte, ok bool) {
	index := skipJSONSpace(data, 0)
	if index >= len(data) || data[index] != '{' {
		return nil, true
	}
	index = skipJSONSpace(data, index+1)
	if index < len(data) && data[index] == '}' {
		return nil, true
	}
	for index < len(data) {
		if data[index] != '"' {
			return nil, false
		}
		keyEnd := skipJSONString(data, index)
		if keyEnd < 0 || bytes.IndexByte(data[index:keyEnd], '\\') >= 0 {
			return nil, false
		}
		key := data[index+1 : keyEnd-1]
		index = skipJSONSpace(data, keyEnd)
		if index >= len(data) || data[index] != ':' {
			return nil, false
		}
		valueStart := skipJSONSpace(data, index+1)
		valueEnd := skipJSONValue(data, valueStart)
		if valueEnd < 0 {
			return nil, false
		}
		if string(key) == "result" {
			member = data[valueStart:valueEnd]
		} else if foldsToResult(key) {
			return nil, false
		}
		index = skipJSONSpace(data, valueEnd)
		if index >= len(data) {
			return nil, false
		}
		switch data[index] {
		case ',':
			index = skipJSONSpace(data, index+1)
		case '}':
			if skipJSONSpace(data, index+1) != len(data) {
				return nil, false
			}
			return member, true
		default:
			return nil, false
		}
	}
	return nil, false
}

// foldsToResult reports keys json.Unmarshal might still match to "result"
// (ASCII case variants and any non-ASCII spelling); those take the slow path.
func foldsToResult(key []byte) bool {
	if len(key) < len("result") {
		return false
	}
	for _, character := range key {
		if character >= 0x80 {
			return true
		}
	}
	return strings.EqualFold(string(key), "result")
}

func (runtimeHost *processRuntime) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	return runtimeHost.callGeneration(ctx, runtimeHost.Generation(), method, params)
}

func (runtimeHost *processRuntime) callGeneration(ctx context.Context, generation uint64, method string, params json.RawMessage) (json.RawMessage, error) {
	return runtimeHost.callGenerationLimit(ctx, generation, method, params, false)
}

// callGenerationLarge is callGeneration for a call whose response may exceed
// the 1 MiB line cap, up to the cap the generation negotiated (database
// driver protocol 1.1 pages). Without a raised cap it behaves the same.
func (runtimeHost *processRuntime) callGenerationLarge(ctx context.Context, generation uint64, method string, params json.RawMessage) (json.RawMessage, error) {
	return runtimeHost.callGenerationLimit(ctx, generation, method, params, true)
}

func (runtimeHost *processRuntime) callGenerationLimit(ctx context.Context, generation uint64, method string, params json.RawMessage, largeReply bool) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := strconv.FormatUint(runtimeHost.requestID.Add(1), 10)
	idJSON, _ := json.Marshal(id)
	responseChannel := make(chan rpcEnvelope, 1)

	runtimeHost.mu.Lock()
	if runtimeHost.cmd == nil || runtimeHost.stdin == nil {
		runtimeHost.mu.Unlock()
		return nil, errors.New("plugin process is not running")
	}
	if generation != runtimeHost.generation {
		runtimeHost.mu.Unlock()
		return nil, errors.New("plugin process generation changed")
	}
	runtimeHost.pending[id] = responseChannel
	if largeReply {
		runtimeHost.largePending[id] = struct{}{}
	}
	runtimeHost.mu.Unlock()

	message := rpcEnvelope{JSONRPC: "2.0", ID: idJSON, Method: method, Params: params}
	if err := runtimeHost.writeContext(ctx, generation, message); err != nil {
		runtimeHost.removePending(id)
		return nil, err
	}
	select {
	case response := <-responseChannel:
		if response.Error != nil {
			return nil, safeProcessRPCError(method, response.Error)
		}
		return response.Result, nil
	case <-ctx.Done():
		runtimeHost.removePending(id)
		return nil, ctx.Err()
	}
}

func (runtimeHost *processRuntime) write(generation uint64, message rpcEnvelope) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return runtimeHost.writeContext(ctx, generation, message)
}

func (runtimeHost *processRuntime) writeContext(ctx context.Context, generation uint64, message rpcEnvelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	// Host requests and notifications stay within 1 MiB; replies may use the
	// generation's raised limit (features.largeStorage).
	limit := pluginRPCMaxMessage
	if message.Method == "" && len(message.ID) > 0 {
		limit = runtimeHost.replyLimit()
	}
	if len(encoded) >= limit {
		if message.Method != "" || len(message.ID) == 0 {
			return errors.New("plugin rpc message exceeds 1 MiB")
		}
		// Keep the request ID, but never echo an oversized result or error payload.
		encoded, err = json.Marshal(rpcEnvelope{JSONRPC: "2.0", ID: message.ID,
			Error: &rpcError{Code: -32005, Message: "plugin rpc response exceeds 1 MiB"}})
		if err != nil || len(encoded) >= pluginRPCMaxMessage {
			return errors.New("plugin rpc response ID exceeds message limit")
		}
	}
	return runtimeHost.enqueueWrite(ctx, generation, runtimeHost.encodeMessage(encoded))
}

// encodeMessage wraps one JSON-RPC message for the negotiated stdio transport.
func (runtimeHost *processRuntime) encodeMessage(encoded []byte) []byte {
	if runtimeHost.manifest.Runtime.transport() == pluginTransportFramed {
		return encodeJSONFrame(encoded)
	}
	return append(encoded, '\n')
}

// writeBinary sends one binary frame to a framed-transport plugin process.
func (runtimeHost *processRuntime) writeBinary(ctx context.Context, generation uint64, channel string, data []byte) error {
	if runtimeHost.manifest.Runtime.transport() != pluginTransportFramed {
		return errors.New("plugin does not use the framed transport; declare runtime.transport \"framed\" to exchange binary data")
	}
	frame, err := encodeBinaryFrame(channel, data)
	if err != nil {
		return err
	}
	return runtimeHost.enqueueWrite(ctx, generation, frame)
}

func (runtimeHost *processRuntime) enqueueWrite(ctx context.Context, generation uint64, data []byte) error {
	runtimeHost.mu.Lock()
	if generation != runtimeHost.generation {
		runtimeHost.mu.Unlock()
		return errors.New("plugin process generation changed")
	}
	writes, runContext := runtimeHost.writes, runtimeHost.runContext
	runtimeHost.mu.Unlock()
	if writes == nil || runContext == nil {
		return errors.New("plugin process stdin is closed")
	}
	request := rpcWriteRequest{ctx: ctx, data: data, result: make(chan error, 1), validate: func() error {
		runtimeHost.mu.Lock()
		defer runtimeHost.mu.Unlock()
		if runtimeHost.generation != generation || runtimeHost.writes != writes {
			return errors.New("plugin process generation changed")
		}
		return runContext.Err()
	}}
	select {
	case writes <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-runContext.Done():
		return errors.New("plugin process exited")
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-runContext.Done():
		return errors.New("plugin process exited")
	}
}

// Exactly one writer per process generation, with a bounded queue and no per-call
// goroutines. No runtime lock is held during I/O: Stop can kill a non-reading
// child, and exec.Cmd.Wait closes the pipe to release an in-flight write.
func writePluginRPC(ctx context.Context, stdin io.Writer, writes <-chan rpcWriteRequest) {
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-writes:
			if err := request.ctx.Err(); err != nil {
				request.result <- err
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if request.validate != nil {
				if err := request.validate(); err != nil {
					request.result <- err
					continue
				}
			}
			// Cancellation may have happened while validating. The writer and
			// pipe are generation-local, so reload can never redirect this write.
			if err := request.ctx.Err(); err != nil {
				request.result <- err
				continue
			}
			n, err := stdin.Write(request.data)
			if err == nil && n != len(request.data) {
				err = io.ErrShortWrite
			}
			if err != nil {
				err = fmt.Errorf("write plugin rpc: %w", err)
			}
			request.result <- err
		}
	}
}

func (runtimeHost *processRuntime) readStdout(generation uint64, reader io.Reader, lineLimit *atomic.Int64) {
	if runtimeHost.manifest.Runtime.transport() == pluginTransportFramed {
		runtimeHost.readFramedStdout(generation, reader, lineLimit)
		return
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), pluginRPCMaxLine)
	scanner.Split(limitedRPCLines(lineLimit))
	busyDropped := false
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if !runtimeHost.handleStdoutMessage(generation, line, &busyDropped) {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		runtimeHost.logMessage("error", "plugin stdout: "+err.Error())
	}
}

func (runtimeHost *processRuntime) readFramedStdout(generation uint64, reader io.Reader, lineLimit *atomic.Int64) {
	buffered := bufio.NewReaderSize(reader, 64*1024)
	busyDropped := false
	for {
		frame, err := readPluginFrame(buffered, int(lineLimit.Load()))
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// A corrupt frame desynchronizes the stream; stop reading like an
				// oversized line does in the JSONL transport.
				runtimeHost.logMessage("error", "plugin stdout: "+err.Error())
			}
			return
		}
		if frame.kind == pluginFrameBinary {
			runtimeHost.deliverBinary(generation, frame.channel, frame.data)
			continue
		}
		if !runtimeHost.handleStdoutMessage(generation, frame.json, &busyDropped) {
			return
		}
	}
}

// handleStdoutMessage routes one JSON-RPC message from the plugin (a line or a
// JSON frame, already bounded by the negotiated stdout limit). It returns false
// when reading must stop: the generation is gone, or a message of 1 MiB or
// more is anything but a response to a call that allowed a large reply.
func (runtimeHost *processRuntime) handleStdoutMessage(generation uint64, line []byte, busyDropped *bool) bool {
	oversized := len(line) >= pluginRPCMaxMessage
	if trimmed := bytes.TrimLeft(line, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '[' {
		if oversized {
			runtimeHost.logMessage("error", "plugin stdout: "+bufio.ErrTooLong.Error())
			return false
		}
		runtimeHost.handleBatch(generation, trimmed, busyDropped)
		return true
	}
	var message rpcEnvelope
	if err := json.Unmarshal(line, &message); err != nil {
		if oversized {
			runtimeHost.logMessage("error", "plugin stdout: "+bufio.ErrTooLong.Error())
			return false
		}
		runtimeHost.logMessage("error", "invalid JSON-RPC output: "+truncateLog(string(line)))
		// JSON-RPC 2.0 §5.1: unparsable input -> -32700; parsable but not a
		// request object -> -32600. Both with id null.
		if json.Valid(line) {
			runtimeHost.replyNonBlocking(generation, protocolError(replyIDOf(line), jsonrpcInvalidRequest))
		} else {
			runtimeHost.replyNonBlocking(generation, protocolError(jsonNull, jsonrpcParseError))
		}
		return true
	}
	if reply, problem := invalidRPCMessage(message); problem != "" {
		runtimeHost.logMessage("error", problem)
		if reply != nil {
			runtimeHost.replyNonBlocking(generation, *reply)
		}
		return true
	}
	if message.Method != "" {
		if oversized && !(message.Method == "host.storage.set" && len(message.ID) > 0 && len(line) < pluginRPCMaxStorageMessage && runtimeHost.featureNegotiated("largeStorage")) {
			// Plugin requests and notifications never get the raised cap, except
			// a host.storage.set carrying a large value after largeStorage.
			runtimeHost.logMessage("error", "plugin stdout: "+bufio.ErrTooLong.Error())
			return false
		}
		if message.Method == pluginTaskChangedNotification && len(message.ID) == 0 {
			// A hint only, handled inline without a goroutine: it never
			// replies and at most wakes the task's monitor (tasks.go).
			runtimeHost.taskChanged(generation, message.Params)
			return true
		}
		if message.Method == hostUIPostMessageMethod && len(message.ID) == 0 {
			// Ordered view messages (ui_messages.go): emitted inline, never replied to.
			runtimeHost.uiMessageNotification(generation, message.Params)
			return true
		}
		select {
		case runtimeHost.hostRequests <- struct{}{}:
			go func() {
				defer func() { <-runtimeHost.hostRequests }()
				runtimeHost.handleHostRequest(generation, message)
			}()
		default:
			if !runtimeHost.respondBusy(generation, message) && !*busyDropped {
				*busyDropped = true
				runtimeHost.logMessage("error", "plugin busy response queue full; excess busy replies dropped")
			}
		}
		return true
	}
	return runtimeHost.deliverResponse(generation, message, oversized)
}

// deliverResponse hands a plugin reply to its pending host call. It reports
// false when the stream must end (oversized reply that was not allowed).
func (runtimeHost *processRuntime) deliverResponse(generation uint64, message rpcEnvelope, oversized bool) bool {
	id := rpcID(message.ID)
	if id == "" {
		if oversized {
			runtimeHost.logMessage("error", "plugin stdout: "+bufio.ErrTooLong.Error())
			return false
		}
		runtimeHost.logMessage("error", "ignored JSON-RPC response without id")
		return true
	}
	runtimeHost.mu.Lock()
	if generation != runtimeHost.generation {
		runtimeHost.mu.Unlock()
		return false
	}
	_, large := runtimeHost.largePending[id]
	if oversized && !large {
		// Only calls that allowed a large reply may exceed 1 MiB; anything
		// else is the same fatal oversize as before negotiation.
		runtimeHost.mu.Unlock()
		runtimeHost.logMessage("error", "plugin stdout: "+bufio.ErrTooLong.Error())
		return false
	}
	responseChannel := runtimeHost.pending[id]
	delete(runtimeHost.pending, id)
	delete(runtimeHost.largePending, id)
	runtimeHost.mu.Unlock()
	if responseChannel != nil {
		responseChannel <- message
	}
	return true
}

func (runtimeHost *processRuntime) readStderr(generation uint64, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 32*1024), pluginRPCMaxMessage)
	for scanner.Scan() {
		runtimeHost.mu.Lock()
		active := generation == runtimeHost.generation
		runtimeHost.mu.Unlock()
		if !active {
			return
		}
		runtimeHost.logMessage("stderr", scanner.Text())
	}
}

// Busy replies must never wait for stdin: the child may be blocked writing
// stdout itself. If the bounded writer queue is full, the peer must time out
// that rejected request; continue draining stdout so ordinary replies survive.
func (runtimeHost *processRuntime) respondBusy(generation uint64, message rpcEnvelope) bool {
	if len(message.ID) == 0 {
		return true
	}
	return runtimeHost.replyNonBlocking(generation, busyReply(message.ID))
}

func busyReply(id json.RawMessage) rpcEnvelope {
	return rpcEnvelope{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32004, Message: "plugin host request concurrency limit reached"}}
}

// replyNonBlocking queues a reply without waiting for stdin (see respondBusy).
func (runtimeHost *processRuntime) replyNonBlocking(generation uint64, reply rpcEnvelope) bool {
	encoded, err := json.Marshal(reply)
	if err != nil {
		return false
	}
	return runtimeHost.queueEncodedNonBlocking(generation, encoded)
}

func (runtimeHost *processRuntime) queueEncodedNonBlocking(generation uint64, encoded []byte) bool {
	if len(encoded) >= pluginRPCMaxMessage {
		return false
	}
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	if generation != runtimeHost.generation || runtimeHost.runContext == nil || runtimeHost.runContext.Err() != nil {
		return true
	}
	writes, ctx := runtimeHost.writes, runtimeHost.runContext
	request := rpcWriteRequest{ctx: ctx, data: runtimeHost.encodeMessage(encoded), result: make(chan error, 1), validate: func() error {
		runtimeHost.mu.Lock()
		defer runtimeHost.mu.Unlock()
		if runtimeHost.generation != generation || runtimeHost.writes != writes {
			return errors.New("plugin process generation changed")
		}
		return ctx.Err()
	}}
	select {
	case writes <- request:
		return true
	default:
		return false
	}
}

// SetExitHandler installs a generation-aware replacement for the legacy callback.
// Notifications run outside mu and carry the exiting generation even after reload.
func (runtimeHost *processRuntime) SetExitHandler(handler func(uint64, error)) {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	runtimeHost.onExitGeneration = handler
}

func (runtimeHost *processRuntime) Generation() uint64 {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	return runtimeHost.generation
}

func (runtimeHost *processRuntime) handleHostRequest(generation uint64, message rpcEnvelope) {
	runtimeHost.serveHostRequest(generation, message, func(result any, rpcErr *rpcError) {
		if len(message.ID) == 0 {
			return
		}
		if err := runtimeHost.write(generation, hostReply(message.ID, result, rpcErr)); err != nil {
			runtimeHost.logMessage("error", err.Error())
		}
	})
}

func hostReply(id json.RawMessage, result any, rpcErr *rpcError) rpcEnvelope {
	response := rpcEnvelope{JSONRPC: "2.0", ID: id, Error: rpcErr}
	if rpcErr == nil {
		response.Result, _ = json.Marshal(result)
	}
	return response
}

// serveHostRequest runs one plugin-to-host request; respond delivers its reply.
// It reports false (and never calls respond) for a stale process generation.
func (runtimeHost *processRuntime) serveHostRequest(generation uint64, message rpcEnvelope, respond func(any, *rpcError)) bool {
	runtimeHost.mu.Lock()
	if generation != runtimeHost.generation || runtimeHost.cmd == nil || runtimeHost.runContext == nil {
		runtimeHost.mu.Unlock()
		return false
	}
	runContext := runtimeHost.runContext
	runtimeHost.mu.Unlock()

	switch message.Method {
	case "host.log":
		var params struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil || strings.TrimSpace(params.Message) == "" {
			respond(nil, &rpcError{Code: -32602, Message: "invalid log params"})
			return true
		}
		runtimeHost.logMessage(params.Level, params.Message)
		respond(true, nil)
	case "host.api.describe":
		respond(runtimeHost.describeAPI(), nil)
	case "host.info":
		respond(map[string]any{"name": "MinCode", "apiVersion": api.RuntimeProtocolVersion, "runtimeProtocolVersion": api.RuntimeProtocolVersion, "manifestApiVersion": runtimeHost.manifest.APIVersion, "platform": runtime.GOOS}, nil)
	case "host.storage.get", "host.storage.set", "host.storage.delete", "host.storage.list", "host.storage.file.read", "host.storage.file.write", "host.storage.file.delete", "host.storage.file.list", "host.storage.file.stat":
		if _, allowed := runtimeHost.capabilities[api.CapabilityStorage]; !allowed {
			respond(nil, &rpcError{Code: -32001, Message: "storage capability not granted"})
			return true
		}
		message.hostGeneration = generation
		runtimeHost.handleStorageRequest(message, respond)
	case "host.secrets.get", "host.secrets.set", "host.secrets.delete", "host.secrets.list":
		if _, allowed := runtimeHost.capabilities[api.CapabilitySecrets]; !allowed {
			respond(nil, &rpcError{Code: -32001, Message: "secrets capability not granted"})
			return true
		}
		runtimeHost.handleSecretRequest(message, respond)

	case "host.shell.exec":
		if _, allowed := runtimeHost.capabilities[api.CapabilityShell]; !allowed {
			respond(nil, &rpcError{Code: -32001, Message: "shell capability not granted"})
			return true
		}
		runtimeHost.handleShellRequest(runContext, message, respond)
	case "host.ai.turn.start", "host.ai.turn.poll", "host.ai.turn.cancel", "host.ai.turn.approve", "host.ai.plugins", "host.ai.grants":
		if _, allowed := runtimeHost.capabilities[api.CapabilityAI]; !allowed {
			respond(nil, &rpcError{Code: -32001, Message: "ai capability not granted"})
			return true
		}
		runtimeHost.handleAITurnRequest(message, respond)
	case "host.context.set":
		if _, allowed := runtimeHost.capabilities[api.CapabilityCommands]; !allowed {
			respond(nil, &rpcError{Code: -32001, Message: "commands capability not granted"})
			return true
		}
		var params struct {
			Key   string `json:"key"`
			Value any    `json:"value"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			respond(nil, &rpcError{Code: -32602, Message: "invalid context params"})
			return true
		}
		if runtimeHost.contextSet == nil {
			respond(nil, &rpcError{Code: -32603, Message: "context store unavailable"})
			return true
		}
		if err := runtimeHost.contextSet(params.Key, params.Value); err != nil {
			respond(nil, &rpcError{Code: -32602, Message: err.Error()})
			return true
		}
		respond(true, nil)
	default:
		// Methods added by contribution points (registerHostMethod).
		message.hostGeneration = generation
		if runtimeHost.serveRegisteredHostMethod(message, respond) {
			return true
		}
		respond(nil, &rpcError{Code: -32601, Message: "host method not found"})
	}
	return true
}

func (runtimeHost *processRuntime) handleShellRequest(ctx context.Context, message rpcEnvelope, respond func(any, *rpcError)) {
	var params struct {
		Command     string `json:"command"`
		Cwd         string `json:"cwd"`
		TimeoutSecs uint64 `json:"timeoutSecs"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil || strings.TrimSpace(params.Command) == "" {
		respond(nil, &rpcError{Code: -32602, Message: "invalid shell params"})
		return
	}
	cwd := runtimeHost.root
	if relative := strings.TrimSpace(params.Cwd); relative != "" && relative != "." {
		resolved, err := resolvePluginPath(runtimeHost.root, relative)
		if err != nil {
			respond(nil, &rpcError{Code: -32602, Message: "shell cwd must stay inside the plugin directory"})
			return
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			respond(nil, &rpcError{Code: -32602, Message: "shell cwd is not a directory"})
			return
		}
		cwd = resolved
	}
	seconds := params.TimeoutSecs
	if seconds == 0 {
		seconds = 30
	}
	if seconds > 300 {
		seconds = 300
	}
	// JSON can expand each output byte to six bytes. Reserve envelope/ID and
	// truncation-marker space; non-RPC shell callers retain their existing budget.
	const rpcShellStreamBytes = (pluginRPCMaxMessage - 4096) / 12
	output, err := runShellCommandEnvironmentLimit(ctx, strings.TrimSpace(params.Command), cwd, time.Duration(seconds)*time.Second, pluginProcessEnvironment(runtimeHost.pluginID, runtimeHost.root, runtimeHost.manifest.APIVersion), rpcShellStreamBytes)
	if err != nil {
		respond(nil, &rpcError{Code: -32003, Message: err.Error()})
		return
	}
	respond(output, nil)
}

func (runtimeHost *processRuntime) handleStorageRequest(message rpcEnvelope, respond func(any, *rpcError)) {
	var params struct {
		Key      string          `json:"key"`
		Value    json.RawMessage `json:"value"`
		Path     string          `json:"path"`
		Offset   int64           `json:"offset"`
		MaxBytes int64           `json:"maxBytes"`
		Data     string          `json:"data"`
		Truncate bool            `json:"truncate"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		respond(nil, &rpcError{Code: -32602, Message: "invalid storage params"})
		return
	}
	if strings.TrimSpace(params.Key) == customAiPromptsKey && (message.Method == "host.storage.set" || message.Method == "host.storage.delete") {
		result, rpcErr := runtimeHost.customAiPromptRequest(message.hostGeneration, strings.TrimPrefix(message.Method, "host."), message.Params)
		respond(result, rpcErr)
		return
	}
	var result any
	var err error
	switch message.Method {
	case "host.storage.get":
		var value json.RawMessage
		var found bool
		value, found, err = pluginKVGetRaw(runtimeHost.pluginID, params.Key)
		if found {
			result = value
		}
	case "host.storage.set":
		err = pluginKVSetRaw(runtimeHost.pluginID, params.Key, params.Value)
		result = err == nil
	case "host.storage.delete":
		err = pluginKVDeleteRaw(runtimeHost.pluginID, params.Key)
		result = err == nil
	case "host.storage.list":
		result, err = pluginKVListRaw(runtimeHost.pluginID)
	case "host.storage.file.read":
		var data []byte
		var eof bool
		data, eof, err = pluginStorageRead(runtimeHost.pluginID, params.Path, params.Offset, params.MaxBytes)
		if errors.Is(err, fs.ErrNotExist) {
			err = nil // Missing files are distinct from empty files in the SDK.
		} else if err == nil {
			result = map[string]any{"data": encodeStorageData(data), "offset": params.Offset, "nextOffset": params.Offset + int64(len(data)), "eof": eof}
		}
	case "host.storage.file.write":
		var data []byte
		data, err = decodeStorageData(params.Data)
		if err == nil {
			err = pluginStorageWrite(runtimeHost.pluginID, params.Path, params.Offset, data, params.Truncate)
		}
		result = err == nil
	case "host.storage.file.delete":
		err = pluginStorageDelete(runtimeHost.pluginID, params.Path)
		result = err == nil
	case "host.storage.file.list":
		result, err = pluginStorageList(runtimeHost.pluginID, params.Path)
	case "host.storage.file.stat":
		result, err = pluginStorageStat(runtimeHost.pluginID, params.Path)
	}
	if err != nil {
		respond(nil, &rpcError{Code: -32002, Message: err.Error()})
		return
	}
	respond(result, nil)
}

func (runtimeHost *processRuntime) handleSecretRequest(message rpcEnvelope, respond func(any, *rpcError)) {
	var params struct {
		Key   string  `json:"key"`
		Value *string `json:"value"`
	}
	if len(message.Params) > 0 && string(message.Params) != "null" {
		if err := json.Unmarshal(message.Params, &params); err != nil {
			respond(nil, &rpcError{Code: -32602, Message: "invalid secrets params"})
			return
		}
	}
	if message.Method != "host.secrets.list" && pluginSecretKeyReserved(params.Key) {
		respond(nil, &rpcError{Code: -32002, Message: errPluginSecretReserved.Error()})
		return
	}
	var result any
	var err error
	switch message.Method {
	case "host.secrets.get":
		var value string
		var found bool
		value, found, err = pluginSecretGet(runtimeHost.pluginID, params.Key)
		if found {
			result = value
		}
	case "host.secrets.set":
		if params.Value == nil {
			respond(nil, &rpcError{Code: -32602, Message: "host.secrets.set requires a string value"})
			return
		}
		err = pluginSecretSet(runtimeHost.pluginID, params.Key, *params.Value)
		result = err == nil
	case "host.secrets.delete":
		err = pluginSecretDelete(runtimeHost.pluginID, params.Key)
		result = err == nil
	case "host.secrets.list":
		result, err = pluginFacingSecretList(runtimeHost.pluginID)
	}
	if err != nil {
		respond(nil, &rpcError{Code: -32002, Message: err.Error()})
		return
	}
	respond(result, nil)
}

func (runtimeHost *processRuntime) heartbeat(generation uint64) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	failures := 0
	for {
		runtimeHost.mu.Lock()
		end := runtimeHost.heartbeatEnd
		active := generation == runtimeHost.generation && runtimeHost.cmd != nil
		runtimeHost.mu.Unlock()
		if !active {
			return
		}
		select {
		case <-end:
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := runtimeHost.callGeneration(pingCtx, generation, "plugin.ping", json.RawMessage(`{}`))
			cancel()
			if err == nil {
				failures = 0
				continue
			}
			failures++
			runtimeHost.logMessage("error", fmt.Sprintf("heartbeat failed (%d/3): %v", failures, err))
			if failures >= 3 {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
				_ = runtimeHost.ForceStopGeneration(cleanupCtx, generation)
				cleanupCancel()
				return
			}
		}
	}
}

func (runtimeHost *processRuntime) processExited(generation uint64, waitErr error) {
	runtimeHost.mu.Lock()
	if generation != runtimeHost.generation {
		runtimeHost.mu.Unlock()
		return
	}
	stopping := runtimeHost.stopping
	pending := runtimeHost.pending
	done := runtimeHost.done
	heartbeatEnd := runtimeHost.heartbeatEnd
	runCancel := runtimeHost.runCancel
	onExit, onExitGeneration := runtimeHost.onExit, runtimeHost.onExitGeneration
	runtimeHost.cmd = nil
	runtimeHost.stdin = nil
	runtimeHost.pending = nil
	runtimeHost.largePending = nil
	runtimeHost.runContext = nil
	runtimeHost.runCancel = nil
	runtimeHost.stopping = false
	runtimeHost.eventSubscriptions = nil
	runtimeHost.fileEventsSubscribedAt = time.Time{}
	runtimeHost.fileEventLoss = workspaceEventLoss{}
	if runCancel != nil {
		runCancel()
	}
	closeOnce(heartbeatEnd)
	for _, responseChannel := range pending {
		responseChannel <- rpcEnvelope{Error: &rpcError{Code: -32000, Message: "plugin process exited"}}
	}
	runtimeHost.mu.Unlock()
	// Completion includes owner hooks (storage, sessions and UI), not just
	// command.Wait/pending replies. Stop must join them before fixture/data
	// owners may release or replace their resources.
	defer closeOnce(done)
	pluginAITurns.abandonPlugin(runtimeHost.pluginID)
	if stopping {
		runtimeHost.logMessage("info", "process stopped")
		return
	}
	if waitErr == nil {
		waitErr = errors.New("plugin process exited unexpectedly")
	}
	runtimeHost.logMessage("error", waitErr.Error())
	if onExitGeneration != nil {
		onExitGeneration(generation, waitErr)
	} else if onExit != nil {
		onExit(waitErr)
	}
}

func (runtimeHost *processRuntime) removePending(id string) {
	runtimeHost.mu.Lock()
	delete(runtimeHost.pending, id)
	delete(runtimeHost.largePending, id)
	runtimeHost.mu.Unlock()
}

// newRPCLineLimit returns a stdout line cap at the default 1 MiB.
func newRPCLineLimit() *atomic.Int64 {
	limit := &atomic.Int64{}
	limit.Store(pluginRPCMaxMessage)
	return limit
}

// limitedRPCLines splits stdout into lines like bufio.ScanLines, failing with
// bufio.ErrTooLong (fatal for the reader) once a line reaches the current cap
// of its generation, read on every call so negotiation can raise it.
func limitedRPCLines(lineLimit *atomic.Int64) bufio.SplitFunc {
	return func(data []byte, atEOF bool) (int, []byte, error) {
		limit := int(lineLimit.Load())
		if index := bytes.IndexByte(data, '\n'); index >= 0 {
			if index >= limit {
				return 0, nil, bufio.ErrTooLong
			}
			return bufio.ScanLines(data, atEOF)
		}
		if len(data) >= limit {
			return 0, nil, bufio.ErrTooLong
		}
		return bufio.ScanLines(data, atEOF)
	}
}

func (runtimeHost *processRuntime) logMessage(level, message string) {
	if runtimeHost.log != nil {
		runtimeHost.log(strings.TrimSpace(level), truncateLog(message))
	}
}

func rpcID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var id string
	if json.Unmarshal(raw, &id) == nil {
		return id
	}
	return string(raw)
}

func truncateLog(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 4096 {
		return message[:4096] + "…"
	}
	return message
}

func closeOnce(channel chan struct{}) {
	if channel == nil {
		return
	}
	defer func() { _ = recover() }()
	select {
	case <-channel:
	default:
		close(channel)
	}
}

func pluginProcessEnvironment(pluginID, root string, manifestAPIVersion int) []string {
	allowed := map[string]struct{}{
		"PATH": {}, "HOME": {}, "USERPROFILE": {}, "TMP": {}, "TEMP": {}, "TMPDIR": {},
		"SYSTEMROOT": {}, "COMSPEC": {}, "PATHEXT": {}, "LANG": {}, "LC_ALL": {},
		// Windows per-user folders (the counterparts of HOME); installers build paths from them.
		"LOCALAPPDATA": {}, "APPDATA": {},
	}
	environment := make([]string, 0, len(allowed)+3)
	for _, item := range os.Environ() {
		name, _, hasSeparator := strings.Cut(item, "=")
		if _, include := allowed[strings.ToUpper(name)]; hasSeparator && include {
			environment = append(environment, item)
		}
	}
	if runtime.GOOS != "windows" {
		pathValue := os.Getenv("PATH")
		for _, directory := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
			found := false
			for _, current := range filepath.SplitList(pathValue) {
				if current == directory {
					found = true
					break
				}
			}
			if !found {
				if pathValue != "" {
					pathValue += string(os.PathListSeparator)
				}
				pathValue += directory
			}
		}
		filtered := environment[:0]
		for _, item := range environment {
			if !strings.HasPrefix(item, "PATH=") {
				filtered = append(filtered, item)
			}
		}
		environment = append(filtered, "PATH="+pathValue)
	}
	environment = append(environment,
		"MINCODE_PLUGIN_ID="+pluginID,
		"MINCODE_PLUGIN_ROOT="+root,
		"MINCODE_PLUGIN_API_VERSION="+strconv.Itoa(api.RuntimeProtocolVersion),
		"MINCODE_PLUGIN_RUNTIME_PROTOCOL_VERSION="+strconv.Itoa(api.RuntimeProtocolVersion),
		"MINCODE_PLUGIN_MANIFEST_API_VERSION="+strconv.Itoa(manifestAPIVersion),
	)
	return environment
}

func pluginProcessCommand(entry string, args, environment []string) (*exec.Cmd, error) {
	if !strings.HasSuffix(strings.ToLower(entry), ".mjs") {
		return exec.Command(entry, args...), nil
	}
	node, err := findNodePluginInterpreter(environment)
	if err != nil {
		return nil, err
	}
	return exec.Command(node, append([]string{entry}, args...)...), nil
}

// Only preflight the exact Node shebang used by bundled plugins; other runtimes
// keep their existing launch semantics. Do not execute shell startup scripts.
func checkNodePluginInterpreter(entry string, environment []string) error {
	_, err := findNodePluginInterpreterForEntry(entry, environment)
	return err
}

func findNodePluginInterpreterForEntry(entry string, environment []string) (string, error) {
	file, err := os.Open(entry)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 256)
	line, _, err := reader.ReadLine()
	if err != nil || strings.TrimSpace(string(line)) != "#!/usr/bin/env node" {
		return entry, nil
	}
	return findNodePluginInterpreter(environment)
}

func findNodePluginInterpreter(environment []string) (string, error) {
	for _, item := range environment {
		if !strings.HasPrefix(item, "PATH=") {
			continue
		}
		for _, directory := range filepath.SplitList(strings.TrimPrefix(item, "PATH=")) {
			if !filepath.IsAbs(directory) {
				continue
			}
			for _, executable := range []string{"node", "node.exe"} {
				candidate := filepath.Join(directory, executable)
				info, err := os.Stat(candidate)
				if err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0111 != 0) {
					return candidate, nil
				}
			}
		}
	}
	return "", errors.New("插件需要 Node.js，但插件启动 PATH 中未找到可执行的 node；请安装 Node.js 或将其目录加入 MinCode 启动环境后重启应用")
}
