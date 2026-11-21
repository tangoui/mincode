//go:build windows

package browser

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
	"mincode/app/backend/core"
)

const (
	browserWorkMessage             = 0x8000 + 87
	browserDefaultTab              = "browser:default"
	browserMountTimeout            = 20 * time.Second
	iidBrowserEnvironmentCompleted = "{4E8A3389-C9D8-4BD2-B6B5-124FEE6CC14D}"
	iidBrowserControllerCompleted  = "{6C4819F3-C9B7-4260-8127-C9F5BDE7F68C}"
	iidBrowserScriptCompleted      = "{49511172-CC67-4BCA-9923-137112F4C4CC}"
	iidBrowserNavigationCompleted  = "{D33A35BF-1C49-4F98-93AB-006E0533FE1C}"
	iidBrowserCaptureCompleted     = "{697E05E9-3D8F-45FA-96F4-8FFE1EDEDAF5}"
)

type windowsBrowserTab struct {
	host, container    uintptr
	id, url, userAgent string
	controller, view   *browserCOM
	rect               browserRect
	visible, closed    bool
	deadline           time.Time
	mountDone          func(error)
	events             []browserEvent
	navigationErrors   []map[string]any
	// generation 标记 controller 所属的 environment；failure 非空表示浏览器进程已退出，
	// 原生对象已释放，条目只保留错误记录，等待前端重新挂载或关闭。
	generation               uint64
	failure                  error
	retried                  bool
	popup                    bool
	popupArgs, popupDeferral *browserCOM
	openerID, popupURL       string
	dialogs                  map[string]*windowsPageDialog
	fileChooser              *windowsFileChooser
	downloads                []*windowsDownload
	downloadAuthorizedUntil  time.Time
}

// Dialog arguments and deferrals belong to the browser STA, never the host UI.
type windowsPageDialog struct {
	args, deferral *browserCOM
	kind           string
}
type browserEvent struct {
	slot  int
	token int64
	owner *browserCOM // Optional derived interface, retained until unregistration.
}
type windowsBrowserHost struct {
	tabs   map[string]*windowsBrowserTab
	active string
	hidden bool
}
type windowsBrowserRuntime struct {
	thread             uint32
	dispatcher         uintptr
	mu                 sync.Mutex
	queue              []func()
	ready              chan struct{}
	err                error
	hosts              map[uintptr]*windowsBrowserHost
	environment        *browserCOM
	environmentPending bool
	generation         uint64
	waiting            []*windowsBrowserTab
}

// errBrowserProcessExited 是浏览器进程退出后旧 tab 返回给 UI/AI 的可操作错误。
var errBrowserProcessExited = errors.New("WebView2 browser process exited; reopen the embedded preview to restart it")

var windowsBrowserOnce sync.Once
var windowsBrowser windowsBrowserRuntime

func browserRuntime() (*windowsBrowserRuntime, error) {
	windowsBrowserOnce.Do(func() {
		windowsBrowser.ready = make(chan struct{})
		go windowsBrowser.run()
	})
	<-windowsBrowser.ready
	return &windowsBrowser, windowsBrowser.err
}
func (r *windowsBrowserRuntime) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	restoreDPI := browserPhysicalDPIContext()
	defer restoreDPI()
	hr, _, _ := browserOle32.NewProc("CoInitializeEx").Call(0, 2)
	if r.err = browserHRESULT(hr); r.err != nil {
		close(r.ready)
		return
	}
	defer browserOle32.NewProc("CoUninitialize").Call()
	r.hosts = make(map[uintptr]*windowsBrowserHost)
	r.thread = windows.GetCurrentThreadId()
	var msg browserMessage
	browserPeekMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 0)
	r.dispatcher, r.err = browserCreateDispatcher()
	if r.err != nil {
		close(r.ready)
		return
	}
	defer browserDestroyWindow.Call(r.dispatcher)
	stopInputHooks, hookErr := browserInstallInputHooks()
	if hookErr != nil {
		r.err = hookErr
		close(r.ready)
		return
	}
	defer stopInputHooks()
	// 定时回收被销毁宿主及超时初始化，不依赖调用方继续发送命令。
	if timer, _, err := browserSetTimer.Call(0, 0, 250, 0); timer == 0 {
		r.err = err
		close(r.ready)
		return
	}
	close(r.ready)
	for {
		result, _, _ := browserGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) <= 0 {
			return
		}
		// COM/WebView2 can run a nested message loop which consumes posted
		// thread messages without dispatching them. The timer is a second wake
		// source so a queued operation cannot be stranded indefinitely.
		if msg.Window == 0 && (msg.Message == browserWorkMessage || msg.Message == 0x0113) {
			r.drain()
		} else {
			r.noteNativeInput(&msg)
			browserTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			browserDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
		}
		// Drain only after DispatchMessage has unwound to the outer loop.
		// The periodic thread timer also wakes queued work if a nested COM
		// pump consumed its dispatcher message.
		r.drain()
		r.reap()
	}
}

func (r *windowsBrowserRuntime) noteNativeInput(msg *browserMessage) {
	if msg == nil || (msg.Message != 0x0201 && msg.Message != 0x0204 && msg.Message != 0x0100 && msg.Message != 0x0104) {
		return
	}
	for _, host := range r.hosts {
		for _, tab := range host.tabs {
			if tab.container != 0 && (msg.Window == tab.container || browserChildWindow(tab.container, msg.Window)) {
				noteUserInput(tab.id)
			}
		}
	}
}
func (r *windowsBrowserRuntime) post(fn func()) error {
	r.mu.Lock()
	r.queue = append(r.queue, fn)
	r.mu.Unlock()
	ok, _, err := browserPostMessage.Call(r.dispatcher, browserWorkMessage, 0, 0)
	if ok == 0 {
		return fmt.Errorf("dispatch browser operation: %w", err)
	}
	return nil
}
func (r *windowsBrowserRuntime) drain() {
	r.mu.Lock()
	work := r.queue
	r.queue = nil
	r.mu.Unlock()
	for _, fn := range work {
		fn()
	}
}
func (r *windowsBrowserRuntime) reap() {
	waiting := r.waiting[:0]
	for _, tab := range r.waiting {
		if !tab.closed {
			waiting = append(waiting, tab)
		}
	}
	r.waiting = waiting
	for hwnd, host := range r.hosts {
		for _, tab := range host.tabs {
			r.pollDownloads(tab)
		}
		if !browserValidHost(hwnd) {
			host.active = ""
		}
		for _, tab := range host.tabs {
			if !browserValidHost(hwnd) || (tab.mountDone != nil && time.Now().After(tab.deadline)) {
				r.closeTab(tab, errors.New("browser host closed or initialization timed out"))
			}
		}
		if !browserValidHost(hwnd) || len(host.tabs) == 0 {
			delete(r.hosts, hwnd)
		}
	}
	if len(r.hosts) == 0 && !r.environmentPending && r.environment != nil {
		r.environment.release()
		r.environment = nil
	}
}
func browserDispatch(timeout time.Duration, fn func(*windowsBrowserRuntime, func(string))) (string, error) {
	if win := hostWindow(); win != nil && browserOnHostUIThread(uintptr(win)) {
		return "", errors.New("embedded browser operations must run outside the host UI thread")
	}
	r, err := browserRuntime()
	if err != nil {
		return "", err
	}
	if windows.GetCurrentThreadId() == r.thread {
		return "", errors.New("cannot synchronously wait on the browser STA thread")
	}
	result := make(chan string, 1)
	var cancelled atomic.Bool
	err = r.post(func() {
		if !cancelled.Load() {
			fn(r, func(value string) {
				select {
				case result <- value:
				default:
				}
			})
		}
	})
	if err != nil {
		return "", err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case value := <-result:
		return value, nil
	case <-timer.C:
		cancelled.Store(true)
		return "", errors.New("embedded browser operation timed out")
	}
}
func browserJSON(value any) string { data, _ := json.Marshal(value); return string(data) }
func browserFailure(err error) string {
	return browserJSON(map[string]any{"ok": false, "error": err.Error()})
}
func browserTimeout(ms int) time.Duration {
	if ms <= 0 {
		ms = 3000
	}
	if ms > 30000 {
		ms = 30000
	}
	return time.Duration(ms) * time.Millisecond
}
func browserTabID(id string) string {
	if id == "" {
		return browserDefaultTab
	}
	return id
}
func (r *windowsBrowserRuntime) tab(hwnd uintptr, id string) *windowsBrowserTab {
	host := r.hosts[hwnd]
	if host == nil {
		return nil
	}
	if id == "" {
		id = host.active
	}
	return host.tabs[browserTabID(id)]
}
func browserBounds(hwnd uintptr, x, y, w, h float64) (browserRect, error) {
	for _, value := range []float64{x, y, w, h} {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 1e7 {
			return browserRect{}, errors.New("invalid browser bounds")
		}
	}
	if x < 0 || y < 0 || w <= 0 || h <= 0 {
		return browserRect{}, errors.New("invalid browser bounds")
	}
	scale := browserDPIScale(hwnd)
	return browserRect{int32(math.Round(x * scale)), int32(math.Round(y * scale)), int32(math.Round((x + w) * scale)), int32(math.Round((y + h) * scale))}, nil
}
func browserPositionTab(tab *windowsBrowserTab) error {
	rect := tab.rect
	ok, _, err := browserSetWindowPos.Call(tab.container, 0, uintptr(rect.Left), uintptr(rect.Top), uintptr(rect.Right-rect.Left), uintptr(rect.Bottom-rect.Top), 0x0010)
	if ok == 0 {
		return fmt.Errorf("position browser child window: %w", err)
	}
	return browserPutBounds(tab.controller, browserRect{0, 0, rect.Right - rect.Left, rect.Bottom - rect.Top})
}
func browserNavigationAllowed(raw string) bool {
	return raw == "about:blank" || validateNavigationURL(raw) == nil
}
func platformAvailable() bool {
	if runtime.GOARCH != "amd64" {
		return false
	}
	version, err := webviewloader.GetInstalledVersion()
	return err == nil && version != ""
}
func platformMount(win unsafe.Pointer, id, url, ua string, x, y, w, h float64) error {
	if runtime.GOARCH != "amd64" {
		return errors.New("embedded browser currently requires Windows x64; ARM64 support is not yet verified")
	}
	hwnd := uintptr(win)
	if !browserValidHost(hwnd) {
		return errors.New("embedded browser requires a live MinCode host window")
	}
	if url != "" && !browserNavigationAllowed(url) {
		return errors.New("embedded browser URL is not allowed")
	}
	if strings.ContainsRune(ua, 0) {
		return errors.New("invalid browser user agent")
	}
	rect, err := browserBounds(hwnd, x, y, w, h)
	if err != nil {
		return err
	}
	id = browserTabID(id)
	created := &windowsBrowserTab{host: hwnd, id: id, url: url, userAgent: ua, rect: rect, visible: true, deadline: time.Now().Add(browserMountTimeout), navigationErrors: make([]map[string]any, 0)}
	value, err := browserDispatch(browserMountTimeout, func(r *windowsBrowserRuntime, done func(string)) {
		host := r.hosts[hwnd]
		if host == nil {
			host = &windowsBrowserHost{tabs: make(map[string]*windowsBrowserTab)}
			r.hosts[hwnd] = host
		}
		if tab := host.tabs[id]; tab != nil && tab.failure != nil {
			// 浏览器进程已退出的 tab 重新挂载时创建新的 controller，旧错误不再沿用。
			r.closeTab(tab, tab.failure)
		}
		if tab := host.tabs[id]; tab != nil {
			if tab.view == nil || tab.mountDone != nil {
				done(browserFailure(errors.New("browser tab is still initializing")))
				return
			}
			tab.rect = rect
			tab.visible = true
			err := browserPositionTab(tab)
			if err == nil {
				r.activate(hwnd, id)
				if url != "" && url != tab.url {
					err = r.navigate(tab, url)
				}
			}
			if err = r.nativeFailed(tab, err); err == nil {
				done(`{"ok":true}`)
				return
			}
			if tab.failure == nil {
				done(browserFailure(err))
				return
			}
			// 浏览器进程刚好退出：丢弃旧 tab，下面用新 environment 重新创建。
			r.closeTab(tab, tab.failure)
		}
		tab := created
		tab.mountDone = func(err error) {
			if err != nil {
				done(browserFailure(err))
			} else {
				done(`{"ok":true}`)
			}
		}
		host.tabs[id] = tab
		host.active = id
		host.hidden = false
		r.ensureEnvironment(tab)
	})
	if err != nil {
		// 取消也在 STA 排队，晚到的 controller 回调通过 closed 标记释放资源。
		if r, e := browserRuntime(); e == nil {
			_ = r.post(func() {
				if created != nil {
					r.closeTab(created, err)
				}
			})
		}
		return err
	}
	var result evalResult
	if e := json.Unmarshal([]byte(value), &result); e != nil {
		return e
	}
	if !result.OK {
		return errors.New(result.Error)
	}
	return nil
}

// browserProfileFolder 是所有内嵌浏览器共享的 WebView2 用户数据目录。
func browserProfileFolder() (string, error) {
	folder, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	folder = filepath.Join(folder, "MinCode", "EmbeddedBrowser")
	return folder, os.MkdirAll(folder, 0700)
}
func (r *windowsBrowserRuntime) ensureEnvironment(tab *windowsBrowserTab) {
	if r.environment != nil {
		r.createController(tab)
		return
	}
	r.waiting = append(r.waiting, tab)
	if r.environmentPending {
		return
	}
	r.environmentPending = true
	if os.Getenv("WEBVIEW2_USER_DATA_FOLDER") != "" || os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS") != "" || os.Getenv("WEBVIEW2_PIPE_FOR_SCRIPT_DEBUGGER") != "" {
		r.environmentReady(nil, errors.New("WebView2 environment overrides must not replace the isolated MinCode profile or enable external browser control"))
		return
	}
	folder, err := browserProfileFolder()
	if err != nil {
		r.environmentReady(nil, err)
		return
	}
	path, err := windows.UTF16PtrFromString(folder)
	if err != nil {
		r.environmentReady(nil, err)
		return
	}
	cb := newBrowserCallback(iidBrowserEnvironmentCompleted, false, func(hr uintptr, env unsafe.Pointer) uintptr {
		err := browserHRESULT(hr)
		p := (*browserCOM)(env)
		if err == nil && p == nil {
			err = errors.New("WebView2 returned no environment")
		}
		if err == nil {
			p.retain()
		} else {
			p = nil
		}
		r.environmentReady(p, err)
		return 0
	})
	hr, err := webviewloader.CreateCoreWebView2EnvironmentWithOptions(nil, path, 0, cb.pointer())
	cb.release()
	runtime.KeepAlive(path)
	if err == nil {
		err = browserHRESULT(hr)
	}
	if err != nil {
		r.environmentReady(nil, err)
	}
}
func (r *windowsBrowserRuntime) environmentReady(env *browserCOM, err error) {
	r.environmentPending = false
	r.environment = env
	if env != nil {
		r.generation++
	}
	waiting := r.waiting
	r.waiting = nil
	for _, tab := range waiting {
		if tab.closed {
			continue
		}
		if err != nil {
			r.closeTab(tab, err)
		} else {
			r.createController(tab)
		}
	}
}
func (r *windowsBrowserRuntime) createController(tab *windowsBrowserTab) {
	if tab.closed {
		return
	}
	class, _ := windows.UTF16PtrFromString("STATIC")
	container, _, createErr := browserCreateWindow.Call(0, uintptr(unsafe.Pointer(class)), 0, 0x40000000|0x02000000|0x04000000, 0, 0, 1, 1, tab.host, 0, 0, 0)
	if container == 0 {
		r.closeTab(tab, fmt.Errorf("create browser child window: %w", createErr))
		return
	}
	tab.container = container
	tab.generation = r.generation
	cb := newBrowserCallback(iidBrowserControllerCompleted, false, func(hr uintptr, controller unsafe.Pointer) uintptr {
		p := (*browserCOM)(controller)
		err := browserHRESULT(hr)
		if err == nil && p == nil {
			err = errors.New("WebView2 returned no controller")
		}
		if !tab.closed && tab.container != container {
			// 已为新的 environment 重新创建过 controller，这是旧进程迟到的结果。
			if p != nil {
				_, _ = p.call(24)
			}
			return 0
		}
		if tab.closed || !browserValidHost(tab.host) || time.Now().After(tab.deadline) {
			if p != nil {
				_, _ = p.call(24)
			}
			r.closeTab(tab, errors.New("browser initialization cancelled"))
			return 0
		}
		if err != nil {
			if browserProcessGone(err) {
				// 回调内不释放 environment，回到外层消息循环再重建。
				generation := tab.generation
				if e := r.post(func() { r.restartController(tab, generation, err) }); e != nil {
					r.closeTab(tab, err)
				}
				return 0
			}
			r.closeTab(tab, err)
			return 0
		}
		p.retain()
		tab.controller = p
		tab.view, err = p.object(25)
		if err != nil {
			r.closeTab(tab, err)
			return 0
		}
		// Return from controller completion before configuring it, and wait for
		// asynchronous document-script registration before the first navigation.
		if err := r.post(func() {
			if tab.closed {
				return
			}
			if err := r.configure(tab); err != nil {
				r.closeTab(tab, err)
				return
			}
			cb := newBrowserCallback("{B99369F3-9B11-47B5-BC6F-8E7895FCEA17}", false, func(hr uintptr, _ unsafe.Pointer) uintptr {
				completionErr := browserHRESULT(hr)
				if err := r.post(func() {
					if completionErr != nil {
						r.finishMount(tab, completionErr)
						return
					}
					r.configureFileChooser(tab, func(err error) { r.finishMount(tab, err) })
				}); err != nil {
					r.closeTab(tab, err)
				}
				return 0
			})
			err := tab.view.stringArg(27, browserWindowsDiagnostics, cb.pointer())
			cb.release()
			if err != nil {
				r.closeTab(tab, err)
			}
		}); err != nil {
			r.closeTab(tab, err)
		}
		return 0
	})
	_, err := r.environment.call(3, tab.container, cb.pointer())
	cb.release()
	if err != nil {
		if browserProcessGone(err) {
			r.restartController(tab, tab.generation, err)
			return
		}
		r.closeTab(tab, err)
	}
}

// restartController 在 environment 所属浏览器进程已退出时作废它，并为仍在挂载的
// tab 重新创建一次 environment 和 controller；第二次仍失败则如实报错。
func (r *windowsBrowserRuntime) restartController(tab *windowsBrowserTab, generation uint64, cause error) {
	if generation == r.generation && r.environment != nil {
		// Invalidation retries all in-flight mounts, including this one. Do not
		// continue here and accidentally retry the same controller twice.
		r.browserProcessExited(generation)
		return
	}
	if tab.closed {
		return
	}
	if tab.popup || tab.retried || time.Now().After(tab.deadline) {
		r.closeTab(tab, fmt.Errorf("%w (%v)", errBrowserProcessExited, cause))
		return
	}
	tab.retried = true
	r.releaseNative(tab)
	r.ensureEnvironment(tab)
}

// browserProcessExited 处理 ProcessFailed kind 0（浏览器进程退出）或等价 HRESULT：
// 释放该代 environment，使下一次挂载新建浏览器进程；已挂载 tab 释放原生对象但保留
// 条目与错误，前端/AI 调用得到可操作的错误，重新挂载时恢复。重复通知只处理一次。
func (r *windowsBrowserRuntime) browserProcessExited(generation uint64) {
	if generation == 0 || generation != r.generation || r.environment == nil {
		return
	}
	r.environment.release()
	r.environment = nil
	var restart []*windowsBrowserTab
	for _, host := range r.hosts {
		for _, tab := range host.tabs {
			if tab.closed || tab.generation != generation || tab.failure != nil {
				continue
			}
			if tab.mountDone != nil {
				// 正在挂载的 tab 在死进程上的 CreateController 可能永远不回调。
				restart = append(restart, tab)
				continue
			}
			tab.failure = errBrowserProcessExited
			r.recordError(tab, tab.url, 0, errBrowserProcessExited.Error())
			r.releaseNative(tab)
		}
	}
	for _, tab := range restart {
		r.restartController(tab, generation, errBrowserProcessExited)
	}
}

// liveTab 返回可调用原生对象的 tab，进程退出的 tab 返回保留的错误。
func (r *windowsBrowserRuntime) liveTab(hwnd uintptr, id string) (*windowsBrowserTab, error) {
	tab := r.tab(hwnd, id)
	if tab != nil && tab.failure != nil {
		return nil, tab.failure
	}
	if tab == nil || tab.view == nil {
		return nil, errors.New("browser tab is not mounted")
	}
	return tab, nil
}

// nativeFailed 将调用中暴露的浏览器进程退出转换为统一恢复流程。
func (r *windowsBrowserRuntime) nativeFailed(tab *windowsBrowserTab, err error) error {
	if err == nil || !browserProcessGone(err) {
		return err
	}
	r.browserProcessExited(tab.generation)
	if tab.failure != nil {
		return tab.failure
	}
	return errBrowserProcessExited
}
func (r *windowsBrowserRuntime) finishMount(tab *windowsBrowserTab, err error) {
	if tab.closed {
		return
	}
	if err == nil && (!browserValidHost(tab.host) || time.Now().After(tab.deadline)) {
		err = errors.New("browser initialization cancelled")
	}
	if err == nil {
		err = browserPositionTab(tab)
	}
	if err != nil {
		r.closeTab(tab, err)
		return
	}
	if host := r.hosts[tab.host]; host != nil && host.active == tab.id && !host.hidden && tab.visible {
		r.activate(tab.host, tab.id)
	} else {
		_, _ = tab.controller.call(4, 0)
	}
	if tab.popupArgs != nil {
		_, err := tab.popupArgs.call(4, uintptr(unsafe.Pointer(tab.view)))
		_, completionErr := tab.popupDeferral.call(3)
		tab.popupArgs.release()
		tab.popupDeferral.release()
		tab.popupArgs, tab.popupDeferral = nil, nil
		if err == nil {
			err = completionErr
		}
		if err != nil {
			r.closeTab(tab, err)
			return
		}
		core.Emit(popupOpenedEvent, popupOpened{TabID: tab.id, OpenerTabID: tab.openerID, URL: tab.popupURL})
	} else if tab.url != "" {
		if err := r.navigate(tab, tab.url); err != nil {
			r.closeTab(tab, err)
			return
		}
	}
	if tab.mountDone != nil {
		tab.mountDone(nil)
		tab.mountDone = nil
	}
}

func (r *windowsBrowserRuntime) register(tab *windowsBrowserTab, slot int, iid string, fn func(uintptr, unsafe.Pointer) uintptr) error {
	cb := newBrowserCallback(iid, false, fn)
	var token int64
	_, err := tab.view.call(slot, cb.pointer(), uintptr(unsafe.Pointer(&token)))
	cb.release()
	if err == nil {
		tab.events = append(tab.events, browserEvent{slot: slot + 1, token: token})
	}
	return err
}
func (r *windowsBrowserRuntime) configure(tab *windowsBrowserTab) error {
	settings, err := tab.view.object(3)
	if err != nil {
		return err
	}
	defer settings.release()
	// No page-to-host messages, browser context menus or COM host objects.
	for _, slot := range []int{6, 8, 14, 16, 20} {
		if _, err = settings.call(slot, 0); err != nil {
			return err
		}
	}
	if tab.userAgent != "" {
		settings2, e := settings.query("{EE9A0F68-F46C-4E32-AC23-EF8CAC224D2A}")
		if e != nil {
			return fmt.Errorf("custom user agent: %w", e)
		}
		err = settings2.stringArg(22, tab.userAgent)
		settings2.release()
		if err != nil {
			return err
		}
	}
	if err = r.register(tab, 7, "{9ADBE429-F36D-432B-9DDC-F8881FBD76E3}", func(_ uintptr, args unsafe.Pointer) uintptr {
		p := (*browserCOM)(args)
		url, _ := p.text(3)
		r.cancelFileChooser(tab)
		tab.navigationErrors = tab.navigationErrors[:0]
		if !pageNavigationAllowed(url, true) || strings.HasPrefix(strings.ToLower(url), "data:") {
			_, _ = p.call(8, 1)
			r.recordNavigationRefusal(tab, url)
		} else {
			var redirected int32
			_, _ = p.call(5, uintptr(unsafe.Pointer(&redirected)))
			if redirected == 0 {
				tab.downloadAuthorizedUntil = time.Time{}
				if admitDownload(tab.id) {
					tab.downloadAuthorizedUntil = time.Now().Add(30 * time.Second)
				}
			}
		}
		return 0
	}); err != nil {
		return err
	}
	if err = r.register(tab, 15, iidBrowserNavigationCompleted, func(_ uintptr, args unsafe.Pointer) uintptr {
		p := (*browserCOM)(args)
		var success int32
		var status int32
		_, _ = p.call(3, uintptr(unsafe.Pointer(&success)))
		_, _ = p.call(4, uintptr(unsafe.Pointer(&status)))
		if source, e := tab.view.text(4); e == nil {
			tab.url = source
		}
		if success == 0 && status != 14 { // OPERATION_CANCELED: superseded navigation, not a failed page.
			r.recordError(tab, tab.url, int(status), fmt.Sprintf("WebView2 navigation failed (%d)", status))
		}
		return 0
	}); err != nil {
		return err
	}
	if err = r.register(tab, 25, "{79E0AEA4-990B-42D9-AA1D-0FCC2E5BC7F1}", func(_ uintptr, args unsafe.Pointer) uintptr {
		var kind int32
		_, _ = (*browserCOM)(args).call(3, uintptr(unsafe.Pointer(&kind)))
		if kind >= 0 && kind <= 3 {
			r.recordError(tab, tab.url, int(kind), fmt.Sprintf("WebView2 browser/render process failed (%d); close and reopen the preview", kind))
		}
		if kind == 0 {
			// BROWSER_PROCESS_EXITED：该代 environment 已不能创建 controller。事件回调内
			// 不能注销事件或释放 controller，回到外层消息循环后统一处理。
			generation := tab.generation
			_ = r.post(func() { r.browserProcessExited(generation) })
		}
		return 0
	}); err != nil {
		return err
	}
	if err = r.register(tab, 23, "{15E1C6A3-C72A-4DF3-91D7-D097FBEC6BFD}", func(_ uintptr, args unsafe.Pointer) uintptr {
		// 网页不能静默获得摄像头、麦克风、定位或剪贴板能力。
		_, _ = (*browserCOM)(args).call(7, 2)
		return 0
	}); err != nil {
		return err
	}
	if err := r.register(tab, 17, "{E79908BF-2D5D-4968-83DB-263FEA2C1DA3}", func(_ uintptr, args unsafe.Pointer) uintptr {
		p := (*browserCOM)(args)
		address, _ := p.text(3)
		r.cancelFileChooser(tab)
		if !pageNavigationAllowed(address, false) {
			_, _ = p.call(8, 1)
			r.recordNavigationRefusal(tab, address)
		}
		return 0
	}); err != nil {
		return err
	}
	if err := r.register(tab, 21, "{EF381BF9-AFA8-4E37-91C4-8AC48524BDFB}", func(_ uintptr, args unsafe.Pointer) uintptr {
		r.openPageDialog(tab, (*browserCOM)(args))
		return 0
	}); err != nil {
		return err
	}
	if err := r.configureDownloads(tab); err != nil {
		return err
	}
	if err := r.register(tab, 59, "{5C19E9E0-092F-486B-AFFA-CA8231913039}", func(_ uintptr, _ unsafe.Pointer) uintptr {
		if tab.popup {
			_ = r.post(func() {
				if !tab.closed {
					popupClosed(tab.id)
					r.closeTab(tab, errors.New("window.close"))
				}
			})
		}
		return 0
	}); err != nil {
		return err
	}
	return r.register(tab, 44, "{D4C185FE-C81C-4989-97AF-2D3FA7AB5651}", func(_ uintptr, args unsafe.Pointer) uintptr {
		r.openPopup(tab, (*browserCOM)(args))
		return 0
	})
}
func (r *windowsBrowserRuntime) recordError(tab *windowsBrowserTab, url string, code int, message string) {
	tab.navigationErrors = append(tab.navigationErrors, map[string]any{"timestamp": time.Now().UnixMilli(), "url": url, "code": code, "domain": "WebView2", "message": message})
	if len(tab.navigationErrors) > 100 {
		tab.navigationErrors = tab.navigationErrors[len(tab.navigationErrors)-100:]
	}
}
func (r *windowsBrowserRuntime) navigate(tab *windowsBrowserTab, url string) error {
	if !browserNavigationAllowed(url) {
		r.recordError(tab, url, -1, "Navigation URL is not allowed")
		return errors.New("Navigation URL is not allowed")
	}
	tab.navigationErrors = tab.navigationErrors[:0]
	if err := tab.view.stringArg(5, url); err != nil {
		r.recordError(tab, url, -1, err.Error())
		return err
	}
	tab.url = url
	return nil
}
func (r *windowsBrowserRuntime) activate(hwnd uintptr, id string) {
	host := r.hosts[hwnd]
	if host == nil || host.tabs[id] == nil {
		return
	}
	host.active = id
	host.hidden = false
	for key, tab := range host.tabs {
		if key != id {
			r.cancelFileChooser(tab)
			r.cancelPendingDownloadPickers(tab)
			forgetPopupAllowances(tab.id); forgetFileChooserAllowances(tab.id); forgetDownloadAllowances(tab.id)
			tab.downloadAuthorizedUntil = time.Time{}
		}
		if tab.controller != nil {
			visible := uintptr(0)
			if key == id && tab.visible {
				visible = 1
			}
			_, _ = tab.controller.call(4, visible)
			browserShowWindow.Call(tab.container, visible*4) // SW_SHOWNOACTIVATE: do not synchronously activate a cross-thread parent.
			if visible != 0 {
				_ = browserPositionTab(tab)
			}
			_, _ = tab.controller.call(23)
		}
	}
}
func (r *windowsBrowserRuntime) closeTab(tab *windowsBrowserTab, reason error) {
	if tab.closed {
		return
	}
	tab.closed = true
	if tab.mountDone != nil {
		tab.mountDone(reason)
		tab.mountDone = nil
	}
	r.releaseNative(tab)
	if host := r.hosts[tab.host]; host != nil && host.tabs[tab.id] == tab {
		delete(host.tabs, tab.id)
		if host.active == tab.id {
			// The frontend explicitly activates the next tab. Closing a hidden
			// workspace must never reveal another preview behind its dialog.
			host.active = ""
		}
	}
}

// releaseNative 注销事件并释放 tab 的 WebView2 对象和子窗口，不改动 tab 条目本身。
func (r *windowsBrowserRuntime) releaseNative(tab *windowsBrowserTab) {
	for id, d := range tab.dialogs {
		delete(tab.dialogs, id)
		_ = finishWindowsPageDialog(d, d.kind == dialogKindAlert, "")
	}
	cancelDialogsForTab(tab.id)
	forgetPopupAllowances(tab.id)
	forgetFileChooserAllowances(tab.id)
	forgetDownloadAllowances(tab.id)
	r.cancelDownloads(tab)
	r.cancelFileChooser(tab)
	if tab.popupArgs != nil {
		_, _ = tab.popupDeferral.call(3)
		tab.popupArgs.release()
		tab.popupDeferral.release()
		tab.popupArgs, tab.popupDeferral = nil, nil
	}
	if tab.view != nil {
		for _, event := range tab.events {
			owner := tab.view
			if event.owner != nil {
				owner = event.owner
			}
			if runtime.GOARCH == "386" {
				_, _ = owner.call(event.slot, uintptr(uint32(event.token)), uintptr(uint32(uint64(event.token)>>32)))
			} else {
				_, _ = owner.call(event.slot, uintptr(event.token))
			}
			if event.owner != nil {
				event.owner.release()
			}
		}
		tab.events = nil
		tab.view.release()
		tab.view = nil
	}
	if tab.controller != nil {
		_, _ = tab.controller.call(24)
		tab.controller.release()
		tab.controller = nil
	}
	if tab.container != 0 {
		browserDestroyWindow.Call(tab.container)
		tab.container = 0
	}
}
func browserDo(win unsafe.Pointer, id string, fn func(*windowsBrowserRuntime, *windowsBrowserTab) error) error {
	value, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		tab, err := r.liveTab(uintptr(win), id)
		if err != nil {
			done(browserFailure(err))
			return
		}
		if err := r.nativeFailed(tab, fn(r, tab)); err != nil {
			done(browserFailure(err))
		} else {
			done(`{"ok":true}`)
		}
	})
	if err != nil {
		return err
	}
	var result evalResult
	if err = json.Unmarshal([]byte(value), &result); err != nil {
		return err
	}
	if !result.OK {
		return errors.New(result.Error)
	}
	return nil
}
func browserTabFailed(win unsafe.Pointer, id string) bool {
	value, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		if tab := r.tab(uintptr(win), id); tab != nil && tab.failure != nil {
			done("true")
		} else {
			done("false")
		}
	})
	return err == nil && value == "true"
}
func platformSetBounds(win unsafe.Pointer, id string, x, y, w, h float64, visible bool) {
	_ = platformSetBoundsResult(win, id, x, y, w, h, visible)
}
func platformSetBoundsResult(win unsafe.Pointer, id string, x, y, w, h float64, visible bool) error {
	if !visible || w < 2 || h < 2 {
		if browserTabFailed(win, id) {
			return nil // 已失效 tab 没有原生窗口，隐藏它不应报错。
		}
		return browserDo(win, id, func(r *windowsBrowserRuntime, tab *windowsBrowserTab) error {
			r.cancelFileChooser(tab)
			r.cancelPendingDownloadPickers(tab)
			tab.downloadAuthorizedUntil = time.Time{}
			forgetPopupAllowances(tab.id); forgetFileChooserAllowances(tab.id); forgetDownloadAllowances(tab.id)
			tab.visible = false
			_, err := tab.controller.call(4, 0)
			browserShowWindow.Call(tab.container, 0)
			return err
		})
	}
	rect, err := browserBounds(uintptr(win), x, y, w, h)
	if err != nil {
		return err
	}
	return browserDo(win, id, func(r *windowsBrowserRuntime, tab *windowsBrowserTab) error {
		tab.rect = rect
		tab.visible = visible
		if err := browserPositionTab(tab); err != nil {
			return err
		}
		r.activate(tab.host, tab.id)
		return nil
	})
}
func platformHide(win unsafe.Pointer) { _ = platformHideResult(win) }
func platformHideResult(win unsafe.Pointer) error {
	_, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		if host := r.hosts[uintptr(win)]; host != nil {
			host.hidden = true
			for _, tab := range host.tabs {
				r.cancelFileChooser(tab)
				r.cancelPendingDownloadPickers(tab)
				tab.downloadAuthorizedUntil = time.Time{}
				forgetPopupAllowances(tab.id); forgetFileChooserAllowances(tab.id); forgetDownloadAllowances(tab.id)
				if tab.controller != nil {
					_, _ = tab.controller.call(4, 0)
					browserShowWindow.Call(tab.container, 0)
				}
			}
		}
		done("")
	})
	return err
}
func platformClose(win unsafe.Pointer) { _ = platformCloseResult(win) }
func platformCloseResult(win unsafe.Pointer) error {
	_, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		if host := r.hosts[uintptr(win)]; host != nil {
			host.active = ""
			for _, tab := range host.tabs {
				r.closeTab(tab, errors.New("browser closed"))
			}
			delete(r.hosts, uintptr(win))
		}
		done("")
	})
	return err
}
func platformCloseTab(win unsafe.Pointer, id string) { _ = platformCloseTabResult(win, id) }
func platformCloseTabResult(win unsafe.Pointer, id string) error {
	_, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		if tab := r.tab(uintptr(win), id); tab != nil {
			r.closeTab(tab, errors.New("browser tab closed"))
		}
		done("")
	})
	return err
}
func platformActivate(win unsafe.Pointer, id string) { _ = platformActivateResult(win, id) }
func platformActivateResult(win unsafe.Pointer, id string) error {
	return browserDo(win, id, func(r *windowsBrowserRuntime, tab *windowsBrowserTab) error {
		tab.visible = true
		r.activate(tab.host, tab.id)
		return nil
	})
}
func platformNavigate(win unsafe.Pointer, id, url string) { _ = platformNavigateResult(win, id, url) }
func platformNavigateResult(win unsafe.Pointer, id, url string) error {
	return browserDo(win, id, func(r *windowsBrowserRuntime, tab *windowsBrowserTab) error { return r.navigate(tab, url) })
}
func platformReload(win unsafe.Pointer, id string) { _ = platformReloadResult(win, id) }
func platformReloadResult(win unsafe.Pointer, id string) error {
	return browserDo(win, id, func(_ *windowsBrowserRuntime, tab *windowsBrowserTab) error {
		tab.navigationErrors = tab.navigationErrors[:0]
		_, err := tab.view.call(31)
		return err
	})
}
func platformSetZoom(win unsafe.Pointer, id string, zoom float64) {
	_ = platformSetZoomResult(win, id, zoom)
}
func platformSetZoomResult(win unsafe.Pointer, id string, zoom float64) error {
	if math.IsNaN(zoom) || math.IsInf(zoom, 0) || zoom < 0.25 || zoom > 5 {
		return errors.New("browser zoom must be between 0.25 and 5")
	}
	return browserDo(win, id, func(_ *windowsBrowserRuntime, tab *windowsBrowserTab) error {
		return browserPutZoom(tab.controller, zoom)
	})
}
func platformGetURL(win unsafe.Pointer, id string) string {
	value, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		tab := r.tab(uintptr(win), id)
		if tab != nil && tab.failure != nil {
			done(tab.url)
			return
		}
		if tab == nil || tab.view == nil {
			done("")
			return
		}
		url, err := tab.view.text(4)
		if err != nil {
			url = tab.url
		}
		done(url)
	})
	if err != nil {
		return ""
	}
	return value
}
func browserEvalScript(script string) string {
	encoded, _ := json.Marshal(script)
	return `(()=>{try{let value=(0,eval)(` + string(encoded) + `);if(value&&typeof value.then==='function')throw new Error('Promise results are not supported; evaluate a synchronous expression');return {ok:true,value:value===undefined?null:value}}catch(error){return {ok:false,error:String(error&&error.message||error)}}})()`
}
func platformEval(win unsafe.Pointer, id, script string, timeout int) string {
	value, err := browserDispatch(browserTimeout(timeout), func(r *windowsBrowserRuntime, done func(string)) {
		tab, err := r.liveTab(uintptr(win), id)
		if err != nil {
			done(browserFailure(err))
			return
		}
		cb := newBrowserCallback(iidBrowserScriptCompleted, false, func(hr uintptr, result unsafe.Pointer) uintptr {
			if err := browserHRESULT(hr); err != nil {
				done(browserFailure(r.nativeFailed(tab, err)))
			} else {
				raw := windows.UTF16PtrToString((*uint16)(result))
				var envelope evalResult
				if json.Unmarshal([]byte(raw), &envelope) != nil || (!envelope.OK && envelope.Error == "") {
					done(browserFailure(errors.New("JavaScript returned an invalid or non-serializable value")))
				} else {
					done(raw)
				}
			}
			return 0
		})
		err = tab.view.stringArg(29, browserEvalScript(script), cb.pointer())
		cb.release()
		if err != nil {
			done(browserFailure(r.nativeFailed(tab, err)))
		}
	})
	if err != nil {
		return browserFailure(err)
	}
	return value
}
func platformCapture(win unsafe.Pointer, id string, timeout int) string {
	value, err := browserDispatch(browserTimeout(timeout), func(r *windowsBrowserRuntime, done func(string)) {
		tab, err := r.liveTab(uintptr(win), id)
		if err != nil {
			done(browserFailure(err))
			return
		}
		stream, err := browserMemoryStream()
		if err != nil {
			done(browserFailure(err))
			return
		}
		cb := newBrowserCallback(iidBrowserCaptureCompleted, true, func(hr uintptr, _ unsafe.Pointer) uintptr {
			if err := browserHRESULT(hr); err != nil {
				done(browserFailure(r.nativeFailed(tab, err)))
				return 0
			}
			data, err := browserStreamBytes(stream)
			if err != nil {
				done(browserFailure(err))
				return 0
			}
			config, err := png.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				done(browserFailure(err))
				return 0
			}
			done(browserJSON(browserCapturePayload{OK: true, MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(data), Width: config.Width, Height: config.Height}))
			return 0
		})
		cb.cleanup = stream.release
		_, err = tab.view.call(30, 0, uintptr(unsafe.Pointer(stream)), cb.pointer())
		cb.release()
		if err != nil {
			done(browserFailure(r.nativeFailed(tab, err)))
		}
	})
	if err != nil {
		return browserFailure(err)
	}
	return value
}
func platformNavigationErrors(win unsafe.Pointer, id string) string {
	value, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		tab := r.tab(uintptr(win), id)
		if tab == nil {
			done("[]")
		} else {
			done(browserJSON(tab.navigationErrors))
		}
	})
	if err != nil {
		return "[]"
	}
	return value
}
func platformToggleDevtools(win unsafe.Pointer, id string) error {
	// WebView2 公开 API 仅提供打开（重复调用聚焦已有窗口），不提供关闭 inspector。
	return browserDo(win, id, func(_ *windowsBrowserRuntime, tab *windowsBrowserTab) error { _, err := tab.view.call(51); return err })
}
func platformClearData(win unsafe.Pointer) { _ = platformClearDataResult(win) }
func platformClearDataResult(win unsafe.Pointer) error {
	value, err := browserDispatch(30*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		host := r.hosts[uintptr(win)]
		var tab *windowsBrowserTab
		if host != nil {
			for _, candidate := range host.tabs {
				if candidate.view != nil {
					tab = candidate
					break
				}
			}
		}
		if tab == nil {
			done(browserFailure(errors.New("mount a browser tab before clearing its browsing data")))
			return
		}
		view13, err := tab.view.query("{F75F09A8-667E-4983-88D6-C8773F315E84}")
		if err != nil {
			done(browserFailure(fmt.Errorf("WebView2 runtime does not support browser profiles: %w", err)))
			return
		}
		profile, err := view13.object(105)
		view13.release()
		if err != nil {
			done(browserFailure(err))
			return
		}
		profile2, err := profile.query("{FA740D4B-5EAE-4344-A8AD-74BE31925397}")
		profile.release()
		if err != nil {
			done(browserFailure(fmt.Errorf("WebView2 runtime does not support clearing browsing data: %w", err)))
			return
		}
		cb := newBrowserCallback("{E9710A06-1D1D-49B2-8234-226F35846AE5}", true, func(hr uintptr, _ unsafe.Pointer) uintptr {
			if err := browserHRESULT(hr); err != nil {
				done(browserFailure(err))
			} else {
				done(`{"ok":true}`)
			}
			return 0
		})
		cb.cleanup = profile2.release
		_, err = profile2.call(12, cb.pointer())
		cb.release()
		if err != nil {
			done(browserFailure(err))
		}
	})
	if err != nil {
		return err
	}
	var result evalResult
	if err = json.Unmarshal([]byte(value), &result); err != nil {
		return err
	}
	if !result.OK {
		return errors.New(result.Error)
	}
	return nil
}
func platformScreenRegion(win unsafe.Pointer, x, y, w, h float64) (int, int, int, int, bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	restoreDPI := browserPhysicalDPIContext()
	defer restoreDPI()
	hwnd := uintptr(win)
	if !browserValidHost(hwnd) {
		return 0, 0, 0, 0, false
	}
	rect, err := browserBounds(hwnd, x, y, w, h)
	if err != nil {
		return 0, 0, 0, 0, false
	}
	point := browserPoint{rect.Left, rect.Top}
	ok, _, _ := browserClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&point)))
	if ok == 0 {
		return 0, 0, 0, 0, false
	}
	return int(point.X), int(point.Y), int(rect.Right - rect.Left), int(rect.Bottom - rect.Top), true
}
func platformExists(win unsafe.Pointer) bool {
	if !browserValidHost(uintptr(win)) {
		return false
	}
	value, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		host := r.hosts[uintptr(win)]
		if host != nil {
			for _, tab := range host.tabs {
				if tab.view != nil && !tab.closed && tab.failure == nil {
					done("true")
					return
				}
			}
		}
		done("false")
	})
	return err == nil && value == "true"
}

// platformActiveTabID reports the tab the WebView2 host currently shows; "" means unknown.
var platformActiveTabID = func(win unsafe.Pointer) string {
	if !browserValidHost(uintptr(win)) {
		return ""
	}
	value, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		host := r.hosts[uintptr(win)]
		if host == nil || host.active == "" {
			done("")
			return
		}
		if tab := host.tabs[browserTabID(host.active)]; tab == nil || tab.closed {
			done("")
			return
		}
		done(host.active)
	})
	if err != nil {
		return ""
	}
	return value
}

func (r *windowsBrowserRuntime) recordNavigationRefusal(tab *windowsBrowserTab, address string) {
	parsed, _ := url.Parse(address)
	scheme := "unknown"
	if parsed != nil && parsed.Scheme != "" && len(parsed.Scheme) < 33 {
		scheme = parsed.Scheme
	}
	r.recordError(tab, "", -1, "Blocked a page navigation: the "+scheme+" address is not allowed")
}

func (r *windowsBrowserRuntime) openPageDialog(tab *windowsBrowserTab, args *browserCOM) {
	var kind int32
	if _, err := args.call(4, uintptr(unsafe.Pointer(&kind))); err != nil {
		return
	}
	deferral, err := args.object(10)
	if err != nil || deferral == nil {
		return
	}
	args.retain()
	id := randomRequestID()
	name := dialogKindConfirm
	if kind == 0 {
		name = dialogKindAlert
	} else if kind == 2 {
		name = dialogKindPrompt
	}
	message, _ := args.text(5)
	defaultText, _ := args.text(7)
	origin, _ := args.text(3)
	if tab.dialogs == nil {
		tab.dialogs = make(map[string]*windowsPageDialog)
	}
	tab.dialogs[id] = &windowsPageDialog{args: args, deferral: deferral, kind: name}
	openDialog(pendingDialog{ID: id, TabID: tab.id, Kind: name, Message: message, DefaultText: defaultText, Origin: origin})
}

func finishWindowsPageDialog(d *windowsPageDialog, accept bool, text string) error {
	defer d.args.release()
	defer d.deferral.release()
	var err error
	if accept || d.kind == dialogKindAlert {
		if d.kind == dialogKindPrompt {
			err = d.args.stringArg(9, text)
		}
		if err == nil {
			_, err = d.args.call(6)
		}
	}
	_, completionErr := d.deferral.call(3)
	if err == nil {
		err = completionErr
	}
	return err
}

// Called by tools or the host message bar; all native work stays on its STA.
var platformCompleteDialog func(unsafe.Pointer, string, string, bool, string) error

func init() { platformCompleteDialog = completeWindowsDialog }

func completeWindowsDialog(_ unsafe.Pointer, tabID, dialogID string, accept bool, text string) error {
	r, err := browserRuntime()
	if err != nil {
		return err
	}
	finish := func() error {
		for _, host := range r.hosts {
			if tab := host.tabs[tabID]; tab != nil {
				if d := tab.dialogs[dialogID]; d != nil {
					delete(tab.dialogs, dialogID)
					return finishWindowsPageDialog(d, accept, text)
				}
			}
		}
		return newToolError(errCodeDialogAlreadyHandled, "this page dialog was already answered or closed", map[string]any{"tabId": tabID, "dialogId": dialogID})
	}
	if windows.GetCurrentThreadId() == r.thread {
		return finish()
	}
	result := make(chan error, 1)
	if err := r.post(func() { result <- finish() }); err != nil {
		return err
	}
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		return errors.New("page dialog completion timed out")
	}
}

func (r *windowsBrowserRuntime) popupCount() int {
	count := 0
	for _, host := range r.hosts {
		for _, tab := range host.tabs {
			if tab.popup && !tab.closed {
				count++
			}
		}
	}
	return count
}

func (r *windowsBrowserRuntime) openPopup(opener *windowsBrowserTab, args *browserCOM) {
	_, _ = args.call(6, 1) // Never let WebView2 launch an unmanaged window.
	address, _ := args.text(3)
	if r.popupCount() >= maxPopupTabs {
		r.recordError(opener, "", -1, "Blocked a new window: too many tabs opened by pages are still open")
		return
	}
	event, err := preparePopup(opener.id, address)
	if err != nil {
		r.recordError(opener, "", -1, err.Error())
		return
	}
	deferral, err := args.object(9)
	if err != nil || deferral == nil {
		r.recordError(opener, "", -1, "Blocked a new window: deferral unavailable")
		return
	}
	args.retain()
	tab := &windowsBrowserTab{host: opener.host, id: event.TabID, userAgent: opener.userAgent, rect: opener.rect,
		popup: true, popupArgs: args, popupDeferral: deferral, openerID: opener.id, popupURL: event.URL,
		deadline: time.Now().Add(browserMountTimeout), navigationErrors: make([]map[string]any, 0)}
	if admitDownload(opener.id) {
		tab.downloadAuthorizedUntil = time.Now().Add(30 * time.Second)
	}
	tab.mountDone = func(err error) {
		if err != nil {
			r.recordError(opener, "", -1, "Blocked a new window: child initialization failed")
		}
	}
	r.hosts[opener.host].tabs[tab.id] = tab
	_ = r.post(func() {
		if !tab.closed {
			r.ensureEnvironment(tab)
		}
	})
}

// windowsHistoryWait bounds how long platformGoHistory waits for the URL to change.
const windowsHistoryWait = 5 * time.Second

// Native history capability correctly includes distinct entries at the same URL.
var platformGoHistory = func(win unsafe.Pointer, tabID string, delta int) (bool, error) {
	moved := false
	err := browserDo(win, tabID, func(_ *windowsBrowserRuntime, tab *windowsBrowserTab) error {
		get, goSlot := 38, 40
		if delta >= 0 {
			get, goSlot = 39, 41
		}
		var available int32
		if _, err := tab.view.call(get, uintptr(unsafe.Pointer(&available))); err != nil {
			return err
		}
		if available == 0 {
			return nil
		}
		if _, err := tab.view.call(goSlot); err != nil {
			return err
		}
		moved = true
		return nil
	})
	return moved, err
}
