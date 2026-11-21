//go:build !windows

package proctree

import (
	"syscall"
	"time"
)

// Terminate ends procs: SIGHUP first (what closing a terminal sends), then,
// after grace, SIGKILL to those still running. A process is signalled only
// while it is still the listed instance, so a pid reused meanwhile is left
// alone. It blocks for up to grace.
func Terminate(procs []Process, grace time.Duration) {
	signalCurrent(procs, syscall.SIGHUP)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if len(current(procs)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	signalCurrent(procs, syscall.SIGKILL)
}

func signalCurrent(procs []Process, signal syscall.Signal) {
	for _, p := range current(procs) {
		_ = syscall.Kill(p.Pid, signal)
	}
}

// current returns the procs that still run as the listed instances.
func current(procs []Process) []Process {
	if len(procs) == 0 {
		return nil
	}
	listed, err := List()
	if err != nil {
		return nil
	}
	live := make(map[int]string, len(listed))
	for _, p := range listed {
		live[p.Pid] = p.Start
	}
	out := make([]Process, 0, len(procs))
	for _, p := range procs {
		if start, ok := live[p.Pid]; ok && start == p.Start {
			out = append(out, p)
		}
	}
	return out
}
