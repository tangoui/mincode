package browser

import (
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"mincode/app/backend/core"
)

type dialogHookCall struct {
	TabID    string
	DialogID string
	Accept   bool
	Text     string
}

type recordedEvent struct {
	Name string
	Data any
}

// stubDialogPlatform replaces the native dialog hook and the event emitter and
// clears pending dialogs when the test ends.
func stubDialogPlatform(t *testing.T) (calls func() []dialogHookCall, events func() []recordedEvent) {
	t.Helper()
	var mu sync.Mutex
	var hookCalls []dialogHookCall
	var emitted []recordedEvent
	original, originalSupported := platformCompleteDialog, platformPageDialogsSupported
	platformPageDialogsSupported = func() bool { return true }
	platformCompleteDialog = func(_ unsafe.Pointer, tabID, dialogID string, accept bool, text string) error {
		mu.Lock()
		defer mu.Unlock()
		hookCalls = append(hookCalls, dialogHookCall{tabID, dialogID, accept, text})
		return nil
	}
	core.SetEmitter(func(name string, data any) {
		mu.Lock()
		defer mu.Unlock()
		emitted = append(emitted, recordedEvent{name, data})
	})
	resetPendingDialogs()
	t.Cleanup(func() {
		platformCompleteDialog = original
		platformPageDialogsSupported = originalSupported
		core.SetEmitter(nil)
		resetPendingDialogs()
	})
	return func() []dialogHookCall {
			mu.Lock()
			defer mu.Unlock()
			return append([]dialogHookCall{}, hookCalls...)
		}, func() []recordedEvent {
			mu.Lock()
			defer mu.Unlock()
			return append([]recordedEvent{}, emitted...)
		}
}

func TestOpenDialogRecordsAndEmits(t *testing.T) {
	_, events := stubDialogPlatform(t)
	openDialog(pendingDialog{ID: "d1", TabID: "a", Kind: "confirm", Message: "Leave?", Origin: "https://example.com"})
	d, ok := dialogForTab("a")
	if !ok || d.ID != "d1" || d.Kind != "confirm" || d.Message != "Leave?" || d.OpenedAt == 0 {
		t.Fatalf("unexpected pending dialog %+v ok=%v", d, ok)
	}
	got := events()
	if len(got) != 1 || got[0].Name != "browser:dialog-opened" {
		t.Fatalf("expected one browser:dialog-opened event, got %+v", got)
	}
	encoded, _ := json.Marshal(got[0].Data)
	for _, key := range []string{`"id":"d1"`, `"tabId":"a"`, `"kind":"confirm"`, `"origin":"https://example.com"`, `"openedAt":`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("dialog-opened payload %s is missing %s", encoded, key)
		}
	}
}

func TestOpenDialogHidesLocalFilePaths(t *testing.T) {
	stubDialogPlatform(t)
	openDialog(pendingDialog{ID: "d1", TabID: "a", Kind: "alert", Origin: "file:///Users/someone/secret/page.html"})
	d, _ := dialogForTab("a")
	if strings.Contains(d.Origin, "Users") || d.Origin != "file://" {
		t.Fatalf("file origin must not carry a local path, got %q", d.Origin)
	}
}

func TestCompleteDialogOnlyOnce(t *testing.T) {
	calls, events := stubDialogPlatform(t)
	openDialog(pendingDialog{ID: "d1", TabID: "a", Kind: "prompt", Message: "Name?", DefaultText: "x"})

	if err := completeDialog("a", "d1", true, "Ada", "user"); err != nil {
		t.Fatalf("first completion failed: %v", err)
	}
	err := completeDialog("a", "d1", false, "", "ai")
	requireToolErrorCode(t, err, errCodeDialogAlreadyHandled)

	got := calls()
	if len(got) != 1 || got[0] != (dialogHookCall{"a", "d1", true, "Ada"}) {
		t.Fatalf("platform hook must run exactly once with the first answer, got %+v", got)
	}
	if _, ok := dialogForTab("a"); ok {
		t.Fatal("dialog must not stay pending after completion")
	}
	var closed []recordedEvent
	for _, event := range events() {
		if event.Name == "browser:dialog-closed" {
			closed = append(closed, event)
		}
	}
	if len(closed) != 1 {
		t.Fatalf("expected exactly one dialog-closed event, got %+v", closed)
	}
	encoded, _ := json.Marshal(closed[0].Data)
	for _, key := range []string{`"tabId":"a"`, `"dialogId":"d1"`, `"by":"user"`, `"accepted":true`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("dialog-closed payload %s is missing %s", encoded, key)
		}
	}
}

func TestCompleteDialogConcurrentRespondersOnlyOneWins(t *testing.T) {
	calls, _ := stubDialogPlatform(t)
	openDialog(pendingDialog{ID: "d1", TabID: "a", Kind: "confirm"})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, by := range []string{"user", "ai"} {
		wg.Add(1)
		go func(by string) {
			defer wg.Done()
			results <- completeDialog("a", "d1", true, "", by)
		}(by)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		requireToolErrorCode(t, err, errCodeDialogAlreadyHandled)
	}
	if successes != 1 || len(calls()) != 1 {
		t.Fatalf("expected one winner and one hook call, got successes=%d calls=%d", successes, len(calls()))
	}
}

func TestCompleteDialogRejectsStaleDialogID(t *testing.T) {
	calls, _ := stubDialogPlatform(t)
	openDialog(pendingDialog{ID: "d2", TabID: "a", Kind: "confirm"})
	requireToolErrorCode(t, completeDialog("a", "d1", true, "", "user"), errCodeDialogAlreadyHandled)
	if len(calls()) != 0 {
		t.Fatal("a stale dialog id must not reach the platform hook")
	}
	if _, ok := dialogForTab("a"); !ok {
		t.Fatal("the current dialog must stay pending")
	}
}

func TestCancelDialogsForTabCompletesPending(t *testing.T) {
	calls, events := stubDialogPlatform(t)
	openDialog(pendingDialog{ID: "c1", TabID: "a", Kind: "confirm", Message: "Sure?"})
	openDialog(pendingDialog{ID: "al1", TabID: "b", Kind: "alert", Message: "Hi"})

	cancelDialogsForTab("a")
	cancelDialogsForTab("b")
	cancelDialogsForTab("missing")

	got := calls()
	want := []dialogHookCall{{"a", "c1", false, ""}, {"b", "al1", true, ""}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected confirm cancelled and alert acknowledged, got %+v", got)
	}
	for _, tabID := range []string{"a", "b"} {
		if _, ok := dialogForTab(tabID); ok {
			t.Fatalf("tab %s still has a pending dialog", tabID)
		}
	}
	for _, event := range events() {
		if event.Name != "browser:dialog-closed" {
			continue
		}
		encoded, _ := json.Marshal(event.Data)
		if !strings.Contains(string(encoded), `"by":"system"`) {
			t.Fatalf("cancellation must be reported as by=system, got %s", encoded)
		}
	}
}

func TestOpenDialogReplacingPendingReleasesPrevious(t *testing.T) {
	calls, _ := stubDialogPlatform(t)
	openDialog(pendingDialog{ID: "old", TabID: "a", Kind: "confirm"})
	openDialog(pendingDialog{ID: "new", TabID: "a", Kind: "prompt"})
	got := calls()
	if len(got) != 1 || got[0] != (dialogHookCall{"a", "old", false, ""}) {
		t.Fatalf("previous dialog must be released as cancelled, got %+v", got)
	}
	if d, _ := dialogForTab("a"); d.ID != "new" {
		t.Fatalf("expected the new dialog to be pending, got %+v", d)
	}
}

func TestDialogGateBlocksNonAllowlistedTools(t *testing.T) {
	stubDialogPlatform(t)
	useTestHostWindow(t)
	setTestAIAccess(true, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	openDialog(pendingDialog{ID: "d1", TabID: "a", Kind: "confirm", Message: "Delete everything?", Origin: "https://example.com"})

	for _, name := range []string{"browser.click", "browser.get_page_snapshot", "browser.get_console_logs", "browser.navigate", "browser.evaluate", "browser.wait_for"} {
		_, err := ExecuteAITool(name, map[string]any{"snapshotId": "s", "ref": "e1", "url": "https://example.com"})
		toolErr := requireToolErrorCode(t, err, errCodeDialogPending)
		dialog, ok := toolErr.Details["dialog"].(pendingDialog)
		if !ok || dialog.ID != "d1" || dialog.Kind != "confirm" || dialog.Message != "Delete everything?" {
			t.Fatalf("%s: dialog_pending details must carry the dialog, got %#v", name, toolErr.Details)
		}
		if notice, _ := toolErr.Details["notice"].(string); !strings.Contains(notice, "web page") {
			t.Fatalf("%s: dialog details must mark the text as page content, got %#v", name, toolErr.Details)
		}
	}

	for _, name := range []string{"browser.handle_dialog", "browser.get_url", "browser.get_navigation_errors", "browser.list_tabs", "browser.get_downloads", "browser.capture_view"} {
		if err := dialogGate(name, "a"); err != nil {
			t.Fatalf("%s must not be blocked by a pending dialog: %v", name, err)
		}
	}
	if err := dialogGate("browser.click", "other"); err != nil {
		t.Fatalf("a dialog on tab a must not block tab other: %v", err)
	}

	// The gate runs before any page script, so the encoded error carries the dialog too.
	_, err := handleAIExecute("", `{"toolName":"browser.click","arguments":{"snapshotId":"s","ref":"e1"}}`)
	var decoded struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	if err == nil || json.Unmarshal([]byte(err.Error()), &decoded) != nil || decoded.Code != errCodeDialogPending {
		t.Fatalf("expected encoded dialog_pending error, got %v", err)
	}
	if dialog, _ := decoded.Details["dialog"].(map[string]any); dialog["id"] != "d1" || dialog["origin"] != "https://example.com" {
		t.Fatalf("encoded details must include the dialog, got %v", decoded.Details)
	}
}

func TestHandleDialogRequiresPending(t *testing.T) {
	calls, _ := stubDialogPlatform(t)
	useTestHostWindow(t)
	setTestAIAccess(true, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })

	_, err := ExecuteAITool("browser.handle_dialog", map[string]any{"accept": true})
	requireToolErrorCode(t, err, errCodeDialogAlreadyHandled)
	if len(calls()) != 0 {
		t.Fatal("no hook call expected without a pending dialog")
	}
}

func TestHandleDialogAnswersPendingPrompt(t *testing.T) {
	calls, events := stubDialogPlatform(t)
	useTestHostWindow(t)
	setTestAIAccess(true, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })

	if _, err := ExecuteAITool("browser.handle_dialog", map[string]any{}); err == nil || !strings.Contains(err.Error(), "accept") {
		t.Fatalf("handle_dialog without accept must fail clearly, got %v", err)
	}

	openDialog(pendingDialog{ID: "p1", TabID: "a", Kind: "prompt", Message: "Name?", DefaultText: "guest"})
	result, err := ExecuteAITool("browser.handle_dialog", map[string]any{"accept": true})
	if err != nil {
		t.Fatalf("handle_dialog failed: %v", err)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"dialogId":"p1"`) || !strings.Contains(string(encoded), `"accepted":true`) {
		t.Fatalf("unexpected handle_dialog result %s", encoded)
	}
	if got := calls(); len(got) != 1 || got[0] != (dialogHookCall{"a", "p1", true, "guest"}) {
		t.Fatalf("accepting a prompt without promptText must answer with its default text, got %+v", got)
	}

	openDialog(pendingDialog{ID: "p2", TabID: "a", Kind: "prompt", DefaultText: "guest"})
	if _, err := ExecuteAITool("browser.handle_dialog", map[string]any{"accept": true, "promptText": "Ada"}); err != nil {
		t.Fatalf("handle_dialog with promptText failed: %v", err)
	}
	openDialog(pendingDialog{ID: "c1", TabID: "a", Kind: "confirm"})
	if _, err := ExecuteAITool("browser.handle_dialog", map[string]any{"accept": false, "promptText": "ignored"}); err != nil {
		t.Fatalf("handle_dialog on confirm failed: %v", err)
	}
	got := calls()
	if got[1] != (dialogHookCall{"a", "p2", true, "Ada"}) || got[2] != (dialogHookCall{"a", "c1", false, ""}) {
		t.Fatalf("unexpected hook calls %+v", got)
	}
	for _, event := range events() {
		if event.Name == "browser:dialog-closed" {
			if encoded, _ := json.Marshal(event.Data); !strings.Contains(string(encoded), `"by":"ai"`) {
				t.Fatalf("AI answers must be reported as by=ai, got %s", encoded)
			}
		}
	}
}

func TestCompleteDialogCommandAnswersAsUser(t *testing.T) {
	calls, events := stubDialogPlatform(t)
	openDialog(pendingDialog{ID: "d1", TabID: "a", Kind: "confirm"})

	if _, err := handleCompleteDialog("", `{"payload":{"tabId":"a","dialogId":"d1","accept":true,"text":""}}`); err != nil {
		t.Fatalf("complete dialog command failed: %v", err)
	}
	if got := calls(); len(got) != 1 || got[0] != (dialogHookCall{"a", "d1", true, ""}) {
		t.Fatalf("unexpected hook calls %+v", got)
	}
	found := false
	for _, event := range events() {
		if encoded, _ := json.Marshal(event.Data); event.Name == "browser:dialog-closed" && strings.Contains(string(encoded), `"by":"user"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("the command must report by=user")
	}

	_, err := handleCompleteDialog("", `{"payload":{"tabId":"a","dialogId":"d1","accept":false}}`)
	var decoded map[string]any
	if err == nil || json.Unmarshal([]byte(err.Error()), &decoded) != nil || decoded["code"] != errCodeDialogAlreadyHandled {
		t.Fatalf("second answer must return encoded dialog_already_handled, got %v", err)
	}
}

func TestGetDialogCommandReturnsPendingDialog(t *testing.T) {
	stubDialogPlatform(t)
	raw, err := handleGetDialog("", `{"payload":{"tabId":"a"}}`)
	if err != nil || raw != "null" {
		t.Fatalf("expected null without a dialog, got %q %v", raw, err)
	}
	openDialog(pendingDialog{ID: "d1", TabID: "a", Kind: "alert", Message: "Hi"})
	raw, err = handleGetDialog("", `{"payload":{"tabId":"a"}}`)
	if err != nil || !strings.Contains(raw, `"id":"d1"`) || !strings.Contains(raw, `"kind":"alert"`) {
		t.Fatalf("expected the pending dialog, got %q %v", raw, err)
	}
}

func TestCompleteDialogReportsPlatformFailure(t *testing.T) {
	stubDialogPlatform(t)
	platformCompleteDialog = func(unsafe.Pointer, string, string, bool, string) error {
		return errors.New("handler gone")
	}
	openDialog(pendingDialog{ID: "d1", TabID: "a", Kind: "confirm"})
	if err := completeDialog("a", "d1", true, "", "user"); err == nil || !strings.Contains(err.Error(), "handler gone") {
		t.Fatalf("expected the platform error, got %v", err)
	}
	if _, ok := dialogForTab("a"); ok {
		t.Fatal("a dialog whose native handler is gone must not keep gating tools")
	}
}

func TestPlatformCompleteDialogDefaultIsUnsupportedOffNativePlatforms(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("macOS completes dialogs through the native overlay")
	}
	err := platformCompleteDialog(nil, "a", "d1", true, "")
	requireToolErrorCode(t, err, errCodeUnsupportedOnPlatform)
}

func TestHandleDialogRejectsMismatchedDialogID(t *testing.T) {
	calls, _ := stubDialogPlatform(t)
	useTestHostWindow(t)
	setTestAIAccess(true, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })

	openDialog(pendingDialog{ID: "later", TabID: "a", Kind: "confirm", Message: "Delete?"})
	_, err := ExecuteAITool("browser.handle_dialog", map[string]any{"accept": true, "dialogId": "approved"})
	requireToolErrorCode(t, err, errCodeDialogAlreadyHandled)
	if len(calls()) != 0 {
		t.Fatal("a mismatched dialogId must not answer the pending dialog")
	}
	if _, ok := dialogForTab("a"); !ok {
		t.Fatal("the later dialog must stay pending")
	}

	result, err := ExecuteAITool("browser.handle_dialog", map[string]any{"accept": false, "dialogId": "later"})
	if err != nil {
		t.Fatalf("handle_dialog with the matching dialogId failed: %v", err)
	}
	if encoded, _ := json.Marshal(result); !strings.Contains(string(encoded), `"dialogId":"later"`) {
		t.Fatalf("unexpected result %s", encoded)
	}
	if got := calls(); len(got) != 1 || got[0] != (dialogHookCall{"a", "later", false, ""}) {
		t.Fatalf("unexpected hook calls %+v", got)
	}
}

func TestHandleDialogRejectsOverlongPromptText(t *testing.T) {
	calls, _ := stubDialogPlatform(t)
	useTestHostWindow(t)
	setTestAIAccess(true, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })

	openDialog(pendingDialog{ID: "p1", TabID: "a", Kind: "prompt"})
	_, err := ExecuteAITool("browser.handle_dialog", map[string]any{"accept": true, "promptText": strings.Repeat("字", 2001)})
	if err == nil || !strings.Contains(err.Error(), "2000") {
		t.Fatalf("promptText over 2000 characters must be rejected clearly, got %v", err)
	}
	if len(calls()) != 0 {
		t.Fatal("an overlong promptText must not answer the dialog")
	}
	if _, err := ExecuteAITool("browser.handle_dialog", map[string]any{"accept": true, "promptText": strings.Repeat("字", 2000)}); err != nil {
		t.Fatalf("2000 characters must be accepted: %v", err)
	}
}

func TestHandleDialogIsUnsupportedWhereThePlatformKeepsDialogs(t *testing.T) {
	calls, _ := stubDialogPlatform(t)
	platformPageDialogsSupported = func() bool { return false }
	useTestHostWindow(t)
	setTestAIAccess(true, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })

	// Checked first: with or without a pending dialog, and before argument checks.
	_, err := ExecuteAITool("browser.handle_dialog", map[string]any{"accept": true})
	requireToolErrorCode(t, err, errCodeUnsupportedOnPlatform)
	_, err = ExecuteAITool("browser.handle_dialog", map[string]any{})
	requireToolErrorCode(t, err, errCodeUnsupportedOnPlatform)
	if len(calls()) != 0 {
		t.Fatal("an unsupported platform must not reach the native hook")
	}
}

func TestPageDialogsSupportedOnNativePlatforms(t *testing.T) {
	if got, want := platformPageDialogsSupported(), runtime.GOOS == "darwin" || runtime.GOOS == "windows"; got != want {
		t.Fatalf("platformPageDialogsSupported() = %v on %s, want %v", got, runtime.GOOS, want)
	}
}

// stubBlockingEval makes evalValue's platform eval behave like a page whose
// script opens a dialog: it records the dialog, then blocks until release (or
// its own timeout) like WebKit's evaluateJavaScript does.
func stubBlockingEval(t *testing.T, open *pendingDialog) (release func(), finished <-chan struct{}) {
	t.Helper()
	gate := make(chan struct{})
	done := make(chan struct{})
	original := evalPlatform
	evalPlatform = func(_ unsafe.Pointer, _, _ string, timeout int) string {
		defer close(done)
		if open != nil {
			openDialog(*open)
		}
		select {
		case <-gate:
			return `{"ok":true,"value":"late"}`
		case <-time.After(time.Duration(timeout) * time.Millisecond):
			return `{"ok":false,"error":"eval timeout"}`
		}
	}
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() { close(gate) })
		<-done
		evalPlatform = original
	})
	return func() { once.Do(func() { close(gate) }) }, done
}

func TestEvalReturnsDialogPendingAsSoonAsTheScriptOpensADialog(t *testing.T) {
	stubDialogPlatform(t)
	release, finished := stubBlockingEval(t, &pendingDialog{ID: "c1", TabID: "a", Kind: "confirm", Message: "ok?"})
	started := time.Now()
	var result any
	err := evalMutation(nil, "a", "button.click()", 3000, &result)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("evalValue waited %s; it must return when the dialog opens, not at the timeout", elapsed)
	}
	requireToolErrorCode(t, err, errCodeDialogPending)
	var toolErr *browserToolError
	errors.As(err, &toolErr)
	if toolErr.Details["actionDelivered"] != true {
		t.Fatalf("details.actionDelivered must be true, got %+v", toolErr.Details)
	}
	if d, _ := toolErr.Details["dialog"].(pendingDialog); d.ID != "c1" || d.Kind != "confirm" {
		t.Fatalf("details.dialog must describe the open dialog, got %+v", toolErr.Details["dialog"])
	}
	// The native eval finishes once the dialog is answered; its result is dropped.
	release()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the platform eval goroutine did not finish after the dialog was answered")
	}
	pageDialogs.Lock()
	watchers := len(pageDialogs.watchers)
	pageDialogs.Unlock()
	if watchers != 0 {
		t.Fatalf("evalValue must stop watching for dialogs when it returns, %d watcher tabs left", watchers)
	}
}

func TestReadOnlyEvalInterruptedByDialogDoesNotClaimDelivery(t *testing.T) {
	stubDialogPlatform(t)
	stubBlockingEval(t, &pendingDialog{ID: "c2", TabID: "a", Kind: "alert", Message: "timer"})
	err := evalValue(nil, "a", "document.title", 3000, nil)
	requireToolErrorCode(t, err, errCodeDialogPending)
	var toolErr *browserToolError
	errors.As(err, &toolErr)
	if toolErr.Details["actionDelivered"] != false {
		t.Fatalf("a read-only script delivers no action; details.actionDelivered must be false, got %+v", toolErr.Details)
	}
	if strings.Contains(toolErr.Message, "action was delivered") {
		t.Fatalf("read-only interruption must not say the action was delivered: %q", toolErr.Message)
	}
}

func TestEvalFailureWithPendingDialogKeepsTheDeliveryFlag(t *testing.T) {
	stubDialogPlatform(t)
	original := evalPlatform
	evalPlatform = func(_ unsafe.Pointer, tabID, _ string, _ int) string {
		pageDialogs.Lock()
		pageDialogs.pending[tabID] = pendingDialog{ID: "p2", TabID: tabID, Kind: "confirm"}
		pageDialogs.Unlock()
		return `{"ok":false,"error":"eval timeout"}`
	}
	t.Cleanup(func() { evalPlatform = original })
	for _, tc := range []struct {
		name string
		run  func() error
		want bool
	}{
		{"read", func() error { return evalValue(nil, "a", "1", 3000, nil) }, false},
		{"mutation", func() error { return evalMutation(nil, "a", "1", 3000, nil) }, true},
	} {
		pageDialogs.Lock()
		delete(pageDialogs.pending, "a")
		pageDialogs.Unlock()
		err := tc.run()
		var toolErr *browserToolError
		if !errors.As(err, &toolErr) || toolErr.Details["actionDelivered"] != tc.want {
			t.Fatalf("%s: actionDelivered must be %v, got %v", tc.name, tc.want, err)
		}
	}
}

func TestEvalIgnoresDialogsOnOtherTabs(t *testing.T) {
	stubDialogPlatform(t)
	release, _ := stubBlockingEval(t, &pendingDialog{ID: "c1", TabID: "other", Kind: "confirm"})
	go func() { time.Sleep(100 * time.Millisecond); release() }()
	var result string
	if err := evalValue(nil, "a", "1", 3000, &result); err != nil || result != "late" {
		t.Fatalf("a dialog on another tab must not interrupt this tab's eval, got %q, %v", result, err)
	}
}

func TestEvalFailureWithPendingDialogReportsTheDialog(t *testing.T) {
	stubDialogPlatform(t)
	original := evalPlatform
	evalPlatform = func(_ unsafe.Pointer, tabID, _ string, _ int) string {
		// The dialog is recorded but the watcher signal is lost: only the timeout comes back.
		pageDialogs.Lock()
		pageDialogs.pending[tabID] = pendingDialog{ID: "p1", TabID: tabID, Kind: "prompt"}
		pageDialogs.Unlock()
		return `{"ok":false,"error":"eval timeout"}`
	}
	t.Cleanup(func() { evalPlatform = original })
	err := evalValue(nil, "a", "1", 3000, nil)
	requireToolErrorCode(t, err, errCodeDialogPending)
}
