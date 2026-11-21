//go:build darwin

package browser

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc
#cgo darwin LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#include "overlay_darwin.h"
*/
import "C"

import "unsafe"

func cOptional(value string) (*C.char, func()) {
	if value == "" {
		return nil, func() {}
	}
	ptr := C.CString(value)
	return ptr, func() { C.free(unsafe.Pointer(ptr)) }
}

func platformMount(win unsafe.Pointer, tabID, url, userAgent string, x, y, width, height float64) error {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	cURL, freeURL := cOptional(url)
	defer freeURL()
	cUA, freeUA := cOptional(userAgent)
	defer freeUA()
	rc := C.mincode_browser_mount(win, cTab, cURL, cUA,
		C.double(x), C.double(y), C.double(width), C.double(height))
	if rc != 0 {
		return errMountFailed(int(rc))
	}
	return nil
}

func platformSetBounds(win unsafe.Pointer, tabID string, x, y, width, height float64, visible bool) {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	C.mincode_browser_set_bounds(win, cTab, C.double(x), C.double(y),
		C.double(width), C.double(height), C.bool(visible))
}

func platformHide(win unsafe.Pointer)  { C.mincode_browser_hide(win) }
func platformClose(win unsafe.Pointer) { C.mincode_browser_close(win) }

func platformCloseTab(win unsafe.Pointer, tabID string) {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	C.mincode_browser_close_tab(win, cTab)
}

func platformActivate(win unsafe.Pointer, tabID string) {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	C.mincode_browser_activate(win, cTab)
}

func platformNavigate(win unsafe.Pointer, tabID, url string) {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	cURL, freeURL := cOptional(url)
	defer freeURL()
	C.mincode_browser_navigate(win, cTab, cURL)
}

func platformReload(win unsafe.Pointer, tabID string) {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	C.mincode_browser_reload(win, cTab)
}

func platformSetZoom(win unsafe.Pointer, tabID string, zoom float64) {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	C.mincode_browser_set_zoom(win, cTab, C.double(zoom))
}

func platformGetURL(win unsafe.Pointer, tabID string) string {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	cs := C.mincode_browser_get_url(win, cTab)
	if cs == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cs))
	return C.GoString(cs)
}

func platformEval(win unsafe.Pointer, tabID, script string, timeoutMs int) string {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	cScript := C.CString(script)
	defer C.free(unsafe.Pointer(cScript))
	cs := C.mincode_browser_eval(win, cTab, cScript, C.int(timeoutMs))
	if cs == nil {
		return `{"ok":false,"error":"eval returned null"}`
	}
	defer C.free(unsafe.Pointer(cs))
	return C.GoString(cs)
}

func platformCapture(win unsafe.Pointer, tabID string, timeoutMs int) string {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	cs := C.mincode_browser_capture(win, cTab, C.int(timeoutMs))
	if cs == nil {
		return `{"ok":false,"error":"snapshot returned null"}`
	}
	defer C.free(unsafe.Pointer(cs))
	return C.GoString(cs)
}

func platformNavigationErrors(win unsafe.Pointer, tabID string) string {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	cs := C.mincode_browser_navigation_errors(win, cTab)
	if cs == nil {
		return "[]"
	}
	defer C.free(unsafe.Pointer(cs))
	return C.GoString(cs)
}

func platformToggleDevtools(win unsafe.Pointer, tabID string) error {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	rc := C.mincode_browser_toggle_devtools(win, cTab)
	if rc != 0 {
		return errDevtoolsFailed(int(rc))
	}
	return nil
}

func platformClearData(win unsafe.Pointer) { C.mincode_browser_clear_data(win) }

func platformScreenRegion(win unsafe.Pointer, x, y, width, height float64) (rx, ry, rw, rh int, ok bool) {
	var cx, cy, cw, ch C.int
	rc := C.mincode_browser_screen_region(win, C.double(x), C.double(y),
		C.double(width), C.double(height), &cx, &cy, &cw, &ch)
	if rc != 0 {
		return 0, 0, 0, 0, false
	}
	return int(cx), int(cy), int(cw), int(ch), true
}

func platformExists(win unsafe.Pointer) bool { return bool(C.mincode_browser_exists(win)) }
func platformAvailable() bool                { return true }

// platformActiveTabID reports the tab whose native view the overlay shows; "" means unknown.
var platformActiveTabID = func(win unsafe.Pointer) string {
	cs := C.mincode_browser_active_tab_id(win)
	if cs == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cs))
	return C.GoString(cs)
}

// platformCompleteDialog runs the page dialog's WebKit completion handler. The
// window may be nil: handlers are kept process-wide, not per window.
var platformCompleteDialog = func(win unsafe.Pointer, tabID, dialogID string, accept bool, text string) error {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	cID, freeID := cOptional(dialogID)
	defer freeID()
	cText, freeText := cOptional(text)
	defer freeText()
	if C.mincode_browser_complete_dialog(win, cTab, cID, C.bool(accept), cText) != 0 {
		return newToolError(errCodeDialogAlreadyHandled, "this page dialog was already answered or closed", map[string]any{"tabId": tabID, "dialogId": dialogID})
	}
	return nil
}

// goBrowserDialogOpened is called by the overlay on the main thread when a page
// opens alert/confirm/prompt. openDialog only takes the dialogs lock.
//
//export goBrowserDialogOpened
func goBrowserDialogOpened(tabID, dialogID, kind, message, defaultText, origin *C.char) {
	openDialog(pendingDialog{
		ID:          C.GoString(dialogID),
		TabID:       C.GoString(tabID),
		Kind:        C.GoString(kind),
		Message:     C.GoString(message),
		DefaultText: C.GoString(defaultText),
		Origin:      C.GoString(origin),
	})
}

// goBrowserDialogsCancelled is called on the main thread after the overlay has
// cancelled a tab's handlers (tab closed, content process gone).
//
//export goBrowserDialogsCancelled
func goBrowserDialogsCancelled(tabID *C.char) {
	cancelDialogsForTab(C.GoString(tabID))
}

// goBrowserAdmitPopup is called on the main thread when a page asks for a new
// window. It returns the new tab id (prefixed popupTabIDPrefix) or, when the
// window is refused, a short reason without the address; the caller frees it.
// Only the popup-allowance lock is taken.
//
//export goBrowserAdmitPopup
func goBrowserAdmitPopup(openerTabID, url *C.char) *C.char {
	tabID, err := admitPopup(C.GoString(openerTabID), C.GoString(url))
	if err != nil {
		return C.CString(err.Error())
	}
	return C.CString(tabID)
}

// goBrowserUserInput is called on the main thread for each real mouse or key
// event the tab's web view receives.
//
//export goBrowserUserInput
func goBrowserUserInput(tabID *C.char) {
	noteUserInput(C.GoString(tabID))
}

// goBrowserPopupClosed is called on the main thread before the overlay removes
// a popup that closed itself.
//
//export goBrowserPopupClosed
func goBrowserPopupClosed(tabID *C.char) {
	popupClosed(C.GoString(tabID))
}

// goBrowserFileChooser is called on the main thread when a tab's file chooser
// opens (count 0) or closes with count chosen files. Only the file-chooser lock
// is taken; paths never reach Go.
//
//export goBrowserFileChooser
func goBrowserFileChooser(tabID *C.char, open C.bool, count C.int) {
	if bool(open) {
		fileChooserOpened(C.GoString(tabID))
		return
	}
	fileChooserClosed(C.GoString(tabID), int(count))
}

// goBrowserAdmitFileChooser is called on the main thread before the overlay
// shows a page's file chooser; false refuses it. Only the file-chooser lock is taken.
//
//export goBrowserAdmitFileChooser
func goBrowserAdmitFileChooser(tabID *C.char) C.bool {
	return C.bool(admitFileChooser(C.GoString(tabID)))
}

// goBrowserDownloadStarted is called on the main thread when WebKit asks where
// to save a tab's download. It records a pending download and returns its id;
// *filename receives the sanitized name for the save panel. The caller frees
// both. Only the downloads lock is taken.
//
//export goBrowserDownloadStarted
func goBrowserDownloadStarted(tabID, origin, suggested *C.char, filename **C.char) *C.char {
	id, name := downloadStarted(C.GoString(tabID), C.GoString(origin), C.GoString(suggested))
	*filename = C.CString(name)
	return C.CString(id)
}

// goBrowserDownloadUpdated is called on the main thread with a download's
// progress or final state ("" keeps the state; total < 0 is unknown).
//
//export goBrowserDownloadUpdated
func goBrowserDownloadUpdated(id, state *C.char, bytes, total C.longlong, errMsg *C.char) {
	downloadUpdated(C.GoString(id), C.GoString(state), int64(bytes), int64(total), C.GoString(errMsg))
}

// goBrowserDownloadDestination is called on the main thread with the path the
// user chose in the save panel; it stays in Go.
//
//export goBrowserDownloadDestination
func goBrowserDownloadDestination(id, path *C.char) {
	downloadDestination(C.GoString(id), C.GoString(path))
}

// goBrowserAdmitDownload is called on the main thread before the overlay shows
// a save panel; false cancels the download. Only the downloads lock is taken.
//
//export goBrowserAdmitDownload
func goBrowserAdmitDownload(tabID *C.char) C.bool {
	return C.bool(admitDownload(C.GoString(tabID)))
}

// goBrowserNavigationAllowed is called on the main thread for each navigation
// a page starts. It takes no locks.
//
//export goBrowserNavigationAllowed
func goBrowserNavigationAllowed(rawURL *C.char, mainFrame C.bool) C.bool {
	return C.bool(pageNavigationAllowed(C.GoString(rawURL), bool(mainFrame)))
}

// goBrowserDownloadURLAllowed is called on the main thread for links the page
// marks as downloads. It takes no locks.
//
//export goBrowserDownloadURLAllowed
func goBrowserDownloadURLAllowed(rawURL *C.char) C.bool {
	return C.bool(downloadURLAllowed(C.GoString(rawURL)))
}

// goBrowserDownloadRedirectAllowed is called on the main thread when a running
// download is redirected. It takes no locks.
//
//export goBrowserDownloadRedirectAllowed
func goBrowserDownloadRedirectAllowed(rawURL *C.char) C.bool {
	return C.bool(downloadRedirectAllowed(C.GoString(rawURL)))
}

// platformGoHistory runs goBack (delta < 0) or goForward on the tab's web view
// when canGoBack/canGoForward allows it; false means there is no such entry.
var platformGoHistory = func(win unsafe.Pointer, tabID string, delta int) (bool, error) {
	cTab, freeTab := cOptional(tabID)
	defer freeTab()
	return bool(C.mincode_browser_go_history(win, cTab, C.int(delta))), nil
}
