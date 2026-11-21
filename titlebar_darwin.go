package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#include <stdint.h>

static void alignTitleBar(NSWindow *window, CGFloat centerY) {
	if ((window.styleMask & NSWindowStyleMaskFullScreen) != 0) return;
	NSView *content = window.contentView;
	if (content == nil || centerY > NSHeight(content.bounds)) return;
	CGFloat targetY = content.isFlipped ? NSMinY(content.bounds) + centerY : NSMaxY(content.bounds) - centerY;

	// AppKit draws the window-edge decoration in the title bar container.
	// Keep that container at the window edge and move only the buttons.
	NSWindowButton kinds[] = {NSWindowCloseButton, NSWindowMiniaturizeButton, NSWindowZoomButton};
	for (int i = 0; i < 3; i++) {
		NSButton *button = [window standardWindowButton:kinds[i]];
		NSView *parent = button.superview;
		if (parent == nil) continue;
		NSPoint center = NSMakePoint(NSMidX(button.bounds), NSMidY(button.bounds));
		NSPoint target = [button convertPoint:center toView:content];
		target.y = targetY;
		CGFloat delta = [content convertPoint:target toView:parent].y - [button convertPoint:center toView:parent].y;
		if (fabs(delta) < 0.01) continue;
		NSPoint origin = button.frame.origin;
		origin.y += delta;
		[button setFrameOrigin:origin];
	}
}

static void scheduleAlignTitleBar(uintptr_t nativeWindow, CGFloat centerY) {
	NSWindow *window = (NSWindow *)nativeWindow;
	dispatch_async(dispatch_get_main_queue(), ^{ alignTitleBar(window, centerY); });
}
*/
import "C"

import (
	"encoding/json"

	"github.com/tangoui/tango"
	"github.com/tangoui/tango/ipc"
)

var registerTitleBarProbe = func(*tango.App, *ipc.Router, *tango.Surface) {}

// The main page can load its assets from a development server. Measure the
// rendered row so its height need not match the native host's build.
const observeTitleBar = `(() => {
	const observe = () => {
		const row = document.querySelector(".app-frame--unified-titlebar .app-top-tabs");
		if (!row) return false;
		let previous;
		const report = () => {
			const { top, height } = row.getBoundingClientRect();
			const centerY = top + height / 2;
			if (height > 0 && centerY !== previous) {
				previous = centerY;
				window.tango.emit("mincode.titlebar.layout", centerY);
			}
		};
		new ResizeObserver(report).observe(row);
		report();
		return true;
	};
	if (!observe()) {
		const mount = new MutationObserver(() => {
			if (observe()) mount.disconnect();
		});
		mount.observe(document.documentElement, { childList: true, subtree: true });
	}
})()`

func alignMainTitleBar(app *tango.App, router *ipc.Router, surface *tango.Surface) {
	registerTitleBarProbe(app, router, surface)
	var centerY float64 // Tango delivers surface callbacks and page events on the main thread.
	align := func() {
		if centerY > 0 {
			if window := surface.Native(); window != 0 {
				C.scheduleAlignTitleBar(C.uintptr_t(window), C.CGFloat(centerY))
			}
		}
	}
	off := router.On("mincode.titlebar.layout", func(page ipc.Conn, raw json.RawMessage) {
		var y float64
		if page == surface && json.Unmarshal(raw, &y) == nil && y > 0 {
			centerY = y
			align()
		}
	})
	surface.OnClosed(off)
	surface.OnLoaded(func() {
		surface.Eval(observeTitleBar)
	})
	surface.OnFocused(align)
	surface.OnResized(func(width, height int) { align() })
	surface.OnStateChanged(func(state tango.WindowState) { align() })
}
