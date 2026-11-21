package debug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"mincode/app/backend/core"
	"mincode/app/backend/terminal"
	"mincode/app/plugins/api"
)

// RunInTerminalEvent is the synthetic event emitted once a runInTerminal
// debuggee runs. In a PTY its body is {"terminalSessionId", "title"} and the
// frontend attaches to that terminal session (attach_local_shell) to show it
// in the Console; when an external terminal was asked for but could not be
// used, it also carries "externalFallback": {"code", "message"} (code is a
// terminal.External* code). In a system terminal window (kind "external") the
// body is {"external": true, "terminal": <application name>, "title"}.
const RunInTerminalEvent = "mincode.runInTerminal"

// externalProgram is a debuggee running in a system terminal window.
type externalProgram interface {
	// End ends the program if it still runs (politely, then forcibly) and
	// leaves the window open.
	End()
}

// externalStart is a started external debuggee.
type externalStart struct {
	PID, ShellPID int
	Terminal      string
	Program       externalProgram
}

// startExternal runs a validated debuggee in a system terminal window
// (terminal.StartExternalDebuggee); tests replace it.
var startExternal = func(ctx context.Context, spec terminal.DebuggeeShellSpec, title string) (externalStart, error) {
	d, err := terminal.StartExternalDebuggee(ctx, spec, title)
	if err != nil {
		return externalStart{}, err
	}
	return externalStart{PID: d.PID, ShellPID: d.ShellPID, Terminal: d.Terminal, Program: d}, nil
}

// debuggersPoint is the contribution point whose contract lists the
// environment names a debuggee may not receive.
const debuggersPoint = "debuggers"

// runInTerminal answers the session adapter's runInTerminal request: it
// starts args.Args in a project PTY (terminal session "debug-<id>-<n>"),
// announces it with RunInTerminalEvent and returns the program's pid. The
// cwd must be inside the project root (empty means the root) or be the
// launch request's own `cwd` outside it (the run configuration's working
// directory, which the user confirmed: see CwdOutsideProject); any other
// directory the adapter names is refused. A bare program
// name is resolved on the expanded PATH, and denied environment names are
// dropped (and logged) rather than failing the request. The PTY is closed
// when the session ends (see closeTerminals).
//
// Kind "external" runs the program in a system terminal window instead,
// after exactly the same checks; the program is ended (its window kept) when
// the session ends. When no external terminal can be used (none available,
// a value its shell cannot take, the window never ran the program) the
// program runs in the PTY and the event says why.
func (e *entry) runInTerminal(ctx context.Context, args RunInTerminalArgs) (RunInTerminalResult, error) {
	if len(args.Args) == 0 || args.Args[0] == "" {
		return RunInTerminalResult{}, errors.New("runInTerminal: args is empty")
	}
	cwd := e.info.ProjectRoot
	if args.Cwd != "" {
		dir, err := DirInProject(e.info.ProjectRoot, args.Cwd)
		if err != nil {
			if dir, err = e.launchCwdOutsideProject(args.Cwd, err); err != nil {
				return RunInTerminalResult{}, fmt.Errorf("runInTerminal: cwd: %w", err)
			}
		}
		cwd = dir
	}
	program, err := resolveProgram(args.Args[0], cwd)
	if err != nil {
		return RunInTerminalResult{}, fmt.Errorf("runInTerminal: %w", err)
	}
	env, unset, err := debuggeeEnv(e.info.ID, args.Env)
	if err != nil {
		return RunInTerminalResult{}, fmt.Errorf("runInTerminal: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return RunInTerminalResult{}, fmt.Errorf("runInTerminal: %w", err)
	}

	title := args.Title
	if title == "" {
		title = filepath.Base(args.Args[0])
	}
	spec := terminal.DebuggeeShellSpec{
		Executable: program,
		Args:       args.Args[1:],
		Env:        env,
		UnsetEnv:   unset,
		Cwd:        cwd,
	}

	var fallback *terminal.ExternalTerminalError
	if args.Kind == "external" {
		result, err := e.runInExternalTerminal(ctx, spec, title)
		if err == nil {
			return result, nil
		}
		if !errors.As(err, &fallback) {
			return RunInTerminalResult{}, fmt.Errorf("runInTerminal: %w", err)
		}
		fmt.Fprintf(os.Stderr, "[debug] session %s: runInTerminal: %v; running in the integrated terminal\n", e.info.ID, err)
	}

	id, ok := e.reserveTerminal()
	if !ok {
		return RunInTerminalResult{}, errors.New("runInTerminal: debug session ended")
	}
	spec.SessionID = id
	pid, err := terminal.StartDebuggeeShell(spec)
	if err != nil {
		e.releaseTerminal(id)
		if fallback != nil {
			return RunInTerminalResult{}, fmt.Errorf("runInTerminal: %v; integrated terminal: %w", fallback, err)
		}
		return RunInTerminalResult{}, fmt.Errorf("runInTerminal: %w", err)
	}
	// A session that ended during the start did not see this terminal yet.
	// It was never announced, so no Console will attach: drop it entirely.
	if !e.terminalStillWanted() {
		terminal.CloseLocalShell(id)
		return RunInTerminalResult{}, errors.New("runInTerminal: debug session ended")
	}

	body := map[string]any{"terminalSessionId": id, "title": title}
	if fallback != nil {
		body["externalFallback"] = map[string]string{"code": fallback.Code, "message": fallback.Detail()}
	}
	e.pushRunInTerminal(body)
	return RunInTerminalResult{ProcessID: pid}, nil
}

// runInExternalTerminal runs spec in a system terminal window and records it
// for closeTerminals. A *terminal.ExternalTerminalError means the integrated
// terminal should be used instead; any other error fails the request.
func (e *entry) runInExternalTerminal(ctx context.Context, spec terminal.DebuggeeShellSpec, title string) (RunInTerminalResult, error) {
	if !e.terminalStillWanted() {
		return RunInTerminalResult{}, errors.New("debug session ended")
	}
	// Only names the validation; no terminal session is created.
	spec.SessionID = fmt.Sprintf("debug-%s-external", e.info.ID)
	started, err := startExternal(ctx, spec, title)
	if err != nil {
		return RunInTerminalResult{}, err
	}
	if !e.addExternal(started.Program) {
		// The session ended during the start and will not end it: do it here.
		started.Program.End()
		return RunInTerminalResult{}, errors.New("debug session ended")
	}
	e.pushRunInTerminal(map[string]any{"external": true, "terminal": started.Terminal, "title": title})
	return RunInTerminalResult{ProcessID: started.PID, ShellProcessID: started.ShellPID}, nil
}

func (e *entry) pushRunInTerminal(body map[string]any) {
	raw, err := json.Marshal(body)
	if err == nil { // strings and bools always encode
		e.pump.push(Event{SessionID: e.info.ID, Event: RunInTerminalEvent, Body: raw})
	}
}

// launchCwdOutsideProject accepts cwd, refused by DirInProject with
// inProjectErr, when its real path (symbolic links resolved) is exactly the
// out-of-project directory the user confirmed (BeginParams.ConfirmedOutsideCwd,
// the real path judged at prepare time) and an existing directory; otherwise
// it returns inProjectErr. It never judges the directory again: one that was
// in the project at prepare (no confirmation) and now leads outside stays
// refused, and a confirmed link since pointed elsewhere no longer matches.
// The program runs in the confirmed real path, not through the link.
func (e *entry) launchCwdOutsideProject(cwd string, inProjectErr error) (string, error) {
	confirmed := ""
	if e.session != nil {
		confirmed = e.session.confirmedCwd()
	}
	if confirmed == "" || !filepath.IsAbs(cwd) {
		return "", inProjectErr
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		if filepath.Clean(cwd) == confirmed {
			return "", fmt.Errorf("not_found: %s is not a directory", confirmed)
		}
		return "", inProjectErr
	}
	if resolved != confirmed {
		return "", inProjectErr
	}
	if info, err := os.Stat(confirmed); err != nil || !info.IsDir() {
		return "", fmt.Errorf("not_found: %s is not a directory", confirmed)
	}
	return confirmed, nil
}

// CwdOutsideProject is the real path (symbolic links resolved; cleaned when it
// does not exist yet) of the launch `cwd` (DAP's common launch argument for
// the debuggee's working directory), when it is an absolute directory path
// outside projectRoot (lexically or through a symbolic link); "" otherwise
// (no cwd, a relative one, or one in the project). The plugin host judges it
// once at prepare, makes it part of the launch fingerprint (so a link
// re-pointed elsewhere needs a new confirmation) and passes it as
// BeginParams.ConfirmedOutsideCwd, which is all runInTerminal accepts.
func CwdOutsideProject(projectRoot, cwd string) string {
	if cwd == "" || strings.ContainsRune(cwd, 0) || !filepath.IsAbs(cwd) {
		return ""
	}
	outside := func(path string) bool {
		_, err := DirInProject(projectRoot, path)
		return err != nil && strings.HasPrefix(err.Error(), "forbidden:")
	}
	if !outside(cwd) {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return filepath.Clean(cwd)
	}
	// Spelled through a link to the project (e.g. /var for /private/var).
	if !outside(resolved) {
		return ""
	}
	return resolved
}

// LaunchArgumentsCwd is the string `cwd` of DAP launch arguments ("" when
// absent or not a string).
func LaunchArgumentsCwd(arguments json.RawMessage) string {
	var launch struct {
		Cwd any `json:"cwd"`
	}
	if json.Unmarshal(arguments, &launch) != nil {
		return ""
	}
	cwd, _ := launch.Cwd.(string)
	return cwd
}

// reserveTerminal names the next debuggee terminal and records it for
// closeTerminals; false once the session has ended.
func (e *entry) reserveTerminal() (string, bool) {
	e.termMu.Lock()
	defer e.termMu.Unlock()
	if e.termClosed {
		return "", false
	}
	e.termSeq++
	id := fmt.Sprintf("debug-%s-%d", e.info.ID, e.termSeq)
	e.terminals = append(e.terminals, id)
	return id, true
}

// addExternal records an external debuggee for closeTerminals; false once
// the session has ended.
func (e *entry) addExternal(p externalProgram) bool {
	e.termMu.Lock()
	defer e.termMu.Unlock()
	if e.termClosed {
		return false
	}
	e.externals = append(e.externals, p)
	return true
}

func (e *entry) releaseTerminal(id string) {
	e.termMu.Lock()
	defer e.termMu.Unlock()
	e.terminals = slices.DeleteFunc(e.terminals, func(t string) bool { return t == id })
}

// terminalStillWanted reports whether the session has not ended. Checked
// after a start: closeTerminals either ran before (then the caller closes the
// new terminal) or will close it.
func (e *entry) terminalStillWanted() bool {
	e.termMu.Lock()
	defer e.termMu.Unlock()
	return !e.termClosed
}

// closeTerminals ends every debuggee PTY of the session and refuses new
// ones. It runs on every session end path (from entry.closed). The programs
// are killed, but what they printed stays attachable (terminal.EndDebuggeeShell):
// the Console attaches only after the start returns, so a program that
// finished first would otherwise show an empty terminal. Programs in
// external terminal windows that still run are ended too (in parallel; it
// returns once they are gone), their windows stay open.
func (e *entry) closeTerminals() {
	e.termMu.Lock()
	e.termClosed = true
	ids := e.terminals
	e.terminals = nil
	externals := e.externals
	e.externals = nil
	e.termMu.Unlock()
	for _, id := range ids {
		terminal.EndDebuggeeShell(id)
	}
	var wg sync.WaitGroup
	for _, p := range externals {
		wg.Go(p.End)
	}
	wg.Wait()
}

// resolveProgram returns name as an absolute path: absolute names are kept,
// names with a path separator are taken relative to cwd, and a bare name is
// searched on the PATH of core.ExpandedEnv (on Windows a name without
// extension is tried with .exe, .cmd and .bat). The terminal package checks
// that the result is an executable file.
func resolveProgram(name, cwd string) (string, error) {
	if strings.ContainsRune(name, '\x00') {
		return "", errors.New("program name contains NUL")
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name), nil
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
		return filepath.Join(cwd, name), nil
	}
	exts := []string{""}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		exts = []string{".exe", ".cmd", ".bat"}
	}
	for _, dir := range filepath.SplitList(envValue(core.ExpandedEnv(), "PATH")) {
		if dir == "" || !filepath.IsAbs(dir) { // never search relative PATH entries
			continue
		}
		for _, ext := range exts {
			candidate := filepath.Join(dir, name+ext)
			if runnable(candidate) {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("program %q was not found on PATH", name)
}

// runnable reports whether path is a regular file that may be executed.
func runnable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}

// envValue returns the value of name in env (case-insensitive on Windows).
func envValue(env []string, name string) string {
	value := ""
	for _, pair := range env {
		k, v, ok := strings.Cut(pair, "=")
		if ok && (k == name || (runtime.GOOS == "windows" && strings.EqualFold(k, name))) {
			value = v
		}
	}
	return value
}

// debuggeeEnv splits the runInTerminal env into variables to set and to
// remove (null values). Names the `debuggers` contract denies (names and
// prefixes, case-insensitive) and malformed entries are dropped and logged
// to stderr by name only; they do not fail the request.
func debuggeeEnv(sessionID string, env map[string]*string) (map[string]string, []string, error) {
	spec, ok := api.ContributionPointSpecFor(debuggersPoint)
	if !ok {
		return nil, nil, errors.New("the debuggers contribution-point contract is missing")
	}
	denied := map[string]bool{}
	for _, name := range spec.List("deniedEnvNames") {
		denied[strings.ToUpper(name)] = true
	}
	prefixes := spec.List("deniedEnvPrefixes")
	isDenied := func(name string) bool {
		upper := strings.ToUpper(name)
		if denied[upper] {
			return true
		}
		for _, p := range prefixes {
			if strings.HasPrefix(upper, strings.ToUpper(p)) {
				return true
			}
		}
		return false
	}

	set := map[string]string{}
	var unset []string
	for _, name := range slices.Sorted(maps.Keys(env)) {
		value := env[name]
		switch {
		case name == "" || strings.ContainsAny(name, "=\x00"):
			fmt.Fprintf(os.Stderr, "[debug] session %s: runInTerminal: dropped malformed environment name %q\n", sessionID, name)
		case isDenied(name):
			fmt.Fprintf(os.Stderr, "[debug] session %s: runInTerminal: dropped denied environment variable %s\n", sessionID, name)
		case value == nil:
			unset = append(unset, name)
		case strings.ContainsRune(*value, '\x00'):
			fmt.Fprintf(os.Stderr, "[debug] session %s: runInTerminal: dropped environment variable %s (value contains NUL)\n", sessionID, name)
		default:
			set[name] = *value
		}
	}
	return set, unset, nil
}
