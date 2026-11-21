package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mincode/app/backend/core"
)

func init() {
	core.Register("shell_run_command", cmdRunShellCommand)
}

// AI 一次性命令执行的边界：默认 30 秒超时、单流输出最多保留 64KB，
// 避免长时间阻塞工具循环或把超大输出塞回模型上下文。
const (
	defaultRunCommandTimeout = 30 * time.Second
	maxRunCommandTimeout     = 120 * time.Second
	defaultRunOutputLimit    = 64 * 1024
	maxRunOutputLimit        = 512 * 1024
	runCommandWaitDelay      = 2 * time.Second
)

type truncatingWriter struct {
	limit     int
	truncated bool
	buf       bytes.Buffer
}

func (w *truncatingWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.limit {
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	if w.buf.Len() > w.limit {
		take := w.buf.Bytes()
		start := len(take) - w.limit
		// 从 UTF-8 续字节中间截断会破坏编码，向后移到字符边界。
		for start < len(take) && start > 0 && (take[start]&0xc0) == 0x80 {
			start++
		}
		w.buf.Reset()
		w.buf.Write(take[start:])
		w.truncated = true
	}
	return len(p), nil
}

func cmdRunShellCommand(_ string, argsJSON string) (string, error) {
	var payload RunShellCommandPayload
	if err := core.DecodeArgsPayload(argsJSON, &payload); err != nil {
		return "", err
	}
	result, err := runShellCommand(payload)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

// runShellCommand 一次性执行本地命令并捕获输出，供 AI 会话内执行脚本/命令使用；
// 不创建交互式 PTY，也不影响现有终端会话。
func runShellCommand(payload RunShellCommandPayload) (*RunShellCommandResult, error) {
	command := strings.TrimSpace(payload.Command)
	if command == "" {
		return nil, fmt.Errorf("shell_run_command: command 不能为空")
	}

	timeout := defaultRunCommandTimeout
	if payload.TimeoutMs != nil {
		requested := time.Duration(*payload.TimeoutMs) * time.Millisecond
		switch {
		case requested <= 0:
			return nil, fmt.Errorf("shell_run_command: timeoutMs 必须为正数")
		case requested > maxRunCommandTimeout:
			timeout = maxRunCommandTimeout
		default:
			timeout = requested
		}
	}

	limit := defaultRunOutputLimit
	if payload.OutputLimit != nil {
		switch {
		case *payload.OutputLimit <= 0:
			return nil, fmt.Errorf("shell_run_command: outputLimit 必须为正数")
		case *payload.OutputLimit > maxRunOutputLimit:
			limit = maxRunOutputLimit
		default:
			limit = *payload.OutputLimit
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shellRunProgram(), shellRunArgs(command)...)
	// 超时需结束整个进程树；后台孙进程持有输出管道时，WaitDelay 保证 Run 仍能返回。
	core.PrepareProcessTree(cmd)
	cmd.Cancel = func() error { return core.KillProcessTree(cmd) }
	cmd.WaitDelay = runCommandWaitDelay
	if payload.Cwd != nil {
		dir := strings.TrimSpace(*payload.Cwd)
		if dir != "" {
			if !filepath.IsAbs(dir) {
				return nil, fmt.Errorf("shell_run_command: cwd 必须为绝对路径: %s", dir)
			}
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				return nil, fmt.Errorf("shell_run_command: cwd 不是可用目录: %s", dir)
			}
			cmd.Dir = dir
		}
	}

	stdout := &truncatingWriter{limit: limit}
	stderr := &truncatingWriter{limit: limit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	started := time.Now()
	waitErr := cmd.Run()
	duration := uint64(time.Since(started).Milliseconds())

	result := &RunShellCommandResult{
		Stdout:          stdout.buf.String(),
		Stderr:          stderr.buf.String(),
		DurationMs:      duration,
		StdoutTruncated: stdout.truncated,
		StderrTruncated: stderr.truncated,
	}

	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.TimedOut = true
		result.ExitCode = -1
	case waitErr == nil, errors.Is(waitErr, exec.ErrWaitDelay):
		// ErrWaitDelay 仅在 shell 正常退出、但后台进程仍占用输出管道时返回。
		result.ExitCode = 0
	default:
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("shell_run_command: %w", waitErr)
		}
	}
	return result, nil
}
