// 终端 Wire 数据面帧格式 v1: [version:1][type:1][length:4 BE][payload]。
// 由 wire_stream.go 的 IPC 流 通道使用，前端 terminal-wire.ts 按同一格式编解码。
package terminal

const (
	wireVersion     byte = 1
	msgData         byte = 1
	msgResize       byte = 2
	msgHeartbeat    byte = 3
	wireHeaderLen        = 6
	maxFramePayload      = 512 * 1024
)

func encodeWireFrame(msgType byte, payload []byte) []byte {
	n := len(payload)
	if n > maxFramePayload {
		n = maxFramePayload
	}
	frame := make([]byte, wireHeaderLen+n)
	frame[0] = wireVersion
	frame[1] = msgType
	frame[2] = byte(n >> 24)
	frame[3] = byte(n >> 16)
	frame[4] = byte(n >> 8)
	frame[5] = byte(n)
	copy(frame[wireHeaderLen:], payload[:n])
	return frame
}

func decodeWireFrame(data []byte) (msgType byte, payload []byte, ok bool) {
	if len(data) < wireHeaderLen {
		return 0, nil, false
	}
	if data[0] != wireVersion {
		return 0, nil, false
	}
	msgType = data[1]
	length := int(data[2])<<24 | int(data[3])<<16 | int(data[4])<<8 | int(data[5])
	if len(data) < wireHeaderLen+length {
		return 0, nil, false
	}
	return msgType, data[wireHeaderLen : wireHeaderLen+length], true
}

// wireInputHandlers 把 Wire 客户端收到的数据/resize 转交给会话所属的输入通道
// （本地 PTY 写入 或 SSH channel 写入/window_change）。
type wireInputHandlers struct {
	onData   func(text string)
	onResize func(cols, rows uint16)
}
