package terminal

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Codes of ExternalTerminalError: why a debuggee could not run in an external
// terminal. The caller then runs it in the integrated terminal and says why.
const (
	// ExternalUnavailable: no terminal to open (no graphical session, none installed).
	ExternalUnavailable = "unavailable"
	// ExternalUnsupported: the launch has a value the terminal's shell cannot take safely.
	ExternalUnsupported = "unsupported"
	// ExternalFailed: the terminal did not start or never ran the program.
	ExternalFailed = "failed"
)

// Files of a launch: the private directory (0700, prefix externalDirPrefix)
// holds the script and the report directory; the script writes the program's
// pid (externalPIDFile) or, when it stopped before running the program,
// externalFailedFile into the report directory, and the program waits on the
// FIFO externalAckFile there until the host has recorded its identity.
const (
	externalDirPrefix  = "mincode-debuggee-"
	externalReportDir  = "r"
	externalPIDFile    = "pid"
	externalFailedFile = "failed"
	externalAckFile    = "go"
	// externalDetailMax bounds the explanation carried by an ExternalTerminalError.
	externalDetailMax = 512
)

// externalEndGrace is how long End waits after the polite request before it
// kills the program.
const externalEndGrace = 2 * time.Second

// ExternalTerminalError means the debuggee could not run in an external
// terminal for a reason the integrated terminal does not share (see the
// External* codes). Any other StartExternalDebuggee error refuses the launch
// itself, exactly as StartDebuggeeShell would.
type ExternalTerminalError struct {
	Code string
	Err  error
}

func (e *ExternalTerminalError) Error() string {
	return "external terminal: " + e.Detail()
}

// Detail is the explanation without the "external terminal: " prefix,
// at most externalDetailMax runes (terminal error output can be long).
func (e *ExternalTerminalError) Detail() string {
	return truncateRunes(e.Err.Error(), externalDetailMax)
}

func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit-1]) + "…"
}

func (e *ExternalTerminalError) Unwrap() error { return e.Err }

func externalError(code, format string, args ...any) *ExternalTerminalError {
	return &ExternalTerminalError{Code: code, Err: fmt.Errorf(format, args...)}
}

// ExternalDebuggee is a program running in a system terminal window.
type ExternalDebuggee struct {
	// PID is the program's process id; 0 when it could not be learned (a
	// Windows batch launcher), ShellPID is then set.
	PID      int
	ShellPID int
	// Terminal names the terminal application (shown to the user).
	Terminal string

	endOnce sync.Once
	end     func()
}

// End ends the program if it still runs: politely first, forcibly after a
// grace period. The window stays open with what the program printed. It
// returns once the program is gone (or was left alone because its pid now
// belongs to another process) and may be called more than once.
func (d *ExternalDebuggee) End() {
	d.endOnce.Do(func() {
		if d.end != nil {
			d.end()
		}
	})
}

// StartExternalDebuggee runs spec in a new system terminal window and returns
// the running program. spec is validated exactly like StartDebuggeeShell
// before anything is written or opened; such a refusal is a plain error.
// Reasons the external terminal cannot be used (none available, a value its
// shell cannot take, the window never ran the program) are
// *ExternalTerminalError. ctx cancels the wait for the program to start.
//
// On macOS and Linux a launch script in a private temporary directory (0700)
// sets the environment, changes to Cwd, reports its pid and execs the
// program; the script removes itself when it starts and the directory is
// removed once the pid is read or the wait ends. On Windows the program runs
// under cmd.exe in a new console; the environment and working directory are
// handed to the process directly, nothing is written to disk.
func StartExternalDebuggee(ctx context.Context, spec DebuggeeShellSpec, title string) (*ExternalDebuggee, error) {
	ps, err := checkDebuggeeSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("start debuggee: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start debuggee: %w", err)
	}
	return startExternalDebuggee(ctx, ps, title)
}

// checkDebuggeeSpec is the validation every debuggee launch passes, in the
// integrated or an external terminal.
func checkDebuggeeSpec(spec DebuggeeShellSpec) (ProfileShellSpec, error) {
	ps := ProfileShellSpec{
		SessionID:  spec.SessionID,
		Executable: spec.Executable,
		Args:       spec.Args,
		Env:        spec.Env,
		Cwd:        spec.Cwd,
		unsetEnv:   spec.UnsetEnv,
		debuggee:   true,
	}
	if err := validateShellSpec(ps, debuggeeExecutableAllowed); err != nil {
		return ps, err
	}
	for _, name := range spec.UnsetEnv {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return ps, fmt.Errorf("environment name %q is invalid", name)
		}
	}
	for _, value := range spec.Env {
		if strings.ContainsRune(value, 0) {
			return ps, errors.New("environment value contains NUL")
		}
	}
	if err := debuggeeArgsAllowed(ps); err != nil {
		return ps, err
	}
	return ps, nil
}

// externalTerminalLaunch is the command that opens a terminal running a
// launch script; Name is shown to the user.
type externalTerminalLaunch struct {
	Name string
	Argv []string
}

// scriptLaunch is what a POSIX sh launch script runs.
type scriptLaunch struct {
	Script string
	// Report is the report directory (pid, failed, the ack FIFO).
	Report  string
	Title   string
	Cwd     string
	Program string
	Args    []string
	Env     map[string]string
	Unset   []string
}

var shellName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// shQuote quotes s as one POSIX sh word: everything inside single quotes is
// literal (line breaks included); a single quote ends the quoting, is
// written escaped and the quoting resumes.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shReadonlyNames are variables that bash, which is /bin/sh on macOS, keeps
// read-only: assigning or unsetting one ends a non-interactive sh at once.
var shReadonlyNames = []string{"BASHOPTS", "BASH_VERSINFO", "EUID", "PPID", "SHELLOPTS", "UID"}

// shLaunchScript builds the launch script: it removes itself, titles the
// window, changes to the working directory, sets and removes environment
// variables with shell builtins (values never appear in a process's
// arguments), then runs the program in a child sh that writes its own pid
// (atomically, via rename), waits until the host has recorded the identity
// of that pid (a line on the ack FIFO) and only then execs the program, so
// the reported pid is the program's and cannot have been reused in between.
// A failed pid write (the host gave up and moved the report directory away)
// or a closed ack ends the launch before the program runs. If the script
// stops before reporting a pid (a failed `cd`, a shell error), its exit trap
// leaves a "failed" marker so the host falls back at once. After the program
// ends the window waits for Enter, so its last output stays readable.
//
// Names that are not sh identifiers, or that bash keeps read-only, cannot be
// set by a builtin: they are refused (ExternalUnsupported) rather than passed
// some other way.
func shLaunchScript(l scriptLaunch) (string, error) {
	for _, name := range slices.Concat(slices.Collect(maps.Keys(l.Env)), l.Unset) {
		if !shellName.MatchString(name) {
			return "", externalError(ExternalUnsupported, "environment name %q cannot be set by a POSIX shell", name)
		}
		if slices.Contains(shReadonlyNames, name) {
			return "", externalError(ExternalUnsupported, "environment variable %s is read-only in the system shell", name)
		}
	}
	report := shQuote(l.Report)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "rm -f -- %s\n", shQuote(l.Script))
	fmt.Fprintf(&b, "trap %s EXIT\n", shQuote(`[ -e `+report+`/pid ] || { : > `+report+`/failed; } 2>/dev/null`))
	if title := sanitizeTitle(l.Title); title != "" {
		fmt.Fprintf(&b, "printf '\\033]0;%%s\\007' %s\n", shQuote(title))
	}
	fmt.Fprintf(&b, "cd -- %s || exit 1\n", shQuote(l.Cwd))
	for _, name := range slices.Sorted(slices.Values(l.Unset)) {
		fmt.Fprintf(&b, "unset %s\n", name)
	}
	for _, name := range slices.Sorted(maps.Keys(l.Env)) {
		fmt.Fprintf(&b, "export %s=%s\n", name, shQuote(l.Env[name]))
	}
	words := []string{shQuote(l.Program)}
	for _, arg := range l.Args {
		words = append(words, shQuote(arg))
	}
	fmt.Fprintf(&b, "/bin/sh -c %s mincode-debuggee %s %s\n",
		shQuote(`printf '%s\n' "$$" > "$1/pid.tmp" && /bin/mv -f -- "$1/pid.tmp" "$1/pid" && read -r _ < "$1/go" || exit 127; shift; exec "$@"`),
		report, strings.Join(words, " "))
	b.WriteString("status=$?\n")
	b.WriteString(`printf '\n[MinCode] The program exited with code %s. Press Enter to close this window.\n' "$status"` + "\n")
	b.WriteString("read -r _ || true\n")
	return b.String(), nil
}

// sanitizeTitle drops control characters (C0, DEL, C1), so a title cannot
// carry terminal escape sequences.
func sanitizeTitle(title string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, title)
}

// linuxTerminals are probed in this order after $TERMINAL.
var linuxTerminals = []string{"x-terminal-emulator", "gnome-terminal", "konsole", "xfce4-terminal", "kitty", "alacritty", "xterm"}

// linuxTerminalArgs is how each terminal is told to run a command with
// arguments (as separate words, never a string the terminal would split).
func linuxTerminalArgs(name, script string) []string {
	switch name {
	case "gnome-terminal":
		return []string{"--", "/bin/sh", script}
	case "xfce4-terminal":
		return []string{"-x", "/bin/sh", script}
	case "kitty":
		return []string{"/bin/sh", script}
	case "x-terminal-emulator", "konsole", "alacritty", "xterm":
		return []string{"-e", "/bin/sh", script}
	default:
		// An unknown $TERMINAL: some take `-e` with one word, some with a
		// string they split; the executable script as one plain word means
		// the same to both (linuxTerminalLaunch checks its characters).
		return []string{"-e", script}
	}
}

// linuxKnownTerminal reports whether name has a known argument form.
func linuxKnownTerminal(name string) bool {
	return slices.Contains(linuxTerminals, name)
}

// plainPath matches a path no terminal would split or interpret.
var plainPath = regexp.MustCompile(`^[A-Za-z0-9/._-]+$`)

// linuxTerminalLaunch picks the terminal for script: $TERMINAL, then
// linuxTerminals, the first found by lookPath. Without DISPLAY or
// WAYLAND_DISPLAY there is no graphical session to open a window in.
func linuxTerminalLaunch(getenv func(string) string, lookPath func(string) (string, error), script string) (externalTerminalLaunch, error) {
	if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
		return externalTerminalLaunch{}, externalError(ExternalUnavailable, "no graphical session (neither DISPLAY nor WAYLAND_DISPLAY is set)")
	}
	candidates := linuxTerminals
	if preferred := strings.TrimSpace(getenv("TERMINAL")); preferred != "" {
		candidates = append([]string{preferred}, linuxTerminals...)
	}
	for _, candidate := range candidates {
		name := filepath.Base(candidate)
		if !linuxKnownTerminal(name) && !plainPath.MatchString(script) {
			continue // the one-word form needs a path without spaces or quotes
		}
		path, err := lookPath(candidate)
		if err != nil || path == "" {
			continue
		}
		return externalTerminalLaunch{Name: name, Argv: append([]string{path}, linuxTerminalArgs(name, script)...)}, nil
	}
	return externalTerminalLaunch{}, externalError(ExternalUnavailable, "no terminal emulator found ($TERMINAL, %s)", strings.Join(linuxTerminals, ", "))
}

// windowsConsoleCommandLine is the cmd.exe command line that runs program
// with args in a new console and then waits for a key (so the window keeps
// the program's last output): every word in plain double quotes (/s strips
// only the outer pair, /d skips AutoRun, /v:off keeps `!` literal). cmd.exe
// still interprets `"` (ends the quoting), `%` (expands variables) and line
// breaks inside quotes, and `!` under delayed expansion: values containing
// them are refused (ExternalUnsupported). A native program parses its
// arguments with CommandLineToArgvW, so a trailing backslash is doubled to
// keep it from escaping the closing quote; a batch launcher's are kept as is.
func windowsConsoleCommandLine(comspec, program string, args []string) (string, error) {
	batch := strings.HasSuffix(strings.ToLower(program), ".cmd") || strings.HasSuffix(strings.ToLower(program), ".bat")
	words := make([]string, 0, len(args)+1)
	for i, word := range slices.Concat([]string{program}, args) {
		if at := strings.IndexAny(word, "\"%!\r\n\x00"); at >= 0 {
			// Name the position and the character, never the value (it may be a secret).
			what := "the program path"
			if i > 0 {
				what = fmt.Sprintf("argument %d", i)
			}
			return "", externalError(ExternalUnsupported, "%s contains %s, which cmd.exe would interpret", what, cmdCharName(word[at]))
		}
		if !batch {
			trimmed := strings.TrimRight(word, `\`)
			word += strings.Repeat(`\`, len(word)-len(trimmed))
		}
		words = append(words, `"`+word+`"`)
	}
	return `"` + comspec + `" /d /v:off /s /c "` + strings.Join(words, " ") + ` & pause"`, nil
}

// cmdCharName names a character windowsConsoleCommandLine refuses.
func cmdCharName(c byte) string {
	switch c {
	case '\r', '\n':
		return "a line break"
	case 0:
		return "NUL"
	default:
		return "`" + string(c) + "`"
	}
}

// waitExternalPID waits for the launch script to report the program's pid
// in the report directory, at most timeout. terminalExited delivers the
// terminal command's exit: an error means the window never opened (a clean
// exit is normal for launchers that hand the window to a server). A
// "failed" marker means the script stopped before running the program.
// When it gives up it first renames the report directory (inside the
// private launch directory, where nobody else can create names), so a
// script that reports later can no longer write its pid and stops before
// running the program, then reads the pid once more: a pid returned with an
// error is a launch the caller must end (it waits for its ack, so the
// program has not run). The report directory is kept on success (the ack
// FIFO is in it) and removed when it gives up.
func waitExternalPID(ctx context.Context, report string, timeout time.Duration, terminalExited <-chan error) (int, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	giveUp := func(err error) (int, error) {
		aside := report + ".abandoned"
		if os.Rename(report, aside) != nil {
			aside = report
		}
		pid, _ := readExternalPID(aside)
		_ = os.RemoveAll(aside)
		return pid, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return giveUp(fmt.Errorf("external terminal: %w", err))
		}
		if pid, err := readExternalPID(report); err != nil {
			return giveUp(&ExternalTerminalError{Code: ExternalFailed, Err: err})
		} else if pid > 0 {
			return pid, nil
		}
		if _, err := os.Stat(filepath.Join(report, externalFailedFile)); err == nil {
			return giveUp(externalError(ExternalFailed, "the launch script stopped before running the program (see the terminal window)"))
		}
		select {
		case <-ctx.Done():
		case <-timer.C:
			return giveUp(externalError(ExternalFailed, "the program did not start within %s", timeout))
		case err := <-terminalExited:
			if err != nil {
				return giveUp(&ExternalTerminalError{Code: ExternalFailed, Err: err})
			}
			terminalExited = nil
		case <-ticker.C:
		}
	}
}

// readExternalPID reads the pid file in dir: 0 while it does not exist yet.
func readExternalPID(dir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(dir, externalPIDFile))
	if err != nil {
		return 0, nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("the launch script reported an invalid pid %q", strings.TrimSpace(string(data)))
	}
	return pid, nil
}

// windowsEnvBlock is env as a CreateProcess environment block: "k=v" entries
// sorted case-insensitively by name (as Windows expects), each NUL-ended, then
// a final NUL.
func windowsEnvBlock(env []string) []uint16 {
	sorted := slices.Clone(env)
	slices.SortStableFunc(sorted, func(a, b string) int {
		an, _, _ := strings.Cut(a, "=")
		bn, _, _ := strings.Cut(b, "=")
		return strings.Compare(strings.ToUpper(an), strings.ToUpper(bn))
	})
	var block []uint16
	for _, pair := range sorted {
		block = append(block, utf16.Encode([]rune(pair))...)
		block = append(block, 0)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	return append(block, 0)
}

// windowsProcess is a process snapshot entry (Windows).
type windowsProcess struct {
	pid, parent int
	name        string
}

// windowsConsoleHost reports whether name is the process hosting a console
// window: ending it would close the window.
func windowsConsoleHost(name string) bool {
	return strings.EqualFold(name, "conhost.exe") || strings.EqualFold(name, "OpenConsole.exe")
}

// windowsProgramPID picks the program among the launch's job members: a
// direct child of the shell named exe. Parent pids in a snapshot can be
// stale (a reused pid), so only job members count.
func windowsProgramPID(procs []windowsProcess, members []int, shellPID int, exe string) int {
	for _, p := range procs {
		if p.parent == shellPID && p.pid != shellPID && strings.EqualFold(p.name, exe) && slices.Contains(members, p.pid) {
			return p.pid
		}
	}
	return 0
}

// windowsEndTargets are the job members to end at the session end: every
// live member except the shell (which then reaches `pause`) and the console
// host, so the window stays open.
func windowsEndTargets(procs []windowsProcess, members []int, shellPID int) []int {
	var targets []int
	for _, p := range procs {
		if p.pid == shellPID || windowsConsoleHost(p.name) || !slices.Contains(members, p.pid) {
			continue
		}
		targets = append(targets, p.pid)
	}
	return targets
}
