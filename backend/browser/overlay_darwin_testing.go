//go:build darwin

package browser

// Test-only helpers for the darwin overlay tests. cgo is not allowed in
// _test.go files, so the native pieces the tests need live here; production
// code never calls them and the linker drops the Go side.

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc
#cgo darwin LDFLAGS: -framework CoreGraphics
#import <Cocoa/Cocoa.h>
#import <CoreGraphics/CoreGraphics.h>
#include <stdlib.h>
#include "overlay_darwin.h"

static bool mincode_test_gui_available(void) {
  CFDictionaryRef session = CGSessionCopyCurrentDictionary();
  if (session == NULL) return false;
  CFRelease(session);
  return true;
}

// Runs the main thread's loop forever; the test binary exits via os.Exit.
static void mincode_test_run_main_loop(bool gui) {
  if (gui) {
    [NSApplication sharedApplication];
    [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    [NSApp run];
    return;
  }
  // Keep a timer installed so the run loop has a source and services the main queue.
  NSTimer *keepAlive = [NSTimer timerWithTimeInterval:3600 repeats:YES block:^(NSTimer *t) {}];
  [[NSRunLoop mainRunLoop] addTimer:keepAlive forMode:NSDefaultRunLoopMode];
  for (;;) CFRunLoopRunInMode(kCFRunLoopDefaultMode, 1.0, false);
}

static void *mincode_test_new_window(void) {
  __block void *out = NULL;
  dispatch_sync(dispatch_get_main_queue(), ^{
    NSWindow *win = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 640, 480)
                                                styleMask:NSWindowStyleMaskBorderless
                                                  backing:NSBackingStoreBuffered
                                                    defer:NO];
    win.releasedWhenClosed = NO;
    [win orderBack:nil];
    out = (__bridge_retained void *)win;
  });
  return out;
}

static void mincode_test_release_window(void *ptr) {
  dispatch_sync(dispatch_get_main_queue(), ^{
    NSWindow *win = (__bridge_transfer NSWindow *)ptr;
    [win orderOut:nil];
    [win close];
  });
}
*/
import "C"

import "unsafe"

func darwinTestGUIAvailable() bool               { return bool(C.mincode_test_gui_available()) }
func darwinTestRunMainLoop(gui bool)             { C.mincode_test_run_main_loop(C.bool(gui)) }
func darwinTestNewWindow() unsafe.Pointer        { return C.mincode_test_new_window() }
func darwinTestReleaseWindow(win unsafe.Pointer) { C.mincode_test_release_window(win) }

// darwinTestRegisterDialog stores a fake completion handler under dialogID for tabID.
func darwinTestRegisterDialog(tabID, dialogID string) {
	cTab, cID := C.CString(tabID), C.CString(dialogID)
	defer C.free(unsafe.Pointer(cTab))
	defer C.free(unsafe.Pointer(cID))
	C.mincode_browser_test_register_dialog(cTab, cID)
}

// darwinTestDialogCalls reports how many times fake handlers have run.
func darwinTestDialogCalls() int { return int(C.mincode_browser_test_dialog_calls()) }

// darwinTestPopupCount reports how many popup tabs hold a slot of the global cap.
func darwinTestPopupCount() int { return int(C.mincode_browser_test_popup_count()) }

// darwinTestMouseClick sends a left mouse down/up to the tab's web view at a
// point in its own coordinates, the way AppKit delivers a real click.
func darwinTestMouseClick(win unsafe.Pointer, tabID string, x, y float64) {
	cTab := C.CString(tabID)
	defer C.free(unsafe.Pointer(cTab))
	C.mincode_browser_test_mouse_click(win, cTab, C.double(x), C.double(y))
}

// darwinTestFinishFileChooser finishes the tab's open file chooser with path
// ("" cancels). It returns -1 when none is open, else bit 0 = multiple, bit 1 = directories.
func darwinTestFinishFileChooser(win unsafe.Pointer, tabID, path string) int {
	cTab := C.CString(tabID)
	defer C.free(unsafe.Pointer(cTab))
	var cPath *C.char
	if path != "" {
		cPath = C.CString(path)
		defer C.free(unsafe.Pointer(cPath))
	}
	return int(C.mincode_browser_test_finish_file_chooser(win, cTab, cPath))
}

// darwinTestFileChooserMessage returns the open panel's message for the tab ("" when none).
func darwinTestFileChooserMessage(win unsafe.Pointer, tabID string) string {
	cTab := C.CString(tabID)
	defer C.free(unsafe.Pointer(cTab))
	cs := C.mincode_browser_test_file_chooser_message(win, cTab)
	if cs == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(cs))
	return C.GoString(cs)
}

// darwinTestSavePanel reports the tab's open save panel: its message and
// suggested file name.
func darwinTestSavePanel(win unsafe.Pointer, tabID string) (message, name string, open bool) {
	cTab := C.CString(tabID)
	defer C.free(unsafe.Pointer(cTab))
	var cName *C.char
	cs := C.mincode_browser_test_save_panel(win, cTab, &cName)
	if cs == nil {
		return "", "", false
	}
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(cName))
	return C.GoString(cs), C.GoString(cName), true
}

// darwinTestFinishSavePanel finishes the tab's save panel as if the user chose
// path ("" cancels); false when none is open.
func darwinTestFinishSavePanel(win unsafe.Pointer, tabID, path string) bool {
	cTab := C.CString(tabID)
	defer C.free(unsafe.Pointer(cTab))
	var cPath *C.char
	if path != "" {
		cPath = C.CString(path)
		defer C.free(unsafe.Pointer(cPath))
	}
	return C.mincode_browser_test_finish_save_panel(win, cTab, cPath) == 0
}
