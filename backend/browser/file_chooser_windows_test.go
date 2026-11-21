//go:build windows

package browser

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type chooserTestCOM struct {
	browserCOM
	state *chooserTestState
}

type chooserTestCall struct {
	method string
	params map[string]any
}

type chooserTestState struct {
	receiver   *chooserTestCOM
	handler    *browserCallback
	calls      []chooserTestCall
	failMethod string
	frameTree  string
}

var chooserTestRelease = syscall.NewCallback(func(_ uintptr) uintptr { return 1 })
var chooserTestReceiver = syscall.NewCallback(func(this *chooserTestCOM, name *uint16, out **browserCOM) uintptr {
	if windows.UTF16PtrToString(name) != "Page.fileChooserOpened" {
		return 0x80004005
	}
	*out = &this.state.receiver.browserCOM
	return 0
})
var chooserTestAdd = syscall.NewCallback(func(this *chooserTestCOM, handler *browserCallback, token *int64) uintptr {
	this.state.handler = handler
	handler.refs.Add(1)
	*token = 7
	return 0
})
var chooserTestCDP = syscall.NewCallback(func(this *chooserTestCOM, method, params *uint16, callback *browserCallback) uintptr {
	name := windows.UTF16PtrToString(method)
	var values map[string]any
	_ = json.Unmarshal([]byte(windows.UTF16PtrToString(params)), &values)
	this.state.calls = append(this.state.calls, chooserTestCall{name, values})
	if this.state.failMethod == name {
		callback.invoke(0x80004005, nil)
		return 0
	}
	response := "{}"
	if name == "Page.getFrameTree" {
		response = this.state.frameTree
	}
	text, _ := windows.UTF16PtrFromString(response)
	callback.invoke(0, unsafe.Pointer(text))
	runtime.KeepAlive(text)
	return 0
})

func newChooserTestRuntime(t *testing.T) (*windowsBrowserRuntime, *windowsBrowserTab, *chooserTestState) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	name, _ := windows.UTF16PtrFromString("STATIC")
	hwnd, _, err := browserCreateWindow.Call(0, uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 1, 1, ^uintptr(2), 0, 0, 0)
	if hwnd == 0 {
		t.Fatalf("创建测试消息窗口失败: %v", err)
	}
	t.Cleanup(func() { browserDestroyWindow.Call(hwnd) })
	state := &chooserTestState{frameTree: `{"frameTree":{"frame":{"id":"main","securityOrigin":"https://parent.test"},"childFrames":[{"frame":{"id":"upload","securityOrigin":"https://upload.test"}}]}}`}
	receiverTable := &[128]uintptr{2: chooserTestRelease, 3: chooserTestAdd}
	state.receiver = &chooserTestCOM{browserCOM: browserCOM{vtable: receiverTable}, state: state}
	view := &chooserTestCOM{browserCOM: browserCOM{vtable: &[128]uintptr{2: chooserTestRelease, 36: chooserTestCDP, 42: chooserTestReceiver}}, state: state}
	tab := &windowsBrowserTab{id: "chooser-test", view: &view.browserCOM, visible: true}
	r := &windowsBrowserRuntime{dispatcher: hwnd, hosts: map[uintptr]*windowsBrowserHost{0: {active: tab.id, tabs: map[string]*windowsBrowserTab{tab.id: tab}}}}
	t.Cleanup(func() {
		if state.handler != nil {
			state.handler.release()
		}
		runtime.KeepAlive(view)
		runtime.KeepAlive(state)
	})
	return r, tab, state
}

// 该烟测替换的仅是用户选取面板，CDP 事件、文件输入框及文件内容读取均为真实 WebView2。
func TestWindowsFileChooserNativeSmoke(t *testing.T) {
	if os.Getenv("MINCODE_WEBVIEW2_CHOOSER_SMOKE") != "1" {
		t.Skip("设置 MINCODE_WEBVIEW2_CHOOSER_SMOKE=1 以验证真实 WebView2 上传链路")
	}
	if !platformAvailable() {
		t.Fatal("需要 Windows x64 与已安装的 WebView2 Runtime")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	payload := filepath.Join(t.TempDir(), "chooser-fixture.txt")
	if err := os.WriteFile(payload, []byte("chooser-private-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><title>chooser smoke</title><input id="upload" type="file" multiple><script>window.uploadChanges=0;document.querySelector('#upload').addEventListener('change',()=>uploadChanges++)</script>`))
	}))
	defer fixture.Close()
	win := nativeSmokeHost(t)
	t.Cleanup(func() { _ = platformCloseResult(win) })
	const id = "native-smoke:chooser"
	picked := make(chan string, 1)
	original := windowsFileChooserPicker
	windowsFileChooserPicker = func(_ uintptr, origin string, chooser *windowsFileChooser) ([]string, error) {
		if !chooser.multiple {
			return nil, errors.New("多选信息未到达原生面板")
		}
		picked <- origin
		return []string{payload}, nil
	}
	t.Cleanup(func() { windowsFileChooserPicker = original; resetFileChoosers() })
	if err := platformMount(win, id, fixture.URL, "", 0, 0, 640, 420); err != nil {
		t.Fatalf("挂载失败: %v", err)
	}
	nativeSmokeWait(t, "上传 fixture 加载", func() (bool, string) {
		return nativeSmokeBool(win, id, `document.readyState==='complete' && !!document.querySelector('#upload')`)
	})
	nativeSmokeAssert(t, win, id, `document.querySelector('#upload').click(); true`)
	armFileChooserWait(id)
	if waitFileChooser(id, 150*time.Millisecond) {
		t.Fatal("无授权点击不应打开选择器")
	}
	select {
	case <-picked:
		t.Fatal("无授权点击进入了用户选择面板")
	default:
	}
	allowAIFileChooser(id)
	armFileChooserWait(id)
	raw := platformEval(win, id, `document.querySelector('#upload').click(); true`, 2000)
	var result evalResult
	if json.Unmarshal([]byte(raw), &result) != nil || !result.OK {
		t.Fatalf("输入框点击失败: %s", raw)
	}
	if !waitFileChooser(id, 3*time.Second) {
		t.Fatal("未收到真实 Page.fileChooserOpened 事件")
	}
	select {
	case origin := <-picked:
		if origin != fixture.URL {
			t.Fatalf("来源错误: %s", origin)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未进入测试用户选取面板")
	}
	nativeSmokeWait(t, "真实文件附加到 DOM 输入框", func() (bool, string) {
		return nativeSmokeBool(win, id, `document.querySelector('#upload').files.length===1 && document.querySelector('#upload').files[0].name==='chooser-fixture.txt' && window.uploadChanges===1`)
	})
	nativeSmokeAssert(t, win, id, `document.querySelector('#upload').files[0].text().then(text=>window.__chooserText=text); true`)
	nativeSmokeWait(t, "网页读取用户选中的测试文件", func() (bool, string) {
		return nativeSmokeBool(win, id, `window.__chooserText==='chooser-private-fixture'`)
	})
	t.Log("验证真实 popup 的上传通道")
	allowAIPopupWindow(id)
	nativeSmokeAssert(t, win, id, `window.__chooserPopup=window.open('/popup'); true`)
	var popupID string
	nativeSmokeWait(t, "popup 建立并加载", func() (bool, string) {
		value, err := browserDispatch(time.Second, func(r *windowsBrowserRuntime, done func(string)) {
			for _, tab := range r.hosts[uintptr(win)].tabs {
				if tab.popup && tab.mountDone == nil {
					done(tab.id)
					return
				}
			}
			done("")
		})
		popupID = value
		return value != "", browserJSON(err)
	})
	if err := platformActivateResult(win, popupID); err != nil {
		t.Fatal(err)
	}
	nativeSmokeWait(t, "popup 上传控件加载", func() (bool, string) {
		return nativeSmokeBool(win, popupID, `document.readyState==='complete' && !!document.querySelector('#upload')`)
	})
	allowAIFileChooser(popupID)
	armFileChooserWait(popupID)
	nativeSmokeAssert(t, win, popupID, `document.querySelector('#upload').click(); true`)
	if !waitFileChooser(popupID, 3*time.Second) {
		t.Fatal("popup 的文件上传事件没有被接管")
	}
	nativeSmokeWait(t, "popup 文件附加与 change 事件", func() (bool, string) {
		return nativeSmokeBool(win, popupID, `document.querySelector('#upload').files.length===1 && window.uploadChanges===1`)
	})
	<-picked

	t.Log("验证关闭真实 popup 时取消等待中的用户选择")
	started, release, returned := make(chan *windowsFileChooser, 1), make(chan struct{}), make(chan struct{})
	windowsFileChooserPicker = func(_ uintptr, _ string, chooser *windowsFileChooser) ([]string, error) {
		started <- chooser
		<-release
		close(returned)
		return []string{payload}, nil
	}
	allowAIFileChooser(popupID)
	nativeSmokeAssert(t, win, popupID, `document.querySelector('#upload').click(); true`)
	var chooser *windowsFileChooser
	select {
	case chooser = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("关闭验收的用户面板未打开")
	}
	if err := platformCloseTabResult(win, popupID); err != nil {
		t.Fatal(err)
	}
	if !chooser.cancelled.Load() || fileChooserIsOpen(popupID) {
		t.Fatal("关闭标签未取消文件选择器")
	}
	close(release)
	<-returned
	if _, err := browserDispatch(time.Second, func(_ *windowsBrowserRuntime, done func(string)) { done("") }); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsFileChooserNativePanelCancelSmoke(t *testing.T) {
	if os.Getenv("MINCODE_WEBVIEW2_CHOOSER_SMOKE") != "1" {
		t.Skip("设置 MINCODE_WEBVIEW2_CHOOSER_SMOKE=1 以验证真实文件选择面板取消")
	}
	win := nativeSmokeHost(t)
	chooser := &windowsFileChooser{}
	type pickResult struct {
		files []string
		err   error
	}
	done := make(chan pickResult, 1)
	go func() {
		files, err := browserPickUploadFiles(uintptr(win), "https://upload.example.test", chooser)
		done <- pickResult{files, err}
	}()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for chooser.window.Load() == 0 {
		select {
		case result := <-done:
			t.Fatalf("真实面板未创建即退出: %v", result.err)
		case <-deadline.C:
			chooser.cancelled.Store(true)
			t.Fatal("等待真实文件选择面板创建超时")
		case <-ticker.C:
		}
	}
	hwnd := chooser.window.Load()
	chooser.cancelled.Store(true)
	browserPostMessage.Call(hwnd, 0x0111, 2, 0)
	select {
	case result := <-done:
		if result.err != nil || len(result.files) != 0 || chooser.window.Load() != 0 {
			t.Fatalf("原生面板取消结果错误: files=%d error=%v", len(result.files), result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("真实 GetOpenFileNameW 面板无法取消")
	}
}

func TestWindowsFileChooserConfigurationWaitsForProtocol(t *testing.T) {
	r, tab, state := newChooserTestRuntime(t)
	finished := false
	r.configureFileChooser(tab, func(err error) {
		if err != nil {
			t.Errorf("配置失败: %v", err)
		}
		finished = true
	})
	if finished || len(state.calls) != 1 || state.calls[0].method != "Page.enable" {
		t.Fatalf("未等待 Page.enable 完成: %+v", state.calls)
	}
	r.drain()
	if finished || len(state.calls) != 2 || state.calls[1].method != "Page.setInterceptFileChooserDialog" || state.calls[1].params["enabled"] != true {
		t.Fatalf("协议配置顺序错误: %+v", state.calls)
	}
	r.drain()
	if !finished || len(tab.events) != 1 || tab.events[0].owner != &state.receiver.browserCOM || tab.events[0].slot != 4 {
		t.Fatal("完成回调或接收器生命周期不正确")
	}
}

func TestWindowsFileChooserConfigurationFailureStopsMount(t *testing.T) {
	for _, method := range []string{"Page.enable", "Page.setInterceptFileChooserDialog"} {
		t.Run(method, func(t *testing.T) {
			r, tab, state := newChooserTestRuntime(t)
			state.failMethod = method
			var failure error
			calls := 0
			r.configureFileChooser(tab, func(err error) { failure = err; calls++ })
			r.drain()
			r.drain()
			if failure == nil || calls != 1 {
				t.Fatalf("失败被吞掉或重复回调: %v, %d", failure, calls)
			}
			if method == "Page.enable" && len(state.calls) != 1 {
				t.Fatal("Page.enable 失败后仍然启用拦截")
			}
		})
	}
}

func TestWindowsFileChooserRequiresAllowanceAndValidEvent(t *testing.T) {
	r, tab, state := newChooserTestRuntime(t)
	recordEvents(t)
	t.Cleanup(resetFileChoosers)
	for _, raw := range []string{`{}`, `{"frameId":"upload","backendNodeId":0,"mode":"selectSingle"}`, `{"frameId":"upload","backendNodeId":1,"mode":"selectFolder"}`, `{"frameId":"upload","backendNodeId":1,"mode":"selectSingle"}`} {
		r.openFileChooser(tab, raw)
	}
	if tab.fileChooser != nil || len(state.calls) != 0 || fileChooserIsOpen(tab.id) {
		t.Fatal("无授权或无效事件打开了选择器")
	}
}

func TestWindowsFileChooserRejectsHiddenOrInactiveTabs(t *testing.T) {
	r, tab, state := newChooserTestRuntime(t)
	recordEvents(t)
	t.Cleanup(resetFileChoosers)
	host := r.hosts[tab.host]
	for _, change := range []func(){
		func() { tab.visible = false },
		func() { tab.visible = true; host.hidden = true },
		func() { host.hidden = false; host.active = "other-tab" },
	} {
		change()
		allowAIFileChooser(tab.id)
		r.openFileChooser(tab, `{"frameId":"upload","backendNodeId":1,"mode":"selectSingle"}`)
		if tab.fileChooser != nil || len(state.calls) != 0 {
			t.Fatal("隐藏或非当前标签打开了选择器")
		}
	}
}

func TestWindowsFileChooserSelectionKeepsPathsPrivate(t *testing.T) {
	r, tab, state := newChooserTestRuntime(t)
	events := recordEvents(t)
	t.Cleanup(resetFileChoosers)
	original := windowsFileChooserPicker
	windowsFileChooserPicker = func(_ uintptr, origin string, chooser *windowsFileChooser) ([]string, error) {
		if origin != "https://upload.test" || !chooser.multiple {
			return nil, errors.New("来源或多选标记错误")
		}
		return []string{`C:\private\one.txt`, `D:\private\two.txt`}, nil
	}
	t.Cleanup(func() { windowsFileChooserPicker = original })
	allowAIFileChooser(tab.id)
	r.openFileChooser(tab, `{"frameId":"upload","backendNodeId":23,"mode":"selectMultiple"}`)
	if tab.fileChooser == nil || !fileChooserIsOpen(tab.id) {
		t.Fatal("未报告选择器已打开")
	}
	deadline := time.Now().Add(time.Second)
	for tab.fileChooser != nil && time.Now().Before(deadline) {
		r.drain()
		runtime.Gosched()
	}
	if tab.fileChooser != nil || len(state.calls) != 2 || state.calls[1].method != "DOM.setFileInputFiles" || state.calls[1].params["backendNodeId"] != float64(23) {
		t.Fatalf("未完成原生选择: %+v", state.calls)
	}
	got := eventsNamed(events(), fileChooserEvent)
	if len(got) != 2 || got[1].Data != (fileChooserState{TabID: tab.id, SelectedCount: 2}) {
		t.Fatalf("选择状态事件错误: %+v", got)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "one.txt") {
		t.Fatal("本地路径泄漏到事件")
	}
}

func TestWindowsFileChooserCancellationIgnoresLateSelection(t *testing.T) {
	r, tab, state := newChooserTestRuntime(t)
	events := recordEvents(t)
	t.Cleanup(resetFileChoosers)
	allowAIFileChooser(tab.id)
	r.openFileChooser(tab, `{"frameId":"upload","backendNodeId":23,"mode":"selectSingle"}`)
	chooser := tab.fileChooser
	r.cancelFileChooser(tab)
	r.cancelFileChooser(tab)
	r.drain()
	if !chooser.cancelled.Load() || tab.fileChooser != nil || fileChooserIsOpen(tab.id) || len(state.calls) != 1 {
		t.Fatal("取消后仍使用选择结果")
	}
	got := eventsNamed(events(), fileChooserEvent)
	if len(got) != 2 || got[1].Data != (fileChooserState{TabID: tab.id}) {
		t.Fatalf("取消应只报告一次关闭: %+v", got)
	}
}

func TestWindowsFileChooserCancellationDuringNativePick(t *testing.T) {
	r, tab, state := newChooserTestRuntime(t)
	events := recordEvents(t)
	t.Cleanup(resetFileChoosers)
	original := windowsFileChooserPicker
	started, finish, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	windowsFileChooserPicker = func(_ uintptr, _ string, _ *windowsFileChooser) ([]string, error) {
		close(started)
		<-finish
		close(returned)
		return []string{`C:\private\late.txt`}, nil
	}
	t.Cleanup(func() { windowsFileChooserPicker = original })
	allowAIFileChooser(tab.id)
	r.openFileChooser(tab, `{"frameId":"upload","backendNodeId":23,"mode":"selectSingle"}`)
	r.drain()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("原生选择器未启动")
	}
	r.cancelFileChooser(tab)
	close(finish)
	<-returned
	deadline := time.Now().Add(time.Second)
	for {
		r.mu.Lock()
		pending := len(r.queue)
		r.mu.Unlock()
		if pending != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("原生选择结果未返回 STA 队列")
		}
		runtime.Gosched()
	}
	r.drain()
	if len(state.calls) != 1 || fileChooserIsOpen(tab.id) {
		t.Fatal("取消后的迟到选择仍被附加到输入框")
	}
	if got := eventsNamed(events(), fileChooserEvent); len(got) != 2 || got[1].Data != (fileChooserState{TabID: tab.id}) {
		t.Fatalf("取消后状态事件错误: %+v", got)
	}
}

func TestWindowsFileChooserRejectsSelectionAfterTabHidden(t *testing.T) {
	for _, hide := range []string{"invisible", "host-hidden", "inactive"} {
		t.Run(hide, func(t *testing.T) {
			r, tab, state := newChooserTestRuntime(t)
			recordEvents(t)
			t.Cleanup(resetFileChoosers)
			original := windowsFileChooserPicker
			started, release := make(chan struct{}), make(chan struct{})
			windowsFileChooserPicker = func(_ uintptr, _ string, _ *windowsFileChooser) ([]string, error) {
				close(started)
				<-release
				return []string{`C:\private\background.txt`}, nil
			}
			t.Cleanup(func() { windowsFileChooserPicker = original })
			allowAIFileChooser(tab.id)
			r.openFileChooser(tab, `{"frameId":"upload","backendNodeId":23,"mode":"selectSingle"}`)
			r.drain()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("选择面板没有启动")
			}
			switch hide {
			case "invisible":
				tab.visible = false
			case "host-hidden":
				r.hosts[tab.host].hidden = true
			case "inactive":
				r.hosts[tab.host].active = "other"
			}
			close(release)
			deadline := time.Now().Add(time.Second)
			for tab.fileChooser != nil && time.Now().Before(deadline) {
				r.drain()
				runtime.Gosched()
			}
			if tab.fileChooser != nil || len(state.calls) != 1 || fileChooserIsOpen(tab.id) {
				t.Fatal("后台标签的迟到文件结果未被拒绝")
			}
		})
	}
}

func TestWindowsFileChooserFrameOriginAndNativeBuffer(t *testing.T) {
	tree := []byte(`{"frameTree":{"frame":{"id":"main","securityOrigin":"https://parent.test"},"childFrames":[{"frame":{"id":"sub","securityOrigin":"https://user:secret@upload.test:8443/path?token=x#fragment"}}]}}`)
	if got := browserChooserFrameOrigin(tree, "sub"); got != "https://upload.test:8443" {
		t.Fatalf("来源必须来自对应 frame 且移除敏感部分: %q", got)
	}
	if got := browserChooserFrameOrigin(tree, "missing"); got != "" {
		t.Fatalf("不存在的 frame 来源: %q", got)
	}
	buffer := []uint16{'C', ':', '\\', 'a', 0, 'x', '.', 't', 'x', 't', 0, 'y', '.', 't', 'x', 't', 0, 0}
	if got := browserChooserPaths(buffer, true); !reflect.DeepEqual(got, []string{`C:\a\x.txt`, `C:\a\y.txt`}) {
		t.Fatalf("多选缓冲区解析错误: %q", got)
	}
	if got := browserChooserPaths(buffer, false); got != nil {
		t.Fatal("单选错误接受多选缓冲区")
	}
	if got := browserChooserPaths([]uint16{'x'}, false); got != nil {
		t.Fatal("接受了未终止缓冲区")
	}
	if runtime.GOARCH == "amd64" && unsafe.Sizeof(browserOpenFilename{}) != 152 {
		t.Fatalf("OPENFILENAMEW ABI 大小错误: %d", unsafe.Sizeof(browserOpenFilename{}))
	}
}
