//go:build !darwin && !windows

package browser

import (
	"errors"
	"unsafe"
)

func platformMount(_ unsafe.Pointer, _, _, _ string, _, _, _, _ float64) error {
	return errors.New("embedded browser is not supported on this platform")
}
func platformSetBounds(_ unsafe.Pointer, _ string, _, _, _, _ float64, _ bool) {}
func platformHide(_ unsafe.Pointer)                                            {}
func platformClose(_ unsafe.Pointer)                                           {}
func platformCloseTab(_ unsafe.Pointer, _ string)                              {}
func platformActivate(_ unsafe.Pointer, _ string)                              {}
func platformNavigate(_ unsafe.Pointer, _, _ string)                           {}
func platformReload(_ unsafe.Pointer, _ string)                                {}
func platformSetZoom(_ unsafe.Pointer, _ string, _ float64)                    {}
func platformGetURL(_ unsafe.Pointer, _ string) string                         { return "" }
func platformEval(_ unsafe.Pointer, _, _ string, _ int) string {
	return `{"ok":false,"error":"embedded browser is not supported on this platform"}`
}
func platformCapture(_ unsafe.Pointer, _ string, _ int) string {
	return `{"ok":false,"error":"embedded browser capture is not supported on this platform"}`
}
func platformNavigationErrors(_ unsafe.Pointer, _ string) string { return "[]" }
func platformToggleDevtools(_ unsafe.Pointer, _ string) error {
	return errors.New("embedded browser developer tools are not supported on this platform")
}
func platformClearData(_ unsafe.Pointer) {}
func platformScreenRegion(_ unsafe.Pointer, _, _, _, _ float64) (int, int, int, int, bool) {
	return 0, 0, 0, 0, false
}
func platformExists(_ unsafe.Pointer) bool { return false }
func platformAvailable() bool              { return false }

// platformActiveTabID reports the natively visible tab; "" means unknown.
var platformActiveTabID = func(_ unsafe.Pointer) string { return "" }

// platformCompleteDialog answers a page dialog natively; this platform has no native browser.
var platformCompleteDialog = func(_ unsafe.Pointer, _, _ string, _ bool, _ string) error {
	return newToolError(errCodeUnsupportedOnPlatform, "page dialogs are not supported on this platform", nil)
}

// platformGoHistory steps the tab's history; this platform has no native browser.
var platformGoHistory = func(_ unsafe.Pointer, _ string, _ int) (bool, error) {
	return false, newToolError(errCodeUnsupportedOnPlatform, "browser history navigation is not supported on this platform", nil)
}
