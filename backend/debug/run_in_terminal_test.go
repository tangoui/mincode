package debug

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeProgramEnv makes the test binary act as a debuggee started through
// runInTerminal; os.Args[1] picks the behaviour (see runFakeProgram).
const fakeProgramEnv = "GO_DEBUG_FAKE_PROGRAM"

// runFakeProgram is the debuggee role of the test binary:
//
//	sleep           print "ready" and sleep until killed
//	env NAME...     print NAME=<value> (or NAME=<unset>) per name, then sleep
func runFakeProgram(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "fake program: no mode")
		return 2
	}
	switch args[0] {
	case "sleep":
		fmt.Println("ready")
	case "env":
		for _, name := range args[1:] {
			if v, ok := os.LookupEnv(name); ok {
				fmt.Printf("%s=%s\n", name, v)
			} else {
				fmt.Printf("%s=<unset>\n", name)
			}
		}
		fmt.Println("done")
	default:
		fmt.Fprintf(os.Stderr, "fake program: unknown mode %q\n", args[0])
		return 2
	}
	time.Sleep(time.Hour)
	return 0
}

// runInTerminalReply sends a runInTerminal reverse request and returns the
// client's response.
func runInTerminalReply(t *testing.T, fa *fakeAdapter, args map[string]any) Message {
	t.Helper()
	seq := fa.SendRequest("runInTerminal", args)
	select {
	case resp := <-fa.responses:
		if resp.RequestSeq != seq || resp.Command != "runInTerminal" {
			t.Fatalf("response = %+v, want runInTerminal request_seq %d", resp, seq)
		}
		return resp
	case <-time.After(testTimeout):
		t.Fatal("no response to runInTerminal")
		return Message{}
	}
}

func testExecutable(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func programEnv(extra map[string]any) map[string]any {
	env := map[string]any{fakeProgramEnv: "1"}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

type attachResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Offset int64  `json:"offset"`
	Ended  bool   `json:"ended"`
}

// waitTerminalOutput attaches to the terminal session until its buffered
// output contains want.
func waitTerminalOutput(t *testing.T, terminalID, want string) attachResult {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	var last attachResult
	for time.Now().Before(deadline) {
		raw, err := dispatch(t, "attach_local_shell", map[string]any{"sessionId": terminalID})
		if err != nil {
			t.Fatalf("attach_local_shell %s: %v", terminalID, err)
		}
		last = attachResult{}
		if err := json.Unmarshal([]byte(raw), &last); err != nil {
			t.Fatalf("attach result %s: %v", raw, err)
		}
		if strings.Contains(last.Output, want) {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("terminal %s output %q never contained %q", terminalID, last.Output, want)
	return last
}

// runInTerminalEvent waits for the mincode.runInTerminal event of sessionID.
func runInTerminalEvent(t *testing.T, rec *emitRecorder, sessionID string) (terminalID, title string) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		for _, ev := range rec.events {
			if ev.SessionID == sessionID && ev.Event == RunInTerminalEvent {
				var body struct {
					TerminalSessionID string `json:"terminalSessionId"`
					Title             string `json:"title"`
				}
				if err := json.Unmarshal(ev.Body, &body); err != nil {
					rec.mu.Unlock()
					t.Fatalf("event body %s: %v", ev.Body, err)
				}
				rec.mu.Unlock()
				return body.TerminalSessionID, body.Title
			}
		}
		rec.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s event for %s", RunInTerminalEvent, sessionID)
	return "", ""
}

func responsePID(t *testing.T, resp Message) int {
	t.Helper()
	if resp.Success == nil || !*resp.Success {
		t.Fatalf("runInTerminal failed: %q", resp.Message)
	}
	var body RunInTerminalResult
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("response body %s: %v", resp.Body, err)
	}
	if body.ProcessID <= 0 {
		t.Fatalf("processId = %d, want > 0 (body %s)", body.ProcessID, resp.Body)
	}
	return body.ProcessID
}

func TestRunInTerminalStartsPTYAndReportsPID(t *testing.T) {
	rec := captureEmits(t)
	fa, s, root := registerScripted(t, nil)
	resp := runInTerminalReply(t, fa, map[string]any{
		"kind":  "integrated",
		"title": "Fake program",
		"cwd":   root,
		"args":  []string{testExecutable(t), "sleep"},
		"env":   programEnv(nil),
	})
	responsePID(t, resp)
	terminalID, title := runInTerminalEvent(t, rec, s.ID())
	if want := "debug-" + s.ID() + "-1"; terminalID != want {
		t.Fatalf("terminalSessionId = %q, want %q", terminalID, want)
	}
	if title != "Fake program" {
		t.Fatalf("title = %q", title)
	}
	got := waitTerminalOutput(t, terminalID, "ready")
	if !got.OK || got.Ended || got.Offset < int64(len("ready")) {
		t.Fatalf("attach = %+v", got)
	}
}

func TestRunInTerminalDefaultsCwdToProjectRoot(t *testing.T) {
	captureEmits(t)
	fa, _, _ := registerScripted(t, nil)
	resp := runInTerminalReply(t, fa, map[string]any{
		"args": []string{testExecutable(t), "sleep"},
		"env":  programEnv(nil),
	})
	responsePID(t, resp)
}

func TestRunInTerminalRejectsCwdOutsideProject(t *testing.T) {
	rec := captureEmits(t)
	fa, s, root := registerScripted(t, nil)
	for _, cwd := range []string{t.TempDir(), filepath.Join(root, ".."), "relative/dir"} {
		resp := runInTerminalReply(t, fa, map[string]any{
			"cwd":  cwd,
			"args": []string{testExecutable(t), "sleep"},
			"env":  programEnv(nil),
		})
		if resp.Success == nil || *resp.Success {
			t.Fatalf("cwd %q: runInTerminal succeeded", cwd)
		}
		if resp.Message == "" {
			t.Fatalf("cwd %q: empty failure message", cwd)
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, ev := range rec.events {
		if ev.SessionID == s.ID() && ev.Event == RunInTerminalEvent {
			t.Fatalf("terminal announced for a rejected request: %s", ev.Body)
		}
	}
}

func TestRunInTerminalAcceptsTheLaunchCwdOutsideProject(t *testing.T) {
	captureEmits(t)
	fa, s, root := registerScripted(t, nil)
	outside := t.TempDir()
	// The launch request asked for it (the user's run configuration; confirmed with the launch).
	s.mu.Lock()
	s.confirmedOutsideCwd = realDir(t, outside)
	s.mu.Unlock()
	if got := CwdOutsideProject(root, outside); got != realDir(t, outside) {
		t.Fatalf("CwdOutsideProject(%q) = %q", outside, got)
	}
	responsePID(t, runInTerminalReply(t, fa, map[string]any{
		"cwd":  outside + string(filepath.Separator),
		"args": []string{testExecutable(t), "sleep"},
		"env":  programEnv(nil),
	}))
	// Any other directory outside the project the adapter names is still refused.
	for _, cwd := range []string{t.TempDir(), filepath.Join(outside, "..")} {
		resp := runInTerminalReply(t, fa, map[string]any{
			"cwd":  cwd,
			"args": []string{testExecutable(t), "sleep"},
			"env":  programEnv(nil),
		})
		if resp.Success == nil || *resp.Success {
			t.Fatalf("cwd %q: runInTerminal succeeded", cwd)
		}
	}
}

func TestRunInTerminalRefusesAnUnconfirmedLaunchCwdThatNowLeadsOutside(t *testing.T) {
	captureEmits(t)
	fa, s, root := registerScripted(t, func(fa *fakeAdapter) {
		fa.Handle("initialize", func(req Message) {
			fa.Respond(req, true, map[string]any{"supportsConfigurationDoneRequest": true}, "")
			fa.SendEvent("initialized", nil)
		})
		fa.Handle("launch", func(req Message) { fa.Respond(req, true, nil, "") })
		fa.Handle("configurationDone", func(req Message) { fa.Respond(req, true, nil, "") })
	})
	// The launch cwd was a directory in the project: nothing outside was confirmed.
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]any{"cwd": sub})
	if _, err := s.Begin(testCtx(t), BeginParams{AdapterID: "fake", Request: "launch", Arguments: arguments}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// Later it is replaced by a link to a directory outside the project.
	if err := os.Remove(sub); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), sub); err != nil {
		t.Skipf("symlink: %v", err)
	}
	resp := runInTerminalReply(t, fa, map[string]any{"cwd": sub, "args": []string{testExecutable(t), "sleep"}, "env": programEnv(nil)})
	if resp.Success == nil || *resp.Success {
		t.Fatalf("runInTerminal in %s (now outside the project, never confirmed) succeeded", sub)
	}
}

// realDir is dir with symbolic links resolved (t.TempDir lies under /var on macOS).
func realDir(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestRunInTerminalRefusesAConfirmedCwdLinkPointedElsewhere(t *testing.T) {
	captureEmits(t)
	fa, s, root := registerScripted(t, nil)
	target, other := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "workdir")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	// Prepare judged the link and confirmed the directory it pointed to.
	confirmed := CwdOutsideProject(root, link)
	if confirmed != realDir(t, target) {
		t.Fatalf("CwdOutsideProject(%q) = %q, want %q", link, confirmed, realDir(t, target))
	}
	s.mu.Lock()
	s.confirmedOutsideCwd = confirmed
	s.mu.Unlock()
	run := func() Message {
		return runInTerminalReply(t, fa, map[string]any{
			"cwd":  link,
			"args": []string{testExecutable(t), "sleep"},
			"env":  programEnv(nil),
		})
	}
	responsePID(t, run())
	// The link now points elsewhere: the confirmation no longer covers it.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if resp := run(); resp.Success == nil || *resp.Success {
		t.Fatalf("runInTerminal in %s (re-pointed to %s after confirmation) succeeded", link, other)
	}
}

func TestCwdOutsideProject(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("symlink: %v", err)
	}
	for cwd, want := range map[string]string{
		"": "", "relative": "", root: "", sub: "", filepath.Join(root, "missing"): "",
		outside: realDir(t, outside), filepath.Join(root, ".."): realDir(t, filepath.Dir(root)),
		// A link is judged, and kept, by the directory it points to.
		escape: realDir(t, outside),
	} {
		if got := CwdOutsideProject(root, cwd); got != want {
			t.Errorf("CwdOutsideProject(%q) = %q, want %q", cwd, got, want)
		}
	}
	if got := LaunchArgumentsCwd(json.RawMessage(`{"cwd":"/x","program":"a"}`)); got != "/x" {
		t.Errorf("LaunchArgumentsCwd = %q", got)
	}
	for _, raw := range []string{`{"cwd":3}`, `{}`, `[1]`, ``} {
		if got := LaunchArgumentsCwd(json.RawMessage(raw)); got != "" {
			t.Errorf("LaunchArgumentsCwd(%s) = %q", raw, got)
		}
	}
}

func TestRunInTerminalAcceptsSubdirectoryCwd(t *testing.T) {
	captureEmits(t)
	fa, _, root := registerScripted(t, nil)
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	resp := runInTerminalReply(t, fa, map[string]any{
		"cwd":  sub,
		"args": []string{testExecutable(t), "sleep"},
		"env":  programEnv(nil),
	})
	responsePID(t, resp)
}

func TestRunInTerminalResolvesBareProgram(t *testing.T) {
	rec := captureEmits(t)
	binDir := t.TempDir()
	name := "mincode-fake-debuggee"
	file := name
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	copyFile(t, testExecutable(t), filepath.Join(binDir, file))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	fa, s, root := registerScripted(t, nil)
	resp := runInTerminalReply(t, fa, map[string]any{
		"cwd":  root,
		"args": []string{name, "sleep"},
		"env":  programEnv(nil),
	})
	responsePID(t, resp)
	terminalID, title := runInTerminalEvent(t, rec, s.ID())
	if title != name {
		t.Fatalf("default title = %q, want %q", title, name)
	}
	waitTerminalOutput(t, terminalID, "ready")

	missing := runInTerminalReply(t, fa, map[string]any{
		"cwd":  root,
		"args": []string{"mincode-no-such-program", "sleep"},
	})
	if missing.Success == nil || *missing.Success || !strings.Contains(missing.Message, "mincode-no-such-program") {
		t.Fatalf("missing program: success=%v message=%q", missing.Success, missing.Message)
	}
}

func TestRunInTerminalDropsDeniedEnv(t *testing.T) {
	rec := captureEmits(t)
	t.Setenv("DEBUGGEE_TEST_REMOVE_ME", "inherited")
	fa, s, root := registerScripted(t, nil)
	resp := runInTerminalReply(t, fa, map[string]any{
		"cwd":  root,
		"args": []string{testExecutable(t), "env", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "DEBUGGEE_TEST_KEEP", "DEBUGGEE_TEST_REMOVE_ME"},
		"env": programEnv(map[string]any{
			"LD_PRELOAD":              "/tmp/evil.so",
			"DYLD_INSERT_LIBRARIES":   "/tmp/evil.dylib",
			"DEBUGGEE_TEST_KEEP":      "kept",
			"DEBUGGEE_TEST_REMOVE_ME": nil,
		}),
	})
	responsePID(t, resp)
	terminalID, _ := runInTerminalEvent(t, rec, s.ID())
	out := waitTerminalOutput(t, terminalID, "done").Output
	for _, want := range []string{"LD_PRELOAD=<unset>", "DYLD_INSERT_LIBRARIES=<unset>", "DEBUGGEE_TEST_KEEP=kept", "DEBUGGEE_TEST_REMOVE_ME=<unset>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("debuggee output %q lacks %q", out, want)
		}
	}
}

func TestRunInTerminalPTYClosedOnSessionEnd(t *testing.T) {
	for _, end := range []string{"debug_stop", "close"} {
		t.Run(end, func(t *testing.T) {
			rec := captureEmits(t)
			fa, s, root := registerScripted(t, func(fa *fakeAdapter) { answerBegin(fa) })
			beginScripted(t, s, "launch")
			pid := responsePID(t, runInTerminalReply(t, fa, map[string]any{
				"cwd":  root,
				"args": []string{testExecutable(t), "sleep"},
				"env":  programEnv(nil),
			}))
			terminalID, _ := runInTerminalEvent(t, rec, s.ID())
			waitTerminalOutput(t, terminalID, "ready")

			if end == "debug_stop" {
				if _, err := dispatch(t, "debug_stop", map[string]any{"sessionId": s.ID()}); err != nil {
					t.Fatal(err)
				}
			} else {
				s.Close("adapter went away")
			}
			rec.waitEnded(t, s.ID())
			waitProcessGone(t, pid)
			// The program is gone but what it printed stays readable: the
			// Console attaches only after the start returned, which can be
			// after a short program already finished.
			raw, err := dispatch(t, "attach_local_shell", map[string]any{"sessionId": terminalID})
			if err != nil {
				t.Fatalf("attach after session end: %v", err)
			}
			var got attachResult
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatal(err)
			}
			if !got.OK || !got.Ended || !strings.Contains(got.Output, "ready") {
				t.Fatalf("attach after session end = %+v, want ended with the program's output", got)
			}
			// Closing the terminal view drops the kept output.
			if _, err := dispatch(t, "close_local_shell", map[string]any{"sessionId": terminalID}); err != nil {
				t.Fatal(err)
			}
			_, err = dispatch(t, "attach_local_shell", map[string]any{"sessionId": terminalID})
			wantErrContains(t, err, "not_found:")
		})
	}
}

func TestRunInTerminalAfterSessionEndStartsNothing(t *testing.T) {
	rec := captureEmits(t)
	_, s, root := registerScripted(t, nil)
	s.Close("ended")
	rec.waitEnded(t, s.ID())
	e := &entry{info: SessionInfo{ID: s.ID(), ProjectRoot: root}, done: make(chan struct{})}
	close(e.done)
	e.closeTerminals()
	if _, err := e.runInTerminal(testCtx(t), RunInTerminalArgs{Cwd: root, Args: []string{testExecutable(t), "sleep"}}); err == nil {
		t.Fatal("runInTerminal started a program after the session ended")
	}
	if _, err := dispatch(t, "attach_local_shell", map[string]any{"sessionId": "debug-" + s.ID() + "-1"}); err == nil {
		t.Fatal("terminal exists after a refused start")
	}
}

func TestRequestOnEndedSessionIsUnavailable(t *testing.T) {
	rec := captureEmits(t)
	// The adapter ends the session instead of answering.
	_, s, _ := registerScripted(t, func(fa *fakeAdapter) {
		fa.Handle("threads", func(Message) { fa.SendEvent("terminated", nil) })
	})
	_, err := dispatch(t, "debug_request", map[string]any{"sessionId": s.ID(), "command": "threads"})
	wantErrContains(t, err, "unavailable: debug session ended")
	rec.waitEnded(t, s.ID())

	_, s2, root2 := registerScripted(t, func(fa *fakeAdapter) {
		fa.Handle("setBreakpoints", func(Message) { fa.SendEvent("terminated", nil) })
	})
	file2 := filepath.Join(root2, "main.py")
	if err := os.WriteFile(file2, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = dispatch(t, "debug_set_breakpoints", map[string]any{
		"sessionId": s2.ID(), "path": file2, "breakpoints": []map[string]any{{"line": 1}},
	})
	wantErrContains(t, err, "unavailable: debug session ended")
	rec.waitEnded(t, s2.ID())
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
