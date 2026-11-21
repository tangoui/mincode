package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"mincode/app/backend/core"
)

func setTestAIAccess(enabled bool, tabID string) {
	payload, _ := json.Marshal(map[string]any{"enabled": enabled, "tabId": tabID})
	if _, err := handleSetAIAccess("", string(payload)); err != nil {
		panic(err)
	}
}

func setTestSharedAIAccess(t *testing.T, shared []string, target string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"sharedTabIds": shared, "targetTabId": target})
	if _, err := handleSetAIAccess("", string(payload)); err != nil {
		t.Fatalf("set AI access: %v", err)
	}
}

func requireToolErrorCode(t *testing.T, err error, code string) *browserToolError {
	t.Helper()
	var toolErr *browserToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected browserToolError with code %q, got %T %v", code, err, err)
	}
	if toolErr.Code != code {
		t.Fatalf("expected error code %q, got %q (%s)", code, toolErr.Code, toolErr.Message)
	}
	return toolErr
}

func useTestHostWindow(t *testing.T) {
	t.Helper()
	var sentinel byte
	SetNativeWindowProvider(func() unsafe.Pointer { return unsafe.Pointer(&sentinel) })
	t.Cleanup(func() { SetNativeWindowProvider(nil) })
}

func TestSetAIAccessAcceptsSharedSet(t *testing.T) {
	setTestSharedAIAccess(t, []string{"a", "b"}, "b")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	if tabID, err := resolveAITab(map[string]any{}); err != nil || tabID != "b" {
		t.Fatalf("expected target tab b, got tab=%q err=%v", tabID, err)
	}
	if tabID, err := resolveAITab(map[string]any{"tabId": "a"}); err != nil || tabID != "a" {
		t.Fatalf("expected shared tab a, got tab=%q err=%v", tabID, err)
	}
	_, err := resolveAITab(map[string]any{"tabId": "c"})
	requireToolErrorCode(t, err, errCodeTabNotShared)
	shared, target := sharedAITabs()
	if strings.Join(shared, ",") != "a,b" || target != "b" {
		t.Fatalf("unexpected shared state shared=%v target=%q", shared, target)
	}
}

func TestSetAIAccessLegacyPayload(t *testing.T) {
	setTestAIAccess(true, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	if tabID, err := resolveAITab(map[string]any{}); err != nil || tabID != "a" {
		t.Fatalf("expected legacy tab a, got tab=%q err=%v", tabID, err)
	}
	if shared, target := sharedAITabs(); strings.Join(shared, ",") != "a" || target != "a" {
		t.Fatalf("unexpected legacy shared state shared=%v target=%q", shared, target)
	}
	setTestAIAccess(false, "a")
	_, err := resolveAITab(map[string]any{})
	requireToolErrorCode(t, err, errCodeTabNotShared)
}

func TestSetAIAccessDropsUnsharedTarget(t *testing.T) {
	setTestSharedAIAccess(t, []string{"a"}, "b")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	_, err := resolveAITab(map[string]any{})
	requireToolErrorCode(t, err, errCodeTabNotShared)
	if _, err := resolveAITab(map[string]any{"tabId": "b"}); err == nil {
		t.Fatal("target outside the shared set must not become reachable")
	}
}

func TestUnsharingTabRejectsLaterCalls(t *testing.T) {
	setTestSharedAIAccess(t, []string{"a"}, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	if _, err := resolveAITab(map[string]any{"tabId": "a"}); err != nil {
		t.Fatalf("expected shared tab to resolve, got %v", err)
	}
	setTestSharedAIAccess(t, []string{}, "")
	_, err := ExecuteAITool("browser.get_url", map[string]any{"tabId": "a"})
	requireToolErrorCode(t, err, errCodeTabNotShared)
	_, err = ExecuteAITool("browser.get_url", nil)
	requireToolErrorCode(t, err, errCodeTabNotShared)
}

func TestCaptureRequiresForegroundTab(t *testing.T) {
	useTestHostWindow(t)
	original := platformActiveTabID
	platformActiveTabID = func(unsafe.Pointer) string { return "b" }
	t.Cleanup(func() { platformActiveTabID = original })
	setTestSharedAIAccess(t, []string{"a", "b"}, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	_, err := ExecuteAITool("browser.capture_view", map[string]any{})
	requireToolErrorCode(t, err, errCodeTabNotVisible)
	_, err = ExecuteAITool("browser.computer_action", map[string]any{"actions": []any{map[string]any{"type": "wait"}}})
	requireToolErrorCode(t, err, errCodeTabNotVisible)
}

func TestRequireForegroundTabAllowsUnknownOrMatchingActiveTab(t *testing.T) {
	original := platformActiveTabID
	t.Cleanup(func() { platformActiveTabID = original })
	for _, active := range []string{"", "a"} {
		platformActiveTabID = func(unsafe.Pointer) string { return active }
		if err := requireForegroundTab(nil, "a"); err != nil {
			t.Fatalf("active=%q: expected no error, got %v", active, err)
		}
	}
}

func TestHandleAIExecuteEncodesToolErrorsAsJSON(t *testing.T) {
	setTestAIAccess(false, "")
	_, err := handleAIExecute("", `{"toolName":"browser.get_url","arguments":{"tabId":"x"}}`)
	if err == nil {
		t.Fatal("expected unshared tab error")
	}
	var decoded map[string]any
	if jsonErr := json.Unmarshal([]byte(err.Error()), &decoded); jsonErr != nil {
		t.Fatalf("expected JSON error text, got %q: %v", err.Error(), jsonErr)
	}
	if decoded["code"] != errCodeTabNotShared || decoded["message"] == "" {
		t.Fatalf("unexpected structured error %v", decoded)
	}
	if _, ok := decoded["details"].(map[string]any); !ok || len(decoded) != 3 {
		t.Fatalf("expected exactly code/message/details object, got %v", decoded)
	}

	useTestHostWindow(t)
	setTestAIAccess(true, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	_, err = handleAIExecute("", `{"toolName":"browser.nope","arguments":{}}`)
	if err == nil || err.Error() != "unknown browser AI tool: browser.nope" {
		t.Fatalf("expected ordinary errors to stay plain text, got %v", err)
	}
}

func TestActiveAITabRequiresExplicitSharing(t *testing.T) {
	setTestAIAccess(false, "tab-1")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	if _, err := activeAITab(); err == nil || !strings.Contains(err.Error(), "sharing is disabled") {
		t.Fatalf("expected sharing-disabled error, got %v", err)
	}
	setTestAIAccess(true, "")
	if _, err := activeAITab(); err == nil || !strings.Contains(err.Error(), "sharing is disabled") {
		t.Fatalf("expected an empty legacy tab to share nothing, got %v", err)
	}
	setTestSharedAIAccess(t, []string{"tab-1"}, "")
	_, err := activeAITab()
	if err == nil || !strings.Contains(err.Error(), "no active browser tab") {
		t.Fatalf("expected missing-tab error, got %v", err)
	}
	requireToolErrorCode(t, err, errCodeTabNotShared)
}

func TestResolveAITabRejectsAnotherTab(t *testing.T) {
	setTestAIAccess(true, "tab-shared")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	_, err := resolveAITab(map[string]any{"tabId": "tab-other"})
	requireToolErrorCode(t, err, errCodeTabNotShared)
	if tabID, err := resolveAITab(map[string]any{"tabId": "tab-shared"}); err != nil || tabID != "tab-shared" {
		t.Fatalf("expected shared tab, got tab=%q err=%v", tabID, err)
	}
}

func TestNumericArgumentClampsSnapshotLimits(t *testing.T) {
	if got := numericArgument(map[string]any{"maxText": json.Number("999999")}, "maxText", 12000, 1000, 50000); got != 50000 {
		t.Fatalf("expected max clamp, got %d", got)
	}
	if got := numericArgument(map[string]any{"maxElements": float64(-1)}, "maxElements", 120, 10, 500); got != 10 {
		t.Fatalf("expected min clamp, got %d", got)
	}
}

func TestBrowserURLFromEvalUsesDocumentLocation(t *testing.T) {
	raw := `{"ok":true,"value":"https://example.org/"}`
	if got := browserURLFromEval(raw); got != "https://example.org/" {
		t.Fatalf("expected document URL, got %q", got)
	}
	for _, invalid := range []string{`{"ok":false,"error":"not ready"}`, `{"ok":true,"value":null}`, `not-json`} {
		if got := browserURLFromEval(invalid); got != "" {
			t.Fatalf("expected invalid eval result to be ignored, got %q", got)
		}
	}
}

func TestBrowserReadyMatchesRejectsBlankAndStaleNavigationStates(t *testing.T) {
	tests := []struct {
		name       string
		state      browserReadyState
		expected   string
		previous   string
		shouldPass bool
	}{
		{name: "loading", state: browserReadyState{ReadyState: "loading", URL: "https://example.com/"}, expected: "https://example.com/"},
		{name: "initial about blank", state: browserReadyState{ReadyState: "complete", URL: "about:blank"}, expected: "https://example.com/"},
		{name: "stale previous page", state: browserReadyState{ReadyState: "complete", URL: "https://www.google.com/"}, expected: "https://example.com/", previous: "https://www.google.com/"},
		{name: "target page", state: browserReadyState{ReadyState: "complete", URL: "https://example.com/", Title: "Example Domain"}, expected: "https://example.com/", previous: "https://www.google.com/", shouldPass: true},
		{name: "redirected target", state: browserReadyState{ReadyState: "interactive", URL: "https://www.google.com.hk/"}, expected: "https://www.google.com/", previous: "about:blank", shouldPass: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := browserReadyMatches(test.state, test.expected, test.previous); got != test.shouldPass {
				t.Fatalf("expected pass=%v, got %v for state=%+v", test.shouldPass, got, test.state)
			}
		})
	}
}

func TestFillValueArgUsesCanonicalValueAndSupportsLegacyText(t *testing.T) {
	value, err := fillValueArg(map[string]any{"value": "  exact value  "})
	if err != nil || value != "  exact value  " {
		t.Fatalf("expected canonical value to preserve whitespace, got value=%q err=%v", value, err)
	}
	legacy, err := fillValueArg(map[string]any{"text": "legacy"})
	if err != nil || legacy != "legacy" {
		t.Fatalf("expected legacy text fallback, got value=%q err=%v", legacy, err)
	}
	if _, err := fillValueArg(map[string]any{}); err == nil || !strings.Contains(err.Error(), "value is required") {
		t.Fatalf("expected a missing-value error, got %v", err)
	}
	if _, err := fillValueArg(map[string]any{"value": 42}); err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("expected a type error, got %v", err)
	}
}

func TestSnapshotScriptsRejectStaleAndSensitiveReferences(t *testing.T) {
	snapshot := pageScript("snapshot", 12000, 200)
	inspect := pageScript("inspect", "snap-1", "e1")
	elementActions := map[string]string{
		"click":     pageScript("click", "snap-1", "e1"),
		"fill":      pageScript("fill", "snap-1", "e1", "value"),
		"press_key": pageScript("press_key", "snap-1", "e1", "Enter"),
	}
	for _, expected := range []string{"stale snapshot", "element reference is stale"} {
		if !strings.Contains(inspect, expected) {
			t.Fatalf("expected element inspection to contain %q guard", expected)
		}
		for name, script := range elementActions {
			if !strings.Contains(script, expected) {
				t.Fatalf("expected %s script to contain %q guard", name, expected)
			}
		}
	}
	for _, expected := range []string{"input:not([type=password])", "type==='password'", "type==='file'"} {
		if !strings.Contains(snapshot, expected) {
			t.Fatalf("expected page snapshot script to contain sensitive-input guard %q", expected)
		}
	}
	for _, expected := range []string{"type==='password'", "type==='file'", "sensitive input interaction is blocked"} {
		if !strings.Contains(inspect, expected) {
			t.Fatalf("expected element inspection to include %q", expected)
		}
		for name, script := range elementActions {
			if !strings.Contains(script, expected) {
				t.Fatalf("expected %s script to include the shared interaction guard %q", name, expected)
			}
		}
	}
}

func TestValidateNavigationURL(t *testing.T) {
	for _, rawURL := range []string{"file:///etc/passwd", "http://169.254.169.254/latest/meta-data", "http://[::]/"} {
		if err := validateNavigationURL(rawURL); err == nil {
			t.Fatalf("expected URL to be rejected: %s", rawURL)
		}
	}
	if err := validateNavigationURL("https://example.com/path"); err != nil {
		t.Fatalf("expected normal HTTPS URL to pass: %v", err)
	}
}

// fakeHistoryEval answers history_state scripts with the given states in order
// (the last one repeats) and records whether each call set the mark.
func fakeHistoryEval(states ...historyState) (pageEvaluator, *[]bool) {
	var marks []bool
	calls := 0
	return func(_ unsafe.Pointer, _, script string, _ int, target any) error {
		marks = append(marks, strings.Contains(script, "mark=true"))
		state := states[len(states)-1]
		if calls < len(states) {
			state = states[calls]
		}
		calls++
		*(target.(*historyState)) = state
		return nil
	}, &marks
}

func stubGoHistory(t *testing.T, moved bool, err error) *[]int {
	t.Helper()
	var deltas []int
	original := platformGoHistory
	platformGoHistory = func(_ unsafe.Pointer, _ string, delta int) (bool, error) {
		deltas = append(deltas, delta)
		return moved, err
	}
	t.Cleanup(func() { platformGoHistory = original })
	return &deltas
}

func TestGoHistoryWithoutHistoryReportsNoHistory(t *testing.T) {
	deltas := stubGoHistory(t, false, nil)
	eval, _ := fakeHistoryEval(historyState{ReadyState: "complete", URL: "https://a.test/", Marked: true})
	_, err := goHistory(nil, "a", -1, 2000, eval)
	toolErr := requireToolErrorCode(t, err, errCodeNoHistory)
	if toolErr.Details["direction"] != "back" || !strings.Contains(toolErr.Message, "back") {
		t.Fatalf("no_history should name the direction, got %q %v", toolErr.Message, toolErr.Details)
	}
	_, err = goHistory(nil, "a", 1, 2000, eval)
	toolErr = requireToolErrorCode(t, err, errCodeNoHistory)
	if toolErr.Details["direction"] != "forward" {
		t.Fatalf("expected forward direction, got %v", toolErr.Details)
	}
	if len(*deltas) != 2 || (*deltas)[0] != -1 || (*deltas)[1] != 1 {
		t.Fatalf("expected deltas [-1 1], got %v", *deltas)
	}
}

func TestGoHistoryPassesPlatformErrorsThrough(t *testing.T) {
	stubGoHistory(t, false, newToolError(errCodeUnsupportedOnPlatform, "no history here", nil))
	eval, _ := fakeHistoryEval(historyState{ReadyState: "complete", URL: "https://a.test/", Marked: true})
	_, err := goHistory(nil, "a", -1, 2000, eval)
	requireToolErrorCode(t, err, errCodeUnsupportedOnPlatform)
}

func TestGoHistoryReleasesDialogsAndCapturesBeforeNavigating(t *testing.T) {
	resetPendingDialogs()
	t.Cleanup(resetPendingDialogs)
	openDialog(pendingDialog{ID: "d1", TabID: "a", Kind: "alert", Message: "hi"})
	stubGoHistory(t, false, nil)
	eval, _ := fakeHistoryEval(historyState{ReadyState: "complete", URL: "https://a.test/", Marked: true})
	_, _ = goHistory(nil, "a", -1, 2000, eval)
	if _, ok := dialogForTab("a"); ok {
		t.Fatal("going through history must release the tab's pending dialog like navigate does")
	}
}

func TestPlatformGoHistoryIsUnsupportedOnStubPlatforms(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("macOS and Windows implement history navigation")
	}
	_, err := platformGoHistory(nil, "a", -1)
	requireToolErrorCode(t, err, errCodeUnsupportedOnPlatform)
}

func TestWaitForHistoryChangeDetectsURLOrNewDocument(t *testing.T) {
	before := historyState{ReadyState: "complete", URL: "https://a.test/b", Marked: true}
	// The URL changes (a cross-document or same-document entry).
	eval, _ := fakeHistoryEval(before, before, historyState{ReadyState: "loading", URL: "https://a.test/a", Marked: false})
	state, err := waitForHistoryChange(nil, "a", "tok", before, true, time.Now().Add(2*time.Second), eval)
	if err != nil || state.URL != "https://a.test/a" {
		t.Fatalf("expected the new URL, got %+v %v", state, err)
	}
	// Same URL, but a new document no longer carries the mark.
	eval, _ = fakeHistoryEval(before, historyState{ReadyState: "complete", URL: "https://a.test/b", Marked: false})
	if _, err := waitForHistoryChange(nil, "a", "tok", before, true, time.Now().Add(2*time.Second), eval); err != nil {
		t.Fatalf("a new document at the same URL should count as navigated: %v", err)
	}
	// Nothing changes: time out.
	eval, marks := fakeHistoryEval(before)
	if _, err := waitForHistoryChange(nil, "a", "tok", before, true, time.Now().Add(250*time.Millisecond), eval); err == nil {
		t.Fatal("expected a timeout when neither URL nor document changes")
	}
	for _, marked := range *marks {
		if marked {
			t.Fatal("polling must not re-set the mark")
		}
	}
	// Without a mark only the URL counts.
	eval, _ = fakeHistoryEval(historyState{ReadyState: "complete", URL: "https://a.test/b", Marked: false})
	if _, err := waitForHistoryChange(nil, "a", "tok", before, false, time.Now().Add(250*time.Millisecond), eval); err == nil {
		t.Fatal("an unmarked page must not count as a new document")
	}
}

func TestSemanticToolsValidateArgumentsBeforeTouchingThePage(t *testing.T) {
	useTestHostWindow(t)
	setTestAIAccess(true, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	resetPendingDialogs()
	ref := map[string]any{"snapshotId": "s", "ref": "e1"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range ref {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"browser.select_option", with(nil), "either values or labels"},
		{"browser.select_option", with(map[string]any{"values": []any{"a"}, "labels": []any{"A"}}), "either values or labels"},
		{"browser.select_option", with(map[string]any{"values": []any{}}), "either values or labels"},
		{"browser.select_option", with(map[string]any{"values": []any{"a", 3}}), "values must be an array of strings"},
		{"browser.select_option", with(map[string]any{"labels": "A"}), "labels must be an array of strings"},
		{"browser.select_option", map[string]any{"values": []any{"a"}}, "snapshotId and ref are required"},
		{"browser.set_checked", with(nil), "checked"},
		{"browser.set_checked", with(map[string]any{"checked": "yes"}), "checked"},
		{"browser.hover", map[string]any{"ref": "e1"}, "snapshotId and ref are required"},
		{"browser.scroll", map[string]any{}, "either ref or direction"},
		{"browser.scroll", with(map[string]any{"direction": "down"}), "either ref or direction"},
		{"browser.scroll", map[string]any{"direction": "sideways"}, "direction must be"},
		{"browser.scroll", map[string]any{"direction": "down", "amount": "lots"}, "amount must be"},
		{"browser.scroll", map[string]any{"direction": "down", "amount": -5.0}, "amount must be"},
		{"browser.scroll", map[string]any{"ref": "e1"}, "snapshotId is required"},
		{"browser.scroll", map[string]any{"direction": "down", "containerRef": "e2"}, "snapshotId is required"},
		{"browser.extract_content", map[string]any{"format": "html"}, "format must be markdown or text"},
		{"browser.extract_content", map[string]any{"format": 3.0}, "format must be markdown or text"},
		{"browser.extract_content", map[string]any{"ref": "e1"}, "both snapshotId and ref"},
		{"browser.extract_content", map[string]any{"snapshotId": "s"}, "both snapshotId and ref"},
	}
	for _, c := range cases {
		_, err := ExecuteAITool(c.tool, c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s %v: expected error containing %q, got %v", c.tool, c.args, c.want, err)
		}
	}
}

func TestScrollAmountArgument(t *testing.T) {
	for _, c := range []struct {
		raw  any
		want any
	}{{nil, "page"}, {"page", "page"}, {240.0, 240}, {0.4, 1}, {1e9, 100000}} {
		args := map[string]any{}
		if c.raw != nil {
			args["amount"] = c.raw
		}
		got, err := scrollAmountArg(args)
		if err != nil || got != c.want {
			t.Fatalf("amount %v: expected %v, got %v %v", c.raw, c.want, got, err)
		}
	}
}

func TestSemanticToolsAreBlockedWhileADialogIsOpen(t *testing.T) {
	useTestHostWindow(t)
	setTestAIAccess(true, "a")
	t.Cleanup(func() { setTestAIAccess(false, ""); resetPendingDialogs() })
	resetPendingDialogs()
	openDialog(pendingDialog{ID: "d1", TabID: "a", Kind: "confirm", Message: "Leave?"})
	for _, name := range []string{"browser.go_back", "browser.go_forward", "browser.select_option", "browser.set_checked", "browser.hover", "browser.scroll", "browser.extract_content"} {
		_, err := ExecuteAITool(name, map[string]any{"snapshotId": "s", "ref": "e1", "checked": true, "values": []any{"a"}})
		requireToolErrorCode(t, err, errCodeDialogPending)
	}
}

// fakeCheckedEval answers set_checked with first and checked_state with
// state(poll) for the poll-th read, counting the clicks (set_checked runs).
func fakeCheckedEval(first map[string]any, state func(poll int) bool) (pageEvaluator, *int, *int) {
	clicks, polls := 0, 0
	return func(_ unsafe.Pointer, _, script string, _ int, target any) error {
		out := target.(*any)
		switch {
		case strings.Contains(script, `actionName="set_checked"`):
			clicks++
			*out = first
		case strings.Contains(script, `actionName="checked_state"`):
			polls++
			*out = map[string]any{"ref": "e1", "checked": state(polls)}
		default:
			return errors.New("unexpected script")
		}
		return nil
	}, &clicks, &polls
}

func TestSetCheckedWaitsForAPendingARIAToggle(t *testing.T) {
	eval, clicks, polls := fakeCheckedEval(map[string]any{"changed": true, "ref": "e1", "checked": true, "pending": true}, func(poll int) bool { return poll >= 3 })
	result, err := setChecked(nil, "a", "s", "e1", true, time.Second, eval)
	if err != nil {
		t.Fatalf("a toggle the page applies later must succeed: %v", err)
	}
	got, _ := result.(map[string]any)
	if got["changed"] != true || got["checked"] != true || got["pending"] != nil {
		t.Fatalf("unexpected result %v", result)
	}
	if *clicks != 1 || *polls != 3 {
		t.Fatalf("expected one click and three polls, got clicks=%d polls=%d", *clicks, *polls)
	}
}

func TestSetCheckedFailsWhenTheARIAStateNeverChanges(t *testing.T) {
	eval, clicks, polls := fakeCheckedEval(map[string]any{"changed": true, "ref": "e1", "checked": true, "pending": true}, func(int) bool { return false })
	start := time.Now()
	_, err := setChecked(nil, "a", "s", "e1", true, 300*time.Millisecond, eval)
	if err == nil || !strings.Contains(err.Error(), "did not become checked") {
		t.Fatalf("expected a did-not-become-checked error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("expected to poll for about the timeout, took %s", elapsed)
	}
	if *clicks != 1 || *polls < 2 {
		t.Fatalf("expected one click and repeated polls, got clicks=%d polls=%d", *clicks, *polls)
	}
}

func TestSetCheckedReturnsSettledResultsWithoutPolling(t *testing.T) {
	for _, first := range []map[string]any{
		{"changed": false, "ref": "e1", "checked": true},
		{"changed": true, "ref": "e1", "checked": true},
	} {
		eval, clicks, polls := fakeCheckedEval(first, func(int) bool { return true })
		result, err := setChecked(nil, "a", "s", "e1", true, time.Second, eval)
		if err != nil || result.(map[string]any)["changed"] != first["changed"] {
			t.Fatalf("expected %v back, got %v %v", first, result, err)
		}
		if *clicks != 1 || *polls != 0 {
			t.Fatalf("a settled result must not poll: clicks=%d polls=%d", *clicks, *polls)
		}
	}
}

// answerTabRequests makes the fake front end answer every browser:ai-tab-request
// with respond(request) and records the requests it saw.
func answerTabRequests(t *testing.T, respond func(aiTabRequest) map[string]any) func() []aiTabRequest {
	t.Helper()
	var mu sync.Mutex
	var seen []aiTabRequest
	core.SetEmitter(func(name string, data any) {
		if name != "browser:ai-tab-request" {
			return
		}
		request := data.(aiTabRequest)
		mu.Lock()
		seen = append(seen, request)
		mu.Unlock()
		completion := respond(request)
		if completion == nil {
			return
		}
		completion["requestId"] = request.RequestID
		payload, _ := json.Marshal(map[string]any{"payload": completion})
		if _, err := handleCompleteTabRequest("", string(payload)); err != nil {
			t.Errorf("complete tab request: %v", err)
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	return func() []aiTabRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]aiTabRequest{}, seen...)
	}
}

func TestListTabsAsksTheFrontEndAndReturnsItsAnswer(t *testing.T) {
	setTestSharedAIAccess(t, []string{"a"}, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	seen := answerTabRequests(t, func(aiTabRequest) map[string]any {
		return map[string]any{"result": map[string]any{
			"tabs":          []any{map[string]any{"tabId": "a", "title": "A", "url": "https://a.test/", "foreground": true, "target": true}},
			"unsharedCount": 2,
		}}
	})
	result, err := ExecuteAITool("browser.list_tabs", nil)
	if err != nil {
		t.Fatalf("list_tabs: %v", err)
	}
	requests := seen()
	if len(requests) != 1 || requests[0].Action != "list" || requests[0].TabID != "" || requests[0].RequestID == "" {
		t.Fatalf("unexpected tab requests %#v", requests)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"unsharedCount":2`) || !strings.Contains(string(encoded), `"tabId":"a"`) {
		t.Fatalf("list_tabs result was not passed through: %s", encoded)
	}
}

func TestListTabsWorksWhileSharingIsOffAndADialogIsOpen(t *testing.T) {
	setTestAIAccess(false, "")
	resetPendingDialogs()
	t.Cleanup(resetPendingDialogs)
	answerTabRequests(t, func(aiTabRequest) map[string]any {
		return map[string]any{"result": map[string]any{"tabs": []any{}, "unsharedCount": 1}}
	})
	if _, err := ExecuteAITool("browser.list_tabs", nil); err != nil {
		t.Fatalf("list_tabs with sharing off must report the unshared count, got %v", err)
	}
}

func TestSwitchTabRequiresASharedTabBeforeAskingTheFrontEnd(t *testing.T) {
	setTestSharedAIAccess(t, []string{"a", "b"}, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	seen := answerTabRequests(t, func(aiTabRequest) map[string]any {
		return map[string]any{"result": map[string]any{"tabId": "b"}}
	})
	_, err := ExecuteAITool("browser.switch_tab", map[string]any{"tabId": "private"})
	requireToolErrorCode(t, err, errCodeTabNotShared)
	if _, err := ExecuteAITool("browser.switch_tab", map[string]any{}); err == nil {
		t.Fatal("switch_tab without tabId must fail")
	}
	if len(seen()) != 0 {
		t.Fatalf("an unshared or missing tab must not reach the front end: %#v", seen())
	}
	if _, err := ExecuteAITool("browser.switch_tab", map[string]any{"tabId": "b"}); err != nil {
		t.Fatalf("switch_tab: %v", err)
	}
	if requests := seen(); len(requests) != 1 || requests[0].Action != "switch" || requests[0].TabID != "b" {
		t.Fatalf("unexpected tab requests %#v", requests)
	}
}

func TestSwitchTabIsAllowedWhileADialogIsOpen(t *testing.T) {
	setTestSharedAIAccess(t, []string{"a", "b"}, "a")
	resetPendingDialogs()
	t.Cleanup(func() { setTestAIAccess(false, ""); resetPendingDialogs() })
	openDialog(pendingDialog{ID: "d1", TabID: "a", Kind: "alert", Message: "Hi"})
	openDialog(pendingDialog{ID: "d2", TabID: "b", Kind: "confirm", Message: "Leave?"})
	answerTabRequests(t, func(request aiTabRequest) map[string]any {
		return map[string]any{"result": map[string]any{"tabId": request.TabID}}
	})
	for _, tabID := range []string{"b", "a"} {
		if _, err := ExecuteAITool("browser.switch_tab", map[string]any{"tabId": tabID}); err != nil {
			t.Fatalf("switching to %s with dialogs open: %v", tabID, err)
		}
	}
}

func TestTabRequestPassesStructuredFrontEndErrorsThrough(t *testing.T) {
	setTestSharedAIAccess(t, []string{"a"}, "a")
	t.Cleanup(func() { setTestAIAccess(false, "") })
	answerTabRequests(t, func(aiTabRequest) map[string]any {
		return map[string]any{"error": `{"code":"browser_tab_not_shared","message":"the requested browser tab is not shared with AI","details":{"tabId":"a"}}`}
	})
	_, err := ExecuteAITool("browser.switch_tab", map[string]any{"tabId": "a"})
	toolErr := requireToolErrorCode(t, err, errCodeTabNotShared)
	if toolErr.Details["tabId"] != "a" {
		t.Fatalf("details lost: %#v", toolErr.Details)
	}
	answerTabRequests(t, func(aiTabRequest) map[string]any {
		return map[string]any{"error": "workspace closed"}
	})
	if _, err := ExecuteAITool("browser.list_tabs", nil); err == nil || err.Error() != "workspace closed" {
		t.Fatalf("plain front-end errors must pass through, got %v", err)
	}
}

func TestTabRequestTimesOutInsteadOfHanging(t *testing.T) {
	setTestSharedAIAccess(t, []string{"a"}, "a")
	original := aiTabRequestTimeout
	aiTabRequestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { aiTabRequestTimeout = original; setTestAIAccess(false, "") })
	answerTabRequests(t, func(aiTabRequest) map[string]any { return nil })
	done := make(chan error, 1)
	go func() {
		_, err := ExecuteAITool("browser.list_tabs", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("expected a timeout error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("browser.list_tabs hung waiting for the front end")
	}
	aiTabRequests.Lock()
	pending := len(aiTabRequests.pending)
	aiTabRequests.Unlock()
	if pending != 0 {
		t.Fatalf("timed-out tab requests must be forgotten, %d still pending", pending)
	}
}

func TestDefaultTabRequestTimeoutIsFiveSeconds(t *testing.T) {
	if aiTabRequestTimeout != 5*time.Second {
		t.Fatalf("tab requests must time out after 5s, got %v", aiTabRequestTimeout)
	}
}

func TestBoundSnapshotDropsTrailingElementsOverTheByteCap(t *testing.T) {
	elements := make([]any, 100)
	for i := range elements {
		elements[i] = map[string]any{"ref": fmt.Sprintf("e%d", i+1), "name": strings.Repeat("n", 1000)}
	}
	snapshot := map[string]any{"snapshotId": "s", "text": "page", "elements": elements, "truncatedElements": false}

	bounded, err := boundSnapshot(snapshot, 10_000)
	if err != nil {
		t.Fatalf("bound: %v", err)
	}
	encoded, _ := json.Marshal(bounded)
	fields := bounded.(map[string]any)
	kept := fields["elements"].([]any)
	if len(encoded) > 10_000 || len(kept) == 0 || len(kept) >= 100 || fields["truncatedElements"] != true {
		t.Fatalf("expected a prefix of elements under the cap with truncatedElements, got %d bytes, %d elements, %v", len(encoded), len(kept), fields["truncatedElements"])
	}
	if kept[0].(map[string]any)["ref"] != "e1" {
		t.Fatalf("elements must be dropped from the end, got %v first", kept[0])
	}
	if len(snapshot["elements"].([]any)) != 100 {
		t.Fatal("the input snapshot must not be modified")
	}

	small := map[string]any{"elements": []any{map[string]any{"ref": "e1"}}, "truncatedElements": false}
	if same, err := boundSnapshot(small, 10_000); err != nil || same.(map[string]any)["truncatedElements"] != false {
		t.Fatalf("a snapshot under the cap passes unchanged, got %v %v", same, err)
	}
	huge := map[string]any{"title": strings.Repeat("t", 20_000), "elements": []any{}}
	_, err = boundSnapshot(huge, 10_000)
	if err == nil || !strings.Contains(err.Error(), "title or URL is too large") || strings.Contains(err.Error(), "maxElements") {
		t.Fatalf("a snapshot that cannot fit even without elements must blame the title or URL, not maxElements, got %v", err)
	}
	// Elements alone overflowing still suggests a smaller maxElements.
	if _, err := boundSnapshot(map[string]any{"elements": []any{strings.Repeat("e", 20_000)}}, 10_000); err != nil {
		t.Fatalf("an oversized element is dropped, not an error: %v", err)
	}
}

func TestExtractContentScriptArguments(t *testing.T) {
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, `format="markdown",maxChars=20000,snapshotId="",ref=""`},
		{map[string]any{"format": "text", "maxChars": 5.0}, `format="text",maxChars=1000,`},
		{map[string]any{"format": " markdown ", "maxChars": 1e9}, `format="markdown",maxChars=100000,`},
		{map[string]any{"maxChars": 4321.0, "snapshotId": "s1", "ref": "e7"}, `maxChars=4321,snapshotId="s1",ref="e7"`},
	} {
		script, err := extractContentScript(c.args)
		if err != nil || !strings.Contains(script, c.want) {
			t.Fatalf("%v: expected script containing %s, got err %v", c.args, c.want, err)
		}
	}
}

func TestBoundExtractRefusesOversizedResults(t *testing.T) {
	small := map[string]any{"title": "t", "url": "https://x.test/", "content": "hello", "truncated": false}
	if got, err := boundExtract(small, 1000); err != nil || got.(map[string]any)["content"] != "hello" {
		t.Fatalf("a small result passes unchanged, got %v %v", got, err)
	}
	huge := map[string]any{"title": "t", "url": "https://x.test/" + strings.Repeat("u", 5000), "content": "", "truncated": false}
	if _, err := boundExtract(huge, 1000); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("an oversized result is an error, got %v", err)
	}
}
