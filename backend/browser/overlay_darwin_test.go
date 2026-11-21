//go:build darwin

package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"mincode/app/backend/core"
)

// WebKit and AppKit need the process main thread running its loop. init locks
// the main goroutine to the main thread; TestMain then runs the tests on another
// goroutine and turns the main thread into the run loop.
func init() { runtime.LockOSThread() }

var darwinTestGUI bool

func TestMain(m *testing.M) {
	darwinTestGUI = os.Getenv("MINCODE_SKIP_WEBKIT_TESTS") == "" && darwinTestGUIAvailable()
	go func() { os.Exit(m.Run()) }()
	darwinTestRunMainLoop(darwinTestGUI)
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func TestDarwinDialogHandlerRunsExactlyOnce(t *testing.T) {
	resetPendingDialogs()
	before := darwinTestDialogCalls()
	darwinTestRegisterDialog("bookkeeping-tab", "fake-1")
	if err := platformCompleteDialog(nil, "bookkeeping-tab", "fake-1", true, ""); err != nil {
		t.Fatalf("first completion should find the handler, got %v", err)
	}
	err := platformCompleteDialog(nil, "bookkeeping-tab", "fake-1", true, "")
	if code := toolErrorCode(err); code != errCodeDialogAlreadyHandled {
		t.Fatalf("second completion should report dialog_already_handled, got %v", err)
	}
	if err := platformCompleteDialog(nil, "bookkeeping-tab", "never-registered", false, ""); toolErrorCode(err) != errCodeDialogAlreadyHandled {
		t.Fatalf("unknown dialog should report dialog_already_handled, got %v", err)
	}
	waitFor(t, 2*time.Second, "the fake handler to run", func() bool { return darwinTestDialogCalls() == before+1 })
	time.Sleep(50 * time.Millisecond)
	if got := darwinTestDialogCalls(); got != before+1 {
		t.Fatalf("handler must run exactly once, ran %d times", got-before)
	}
}

func toolErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var toolErr *browserToolError
	if errors.As(err, &toolErr) {
		return toolErr.Code
	}
	return err.Error()
}

// mountDarwinTestTab mounts a real WKWebView tab in an offscreen window.
func mountDarwinTestTab(t *testing.T, tabID, pageURL string) unsafe.Pointer {
	t.Helper()
	if !darwinTestGUI {
		t.Skip("no macOS window server session; run from a logged-in GUI session (see task-5 report)")
	}
	core.SetEmitter(nil)
	resetPendingDialogs()
	win := darwinTestNewWindow()
	SetNativeWindowProvider(func() unsafe.Pointer { return win })
	t.Cleanup(func() {
		platformClose(win)
		SetNativeWindowProvider(nil)
		resetPendingDialogs()
		darwinTestReleaseWindow(win)
	})
	if err := platformMount(win, tabID, pageURL, "", 0, 0, 640, 480); err != nil {
		t.Fatalf("mount: %v", err)
	}
	return win
}

func waitForDialog(t *testing.T, tabID string) pendingDialog {
	t.Helper()
	var d pendingDialog
	// An upper bound only: loading the page in a fresh WebContent process can take
	// several seconds while go test ./... runs other packages in parallel.
	waitFor(t, 10*time.Second, "the page dialog to open", func() bool {
		var ok bool
		d, ok = dialogForTab(tabID)
		return ok
	})
	return d
}

const confirmPage = "data:text/html,%3Cscript%3Ewindow.r%3Dconfirm('ok%3F')%3C/script%3E"

func TestDarwinConfirmDialogRoundTrip(t *testing.T) {
	win := mountDarwinTestTab(t, "dialog-tab", confirmPage)
	d := waitForDialog(t, "dialog-tab")
	if d.Kind != dialogKindConfirm || d.Message != "ok?" || d.ID == "" {
		t.Fatalf("unexpected dialog %+v", d)
	}
	if _, rest, _ := strings.Cut(d.Origin, "://"); strings.Contains(rest, "/") {
		t.Fatalf("origin must be scheme+host only, got %q", d.Origin)
	}
	t.Logf("dialog origin for a data: page: %q", d.Origin)
	if err := completeDialog("dialog-tab", d.ID, true, "", dialogByAI); err != nil {
		t.Fatalf("complete: %v", err)
	}
	var result string
	waitFor(t, 3*time.Second, "window.r to be set", func() bool {
		result = platformEval(win, "dialog-tab", "window.r", 1000)
		return strings.Contains(result, `"ok":true`) && !strings.Contains(result, `"value":null`)
	})
	if result != `{"ok":true,"value":true}` && result != `{"ok":true,"value":1}` {
		t.Fatalf("confirm should have returned true, eval gave %s", result)
	}
}

func TestDarwinPromptDialogReturnsText(t *testing.T) {
	page := "data:text/html,%3Cscript%3Ewindow.r%3Dprompt('name%3F','def')%3C/script%3E"
	win := mountDarwinTestTab(t, "prompt-tab", page)
	d := waitForDialog(t, "prompt-tab")
	if d.Kind != dialogKindPrompt || d.Message != "name?" || d.DefaultText != "def" {
		t.Fatalf("unexpected dialog %+v", d)
	}
	if err := completeDialog("prompt-tab", d.ID, true, "typed", dialogByUser); err != nil {
		t.Fatalf("complete: %v", err)
	}
	var result string
	waitFor(t, 3*time.Second, "window.r to be set", func() bool {
		result = platformEval(win, "prompt-tab", "window.r", 1000)
		return strings.Contains(result, `"value":"typed"`)
	})
}

func TestDarwinClosingTabCancelsDialog(t *testing.T) {
	win := mountDarwinTestTab(t, "closing-tab", confirmPage)
	d := waitForDialog(t, "closing-tab")
	platformCloseTab(win, "closing-tab")
	waitFor(t, 2*time.Second, "the pending dialog to clear", func() bool {
		_, ok := dialogForTab("closing-tab")
		return !ok
	})
	if err := completeDialog("closing-tab", d.ID, true, "", dialogByAI); toolErrorCode(err) != errCodeDialogAlreadyHandled {
		t.Fatalf("answering a dialog of a closed tab should report dialog_already_handled, got %v", err)
	}
}

func TestDarwinAlertReportsHTTPOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<script>alert('hi');window.done=true</script>`))
	}))
	defer server.Close()
	win := mountDarwinTestTab(t, "alert-tab", server.URL+"/secret/path?q=1")
	d := waitForDialog(t, "alert-tab")
	if d.Kind != dialogKindAlert || d.Message != "hi" {
		t.Fatalf("unexpected dialog %+v", d)
	}
	if want := server.URL; d.Origin != want {
		t.Fatalf("origin should be scheme+host+port %q, got %q", want, d.Origin)
	}
	if err := completeDialog("alert-tab", d.ID, true, "", dialogByUser); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// The overlay's eval reports JS booleans as NSNumber, so true reads as 1.
	waitFor(t, 3*time.Second, "the page to continue after the alert", func() bool {
		return platformEval(win, "alert-tab", "window.done === true ? 'yes' : 'no'", 1000) == `{"ok":true,"value":"yes"}`
	})
}

// popupTestServer serves an opener page whose link opens /popup in a new
// window; the popup posts "hi" to its opener and can close itself.
func popupTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/popup":
			_, _ = w.Write([]byte(`<script>opener.postMessage('hi','*')</script>`))
		case "/auto":
			_, _ = w.Write([]byte(`<script>window.w=window.open('/popup')</script>`))
		case "/sneaky":
			// Opens a window from inside a read-only tool's script.
			_, _ = w.Write([]byte(`<a href="/x">link</a><script>const real=Element.prototype.getBoundingClientRect;` +
				`Element.prototype.getBoundingClientRect=function(){window.w=window.open('/popup');return real.call(this)}</script>`))
		case "/double":
			_, _ = w.Write([]byte(`<button onclick="window.a=window.open('/popup');window.b=window.open('/popup')">two</button>`))
		default:
			// Anchors with target=_blank are noopener by default; rel=opener keeps
			// window.opener the way OAuth-style links that need it do.
			_, _ = w.Write([]byte(`<a id="go" target="_blank" rel="opener" href="/popup">open</a> ` +
				`<button onclick="window.open('/popup')">popup</button>` +
				`<script>window.got=[];addEventListener('message',e=>window.got.push(e.data))</script>`))
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func waitForEvent(t *testing.T, events func() []recordedEvent, name string) recordedEvent {
	t.Helper()
	var found recordedEvent
	waitFor(t, 3*time.Second, name, func() bool {
		matches := eventsNamed(events(), name)
		if len(matches) == 0 {
			return false
		}
		found = matches[0]
		return true
	})
	return found
}

// waitForPageReady waits until the tab has left its initial about:blank and loaded.
func waitForPageReady(t *testing.T, win unsafe.Pointer, tabID string) {
	t.Helper()
	waitFor(t, 3*time.Second, tabID+" to load", func() bool {
		return platformEval(win, tabID, "location.protocol + document.readyState", 1000) == `{"ok":true,"value":"http:complete"}`
	})
}

func TestDarwinTargetBlankOpensPopupWithOpener(t *testing.T) {
	server := popupTestServer(t)
	win := mountDarwinTestTab(t, "opener-tab", server.URL+"/")
	events := recordEvents(t)
	setTestSharedAIAccess(t, []string{"opener-tab"}, "opener-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows() })
	waitForPageReady(t, win, "opener-tab")

	snapshot, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapshotID, _ := snapshot.(map[string]any)["snapshotId"].(string)
	if _, err := ExecuteAITool("browser.click", map[string]any{"snapshotId": snapshotID, "ref": "e1"}); err != nil {
		t.Fatalf("click: %v", err)
	}

	opened, ok := waitForEvent(t, events, popupOpenedEvent).Data.(popupOpened)
	if !ok || !strings.HasPrefix(opened.TabID, popupTabIDPrefix) || opened.OpenerTabID != "opener-tab" || opened.URL != server.URL+"/popup" {
		t.Fatalf("unexpected popup-opened payload %+v", opened)
	}
	waitFor(t, 3*time.Second, "the opener to receive the popup's message", func() bool {
		return platformEval(win, "opener-tab", "window.got.join(',')", 1000) == `{"ok":true,"value":"hi"}`
	})

	// Hidden until the front end activates it; activation works before any mount.
	if active := platformActiveTabID(win); active != "opener-tab" {
		t.Fatalf("the popup must stay hidden until activated, active tab is %q", active)
	}
	platformActivate(win, opened.TabID)
	if active := platformActiveTabID(win); active != opened.TabID {
		t.Fatalf("activating the popup should show it, active tab is %q", active)
	}
	// Mounting an existing popup only moves it: the page keeps its state.
	if got := platformEval(win, opened.TabID, "window.marker = 'kept'", 1000); got != `{"ok":true,"value":"kept"}` {
		t.Fatalf("eval in popup: %s", got)
	}
	if err := platformMount(win, opened.TabID, server.URL+"/other", "", 10, 10, 300, 200); err != nil {
		t.Fatalf("mount popup: %v", err)
	}
	if got := platformEval(win, opened.TabID, "window.marker", 1000); got != `{"ok":true,"value":"kept"}` {
		t.Fatalf("mount must not recreate or reload the popup, eval gave %s", got)
	}

	// window.close() closes the tab and tells the front end.
	platformEval(win, opened.TabID, "window.close(); 1", 1000)
	closed, _ := waitForEvent(t, events, tabClosedEvent).Data.(tabClosed)
	if closed.TabID != opened.TabID || closed.Reason != "window.close" {
		t.Fatalf("unexpected tab-closed payload %+v", closed)
	}
	waitFor(t, 2*time.Second, "the popup web view to go away", func() bool {
		return platformActiveTabID(win) == "opener-tab"
	})
}

func TestDarwinScriptPopupWithoutGestureIsBlocked(t *testing.T) {
	server := popupTestServer(t)
	win := mountDarwinTestTab(t, "auto-tab", server.URL+"/auto")
	events := recordEvents(t)
	waitForPageReady(t, win, "auto-tab")
	time.Sleep(300 * time.Millisecond)
	if opened := eventsNamed(events(), popupOpenedEvent); len(opened) != 0 {
		t.Fatalf("window.open without a user gesture must be blocked, got %+v", opened)
	}
	if got := platformEval(win, "auto-tab", "window.w === null ? 'blocked' : 'opened'", 1000); got != `{"ok":true,"value":"blocked"}` {
		t.Fatalf("window.open should have returned null, eval gave %s", got)
	}
}

func TestDarwinWindowOpenFromAIClickKeepsOpener(t *testing.T) {
	server := popupTestServer(t)
	win := mountDarwinTestTab(t, "open-tab", server.URL+"/")
	events := recordEvents(t)
	setTestSharedAIAccess(t, []string{"open-tab"}, "open-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows() })
	waitForPageReady(t, win, "open-tab")
	snapshot, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapshotID, _ := snapshot.(map[string]any)["snapshotId"].(string)
	if _, err := ExecuteAITool("browser.click", map[string]any{"snapshotId": snapshotID, "ref": "e2"}); err != nil {
		t.Fatalf("click: %v", err)
	}
	if opened, _ := waitForEvent(t, events, popupOpenedEvent).Data.(popupOpened); opened.OpenerTabID != "open-tab" {
		t.Fatalf("unexpected popup-opened payload %+v", opened)
	}
	waitFor(t, 3*time.Second, "the opener to receive the popup's message", func() bool {
		return platformEval(win, "open-tab", "window.got.join(',')", 1000) == `{"ok":true,"value":"hi"}`
	})
}

func popupNavigationErrors(t *testing.T, win unsafe.Pointer, tabID string) []string {
	t.Helper()
	var items []struct {
		Domain  string `json:"domain"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(platformNavigationErrors(win, tabID)), &items); err != nil {
		t.Fatalf("navigation errors: %v", err)
	}
	var out []string
	for _, item := range items {
		if item.Domain == "MincodeBrowserPopup" {
			out = append(out, item.Message)
		}
	}
	return out
}

func TestDarwinInjectedReadScriptCannotOpenPopup(t *testing.T) {
	server := popupTestServer(t)
	win := mountDarwinTestTab(t, "sneaky-tab", server.URL+"/sneaky")
	events := recordEvents(t)
	setTestSharedAIAccess(t, []string{"sneaky-tab"}, "sneaky-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows() })
	waitForPageReady(t, win, "sneaky-tab")
	if _, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if opened := eventsNamed(events(), popupOpenedEvent); len(opened) != 0 {
		t.Fatalf("a page must not open windows from inside an injected read script, got %+v", opened)
	}
	if errs := popupNavigationErrors(t, win, "sneaky-tab"); len(errs) == 0 {
		t.Fatal("the refused popup should be recorded as an opener navigation error")
	}
}

func TestDarwinAIClickOpensOnlyOnePopup(t *testing.T) {
	server := popupTestServer(t)
	win := mountDarwinTestTab(t, "double-tab", server.URL+"/double")
	events := recordEvents(t)
	setTestSharedAIAccess(t, []string{"double-tab"}, "double-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows() })
	waitForPageReady(t, win, "double-tab")
	snapshot, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapshotID, _ := snapshot.(map[string]any)["snapshotId"].(string)
	if _, err := ExecuteAITool("browser.click", map[string]any{"snapshotId": snapshotID, "ref": "e1"}); err != nil {
		t.Fatalf("click: %v", err)
	}
	waitForEvent(t, events, popupOpenedEvent)
	time.Sleep(200 * time.Millisecond)
	if opened := eventsNamed(events(), popupOpenedEvent); len(opened) != 1 {
		t.Fatalf("one AI click must open exactly one popup, got %d", len(opened))
	}
}

func TestDarwinRealMouseClickAllowsOnePopup(t *testing.T) {
	server := popupTestServer(t)
	win := mountDarwinTestTab(t, "mouse-tab", server.URL+"/")
	events := recordEvents(t)
	t.Cleanup(resetAIPopupWindows)
	waitForPageReady(t, win, "mouse-tab")
	// The page opens a window from an injected script: refused without input.
	platformEval(win, "mouse-tab", "window.open('/popup'); 1", 1000)
	time.Sleep(200 * time.Millisecond)
	if opened := eventsNamed(events(), popupOpenedEvent); len(opened) != 0 {
		t.Fatalf("no popup before user input, got %+v", opened)
	}
	// A mouse event delivered to the tab's web view counts as real user input.
	darwinTestMouseClick(win, "mouse-tab", 5, 5)
	platformEval(win, "mouse-tab", "window.open('/popup'); 1", 1000)
	waitForEvent(t, events, popupOpenedEvent)
}

func TestDarwinPopupTabsAreCapped(t *testing.T) {
	server := popupTestServer(t)
	win := mountDarwinTestTab(t, "cap-tab", server.URL+"/")
	events := recordEvents(t)
	t.Cleanup(resetAIPopupWindows)
	waitForPageReady(t, win, "cap-tab")
	for i := 0; i < maxPopupTabs+1; i++ {
		allowAIPopupWindow("cap-tab")
		platformEval(win, "cap-tab", "window.open('/popup'); 1", 1000)
	}
	time.Sleep(200 * time.Millisecond)
	if opened := eventsNamed(events(), popupOpenedEvent); len(opened) != maxPopupTabs {
		t.Fatalf("expected %d popups, got %d", maxPopupTabs, len(opened))
	}
	if errs := popupNavigationErrors(t, win, "cap-tab"); len(errs) != 1 {
		t.Fatalf("the popup over the cap should be recorded once, got %v", errs)
	}
	// Closing one frees a slot.
	first := eventsNamed(events(), popupOpenedEvent)[0].Data.(popupOpened).TabID
	platformCloseTab(win, first)
	allowAIPopupWindow("cap-tab")
	platformEval(win, "cap-tab", "window.open('/popup'); 1", 1000)
	waitFor(t, 2*time.Second, "a popup after closing one", func() bool {
		return len(eventsNamed(events(), popupOpenedEvent)) == maxPopupTabs+1
	})
}

// A host window closed without mincode_browser_close (no explicit teardown) must
// still give back its popup slots and answer its pending dialogs.
func TestDarwinClosedWindowReleasesPopupSlotsAndDialogs(t *testing.T) {
	if !darwinTestGUI {
		t.Skip("no macOS window server session")
	}
	server := popupTestServer(t)
	core.SetEmitter(nil)
	resetPendingDialogs()
	t.Cleanup(func() { resetAIPopupWindows(); resetPendingDialogs(); SetNativeWindowProvider(nil) })
	before := darwinTestPopupCount()
	win := darwinTestNewWindow()
	SetNativeWindowProvider(func() unsafe.Pointer { return win })
	if err := platformMount(win, "freed-tab", server.URL+"/", "", 0, 0, 640, 480); err != nil {
		t.Fatalf("mount: %v", err)
	}
	waitForPageReady(t, win, "freed-tab")
	for i := 0; i < 2; i++ {
		allowAIPopupWindow("freed-tab")
		platformEval(win, "freed-tab", "window.open('/popup'); 1", 1000)
	}
	waitFor(t, 2*time.Second, "two popups", func() bool { return darwinTestPopupCount() == before+2 })
	darwinTestRegisterDialog("freed-tab", "freed-dialog")
	calls := darwinTestDialogCalls()

	SetNativeWindowProvider(nil)
	darwinTestReleaseWindow(win) // closes the window; no platformClose
	waitFor(t, 3*time.Second, "popup slots released", func() bool { return darwinTestPopupCount() == before })
	if got := darwinTestDialogCalls(); got != calls+1 {
		t.Fatalf("the closed window's pending dialog must be answered once, handler calls %d -> %d", calls, got)
	}
}

func fileChooserTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/autoclick":
			// Clicks the input while loading, with no user gesture.
			_, _ = w.Write([]byte(`<input id="up" type="file"><script>document.getElementById('up').click()</script>`))
			return
		case "/sneaky":
			// Opens the chooser from inside a read-only tool's script.
			_, _ = w.Write([]byte(`<a href="/x">link</a><input id="up" type="file"><script>const real=Element.prototype.getBoundingClientRect;` +
				`Element.prototype.getBoundingClientRect=function(){document.getElementById('up').click();return real.call(this)}</script>`))
			return
		case "/prevented":
			// The page cancels the click, so no chooser opens.
			_, _ = w.Write([]byte(`<input id="up" type="file" onclick="event.preventDefault()">`))
			return
		}
		_, _ = w.Write([]byte(`<input id="up" type="file" multiple accept=".txt">` +
			`<script>window.picked=[];document.getElementById('up').addEventListener('change',e=>{window.picked=[...e.target.files].map(f=>f.name)})</script>`))
	}))
	t.Cleanup(server.Close)
	return server
}

func fileInputRef(t *testing.T, snapshot any) (snapshotID, ref string, item map[string]any) {
	t.Helper()
	fields, _ := snapshot.(map[string]any)
	snapshotID, _ = fields["snapshotId"].(string)
	elements, _ := fields["elements"].([]any)
	for _, raw := range elements {
		element, _ := raw.(map[string]any)
		if element["type"] == "file" {
			ref, _ = element["ref"].(string)
			return snapshotID, ref, element
		}
	}
	t.Fatalf("snapshot lists no file input: %+v", snapshot)
	return "", "", nil
}

// An AI click on a file input runs inside evaluateJavaScript, which WebKit
// treats as a user gesture, so the native panel opens. The test then finishes
// the panel with a fixture file through the test hook.
func TestDarwinAIClickOnFileInputOpensChooser(t *testing.T) {
	server := fileChooserTestServer(t)
	win := mountDarwinTestTab(t, "upload-tab", server.URL+"/upload/page?token=secret")
	events := recordEvents(t)
	setTestSharedAIAccess(t, []string{"upload-tab"}, "upload-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows(); resetFileChoosers() })
	waitForPageReady(t, win, "upload-tab")

	snapshot, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapshotID, ref, item := fileInputRef(t, snapshot)
	if item["multiple"] != true || item["accept"] != ".txt" || item["selectedCount"] != float64(0) {
		t.Fatalf("unexpected file input item %+v", item)
	}
	result, err := ExecuteAITool("browser.click", map[string]any{"snapshotId": snapshotID, "ref": ref})
	if err != nil {
		t.Fatalf("an AI click on a file input should open the chooser, got %v", err)
	}
	if fields := result.(map[string]any); fields["fileChooser"] != "opened" || fields["clicked"] != true {
		t.Fatalf("unexpected click result %+v", fields)
	}
	if opened := waitForEvent(t, events, fileChooserEvent).Data.(fileChooserState); !opened.Open || opened.TabID != "upload-tab" {
		t.Fatalf("unexpected file-chooser event %+v", opened)
	}

	host := strings.TrimPrefix(server.URL, "http://")
	if message := darwinTestFileChooserMessage(win, "upload-tab"); message != "http://"+host+" wants you to choose files" {
		t.Fatalf("panel message should name only the page origin, got %q", message)
	}

	fixture := t.TempDir() + "/fixture-upload.txt"
	if err := os.WriteFile(fixture, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	flags := darwinTestFinishFileChooser(win, "upload-tab", fixture)
	if flags < 0 {
		t.Fatal("the overlay has no open chooser for the tab")
	}
	if flags&1 == 0 || flags&2 != 0 {
		t.Fatalf("panel should allow multiple files and no directories, flags=%d", flags)
	}
	waitFor(t, 3*time.Second, "the chooser to close with one file", func() bool {
		for _, event := range eventsNamed(events(), fileChooserEvent) {
			if state := event.Data.(fileChooserState); !state.Open && state.SelectedCount == 1 {
				return true
			}
		}
		return false
	})
	waitFor(t, 3*time.Second, "the page to see the chosen file", func() bool {
		return platformEval(win, "upload-tab", "window.picked.join(',')", 1000) == `{"ok":true,"value":"fixture-upload.txt"}`
	})
	after, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, _, item := fileInputRef(t, after); item["selectedCount"] != float64(1) {
		t.Fatalf("the next snapshot should count the chosen file, got %+v", item)
	}
	encoded, _ := json.Marshal(after)
	if strings.Contains(string(encoded), "fixture-upload") {
		t.Fatalf("snapshots must not reveal chosen file names: %s", encoded)
	}
	for _, event := range events() {
		if encoded, _ := json.Marshal(event.Data); strings.Contains(string(encoded), "fixture-upload") {
			t.Fatalf("events must not reveal chosen files: %s", encoded)
		}
	}
}

func TestDarwinClosingTabCancelsFileChooser(t *testing.T) {
	server := fileChooserTestServer(t)
	win := mountDarwinTestTab(t, "upload-close-tab", server.URL+"/")
	events := recordEvents(t)
	t.Cleanup(resetFileChoosers)
	waitForPageReady(t, win, "upload-close-tab")
	allowAIFileChooser("upload-close-tab")
	platformEval(win, "upload-close-tab", "document.getElementById('up').click(); 1", 1000)
	waitForEvent(t, events, fileChooserEvent)
	platformCloseTab(win, "upload-close-tab")
	waitFor(t, 3*time.Second, "the chooser to be cancelled", func() bool {
		for _, event := range eventsNamed(events(), fileChooserEvent) {
			if state := event.Data.(fileChooserState); !state.Open && state.SelectedCount == 0 {
				return true
			}
		}
		return false
	})
	if flags := darwinTestFinishFileChooser(win, "upload-close-tab", ""); flags >= 0 {
		t.Fatal("closing the tab must drop its chooser")
	}
}

func TestDarwinFileChooserWithoutGestureIsBlocked(t *testing.T) {
	server := fileChooserTestServer(t)
	win := mountDarwinTestTab(t, "upload-auto-tab", server.URL+"/autoclick")
	events := recordEvents(t)
	t.Cleanup(resetFileChoosers)
	waitForPageReady(t, win, "upload-auto-tab")
	time.Sleep(300 * time.Millisecond)
	if got := eventsNamed(events(), fileChooserEvent); len(got) != 0 {
		t.Fatalf("WebKit should refuse a chooser without a user gesture, got %+v", got)
	}
}

func TestDarwinAIClickWithoutChooserRequiresUser(t *testing.T) {
	server := fileChooserTestServer(t)
	win := mountDarwinTestTab(t, "upload-prevented-tab", server.URL+"/prevented")
	setTestSharedAIAccess(t, []string{"upload-prevented-tab"}, "upload-prevented-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows(); resetFileChoosers() })
	waitForPageReady(t, win, "upload-prevented-tab")
	snapshot, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapshotID, ref, _ := fileInputRef(t, snapshot)
	started := time.Now()
	_, err = ExecuteAITool("browser.click", map[string]any{"snapshotId": snapshotID, "ref": ref})
	requireToolErrorCode(t, err, errCodeFileChooserRequiresUser)
	if elapsed := time.Since(started); elapsed < fileChooserWaitTimeout || elapsed > fileChooserWaitTimeout+2*time.Second {
		t.Fatalf("the click should wait about %s for the chooser, took %s", fileChooserWaitTimeout, elapsed)
	}
}

func fileChooserNavigationErrors(t *testing.T, win unsafe.Pointer, tabID string) []string {
	t.Helper()
	var items []struct {
		Domain  string `json:"domain"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(platformNavigationErrors(win, tabID)), &items); err != nil {
		t.Fatalf("navigation errors: %v", err)
	}
	var out []string
	for _, item := range items {
		if item.Domain == "MincodeBrowserFileChooser" {
			out = append(out, item.Message)
		}
	}
	return out
}

// Injected read scripts carry a user gesture, so WebKit alone would let the
// page open a chooser during an AI snapshot; the overlay refuses it.
func TestDarwinPageCannotOpenChooserDuringAIRead(t *testing.T) {
	server := fileChooserTestServer(t)
	win := mountDarwinTestTab(t, "upload-sneaky-tab", server.URL+"/sneaky")
	events := recordEvents(t)
	setTestSharedAIAccess(t, []string{"upload-sneaky-tab"}, "upload-sneaky-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows(); resetFileChoosers() })
	waitForPageReady(t, win, "upload-sneaky-tab")
	if _, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := eventsNamed(events(), fileChooserEvent); len(got) != 0 {
		t.Fatalf("a page must not open a chooser from inside an injected read script, got %+v", got)
	}
	if darwinTestFinishFileChooser(win, "upload-sneaky-tab", "") >= 0 {
		t.Fatal("no panel may be shown")
	}
	errs := fileChooserNavigationErrors(t, win, "upload-sneaky-tab")
	if len(errs) == 0 {
		t.Fatal("the refused chooser should be recorded as a navigation error")
	}
	for _, message := range errs {
		if strings.Contains(message, "127.0.0.1") || strings.Contains(message, "/") {
			t.Fatalf("the refusal must not carry the page address: %q", message)
		}
	}
}

func TestDarwinRealMouseClickOnFileInputOpensChooser(t *testing.T) {
	server := fileChooserTestServer(t)
	win := mountDarwinTestTab(t, "upload-mouse-tab", server.URL+"/")
	events := recordEvents(t)
	t.Cleanup(func() { resetAIPopupWindows(); resetFileChoosers() })
	waitForPageReady(t, win, "upload-mouse-tab")
	// The input sits at the page's top-left corner.
	darwinTestMouseClick(win, "upload-mouse-tab", 20, 15)
	if opened := waitForEvent(t, events, fileChooserEvent).Data.(fileChooserState); !opened.Open {
		t.Fatalf("unexpected file-chooser event %+v", opened)
	}
	if darwinTestFinishFileChooser(win, "upload-mouse-tab", "") < 0 {
		t.Fatal("a real click should have shown the panel")
	}
}

// downloadTestServer serves a page with a download link; /file is an
// attachment, /slow streams an attachment that never finishes.
func downloadTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file":
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("Content-Disposition", `attachment; filename=".report.csv"`)
			_, _ = w.Write([]byte("a,b\n1,2\n"))
			return
		case "/delayed":
			// Like a generated export: the attachment starts two seconds after the click.
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("Content-Disposition", `attachment; filename="export.csv"`)
			_, _ = w.Write([]byte("late\n"))
			return
		case "/redirect":
			http.Redirect(w, r, "/delayed", http.StatusFound)
			return
		case "/redir-bad":
			http.Redirect(w, r, "http://169.254.169.254/latest?secret=1", http.StatusFound)
			return
		case "/slow":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="big.bin"`)
			w.Header().Set("Content-Length", "10000000")
			_, _ = w.Write(make([]byte, 64*1024))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/auto":
			// Starts a download while loading, with no click.
			_, _ = w.Write([]byte(`<script>location.href='/file?token=secret'</script>`))
		case "/blocked":
			// A page-started top-level navigation to a refused address, and a frame with a refused scheme.
			_, _ = w.Write([]byte(`<iframe src="x-mincode-test://frame/path"></iframe><script>setTimeout(()=>{location.href='http://169.254.169.254/latest?secret=1'},50)</script>`))
		case "/slowpage":
			_, _ = w.Write([]byte(`<a href="/slow">big</a>`))
		case "/linkauto":
			_, _ = w.Write([]byte(`<a href="/auto">next</a>`))
		case "/delaypage":
			_, _ = w.Write([]byte(`<a href="/delayed">export</a>`))
		case "/redirectpage":
			_, _ = w.Write([]byte(`<a href="/redirect">export</a>`))
		case "/badredirect":
			_, _ = w.Write([]byte(`<a download="x.bin" href="/redir-bad">get</a>`))
		case "/blobdl":
			_, _ = w.Write([]byte(`<button onclick="location.href=window.URL.createObjectURL(new window.Blob(['a,b'],{type:'application/octet-stream'}))">save</button>`))
		case "/blobview":
			_, _ = w.Write([]byte(`<button onclick="location.href=window.URL.createObjectURL(new window.Blob(['hello'],{type:'text/plain'}))">view</button>`))
		case "/datadl":
			_, _ = w.Write([]byte(`<button onclick="location.href='data:application/octet-stream,abc'">save</button>`))
		case "/datahtml":
			_, _ = w.Write([]byte(`<button onclick="location.href='data:text/html,%3Cp%3Ehi%3C/p%3E'">open</button>`))
		default:
			_, _ = w.Write([]byte(`<a href="/file?token=secret">report</a>`))
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })
	return server
}

func waitForSavePanel(t *testing.T, win unsafe.Pointer, tabID string) (message, name string) {
	t.Helper()
	waitFor(t, 3*time.Second, "the save panel", func() bool {
		var ok bool
		message, name, ok = darwinTestSavePanel(win, tabID)
		return ok
	})
	return message, name
}

func waitForDownloadState(t *testing.T, tabID, state string) downloadRecord {
	t.Helper()
	var found downloadRecord
	waitFor(t, 5*time.Second, "a "+state+" download", func() bool {
		for _, record := range downloadsForTab(tabID) {
			if record.State == state {
				found = record
				return true
			}
		}
		return false
	})
	return found
}

func clickFirstLink(t *testing.T) {
	t.Helper()
	snapshot, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapshotID, _ := snapshot.(map[string]any)["snapshotId"].(string)
	if _, err := ExecuteAITool("browser.click", map[string]any{"snapshotId": snapshotID, "ref": "e1"}); err != nil {
		t.Fatalf("click: %v", err)
	}
}

// An AI click on a download link asks the user in a save panel; the test hook
// stands in for the user and saves to a temp directory.
func TestDarwinAIClickDownloadAsksInSavePanel(t *testing.T) {
	server := downloadTestServer(t)
	win := mountDarwinTestTab(t, "download-tab", server.URL+"/")
	events := recordEvents(t)
	resetDownloads()
	setTestSharedAIAccess(t, []string{"download-tab"}, "download-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows(); resetFileChoosers(); resetDownloads() })
	waitForPageReady(t, win, "download-tab")

	clickFirstLink(t)
	message, name := waitForSavePanel(t, win, "download-tab")
	host := strings.TrimPrefix(server.URL, "http://")
	if name != "report.csv" {
		t.Fatalf("the panel should suggest the sanitized name, got %q", name)
	}
	if message != "http://"+host+" wants to download report.csv" {
		t.Fatalf("the panel should name only the page origin, got %q", message)
	}
	pending := waitForDownloadState(t, "download-tab", downloadStatePending)
	if pending.Filename != "report.csv" || pending.Origin != "http://"+host {
		t.Fatalf("unexpected pending record %+v", pending)
	}

	destination := t.TempDir() + "/" + name
	if !darwinTestFinishSavePanel(win, "download-tab", destination) {
		t.Fatal("the overlay has no open save panel for the tab")
	}
	done := waitForDownloadState(t, "download-tab", downloadStateCompleted)
	if done.Bytes != 8 || done.FinishedAt == 0 {
		t.Fatalf("unexpected completed record %+v", done)
	}
	content, err := os.ReadFile(destination)
	if err != nil || string(content) != "a,b\n1,2\n" {
		t.Fatalf("the file should land where the user chose: %q %v", content, err)
	}
	result, err := ExecuteAITool("browser.get_downloads", map[string]any{})
	if err != nil {
		t.Fatalf("get_downloads: %v", err)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"state":"completed"`) || strings.Contains(string(encoded), destination) || strings.Contains(string(encoded), "secret") {
		t.Fatalf("get_downloads should report the completed download without paths: %s", encoded)
	}
	for _, event := range eventsNamed(events(), downloadUpdatedEvent) {
		if encoded, _ := json.Marshal(event.Data); strings.Contains(string(encoded), destination) || strings.Contains(string(encoded), "secret") {
			t.Fatalf("download events must not carry paths or queries: %s", encoded)
		}
	}
	var revealed string
	original := revealDownloadInFileManager
	revealDownloadInFileManager = func(path string) error { revealed = path; return nil }
	t.Cleanup(func() { revealDownloadInFileManager = original })
	if _, err := handleRevealDownload("", `{"payload":{"id":"`+done.ID+`"}}`); err != nil || revealed != destination {
		t.Fatalf("reveal should open the saved file, got %q %v", revealed, err)
	}
}

func TestDarwinCancelledSavePanelCancelsDownload(t *testing.T) {
	server := downloadTestServer(t)
	win := mountDarwinTestTab(t, "download-cancel-tab", server.URL+"/")
	recordEvents(t)
	resetDownloads()
	setTestSharedAIAccess(t, []string{"download-cancel-tab"}, "download-cancel-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows(); resetFileChoosers(); resetDownloads() })
	waitForPageReady(t, win, "download-cancel-tab")
	clickFirstLink(t)
	waitForSavePanel(t, win, "download-cancel-tab")
	if !darwinTestFinishSavePanel(win, "download-cancel-tab", "") {
		t.Fatal("no save panel to cancel")
	}
	cancelled := waitForDownloadState(t, "download-cancel-tab", downloadStateCancelled)
	if cancelled.Error != "user_cancelled" {
		t.Fatalf("unexpected cancelled record %+v", cancelled)
	}
}

func TestDarwinPageDownloadWithoutUserActionIsCancelled(t *testing.T) {
	server := downloadTestServer(t)
	resetDownloads()
	t.Cleanup(func() { resetAIPopupWindows(); resetFileChoosers(); resetDownloads() })
	win := mountDarwinTestTab(t, "download-auto-tab", server.URL+"/auto")
	recordEvents(t)
	cancelled := waitForDownloadState(t, "download-auto-tab", downloadStateCancelled)
	if cancelled.Error != downloadErrorNoUserAction || cancelled.Filename != "report.csv" {
		t.Fatalf("unexpected record %+v", cancelled)
	}
	if _, _, open := darwinTestSavePanel(win, "download-auto-tab"); open {
		t.Fatal("no save panel may be shown without a click")
	}
}

func TestDarwinClosingTabCancelsDownload(t *testing.T) {
	server := downloadTestServer(t)
	win := mountDarwinTestTab(t, "download-close-tab", server.URL+"/slowpage")
	recordEvents(t)
	resetDownloads()
	setTestSharedAIAccess(t, []string{"download-close-tab"}, "download-close-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows(); resetFileChoosers(); resetDownloads() })
	waitForPageReady(t, win, "download-close-tab")
	clickFirstLink(t)
	_, name := waitForSavePanel(t, win, "download-close-tab")
	if !darwinTestFinishSavePanel(win, "download-close-tab", t.TempDir()+"/"+name) {
		t.Fatal("no save panel")
	}
	waitFor(t, 5*time.Second, "download progress", func() bool {
		records := downloadsForTab("download-close-tab")
		return len(records) == 1 && records[0].State == downloadStateSaving && records[0].Bytes > 0
	})
	platformCloseTab(win, "download-close-tab")
	cancelled := waitForDownloadState(t, "download-close-tab", downloadStateCancelled)
	if cancelled.Error != "tab_closed" || cancelled.TotalBytes != 10000000 {
		t.Fatalf("unexpected record %+v", cancelled)
	}
}

// Replacing an existing file keeps it until the new download has finished.
func TestDarwinReplacingAFileKeepsItUntilTheDownloadCompletes(t *testing.T) {
	server := downloadTestServer(t)
	win := mountDarwinTestTab(t, "download-replace-tab", server.URL+"/")
	recordEvents(t)
	resetDownloads()
	setTestSharedAIAccess(t, []string{"download-replace-tab"}, "download-replace-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows(); resetFileChoosers(); resetDownloads() })
	waitForPageReady(t, win, "download-replace-tab")
	clickFirstLink(t)
	_, name := waitForSavePanel(t, win, "download-replace-tab")
	dir := t.TempDir()
	destination := dir + "/" + name
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !darwinTestFinishSavePanel(win, "download-replace-tab", destination) {
		t.Fatal("no save panel")
	}
	done := waitForDownloadState(t, "download-replace-tab", downloadStateCompleted)
	content, err := os.ReadFile(destination)
	if err != nil || string(content) != "a,b\n1,2\n" {
		t.Fatalf("the finished download should replace the file: %q %v", content, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("no temporary file may be left next to the download, got %d entries", len(entries))
	}
	var revealed string
	original := revealDownloadInFileManager
	revealDownloadInFileManager = func(path string) error { revealed = path; return nil }
	t.Cleanup(func() { revealDownloadInFileManager = original })
	if _, err := handleRevealDownload("", `{"payload":{"id":"`+done.ID+`"}}`); err != nil || revealed != destination {
		t.Fatalf("reveal should open the replaced file, got %q %v", revealed, err)
	}
}

// A download that does not finish leaves the file it would have replaced untouched.
func TestDarwinInterruptedReplacementKeepsTheOriginalFile(t *testing.T) {
	server := downloadTestServer(t)
	win := mountDarwinTestTab(t, "download-keep-tab", server.URL+"/slowpage")
	recordEvents(t)
	resetDownloads()
	setTestSharedAIAccess(t, []string{"download-keep-tab"}, "download-keep-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows(); resetFileChoosers(); resetDownloads() })
	waitForPageReady(t, win, "download-keep-tab")
	clickFirstLink(t)
	_, name := waitForSavePanel(t, win, "download-keep-tab")
	dir := t.TempDir()
	destination := dir + "/" + name
	if err := os.WriteFile(destination, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !darwinTestFinishSavePanel(win, "download-keep-tab", destination) {
		t.Fatal("no save panel")
	}
	waitFor(t, 5*time.Second, "download progress", func() bool {
		records := downloadsForTab("download-keep-tab")
		return len(records) == 1 && records[0].State == downloadStateSaving && records[0].Bytes > 0
	})
	if content, _ := os.ReadFile(destination); string(content) != "keep me" {
		t.Fatalf("the original file must stay while the download runs, got %d bytes", len(content))
	}
	platformCloseTab(win, "download-keep-tab")
	waitForDownloadState(t, "download-keep-tab", downloadStateCancelled)
	waitFor(t, 3*time.Second, "temporary download file removed", func() bool {
		entries, _ := os.ReadDir(dir)
		return len(entries) == 1
	})
	if content, _ := os.ReadFile(destination); string(content) != "keep me" {
		t.Fatalf("a cancelled download must not touch the original file, got %d bytes", len(content))
	}
}

func TestDarwinClosingTabCancelsOpenSavePanel(t *testing.T) {
	server := downloadTestServer(t)
	win := mountDarwinTestTab(t, "download-close-panel-tab", server.URL+"/")
	recordEvents(t)
	resetDownloads()
	setTestSharedAIAccess(t, []string{"download-close-panel-tab"}, "download-close-panel-tab")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows(); resetFileChoosers(); resetDownloads() })
	waitForPageReady(t, win, "download-close-panel-tab")
	clickFirstLink(t)
	waitForSavePanel(t, win, "download-close-panel-tab")
	platformCloseTab(win, "download-close-panel-tab")
	if cancelled := waitForDownloadState(t, "download-close-panel-tab", downloadStateCancelled); cancelled.Error != "tab_closed" {
		t.Fatalf("unexpected record %+v", cancelled)
	}
	if _, _, open := darwinTestSavePanel(win, "download-close-panel-tab"); open {
		t.Fatal("closing the tab must take its save panel down")
	}
}

func navigationErrorsIn(t *testing.T, win unsafe.Pointer, tabID, domain string) []string {
	t.Helper()
	var items []struct {
		Domain  string `json:"domain"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(platformNavigationErrors(win, tabID)), &items); err != nil {
		t.Fatalf("navigation errors: %v", err)
	}
	var out []string
	for _, item := range items {
		if item.Domain == domain {
			out = append(out, item.Message)
		}
	}
	return out
}

func TestDarwinPageNavigationsAreValidated(t *testing.T) {
	server := downloadTestServer(t)
	win := mountDarwinTestTab(t, "nav-policy-tab", server.URL+"/blocked")
	waitForPageReady(t, win, "nav-policy-tab")
	var errs []string
	waitFor(t, 3*time.Second, "both refusals", func() bool {
		errs = navigationErrorsIn(t, win, "nav-policy-tab", "MincodeBrowserNavigation")
		return len(errs) >= 2
	})
	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, "x-mincode-test") || !strings.Contains(joined, "http") {
		t.Fatalf("refusals should name the scheme: %q", joined)
	}
	if strings.Contains(joined, "169.254") || strings.Contains(joined, "secret") || strings.Contains(joined, "/") {
		t.Fatalf("refusals must carry the scheme only: %q", joined)
	}
	if got := platformEval(win, "nav-policy-tab", "location.pathname", 1000); got != `{"ok":true,"value":"\/blocked"}` {
		t.Fatalf("the page should stay put, got %s", got)
	}
}

func TestDarwinRealMouseClickOnDownloadLinkAsksInSavePanel(t *testing.T) {
	server := downloadTestServer(t)
	win := mountDarwinTestTab(t, "download-mouse-tab", server.URL+"/")
	recordEvents(t)
	resetDownloads()
	t.Cleanup(func() { resetAIPopupWindows(); resetFileChoosers(); resetDownloads() })
	waitForPageReady(t, win, "download-mouse-tab")
	// The link sits at the page's top-left corner.
	darwinTestMouseClick(win, "download-mouse-tab", 20, 15)
	waitForSavePanel(t, win, "download-mouse-tab")
	if !darwinTestFinishSavePanel(win, "download-mouse-tab", "") {
		t.Fatal("a real click should have shown the save panel")
	}
}

// mountDownloadTab mounts path, shares the tab with AI and clicks its first element.
func aiClickOnDownloadPage(t *testing.T, tabID, path string) (unsafe.Pointer, *httptest.Server) {
	t.Helper()
	server := downloadTestServer(t)
	win := mountDarwinTestTab(t, tabID, server.URL+path)
	recordEvents(t)
	resetDownloads()
	setTestSharedAIAccess(t, []string{tabID}, tabID)
	t.Cleanup(func() { setTestAIAccess(false, ""); resetAIPopupWindows(); resetFileChoosers(); resetDownloads() })
	waitForPageReady(t, win, tabID)
	clickFirstLink(t)
	return win, server
}

func saveAndWaitForCompletion(t *testing.T, win unsafe.Pointer, tabID string) (downloadRecord, string) {
	t.Helper()
	_, name := waitForSavePanelWithin(t, win, tabID, 6*time.Second)
	destination := t.TempDir() + "/" + name
	if !darwinTestFinishSavePanel(win, tabID, destination) {
		t.Fatal("no save panel")
	}
	return waitForDownloadState(t, tabID, downloadStateCompleted), destination
}

func waitForSavePanelWithin(t *testing.T, win unsafe.Pointer, tabID string, timeout time.Duration) (message, name string) {
	t.Helper()
	waitFor(t, timeout, "the save panel", func() bool {
		var ok bool
		message, name, ok = darwinTestSavePanel(win, tabID)
		return ok
	})
	return message, name
}

// The click authorizes the navigation, so an attachment that arrives well
// after the 1s allowance still gets its save panel.
func TestDarwinSlowAttachmentAfterAIClickAsksInSavePanel(t *testing.T) {
	win, _ := aiClickOnDownloadPage(t, "download-delay-tab", "/delaypage")
	record, destination := saveAndWaitForCompletion(t, win, "download-delay-tab")
	if content, err := os.ReadFile(destination); err != nil || string(content) != "late\n" || record.Filename != "export.csv" {
		t.Fatalf("unexpected download %+v %q %v", record, content, err)
	}
}

func TestDarwinRedirectedSlowAttachmentAfterAIClickAsksInSavePanel(t *testing.T) {
	win, _ := aiClickOnDownloadPage(t, "download-redirect-tab", "/redirectpage")
	if record, _ := saveAndWaitForCompletion(t, win, "download-redirect-tab"); record.Filename != "export.csv" {
		t.Fatalf("unexpected download %+v", record)
	}
}

func TestDarwinBlobDownloadFromLocationAsksInSavePanel(t *testing.T) {
	win, _ := aiClickOnDownloadPage(t, "download-blob-tab", "/blobdl")
	if record, destination := saveAndWaitForCompletion(t, win, "download-blob-tab"); record.Bytes != 3 {
		content, _ := os.ReadFile(destination)
		t.Fatalf("unexpected blob download %+v %q", record, content)
	}
}

func TestDarwinDataDownloadFromLocationAsksInSavePanel(t *testing.T) {
	win, _ := aiClickOnDownloadPage(t, "download-data-tab", "/datadl")
	if record, destination := saveAndWaitForCompletion(t, win, "download-data-tab"); record.Bytes != 3 {
		content, _ := os.ReadFile(destination)
		t.Fatalf("unexpected data download %+v %q", record, content)
	}
}

func TestDarwinTopLevelBlobPreviewIsAllowed(t *testing.T) {
	win, _ := aiClickOnDownloadPage(t, "blob-view-tab", "/blobview")
	waitFor(t, 3*time.Second, "the blob page", func() bool {
		return platformEval(win, "blob-view-tab", "location.protocol + document.body.innerText", 1000) == `{"ok":true,"value":"blob:hello"}`
	})
}

func TestDarwinTopLevelDataPageIsBlocked(t *testing.T) {
	win, _ := aiClickOnDownloadPage(t, "data-html-tab", "/datahtml")
	time.Sleep(500 * time.Millisecond)
	if got := platformEval(win, "data-html-tab", "location.pathname", 1000); got != `{"ok":true,"value":"\/datahtml"}` {
		t.Fatalf("a page must not open a top-level data: document, got %s", got)
	}
	if _, _, open := darwinTestSavePanel(win, "data-html-tab"); open {
		t.Fatal("no save panel for a data: page")
	}
	// WebKit itself refuses page-started top-level data: documents before a
	// response; the overlay's response check is the backstop.
	for _, message := range navigationErrorsIn(t, win, "data-html-tab", "MincodeBrowserNavigation") {
		if strings.Contains(message, "/") || strings.Contains(message, "hi") {
			t.Fatalf("a refusal must name the scheme only: %q", message)
		}
	}
}

// A download link's redirect arrives as a navigation action before WebKit
// turns it into a download, so the navigation rules refuse it. (Redirects of a
// running WKDownload go through willPerformHTTPRedirection with the same check.)
func TestDarwinDownloadRedirectIsValidated(t *testing.T) {
	win, _ := aiClickOnDownloadPage(t, "download-bad-redirect-tab", "/badredirect")
	var errs []string
	waitFor(t, 3*time.Second, "the refusal", func() bool {
		errs = navigationErrorsIn(t, win, "download-bad-redirect-tab", "MincodeBrowserNavigation")
		return len(errs) > 0
	})
	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, "the http address") || strings.Contains(joined, "169.254") || strings.Contains(joined, "secret") {
		t.Fatalf("the refusal should name the scheme only: %q", joined)
	}
	time.Sleep(300 * time.Millisecond)
	if _, _, open := darwinTestSavePanel(win, "download-bad-redirect-tab"); open {
		t.Fatal("no save panel after a refused redirect")
	}
	for _, record := range downloadsForTab("download-bad-redirect-tab") {
		if record.State != downloadStateFailed && record.State != downloadStateCancelled {
			t.Fatalf("a refused redirect must not download, got %+v", record)
		}
	}
}

// The click authorizes its own navigation only: once the next page commits,
// a download that page starts by itself is refused.
func TestDarwinClickAuthorizationEndsWhenThePageCommits(t *testing.T) {
	win, _ := aiClickOnDownloadPage(t, "download-commit-tab", "/linkauto")
	cancelled := waitForDownloadState(t, "download-commit-tab", downloadStateCancelled)
	if cancelled.Error != downloadErrorNoUserAction {
		t.Fatalf("unexpected record %+v", cancelled)
	}
	if _, _, open := darwinTestSavePanel(win, "download-commit-tab"); open {
		t.Fatal("no save panel for a download the next page started itself")
	}
}

func historyTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<title>` + strings.TrimPrefix(r.URL.Path, "/") + `</title><p>page ` + r.URL.Path + `</p>`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDarwinGoBackAndForwardFollowTheTabHistory(t *testing.T) {
	server := historyTestServer(t)
	win := mountDarwinTestTab(t, "history-tab", server.URL+"/a")
	waitForPageReady(t, win, "history-tab")
	setTestAIAccess(true, "history-tab")
	t.Cleanup(func() { setTestAIAccess(false, "") })

	_, err := ExecuteAITool("browser.go_back", map[string]any{})
	if code := toolErrorCode(err); code != errCodeNoHistory {
		t.Fatalf("the first page has nothing to go back to, got %v", err)
	}
	if _, err := ExecuteAITool("browser.navigate", map[string]any{"url": server.URL + "/b"}); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	urlOf := func(result any) string {
		state, _ := result.(browserReadyState)
		return state.URL
	}
	result, err := ExecuteAITool("browser.go_back", map[string]any{})
	if err != nil || urlOf(result) != server.URL+"/a" {
		t.Fatalf("go_back should return to /a, got %#v %v", result, err)
	}
	result, err = ExecuteAITool("browser.go_forward", map[string]any{})
	if err != nil || urlOf(result) != server.URL+"/b" {
		t.Fatalf("go_forward should return to /b, got %#v %v", result, err)
	}
	if _, err := ExecuteAITool("browser.go_forward", map[string]any{}); toolErrorCode(err) != errCodeNoHistory {
		t.Fatalf("the last page has nothing to go forward to, got %v", err)
	}

	// A same-document entry (a fragment) is history too.
	if got := platformEval(win, "history-tab", "location.hash='part';location.href", 1000); !strings.Contains(got, "#part") {
		t.Fatalf("fragment navigation failed: %s", got)
	}
	result, err = ExecuteAITool("browser.go_back", map[string]any{})
	if err != nil || urlOf(result) != server.URL+"/b" {
		t.Fatalf("go_back should leave the fragment, got %#v %v", result, err)
	}
}

// A same-origin iframe's button is listed with its frame path, clickable through
// its ref, and inspected in top-viewport coordinates, and a cross-origin iframe
// is listed by origin only, in a real WKWebView.
func TestDarwinSnapshotListsAndClicksSameOriginIframeButton(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/frame" {
			_, _ = w.Write([]byte(`<button onclick="window.clicked=(window.clicked||0)+1">Inside</button>`))
			return
		}
		// localhost and 127.0.0.1 are different origins, so the second iframe is cross-origin.
		crossOrigin := "http://localhost:" + strings.Split(r.Host, ":")[1]
		_, _ = w.Write([]byte(`<button>Top</button><iframe src="/frame" style="margin-left:40px;width:300px;height:120px;border:5px solid"></iframe>` +
			`<iframe src="` + crossOrigin + `/frame?token=secret"></iframe>`))
	}))
	t.Cleanup(server.Close)
	win := mountDarwinTestTab(t, "iframe-tab", server.URL+"/")
	setTestSharedAIAccess(t, []string{"iframe-tab"}, "iframe-tab")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	waitForPageReady(t, win, "iframe-tab")
	const frameButton = "document.querySelector('iframe').contentDocument.querySelector('button')"
	// Until it commits, the cross-origin iframe still shows its same-origin initial about:blank document.
	waitFor(t, 3*time.Second, "both iframes to load", func() bool {
		return platformEval(win, "iframe-tab", frameButton+"&&document.querySelectorAll('iframe')[1].contentDocument===null?'loaded':'no'", 1000) == `{"ok":true,"value":"loaded"}`
	})

	snapshot, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	fields, _ := snapshot.(map[string]any)
	snapshotID, _ := fields["snapshotId"].(string)
	var inside map[string]any
	elements, _ := fields["elements"].([]any)
	for _, raw := range elements {
		if element, _ := raw.(map[string]any); element["name"] == "Inside" {
			inside = element
		}
	}
	if inside == nil || inside["frame"] != "0" || inside["inViewport"] != true || inside["tag"] != "button" {
		t.Fatalf("the iframe button should be listed with frame 0 and inViewport, got %+v", snapshot)
	}
	ref, _ := inside["ref"].(string)
	var crossOrigin map[string]any
	for _, raw := range elements {
		if element, _ := raw.(map[string]any); element["crossOrigin"] == true {
			crossOrigin = element
		}
	}
	if wantOrigin := "http://localhost:" + strings.Split(server.URL, ":")[2]; crossOrigin == nil || crossOrigin["tag"] != "iframe" || crossOrigin["origin"] != wantOrigin {
		t.Fatalf("the cross-origin iframe should be listed with origin %s only, got %+v", wantOrigin, snapshot)
	}
	if encoded, _ := json.Marshal(snapshot); strings.Contains(string(encoded), "secret") {
		t.Fatalf("the snapshot must not reveal the cross-origin iframe URL: %s", encoded)
	}

	inspected, err := ExecuteAITool("browser.inspect_element", map[string]any{"snapshotId": snapshotID, "ref": ref})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	rect, _ := inspected.(map[string]any)["rect"].(map[string]any)
	want := platformEval(win, "iframe-tab", "(()=>{const f=document.querySelector('iframe'),b="+frameButton+".getBoundingClientRect(),r=f.getBoundingClientRect();return [Math.round(r.left+f.clientLeft+b.left),Math.round(r.top+f.clientTop+b.top)]})()", 1000)
	if got := fmt.Sprintf(`{"ok":true,"value":[%v,%v]}`, rect["x"], rect["y"]); got != want {
		t.Fatalf("inspect rect should be in top-viewport coordinates: got %s, want %s", got, want)
	}

	if _, err := ExecuteAITool("browser.click", map[string]any{"snapshotId": snapshotID, "ref": ref}); err != nil {
		t.Fatalf("click: %v", err)
	}
	if got := platformEval(win, "iframe-tab", "document.querySelector('iframe').contentWindow.clicked", 1000); got != `{"ok":true,"value":1}` {
		t.Fatalf("the iframe button should have been clicked once, got %s", got)
	}
}

// browser.extract_content reads the article (not the navigation, hidden text or
// form values) as Markdown with sanitized links, appends a same-origin iframe's
// article, and can be scoped to a snapshot ref, in a real WKWebView.
func TestDarwinExtractContentReadsTheArticleAsMarkdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/frame" {
			_, _ = w.Write([]byte(`<nav>Frame nav</nav><article><p>Frame article text</p></article>`))
			return
		}
		_, _ = w.Write([]byte(`<title>Extract test</title><style>.gone{display:none}</style>` +
			`<nav><a href="/home">Home</a></nav>` +
			`<article><h1>Heading</h1><p>Read <a href="/docs?token=secret#top">the docs</a>.</p>` +
			`<p class=gone>Hidden by CSS</p><input value="typed secret"><input type=password value=hunter2>` +
			`<ul><li>One</li><li>Two</li></ul>` +
			`<table><tr><th>K</th><th>V</th></tr><tr><td>a</td><td>1</td></tr></table>` +
			`<iframe src="/frame"></iframe></article><footer>Copyright</footer>`))
	}))
	t.Cleanup(server.Close)
	win := mountDarwinTestTab(t, "extract-tab", server.URL+"/")
	setTestSharedAIAccess(t, []string{"extract-tab"}, "extract-tab")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	waitForPageReady(t, win, "extract-tab")
	waitFor(t, 3*time.Second, "the iframe to load", func() bool {
		return platformEval(win, "extract-tab", "document.querySelector('iframe').contentDocument.querySelector('article')?'loaded':'no'", 1000) == `{"ok":true,"value":"loaded"}`
	})

	result, err := ExecuteAITool("browser.extract_content", map[string]any{})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	fields, _ := result.(map[string]any)
	want := "# Heading\n\nRead [the docs](" + server.URL + "/docs).\n\n- One\n- Two\n\n| K | V |\n| --- | --- |\n| a | 1 |\n\nFrame article text"
	if fields["content"] != want || fields["title"] != "Extract test" || fields["truncated"] != false {
		t.Fatalf("unexpected extraction:\n got %#v\nwant content %q", fields, want)
	}
	if encoded, _ := json.Marshal(result); strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "hunter2") {
		t.Fatalf("extraction must not reveal query strings or form values: %s", encoded)
	}

	text, err := ExecuteAITool("browser.extract_content", map[string]any{"format": "text"})
	if err != nil || text.(map[string]any)["content"] != "Heading\n\nRead the docs.\n\nOne\nTwo\n\nK\tV\na\t1\n\nFrame article text" {
		t.Fatalf("unexpected text extraction: %#v %v", text, err)
	}

	snapshot, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapshotFields, _ := snapshot.(map[string]any)
	var home string
	for _, raw := range snapshotFields["elements"].([]any) {
		if element, _ := raw.(map[string]any); element["name"] == "Home" {
			home, _ = element["ref"].(string)
		}
	}
	scoped, err := ExecuteAITool("browser.extract_content", map[string]any{"snapshotId": snapshotFields["snapshotId"], "ref": home})
	if err != nil || scoped.(map[string]any)["content"] != "[Home]("+server.URL+"/home)" {
		t.Fatalf("a ref restricts extraction to that element, got %#v %v", scoped, err)
	}
}

// A closed tab's id must not resolve to the tab that is left: eval, URL and
// navigation for it fail instead of acting on another page.
func TestDarwinClosedTabDoesNotResolveToRemainingTab(t *testing.T) {
	win := mountDarwinTestTab(t, "kept-tab", "about:blank")
	if err := platformMount(win, "closed-tab", "about:blank", "", 0, 0, 640, 480); err != nil {
		t.Fatalf("mount second tab: %v", err)
	}
	platformCloseTab(win, "closed-tab")
	var value any
	if err := evalValue(win, "closed-tab", "1 + 1", 1000, &value); err == nil {
		t.Fatalf("eval on a closed tab ran in another tab and returned %v", value)
	}
	if err := evalValue(win, "kept-tab", "1 + 1", 1000, &value); err != nil || value != float64(2) {
		t.Fatalf("eval on the remaining tab: %v, %v", value, err)
	}
}

// A failed load names the address that failed (without credentials), not the
// page the tab was showing before.
func TestDarwinNavigationErrorNamesFailingAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/reset" {
			if hijacker, ok := w.(http.Hijacker); ok {
				if conn, _, err := hijacker.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
		}
		_, _ = w.Write([]byte("<!doctype html><title>ok</title>"))
	}))
	t.Cleanup(server.Close)
	win := mountDarwinTestTab(t, "failing-tab", server.URL+"/start")
	waitForPageReady(t, win, "failing-tab")
	failing := strings.Replace(server.URL, "http://", "http://user:secret@", 1) + "/reset"
	platformNavigate(win, "failing-tab", failing)
	var items []map[string]any
	waitFor(t, 10*time.Second, "a navigation error", func() bool {
		items = nil
		_ = json.Unmarshal([]byte(platformNavigationErrors(win, "failing-tab")), &items)
		return len(items) > 0
	})
	got, _ := items[len(items)-1]["url"].(string)
	if got != server.URL+"/reset" {
		t.Fatalf("navigation error url = %q, want %q", got, server.URL+"/reset")
	}
}

// An AI click whose handler calls confirm() stops inside evaluateJavaScript
// until the dialog is answered. The click must come back as dialog_pending at
// once (the click was delivered), not as a code-less eval timeout after 3 s.
func TestDarwinAIClickThatOpensConfirmReturnsDialogPending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<button onclick="window.r=confirm('ok?')">Ask</button>`))
	}))
	defer server.Close()
	win := mountDarwinTestTab(t, "confirm-click-tab", server.URL+"/")
	setTestSharedAIAccess(t, []string{"confirm-click-tab"}, "confirm-click-tab")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	waitForPageReady(t, win, "confirm-click-tab")
	snapshot, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	fields, _ := snapshot.(map[string]any)
	snapshotID, _ := fields["snapshotId"].(string)
	ref := ""
	for _, raw := range fields["elements"].([]any) {
		if element, _ := raw.(map[string]any); element["tag"] == "button" {
			ref, _ = element["ref"].(string)
		}
	}
	if ref == "" {
		t.Fatalf("snapshot lists no button: %+v", snapshot)
	}

	started := time.Now()
	_, err = ExecuteAITool("browser.click", map[string]any{"snapshotId": snapshotID, "ref": ref})
	elapsed := time.Since(started)
	toolErr := requireToolErrorCode(t, err, errCodeDialogPending)
	if elapsed > time.Second {
		t.Fatalf("click returned after %s; it must return as soon as the confirm opens", elapsed)
	}
	if toolErr.Details["actionDelivered"] != true {
		t.Fatalf("details.actionDelivered must be true, got %+v", toolErr.Details)
	}
	d, _ := toolErr.Details["dialog"].(pendingDialog)
	if d.Kind != dialogKindConfirm || d.Message != "ok?" {
		t.Fatalf("details.dialog must describe the confirm, got %+v", toolErr.Details["dialog"])
	}
	t.Logf("click returned dialog_pending after %s", elapsed)

	if _, err := ExecuteAITool("browser.handle_dialog", map[string]any{"accept": true, "dialogId": d.ID}); err != nil {
		t.Fatalf("handle_dialog: %v", err)
	}
	waitFor(t, 3*time.Second, "window.r to be true", func() bool {
		got := platformEval(win, "confirm-click-tab", "window.r === true ? 'yes' : 'no'", 1000)
		return got == `{"ok":true,"value":"yes"}`
	})
	// The page answers scripts again once the dialog is gone.
	if _, err := ExecuteAITool("browser.get_page_snapshot", map[string]any{}); err != nil {
		t.Fatalf("snapshot after the dialog: %v", err)
	}
}
