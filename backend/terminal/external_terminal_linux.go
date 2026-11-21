//go:build linux

package terminal

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const externalScriptName = "run.sh"

// platformExternalTerminal picks $TERMINAL or the first known terminal
// emulator on PATH (linuxTerminalLaunch).
func platformExternalTerminal(script string) (externalTerminalLaunch, error) {
	return linuxTerminalLaunch(os.Getenv, exec.LookPath, script)
}

// processIdentity is pid's start time (field 22 of /proc/<pid>/stat); alive
// is false when pid does not exist or is a zombie.
func processIdentity(pid int) (identity string, alive bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	// The command name (field 2) may contain spaces and parentheses.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", false
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return "", false
	}
	return fields[19], true
}
