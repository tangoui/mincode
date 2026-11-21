//go:build darwin

package misc

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=10.13
#cgo darwin LDFLAGS: -framework Cocoa -mmacosx-version-min=10.13

#import <Cocoa/Cocoa.h>
#import <stdlib.h>
#import <string.h>

static int mincode_clipboard_write_files(const char **paths, int count) {
  __block int result = 0;
  void (^writeClipboard)(void) = ^{
    @autoreleasepool {
      NSMutableArray<NSURL *> *urls = [NSMutableArray array];
      for (int i = 0; i < count; i++) {
        NSString *path = [NSString stringWithUTF8String:paths[i]];
        NSURL *url = [NSURL fileURLWithPath:path];
        if (url != nil) [urls addObject:url];
      }
      if (urls.count == 0) { result = 1; return; }
      NSPasteboard *pasteboard = [NSPasteboard generalPasteboard];
      [pasteboard clearContents];
      if (![pasteboard writeObjects:urls]) result = 2;
    }
  };
  if ([NSThread isMainThread]) writeClipboard();
  else dispatch_sync(dispatch_get_main_queue(), writeClipboard);
  return result;
}

// 返回换行分隔的绝对路径列表；无文件时返回 NULL。调用方负责 free。
static char *mincode_clipboard_read_files(void) {
  __block char *result = NULL;
  void (^readClipboard)(void) = ^{
    @autoreleasepool {
      NSPasteboard *pasteboard = [NSPasteboard generalPasteboard];
      NSArray *urls = [pasteboard readObjectsForClasses:@[ [NSURL class] ]
        options:@{ NSPasteboardURLReadingFileURLsOnlyKey: @YES }];
      if (urls.count == 0) return;
      NSMutableArray<NSString *> *paths = [NSMutableArray array];
      for (NSURL *url in urls) {
        NSString *path = [url path];
        if (path.length > 0) [paths addObject:path];
      }
      if (paths.count == 0) return;
      NSString *joined = [paths componentsJoinedByString:@"\n"];
      const char *utf8 = [joined UTF8String];
      if (utf8 != NULL) result = strdup(utf8);
    }
  };
  if ([NSThread isMainThread]) readClipboard();
  else dispatch_sync(dispatch_get_main_queue(), readClipboard);
  return result;
}
*/
import "C"

import (
	"errors"
	"strconv"
	"strings"
	"unsafe"
)

func clipboardWriteFiles(paths []string) error {
	cPaths := make([]*C.char, len(paths))
	for i, p := range paths {
		cPaths[i] = C.CString(p)
		defer C.free(unsafe.Pointer(cPaths[i]))
	}
	code := C.mincode_clipboard_write_files(&cPaths[0], C.int(len(cPaths)))
	if code != 0 {
		return errors.New("clipboard write files failed with code " + strconv.Itoa(int(code)))
	}
	return nil
}

func clipboardReadFiles() ([]string, error) {
	cOut := C.mincode_clipboard_read_files()
	if cOut == nil {
		return []string{}, nil
	}
	defer C.free(unsafe.Pointer(cOut))
	out := C.GoString(cOut)
	return strings.Split(out, "\n"), nil
}
