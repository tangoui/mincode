//go:build !windows

package updater

import (
	"os"
	"syscall"
)

// restartCurrentProcess 以相同参数与环境原地替换进程映像重启应用。
// 正常情况下 syscall.Exec 不会返回；若失败则优雅退出。
func restartCurrentProcess() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(0)
		return
	}
	_ = syscall.Exec(exe, os.Args, os.Environ())
	os.Exit(0)
}
