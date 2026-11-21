//go:build darwin

package misc

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=10.13
#cgo darwin LDFLAGS: -framework Cocoa -mmacosx-version-min=10.13

#import <Cocoa/Cocoa.h>
#import <stdlib.h>

static int mincode_clipboard_write_rich(const char *textValue, const char *htmlValue,
                                         const char *mimeTypeValue,
                                         const unsigned char *imageBytes, long imageLength) {
  if (imageBytes == NULL || imageLength <= 0) return 1;
  __block int result = 0;
  void (^writeClipboard)(void) = ^{
    @autoreleasepool {
      NSString *text = textValue == NULL ? @"" : [NSString stringWithUTF8String:textValue];
      NSString *html = htmlValue == NULL ? @"" : [NSString stringWithUTF8String:htmlValue];
      NSString *mimeType = mimeTypeValue == NULL ? @"image/png" : [NSString stringWithUTF8String:mimeTypeValue];
      NSData *htmlData = [html dataUsingEncoding:NSUTF8StringEncoding];
      NSData *sourceImageData = [NSData dataWithBytes:imageBytes length:(NSUInteger)imageLength];
      NSImage *sourceImage = [[NSImage alloc] initWithData:sourceImageData];
      if (sourceImage == nil) { result = 2; return; }

      NSData *tiff = [sourceImage TIFFRepresentation];
      if (tiff.length == 0) { result = 3; return; }
      NSBitmapImageRep *bitmap = [NSBitmapImageRep imageRepWithData:tiff];
      NSData *png = bitmap == nil ? nil : [bitmap representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
      if (png.length == 0) { result = 4; return; }

      NSPasteboardItem *item = [[NSPasteboardItem alloc] init];
      if (text.length > 0) [item setString:text forType:NSPasteboardTypeString];
      if (htmlData.length > 0) {
        [item setData:htmlData forType:NSPasteboardTypeHTML];
        NSError *attributedError = nil;
        NSAttributedString *attributed = [[NSAttributedString alloc]
          initWithData:htmlData
          options:@{
            NSDocumentTypeDocumentOption: NSHTMLTextDocumentType,
            NSCharacterEncodingDocumentOption: @(NSUTF8StringEncoding)
          }
          documentAttributes:nil
          error:&attributedError];
        if (attributed != nil && attributed.length > 0) {
          NSError *rtfdError = nil;
          NSData *rtfd = [attributed dataFromRange:NSMakeRange(0, attributed.length)
            documentAttributes:@{NSDocumentTypeDocumentAttribute: NSRTFDTextDocumentType}
            error:&rtfdError];
          if (rtfd.length > 0) [item setData:rtfd forType:NSPasteboardTypeRTFD];
        }
      }

      // 同时提供 PNG 与 TIFF：浏览器、聊天软件通常读取 PNG，原生编辑器常优先读取 TIFF。
      [item setData:png forType:NSPasteboardTypePNG];
      [item setData:tiff forType:NSPasteboardTypeTIFF];
      if ([mimeType isEqualToString:@"image/png"]) {
        [item setData:sourceImageData forType:NSPasteboardTypePNG];
      }

      NSPasteboard *pasteboard = [NSPasteboard generalPasteboard];
      [pasteboard clearContents];
      if (![pasteboard writeObjects:@[item]]) result = 5;
    }
  };
  if ([NSThread isMainThread]) writeClipboard();
  else dispatch_sync(dispatch_get_main_queue(), writeClipboard);
  return result;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func clipboardWriteRich(text, html, mimeType string, image []byte) error {
	textValue := C.CString(text)
	htmlValue := C.CString(html)
	mimeTypeValue := C.CString(mimeType)
	defer C.free(unsafe.Pointer(textValue))
	defer C.free(unsafe.Pointer(htmlValue))
	defer C.free(unsafe.Pointer(mimeTypeValue))

	code := C.mincode_clipboard_write_rich(
		textValue,
		htmlValue,
		mimeTypeValue,
		(*C.uchar)(unsafe.Pointer(&image[0])),
		C.long(len(image)),
	)
	if code != 0 {
		return fmt.Errorf("write rich clipboard failed with code %d", int(code))
	}
	return nil
}
