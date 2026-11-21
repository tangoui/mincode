package db

// 查询结果分块经 IPC 流 推送，避免大结果集继续占用事件通道。

import (
	"sync"

	"mincode/app/backend/core"
)

var queryChunkStreamSubs = struct {
	sync.Mutex
	conns map[core.StreamConn]struct{}
}{conns: make(map[core.StreamConn]struct{})}

func init() {
	core.HandleStream("db-query-chunks", queryChunkRelayHandler)
}

// emitQueryChunk 向 Streams 订阅者推送查询结果分块。
func emitQueryChunk(chunk QueryResultChunk) {
	queryChunkStreamSubs.Lock()
	conns := make([]core.StreamConn, 0, len(queryChunkStreamSubs.conns))
	for conn := range queryChunkStreamSubs.conns {
		conns = append(conns, conn)
	}
	queryChunkStreamSubs.Unlock()
	for _, conn := range conns {
		if err := conn.SendJSON(chunk); err != nil {
			queryChunkStreamSubs.Lock()
			delete(queryChunkStreamSubs.conns, conn)
			queryChunkStreamSubs.Unlock()
			_ = conn.Close()
		}
	}
}

// queryChunkRelayHandler：连接即订阅，对端断开自动退订。
func queryChunkRelayHandler(conn core.StreamConn) {
	queryChunkStreamSubs.Lock()
	queryChunkStreamSubs.conns[conn] = struct{}{}
	queryChunkStreamSubs.Unlock()
	if err := conn.SendJSON(map[string]bool{"subscribed": true}); err != nil {
		queryChunkStreamSubs.Lock()
		delete(queryChunkStreamSubs.conns, conn)
		queryChunkStreamSubs.Unlock()
		return
	}
	defer func() {
		queryChunkStreamSubs.Lock()
		delete(queryChunkStreamSubs.conns, conn)
		queryChunkStreamSubs.Unlock()
	}()
	for {
		if _, err := conn.Receive(); err != nil {
			return
		}
	}
}
