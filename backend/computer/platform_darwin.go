//go:build darwin

package computer

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc
#cgo darwin LDFLAGS: -framework Cocoa -framework ApplicationServices -framework Carbon -framework CoreImage -framework CoreMedia -framework CoreVideo
#include <stdlib.h>
#include "platform_darwin.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unsafe"
)

type darwinCapturePayload struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error"`
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type darwinDisplayListPayload struct {
	OK       bool          `json:"ok"`
	Error    string        `json:"error"`
	Displays []DisplayInfo `json:"displays"`
}

func platformStatus() Status {
	displays := darwinDisplays()
	primary := DisplayInfo{}
	for _, display := range displays {
		if display.Primary {
			primary = display
			break
		}
	}
	if primary.PixelWidth <= 0 {
		primary = darwinDisplayInfo()
		if primary.PixelWidth > 0 {
			primary.Primary = true
			displays = []DisplayInfo{primary}
		}
	}
	return Status{
		Supported:               bool(C.mincode_computer_supported()),
		Platform:                "darwin",
		AccessibilityAuthorized: bool(C.mincode_computer_accessibility_trusted(false)),
		ScreenCaptureAuthorized: bool(C.mincode_computer_screen_capture_authorized()),
		Display:                 primary,
		Displays:                displays,
	}
}

func darwinDisplays() []DisplayInfo {
	raw := C.mincode_computer_display_list()
	if raw == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(raw))
	var payload darwinDisplayListPayload
	if err := json.Unmarshal([]byte(C.GoString(raw)), &payload); err != nil || !payload.OK {
		return nil
	}
	displays := payload.Displays[:0]
	for _, display := range payload.Displays {
		if display.PointWidth <= 0 || display.PointHeight <= 0 || display.PixelWidth <= 0 || display.PixelHeight <= 0 {
			continue
		}
		display.ScaleX = float64(display.PixelWidth) / display.PointWidth
		display.ScaleY = float64(display.PixelHeight) / display.PointHeight
		displays = append(displays, display)
	}
	return displays
}

func platformRequestPermissions(accessibility, screenCapture bool) Status {
	if accessibility {
		C.mincode_computer_accessibility_trusted(true)
	}
	if screenCapture {
		C.mincode_computer_request_screen_capture()
	}
	return platformStatus()
}

func darwinDisplayInfo() DisplayInfo {
	var displayID C.uint32_t
	var originX, originY, pointWidth, pointHeight C.double
	var pixelWidth, pixelHeight C.int
	if C.mincode_computer_main_display(
		&displayID,
		&originX,
		&originY,
		&pointWidth,
		&pointHeight,
		&pixelWidth,
		&pixelHeight,
	) != 0 {
		return DisplayInfo{}
	}
	info := DisplayInfo{
		ID:          uint32(displayID),
		OriginX:     float64(originX),
		OriginY:     float64(originY),
		PointWidth:  float64(pointWidth),
		PointHeight: float64(pointHeight),
		PixelWidth:  int(pixelWidth),
		PixelHeight: int(pixelHeight),
	}
	if info.PointWidth > 0 {
		info.ScaleX = float64(info.PixelWidth) / info.PointWidth
	}
	if info.PointHeight > 0 {
		info.ScaleY = float64(info.PixelHeight) / info.PointHeight
	}
	return info
}

func platformCaptureScreen(displayID uint32, timeout time.Duration) (Capture, error) {
	display, ok := findDisplay(platformStatus(), displayID)
	if !ok {
		return Capture{}, fmt.Errorf("display %d is not connected", displayID)
	}
	raw := C.mincode_computer_capture_display(C.uint32_t(display.ID), C.int(timeout.Milliseconds()))
	if raw == nil {
		return Capture{}, errors.New("native screen capture returned no result")
	}
	defer C.free(unsafe.Pointer(raw))
	var payload darwinCapturePayload
	if err := json.Unmarshal([]byte(C.GoString(raw)), &payload); err != nil {
		return Capture{}, fmt.Errorf("decode native screen capture: %w", err)
	}
	if !payload.OK {
		if payload.Error == "" {
			payload.Error = "native screen capture failed"
		}
		return Capture{}, errors.New(payload.Error)
	}
	// ScreenCaptureKit defines the screenshot coordinate frame. Keep its actual
	// image dimensions even if display scaling changes between enumeration and capture.
	display.PixelWidth = payload.Width
	display.PixelHeight = payload.Height
	if display.PointWidth > 0 {
		display.ScaleX = float64(display.PixelWidth) / display.PointWidth
	}
	if display.PointHeight > 0 {
		display.ScaleY = float64(display.PixelHeight) / display.PointHeight
	}
	return Capture{
		MimeType: payload.MimeType,
		Data:     payload.Data,
		Width:    payload.Width,
		Height:   payload.Height,
		Display:  display,
	}, nil
}

func platformMovePointer(x, y float64) error {
	return darwinResult("move pointer", C.mincode_computer_mouse_move(C.double(x), C.double(y)))
}

func platformClick(x, y float64, button string, count int) error {
	return darwinResult("click pointer", C.mincode_computer_mouse_click(C.double(x), C.double(y), C.int(darwinMouseButton(button)), C.int(count)))
}

func platformMouseDown(x, y float64, button string) error {
	return darwinResult("press pointer", C.mincode_computer_mouse_down(C.double(x), C.double(y), C.int(darwinMouseButton(button))))
}

func platformMouseDrag(x, y float64, button string) error {
	return darwinResult("drag pointer", C.mincode_computer_mouse_drag(C.double(x), C.double(y), C.int(darwinMouseButton(button))))
}

func platformMouseUp(x, y float64, button string) error {
	return darwinResult("release pointer", C.mincode_computer_mouse_up(C.double(x), C.double(y), C.int(darwinMouseButton(button))))
}

func platformScroll(deltaX, deltaY float64) error {
	return darwinResult("scroll", C.mincode_computer_scroll(C.double(deltaX), C.double(deltaY)))
}

func platformTypeText(text string) error {
	value := C.CString(text)
	defer C.free(unsafe.Pointer(value))
	return darwinResult("type text", C.mincode_computer_type_text(value))
}

func platformPressKey(key string, modifiers uint32) error {
	value := C.CString(key)
	defer C.free(unsafe.Pointer(value))
	code := C.mincode_computer_press_key(value, C.uint32_t(modifiers))
	if int(code) == -2 {
		return fmt.Errorf("unsupported key %q", key)
	}
	return darwinResult("press key", code)
}

func platformKeyEvent(key string, down bool) error {
	value := C.CString(key)
	defer C.free(unsafe.Pointer(value))
	code := C.mincode_computer_key_event(value, C.bool(down))
	if int(code) == -2 {
		return fmt.Errorf("unsupported key %q", key)
	}
	return darwinResult("key event", code)
}

func platformCursorPosition() (Point, error) {
	var x, y C.double
	if err := darwinResult("read cursor position", C.mincode_computer_cursor_position(&x, &y)); err != nil {
		return Point{}, err
	}
	return Point{X: float64(x), Y: float64(y)}, nil
}

func platformOpenApp(application string) error {
	value := C.CString(application)
	defer C.free(unsafe.Pointer(value))
	return darwinResult("open application", C.mincode_computer_open_app(value))
}

func platformOpenURL(targetURL, browser string) error {
	urlValue := C.CString(targetURL)
	defer C.free(unsafe.Pointer(urlValue))
	browserValue := C.CString(browser)
	defer C.free(unsafe.Pointer(browserValue))
	return darwinResult("open URL", C.mincode_computer_open_url(urlValue, browserValue))
}

func darwinMouseButton(button string) int {
	switch button {
	case "right":
		return 1
	case "wheel":
		return 2
	default:
		return 0
	}
}

func darwinResult(operation string, code C.int) error {
	if code == 0 {
		return nil
	}
	return fmt.Errorf("%s failed with native status %d", operation, int(code))
}
