package acp

// ACP 高频事件经 IPC 流 推送，控制面继续使用各自的命令通道。

import (
	"encoding/json"
	"sync"

	"mincode/app/backend/core"
)

var acpStreamSubs = struct {
	sync.Mutex
	conns map[core.StreamConn]struct{}
}{conns: make(map[core.StreamConn]struct{})}

// acpStreamEnvelope 与原先的 map{"event","payload"} 编码结果一致（字段按字母序）。
type acpStreamEnvelope struct {
	Event   string `json:"event"`
	Payload any    `json:"payload"`
}

func init() {
	core.HandleStream("acp-events", acpEventsRelayHandler)
}

// emitAcpEvent 将高频会话更新发给 Streams 订阅者；权限请求保留可靠事件通道。
func emitAcpEvent(event string, payload any) {
	if event == "acp:permission-request" {
		core.Emit(event, payload)
		return
	}
	acpStreamSubs.Lock()
	conns := make([]core.StreamConn, 0, len(acpStreamSubs.conns))
	for conn := range acpStreamSubs.conns {
		conns = append(conns, conn)
	}
	acpStreamSubs.Unlock()
	if len(conns) == 0 {
		return
	}
	// 每个事件只序列化一次，所有订阅者共享同一份只读字节。
	data, err := json.Marshal(acpStreamEnvelope{Event: event, Payload: payload})
	if err != nil {
		return
	}
	for _, conn := range conns {
		if err := conn.Send(data); err != nil {
			acpStreamSubs.Lock()
			delete(acpStreamSubs.conns, conn)
			acpStreamSubs.Unlock()
			_ = conn.Close()
		}
	}
}

// acpEventsRelayHandler：连接即订阅，对端断开自动退订。
func acpEventsRelayHandler(conn core.StreamConn) {
	acpStreamSubs.Lock()
	acpStreamSubs.conns[conn] = struct{}{}
	acpStreamSubs.Unlock()
	defer func() {
		acpStreamSubs.Lock()
		delete(acpStreamSubs.conns, conn)
		acpStreamSubs.Unlock()
	}()
	if err := conn.SendJSON(map[string]bool{"subscribed": true}); err != nil {
		return
	}
	for {
		if _, err := conn.Receive(); err != nil {
			return
		}
	}
}
