package terminal

import (
	"fmt"
	"strings"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/proctree"
)

func init() {
	core.Register("local_shell_resource_usage", cmdLocalShellResourceUsage)
	core.Register("local_shell_end_process", cmdLocalShellEndProcess)
}

// usageTopProcesses bounds the processes listed per session (largest first).
const usageTopProcesses = 8

// endProcessGrace is how long an ended process gets after the hangup before it is killed.
const endProcessGrace = 2 * time.Second

// LocalShellResourceUsagePayload asks for the memory of local terminal sessions.
type LocalShellResourceUsagePayload struct {
	SessionIDs []string `json:"sessionIds"`
}

// localShellUsage is the memory of one local terminal session: the shell and
// everything started in it.
type localShellUsage struct {
	SessionID    string                  `json:"sessionId"`
	ShellPid     int                     `json:"shellPid"`
	Bytes        uint64                  `json:"bytes"`
	ProcessCount int                     `json:"processCount"`
	Processes    []proctree.ProcessUsage `json:"processes"`
}

// LocalShellEndProcessPayload ends one process (and what it started) of a local terminal session.
type LocalShellEndProcessPayload struct {
	SessionID string `json:"sessionId"`
	Pid       int    `json:"pid"`
}

// sessionProcessLister is implemented by local PTYs that can list their session's processes.
type sessionProcessLister interface {
	processesIn(procs []proctree.Process) []proctree.Process
}

// sessionProcesses returns the processes of the live local session id from
// procs, and its shell's pid; ok is false for an unknown session or one whose
// processes cannot be listed (Windows, SSH).
func sessionProcesses(id string, procs []proctree.Process) (members []proctree.Process, shellPid int, ok bool) {
	proc, found := sessions.get(id)
	if !found {
		return nil, 0, false
	}
	proc.mu.Lock()
	handle := proc.localPTY
	proc.mu.Unlock()
	lister, can := handle.(sessionProcessLister)
	if handle == nil || !can {
		return nil, 0, false
	}
	return lister.processesIn(procs), handle.Pid(), true
}

func cmdLocalShellResourceUsage(_, argsJSON string) (string, error) {
	var payload LocalShellResourceUsagePayload
	if err := core.DecodeArgsPayload(argsJSON, &payload); err != nil {
		return "", err
	}
	usages := []localShellUsage{}
	if !proctree.Supported() || len(payload.SessionIDs) == 0 {
		return core.EncodeResult(usages)
	}
	procs, err := proctree.List()
	if err != nil {
		return "", err
	}
	for _, id := range payload.SessionIDs {
		members, shellPid, ok := sessionProcesses(strings.TrimSpace(id), procs)
		if !ok {
			continue
		}
		usage := proctree.Measure(members)
		top := usage.Processes
		if len(top) > usageTopProcesses {
			top = top[:usageTopProcesses]
		}
		usages = append(usages, localShellUsage{SessionID: id, ShellPid: shellPid, Bytes: usage.Bytes, ProcessCount: len(members), Processes: top})
	}
	return core.EncodeResult(usages)
}

func cmdLocalShellEndProcess(_, argsJSON string) (string, error) {
	var payload LocalShellEndProcessPayload
	if err := core.DecodeArgsPayload(argsJSON, &payload); err != nil {
		return "", err
	}
	if !proctree.Supported() {
		return "", fmt.Errorf("unsupported: process listing is not available on this platform")
	}
	procs, err := proctree.List()
	if err != nil {
		return "", err
	}
	members, shellPid, ok := sessionProcesses(strings.TrimSpace(payload.SessionID), procs)
	if !ok {
		return "", fmt.Errorf("not_found: terminal session %q", payload.SessionID)
	}
	// The shell is ended by closing its terminal.
	if payload.Pid == shellPid {
		return "", fmt.Errorf("invalid_request: the terminal's shell is ended by closing the terminal")
	}
	inSession := false
	for _, p := range members {
		inSession = inSession || p.Pid == payload.Pid
	}
	if !inSession {
		return "", fmt.Errorf("not_found: process %d is not part of this terminal", payload.Pid)
	}
	// The process with everything it started, but never the shell.
	var targets []proctree.Process
	for _, p := range proctree.Members(procs, payload.Pid, proctree.NoTTY) {
		if p.Pid != shellPid {
			targets = append(targets, p)
		}
	}
	go proctree.Terminate(targets, endProcessGrace)
	return core.EncodeResult(nil)
}
