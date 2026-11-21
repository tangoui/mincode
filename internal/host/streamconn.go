package host

import (
	"context"
	"encoding/json"

	"github.com/tangoui/tango/ipc"

	"mincode/app/backend/core"
)

// StreamConn / StreamHandler 复用后端 core 的抽象，后端流处理器无需改动。
type (
	StreamConn    = core.StreamConn
	StreamHandler = core.StreamHandler
)

// RegisterStream 是 core.SetStreamRegistrar 的注入目标：把后端流注册为 ipc 双向流，
// 页面用 socket(name) 打开（WebSocket 形状，见前端 desktop-stream.ts）。
func (h *Host) RegisterStream(name string, handler StreamHandler) {
	h.router.HandleDuplex(name, func(s *ipc.Stream, _ json.RawMessage) error {
		handler(streamConn{s})
		return nil
	})
}

// streamConn 把 ipc.Stream 适配成 core.StreamConn。
//
// 帧的对应关系：页面 send(string) 是 JSON 字符串帧，Receive 返回字符串的字节
// （JSONStream 发的就是 JSON 文本，ReceiveJSON 再解析）；页面发字节帧时原样返回。
// Go 侧 Send 和 SendJSON 都发字节帧（页面收到 ArrayBuffer，SendJSON 的是 JSON 文本）：
// 若 SendJSON 发 JSON 帧，页面对字符串值拿到的是裸字符串、对其他值拿到 JSON 文本，
// 对象流无法区分。
type streamConn struct{ s *ipc.Stream }

func (c streamConn) Send(data []byte) error { return c.s.WriteBytes(data) }
func (c streamConn) SendJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.s.WriteBytes(b)
}

func (c streamConn) Receive() ([]byte, error) {
	m, err := c.s.Receive()
	if err != nil {
		return nil, err
	}
	if m.Binary {
		return m.Bytes, nil
	}
	var text string
	if json.Unmarshal(m.JSON, &text) == nil {
		return []byte(text), nil
	}
	return []byte(m.JSON), nil
}

func (c streamConn) ReceiveJSON(v any) error {
	b, err := c.Receive()
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (c streamConn) Close() error             { return c.s.Close() }
func (c streamConn) Context() context.Context { return c.s.Context() }
