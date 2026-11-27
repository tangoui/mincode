package terminal

// IPC 流版终端数据面，经资产服务器承载：不占 TCP 端口、无需 token 认证
// （同应用内通道）。帧格式见 wire_frame.go。输出分发优先级：
// streamHub（Streams 连接）→ core.Emit 事件。

import (
	"strings"
	"sync"
	"sync/atomic"

	"mincode/app/backend/core"
)

type wireStreamSession struct {
	mu       sync.Mutex
	conns    map[core.StreamConn]struct{}
	handlers wireInputHandlers
	active   atomic.Bool
}

func (s *wireStreamSession) add(conn core.StreamConn) {
	s.mu.Lock()
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
	s.active.Store(true)
}

// remove 摘除连接，返回会话是否已无任何连接。
func (s *wireStreamSession) remove(conn core.StreamConn) bool {
	s.mu.Lock()
	delete(s.conns, conn)
	empty := len(s.conns) == 0
	s.mu.Unlock()
	if empty {
		s.active.Store(false)
	}
	return empty
}

// publish 向所有连接广播帧。只要任一连接成功接收就视为已送达；全部失败时
// 返回 false，让 dispatchShellOutput 回退到 shell-output:<id> 事件，不能静默丢失回显。
func (s *wireStreamSession) publish(frame []byte) bool {
	s.mu.Lock()
	conns := make([]core.StreamConn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	delivered := false
	for _, c := range conns {
		if c.Send(frame) == nil {
			delivered = true
			continue
		}
		s.remove(c)
		_ = c.Close()
	}
	return delivered
}

type wireStreamHub struct {
	mu       sync.Mutex
	sessions map[string]*wireStreamSession
}

var streamHub = &wireStreamHub{sessions: map[string]*wireStreamSession{}}

func (h *wireStreamHub) publishOutput(sessionID, text string) bool {
	h.mu.Lock()
	sess, ok := h.sessions[sessionID]
	h.mu.Unlock()
	if !ok || !sess.active.Load() {
		return false
	}
	return sess.publish(encodeWireFrame(msgData, []byte(text)))
}

func init() {
	core.HandleStream("shell-wire", shellWireStreamHandler)
}

// wireHandlersForSession 构造会话的输入/resize 回调，WS 桥与 Streams 桥共用。
// 每次回调现取会话表：SSH 断线重连会替换 proc，握手时捕获的引用会过期。
func wireHandlersForSession(sessionID string) wireInputHandlers {
	return wireInputHandlers{
		onData: func(text string) {
			proc, ok := sessions.get(sessionID)
			if !ok {
				return
			}
			// 与 write_to_shell 一致：投递前静默还原 SSH 输入行，wire 数据面不得绕过历史捕获。
			attention.observeInput(sessionID, text)
			proc.control.observeInput(text)
			captureSSHInput(proc, text)
			_ = enqueueShellInput(proc, text)
		},
		onResize: func(cols, rows uint16) {
			proc, ok := sessions.get(sessionID)
			if !ok {
				return
			}
			proc.mu.Lock()
			resizeTx := proc.sshResizeTx
			proc.mu.Unlock()
			if resizeTx != nil {
				// 与 applyLocalShellResize 一致：relay writer 已退出或积压时丢弃，不得阻塞输入流。
				select {
				case resizeTx <- [2]uint16{cols, rows}:
				default:
				}
				return
			}
			_ = applyLocalShellResize(sessionID, cols, rows)
		},
	}
}

// shellWireStreamHandler：前端连上后首帧 JSON 握手 {sessionId}，随后双向帧流
// 直接桥接到该会话的 PTY/SSH 通道（本地与 SSH 会话共用）。
func shellWireStreamHandler(conn core.StreamConn) {
	var hello struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.ReceiveJSON(&hello); err != nil {
		return
	}
	sessionID := strings.TrimSpace(hello.SessionID)
	if sessionID == "" {
		return
	}
	_, ok := sessions.get(sessionID)
	if !ok {
		// 携带 sessionId 便于前端诊断面板展示真实拒绝原因。
		_ = conn.SendJSON(map[string]string{"error": "session not found", "sessionId": sessionID})
		return
	}

	streamHub.mu.Lock()
	sess, ok := streamHub.sessions[sessionID]
	if !ok {
		sess = &wireStreamSession{
			conns:    map[core.StreamConn]struct{}{},
			handlers: wireHandlersForSession(sessionID),
		}
		streamHub.sessions[sessionID] = sess
	}
	streamHub.mu.Unlock()

	sess.add(conn)
	defer func() {
		if sess.remove(conn) {
			streamHub.mu.Lock()
			if streamHub.sessions[sessionID] == sess {
				delete(streamHub.sessions, sessionID)
			}
			streamHub.mu.Unlock()
		}
	}()

	// 握手确认：前端据此切换到流通道（输出/输入都走流）。
	_ = conn.Send(encodeWireFrame(msgHeartbeat, []byte("ok")))

	for {
		frame, err := conn.Receive()
		if err != nil {
			return
		}
		frameType, payload, ok := decodeWireFrame(frame)
		if !ok {
			continue
		}
		switch frameType {
		case msgData:
			if sess.handlers.onData != nil {
				sess.handlers.onData(string(payload))
			}
		case msgResize:
			if len(payload) >= 4 && sess.handlers.onResize != nil {
				cols := uint16(payload[0])<<8 | uint16(payload[1])
				rows := uint16(payload[2])<<8 | uint16(payload[3])
				sess.handlers.onResize(cols, rows)
			}
		case msgHeartbeat:
			_ = conn.Send(encodeWireFrame(msgHeartbeat, []byte("pong")))
		}
	}
}
