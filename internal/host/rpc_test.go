package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tangoui/tango/ipc"
	"github.com/tangoui/tango/sys/dialog"
)

func call(t *testing.T, h *Host, method string, arg any) (json.RawMessage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.router.Call(ctx, method, arg)
}

// pageCall 模拟页面发来的 ipc 调用，返回 ipc 回复消息（ret）。
func pageCall(t *testing.T, h *Host, page *fakePage, id int, method string, arg any) string {
	t.Helper()
	argJSON, _ := json.Marshal([]any{arg})
	msg, _ := json.Marshal(map[string]any{"tango": "call", "id": id, "method": method, "args": json.RawMessage(argJSON)})
	before := len(page.snapshot())
	h.router.HandleMessage(page, string(msg))
	want := `"id":` + strings.TrimSpace(string(mustJSON(id)))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range page.snapshot()[before:] {
			if strings.Contains(m, `"tango":"ret"`) && strings.Contains(m, want) {
				return m
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no reply to %s: %v", method, page.snapshot()[before:])
	return ""
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestInvokeReturnsCommandJSON(t *testing.T) {
	h, _, _ := newTestHost()
	raw, err := call(t, h, methodInvoke, map[string]any{"command": "echo", "args": map[string]int{"a": 1}})
	if err != nil || string(raw) != `{"a":1}` {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
	// 没有参数时后端拿到 "{}"；空结果是 null。
	if raw, _ := call(t, h, methodInvoke, map[string]any{"command": "echo"}); string(raw) != `{}` {
		t.Fatalf("no-args raw=%s", raw)
	}
	if raw, _ := call(t, h, methodInvoke, map[string]any{"command": "empty"}); string(raw) != `null` {
		t.Fatalf("empty raw=%s", raw)
	}
}

func TestInvokeErrorCarriesMessage(t *testing.T) {
	h, _, _ := newTestHost()
	_, err := call(t, h, methodInvoke, map[string]any{"command": "fail"})
	var ie *ipc.Error
	if !errors.As(err, &ie) || !strings.Contains(ie.Message, "boom") {
		t.Fatalf("err=%#v", err)
	}
	if _, err := call(t, h, methodInvoke, map[string]any{}); err == nil {
		t.Fatal("missing command accepted")
	}
}

// 带取消的调用走流：取消流即取消后端命令的 ctx。
func TestInvokeStreamCancelCancelsCommand(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	page := &fakePage{}
	h := New(&fakeNative{}, func(ctx context.Context, cmd, args string) (string, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	}, ipc.NewRouter())
	h.AddPage(page)
	ctx, cancel := context.WithCancel(context.Background())
	s, err := h.router.Open(ctx, methodInvokeStream, map[string]any{"command": "wait"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	<-started
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("command ctx not canceled")
	}
}

func TestInvokeStreamDeliversResult(t *testing.T) {
	h, _, _ := newTestHost()
	s, err := h.router.Open(context.Background(), methodInvokeStream, map[string]any{"command": "echo", "args": map[string]int{"b": 2}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := s.Receive()
	if err != nil || string(m.JSON) != `{"b":2}` {
		t.Fatalf("frame=%s err=%v", m.JSON, err)
	}
	if _, err := s.Receive(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream not ended after the result: %v", err)
	}
}

// 窗口类操作只作用于发起调用的页面所属窗口。
func TestNativeWindowOpsUseCallingPage(t *testing.T) {
	h, native, page := newTestHost()
	fw := native.window.(*fakeWindow)
	reply := pageCall(t, h, page, 1, methodNative, map[string]any{"op": "window.minimize"})
	if strings.Contains(reply, `"error"`) {
		t.Fatalf("reply %s", reply)
	}
	reply = pageCall(t, h, page, 2, methodNative, map[string]any{"op": "window.size"})
	if !strings.Contains(reply, `{"height":800,"width":1200}`) {
		t.Fatalf("size reply %s", reply)
	}
	pageCall(t, h, page, 3, methodNative, map[string]any{"op": "window.close"})
	if got := strings.Join(fw.recorded(), ","); got != "Minimize,Close" {
		t.Fatalf("window calls %s", got)
	}
	other := &fakePage{}
	h.AddPage(other)
	reply = pageCall(t, h, other, 4, methodNative, map[string]any{"op": "window.minimize"})
	if !strings.Contains(reply, `"not_found"`) {
		t.Fatalf("other page reached the window: %s", reply)
	}
}

func TestNativeClipboardDialogNotify(t *testing.T) {
	h, native, _ := newTestHost()
	if _, err := call(t, h, methodNative, map[string]any{"op": "clipboard.setText", "args": map[string]string{"text": "hello"}}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := call(t, h, methodNative, map[string]any{"op": "clipboard.text"}); string(raw) != `"hello"` {
		t.Fatalf("clipboard=%s", raw)
	}
	// 取消对话框（没有选中）返回 null。
	if raw, _ := call(t, h, methodNative, map[string]any{"op": "dialog.open", "args": map[string]any{}}); string(raw) != `null` {
		t.Fatalf("canceled open=%s", raw)
	}
	native.dialogRet = []string{"/tmp/a.epub", "/tmp/b.pdf"}
	if raw, _ := call(t, h, methodNative, map[string]any{"op": "dialog.open", "args": map[string]any{"AllowsMultipleSelection": true}}); string(raw) != `["/tmp/a.epub","/tmp/b.pdf"]` {
		t.Fatalf("open=%s", raw)
	}
	if raw, _ := call(t, h, methodNative, map[string]any{"op": "dialog.save", "args": map[string]any{"Filename": "x"}}); string(raw) != `"/tmp/a.epub"` {
		t.Fatalf("save=%s", raw)
	}
	if _, err := call(t, h, methodNative, map[string]any{"op": "notify.send", "args": map[string]any{"title": "T", "body": "B", "target": map[string]string{"kind": "ai"}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(native.notify, ",") != "T|B" {
		t.Fatalf("notify=%v", native.notify)
	}
	if _, err := call(t, h, methodNative, map[string]any{"op": "nope"}); err == nil {
		t.Fatal("unknown op accepted")
	}
}

func TestNativeFileDialogCancellation(t *testing.T) {
	h, native, _ := newTestHost()
	for _, op := range []string{"dialog.open", "dialog.save"} {
		t.Run(op, func(t *testing.T) {
			request := map[string]any{"op": op, "args": map[string]any{}}
			native.dialogErr = dialog.ErrCanceled
			if raw, err := call(t, h, methodNative, request); err != nil || string(raw) != "null" {
				t.Fatalf("cancel returned %s, %v", raw, err)
			}
			native.dialogErr = errors.New("dialog failed")
			if _, err := call(t, h, methodNative, request); err == nil || !strings.Contains(err.Error(), "dialog failed") {
				t.Fatalf("dialog failure = %v", err)
			}
		})
	}
}

// 页面 emit 的应用事件广播到所有页面（含发送者），沿用事件下行通道。
func TestPageEmitBroadcasts(t *testing.T) {
	h, _, a := newTestHost()
	b := &fakePage{}
	h.AddPage(b)
	h.router.HandleMessage(a, `{"tango":"event","name":"`+eventEmit+`","data":{"name":"x:changed","data":{"v":1}}}`)
	want := msgInline + `{"data":{"v":1},"name":"x:changed"}`
	for _, p := range []*fakePage{a, b} {
		if got := waitMsgs(t, p, 1)[0]; got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

// 后端流经 ipc 双向流承载：字符串帧当文本收、字节帧原样收；Send、SendJSON 都发字节帧。
func TestRegisterStreamAdaptsFrames(t *testing.T) {
	h, _, _ := newTestHost()
	h.RegisterStream("echo", func(conn StreamConn) {
		for {
			b, err := conn.Receive()
			if err != nil {
				return
			}
			if string(b) == `{"json":true}` {
				var v map[string]bool
				_ = json.Unmarshal(b, &v)
				_ = conn.SendJSON(v)
				continue
			}
			_ = conn.Send(b)
		}
	})
	s, err := h.router.Open(context.Background(), "echo")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Write("hello"); err != nil { // 页面 send("hello")
		t.Fatal(err)
	}
	if m, err := s.Receive(); err != nil || !m.Binary || string(m.Bytes) != "hello" {
		t.Fatalf("text echo: %+v %v", m, err)
	}
	if err := s.WriteBytes([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Receive(); err != nil || !m.Binary || string(m.Bytes) != "\x01\x02\x03" {
		t.Fatalf("bytes echo: %+v %v", m, err)
	}
	if err := s.Write(`{"json":true}`); err != nil { // JSONStream.send({json:true})
		t.Fatal(err)
	}
	if m, err := s.Receive(); err != nil || !m.Binary || string(m.Bytes) != `{"json":true}` {
		t.Fatalf("json echo: %+v %v", m, err)
	}
}
