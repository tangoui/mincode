//go:build darwin

#import "overlay_darwin.h"
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>
#import <dispatch/dispatch.h>

static const void *kMincodeBrowserKey = &kMincodeBrowserKey;
static NSString *const kDefaultTabID = @"browser:default";

// Implemented in Go (overlay_darwin.go). Called on the main thread; they only
// take the Go dialogs lock and never wait for the main thread.
extern void goBrowserDialogOpened(char *tabID, char *dialogID, char *kind,
                                  char *message, char *defaultText, char *origin);
extern void goBrowserDialogsCancelled(char *tabID);
// Returns the new tab id for a page-opened window (prefixed kPopupTabIDPrefix),
// or a refusal reason. The caller frees it. Only takes the Go popup-allowance
// lock and emits an event.
extern char *goBrowserAdmitPopup(char *openerTabID, char *url);
// Records a real mouse/key event on a tab's web view (opens its popup allowance).
extern void goBrowserUserInput(char *tabID);
extern void goBrowserPopupClosed(char *tabID);
// Reports a tab's file chooser opening (count 0) or closing with count files.
// Only takes the Go file-chooser lock and emits an event.
extern void goBrowserFileChooser(char *tabID, bool open, int count);
// Whether the tab may show a chooser now (an AI click or real input in the last
// second); uses up that allowance. Only takes the Go file-chooser lock.
extern bool goBrowserAdmitFileChooser(char *tabID);
// Downloads (downloads.go). Each only takes the Go downloads lock and emits an event.
// Records a pending download; returns its id and stores the sanitized file name
// in *filename (the caller frees both).
extern char *goBrowserDownloadStarted(char *tabID, char *origin, char *suggested, char **filename);
// state "" keeps the current state; total < 0 means unknown.
extern void goBrowserDownloadUpdated(char *downloadID, char *state, long long bytes, long long total, char *errMsg);
// Keeps the chosen destination in Go (for Show in Finder); it is never reported.
extern void goBrowserDownloadDestination(char *downloadID, char *path);
// Whether the tab may show a save panel now (an AI click or real input in the
// last second); uses up that allowance.
extern bool goBrowserAdmitDownload(char *tabID);
// Whether a page-started navigation (mainFrame or a subframe) may proceed.
extern bool goBrowserNavigationAllowed(char *url, bool mainFrame);
// Whether a link the page marked as a download may start one.
extern bool goBrowserDownloadURLAllowed(char *url);
// Whether a running download may follow a server redirect to url.
extern bool goBrowserDownloadRedirectAllowed(char *url);

static NSString *const kPopupTabIDPrefix = @"browser:popup:";
// Page-opened tabs alive across all overlays; matches maxPopupTabs in popups.go.
static const NSUInteger kMaxPopupTabs = 10;
static NSUInteger popup_tab_count = 0; // main thread only

// ---- Page dialog handlers ----
// WebKit hands each alert/confirm/prompt a completion handler that must run
// exactly once. They live in one process-wide table guarded by its own lock, so
// Go can answer from any thread (and without a window pointer) without waiting
// for the main thread: whoever removes the entry first is the only caller.

typedef void (^MincodeDialogReply)(BOOL accept, NSString *text);

@interface MincodeDialogEntry : NSObject
@property(nonatomic, copy) NSString *tabID;
@property(nonatomic, copy) MincodeDialogReply reply;
@end

@implementation MincodeDialogEntry
@end

static NSMutableDictionary<NSString *, MincodeDialogEntry *> *dialog_entries(void) {
  static NSMutableDictionary *entries;
  static dispatch_once_t once;
  dispatch_once(&once, ^{ entries = [NSMutableDictionary dictionary]; });
  return entries;
}

static void register_dialog(NSString *dialogID, NSString *tabID, MincodeDialogReply reply) {
  MincodeDialogEntry *entry = [[MincodeDialogEntry alloc] init];
  entry.tabID = tabID;
  entry.reply = reply;
  NSMutableDictionary *entries = dialog_entries();
  @synchronized(entries) { entries[dialogID] = entry; }
}

// Removes and returns the entry, or nil when it was already answered or cancelled.
static MincodeDialogEntry *take_dialog(NSString *dialogID, NSString *tabID) {
  NSMutableDictionary<NSString *, MincodeDialogEntry *> *entries = dialog_entries();
  @synchronized(entries) {
    MincodeDialogEntry *entry = entries[dialogID];
    if (entry == nil || (tabID.length > 0 && ![entry.tabID isEqualToString:tabID])) return nil;
    [entries removeObjectForKey:dialogID];
    return entry;
  }
}

static NSArray<MincodeDialogEntry *> *take_dialogs_for_tab(NSString *tabID) {
  NSMutableDictionary<NSString *, MincodeDialogEntry *> *entries = dialog_entries();
  NSMutableArray<MincodeDialogEntry *> *taken = [NSMutableArray array];
  @synchronized(entries) {
    for (NSString *dialogID in entries.allKeys) {
      MincodeDialogEntry *entry = entries[dialogID];
      if ([entry.tabID isEqualToString:tabID]) {
        [taken addObject:entry];
        [entries removeObjectForKey:dialogID];
      }
    }
  }
  return taken;
}

// Cancels the tab's remaining dialogs (alert: OK) and tells Go. Main thread only.
static void cancel_dialogs_for_tab(NSString *tabID) {
  if (tabID.length == 0) return;
  for (MincodeDialogEntry *entry in take_dialogs_for_tab(tabID)) entry.reply(NO, nil);
  goBrowserDialogsCancelled((char *)tabID.UTF8String);
}

// protocol://host[:port]; local and opaque origins keep only the protocol.
static NSString *dialog_origin(WKFrameInfo *frame) {
  WKSecurityOrigin *origin = frame.securityOrigin;
  NSString *protocol = origin.protocol.lowercaseString;
  if (protocol.length == 0) return @"";
  NSString *host = origin.host;
  if (host.length == 0 || [protocol isEqualToString:@"file"]) return [protocol stringByAppendingString:@"://"];
  if ([host containsString:@":"] && ![host hasPrefix:@"["]) host = [NSString stringWithFormat:@"[%@]", host];
  if (origin.port == 0) return [NSString stringWithFormat:@"%@://%@", protocol, host];
  return [NSString stringWithFormat:@"%@://%@:%ld", protocol, host, (long)origin.port];
}

@class MincodeBrowserOverlay;
static WKWebView *create_tab_webview(MincodeBrowserOverlay *overlay, NSView *content, NSString *tabID,
                                     NSRect frame, WKWebViewConfiguration *configuration,
                                     NSString *userAgent);
static void destroy_tab_webview(MincodeBrowserOverlay *overlay, NSString *tabID);

// A tab's web view. It reports real mouse and key events to Go: WKWebView runs
// every evaluateJavaScript with a user gesture, so WebKit cannot tell a real
// click from a page abusing an injected script, and only these events (or an AI
// click) may let a page open a window.
@interface MincodeTabWebView : WKWebView
@property(nonatomic, copy) NSString *mincodeTabID;
// The address Go asked this tab to load (already validated in Go). Its
// navigation action is the host's, not the page's; cleared by the next
// top-level navigation action.
@property(nonatomic, copy) NSURL *mincodeHostLoad;
// Origin of the frame that started the latest navigation ("" for host loads);
// a response that turns into a download is attributed to it.
@property(nonatomic, copy) NSString *mincodeNavigationOrigin;
// Whether the current top-level navigation is a host load.
@property(nonatomic) BOOL mincodeHostNavigation;
// A download authorization taken from an AI click or real input when the page
// started a navigation (systemUptime deadline, 0 = none). A response that turns
// into a download, however late, inherits it; it ends when a page commits,
// when a download uses it, or after kDownloadAuthorizationTTL.
@property(nonatomic) NSTimeInterval mincodeDownloadAuthorizedUntil;
@end

static const NSTimeInterval kDownloadAuthorizationTTL = 60;

static void authorize_download(WKWebView *webView) {
  if ([webView isKindOfClass:MincodeTabWebView.class])
    ((MincodeTabWebView *)webView).mincodeDownloadAuthorizedUntil = NSProcessInfo.processInfo.systemUptime + kDownloadAuthorizationTTL;
}

// Uses up the web view's download authorization; YES when it was still valid.
static BOOL take_download_authorization(WKWebView *webView) {
  if (![webView isKindOfClass:MincodeTabWebView.class]) return NO;
  MincodeTabWebView *tab = (MincodeTabWebView *)webView;
  BOOL valid = tab.mincodeDownloadAuthorizedUntil > NSProcessInfo.processInfo.systemUptime;
  tab.mincodeDownloadAuthorizedUntil = 0;
  return valid;
}

@implementation MincodeTabWebView
- (void)noteUserInput {
  if (self.mincodeTabID.length > 0) goBrowserUserInput((char *)self.mincodeTabID.UTF8String);
}
- (void)mouseDown:(NSEvent *)event { [self noteUserInput]; [super mouseDown:event]; }
- (void)mouseUp:(NSEvent *)event { [self noteUserInput]; [super mouseUp:event]; }
- (void)rightMouseDown:(NSEvent *)event { [self noteUserInput]; [super rightMouseDown:event]; }
- (void)otherMouseDown:(NSEvent *)event { [self noteUserInput]; [super otherMouseDown:event]; }
- (void)otherMouseUp:(NSEvent *)event { [self noteUserInput]; [super otherMouseUp:event]; }
- (void)keyDown:(NSEvent *)event { [self noteUserInput]; [super keyDown:event]; }
@end

static void record_popup_refusal(MincodeBrowserOverlay *overlay, WKWebView *opener, NSString *reason);
static void record_file_chooser_error(MincodeBrowserOverlay *overlay, WKWebView *webView, NSString *reason);

// ---- File choosers ----
// <input type=file> asks the UI delegate for files. The user always picks them
// in an NSOpenPanel sheet on the host window; the page gets the URLs from
// WebKit and Go only learns how many were chosen. One chooser per tab, and its
// reply runs exactly once (main thread only).
@interface MincodeFileChooser : NSObject
@property(nonatomic, strong) NSOpenPanel *panel;
@property(nonatomic, copy) void (^reply)(NSArray<NSURL *> *URLs);
@end

@implementation MincodeFileChooser
@end

// ---- Downloads ----
// A page download (WKDownload) the overlay tracks from didBecomeDownload until
// it finishes. Go assigns goID when WebKit asks for a destination; the user then
// confirms it in an NSSavePanel sheet. Main thread only, except the KVO
// callback, which only hops to the main thread.
@interface MincodeDownloadEntry : NSObject
@property(nonatomic, strong) WKDownload *download;
@property(nonatomic, copy) NSString *tabID;
@property(nonatomic, copy) NSString *origin;
@property(nonatomic, copy) NSString *goID;
@property(nonatomic, copy) NSString *filename;
@property(nonatomic) long long expectedBytes;
@property(nonatomic, strong) NSSavePanel *panel;
// WebKit's destination handler while the save panel is open; runs once.
@property(nonatomic, copy) void (^decide)(NSURL *destination);
@property(nonatomic, copy) void (^progressChanged)(void);
@property(nonatomic) BOOL observing;
@property(nonatomic) BOOL finished;
// Started by a navigation an AI click or real input authorized.
@property(nonatomic) BOOL authorized;
@property(nonatomic) BOOL progressScheduled;
@property(nonatomic) NSTimeInterval lastProgress;
// Replacing an existing file: WebKit writes to tempURL, which replaces
// finalURL only once the download has finished.
@property(nonatomic, strong) NSURL *finalURL;
@property(nonatomic, strong) NSURL *tempURL;
@end

@implementation MincodeDownloadEntry
- (void)observeValueForKeyPath:(NSString *)keyPath ofObject:(id)object change:(NSDictionary *)change
                       context:(void *)context {
  void (^changed)(void) = self.progressChanged;
  if (changed != nil) dispatch_async(dispatch_get_main_queue(), changed);
}
@end

// Progress events per download are limited to four a second.
static const NSTimeInterval kDownloadProgressInterval = 0.25;

@interface MincodeBrowserOverlay : NSObject <WKNavigationDelegate, WKUIDelegate, WKDownloadDelegate>
@property(nonatomic, strong) NSMutableDictionary<NSString *, WKWebView *> *webViews;
@property(nonatomic, strong) NSMapTable<WKWebView *, NSString *> *tabIDs;
@property(nonatomic, strong) NSMutableDictionary<NSString *, NSMutableArray *> *navigationErrors;
// Tabs whose web view a page opened (target=_blank, window.open).
@property(nonatomic, strong) NSMutableSet<NSString *> *popupTabIDs;
// Open file choosers by tab id.
@property(nonatomic, strong) NSMutableDictionary<NSString *, MincodeFileChooser *> *fileChoosers;
// Downloads in flight, across tabs.
@property(nonatomic, strong) NSMutableArray<MincodeDownloadEntry *> *downloads;
@property(nonatomic, copy) NSString *activeTabID;
@property(nonatomic, weak) NSWindow *hostWindow;
// NSWindowWillCloseNotification observer: closing the host window tears the overlay down.
@property(nonatomic, strong) id windowCloseObserver;
- (void)finishFileChooser:(MincodeFileChooser *)chooser tabID:(NSString *)tabID URLs:(NSArray<NSURL *> *)URLs;
- (void)cancelFileChooserForTab:(NSString *)tabID;
- (void)finishSavePanel:(MincodeDownloadEntry *)entry URL:(NSURL *)URL;
- (void)cancelDownloadsForTab:(NSString *)tabID;
@end

static void record_navigation_refusal(MincodeBrowserOverlay *overlay, WKWebView *webView, NSURL *url, NSString *what);
static void present_save_panel(MincodeBrowserOverlay *overlay, NSWindow *host, MincodeDownloadEntry *entry);

// Host loads compare equal when only a trailing slash differs.
static BOOL same_address(NSURL *a, NSURL *b) {
  NSString *left = a.absoluteString ?: @"", *right = b.absoluteString ?: @"";
  if ([left isEqualToString:right]) return YES;
  if ([left hasSuffix:@"/"] != [right hasSuffix:@"/"]) {
    return [[left stringByAppendingString:@"/"] isEqualToString:right] || [[right stringByAppendingString:@"/"] isEqualToString:left];
  }
  return NO;
}

static BOOL response_is_attachment(NSURLResponse *response) {
  if (![response isKindOfClass:NSHTTPURLResponse.class]) return NO;
  NSString *disposition = [(NSHTTPURLResponse *)response valueForHTTPHeaderField:@"Content-Disposition"];
  NSString *kind = [[disposition componentsSeparatedByString:@";"].firstObject
      stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceAndNewlineCharacterSet].lowercaseString;
  return [kind isEqualToString:@"attachment"];
}

// Names who asks, like the file chooser: the page origin (scheme, host, port)
// and the sanitized file name, never a path or query.
static NSString *download_message(NSString *origin, NSString *filename) {
  NSRange separator = [origin rangeOfString:@"://"];
  BOOL hasHost = separator.location != NSNotFound && NSMaxRange(separator) < origin.length;
  if (!hasHost) return [NSString stringWithFormat:@"A web page wants to download %@", filename];
  return [NSString stringWithFormat:@"%@ wants to download %@", origin, filename];
}

@implementation MincodeBrowserOverlay
- (instancetype)init {
  self = [super init];
  if (self) {
    _webViews = [NSMutableDictionary dictionary];
    _tabIDs = [NSMapTable weakToStrongObjectsMapTable];
    _navigationErrors = [NSMutableDictionary dictionary];
    _popupTabIDs = [NSMutableSet set];
    _fileChoosers = [NSMutableDictionary dictionary];
    _downloads = [NSMutableArray array];
    _activeTabID = kDefaultTabID;
  }
  return self;
}


- (void)appendNavigationError:(NSError *)error webView:(WKWebView *)webView {
  NSString *tabID = [self.tabIDs objectForKey:webView] ?: self.activeTabID ?: kDefaultTabID;
  NSMutableArray *items = self.navigationErrors[tabID];
  if (items == nil) {
    items = [NSMutableArray array];
    self.navigationErrors[tabID] = items;
  }
  // A failed provisional load leaves webView.URL on the previous page; name the
  // address that failed, without user:password. Refusals we record ourselves
  // carry no failing URL, so they never name the refused address.
  NSURL *failing = [error.userInfo[NSURLErrorFailingURLErrorKey] isKindOfClass:NSURL.class] ? error.userInfo[NSURLErrorFailingURLErrorKey] : nil;
  NSURLComponents *failingParts = failing ? [NSURLComponents componentsWithURL:failing resolvingAgainstBaseURL:NO] : nil;
  failingParts.user = nil;
  failingParts.password = nil;
  NSString *url = failingParts.URL.absoluteString ?: webView.URL.absoluteString ?: @"";
  [items addObject:@{
    @"timestamp": @((long long)(NSDate.date.timeIntervalSince1970 * 1000)),
    @"url": url,
    @"code": @(error.code),
    @"domain": error.domain ?: @"",
    @"message": error.localizedDescription ?: @"Navigation failed"
  }];
  if (items.count > 100) [items removeObjectsInRange:NSMakeRange(0, items.count - 100)];
}

- (void)webView:(WKWebView *)webView didFailNavigation:(WKNavigation *)navigation
    withError:(NSError *)error {
  [self appendNavigationError:error webView:webView];
}

- (void)webView:(WKWebView *)webView didFailProvisionalNavigation:(WKNavigation *)navigation
    withError:(NSError *)error {
  // "Frame load interrupted": the response became a download or a policy
  // refusal the overlay already recorded.
  if ([error.domain isEqualToString:WKErrorDomain] || [error.domain isEqualToString:@"WebKitErrorDomain"]) {
    if (error.code == 102) return;
  }
  [self appendNavigationError:error webView:webView];
}

// Page-started navigations get the same address rules as AI and user ones
// (validateNavigationURL in Go); frames may also show data: and blob:
// documents. Refusals are recorded with the scheme only. Links the page marks
// as downloads become downloads, which the save panel gates.
- (void)webView:(WKWebView *)webView decidePolicyForNavigationAction:(WKNavigationAction *)action
    decisionHandler:(void (^)(WKNavigationActionPolicy))decisionHandler {
  NSURL *url = action.request.URL;
  NSString *address = url.absoluteString ?: @"";
  // WKDownload.originatingFrame needs macOS 15.2, so the origin of the frame
  // that started a top-level navigation is noted here for response downloads.
  WKFrameInfo *source = action.sourceFrame;
  if (action.targetFrame.isMainFrame && [webView isKindOfClass:MincodeTabWebView.class]) {
    ((MincodeTabWebView *)webView).mincodeNavigationOrigin = source != nil ? dialog_origin(source) : @"";
  }
  NSString *tabID = [self.tabIDs objectForKey:webView] ?: @"";
  if (action.shouldPerformDownload) {
    if (goBrowserDownloadURLAllowed((char *)address.UTF8String)) {
      // The click allowance is taken now: the server may answer much later.
      if (goBrowserAdmitDownload((char *)tabID.UTF8String)) authorize_download(webView);
      decisionHandler(WKNavigationActionPolicyDownload);
    } else {
      decisionHandler(WKNavigationActionPolicyCancel);
      record_navigation_refusal(self, webView, url, @"download");
    }
    return;
  }
  // A new window: createWebView admits it (popups.go) and its own web view
  // then decides on the load.
  if (action.targetFrame == nil) { decisionHandler(WKNavigationActionPolicyAllow); return; }
  BOOL mainFrame = action.targetFrame.isMainFrame;
  if (mainFrame && [webView isKindOfClass:MincodeTabWebView.class]) {
    MincodeTabWebView *tab = (MincodeTabWebView *)webView;
    NSURL *hostLoad = tab.mincodeHostLoad;
    tab.mincodeHostLoad = nil;
    if (hostLoad != nil && same_address(hostLoad, url)) {
      tab.mincodeNavigationOrigin = @"";
      tab.mincodeHostNavigation = YES;
      decisionHandler(WKNavigationActionPolicyAllow);
      return;
    }
    // Server redirects of a host load come here too; they stay host navigations.
    if (action.navigationType != WKNavigationTypeOther) tab.mincodeHostNavigation = NO;
  }
  // Reload and back/forward revisit pages these rules already admitted.
  if (mainFrame && (action.navigationType == WKNavigationTypeReload || action.navigationType == WKNavigationTypeBackForward)) {
    decisionHandler(WKNavigationActionPolicyAllow);
    return;
  }
  if (goBrowserNavigationAllowed((char *)address.UTF8String, mainFrame)) {
    // A top-level page navigation started by a click takes the download
    // allowance, so a slow or redirected attachment still gets its panel.
    if (mainFrame && goBrowserAdmitDownload((char *)tabID.UTF8String)) authorize_download(webView);
    decisionHandler(WKNavigationActionPolicyAllow);
    return;
  }
  decisionHandler(WKNavigationActionPolicyCancel);
  record_navigation_refusal(self, webView, url, mainFrame ? @"page navigation" : @"frame navigation");
}

- (void)webView:(WKWebView *)webView decidePolicyForNavigationResponse:(WKNavigationResponse *)navigationResponse
    decisionHandler:(void (^)(WKNavigationResponsePolicy))decisionHandler {
  NSURL *url = navigationResponse.response.URL;
  BOOL isData = [url.scheme.lowercaseString isEqualToString:@"data"];
  if (!navigationResponse.canShowMIMEType || response_is_attachment(navigationResponse.response)) {
    if (!isData) {
      decisionHandler(WKNavigationResponsePolicyDownload);
      return;
    }
    // WebKit does not turn a data: response into a WKDownload, so the overlay
    // starts one itself; it keeps the navigation's authorization and origin.
    decisionHandler(WKNavigationResponsePolicyCancel);
    BOOL authorized = take_download_authorization(webView);
    NSString *origin = [webView isKindOfClass:MincodeTabWebView.class] ? ((MincodeTabWebView *)webView).mincodeNavigationOrigin : nil;
    __weak MincodeBrowserOverlay *weakSelf = self;
    __weak WKWebView *weakWebView = webView;
    [webView startDownloadUsingRequest:[NSURLRequest requestWithURL:url] completionHandler:^(WKDownload *download) {
      MincodeBrowserOverlay *overlay = weakSelf;
      WKWebView *view = weakWebView;
      if (overlay == nil || view == nil) { [download cancel:nil]; return; }
      [overlay adoptDownload:download webView:view origin:origin ?: @""];
      [overlay entryForDownload:download].authorized = authorized;
    }];
    return;
  }
  // As in Chrome, a page may save a data: URL but not show one as a top-level document.
  BOOL hostNavigation = [webView isKindOfClass:MincodeTabWebView.class] && ((MincodeTabWebView *)webView).mincodeHostNavigation;
  if (navigationResponse.isForMainFrame && !hostNavigation && isData) {
    decisionHandler(WKNavigationResponsePolicyCancel);
    record_navigation_refusal(self, webView, url, @"page navigation");
    return;
  }
  decisionHandler(WKNavigationResponsePolicyAllow);
}

// A committed page ends the authorization its navigation carried.
- (void)webView:(WKWebView *)webView didCommitNavigation:(WKNavigation *)navigation {
  if ([webView isKindOfClass:MincodeTabWebView.class]) ((MincodeTabWebView *)webView).mincodeDownloadAuthorizedUntil = 0;
}

- (void)webView:(WKWebView *)webView navigationAction:(WKNavigationAction *)navigationAction
    didBecomeDownload:(WKDownload *)download {
  WKFrameInfo *source = navigationAction.sourceFrame;
  [self adoptDownload:download webView:webView origin:source != nil ? dialog_origin(source) : @""];
  [self entryForDownload:download].authorized = take_download_authorization(webView);
}

- (void)webView:(WKWebView *)webView navigationResponse:(WKNavigationResponse *)navigationResponse
    didBecomeDownload:(WKDownload *)download {
  NSString *origin = [webView isKindOfClass:MincodeTabWebView.class] ? ((MincodeTabWebView *)webView).mincodeNavigationOrigin : nil;
  [self adoptDownload:download webView:webView origin:origin ?: @""];
  [self entryForDownload:download].authorized = take_download_authorization(webView);
}

- (MincodeDownloadEntry *)entryForDownload:(WKDownload *)download {
  for (MincodeDownloadEntry *entry in self.downloads) {
    if (entry.download == download) return entry;
  }
  return nil;
}

- (void)adoptDownload:(WKDownload *)download webView:(WKWebView *)webView origin:(NSString *)origin {
  NSString *tabID = [self.tabIDs objectForKey:webView];
  if (tabID.length == 0) { [download cancel:nil]; return; }
  MincodeDownloadEntry *entry = [[MincodeDownloadEntry alloc] init];
  entry.download = download;
  entry.tabID = tabID;
  entry.origin = origin;
  entry.expectedBytes = -1;
  download.delegate = self;
  __weak MincodeBrowserOverlay *weakSelf = self;
  __weak MincodeDownloadEntry *weakEntry = entry;
  entry.progressChanged = ^{ [weakSelf downloadProgressed:weakEntry]; };
  [download.progress addObserver:entry forKeyPath:@"completedUnitCount" options:0 context:NULL];
  entry.observing = YES;
  [self.downloads addObject:entry];
}

static long long download_total(MincodeDownloadEntry *entry) {
  int64_t total = entry.download.progress.totalUnitCount;
  return total > 0 ? total : entry.expectedBytes;
}

// Ends tracking and reports the final state once. Main thread only.
- (void)finishDownload:(MincodeDownloadEntry *)entry state:(NSString *)state error:(NSString *)error {
  if (entry == nil || entry.finished) return;
  entry.finished = YES;
  [self.downloads removeObject:entry];
  if (entry.tempURL != nil && ![state isEqualToString:@"completed"]) {
    // The file the user chose to replace stays as it was.
    [NSFileManager.defaultManager removeItemAtURL:entry.tempURL error:nil];
  }
  if (entry.observing) {
    [entry.download.progress removeObserver:entry forKeyPath:@"completedUnitCount"];
    entry.observing = NO;
  }
  if (entry.goID.length == 0) return;
  goBrowserDownloadUpdated((char *)entry.goID.UTF8String, (char *)state.UTF8String,
                           entry.download.progress.completedUnitCount, download_total(entry),
                           (char *)(error ?: @"").UTF8String);
}

// Reports progress at most every kDownloadProgressInterval, with a trailing
// update so the last change before a pause is not lost.
- (void)downloadProgressed:(MincodeDownloadEntry *)entry {
  if (entry == nil || entry.finished || entry.goID.length == 0 || entry.decide != nil || entry.progressScheduled) return;
  NSTimeInterval wait = entry.lastProgress + kDownloadProgressInterval - NSProcessInfo.processInfo.systemUptime;
  entry.progressScheduled = YES;
  dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(MAX(wait, 0) * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
    entry.progressScheduled = NO;
    if (entry.finished) return;
    entry.lastProgress = NSProcessInfo.processInfo.systemUptime;
    goBrowserDownloadUpdated((char *)entry.goID.UTF8String, "", entry.download.progress.completedUnitCount,
                             download_total(entry), "");
  });
}

// WebKit asks where to save: the user decides in a save panel sheet, which only
// an AI click or real input in the last second may open. Every other download
// is cancelled and recorded as no_user_action.
- (void)download:(WKDownload *)download decideDestinationUsingResponse:(NSURLResponse *)response
    suggestedFilename:(NSString *)suggestedFilename completionHandler:(void (^)(NSURL *destination))completionHandler {
  MincodeDownloadEntry *entry = [self entryForDownload:download];
  if (entry == nil) { completionHandler(nil); return; }
  char *filename = NULL;
  char *downloadID = goBrowserDownloadStarted((char *)entry.tabID.UTF8String, (char *)entry.origin.UTF8String,
                                              (char *)(suggestedFilename ?: @"").UTF8String, &filename);
  entry.goID = downloadID ? [NSString stringWithUTF8String:downloadID] : @"";
  entry.filename = filename ? [NSString stringWithUTF8String:filename] : @"download";
  free(downloadID);
  free(filename);
  entry.expectedBytes = response.expectedContentLength;
  // The navigation's authorization first; otherwise (e.g. a download started
  // without a navigation) a click or input in the last second.
  if (!entry.authorized && !goBrowserAdmitDownload((char *)entry.tabID.UTF8String)) {
    [self finishDownload:entry state:@"cancelled" error:@"no_user_action"];
    completionHandler(nil);
    return;
  }
  NSWindow *host = self.hostWindow;
  if (host == nil || host.attachedSheet != nil) {
    [self finishDownload:entry state:@"cancelled" error:@"save_panel_unavailable"];
    completionHandler(nil);
    return;
  }
  NSSavePanel *panel = [NSSavePanel savePanel];
  panel.directoryURL = [NSFileManager.defaultManager URLsForDirectory:NSDownloadsDirectory inDomains:NSUserDomainMask].firstObject;
  panel.nameFieldStringValue = entry.filename;
  panel.message = download_message(entry.origin, entry.filename);
  panel.canCreateDirectories = YES;
  entry.panel = panel;
  entry.decide = completionHandler;
  present_save_panel(self, host, entry);
}

// Runs WebKit's destination handler once: nil cancels the download.
- (void)finishSavePanel:(MincodeDownloadEntry *)entry URL:(NSURL *)URL {
  if (entry == nil || entry.decide == nil) return;
  void (^decide)(NSURL *) = entry.decide;
  entry.decide = nil;
  if (URL == nil) {
    [self finishDownload:entry state:@"cancelled" error:@"user_cancelled"];
    decide(nil);
    return;
  }
  // WKDownload never overwrites; the panel already asked the user to replace an
  // existing file. The download goes to a hidden file next to it, which replaces
  // the original only when it completes, so a failed or cancelled download
  // leaves the user's file as it was. Anything but a plain file is left alone.
  NSURL *target = URL;
  NSDictionary *attributes = [NSFileManager.defaultManager attributesOfItemAtPath:URL.path error:nil];
  if (attributes != nil) {
    if (![attributes.fileType isEqualToString:NSFileTypeRegular]) {
      [self finishDownload:entry state:@"failed" error:@"destination_exists"];
      decide(nil);
      return;
    }
    NSString *tempName = [NSString stringWithFormat:@".%@.mincode-download-%@", URL.lastPathComponent,
                                                    NSUUID.UUID.UUIDString];
    entry.finalURL = URL;
    entry.tempURL = [URL.URLByDeletingLastPathComponent URLByAppendingPathComponent:tempName isDirectory:NO];
    target = entry.tempURL;
  }
  goBrowserDownloadDestination((char *)entry.goID.UTF8String, (char *)URL.path.UTF8String);
  entry.lastProgress = NSProcessInfo.processInfo.systemUptime;
  goBrowserDownloadUpdated((char *)entry.goID.UTF8String, "saving", 0, entry.expectedBytes, "");
  decide(target);
}

// Redirects of a running download get the same address check as navigations.
- (void)download:(WKDownload *)download willPerformHTTPRedirection:(NSHTTPURLResponse *)response
      newRequest:(NSURLRequest *)request decisionHandler:(void (^)(WKDownloadRedirectPolicy))decisionHandler {
  NSURL *url = request.URL;
  if (goBrowserDownloadRedirectAllowed((char *)(url.absoluteString ?: @"").UTF8String)) {
    decisionHandler(WKDownloadRedirectPolicyAllow);
    return;
  }
  decisionHandler(WKDownloadRedirectPolicyCancel);
  MincodeDownloadEntry *entry = [self entryForDownload:download];
  if (entry == nil) return;
  WKWebView *webView = self.webViews[entry.tabID];
  if (webView != nil) record_navigation_refusal(self, webView, url, @"download redirect");
  if (entry.goID.length == 0) {
    // Refused before WebKit asked for a destination: record it under the
    // original request's file name.
    char *filename = NULL;
    NSString *suggested = download.originalRequest.URL.lastPathComponent ?: @"";
    char *downloadID = goBrowserDownloadStarted((char *)entry.tabID.UTF8String, (char *)entry.origin.UTF8String,
                                                (char *)suggested.UTF8String, &filename);
    entry.goID = downloadID ? [NSString stringWithUTF8String:downloadID] : @"";
    free(downloadID);
    free(filename);
  }
  [self finishDownload:entry state:@"failed" error:@"redirect_blocked"];
}

- (void)downloadDidFinish:(WKDownload *)download {
  MincodeDownloadEntry *entry = [self entryForDownload:download];
  if (entry.tempURL != nil && entry.finalURL != nil) {
    NSError *error = nil;
    if (![NSFileManager.defaultManager replaceItemAtURL:entry.finalURL withItemAtURL:entry.tempURL
                                         backupItemName:nil options:0 resultingItemURL:NULL error:&error]) {
      // finishDownload removes the temporary file; the original stays.
      [self finishDownload:entry state:@"failed" error:@"replace_failed"];
      return;
    }
    entry.tempURL = nil;
  }
  [self finishDownload:entry state:@"completed" error:nil];
}

- (void)download:(WKDownload *)download didFailWithError:(NSError *)error resumeData:(NSData *)resumeData {
  MincodeDownloadEntry *entry = [self entryForDownload:download];
  if ([error.domain isEqualToString:NSURLErrorDomain] && error.code == NSURLErrorCancelled) {
    [self finishDownload:entry state:@"cancelled" error:@"cancelled"];
    return;
  }
  // Only the error's domain and code: its description may name the file or address.
  [self finishDownload:entry state:@"failed"
                 error:[NSString stringWithFormat:@"download_failed %@ %ld", error.domain ?: @"", (long)error.code]];
}

// Cancels the tab's downloads and takes down their save panels (tab closed).
- (void)cancelDownloadsForTab:(NSString *)tabID {
  for (MincodeDownloadEntry *entry in [self.downloads copy]) {
    if (![entry.tabID isEqualToString:tabID]) continue;
    void (^decide)(NSURL *) = entry.decide;
    entry.decide = nil;
    NSSavePanel *panel = entry.panel;
    [self finishDownload:entry state:@"cancelled" error:@"tab_closed"];
    if (decide != nil) decide(nil);
    else [entry.download cancel:nil];
    if (panel.sheetParent != nil) [panel.sheetParent endSheet:panel returnCode:NSModalResponseCancel];
  }
}

- (void)webViewWebContentProcessDidTerminate:(WKWebView *)webView {
  NSString *tabID = [self.tabIDs objectForKey:webView];
  if (tabID != nil) {
    cancel_dialogs_for_tab(tabID);
    [self cancelFileChooserForTab:tabID];
  }
}

// Replies to the tab's chooser once (nil URLs = cancelled) and tells Go. A
// stale entry (an earlier chooser) is ignored.
- (void)finishFileChooser:(MincodeFileChooser *)chooser tabID:(NSString *)tabID URLs:(NSArray<NSURL *> *)URLs {
  if (chooser == nil || self.fileChoosers[tabID] != chooser) return;
  [self.fileChoosers removeObjectForKey:tabID];
  chooser.reply(URLs.count > 0 ? URLs : nil);
  goBrowserFileChooser((char *)tabID.UTF8String, false, (int)URLs.count);
}

// Cancels the tab's chooser and takes its sheet down (tab closed, process gone).
- (void)cancelFileChooserForTab:(NSString *)tabID {
  MincodeFileChooser *chooser = self.fileChoosers[tabID];
  if (chooser == nil) return;
  [self finishFileChooser:chooser tabID:tabID URLs:nil];
  NSWindow *sheetParent = chooser.panel.sheetParent;
  if (sheetParent != nil) [sheetParent endSheet:chooser.panel returnCode:NSModalResponseCancel];
}

// Names who asks, so the user never mistakes a page's request for the AI's:
// scheme, host and port of the frame's origin only, never a path or query.
static NSString *file_chooser_message(WKFrameInfo *frame, WKOpenPanelParameters *parameters) {
  NSString *what = parameters.allowsDirectories ? @"a folder" : (parameters.allowsMultipleSelection ? @"files" : @"a file");
  NSString *host = frame.securityOrigin.host ?: @"";
  if (host.length == 0) return [NSString stringWithFormat:@"A web page wants you to choose %@", what];
  return [NSString stringWithFormat:@"%@ wants you to choose %@", dialog_origin(frame), what];
}

#ifdef MINCODE_BROWSER_E2E
// Desktop smoke only. MINCODE_BROWSER_E2E is defined solely by e2e_hooks_e2e.go
// (`-tags e2e`), so production builds contain neither these variables nor the
// automatic answers below: there every chooser and save panel waits for the user.
static NSString *e2e_save_directory;
static NSString *e2e_upload_file;

void mincode_browser_e2e_configure(const char *saveDirectory, const char *uploadFile) {
  e2e_save_directory = saveDirectory ? [NSString stringWithUTF8String:saveDirectory] : nil;
  e2e_upload_file = uploadFile ? [NSString stringWithUTF8String:uploadFile] : nil;
}

// The hooks answer a little later, as a user would, after the gates already
// admitted the panel and Go saw it open.
static const int64_t kE2EPanelAnswerDelay = 300 * NSEC_PER_MSEC;
#endif

// Shows the panel as a sheet on the host window. The sheet's completion is the
// only path that finishes the chooser with the user's choice; only an e2e build
// (MINCODE_BROWSER_E2E) picks the fixture file instead.
static void present_file_panel(MincodeBrowserOverlay *overlay, NSWindow *host, NSString *tabID,
                               MincodeFileChooser *chooser) {
  __weak MincodeBrowserOverlay *weakOverlay = overlay;
  __weak MincodeFileChooser *weakChooser = chooser;
#ifdef MINCODE_BROWSER_E2E
  if (e2e_upload_file.length > 0) {
    NSArray<NSURL *> *picked = @[[NSURL fileURLWithPath:e2e_upload_file]];
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, kE2EPanelAnswerDelay), dispatch_get_main_queue(), ^{
      [weakOverlay finishFileChooser:weakChooser tabID:tabID URLs:picked];
    });
    return;
  }
#endif
  NSOpenPanel *panel = chooser.panel;
  [panel beginSheetModalForWindow:host completionHandler:^(NSModalResponse response) {
    MincodeFileChooser *current = weakChooser;
    [weakOverlay finishFileChooser:current tabID:tabID URLs:response == NSModalResponseOK ? panel.URLs : nil];
  }];
}

- (void)webView:(WKWebView *)webView runOpenPanelWithParameters:(WKOpenPanelParameters *)parameters
    initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(NSArray<NSURL *> *URLs))completionHandler {
  NSString *tabID = [self.tabIDs objectForKey:webView];
  if (tabID.length == 0) { completionHandler(nil); return; }
  NSWindow *host = self.hostWindow;
  if (host == nil) {
    completionHandler(nil);
    record_file_chooser_error(self, webView, @"Blocked a file chooser: the browser has no window to show it in");
    return;
  }
  if (self.fileChoosers[tabID] != nil || host.attachedSheet != nil) {
    completionHandler(nil);
    record_file_chooser_error(self, webView, @"Blocked a file chooser: another chooser or sheet is already open");
    return;
  }
  // Injected scripts carry a user gesture, so WebKit would also let a page open
  // window-modal choosers from inside an AI read; only an AI click or real input admits one.
  if (!goBrowserAdmitFileChooser((char *)tabID.UTF8String)) {
    completionHandler(nil);
    record_file_chooser_error(self, webView, @"Blocked a file chooser: it was not opened by a click or key press");
    return;
  }
  MincodeFileChooser *chooser = [[MincodeFileChooser alloc] init];
  chooser.reply = completionHandler;
  NSOpenPanel *panel = [NSOpenPanel openPanel];
  panel.allowsMultipleSelection = parameters.allowsMultipleSelection;
  panel.canChooseDirectories = parameters.allowsDirectories;
  panel.canChooseFiles = !parameters.allowsDirectories;
  panel.resolvesAliases = YES;
  panel.message = file_chooser_message(frame, parameters);
  chooser.panel = panel;
  self.fileChoosers[tabID] = chooser;
  goBrowserFileChooser((char *)tabID.UTF8String, true, 0);
  present_file_panel(self, host, tabID, chooser);
}

// Registers the handler, then reports the dialog to Go, which shows it to the
// user and the AI. A web view we do not track gets an immediate cancel.
- (void)openDialogOfKind:(NSString *)kind webView:(WKWebView *)webView message:(NSString *)message
    defaultText:(NSString *)defaultText frame:(WKFrameInfo *)frame reply:(MincodeDialogReply)reply {
  NSString *tabID = [self.tabIDs objectForKey:webView];
  if (tabID.length == 0) { reply(NO, nil); return; }
  NSString *dialogID = [@"dialog-" stringByAppendingString:NSUUID.UUID.UUIDString.lowercaseString];
  register_dialog(dialogID, tabID, reply);
  goBrowserDialogOpened((char *)tabID.UTF8String, (char *)dialogID.UTF8String, (char *)kind.UTF8String,
                        (char *)(message ?: @"").UTF8String, (char *)(defaultText ?: @"").UTF8String,
                        (char *)dialog_origin(frame).UTF8String);
}

- (void)webView:(WKWebView *)webView runJavaScriptAlertPanelWithMessage:(NSString *)message
    initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(void))completionHandler {
  [self openDialogOfKind:@"alert" webView:webView message:message defaultText:nil frame:frame
                   reply:^(BOOL accept, NSString *text) { completionHandler(); }];
}

- (void)webView:(WKWebView *)webView runJavaScriptConfirmPanelWithMessage:(NSString *)message
    initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(BOOL result))completionHandler {
  [self openDialogOfKind:@"confirm" webView:webView message:message defaultText:nil frame:frame
                   reply:^(BOOL accept, NSString *text) { completionHandler(accept); }];
}

- (void)webView:(WKWebView *)webView runJavaScriptTextInputPanelWithPrompt:(NSString *)prompt
    defaultText:(NSString *)defaultText initiatedByFrame:(WKFrameInfo *)frame
    completionHandler:(void (^)(NSString *result))completionHandler {
  [self openDialogOfKind:@"prompt" webView:webView message:prompt defaultText:defaultText frame:frame
                   reply:^(BOOL accept, NSString *text) { completionHandler(accept ? (text ?: @"") : nil); }];
}

// target=_blank and window.open: the child web view must be built from the
// configuration WebKit passes so window.opener and postMessage work. It becomes
// a hidden tab until the front end activates it.
- (WKWebView *)webView:(WKWebView *)webView createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration
    forNavigationAction:(WKNavigationAction *)navigationAction windowFeatures:(WKWindowFeatures *)windowFeatures {
  NSString *openerID = [self.tabIDs objectForKey:webView];
  NSView *content = self.hostWindow.contentView;
  if (openerID.length == 0 || content == nil) return nil;
  if (popup_tab_count >= kMaxPopupTabs) {
    record_popup_refusal(self, webView, @"Blocked a new window: too many tabs opened by pages are still open");
    return nil;
  }
  NSString *url = navigationAction.request.URL.absoluteString ?: @"";
  char *admitted = goBrowserAdmitPopup((char *)openerID.UTF8String, (char *)url.UTF8String);
  NSString *popupID = admitted ? [NSString stringWithUTF8String:admitted] : @"";
  free(admitted);
  if (![popupID hasPrefix:kPopupTabIDPrefix]) {
    record_popup_refusal(self, webView, popupID.length > 0 ? popupID : @"Blocked a new window");
    return nil;
  }
  WKWebView *popup = create_tab_webview(self, content, popupID, webView.frame, configuration, webView.customUserAgent);
  popup.hidden = YES;
  // A window opened by a click may be a download (target=_blank attachments).
  if (goBrowserAdmitDownload((char *)openerID.UTF8String)) authorize_download(popup);
  [self.popupTabIDs addObject:popupID];
  popup_tab_count++;
  return popup;
}

// window.close() from a page-opened window closes its tab.
- (void)webViewDidClose:(WKWebView *)webView {
  NSString *tabID = [self.tabIDs objectForKey:webView];
  if (tabID == nil || ![self.popupTabIDs containsObject:tabID]) return;
  goBrowserPopupClosed((char *)tabID.UTF8String);
  destroy_tab_webview(self, tabID);
}
@end

// The reason never contains the refused address, which may carry page data.
static void record_popup_refusal(MincodeBrowserOverlay *overlay, WKWebView *opener, NSString *reason) {
  [overlay appendNavigationError:[NSError errorWithDomain:@"MincodeBrowserPopup" code:1
                                                 userInfo:@{NSLocalizedDescriptionKey: reason}]
                         webView:opener];
}

// The reason names the scheme only, never the refused address.
static void record_navigation_refusal(MincodeBrowserOverlay *overlay, WKWebView *webView, NSURL *url, NSString *what) {
  NSString *scheme = url.scheme.lowercaseString ?: @"";
  NSCharacterSet *invalid = [NSCharacterSet characterSetWithCharactersInString:@"abcdefghijklmnopqrstuvwxyz0123456789+-."].invertedSet;
  if (scheme.length == 0 || scheme.length > 32 || [scheme rangeOfCharacterFromSet:invalid].location != NSNotFound) scheme = @"unknown";
  NSString *reason = [NSString stringWithFormat:@"Blocked a %@: the %@ address is not allowed", what, scheme];
  [overlay appendNavigationError:[NSError errorWithDomain:@"MincodeBrowserNavigation" code:1
                                                 userInfo:@{NSLocalizedDescriptionKey: reason}]
                         webView:webView];
}

// Shows the save panel as a sheet on the host window. The sheet's completion
// is the only path that finishes it with the user's choice; only an e2e build
// (MINCODE_BROWSER_E2E) saves into the smoke fixture directory instead.
static void present_save_panel(MincodeBrowserOverlay *overlay, NSWindow *host, MincodeDownloadEntry *entry) {
  __weak MincodeBrowserOverlay *weakOverlay = overlay;
  __weak MincodeDownloadEntry *weakEntry = entry;
#ifdef MINCODE_BROWSER_E2E
  if (e2e_save_directory.length > 0) {
    NSURL *destination = [[NSURL fileURLWithPath:e2e_save_directory isDirectory:YES]
        URLByAppendingPathComponent:entry.filename.lastPathComponent];
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, kE2EPanelAnswerDelay), dispatch_get_main_queue(), ^{
      [weakOverlay finishSavePanel:weakEntry URL:destination];
    });
    return;
  }
#endif
  NSSavePanel *panel = entry.panel;
  [panel beginSheetModalForWindow:host completionHandler:^(NSModalResponse response) {
    [weakOverlay finishSavePanel:weakEntry URL:response == NSModalResponseOK ? panel.URL : nil];
  }];
}

// Like popup refusals, the reason never contains a path or address.
static void record_file_chooser_error(MincodeBrowserOverlay *overlay, WKWebView *webView, NSString *reason) {
  [overlay appendNavigationError:[NSError errorWithDomain:@"MincodeBrowserFileChooser" code:1
                                                 userInfo:@{NSLocalizedDescriptionKey: reason}]
                         webView:webView];
}

static void run_on_main_sync(dispatch_block_t block) {
  if ([NSThread isMainThread]) block();
  else dispatch_sync(dispatch_get_main_queue(), block);
}

static MincodeBrowserOverlay *overlay_for(NSWindow *win) {
  if (win == nil) return nil;
  return objc_getAssociatedObject(win, kMincodeBrowserKey);
}

// Destroys every tab (popup slots, dialogs, choosers and downloads go with
// them) and detaches the overlay from its window. Main thread only.
static void teardown_overlay(NSWindow *win, MincodeBrowserOverlay *overlay) {
  if (overlay == nil) return;
  if (overlay.windowCloseObserver != nil) {
    [NSNotificationCenter.defaultCenter removeObserver:overlay.windowCloseObserver];
    overlay.windowCloseObserver = nil;
  }
  for (NSString *tabID in overlay.webViews.allKeys) destroy_tab_webview(overlay, tabID);
  if (win != nil && overlay_for(win) == overlay) {
    objc_setAssociatedObject(win, kMincodeBrowserKey, nil, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
  }
}

static MincodeBrowserOverlay *ensure_overlay(NSWindow *win) {
  MincodeBrowserOverlay *overlay = overlay_for(win);
  if (overlay == nil) {
    overlay = [[MincodeBrowserOverlay alloc] init];
    overlay.hostWindow = win;
    objc_setAssociatedObject(win, kMincodeBrowserKey, overlay, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
    // A window closed without mincode_browser_close still gives back its popup
    // slots of the global cap and answers its tabs' pending dialogs. The web
    // views keep the window (and so the overlay) alive, so dealloc would not.
    __weak MincodeBrowserOverlay *weakOverlay = overlay;
    __weak NSWindow *weakWin = win;
    overlay.windowCloseObserver = [NSNotificationCenter.defaultCenter
        addObserverForName:NSWindowWillCloseNotification
                    object:win
                     queue:NSOperationQueue.mainQueue
                usingBlock:^(NSNotification *note) {
                  teardown_overlay(weakWin, weakOverlay);
                }];
  }
  return overlay;
}

static NSRect to_appkit_rect(NSView *content, double x, double y, double w, double h) {
  CGFloat H = content.bounds.size.height;
  return NSMakeRect((CGFloat)x, H - (CGFloat)y - (CGFloat)h, (CGFloat)w, (CGFloat)h);
}

static NSString *nsstr(const char *s) { return s ? [NSString stringWithUTF8String:s] : nil; }
static NSString *tab_id(const char *s) {
  NSString *value = nsstr(s);
  return value.length > 0 ? value : kDefaultTabID;
}

static WKWebView *webview_for(MincodeBrowserOverlay *overlay, NSString *tabID) {
  if (overlay == nil) return nil;
  NSString *resolved = tabID.length > 0 ? tabID : overlay.activeTabID;
  WKWebView *webView = overlay.webViews[resolved ?: kDefaultTabID];
  // Only an unnamed caller falls back to the single tab: a named tab that was
  // closed must resolve to nothing, never to whichever tab is left.
  if (webView == nil && [resolved isEqualToString:kDefaultTabID] && overlay.webViews.count == 1) {
    webView = overlay.webViews.allValues.firstObject;
  }
  return webView;
}

static NSString *diagnostics_script(void) {
  return @"(() => {"
    "if (window.__mincodeDiagnostics) return;"
    "const limit=200;"
    "const state={console:[],network:[],errors:[]};"
    "const cleanUrl=(value)=>{try{const u=new URL(String(value),location.href);u.username='';u.password='';u.search='';u.hash='';return u.toString()}catch{return String(value).slice(0,500)}};"
    "const text=(value)=>{try{if(typeof value==='string')return value.slice(0,1000);return JSON.stringify(value).slice(0,1000)}catch{return String(value).slice(0,1000)}};"
    "const push=(list,item)=>{list.push(item);if(list.length>limit)list.splice(0,list.length-limit)};"
    "for(const level of ['log','info','warn','error','debug']){const original=console[level]?.bind(console);if(!original)continue;console[level]=(...args)=>{push(state.console,{timestamp:Date.now(),level,message:args.map(text).join(' ')});original(...args)}}"
    "addEventListener('error',(event)=>push(state.errors,{timestamp:Date.now(),type:'error',message:text(event.message||event.error),source:cleanUrl(event.filename||location.href),line:event.lineno||0,column:event.colno||0}),true);"
    "addEventListener('unhandledrejection',(event)=>push(state.errors,{timestamp:Date.now(),type:'unhandledrejection',message:text(event.reason)}));"
    "const nativeFetch=window.fetch?.bind(window);if(nativeFetch){window.fetch=async(input,init)=>{const started=performance.now();const method=String(init?.method||input?.method||'GET').toUpperCase();const url=cleanUrl(input?.url||input);try{const response=await nativeFetch(input,init);push(state.network,{timestamp:Date.now(),kind:'fetch',method,url,status:response.status,ok:response.ok,durationMs:Math.round(performance.now()-started)});return response}catch(error){push(state.network,{timestamp:Date.now(),kind:'fetch',method,url,status:0,ok:false,durationMs:Math.round(performance.now()-started),error:text(error)});throw error}}}"
    "const XHR=window.XMLHttpRequest;if(XHR){const open=XHR.prototype.open,send=XHR.prototype.send;XHR.prototype.open=function(method,url,...rest){this.__mincodeRequest={method:String(method||'GET').toUpperCase(),url:cleanUrl(url)};return open.call(this,method,url,...rest)};XHR.prototype.send=function(...args){const started=performance.now();const done=()=>push(state.network,{timestamp:Date.now(),kind:'xhr',method:this.__mincodeRequest?.method||'GET',url:this.__mincodeRequest?.url||'',status:this.status||0,ok:this.status>=200&&this.status<400,durationMs:Math.round(performance.now()-started)});this.addEventListener('loadend',done,{once:true});return send.apply(this,args)}}"
    "Object.defineProperty(window,'__mincodeDiagnostics',{value:state,enumerable:false,configurable:false,writable:false});"
  "})();";
}

// `inspectable` only exposes the page to Safari's Develop menu. WebKit's local
// inspector also requires developer extras, which WKWebView does not expose publicly.
static BOOL enable_developer_extras(WKPreferences *preferences) {
  SEL selector = NSSelectorFromString(@"_setDeveloperExtrasEnabled:");
  if (preferences == nil || ![preferences respondsToSelector:selector]) return NO;
  typedef void (*DeveloperExtrasSetter)(id, SEL, BOOL);
  DeveloperExtrasSetter setter = (DeveloperExtrasSetter)[preferences methodForSelector:selector];
  setter(preferences, selector, YES);
  return YES;
}

static WKWebViewConfiguration *browser_configuration(void) {
  WKWebViewConfiguration *configuration = [[WKWebViewConfiguration alloc] init];
  configuration.suppressesIncrementalRendering = NO;
  // macOS defaults this to YES. With NO WebKit refuses window.open outside a
  // user gesture; our own allowance (popups.go) then narrows "gesture" to real
  // input and AI clicks, since injected scripts also count as gestures.
  configuration.preferences.javaScriptCanOpenWindowsAutomatically = NO;
  enable_developer_extras(configuration.preferences);
  WKUserScript *script = [[WKUserScript alloc] initWithSource:diagnostics_script()
      injectionTime:WKUserScriptInjectionTimeAtDocumentStart
      forMainFrameOnly:YES];
  [configuration.userContentController addUserScript:script];
  return configuration;
}

// Creates and registers a tab's web view. Every web view the overlay hosts
// (mounted tabs and page-opened popups) is created here so it gets the
// navigation and UI delegates. A nil configuration means a fresh browser one; a
// popup passes the configuration WebKit handed to the UI delegate.
static WKWebView *create_tab_webview(MincodeBrowserOverlay *overlay, NSView *content, NSString *tabID,
                                     NSRect frame, WKWebViewConfiguration *configuration,
                                     NSString *userAgent) {
  MincodeTabWebView *webView = [[MincodeTabWebView alloc] initWithFrame:frame
                                                         configuration:configuration ?: browser_configuration()];
  webView.mincodeTabID = tabID;
  webView.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable | NSViewMinYMargin;
  webView.navigationDelegate = overlay;
  webView.UIDelegate = overlay;
  if (userAgent.length > 0) webView.customUserAgent = userAgent;
  if (@available(macOS 13.3, *)) webView.inspectable = YES;
  [content addSubview:webView positioned:NSWindowAbove relativeTo:nil];
  overlay.webViews[tabID] = webView;
  [overlay.tabIDs setObject:tabID forKey:webView];
  overlay.navigationErrors[tabID] = [NSMutableArray array];
  return webView;
}

// Tears down a tab's web view and cancels its pending dialogs. Main thread only.
static void destroy_tab_webview(MincodeBrowserOverlay *overlay, NSString *tabID) {
  WKWebView *webView = overlay.webViews[tabID];
  if (webView == nil) return;
  [webView stopLoading];
  webView.navigationDelegate = nil;
  webView.UIDelegate = nil;
  [webView removeFromSuperview];
  [overlay.tabIDs removeObjectForKey:webView];
  [overlay.webViews removeObjectForKey:tabID];
  [overlay.navigationErrors removeObjectForKey:tabID];
  if ([overlay.popupTabIDs containsObject:tabID]) {
    [overlay.popupTabIDs removeObject:tabID];
    if (popup_tab_count > 0) popup_tab_count--;
  }
  if ([overlay.activeTabID isEqualToString:tabID]) overlay.activeTabID = overlay.webViews.allKeys.firstObject ?: kDefaultTabID;
  cancel_dialogs_for_tab(tabID);
  [overlay cancelFileChooserForTab:tabID];
  [overlay cancelDownloadsForTab:tabID];
}

// Loads an address Go asked for (and validated) and marks it as the host's.
static void host_load(WKWebView *webView, NSURL *target) {
  if ([webView isKindOfClass:MincodeTabWebView.class]) ((MincodeTabWebView *)webView).mincodeHostLoad = target;
  [webView loadRequest:[NSURLRequest requestWithURL:target]];
}

static void show_only(MincodeBrowserOverlay *overlay, NSString *tabID) {
  overlay.activeTabID = tabID;
  [overlay.webViews enumerateKeysAndObjectsUsingBlock:^(NSString *key, WKWebView *webView, BOOL *stop) {
    webView.hidden = ![key isEqualToString:tabID];
  }];
}

int mincode_browser_mount(void *nsWindow, const char *tabId, const char *url,
                          const char *userAgent, double x, double y,
                          double width, double height) {
  if (nsWindow == NULL) return 1;
  __block int rc = 0;
  NSString *tabID = tab_id(tabId);
  NSString *urlStr = nsstr(url);
  NSString *uaStr = nsstr(userAgent);
  run_on_main_sync(^{
    NSWindow *win = (__bridge NSWindow *)nsWindow;
    NSView *content = win.contentView;
    if (content == nil) { rc = 2; return; }
    MincodeBrowserOverlay *overlay = ensure_overlay(win);
    WKWebView *webView = overlay.webViews[tabID];
    if (webView == nil) {
      webView = create_tab_webview(overlay, content, tabID, to_appkit_rect(content, x, y, width, height),
                                   nil, uaStr);
    } else {
      webView.frame = to_appkit_rect(content, x, y, width, height);
    }
    show_only(overlay, tabID);
    // A popup already loads what its page opened; mounting it only places it.
    if (urlStr.length > 0 && ![overlay.popupTabIDs containsObject:tabID]) {
      NSURL *target = [NSURL URLWithString:urlStr];
      if (target != nil && ![webView.URL.absoluteString isEqualToString:urlStr]) host_load(webView, target);
    }
  });
  return rc;
}

void mincode_browser_set_bounds(void *nsWindow, const char *tabId, double x,
                                double y, double width, double height,
                                bool visible) {
  if (nsWindow == NULL) return;
  NSString *tabID = tab_id(tabId);
  run_on_main_sync(^{
    NSWindow *win = (__bridge NSWindow *)nsWindow;
    MincodeBrowserOverlay *overlay = overlay_for(win);
    WKWebView *webView = webview_for(overlay, tabID);
    NSView *content = win.contentView;
    if (webView == nil || content == nil) return;
    if (!visible || width < 2 || height < 2) { webView.hidden = YES; return; }
    webView.frame = to_appkit_rect(content, x, y, width, height);
    show_only(overlay, tabID);
  });
}

void mincode_browser_hide(void *nsWindow) {
  if (nsWindow == NULL) return;
  run_on_main_sync(^{
    MincodeBrowserOverlay *overlay = overlay_for((__bridge NSWindow *)nsWindow);
    for (WKWebView *webView in overlay.webViews.allValues) webView.hidden = YES;
  });
}

void mincode_browser_close_tab(void *nsWindow, const char *tabId) {
  if (nsWindow == NULL) return;
  NSString *tabID = tab_id(tabId);
  run_on_main_sync(^{
    MincodeBrowserOverlay *overlay = overlay_for((__bridge NSWindow *)nsWindow);
    if (overlay.webViews[tabID] == nil) return;
    destroy_tab_webview(overlay, tabID);
  });
}

void mincode_browser_close(void *nsWindow) {
  if (nsWindow == NULL) return;
  run_on_main_sync(^{
    NSWindow *win = (__bridge NSWindow *)nsWindow;
    teardown_overlay(win, overlay_for(win));
  });
}

void mincode_browser_activate(void *nsWindow, const char *tabId) {
  if (nsWindow == NULL) return;
  NSString *tabID = tab_id(tabId);
  run_on_main_sync(^{
    MincodeBrowserOverlay *overlay = overlay_for((__bridge NSWindow *)nsWindow);
    if (overlay.webViews[tabID] != nil) show_only(overlay, tabID);
  });
}

void mincode_browser_navigate(void *nsWindow, const char *tabId, const char *url) {
  if (nsWindow == NULL) return;
  NSString *tabID = tab_id(tabId), *urlStr = nsstr(url);
  run_on_main_sync(^{
    WKWebView *webView = webview_for(overlay_for((__bridge NSWindow *)nsWindow), tabID);
    NSURL *target = urlStr.length > 0 ? [NSURL URLWithString:urlStr] : nil;
    if (webView != nil && target != nil) host_load(webView, target);
  });
}

void mincode_browser_reload(void *nsWindow, const char *tabId) {
  if (nsWindow == NULL) return;
  NSString *tabID = tab_id(tabId);
  run_on_main_sync(^{ [webview_for(overlay_for((__bridge NSWindow *)nsWindow), tabID) reload]; });
}

// Back/forward loads are admitted by decidePolicyForNavigationAction like reloads.
bool mincode_browser_go_history(void *nsWindow, const char *tabId, int delta) {
  if (nsWindow == NULL || delta == 0) return false;
  NSString *tabID = tab_id(tabId);
  __block bool navigated = false;
  run_on_main_sync(^{
    WKWebView *webView = webview_for(overlay_for((__bridge NSWindow *)nsWindow), tabID);
    if (webView == nil) return;
    if (delta < 0 && webView.canGoBack) {
      [webView goBack];
      navigated = true;
    } else if (delta > 0 && webView.canGoForward) {
      [webView goForward];
      navigated = true;
    }
  });
  return navigated;
}

void mincode_browser_set_zoom(void *nsWindow, const char *tabId, double zoom) {
  if (nsWindow == NULL) return;
  NSString *tabID = tab_id(tabId);
  run_on_main_sync(^{ webview_for(overlay_for((__bridge NSWindow *)nsWindow), tabID).pageZoom = (CGFloat)zoom; });
}

char *mincode_browser_get_url(void *nsWindow, const char *tabId) {
  if (nsWindow == NULL) return NULL;
  NSString *tabID = tab_id(tabId);
  __block char *out = NULL;
  run_on_main_sync(^{
    NSString *value = webview_for(overlay_for((__bridge NSWindow *)nsWindow), tabID).URL.absoluteString;
    if (value.length > 0) out = strdup(value.UTF8String);
  });
  return out;
}

static NSString *json_quote(NSString *value) {
  NSData *data = [NSJSONSerialization dataWithJSONObject:@[value ?: @""] options:0 error:nil];
  NSString *array = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
  return array.length >= 2 ? [array substringWithRange:NSMakeRange(1, array.length - 2)] : @"\"\"";
}

char *mincode_browser_eval(void *nsWindow, const char *tabId, const char *script, int timeoutMs) {
  if (nsWindow == NULL) return strdup("{\"ok\":false,\"error\":\"no window\"}");
  NSString *tabID = tab_id(tabId), *javascript = nsstr(script);
  if (javascript.length == 0) return strdup("{\"ok\":false,\"error\":\"empty script\"}");
  dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
  __block NSString *result = nil, *errorMessage = nil;
  dispatch_async(dispatch_get_main_queue(), ^{
    WKWebView *webView = webview_for(overlay_for((__bridge NSWindow *)nsWindow), tabID);
    if (webView == nil) { errorMessage = @"browser tab not mounted"; dispatch_semaphore_signal(semaphore); return; }
    [webView evaluateJavaScript:javascript completionHandler:^(id value, NSError *error) {
      if (error != nil) errorMessage = error.localizedDescription;
      else if (value == nil || value == NSNull.null) result = @"null";
      else if ([value isKindOfClass:NSString.class]) result = json_quote(value);
      else if ([value isKindOfClass:NSNumber.class]) result = [value stringValue];
      else {
        NSData *data = [NSJSONSerialization dataWithJSONObject:value options:0 error:nil];
        result = data ? [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding] : @"null";
      }
      dispatch_semaphore_signal(semaphore);
    }];
  });
  long timeout = timeoutMs > 0 ? timeoutMs : 3000;
  if (dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, timeout * NSEC_PER_MSEC)) != 0)
    return strdup("{\"ok\":false,\"error\":\"eval timeout\"}");
  NSString *payload = errorMessage
      ? [NSString stringWithFormat:@"{\"ok\":false,\"error\":%@}", json_quote(errorMessage)]
      : [NSString stringWithFormat:@"{\"ok\":true,\"value\":%@}", result ?: @"null"];
  return strdup(payload.UTF8String);
}

char *mincode_browser_capture(void *nsWindow, const char *tabId, int timeoutMs) {
  if (nsWindow == NULL) return strdup("{\"ok\":false,\"error\":\"no window\"}");
  NSString *tabID = tab_id(tabId);
  dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
  __block NSString *payload = nil;
  dispatch_async(dispatch_get_main_queue(), ^{
    WKWebView *webView = webview_for(overlay_for((__bridge NSWindow *)nsWindow), tabID);
    if (webView == nil) { payload = @"{\"ok\":false,\"error\":\"browser tab not mounted\"}"; dispatch_semaphore_signal(semaphore); return; }
    [webView takeSnapshotWithConfiguration:nil completionHandler:^(NSImage *image, NSError *error) {
      if (error != nil || image == nil) {
        payload = [NSString stringWithFormat:@"{\"ok\":false,\"error\":%@}", json_quote(error.localizedDescription ?: @"snapshot failed")];
      } else {
        NSData *tiff = image.TIFFRepresentation;
        NSBitmapImageRep *bitmap = tiff ? [NSBitmapImageRep imageRepWithData:tiff] : nil;
        NSData *png = [bitmap representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
        if (png == nil) payload = @"{\"ok\":false,\"error\":\"PNG encoding failed\"}";
        else payload = [NSString stringWithFormat:@"{\"ok\":true,\"mimeType\":\"image/png\",\"data\":%@,\"width\":%ld,\"height\":%ld}", json_quote([png base64EncodedStringWithOptions:0]), (long)bitmap.pixelsWide, (long)bitmap.pixelsHigh];
      }
      dispatch_semaphore_signal(semaphore);
    }];
  });
  long timeout = timeoutMs > 0 ? timeoutMs : 10000;
  if (dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, timeout * NSEC_PER_MSEC)) != 0)
    return strdup("{\"ok\":false,\"error\":\"snapshot timeout\"}");
  return strdup((payload ?: @"{\"ok\":false,\"error\":\"snapshot failed\"}").UTF8String);
}

char *mincode_browser_navigation_errors(void *nsWindow, const char *tabId) {
  if (nsWindow == NULL) return strdup("[]");
  NSString *tabID = tab_id(tabId);
  __block NSString *payload = @"[]";
  run_on_main_sync(^{
    MincodeBrowserOverlay *overlay = overlay_for((__bridge NSWindow *)nsWindow);
    NSArray *items = [overlay.navigationErrors[tabID] copy] ?: @[];
    NSData *data = [NSJSONSerialization dataWithJSONObject:items options:0 error:nil];
    if (data) payload = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
  });
  return strdup(payload.UTF8String);
}

int mincode_browser_toggle_devtools(void *nsWindow, const char *tabId) {
  if (nsWindow == NULL) return 1;
  NSString *tabID = tab_id(tabId);
  __block int result = 0;
  run_on_main_sync(^{
    WKWebView *webView = webview_for(overlay_for((__bridge NSWindow *)nsWindow), tabID);
    if (webView == nil) { result = 2; return; }
    if (@available(macOS 13.3, *)) webView.inspectable = YES;
    if (!enable_developer_extras(webView.configuration.preferences)) { result = 3; return; }

    SEL inspectorSelector = NSSelectorFromString(@"_inspector");
    SEL showSelector = NSSelectorFromString(@"show");
    if (![webView respondsToSelector:inspectorSelector]) { result = 4; return; }

#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Warc-performSelector-leaks"
    id inspector = [webView performSelector:inspectorSelector];
    if (inspector == nil || ![inspector respondsToSelector:showSelector]) {
      result = 5;
    } else {
      [inspector performSelector:showSelector];
    }
#pragma clang diagnostic pop
  });
  return result;
}

void mincode_browser_clear_data(void *nsWindow) {
  if (nsWindow == NULL) return;
  run_on_main_sync(^{
    [[WKWebsiteDataStore defaultDataStore] removeDataOfTypes:WKWebsiteDataStore.allWebsiteDataTypes
      modifiedSince:[NSDate dateWithTimeIntervalSince1970:0] completionHandler:^{}];
  });
}

bool mincode_browser_exists(void *nsWindow) {
  if (nsWindow == NULL) return false;
  __block bool exists = false;
  run_on_main_sync(^{ exists = overlay_for((__bridge NSWindow *)nsWindow).webViews.count > 0; });
  return exists;
}

int mincode_browser_screen_region(void *nsWindow, double x, double y, double width,
                                  double height, int *outX, int *outY, int *outW,
                                  int *outH) {
  if (nsWindow == NULL) return 1;
  __block int rc = 0;
  run_on_main_sync(^{
    NSWindow *win = (__bridge NSWindow *)nsWindow;
    NSView *content = win.contentView;
    NSScreen *screen = win.screen ?: NSScreen.mainScreen;
    if (content == nil || screen == nil) { rc = 2; return; }
    NSRect inContent = to_appkit_rect(content, x, y, width, height);
    NSRect inWindow = [content convertRect:inContent toView:nil];
    NSRect inScreen = [win convertRectToScreen:inWindow];
    CGFloat backing = screen.backingScaleFactor;
    CGFloat topLeftY = screen.frame.size.height - (inScreen.origin.y + inScreen.size.height);
    *outX = (int)llround(inScreen.origin.x * backing);
    *outY = (int)llround(topLeftY * backing);
    *outW = MAX(1, (int)llround(inScreen.size.width * backing));
    *outH = MAX(1, (int)llround(inScreen.size.height * backing));
  });
  return rc;
}

// Returns the tab id whose web view the overlay currently shows, or NULL when
// there is no overlay or the active id does not map to a mounted web view.
char *mincode_browser_active_tab_id(void *nsWindow) {
  if (nsWindow == NULL) return NULL;
  __block char *out = NULL;
  run_on_main_sync(^{
    MincodeBrowserOverlay *overlay = overlay_for((__bridge NSWindow *)nsWindow);
    NSString *active = overlay.activeTabID;
    if (active.length > 0 && overlay.webViews[active] != nil) out = strdup(active.UTF8String);
  });
  return out;
}

// Answers a page dialog. Never waits for the main thread: Go may call this while
// holding locks the main thread could need. The window is not needed because
// handlers live in a process-wide table. Returns 1 when the handler is gone.
int mincode_browser_complete_dialog(void *nsWindow, const char *tabId, const char *dialogId,
                                    bool accept, const char *text) {
  (void)nsWindow;
  NSString *dialogID = nsstr(dialogId);
  if (dialogID.length == 0) return 1;
  MincodeDialogEntry *entry = take_dialog(dialogID, nsstr(tabId));
  if (entry == nil) return 1;
  NSString *reply = nsstr(text);
  BOOL accepted = accept ? YES : NO;
  dispatch_async(dispatch_get_main_queue(), ^{ entry.reply(accepted, reply); });
  return 0;
}

// ---- Test hooks (overlay_darwin_testing.go) ----
static volatile int64_t test_dialog_calls = 0;

void mincode_browser_test_register_dialog(const char *tabId, const char *dialogId) {
  register_dialog(nsstr(dialogId), tab_id(tabId), ^(BOOL accept, NSString *text) {
    __atomic_add_fetch(&test_dialog_calls, 1, __ATOMIC_SEQ_CST);
  });
}

int mincode_browser_test_popup_count(void) {
  __block int count = 0;
  run_on_main_sync(^{
    count = (int)popup_tab_count;
  });
  return count;
}

int mincode_browser_test_dialog_calls(void) {
  return (int)__atomic_load_n(&test_dialog_calls, __ATOMIC_SEQ_CST);
}

// Sends a left mouse down/up to the tab's web view at (x, y) in its own
// top-left coordinates, through the same methods AppKit calls for a real click.
void mincode_browser_test_mouse_click(void *nsWindow, const char *tabId, double x, double y) {
  NSString *tabID = tab_id(tabId);
  run_on_main_sync(^{
    NSWindow *win = (__bridge NSWindow *)nsWindow;
    WKWebView *webView = webview_for(overlay_for(win), tabID);
    if (webView == nil) return;
    NSPoint inView = NSMakePoint(x, webView.isFlipped ? y : webView.bounds.size.height - y);
    NSPoint inWindow = [webView convertPoint:inView toView:nil];
    for (NSNumber *type in @[@(NSEventTypeLeftMouseDown), @(NSEventTypeLeftMouseUp)]) {
      NSEvent *event = [NSEvent mouseEventWithType:(NSEventType)type.unsignedIntegerValue location:inWindow
                                     modifierFlags:0 timestamp:NSProcessInfo.processInfo.systemUptime
                                      windowNumber:win.windowNumber context:nil eventNumber:0 clickCount:1 pressure:1];
      if (event.type == NSEventTypeLeftMouseDown) [webView mouseDown:event];
      else [webView mouseUp:event];
    }
  });
}

// Finishes the tab's open chooser as if the user picked path (NULL or "" =
// cancel) and takes the sheet down. Returns -1 when no chooser is open, else
// bit 0 = multiple files allowed, bit 1 = directories allowed.
int mincode_browser_test_finish_file_chooser(void *nsWindow, const char *tabId, const char *path) {
  NSString *tabID = tab_id(tabId), *filePath = nsstr(path);
  __block int flags = -1;
  run_on_main_sync(^{
    MincodeBrowserOverlay *overlay = overlay_for((__bridge NSWindow *)nsWindow);
    MincodeFileChooser *chooser = overlay.fileChoosers[tabID];
    if (chooser == nil) return;
    flags = (chooser.panel.allowsMultipleSelection ? 1 : 0) | (chooser.panel.canChooseDirectories ? 2 : 0);
    NSArray<NSURL *> *URLs = filePath.length > 0 ? @[[NSURL fileURLWithPath:filePath]] : nil;
    [overlay finishFileChooser:chooser tabID:tabID URLs:URLs];
    NSWindow *sheetParent = chooser.panel.sheetParent;
    if (sheetParent != nil) [sheetParent endSheet:chooser.panel returnCode:NSModalResponseCancel];
  });
  return flags;
}

char *mincode_browser_test_file_chooser_message(void *nsWindow, const char *tabId) {
  NSString *tabID = tab_id(tabId);
  __block char *out = NULL;
  run_on_main_sync(^{
    MincodeFileChooser *chooser = overlay_for((__bridge NSWindow *)nsWindow).fileChoosers[tabID];
    if (chooser != nil) out = strdup((chooser.panel.message ?: @"").UTF8String);
  });
  return out;
}

static MincodeDownloadEntry *open_save_panel_entry(MincodeBrowserOverlay *overlay, NSString *tabID) {
  for (MincodeDownloadEntry *entry in overlay.downloads) {
    if ([entry.tabID isEqualToString:tabID] && entry.decide != nil) return entry;
  }
  return nil;
}

char *mincode_browser_test_save_panel(void *nsWindow, const char *tabId, char **name) {
  NSString *tabID = tab_id(tabId);
  __block char *out = NULL;
  run_on_main_sync(^{
    MincodeDownloadEntry *entry = open_save_panel_entry(overlay_for((__bridge NSWindow *)nsWindow), tabID);
    if (entry == nil) return;
    out = strdup((entry.panel.message ?: @"").UTF8String);
    *name = strdup((entry.panel.nameFieldStringValue ?: @"").UTF8String);
  });
  return out;
}

// Finishes the tab's save panel as if the user chose path (NULL = cancel) and
// takes the sheet down. Returns -1 when no save panel is open.
int mincode_browser_test_finish_save_panel(void *nsWindow, const char *tabId, const char *path) {
  NSString *tabID = tab_id(tabId), *filePath = nsstr(path);
  __block int rc = -1;
  run_on_main_sync(^{
    MincodeBrowserOverlay *overlay = overlay_for((__bridge NSWindow *)nsWindow);
    MincodeDownloadEntry *entry = open_save_panel_entry(overlay, tabID);
    if (entry == nil) return;
    rc = 0;
    NSSavePanel *panel = entry.panel;
    [overlay finishSavePanel:entry URL:filePath.length > 0 ? [NSURL fileURLWithPath:filePath] : nil];
    if (panel.sheetParent != nil) [panel.sheetParent endSheet:panel returnCode:NSModalResponseCancel];
  });
  return rc;
}
