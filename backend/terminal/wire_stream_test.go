package terminal

// shell-wire Streams 数据面的进程内端到端测试：
// 真实 PTY + fakeStreamConn 驱动 shellWireStreamHandler，
// 验证 握手 → 输入帧 → PTY 执行 → 输出帧经流回传 的完整链路。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
)

// fakeStreamConn 以内存管道实现 core.StreamConn。
type fakeStreamConn struct {
	in        chan []byte
	out       chan []byte
	closed    chan struct{}
	closeOnce sync.Once
	sendErr   error
}

func newFakeStreamConn() *fakeStreamConn {
	return &fakeStreamConn{
		in:     make(chan []byte, 64),
		out:    make(chan []byte, 1024),
		closed: make(chan struct{}),
	}
}

func (f *fakeStreamConn) Send(data []byte) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	buf := append([]byte(nil), data...)
	select {
	case f.out <- buf:
		return nil
	case <-f.closed:
		return errors.New("conn closed")
	}
}

func (f *fakeStreamConn) SendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return f.Send(data)
}

func (f *fakeStreamConn) Receive() ([]byte, error) {
	select {
	case data := <-f.in:
		return data, nil
	case <-f.closed:
		return nil, errors.New("conn closed")
	}
}

func (f *fakeStreamConn) ReceiveJSON(v any) error {
	data, err := f.Receive()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (f *fakeStreamConn) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeStreamConn) Context() context.Context { return context.Background() }

func TestWireStreamPublishRemovesFailedConnectionAndFallsBack(t *testing.T) {
	failed := newFakeStreamConn()
	failed.sendErr = errors.New("send failed")
	session := &wireStreamSession{conns: map[core.StreamConn]struct{}{failed: {}}}
	session.active.Store(true)

	if session.publish([]byte("frame")) {
		t.Fatal("failed stream send must not report delivery")
	}
	if session.active.Load() {
		t.Fatal("failed stream connection must become inactive")
	}
	select {
	case <-failed.closed:
	default:
		t.Fatal("failed stream connection was not closed")
	}
}

type shortWriter struct {
	max int
	buf strings.Builder
}

func (w *shortWriter) Write(data []byte) (int, error) {
	if len(data) > w.max {
		data = data[:w.max]
	}
	_, _ = w.buf.Write(data)
	return len(data), nil
}

func TestWriteAllRetriesShortWrites(t *testing.T) {
	writer := &shortWriter{max: 2}
	input := []byte("快速输入 command\r")
	if err := writeAll(writer, input); err != nil {
		t.Fatalf("完整写入失败: %v", err)
	}
	if writer.buf.String() != string(input) {
		t.Fatalf("短写后输入不完整: got %q want %q", writer.buf.String(), input)
	}
}

type testInputPTY struct {
	shortWriter
	failAfter int
}

func (p *testInputPTY) Read([]byte) (int, error)    { return 0, io.EOF }
func (p *testInputPTY) Close() error                { return nil }
func (p *testInputPTY) Resize(uint16, uint16) error { return nil }
func (p *testInputPTY) Pid() int                    { return 1 }
func (p *testInputPTY) Write(data []byte) (int, error) {
	if p.failAfter > 0 && p.buf.Len() >= p.failAfter {
		return 0, io.ErrClosedPipe
	}
	return p.shortWriter.Write(data)
}

func TestLocalShellInputConfirmsCompleteWriteAndReportsFailure(t *testing.T) {
	pty := &testInputPTY{shortWriter: shortWriter{max: 1}, failAfter: 2}
	proc := &shellProcess{localPTY: pty, stdinDone: make(chan struct{})}
	if err := writeLocalShellInput(proc, "12"); err != nil {
		t.Fatalf("complete write failed: %v", err)
	}
	if got := pty.buf.String(); got != "12" {
		t.Fatalf("partial input: %q", got)
	}
	if err := writeLocalShellInput(proc, "3"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("expected PTY write error, got %v", err)
	}
	if got := pty.buf.String(); got != "12" {
		t.Fatalf("failed write should not claim input: %q", got)
	}
}

func TestEnqueueShellInputWaitsForCapacityWithoutDroppingInput(t *testing.T) {
	input := make(chan string, 1)
	input <- "pending"
	proc := &shellProcess{stdinTx: input, stdinDone: make(chan struct{})}
	result := make(chan error, 1)

	go func() {
		result <- enqueueShellInput(proc, "x")
	}()

	select {
	case err := <-result:
		t.Fatalf("输入队列暂满时不应提前返回: %v", err)
	case <-time.After(1100 * time.Millisecond):
	}

	if got := <-input; got != "pending" {
		t.Fatalf("队列原有输入异常: %q", got)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("队列恢复容量后写入失败: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("队列恢复容量后输入仍未写入")
	}
	if got := <-input; got != "x" {
		t.Fatalf("恢复容量后输入异常: %q", got)
	}
}

// TestShellWireStreamRejectsUnknownSession 验证未知会话握手被 JSON 拒绝帧响应
// 并附带 sessionId，前端据此在诊断面板展示真实原因而非笼统的
// "closed before handshake"。
func TestShellWireStreamRejectsUnknownSession(t *testing.T) {
	conn := newFakeStreamConn()
	done := make(chan struct{})
	go func() {
		defer close(done)
		shellWireStreamHandler(conn)
	}()

	hello, _ := json.Marshal(map[string]string{"sessionId": "wire-stream-unknown"})
	conn.in <- hello

	select {
	case frame := <-conn.out:
		var rejection struct {
			Error     string `json:"error"`
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(frame, &rejection); err != nil {
			t.Fatalf("拒绝帧不是合法 JSON: %v", err)
		}
		if rejection.Error != "session not found" {
			t.Fatalf("拒绝原因异常: %q", rejection.Error)
		}
		if rejection.SessionID != "wire-stream-unknown" {
			t.Fatalf("拒绝帧 sessionId 异常: %q", rejection.SessionID)
		}
		_ = conn.Close()
		<-done
	case <-time.After(5 * time.Second):
		t.Fatal("等待握手拒绝帧超时")
	}
}

// TestShellWireStreamEndToEnd 覆盖：JSON 握手、心跳确认、输入帧写入 PTY、
// 输出帧经 streamHub 回传（而非 shell-output 事件）。
func TestShellWireStreamEndToEnd(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("PTY 集成测试不在 CI 环境运行")
	}
	const sessionID = "wire-stream-test"

	startArgs, _ := json.Marshal(map[string]any{
		"payload": map[string]any{"sessionId": sessionID, "cols": 80, "rows": 24},
	})
	if _, err := core.Dispatch("start_local_shell", string(startArgs)); err != nil {
		t.Skipf("本地 shell 不可用，跳过: %v", err)
	}
	defer func() {
		closeArgs, _ := json.Marshal(map[string]any{
			"payload": map[string]any{"sessionId": sessionID},
		})
		_, _ = core.Dispatch("close_local_shell", string(closeArgs))
	}()

	conn := newFakeStreamConn()
	done := make(chan struct{})
	go func() {
		defer close(done)
		shellWireStreamHandler(conn)
	}()

	// 握手：首帧 JSON 指定会话。
	hello, _ := json.Marshal(map[string]string{"sessionId": sessionID})
	conn.in <- hello

	// 等待握手确认心跳帧。
	select {
	case frame := <-conn.out:
		msgType, payload, ok := decodeWireFrame(frame)
		if !ok || msgType != msgHeartbeat || string(payload) != "ok" {
			t.Fatalf("握手确认帧异常: type=%d payload=%q", msgType, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待握手确认帧超时")
	}

	// 输入帧：让 shell 回显一个唯一标记。
	marker := fmt.Sprintf("WIRE_OK_%d", time.Now().UnixNano()%1000000)
	conn.in <- encodeWireFrame(msgData, []byte("echo "+marker+"\r"))

	// 输出应经流的 msgData 帧回传（PTY 回显或命令输出都证明回路连通）。
	var collected strings.Builder
	deadline := time.After(10 * time.Second)
	for {
		select {
		case frame := <-conn.out:
			msgType, payload, ok := decodeWireFrame(frame)
			if !ok || msgType != msgData {
				continue
			}
			collected.Write(payload)
			if strings.Contains(collected.String(), marker) {
				_ = conn.Close()
				<-done
				return
			}
		case <-deadline:
			t.Fatalf("输出帧未经流回传，已收内容: %q", collected.String())
		}
	}
}

func TestWireResizeDoesNotBlockWhenRelayWriterExited(t *testing.T) {
	const sessionID = "wire-resize-nonblocking-test"
	// 无缓冲且无读端：模拟 relay writer 已退出。
	proc := &shellProcess{sshResizeTx: make(chan [2]uint16)}
	sessions.put(sessionID, proc)
	defer sessions.remove(sessionID)

	done := make(chan struct{})
	go func() {
		defer close(done)
		wireHandlersForSession(sessionID).onResize(120, 40)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onResize blocked on an unread resize channel")
	}
}
