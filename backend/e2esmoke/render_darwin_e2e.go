//go:build e2e && darwin

package e2esmoke

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/message.h>

static WKWebView *e2eFindWebView(NSView *view) {
	if ([view isKindOfClass:[WKWebView class]]) return (WKWebView *)view;
	for (NSView *child in view.subviews) {
		WKWebView *found = e2eFindWebView(child);
		if (found) return found;
	}
	return nil;
}

// Returns bit 1 when App Nap is held off, bit 2 when WebKit's occlusion
// detection was turned off for the window's web view.
static int e2eKeepRendering(void *window) {
	int result = 0;
	static id activity = nil;
	if (activity == nil) {
		activity = [[[NSProcessInfo processInfo]
			beginActivityWithOptions:(NSActivityUserInitiatedAllowingIdleSystemSleep | NSActivityLatencyCritical)
			reason:@"MinCode desktop smoke harness"] retain];
	}
	if (activity != nil) result |= 1;
	NSWindow *nsWindow = (NSWindow *)window;
	WKWebView *webView = nsWindow ? e2eFindWebView(nsWindow.contentView) : nil;
	SEL setter = NSSelectorFromString(@"_setWindowOcclusionDetectionEnabled:");
	if (webView && [webView respondsToSelector:setter]) {
		((void (*)(id, SEL, BOOL))objc_msgSend)(webView, setter, NO);
		result |= 2;
	}
	return result;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// KeepRenderingWhenCovered stops macOS from pausing the smoke window while
// other windows cover it. WKWebView suspends requestAnimationFrame, rendering
// updates and ResizeObserver delivery for an occluded window, and App Nap
// throttles the app's timers; layout-driven scenarios then wait forever on a
// desktop that is in use. The occlusion switch is WebKit SPI (the one WebKit's
// own test runners use), so it exists only in `-tags e2e` builds and is
// skipped when a WebKit version no longer responds to it. Call on the main
// thread with the window's NativeWindow handle.
func KeepRenderingWhenCovered(nativeWindow unsafe.Pointer) error {
	if !Enabled() {
		return nil
	}
	result := int(C.e2eKeepRendering(nativeWindow))
	if result&2 == 0 {
		return fmt.Errorf("web view occlusion detection could not be turned off (app nap held off: %t)", result&1 != 0)
	}
	if result&1 == 0 {
		return fmt.Errorf("app nap could not be held off")
	}
	return nil
}
