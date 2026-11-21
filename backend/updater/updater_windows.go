//go:build windows

package updater

import (
	"os"
	"os/exec"
)

// restartCurrentProcess 在 Windows 上启动一个新的进程副本后退出当前进程
// （Windows 无 exec 原地替换语义）。
func restartCurrentProcess() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(0)
		return
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	_ = cmd.Start()
	os.Exit(0)
}
