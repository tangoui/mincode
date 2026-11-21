//go:build windows

package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/UserExistsError/conpty"
)

// profileExecutableAllowed only admits native executables: .cmd/.bat would be
// interpreted by cmd.exe, whose metacharacter rules the argument quoting below
// cannot neutralise.
func profileExecutableAllowed(path string, _ os.FileInfo) error {
	lower := strings.ToLower(path)
	if !strings.HasSuffix(lower, ".exe") && !strings.HasSuffix(lower, ".com") {
		return fmt.Errorf("profile executable %q must be an .exe or .com file on Windows", path)
	}
	return nil
}

// debuggeeExecutableAllowed also admits .cmd/.bat launchers, which debug
// adapters commonly hand out; debuggeeArgsAllowed and batchCommandLine keep
// their arguments literal under cmd.exe.
func debuggeeExecutableAllowed(path string, info os.FileInfo) error {
	if isBatchFile(path) {
		return nil
	}
	return profileExecutableAllowed(path, info)
}

func isBatchFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat")
}

// debuggeeArgsAllowed rejects, for a batch launcher, the characters that
// cmd.exe would still interpret inside double quotes: `"` ends the quoting,
// `%` expands variables, and line breaks end the command.
func debuggeeArgsAllowed(spec ProfileShellSpec) error {
	if !isBatchFile(spec.Executable) {
		return nil
	}
	for _, s := range append([]string{spec.Executable}, spec.Args...) {
		if strings.ContainsAny(s, "\"%\r\n") {
			return fmt.Errorf("argument %q of batch launcher %s contains \", %% or a line break", s, filepath.Base(spec.Executable))
		}
	}
	return nil
}

// batchCommandLine runs a .cmd/.bat through cmd.exe with every word in plain
// double quotes (/s strips only the outer pair; /d skips AutoRun; /v:off
// keeps `!` literal).
func batchCommandLine(spec ProfileShellSpec) string {
	comspec := "cmd.exe"
	if root := os.Getenv("SystemRoot"); root != "" {
		comspec = filepath.Join(root, "System32", "cmd.exe")
	}
	words := make([]string, 0, len(spec.Args)+1)
	words = append(words, `"`+spec.Executable+`"`)
	for _, arg := range spec.Args {
		words = append(words, `"`+arg+`"`)
	}
	return syscall.EscapeArg(comspec) + ` /d /v:off /s /c "` + strings.Join(words, " ") + `"`
}

func startProfilePTY(spec ProfileShellSpec, cols, rows uint16) (localPTYHandle, error) {
	parts := make([]string, 0, len(spec.Args)+1)
	parts = append(parts, syscall.EscapeArg(spec.Executable))
	for _, arg := range spec.Args {
		parts = append(parts, syscall.EscapeArg(arg))
	}
	commandLine := strings.Join(parts, " ")
	if spec.debuggee && isBatchFile(spec.Executable) {
		commandLine = batchCommandLine(spec)
	}
	env := append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	env = withoutEnv(env, spec.unsetEnv, true)
	env = dedupeEnv(append(env, profileEnvPairs(spec.Env)...), true)
	cp, err := conpty.Start(commandLine,
		conpty.ConPtyDimensions(int(cols), int(rows)),
		conpty.ConPtyWorkDir(spec.Cwd),
		conpty.ConPtyEnv(env),
	)
	if err != nil {
		return nil, err
	}
	return &windowsPTY{cp: cp}, nil
}
