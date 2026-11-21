//go:build windows

package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsDownload 及其 COM 引用只属于浏览器 STA；目标路径不会进入事件或工具结果。
type windowsDownload struct {
	args, operation, deferral *browserCOM
	id, filename, origin, uri string
	destination, temporary    string
	cancelPicker              context.CancelFunc
	pickerDone, finished      bool
	bytes, total              int64
}

// windowsSaveRequest 仅包含复制后的值，不向保存面板线程传递 WebView2 COM 对象。
type windowsSaveRequest struct {
	Owner  uintptr
	Origin string
	Name   string
}

var (
	windowsComdlg32             = windows.NewLazySystemDLL("comdlg32.dll")
	windowsGetSaveFileName      = windowsComdlg32.NewProc("GetSaveFileNameW")
	windowsCommDlgExtendedError = windowsComdlg32.NewProc("CommDlgExtendedError")
	windowsGetParent            = browserUser32.NewProc("GetParent")

	// 真实设备测试可替换此包私有入口；生产环境始终要求用户操作原生保存面板。
	windowsDownloadSavePicker = windowsNativeSavePicker
)

// OPENFILENAMEW 的布局由 ABI 测试锁定，不能将 DWORD 改成 uintptr。
type windowsOpenFileName struct {
	StructSize    uint32
	Owner         uintptr
	Instance      uintptr
	Filter        *uint16
	CustomFilter  *uint16
	MaxCustFilter uint32
	FilterIndex   uint32
	File          *uint16
	MaxFile       uint32
	FileTitle     *uint16
	MaxFileTitle  uint32
	InitialDir    *uint16
	Title         *uint16
	Flags         uint32
	FileOffset    uint16
	FileExtension uint16
	DefExt        *uint16
	CustData      unsafe.Pointer
	Hook          uintptr
	TemplateName  *uint16
	Reserved      uintptr
	Reserved2     uint32
	FlagsEx       uint32
}

type windowsSaveDialogState struct {
	window    atomic.Uintptr
	cancelled atomic.Bool
}

var windowsSaveDialogHook = syscall.NewCallback(func(hwnd uintptr, message uint32, _ uintptr, param *windowsOpenFileName) uintptr {
	if message == 0x0110 && param != nil { // WM_INITDIALOG；Explorer hook 收到子面板句柄。
		state := (*windowsSaveDialogState)(param.CustData)
		parent, _, _ := windowsGetParent.Call(hwnd)
		if parent == 0 {
			parent = hwnd
		}
		state.window.Store(parent)
		if state.cancelled.Load() {
			browserPostMessage.Call(parent, 0x0010, 0, 0) // WM_CLOSE
		}
	}
	return 0
})

func windowsNativeSavePicker(ctx context.Context, request windowsSaveRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name := sanitizeDownloadFilename(request.Name)
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return "", err
	}
	file := make([]uint16, 32768)
	copy(file, encoded)
	filter := utf16.Encode([]rune("All files\x00*.*\x00\x00"))
	titleText := "保存下载"
	if request.Origin != "" {
		titleText += " — " + request.Origin
	}
	title, err := windows.UTF16FromString(titleText)
	if err != nil {
		return "", err
	}
	state := &windowsSaveDialogState{}
	var pin runtime.Pinner
	pin.Pin(state)
	defer pin.Unpin()
	closed := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			state.cancelled.Store(true)
			if hwnd := state.window.Load(); hwnd != 0 {
				browserPostMessage.Call(hwnd, 0x0010, 0, 0)
			}
		case <-closed:
		}
	}()
	defer func() {
		close(closed)
		<-watchDone
	}()
	dialog := windowsOpenFileName{
		Owner: request.Owner, Filter: &filter[0], File: &file[0], MaxFile: uint32(len(file)), Title: &title[0],
		// EXPLORER | OVERWRITEPROMPT | PATHMUSTEXIST | NOCHANGEDIR | ENABLEHOOK |
		// NOTESTFILECREATE：面板只选路径，不创建或测试写入最终目标。
		Flags: 0x00080000 | 0x00000002 | 0x00000800 | 0x00000008 | 0x00000020 | 0x00010000,
		Hook:  windowsSaveDialogHook, CustData: unsafe.Pointer(state),
	}
	dialog.StructSize = uint32(unsafe.Sizeof(dialog))
	ok, _, _ := windowsGetSaveFileName.Call(uintptr(unsafe.Pointer(&dialog)))
	runtime.KeepAlive(file)
	runtime.KeepAlive(filter)
	runtime.KeepAlive(title)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if ok == 0 {
		code, _, _ := windowsCommDlgExtendedError.Call()
		if code == 0 {
			return "", context.Canceled
		}
		return "", fmt.Errorf("save dialog failed: 0x%x", code)
	}
	return windows.UTF16PtrToString(dialog.File), nil
}

func (r *windowsBrowserRuntime) configureDownloads(tab *windowsBrowserTab) error {
	view4, err := tab.view.query("{20D02D59-6DF2-42DC-BD06-F98A694B1302}")
	if err != nil {
		return fmt.Errorf("WebView2 runtime is too old for safe download handling: %w", err)
	}
	cb := newBrowserCallback("{EFEDC989-C396-41CA-83F7-07F845A55724}", false, func(_ uintptr, raw unsafe.Pointer) uintptr {
		r.downloadStarting(tab, (*browserCOM)(raw))
		return 0
	})
	var token int64
	_, err = view4.call(75, cb.pointer(), uintptr(unsafe.Pointer(&token)))
	cb.release()
	if err != nil {
		view4.release()
		return err
	}
	tab.events = append(tab.events, browserEvent{slot: 76, token: token, owner: view4})
	return nil
}

func windowsDownloadName(raw string) string {
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return sanitizeDownloadFilename(filepath.Base(u.Path))
	}
	return "download"
}

func (r *windowsBrowserRuntime) downloadPickerVisible(tab *windowsBrowserTab) bool {
	if tab == nil || tab.closed || tab.failure != nil || !tab.visible {
		return false
	}
	host := r.hosts[tab.host]
	return host != nil && !host.hidden && host.active == tab.id && host.tabs[tab.id] == tab
}

func (r *windowsBrowserRuntime) downloadStarting(tab *windowsBrowserTab, args *browserCOM) {
	if args == nil {
		return
	}
	// 默认取消并隐藏 WebView2 自带面板；只有保存面板确认成功才能清除取消位。
	_, cancelErr := args.call(5, 1)
	_, handledErr := args.call(9, 1)
	if !r.downloadPickerVisible(tab) || cancelErr != nil || handledErr != nil {
		return
	}
	op, err := args.object(3)
	if err != nil || op == nil {
		return
	}
	uri, err := op.text(9)
	if err != nil || !downloadURLAllowed(uri) {
		op.release()
		return
	}
	allowed := false
	if !tab.downloadAuthorizedUntil.IsZero() && time.Now().Before(tab.downloadAuthorizedUntil) {
		allowed = true
		tab.downloadAuthorizedUntil = time.Time{}
	}
	if !allowed {
		allowed = admitDownload(tab.id)
	}
	suggested, _ := args.text(6)
	if suggested == "" {
		suggested = windowsDownloadName(uri)
	}
	origin := sanitizeDownloadOrigin(uri)
	if origin == "" { // blob/data 下载的来源显示创建它的网页，而不是内嵌数据。
		origin = sanitizeDownloadOrigin(tab.url)
	}
	id, name := downloadStarted(tab.id, origin, suggested)
	if !allowed {
		// 断流时 Chromium 可能为同一 URI 再发起一个下载；不重复展示保存
		// 面板，也不能让此前已接受的操作及其临时文件永久悬挂。
		for _, previous := range tab.downloads {
			if !previous.finished && previous.uri == uri && previous.pickerDone {
				r.finishDownload(previous, downloadStateFailed, "download_restarted")
			}
		}
		// Chromium 对断流响应可能再次触发 DownloadStarting；拒绝时也取消操作，
		// 避免其先前创建的下载一直停留在 IN_PROGRESS 并持有临时文件。
		_, _ = op.call(18)
		op.release()
		downloadUpdated(id, downloadStateCancelled, 0, 0, downloadErrorNoUserAction)
		return
	}
	deferral, err := args.object(10)
	if err != nil || deferral == nil {
		op.release()
		downloadUpdated(id, downloadStateFailed, 0, 0, "deferral_unavailable")
		return
	}
	args.retain()
	ctx, cancel := context.WithCancel(context.Background())
	d := &windowsDownload{args: args, operation: op, deferral: deferral, id: id, filename: name, origin: origin, uri: uri, cancelPicker: cancel}
	tab.downloads = append(tab.downloads, d)
	request := windowsSaveRequest{Owner: tab.host, Origin: origin, Name: name}
	picker := windowsDownloadSavePicker
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		hr, _, _ := browserOle32.NewProc("CoInitializeEx").Call(0, 2)
		pickerErr := browserHRESULT(hr)
		path := ""
		if pickerErr == nil {
			path, pickerErr = picker(ctx, request)
			browserOle32.NewProc("CoUninitialize").Call()
		}
		_ = r.post(func() { r.finishDownloadPicker(tab, d, path, pickerErr) })
	}()
}

func (r *windowsBrowserRuntime) finishDownloadPicker(tab *windowsBrowserTab, d *windowsDownload, path string, pickerErr error) {
	if d.finished {
		return
	}
	if !r.downloadPickerVisible(tab) {
		r.finishDownload(d, downloadStateCancelled, "tab_inactive")
		return
	}
	if pickerErr != nil || strings.TrimSpace(path) == "" {
		state, code := downloadStateCancelled, "save_cancelled"
		if pickerErr != nil && !errors.Is(pickerErr, context.Canceled) {
			state, code = downloadStateFailed, "save_dialog_failed"
		}
		r.finishDownload(d, state, code)
		return
	}
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		r.finishDownload(d, downloadStateFailed, "invalid_destination")
		return
	}
	path = filepath.Clean(path)
	tmp, err := windowsOwnedTemp(path)
	if err != nil {
		r.finishDownload(d, downloadStateFailed, "temporary_file_unavailable")
		return
	}
	d.destination, d.temporary = path, tmp
	if err := d.args.stringArg(7, tmp); err != nil {
		r.finishDownload(d, downloadStateFailed, "result_path_unavailable")
		return
	}
	if _, err := d.args.call(5, 0); err != nil {
		r.finishDownload(d, downloadStateFailed, "download_start_failed")
		return
	}
	d.pickerDone = true
	downloadDestination(d.id, path)
	downloadUpdated(d.id, downloadStateSaving, 0, 0, "")
	r.completeDownloadDeferral(d)
}

func windowsOwnedTemp(destination string) (string, error) {
	// 与目标同目录才能以 MoveFileEx 原子提交；仅删除此处独占创建的文件。
	f, err := os.CreateTemp(filepath.Dir(destination), ".mincode-download-*.tmp")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func (r *windowsBrowserRuntime) completeDownloadDeferral(d *windowsDownload) {
	if d.deferral != nil {
		_, _ = d.deferral.call(3)
		d.deferral.release()
		d.deferral = nil
	}
	if d.args != nil {
		d.args.release()
		d.args = nil
	}
}

func (r *windowsBrowserRuntime) finishDownload(d *windowsDownload, state, code string) {
	if d.finished {
		return
	}
	d.finished = true
	if d.cancelPicker != nil {
		d.cancelPicker()
		d.cancelPicker = nil
	}
	if state != downloadStateCompleted {
		if d.args != nil {
			_, _ = d.args.call(5, 1)
		}
		if d.operation != nil {
			_, _ = d.operation.call(18)
		}
	}
	r.completeDownloadDeferral(d)
	if d.operation != nil {
		d.operation.release()
		d.operation = nil
	}
	if d.temporary != "" {
		windowsRemoveDownloadTemp(d.temporary)
		d.temporary = ""
	}
	downloadUpdated(d.id, state, d.bytes, d.total, code)
}

func windowsRemoveDownloadTemp(path string) {
	if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
		return
	}
	// WebView2 的取消有时稍后才关闭写入句柄；只重试已知归属的精确文件路径。
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for attempts := 0; attempts < 50; attempts++ {
			<-ticker.C
			if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
				return
			}
		}
	}()
}

func (r *windowsBrowserRuntime) cancelDownloadRecord(d *windowsDownload) {
	r.finishDownload(d, downloadStateCancelled, "tab_closed")
}

func (r *windowsBrowserRuntime) cancelPendingDownloadPickers(tab *windowsBrowserTab) {
	for _, d := range tab.downloads {
		if !d.pickerDone {
			r.finishDownload(d, downloadStateCancelled, "tab_inactive")
		}
	}
}

func (r *windowsBrowserRuntime) cancelDownloads(tab *windowsBrowserTab) {
	for _, d := range tab.downloads {
		r.cancelDownloadRecord(d)
	}
	tab.downloads = nil
	tab.downloadAuthorizedUntil = time.Time{}
}

func (r *windowsBrowserRuntime) pollDownloads(tab *windowsBrowserTab) {
	active := tab.downloads[:0]
	for _, d := range tab.downloads {
		if !d.finished && d.operation != nil && d.pickerDone {
			var state, reason int32
			if _, err := d.operation.call(16, uintptr(unsafe.Pointer(&state))); err != nil {
				r.finishDownload(d, downloadStateFailed, "download_state_unavailable")
			} else {
				_, _ = d.operation.call(12, uintptr(unsafe.Pointer(&d.total)))
				_, _ = d.operation.call(13, uintptr(unsafe.Pointer(&d.bytes)))
				switch state {
				case 0: // COREWEBVIEW2_DOWNLOAD_STATE_IN_PROGRESS
					downloadUpdated(d.id, downloadStateSaving, d.bytes, d.total, "")
				case 1: // COREWEBVIEW2_DOWNLOAD_STATE_INTERRUPTED
					_, _ = d.operation.call(17, uintptr(unsafe.Pointer(&reason)))
					state, code := windowsInterruptedDownload(reason)
					r.finishDownload(d, state, code)
				case 2: // COREWEBVIEW2_DOWNLOAD_STATE_COMPLETED
					if err := windowsCommitDownload(d.temporary, d.destination); err != nil {
						r.finishDownload(d, downloadStateFailed, "finalize_failed")
					} else {
						d.temporary = ""
						r.finishDownload(d, downloadStateCompleted, "")
					}
				default:
					r.finishDownload(d, downloadStateFailed, "invalid_download_state")
				}
			}
		}
		if !d.finished {
			active = append(active, d)
		}
	}
	tab.downloads = active
}

func windowsInterruptedDownload(reason int32) (string, string) {
	if reason == 26 { // COREWEBVIEW2_DOWNLOAD_INTERRUPT_REASON_USER_CANCELED
		return downloadStateCancelled, "user_cancelled"
	}
	return downloadStateFailed, fmt.Sprintf("download_interrupted_%d", reason)
}

func windowsCommitDownload(temporary, destination string) error {
	from, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
