// MinCode 的 tango WebView 宿主：用 tango 的系统 WebView 承载 MinCode 的 Vue 前端，
// 页面经 tango ipc（@tangoui/runtime）调用同一套 Go 后端（backend.DispatchContext），
// 接线见 internal/host。
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"mincode/app/internal/host"

	"mincode/app/backend"
	"mincode/app/backend/appopen"
	"mincode/app/backend/browser"
	"mincode/app/backend/core"
	"mincode/app/backend/desktop"
	"mincode/app/backend/e2esmoke"
	"mincode/app/backend/externalmcp"
	"mincode/app/backend/misc"
	"mincode/app/backend/plugin"
	"mincode/app/backend/terminalactivity"

	"github.com/tangoui/tango"
	"github.com/tangoui/tango/ipc"
	"github.com/tangoui/tango/sys/appctl"
	"github.com/tangoui/tango/sys/single"
	"github.com/tangoui/tango/sys/windowstate"
)

//go:embed all:frontend/dist
var frontendDist embed.FS

//go:embed icons/appicon.png
var appIcon []byte

//go:embed icons/tray-template.png
var trayIconTemplate []byte

//go:embed icons/tray-light.png
var trayIconLight []byte

//go:embed icons/tray-dark.png
var trayIconDark []byte

const appID = "com.mincode.desktop"

// appIdentity 是应用名和 ID。测试可用 MINCODE_APP_NAME / MINCODE_APP_ID 换一套身份：
// tango 按应用名给 WebView 的本地存储分目录（macOS 为 ~/Library/WebKit/<名字>），按 ID
// 做单实例与窗口几何，换了身份的进程与平时运行的 MinCode 互不影响（e2e/ 用它隔离）。
func appIdentity() (name, id string) {
	name, id = "MinCode", appID
	if v := os.Getenv("MINCODE_APP_NAME"); v != "" {
		name = v
	}
	if v := os.Getenv("MINCODE_APP_ID"); v != "" {
		id = v
	}
	return name, id
}

// urlSchemes 是应用处理的链接协议：mincode:// 深链，以及交给插件外部打开处理器的
// 协议（magnet 等）。必须与 tan.json 的 "urlSchemes" 一致：打包时 tan 按那份列表
// 向系统注册；运行时 Windows/Linux 靠这份列表从启动参数里认出链接。
var urlSchemes = []string{"mincode", "magnet", "ed2k", "thunder", "ftps", "sftp", "ssh", "webcal", "webcals"}

func mustReadStdin() []byte {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return nil
	}
	return data
}

func main() {
	// 与 MinCode 一致的 CLI 辅助模式（打包 CLI / MCP helper / 终端钩子），先于桌面启动。
	if plugin.IsBundledCLIMode(os.Args[1:]) {
		os.Exit(plugin.RunBundledCLI(os.Args[2:]))
	}
	if externalmcp.IsHelper(os.Args[1:]) {
		if err := externalmcp.RunStdio(os.Stdin, os.Stdout); err != nil {
			log.Printf("external MCP helper failed: %v", err)
			os.Exit(2)
		}
		return
	}
	if terminalactivity.IsHookMode(os.Args[1:]) {
		if err := terminalactivity.RunHook(mustReadStdin()); err != nil {
			os.Exit(2)
		}
		return
	}
	if browser.IsInternalMCPMode(os.Args[1:]) {
		if err := browser.RunInternalMCPStdio(os.Stdin, os.Stdout); err != nil {
			log.Printf("internal browser MCP helper failed: %v", err)
			os.Exit(2)
		}
		return
	}

	// 数据隔离：使用独立数据目录，不读写 MinCode 数据。
	if os.Getenv("MINCODE_DATA_DIR") == "" {
		if home, err := os.UserHomeDir(); err == nil {
			_ = os.Setenv("MINCODE_DATA_DIR", filepath.Join(home, ".mincode-tango-web", "data"))
		}
	}

	var win *tango.Surface
	var h *host.Host
	var quitting atomic.Bool
	var popupMu sync.Mutex
	popups := map[string]*tango.Surface{}

	// 导航：系统通知点击 / 桌面导航命令共用，对齐 desktopRuntime.navigate。
	// 主窗口创建前到达的导航目标（冷启动时的 mincode:// 链接）先存着，窗口建好后补发。
	var navMu sync.Mutex
	var pendingTargets []desktop.NavigationTarget
	navigateTo := func(target desktop.NavigationTarget) {
		if err := desktop.ValidateTarget(target); err != nil {
			log.Printf("ignored invalid desktop target: %v", err)
			return
		}
		navMu.Lock()
		if win == nil || h == nil {
			pendingTargets = append(pendingTargets, target)
			navMu.Unlock()
			return
		}
		navMu.Unlock()
		win.Show()
		win.Focus()
		if _, queued := desktop.DeferUntilFrontendReady(target); queued {
			return
		}
		h.Emit("desktop:navigate", target)
	}
	navigate := func(kind, value string) {
		navigateTo(desktop.NavigationTarget{Kind: kind, Value: value})
	}
	showMain := func() {
		if win != nil {
			if win.State() == tango.StateMinimized {
				_ = win.Restore()
			}
			win.Show()
			win.Focus()
		}
	}

	// Assets 路由先以占位 handler 创建 app，随后注入协议桥（bridge 在 Run 前赋值）。
	var bridge http.Handler
	appName, appIdent := appIdentity()
	app := tango.New(tango.Options{
		Name:       appName,
		ID:         appIdent,
		URLSchemes: urlSchemes,
		Assets: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if bridge != nil {
				bridge.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
		}),
	})

	// 页面运行时与 ipc：调用、事件、流都走 tango ipc。
	router := ipc.NewRouter()
	host.Attach(app, router)
	native := host.NewTangoNative(app, navigate)
	h = host.New(native, backend.DispatchContext, router)
	bridge = native.AssetsHandler(h, frontendFS(), os.Getenv("TANGO_DEV_URL"))

	// 后端事件 / 流桥注入（与 MinCode main.go 一致）。
	core.SetEmitter(h.Emit)
	core.SetStreamRegistrar(h.RegisterStream)
	// 全局快捷键：MinCode 自己的唤起快捷键（默认 CmdOrCtrl+Shift+Space）和插件的全局快捷键
	// 共用一个管理器（globalshortcuts.go）。
	shortcuts := newGlobalShortcuts(app, showMain)
	// 应用菜单与系统托盘（menus.go）：插件页面接管键盘时关掉自定义菜单快捷键，插件托盘项进托盘菜单。
	appMenuCtl := newAppMenu(app, showMain, navigate)
	trayCtl := newTrayMenu(app, showMain, func() {
		if win != nil {
			win.Hide()
		}
	}, app.Quit)
	desktop.SetAdapter(desktop.Adapter{
		FocusTarget: func(target desktop.NavigationTarget) { navigate(target.Kind, target.Value) },
		SetBadge: func(label string) error {
			if runtime.GOOS == "darwin" {
				return appctl.SetBadge(app, label)
			}
			return nil
		},
		SetGlobalShortcut:        shortcuts.setHost,
		RegisterPluginShortcut:   shortcuts.register,
		UnregisterPluginShortcut: shortcuts.unregister,
		HostShortcut:             shortcuts.hostShortcut,
		IsWayland:                desktop.DetectWayland,
		SetMenuAccelerators:      appMenuCtl.setAccelerators,
		SetPluginTray:            trayCtl.setPluginGroups,
	})

	app.OnReady(func() {
		primary, err := single.Claim(app, single.Options{
			// 冒烟测试（tools/e2e/desktop-smoke.mjs）的进程不能和平时运行的 MinCode 互相转交启动。
			Key: e2esmoke.IsolatedInstanceID(),
			OnSecondInstance: func(args []string, cwd string) {
				if win != nil {
					win.Show()
					win.Focus()
				}
			},
		})
		if err == nil && !primary {
			app.Quit()
			return
		}

		titleBar := tango.TitleBarDefault
		if runtime.GOOS == "darwin" {
			titleBar = tango.TitleBarHidden
		}
		s, err := app.NewSurface(tango.SurfaceOptions{
			Title:     "MinCode",
			URL:       "/",
			Width:     1200,
			Height:    800,
			MinWidth:  800,
			MinHeight: 600,
			Resizable: true,
			Appearance: tango.Appearance{
				TitleBar: titleBar,
			},
			AcceptFiles: true,
			WebView: tango.WebViewOptions{
				Headers: http.Header{host.SurfaceHeader: []string{"main"}},
			},
		})
		if err != nil {
			log.Fatal(err)
		}
		alignMainTitleBar(app, router, s)
		navMu.Lock()
		win = s
		pending := pendingTargets
		pendingTargets = nil
		navMu.Unlock()
		// 内置浏览器的原生覆盖层挂载在主窗口上。Native 在窗口关闭后返回 0。
		browser.SetNativeWindowProvider(func() unsafe.Pointer { return unsafe.Pointer(s.Native()) })
		native.AddSurface(h, s, "main")
		s.OnClosed(func() { native.RemoveSurface(h, s) })
		if e2esmoke.Enabled() {
			// 冒烟测试与用户共用桌面：窗口被遮挡时也保持渲染（否则 rAF 暂停，计时失真）。
			if err := e2esmoke.KeepRenderingWhenCovered(unsafe.Pointer(s.Native())); err != nil {
				log.Printf("e2e: %v; scenarios may stall while the window is covered", err)
			} else {
				log.Printf("e2e: smoke window keeps rendering while covered")
			}
		}
		for _, target := range pending {
			navigateTo(target)
		}

		// misc 的窗口 seam 由 tango 提供。
		misc.FocusMainWindow = func() error { win.Show(); win.Focus(); return nil }
		misc.CurrentWindowName = func() string { return "main" }
		misc.CreatePopupWindow = func(label, title, url string) error {
			popupMu.Lock()
			defer popupMu.Unlock()
			if p, ok := popups[label]; ok {
				p.Show()
				p.Focus()
				return nil
			}
			p, err := app.NewSurface(tango.SurfaceOptions{
				Title: title, URL: url, Width: 1100, Height: 760, MinWidth: 720, MinHeight: 460,
				Resizable:  true,
				Appearance: tango.Appearance{TitleBar: tango.TitleBarDefault},
				WebView:    tango.WebViewOptions{Headers: http.Header{host.SurfaceHeader: []string{label}}},
			})
			if err != nil {
				return err
			}
			popups[label] = p
			native.AddSurface(h, p, label)
			p.OnClosed(func() {
				popupMu.Lock()
				delete(popups, label)
				popupMu.Unlock()
				native.RemoveSurface(h, p)
				h.Emit("popup-tab-window-closing", label)
			})
			p.Show()
			p.Focus()
			return nil
		}
		misc.PopupWindowAction = func(label, action string) error {
			popupMu.Lock()
			p := popups[label]
			popupMu.Unlock()
			if p == nil {
				return fmt.Errorf("popup 窗口不存在: %q", label)
			}
			switch action {
			case "focus":
				p.Show()
				p.Focus()
			case "close":
				p.Close()
			}
			return nil
		}

		// 几何持久化。
		if path, err := windowstate.DefaultPath(appIdent); err == nil {
			_ = windowstate.Track(s, "main", windowstate.NewFileStore(path))
		}

		// 关闭主窗口：按设置隐藏到托盘或退出（对齐 MinCode WindowClosing）。
		// 系统关闭按钮和 IPC 关闭请求使用同一策略。
		closeMain := func() {
			if misc.MinimizeToTrayEnabled() {
				s.Hide()
				return
			}
			app.Quit()
		}
		s.OnClose(func() bool {
			if quitting.Load() {
				return true
			}
			closeMain()
			return false
		})
		native.SetCloseRequest(s, closeMain)

		s.OnFilesDropped(func(paths []string, x, y int) { handleFileDrop(s, h, paths, x, y) })

		trayCtl.install()
		appMenuCtl.install()

		// 后端域启动放在窗口创建之后（对齐 MinCode）。
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := backend.Startup(ctx); err != nil {
				log.Printf("backend startup completed with plugin errors: %v", err)
			}
		}()
	})

	// 退出前给前端一个短宽限刷新防抖 UI 状态（对齐 MinCode ShouldQuit）。
	app.OnLifecycle(func(ev tango.Lifecycle) {
		if ev != tango.Terminating {
			return
		}
		if quitting.Load() {
			return
		}
		quitting.Store(true)
		h.Emit("app:before-quit", nil)
	})

	// 系统让应用打开文件或链接：文件关联、Finder/资源管理器的“打开方式”、拖到程序坞图标、
	// 点击 mincode:// 或 magnet: 等链接。冷启动时的启动参数和 macOS 的打开事件在窗口创建前
	// 就会到达；之后再次启动时，single 把那次的参数转给本进程，也走这里。
	//   - 文件：插件的外部打开处理器能认领的（种子、电子书……）交给插件，其余进 appopen
	//     队列——前端挂载后会取走冷启动时排队的文件，运行中收到 app-open-files 事件；
	//   - 链接：插件处理的协议交给插件（前端经 plugin:external-open 让用户确认），
	//     mincode:// 深链解析成导航目标。
	app.OnOpenFiles(func(paths []string) {
		log.Printf("open files requested by the system: %d", len(paths))
		if added := appopen.Enqueue(plugin.OfferExternalFiles(paths)...); len(added) > 0 {
			h.Emit("app-open-files", map[string]any{"files": added})
			showMain()
		}
	})
	app.OnOpenURL(func(raw string) {
		log.Printf("open link requested by the system: %s", strings.SplitN(raw, "?", 2)[0])
		if plugin.EnqueueExternalURI(raw) {
			showMain()
			return
		}
		target, err := desktop.ParseDeepLink(raw)
		if err != nil {
			log.Printf("ignored unsupported link %q: %v", raw, err)
			return
		}
		navigateTo(target)
	})

	// 冒烟测试写完报告后由它请求退出。
	e2esmoke.SetQuitHandler(func() {
		quitting.Store(true)
		app.Quit()
	})

	err := app.Run()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 8*time.Second)
	backend.Shutdown(shutdownCtx)
	cancelShutdown()
	if err != nil {
		log.Fatal(err)
	}
}

// frontendFS 选择前端来源：TANGO_FRONTEND_DIST 磁盘目录（开发）或嵌入产物。
func frontendFS() fs.FS {
	if dir := os.Getenv("TANGO_FRONTEND_DIST"); dir != "" {
		return os.DirFS(dir)
	}
	sub, err := fs.Sub(frontendDist, "frontend/dist")
	if err != nil {
		log.Fatalf("frontend embed: %v", err)
	}
	return sub
}

// handleFileDrop 对齐 MinCode 的原生文件拖放：目录作为本地项目打开，
// 文件交给项目编辑区（白名单目标）。插件文件拖放暂不支持。
func handleFileDrop(win *tango.Surface, h *host.Host, paths []string, x, y int) {
	if len(paths) == 0 {
		return
	}
	go func() {
		for _, p := range paths {
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				h.Emit("native-files-dropped", map[string]any{
					"files": paths, "targetId": "app-project-drop-target",
				})
				return
			}
		}
		h.Emit("native-files-dropped", map[string]any{
			"files": paths, "targetId": "project-editor-drop-target",
		})
	}()
}
