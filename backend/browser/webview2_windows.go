//go:build windows

package browser

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// COM 对象只在浏览器 STA 线程调用；异步回调的引用由 COM 和此注册表共同持有。
type browserCOM struct{ vtable *[128]uintptr }

//go:uintptrescapes
func (p *browserCOM) call(slot int, args ...uintptr) (uintptr, error) {
	if p == nil {
		return 0, errors.New("WebView2 COM object is unavailable")
	}
	values := append([]uintptr{uintptr(unsafe.Pointer(p))}, args...)
	hr, _, _ := syscall.SyscallN(p.vtable[slot], values...)
	return hr, browserHRESULT(hr)
}
func (p *browserCOM) release() {
	if p != nil {
		_, _ = p.call(2)
	}
}
func (p *browserCOM) retain() {
	if p != nil {
		_, _ = p.call(1)
	}
}

// browserHRESULTError 保留原始 HRESULT，便于识别已失效的浏览器进程。
type browserHRESULTError uint32

func (e browserHRESULTError) Error() string { return fmt.Sprintf("WebView2 HRESULT 0x%08X", uint32(e)) }

func browserHRESULT(hr uintptr) error {
	if int32(uint32(hr)) < 0 {
		return browserHRESULTError(uint32(hr))
	}
	return nil
}

// browserProcessGone 判断错误是否表示 WebView2 浏览器进程已经不可用：
// ERROR_INVALID_STATE（进程退出后 controller/environment 的调用结果）、
// RPC_E_DISCONNECTED 与 RPC_S_SERVER_UNAVAILABLE。
func browserProcessGone(err error) bool {
	var hr browserHRESULTError
	if !errors.As(err, &hr) {
		return false
	}
	switch uint32(hr) {
	case 0x8007139F, 0x80010108, 0x800706BA:
		return true
	}
	return false
}
func (p *browserCOM) query(iid string) (*browserCOM, error) {
	id, err := windows.GUIDFromString(iid)
	if err != nil {
		return nil, err
	}
	var result *browserCOM
	_, err = p.call(0, uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&result)))
	return result, err
}
func (p *browserCOM) object(slot int) (*browserCOM, error) {
	var result *browserCOM
	_, err := p.call(slot, uintptr(unsafe.Pointer(&result)))
	return result, err
}
func (p *browserCOM) text(slot int) (string, error) {
	var value *uint16
	_, err := p.call(slot, uintptr(unsafe.Pointer(&value)))
	if value != nil {
		defer windows.CoTaskMemFree(unsafe.Pointer(value))
	}
	return windows.UTF16PtrToString(value), err
}
func (p *browserCOM) stringArg(slot int, value string, rest ...uintptr) error {
	text, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return err
	}
	args := append([]uintptr{uintptr(unsafe.Pointer(text))}, rest...)
	_, err = p.call(slot, args...)
	runtime.KeepAlive(text)
	return err
}

type browserCallback struct {
	vtable  *[4]uintptr
	refs    atomic.Int32
	iid     windows.GUID
	invoke  func(uintptr, unsafe.Pointer) uintptr
	cleanup func()
	pin     runtime.Pinner
}

var browserCallbacks = struct {
	sync.Mutex
	live map[*browserCallback]struct{}
}{live: make(map[*browserCallback]struct{})}
var browserIUnknown = windows.GUID{Data1: 0, Data2: 0, Data3: 0, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
var browserCallbackVTable = [4]uintptr{
	syscall.NewCallback(func(this *browserCallback, iid *windows.GUID, out *unsafe.Pointer) uintptr {
		cb := this
		if out == nil {
			return 0x80004003
		}
		*out = nil
		if iid != nil && (*iid == cb.iid || *iid == browserIUnknown) {
			*out = unsafe.Pointer(this)
			cb.refs.Add(1)
			return 0
		}
		return 0x80004002
	}),
	syscall.NewCallback(func(this *browserCallback) uintptr { return uintptr(this.refs.Add(1)) }),
	syscall.NewCallback(func(this *browserCallback) uintptr { return this.release() }),
	syscall.NewCallback(func(this *browserCallback, a uintptr, b unsafe.Pointer) uintptr { return this.invoke(a, b) }),
}
var browserCallbackShortVTable = func() [4]uintptr {
	table := browserCallbackVTable
	table[3] = syscall.NewCallback(func(this *browserCallback, a uintptr) uintptr { return this.invoke(a, nil) })
	return table
}()

func newBrowserCallback(iid string, short bool, invoke func(uintptr, unsafe.Pointer) uintptr) *browserCallback {
	cb := &browserCallback{vtable: &browserCallbackVTable, iid: windows.GUID{}, invoke: invoke}
	cb.iid, _ = windows.GUIDFromString(iid)
	if short {
		cb.vtable = &browserCallbackShortVTable
	}
	cb.refs.Store(1)
	cb.pin.Pin(cb)
	browserCallbacks.Lock()
	browserCallbacks.live[cb] = struct{}{}
	browserCallbacks.Unlock()
	return cb
}
func (cb *browserCallback) pointer() uintptr { return uintptr(unsafe.Pointer(cb)) }
func (cb *browserCallback) release() uintptr {
	count := cb.refs.Add(-1)
	if count == 0 {
		if cb.cleanup != nil {
			cb.cleanup()
		}
		browserCallbacks.Lock()
		delete(browserCallbacks.live, cb)
		browserCallbacks.Unlock()
		cb.pin.Unpin()
	}
	return uintptr(count)
}

var browserUser32 = windows.NewLazySystemDLL("user32.dll")
var browserOle32 = windows.NewLazySystemDLL("ole32.dll")
var browserCreateWindow = browserUser32.NewProc("CreateWindowExW")
var browserDestroyWindow = browserUser32.NewProc("DestroyWindow")
var browserSetWindowPos = browserUser32.NewProc("SetWindowPos")
var browserShowWindow = browserUser32.NewProc("ShowWindow")
var browserGetMessage = browserUser32.NewProc("GetMessageW")
var browserPeekMessage = browserUser32.NewProc("PeekMessageW")
var browserDispatchMessage = browserUser32.NewProc("DispatchMessageW")
var browserTranslateMessage = browserUser32.NewProc("TranslateMessage")
var browserPostThreadMessage = browserUser32.NewProc("PostThreadMessageW")
var browserSetTimer = browserUser32.NewProc("SetTimer")
var browserPostMessage = browserUser32.NewProc("PostMessageW")
var browserRegisterClass = browserUser32.NewProc("RegisterClassExW")
var browserDefWindowProc = browserUser32.NewProc("DefWindowProcW")
var browserIsWindow = browserUser32.NewProc("IsWindow")
var browserIsChild = browserUser32.NewProc("IsChild")

func browserChildWindow(parent, child uintptr) bool {
	ok, _, _ := browserIsChild.Call(parent, child)
	return ok != 0
}

var browserCallNextHook = browserUser32.NewProc("CallNextHookEx")
var browserInputHook = syscall.NewCallback(func(code int32, removed, message uintptr) uintptr {
	if code >= 0 && removed != 0 && message != 0 {
		windowsBrowser.noteNativeInput((*browserMessage)(browserNativePointer(message)))
	}
	result, _, _ := browserCallNextHook.Call(0, uintptr(code), removed, message)
	return result
})

// Thread-scoped WH_GETMESSAGE also observes input consumed by WebView2's nested
// pumps. No global hook, page message bridge, or synthetic JS is trusted as input.
func browserInstallInputHooks() (func(), error) {
	hook, _, err := browserUser32.NewProc("SetWindowsHookExW").Call(3, browserInputHook, 0, uintptr(windows.GetCurrentThreadId()))
	if hook == 0 {
		return nil, fmt.Errorf("install browser thread input hook: %w", err)
	}
	return func() { browserUser32.NewProc("UnhookWindowsHookEx").Call(hook) }, nil
}

var browserGetWindowProcess = browserUser32.NewProc("GetWindowThreadProcessId")
var browserGetDPI = browserUser32.NewProc("GetDpiForWindow")
var browserSetThreadDPIContext = browserUser32.NewProc("SetThreadDpiAwarenessContext")

// The desktop host sets awareness on its UI thread, not every Go worker. All screen/child
// coordinates must use physical pixels on the STA and the capture worker too.
// The caller must pin its OS thread until the returned restore function runs.
func browserPhysicalDPIContext() func() {
	if browserSetThreadDPIContext.Find() != nil { return func() {} }
	old, _, _ := browserSetThreadDPIContext.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
	return func() { if old != 0 { browserSetThreadDPIContext.Call(old) } }
}
var browserClientToScreen = browserUser32.NewProc("ClientToScreen")
var browserCreateStream = browserOle32.NewProc("CreateStreamOnHGlobal")
var browserGetStreamMemory = browserOle32.NewProc("GetHGlobalFromStream")
var browserKernel32 = windows.NewLazySystemDLL("kernel32.dll")
var browserGlobalLock = browserKernel32.NewProc("GlobalLock")
var browserGlobalUnlock = browserKernel32.NewProc("GlobalUnlock")
var browserGlobalSize = browserKernel32.NewProc("GlobalSize")

type browserWindowClass struct {
	Size, Style                        uint32
	Procedure                          uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}

var browserDispatcherProcedure = syscall.NewCallback(func(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	if message == browserWorkMessage {
		// Wake the outer STA loop only. WebView2 can dispatch this window from
		// a nested COM pump; executing work here would reenter an active COM
		// callback and can invalidate controllers or strand async callbacks.
		return 0
	}
	value, _, _ := browserDefWindowProc.Call(hwnd, uintptr(message), wparam, lparam)
	return value
})

func browserCreateDispatcher() (uintptr, error) {
	name, _ := windows.UTF16PtrFromString("MinCodeEmbeddedBrowserDispatcher")
	class := browserWindowClass{Size: uint32(unsafe.Sizeof(browserWindowClass{})), Procedure: browserDispatcherProcedure, Name: name}
	atom, _, err := browserRegisterClass.Call(uintptr(unsafe.Pointer(&class)))
	if atom == 0 {
		return 0, fmt.Errorf("register browser dispatch window: %w", err)
	}
	// A message-only window survives COM nested pumps, unlike thread messages.
	hwnd, _, err := browserCreateWindow.Call(0, uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	runtime.KeepAlive(name)
	if hwnd == 0 {
		return 0, fmt.Errorf("create browser dispatch window: %w", err)
	}
	return hwnd, nil
}

type browserPoint struct{ X, Y int32 }
type browserRect struct{ Left, Top, Right, Bottom int32 }
type browserMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          browserPoint
	Private        uint32
}

func browserValidHost(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	valid, _, _ := browserIsWindow.Call(hwnd)
	var pid uint32
	browserGetWindowProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return valid != 0 && pid == windows.GetCurrentProcessId()
}
func browserOnHostUIThread(hwnd uintptr) bool {
	thread, _, _ := browserGetWindowProcess.Call(hwnd, 0)
	return thread != 0 && uint32(thread) == windows.GetCurrentThreadId()
}
func browserDPIScale(hwnd uintptr) float64 {
	if browserGetDPI.Find() == nil {
		dpi, _, _ := browserGetDPI.Call(hwnd)
		if dpi > 0 {
			return float64(dpi) / 96
		}
	}
	return 1
}
func browserPutBounds(controller *browserCOM, rect browserRect) error {
	// Win64 x86 按指针传递大结构，ARM64 按两个寄存器，x86 按四个栈参数。
	var err error
	switch runtime.GOARCH {
	case "arm64":
		words := *(*[2]uintptr)(unsafe.Pointer(&rect))
		_, err = controller.call(6, words[0], words[1])
	case "386":
		_, err = controller.call(6, uintptr(rect.Left), uintptr(rect.Top), uintptr(rect.Right), uintptr(rect.Bottom))
	default:
		_, err = controller.call(6, uintptr(unsafe.Pointer(&rect)))
	}
	return err
}
func browserPutZoom(controller *browserCOM, zoom float64) error {
	// syscall.SyscallN 将整数寄存器同步到浮点寄存器，符合 Win64 COM ABI。
	bits := *(*uint64)(unsafe.Pointer(&zoom))
	if runtime.GOARCH == "386" {
		_, err := controller.call(8, uintptr(uint32(bits)), uintptr(uint32(bits>>32)))
		return err
	}
	_, err := controller.call(8, uintptr(bits))
	return err
}
func browserMemoryStream() (*browserCOM, error) {
	var stream *browserCOM
	hr, _, _ := browserCreateStream.Call(0, 1, uintptr(unsafe.Pointer(&stream)))
	return stream, browserHRESULT(hr)
}

// browserNativePointer 仅用于 Win32 返回的非 Go 内存地址，不转换 Go 指针。
func browserNativePointer(address uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&address))
}
func browserStreamBytes(stream *browserCOM) ([]byte, error) {
	var handle uintptr
	hr, _, _ := browserGetStreamMemory.Call(uintptr(unsafe.Pointer(stream)), uintptr(unsafe.Pointer(&handle)))
	if err := browserHRESULT(hr); err != nil {
		return nil, err
	}
	var length uint64
	if runtime.GOARCH == "386" {
		_, err := stream.call(5, 0, 0, 2, uintptr(unsafe.Pointer(&length)))
		if err != nil {
			return nil, err
		}
	} else {
		_, err := stream.call(5, 0, 2, uintptr(unsafe.Pointer(&length)))
		if err != nil {
			return nil, err
		}
	}
	size, _, _ := browserGlobalSize.Call(handle)
	if length == 0 || length > 64<<20 || length > uint64(size) {
		return nil, errors.New("browser snapshot is empty or exceeds 64 MiB")
	}
	ptr, _, err := browserGlobalLock.Call(handle)
	if ptr == 0 {
		return nil, err
	}
	defer browserGlobalUnlock.Call(handle)
	return append([]byte(nil), unsafe.Slice((*byte)(browserNativePointer(ptr)), int(length))...), nil
}
