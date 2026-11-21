package debug

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// defaultMaxMessageBytes caps a single adapter frame when SessionOptions
// leaves MaxMessageBytes at zero.
const defaultMaxMessageBytes = 4 << 20

// defaultLocale is sent in `initialize` when BeginParams.Locale is empty.
const defaultLocale = "en-US"

// BreakpointsSetEvent is the synthetic event Begin emits for each
// setBreakpoints / setFunctionBreakpoints / setDataBreakpoints /
// setExceptionBreakpoints result. Its body is an object:
//
//	path: absolute source path, or the debugger address of an
//	  AddressBreakpoints entry ("" for the other three)
//	address: true for an AddressBreakpoints result (omitted otherwise)
//	breakpoints | functionBreakpoints | dataBreakpoints | exceptionFilters:
//	  response body.breakpoints, or null
//	error: failure message (omitted on success)
const BreakpointsSetEvent = "mincode.breakpointsSet"

// runInTerminalUnsupported is the failure message for the runInTerminal
// reverse request when SessionOptions.OnRunInTerminal is not set.
const runInTerminalUnsupported = "runInTerminal is not supported yet"

// ErrSessionClosed is returned by requests on a session that has closed (or
// closes while they wait). The error text includes the close reason.
var ErrSessionClosed = errors.New("debug session closed")

// SessionOptions configures a Session. OnEvent and OnClose may be nil.
type SessionOptions struct {
	ID, ProjectRoot, OwnerPluginID, DebuggerType string
	// MaxMessageBytes caps a single adapter frame; 0 means 4 MiB.
	MaxMessageBytes int
	// OnEvent receives every adapter event in arrival order. It runs on the
	// session's read goroutine, so it must not block for long and must not
	// call Request on the same session synchronously (the response could
	// never be read). Begin also calls it, from its own goroutine, with
	// synthetic BreakpointsSetEvent events, so it must be safe for
	// concurrent use.
	OnEvent func(ev Event)
	// OnClose is called exactly once, after the connection is closed.
	OnClose func(reason string)
	// OnRunInTerminal handles this session's adapter's `runInTerminal`
	// reverse request; nil answers it with a failure. It runs on its own
	// goroutine; ctx ends when the session closes. An error becomes the
	// failure message of the response.
	OnRunInTerminal func(ctx context.Context, args RunInTerminalArgs) (RunInTerminalResult, error)
}

// RunInTerminalArgs are the DAP `runInTerminal` arguments, as sent. An Env
// value of nil (JSON null) asks for the variable to be removed.
type RunInTerminalArgs struct {
	Kind  string             `json:"kind"`
	Title string             `json:"title"`
	Cwd   string             `json:"cwd"`
	Args  []string           `json:"args"`
	Env   map[string]*string `json:"env"`
}

// RunInTerminalResult is the `runInTerminal` response body.
type RunInTerminalResult struct {
	ProcessID      int `json:"processId,omitempty"`
	ShellProcessID int `json:"shellProcessId,omitempty"`
}

// Event is an adapter event forwarded to OnEvent.
type Event struct {
	SessionID string          `json:"sessionId"`
	Event     string          `json:"event"`
	Body      json.RawMessage `json:"body,omitempty"`
}

// RequestError is a failure response (`success: false`) from the adapter.
type RequestError struct {
	Command, Message string
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("dap %s failed: %s", e.Command, e.Message)
}

// SourceBreakpoint is one line breakpoint in a setBreakpoints request.
type SourceBreakpoint struct {
	Line         int    `json:"line"`
	Condition    string `json:"condition,omitempty"`
	HitCondition string `json:"hitCondition,omitempty"`
	LogMessage   string `json:"logMessage,omitempty"`
}

// FunctionBreakpoint is one entry of a setFunctionBreakpoints request; the
// adapter defines the syntax of Name.
type FunctionBreakpoint struct {
	Name         string `json:"name"`
	Condition    string `json:"condition,omitempty"`
	HitCondition string `json:"hitCondition,omitempty"`
}

// DataBreakpoint is one entry of a setDataBreakpoints request. DataID comes
// from the adapter's dataBreakpointInfo response; AccessType is "read",
// "write" or "readWrite" (empty: the adapter's default).
type DataBreakpoint struct {
	DataID       string `json:"dataId"`
	AccessType   string `json:"accessType,omitempty"`
	Condition    string `json:"condition,omitempty"`
	HitCondition string `json:"hitCondition,omitempty"`
}

// BeginParams drives the launch sequence run by Begin.
type BeginParams struct {
	AdapterID string
	// Request is "launch" or "attach".
	Request   string
	Arguments json.RawMessage
	// Breakpoints maps absolute source paths to their breakpoints.
	Breakpoints map[string][]SourceBreakpoint
	// AddressBreakpoints maps debugger-provided source addresses (opaque
	// strings the caller checked with ValidDebuggerAddress, never project
	// paths) to their breakpoints. They are sent after Breakpoints as
	// setBreakpoints {source: {path: <address>}}.
	AddressBreakpoints map[string][]SourceBreakpoint
	// FunctionBreakpoints and DataBreakpoints are sent only when the adapter
	// declares supportsFunctionBreakpoints / supportsDataBreakpoints.
	FunctionBreakpoints []FunctionBreakpoint
	DataBreakpoints     []DataBreakpoint
	ExceptionFilters    []string
	// ConfirmedOutsideCwd is the real path of the launch `cwd` outside the
	// project that the user confirmed with the launch (part of its
	// fingerprint, see CwdOutsideProject), "" when none: runInTerminal accepts
	// a cwd whose real path is exactly it.
	ConfirmedOutsideCwd string
	// Locale is the `initialize` locale; empty means "en-US".
	Locale string
}

// Session is a DAP client connection to one debug adapter. It correlates
// requests with responses, forwards events and answers reverse requests.
type Session struct {
	opts     SessionOptions
	conn     io.ReadWriteCloser
	maxBytes int

	writeMu            sync.Mutex // serializes frames written to conn
	seq                atomic.Int64
	logpointsSupported atomic.Bool

	mu          sync.Mutex
	pending     map[int64]chan Message
	closed      bool
	closeReason string
	// request is BeginParams.Request ("launch" or "attach"), "" before Begin.
	request string
	// confirmedOutsideCwd is BeginParams.ConfirmedOutsideCwd, cleaned ("" for
	// attach, before Begin, or without one); see runInTerminal.
	confirmedOutsideCwd string

	done            chan struct{} // closed by Close
	initialized     chan struct{} // closed on the first `initialized` event
	initializedOnce sync.Once
}

// newSession wraps conn and starts the read loop. The session owns conn and
// closes it on Close or on the first read error.
func newSession(conn io.ReadWriteCloser, opts SessionOptions) *Session {
	maxBytes := opts.MaxMessageBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxMessageBytes
	}
	s := &Session{
		opts:        opts,
		conn:        conn,
		maxBytes:    maxBytes,
		pending:     map[int64]chan Message{},
		done:        make(chan struct{}),
		initialized: make(chan struct{}),
	}
	go s.readLoop(bufio.NewReader(conn))
	return s
}

// ID returns the session id given in SessionOptions.
func (s *Session) ID() string { return s.opts.ID }

// Done is closed when the session closes (every end path: adapter exit,
// debug_stop, plugin lifecycle, Shutdown, a failed launch).
func (s *Session) Done() <-chan struct{} { return s.done }

// Close closes the connection, fails pending requests and calls OnClose with
// reason. Only the first call has any effect.
func (s *Session) Close(reason string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.closeReason = reason
	s.pending = map[int64]chan Message{} // waiters are released via done
	close(s.done)
	s.mu.Unlock()

	_ = s.conn.Close()
	if s.opts.OnClose != nil {
		s.opts.OnClose(reason)
	}
}

// Request sends a DAP request and waits for its response body. A failure
// response yields *RequestError; ctx cancellation yields ctx.Err().
//
// ctx only bounds the wait for the response: a write blocked on an adapter
// that stopped reading cannot be interrupted by ctx, only by Close. Request
// must not be called synchronously from OnEvent, because the response is
// delivered by the read goroutine that runs OnEvent.
func (s *Session) Request(ctx context.Context, command string, args any) (json.RawMessage, error) {
	call, err := s.send(command, args)
	if err != nil {
		return nil, err
	}
	return s.wait(ctx, call)
}

// Begin runs the DAP launch sequence: initialize, launch/attach (without
// waiting for its response), wait for `initialized`, setBreakpoints per file,
// then per debugger address (AddressBreakpoints), setFunctionBreakpoints and
// setDataBreakpoints when given and supported,
// setExceptionBreakpoints when filters are given, configurationDone (when the
// adapter supports it), then the launch/attach response. It returns the
// adapter capabilities. For a noDebug launch, a successful launch response
// also completes the sequence without initialized (java-debug omits that event
// intentionally). Adapters that send initialized first still get configured.
// ctx bounds the whole sequence; on error the caller should Close the session.
//
// A failure response to any of the breakpoint requests does not abort the
// launch: every result is reported through OnEvent as a synthetic
// BreakpointsSetEvent event (see reportBreakpointsSet) and the sequence goes on.
func (s *Session) Begin(ctx context.Context, b BeginParams) (json.RawMessage, error) {
	if b.Request != "launch" && b.Request != "attach" {
		return nil, fmt.Errorf("debug begin: request must be \"launch\" or \"attach\", got %q", b.Request)
	}
	s.mu.Lock()
	s.request = b.Request
	if b.Request == "launch" && filepath.IsAbs(b.ConfirmedOutsideCwd) {
		s.confirmedOutsideCwd = filepath.Clean(b.ConfirmedOutsideCwd)
	}
	s.mu.Unlock()
	locale := b.Locale
	if locale == "" {
		locale = defaultLocale
	}
	caps, err := s.Request(ctx, "initialize", map[string]any{
		"clientID":                     "mincode",
		"clientName":                   "MinCode",
		"adapterID":                    b.AdapterID,
		"linesStartAt1":                true,
		"columnsStartAt1":              true,
		"pathFormat":                   "path",
		"supportsVariableType":         true,
		"supportsRunInTerminalRequest": true,
		"locale":                       locale,
	})
	if err != nil {
		return nil, fmt.Errorf("debug begin: %w", err)
	}

	var capabilities struct {
		SupportsLogPoints           bool `json:"supportsLogPoints"`
		SupportsFunctionBreakpoints bool `json:"supportsFunctionBreakpoints"`
		SupportsDataBreakpoints     bool `json:"supportsDataBreakpoints"`
	}
	_ = json.Unmarshal(caps, &capabilities)
	s.logpointsSupported.Store(capabilities.SupportsLogPoints)

	launchArgs := b.Arguments
	if len(launchArgs) == 0 {
		launchArgs = json.RawMessage(`{}`)
	}
	var launchOptions struct {
		NoDebug bool `json:"noDebug"`
	}
	_ = json.Unmarshal(launchArgs, &launchOptions)
	noDebug := b.Request == "launch" && launchOptions.NoDebug
	launch, err := s.send(b.Request, launchArgs)
	if err != nil {
		return nil, fmt.Errorf("debug begin: %w", err)
	}
	// Adapters such as debugpy answer launch only after configurationDone, but
	// a failing launch may be answered before `initialized` ever arrives.
	var launchResp *Message
	select {
	case <-s.initialized:
	case m := <-launch.ch:
		launchResp = &m
		if err := responseError(launch.command, m); err != nil {
			return nil, fmt.Errorf("debug begin: %w", err)
		}
		if noDebug {
			select {
			case <-s.initialized:
				// Both may be ready: honor an initialized event that the
				// read loop delivered before the launch response.
			default:
				return caps, nil
			}
		}
		select {
		case <-s.initialized:
		case <-ctx.Done():
			return nil, fmt.Errorf("debug begin: waiting for initialized: %w", ctx.Err())
		case <-s.done:
			return nil, fmt.Errorf("debug begin: %w", s.closedErr())
		}
	case <-ctx.Done():
		s.dropPending(launch.seq)
		return nil, fmt.Errorf("debug begin: waiting for initialized: %w", ctx.Err())
	case <-s.done:
		// A launch response answered just before `terminated` explains a
		// failure (or completes a noDebug run) better than the close reason.
		select {
		case m := <-launch.ch:
			if err := responseError(launch.command, m); err != nil {
				return nil, fmt.Errorf("debug begin: %w", err)
			}
			if noDebug {
				return caps, nil
			}
		default:
		}
		return nil, fmt.Errorf("debug begin: %w", s.closedErr())
	}

	// noDebug still completes the handshake, but never installs stopping or logging breakpoints.
	if !noDebug {
		paths := make([]string, 0, len(b.Breakpoints))
		for p := range b.Breakpoints {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			bps := b.Breakpoints[p]
			if bps == nil {
				bps = []SourceBreakpoint{}
			}
			batch := s.prepareSourceBreakpoints(bps)
			body, err := s.Request(ctx, "setBreakpoints", batch.arguments(p))
			body, err = batch.finish(body, err)
			if err := s.reportBreakpointsSet(p, "breakpoints", body, err); err != nil {
				s.dropPending(launch.seq)
				return nil, fmt.Errorf("debug begin: breakpoints for %s: %w", p, err)
			}
		}
		for _, address := range slices.Sorted(maps.Keys(b.AddressBreakpoints)) {
			bps := b.AddressBreakpoints[address]
			if bps == nil {
				bps = []SourceBreakpoint{}
			}
			batch := s.prepareSourceBreakpoints(bps)
			body, err := s.Request(ctx, "setBreakpoints", batch.addressArguments(address))
			body, err = batch.finish(body, err)
			if err := s.reportAddressBreakpointsSet(address, body, err); err != nil {
				s.dropPending(launch.seq)
				return nil, fmt.Errorf("debug begin: breakpoints for address %q: %w", address, err)
			}
		}
		if capabilities.SupportsFunctionBreakpoints && len(b.FunctionBreakpoints) > 0 {
			body, err := s.Request(ctx, "setFunctionBreakpoints", map[string]any{"breakpoints": b.FunctionBreakpoints})
			if err := s.reportBreakpointsSet("", "functionBreakpoints", body, err); err != nil {
				s.dropPending(launch.seq)
				return nil, fmt.Errorf("debug begin: function breakpoints: %w", err)
			}
		}
		if capabilities.SupportsDataBreakpoints && len(b.DataBreakpoints) > 0 {
			body, err := s.Request(ctx, "setDataBreakpoints", map[string]any{"breakpoints": b.DataBreakpoints})
			if err := s.reportBreakpointsSet("", "dataBreakpoints", body, err); err != nil {
				s.dropPending(launch.seq)
				return nil, fmt.Errorf("debug begin: data breakpoints: %w", err)
			}
		}
		if len(b.ExceptionFilters) > 0 {
			body, err := s.Request(ctx, "setExceptionBreakpoints", map[string]any{"filters": b.ExceptionFilters})
			if err := s.reportBreakpointsSet("", "exceptionFilters", body, err); err != nil {
				s.dropPending(launch.seq)
				return nil, fmt.Errorf("debug begin: %w", err)
			}
		}
	}

	if supportsConfigurationDone(caps) {
		if _, err := s.Request(ctx, "configurationDone", nil); err != nil {
			s.dropPending(launch.seq)
			return nil, fmt.Errorf("debug begin: %w", err)
		}
	}

	if launchResp == nil {
		if _, err := s.wait(ctx, launch); err != nil {
			return nil, fmt.Errorf("debug begin: %w", err)
		}
	}
	return caps, nil
}

// call is an in-flight request.
type call struct {
	seq     int64
	command string
	ch      chan Message
}

// send registers and writes a request without waiting for its response.
func (s *Session) send(command string, args any) (*call, error) {
	var raw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("dap %s: encode arguments: %w", command, err)
		}
		raw = b
	}
	c := &call{seq: s.seq.Add(1), command: command, ch: make(chan Message, 1)}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, s.closedErr()
	}
	s.pending[c.seq] = c.ch
	s.mu.Unlock()

	if err := s.write(Message{Seq: c.seq, Type: "request", Command: command, Arguments: raw}); err != nil {
		s.dropPending(c.seq)
		if s.isClosed() {
			return nil, s.closedErr()
		}
		return nil, fmt.Errorf("dap %s: %w", command, err)
	}
	return c, nil
}

// wait blocks until c's response arrives, ctx ends or the session closes.
func (s *Session) wait(ctx context.Context, c *call) (json.RawMessage, error) {
	select {
	case m := <-c.ch:
		if err := responseError(c.command, m); err != nil {
			return nil, err
		}
		return m.Body, nil
	case <-ctx.Done():
		s.dropPending(c.seq)
		return nil, ctx.Err()
	case <-s.done:
		// The read loop delivers a response before it handles a later
		// `terminated`, so a response that raced the close is already
		// buffered: prefer it (a short program may end right after
		// answering).
		select {
		case m := <-c.ch:
			if err := responseError(c.command, m); err != nil {
				return nil, err
			}
			return m.Body, nil
		default:
		}
		return nil, s.closedErr()
	}
}

// reportBreakpointsSet emits a BreakpointsSetEvent for one setBreakpoints
// (key "breakpoints"), setFunctionBreakpoints ("functionBreakpoints"),
// setDataBreakpoints ("dataBreakpoints") or setExceptionBreakpoints
// ("exceptionFilters") result. An adapter failure response is reported and swallowed; any other
// error (ctx done, session closed, write failure) is returned.
func (s *Session) reportBreakpointsSet(path, key string, body json.RawMessage, reqErr error) error {
	return s.emitBreakpointsSet(path, false, key, body, reqErr)
}

// reportAddressBreakpointsSet is reportBreakpointsSet for one
// AddressBreakpoints entry: the event's path is the address and it carries
// address: true, so a frontend never files the result under a project path.
func (s *Session) reportAddressBreakpointsSet(address string, body json.RawMessage, reqErr error) error {
	return s.emitBreakpointsSet(address, true, "breakpoints", body, reqErr)
}

func (s *Session) emitBreakpointsSet(path string, address bool, key string, body json.RawMessage, reqErr error) error {
	var re *RequestError
	if reqErr != nil && !errors.As(reqErr, &re) {
		return reqErr
	}
	var resp struct {
		Breakpoints json.RawMessage `json:"breakpoints"`
	}
	if reqErr == nil && len(body) > 0 {
		_ = json.Unmarshal(body, &resp) // malformed body: report null
	}
	out := map[string]any{"path": path, key: resp.Breakpoints} // nil RawMessage encodes as null
	if address {
		out["address"] = true
	}
	if re != nil {
		out["error"] = re.Message
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", BreakpointsSetEvent, err)
	}
	s.emit(Event{SessionID: s.opts.ID, Event: BreakpointsSetEvent, Body: raw})
	return nil
}

func (s *Session) emit(ev Event) {
	if s.opts.OnEvent != nil {
		s.opts.OnEvent(ev)
	}
}

func (s *Session) write(m Message) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return WriteMessage(s.conn, m)
}

func (s *Session) dropPending(seq int64) {
	s.mu.Lock()
	delete(s.pending, seq)
	s.mu.Unlock()
}

func (s *Session) pendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// confirmedCwd returns the confirmed out-of-project launch `cwd` ("" when none).
func (s *Session) confirmedCwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.confirmedOutsideCwd
}

// requestKind returns "launch" or "attach" once Begin has started, else "".
func (s *Session) requestKind() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.request
}

func (s *Session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Session) closedErr() error {
	s.mu.Lock()
	reason := s.closeReason
	s.mu.Unlock()
	return fmt.Errorf("%w: %s", ErrSessionClosed, reason)
}

// readLoop dispatches adapter messages until the first read error, which
// closes the session: after a bad frame the stream cannot be resynchronised.
// It also stops once the session is closed, so frames still buffered in r
// (e.g. an `output` that arrived in the same write as `terminated`) are never
// delivered after OnClose.
func (s *Session) readLoop(r *bufio.Reader) {
	for {
		m, err := ReadMessage(r, s.maxBytes)
		if err != nil {
			s.Close(fmt.Sprintf("adapter connection: %v", err))
			return
		}
		select {
		case <-s.done:
			return
		default:
		}
		switch m.Type {
		case "response":
			s.mu.Lock()
			ch, ok := s.pending[m.RequestSeq]
			delete(s.pending, m.RequestSeq)
			s.mu.Unlock()
			if ok {
				ch <- m // buffered; each seq is answered at most once
			}
		case "event":
			s.handleEvent(m)
		case "request":
			s.answerReverseRequest(m)
		}
	}
}

// reservedEventPrefix names the host's own synthetic events (sessionEnded,
// runInTerminal, breakpointsSet). Adapter events with it are dropped so an
// adapter cannot impersonate the host.
const reservedEventPrefix = "mincode."

func (s *Session) handleEvent(m Message) {
	if strings.HasPrefix(m.Event, reservedEventPrefix) {
		return
	}
	if m.Event == "initialized" {
		s.initializedOnce.Do(func() { close(s.initialized) })
	}
	s.emit(Event{SessionID: s.opts.ID, Event: m.Event, Body: m.Body})
	if m.Event == "terminated" {
		s.Close("adapter terminated")
	}
}

// answerReverseRequest answers runInTerminal through OnRunInTerminal (when
// set) and fails every other reverse request. The reply is produced and
// written off the read goroutine so neither the handler nor a slow adapter
// reader can stall event delivery.
func (s *Session) answerReverseRequest(m Message) {
	if m.Command == "runInTerminal" && s.opts.OnRunInTerminal != nil {
		go func() { _ = s.write(s.runInTerminal(m)) }() // write errors surface as a read error / Close
		return
	}
	var msg string
	switch m.Command {
	case "runInTerminal":
		msg = runInTerminalUnsupported
	case "startDebugging":
		msg = "startDebugging is not supported"
	default:
		msg = fmt.Sprintf("reverse request %q is not supported", m.Command)
	}
	failed := false
	resp := Message{
		Seq:        s.seq.Add(1),
		Type:       "response",
		RequestSeq: m.Seq,
		Command:    m.Command,
		Success:    &failed,
		Message:    msg,
	}
	go func() { _ = s.write(resp) }() // write errors surface as a read error / Close
}

// runInTerminal runs OnRunInTerminal for request m and builds the response.
func (s *Session) runInTerminal(m Message) Message {
	resp := Message{Seq: s.seq.Add(1), Type: "response", RequestSeq: m.Seq, Command: m.Command}
	fail := func(msg string) Message {
		failed := false
		resp.Success = &failed
		resp.Message = msg
		return resp
	}
	var args RunInTerminalArgs
	if len(m.Arguments) == 0 || json.Unmarshal(m.Arguments, &args) != nil {
		return fail("runInTerminal: invalid arguments")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	result, err := s.opts.OnRunInTerminal(ctx, args)
	if err != nil {
		return fail(err.Error())
	}
	body, err := json.Marshal(result)
	if err != nil {
		return fail(fmt.Sprintf("runInTerminal: encode result: %v", err))
	}
	ok := true
	resp.Success = &ok
	resp.Body = body
	return resp
}

// responseError converts a failure response to command into *RequestError.
func responseError(command string, m Message) error {
	if m.Success != nil && *m.Success {
		return nil
	}
	msg := m.Message
	if msg == "" {
		var body struct {
			Error struct {
				Format string `json:"format"`
			} `json:"error"`
		}
		if json.Unmarshal(m.Body, &body) == nil {
			msg = body.Error.Format
		}
	}
	if msg == "" {
		msg = "request failed"
	}
	return &RequestError{Command: command, Message: msg}
}

func supportsConfigurationDone(caps json.RawMessage) bool {
	var c struct {
		SupportsConfigurationDoneRequest bool `json:"supportsConfigurationDoneRequest"`
	}
	return json.Unmarshal(caps, &c) == nil && c.SupportsConfigurationDoneRequest
}
