//go:build !windows && !(darwin && cgo)

package terminal

import (
	"fmt"
	"os"
)

// nativeProcessCwd 读取 /proc/<pid>/cwd 符号链接（Linux 及挂载了 procfs 的系统）。
func nativeProcessCwd(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}
