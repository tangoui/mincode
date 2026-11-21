//go:build !windows

package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"

	"mincode/app/backend/core"
)

func profileExecutableAllowed(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("profile executable %q is not executable", path)
	}
	return nil
}

// debuggeeExecutableAllowed is profileExecutableAllowed: any executable file.
func debuggeeExecutableAllowed(path string, info os.FileInfo) error {
	return profileExecutableAllowed(path, info)
}

// debuggeeArgsAllowed has nothing extra to check outside Windows.
func debuggeeArgsAllowed(ProfileShellSpec) error { return nil }

// startProfilePTY runs exactly spec.Executable with spec.Args. The base
// environment matches a user shell (inherited env, TERM/COLORTERM, login PATH,
// UTF-8 locale fallback); spec.Env was validated against the contract
// denylist by the plugin host and is applied last.
func startProfilePTY(spec ProfileShellSpec, cols, rows uint16) (localPTYHandle, error) {
	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	if loginPath := core.LoginShellPath(); loginPath != "" {
		cmd.Env = append(cmd.Env, "PATH="+loginPath)
	}
	if lang := utf8LangFallback(); lang != "" {
		cmd.Env = append(cmd.Env, "LANG="+lang)
	}
	cmd.Env = withoutEnv(cmd.Env, spec.unsetEnv, false)
	cmd.Env = append(cmd.Env, profileEnvPairs(spec.Env)...)
	cmd.Dir = spec.Cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, err
	}
	return newUnixPTY(f, cmd), nil
}
