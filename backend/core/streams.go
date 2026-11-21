package core

// Streams 桥：backend 各子包不直接依赖 宿主应用实例，
// 由 main.go 在创建 app 后注入注册器，提供 ipc 双向流（宿主 RegisterStream）
// 的双向字节流能力。模式与 events.go 的 SetEmitter 注入保持一致。

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// StreamConn 是流连接的抽象接口，具体实现由 main.go 适配注入
// （*application.StreamConn 天然满足该接口）。
type StreamConn interface {
	Send(data []byte) error
	SendJSON(v any) error
	Receive() ([]byte, error)
	ReceiveJSON(v any) error
	Close() error
	Context() context.Context
}

// StreamHandler 每个连接在独立 goroutine 执行一次；连接断开（页面重载/关闭）时
// Receive 返回错误，handler 应据此退出。
type StreamHandler func(conn StreamConn)

type StreamDiagnostics struct {
	Transport         string `json:"transport"`
	ActiveConnections int64  `json:"activeConnections"`
	BytesSent         uint64 `json:"bytesSent"`
	BytesReceived     uint64 `json:"bytesReceived"`
	HandshakeFailures uint64 `json:"handshakeFailures"`
	Fallbacks         uint64 `json:"fallbacks"`
	LastError         string `json:"lastError"`
	LastErrorAt       int64  `json:"lastErrorAt"`
}

type streamDiagnosticConn struct {
	StreamConn
}

var (
	streamMu        sync.RWMutex
	streamRegistrar func(name string, handler StreamHandler)
	streamPending   = map[string]StreamHandler{}

	streamActive           atomic.Int64
	streamBytesSent        atomic.Uint64
	streamBytesReceived    atomic.Uint64
	streamHandshakeFailure atomic.Uint64
	streamFallbacks        atomic.Uint64
	streamErrorMu          sync.RWMutex
	streamLastError        string
	streamLastErrorAt      int64
)

func init() {
	Register("stream_diagnostics_get", streamDiagnosticsGetHandler)
	Register("stream_diagnostics_report", streamDiagnosticsReportHandler)
}

func recordStreamError(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	streamErrorMu.Lock()
	streamLastError = err.Error()
	streamLastErrorAt = time.Now().UnixMilli()
	streamErrorMu.Unlock()
}

func (c *streamDiagnosticConn) Send(data []byte) error {
	err := c.StreamConn.Send(data)
	if err != nil {
		recordStreamError(err)
		return err
	}
	streamBytesSent.Add(uint64(len(data)))
	return nil
}

func (c *streamDiagnosticConn) SendJSON(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		recordStreamError(err)
		return err
	}
	return c.Send(data)
}

func (c *streamDiagnosticConn) Receive() ([]byte, error) {
	data, err := c.StreamConn.Receive()
	if err != nil {
		if c.Context().Err() == nil {
			recordStreamError(err)
		}
		return nil, err
	}
	streamBytesReceived.Add(uint64(len(data)))
	return data, nil
}

func (c *streamDiagnosticConn) ReceiveJSON(value any) error {
	data, err := c.Receive()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		recordStreamError(err)
		return err
	}
	return nil
}

func StreamDiagnosticsSnapshot() StreamDiagnostics {
	streamErrorMu.RLock()
	lastError, lastErrorAt := streamLastError, streamLastErrorAt
	streamErrorMu.RUnlock()
	return StreamDiagnostics{
		Transport:         "tango-ipc-stream",
		ActiveConnections: streamActive.Load(),
		BytesSent:         streamBytesSent.Load(),
		BytesReceived:     streamBytesReceived.Load(),
		HandshakeFailures: streamHandshakeFailure.Load(),
		Fallbacks:         streamFallbacks.Load(),
		LastError:         lastError,
		LastErrorAt:       lastErrorAt,
	}
}

func streamDiagnosticsGetHandler(_, _ string) (string, error) {
	return EncodeResult(StreamDiagnosticsSnapshot())
}

func streamDiagnosticsReportHandler(_, argsJSON string) (string, error) {
	var args struct {
		Kind  string `json:"kind"`
		Error string `json:"error"`
	}
	if err := DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	switch args.Kind {
	case "handshake_failure":
		streamHandshakeFailure.Add(1)
	case "fallback":
		streamFallbacks.Add(1)
	case "error":
	default:
		return "", errors.New("invalid stream diagnostic kind")
	}
	if args.Error != "" {
		recordStreamError(errors.New(args.Error))
	}
	return "", nil
}

// SetStreamRegistrar 注入流注册实现（main.go 创建 app 后调用一次）。
// 注入前经 HandleStream 暂存的流会在此时补注册。
func SetStreamRegistrar(fn func(name string, handler StreamHandler)) {
	streamMu.Lock()
	defer streamMu.Unlock()
	streamRegistrar = fn
	for name, handler := range streamPending {
		fn(name, handler)
	}
	streamPending = map[string]StreamHandler{}
}

// HandleStream 注册具名流，前端以 Stream(name) 连接。
// 允许在注册器注入前调用（包 init 阶段）：先暂存，注入后自动补注册。
func HandleStream(name string, handler StreamHandler) {
	streamMu.Lock()
	defer streamMu.Unlock()
	tracked := func(conn StreamConn) {
		streamActive.Add(1)
		defer streamActive.Add(-1)
		handler(&streamDiagnosticConn{StreamConn: conn})
	}
	if streamRegistrar != nil {
		streamRegistrar(name, tracked)
		return
	}
	streamPending[name] = tracked
}
