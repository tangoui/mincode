package terminal

import (
	"fmt"
	"io"
	"os"
	"strings"

	"mincode/app/backend/agentcontrol"
	"mincode/app/backend/core"
)

func init() {
	core.Register("start_local_shell", cmdStartLocalShell)
	core.Register("write_to_shell", cmdWriteToShell)
	core.Register("resize_local_shell", cmdResizeLocalShell)
	core.Register("close_local_shell", cmdCloseLocalShell)
	core.Register("get_local_shell_cwd", cmdGetLocalShellCwd)
}

func cmdStartLocalShell(_, argsJSON string) (string, error) {
	var payload StartLocalShellPayload
	if err := core.DecodeArgsPayload(argsJSON, &payload); err != nil {
		return "", err
	}
	msg, err := startLocalShell(payload)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(msg)
}

func cmdWriteToShell(_, argsJSON string) (string, error) {
	var payload WriteToShellPayload
	if err := core.DecodeArgsPayload(argsJSON, &payload); err != nil {
		return "", err
	}
	if err := writeToShell(payload); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func cmdResizeLocalShell(_, argsJSON string) (string, error) {
	var payload ResizeLocalShellPayload
	if err := core.DecodeArgsPayload(argsJSON, &payload); err != nil {
		return "", err
	}
	if err := applyLocalShellResize(payload.SessionID, uint16(payload.Cols), uint16(payload.Rows)); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func cmdCloseLocalShell(_, argsJSON string) (string, error) {
	var payload CloseShellPayload
	if err := core.DecodeArgsPayload(argsJSON, &payload); err != nil {
		return "", err
	}
	sessionID := strings.TrimSpace(payload.SessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session_id 不能为空")
	}
	CloseLocalShell(sessionID)
	return core.EncodeResult(nil)
}

func cmdGetLocalShellCwd(_, argsJSON string) (string, error) {
	var payload GetLocalShellCwdPayload
	if err := core.DecodeArgsPayload(argsJSON, &payload); err != nil {
		return "", err
	}
	sid := strings.TrimSpace(payload.SessionID)
	if sid == "" {
		return "", fmt.Errorf("session_id 不能为空")
	}
	proc, ok := sessions.get(sid)
	if !ok {
		return "", fmt.Errorf("本地 shell 会话不存在或已关闭")
	}
	proc.mu.Lock()
	pty := proc.localPTY
	proc.mu.Unlock()
	if pty == nil {
		return "", fmt.Errorf("子进程未就绪")
	}
	cwd, err := cwdFromPid(pty.Pid())
	if err != nil {
		return "", err
	}
	return core.EncodeResult(cwd)
}

// applyLocalShellResize 对齐 Rust 侧 apply_local_shell_resize。
func applyLocalShellResize(sessionID string, cols, rows uint16) error {
	c32, r32 := uint32(cols), uint32(rows)
	cols, rows = clampPtyColsRows(&c32, &r32)
	proc, ok := sessions.get(strings.TrimSpace(sessionID))
	if !ok {
		return fmt.Errorf("未知会话: %s", sessionID)
	}
	proc.mu.Lock()
	pty := proc.localPTY
	resizeTx := proc.sshResizeTx
	proc.mu.Unlock()
	if resizeTx != nil {
		select {
		case resizeTx <- [2]uint16{cols, rows}:
		default:
		}
		return nil
	}
	if pty == nil {
		return fmt.Errorf("PTY 已关闭")
	}
	return pty.Resize(cols, rows)
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func enqueueShellInput(proc *shellProcess, input string) error {
	proc.mu.Lock()
	tx := proc.stdinTx
	done := proc.stdinDone
	proc.mu.Unlock()
	if tx == nil || done == nil {
		return fmt.Errorf("Shell not running")
	}

	select {
	case tx <- input:
		return nil
	case <-done:
		return fmt.Errorf("Shell not running")
	}
}

// 本地 PTY 直接串行写入：IPC 调用成功即表示字节确实已写入，而非仅已入队。
func writeLocalShellInput(proc *shellProcess, input string) error {
	proc.inputMu.Lock()
	defer proc.inputMu.Unlock()
	proc.mu.Lock()
	pty := proc.localPTY
	done := proc.stdinDone
	proc.mu.Unlock()
	if pty == nil || done == nil {
		return fmt.Errorf("Shell not running")
	}
	select {
	case <-done:
		return fmt.Errorf("Shell not running")
	default:
	}
	if err := writeAll(pty, []byte(input)); err != nil {
		return fmt.Errorf("write to local PTY: %w", err)
	}
	return nil
}

// writeToShell 对本地 PTY 等待完整写入；SSH 仍通过其 relay 输入队列提交。
func writeToShell(payload WriteToShellPayload) error {
	proc, ok := sessions.get(payload.SessionID)
	if !ok {
		return agentcontrol.ErrPaneNotFound
	}
	attention.observeInput(payload.SessionID, payload.Input)
	proc.control.observeInput(payload.Input)
	proc.mu.Lock()
	local := proc.localPTY != nil
	proc.mu.Unlock()
	if local {
		return writeLocalShellInput(proc, payload.Input)
	}
	captureSSHInput(proc, payload.Input)
	return enqueueShellInput(proc, payload.Input)
}

// startLocalShell 对齐 Rust 侧 start_local_shell：创建/复用本地 PTY 会话，起读写
// goroutine 通过 OutputBatch 合批推送 "shell-output" 事件。
func startLocalShell(payload StartLocalShellPayload) (string, error) {
	payload.SessionID = strings.TrimSpace(payload.SessionID)
	cwd := strings.TrimSpace(derefStr(payload.Cwd))
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		cwd, _ = os.UserHomeDir()
	}
	agent := ""
	if payload.ExtraEnv["MINCODE_CLI_TAB_TOKEN"] != "" {
		agent = "claude"
	}
	info := PaneInfo{CWD: cwd, Agent: agent}
	if len(payload.InitialArgs) > 0 {
		info.State = "working"
	}
	return runLocalShellSession(payload.SessionID, payload.AttachIfExists, payload.Cols, payload.Rows, "start_local_shell",
		func(cols, rows uint16) (localPTYHandle, string, error) {
			return startLocalPTY(payload, cols, rows)
		}, nil, nil, info)
}

// runLocalShellSession owns the session lifecycle shared by user shells
// (start_local_shell) and plugin terminal profiles (StartProfileShell): attach
// or start, publish the session, and pump PTY output/input. start receives the
// clamped size and returns the PTY plus an optional "fell back from" label.
// onExit, when set, runs once after a started session's output pump ends
// (process exit or close_local_shell); it is not called when start fails.
// output, when set, keeps the session's output for attach_local_shell (also
// after the process exits, until CloseLocalShell), marks output events with
// their offset and reports the end as "command_exited" (debuggee sessions).
func runLocalShellSession(rawSessionID string, attachIfExists bool, rawCols, rawRows *uint32, logLabel string,
	start func(cols, rows uint16) (localPTYHandle, string, error), onExit func(), output *outputLog, metadata ...PaneInfo) (string, error) {
	sessionID := strings.TrimSpace(rawSessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session_id 不能为空")
	}

	for {
		wait, owner, existing := sessions.beginStart(sessionID, attachIfExists)
		if existing != nil {
			return "Shell attached", nil
		}
		if !owner {
			<-wait
			continue
		}
		break
	}

	cols, rows := clampPtyColsRows(rawCols, rawRows)
	handle, fallbackFrom, err := start(cols, rows)
	if err != nil {
		sessions.cancelStart(sessionID)
		return "", err
	}

	stdinTx := make(chan string, 64)
	stdinDone := make(chan struct{})
	proc := &shellProcess{
		localPTY:     handle,
		stdinTx:      stdinTx,
		stdinDone:    stdinDone,
		sshLinkState: sshLinkActive,
		output:       output,
	}
	if len(metadata) > 0 {
		info := metadata[0]
		proc.control = addControlledPane(sessionID, info.Title, info.CWD, info.Agent == "claude", handle)
		if info.State == "working" {
			proc.control.observeInput("\r")
		}
	}
	old := sessions.publishStart(sessionID, proc)
	if old != nil {
		shutdownShellProcess(old)
	}
	sessions.finishStart(sessionID)

	// 读 goroutine：阻塞 read -> UTF-8 边界处理 -> 投递到 outCh。
	outCh := make(chan string, 256)
	go func() {
		buf := make([]byte, ptyReadBufferSize)
		var carry []byte
		for {
			n, err := handle.Read(buf)
			if n > 0 {
				data := ptyPushAndTakeUTF8(&carry, buf[:n])
				if data != "" {
					outCh <- data
				}
			}
			if err != nil {
				if len(carry) > 0 {
					tail := string(carry)
					carry = nil
					if tail != "" {
						outCh <- tail
					}
				}
				break
			}
		}
		close(outCh)
	}()

	// 转发 goroutine：空闲后首块立即 emit "shell-output"，持续输出按时间窗合批。
	go func() {
		defer func() {
			if old, ok := sessions.retireIfSame(sessionID, proc); ok {
				shutdownShellProcess(old)
			}
			if onExit != nil {
				onExit()
			}
		}()
		coalesceOutput(outCh, nil, (*outputBatch).push, func(data string) {
			proc.recordControlOutput(data)
			emitLoggedOutput(sessionID, data, output)
		}, outputCoalesceWindow)
		if proc.control != nil {
			proc.control.end()
		}
		if output != nil {
			emitShellCommandEnded(sessionID)
			return
		}
		emitShellSessionEnded(sessionID, "pty_closed", false)
	}()

	// Wire/Stream 和内部写入仍使用 stdinTx；与直接的 IPC 写入共用同一串行锁。
	go func() {
		for {
			select {
			case <-stdinDone:
				return
			case input := <-stdinTx:
				if err := writeLocalShellInput(proc, input); err != nil {
					if old, ok := sessions.removeIfSame(sessionID, proc); ok {
						shutdownShellProcess(old)
					}
					return
				}
			}
		}
	}()

	fmt.Printf("[app] %s session=%s pid=%d cols=%d rows=%d\n", logLabel, sessionID, handle.Pid(), cols, rows)
	message := fmt.Sprintf("Shell ready (PID: %d)", handle.Pid())
	if fallbackFrom != "" {
		message += fmt.Sprintf("; %s unavailable, using system shell", fallbackFrom)
	}
	return message, nil
}
