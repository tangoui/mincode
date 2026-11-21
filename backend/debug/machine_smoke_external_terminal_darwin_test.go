//go:build darwin

package debug

// Opt-in macOS smoke test of runInTerminal kind "external": debugpy with
// console "externalTerminal" runs the program in a real Terminal.app window.
// It opens (and leaves) Terminal windows, so it needs a desktop session and
// its own flag besides the debugpy machine smoke's:
//
//	MINCODE_DEBUG_MACHINE_SMOKE=1 MINCODE_DEBUG_SMOKE_EXTERNAL_TERMINAL=1 \
//	MINCODE_DEBUG_SMOKE_PYTHON=$HOME/.cache/mincode-debugpy-venv/bin/python \
//	  go test ./backend/debug -run TestMachineSmokePythonExternalTerminal -v

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"mincode/app/backend/terminal"
)

func TestMachineSmokePythonExternalTerminal(t *testing.T) {
	for _, end := range []string{"debug_stop", "adapter killed"} {
		t.Run(end, func(t *testing.T) { smokeExternalTerminal(t, end) })
	}
}

func smokeExternalTerminal(t *testing.T, end string) {
	python := smokePython(t)
	if os.Getenv("MINCODE_DEBUG_SMOKE_EXTERNAL_TERMINAL") != "1" {
		t.Skip("set MINCODE_DEBUG_SMOKE_EXTERNAL_TERMINAL=1 to open Terminal.app windows")
	}
	app := smokeScript(t, "terminal_app.py")
	root := filepath.Dir(app)
	// Launch directories go under a fresh TMPDIR, so leftovers are visible.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	// Record what the real external start returned.
	var mu sync.Mutex
	var started externalStart
	inner := startExternal
	startExternal = func(ctx context.Context, spec terminal.DebuggeeShellSpec, title string) (externalStart, error) {
		s, err := inner(ctx, spec, title)
		mu.Lock()
		started = s
		mu.Unlock()
		return s, err
	}
	t.Cleanup(func() { startExternal = inner })

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
	launchArgs, _ := json.Marshal(map[string]any{
		"type":       "python",
		"program":    app,
		"cwd":        root,
		"python":     []string{python},
		"console":    "externalTerminal",
		"justMyCode": true,
		"env":        map[string]string{"MINCODE_SMOKE_SECRET": "it's $secret"},
	})
	if _, err := s.Begin(ctx, BeginParams{
		AdapterID:   "debugpy",
		Request:     "launch",
		Arguments:   launchArgs,
		Breakpoints: map[string][]SourceBreakpoint{app: {{Line: 5}}},
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	ev := rec.waitEvent(ctx, t, s.ID(), RunInTerminalEvent)
	var body map[string]any
	_ = json.Unmarshal(ev.Body, &body)
	if body["external"] != true || body["terminal"] != "Terminal" {
		t.Fatalf("%s body = %s, want an external Terminal run", RunInTerminalEvent, ev.Body)
	}
	if left := dirEntries(t, tmp); len(left) != 0 {
		t.Fatalf("temporary files left after the start: %v", left)
	}

	stopped := rec.waitEvent(ctx, t, s.ID(), "stopped")
	var stop struct {
		Reason   string `json:"reason"`
		ThreadID int    `json:"threadId"`
	}
	_ = json.Unmarshal(stopped.Body, &stop)
	if stop.Reason != "breakpoint" {
		t.Fatalf("stopped = %s, want breakpoint", stopped.Body)
	}
	var trace struct {
		StackFrames []struct {
			ID   int `json:"id"`
			Line int `json:"line"`
		} `json:"stackFrames"`
	}
	smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": stop.ThreadID, "startFrame": 0, "levels": 20}, &trace)
	if len(trace.StackFrames) == 0 || trace.StackFrames[0].Line != 5 {
		t.Fatalf("stackTrace = %+v, want line 5", trace.StackFrames)
	}
	if total := smokeLocal(ctx, t, s, trace.StackFrames[0].ID, "total"); total != "5" {
		t.Fatalf("total = %q", total)
	}

	// processId is the program debugpy asked to run (its launcher), and the
	// debugged Python process (debugpy's `process` event) is its child.
	mu.Lock()
	launcher := started.PID
	mu.Unlock()
	if launcher <= 0 || !smokeAlive(launcher) {
		t.Fatalf("processId = %d, want a running process", launcher)
	}
	processEvent := rec.waitEvent(ctx, t, s.ID(), "process")
	var proc struct {
		SystemProcessID int `json:"systemProcessId"`
	}
	_ = json.Unmarshal(processEvent.Body, &proc)
	if proc.SystemProcessID <= 0 {
		t.Fatalf("process event = %s", processEvent.Body)
	}
	if ppid := smokeParent(t, proc.SystemProcessID); ppid != launcher {
		t.Fatalf("debuggee %d has parent %d, want the reported processId %d", proc.SystemProcessID, ppid, launcher)
	}
	t.Logf("processId %d (launcher), debuggee %d", launcher, proc.SystemProcessID)

	if end == "debug_stop" {
		if _, err := dispatch(t, "debug_stop", map[string]any{"sessionId": s.ID()}); err != nil {
			t.Fatal(err)
		}
	} else {
		s.Close("adapter went away")
	}
	rec.waitEnded(t, s.ID())
	waitGone(t, launcher)
	waitGone(t, proc.SystemProcessID)
	if left := dirEntries(t, tmp); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func smokeAlive(pid int) bool {
	return exec.Command("/bin/kill", "-0", strconv.Itoa(pid)).Run() == nil
}

func smokeParent(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("/bin/ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps %d: %v", pid, err)
	}
	ppid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return ppid
}
