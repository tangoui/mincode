package debug

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/backend/core"
)

// registerScripted registers a session whose DAP side is an in-memory
// fakeAdapter (scripted by setup) and whose adapter process is a "silent"
// fake that only dies when killed. It returns the fake, the session and the
// session's project root.
func registerScripted(t *testing.T, setup func(fa *fakeAdapter)) (*fakeAdapter, *Session, string) {
	t.Helper()
	return registerScriptedSpec(t, setup, nil)
}

// registerScriptedSpec is registerScripted with a hook to adjust the spec.
func registerScriptedSpec(t *testing.T, setup func(fa *fakeAdapter), mutate func(spec *AdapterSpec)) (*fakeAdapter, *Session, string) {
	t.Helper()
	spec := fakeSpec(t, "stdio", "silent")
	if mutate != nil {
		mutate(&spec)
	}
	cmd, stderr := newCommand(spec, nil)
	proc, err := startProcess(cmd, stderr)
	if err != nil {
		t.Fatalf("start silent adapter: %v", err)
	}
	fa, clientEnd := newFakeAdapter(t)
	if setup != nil {
		setup(fa)
	}
	fa.Start()
	id, err := newSessionID()
	if err != nil {
		t.Fatal(err)
	}
	s := register(id, spec, proc, newAdapterConn(clientEnd, clientEnd, proc, clientEnd))
	return fa, s, spec.ProjectRoot
}

func dispatch(t *testing.T, cmd string, args any) (string, error) {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return core.DispatchContext(testCtx(t), cmd, string(b))
}

func wantErrContains(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("err = %v, want it to contain %q", err, substr)
	}
}

func TestDebugListSessions(t *testing.T) {
	captureEmits(t)
	_, s, root := registerScripted(t, nil)
	out, err := dispatch(t, "debug_list_sessions", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var infos []SessionInfo
	if err := json.Unmarshal([]byte(out), &infos); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if len(infos) != 1 || infos[0].ID != s.ID() || infos[0].ProjectRoot != root {
		t.Fatalf("debug_list_sessions = %s", out)
	}
}

func TestDebugListSessionsEmptyIsArray(t *testing.T) {
	captureEmits(t)
	out, err := dispatch(t, "debug_list_sessions", map[string]any{})
	if err != nil || out != "[]" {
		t.Fatalf("debug_list_sessions = %q, %v", out, err)
	}
}

func TestDebugRequestRejectsHostOnlyCommands(t *testing.T) {
	captureEmits(t)
	fa, s, _ := registerScripted(t, nil)
	for _, command := range []string{"initialize", "launch", "attach", "configurationDone", "runInTerminal", "Launch", "setBreakpoints", "restart", "Restart"} {
		_, err := dispatch(t, "debug_request", map[string]any{"sessionId": s.ID(), "command": command, "arguments": map[string]any{}})
		wantErrContains(t, err, "forbidden")
	}
	// A permitted request on the same session still goes through, and is the
	// only thing the adapter ever saw.
	if _, err := dispatch(t, "debug_request", map[string]any{"sessionId": s.ID(), "command": "threads"}); err != nil {
		t.Fatal(err)
	}
	if got := fa.Commands(); len(got) != 1 || got[0] != "threads" {
		t.Fatalf("adapter received %v", got)
	}
}

func TestDebugRequestUnknownSession(t *testing.T) {
	captureEmits(t)
	for _, tc := range []struct {
		cmd  string
		args map[string]any
	}{
		{"debug_request", map[string]any{"sessionId": "dbg-missing", "command": "threads"}},
		{"debug_set_breakpoints", map[string]any{"sessionId": "dbg-missing", "path": "/x.py", "breakpoints": []any{}}},
		{"debug_stop", map[string]any{"sessionId": "dbg-missing"}},
	} {
		_, err := dispatch(t, tc.cmd, tc.args)
		wantErrContains(t, err, "not_found")
	}
}

func TestDebugRequestForwardsArgumentsAndBody(t *testing.T) {
	captureEmits(t)
	fa, s, _ := registerScripted(t, func(fa *fakeAdapter) {
		fa.Handle("stackTrace", func(req Message) {
			fa.Respond(req, true, json.RawMessage(`{"stackFrames":[{"id":7,"name":"main","line":3,"column":1}],"totalFrames":1}`), "")
		})
		fa.Handle("evaluate", func(req Message) { fa.Respond(req, false, nil, "name 'x' is not defined") })
	})
	out, err := dispatch(t, "debug_request", map[string]any{
		"sessionId": s.ID(),
		"command":   "stackTrace",
		"arguments": map[string]any{"threadId": 1, "levels": 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"stackFrames":[{"id":7,"name":"main","line":3,"column":1}],"totalFrames":1}` {
		t.Fatalf("body = %s", out)
	}
	reqs := fa.Requests("stackTrace")
	if len(reqs) != 1 || string(reqs[0].Arguments) != `{"levels":20,"threadId":1}` {
		t.Fatalf("stackTrace requests = %+v", reqs)
	}

	_, err = dispatch(t, "debug_request", map[string]any{"sessionId": s.ID(), "command": "evaluate", "arguments": map[string]any{"expression": "x"}})
	wantErrContains(t, err, "name 'x' is not defined")

	// No body: null.
	out, err = dispatch(t, "debug_request", map[string]any{"sessionId": s.ID(), "command": "pause", "arguments": map[string]any{"threadId": 1}})
	if err != nil || out != "null" {
		t.Fatalf("pause = %q, %v", out, err)
	}
}

func TestDebugRequestValidatesInput(t *testing.T) {
	captureEmits(t)
	fa, s, _ := registerScripted(t, nil)
	for _, args := range []map[string]any{
		{"sessionId": s.ID(), "command": ""},
		{"sessionId": s.ID(), "command": "threads", "arguments": []int{1}},
		{"sessionId": s.ID(), "command": "threads", "arguments": "x"},
		{"sessionId": s.ID(), "command": "threads", "timeoutMs": -1},
	} {
		_, err := dispatch(t, "debug_request", args)
		wantErrContains(t, err, "invalid_request")
	}
	if got := fa.Commands(); len(got) != 0 {
		t.Fatalf("adapter received %v", got)
	}
}

func TestDebugRequestTimeout(t *testing.T) {
	captureEmits(t)
	_, s, _ := registerScripted(t, func(fa *fakeAdapter) {
		fa.Handle("threads", func(Message) {}) // never answers
	})
	start := time.Now()
	_, err := dispatch(t, "debug_request", map[string]any{"sessionId": s.ID(), "command": "threads", "timeoutMs": 50})
	wantErrContains(t, err, "timeout")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
	if !listed(s.ID()) {
		t.Fatal("a timed-out request must not end the session")
	}
}

func TestRequestTimeoutBounds(t *testing.T) {
	for _, tc := range []struct {
		ms   int
		want time.Duration
	}{
		{0, 15 * time.Second},
		{1, time.Millisecond},
		{120000, 120 * time.Second},
		{999999999, 120 * time.Second},
	} {
		got, err := requestTimeout(tc.ms)
		if err != nil || got != tc.want {
			t.Fatalf("requestTimeout(%d) = %v, %v; want %v", tc.ms, got, err, tc.want)
		}
	}
	if _, err := requestTimeout(-5); err == nil {
		t.Fatal("negative timeout accepted")
	}
}

func TestDebugSetBreakpointsRejectsPathOutsideProject(t *testing.T) {
	captureEmits(t)
	fa, s, root := registerScripted(t, nil)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.py")
	if err := os.WriteFile(outside, []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.py")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	linkDir := filepath.Join(root, "linkdir")
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		outside,
		link,                                // symlink inside the root to a file outside
		filepath.Join(linkDir, "secret.py"), // file under a symlinked directory
		filepath.Join(root, "..", filepath.Base(outsideDir), "secret.py"),
	} {
		_, err := dispatch(t, "debug_set_breakpoints", map[string]any{
			"sessionId": s.ID(), "path": path, "breakpoints": []map[string]any{{"line": 1}},
		})
		wantErrContains(t, err, "forbidden")
	}
	_, err := dispatch(t, "debug_set_breakpoints", map[string]any{
		"sessionId": s.ID(), "path": "relative/main.py", "breakpoints": []any{},
	})
	wantErrContains(t, err, "invalid_request")
	if got := fa.Commands(); len(got) != 0 {
		t.Fatalf("adapter received %v", got)
	}
}

func TestDebugSetBreakpointsSendsRequest(t *testing.T) {
	captureEmits(t)
	fa, s, root := registerScripted(t, func(fa *fakeAdapter) {
		fa.Handle("setBreakpoints", func(req Message) {
			fa.Respond(req, true, json.RawMessage(`{"breakpoints":[{"verified":true,"line":2}]}`), "")
		})
	})
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "pkg", "main.py")
	if err := os.WriteFile(file, []byte("a = 1\nb = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := dispatch(t, "debug_set_breakpoints", map[string]any{
		"sessionId":   s.ID(),
		"path":        file,
		"breakpoints": []map[string]any{{"line": 2, "condition": "b > 1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"breakpoints":[{"verified":true,"line":2}]}` {
		t.Fatalf("body = %s", out)
	}
	reqs := fa.Requests("setBreakpoints")
	if len(reqs) != 1 {
		t.Fatalf("setBreakpoints requests = %+v", reqs)
	}
	var args struct {
		Source struct {
			Path, Name string
		} `json:"source"`
		Breakpoints []SourceBreakpoint `json:"breakpoints"`
	}
	if err := json.Unmarshal(reqs[0].Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if args.Source.Path != file || args.Source.Name != "main.py" || len(args.Breakpoints) != 1 || args.Breakpoints[0] != (SourceBreakpoint{Line: 2, Condition: "b > 1"}) {
		t.Fatalf("setBreakpoints arguments = %s", reqs[0].Arguments)
	}

	// Clearing (no breakpoints) sends an empty array, not null.
	if _, err := dispatch(t, "debug_set_breakpoints", map[string]any{"sessionId": s.ID(), "path": file}); err != nil {
		t.Fatal(err)
	}
	reqs = fa.Requests("setBreakpoints")
	if len(reqs) != 2 || !strings.Contains(string(reqs[1].Arguments), `"breakpoints":[]`) {
		t.Fatalf("clear arguments = %s", reqs[len(reqs)-1].Arguments)
	}

	_, err = dispatch(t, "debug_set_breakpoints", map[string]any{
		"sessionId": s.ID(), "path": file, "breakpoints": []map[string]any{{"line": 0}},
	})
	wantErrContains(t, err, "invalid_request")
}

// answerBegin scripts fa to answer launch and attach and send `initialized`,
// so Begin completes on a scripted session.
func answerBegin(fa *fakeAdapter) {
	for _, command := range []string{"launch", "attach"} {
		fa.Handle(command, func(req Message) {
			fa.Respond(req, true, nil, "")
			fa.SendEvent("initialized", nil)
		})
	}
}

// beginScripted runs Begin with the given request kind on a scripted session.
func beginScripted(t *testing.T, s *Session, request string) {
	t.Helper()
	if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "fake", Request: request}); err != nil {
		t.Fatalf("Begin %s: %v", request, err)
	}
}

func TestDebugStopForceKillsAfterGrace(t *testing.T) {
	rec := captureEmits(t)
	fa, s, _ := registerScripted(t, func(fa *fakeAdapter) {
		answerBegin(fa)
		fa.Handle("disconnect", func(Message) {}) // ignored: never answers, never exits
	})
	beginScripted(t, s, "launch")
	start := time.Now()
	if _, err := dispatch(t, "debug_stop", map[string]any{"sessionId": s.ID()}); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < stopGrace-100*time.Millisecond || elapsed > stopGrace+adapterExitWait+time.Second {
		t.Fatalf("debug_stop took %v, want about %v", elapsed, stopGrace)
	}
	if listed(s.ID()) {
		t.Fatal("session still listed after debug_stop")
	}
	rec.waitEnded(t, s.ID())
	reqs := fa.Requests("disconnect")
	if len(reqs) != 1 || string(reqs[0].Arguments) != `{"terminateDebuggee":true}` {
		t.Fatalf("disconnect requests = %+v", reqs)
	}
}

func TestDebugStopEndsOnDisconnectResponse(t *testing.T) {
	captureEmits(t)
	fa, s, _ := registerScripted(t, nil) // disconnect gets an immediate success
	start := time.Now()
	if _, err := dispatch(t, "debug_stop", map[string]any{"sessionId": s.ID(), "terminateDebuggee": false}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed >= stopGrace {
		t.Fatalf("debug_stop waited %v despite the disconnect response", elapsed)
	}
	if listed(s.ID()) {
		t.Fatal("session still listed after debug_stop")
	}
	reqs := fa.Requests("disconnect")
	if len(reqs) != 1 || string(reqs[0].Arguments) != `{"terminateDebuggee":false}` {
		t.Fatalf("disconnect requests = %+v", reqs)
	}
}

func TestDebugSetBreakpointsClearsDeletedFile(t *testing.T) {
	captureEmits(t)
	fa, s, root := registerScripted(t, nil)
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(root, "out")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	clearBPs := func(path string) error {
		_, err := dispatch(t, "debug_set_breakpoints", map[string]any{"sessionId": s.ID(), "path": path, "breakpoints": []any{}})
		return err
	}
	deleted := filepath.Join(root, "pkg", "gone.py")
	if err := clearBPs(deleted); err != nil {
		t.Fatalf("clear deleted file: %v", err)
	}
	if err := clearBPs(filepath.Join(root, "top.py")); err != nil {
		t.Fatalf("clear deleted top-level file: %v", err)
	}
	reqs := fa.Requests("setBreakpoints")
	if len(reqs) != 2 || !strings.Contains(string(reqs[0].Arguments), `"path":"`+deleted+`"`) {
		t.Fatalf("setBreakpoints requests = %+v", reqs)
	}

	// Setting breakpoints on a missing file is still refused.
	_, err := dispatch(t, "debug_set_breakpoints", map[string]any{
		"sessionId": s.ID(), "path": deleted, "breakpoints": []map[string]any{{"line": 1}},
	})
	wantErrContains(t, err, "not_found")
	// So is clearing when the parent is missing or resolves outside the root.
	wantErrContains(t, clearBPs(filepath.Join(root, "nodir", "gone.py")), "not_found")
	wantErrContains(t, clearBPs(filepath.Join(root, "out", "gone.py")), "not_found")
	if n := len(fa.Requests("setBreakpoints")); n != 2 {
		t.Fatalf("adapter received %d setBreakpoints requests", n)
	}
}

func TestDebugStopDefaultTerminateFollowsRequestKind(t *testing.T) {
	captureEmits(t)
	for _, tc := range []struct{ request, want string }{
		{"launch", `{"terminateDebuggee":true}`},
		// Stopping an attach session must not kill the user's target.
		{"attach", `{"terminateDebuggee":false}`},
	} {
		fa, s, _ := registerScripted(t, answerBegin)
		beginScripted(t, s, tc.request)
		if _, err := dispatch(t, "debug_stop", map[string]any{"sessionId": s.ID()}); err != nil {
			t.Fatal(err)
		}
		reqs := fa.Requests("disconnect")
		if len(reqs) != 1 || string(reqs[0].Arguments) != tc.want {
			t.Fatalf("%s: disconnect requests = %+v, want arguments %s", tc.request, reqs, tc.want)
		}
	}
}

// disconnectArgs returns the arguments of the single disconnect fa received.
func disconnectArgs(t *testing.T, fa *fakeAdapter) string {
	t.Helper()
	reqs := fa.Requests("disconnect")
	if len(reqs) != 1 {
		t.Fatalf("adapter received %d disconnect requests, want 1 (commands %v)", len(reqs), fa.Commands())
	}
	return string(reqs[0].Arguments)
}

func TestStopForOwnerDisconnectsGracefully(t *testing.T) {
	rec := captureEmits(t)
	launchFA, launch, _ := registerScripted(t, answerBegin)
	beginScripted(t, launch, "launch")
	attachFA, attach, _ := registerScripted(t, answerBegin)
	beginScripted(t, attach, "attach")
	otherFA, other, _ := registerScriptedSpec(t, answerBegin, func(spec *AdapterSpec) { spec.OwnerPluginID = "owner-b" })
	beginScripted(t, other, "launch")

	StopForOwner("owner-a", "plugin disabled")
	if got := disconnectArgs(t, launchFA); got != `{"terminateDebuggee":true}` {
		t.Fatalf("launch session disconnect = %s", got)
	}
	if got := disconnectArgs(t, attachFA); got != `{"terminateDebuggee":false}` {
		t.Fatalf("attach session disconnect = %s", got)
	}
	// StopForOwner returns after cleanup, which emits the end event first.
	for _, s := range []*Session{launch, attach} {
		if reason := endedReason(rec, s.ID()); reason != "plugin disabled" {
			t.Fatalf("reason = %q", reason)
		}
	}
	if !listed(other.ID()) || len(otherFA.Requests("disconnect")) != 0 {
		t.Fatal("another owner's session was stopped")
	}
}

func TestStopAllStopsSessionsInParallel(t *testing.T) {
	captureEmits(t)
	var fas []*fakeAdapter
	for i := 0; i < 3; i++ {
		fa, s, _ := registerScripted(t, func(fa *fakeAdapter) {
			answerBegin(fa)
			fa.Handle("disconnect", func(Message) {}) // never answers
		})
		beginScripted(t, s, "launch")
		fas = append(fas, fa)
	}
	start := time.Now()
	StopAll("test stop")
	if elapsed := time.Since(start); elapsed > 2*stopGrace {
		t.Fatalf("StopAll of 3 unresponsive sessions took %v, want about one grace (%v)", elapsed, stopGrace)
	}
	for _, fa := range fas {
		disconnectArgs(t, fa)
	}
	if n := len(List()); n != 0 {
		t.Fatalf("%d sessions still listed", n)
	}
}

func TestValidFalseDisconnectsGracefully(t *testing.T) {
	rec := captureEmits(t)
	prev := validCheckInterval
	validCheckInterval = 20 * time.Millisecond
	t.Cleanup(func() { validCheckInterval = prev })
	var valid atomic.Bool
	valid.Store(true)
	fa, s, _ := registerScriptedSpec(t, answerBegin, func(spec *AdapterSpec) { spec.Valid = valid.Load })
	beginScripted(t, s, "launch")
	valid.Store(false)
	if reason := rec.waitEnded(t, s.ID()); reason != "plugin changed" {
		t.Fatalf("reason = %q", reason)
	}
	if got := disconnectArgs(t, fa); got != `{"terminateDebuggee":true}` {
		t.Fatalf("disconnect = %s", got)
	}
}

func TestStopSessionDisconnectsAfterFailedBegin(t *testing.T) {
	rec := captureEmits(t)
	fa, s, _ := registerScripted(t, func(fa *fakeAdapter) {
		fa.Handle("launch", func(req Message) { fa.Respond(req, false, nil, "no such program") })
	})
	if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "fake", Request: "launch"}); err == nil {
		t.Fatal("Begin succeeded")
	}
	StopSession(testCtx(t), s, "debug launch failed")
	if reason := rec.waitEnded(t, s.ID()); reason != "debug launch failed" {
		t.Fatalf("reason = %q", reason)
	}
	if got := disconnectArgs(t, fa); got != `{"terminateDebuggee":true}` {
		t.Fatalf("disconnect = %s", got)
	}
	// An unregistered (already ended) session is just closed again.
	StopSession(testCtx(t), s, "again")
}

func TestShutdownStopsSessionsAndRefusesNewOnes(t *testing.T) {
	rec := captureEmits(t)
	t.Cleanup(resetShutdownForTest)
	answering, a, _ := registerScripted(t, answerBegin)
	beginScripted(t, a, "attach")
	silent, b, _ := registerScripted(t, func(fa *fakeAdapter) {
		answerBegin(fa)
		fa.Handle("disconnect", func(Message) {}) // never answers: killed after the grace
	})
	beginScripted(t, b, "launch")

	start := time.Now()
	Shutdown(testCtx(t))
	if elapsed := time.Since(start); elapsed > stopGrace+adapterExitWait {
		t.Fatalf("Shutdown took %v", elapsed)
	}
	if got := disconnectArgs(t, answering); got != `{"terminateDebuggee":false}` {
		t.Fatalf("attach disconnect = %s", got)
	}
	if got := disconnectArgs(t, silent); got != `{"terminateDebuggee":true}` {
		t.Fatalf("launch disconnect = %s", got)
	}
	if endedReason(rec, a.ID()) != shutdownReason || endedReason(rec, b.ID()) != forcedReason(shutdownReason) {
		t.Fatalf("end reasons = %q, %q", endedReason(rec, a.ID()), endedReason(rec, b.ID()))
	}
	if n := len(List()); n != 0 {
		t.Fatalf("%d sessions still listed after Shutdown", n)
	}
	if _, err := StartAdapter(testCtx(t), fakeSpec(t, "stdio", "stdio")); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("StartAdapter after Shutdown: err = %v", err)
	}
}

// resetShutdownForTest lets sessions start again after a Shutdown test.
func resetShutdownForTest() {
	registry.mu.Lock()
	registry.shuttingDown = false
	registry.mu.Unlock()
}

// endedReason returns the reason of an already emitted sessionEnded event of
// id, or "" if there is none.
func endedReason(rec *emitRecorder, id string) string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, ev := range rec.events {
		if ev.SessionID == id && ev.Event == SessionEndedEvent {
			var body struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(ev.Body, &body)
			return body.Reason
		}
	}
	return ""
}
