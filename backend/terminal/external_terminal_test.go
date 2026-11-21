package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func wantExternalError(t *testing.T, err error, code string) *ExternalTerminalError {
	t.Helper()
	var ext *ExternalTerminalError
	if !errors.As(err, &ext) {
		t.Fatalf("err = %v (%T), want *ExternalTerminalError %s", err, err, code)
	}
	if ext.Code != code {
		t.Fatalf("code = %q, want %q (%v)", ext.Code, code, err)
	}
	return ext
}

func TestShLaunchScriptLayout(t *testing.T) {
	script, err := shLaunchScript(scriptLaunch{
		Script:  "/tmp/d/run.command",
		Report:  "/tmp/d/r",
		Title:   "my\x1b]0;evil\x07 app",
		Cwd:     "/work dir/it's",
		Program: "/usr/bin/python3",
		Args:    []string{"-m", "a b", "$HOME", "`id`", "it's", "line1\nline2", "中文"},
		Env:     map[string]string{"B_VAR": "x'y", "A_VAR": "$(rm -rf /)"},
		Unset:   []string{"DROP_ME"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(script, "\n")
	if lines[0] != "#!/bin/sh" || lines[1] != `rm -f -- '/tmp/d/run.command'` {
		t.Fatalf("the script must remove itself first:\n%s", script)
	}
	for _, want := range []string{
		`cd -- '/work dir/it'\''s' || exit 1`,
		`unset DROP_ME`,
		`export A_VAR='$(rm -rf /)'`,
		`export B_VAR='x'\''y'`,
		`'/usr/bin/python3' '-m' 'a b' '$HOME' '` + "`id`" + `' 'it'\''s' 'line1` + "\n" + `line2' '中文'`,
		`mincode-debuggee '/tmp/d/r' '/usr/bin/python3'`,
		`trap '[ -e '\''/tmp/d/r'\''/pid ] || { : > '\''/tmp/d/r'\''/failed; } 2>/dev/null' EXIT`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	// Control characters never reach the terminal title.
	if strings.ContainsAny(script, "\x1b\x07") {
		t.Errorf("script keeps control characters of the title:\n%q", script)
	}
	// Exports are sorted, so the script is deterministic.
	if strings.Index(script, "export A_VAR") > strings.Index(script, "export B_VAR") {
		t.Errorf("exports not sorted:\n%s", script)
	}
}

func TestShLaunchScriptRefusesNamesShellCannotSet(t *testing.T) {
	for _, l := range []scriptLaunch{
		{Env: map[string]string{"BAD-NAME": "x"}},
		{Env: map[string]string{"1ABC": "x"}},
		{Env: map[string]string{"A B": "x"}},
		{Unset: []string{"ODD.NAME"}},
		// bash (/bin/sh on macOS) keeps these read-only: the script would stop.
		{Env: map[string]string{"UID": "0"}},
		{Env: map[string]string{"EUID": "0"}},
		{Env: map[string]string{"PPID": "1"}},
		{Env: map[string]string{"SHELLOPTS": "x"}},
		{Env: map[string]string{"BASHOPTS": "x"}},
		{Unset: []string{"BASH_VERSINFO"}},
	} {
		l.Script, l.Report, l.Cwd, l.Program = "/t/run", "/t/r", "/", "/bin/true"
		_, err := shLaunchScript(l)
		wantExternalError(t, err, ExternalUnsupported)
	}
}

func TestSanitizeTitle(t *testing.T) {
	for in, want := range map[string]string{
		"plain":            "plain",
		"a\x1b[31mb\x07c":  "a[31mbc",
		"tab\there\nnext":  "tabherenext",
		"c1\u009bx \u7a0b": "c1x \u7a0b",
	} {
		if got := sanitizeTitle(in); got != want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinuxTerminalLaunchProbeOrderAndArgs(t *testing.T) {
	const script = "/tmp/mincode-debuggee-1/run.sh"
	order := []string{"x-terminal-emulator", "gnome-terminal", "konsole", "xfce4-terminal", "kitty", "alacritty", "xterm"}
	wantArgs := map[string][]string{
		"x-terminal-emulator": {"-e", "/bin/sh", script},
		"gnome-terminal":      {"--", "/bin/sh", script},
		"konsole":             {"-e", "/bin/sh", script},
		"xfce4-terminal":      {"-x", "/bin/sh", script},
		"kitty":               {"/bin/sh", script},
		"alacritty":           {"-e", "/bin/sh", script},
		"xterm":               {"-e", "/bin/sh", script},
	}
	env := map[string]string{"DISPLAY": ":0"}
	getenv := func(name string) string { return env[name] }
	// Each terminal is chosen when it and only later ones are installed.
	for i, name := range order {
		installed := map[string]bool{}
		for _, later := range order[i:] {
			installed[later] = true
		}
		lookPath := func(file string) (string, error) {
			if installed[file] {
				return "/usr/bin/" + file, nil
			}
			return "", errors.New("not found")
		}
		got, err := linuxTerminalLaunch(getenv, lookPath, script)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Name != name || got.Argv[0] != "/usr/bin/"+name || strings.Join(got.Argv[1:], "|") != strings.Join(wantArgs[name], "|") {
			t.Errorf("installed from %s: got %+v, want %s %v", name, got, name, wantArgs[name])
		}
	}

	// $TERMINAL comes first; a known name keeps its own argument form, an
	// unknown one gets -e with the script as one word (it is executable).
	all := func(file string) (string, error) { return "/opt/bin/" + filepath.Base(file), nil }
	env["TERMINAL"] = "kitty"
	if got, _ := linuxTerminalLaunch(getenv, all, script); got.Name != "kitty" || strings.Join(got.Argv[1:], "|") != "/bin/sh|"+script {
		t.Errorf("$TERMINAL=kitty: %+v", got)
	}
	env["TERMINAL"] = "/usr/local/bin/foot"
	if got, _ := linuxTerminalLaunch(getenv, all, script); got.Name != "foot" || strings.Join(got.Argv[1:], "|") != "-e|"+script {
		t.Errorf("$TERMINAL=foot: %+v", got)
	}
	// An unknown $TERMINAL is skipped when the script path has characters it might split.
	env["TERMINAL"] = "foot"
	if got, _ := linuxTerminalLaunch(getenv, all, "/tmp/my dir/run.sh"); got.Name != "x-terminal-emulator" || got.Argv[len(got.Argv)-1] != "/tmp/my dir/run.sh" {
		t.Errorf("unknown $TERMINAL with a spaced path: %+v", got)
	}
	env["TERMINAL"] = "/usr/local/bin/foot"
	// A $TERMINAL that is not installed is skipped.
	only := func(file string) (string, error) {
		if file == "xterm" {
			return "/usr/bin/xterm", nil
		}
		return "", errors.New("not found")
	}
	if got, _ := linuxTerminalLaunch(getenv, only, script); got.Name != "xterm" {
		t.Errorf("missing $TERMINAL: %+v", got)
	}
}

func TestLinuxTerminalLaunchUnavailable(t *testing.T) {
	all := func(file string) (string, error) { return "/usr/bin/" + file, nil }
	none := func(string) (string, error) { return "", errors.New("not found") }

	_, err := linuxTerminalLaunch(func(string) string { return "" }, all, "/t/run.sh")
	if ext := wantExternalError(t, err, ExternalUnavailable); !strings.Contains(ext.Error(), "DISPLAY") {
		t.Errorf("no graphical session: %v", err)
	}
	wayland := func(name string) string {
		if name == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}
	if _, err := linuxTerminalLaunch(wayland, all, "/t/run.sh"); err != nil {
		t.Errorf("Wayland session: %v", err)
	}
	_, err = linuxTerminalLaunch(wayland, none, "/t/run.sh")
	if ext := wantExternalError(t, err, ExternalUnavailable); !strings.Contains(ext.Error(), "xterm") {
		t.Errorf("no terminal: %v", err)
	}
}

func TestWindowsConsoleCommandLine(t *testing.T) {
	const comspec = `C:\Windows\System32\cmd.exe`
	got, err := windowsConsoleCommandLine(comspec, `C:\Program Files\Python\python.exe`,
		[]string{"-m", "a b", `C:\dir\`, `x\y`, "a&b|c<d>e^f(g)", "中文", ""})
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\Windows\System32\cmd.exe" /d /v:off /s /c ""C:\Program Files\Python\python.exe" "-m" "a b" "C:\dir\\" "x\y" "a&b|c<d>e^f(g)" "中文" "" & pause"`
	if got != want {
		t.Errorf("exe:\n got %s\nwant %s", got, want)
	}
	// A batch launcher's arguments are not parsed by CommandLineToArgvW: no doubling.
	got, err = windowsConsoleCommandLine(comspec, `C:\tools\run.cmd`, []string{`C:\dir\`})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"C:\Windows\System32\cmd.exe" /d /v:off /s /c ""C:\tools\run.cmd" "C:\dir\" & pause"`; got != want {
		t.Errorf("batch:\n got %s\nwant %s", got, want)
	}
}

func TestWindowsConsoleCommandLineRefusesWhatCmdInterprets(t *testing.T) {
	for _, bad := range []string{`say "hi"`, "100%", "%PATH%", "wow!", "a\rb", "a\nb", "a\x00b"} {
		_, err := windowsConsoleCommandLine(`C:\cmd.exe`, `C:\p.exe`, []string{"ok", "tok-" + bad + "-secret"})
		ext := wantExternalError(t, err, ExternalUnsupported)
		// The reason names the position, never the value (it may be a secret).
		if strings.Contains(ext.Error(), "secret") || !strings.Contains(ext.Error(), "argument 2") {
			t.Errorf("reason %q", ext.Error())
		}
		_, err = windowsConsoleCommandLine(`C:\cmd.exe`, `C:\dir`+bad+`\p.exe`, nil)
		wantExternalError(t, err, ExternalUnsupported)
	}
}

func writePIDFile(t *testing.T, dir string, pid string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, externalPIDFile), []byte(pid+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWaitExternalPID(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		dir := t.TempDir()
		go func() {
			time.Sleep(50 * time.Millisecond)
			writePIDFile(t, dir, "4242")
		}()
		pid, err := waitExternalPID(context.Background(), dir, 5*time.Second, nil)
		if err != nil || pid != 4242 {
			t.Fatalf("pid, err = %d, %v", pid, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "d")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		pid, err := waitExternalPID(context.Background(), dir, 100*time.Millisecond, nil)
		if pid != 0 {
			t.Fatalf("pid = %d", pid)
		}
		if ext := wantExternalError(t, err, ExternalFailed); !strings.Contains(ext.Error(), "did not start") {
			t.Errorf("timeout error: %v", err)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("timeout took %v", time.Since(start))
		}
		// The directory was moved aside, so a late script can no longer report into it.
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("directory still in place after the timeout: %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		dir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		pid, err := waitExternalPID(ctx, dir, time.Minute, nil)
		if pid != 0 || !errors.Is(err, context.Canceled) {
			t.Fatalf("pid, err = %d, %v; want context.Canceled", pid, err)
		}
	})
	t.Run("terminal failed", func(t *testing.T) {
		dir := t.TempDir()
		exited := make(chan error, 1)
		exited <- errors.New("exit status 1: no such terminal profile")
		_, err := waitExternalPID(context.Background(), dir, time.Minute, exited)
		if ext := wantExternalError(t, err, ExternalFailed); !strings.Contains(ext.Error(), "no such terminal profile") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("terminal exited cleanly keeps waiting", func(t *testing.T) {
		dir := t.TempDir()
		exited := make(chan error, 1)
		exited <- nil // e.g. `open` or gnome-terminal hand the window to a server
		go func() {
			time.Sleep(50 * time.Millisecond)
			writePIDFile(t, dir, "77")
		}()
		if pid, err := waitExternalPID(context.Background(), dir, 5*time.Second, exited); pid != 77 || err != nil {
			t.Fatalf("pid, err = %d, %v", pid, err)
		}
	})
	t.Run("reported while giving up", func(t *testing.T) {
		dir := t.TempDir()
		writePIDFile(t, dir, "99")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		// The pid is read once more after the directory is moved aside: the
		// caller learns about a program it must end.
		pid, err := waitExternalPID(ctx, dir, time.Minute, nil)
		if pid != 99 || !errors.Is(err, context.Canceled) {
			t.Fatalf("pid, err = %d, %v; want 99 with context.Canceled", pid, err)
		}
	})
	t.Run("script stopped early", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, externalFailedFile), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err := waitExternalPID(context.Background(), dir, time.Minute, nil)
		if ext := wantExternalError(t, err, ExternalFailed); !strings.Contains(ext.Error(), "stopped before") {
			t.Errorf("err = %v", err)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("an early script failure took %v to notice", time.Since(start))
		}
	})
	t.Run("garbage", func(t *testing.T) {
		dir := t.TempDir()
		writePIDFile(t, dir, "-3")
		if pid, err := waitExternalPID(context.Background(), dir, 100*time.Millisecond, nil); pid != 0 || err == nil {
			t.Fatalf("pid, err = %d, %v", pid, err)
		}
	})
}

func TestWindowsEnvBlock(t *testing.T) {
	got := windowsEnvBlock([]string{"b=2", "A=1", "中=文"})
	want := "A=1\x00b=2\x00中=文\x00\x00"
	if string(utf16.Decode(got)) != want {
		t.Errorf("block = %q, want %q", string(utf16.Decode(got)), want)
	}
	if got := windowsEnvBlock(nil); len(got) != 2 || got[0] != 0 || got[1] != 0 {
		t.Errorf("empty block = %v", got)
	}
}

func TestExternalTerminalErrorDetailIsBounded(t *testing.T) {
	long := strings.Repeat("x", 5000)
	err := &ExternalTerminalError{Code: ExternalFailed, Err: errors.New(long)}
	if n := len([]rune(err.Detail())); n > externalDetailMax {
		t.Fatalf("detail has %d runes", n)
	}
	if !strings.HasPrefix(err.Error(), "external terminal: ") || len([]rune(err.Error())) > externalDetailMax+len("external terminal: ") {
		t.Fatalf("error = %d runes", len([]rune(err.Error())))
	}
	if got := (&ExternalTerminalError{Err: errors.New("short")}).Detail(); got != "short" {
		t.Fatalf("detail = %q", got)
	}
}

func TestWindowsProgramPID(t *testing.T) {
	const shell = 100
	procs := []windowsProcess{
		{pid: shell, parent: 4, name: "cmd.exe"},
		{pid: 101, parent: shell, name: "conhost.exe"},
		// An unrelated process that inherited the shell's old pid as parent:
		// not in the job, never picked.
		{pid: 300, parent: shell, name: "python.exe"},
		{pid: 102, parent: shell, name: "Python.EXE"},
		{pid: 103, parent: 102, name: "python.exe"},
	}
	members := []int{shell, 101, 102, 103}
	if got := windowsProgramPID(procs, members, shell, "python.exe"); got != 102 {
		t.Fatalf("program = %d, want 102", got)
	}
	if got := windowsProgramPID(procs, []int{shell, 101}, shell, "python.exe"); got != 0 {
		t.Fatalf("program outside the job = %d, want 0", got)
	}
}

func TestWindowsEndTargets(t *testing.T) {
	const shell = 100
	procs := []windowsProcess{
		{pid: shell, parent: 4, name: "cmd.exe"},
		{pid: 101, parent: shell, name: "conhost.exe"},
		{pid: 105, parent: shell, name: "OpenConsole.exe"},
		{pid: 102, parent: shell, name: "python.exe"},
		{pid: 103, parent: 102, name: "worker.exe"},
		{pid: 300, parent: shell, name: "unrelated.exe"}, // stale parent pid, not in the job
	}
	// 104 left the job's snapshot already (exited): skipped.
	members := []int{shell, 101, 102, 103, 104, 105}
	got := windowsEndTargets(procs, members, shell)
	if strings.Trim(strings.Join(strings.Fields(fmt.Sprint(got)), ","), "[]") != "102,103" {
		t.Fatalf("targets = %v, want [102 103] (never the shell, the console host or a process outside the job)", got)
	}
	// The shell already exited: what is left of the job is still only ours.
	if got := windowsEndTargets(procs[3:5], []int{102, 103}, shell); len(got) != 2 {
		t.Fatalf("targets after the shell exited = %v", got)
	}
	if got := windowsEndTargets(procs, []int{shell, 101}, shell); len(got) != 0 {
		t.Fatalf("nothing to end, got %v", got)
	}
}
