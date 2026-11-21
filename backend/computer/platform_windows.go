//go:build windows

package computer

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	smCXScreen = 0
	smCYScreen = 1

	srccopy      = 0x00CC0020
	dibRGBColors = 0
	biRGB        = 0

	inputMouse    = 0
	inputKeyboard = 1

	mouseeventfLeftDown   = 0x0002
	mouseeventfLeftUp     = 0x0004
	mouseeventfRightDown  = 0x0008
	mouseeventfRightUp    = 0x0010
	mouseeventfMiddleDown = 0x0020
	mouseeventfMiddleUp   = 0x0040
	mouseeventfWheel      = 0x0800
	mouseeventfHWheel     = 0x1000

	keyeventfExtendedKey = 0x0001
	keyeventfKeyUp       = 0x0002
	keyeventfUnicode     = 0x0004

	swShowNormal = 1

	monitorInfoPrimary = 1
)

var (
	user32                     = windows.NewLazySystemDLL("user32.dll")
	gdi32                      = windows.NewLazySystemDLL("gdi32.dll")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procSetCursorPos           = user32.NewProc("SetCursorPos")
	procSendInput              = user32.NewProc("SendInput")
	procVkKeyScanW             = user32.NewProc("VkKeyScanW")
	procEnumDisplayMonitors    = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW        = user32.NewProc("GetMonitorInfoW")
	procGetCursorPos           = user32.NewProc("GetCursorPos")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type windowsRect struct {
	Left, Top, Right, Bottom int32
}

type monitorInfoEx struct {
	Size    uint32
	Monitor windowsRect
	Work    windowsRect
	Flags   uint32
	Device  [32]uint16
}

type keyboardEvent struct {
	virtualKey uint16
	scanCode   uint16
	flags      uint32
}

type mouseEvent struct {
	data  uint32
	flags uint32
}

func platformStatus() Status {
	displays := windowsDisplays()
	display := DisplayInfo{}
	for _, candidate := range displays {
		if candidate.Primary {
			display = candidate
			break
		}
	}
	if display.PixelWidth <= 0 {
		display = windowsDisplayInfo()
		displays = nil
		if display.PixelWidth > 0 {
			displays = []DisplayInfo{display}
		}
	}
	return Status{
		Supported:               display.PixelWidth > 0 && display.PixelHeight > 0,
		Platform:                "windows",
		AccessibilityAuthorized: true,
		ScreenCaptureAuthorized: true,
		Display:                 display,
		Displays:                displays,
	}
}

var (
	enumDisplayMonitorsMu       sync.Mutex
	enumDisplayMonitorsResult   []uintptr
	enumDisplayMonitorsCallback = windows.NewCallback(func(monitor, _, _, _ uintptr) uintptr {
		enumDisplayMonitorsResult = append(enumDisplayMonitorsResult, monitor)
		return 1
	})
)

// windowsDisplays enumerates monitors in virtual-desktop pixels. The process is
// expected to be DPI aware, so point and pixel sizes are identical.
func windowsDisplays() []DisplayInfo {
	enumDisplayMonitorsMu.Lock()
	enumDisplayMonitorsResult = enumDisplayMonitorsResult[:0]
	ok, _, _ := procEnumDisplayMonitors.Call(0, 0, enumDisplayMonitorsCallback, 0)
	monitors := append([]uintptr(nil), enumDisplayMonitorsResult...)
	enumDisplayMonitorsMu.Unlock()
	if ok == 0 {
		return nil
	}
	displays := make([]DisplayInfo, 0, len(monitors))
	used := map[uint32]bool{}
	for index, monitor := range monitors {
		info := monitorInfoEx{}
		info.Size = uint32(unsafe.Sizeof(info))
		if result, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info))); result == 0 {
			continue
		}
		width := int(info.Monitor.Right - info.Monitor.Left)
		height := int(info.Monitor.Bottom - info.Monitor.Top)
		if width <= 0 || height <= 0 {
			continue
		}
		id := windowsDisplayID(windows.UTF16ToString(info.Device[:]), index)
		for used[id] {
			id += 100
		}
		used[id] = true
		displays = append(displays, DisplayInfo{
			ID:          id,
			Primary:     info.Flags&monitorInfoPrimary != 0,
			OriginX:     float64(info.Monitor.Left),
			OriginY:     float64(info.Monitor.Top),
			PointWidth:  float64(width),
			PointHeight: float64(height),
			PixelWidth:  width,
			PixelHeight: height,
			ScaleX:      1,
			ScaleY:      1,
		})
	}
	return displays
}

func windowsDisplayID(device string, index int) uint32 {
	digits := strings.TrimLeftFunc(device, func(r rune) bool { return r < '0' || r > '9' })
	if value, err := strconv.ParseUint(digits, 10, 32); err == nil && value > 0 {
		return uint32(value)
	}
	return uint32(index + 1)
}

func platformRequestPermissions(_, _ bool) Status { return platformStatus() }

func windowsDisplayInfo() DisplayInfo {
	width, _, _ := procGetSystemMetrics.Call(smCXScreen)
	height, _, _ := procGetSystemMetrics.Call(smCYScreen)
	if int32(width) <= 0 || int32(height) <= 0 {
		return DisplayInfo{}
	}
	return DisplayInfo{
		ID:          1,
		Primary:     true,
		OriginX:     0,
		OriginY:     0,
		PointWidth:  float64(int32(width)),
		PointHeight: float64(int32(height)),
		PixelWidth:  int(int32(width)),
		PixelHeight: int(int32(height)),
		ScaleX:      1,
		ScaleY:      1,
	}
}

func platformCaptureScreen(displayID uint32, _ time.Duration) (Capture, error) {
	display, ok := findDisplay(platformStatus(), displayID)
	if !ok {
		return Capture{}, fmt.Errorf("display %d is not connected", displayID)
	}
	if display.PixelWidth <= 0 || display.PixelHeight <= 0 {
		return Capture{}, errors.New("Windows display is unavailable")
	}
	screenDC, _, callErr := procGetDC.Call(0)
	if screenDC == 0 {
		return Capture{}, fmt.Errorf("GetDC failed: %w", callErr)
	}
	defer procReleaseDC.Call(0, screenDC)

	memoryDC, _, callErr := procCreateCompatibleDC.Call(screenDC)
	if memoryDC == 0 {
		return Capture{}, fmt.Errorf("CreateCompatibleDC failed: %w", callErr)
	}
	defer procDeleteDC.Call(memoryDC)

	bitmap, _, callErr := procCreateCompatibleBitmap.Call(screenDC, uintptr(display.PixelWidth), uintptr(display.PixelHeight))
	if bitmap == 0 {
		return Capture{}, fmt.Errorf("CreateCompatibleBitmap failed: %w", callErr)
	}
	defer procDeleteObject.Call(bitmap)

	previous, _, callErr := procSelectObject.Call(memoryDC, bitmap)
	if previous == 0 {
		return Capture{}, fmt.Errorf("SelectObject failed: %w", callErr)
	}
	defer procSelectObject.Call(memoryDC, previous)

	copied, _, callErr := procBitBlt.Call(
		memoryDC,
		0,
		0,
		uintptr(display.PixelWidth),
		uintptr(display.PixelHeight),
		screenDC,
		uintptr(int32(display.OriginX)),
		uintptr(int32(display.OriginY)),
		srccopy,
	)
	if copied == 0 {
		return Capture{}, fmt.Errorf("BitBlt failed: %w", callErr)
	}

	pixels := make([]byte, display.PixelWidth*display.PixelHeight*4)
	info := bitmapInfo{Header: bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(display.PixelWidth),
		Height:      -int32(display.PixelHeight),
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
		SizeImage:   uint32(len(pixels)),
	}}
	scanLines, _, callErr := procGetDIBits.Call(
		memoryDC,
		bitmap,
		0,
		uintptr(display.PixelHeight),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&info)),
		dibRGBColors,
	)
	if scanLines == 0 {
		return Capture{}, fmt.Errorf("GetDIBits failed: %w", callErr)
	}

	for offset := 0; offset < len(pixels); offset += 4 {
		pixels[offset], pixels[offset+2] = pixels[offset+2], pixels[offset]
		pixels[offset+3] = 0xff
	}
	imageValue := &image.RGBA{
		Pix:    pixels,
		Stride: display.PixelWidth * 4,
		Rect:   image.Rect(0, 0, display.PixelWidth, display.PixelHeight),
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, imageValue); err != nil {
		return Capture{}, fmt.Errorf("encode Windows screen capture: %w", err)
	}
	return Capture{
		MimeType: "image/png",
		Data:     base64.StdEncoding.EncodeToString(encoded.Bytes()),
		Width:    display.PixelWidth,
		Height:   display.PixelHeight,
		Display:  display,
	}, nil
}

func platformMovePointer(x, y float64) error {
	result, _, callErr := procSetCursorPos.Call(uintptr(int32(math.Round(x))), uintptr(int32(math.Round(y))))
	if result == 0 {
		return fmt.Errorf("SetCursorPos failed: %w", callErr)
	}
	return nil
}

func platformClick(x, y float64, button string, count int) error {
	if err := platformMovePointer(x, y); err != nil {
		return err
	}
	down, up := windowsMouseButtonFlags(button)
	for index := 0; index < count; index++ {
		if err := sendMouseEvents(mouseEvent{flags: down}, mouseEvent{flags: up}); err != nil {
			return err
		}
	}
	return nil
}

func platformMouseDown(x, y float64, button string) error {
	if err := platformMovePointer(x, y); err != nil {
		return err
	}
	down, _ := windowsMouseButtonFlags(button)
	return sendMouseEvents(mouseEvent{flags: down})
}

func platformMouseDrag(x, y float64, _ string) error { return platformMovePointer(x, y) }

func platformMouseUp(x, y float64, button string) error {
	if err := platformMovePointer(x, y); err != nil {
		return err
	}
	_, up := windowsMouseButtonFlags(button)
	return sendMouseEvents(mouseEvent{flags: up})
}

func platformScroll(deltaX, deltaY float64) error {
	events := make([]mouseEvent, 0, 2)
	if deltaY != 0 {
		events = append(events, mouseEvent{data: uint32(int32(math.Round(-deltaY))), flags: mouseeventfWheel})
	}
	if deltaX != 0 {
		events = append(events, mouseEvent{data: uint32(int32(math.Round(deltaX))), flags: mouseeventfHWheel})
	}
	return sendMouseEvents(events...)
}

func platformTypeText(text string) error {
	units := utf16.Encode([]rune(text))
	events := make([]keyboardEvent, 0, len(units)*2)
	for _, unit := range units {
		events = append(events,
			keyboardEvent{scanCode: unit, flags: keyeventfUnicode},
			keyboardEvent{scanCode: unit, flags: keyeventfUnicode | keyeventfKeyUp},
		)
	}
	return sendKeyboardEvents(events...)
}

func platformPressKey(key string, modifiers uint32) error {
	virtualKey, extraModifiers, extended, ok := windowsVirtualKey(key)
	if !ok {
		return fmt.Errorf("unsupported key %q", key)
	}
	modifiers |= extraModifiers
	events := make([]keyboardEvent, 0, 10)
	for _, value := range windowsModifierKeys(modifiers) {
		events = append(events, keyboardEvent{virtualKey: value})
	}
	flags := uint32(0)
	if extended {
		flags = keyeventfExtendedKey
	}
	events = append(events,
		keyboardEvent{virtualKey: virtualKey, flags: flags},
		keyboardEvent{virtualKey: virtualKey, flags: flags | keyeventfKeyUp},
	)
	modifierKeys := windowsModifierKeys(modifiers)
	for index := len(modifierKeys) - 1; index >= 0; index-- {
		events = append(events, keyboardEvent{virtualKey: modifierKeys[index], flags: keyeventfKeyUp})
	}
	return sendKeyboardEvents(events...)
}

func platformKeyEvent(key string, down bool) error {
	virtualKey, extraModifiers, extended, ok := windowsVirtualKey(key)
	if !ok {
		return fmt.Errorf("unsupported key %q", key)
	}
	flags := uint32(0)
	if extended {
		flags = keyeventfExtendedKey
	}
	if !down {
		return sendKeyboardEvents(keyboardEvent{virtualKey: virtualKey, flags: flags | keyeventfKeyUp})
	}
	// Layout modifiers (for example Shift for "+") only wrap the key-down.
	implicit := windowsModifierKeys(extraModifiers)
	events := make([]keyboardEvent, 0, len(implicit)*2+1)
	for _, value := range implicit {
		events = append(events, keyboardEvent{virtualKey: value})
	}
	events = append(events, keyboardEvent{virtualKey: virtualKey, flags: flags})
	for index := len(implicit) - 1; index >= 0; index-- {
		events = append(events, keyboardEvent{virtualKey: implicit[index], flags: keyeventfKeyUp})
	}
	return sendKeyboardEvents(events...)
}

func platformCursorPosition() (Point, error) {
	var point struct{ X, Y int32 }
	if result, _, callErr := procGetCursorPos.Call(uintptr(unsafe.Pointer(&point))); result == 0 {
		return Point{}, fmt.Errorf("GetCursorPos failed: %w", callErr)
	}
	return Point{X: float64(point.X), Y: float64(point.Y)}, nil
}

func platformOpenApp(application string) error {
	candidates := windowsApplicationCandidates(application)
	for _, candidate := range candidates {
		if path, err := resolveWindowsExecutable(candidate); err == nil {
			if err := exec.Command(path).Start(); err != nil {
				return fmt.Errorf("start %s: %w", application, err)
			}
			return nil
		}
	}
	return shellExecute(application, "")
}

func platformOpenURL(targetURL, browser string) error {
	if browser == "default" {
		return shellExecute(targetURL, "")
	}
	if browser == "safari" {
		return errors.New("Safari is not supported on Windows")
	}
	executable := map[string]string{
		"chrome":  "chrome.exe",
		"edge":    "msedge.exe",
		"firefox": "firefox.exe",
	}[browser]
	if executable == "" {
		return fmt.Errorf("unsupported Windows browser %q", browser)
	}
	path, err := resolveWindowsExecutable(executable)
	if err != nil {
		return fmt.Errorf("locate %s: %w", browser, err)
	}
	if err := exec.Command(path, targetURL).Start(); err != nil {
		return fmt.Errorf("start %s: %w", browser, err)
	}
	return nil
}

func windowsMouseButtonFlags(button string) (uint32, uint32) {
	switch button {
	case "right":
		return mouseeventfRightDown, mouseeventfRightUp
	case "wheel":
		return mouseeventfMiddleDown, mouseeventfMiddleUp
	default:
		return mouseeventfLeftDown, mouseeventfLeftUp
	}
}

func sendMouseEvents(events ...mouseEvent) error {
	if len(events) == 0 {
		return nil
	}
	inputSize, unionOffset := windowsInputLayout()
	buffer := make([]byte, inputSize*len(events))
	for index, event := range events {
		offset := index * inputSize
		binary.LittleEndian.PutUint32(buffer[offset:], inputMouse)
		binary.LittleEndian.PutUint32(buffer[offset+unionOffset+8:], event.data)
		binary.LittleEndian.PutUint32(buffer[offset+unionOffset+12:], event.flags)
	}
	return callSendInput(buffer, inputSize, len(events))
}

func sendKeyboardEvents(events ...keyboardEvent) error {
	if len(events) == 0 {
		return nil
	}
	inputSize, unionOffset := windowsInputLayout()
	buffer := make([]byte, inputSize*len(events))
	for index, event := range events {
		offset := index * inputSize
		binary.LittleEndian.PutUint32(buffer[offset:], inputKeyboard)
		binary.LittleEndian.PutUint16(buffer[offset+unionOffset:], event.virtualKey)
		binary.LittleEndian.PutUint16(buffer[offset+unionOffset+2:], event.scanCode)
		binary.LittleEndian.PutUint32(buffer[offset+unionOffset+4:], event.flags)
	}
	return callSendInput(buffer, inputSize, len(events))
}

func windowsInputLayout() (inputSize, unionOffset int) {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return 40, 8
	}
	return 28, 4
}

func callSendInput(buffer []byte, inputSize, count int) error {
	sent, _, callErr := procSendInput.Call(
		uintptr(count),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(inputSize),
	)
	if int(sent) != count {
		return fmt.Errorf("SendInput sent %d of %d events: %w", sent, count, callErr)
	}
	return nil
}

func windowsVirtualKey(key string) (virtualKey uint16, modifiers uint32, extended bool, ok bool) {
	normalized := strings.ToUpper(strings.TrimSpace(key))
	if len(normalized) == 1 {
		character := rune(normalized[0])
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			return uint16(character), 0, false, true
		}
	}
	if value, exists := (map[string]struct {
		key      uint16
		extended bool
	}{
		"ENTER": {0x0D, false}, "RETURN": {0x0D, false}, "TAB": {0x09, false},
		"ESC": {0x1B, false}, "ESCAPE": {0x1B, false}, "SPACE": {0x20, false},
		"BACKSPACE": {0x08, false}, "DELETE": {0x2E, true}, "INSERT": {0x2D, true},
		"LEFT": {0x25, true}, "ARROWLEFT": {0x25, true}, "UP": {0x26, true}, "ARROWUP": {0x26, true},
		"RIGHT": {0x27, true}, "ARROWRIGHT": {0x27, true}, "DOWN": {0x28, true}, "ARROWDOWN": {0x28, true},
		"HOME": {0x24, true}, "END": {0x23, true}, "PAGEUP": {0x21, true}, "PAGEDOWN": {0x22, true},
		"CAPSLOCK": {0x14, false},
		"SHIFT":    {0x10, false}, "CTRL": {0x11, false}, "CONTROL": {0x11, false},
		"ALT": {0x12, false}, "OPTION": {0x12, false},
		"META": {0x5B, true}, "CMD": {0x5B, true}, "COMMAND": {0x5B, true},
		"WIN": {0x5B, true}, "WINDOWS": {0x5B, true}, "SUPER": {0x5B, true},
		"RSHIFT": {0xA1, false}, "RCTRL": {0xA3, true}, "RALT": {0xA5, true}, "ROPTION": {0xA5, true},
		"RWIN": {0x5C, true}, "RCMD": {0x5C, true}, "RMETA": {0x5C, true},
	})[normalized]; exists {
		return value.key, 0, value.extended, true
	}
	if strings.HasPrefix(normalized, "NUMPAD") {
		if number, err := strconv.Atoi(strings.TrimPrefix(normalized, "NUMPAD")); err == nil && number >= 0 && number <= 9 {
			return uint16(0x60 + number), 0, false, true
		}
	}
	if normalized == "PLUS" {
		key = "+"
	}
	if strings.HasPrefix(normalized, "F") {
		var number int
		if _, err := fmt.Sscanf(normalized, "F%d", &number); err == nil && number >= 1 && number <= 24 {
			return uint16(0x70 + number - 1), 0, false, true
		}
	}
	runes := []rune(strings.TrimSpace(key))
	if len(runes) == 1 && runes[0] <= 0xffff {
		result, _, _ := procVkKeyScanW.Call(uintptr(runes[0]))
		value := int16(result)
		if value != -1 {
			state := uint16(value)
			if state&0x0100 != 0 {
				modifiers |= 2
			}
			if state&0x0200 != 0 {
				modifiers |= 1
			}
			if state&0x0400 != 0 {
				modifiers |= 4
			}
			return state & 0xff, modifiers, false, true
		}
	}
	return 0, 0, false, false
}

func windowsModifierKeys(modifiers uint32) []uint16 {
	keys := make([]uint16, 0, 4)
	if modifiers&1 != 0 {
		keys = append(keys, 0x11)
	}
	if modifiers&2 != 0 {
		keys = append(keys, 0x10)
	}
	if modifiers&4 != 0 {
		keys = append(keys, 0x12)
	}
	if modifiers&8 != 0 {
		keys = append(keys, 0x5B)
	}
	return keys
}

func windowsApplicationCandidates(application string) []string {
	trimmed := strings.TrimSpace(application)
	alias := map[string]string{
		"chrome": "chrome.exe", "google chrome": "chrome.exe",
		"edge": "msedge.exe", "microsoft edge": "msedge.exe",
		"firefox": "firefox.exe", "mozilla firefox": "firefox.exe",
		"notepad": "notepad.exe", "记事本": "notepad.exe",
		"explorer": "explorer.exe", "file explorer": "explorer.exe", "资源管理器": "explorer.exe",
		"terminal": "wt.exe", "windows terminal": "wt.exe",
		"powershell": "powershell.exe", "command prompt": "cmd.exe", "cmd": "cmd.exe",
		"visual studio code": "code.exe", "vscode": "code.exe", "code": "code.exe",
	}
	if executable := alias[strings.ToLower(trimmed)]; executable != "" {
		return []string{executable}
	}
	if strings.HasSuffix(strings.ToLower(trimmed), ".exe") {
		return []string{trimmed}
	}
	return []string{trimmed, trimmed + ".exe"}
}

func resolveWindowsExecutable(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		for _, prefix := range []string{
			`SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`,
			`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\App Paths\`,
		} {
			key, err := registry.OpenKey(root, prefix+filepath.Base(name), registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			value, _, valueErr := key.GetStringValue("")
			key.Close()
			if valueErr == nil && strings.TrimSpace(value) != "" {
				return value, nil
			}
		}
	}
	return "", fmt.Errorf("executable %q was not found in PATH or App Paths", name)
}

func shellExecute(target, parameters string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return fmt.Errorf("encode ShellExecute target: %w", err)
	}
	var params *uint16
	if parameters != "" {
		params, err = windows.UTF16PtrFromString(parameters)
		if err != nil {
			return fmt.Errorf("encode ShellExecute parameters: %w", err)
		}
	}
	if err := windows.ShellExecute(0, verb, file, params, nil, swShowNormal); err != nil {
		return fmt.Errorf("ShellExecute %q: %w", target, err)
	}
	return nil
}
