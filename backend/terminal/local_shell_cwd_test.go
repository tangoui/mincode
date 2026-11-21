//go:build !windows

package terminal

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCwdFromPidReadsChildWorkingDirectory(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sleep", "30")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	native, err := nativeProcessCwd(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("nativeProcessCwd: %v", err)
	}
	if native != dir {
		t.Fatalf("nativeProcessCwd = %q, want %q", native, dir)
	}
	got, err := cwdFromPid(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("cwdFromPid = %q, want %q", got, dir)
	}
}

func TestNativeProcessCwdRejectsMissingProcess(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if cwd, err := nativeProcessCwd(cmd.Process.Pid); err == nil && cwd != "" {
		t.Fatalf("nativeProcessCwd for exited pid = %q, want error", cwd)
	}
}
