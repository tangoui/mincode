//go:build windows

package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// PrepareBackgroundCommand 禁止后台命令创建独立控制台，同时保留调用方已有的进程属性。
// 不用于用户主动打开的 GUI 程序或由 ConPTY 托管的交互式终端。
func PrepareBackgroundCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW
}

// PrepareProcessTree 配置后台进程；Windows 上由 taskkill /T 负责终止后代。
func PrepareProcessTree(cmd *exec.Cmd) {
	PrepareBackgroundCommand(cmd)
}

// KillProcessTree 通过 taskkill /T 终止进程及其后代；失败时回退终止直接子进程。
func KillProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return nil
	}
	killCmd, cancel := CommandWithTimeout(ShortCommandTimeout, "taskkill", "/PID", fmt.Sprint(cmd.Process.Pid), "/T", "/F")
	defer cancel()
	treeErr := killCmd.Run()
	if treeErr == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("taskkill process tree: %v; kill process: %w", treeErr, err)
	}
	return nil
}
