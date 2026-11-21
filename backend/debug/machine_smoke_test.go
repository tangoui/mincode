package debug

// Opt-in end-to-end smoke test against a real debugpy adapter.
//
//	MINCODE_DEBUG_MACHINE_SMOKE=1     enables the test (it is skipped otherwise)
//	MINCODE_DEBUG_SMOKE_PYTHON=<path> interpreter that has debugpy installed
//	                                  (default: python3 on PATH)
//
// The test is skipped when `import debugpy` fails for that interpreter, e.g.:
//
//	python3 -m venv /tmp/mincode-debugpy-venv
//	/tmp/mincode-debugpy-venv/bin/pip install debugpy
//	MINCODE_DEBUG_MACHINE_SMOKE=1 MINCODE_DEBUG_SMOKE_PYTHON=/tmp/mincode-debugpy-venv/bin/python \
//	  go test ./backend/debug -run TestMachineSmokePython -v
//
// TestMachineSmokePythonIntegratedTerminal launches with
// console "integratedTerminal" (the python-debug default), so debugpy starts
// the program through the runInTerminal reverse request in a project PTY.
//
// TestMachineSmokePythonShutdownEndsDebuggee (Unix) also checks that
// Shutdown ends a launched debuggee, which debugpy runs in its own process
// group.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const machineSmokeTimeout = 60 * time.Second

// smokePython returns the debugpy interpreter, skipping the test when the
// machine smoke is not enabled or debugpy is unavailable.
func smokePython(t *testing.T) string {
	t.Helper()
	if os.Getenv("MINCODE_DEBUG_MACHINE_SMOKE") != "1" {
		t.Skip("set MINCODE_DEBUG_MACHINE_SMOKE=1 to run the debugpy machine smoke test")
	}
	python := os.Getenv("MINCODE_DEBUG_SMOKE_PYTHON")
	if python == "" {
		p, err := exec.LookPath("python3")
		if err != nil {
			t.Skipf("python3 not found on PATH (set MINCODE_DEBUG_SMOKE_PYTHON): %v", err)
		}
		python = p
	}
	if out, err := exec.Command(python, "-c", "import debugpy").CombinedOutput(); err != nil {
		t.Skipf("%s cannot import debugpy (install it in a venv and set MINCODE_DEBUG_SMOKE_PYTHON): %v: %s",
			python, err, strings.TrimSpace(string(out)))
	}
	return python
}

// smokeScript returns the resolved absolute path of testdata/smoke/<name>.
func smokeScript(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", "smoke", name))
	if err != nil {
		t.Fatal(err)
	}
	if path, err = filepath.EvalSymlinks(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMachineSmokePython(t *testing.T) {
	python := smokePython(t)
	app := smokeScript(t, "app.py")
	root := filepath.Dir(app)

	ctx, cancel := context.WithTimeout(context.Background(), machineSmokeTimeout)
	defer cancel()
	rec := captureEmits(t)

	s, err := StartAdapter(ctx, AdapterSpec{
		Kind:          "stdio",
		Executable:    python,
		Args:          []string{"-m", "debugpy.adapter"},
		ProjectRoot:   root,
		OwnerPluginID: "machine-smoke",
		DebuggerType:  "python",
	})
	if err != nil {
		t.Fatalf("StartAdapter: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			rec.logAll(t)
		}
	})

	launchArgs, err := json.Marshal(map[string]any{
		"type":       "python",
		"program":    app,
		"console":    "internalConsole",
		"justMyCode": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(ctx, BeginParams{
		AdapterID:   "debugpy",
		Request:     "launch",
		Arguments:   launchArgs,
		Breakpoints: map[string][]SourceBreakpoint{app: {{Line: 3}}},
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	stopped := rec.waitEvent(ctx, t, s.ID(), "stopped")
	var stop struct {
		Reason   string `json:"reason"`
		ThreadID int    `json:"threadId"`
	}
	if err := json.Unmarshal(stopped.Body, &stop); err != nil {
		t.Fatalf("decode stopped: %v (%s)", err, stopped.Body)
	}
	if stop.Reason != "breakpoint" {
		t.Fatalf("stopped reason = %q, want breakpoint (%s)", stop.Reason, stopped.Body)
	}

	var trace struct {
		StackFrames []struct {
			ID   int `json:"id"`
			Line int `json:"line"`
		} `json:"stackFrames"`
	}
	smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": stop.ThreadID, "startFrame": 0, "levels": 20}, &trace)
	if len(trace.StackFrames) == 0 || trace.StackFrames[0].Line != 3 {
		t.Fatalf("stackTrace = %+v, want top frame on line 3", trace.StackFrames)
	}

	var scopes struct {
		Scopes []struct {
			Name               string `json:"name"`
			VariablesReference int    `json:"variablesReference"`
		} `json:"scopes"`
	}
	smokeRequest(ctx, t, s, "scopes", map[string]any{"frameId": trace.StackFrames[0].ID}, &scopes)
	total, found := "", false
	for _, sc := range scopes.Scopes {
		if sc.VariablesReference == 0 {
			continue
		}
		var vars struct {
			Variables []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"variables"`
		}
		smokeRequest(ctx, t, s, "variables", map[string]any{"variablesReference": sc.VariablesReference}, &vars)
		for _, v := range vars.Variables {
			if v.Name == "total" {
				total, found = v.Value, true
			}
		}
		if found {
			break
		}
	}
	if !found || total != "5" {
		t.Fatalf("variable total = %q (found %v), want \"5\"; scopes %+v", total, found, scopes.Scopes)
	}

	smokeRequest(ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	rec.waitEvent(ctx, t, s.ID(), "terminated")
	ended := rec.waitEvent(ctx, t, s.ID(), SessionEndedEvent)
	if !strings.Contains(string(ended.Body), "adapter terminated") {
		t.Fatalf("%s body = %s, want reason \"adapter terminated\"", SessionEndedEvent, ended.Body)
	}
	if infos := List(); len(infos) != 0 {
		t.Fatalf("List() after terminated = %+v, want empty", infos)
	}
}

// TestMachineSmokePythonIntegratedTerminal runs the program in a PTY via the
// DAP runInTerminal reverse request (env names the debuggers contract denies
// are dropped from what debugpy asks for), stops at a breakpoint, reads a
// variable, checks that the program's output reached the PTY and continues
// to the end.
func TestMachineSmokePythonIntegratedTerminal(t *testing.T) {
	python := smokePython(t)
	app := smokeScript(t, "terminal_app.py")
	root := filepath.Dir(app)

	ctx, cancel := context.WithTimeout(context.Background(), machineSmokeTimeout)
	defer cancel()
	rec := captureEmits(t)

	s, err := StartAdapter(ctx, AdapterSpec{
		Kind:          "stdio",
		Executable:    python,
		Args:          []string{"-m", "debugpy.adapter"},
		ProjectRoot:   root,
		OwnerPluginID: "machine-smoke",
		DebuggerType:  "python",
	})
	if err != nil {
		t.Fatalf("StartAdapter: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			rec.logAll(t)
		}
	})

	launchArgs, err := json.Marshal(map[string]any{
		"type":       "python",
		"program":    app,
		"cwd":        root,
		"python":     []string{python},
		"console":    "integratedTerminal",
		"justMyCode": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(ctx, BeginParams{
		AdapterID:   "debugpy",
		Request:     "launch",
		Arguments:   launchArgs,
		Breakpoints: map[string][]SourceBreakpoint{app: {{Line: 5}}},
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	terminalID, title := runInTerminalEvent(t, rec, s.ID())
	if want := "debug-" + s.ID() + "-"; !strings.HasPrefix(terminalID, want) {
		t.Fatalf("terminal session %q, want prefix %q", terminalID, want)
	}
	t.Logf("runInTerminal: terminal %s, title %q", terminalID, title)

	stopped := rec.waitEvent(ctx, t, s.ID(), "stopped")
	var stop struct {
		Reason   string `json:"reason"`
		ThreadID int    `json:"threadId"`
	}
	if err := json.Unmarshal(stopped.Body, &stop); err != nil {
		t.Fatalf("decode stopped: %v (%s)", err, stopped.Body)
	}
	if stop.Reason != "breakpoint" {
		t.Fatalf("stopped reason = %q, want breakpoint (%s)", stop.Reason, stopped.Body)
	}
	// The debuggee printed before the breakpoint; it went to the PTY, which
	// keeps its output for attach_local_shell. The session is still alive
	// here, so the terminal has not been closed yet.
	attached := waitTerminalOutput(t, terminalID, "mincode-terminal-smoke: started")
	if attached.Ended {
		t.Fatalf("attach = %+v, want the debuggee still running at the breakpoint", attached)
	}

	var trace struct {
		StackFrames []struct {
			ID   int `json:"id"`
			Line int `json:"line"`
		} `json:"stackFrames"`
	}
	smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": stop.ThreadID, "startFrame": 0, "levels": 20}, &trace)
	if len(trace.StackFrames) == 0 || trace.StackFrames[0].Line != 5 {
		t.Fatalf("stackTrace = %+v, want top frame on line 5", trace.StackFrames)
	}
	if total := smokeLocal(ctx, t, s, trace.StackFrames[0].ID, "total"); total != "5" {
		t.Fatalf("variable total = %q, want \"5\"", total)
	}

	smokeRequest(ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
	rec.waitEvent(ctx, t, s.ID(), "terminated")
	ended := rec.waitEvent(ctx, t, s.ID(), SessionEndedEvent)
	if !strings.Contains(string(ended.Body), "adapter terminated") {
		t.Fatalf("%s body = %s, want reason \"adapter terminated\"", SessionEndedEvent, ended.Body)
	}
	if infos := List(); len(infos) != 0 {
		t.Fatalf("List() after terminated = %+v, want empty", infos)
	}
	// The session end ended the debuggee terminal's program but kept what it
	// printed, so a Console that attaches late still shows it.
	after := waitTerminalOutput(t, terminalID, "mincode-terminal-smoke: started")
	if !after.Ended {
		t.Fatalf("attach after the session ended = %+v, want ended with the kept output", after)
	}
	if _, err := dispatch(t, "close_local_shell", map[string]any{"sessionId": terminalID}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatch(t, "attach_local_shell", map[string]any{"sessionId": terminalID}); err == nil {
		t.Fatalf("terminal %s still attachable after close_local_shell", terminalID)
	}
}

// smokeLocal returns the value of variable name in the scopes of frameID
// ("" when not found).
func smokeLocal(ctx context.Context, t *testing.T, s *Session, frameID int, name string) string {
	t.Helper()
	var scopes struct {
		Scopes []struct {
			VariablesReference int `json:"variablesReference"`
		} `json:"scopes"`
	}
	smokeRequest(ctx, t, s, "scopes", map[string]any{"frameId": frameID}, &scopes)
	for _, sc := range scopes.Scopes {
		if sc.VariablesReference == 0 {
			continue
		}
		var vars struct {
			Variables []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"variables"`
		}
		smokeRequest(ctx, t, s, "variables", map[string]any{"variablesReference": sc.VariablesReference}, &vars)
		for _, v := range vars.Variables {
			if v.Name == name {
				return v.Value
			}
		}
	}
	return ""
}

// smokeRequest sends a DAP request and decodes its body into out (if non-nil).
func smokeRequest(ctx context.Context, t *testing.T, s *Session, command string, args, out any) {
	t.Helper()
	body, err := s.Request(ctx, command, args)
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("decode %s response: %v (%s)", command, err, body)
		}
	}
}

// waitEvent polls the recorded emissions (not the lossy channel, which drops
// events once full) until session id has emitted name, and returns the first
// such event. It fails at once when the session ended without emitting name.
func (r *emitRecorder) waitEvent(ctx context.Context, t *testing.T, id, name string) Event {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		r.mu.Lock()
		ended := false
		for _, ev := range r.events {
			if ev.SessionID == id && ev.Event == name {
				r.mu.Unlock()
				return ev
			}
			ended = ended || ev.SessionID == id && ev.Event == SessionEndedEvent
		}
		r.mu.Unlock()
		if ended {
			t.Fatalf("no %q event for %s: the session ended", name, id)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("no %q event for %s: %v", name, id, ctx.Err())
			return Event{}
		}
	}
}

// logAll logs every recorded emission, for diagnosing a failed smoke run.
func (r *emitRecorder) logAll(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		t.Logf("event %s: %s", ev.Event, ev.Body)
	}
}
