package agentprogress

// AI 任务进度快照经 IPC 流 推送，避免高频状态占用事件通道。

import (
	"sync"

	"mincode/app/backend/core"
)

var progressStreamSubs = struct {
	sync.Mutex
	conns map[core.StreamConn]struct{}
}{conns: make(map[core.StreamConn]struct{})}

func init() {
	core.HandleStream("agent-run-progress", progressRelayHandler)
}

// emitProgress 向 Streams 订阅者推送进度快照，并通知进程内观察者。
func emitProgress(run any) {
	if snapshot, ok := run.(AgentRun); ok {
		notifyObservers(snapshot)
	}
	progressStreamSubs.Lock()
	conns := make([]core.StreamConn, 0, len(progressStreamSubs.conns))
	for conn := range progressStreamSubs.conns {
		conns = append(conns, conn)
	}
	progressStreamSubs.Unlock()
	for _, conn := range conns {
		if err := conn.SendJSON(run); err != nil {
			progressStreamSubs.Lock()
			delete(progressStreamSubs.conns, conn)
			progressStreamSubs.Unlock()
			_ = conn.Close()
		}
	}
}

// progressRelayHandler：连接即订阅，对端断开自动退订。
func progressRelayHandler(conn core.StreamConn) {
	progressStreamSubs.Lock()
	progressStreamSubs.conns[conn] = struct{}{}
	progressStreamSubs.Unlock()
	defer func() {
		progressStreamSubs.Lock()
		delete(progressStreamSubs.conns, conn)
		progressStreamSubs.Unlock()
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
