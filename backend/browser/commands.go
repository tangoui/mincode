package browser

// 内置浏览器命令层：解析前端参数（payload 外层）→ 调平台原生覆盖层。
// 命令名与 Tauri 版（src-tauri/src/embedded_browser.rs）保持一致，前端 platform
// 抽象层可无缝分发。

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"mincode/app/backend/core"
)

func init() {
	core.Register("embedded_browser_mount", handleMount)
	core.Register("embedded_browser_sync_bounds", handleSyncBounds)
	core.Register("embedded_browser_hide", handleHide)
	core.Register("embedded_browser_close", handleClose)
	core.Register("embedded_browser_navigate", handleNavigate)
	core.Register("embedded_browser_activate", handleActivate)
	core.Register("embedded_browser_close_tab", handleCloseTab)
	core.Register("embedded_browser_wait_ready", handleWaitReady)
	core.Register("embedded_browser_reload", handleReload)
	core.Register("embedded_browser_get_url", handleGetURL)
	core.Register("embedded_browser_set_zoom", handleSetZoom)
	core.Register("embedded_browser_clear_browsing_data", handleClearData)
	core.Register("embedded_browser_eval", handleEval)
	core.Register("embedded_browser_toggle_devtools", handleToggleDevtools)
	core.Register("embedded_browser_screenshot", handleScreenshot)
	// 前端探测：宿主是否存在覆盖 WebView / 平台是否支持。
	core.Register("embedded_browser_exists", handleExists)
	core.Register("embedded_browser_supported", handleSupported)
}

// requireWindow 返回当前主窗口原生句柄；未就绪时返回错误。
func requireWindow() (unsafe.Pointer, error) {
	win := hostWindow()
	if win == nil {
		return nil, errors.New("内置浏览器：主窗口未就绪")
	}
	return win, nil
}

// ---- 参数结构（camelCase，与 Tauri invoke 对齐）----

type mountArgs struct {
	TabID     string  `json:"tabId"`
	URL       string  `json:"url"`
	UserAgent *string `json:"userAgent"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
}

type boundsArgs struct {
	TabID   string  `json:"tabId"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Width   float64 `json:"width"`
	Height  float64 `json:"height"`
	Visible bool    `json:"visible"`
}

type tabArg struct {
	TabID string `json:"tabId"`
}

type urlArg struct {
	TabID string `json:"tabId"`
	URL   string `json:"url"`
}

type zoomArg struct {
	TabID string  `json:"tabId"`
	Scale float64 `json:"scale"`
}

type evalArg struct {
	TabID       string `json:"tabId"`
	Script      string `json:"script"`
	TimeoutMs   int    `json:"timeoutMs"`
	ExpectedURL string `json:"expectedUrl"`
	PreviousURL string `json:"previousUrl"`
}

type screenshotArgs struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// ---- 处理器 ----

func handleMount(_, argsJson string) (string, error) {
	var a mountArgs
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.TabID) == "" {
		return "", errors.New("tabId is required")
	}
	if err := validateBrowserMountURL(a.URL); err != nil {
		return "", err
	}
	if err := validateBrowserBounds(a.X, a.Y, a.Width, a.Height); err != nil {
		return "", err
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	ua := ""
	if a.UserAgent != nil {
		ua = *a.UserAgent
	}
	browserComputerOperationMu.Lock()
	defer browserComputerOperationMu.Unlock()
	invalidateBrowserComputerCapture(a.TabID)
	if err := platformMount(win, a.TabID, a.URL, ua, a.X, a.Y, a.Width, a.Height); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func handleSyncBounds(_, argsJson string) (string, error) {
	var a boundsArgs
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	if err := validateBrowserBounds(a.X, a.Y, a.Width, a.Height); err != nil {
		return "", err
	}
	if err := platformSetBoundsResult(win, a.TabID, a.X, a.Y, a.Width, a.Height, a.Visible); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func handleHide(_, _ string) (string, error) {
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	platformHide(win)
	return core.EncodeResult(nil)
}

func handleClose(_, _ string) (string, error) {
	browserComputerOperationMu.Lock()
	defer browserComputerOperationMu.Unlock()
	invalidateAllBrowserComputerCaptures()
	cancelAllDialogs()
	win := hostWindow()
	if win != nil {
		platformClose(win)
	}
	return core.EncodeResult(nil)
}

// about:blank is an explicit empty preview, never an arbitrary local/native URL.
func validateBrowserMountURL(raw string) error {
	if raw == "" || raw == "about:blank" {
		return nil
	}
	return validateNavigationURL(raw)
}

func validateBrowserBounds(x, y, width, height float64) error {
	for _, value := range []float64{x, y, width, height} {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 32768 {
			return errors.New("browser bounds must be finite and within 32768 logical pixels")
		}
	}
	if width < 0 || height < 0 {
		return errors.New("browser dimensions must not be negative")
	}
	return nil
}

func validateNavigationURL(raw string) error {
	if err := core.ValidateRequestURL(raw); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("无效 URL: %w", err)
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return errors.New("浏览器地址缺少主机名")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return errors.New("拒绝访问危险网络地址")
		}
	}
	return nil
}

func handleNavigate(_, argsJson string) (string, error) {
	var a urlArg
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	if err := validateNavigationURL(a.URL); err != nil {
		return "", err
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	browserComputerOperationMu.Lock()
	defer browserComputerOperationMu.Unlock()
	invalidateBrowserComputerCapture(a.TabID)
	cancelDialogsForTab(a.TabID)
	if err := platformNavigateResult(win, a.TabID, a.URL); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func handleActivate(_, argsJson string) (string, error) {
	var a tabArg
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.TabID) == "" {
		return "", errors.New("tabId is required")
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	if err := platformActivateResult(win, a.TabID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func handleCloseTab(_, argsJson string) (string, error) {
	var a tabArg
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.TabID) == "" {
		return "", errors.New("tabId is required")
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	browserComputerOperationMu.Lock()
	defer browserComputerOperationMu.Unlock()
	invalidateBrowserComputerCapture(a.TabID)
	cancelDialogsForTab(a.TabID)
	platformCloseTab(win, a.TabID)
	return core.EncodeResult(nil)
}

type browserReadyState struct {
	ReadyState string `json:"readyState"`
	URL        string `json:"url"`
	Title      string `json:"title"`
}

func browserReadyMatches(state browserReadyState, expectedURL, previousURL string) bool {
	if state.ReadyState != "interactive" && state.ReadyState != "complete" {
		return false
	}
	current := strings.TrimSpace(state.URL)
	if current == "" {
		return false
	}
	expected := strings.TrimSpace(expectedURL)
	previous := strings.TrimSpace(previousURL)
	if expected != "" && !strings.EqualFold(expected, "about:blank") && strings.EqualFold(current, "about:blank") {
		return false
	}
	if previous != "" && expected != "" && previous != expected && current == previous {
		return false
	}
	return true
}

func browserNavigationFailure(raw string) error {
	var items []struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(raw), &items) != nil || len(items) == 0 {
		return nil
	}
	if message := strings.TrimSpace(items[len(items)-1].Message); message != "" {
		return fmt.Errorf("browser navigation failed: %s", message)
	}
	return nil
}

func handleWaitReady(_, argsJson string) (string, error) {
	var a evalArg
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	timeout := a.TimeoutMs
	if timeout <= 0 || timeout > 30000 {
		timeout = 15000
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
	for {
		// Windows resets the per-tab failure list when navigation starts. The
		// macOS list is historical, so it must not fail a later successful load.
		if runtime.GOOS == "windows" {
			if err := browserNavigationFailure(platformNavigationErrors(win, a.TabID)); err != nil {
				return "", err
			}
		}
		raw := platformEval(win, a.TabID, `({readyState:document.readyState,url:location.href,title:document.title})`, 1500)
		var result evalResult
		if json.Unmarshal([]byte(raw), &result) == nil && result.OK {
			var state browserReadyState
			if json.Unmarshal(result.Value, &state) == nil && browserReadyMatches(state, a.ExpectedURL, a.PreviousURL) {
				// A native navigation failure can arrive during the JS round trip.
				if runtime.GOOS == "windows" {
					if err := browserNavigationFailure(platformNavigationErrors(win, a.TabID)); err != nil {
						return "", err
					}
				}
				return core.EncodeResult(state)
			}
		}
		if time.Now().After(deadline) {
			return "", errors.New("browser navigation did not become ready before timeout")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func handleReload(_, argsJson string) (string, error) {
	var a tabArg
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	browserComputerOperationMu.Lock()
	defer browserComputerOperationMu.Unlock()
	invalidateBrowserComputerCapture(a.TabID)
	cancelDialogsForTab(a.TabID)
	if err := platformReloadResult(win, a.TabID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func browserURLFromEval(raw string) string {
	var result evalResult
	if json.Unmarshal([]byte(raw), &result) != nil || !result.OK || len(result.Value) == 0 {
		return ""
	}
	var currentURL string
	if json.Unmarshal(result.Value, &currentURL) != nil {
		return ""
	}
	return strings.TrimSpace(currentURL)
}

func currentBrowserURL(win unsafe.Pointer, tabID string) string {
	if currentURL := browserURLFromEval(platformEval(win, tabID, `location.href`, 1500)); currentURL != "" {
		return currentURL
	}
	return strings.TrimSpace(platformGetURL(win, tabID))
}

func handleGetURL(_, argsJson string) (string, error) {
	var a tabArg
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	return core.EncodeResult(addressBarURL(win, a.TabID))
}

// addressBarURL serves the frontend's periodic URL sync. The native property
// read is a cheap main-thread hop, so it is tried first; the location.href
// evaluation (a JS round trip with a 1.5s timeout) is only a fallback for
// platforms without a native URL. Automation keeps currentBrowserURL, which
// prefers the committed document location for readiness checks.
func addressBarURL(win unsafe.Pointer, tabID string) string {
	return preferNativeURL(func() string { return platformGetURL(win, tabID) }, func() string {
		return browserURLFromEval(platformEval(win, tabID, `location.href`, 1500))
	})
}

func preferNativeURL(native, evaluated func() string) string {
	if currentURL := strings.TrimSpace(native()); currentURL != "" {
		return currentURL
	}
	return evaluated()
}

func handleSetZoom(_, argsJson string) (string, error) {
	var a zoomArg
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	zoom := a.Scale
	if zoom <= 0 {
		zoom = 1.0
	}
	if math.IsNaN(zoom) || math.IsInf(zoom, 0) || zoom < 0.25 || zoom > 5 {
		return "", errors.New("browser zoom must be between 0.25 and 5")
	}
	if err := platformSetZoomResult(win, a.TabID, zoom); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func handleClearData(_, _ string) (string, error) {
	browserComputerOperationMu.Lock()
	defer browserComputerOperationMu.Unlock()
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	if err := platformClearDataResult(win); err != nil {
		return "", err
	}
	invalidateAllBrowserComputerCaptures()
	return core.EncodeResult(nil)
}

// evalResult 是原生层返回的 JSON 结构。
type evalResult struct {
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value"`
	Error string          `json:"error"`
}

func handleEval(_, argsJson string) (string, error) {
	var a evalArg
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	timeout := a.TimeoutMs
	if timeout <= 0 {
		timeout = 3000
	}
	raw := platformEval(win, a.TabID, a.Script, timeout)
	var res evalResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return "", fmt.Errorf("eval 结果解析失败: %w", err)
	}
	if !res.OK {
		return "", errors.New(res.Error)
	}
	// 与 Tauri 版对齐：返回被求值表达式的 JSON 结果字符串。
	if len(res.Value) == 0 {
		return core.EncodeResult(nil)
	}
	return string(res.Value), nil
}

func handleToggleDevtools(_, argsJson string) (string, error) {
	var a tabArg
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	if err := platformToggleDevtools(win, a.TabID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// handleSupported reports whether this platform has an embedded browser at all,
// independent of whether an overlay is mounted right now.
func handleSupported(_, _ string) (string, error) {
	return core.EncodeResult(platformAvailable())
}

func handleExists(_, _ string) (string, error) {
	if !platformAvailable() {
		return core.EncodeResult(false)
	}
	win := hostWindow()
	if win == nil {
		return core.EncodeResult(false)
	}
	// 与 Tauri 版 embedded_browser_exists 对齐：判断覆盖 WebView 对象是否
	// 真正挂载，而非仅仅宿主窗口是否就绪——否则前端会跳过 mount 直接调用
	// navigate/eval，导致原生层报 "overlay not mounted"。
	return core.EncodeResult(platformExists(win))
}

// handleScreenshot 截取覆盖区域对应的屏幕像素，返回 PNG 的 base64（不 data: 前缀），
// 与 Tauri 版一致：不截 WebView 内容而是截屏区域，规避跨进程内容抓取限制。
func handleScreenshot(_, argsJson string) (string, error) {
	var a screenshotArgs
	if err := core.DecodeArgsPayload(argsJson, &a); err != nil {
		return "", err
	}
	win, err := requireWindow()
	if err != nil {
		return "", err
	}
	rx, ry, rw, rh, ok := platformScreenRegion(win, a.X, a.Y, a.Width, a.Height)
	if !ok {
		return "", errors.New("截图：无法换算屏幕区域")
	}
	png, err := captureScreenRegionPNG(rx, ry, rw, rh)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(base64.StdEncoding.EncodeToString(png))
}

// captureScreenRegionPNG 用平台工具截取屏幕区域为 PNG 字节。
func captureScreenRegionPNG(x, y, w, h int) ([]byte, error) {
	if w < 1 || h < 1 {
		return nil, errors.New("截图区域无效")
	}
	switch runtime.GOOS {
	case "windows":
		return captureWindowsScreenRegionPNG(x, y, w, h)
	case "darwin":
		tmp := filepath.Join(os.TempDir(),
			fmt.Sprintf("mincode-browser-cap-%d.png", time.Now().UnixNano()))
		region := fmt.Sprintf("%d,%d,%d,%d", x, y, w, h)
		cmd, cancel := core.CommandWithTimeout(core.ShortCommandTimeout, "screencapture", "-x", "-R", region, tmp)
		defer cancel()
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("调用 screencapture 失败: %w", err)
		}
		defer func() { _ = os.Remove(tmp) }()
		return os.ReadFile(tmp)
	default:
		return nil, errors.New("当前平台暂不支持内置浏览器截图")
	}
}
