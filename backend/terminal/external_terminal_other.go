//go:build !windows && !darwin && !linux

package terminal

import (
	"errors"
	"syscall"
)

const externalScriptName = "run.sh"

// platformExternalTerminal: no external terminal support on this system.
func platformExternalTerminal(string) (externalTerminalLaunch, error) {
	return externalTerminalLaunch{}, &ExternalTerminalError{Code: ExternalUnavailable, Err: errors.New("not supported on this operating system")}
}

// processIdentity only knows whether pid exists.
func processIdentity(pid int) (string, bool) {
	if syscall.Kill(pid, 0) != nil {
		return "", false
	}
	return "pid", true
}
