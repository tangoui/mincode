//go:build !windows

package core

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// PrepareBackgroundCommand 在非 Windows 平台无需调整控制台属性。
func PrepareBackgroundCommand(_ *exec.Cmd) {}

// PrepareProcessTree 让子进程成为独立进程组，便于超时或退出时终止其后代。
func PrepareProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// KillProcessTree 终止 cmd 所在的进程组。进程已退出视为成功。
func KillProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
