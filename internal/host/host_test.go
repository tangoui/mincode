package host

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tangoui/tango/ipc"
)

// fakePage 记录 Send 的下行消息（事件消息和 ipc 回复都会记下）。
type fakePage struct {
	mu   sync.Mutex
	msgs []string
}

func (p *fakePage) Send(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.msgs = append(p.msgs, msg)
}

func (p *fakePage) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.msgs...)
}

// fakeWindow 记录窗口控制调用。
type fakeWindow struct {
	mu         sync.Mutex
	calls      []string
	fullscreen bool
	maximized  bool
}

func (w *fakeWindow) record(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, name)
}

func (w *fakeWindow) recorded() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.calls...)
}
func (w *fakeWindow) Minimize()            { w.record("Minimize") }
func (w *fakeWindow) Maximize()            { w.record("Maximize") }
func (w *fakeWindow) Unmaximize()          { w.record("Unmaximize") }
func (w *fakeWindow) ToggleMaximize()      { w.record("ToggleMaximize") }
func (w *fakeWindow) IsMaximized() bool    { return w.maximized }
func (w *fakeWindow) IsMinimized() bool    { return false }
func (w *fakeWindow) SetFullScreen(v bool) { w.fullscreen = v; w.record("SetFullScreen") }
func (w *fakeWindow) IsFullScreen() bool   { return w.fullscreen }
func (w *fakeWindow) Close()               { w.record("Close") }
func (w *fakeWindow) Hide()                { w.record("Hide") }
func (w *fakeWindow) Show()                { w.record("Show") }
func (w *fakeWindow) Focus()               { w.record("Focus") }
func (w *fakeWindow) SetTitle(string)      { w.record("SetTitle") }
func (w *fakeWindow) SetSize(int, int)     { w.record("SetSize") }
func (w *fakeWindow) Size() (int, int)     { return 1200, 800 }
func (w *fakeWindow) SetPosition(int, int) { w.record("SetPosition") }
func (w *fakeWindow) Position() (int, int) { return 10, 20 }

// fakeNative 是 Native 的测试实现；window 只属于 owner 页面。
type fakeNative struct {
	mu        sync.Mutex
	clipboard string
	owner     Page
	window    Window
	dialogRet []string
	dialogErr error
	notify    []string
}

func (n *fakeNative) ClipboardText() string        { return n.clipboard }
func (n *fakeNative) SetClipboardText(text string) { n.clipboard = text }
func (n *fakeNative) OpenDialog(OpenDialogOptions) ([]string, error) {
	return n.dialogRet, n.dialogErr
}
func (n *fakeNative) SaveDialog(SaveDialogOptions) (string, error) {
	if n.dialogErr != nil {
		return "", n.dialogErr
	}
	if len(n.dialogRet) > 0 {
		return n.dialogRet[0], nil
	}
	return "", nil
}
func (n *fakeNative) MessageDialog(kind string, opts MessageDialogOptions) (string, error) {
	if len(opts.Buttons) > 0 {
		return opts.Buttons[0], nil
	}
	return "OK", nil
}
func (n *fakeNative) OpenExternal(string) error { return nil }
func (n *fakeNative) IsDarkMode() bool          { return true }
func (n *fakeNative) Quit()                     {}
func (n *fakeNative) HideApp()                  {}
func (n *fakeNative) ShowApp()                  {}
func (n *fakeNative) Notify(title, body string, data map[string]any) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notify = append(n.notify, title+"|"+body)
	return nil
}
func (n *fakeNative) WindowFor(p Page) Window {
	if p != nil && p == n.owner {
		return n.window
	}
	return nil
}

// echoDispatch 把 argsJSON 原样返回，便于断言往返。
func echoDispatch(ctx context.Context, cmd, argsJSON string) (string, error) {
	switch cmd {
	case "fail":
		return "", errf("boom")
	case "wait":
		<-ctx.Done()
		return "", ctx.Err()
	case "empty":
		return "", nil
	}
	return argsJSON, nil
}

func errf(format string, args ...any) error { return &testError{format} }

type testError struct{ s string }

func (e *testError) Error() string { return e.s }

// newTestHost 返回宿主和一个已注册的页面（事件测试用）。
func newTestHost() (*Host, *fakeNative, *fakePage) {
	page := &fakePage{}
	native := &fakeNative{window: &fakeWindow{}, owner: page}
	h := New(native, echoDispatch, ipc.NewRouter())
	h.AddPage(page)
	return h, native, page
}

// slowPage 模拟 Send 偶尔变慢的页面，放大并发投递的乱序窗口。
type slowPage struct{ fakePage }

func (p *slowPage) Send(msg string) {
	if len(msg)%3 == 0 {
		time.Sleep(50 * time.Microsecond)
	}
	p.fakePage.Send(msg)
}

// gatedPage 的 Send 在 gate 关闭前阻塞，模拟页面投递暂时跟不上。
type gatedPage struct {
	fakePage
	gate chan struct{}
}

func (p *gatedPage) Send(msg string) {
	<-p.gate
	p.fakePage.Send(msg)
}

func waitMsgs(t *testing.T, p interface{ snapshot() []string }, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := p.snapshot(); len(got) >= n {
			return got
		}
		time.Sleep(2 * time.Millisecond)
	}
	got := p.snapshot()
	t.Fatalf("got %d messages, want %d", len(got), n)
	return nil
}

func eventSeq(t *testing.T, msg string) int {
	t.Helper()
	i := strings.Index(msg, `"data":`)
	if i < 0 {
		t.Fatalf("no data in %q", msg)
	}
	rest := msg[i+len(`"data":`):]
	if j := strings.IndexAny(rest, ",}"); j >= 0 {
		rest = rest[:j]
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		t.Fatalf("bad seq in %q: %v", msg, err)
	}
	return n
}

// 同一调用方依次 Emit 的事件必须按调用顺序到达页面。
func TestEmitPreservesOrder(t *testing.T) {
	h := New(&fakeNative{}, echoDispatch, ipc.NewRouter())
	page := &slowPage{}
	h.AddPage(page)
	const n = 2000
	for i := 0; i < n; i++ {
		h.Emit("seq", i)
	}
	for i, msg := range waitMsgs(t, page, n) {
		if got := eventSeq(t, msg); got != i {
			t.Fatalf("event %d arrived at position %d", got, i)
		}
	}
}

// 投递积压时不丢事件：积压超过以前的 4096 上限，放开后全部按序到达。
func TestEmitBacklogKeepsAllEvents(t *testing.T) {
	h := New(&fakeNative{}, echoDispatch, ipc.NewRouter())
	page := &gatedPage{gate: make(chan struct{})}
	h.AddPage(page)
	const n = 5000
	for i := 0; i < n; i++ {
		h.Emit("seq", i)
	}
	close(page.gate)
	got := waitMsgs(t, page, n)
	if len(got) != n {
		t.Fatalf("got %d events, want %d", len(got), n)
	}
	for i, msg := range got {
		if s := eventSeq(t, msg); s != i {
			t.Fatalf("event %d arrived at position %d", s, i)
		}
	}
}

// 页面一注册就能收事件，不再等"就绪"：缓冲交给 tango 和页面运行时。
func TestEmitDeliversRightAfterAddPage(t *testing.T) {
	h, _, page := newTestHost()
	h.Emit("app:test", map[string]int{"v": 1})
	msg := waitMsgs(t, page, 1)[0]
	if msg != msgInline+`{"data":{"v":1},"name":"app:test"}` {
		t.Fatalf("message %q", msg)
	}
}
