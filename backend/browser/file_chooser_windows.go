//go:build windows

package browser

import (
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	iidBrowserDevToolsCompleted = "{5C4889F0-5EF6-4C5A-952C-D8F1B92D0574}"
	iidBrowserDevToolsEvent     = "{E2FDA4BE-5456-406C-A261-3D452138362C}"
)

type windowsFileChooser struct {
	frameID       string
	backendNodeID int64
	multiple      bool
	cancelled     atomic.Bool
	window        atomic.Uintptr
}

type windowsFileChooserEvent struct {
	FrameID       string `json:"frameId"`
	BackendNodeID int64  `json:"backendNodeId"`
	Mode          string `json:"mode"`
}

// 只允许测试替换；生产环境始终由用户在原生面板中选取文件。
var windowsFileChooserPicker = browserPickUploadFiles

func browserChooserError() error {
	return errors.New("WebView2 文件选择器操作失败；请重新打开浏览器后重试")
}

// browserChooserCDP 仅通过当前内嵌 WebView2 的私有 COM 通道调用固定协议方法。
// 协议响应和参数可能含本地路径，不进入日志、事件或对外错误。
func (r *windowsBrowserRuntime) browserChooserCDP(tab *windowsBrowserTab, method string, params any, done func([]byte, error)) {
	view := tab.view
	if tab.closed || view == nil {
		done(nil, browserChooserError())
		return
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		done(nil, browserChooserError())
		return
	}
	methodText, err := windows.UTF16PtrFromString(method)
	if err != nil {
		done(nil, browserChooserError())
		return
	}
	paramsText, err := windows.UTF16PtrFromString(string(encoded))
	if err != nil {
		done(nil, browserChooserError())
		return
	}
	completed := false
	complete := func(data []byte, err error) {
		if completed {
			return
		}
		completed = true
		if tab.closed || tab.view != view {
			done(nil, browserChooserError())
			return
		}
		done(data, err)
	}
	cb := newBrowserCallback(iidBrowserDevToolsCompleted, false, func(hr uintptr, result unsafe.Pointer) uintptr {
		var data []byte
		var callbackErr error
		if browserHRESULT(hr) != nil {
			callbackErr = browserChooserError()
		} else {
			data = []byte(windows.UTF16PtrToString((*uint16)(result)))
			var envelope struct {
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal(data, &envelope) != nil || len(envelope.Error) != 0 {
				callbackErr = browserChooserError()
			}
		}
		if err := r.post(func() { complete(data, callbackErr) }); err != nil {
			complete(nil, browserChooserError())
		}
		return 0
	})
	_, err = view.call(36, uintptr(unsafe.Pointer(methodText)), uintptr(unsafe.Pointer(paramsText)), cb.pointer())
	cb.release()
	runtime.KeepAlive(methodText)
	runtime.KeepAlive(paramsText)
	if err != nil {
		complete(nil, browserChooserError())
	}
}

// configureFileChooser 必须在首次导航之前完成两次协议调用；任何失败均阻止挂载。
func (r *windowsBrowserRuntime) configureFileChooser(tab *windowsBrowserTab, done func(error)) {
	if tab.view == nil || tab.closed {
		done(browserChooserError())
		return
	}
	var receiver *browserCOM
	if err := tab.view.stringArg(42, "Page.fileChooserOpened", uintptr(unsafe.Pointer(&receiver))); err != nil || receiver == nil {
		if receiver != nil {
			receiver.release()
		}
		done(browserChooserError())
		return
	}
	view := tab.view
	cb := newBrowserCallback(iidBrowserDevToolsEvent, false, func(_ uintptr, args unsafe.Pointer) uintptr {
		raw, err := (*browserCOM)(args).text(3)
		if err == nil {
			_ = r.post(func() {
				if !tab.closed && tab.view == view {
					r.openFileChooser(tab, raw)
				}
			})
		}
		return 0
	})
	var token int64
	_, err := receiver.call(3, cb.pointer(), uintptr(unsafe.Pointer(&token)))
	cb.release()
	if err != nil {
		receiver.release()
		done(browserChooserError())
		return
	}
	tab.events = append(tab.events, browserEvent{slot: 4, token: token, owner: receiver})
	r.browserChooserCDP(tab, "Page.enable", map[string]any{}, func(_ []byte, err error) {
		if err != nil {
			done(err)
			return
		}
		r.browserChooserCDP(tab, "Page.setInterceptFileChooserDialog", map[string]any{"enabled": true}, func(_ []byte, err error) { done(err) })
	})
}

func (r *windowsBrowserRuntime) openFileChooser(tab *windowsBrowserTab, raw string) {
	var event windowsFileChooserEvent
	if json.Unmarshal([]byte(raw), &event) != nil || event.FrameID == "" || (event.Mode != "selectSingle" && event.Mode != "selectMultiple") {
		return
	}
	if event.BackendNodeID <= 0 {
		r.recordError(tab, tab.url, -1, "该文件选择器不提供文件输入节点，当前仅支持 input[type=file]；请使用普通文件上传控件")
		return
	}
	host := r.hosts[tab.host]
	if tab.closed || tab.view == nil || !tab.visible || host == nil || host.hidden || host.active != tab.id || host.tabs[tab.id] != tab || tab.fileChooser != nil || fileChooserIsOpen(tab.id) || !admitFileChooser(tab.id) {
		return
	}
	chooser := &windowsFileChooser{frameID: event.FrameID, backendNodeID: event.BackendNodeID, multiple: event.Mode == "selectMultiple"}
	tab.fileChooser = chooser
	fileChooserOpened(tab.id)
	r.browserChooserCDP(tab, "Page.getFrameTree", map[string]any{}, func(data []byte, err error) {
		if tab.fileChooser != chooser || chooser.cancelled.Load() {
			return
		}
		host := r.hosts[tab.host]
		if !tab.visible || host == nil || host.hidden || host.active != tab.id || host.tabs[tab.id] != tab {
			r.cancelFileChooser(tab)
			return
		}
		origin := browserChooserFrameOrigin(data, chooser.frameID)
		if err != nil || origin == "" {
			r.cancelFileChooser(tab)
			return
		}
		picker := windowsFileChooserPicker
		go func() {
			files, pickErr := picker(tab.host, origin, chooser)
			_ = r.post(func() {
				if tab.closed || tab.fileChooser != chooser || chooser.cancelled.Load() {
					return
				}
				host := r.hosts[tab.host]
				if pickErr != nil || len(files) == 0 || !tab.visible || host == nil || host.hidden || host.active != tab.id || host.tabs[tab.id] != tab {
					r.cancelFileChooser(tab)
					return
				}
				r.browserChooserCDP(tab, "DOM.setFileInputFiles", map[string]any{"files": files, "backendNodeId": chooser.backendNodeID}, func(_ []byte, err error) {
					if tab.fileChooser != chooser {
						return
					}
					count := len(files)
					if err != nil {
						count = 0
					}
					tab.fileChooser = nil
					fileChooserClosed(tab.id, count)
				})
			})
		}()
	})
}

// CDP 没有稳定的取消回调 API；取消只关闭宿主面板并丢弃选择结果，不伪造网页 cancel 事件。
// files:[] 只会清空文件列表，因此不能把它当作取消操作发送。
func (r *windowsBrowserRuntime) cancelFileChooser(tab *windowsBrowserTab) {
	chooser := tab.fileChooser
	if chooser == nil {
		return
	}
	tab.fileChooser = nil
	chooser.cancelled.Store(true)
	if hwnd := chooser.window.Load(); hwnd != 0 {
		// 异步取消，绝不阻塞浏览器 STA 或跨线程发送同步消息。
		browserPostMessage.Call(hwnd, 0x0111, 2, 0)
	}
	fileChooserClosed(tab.id, 0)
}

func browserChooserFrameOrigin(data []byte, frameID string) string {
	type frame struct {
		ID             string `json:"id"`
		URL            string `json:"url"`
		SecurityOrigin string `json:"securityOrigin"`
	}
	type tree struct {
		Frame    frame             `json:"frame"`
		Children []json.RawMessage `json:"childFrames"`
	}
	var response struct {
		Tree json.RawMessage `json:"frameTree"`
	}
	if json.Unmarshal(data, &response) != nil {
		return ""
	}
	var find func(json.RawMessage) string
	find = func(raw json.RawMessage) string {
		var item tree
		if json.Unmarshal(raw, &item) != nil {
			return ""
		}
		if item.Frame.ID == frameID {
			value := item.Frame.SecurityOrigin
			if value == "" {
				value = item.Frame.URL
			}
			u, err := url.Parse(value)
			if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && !strings.ContainsAny(u.Host, "\r\n\t") {
				return u.Scheme + "://" + u.Host
			}
			return "不透明来源"
		}
		for _, child := range item.Children {
			if origin := find(child); origin != "" {
				return origin
			}
		}
		return ""
	}
	return find(response.Tree)
}

var browserOpenFileName = windows.NewLazySystemDLL("comdlg32.dll").NewProc("GetOpenFileNameW")
var browserGetParent = browserUser32.NewProc("GetParent")

// 字段顺序遵循 OPENFILENAMEW；不能使用 packed 布局。
type browserOpenFilename struct {
	Size                         uint32
	Owner, Instance              uintptr
	Filter, CustomFilter         *uint16
	MaxCustomFilter, FilterIndex uint32
	File                         *uint16
	MaxFile                      uint32
	FileTitle                    *uint16
	MaxFileTitle                 uint32
	InitialDir, Title            *uint16
	Flags                        uint32
	FileOffset, FileExtension    uint16
	DefaultExtension             *uint16
	CustomData                   uintptr
	Hook                         uintptr
	Template                     *uint16
	Reserved                     unsafe.Pointer
	ReservedValue, FlagsEx       uint32
}

var browserFileChooserHook = syscall.NewCallback(func(hwnd uintptr, message uint32, _ uintptr, lparam uintptr) uintptr {
	if message == 0x0110 && lparam != 0 { // WM_INITDIALOG
		of := (*browserOpenFilename)(browserNativePointer(lparam))
		chooser := (*windowsFileChooser)(browserNativePointer(of.CustomData))
		parent, _, _ := browserGetParent.Call(hwnd)
		chooser.window.Store(parent)
		if chooser.cancelled.Load() {
			browserPostMessage.Call(parent, 0x0111, 2, 0)
		}
	}
	return 0
})

func browserPickUploadFiles(owner uintptr, origin string, chooser *windowsFileChooser) ([]string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := browserOle32.NewProc("CoInitializeEx").Call(0, 2)
	if browserHRESULT(hr) != nil {
		return nil, browserChooserError()
	}
	defer browserOle32.NewProc("CoUninitialize").Call()
	if chooser.cancelled.Load() {
		return nil, nil
	}
	files := make([]uint16, 65536)
	title, err := windows.UTF16PtrFromString("选择上传文件 — " + origin)
	if err != nil {
		return nil, browserChooserError()
	}
	of := browserOpenFilename{Size: uint32(unsafe.Sizeof(browserOpenFilename{})), Owner: owner, File: &files[0], MaxFile: uint32(len(files)), Title: title,
		Flags:      0x00080000 | 0x00000020 | 0x00001000 | 0x00000800 | 0x00000008 | 0x02000000,
		CustomData: uintptr(unsafe.Pointer(chooser)), Hook: browserFileChooserHook}
	if chooser.multiple {
		of.Flags |= 0x00000200
	}
	var pin runtime.Pinner
	pin.Pin(chooser)
	defer pin.Unpin()
	ok, _, _ := browserOpenFileName.Call(uintptr(unsafe.Pointer(&of)))
	chooser.window.Store(0)
	runtime.KeepAlive(chooser)
	runtime.KeepAlive(title)
	runtime.KeepAlive(files)
	if ok == 0 || chooser.cancelled.Load() {
		return nil, nil
	}
	return browserChooserPaths(files, chooser.multiple), nil
}

func browserChooserPaths(buffer []uint16, multiple bool) []string {
	var parts []string
	for start := 0; start < len(buffer) && buffer[start] != 0; {
		end := start
		for end < len(buffer) && buffer[end] != 0 {
			end++
		}
		if end == len(buffer) {
			return nil
		}
		parts = append(parts, windows.UTF16ToString(buffer[start:end]))
		start = end + 1
	}
	if len(parts) == 0 || (!multiple && len(parts) != 1) {
		return nil
	}
	if len(parts) == 1 {
		return parts
	}
	paths := make([]string, 0, len(parts)-1)
	for _, name := range parts[1:] {
		paths = append(paths, filepath.Join(parts[0], name))
	}
	return paths
}
