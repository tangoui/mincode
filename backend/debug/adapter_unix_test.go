//go:build !windows

package debug

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// readChildPID waits for the forkexit fake adapter to record its child.
func readChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatalf("pid file: %v", err)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake adapter did not record its child pid")
	return 0
}

// waitGone waits until pid no longer exists (orphans are reaped by init).
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("adapter child %d still running after the session ended", pid)
}

func TestExitedAdapterChildrenKilled(t *testing.T) {
	for _, kind := range []string{"stdio", "tcp"} {
		t.Run(kind, func(t *testing.T) {
			rec := captureEmits(t)
			spec := fakeSpec(t, kind, "forkexit")
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			spec.Env[fakePIDFileEnv] = pidFile
			s, err := StartAdapter(testCtx(t), spec)
			if kind == "tcp" {
				if err == nil || !strings.Contains(err.Error(), "before accepting") {
					t.Fatalf("err = %v, want early exit", err)
				}
			} else {
				if err != nil {
					t.Fatalf("StartAdapter: %v", err)
				}
				rec.waitEnded(t, s.ID()) // child holds stdout open: closed by the exit watcher
			}
			waitGone(t, readChildPID(t, pidFile))
		})
	}
}
