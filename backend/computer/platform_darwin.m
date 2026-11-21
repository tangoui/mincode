#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>
#import <Carbon/Carbon.h>
#import <CoreImage/CoreImage.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <math.h>
#import <string.h>
#import <unistd.h>

#import "platform_darwin.h"

static char *json_string(NSDictionary *value) {
  NSError *error = nil;
  NSData *data = [NSJSONSerialization dataWithJSONObject:value options:0 error:&error];
  if (data == nil) {
    NSString *message = error.localizedDescription ?: @"Could not encode native result";
    NSString *fallback = [NSString stringWithFormat:@"{\"ok\":false,\"error\":\"%@\"}",
                                                    [message stringByReplacingOccurrencesOfString:@"\"" withString:@"'"]];
    return strdup(fallback.UTF8String);
  }
  NSString *json = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
  return strdup(json.UTF8String);
}

bool mincode_computer_supported(void) {
  if (@available(macOS 12.3, *)) return true;
  return false;
}

bool mincode_computer_accessibility_trusted(bool prompt) {
  if (!prompt) return AXIsProcessTrusted();
  NSDictionary *options = @{(__bridge NSString *)kAXTrustedCheckOptionPrompt: @YES};
  return AXIsProcessTrustedWithOptions((__bridge CFDictionaryRef)options);
}

bool mincode_computer_screen_capture_authorized(void) {
  if (@available(macOS 10.15, *)) return CGPreflightScreenCaptureAccess();
  return true;
}

bool mincode_computer_request_screen_capture(void) {
  if (@available(macOS 10.15, *)) return CGRequestScreenCaptureAccess();
  return true;
}

int mincode_computer_main_display(uint32_t *displayId, double *originX,
                                  double *originY, double *pointWidth,
                                  double *pointHeight, int *pixelWidth,
                                  int *pixelHeight) {
  CGDirectDisplayID display = CGMainDisplayID();
  CGRect bounds = CGDisplayBounds(display);
  if (CGRectIsEmpty(bounds)) return -1;
  if (displayId != NULL) *displayId = display;
  if (originX != NULL) *originX = bounds.origin.x;
  if (originY != NULL) *originY = bounds.origin.y;
  if (pointWidth != NULL) *pointWidth = bounds.size.width;
  if (pointHeight != NULL) *pointHeight = bounds.size.height;
  if (pixelWidth != NULL) *pixelWidth = (int)CGDisplayPixelsWide(display);
  if (pixelHeight != NULL) *pixelHeight = (int)CGDisplayPixelsHigh(display);
  return 0;
}

char *mincode_computer_display_list(void) {
  @autoreleasepool {
    CGDirectDisplayID ids[32];
    uint32_t count = 0;
    if (CGGetActiveDisplayList(32, ids, &count) != kCGErrorSuccess) {
      return json_string(@{@"ok": @NO, @"error": @"Could not enumerate active displays"});
    }
    NSMutableArray *displays = [NSMutableArray arrayWithCapacity:count];
    for (uint32_t index = 0; index < count; index++) {
      CGDirectDisplayID display = ids[index];
      CGRect bounds = CGDisplayBounds(display);
      if (CGRectIsEmpty(bounds)) continue;
      [displays addObject:@{
        @"id": @(display),
        @"primary": @(CGDisplayIsMain(display) ? YES : NO),
        @"originX": @(bounds.origin.x),
        @"originY": @(bounds.origin.y),
        @"pointWidth": @(bounds.size.width),
        @"pointHeight": @(bounds.size.height),
        @"pixelWidth": @((int)CGDisplayPixelsWide(display)),
        @"pixelHeight": @((int)CGDisplayPixelsHigh(display))
      }];
    }
    return json_string(@{@"ok": @YES, @"displays": displays});
  }
}

int mincode_computer_cursor_position(double *x, double *y) {
  CGEventRef event = CGEventCreate(NULL);
  if (event == NULL) return -1;
  CGPoint location = CGEventGetLocation(event);
  CFRelease(event);
  if (x != NULL) *x = location.x;
  if (y != NULL) *y = location.y;
  return 0;
}

@interface MincodeComputerCaptureReceiver : NSObject
@property(nonatomic, strong) NSData *pngData;
@property(nonatomic, strong) NSError *error;
@property(nonatomic, assign) size_t width;
@property(nonatomic, assign) size_t height;
@property(nonatomic, assign) BOOL finished;
@property(nonatomic) dispatch_semaphore_t semaphore;
- (NSDictionary *)captureResult;
@end

@implementation MincodeComputerCaptureReceiver
- (void)finishWithData:(NSData *)data width:(size_t)width height:(size_t)height error:(NSError *)error {
  @synchronized(self) {
    if (self.finished) return;
    self.finished = YES;
    self.pngData = data;
    self.width = width;
    self.height = height;
    self.error = error;
    dispatch_semaphore_signal(self.semaphore);
  }
}

- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer ofType:(SCStreamOutputType)type API_AVAILABLE(macos(12.3)) {
  if (type != SCStreamOutputTypeScreen || !CMSampleBufferIsValid(sampleBuffer)) return;
  CVImageBufferRef imageBuffer = CMSampleBufferGetImageBuffer(sampleBuffer);
  if (imageBuffer == NULL) return;
  CIImage *ciImage = [CIImage imageWithCVPixelBuffer:imageBuffer];
  CIContext *context = [CIContext contextWithOptions:nil];
  CGImageRef image = [context createCGImage:ciImage fromRect:ciImage.extent];
  if (image == NULL) {
    NSError *error = [NSError errorWithDomain:@"MinCodeComputerUse" code:2
                                     userInfo:@{NSLocalizedDescriptionKey: @"Could not convert the captured frame"}];
    [self finishWithData:nil width:0 height:0 error:error];
    return;
  }
  size_t width = CGImageGetWidth(image);
  size_t height = CGImageGetHeight(image);
  NSBitmapImageRep *representation = [[NSBitmapImageRep alloc] initWithCGImage:image];
  CGImageRelease(image);
  NSData *png = [representation representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
  if (png == nil || png.length == 0) {
    NSError *error = [NSError errorWithDomain:@"MinCodeComputerUse" code:3
                                     userInfo:@{NSLocalizedDescriptionKey: @"Could not encode the desktop screenshot as PNG"}];
    [self finishWithData:nil width:0 height:0 error:error];
    return;
  }
  [self finishWithData:png width:width height:height error:nil];
}

- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error API_AVAILABLE(macos(12.3)) {
  [self finishWithData:nil width:0 height:0 error:error];
}

- (NSDictionary *)captureResult {
  @synchronized(self) {
    if (self.error != nil || self.pngData == nil) {
      return @{
        @"ok": @NO,
        @"error": self.error.localizedDescription ?: @"Screen capture returned no frame"
      };
    }
    return @{
      @"ok": @YES,
      @"mimeType": @"image/png",
      @"data": [self.pngData base64EncodedStringWithOptions:0],
      @"width": @(self.width),
      @"height": @(self.height)
    };
  }
}
@end

// Only reached from the @available(macOS 12.3, *) branch of the capture path.
API_AVAILABLE(macos(12.3))
static NSError *stop_capture_stream(SCStream *stream, BOOL *timedOut) {
  dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
  __block NSError *stopError = nil;
  [stream stopCaptureWithCompletionHandler:^(NSError *error) {
    stopError = error;
    dispatch_semaphore_signal(semaphore);
  }];
  long waitResult = dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC));
  if (timedOut != NULL) *timedOut = waitResult != 0;
  return stopError;
}

char *mincode_computer_capture_display(uint32_t displayId, int timeoutMs) {
  @autoreleasepool {
    if (timeoutMs < 100) timeoutMs = 100;
    if (timeoutMs > 30000) timeoutMs = 30000;
    // Every wait shares one deadline so a capture cannot exceed the caller's budget.
    dispatch_time_t deadline = dispatch_time(DISPATCH_TIME_NOW, (int64_t)timeoutMs * NSEC_PER_MSEC);
    if (!mincode_computer_screen_capture_authorized()) {
      return json_string(@{
        @"ok": @NO,
        @"error": @"Screen Recording permission has not been granted to MinCode"
      });
    }
    if (@available(macOS 12.3, *)) {
      Class shareableContentClass = NSClassFromString(@"SCShareableContent");
      Class contentFilterClass = NSClassFromString(@"SCContentFilter");
      Class streamConfigurationClass = NSClassFromString(@"SCStreamConfiguration");
      Class streamClass = NSClassFromString(@"SCStream");
      if (shareableContentClass == Nil || contentFilterClass == Nil ||
          streamConfigurationClass == Nil || streamClass == Nil) {
        return json_string(@{@"ok": @NO, @"error": @"ScreenCaptureKit is unavailable on this macOS version"});
      }

      dispatch_semaphore_t contentSemaphore = dispatch_semaphore_create(0);
      __block SCShareableContent *shareableContent = nil;
      __block NSError *contentError = nil;
      [(id)shareableContentClass getShareableContentWithCompletionHandler:^(SCShareableContent *content, NSError *error) {
        shareableContent = content;
        contentError = error;
        dispatch_semaphore_signal(contentSemaphore);
      }];
      if (dispatch_semaphore_wait(contentSemaphore, deadline) != 0) {
        return json_string(@{@"ok": @NO, @"error": @"Timed out while enumerating shareable displays"});
      }
      if (contentError != nil || shareableContent == nil) {
        return json_string(@{@"ok": @NO, @"error": contentError.localizedDescription ?: @"No shareable display content is available"});
      }

      CGDirectDisplayID targetDisplayID = displayId != 0 ? displayId : CGMainDisplayID();
      SCDisplay *targetDisplay = nil;
      for (SCDisplay *display in shareableContent.displays) {
        if (display.displayID == targetDisplayID) {
          targetDisplay = display;
          break;
        }
      }
      if (targetDisplay == nil) {
        return json_string(@{@"ok": @NO, @"error": [NSString stringWithFormat:@"Display %u is not available for capture", targetDisplayID]});
      }

      SCContentFilter *filter = [[contentFilterClass alloc] initWithDisplay:targetDisplay excludingWindows:@[]];
      SCStreamConfiguration *configuration = [[streamConfigurationClass alloc] init];
      configuration.width = CGDisplayPixelsWide(targetDisplayID);
      configuration.height = CGDisplayPixelsHigh(targetDisplayID);
      configuration.pixelFormat = kCVPixelFormatType_32BGRA;
      configuration.minimumFrameInterval = CMTimeMake(1, 60);
      configuration.queueDepth = 1;
      configuration.showsCursor = YES;

      MincodeComputerCaptureReceiver *receiver = [[MincodeComputerCaptureReceiver alloc] init];
      receiver.semaphore = dispatch_semaphore_create(0);
      SCStream *stream = [[streamClass alloc] initWithFilter:filter configuration:configuration delegate:(id<SCStreamDelegate>)receiver];
      NSError *outputError = nil;
      dispatch_queue_t captureQueue = dispatch_queue_create("dev.mincode.computer-use.capture", DISPATCH_QUEUE_SERIAL);
      if (![stream addStreamOutput:(id<SCStreamOutput>)receiver type:SCStreamOutputTypeScreen sampleHandlerQueue:captureQueue error:&outputError]) {
        return json_string(@{@"ok": @NO, @"error": outputError.localizedDescription ?: @"Could not configure screen capture output"});
      }

      dispatch_semaphore_t startSemaphore = dispatch_semaphore_create(0);
      __block NSError *startError = nil;
      [stream startCaptureWithCompletionHandler:^(NSError *error) {
        startError = error;
        dispatch_semaphore_signal(startSemaphore);
      }];
      if (dispatch_semaphore_wait(startSemaphore, deadline) != 0) {
        BOOL stopTimedOut = NO;
        stop_capture_stream(stream, &stopTimedOut);
        return json_string(@{@"ok": @NO, @"error": @"Timed out while starting screen capture"});
      }
      if (startError != nil) {
        // Tear down any partially initialized stream state before returning the
        // original start failure. The start error remains the actionable cause.
        [stream stopCaptureWithCompletionHandler:nil];
        return json_string(@{@"ok": @NO, @"error": startError.localizedDescription});
      }

      long frameWait = dispatch_semaphore_wait(receiver.semaphore, deadline);
      BOOL stopTimedOut = NO;
      NSError *stopError = stop_capture_stream(stream, &stopTimedOut);
      if (frameWait != 0) {
        return json_string(@{@"ok": @NO, @"error": @"Timed out while waiting for a desktop frame"});
      }
      if (stopTimedOut) {
        return json_string(@{@"ok": @NO, @"error": @"Timed out while stopping screen capture"});
      }
      if (stopError != nil) {
        return json_string(@{@"ok": @NO, @"error": stopError.localizedDescription ?: @"Could not stop screen capture"});
      }
      return json_string([receiver captureResult]);
    }
    return json_string(@{@"ok": @NO, @"error": @"Desktop capture requires macOS 12.3 or later"});
  }
}

// Modifier flags for keys currently held with mincode_computer_key_event. Go
// serializes all desktop input, so this needs no additional locking.
static CGEventFlags g_held_flags = 0;

static CGMouseButton mouse_button(int button) {
  switch (button) {
    case 1: return kCGMouseButtonRight;
    case 2: return kCGMouseButtonCenter;
    default: return kCGMouseButtonLeft;
  }
}

static CGEventType mouse_event_type(int button, bool down, bool dragged) {
  if (dragged) {
    switch (button) {
      case 1: return kCGEventRightMouseDragged;
      case 2: return kCGEventOtherMouseDragged;
      default: return kCGEventLeftMouseDragged;
    }
  }
  if (down) {
    switch (button) {
      case 1: return kCGEventRightMouseDown;
      case 2: return kCGEventOtherMouseDown;
      default: return kCGEventLeftMouseDown;
    }
  }
  switch (button) {
    case 1: return kCGEventRightMouseUp;
    case 2: return kCGEventOtherMouseUp;
    default: return kCGEventLeftMouseUp;
  }
}

static int post_mouse(CGEventType type, double x, double y, int button, int clickCount) {
  CGEventRef event = CGEventCreateMouseEvent(NULL, type, CGPointMake(x, y), mouse_button(button));
  if (event == NULL) return -1;
  if (clickCount > 0) CGEventSetIntegerValueField(event, kCGMouseEventClickState, clickCount);
  if (g_held_flags != 0) CGEventSetFlags(event, g_held_flags);
  CGEventPost(kCGHIDEventTap, event);
  CFRelease(event);
  return 0;
}

int mincode_computer_mouse_move(double x, double y) {
  return post_mouse(kCGEventMouseMoved, x, y, 0, 0);
}

int mincode_computer_mouse_click(double x, double y, int button, int count) {
  if (count < 1 || count > 3) return -2;
  for (int click = 1; click <= count; click++) {
    int rc = post_mouse(mouse_event_type(button, true, false), x, y, button, click);
    if (rc != 0) return rc;
    usleep(30000);
    rc = post_mouse(mouse_event_type(button, false, false), x, y, button, click);
    if (rc != 0) return rc;
    if (click < count) usleep(50000);
  }
  return 0;
}

int mincode_computer_mouse_down(double x, double y, int button) {
  return post_mouse(mouse_event_type(button, true, false), x, y, button, 1);
}

int mincode_computer_mouse_drag(double x, double y, int button) {
  return post_mouse(mouse_event_type(button, false, true), x, y, button, 1);
}

int mincode_computer_mouse_up(double x, double y, int button) {
  return post_mouse(mouse_event_type(button, false, false), x, y, button, 1);
}

int mincode_computer_scroll(double deltaX, double deltaY) {
  int32_t horizontal = (int32_t)llround(-deltaX);
  int32_t vertical = (int32_t)llround(-deltaY);
  CGEventRef event = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitPixel, 2,
                                                   vertical, horizontal);
  if (event == NULL) return -1;
  if (g_held_flags != 0) CGEventSetFlags(event, g_held_flags);
  CGEventPost(kCGHIDEventTap, event);
  CFRelease(event);
  return 0;
}

int mincode_computer_type_text(const char *text) {
  if (text == NULL) return -1;
  @autoreleasepool {
    NSString *value = [NSString stringWithUTF8String:text];
    if (value == nil) return -2;
    const NSUInteger chunkSize = 20;
    for (NSUInteger offset = 0; offset < value.length;) {
      NSUInteger length = MIN(chunkSize, value.length - offset);
      if (offset + length < value.length && length > 1) {
        unichar last = [value characterAtIndex:offset + length - 1];
        unichar next = [value characterAtIndex:offset + length];
        if (CFStringIsSurrogateHighCharacter(last) && CFStringIsSurrogateLowCharacter(next)) {
          length--;
        }
      }
      unichar buffer[20];
      [value getCharacters:buffer range:NSMakeRange(offset, length)];
      CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0, true);
      CGEventRef up = CGEventCreateKeyboardEvent(NULL, 0, false);
      if (down == NULL || up == NULL) {
        if (down != NULL) CFRelease(down);
        if (up != NULL) CFRelease(up);
        return -3;
      }
      CGEventKeyboardSetUnicodeString(down, length, buffer);
      CGEventKeyboardSetUnicodeString(up, length, buffer);
      CGEventPost(kCGHIDEventTap, down);
      CGEventPost(kCGHIDEventTap, up);
      CFRelease(down);
      CFRelease(up);
      offset += length;
    }
    return 0;
  }
}

typedef struct {
  CGKeyCode code;
  CGEventFlags modifierFlag;
  bool shift;
} mincode_key;

static bool key_lookup(NSString *rawKey, mincode_key *out) {
  if (rawKey == nil || out == NULL) return false;
  NSString *key = rawKey.uppercaseString;
  static NSDictionary<NSString *, NSNumber *> *keys = nil;
  static NSDictionary<NSString *, NSNumber *> *shiftedKeys = nil;
  static NSDictionary<NSString *, NSArray<NSNumber *> *> *modifierKeys = nil;
  static dispatch_once_t once;
  dispatch_once(&once, ^{
    keys = @{
      @"A": @(kVK_ANSI_A), @"B": @(kVK_ANSI_B), @"C": @(kVK_ANSI_C),
      @"D": @(kVK_ANSI_D), @"E": @(kVK_ANSI_E), @"F": @(kVK_ANSI_F),
      @"G": @(kVK_ANSI_G), @"H": @(kVK_ANSI_H), @"I": @(kVK_ANSI_I),
      @"J": @(kVK_ANSI_J), @"K": @(kVK_ANSI_K), @"L": @(kVK_ANSI_L),
      @"M": @(kVK_ANSI_M), @"N": @(kVK_ANSI_N), @"O": @(kVK_ANSI_O),
      @"P": @(kVK_ANSI_P), @"Q": @(kVK_ANSI_Q), @"R": @(kVK_ANSI_R),
      @"S": @(kVK_ANSI_S), @"T": @(kVK_ANSI_T), @"U": @(kVK_ANSI_U),
      @"V": @(kVK_ANSI_V), @"W": @(kVK_ANSI_W), @"X": @(kVK_ANSI_X),
      @"Y": @(kVK_ANSI_Y), @"Z": @(kVK_ANSI_Z),
      @"0": @(kVK_ANSI_0), @"1": @(kVK_ANSI_1), @"2": @(kVK_ANSI_2),
      @"3": @(kVK_ANSI_3), @"4": @(kVK_ANSI_4), @"5": @(kVK_ANSI_5),
      @"6": @(kVK_ANSI_6), @"7": @(kVK_ANSI_7), @"8": @(kVK_ANSI_8),
      @"9": @(kVK_ANSI_9),
      @"ENTER": @(kVK_Return), @"RETURN": @(kVK_Return),
      @"TAB": @(kVK_Tab), @"SPACE": @(kVK_Space), @" ": @(kVK_Space),
      @"ESC": @(kVK_Escape), @"ESCAPE": @(kVK_Escape),
      @"BACKSPACE": @(kVK_Delete), @"DELETE": @(kVK_ForwardDelete),
      @"INSERT": @(kVK_Help), @"HELP": @(kVK_Help),
      @"CAPSLOCK": @(kVK_CapsLock),
      @"UP": @(kVK_UpArrow), @"ARROWUP": @(kVK_UpArrow),
      @"DOWN": @(kVK_DownArrow), @"ARROWDOWN": @(kVK_DownArrow),
      @"LEFT": @(kVK_LeftArrow), @"ARROWLEFT": @(kVK_LeftArrow),
      @"RIGHT": @(kVK_RightArrow), @"ARROWRIGHT": @(kVK_RightArrow),
      @"HOME": @(kVK_Home), @"END": @(kVK_End),
      @"PAGEUP": @(kVK_PageUp), @"PAGEDOWN": @(kVK_PageDown),
      @"F1": @(kVK_F1), @"F2": @(kVK_F2), @"F3": @(kVK_F3),
      @"F4": @(kVK_F4), @"F5": @(kVK_F5), @"F6": @(kVK_F6),
      @"F7": @(kVK_F7), @"F8": @(kVK_F8), @"F9": @(kVK_F9),
      @"F10": @(kVK_F10), @"F11": @(kVK_F11), @"F12": @(kVK_F12),
      @"F13": @(kVK_F13), @"F14": @(kVK_F14), @"F15": @(kVK_F15),
      @"F16": @(kVK_F16), @"F17": @(kVK_F17), @"F18": @(kVK_F18),
      @"F19": @(kVK_F19), @"F20": @(kVK_F20),
      @"NUMPAD0": @(kVK_ANSI_Keypad0), @"NUMPAD1": @(kVK_ANSI_Keypad1),
      @"NUMPAD2": @(kVK_ANSI_Keypad2), @"NUMPAD3": @(kVK_ANSI_Keypad3),
      @"NUMPAD4": @(kVK_ANSI_Keypad4), @"NUMPAD5": @(kVK_ANSI_Keypad5),
      @"NUMPAD6": @(kVK_ANSI_Keypad6), @"NUMPAD7": @(kVK_ANSI_Keypad7),
      @"NUMPAD8": @(kVK_ANSI_Keypad8), @"NUMPAD9": @(kVK_ANSI_Keypad9),
      @"-": @(kVK_ANSI_Minus), @"=": @(kVK_ANSI_Equal),
      @"[": @(kVK_ANSI_LeftBracket), @"]": @(kVK_ANSI_RightBracket),
      @"\\": @(kVK_ANSI_Backslash), @";": @(kVK_ANSI_Semicolon),
      @"'": @(kVK_ANSI_Quote), @",": @(kVK_ANSI_Comma),
      @".": @(kVK_ANSI_Period), @"/": @(kVK_ANSI_Slash),
      @"`": @(kVK_ANSI_Grave)
    };
    // US layout characters that require Shift.
    shiftedKeys = @{
      @"+": @(kVK_ANSI_Equal), @"PLUS": @(kVK_ANSI_Equal), @"_": @(kVK_ANSI_Minus),
      @"!": @(kVK_ANSI_1), @"@": @(kVK_ANSI_2), @"#": @(kVK_ANSI_3),
      @"$": @(kVK_ANSI_4), @"%": @(kVK_ANSI_5), @"^": @(kVK_ANSI_6),
      @"&": @(kVK_ANSI_7), @"*": @(kVK_ANSI_8), @"(": @(kVK_ANSI_9),
      @")": @(kVK_ANSI_0), @"{": @(kVK_ANSI_LeftBracket), @"}": @(kVK_ANSI_RightBracket),
      @"|": @(kVK_ANSI_Backslash), @":": @(kVK_ANSI_Semicolon), @"\"": @(kVK_ANSI_Quote),
      @"<": @(kVK_ANSI_Comma), @">": @(kVK_ANSI_Period), @"?": @(kVK_ANSI_Slash),
      @"~": @(kVK_ANSI_Grave)
    };
    NSNumber *shift = @(kCGEventFlagMaskShift);
    NSNumber *control = @(kCGEventFlagMaskControl);
    NSNumber *option = @(kCGEventFlagMaskAlternate);
    NSNumber *command = @(kCGEventFlagMaskCommand);
    NSNumber *fn = @(kCGEventFlagMaskSecondaryFn);
    modifierKeys = @{
      @"SHIFT": @[@(kVK_Shift), shift], @"RSHIFT": @[@(kVK_RightShift), shift],
      @"CTRL": @[@(kVK_Control), control], @"CONTROL": @[@(kVK_Control), control],
      @"RCTRL": @[@(kVK_RightControl), control],
      @"ALT": @[@(kVK_Option), option], @"OPTION": @[@(kVK_Option), option],
      @"RALT": @[@(kVK_RightOption), option], @"ROPTION": @[@(kVK_RightOption), option],
      @"META": @[@(kVK_Command), command], @"CMD": @[@(kVK_Command), command],
      @"COMMAND": @[@(kVK_Command), command], @"SUPER": @[@(kVK_Command), command],
      @"WIN": @[@(kVK_Command), command], @"WINDOWS": @[@(kVK_Command), command],
      @"RCMD": @[@(kVK_RightCommand), command], @"RMETA": @[@(kVK_RightCommand), command],
      @"FN": @[@(kVK_Function), fn]
    };
  });
  NSArray<NSNumber *> *modifier = modifierKeys[key];
  if (modifier != nil) {
    out->code = (CGKeyCode)modifier[0].unsignedShortValue;
    out->modifierFlag = (CGEventFlags)modifier[1].unsignedLongLongValue;
    out->shift = false;
    return true;
  }
  NSNumber *value = keys[key];
  bool shift = false;
  if (value == nil) {
    value = shiftedKeys[key];
    shift = value != nil;
  }
  if (value == nil) return false;
  out->code = (CGKeyCode)value.unsignedShortValue;
  out->modifierFlag = 0;
  out->shift = shift;
  return true;
}

static CGEventFlags modifier_flags(uint32_t modifiers) {
  CGEventFlags flags = 0;
  if ((modifiers & 1u) != 0) flags |= kCGEventFlagMaskControl;
  if ((modifiers & 2u) != 0) flags |= kCGEventFlagMaskShift;
  if ((modifiers & 4u) != 0) flags |= kCGEventFlagMaskAlternate;
  if ((modifiers & 8u) != 0) flags |= kCGEventFlagMaskCommand;
  return flags;
}

static int post_key(CGKeyCode code, bool down, CGEventFlags flags) {
  CGEventRef event = CGEventCreateKeyboardEvent(NULL, code, down);
  if (event == NULL) return -3;
  CGEventSetFlags(event, flags);
  CGEventPost(kCGHIDEventTap, event);
  CFRelease(event);
  return 0;
}

int mincode_computer_key_event(const char *key, bool down) {
  if (key == NULL) return -1;
  @autoreleasepool {
    mincode_key info;
    if (!key_lookup([NSString stringWithUTF8String:key], &info)) return -2;
    if (info.modifierFlag != 0) {
      if (down) g_held_flags |= info.modifierFlag;
      else g_held_flags &= ~info.modifierFlag;
      return post_key(info.code, down, g_held_flags);
    }
    CGEventFlags flags = g_held_flags;
    if (info.shift) flags |= kCGEventFlagMaskShift;
    return post_key(info.code, down, flags);
  }
}

int mincode_computer_press_key(const char *key, uint32_t modifiers) {
  if (key == NULL) return -1;
  @autoreleasepool {
    mincode_key info;
    if (!key_lookup([NSString stringWithUTF8String:key], &info)) return -2;
    static const struct { uint32_t bit; CGKeyCode code; CGEventFlags flag; } modifierTable[] = {
      {1u, kVK_Control, kCGEventFlagMaskControl},
      {2u, kVK_Shift, kCGEventFlagMaskShift},
      {4u, kVK_Option, kCGEventFlagMaskAlternate},
      {8u, kVK_Command, kCGEventFlagMaskCommand},
    };
    CGEventFlags requested = modifier_flags(modifiers);
    CGEventFlags flags = g_held_flags;
    int pressed[4];
    int pressedCount = 0;
    int result = 0;
    for (int index = 0; index < 4 && result == 0; index++) {
      if ((modifiers & modifierTable[index].bit) == 0 || (flags & modifierTable[index].flag) != 0) continue;
      flags |= modifierTable[index].flag;
      result = post_key(modifierTable[index].code, true, flags);
      if (result == 0) pressed[pressedCount++] = index;
    }
    if (result == 0) {
      CGEventFlags keyFlags = flags | requested;
      if (info.shift) keyFlags |= kCGEventFlagMaskShift;
      result = post_key(info.code, true, keyFlags);
      if (result == 0) result = post_key(info.code, false, keyFlags);
    }
    for (int index = pressedCount - 1; index >= 0; index--) {
      flags &= ~modifierTable[pressed[index]].flag;
      int upResult = post_key(modifierTable[pressed[index]].code, false, flags);
      if (result == 0) result = upResult;
    }
    return result;
  }
}

static bool mincode_application_name_matches(NSURL *applicationURL, NSString *requestedName) {
  NSString *requested = requestedName.stringByDeletingPathExtension.lowercaseString;
  NSString *fileName = applicationURL.lastPathComponent.stringByDeletingPathExtension.lowercaseString;
  if ([fileName isEqualToString:requested]) return true;

  NSBundle *bundle = [NSBundle bundleWithURL:applicationURL];
  if (bundle == nil) return false;
  NSDictionary *localizedInfo = bundle.localizedInfoDictionary ?: @{};
  NSDictionary *info = bundle.infoDictionary ?: @{};
  for (NSString *key in @[@"CFBundleDisplayName", @"CFBundleName"]) {
    NSString *value = localizedInfo[key] ?: info[key];
    if ([value isKindOfClass:[NSString class]] && [value.lowercaseString isEqualToString:requested]) {
      return true;
    }
  }
  return false;
}

static NSURL *mincode_find_application(NSString *application) {
  NSWorkspace *workspace = [NSWorkspace sharedWorkspace];
  NSURL *applicationURL = [workspace URLForApplicationWithBundleIdentifier:application];
  if (applicationURL != nil) return applicationURL;

  for (NSRunningApplication *runningApplication in workspace.runningApplications) {
    if ([runningApplication.bundleIdentifier isEqualToString:application] ||
        [runningApplication.localizedName.lowercaseString isEqualToString:application.lowercaseString]) {
      return runningApplication.bundleURL;
    }
  }

  NSString *homeApplications = [NSHomeDirectory() stringByAppendingPathComponent:@"Applications"];
  NSArray<NSString *> *rootPaths = @[
    @"/Applications",
    @"/System/Applications",
    @"/System/Library/CoreServices",
    homeApplications
  ];
  NSFileManager *fileManager = [NSFileManager defaultManager];
  NSArray<NSURLResourceKey> *resourceKeys = @[NSURLIsDirectoryKey];
  for (NSString *rootPath in rootPaths) {
    NSURL *rootURL = [NSURL fileURLWithPath:rootPath isDirectory:YES];
    for (NSString *candidateName in @[application, [application stringByAppendingPathExtension:@"app"]]) {
      NSURL *directURL = [rootURL URLByAppendingPathComponent:candidateName];
      if ([fileManager fileExistsAtPath:directURL.path] && mincode_application_name_matches(directURL, application)) {
        return directURL;
      }
    }

    NSDirectoryEnumerator<NSURL *> *enumerator = [fileManager
        enumeratorAtURL:rootURL
        includingPropertiesForKeys:resourceKeys
        options:(NSDirectoryEnumerationSkipsHiddenFiles | NSDirectoryEnumerationSkipsPackageDescendants)
        errorHandler:^BOOL(__unused NSURL *url, __unused NSError *error) {
          return YES;
        }];
    for (NSURL *candidateURL in enumerator) {
      if (![candidateURL.pathExtension.lowercaseString isEqualToString:@"app"]) continue;
      if (mincode_application_name_matches(candidateURL, application)) return candidateURL;
    }
  }
  return nil;
}

int mincode_computer_open_app(const char *rawApplication) {
  if (rawApplication == NULL) return -1;
  @autoreleasepool {
    NSString *application = [NSString stringWithUTF8String:rawApplication];
    if (application == nil || application.length == 0) return -2;

    NSURL *applicationURL = mincode_find_application(application);
    if (applicationURL == nil) return -3;

    NSWorkspaceOpenConfiguration *configuration = [NSWorkspaceOpenConfiguration configuration];
    configuration.activates = YES;
    configuration.addsToRecentItems = NO;
    __block NSError *openError = nil;
    dispatch_semaphore_t completed = dispatch_semaphore_create(0);
    [[NSWorkspace sharedWorkspace] openApplicationAtURL:applicationURL
                                         configuration:configuration
                                     completionHandler:^(__unused NSRunningApplication *runningApplication, NSError *error) {
                                       openError = error;
                                       dispatch_semaphore_signal(completed);
                                     }];
    if (dispatch_semaphore_wait(completed, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC)) != 0) {
      return -4;
    }
    return openError == nil ? 0 : -5;
  }
}

int mincode_computer_open_url(const char *rawURL, const char *rawBrowser) {
  if (rawURL == NULL || rawBrowser == NULL) return -1;
  @autoreleasepool {
    NSString *urlString = [NSString stringWithUTF8String:rawURL];
    NSString *browser = [NSString stringWithUTF8String:rawBrowser];
    NSURL *url = urlString == nil ? nil : [NSURL URLWithString:urlString];
    if (url == nil || browser == nil) return -2;

    NSWorkspace *workspace = [NSWorkspace sharedWorkspace];
    if ([browser isEqualToString:@"default"]) {
      return [workspace openURL:url] ? 0 : -3;
    }

    NSDictionary<NSString *, NSString *> *bundleIdentifiers = @{
      @"chrome": @"com.google.Chrome",
      @"safari": @"com.apple.Safari",
      @"firefox": @"org.mozilla.firefox",
      @"edge": @"com.microsoft.edgemac"
    };
    NSString *bundleIdentifier = bundleIdentifiers[browser];
    if (bundleIdentifier == nil) return -4;
    NSURL *applicationURL = [workspace URLForApplicationWithBundleIdentifier:bundleIdentifier];
    if (applicationURL == nil) return -5;

    NSWorkspaceOpenConfiguration *configuration = [NSWorkspaceOpenConfiguration configuration];
    configuration.activates = YES;
    __block NSError *openError = nil;
    dispatch_semaphore_t completed = dispatch_semaphore_create(0);
    [workspace openURLs:@[url]
        withApplicationAtURL:applicationURL
               configuration:configuration
           completionHandler:^(__unused NSRunningApplication *application, NSError *error) {
             openError = error;
             dispatch_semaphore_signal(completed);
           }];
    if (dispatch_semaphore_wait(completed, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC)) != 0) {
      return -6;
    }
    return openError == nil ? 0 : -7;
  }
}
