// Package browser 实现内置浏览器的原生 WebView 覆盖层。
//
// 背景：宿主框架 没有 Tauri 那种 host.add_child(webview, pos, size)
// 的子 WebView 覆盖能力——一个窗口与其原生 webview 严格 1:1。因此本模块通过
// WebviewWindow.NativeWindow() 拿到原生窗口句柄（macOS: NSWindow*，Windows: HWND），
// 在其之上自建一个原生 WebView（macOS: WKWebView，Windows: WebView2 controller），
// 并按前端传入的逻辑坐标定位覆盖，复刻 Tauri 版内置浏览器体验。
//
// 命令面与 Tauri 版（src-tauri/src/embedded_browser.rs）对齐：
//
//	embedded_browser_mount / sync_bounds / hide / close / navigate / reload /
//	get_url / set_zoom / clear_browsing_data / eval / toggle_devtools / screenshot
//
// 前端参数经 platform 抽象层传来，顶层为 {"payload": {...}}，故统一用
// core.DecodeArgsPayload 剥离外层（见迁移记录中的通用规律）。
package browser

import (
	"sync"
	"unsafe"
)

// nativeWindowProvider 返回当前主窗口的原生句柄（NSWindow* / HWND）。
// 由 main.go 在创建窗口后经 SetNativeWindowProvider 注入，避免 backend 直接
// 依赖 宿主应用实例（与 core.SetEmitter 的注入模式一致）。
type nativeWindowProvider func() unsafe.Pointer

var (
	provMu       sync.RWMutex
	windowHandle nativeWindowProvider
)

// SetNativeWindowProvider 注入原生窗口句柄提供者（main.go 创建窗口后调用一次）。
func SetNativeWindowProvider(fn nativeWindowProvider) {
	provMu.Lock()
	defer provMu.Unlock()
	windowHandle = fn
}

// hostWindow 返回当前主窗口原生句柄；未注入或窗口未就绪时返回 nil。
func hostWindow() unsafe.Pointer {
	provMu.RLock()
	fn := windowHandle
	provMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}
