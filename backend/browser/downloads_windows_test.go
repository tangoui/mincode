//go:build windows

package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestWindowsOpenFileNameABI(t *testing.T) {
	if got := unsafe.Offsetof(windowsOpenFileName{}.Owner); got != 8 {
		t.Fatalf("Owner offset = %d, want 8", got)
	}
	if got := unsafe.Offsetof(windowsOpenFileName{}.Filter); got != 24 {
		t.Fatalf("Filter offset = %d, want 24", got)
	}
	if got := unsafe.Offsetof(windowsOpenFileName{}.Flags); got != 96 {
		t.Fatalf("Flags offset = %d, want 96", got)
	}
	if got := unsafe.Sizeof(windowsOpenFileName{}); got != 152 {
		t.Fatalf("OPENFILENAMEW size = %d, want 152", got)
	}
}

func TestWindowsOwnedTempIsExclusiveSibling(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "result.bin")
	tmp, err := windowsOwnedTemp(target)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp)
	if filepath.Dir(tmp) != dir || filepath.Base(tmp) == filepath.Base(target) {
		t.Fatalf("temporary path is not a sibling: %q", tmp)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Fatalf("temporary file was not created: %v", err)
	}
	if _, err := windowsOwnedTemp(filepath.Join(dir, "missing", "x")); err == nil {
		t.Fatal("expected failure for missing destination directory")
	}
}

func TestWindowsNativeSavePickerHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := windowsNativeSavePicker(ctx, windowsSaveRequest{Name: "x.txt"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestWindowsDownloadCommitPreservesTargetOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(target, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := windowsCommitDownload(filepath.Join(dir, "not-created"), target); err == nil {
		t.Fatal("commit accepted missing temporary file")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "existing" {
		t.Fatalf("failed commit changed target: %q, %v", data, err)
	}
	tmp, err := windowsOwnedTemp(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte("complete"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := windowsCommitDownload(tmp, target); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(target)
	if err != nil || string(data) != "complete" {
		t.Fatalf("successful commit did not replace target: %q, %v", data, err)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed temporary file still exists: %v", err)
	}
}

func TestWindowsDownloadInterruptionClassification(t *testing.T) {
	state, code := windowsInterruptedDownload(26)
	if state != downloadStateCancelled || code != "user_cancelled" {
		t.Fatalf("user cancellation = %s %s", state, code)
	}
	for _, reason := range []int32{0, 1, 13, 25, 27} {
		state, code := windowsInterruptedDownload(reason)
		if state != downloadStateFailed || code == "user_cancelled" {
			t.Fatalf("interruption %d = %s %s", reason, state, code)
		}
	}
}

func TestWindowsDownloadCancelCleansOnlyOwnedTemp(t *testing.T) {
	resetDownloads()
	defer resetDownloads()
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(target, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	tmp, err := windowsOwnedTemp(target)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := downloadStarted("cleanup-tab", "https://example.test/", "existing.txt")
	ctx, cancel := context.WithCancel(context.Background())
	d := &windowsDownload{id: id, temporary: tmp, destination: target, cancelPicker: cancel}
	tab := &windowsBrowserTab{downloads: []*windowsDownload{d}}
	r := &windowsBrowserRuntime{}
	r.cancelDownloads(tab)
	r.cancelDownloads(tab)
	if !d.finished || len(tab.downloads) != 0 || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("cancel did not finish download and picker")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "existing" {
		t.Fatalf("cancel changed target: %q, %v", data, err)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned temporary file remains: %v", err)
	}
	records := downloadsForTab("cleanup-tab")
	if len(records) != 1 || records[0].State != downloadStateCancelled {
		t.Fatalf("cancel records = %#v", records)
	}
}

func TestWindowsDownloadPollUsesCorrectCOMStateValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		state int32
		want  string
	}{
		{"in_progress", 0, downloadStateSaving},
		{"interrupted", 1, downloadStateFailed},
		{"completed", 2, downloadStateCompleted},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetDownloads()
			defer resetDownloads()
			dir := t.TempDir()
			target := filepath.Join(dir, "target.bin")
			tmp, err := windowsOwnedTemp(target)
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(tmp)
			if err := os.WriteFile(tmp, []byte("body"), 0600); err != nil {
				t.Fatal(err)
			}
			var table [128]uintptr
			releases, cancels := 0, 0
			table[2] = syscall.NewCallback(func(uintptr) uintptr { releases++; return 0 })
			table[12] = syscall.NewCallback(func(_ uintptr, value *int64) uintptr { *value = 4; return 0 })
			table[13] = syscall.NewCallback(func(_ uintptr, value *int64) uintptr { *value = 4; return 0 })
			table[16] = syscall.NewCallback(func(_ uintptr, value *int32) uintptr { *value = test.state; return 0 })
			table[17] = syscall.NewCallback(func(_ uintptr, value *int32) uintptr { *value = 13; return 0 })
			table[18] = syscall.NewCallback(func(uintptr) uintptr { cancels++; return 0 })
			op := &browserCOM{vtable: &table}
			id, _ := downloadStarted("poll-tab", "https://example.test/", "target.bin")
			d := &windowsDownload{id: id, operation: op, temporary: tmp, destination: target, pickerDone: true}
			tab := &windowsBrowserTab{downloads: []*windowsDownload{d}}
			r := &windowsBrowserRuntime{}
			r.pollDownloads(tab)
			records := downloadsForTab("poll-tab")
			if len(records) != 1 || records[0].State != test.want || records[0].Bytes != 4 || records[0].TotalBytes != 4 {
				t.Fatalf("state %d records = %#v", test.state, records)
			}
			if test.state == 0 {
				if releases != 0 || cancels != 0 || len(tab.downloads) != 1 {
					t.Fatal("in-progress operation was released or cancelled")
				}
				r.cancelDownloads(tab)
			} else if releases != 1 || len(tab.downloads) != 0 {
				t.Fatalf("terminal operation not released exactly once: %d", releases)
			}
			if test.state == 2 {
				data, err := os.ReadFile(target)
				if err != nil || string(data) != "body" || cancels != 0 {
					t.Fatalf("completion failed: %q %v, cancels %d", data, err, cancels)
				}
			}
		})
	}
}

func TestWindowsDownloadSaveDialogNativeSmoke(t *testing.T) {
	if os.Getenv("MINCODE_WEBVIEW2_SMOKE") != "1" {
		t.Skip("设置 MINCODE_WEBVIEW2_SMOKE=1 运行真实保存面板测试")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := browserOle32.NewProc("CoInitializeEx").Call(0, 2)
	if err := browserHRESULT(hr); err != nil {
		t.Fatal(err)
	}
	defer browserOle32.NewProc("CoUninitialize").Call()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	path, err := windowsNativeSavePicker(ctx, windowsSaveRequest{Origin: "https://example.test", Name: "native-fixture.txt"})
	if path != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("原生面板创建/自动关闭失败: path=%q error=%v", path, err)
	}
}

// 仅显式 smoke 使用测试 picker；网络与所有下载 COM 对象均来自真实 WebView2。
func TestWindowsBrowserDownloadNativeSmoke(t *testing.T) {
	if os.Getenv("MINCODE_WEBVIEW2_SMOKE") != "1" {
		t.Skip("设置 MINCODE_WEBVIEW2_SMOKE=1 运行真实 WebView2 下载测试")
	}
	if !platformAvailable() {
		t.Fatal("需要 Windows x64 和已安装的 WebView2 Runtime")
	}
	_, source, _, _ := runtime.Caller(0)
	outputs := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", "outputs"))
	if err := os.MkdirAll(outputs, 0700); err != nil {
		t.Fatal(err)
	}
	profile, err := os.MkdirTemp(outputs, "webview2-download-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", profile)
	t.Logf("保留独立下载 smoke profile 用于诊断: %s", profile)
	resetDownloads()
	t.Cleanup(resetDownloads)
	const payload = "native WebView2 download body"
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download" {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="native-fixture.txt"`)
			if r.URL.Query().Get("broken") == "1" {
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)+1000))
			}
			fmt.Fprint(w, payload)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>download smoke</title><body><a id="save" href="/download">下载</a></body>`)
	}))
	t.Cleanup(fixture.Close)
	win := nativeSmokeHost(t)
	t.Cleanup(func() { _ = platformCloseResult(win) })
	const tabID = "download-native:fixture"
	if err := platformMount(win, tabID, fixture.URL, "", 0, 0, 640, 420); err != nil {
		t.Fatal(err)
	}
	nativeSmokeWait(t, "下载页面加载", func() (bool, string) {
		return nativeSmokeBool(win, tabID, `document.readyState==='complete' && document.title==='download smoke'`)
	})
	destination := filepath.Join(t.TempDir(), "existing.txt")
	if err := os.WriteFile(destination, []byte("existing body"), 0600); err != nil {
		t.Fatal(err)
	}
	original := windowsDownloadSavePicker
	var mode, calls atomic.Int32
	requests := make(chan windowsSaveRequest, 8)
	pickerStopped := make(chan struct{}, 1)
	windowsDownloadSavePicker = func(ctx context.Context, req windowsSaveRequest) (string, error) {
		calls.Add(1)
		requests <- req
		switch mode.Load() {
		case 1:
			return "", context.Canceled
		case 2:
			<-ctx.Done()
			pickerStopped <- struct{}{}
			return "", ctx.Err()
		default:
			return destination, nil
		}
	}
	t.Cleanup(func() { windowsDownloadSavePicker = original })
	broken := false
	trigger := func(authorized bool) int {
		t.Helper()
		before := len(downloadsForTab(tabID))
		if authorized {
			allowAIDownload(tabID)
		}
		// 使用唯一原生导航避开 Chromium 对连续脚本 click 下载的自动节流。
		address := fmt.Sprintf("%s/download?attempt=%d", fixture.URL, before)
		if broken {
			address += "&broken=1"
		}
		if err := platformNavigateResult(win, tabID, address); err != nil {
			t.Fatal(err)
		}
		return before
	}
	waitRecord := func(before int, state string) downloadRecord {
		t.Helper()
		var found downloadRecord
		nativeSmokeWait(t, "下载进入 "+state, func() (bool, string) {
			records := downloadsForTab(tabID)
			if len(records) <= before {
				return false, fmt.Sprint(records)
			}
			for _, record := range records[before:] {
				if record.State == state {
					found = record
					return true, fmt.Sprint(record)
				}
			}
			return false, fmt.Sprint(records[before:])
		})
		return found
	}
	checkExisting := func(want string) {
		t.Helper()
		data, err := os.ReadFile(destination)
		if err != nil || string(data) != want {
			t.Fatalf("目标内容=%q error=%v，期望 %q", data, err, want)
		}
	}

	t.Log("无用户动作的下载必须拒绝且不能触发保存面板")
	record := waitRecord(trigger(false), downloadStateCancelled)
	if record.Error != downloadErrorNoUserAction || calls.Load() != 0 {
		t.Fatalf("无许可下载未拒绝: %+v pickerCalls=%d", record, calls.Load())
	}
	checkExisting("existing body")

	t.Log("真实下载应写入临时兄弟文件并在完成后替换用户目标")
	record = waitRecord(trigger(true), downloadStateCompleted)
	if record.Filename != "native-fixture.txt" || record.Origin != fixture.URL || record.Bytes != int64(len(payload)) {
		t.Fatalf("完成下载记录错误: %+v", record)
	}
	checkExisting(payload)
	select {
	case req := <-requests:
		if req.Owner != uintptr(win) || req.Origin != fixture.URL || req.Name != "native-fixture.txt" {
			t.Fatalf("保存面板参数错误: %+v", req)
		}
	default:
		t.Fatal("未调用保存面板")
	}

	t.Log("用户取消保存不能覆盖目标")
	mode.Store(1)
	waitRecord(trigger(true), downloadStateCancelled)
	checkExisting(payload)
	<-requests

	t.Log("网络中断的下载不得替换已有目标")
	mode.Store(0)
	broken = true
	waitRecord(trigger(true), downloadStateFailed)
	checkExisting(payload)
	<-requests
	broken = false

	t.Log("保存面板等待期间浏览器 STA 必须响应且关闭 tab 取消下载")
	mode.Store(2)
	before := trigger(true)
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("等待保存面板未启动")
	}
	value, err := browserDispatch(time.Second, func(_ *windowsBrowserRuntime, done func(string)) { done("responsive") })
	if err != nil || value != "responsive" {
		t.Fatalf("保存面板阻塞浏览器 STA: %s %v", value, err)
	}
	if err := platformCloseTabResult(win, tabID); err != nil {
		t.Fatal(err)
	}
	waitRecord(before, downloadStateCancelled)
	select {
	case <-pickerStopped:
	case <-time.After(5 * time.Second):
		t.Fatal("关闭 tab 未取消保存面板")
	}
	checkExisting(payload)
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(destination) {
		t.Fatalf("下载清理后有残留临时文件: %v %v", entries, err)
	}
}

func TestWindowsDownloadPickerRequiresActiveVisibleIdentity(t *testing.T) {
	tab := &windowsBrowserTab{id: "active", host: 7, visible: true}
	host := &windowsBrowserHost{active: tab.id, tabs: map[string]*windowsBrowserTab{tab.id: tab}}
	r := &windowsBrowserRuntime{hosts: map[uintptr]*windowsBrowserHost{tab.host: host}}
	if !r.downloadPickerVisible(tab) {
		t.Fatal("当前可见标签页不允许保存面板")
	}
	checks := []struct {
		name    string
		mutate  func()
		restore func()
	}{
		{"tab_hidden", func() { tab.visible = false }, func() { tab.visible = true }},
		{"workspace_hidden", func() { host.hidden = true }, func() { host.hidden = false }},
		{"inactive_tab", func() { host.active = "other" }, func() { host.active = tab.id }},
		{"closed_tab", func() { tab.closed = true }, func() { tab.closed = false }},
		{"failed_tab", func() { tab.failure = errors.New("failed") }, func() { tab.failure = nil }},
		{"replaced_tab", func() { host.tabs[tab.id] = &windowsBrowserTab{id: tab.id} }, func() { host.tabs[tab.id] = tab }},
		{"missing_host", func() { delete(r.hosts, tab.host) }, func() { r.hosts[tab.host] = host }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			check.mutate()
			defer check.restore()
			if r.downloadPickerVisible(tab) {
				t.Fatal("后台或失效标签页仍允许保存面板")
			}
		})
	}
}

func TestWindowsDownloadHiddenCancelsPendingButKeepsSaving(t *testing.T) {
	resetDownloads()
	defer resetDownloads()
	pendingID, _ := downloadStarted("hidden", "https://example.test", "pending.txt")
	savingID, _ := downloadStarted("hidden", "https://example.test", "saving.txt")
	downloadUpdated(savingID, downloadStateSaving, 1, 10, "")
	pendingCtx, cancelPending := context.WithCancel(context.Background())
	savingCtx, cancelSaving := context.WithCancel(context.Background())
	defer cancelSaving()
	pending := &windowsDownload{id: pendingID, cancelPicker: cancelPending}
	saving := &windowsDownload{id: savingID, pickerDone: true, cancelPicker: cancelSaving}
	tab := &windowsBrowserTab{downloads: []*windowsDownload{pending, saving}}
	r := &windowsBrowserRuntime{}
	r.cancelPendingDownloadPickers(tab)
	r.cancelPendingDownloadPickers(tab)
	if !pending.finished || !errors.Is(pendingCtx.Err(), context.Canceled) || saving.finished || savingCtx.Err() != nil {
		t.Fatal("隐藏清理未仅取消等待保存面板的下载")
	}
	records := downloadsForTab("hidden")
	if len(records) != 2 || records[0].State != downloadStateCancelled || records[1].State != downloadStateSaving {
		t.Fatalf("隐藏后下载状态错误: %+v", records)
	}
}

func TestWindowsDownloadLatePickerCannotWriteHiddenTab(t *testing.T) {
	resetDownloads()
	defer resetDownloads()
	dir := t.TempDir()
	destination := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(destination, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	id, _ := downloadStarted("late", "https://example.test", "target.txt")
	d := &windowsDownload{id: id}
	tab := &windowsBrowserTab{id: "late", host: 7, visible: true}
	host := &windowsBrowserHost{active: "other", tabs: map[string]*windowsBrowserTab{tab.id: tab}}
	r := &windowsBrowserRuntime{hosts: map[uintptr]*windowsBrowserHost{tab.host: host}}
	r.finishDownloadPicker(tab, d, destination, nil)
	if !d.finished || d.pickerDone || d.temporary != "" {
		t.Fatal("后台标签页接受了迟到的保存结果")
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "existing" {
		t.Fatalf("迟到保存结果改动目标: %q %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("迟到保存结果创建了临时文件: %v %v", entries, err)
	}
}

func TestWindowsSaveDialogHookUsesCallbackABI(t *testing.T) {
	if windowsSaveDialogHook == 0 {
		t.Fatal("save dialog hook callback is not initialized")
	}
	result, _, _ := syscall.SyscallN(windowsSaveDialogHook, 0, 0, 0, 0)
	if result != 0 {
		t.Fatalf("unhandled hook message returned %d", result)
	}
}
