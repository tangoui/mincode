//go:build tango_dev && cgo

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

typedef struct {
	double buttonY[3];
	double titleBarTop;
	double contentTop;
} titleBarGeometry;

static titleBarGeometry readTitleBarGeometry(void *nativeWindow) {
	NSWindow *window = (NSWindow *)nativeWindow;
	NSView *content = window.contentView;
	NSView *frame = content.superview;
	CGFloat top = frame.isFlipped ? NSMinY(frame.bounds) : NSMaxY(frame.bounds);
	CGFloat down = frame.isFlipped ? 1 : -1;
	titleBarGeometry result;
	NSWindowButton kinds[] = {NSWindowCloseButton, NSWindowMiniaturizeButton, NSWindowZoomButton};
	for (int i = 0; i < 3; i++) {
		NSButton *button = [window standardWindowButton:kinds[i]];
		NSPoint center = NSMakePoint(NSMidX(button.bounds), NSMidY(button.bounds));
		result.buttonY[i] = button ? down * ([button convertPoint:center toView:frame].y - top) : -1;
	}
	NSView *titleBar = [window standardWindowButton:NSWindowCloseButton].superview.superview;
	NSRect titleBarRect = [titleBar convertRect:titleBar.bounds toView:frame];
	CGFloat titleBarTop = frame.isFlipped ? NSMinY(titleBarRect) : NSMaxY(titleBarRect);
	NSRect contentRect = [content convertRect:content.bounds toView:frame];
	CGFloat contentTop = frame.isFlipped ? NSMinY(contentRect) : NSMaxY(contentRect);
	result.titleBarTop = titleBar ? down * (titleBarTop - top) : -1;
	result.contentTop = down * (contentTop - top);
	return result;
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"unsafe"

	"github.com/tangoui/tango"
	"github.com/tangoui/tango/ipc"
)

func init() {
	registerTitleBarProbe = func(app *tango.App, router *ipc.Router, surface *tango.Surface) {
		if os.Getenv("MINCODE_E2E_TITLEBAR") != "1" {
			return
		}
		router.Handle("mincode.e2e.titlebar", func(ctx context.Context, _ json.RawMessage) (any, error) {
			page, _ := ipc.ConnFrom(ctx)
			if page != surface {
				return nil, errors.New("expected the MinCode main window")
			}
			return tango.Await(app, func() (result struct {
				ButtonY     [3]float64
				TitleBarTop float64
				ContentTop  float64
			}) {
				window := surface.Native()
				if window == 0 {
					return
				}
				geometry := C.readTitleBarGeometry(unsafe.Pointer(window))
				for i, y := range geometry.buttonY {
					result.ButtonY[i] = float64(y)
				}
				result.TitleBarTop = float64(geometry.titleBarTop)
				result.ContentTop = float64(geometry.contentTop)
				return
			}), nil
		})
	}
}
