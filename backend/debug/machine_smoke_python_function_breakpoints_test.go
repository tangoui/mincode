package debug

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestMachineSmokePythonFunctionBreakpoints measures debugpy's method
// (DAP function) breakpoints, which official-plugins/python-debug declares
// (`functionBreakpoints`): debugpy 1.8 (pydevd) answers every name verified
// at once, also one that matches nothing; qualified names such as
// `Calc.add` or `funcs.twice` never pause (round 1: the program runs to its
// end); a bare function or method name pauses on the `def` line of every
// function or method of that name (round 2, checked through
// hitBreakpointIds). Run with the debugpy machine smoke (see machine_smoke_test.go):
//
//	MINCODE_DEBUG_MACHINE_SMOKE=1 MINCODE_DEBUG_SMOKE_PYTHON=<venv>/bin/python \
//	  go test ./backend/debug -run TestMachineSmokePythonFunctionBreakpoints -v
func TestMachineSmokePythonFunctionBreakpoints(t *testing.T) {
	python := smokePython(t)
	app := smokeScript(t, "funcs.py")
	root := filepath.Dir(app)
	rec := captureEmits(t)
	t.Cleanup(func() {
		if t.Failed() {
			rec.logAll(t)
		}
	})

	type stoppedBody struct {
		Reason           string `json:"reason"`
		ThreadID         int    `json:"threadId"`
		HitBreakpointIDs []int  `json:"hitBreakpointIds"`
	}
	// start launches funcs.py with names as its function breakpoints and
	// returns the session and the adapter's ids for them, in order.
	start := func(ctx context.Context, names ...string) (*Session, []int) {
		t.Helper()
		s, err := StartAdapter(ctx, AdapterSpec{
			Kind: "stdio", Executable: python, Args: []string{"-m", "debugpy.adapter"},
			ProjectRoot: root, OwnerPluginID: "machine-smoke", DebuggerType: "python",
		})
		if err != nil {
			t.Fatalf("StartAdapter: %v", err)
		}
		launchArgs, _ := json.Marshal(map[string]any{"type": "python", "program": app, "console": "internalConsole", "justMyCode": true})
		breakpoints := make([]FunctionBreakpoint, 0, len(names))
		for _, name := range names {
			breakpoints = append(breakpoints, FunctionBreakpoint{Name: name})
		}
		caps, err := s.Begin(ctx, BeginParams{AdapterID: "debugpy", Request: "launch", Arguments: launchArgs, FunctionBreakpoints: breakpoints})
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		var capabilities struct {
			SupportsFunctionBreakpoints bool `json:"supportsFunctionBreakpoints"`
		}
		if json.Unmarshal(caps, &capabilities) != nil || !capabilities.SupportsFunctionBreakpoints {
			t.Fatalf("capabilities %s: no supportsFunctionBreakpoints", caps)
		}
		set := rec.waitEvent(ctx, t, s.ID(), BreakpointsSetEvent)
		var results struct {
			FunctionBreakpoints []struct {
				Verified bool `json:"verified"`
				ID       int  `json:"id"`
			} `json:"functionBreakpoints"`
		}
		if err := json.Unmarshal(set.Body, &results); err != nil || len(results.FunctionBreakpoints) != len(names) {
			t.Fatalf("%s body %s (%v)", BreakpointsSetEvent, set.Body, err)
		}
		ids := []int{}
		for i, result := range results.FunctionBreakpoints {
			if !result.Verified {
				t.Errorf("function breakpoint %q not verified: %s", names[i], set.Body)
			}
			ids = append(ids, result.ID)
		}
		return s, ids
	}

	// Round 1: only qualified names and one that matches nothing. All are
	// verified, none pauses: the program runs to its end.
	ctx, cancel := context.WithTimeout(context.Background(), machineSmokeTimeout)
	defer cancel()
	s, _ := start(ctx, "Calc.add", "funcs.twice", "no_such_function")
	rec.waitEvent(ctx, t, s.ID(), "terminated")
	rec.mu.Lock()
	for _, ev := range rec.events {
		if ev.SessionID == s.ID() && ev.Event == "stopped" {
			t.Errorf("qualified names paused: stopped %s", ev.Body)
		}
	}
	rec.mu.Unlock()

	// Round 2: the bare names pause on entry, on the `def` line, each with its own id.
	ctx2, cancel2 := context.WithTimeout(context.Background(), machineSmokeTimeout)
	defer cancel2()
	s, ids := start(ctx2, "twice", "add")
	// The n-th stopped event of the session (waitEvent returns the first).
	nthStopped := func(n int) Event {
		t.Helper()
		for {
			rec.mu.Lock()
			seen := 0
			for _, ev := range rec.events {
				if ev.SessionID == s.ID() && ev.Event == "stopped" {
					if seen++; seen == n {
						rec.mu.Unlock()
						return ev
					}
				}
			}
			rec.mu.Unlock()
			select {
			case <-time.After(20 * time.Millisecond):
			case <-ctx2.Done():
				t.Fatalf("no stopped event #%d: %v", n, ctx2.Err())
			}
		}
	}
	stops := 0
	stopIn := func(want string, line, id int) int {
		t.Helper()
		stops++
		stopped := nthStopped(stops)
		var stop stoppedBody
		if err := json.Unmarshal(stopped.Body, &stop); err != nil {
			t.Fatalf("decode stopped: %v (%s)", err, stopped.Body)
		}
		var trace struct {
			StackFrames []struct {
				Name string `json:"name"`
				Line int    `json:"line"`
			} `json:"stackFrames"`
		}
		smokeRequest(ctx2, t, s, "stackTrace", map[string]any{"threadId": stop.ThreadID, "startFrame": 0, "levels": 5}, &trace)
		if len(trace.StackFrames) == 0 || trace.StackFrames[0].Name != want || trace.StackFrames[0].Line != line ||
			stop.Reason != "function breakpoint" || !slices.Equal(stop.HitBreakpointIDs, []int{id}) {
			t.Fatalf("stopped %s in %+v, want reason \"function breakpoint\", hitBreakpointIds [%d], in %s line %d", stopped.Body, trace.StackFrames, id, want, line)
		}
		t.Logf("stopped (%s, hit %v) in %s line %d", stop.Reason, stop.HitBreakpointIDs, want, line)
		return stop.ThreadID
	}
	thread := stopIn("twice", 6, ids[0])
	smokeRequest(ctx2, t, s, "continue", map[string]any{"threadId": thread}, nil)
	// A bare method name pauses in the method of a class.
	thread = stopIn("add", 2, ids[1])
	smokeRequest(ctx2, t, s, "continue", map[string]any{"threadId": thread}, nil)
	rec.waitEvent(ctx2, t, s.ID(), "terminated")
}
