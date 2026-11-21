package terminal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProfileShellSpec is a fully resolved launch of a plugin terminal profile.
// It is built only by the plugin host from its own contribution snapshot
// (backend/plugin/terminal_profiles_launch.go) after the user confirmed the
// launch fingerprint; the WebView never supplies Executable, Args, Env or Cwd.
//
// Unlike start_local_shell, a profile launch never falls back to the system
// shell, never adds shell-specific flags, and on Windows never goes through
// cmd.exe: the executable must be an absolute .exe/.com path and every argument
// is quoted with the CommandLineToArgvW rules.
type ProfileShellSpec struct {
	SessionID  string
	Executable string
	Args       []string
	Env        map[string]string
	Cwd        string
	Cols       *uint32
	Rows       *uint32
	// BeforeExec, when set, runs after the session slot is reserved and
	// immediately before the process is created; an error aborts the start.
	// The plugin host uses it to re-verify the identity of a ${pluginRoot}
	// executable it hashed, narrowing the check-to-exec window.
	BeforeExec func() error
	// OnExit, when set, runs once after a started session ended (process exit
	// or close_local_shell). It is never called when the start fails.
	OnExit func()

	// Set only by StartDebuggeeShell: inherited variables to remove, and
	// whether Windows .cmd/.bat launchers may run (through cmd.exe).
	unsetEnv []string
	debuggee bool
}

// StartProfileShell starts a new PTY session for a resolved plugin terminal
// profile. It never attaches to an existing session: callers attach with
// AttachLocalShell, which cannot start anything.
func StartProfileShell(spec ProfileShellSpec) (string, error) {
	if err := validateProfileShellSpec(spec); err != nil {
		return "", err
	}
	return runLocalShellSession(spec.SessionID, false, spec.Cols, spec.Rows, "start_profile_shell",
		func(cols, rows uint16) (localPTYHandle, string, error) {
			if spec.BeforeExec != nil {
				if err := spec.BeforeExec(); err != nil {
					return nil, "", err
				}
			}
			handle, err := startProfilePTY(spec, cols, rows)
			return handle, "", err
		}, spec.OnExit, nil)
}

// AttachLocalShell reports whether a live local session with this ID exists.
// It is the attach half of start_local_shell and never starts a process.
func AttachLocalShell(sessionID string) bool {
	proc, ok := sessions.get(strings.TrimSpace(sessionID))
	if !ok || proc == nil {
		return false
	}
	proc.mu.Lock()
	defer proc.mu.Unlock()
	return proc.localPTY != nil
}

func validateProfileShellSpec(spec ProfileShellSpec) error {
	return validateShellSpec(spec, profileExecutableAllowed)
}

// validateShellSpec checks a launch; allowed vets the executable's type.
func validateShellSpec(spec ProfileShellSpec, allowed func(string, os.FileInfo) error) error {
	if strings.TrimSpace(spec.SessionID) == "" {
		return errors.New("session_id 不能为空")
	}
	if !filepath.IsAbs(spec.Executable) {
		return fmt.Errorf("profile executable %q is not an absolute path", spec.Executable)
	}
	info, err := os.Stat(spec.Executable)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("profile executable %q is not a regular file", spec.Executable)
	}
	if err := allowed(spec.Executable, info); err != nil {
		return err
	}
	if !filepath.IsAbs(spec.Cwd) {
		return fmt.Errorf("profile working directory %q is not absolute", spec.Cwd)
	}
	if info, err := os.Stat(spec.Cwd); err != nil || !info.IsDir() {
		return fmt.Errorf("profile working directory %q does not exist", spec.Cwd)
	}
	for key := range spec.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("profile environment name %q is invalid", key)
		}
	}
	for _, arg := range spec.Args {
		if strings.ContainsRune(arg, 0) {
			return errors.New("profile argument contains NUL")
		}
	}
	return nil
}

// dedupeEnv keeps the last value of each variable; foldCase matches names
// case-insensitively (Windows).
func dedupeEnv(env []string, foldCase bool) []string {
	index := map[string]int{}
	out := make([]string, 0, len(env))
	for _, pair := range env {
		name, _, _ := strings.Cut(pair, "=")
		if foldCase {
			name = strings.ToUpper(name)
		}
		if at, seen := index[name]; seen {
			out[at] = pair
			continue
		}
		index[name] = len(out)
		out = append(out, pair)
	}
	return out
}

// withoutEnv drops the variables named in unset from env; foldCase matches
// names case-insensitively (Windows).
func withoutEnv(env, unset []string, foldCase bool) []string {
	if len(unset) == 0 {
		return env
	}
	drop := make(map[string]bool, len(unset))
	for _, name := range unset {
		if foldCase {
			name = strings.ToUpper(name)
		}
		drop[name] = true
	}
	out := env[:0:0]
	for _, pair := range env {
		name, _, _ := strings.Cut(pair, "=")
		if foldCase {
			name = strings.ToUpper(name)
		}
		if !drop[name] {
			out = append(out, pair)
		}
	}
	return out
}

func profileEnvPairs(env map[string]string) []string {
	pairs := make([]string, 0, len(env))
	for key, value := range env {
		pairs = append(pairs, key+"="+value)
	}
	return pairs
}
