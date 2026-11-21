//go:build windows

package browser

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsCaptureMaxDimension = 16384
	windowsCaptureMaxPixels    = 32 * 1024 * 1024
	windowsCaptureSRCCopy      = 0x00CC0020
	windowsCaptureLayered      = 0x40000000

	windowsCaptureSMXVirtualScreen  = 76
	windowsCaptureSMYVirtualScreen  = 77
	windowsCaptureSMCXVirtualScreen = 78
	windowsCaptureSMCYVirtualScreen = 79
	windowsCaptureSMCMonitors       = 80
)

var (
	captureWindowsUser32                 = windows.NewLazySystemDLL("user32.dll")
	captureWindowsGDI32                  = windows.NewLazySystemDLL("gdi32.dll")
	captureWindowsGetSystemMetrics       = captureWindowsUser32.NewProc("GetSystemMetrics")
	captureWindowsOpenInputDesktop       = captureWindowsUser32.NewProc("OpenInputDesktop")
	captureWindowsCloseDesktop           = captureWindowsUser32.NewProc("CloseDesktop")
	captureWindowsGetThreadDesktop       = captureWindowsUser32.NewProc("GetThreadDesktop")
	captureWindowsGetUserObjectInfo      = captureWindowsUser32.NewProc("GetUserObjectInformationW")
	captureWindowsGetDC                  = captureWindowsUser32.NewProc("GetDC")
	captureWindowsReleaseDC              = captureWindowsUser32.NewProc("ReleaseDC")
	captureWindowsCreateCompatibleDC     = captureWindowsGDI32.NewProc("CreateCompatibleDC")
	captureWindowsDeleteDC               = captureWindowsGDI32.NewProc("DeleteDC")
	captureWindowsCreateCompatibleBitmap = captureWindowsGDI32.NewProc("CreateCompatibleBitmap")
	captureWindowsSelectObject           = captureWindowsGDI32.NewProc("SelectObject")
	captureWindowsDeleteObject           = captureWindowsGDI32.NewProc("DeleteObject")
	captureWindowsBitBlt                 = captureWindowsGDI32.NewProc("BitBlt")
	captureWindowsGetDIBits              = captureWindowsGDI32.NewProc("GetDIBits")
)

type windowsCaptureBitmapInfoHeader struct {
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

type windowsCaptureBitmapInfo struct {
	Header windowsCaptureBitmapInfoHeader
	Colors [1]uint32
}

func validateWindowsCaptureRegion(x, y, w, h int) error {
	if w < 1 || h < 1 {
		return errors.New("截图区域无效")
	}
	if w > windowsCaptureMaxDimension || h > windowsCaptureMaxDimension || int64(w)*int64(h) > windowsCaptureMaxPixels {
		return errors.New("截图区域超过大小上限")
	}
	if int64(x) < math.MinInt32 || int64(x) > math.MaxInt32 || int64(y) < math.MinInt32 || int64(y) > math.MaxInt32 ||
		int64(x)+int64(w) > math.MaxInt32 || int64(y)+int64(h) > math.MaxInt32 {
		return errors.New("截图区域坐标超出 Windows 范围")
	}
	return nil
}

func validateWindowsCaptureDesktop(x, y, w, h, left, top, width, height int) error {
	if width <= 0 || height <= 0 {
		return errors.New("截图：Windows 显示器不可用")
	}
	if int64(x) < int64(left) || int64(y) < int64(top) ||
		int64(x)+int64(w) > int64(left)+int64(width) || int64(y)+int64(h) > int64(top)+int64(height) {
		return errors.New("截图区域超出 Windows 虚拟桌面")
	}
	return nil
}

func captureWindowsScreenRegionPNG(x, y, w, h int) ([]byte, error) {
	if err := validateWindowsCaptureRegion(x, y, w, h); err != nil {
		return nil, err
	}

	// GetDC 与 ReleaseDC 必须在同一个线程执行。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	restoreDPI := browserPhysicalDPIContext()
	defer restoreDPI()
	if err := windowsCaptureCheckInputDesktop(); err != nil {
		return nil, err
	}
	metric := func(index uintptr) int {
		value, _, _ := captureWindowsGetSystemMetrics.Call(index)
		return int(int32(value))
	}
	if metric(windowsCaptureSMCMonitors) <= 0 {
		return nil, errors.New("截图：Windows 显示器不可用")
	}
	if err := validateWindowsCaptureDesktop(x, y, w, h,
		metric(windowsCaptureSMXVirtualScreen), metric(windowsCaptureSMYVirtualScreen),
		metric(windowsCaptureSMCXVirtualScreen), metric(windowsCaptureSMCYVirtualScreen)); err != nil {
		return nil, err
	}

	screenDC, _, callErr := captureWindowsGetDC.Call(0)
	if screenDC == 0 {
		return nil, windowsCaptureCallError("GetDC", callErr)
	}
	defer captureWindowsReleaseDC.Call(0, screenDC)

	memoryDC, _, callErr := captureWindowsCreateCompatibleDC.Call(screenDC)
	if memoryDC == 0 {
		return nil, windowsCaptureCallError("CreateCompatibleDC", callErr)
	}
	var bitmap, previous uintptr
	selected := false
	defer func() {
		if selected {
			captureWindowsSelectObject.Call(memoryDC, previous)
		}
		// 即使恢复选择失败，也先销毁 DC，避免删除仍被选中的位图。
		captureWindowsDeleteDC.Call(memoryDC)
		if bitmap != 0 {
			captureWindowsDeleteObject.Call(bitmap)
		}
	}()

	bitmap, _, callErr = captureWindowsCreateCompatibleBitmap.Call(screenDC, uintptr(w), uintptr(h))
	if bitmap == 0 {
		return nil, windowsCaptureCallError("CreateCompatibleBitmap", callErr)
	}
	previous, _, callErr = captureWindowsSelectObject.Call(memoryDC, bitmap)
	if previous == 0 || previous == ^uintptr(0) {
		return nil, windowsCaptureCallError("SelectObject", callErr)
	}
	selected = true
	copied, _, callErr := captureWindowsBitBlt.Call(memoryDC, 0, 0, uintptr(w), uintptr(h), screenDC,
		uintptr(int32(x)), uintptr(int32(y)), windowsCaptureSRCCopy|windowsCaptureLayered)
	if copied == 0 {
		return nil, windowsCaptureCallError("BitBlt", callErr)
	}

	// GetDIBits 要求目标位图不再被任何 DC 选中。
	restored, _, callErr := captureWindowsSelectObject.Call(memoryDC, previous)
	if restored == 0 || restored == ^uintptr(0) {
		return nil, windowsCaptureCallError("SelectObject restore", callErr)
	}
	selected = false
	pixels := make([]byte, w*h*4)
	info := windowsCaptureBitmapInfo{Header: windowsCaptureBitmapInfoHeader{
		Size:      uint32(unsafe.Sizeof(windowsCaptureBitmapInfoHeader{})),
		Width:     int32(w),
		Height:    -int32(h),
		Planes:    1,
		BitCount:  32,
		SizeImage: uint32(len(pixels)),
	}}
	scanLines, _, callErr := captureWindowsGetDIBits.Call(memoryDC, bitmap, 0, uintptr(h),
		uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&info)), 0)
	if scanLines == 0 {
		return nil, windowsCaptureCallError("GetDIBits", callErr)
	}
	if scanLines != uintptr(h) {
		return nil, fmt.Errorf("截图：GetDIBits 仅返回 %d/%d 行", scanLines, h)
	}
	// 捕获期间可能进入锁屏或 UAC 安全桌面，不能把此时的黑屏视为成功。
	if err := windowsCaptureCheckInputDesktop(); err != nil {
		return nil, err
	}
	return windowsCapturePixelsPNG(pixels, w, h)
}

func windowsCaptureCheckInputDesktop() error {
	const desktopReadObjectsAndSwitch = 0x0001 | 0x0100
	inputDesktop, _, callErr := captureWindowsOpenInputDesktop.Call(0, 0, desktopReadObjectsAndSwitch)
	if inputDesktop == 0 {
		return windowsCaptureCallError("OpenInputDesktop（输入桌面不可截图）", callErr)
	}
	defer captureWindowsCloseDesktop.Call(inputDesktop)
	threadDesktop, _, callErr := captureWindowsGetThreadDesktop.Call(uintptr(windows.GetCurrentThreadId()))
	if threadDesktop == 0 {
		return windowsCaptureCallError("GetThreadDesktop", callErr)
	}
	inputName, err := windowsCaptureDesktopName(inputDesktop)
	if err != nil {
		return err
	}
	threadName, err := windowsCaptureDesktopName(threadDesktop)
	if err != nil {
		return err
	}
	return validateWindowsCaptureInputDesktop(inputName, threadName)
}

func windowsCaptureDesktopName(desktop uintptr) (string, error) {
	const userObjectName = 2
	var name [256]uint16
	var needed uint32
	result, _, callErr := captureWindowsGetUserObjectInfo.Call(desktop, userObjectName,
		uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Sizeof(name)), uintptr(unsafe.Pointer(&needed)))
	if result == 0 {
		return "", windowsCaptureCallError("GetUserObjectInformationW", callErr)
	}
	if needed < 2 || needed > uint32(unsafe.Sizeof(name)) || needed%2 != 0 || name[needed/2-1] != 0 {
		return "", errors.New("截图：Windows 桌面名称无效")
	}
	return windows.UTF16ToString(name[:needed/2]), nil
}

func validateWindowsCaptureInputDesktop(inputName, threadName string) error {
	if !strings.EqualFold(inputName, "Default") || !strings.EqualFold(threadName, inputName) {
		return errors.New("截图：Windows 输入桌面不可截图（锁屏、安全桌面或当前桌面不匹配）")
	}
	return nil
}

func windowsCapturePixelsPNG(pixels []byte, w, h int) ([]byte, error) {
	if err := validateWindowsCaptureRegion(0, 0, w, h); err != nil {
		return nil, err
	}
	if len(pixels) != w*h*4 {
		return nil, errors.New("截图：Windows 像素缓冲区长度无效")
	}
	for offset := 0; offset < len(pixels); offset += 4 {
		pixels[offset], pixels[offset+2] = pixels[offset+2], pixels[offset]
		pixels[offset+3] = 0xff
	}
	img := &image.RGBA{Pix: pixels, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return nil, fmt.Errorf("编码 Windows 截图 PNG：%w", err)
	}
	return encoded.Bytes(), nil
}

func windowsCaptureCallError(operation string, err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return fmt.Errorf("截图：%s 失败", operation)
	}
	return fmt.Errorf("截图：%s 失败：%w", operation, err)
}
