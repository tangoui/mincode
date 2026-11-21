package host

import (
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/tangoui/tango"
	"github.com/tangoui/tango/ipc"
	"github.com/tangoui/tango/runtime-js/runtimejs"
	"github.com/tangoui/tango/sys/appctl"
	"github.com/tangoui/tango/sys/clipboard"
	"github.com/tangoui/tango/sys/dialog"
	"github.com/tangoui/tango/sys/notify"
	"github.com/tangoui/tango/sys/shell"
	"github.com/tangoui/tango/sys/theme"
)

// SurfaceHeader 标识资产请求来源窗口的请求头（经 SurfaceOptions.WebView.Headers 注入）。
const SurfaceHeader = "X-Mincode-Surface"

// Attach 把 ipc 路由接到 app：注入页面运行时（window.tango，即 @tangoui/runtime），
// 消息和流的快速通道交给 r。页面就是 *tango.Surface 本身（它实现 ipc.Conn）。
func Attach(app *tango.App, r *ipc.Router) {
	app.AddBridgeScript(runtimejs.Script)
	app.OnMessage(func(s *tango.Surface, m string) { r.HandleMessage(s, m) })
	app.OnBinary(func(s *tango.Surface, gen uint64, p []byte) { r.HandleBinarySession(s, gen, p) })
}

// surfaceWindow 把 *tango.Surface 包装为 Window。
type surfaceWindow struct {
	s *tango.Surface
	n *TangoNative
}

// Close 等同用户点关闭按钮：有登记的关闭策略（SetCloseRequest）就交给它，
// 否则直接关闭。tango 的 Surface.Close 不询问 OnClose，所以策略要单独登记。
func (w surfaceWindow) Close() {
	w.n.mu.Lock()
	fn := w.n.closers[w.s]
	w.n.mu.Unlock()
	if fn != nil {
		fn()
		return
	}
	w.s.Close()
}

func (w surfaceWindow) Minimize()   { _ = w.s.Minimize() }
func (w surfaceWindow) Maximize()   { _ = w.s.Maximize() }
func (w surfaceWindow) Unmaximize() { _ = w.s.Restore() }
func (w surfaceWindow) ToggleMaximize() {
	if w.s.State() == tango.StateMaximized {
		_ = w.s.Restore()
	} else {
		_ = w.s.Maximize()
	}
}
func (w surfaceWindow) IsMaximized() bool { return w.s.State() == tango.StateMaximized }
func (w surfaceWindow) IsMinimized() bool { return w.s.State() == tango.StateMinimized }
func (w surfaceWindow) SetFullScreen(v bool) {
	if v {
		_ = w.s.Fullscreen()
	} else {
		_ = w.s.Restore()
	}
}
func (w surfaceWindow) IsFullScreen() bool { return w.s.State() == tango.StateFullscreen }
func (w surfaceWindow) Hide()              { w.s.Hide() }
func (w surfaceWindow) Show()              { w.s.Show() }
func (w surfaceWindow) Focus()             { w.s.Focus() }
func (w surfaceWindow) SetTitle(t string)  { w.s.SetTitle(t) }
func (w surfaceWindow) SetSize(wd, ht int) { w.s.SetSize(wd, ht) }
func (w surfaceWindow) Size() (int, int)   { return w.s.Size() }
func (w surfaceWindow) SetPosition(x, y int) {
	r := w.s.Bounds()
	r.X, r.Y = x, y
	_ = w.s.SetBounds(r)
}
func (w surfaceWindow) Position() (int, int) { r := w.s.Bounds(); return r.X, r.Y }

// TangoNative 用 tango sys/ 能力实现 host.Native。
type TangoNative struct {
	app      *tango.App
	mu       sync.Mutex
	windows  map[*tango.Surface]bool
	keys     map[string]*tango.Surface
	closers  map[*tango.Surface]func()
	navigate func(kind, value string)
}

// NewTangoNative 创建适配器。navigate 处理系统通知点击后的导航。
func NewTangoNative(app *tango.App, navigate func(kind, value string)) *TangoNative {
	return &TangoNative{
		app:      app,
		windows:  map[*tango.Surface]bool{},
		keys:     map[string]*tango.Surface{},
		closers:  map[*tango.Surface]func(){},
		navigate: navigate,
	}
}

// AddSurface 登记窗口（key 与 SurfaceOptions.WebView.Headers[SurfaceHeader] 一致）。
func (n *TangoNative) AddSurface(h *Host, s *tango.Surface, key string) {
	n.mu.Lock()
	n.windows[s] = true
	n.keys[key] = s
	n.mu.Unlock()
	h.AddPage(s)
}

// SetCloseRequest 登记窗口的关闭策略：页面调用 window.close 时执行 fn
// （例如主窗口按设置隐藏到托盘），而不是直接关闭。
func (n *TangoNative) SetCloseRequest(s *tango.Surface, fn func()) {
	n.mu.Lock()
	n.closers[s] = fn
	n.mu.Unlock()
}

// RemoveSurface 注销窗口。
func (n *TangoNative) RemoveSurface(h *Host, s *tango.Surface) {
	n.mu.Lock()
	delete(n.windows, s)
	delete(n.closers, s)
	for k, v := range n.keys {
		if v == s {
			delete(n.keys, k)
		}
	}
	n.mu.Unlock()
	h.RemovePage(s)
}

func (n *TangoNative) WindowFor(p Page) Window {
	s, ok := p.(*tango.Surface)
	if !ok {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.windows[s] {
		return surfaceWindow{s, n}
	}
	return nil
}

func (n *TangoNative) ClipboardText() string {
	t, _ := clipboard.ReadText(n.app)
	return t
}
func (n *TangoNative) SetClipboardText(text string)  { _ = clipboard.WriteText(n.app, text) }
func (n *TangoNative) OpenExternal(url string) error { return shell.OpenURL(n.app, url) }
func (n *TangoNative) IsDarkMode() bool {
	t, err := theme.System(n.app)
	return err == nil && t.Dark
}
func (n *TangoNative) Quit()    { n.app.Quit() }
func (n *TangoNative) HideApp() { _ = appctl.Hide(n.app) }
func (n *TangoNative) ShowApp() { _ = appctl.Show(n.app) }

func (n *TangoNative) OpenDialog(opts OpenDialogOptions) ([]string, error) {
	return dialog.Open(n.app, dialog.FileOptions{
		Title:         opts.Title,
		Directory:     opts.Directory,
		Multiple:      opts.AllowsMultipleSelection,
		PickFolders:   opts.CanChooseDirectories && !opts.CanChooseFiles,
		CanCreateDirs: opts.CanCreateDirectories,
		Filters:       mapFilters(opts.Filters),
	})
}

func (n *TangoNative) SaveDialog(opts SaveDialogOptions) (string, error) {
	return dialog.Save(n.app, dialog.FileOptions{
		Title:         opts.Title,
		Directory:     opts.Directory,
		Filename:      opts.Filename,
		CanCreateDirs: opts.CanCreateDirectories,
		Filters:       mapFilters(opts.Filters),
	})
}

func (n *TangoNative) MessageDialog(kind string, opts MessageDialogOptions) (string, error) {
	buttons := opts.Buttons
	if len(buttons) == 0 {
		buttons = []string{"OK"}
	}
	k := dialog.Info
	switch kind {
	case "warning":
		k = dialog.Warning
	case "error":
		k = dialog.Error
	case "question":
		k = dialog.Question
	}
	idx, err := dialog.Message(n.app, dialog.MessageOptions{
		Kind: k, Title: opts.Title, Message: opts.Message, Buttons: buttons,
	})
	if err != nil {
		return "", err
	}
	if idx >= 0 && idx < len(buttons) {
		return buttons[idx], nil
	}
	return buttons[0], nil
}

func (n *TangoNative) Notify(title, body string, data map[string]any) error {
	id, err := notify.Send(n.app, notify.Message{Title: title, Body: body})
	if err != nil {
		return err
	}
	if data != nil && n.navigate != nil {
		kind, _ := data["kind"].(string)
		value, _ := data["value"].(string)
		notify.OnClick(n.app, func(clicked string) {
			if clicked == id {
				n.navigate(kind, value)
			}
		})
	}
	return nil
}

// mapFilters 把 前端对话框过滤器（Pattern "*.epub;*.pdf"）转成 tango Patterns。
func mapFilters(filters []DialogFilter) []dialog.FileFilter {
	if len(filters) == 0 {
		return nil
	}
	out := make([]dialog.FileFilter, 0, len(filters))
	for _, f := range filters {
		var patterns []string
		for _, part := range strings.Split(f.Pattern, ";") {
			part = strings.TrimSpace(part)
			if part != "" {
				patterns = append(patterns, part)
			}
		}
		out = append(out, dialog.FileFilter{Name: f.DisplayName, Patterns: patterns})
	}
	return out
}

// AssetsHandler 构建挂到 tango Options.Assets 的总路由，并按 SurfaceHeader
// 把请求归属到已登记窗口（注入 Page/Window 上下文）。
func (n *TangoNative) AssetsHandler(h *Host, frontend fs.FS, devURL string) http.Handler {
	base := h.Handler(frontend, devURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if key := r.Header.Get(SurfaceHeader); key != "" {
			n.mu.Lock()
			s := n.keys[key]
			n.mu.Unlock()
			if s != nil {
				ctx = WithPage(ctx, s)
			}
		}
		base.ServeHTTP(w, r.WithContext(ctx))
	})
}
