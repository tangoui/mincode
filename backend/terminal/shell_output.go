package terminal

import (
	"time"

	"mincode/app/backend/core"
)

func (p *shellProcess) recordControlOutput(data string) {
	p.control.observeOutput(data, time.Now())
}

// shellOutputPayload 对齐 Rust 侧 "shell-output" 事件负载。
type shellOutputPayload struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"`
	// Offset is the session's total output bytes after Data; only sessions
	// that keep their output for attach_local_shell (debuggees) set it.
	Offset int64 `json:"offset,omitempty"`
}

// shellSessionEndedPayload 对齐 Rust 侧 "shell-session-ended" 事件负载。
type shellSessionEndedPayload struct {
	SessionID   string `json:"sessionId"`
	Reason      string `json:"reason"`
	Recoverable bool   `json:"recoverable"`
}

// shellSessionRecoveredPayload 对齐 Rust 侧 "shell-session-recovered" 事件负载。
type shellSessionRecoveredPayload struct {
	SessionID string `json:"sessionId"`
}

// dispatchShellOutput 优先经 Streams 数据面推送，没有 Streams 连接时退回
// core.Emit 走标准事件通道。
func dispatchShellOutput(sessionID, data string) {
	attention.observeOutput(sessionID, data)
	if streamHub.publishOutput(sessionID, data) {
		return
	}
	emitShellOutputEvent(sessionID, data, 0)
}

// dispatchLocalShellOutput is the output path used by the desktop terminal views.
// LocalTerminal listens to shell-output directly; a stale or independently
// opened stream connection must not consume its output. Explicit stream
// clients still receive frames, while the event remains available to the UI.
// offset (0 = none) is forwarded in the event; see shellOutputPayload.
func dispatchLocalShellOutput(sessionID, data string, offset int64) {
	attention.observeOutput(sessionID, data)
	streamHub.publishOutput(sessionID, data)
	emitShellOutputEvent(sessionID, data, offset)
}

// shellOutputEventPrefix 输出事件按会话命名（"shell-output:<sessionId>"），
// 前端每个终端只订阅自己的会话，避免 N 个终端各自收到并过滤全部输出。
const shellOutputEventPrefix = "shell-output:"

func shellOutputEventName(sessionID string) string {
	return shellOutputEventPrefix + sessionID
}

func emitShellOutputEvent(sessionID, data string, offset int64) {
	core.Emit(shellOutputEventName(sessionID), shellOutputPayload{SessionID: sessionID, Data: data, Offset: offset})
}

// emitShellSessionEnded 对齐 Rust 侧 emit_shell_session_ended：非可恢复时同时关闭
// Wire Bridge 会话。
func emitShellSessionEnded(sessionID, reason string, recoverable bool) {
	if !recoverable {
		attention.ended(sessionID)
	}
	core.Emit("shell-session-ended", shellSessionEndedPayload{
		SessionID:   sessionID,
		Reason:      reason,
		Recoverable: recoverable,
	})

	if recoverable {
		core.Notify(core.AppNotificationPayload{
			Source: "ssh",
			Level:  "warning",
			Code:   "terminal-session-interrupted",
		})
		return
	}

	// reason 是给终端状态机使用的内部信息，通知中心只发稳定的 i18n code，
	// 不把未来可能包含远端上下文的原因文本暴露给前端。
	core.Notify(core.AppNotificationPayload{
		Source: "ssh",
		Level:  "error",
		Code:   "terminal-session-ended",
	})
}

// emitShellCommandEnded 发送派生命令的正常结束事件，不把用户退出容器 shell
// 或停止日志流误报为 SSH 连接故障。
func emitShellCommandEnded(sessionID string) {
	attention.ended(sessionID)
	core.Emit("shell-session-ended", shellSessionEndedPayload{
		SessionID:   sessionID,
		Reason:      "command_exited",
		Recoverable: false,
	})
}

// emitShellSessionRecovered 对齐 Rust 侧 emit_shell_session_recovered。
func emitShellSessionRecovered(sessionID string) {
	core.Emit("shell-session-recovered", shellSessionRecoveredPayload{SessionID: sessionID})
	core.Notify(core.AppNotificationPayload{
		Source: "ssh",
		Level:  "success",
		Code:   "terminal-session-recovered",
	})
}
