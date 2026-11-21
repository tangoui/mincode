package core

import (
	"context"
	"os/exec"
	"time"
)

const (
	// ShortCommandTimeout 适用于系统查询类一次性命令（lsof、ps、reg 等）。
	ShortCommandTimeout = 15 * time.Second
	// commandWaitDelay 限制超时后等待 stdout/stderr 管道关闭的时间，避免孙进程持有管道导致 Wait 永久阻塞。
	commandWaitDelay = 2 * time.Second
)

// CommandWithTimeout 创建一次性子进程命令，超时后自动终止。调用方必须在命令结束后调用返回的 cancel。
// 长生命周期进程（LSP、MCP、PTY、插件进程等）不应使用该函数。
func CommandWithTimeout(timeout time.Duration, name string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd := exec.CommandContext(ctx, name, args...)
	PrepareBackgroundCommand(cmd)
	cmd.WaitDelay = commandWaitDelay
	return cmd, cancel
}
