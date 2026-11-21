//go:build !windows

package terminal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestUnixPTYReapsExitedChild(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	u := newUnixPTY(f, cmd)
	defer u.Close()
	pid := u.Pid()
	if pid <= 0 {
		t.Fatalf("unexpected pid %d", pid)
	}
	select {
	case <-u.waited:
	case <-time.After(5 * time.Second):
		t.Fatal("exited child was not reaped")
	}
	if u.Pid() != pid {
		t.Fatalf("pid changed after reap: %d", u.Pid())
	}
	// 已回收的进程不再以僵尸形式存在，kill(pid, 0) 应返回 ESRCH。
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("expected reaped child to be gone, kill(0) err=%v", err)
	}
	if err := u.Close(); err != nil {
		t.Fatalf("close after reap: %v", err)
	}
}

// Closing a terminal ends what runs in it: background jobs (an AI agent's
// background test runs), nohup jobs and programs that ignore the hangup.
func TestClosingLocalShellEndsItsProcesses(t *testing.T) {
	zsh := "/bin/zsh"
	if _, err := os.Stat(zsh); err != nil {
		t.Skip("zsh not installed")
	}
	custom := "custom"
	pidFile := filepath.Join(t.TempDir(), "pids")
	h, _, err := startLocalPTY(StartLocalShellPayload{ShellType: &custom, CustomPath: &zsh}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := h.Read(buf); err != nil {
				return
			}
		}
	}()
	time.Sleep(1500 * time.Millisecond)
	for _, line := range []string{
		"sleep 300 & echo $! >> " + pidFile,
		"nohup sleep 300 >/dev/null 2>&1 & echo $! >> " + pidFile,
		"(sleep 300 & echo $! >> " + pidFile + ")",
		"/bin/sh -c 'echo $$ >> " + pidFile + "; trap \"\" HUP; exec sleep 300'",
	} {
		if _, err := h.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	var pids []int
	deadline := time.Now().Add(5 * time.Second)
	for len(pids) < 4 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		data, _ := os.ReadFile(pidFile)
		pids = pids[:0]
		for _, field := range strings.Fields(string(data)) {
			if pid, err := strconv.Atoi(field); err == nil {
				pids = append(pids, pid)
			}
		}
	}
	if len(pids) < 4 {
		t.Fatalf("only %d of 4 processes started", len(pids))
	}
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	_ = h.Close()
	for _, pid := range pids {
		gone := false
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			if syscall.Kill(pid, 0) == syscall.ESRCH {
				gone = true
				break
			}
		}
		if !gone {
			t.Errorf("process %d survived closing its terminal", pid)
		}
	}
}
