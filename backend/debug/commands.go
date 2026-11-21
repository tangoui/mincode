package debug

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	projectfs "mincode/app/backend/fs"
)

// Frontend commands for registered debug sessions. Errors that the frontend
// may branch on start with a code: "not_found: …", "forbidden: …",
// "invalid_request: …", "timeout: …", "cancelled: …", "unavailable: …"
// (errSessionEnded: the session ended while the request was in flight).

const (
	defaultRequestTimeout = 15 * time.Second
	maxRequestTimeout     = 120 * time.Second
	// stopGrace is how long a graceful stop (debug_stop, plugin lifecycle,
	// app exit) waits for the session to end after `disconnect` before the
	// adapter is killed.
	stopGrace = 2 * time.Second
)

// errSessionEnded is returned by debug_request and debug_set_breakpoints when
// the session closes before the adapter answered.
var errSessionEnded = errors.New("unavailable: debug session ended")

// Close reasons used by debug_stop.
const (
	stopReason       = "debug session stopped"
	stopForcedReason = "debug adapter did not end after disconnect; killed"
)

// forbiddenRequests maps the DAP commands the frontend may not issue through
// debug_request to the reason. The first five are sent only by the host
// (Begin, the reverse-request handler); restart may carry new launch/attach
// arguments, which would bypass the host-only, user-confirmed launch;
// setBreakpoints must go through debug_set_breakpoints so its project-root
// check cannot be bypassed. A raw `disconnect` stays allowed.
var forbiddenRequests = []struct{ command, reason string }{
	{"initialize", "is sent only by MinCode"},
	{"launch", "is sent only by MinCode"},
	{"attach", "is sent only by MinCode"},
	{"configurationDone", "is sent only by MinCode"},
	{"runInTerminal", "is sent only by MinCode"},
	{"restart", "could change the confirmed launch arguments"},
	{"setBreakpoints", "must be sent through debug_set_breakpoints"},
}

func init() {
	core.RegisterCtx("debug_list_sessions", listSessionsHandler)
	core.RegisterCtx("debug_request", requestHandler)
	core.RegisterCtx("debug_set_breakpoints", setBreakpointsHandler)
	core.RegisterCtx("debug_stop", stopHandler)
}

func listSessionsHandler(_ context.Context, _, _ string) (string, error) {
	return core.EncodeResult(List())
}

type requestArgs struct {
	SessionID string          `json:"sessionId"`
	Command   string          `json:"command"`
	Arguments json.RawMessage `json:"arguments"`
	TimeoutMs int             `json:"timeoutMs"`
}

func requestHandler(ctx context.Context, _, argsJSON string) (string, error) {
	var args requestArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", fmt.Errorf("invalid_request: %w", err)
	}
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return "", errors.New("invalid_request: command is empty")
	}
	for _, f := range forbiddenRequests {
		if strings.EqualFold(command, f.command) {
			return "", fmt.Errorf("forbidden: %s %s", f.command, f.reason)
		}
	}
	var dapArgs any // untyped nil: the request carries no arguments
	if raw := bytes.TrimSpace(args.Arguments); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if raw[0] != '{' {
			return "", errors.New("invalid_request: arguments must be a JSON object")
		}
		dapArgs = json.RawMessage(raw)
	}
	timeout, err := requestTimeout(args.TimeoutMs)
	if err != nil {
		return "", err
	}
	s, err := lookupSession(args.SessionID)
	if err != nil {
		return "", err
	}
	body, err := boundedRequest(ctx, s, command, dapArgs, timeout)
	if err != nil {
		return "", err
	}
	return bodyResult(body), nil
}

// requestTimeout maps timeoutMs (0 = default) to a duration capped at
// maxRequestTimeout.
func requestTimeout(ms int) (time.Duration, error) {
	switch {
	case ms < 0:
		return 0, fmt.Errorf("invalid_request: timeoutMs must not be negative, got %d", ms)
	case ms == 0:
		return defaultRequestTimeout, nil
	case int64(ms) >= maxRequestTimeout.Milliseconds():
		return maxRequestTimeout, nil
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// setBreakpointsArgs names exactly one source: Path, an absolute project file
// (SourcePathInProject), or Address, a debugger-provided source address
// (ValidDebuggerAddress) that is sent verbatim as source.path.
type setBreakpointsArgs struct {
	SessionID   string             `json:"sessionId"`
	Path        string             `json:"path"`
	Address     string             `json:"address"`
	Breakpoints []SourceBreakpoint `json:"breakpoints"`
}

func setBreakpointsHandler(ctx context.Context, _, argsJSON string) (string, error) {
	var args setBreakpointsArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", fmt.Errorf("invalid_request: %w", err)
	}
	if (args.Path == "") == (args.Address == "") {
		return "", errors.New("invalid_request: exactly one of path and address is required")
	}
	var address string
	if args.Address != "" {
		var err error
		if address, err = ValidDebuggerAddress(args.Address); err != nil {
			return "", err
		}
	}
	for _, bp := range args.Breakpoints {
		if bp.Line < 1 {
			return "", fmt.Errorf("invalid_request: breakpoint line must be at least 1, got %d", bp.Line)
		}
	}
	bps := args.Breakpoints
	if bps == nil {
		bps = []SourceBreakpoint{} // clear: DAP wants an array, not null
	}
	s, err := lookupSession(args.SessionID)
	if err != nil {
		return "", err
	}
	batch := s.prepareSourceBreakpoints(bps)
	var dapArgs map[string]any
	if address != "" {
		dapArgs = batch.addressArguments(address)
	} else {
		// Clearing is allowed for a deleted file so its breakpoints can go away.
		path, err := SourcePathInProject(s.opts.ProjectRoot, args.Path, len(bps) == 0)
		if err != nil {
			return "", err
		}
		dapArgs = batch.arguments(path)
	}
	body, err := boundedRequest(ctx, s, "setBreakpoints", dapArgs, defaultRequestTimeout)
	body, err = batch.finish(body, err)
	if err != nil {
		return "", err
	}
	return bodyResult(body), nil
}

// SourcePathInProject validates that path is an absolute path of an existing
// file inside projectRoot, also after following symlinks, and returns it
// cleaned (the adapter gets the path the frontend knows, not the resolved one).
// With allowMissing, a path that does not exist is accepted when its lexical
// parent directory resolves inside projectRoot. It is the one project-root
// check for every breakpoint path: debug_set_breakpoints and the plugin host's
// plugin_host_debugger_start both use it. Errors carry a frontend error code.
func SourcePathInProject(projectRoot, path string, allowMissing bool) (string, error) {
	return pathInProject(projectRoot, path, allowMissing, false)
}

// DirInProject is SourcePathInProject for an existing directory: the project
// root itself or a directory under it. runInTerminal uses it for the cwd.
func DirInProject(projectRoot, path string) (string, error) {
	return pathInProject(projectRoot, path, false, true)
}

// pathInProject implements SourcePathInProject (wantDir false: an existing
// file, or a missing one when allowMissing) and DirInProject (wantDir true).
func pathInProject(projectRoot, path string, allowMissing, wantDir bool) (string, error) {
	kind := "file"
	if wantDir {
		kind = "directory"
	}
	if path == "" || strings.ContainsRune(path, '\x00') || !filepath.IsAbs(path) {
		return "", fmt.Errorf("invalid_request: path must be an absolute %s path, got %q", kind, path)
	}
	clean := filepath.Clean(path)
	rootCanon, err := projectfs.CanonicalProjectRoot(projectRoot)
	if err != nil {
		return "", fmt.Errorf("unavailable: debug session project root %q: %w", projectRoot, err)
	}
	// The path may be spelled under the root as configured or as resolved.
	bases := []string{rootCanon}
	if abs, err := filepath.Abs(projectRoot); err == nil && abs != rootCanon {
		bases = append(bases, abs)
	}
	rel := ""
	for _, base := range bases {
		if r, err := filepath.Rel(base, clean); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r) {
			rel = r
			break
		}
	}
	if rel == "" {
		return "", fmt.Errorf("forbidden: %s is outside the debug session's project root", clean)
	}
	var resolved string
	if rel == "." { // the root itself, which ResolveUnderProjectRoot refuses
		resolved, err = rootCanon, nil
	} else {
		resolved, err = projectfs.ResolveUnderProjectRoot(rootCanon, rel)
	}
	switch {
	case errors.Is(err, projectfs.ErrProjectPathEscapesRoot):
		return "", fmt.Errorf("forbidden: %s resolves outside the debug session's project root", clean)
	case errors.Is(err, projectfs.ErrProjectPathInvalid):
		return "", fmt.Errorf("invalid_request: %s is not a %s in the project", clean, kind)
	case projectfs.IsProjectPathNotExist(err):
		if allowMissing && parentInProject(rootCanon, rel) {
			return clean, nil
		}
		return "", fmt.Errorf("not_found: %s does not exist", clean)
	case err != nil:
		return "", fmt.Errorf("resolve %s: %w", clean, err)
	}
	info, err := os.Stat(resolved)
	switch {
	case err != nil:
		return "", fmt.Errorf("resolve %s: %w", clean, err)
	case wantDir && !info.IsDir():
		return "", fmt.Errorf("invalid_request: %s is not a directory", clean)
	case !wantDir && info.IsDir():
		return "", fmt.Errorf("invalid_request: %s is a directory", clean)
	}
	return clean, nil
}

// parentInProject reports whether the lexical parent of the project-relative
// path rel exists and resolves (following symlinks) inside rootCanon.
func parentInProject(rootCanon, rel string) bool {
	parent := filepath.Dir(rel)
	if parent == "." {
		return true // the root itself
	}
	resolved, err := projectfs.ResolveUnderProjectRoot(rootCanon, parent)
	if err != nil {
		return false
	}
	info, err := os.Stat(resolved)
	return err == nil && info.IsDir()
}

// stopObservers are told the session id when a user stop (debug_stop) of a
// registered session begins, before the session ends; e.g. the plugin host
// ends a test run bound to that session as stopped, not as crashed.
var stopObservers struct {
	mu  sync.Mutex
	fns []func(sessionID string)
}

// OnStopRequested registers fn to run (synchronously, so it must not block)
// when debug_stop begins stopping a registered session.
func OnStopRequested(fn func(sessionID string)) {
	stopObservers.mu.Lock()
	defer stopObservers.mu.Unlock()
	stopObservers.fns = append(stopObservers.fns, fn)
}

func notifyStopRequested(sessionID string) {
	stopObservers.mu.Lock()
	fns := slices.Clone(stopObservers.fns)
	stopObservers.mu.Unlock()
	for _, fn := range fns {
		fn(sessionID)
	}
}

type stopArgs struct {
	SessionID string `json:"sessionId"`
	// TerminateDebuggee defaults to true for a launch session and false for
	// an attach session, whose target is not MinCode's to end.
	TerminateDebuggee *bool `json:"terminateDebuggee"`
}

func stopHandler(ctx context.Context, _, argsJSON string) (string, error) {
	var args stopArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", fmt.Errorf("invalid_request: %w", err)
	}
	e, ok := lookupEntry(args.SessionID)
	if !ok {
		return "", fmt.Errorf("not_found: debug session %q", args.SessionID)
	}
	notifyStopRequested(args.SessionID)
	terminate := e.defaultTerminateDebuggee()
	if args.TerminateDebuggee != nil {
		terminate = *args.TerminateDebuggee
	}
	e.stop(ctx, terminate, stopReason, stopForcedReason)
	return core.EncodeResult(nil)
}

// lookupSession returns the registered session or a not_found error.
func lookupSession(id string) (*Session, error) {
	e, ok := lookupEntry(id)
	if !ok {
		return nil, fmt.Errorf("not_found: debug session %q", id)
	}
	return e.session, nil
}

// boundedRequest runs s.Request with timeout and returns no later than the
// timeout or ctx cancellation, even if the request write is blocked on an
// adapter that stopped reading (that write is released when the session
// closes).
func boundedRequest(ctx context.Context, s *Session, command string, args any, timeout time.Duration) (json.RawMessage, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type result struct {
		body json.RawMessage
		err  error
	}
	done := make(chan result, 1)
	go func() {
		body, err := s.Request(reqCtx, command, args)
		done <- result{body, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-reqCtx.Done():
		r.err = reqCtx.Err()
	}
	if r.err == nil {
		return r.body, nil
	}
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("cancelled: dap %s: %w", command, ctx.Err())
	case errors.Is(r.err, context.DeadlineExceeded):
		return nil, fmt.Errorf("timeout: dap %s did not respond within %v", command, timeout)
	case errors.Is(r.err, ErrSessionClosed):
		return nil, errSessionEnded
	}
	return nil, r.err
}

// bodyResult returns a response body as the command result ("null" if empty).
func bodyResult(body json.RawMessage) string {
	if len(body) == 0 {
		return "null"
	}
	return string(body)
}
