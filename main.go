// MinCode 的 tango WebView 宿主：用 tango 的系统 WebView 承载 MinCode 的 Vue 前端，
// 页面经 tango ipc（@tangoui/runtime）调用同一套 Go 后端（backend.DispatchContext），
// 接线见 internal/host。
package main

import (
	"context"
	"crypto/sha256"
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

	"mincode/app/internal/host"

	"mincode/app/backend"
	"mincode/app/backend/core"
	"mincode/app/backend/desktop"
	"mincode/app/backend/e2esmoke"
	"mincode/app/backend/misc"
	"mincode/app/backend/terminalactivity"
	"mincode/app/backend/updater"

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

const appID = updater.AppID

// appIdentity chooses the native identity. macOS WebKit falls back to the
// process name for a bare executable, so use the build namespace in that case.
// Packaged apps use the bundle ID and keep their display name.
func appIdentity() (name, id string) {
	name, id = "MinCode", appID
	if runtime.GOOS == "darwin" {
		exe, _ := os.Executable()
		binaryDir := filepath.Dir(exe)
		contents := filepath.Dir(binaryDir)
		bundle := filepath.Dir(contents)
		if filepath.Base(binaryDir) != "MacOS" || filepath.Base(contents) != "Contents" || filepath.Ext(bundle) != ".app" {
			name = core.AppDataNamespace
		}
	}
	appNameOverride := os.Getenv("MINCODE_APP_NAME")
	dataOverride := core.UserDataDirOverride()
	if override := appNameOverride; override != "" && !strings.EqualFold(override, "MinCode") {
		name = override
	} else if dataOverride != "" && runtime.GOOS == "darwin" {
		hash := sha256.Sum256([]byte(dataOverride))
		name = fmt.Sprintf("%s.e2e.%x", core.AppDataNamespace, hash[:8])
	}
	if v := os.Getenv("MINCODE_APP_ID"); v != "" {
		id = v
	} else if appNameOverride != "" || dataOverride != "" {
		hash := sha256.Sum256([]byte(appNameOverride + "\x00" + dataOverride))
		id = fmt.Sprintf("%s.e2e.%x", appID, hash[:8])
	}
	return name, id
}

var urlSchemes = []string{"mincode"}

func mustReadStdin() []byte {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return nil
	}
	return data
}

func main() {
	if terminalactivity.IsHookMode(os.Args[1:]) {
		if err := terminalactivity.RunHook(mustReadStdin()); err != nil {
			os.Exit(2)
		}
		return
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
	shortcuts := newGlobalShortcuts(app, showMain)
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
		SetGlobalShortcut: shortcuts.setHost,
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
		native.AddSurface(h, s, "main")
		s.OnClosed(func() { native.RemoveSurface(h, s) })
		updater.Confirm(app.Quit)
		if e2esmoke.Enabled() {
			// 冒烟测试与用户共用桌面：窗口被遮挡时也保持渲染（否则 rAF 暂停，计时失真）。
			if err := e2esmoke.KeepRenderingWhenCovered(s.Native()); err != nil {
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

		s.OnFilesDropped(func(paths []string, _, _ int) {
			h.Emit("native-files-dropped", map[string]any{"files": paths})
		})

		trayCtl.install()
		appMenuCtl.install()

		// 后端域启动放在窗口创建之后（对齐 MinCode）。
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := backend.Startup(ctx); err != nil {
				log.Printf("backend startup failed: %v", err)
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

	app.OnOpenURL(func(raw string) {
		log.Printf("open link requested by the system: %s", strings.SplitN(raw, "?", 2)[0])
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
