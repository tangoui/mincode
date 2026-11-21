//go:build windows

package browser

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 仅在显式启用时创建真实可见窗口，且不复用用户应用或浏览器配置。
func TestWindowsBrowserNativeSmoke(t *testing.T) {
	if os.Getenv("MINCODE_WEBVIEW2_SMOKE") != "1" {
		t.Skip("设置 MINCODE_WEBVIEW2_SMOKE=1 以运行真实 Windows WebView2 smoke")
	}
	if !platformAvailable() {
		t.Fatal("显式启用的 smoke 需要 Windows x64 和已安装的 WebView2 Runtime")
	}
	state, err := browserDispatch(3*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		done(browserJSON(r.environment == nil && !r.environmentPending && len(r.hosts) == 0))
	})
	if err != nil || state != "true" {
		t.Fatalf("smoke 必须独占尚未创建 WebView2 profile 的测试进程: state=%s error=%v", state, err)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位项目 outputs 目录")
	}
	outputs := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "outputs"))
	if err := os.MkdirAll(outputs, 0700); err != nil {
		t.Fatalf("创建项目 outputs 目录失败: %v", err)
	}
	profile, err := os.MkdirTemp(outputs, "webview2-native-smoke-")
	if err != nil {
		t.Fatalf("创建保留的 smoke profile 失败: %v", err)
	}
	t.Setenv("LOCALAPPDATA", profile)
	t.Logf("smoke profile 保留用于诊断，不自动删除: %s", profile)

	var requestMu sync.Mutex
	requests := make(map[string]int)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMu.Lock()
		requests[r.URL.Path]++
		requestMu.Unlock()
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; base-uri 'none'; object-src 'none'")
		if r.URL.Path == "/style.css" {
			w.Header().Set("Content-Type", "text/css")
			_, _ = w.Write([]byte(`body{margin:0;background:white}#green,#red{position:absolute;top:20px;width:128px;height:128px}#green{left:20px;background:rgb(0,255,0)}#red{left:180px;background:rgb(255,0,0)}#state{position:absolute;left:20px;top:190px}`))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>native smoke</title><link rel="stylesheet" href="/style.css"></head><body><div id="green"></div><div id="red"></div><input id="state" value="fixture"></body></html>`))
	}))
	t.Cleanup(fixture.Close)

	win := nativeSmokeHost(t)
	t.Cleanup(func() {
		if err := platformCloseResult(win); err != nil {
			t.Errorf("清理 smoke 浏览器失败: %v", err)
		}
		_, err := browserDispatch(3*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
			if len(r.hosts) == 0 && !r.environmentPending && r.environment != nil {
				r.environment.release()
				r.environment = nil
			}
			done("")
		})
		if err != nil {
			t.Errorf("释放 smoke WebView2 environment 失败: %v", err)
		}
	})
	const first, second = "native-smoke:first", "native-smoke:second"
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		requestMu.Lock()
		counts := make(map[string]int, len(requests))
		total := 0
		for path, count := range requests {
			counts[path] = count
			total += count
		}
		requestMu.Unlock()
		t.Logf("smoke 失败诊断: profile=%s fixture=%s 请求总数=%d 请求路径=%v", profile, fixture.URL, total, counts)
		diagnostic := fmt.Sprintf("profile=%s\nfixture=%s\nrequestTotal=%d\nrequestPaths=%v\n", profile, fixture.URL, total, counts)
		for _, id := range []string{first, second} {
			url, errors := platformGetURL(win, id), platformNavigationErrors(win, id)
			t.Logf("smoke tab=%s nativeURL=%q navigationErrors=%s", id, url, errors)
			diagnostic += fmt.Sprintf("tab=%s nativeURL=%q navigationErrors=%s\n", id, url, errors)
		}
		raw, err := nativeSmokeDirectScript(win, first, "1+1")
		t.Logf("smoke 原生 ExecuteScript 无 eval 对照: raw=%q error=%v", raw, err)
		diagnostic += fmt.Sprintf("directScriptRaw=%q\ndirectScriptError=%v\n", raw, err)
		if err := os.WriteFile(filepath.Join(profile, "smoke-diagnostic.txt"), []byte(diagnostic), 0600); err != nil {
			t.Logf("保存 smoke 诊断失败: %v", err)
		}
	})
	t.Log("挂载第一个真实 WebView2 tab")
	if err := platformMount(win, first, fixture.URL+"/first", "", 0, 0, 640, 420); err != nil {
		t.Fatalf("挂载第一个 tab 失败: %v", err)
	}
	nativeSmokeWait(t, "首个文档和严格 CSP 下的 DOM 执行", func() (bool, string) {
		return nativeSmokeBool(win, first, `document.readyState==='complete' && document.title==='native smoke' && document.querySelector('#state').value==='fixture' && getComputedStyle(document.querySelector('#green')).backgroundColor==='rgb(0, 255, 0)'`)
	})
	if !platformExists(win) {
		t.Fatal("挂载成功后 platformExists 返回 false")
	}
	nativeSmokeAssert(t, win, first, `typeof window.tango==='undefined' && typeof window.__tango==='undefined' && typeof window.go==='undefined' && typeof window.__tangoNative==='undefined'`)
	// WebView2 leaves chrome.webview.postMessage defined even with messages off.
	// Test the actual setting and event delivery, not the existence of its JS stub.
	messages := make(chan struct{}, 1)
	isolation, dispatchErr := browserDispatch(3*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		tab := r.tab(uintptr(win), first)
		settings, e := tab.view.object(3)
		if e != nil {
			done(e.Error())
			return
		}
		var enabled int32
		_, e = settings.call(5, uintptr(unsafe.Pointer(&enabled)))
		settings.release()
		if e != nil || enabled != 0 {
			done("web messages must be disabled")
			return
		}
		e = r.register(tab, 34, "{57213F19-00E6-49FA-8E07-898EA01ECBD2}", func(_ uintptr, _ unsafe.Pointer) uintptr {
			select {
			case messages <- struct{}{}:
			default:
			}
			return 0
		})
		if e != nil {
			done(e.Error())
			return
		}
		done("")
	})
	if dispatchErr != nil || isolation != "" {
		t.Fatalf("isolation settings: %s %v", isolation, dispatchErr)
	}
	nativeSmokeAssert(t, win, first, `(window.chrome.webview.postMessage({mincodeIsolationProbe:true}),true)`)
	select {
	case <-messages:
		t.Fatal("remote page reached host WebMessageReceived despite disabled messages")
	case <-time.After(250 * time.Millisecond):
	}
	nativeSmokeAssert(t, win, first, `(window.__nativeSmokeState='first', document.querySelector('#state').value='first input', true)`)

	t.Log("挂载第二个 tab 并验证切换不丢失第一个 tab 的 DOM 状态")
	if err := platformMount(win, second, fixture.URL+"/second", "", 0, 0, 640, 420); err != nil {
		t.Fatalf("挂载第二个 tab 失败: %v", err)
	}
	nativeSmokeWait(t, "第二个文档加载", func() (bool, string) {
		return nativeSmokeBool(win, second, `document.readyState==='complete' && location.pathname==='/second' && document.querySelector('#state').value==='fixture' && typeof window.__nativeSmokeState==='undefined'`)
	})
	nativeSmokeAssert(t, win, second, `(window.__nativeSmokeState='second', document.querySelector('#state').value='second input', true)`)
	if err := platformActivateResult(win, first); err != nil {
		t.Fatalf("激活第一个 tab 失败: %v", err)
	}
	nativeSmokeAssert(t, win, first, `window.__nativeSmokeState==='first' && document.querySelector('#state').value==='first input' && location.pathname==='/first'`)
	if err := platformActivateResult(win, second); err != nil {
		t.Fatalf("激活第二个 tab 失败: %v", err)
	}
	nativeSmokeAssert(t, win, second, `window.__nativeSmokeState==='second' && document.querySelector('#state').value==='second input'`)
	if err := platformActivateResult(win, first); err != nil {
		t.Fatalf("恢复第一个 tab 失败: %v", err)
	}

	t.Log("验证真实 zoom、resize 和彩色 PNG 捕获")
	viewport := func() int {
		t.Helper()
		raw := platformEval(win, first, `window.innerWidth`, 2000)
		var result evalResult
		var width int
		if err := json.Unmarshal([]byte(raw), &result); err != nil || !result.OK {
			t.Fatalf("读取 viewport 失败: %s (%v)", raw, err)
		}
		if err := json.Unmarshal(result.Value, &width); err != nil || width <= 0 {
			t.Fatalf("viewport 宽度无效: %s (%v)", raw, err)
		}
		return width
	}
	initialWidth := viewport()
	if err := platformSetZoomResult(win, first, 1.25); err != nil {
		t.Fatalf("设置 zoom 失败: %v", err)
	}
	nativeSmokeWait(t, "zoom 改变 viewport", func() (bool, string) {
		return nativeSmokeBool(win, first, fmt.Sprintf(`window.innerWidth < %d`, initialWidth))
	})
	zoomedWidth := viewport()
	if err := platformSetBoundsResult(win, first, 8, 8, 520, 340, true); err != nil {
		t.Fatalf("调整浏览器大小失败: %v", err)
	}
	nativeSmokeWait(t, "resize 改变 viewport", func() (bool, string) {
		return nativeSmokeBool(win, first, fmt.Sprintf(`window.innerWidth < %d && window.innerWidth > 0`, zoomedWidth))
	})
	nativeSmokeWait(t, "PNG 包含绿色和红色 fixture 像素", func() (bool, string) {
		raw := platformCapture(win, first, 3000)
		var result browserCapturePayload
		if err := json.Unmarshal([]byte(raw), &result); err != nil || !result.OK {
			return false, fmt.Sprintf("capture 失败: %.500s (%v)", raw, err)
		}
		data, err := base64.StdEncoding.DecodeString(result.Data)
		if err != nil {
			return false, fmt.Sprintf("base64 无效: %v", err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return false, fmt.Sprintf("PNG 无效: %v", err)
		}
		bounds := img.Bounds()
		if result.MimeType != "image/png" || bounds.Empty() || bounds.Dx() != result.Width || bounds.Dy() != result.Height {
			return false, fmt.Sprintf("PNG 元数据无效: mime=%s bounds=%v size=%dx%d", result.MimeType, bounds, result.Width, result.Height)
		}
		green, red := 0, 0
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r, g, b, a := img.At(x, y).RGBA()
				if a > 0xf000 && g > 0xe000 && r < 0x2000 && b < 0x2000 {
					green++
				}
				if a > 0xf000 && r > 0xe000 && g < 0x2000 && b < 0x2000 {
					red++
				}
			}
		}
		return green >= 100 && red >= 100, fmt.Sprintf("PNG=%dx%d 绿色像素=%d 红色像素=%d", bounds.Dx(), bounds.Dy(), green, red)
	})

	t.Log("验证导航失败记录和恢复")
	failed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	failedURL := failed.URL + "/unreachable"
	failed.Close()
	if err := platformNavigateResult(win, first, failedURL); err != nil {
		t.Fatalf("提交失败导航失败: %v", err)
	}
	nativeSmokeWait(t, "真实连接失败写入 navigation errors", func() (bool, string) {
		raw := platformNavigationErrors(win, first)
		var entries []struct {
			URL     string `json:"url"`
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			return false, fmt.Sprintf("navigation errors JSON 无效: %s (%v)", raw, err)
		}
		for _, entry := range entries {
			if entry.URL == failedURL && entry.Code != 0 && entry.Message != "" {
				return true, raw
			}
		}
		return false, raw
	})
	if err := platformNavigateResult(win, first, fixture.URL+"/recovered"); err != nil {
		t.Fatalf("恢复导航失败: %v", err)
	}
	nativeSmokeWait(t, "失败后的正常文档恢复", func() (bool, string) {
		return nativeSmokeBool(win, first, `document.readyState==='complete' && location.pathname==='/recovered' && document.querySelector('#state').value==='fixture'`)
	})
	if got := platformGetURL(win, first); got != fixture.URL+"/recovered" {
		t.Fatalf("恢复后 URL 不符: %q", got)
	}
	if err := platformCloseTabResult(win, second); err != nil {
		t.Fatalf("关闭第二个 tab 失败: %v", err)
	}
	if !platformExists(win) {
		t.Fatal("关闭第二个 tab 意外移除了第一个 tab")
	}
	if err := platformCloseResult(win); err != nil {
		t.Fatalf("关闭浏览器失败: %v", err)
	}
	if platformExists(win) {
		t.Fatal("关闭后 platformExists 仍返回 true")
	}
}

// TestWindowsBrowserProcessExitRecovery 复现 2026-10-23 的失败：WebView2 浏览器进程
// 退出（ProcessFailed kind 0）后，旧 environment 不再创建 controller，下一次挂载会超时。
// 只结束本测试独立 profile 下的 WebView2 浏览器进程。
func TestWindowsBrowserProcessExitRecovery(t *testing.T) {
	if os.Getenv("MINCODE_WEBVIEW2_SMOKE") != "1" {
		t.Skip("设置 MINCODE_WEBVIEW2_SMOKE=1 以运行真实 Windows WebView2 进程退出恢复测试")
	}
	if !platformAvailable() {
		t.Fatal("需要 Windows x64 和已安装的 WebView2 Runtime")
	}
	_, sourceFile, _, _ := runtime.Caller(0)
	outputs := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "outputs"))
	if err := os.MkdirAll(outputs, 0700); err != nil {
		t.Fatal(err)
	}
	profile, err := os.MkdirTemp(outputs, "webview2-process-exit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", profile)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><title>exit fixture</title><body>exit fixture</body>`))
	}))
	t.Cleanup(fixture.Close)
	win := nativeSmokeHost(t)
	t.Cleanup(func() {
		if err := platformCloseResult(win); err != nil {
			t.Errorf("清理失败: %v", err)
		}
	})
	const first, second = "exit:first", "exit:second"
	if err := platformMount(win, first, fixture.URL+"/first", "", 0, 0, 640, 420); err != nil {
		t.Fatalf("挂载第一个 tab 失败: %v", err)
	}
	nativeSmokeWait(t, "第一个文档加载", func() (bool, string) {
		return nativeSmokeBool(win, first, `document.readyState==='complete' && location.pathname==='/first'`)
	})
	nativeSmokeKillBrowser(t, win, first)

	nativeSmokeWait(t, "进程退出后旧 tab 返回可操作的错误", func() (bool, string) {
		raw := platformEval(win, first, "1", 1000)
		var result evalResult
		_ = json.Unmarshal([]byte(raw), &result)
		return !result.OK && strings.Contains(result.Error, "reopen"), raw
	})
	if errs := platformNavigationErrors(win, first); !strings.Contains(errs, "process") {
		t.Fatalf("旧 tab 未保留进程退出记录: %s", errs)
	}
	t.Log("进程退出后挂载新 tab")
	if err := platformMount(win, second, fixture.URL+"/second", "", 0, 0, 640, 420); err != nil {
		t.Fatalf("进程退出后挂载新 tab 失败: %v", err)
	}
	nativeSmokeWait(t, "新 tab 文档加载", func() (bool, string) {
		return nativeSmokeBool(win, second, `document.readyState==='complete' && location.pathname==='/second'`)
	})
	t.Log("重新挂载已失效的旧 tab")
	if err := platformMount(win, first, fixture.URL+"/again", "", 0, 0, 640, 420); err != nil {
		t.Fatalf("重新挂载旧 tab 失败: %v", err)
	}
	nativeSmokeWait(t, "旧 tab 重新加载", func() (bool, string) {
		return nativeSmokeBool(win, first, `document.readyState==='complete' && location.pathname==='/again'`)
	})
	if errs := platformNavigationErrors(win, first); errs != "[]" {
		t.Fatalf("重新挂载后仍保留旧错误: %s", errs)
	}

	t.Log("再次结束浏览器进程并立即挂载，不等待 ProcessFailed 事件")
	nativeSmokeKillBrowser(t, win, first)
	const third = "exit:third"
	if err := platformMount(win, third, fixture.URL+"/third", "", 0, 0, 640, 420); err != nil {
		t.Fatalf("进程退出后立即挂载失败: %v", err)
	}
	nativeSmokeWait(t, "立即挂载的 tab 文档加载", func() (bool, string) {
		return nativeSmokeBool(win, third, `document.readyState==='complete' && location.pathname==='/third'`)
	})
	value, err := browserDispatch(2*time.Second, func(_ *windowsBrowserRuntime, done func(string)) { done("ok") })
	if err != nil || value != "ok" {
		t.Fatalf("恢复后 STA 无响应: %q %v", value, err)
	}
}

// TestWindowsBrowserUseSmoke checks actual WebView2 dialogs and opener-linked
// popups, including rejection without an action and cleanup after window.close.
func TestWindowsBrowserUseSmoke(t *testing.T) {
	if os.Getenv("MINCODE_WEBVIEW2_SMOKE") != "1" {
		t.Skip("requires real Windows WebView2")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>browser use</title><body><button>fixture</button></body>`)
	}))
	t.Cleanup(fixture.Close)
	win := nativeSmokeHost(t)
	t.Cleanup(func() { _ = platformCloseResult(win) })
	const tab = "use:opener"
	if err := platformMount(win, tab, fixture.URL, "", 0, 0, 640, 420); err != nil {
		t.Fatal(err)
	}
	nativeSmokeWait(t, "fixture load", func() (bool, string) {
		return nativeSmokeBool(win, tab, `document.readyState==='complete' && document.title==='browser use'`)
	})
	for _, kind := range []string{"alert", "confirm", "prompt"} {
		script := `window.dialogResult=` + kind + `('fixture-message'`
		if kind == "prompt" {
			script += `,'default')`
		} else {
			script += `)`
		}
		done := make(chan string, 1)
		go func() { done <- platformEval(win, tab, script, 10000) }()
		var d pendingDialog
		nativeSmokeWait(t, "native "+kind+" routed to Go", func() (bool, string) { var ok bool; d, ok = dialogForTab(tab); return ok, fmt.Sprint(d) })
		if d.Kind != kind || d.Message != "fixture-message" {
			t.Fatalf("unexpected dialog: %+v", d)
		}
		if err := dialogGate("browser.snapshot", tab); err == nil {
			t.Fatal("dialog must gate page JS")
		}
		if _, err := handleDialogTool(tab, map[string]any{"accept": true, "promptText": "native-answer", "dialogId": d.ID}); err != nil {
			t.Fatal(err)
		}
		select {
		case raw := <-done:
			var result evalResult
			if json.Unmarshal([]byte(raw), &result) != nil || !result.OK {
				t.Fatalf("dialog eval: %s", raw)
			}
		case <-time.After(12 * time.Second):
			t.Fatal("dialog completion stalled")
		}
		if kind == "prompt" {
			nativeSmokeAssert(t, win, tab, `window.dialogResult==='native-answer'`)
		}
		if kind == "confirm" {
			nativeSmokeAssert(t, win, tab, `window.dialogResult===true`)
		}
	}
	nativeSmokeAssert(t, win, tab, `(window.open('/blocked'), true)`)
	value, err := browserDispatch(3*time.Second, func(r *windowsBrowserRuntime, done func(string)) { done(fmt.Sprint(r.popupCount())) })
	if err != nil || value != "0" {
		t.Fatalf("unapproved popup was admitted: %s %v", value, err)
	}
	allowAIPopupWindow(tab)
	nativeSmokeAssert(t, win, tab, `(window.child=window.open('/child'), true)`)
	var popup string
	nativeSmokeWait(t, "opener-preserving native popup", func() (bool, string) {
		value, err := browserDispatch(3*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
			for id, child := range r.hosts[uintptr(win)].tabs {
				if child.popup && child.mountDone == nil {
					done(id)
					return
				}
			}
			done("")
		})
		popup = value
		return err == nil && popup != "", fmt.Sprint(value, err)
	})
	nativeSmokeWait(t, "popup navigation and window.opener", func() (bool, string) {
		return nativeSmokeBool(win, popup, `location.pathname==='/child' && window.opener!=null`)
	})
	nativeSmokeAssert(t, win, popup, `(opener.popupMessage='linked', true)`)
	nativeSmokeAssert(t, win, tab, `window.popupMessage==='linked'`)
	nativeSmokeAssert(t, win, popup, `(window.close(), true)`)
	nativeSmokeWait(t, "window.close reclaims native popup", func() (bool, string) {
		value, err := browserDispatch(3*time.Second, func(r *windowsBrowserRuntime, done func(string)) { done(fmt.Sprint(r.popupCount())) })
		return err == nil && value == "0", fmt.Sprint(value, err)
	})
}

// nativeSmokeKillBrowser 结束 tab 所属的测试专用 WebView2 浏览器进程。
func nativeSmokeKillBrowser(t *testing.T, win unsafe.Pointer, id string) {
	t.Helper()
	pidText, err := browserDispatch(3*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		tab := r.tab(uintptr(win), id)
		var pid uint32
		if tab != nil && tab.view != nil {
			_, _ = tab.view.call(37, uintptr(unsafe.Pointer(&pid)))
		}
		done(fmt.Sprint(pid))
	})
	var pid uint32
	if _, scanErr := fmt.Sscan(pidText, &pid); err != nil || scanErr != nil || pid == 0 {
		t.Fatalf("读取 WebView2 浏览器进程失败: %q %v %v", pidText, err, scanErr)
	}
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		t.Fatalf("打开 WebView2 浏览器进程失败: %v", err)
	}
	defer windows.CloseHandle(process)
	if err := windows.TerminateProcess(process, 1); err != nil {
		t.Fatalf("结束 WebView2 浏览器进程失败: %v", err)
	}
	_, _ = windows.WaitForSingleObject(process, 5000)
	t.Logf("已结束测试专用 WebView2 浏览器进程 pid=%d", pid)
}

// TestWindowsBrowserPipelineProbe isolates the host-thread model without changing
// production settings. Run each mode in its own test process/profile.
func TestWindowsBrowserPipelineProbe(t *testing.T) {
	mode := os.Getenv("MINCODE_WEBVIEW2_PROBE")
	if mode != "same-sta" && mode != "cross-thread" {
		t.Skip("set MINCODE_WEBVIEW2_PROBE=same-sta or cross-thread")
	}
	if !platformAvailable() {
		t.Fatal("Windows x64 WebView2 runtime required")
	}
	_, source, _, _ := runtime.Caller(0)
	outputs := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", "outputs"))
	profile, err := os.MkdirTemp(outputs, "webview2-pipeline-"+mode+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", profile)
	t.Logf("mode=%s profile=%s", mode, profile)
	var mu sync.Mutex
	var trace []string
	began := time.Now()
	record := func(format string, args ...any) {
		mu.Lock()
		trace = append(trace, fmt.Sprintf("%s %s", time.Since(began).Round(time.Millisecond), fmt.Sprintf(format, args...)))
		mu.Unlock()
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, line := range trace {
			t.Log(line)
		}
		data, _ := json.MarshalIndent(trace, "", "  ")
		if err := os.WriteFile(filepath.Join(profile, "pipeline-trace.json"), data, 0600); err != nil {
			t.Error(err)
		}
	})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("HTTP %s", r.URL.Path)
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>pipeline fixture</title><body>pipeline fixture</body>"))
	}))
	t.Cleanup(fixture.Close)
	var win unsafe.Pointer
	if mode == "cross-thread" {
		win = nativeSmokeHost(t)
	} else {
		var owned uintptr
		_, err = browserDispatch(5*time.Second, func(_ *windowsBrowserRuntime, done func(string)) {
			class, _ := windows.UTF16PtrFromString("STATIC")
			title, _ := windows.UTF16PtrFromString("MinCode same STA pipeline probe")
			owned, _, err = browserCreateWindow.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), 0x00cf0000|0x10000000, 40, 40, 900, 650, 0, 0, 0, 0)
			runtime.KeepAlive(class)
			runtime.KeepAlive(title)
			if owned == 0 {
				done(fmt.Sprintf("create host failed: %v", err))
			} else {
				done("created")
			}
		})
		if err != nil || owned == 0 {
			t.Fatalf("create same STA host: %v", err)
		}
		win = browserNativePointer(owned)
		t.Cleanup(func() {
			_, err := browserDispatch(5*time.Second, func(_ *windowsBrowserRuntime, done func(string)) {
				browserDestroyWindow.Call(owned)
				done("")
			})
			if err != nil {
				t.Errorf("destroy same STA host: %v", err)
			}
		})
	}
	t.Cleanup(func() {
		if err := platformCloseResult(win); err != nil {
			t.Errorf("probe cleanup: %v", err)
		}
	})
	const id = "pipeline-probe"
	record("mount about:blank")
	if err := platformMount(win, id, "about:blank", "", 0, 0, 640, 420); err != nil {
		t.Fatal(err)
	}
	record("mount returned")
	// Observe native navigation events without changing production handlers.
	navigationCompleted := make(chan struct{}, 8)
	registered, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		tab := r.tab(uintptr(win), id)
		if tab == nil {
			done("missing tab")
			return
		}
		for _, event := range []struct {
			slot       int
			iid, label string
		}{
			{7, "{9ADBE429-F36D-432B-9DDC-F8881FBD76E3}", "NavigationStarting"},
			{15, "{D33A35BF-1C49-4F98-93AB-006E0533FE1C}", "NavigationCompleted"},
		} {
			label := event.label
			if err := r.register(tab, event.slot, event.iid, func(_ uintptr, _ unsafe.Pointer) uintptr {
				record("%s", label)
				if label == "NavigationCompleted" {
					select {
					case navigationCompleted <- struct{}{}:
					default:
					}
				}
				return 0
			}); err != nil {
				done(err.Error())
				return
			}
		}
		done("registered")
	})
	if err != nil || registered != "registered" {
		t.Fatalf("register probe events: %q %v", registered, err)
	}
	probeScript := func(label, script string) {
		t.Helper()
		record("%s submit", label)
		raw, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
			record("%s reached STA", label)
			tab := r.tab(uintptr(win), id)
			if tab == nil || tab.view == nil {
				done("missing view")
				return
			}
			cb := newBrowserCallback(iidBrowserScriptCompleted, false, func(hr uintptr, result unsafe.Pointer) uintptr {
				record("%s callback HRESULT=%08X", label, uint32(hr))
				done(windows.UTF16PtrToString((*uint16)(result)))
				return 0
			})
			err := tab.view.stringArg(29, script, cb.pointer())
			cb.release()
			record("%s API returned error=%v", label, err)
			if err != nil {
				done("API_ERROR:" + err.Error())
			}
		})
		record("%s result=%q error=%v", label, raw, err)
		if len(raw) >= 10 && raw[:10] == "API_ERROR:" {
			t.Errorf("%s native call failed: %s", label, raw)
		}
		if err != nil {
			t.Errorf("%s callback failed: %v", label, err)
		}
		if label == "blank" && err == nil && raw != "2" {
			t.Errorf("1+1 result=%q", raw)
		}
	}
	probeScript("blank", "1+1")
	for len(navigationCompleted) > 0 {
		<-navigationCompleted
	}
	record("navigate fixture")
	if err := platformNavigateResult(win, id, fixture.URL+"/probe"); err != nil {
		t.Errorf("navigate: %v", err)
	}
	select {
	case <-navigationCompleted:
		record("fixture completion observed before script")
	case <-time.After(5 * time.Second):
		t.Error("fixture NavigationCompleted timeout")
	}
	probeScript("document", "({ready:document.readyState,title:document.title,url:location.href})")
	record("nativeURL=%s errors=%s", platformGetURL(win, id), platformNavigationErrors(win, id))
	if os.Getenv("MINCODE_WEBVIEW2_PROBE_TABS") == "1" {
		second := "pipeline-second"
		record("mount second about:blank")
		if err := platformMount(win, second, "about:blank", "", 0, 0, 640, 420); err != nil {
			t.Fatalf("mount second: %v", err)
		}
		raw, err := nativeSmokeDirectScript(win, second, "1+1")
		record("second blank result=%q error=%v", raw, err)
		if err != nil || raw != "2" {
			t.Fatalf("second blank script=%q error=%v", raw, err)
		}
		if err := platformNavigateResult(win, second, fixture.URL+"/second"); err != nil {
			t.Fatal(err)
		}
		raw, err = nativeSmokeDirectScript(win, second, "({ready:document.readyState,title:document.title,url:location.href})")
		record("second document result=%q error=%v", raw, err)
		if err != nil {
			t.Fatal(err)
		}
		if err := platformActivateResult(win, id); err != nil {
			t.Fatal(err)
		}
		probeScript("first-after-second", "document.title")
	}
	// Explicitly request one repaint/position refresh for this owned test window.
	_, paintErr := browserDispatch(3*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		tab := r.tab(uintptr(win), id)
		var visible int32
		_, err := tab.controller.call(3, uintptr(unsafe.Pointer(&visible)))
		record("controller visible=%d error=%v", visible, err)
		browserUser32.NewProc("RedrawWindow").Call(tab.container, 0, 0, 0x181)
		done("")
	})
	if paintErr != nil {
		t.Error(paintErr)
	}
	if os.Getenv("MINCODE_WEBVIEW2_RAW_CAPTURE") == "1" {
		value, err := browserDispatch(5*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
			tab := r.tab(uintptr(win), id)
			stream, err := browserMemoryStream()
			if err != nil {
				done(err.Error())
				return
			}
			cb := newBrowserCallback(iidBrowserCaptureCompleted, true, func(hr uintptr, _ unsafe.Pointer) uintptr {
				record("RAW capture callback HRESULT=%08X", uint32(hr))
				done(fmt.Sprintf("callback HRESULT=%08X", uint32(hr)))
				return 0
			})
			cb.cleanup = stream.release
			_, err = tab.view.call(30, 0, uintptr(unsafe.Pointer(stream)), cb.pointer())
			cb.release()
			record("RAW capture API returned error=%v", err)
			if err != nil {
				done(err.Error())
			}
		})
		record("RAW capture result=%q error=%v", value, err)
		if err != nil {
			t.Error(err)
		}
	}
	capture := platformCapture(win, id, 5000)
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Data  string `json:"data"`
	}
	if err := json.Unmarshal([]byte(capture), &result); err != nil || !result.OK {
		t.Errorf("capture failed: %s (%v)", capture, err)
	}
	record("capture ok=%v error=%s base64Length=%d", result.OK, result.Error, len(result.Data))
	value, err := browserDispatch(2*time.Second, func(_ *windowsBrowserRuntime, done func(string)) { done("STA responsive") })
	record("heartbeat=%q error=%v", value, err)
	if err != nil {
		t.Error(err)
	}
}

// 直接验证 ExecuteScript 回调管线，不经过生产 eval 包装和页面 CSP 的 eval 限制。
func nativeSmokeDirectScript(win unsafe.Pointer, id, script string) (string, error) {
	return browserDispatch(3*time.Second, func(r *windowsBrowserRuntime, done func(string)) {
		tab := r.tab(uintptr(win), id)
		if tab == nil || tab.view == nil {
			done("原生脚本对照失败: tab 未挂载")
			return
		}
		cb := newBrowserCallback(iidBrowserScriptCompleted, false, func(hr uintptr, result unsafe.Pointer) uintptr {
			if err := browserHRESULT(hr); err != nil {
				done(fmt.Sprintf("原生脚本回调 HRESULT: %v", err))
			} else {
				done(windows.UTF16PtrToString((*uint16)(result)))
			}
			return 0
		})
		err := tab.view.stringArg(29, script, cb.pointer())
		cb.release()
		if err != nil {
			done(fmt.Sprintf("原生 ExecuteScript 调用失败: %v", err))
		}
	})
}

func nativeSmokeBool(win unsafe.Pointer, id, script string) (bool, string) {
	raw := platformEval(win, id, script, 1500)
	var result evalResult
	var value bool
	if json.Unmarshal([]byte(raw), &result) != nil || !result.OK || json.Unmarshal(result.Value, &value) != nil {
		return false, raw
	}
	return value, raw
}

func nativeSmokeAssert(t *testing.T, win unsafe.Pointer, id, script string) {
	t.Helper()
	if ok, detail := nativeSmokeBool(win, id, script); !ok {
		t.Fatalf("JavaScript 断言失败: %s\n%s", script, detail)
	}
}

func nativeSmokeWait(t *testing.T, label string, check func() (bool, string)) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	var detail string
	for {
		var ok bool
		ok, detail = check()
		if ok {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("等待 %s 超时: %s", label, detail)
		case <-ticker.C:
		}
	}
}

// 宿主有独立 OS 线程和消息泵，测试调用不占用宿主线程或浏览器 STA。
func nativeSmokeHost(t *testing.T) unsafe.Pointer {
	t.Helper()
	type hostResult struct {
		hwnd   uintptr
		thread uint32
		err    error
	}
	ready := make(chan hostResult, 1)
	stop := make(chan struct{})
	done := make(chan error, 1)
	postMessage := browserUser32.NewProc("PostMessageW")
	var owned uintptr
	var ownerThread uint32
	t.Cleanup(func() {
		close(stop)
		thread, _, _ := browserGetWindowProcess.Call(owned, 0)
		if owned != 0 && browserValidHost(owned) && uint32(thread) == ownerThread {
			if ok, _, err := postMessage.Call(owned, 0x0010, 0, 0); ok == 0 {
				t.Errorf("发送 smoke 宿主 WM_CLOSE 失败: %v", err)
			}
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("smoke 宿主消息泵失败: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("等待 smoke 自有宿主窗口退出超时")
		}
	})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		var pumpErr error
		defer func() { done <- pumpErr }()
		class, _ := windows.UTF16PtrFromString("STATIC")
		title, _ := windows.UTF16PtrFromString("MinCode WebView2 native smoke")
		hwnd, _, err := browserCreateWindow.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), 0x00cf0000|0x10000000|0x02000000, 40, 40, 1100, 800, 0, 0, 0, 0)
		runtime.KeepAlive(class)
		runtime.KeepAlive(title)
		if hwnd == 0 {
			ready <- hostResult{err: fmt.Errorf("创建可见 STATIC 宿主失败: %w", err)}
			return
		}
		timer, _, err := browserSetTimer.Call(hwnd, 0, 100, 0)
		if timer == 0 {
			browserDestroyWindow.Call(hwnd)
			ready <- hostResult{err: fmt.Errorf("创建宿主消息泵定时器失败: %w", err)}
			return
		}
		defer browserUser32.NewProc("KillTimer").Call(hwnd, timer)
		ready <- hostResult{hwnd: hwnd, thread: windows.GetCurrentThreadId()}
		var msg browserMessage
		for browserValidHost(hwnd) {
			select {
			case <-stop:
				postMessage.Call(hwnd, 0x0010, 0, 0)
			default:
			}
			result, _, err := browserGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(result) <= 0 {
				pumpErr = fmt.Errorf("宿主消息泵提前退出: result=%d error=%v", int32(result), err)
				browserDestroyWindow.Call(hwnd)
				return
			}
			browserTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			browserDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()
	select {
	case result := <-ready:
		if result.err != nil {
			t.Fatal(result.err)
		}
		owned = result.hwnd
		ownerThread = result.thread
		visible, _, _ := browserUser32.NewProc("IsWindowVisible").Call(owned)
		if visible == 0 {
			t.Fatal("smoke 宿主必须为可见窗口")
		}
		if browserOnHostUIThread(owned) {
			t.Fatal("测试意外运行于宿主 UI 线程")
		}
		return browserNativePointer(owned)
	case <-time.After(5 * time.Second):
		t.Fatal("创建 smoke 宿主窗口超时")
		return nil
	}
}
