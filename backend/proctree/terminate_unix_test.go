//go:build !windows

package proctree

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func startProcess(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd
}

func listed(t *testing.T, pids ...int) []Process {
	t.Helper()
	procs, err := List()
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]bool{}
	for _, pid := range pids {
		want[pid] = true
	}
	var out []Process
	for _, p := range procs {
		if want[p.Pid] {
			out = append(out, p)
		}
	}
	if len(out) != len(pids) {
		t.Fatalf("listed %d of %d processes", len(out), len(pids))
	}
	return out
}

func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestTerminateHangsUpThenKillsWhatIgnoresIt(t *testing.T) {
	if !Supported() {
		t.Skip("unsupported")
	}
	polite := startProcess(t, "exec sleep 300")
	stubborn := startProcess(t, "trap '' HUP; while :; do sleep 1; done")
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	Terminate(listed(t, polite.Process.Pid, stubborn.Process.Pid), 300*time.Millisecond)
	if !waitGone(polite.Process.Pid, 2*time.Second) || !waitGone(stubborn.Process.Pid, 2*time.Second) {
		t.Fatal("a process survived Terminate")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Terminate took %v", time.Since(start))
	}
}

func TestTerminateSkipsAReusedPid(t *testing.T) {
	if !Supported() {
		t.Skip("unsupported")
	}
	cmd := startProcess(t, "trap '' HUP; while :; do sleep 1; done")
	time.Sleep(100 * time.Millisecond)
	stale := listed(t, cmd.Process.Pid)
	stale[0].Start = "another-instance"
	Terminate(stale, 50*time.Millisecond)
	if syscall.Kill(cmd.Process.Pid, 0) != nil {
		t.Fatal("a process that is not the listed instance was signalled")
	}
}
