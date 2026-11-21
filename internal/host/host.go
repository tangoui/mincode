// Package host 把 MinCode 的 Go 后端接到 tango 上：页面经 tango ipc
// （页面侧 @tangoui/runtime）调用后端命令、打开流、使用原生能力；
// 后端事件按序下发给每个页面（events.go）。
package host

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/tangoui/tango/ipc"
)

// Page 是宿主窗口里的一个页面，即 ipc 连接；tango 里就是 *tango.Surface。
// Send 不等待页面处理，同一 goroutine 先后发送的消息按顺序到达。
type Page = ipc.Conn

// Window 是页面所属窗口的控制面；tango 适配器包装 *tango.Surface 满足它。
type Window interface {
	Minimize()
	Maximize()
	Unmaximize()
	ToggleMaximize()
	IsMaximized() bool
	IsMinimized() bool
	SetFullScreen(bool)
	IsFullScreen() bool
	// Close 按窗口的关闭策略处理（主窗口可能隐藏到托盘），等同于用户点关闭按钮。
	Close()
	Hide()
	Show()
	Focus()
	SetTitle(string)
	SetSize(width, height int)
	Size() (int, int)
	SetPosition(x, y int)
	Position() (int, int)
}

// OpenDialogOptions 是前端打开文件对话框的参数形状。
type OpenDialogOptions struct {
	CanChooseDirectories    bool           `json:"CanChooseDirectories"`
	CanChooseFiles          bool           `json:"CanChooseFiles"`
	CanCreateDirectories    bool           `json:"CanCreateDirectories"`
	AllowsMultipleSelection bool           `json:"AllowsMultipleSelection"`
	Title                   string         `json:"Title"`
	Directory               string         `json:"Directory"`
	Filters                 []DialogFilter `json:"Filters"`
}

// SaveDialogOptions 是前端保存文件对话框的参数形状。
type SaveDialogOptions struct {
	Title                string         `json:"Title"`
	Filename             string         `json:"Filename"`
	Directory            string         `json:"Directory"`
	CanCreateDirectories bool           `json:"CanCreateDirectories"`
	Filters              []DialogFilter `json:"Filters"`
}

// DialogFilter 是对话框过滤器（Pattern 如 "*.epub;*.pdf"）。
type DialogFilter struct {
	DisplayName string `json:"DisplayName"`
	Pattern     string `json:"Pattern"`
}

// MessageDialogOptions 是消息框参数。
type MessageDialogOptions struct {
	Title   string   `json:"Title"`
	Message string   `json:"Message"`
	Detail  string   `json:"Detail"`
	Buttons []string `json:"Buttons"`
}

// Native 是宿主之下的原生能力边界，由 tango 适配器实现；测试用假实现。
type Native interface {
	ClipboardText() string
	SetClipboardText(text string)
	OpenDialog(opts OpenDialogOptions) ([]string, error)
	SaveDialog(opts SaveDialogOptions) (string, error)
	MessageDialog(kind string, opts MessageDialogOptions) (string, error)
	OpenExternal(url string) error
	IsDarkMode() bool
	Quit()
	HideApp()
	ShowApp()
	// Notify 发送系统通知；data 携带点击导航目标（kind/value），无目标时为 nil。
	Notify(title, body string, data map[string]any) error
	// WindowFor 返回页面所属窗口；未知页面返回 nil。
	WindowFor(p Page) Window
}

// DispatchFunc 分发后端命令（backend.DispatchContext 的签名）。
type DispatchFunc func(ctx context.Context, cmd, argsJSON string) (string, error)

type pageState struct {
	// 投递队列（事件 JSON）：Emit 在 h.mu 下入队，由单个 drainPage goroutine
	// 按入队顺序转成下行消息并 Send，保证顺序且不在持锁时投递。
	queue    []string
	draining bool
}

// Host 汇总宿主各部件：ipc 处理器（rpc.go）、事件下发、大事件引用。
type Host struct {
	native   Native
	dispatch DispatchFunc
	router   *ipc.Router

	mu    sync.Mutex
	pages map[Page]*pageState

	refs *refStore // 大事件按引用投递，见 events.go
}

// New 创建宿主并在 r 上注册 MinCode 的 ipc 处理器。
func New(native Native, dispatch DispatchFunc, r *ipc.Router) *Host {
	h := &Host{
		native:   native,
		dispatch: dispatch,
		router:   r,
		pages:    map[Page]*pageState{},
		refs:     newRefStore(),
	}
	h.install()
	return h
}

// AddPage 注册窗口页面（创建窗口时调用），开始接收事件。
//
// 不需要等页面"就绪"：页面加载前 tango 会保存 Send 的消息、加载后按序补发，
// 运行时脚本在页面订阅前也会缓存原始消息，所以窗口一创建就可以投递。
func (h *Host) AddPage(p Page) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.pages[p]; !ok {
		h.pages[p] = &pageState{}
	}
}

// RemovePage 注销页面：结束它的 ipc 调用和流，丢弃它的大事件引用。
func (h *Host) RemovePage(p Page) {
	h.mu.Lock()
	delete(h.pages, p)
	h.mu.Unlock()
	h.router.Drop(p)
	h.refs.dropPage(p)
}

// Emit 是 core.SetEmitter 的注入目标：向所有已注册页面广播事件。
// 队列里存事件 JSON，投递时才决定内联还是按引用（events.go）。
func (h *Host) Emit(name string, data any) {
	b, err := json.Marshal(map[string]any{"name": name, "data": data})
	if err != nil {
		return
	}
	payload := string(b)
	h.mu.Lock()
	defer h.mu.Unlock()
	for p, st := range h.pages {
		h.enqueueLocked(p, st, payload)
	}
}

// enqueueLocked 把事件 JSON 追加到页面投递队列，必要时启动投递 goroutine。
// 调用方须持有 h.mu。
func (h *Host) enqueueLocked(p Page, st *pageState, payloads ...string) {
	// 不设上限、不丢事件：Page.Send 不阻塞（tango 只把消息放进自己的下行队列），
	// 这里只在 drainPage 被调度前短暂积压；真正的积压发生在 tango 下行队列和页面里，
	// 在这一层截断只会丢事件，挡不住堆积。
	st.queue = append(st.queue, payloads...)
	if !st.draining && len(st.queue) > 0 {
		st.draining = true
		go h.drainPage(p, st)
	}
}

// drainPage 按入队顺序逐条 Send，队列空时退出；每个页面同时只有一个。
// 页面注销后剩余事件照常投递，由 Page.Send 自行忽略已关闭的页面
// （此时 refs 已清掉该页面，新的大事件引用只会等到 TTL 过期）。
func (h *Host) drainPage(p Page, st *pageState) {
	for {
		h.mu.Lock()
		batch := st.queue
		st.queue = nil
		if len(batch) == 0 {
			st.draining = false
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
		for _, payload := range batch {
			p.Send(h.eventMessage(p, payload))
		}
	}
}

type contextKey int

const pageKey contextKey = 1

// WithPage 把资源请求所属页面放进上下文（由 tango 适配层按 SurfaceHeader 注入）。
func WithPage(ctx context.Context, p Page) context.Context {
	return context.WithValue(ctx, pageKey, p)
}

// PageFrom 取出资源请求所属页面；无页面时返回 nil。
func PageFrom(ctx context.Context) Page {
	p, _ := ctx.Value(pageKey).(Page)
	return p
}
