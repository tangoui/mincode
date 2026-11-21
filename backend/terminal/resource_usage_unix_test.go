//go:build !windows

package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/proctree"
)

func startUsageTestShell(t *testing.T, sessionID string) {
	t.Helper()
	if !proctree.Supported() {
		t.Skip("process listing unsupported")
	}
	core.SetEmitter(func(string, any) {})
	t.Cleanup(func() { core.SetEmitter(nil) })
	shell := "/bin/sh"
	if _, err := startLocalShell(StartLocalShellPayload{SessionID: sessionID, CustomPath: &shell, InitialArgs: []string{"-i"}}); err != nil {
		t.Skipf("local PTY unavailable: %v", err)
	}
	t.Cleanup(func() {
		if proc, ok := sessions.remove(sessionID); ok {
			shutdownShellProcess(proc)
		}
	})
}

// runInShell starts `sleep` in the background and returns its pid.
func runInShell(t *testing.T, sessionID string) int {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	if err := writeToShell(WriteToShellPayload{SessionID: sessionID, Input: "sleep 300 & echo $! > " + pidFile + "\r"}); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		data, _ := os.ReadFile(pidFile)
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			return pid
		}
	}
	t.Fatal("background process did not start")
	return 0
}

// processEnded reports whether pid has exited. An exited child the shell has
// not reaped yet still answers kill(pid, 0): dash, /bin/sh on Debian and
// Ubuntu, only reaps background jobs when it next reads a command, so a
// zombie also counts as ended.
func processEnded(pid int) bool {
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state follows the parenthesised command name: "pid (comm) S ...".
	i := strings.LastIndexByte(string(stat), ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}

func usageFor(t *testing.T, sessionID string) localShellUsage {
	t.Helper()
	out, err := cmdLocalShellResourceUsage("", `{"payload":{"sessionIds":["`+sessionID+`","unknown-session"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	var usages []localShellUsage
	if err := json.Unmarshal([]byte(out), &usages); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(usages) != 1 || usages[0].SessionID != sessionID {
		t.Fatalf("usages = %+v", usages)
	}
	return usages[0]
}

func TestLocalShellResourceUsageCountsTheSessionProcesses(t *testing.T) {
	const sessionID = "resource-usage-test"
	startUsageTestShell(t, sessionID)
	pid := runInShell(t, sessionID)
	usage := usageFor(t, sessionID)
	proc, _ := sessions.get(sessionID)
	if usage.Bytes == 0 || usage.ProcessCount < 2 || usage.ShellPid != proc.localPTY.Pid() {
		t.Fatalf("usage = %+v", usage)
	}
	found := false
	for _, p := range usage.Processes {
		found = found || p.Pid == pid
	}
	if !found {
		t.Fatalf("background process %d missing from %+v", pid, usage.Processes)
	}
}

func TestLocalShellEndProcessOnlyEndsProcessesOfTheSession(t *testing.T) {
	const sessionID = "end-process-test"
	startUsageTestShell(t, sessionID)
	pid := runInShell(t, sessionID)

	end := func(pid int) error {
		_, err := cmdLocalShellEndProcess("", `{"payload":{"sessionId":"`+sessionID+`","pid":`+strconv.Itoa(pid)+`}}`)
		return err
	}
	if err := end(os.Getpid()); err == nil {
		t.Fatal("ended a process outside the session")
	}
	proc, _ := sessions.get(sessionID)
	if err := end(proc.localPTY.Pid()); err == nil {
		t.Fatal("ended the session's shell")
	}
	if err := end(pid); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if processEnded(pid) {
			if syscall.Kill(proc.localPTY.Pid(), 0) != nil {
				t.Fatal("the shell was ended too")
			}
			return
		}
	}
	t.Fatal("process was not ended")
}
