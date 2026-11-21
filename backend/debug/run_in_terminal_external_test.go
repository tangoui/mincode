package debug

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mincode/app/backend/terminal"
)

// fakeExternalTerminal stands in for terminal.StartExternalDebuggee: it
// records each launch and, unless err is set, runs the program as a plain
// child process (no window) whose End kills it.
type fakeExternalTerminal struct {
	mu     sync.Mutex
	err    error
	specs  []terminal.DebuggeeShellSpec
	titles []string
	ended  int
}

func useFakeExternalTerminal(t *testing.T, err error) *fakeExternalTerminal {
	t.Helper()
	fake := &fakeExternalTerminal{err: err}
	prev := startExternal
	startExternal = func(_ context.Context, spec terminal.DebuggeeShellSpec, title string) (externalStart, error) {
		fake.mu.Lock()
		fake.specs = append(fake.specs, spec)
		fake.titles = append(fake.titles, title)
		fake.mu.Unlock()
		if fake.err != nil {
			return externalStart{}, fake.err
		}
		cmd := exec.Command(spec.Executable, spec.Args...)
		cmd.Dir = spec.Cwd
		cmd.Env = os.Environ()
		for k, v := range spec.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		if err := cmd.Start(); err != nil {
			return externalStart{}, err
		}
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		return externalStart{PID: cmd.Process.Pid, Terminal: "FakeTerm", Program: endFunc(func() {
			fake.mu.Lock()
			fake.ended++
			fake.mu.Unlock()
			_ = cmd.Process.Kill()
			<-done
		})}, nil
	}
	t.Cleanup(func() { startExternal = prev })
	return fake
}

type endFunc func()

func (f endFunc) End() { f() }

func (f *fakeExternalTerminal) launches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.specs)
}

// runInTerminalBody waits for the mincode.runInTerminal event of sessionID
// and returns its body.
func runInTerminalBody(t *testing.T, rec *emitRecorder, sessionID string) map[string]any {
	t.Helper()
	terminalEvent := rec.waitEvent(testCtx(t), t, sessionID, RunInTerminalEvent)
	var body map[string]any
	if err := json.Unmarshal(terminalEvent.Body, &body); err != nil {
		t.Fatalf("event body %s: %v", terminalEvent.Body, err)
	}
	return body
}

func TestRunInTerminalExternalRunsInSystemTerminal(t *testing.T) {
	rec := captureEmits(t)
	fake := useFakeExternalTerminal(t, nil)
	fa, s, root := registerScripted(t, nil)
	resp := runInTerminalReply(t, fa, map[string]any{
		"kind":  "external",
		"title": "Python Debug Console",
		"cwd":   root,
		"args":  []string{testExecutable(t), "sleep"},
		"env":   programEnv(map[string]any{"LD_PRELOAD": "/tmp/evil.so", "KEEP_ME": "kept", "DROP_ME": nil}),
	})
	pid := responsePID(t, resp)
	if fake.launches() != 1 {
		t.Fatalf("external launches = %d", fake.launches())
	}
	spec := fake.specs[0]
	if spec.Executable != testExecutable(t) || spec.Cwd != root || fake.titles[0] != "Python Debug Console" {
		t.Fatalf("spec = %+v, title %q", spec, fake.titles[0])
	}
	// The environment went through the same filter as for the integrated terminal.
	if _, ok := spec.Env["LD_PRELOAD"]; ok || spec.Env["KEEP_ME"] != "kept" || len(spec.UnsetEnv) != 1 || spec.UnsetEnv[0] != "DROP_ME" {
		t.Fatalf("env = %v, unset %v", spec.Env, spec.UnsetEnv)
	}
	body := runInTerminalBody(t, rec, s.ID())
	if body["external"] != true || body["terminal"] != "FakeTerm" || body["title"] != "Python Debug Console" {
		t.Fatalf("event body = %v", body)
	}
	if _, ok := body["terminalSessionId"]; ok {
		t.Fatalf("an external run announced an integrated terminal: %v", body)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d", pid)
	}
	if _, err := dispatch(t, "attach_local_shell", map[string]any{"sessionId": "debug-" + s.ID() + "-1"}); err == nil {
		t.Fatal("an integrated terminal was started as well")
	}
}

func TestRunInTerminalExternalRefusedLaunchOpensNothing(t *testing.T) {
	rec := captureEmits(t)
	fake := useFakeExternalTerminal(t, nil)
	fa, s, root := registerScripted(t, nil)
	for _, args := range []map[string]any{
		{"kind": "external", "cwd": t.TempDir(), "args": []string{testExecutable(t), "sleep"}},
		{"kind": "external", "cwd": filepath.Join(root, ".."), "args": []string{testExecutable(t), "sleep"}},
		{"kind": "external", "cwd": root, "args": []string{"mincode-no-such-program"}},
		{"kind": "external", "cwd": root, "args": []string{}},
	} {
		resp := runInTerminalReply(t, fa, args)
		if resp.Success == nil || *resp.Success || resp.Message == "" {
			t.Fatalf("%v: success=%v message=%q", args, resp.Success, resp.Message)
		}
	}
	if fake.launches() != 0 {
		t.Fatalf("an external terminal was opened %d times for refused launches", fake.launches())
	}
	// A refusal by the terminal package's own validation fails the request
	// too: it would fail the integrated terminal just the same.
	fake.err = errors.New("start debuggee: profile working directory is not absolute")
	resp := runInTerminalReply(t, fa, map[string]any{"kind": "external", "cwd": root, "args": []string{testExecutable(t), "sleep"}})
	if resp.Success == nil || *resp.Success {
		t.Fatal("a refused external launch fell back to the integrated terminal")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, ev := range rec.events {
		if ev.SessionID == s.ID() && ev.Event == RunInTerminalEvent {
			t.Fatalf("terminal announced for a refused request: %s", ev.Body)
		}
	}
}

func TestRunInTerminalExternalFallsBackWithReason(t *testing.T) {
	rec := captureEmits(t)
	useFakeExternalTerminal(t, &terminal.ExternalTerminalError{
		Code: terminal.ExternalUnavailable,
		Err:  errors.New("no graphical session (neither DISPLAY nor WAYLAND_DISPLAY is set)"),
	})
	fa, s, root := registerScripted(t, nil)
	resp := runInTerminalReply(t, fa, map[string]any{
		"kind": "external",
		"cwd":  root,
		"args": []string{testExecutable(t), "sleep"},
		"env":  programEnv(nil),
	})
	responsePID(t, resp)
	body := runInTerminalBody(t, rec, s.ID())
	terminalID, _ := body["terminalSessionId"].(string)
	if terminalID != "debug-"+s.ID()+"-1" {
		t.Fatalf("event body = %v, want the integrated terminal", body)
	}
	fallback, _ := body["externalFallback"].(map[string]any)
	if fallback["code"] != terminal.ExternalUnavailable || !strings.Contains(fallback["message"].(string), "DISPLAY") {
		t.Fatalf("externalFallback = %v", body["externalFallback"])
	}
	waitTerminalOutput(t, terminalID, "ready")
}

func TestRunInTerminalExternalProgramEndsWithSession(t *testing.T) {
	for _, end := range []string{"debug_stop", "close"} {
		t.Run(end, func(t *testing.T) {
			rec := captureEmits(t)
			fake := useFakeExternalTerminal(t, nil)
			fa, s, root := registerScripted(t, func(fa *fakeAdapter) { answerBegin(fa) })
			beginScripted(t, s, "launch")
			pid := responsePID(t, runInTerminalReply(t, fa, map[string]any{
				"kind": "external",
				"cwd":  root,
				"args": []string{testExecutable(t), "sleep"},
				"env":  programEnv(nil),
			}))
			if end == "debug_stop" {
				if _, err := dispatch(t, "debug_stop", map[string]any{"sessionId": s.ID()}); err != nil {
					t.Fatal(err)
				}
			} else {
				s.Close("adapter went away")
			}
			rec.waitEnded(t, s.ID())
			waitProcessGone(t, pid)
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if fake.ended != 1 {
				t.Fatalf("End called %d times", fake.ended)
			}
		})
	}
}

func TestRunInTerminalExternalAfterSessionEndStartsNothing(t *testing.T) {
	rec := captureEmits(t)
	fake := useFakeExternalTerminal(t, nil)
	_, s, root := registerScripted(t, nil)
	s.Close("ended")
	rec.waitEnded(t, s.ID())
	e := &entry{info: SessionInfo{ID: s.ID(), ProjectRoot: root}, done: make(chan struct{})}
	close(e.done)
	e.closeTerminals()
	if _, err := e.runInTerminal(testCtx(t), RunInTerminalArgs{Kind: "external", Cwd: root, Args: []string{testExecutable(t), "sleep"}}); err == nil {
		t.Fatal("runInTerminal started a program after the session ended")
	}
	if fake.launches() != 0 {
		t.Fatalf("external launches = %d", fake.launches())
	}
}

func TestRunInTerminalExternalStartedAsSessionEndsIsEnded(t *testing.T) {
	captureEmits(t)
	fake := useFakeExternalTerminal(t, nil)
	_, s, root := registerScripted(t, nil)
	e := &entry{info: SessionInfo{ID: s.ID(), ProjectRoot: root}, done: make(chan struct{})}
	// The session ends while the window is opening.
	inner := startExternal
	startExternal = func(ctx context.Context, spec terminal.DebuggeeShellSpec, title string) (externalStart, error) {
		started, err := inner(ctx, spec, title)
		e.closeTerminals()
		return started, err
	}
	if _, err := e.runInTerminal(testCtx(t), RunInTerminalArgs{Kind: "external", Cwd: root, Args: []string{testExecutable(t), "sleep"}, Env: map[string]*string{fakeProgramEnv: ptr("1")}}); err == nil {
		t.Fatal("runInTerminal succeeded for a session that ended during the start")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.ended != 1 {
		t.Fatalf("the program started as the session ended was not ended (End called %d times)", fake.ended)
	}
}

func ptr(s string) *string { return &s }

func TestRunInTerminalExternalAcceptsTheConfirmedLaunchCwdOutsideProject(t *testing.T) {
	captureEmits(t)
	fake := useFakeExternalTerminal(t, nil)
	fa, s, _ := registerScripted(t, nil)
	outside := t.TempDir()
	s.mu.Lock()
	s.confirmedOutsideCwd = realDir(t, outside)
	s.mu.Unlock()
	responsePID(t, runInTerminalReply(t, fa, map[string]any{
		"kind": "external",
		"cwd":  outside,
		"args": []string{testExecutable(t), "sleep"},
		"env":  programEnv(nil),
	}))
	if fake.launches() != 1 || fake.specs[0].Cwd != realDir(t, outside) {
		t.Fatalf("external launch cwd = %+v, want the confirmed real path %s", fake.specs, realDir(t, outside))
	}
	// Any other directory outside the project is still refused before a window opens.
	resp := runInTerminalReply(t, fa, map[string]any{"kind": "external", "cwd": t.TempDir(), "args": []string{testExecutable(t), "sleep"}})
	if resp.Success == nil || *resp.Success || fake.launches() != 1 {
		t.Fatalf("unconfirmed outside cwd: success=%v launches=%d", resp.Success, fake.launches())
	}
}

func TestRunInTerminalExternalFallbackReasonIsBounded(t *testing.T) {
	rec := captureEmits(t)
	useFakeExternalTerminal(t, &terminal.ExternalTerminalError{
		Code: terminal.ExternalFailed,
		Err:  errors.New("xterm: exit status 1: " + strings.Repeat("Warning: cannot open display ", 200)),
	})
	fa, s, root := registerScripted(t, nil)
	responsePID(t, runInTerminalReply(t, fa, map[string]any{
		"kind": "external", "cwd": root, "args": []string{testExecutable(t), "sleep"}, "env": programEnv(nil),
	}))
	fallback, _ := runInTerminalBody(t, rec, s.ID())["externalFallback"].(map[string]any)
	message, _ := fallback["message"].(string)
	if fallback["code"] != terminal.ExternalFailed || len([]rune(message)) > 512 || strings.HasPrefix(message, "external terminal:") || !strings.HasPrefix(message, "xterm: exit status 1") {
		t.Fatalf("externalFallback = %v (%d runes)", fallback, len([]rune(message)))
	}
}
