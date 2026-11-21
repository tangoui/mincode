//go:build !windows

package terminal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeExternalTerminal replaces the platform terminal with one that runs the
// launch script directly (stdin /dev/null, so its closing prompt returns at
// once) and counts how often a terminal was opened.
func fakeExternalTerminal(t *testing.T, run bool) *int {
	t.Helper()
	opened := 0
	prev := openExternalTerminal
	openExternalTerminal = func(script string) (externalTerminalLaunch, error) {
		opened++
		if !run {
			return externalTerminalLaunch{Name: "Fake", Argv: []string{"/bin/sh", "-c", "exit 0"}}, nil
		}
		return externalTerminalLaunch{Name: "Fake", Argv: []string{"/bin/sh", script}}, nil
	}
	t.Cleanup(func() { openExternalTerminal = prev })
	// Launch directories go under a fresh TMPDIR, so leftovers are visible.
	t.Setenv("TMPDIR", t.TempDir())
	return &opened
}

func tempLeftovers(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func processAlive(pid int) bool {
	_, alive := processIdentity(pid)
	return alive
}

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("process %d still running", pid)
}

func TestShQuoteRoundTripsThroughSh(t *testing.T) {
	for _, s := range []string{"", "plain", "a b", `it's`, `"dq"`, "$HOME", "`id`", "$(id)", `back\slash`, "line1\nline2", "中文 ü", "*?[a]", "~", "-n", "a'b'c''"} {
		out, err := exec.Command("/bin/sh", "-c", "printf '%s' "+shQuote(s)).Output()
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if string(out) != s {
			t.Errorf("sh read %q back as %q", s, out)
		}
	}
}

func TestStartExternalDebuggeeRunsProgramAndEndsIt(t *testing.T) {
	opened := fakeExternalTerminal(t, true)
	cwd := filepath.Join(t.TempDir(), "work dir 'quoted'")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.txt")
	t.Setenv("EXT_TEST_INHERITED", "inherited")
	tricky := []string{"a b", `it's`, "$HOME", "`echo hacked`", "$(echo hacked)", "line1\nline2", "中文"}
	// The program reports its pid, arguments, cwd and environment, then sleeps.
	program := `printf '%s\n' "$$" > "$OUT.tmp"; for a in "$@"; do printf '[%s]\n' "$a"; done >> "$OUT.tmp"; pwd -P >> "$OUT.tmp"; ` +
		`printf 'SECRET=%s\nINHERITED=%s\n' "$EXT_TEST_SECRET" "${EXT_TEST_INHERITED-<unset>}" >> "$OUT.tmp"; mv "$OUT.tmp" "$OUT"; exec sleep 600`
	d, err := StartExternalDebuggee(context.Background(), DebuggeeShellSpec{
		SessionID:  "debug-ext-1",
		Executable: "/bin/sh",
		Args:       append([]string{"-c", program, "argv0"}, tricky...),
		Env:        map[string]string{"OUT": out, "EXT_TEST_SECRET": "s3cr'et $x `y`"},
		UnsetEnv:   []string{"EXT_TEST_INHERITED"},
		Cwd:        cwd,
	}, "Fake program")
	if err != nil {
		t.Fatalf("StartExternalDebuggee: %v", err)
	}
	t.Cleanup(d.End)
	if *opened != 1 || d.Terminal != "Fake" || d.PID <= 0 {
		t.Fatalf("debuggee = %+v, opened %d", d, *opened)
	}
	// The launch directory (script, pid file, environment) is gone once the pid is known.
	if left := tempLeftovers(t); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}

	var report []byte
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if report, err = os.ReadFile(out); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("program never reported: %v", err)
	}
	lines := strings.Split(string(report), "\n")
	if lines[0] != strconv.Itoa(d.PID) {
		t.Fatalf("processId = %d, but the program runs as %s", d.PID, lines[0])
	}
	text := string(report)
	for _, a := range tricky {
		if !strings.Contains(text, "["+a+"]") {
			t.Errorf("argument %q not passed literally:\n%s", a, text)
		}
	}
	if strings.Contains(text, "[hacked]") {
		t.Errorf("an argument was evaluated by the shell:\n%s", text)
	}
	realCwd, _ := filepath.EvalSymlinks(cwd)
	for _, want := range []string{"\n" + realCwd + "\n", "SECRET=s3cr'et $x `y`\n", "INHERITED=<unset>\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}

	d.End()
	waitDead(t, d.PID)
	d.End() // idempotent
}

func TestStartExternalDebuggeeValidatesBeforeOpening(t *testing.T) {
	opened := fakeExternalTerminal(t, true)
	dir := t.TempDir()
	notExec := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(notExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, spec := range map[string]DebuggeeShellSpec{
		"relative program": {SessionID: "s", Executable: "sh", Cwd: dir},
		"not executable":   {SessionID: "s", Executable: notExec, Cwd: dir},
		"missing cwd":      {SessionID: "s", Executable: "/bin/sh", Cwd: filepath.Join(dir, "missing")},
		"relative cwd":     {SessionID: "s", Executable: "/bin/sh", Cwd: "rel"},
		"bad env name":     {SessionID: "s", Executable: "/bin/sh", Cwd: dir, Env: map[string]string{"A=B": "x"}},
		"bad unset name":   {SessionID: "s", Executable: "/bin/sh", Cwd: dir, UnsetEnv: []string{"A=B"}},
		"NUL argument":     {SessionID: "s", Executable: "/bin/sh", Cwd: dir, Args: []string{"a\x00b"}},
	} {
		_, err := StartExternalDebuggee(context.Background(), spec, "t")
		if err == nil {
			t.Errorf("%s: started", name)
			continue
		}
		var ext *ExternalTerminalError
		if errors.As(err, &ext) {
			t.Errorf("%s: %v is a fallback error; a refused launch must fail like the integrated terminal", name, err)
		}
	}
	// A name sh cannot export is not a refusal of the launch: the integrated terminal can still set it.
	_, err := StartExternalDebuggee(context.Background(), DebuggeeShellSpec{SessionID: "s", Executable: "/bin/sh", Cwd: dir, Env: map[string]string{"A-B": "x"}}, "t")
	wantExternalError(t, err, ExternalUnsupported)
	if *opened != 0 {
		t.Fatalf("a terminal was opened %d times for refused launches", *opened)
	}
	if left := tempLeftovers(t); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestStartExternalDebuggeeTimeoutAndCancelCleanUp(t *testing.T) {
	fakeExternalTerminal(t, false) // the "terminal" never runs the script
	prev := externalPIDTimeout
	externalPIDTimeout = 200 * time.Millisecond
	t.Cleanup(func() { externalPIDTimeout = prev })
	spec := DebuggeeShellSpec{SessionID: "s", Executable: "/bin/sh", Cwd: t.TempDir(), Env: map[string]string{"TOKEN": "secret"}}

	_, err := StartExternalDebuggee(context.Background(), spec, "t")
	wantExternalError(t, err, ExternalFailed)
	if left := tempLeftovers(t); len(left) != 0 {
		t.Fatalf("temporary files left after the timeout: %v", left)
	}

	externalPIDTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err = StartExternalDebuggee(ctx, spec, "t")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("cancellation took %v", time.Since(start))
	}
	if left := tempLeftovers(t); len(left) != 0 {
		t.Fatalf("temporary files left after cancellation: %v", left)
	}
}

func TestStartExternalDebuggeeTerminalFailure(t *testing.T) {
	fakeExternalTerminal(t, true)
	openExternalTerminal = func(string) (externalTerminalLaunch, error) {
		return externalTerminalLaunch{Name: "Broken", Argv: []string{"/bin/sh", "-c", "echo cannot open display >&2; exit 3"}}, nil
	}
	_, err := StartExternalDebuggee(context.Background(), DebuggeeShellSpec{SessionID: "s", Executable: "/bin/sh", Cwd: t.TempDir()}, "t")
	if ext := wantExternalError(t, err, ExternalFailed); !strings.Contains(ext.Error(), "cannot open display") {
		t.Fatalf("err = %v, want the terminal's error output", err)
	}
	openExternalTerminal = func(string) (externalTerminalLaunch, error) {
		return externalTerminalLaunch{}, &ExternalTerminalError{Code: ExternalUnavailable, Err: errors.New("no terminal")}
	}
	_, err = StartExternalDebuggee(context.Background(), DebuggeeShellSpec{SessionID: "s", Executable: "/bin/sh", Cwd: t.TempDir()}, "t")
	wantExternalError(t, err, ExternalUnavailable)
	if left := tempLeftovers(t); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestEndExternalProcessSparesAReusedPID(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "600")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	go func() { _ = cmd.Wait() }() // reaps it, so an ended process is gone, not a zombie
	identity, alive := processIdentity(cmd.Process.Pid)
	if !alive || identity == "" {
		t.Fatalf("identity = %q, alive %v", identity, alive)
	}
	// Another process now has this pid (its start time differs): left alone.
	endUnixProcess(cmd.Process.Pid, identity+"-other", 100*time.Millisecond)
	if !processAlive(cmd.Process.Pid) {
		t.Fatal("ended a process whose identity did not match")
	}
	endUnixProcess(cmd.Process.Pid, identity, 2*time.Second)
	waitDead(t, cmd.Process.Pid)
}

func TestEndExternalProcessForcesAfterGrace(t *testing.T) {
	// The program ignores SIGTERM: SIGKILL follows after the grace period.
	cmd := exec.Command("/bin/sh", "-c", "trap '' TERM; while :; do sleep 0.05; done")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	time.Sleep(100 * time.Millisecond) // let the trap be installed
	identity, _ := processIdentity(cmd.Process.Pid)
	endUnixProcess(cmd.Process.Pid, identity, 300*time.Millisecond)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process ignoring SIGTERM was not killed")
	}
}

// markerProgram is a program that creates marker when it runs, then sleeps.
func markerProgram(t *testing.T, marker string) DebuggeeShellSpec {
	t.Helper()
	return DebuggeeShellSpec{
		SessionID:  "s",
		Executable: "/bin/sh",
		Args:       []string{"-c", `: > "$1"; exec sleep 600`, "argv0", marker},
		Cwd:        t.TempDir(),
		Env:        map[string]string{"TOKEN": "secret"},
	}
}

func assertNeverRuns(t *testing.T, marker string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("the program ran although the host gave up on the launch")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartExternalDebuggeeLateScriptNeverRunsProgram(t *testing.T) {
	fakeExternalTerminal(t, true)
	copies := t.TempDir()
	done := make(chan struct{})
	// The "terminal" keeps a copy of the script and runs it only after the
	// host gave up (the script deletes itself, the host deletes its directory).
	openExternalTerminal = func(script string) (externalTerminalLaunch, error) {
		copyPath := filepath.Join(copies, "late.sh")
		go func() {
			defer close(done)
			for i := 0; i < 200; i++ {
				if data, err := os.ReadFile(script); err == nil {
					_ = os.WriteFile(copyPath, data, 0o700)
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(400 * time.Millisecond)
			_ = exec.Command("/bin/sh", copyPath).Run()
		}()
		return externalTerminalLaunch{Name: "Late", Argv: []string{"/bin/sh", "-c", "exit 0"}}, nil
	}
	prev := externalPIDTimeout
	externalPIDTimeout = 150 * time.Millisecond
	t.Cleanup(func() { externalPIDTimeout = prev })
	marker := filepath.Join(t.TempDir(), "ran")
	_, err := StartExternalDebuggee(context.Background(), markerProgram(t, marker), "t")
	wantExternalError(t, err, ExternalFailed)
	<-done
	assertNeverRuns(t, marker, 300*time.Millisecond)
	if left := tempLeftovers(t); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestStartExternalDebuggeeGivingUpAfterTheReportEndsTheLaunch(t *testing.T) {
	fakeExternalTerminal(t, true)
	marker := filepath.Join(t.TempDir(), "ran")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The session ends right as the script reports its pid.
	go func() {
		for ctx.Err() == nil {
			if found, _ := filepath.Glob(filepath.Join(os.TempDir(), externalDirPrefix+"*", externalReportDir, externalPIDFile)); len(found) > 0 {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	_, err := StartExternalDebuggee(ctx, markerProgram(t, marker), "t")
	if err == nil {
		t.Skip("the host acknowledged the launch before the cancellation (timing)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	assertNeverRuns(t, marker, 500*time.Millisecond)
}

func TestStartExternalDebuggeeProgramWaitsForItsIdentity(t *testing.T) {
	fakeExternalTerminal(t, true)
	marker := filepath.Join(t.TempDir(), "ran")
	checked := false
	externalBeforeAck = func(pid int) {
		checked = true
		time.Sleep(200 * time.Millisecond)
		if _, err := os.Stat(marker); err == nil {
			t.Error("the program ran before the host recorded its identity")
		}
		if _, alive := processIdentity(pid); !alive {
			t.Error("the reported process is gone before the ack")
		}
	}
	t.Cleanup(func() { externalBeforeAck = nil })
	d, err := StartExternalDebuggee(context.Background(), markerProgram(t, marker), "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.End)
	if !checked {
		t.Fatal("hook not called")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			d.End()
			waitDead(t, d.PID)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the program never ran after the ack")
}

func TestStartExternalDebuggeeEarlyScriptFailureFallsBackFast(t *testing.T) {
	fakeExternalTerminal(t, true)
	spec := markerProgram(t, filepath.Join(t.TempDir(), "ran"))
	// The working directory disappears between the checks and the script's cd.
	openExternalTerminal = func(script string) (externalTerminalLaunch, error) {
		_ = os.Remove(spec.Cwd)
		return externalTerminalLaunch{Name: "Fake", Argv: []string{"/bin/sh", script}}, nil
	}
	start := time.Now()
	_, err := StartExternalDebuggee(context.Background(), spec, "t")
	// The fake terminal is the script itself, so its exit may be seen first;
	// either way the failure is noticed at once, not after the pid timeout.
	wantExternalError(t, err, ExternalFailed)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("fell back after %v, want at once", time.Since(start))
	}
	if left := tempLeftovers(t); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestSweepExternalLaunchDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	old := time.Now().Add(-time.Hour)
	mk := func(name string, mtime time.Time, dir bool) string {
		path := filepath.Join(tmp, name)
		if dir {
			if err := os.MkdirAll(filepath.Join(path, "r"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "run.sh"), []byte("export TOKEN='x'"), 0o700); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return path
	}
	stale := mk(externalDirPrefix+"stale", old, true)
	fresh := mk(externalDirPrefix+"fresh", time.Now(), true)
	other := mk("someone-else", old, true)
	file := mk(externalDirPrefix+"file", old, false)
	SweepExternalLaunchDirs()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale launch directory kept: %v", err)
	}
	for _, kept := range []string{fresh, other, file} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s removed: %v", kept, err)
		}
	}
}

func TestStartExternalDebuggeeEarlyFailureMarkerWithLiveTerminal(t *testing.T) {
	fakeExternalTerminal(t, true)
	spec := markerProgram(t, filepath.Join(t.TempDir(), "ran"))
	// Like a real terminal window, the "terminal" outlives the failed script.
	openExternalTerminal = func(script string) (externalTerminalLaunch, error) {
		_ = os.Remove(spec.Cwd)
		return externalTerminalLaunch{Name: "Fake", Argv: []string{"/bin/sh", "-c", `/bin/sh "$1"; sleep 30`, "sh", script}}, nil
	}
	start := time.Now()
	_, err := StartExternalDebuggee(context.Background(), spec, "t")
	if ext := wantExternalError(t, err, ExternalFailed); !strings.Contains(ext.Error(), "stopped before") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("fell back after %v, want at once", time.Since(start))
	}
}
