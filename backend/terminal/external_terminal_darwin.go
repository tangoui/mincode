//go:build darwin

package terminal

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// externalScriptName: Terminal runs a .command file it is asked to open.
const externalScriptName = "run.command"

// platformExternalTerminal opens the script with Terminal through
// LaunchServices (`open -a Terminal`). Unlike AppleScript (`osascript … do
// script`) it needs no Automation permission prompt, and the arguments are
// passed as words, never as script source.
func platformExternalTerminal(script string) (externalTerminalLaunch, error) {
	return externalTerminalLaunch{Name: "Terminal", Argv: []string{"/usr/bin/open", "-a", "Terminal", script}}, nil
}

// processIdentity is pid's start time; alive is false when pid does not
// exist or is a zombie.
func processIdentity(pid int) (identity string, alive bool) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || k == nil || int(k.Proc.P_pid) != pid {
		return "", false
	}
	const szomb = 5 // sys/proc.h SZOMB
	if k.Proc.P_stat == szomb {
		return "", false
	}
	return fmt.Sprintf("%d.%d", k.Proc.P_starttime.Sec, k.Proc.P_starttime.Usec), true
}
