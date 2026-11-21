package plugin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// captureViewRaise replaces the main-window raise of host.view.open.
func captureViewRaise(t *testing.T) *atomic.Int32 {
	t.Helper()
	var raised atomic.Int32
	previous := hostViewRaise
	hostViewRaise = func() error { raised.Add(1); return nil }
	t.Cleanup(func() { hostViewRaise = previous })
	return &raised
}

func rpcErrorDataCode(err *rpcError) string {
	if err == nil {
		return ""
	}
	if data, ok := err.Data.(map[string]string); ok {
		return data["code"]
	}
	return ""
}

func viewOpenEvents(c *capturedUIEvents) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, event := range c.events {
		if event.name == pluginViewOpenEvent {
			count++
		}
	}
	return count
}

func TestRaiseTokenSingleUseExpiryAndOwner(t *testing.T) {
	unlockAppForTest(t)
	events := captureHostUI(t)
	raised := captureViewRaise(t)
	now := time.Unix(1_800_000_000, 0)
	var mu sync.Mutex
	restore := globalShortcutActivations.setClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	t.Cleanup(restore)
	t.Cleanup(func() { revokeActivationTokens("") })
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	notes, report := uiRuntime("notes"), uiRuntime("report")
	raise := func(runtimeHost *processRuntime, params string) (any, *rpcError) {
		return uiCall(hostViewOpen, runtimeHost, params)
	}

	// A normal open without raise behaves as before and never raises.
	if _, err := raise(notes, `{"viewId":"main"}`); err != nil {
		t.Fatal(err)
	}
	if raised.Load() != 0 {
		t.Fatal("plain open raised the window")
	}
	opened := viewOpenEvents(events)

	// raise without a token, with an unknown token, or activation without raise.
	for name, params := range map[string]string{
		"no token":      `{"viewId":"main","raise":true}`,
		"unknown token": `{"viewId":"main","raise":true,"activation":"` + strings.Repeat("a", 32) + `"}`,
	} {
		if _, err := raise(notes, params); err == nil || rpcErrorDataCode(err) != globalShortcutActivationInvalid {
			t.Fatalf("%s: %+v", name, err)
		}
	}
	token := issueActivationToken("notes", "capture")
	if _, err := raise(notes, `{"viewId":"main","activation":"`+token+`"}`); err == nil || rpcErrorDataCode(err) != "invalid_params" {
		t.Fatalf("activation without raise: %+v", err)
	}

	// Another plugin cannot use the token, and raise never opens another
	// plugin's view; neither burns the owner's token.
	if _, err := raise(report, `{"viewId":"main","raise":true,"activation":"`+token+`"}`); err == nil || rpcErrorDataCode(err) != globalShortcutActivationInvalid {
		t.Fatalf("other plugin: %+v", err)
	}
	if _, err := raise(notes, `{"pluginId":"report","viewId":"main","raise":true,"activation":"`+token+`"}`); err == nil || rpcErrorDataCode(err) != contributionErrForbidden {
		t.Fatalf("raise to another plugin's view: %+v", err)
	}
	// A view the plugin does not declare as a tab view (floating, unknown).
	if _, err := raise(notes, `{"viewId":"pet","raise":true,"activation":"`+token+`"}`); err == nil {
		t.Fatal("raise to a non-tab view accepted")
	}
	if raised.Load() != 0 || viewOpenEvents(events) != opened {
		t.Fatalf("rejected raises had effects: raised %d", raised.Load())
	}

	// The owner raises once.
	result, err := raise(notes, `{"viewId":"main","raise":true,"activation":"`+token+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any); got["requested"] != true || got["raised"] != true || raised.Load() != 1 {
		t.Fatalf("result %+v raised %d", got, raised.Load())
	}
	if got := events.last(t, pluginViewOpenEvent).(viewOpenEvent); got.TargetPluginID != "notes" || got.ViewID != "main" {
		t.Fatalf("event %+v", got)
	}
	// Single use.
	if _, err := raise(notes, `{"viewId":"main","raise":true,"activation":"`+token+`"}`); err == nil || rpcErrorDataCode(err) != globalShortcutActivationInvalid {
		t.Fatalf("reused token: %+v", err)
	}

	// 120 s from issue, on the injected clock.
	expiring := issueActivationToken("notes", "capture")
	advance(globalShortcutActivationTTL + time.Second)
	if _, err := raise(notes, `{"viewId":"main","raise":true,"activation":"`+expiring+`"}`); err == nil || rpcErrorDataCode(err) != globalShortcutActivationInvalid {
		t.Fatalf("expired token: %+v", err)
	}
	if raised.Load() != 1 {
		t.Fatalf("raised %d times", raised.Load())
	}

	// Without a desktop to raise, the view still opens and the result says so.
	hostViewRaise = func() error { return errors.New("no desktop") }
	fresh := issueActivationToken("notes", "capture")
	result, err = raise(notes, `{"viewId":"main","raise":true,"activation":"`+fresh+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any); got["raised"] != false {
		t.Fatalf("result %+v", got)
	}
}

// pluginLogLines returns the record's log messages.
func pluginLogLines(record *pluginRecord) []string {
	record.mu.Lock()
	defer record.mu.Unlock()
	lines := make([]string, 0, len(record.logs))
	for _, entry := range record.logs {
		lines = append(lines, entry.Message)
	}
	return lines
}

// waitForLog waits until a log line contains want and returns it.
func waitForLog(t *testing.T, record *pluginRecord, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range pluginLogLines(record) {
			if strings.Contains(line, want) {
				return line
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no log line with %q in %q", want, pluginLogLines(record))
	return ""
}

func TestGlobalShortcutActivationDeliveredToPlugin(t *testing.T) {
	unlockAppForTest(t)
	manager, record := shortcutProcessManager(t, ManifestUIAction{ID: "save", Risk: "write", UICallable: true, MaxPayloadBytes: 1024})
	h := newShortcutHarness(t, manager, "mac", nil)
	// Keep the production delivery (the harness records it otherwise).
	h.registry.onActivation = manager.sendGlobalShortcutActivation
	t.Cleanup(func() { revokeActivationTokens("") })
	enableShortcut(t, h.registry, record.id, "save")
	before := time.Now()
	h.press(t, "CmdOrCtrl+Shift+1")
	waitForLog(t, record, "helper invoke globalShortcut")
	line := waitForLog(t, record, "helper activation ")
	fields := strings.Fields(line[strings.Index(line, "helper activation "):])
	if len(fields) != 5 || fields[2] != "save" || len(fields[3]) != 32 {
		t.Fatalf("activation line %q", line)
	}
	expiresAt, _ := strconv.ParseInt(fields[4], 10, 64)
	if at := time.UnixMilli(expiresAt); at.Before(before.Add(globalShortcutActivationTTL-time.Second)) || at.After(time.Now().Add(globalShortcutActivationTTL+time.Second)) {
		t.Fatalf("expiresAt %v", at)
	}
	// The plugin uses the token to bring its own tab view forward.
	captureHostUI(t)
	raised := captureViewRaise(t)
	viewOpenTarget = func(pluginID, viewID string) error {
		if pluginID == record.id && viewID == "view" {
			return nil
		}
		return errors.New("no such view")
	}
	record.mu.Lock()
	runtimeHost := record.runtime
	record.mu.Unlock()
	if _, err := uiCall(hostViewOpen, runtimeHost, fmt.Sprintf(`{"viewId":"view","raise":true,"activation":%q}`, fields[3])); err != nil || raised.Load() != 1 {
		t.Fatalf("raise with delivered token: %+v (raised %d)", err, raised.Load())
	}
	// Delivery goes only to the generation that ran the action.
	stale := globalShortcutRun{reached: true, runtime: runtimeHost, generation: runtimeHost.Generation() + 1}
	if manager.sendGlobalShortcutActivation(record.id, "save", "x", time.Now(), stale) {
		t.Fatal("delivered to another generation")
	}
	if manager.sendGlobalShortcutActivation(record.id, "save", "x", time.Now(), globalShortcutRun{reached: true}) {
		t.Fatal("delivered without a runtime")
	}
}

func TestGlobalShortcutActivationUnhandledIsQuiet(t *testing.T) {
	// Runtimes without a handler answer -32601; that is not worth a log line.
	if !globalShortcutActivationIgnorable(&processRPCError{Method: globalShortcutActivationMethod, Code: -32601}) {
		t.Fatal("method-not-found is logged")
	}
	if globalShortcutActivationIgnorable(&processRPCError{Code: -32603}) || globalShortcutActivationIgnorable(context.DeadlineExceeded) {
		t.Fatal("real failures are hidden")
	}
}

func TestGlobalShortcutLockAfterAdmissionRefusesAction(t *testing.T) {
	manager, record := shortcutProcessManager(t, ManifestUIAction{ID: "save", Risk: "write", UICallable: true, MaxPayloadBytes: 1024})
	h := newShortcutHarness(t, manager, "mac", nil)
	enableShortcut(t, h.registry, record.id, "save")
	// The app locks while the plugin is being started for the press: the
	// first check in pressed passes, the one after admission must not.
	var checks atomic.Int32
	h.registry.locked = func() bool {
		record.mu.Lock()
		running := record.state == pluginStateRunning
		record.mu.Unlock()
		checks.Add(1)
		return running
	}
	h.press(t, "CmdOrCtrl+Shift+1")
	_, notices, _, activations := h.snapshot()
	if len(notices) != 1 || notices[0].Code != "globalShortcut.locked" || len(activations) != 0 {
		t.Fatalf("notices %+v activations %v", notices, activations)
	}
	record.mu.Lock()
	state := record.state
	record.mu.Unlock()
	if state != pluginStateRunning {
		t.Fatalf("plugin was not admitted (state %s); the test did not reach the second check", state)
	}
	time.Sleep(100 * time.Millisecond)
	for _, line := range pluginLogLines(record) {
		if strings.Contains(line, "helper invoke") {
			t.Fatalf("action ran while locked: %q", pluginLogLines(record))
		}
	}
	assertNoContributionRequestsLeft(t)
}

func TestGlobalShortcutAdmissionFailureReasons(t *testing.T) {
	activation := newContributionError(contributionErrUnavailable, "start failed")
	activation.(*contributionError).activation = true
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"forbidden":         {newContributionError(contributionErrForbidden, "x"), "unavailable"},
		"not found":         {newContributionError(contributionErrNotFound, "x"), "unavailable"},
		"not active":        {newContributionError(contributionErrUnavailable, "x"), "unavailable"},
		"activation":        {activation, "startFailed"},
		"busy":              {newContributionError(contributionErrBusy, "x"), "busy"},
		"locked":            {errGlobalShortcutLocked, "locked"},
		"task limit":        {fmt.Errorf("start task: %w", errTaskLimitReached), "busy"},
		"tasks unsupported": {fmt.Errorf("start task: %w", errTasksUnsupported), "failed"},
		"too large":         {newContributionError(contributionErrTooLarge, "x"), "failed"},
		"invalid request":   {newContributionError(contributionErrInvalidRequest, "x"), "failed"},
		"plain":             {errors.New("boom"), "failed"},
	} {
		if got := globalShortcutNotRunReason(tc.err); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

func TestGlobalShortcutUndeclaredActionIsUnavailable(t *testing.T) {
	manager, record := shortcutProcessManager(t, ManifestUIAction{ID: "save", Risk: "write", UICallable: true, MaxPayloadBytes: 1024})
	h := newShortcutHarness(t, manager, "mac", nil)
	enableShortcut(t, h.registry, record.id, "save")
	// The entry still exists but its action is gone (admission: not_found).
	record.mu.Lock()
	record.manifest.Contributes.Actions = nil
	record.mu.Unlock()
	h.press(t, "CmdOrCtrl+Shift+1")
	_, notices, _, activations := h.snapshot()
	if len(notices) != 1 || notices[0].Code != "globalShortcut.unavailable" || len(activations) != 0 {
		t.Fatalf("notices %+v activations %v", notices, activations)
	}
}

// unlockAppForTest injects an unlocked app lock source (appLocked fails
// closed without one) and returns a setter.
func unlockAppForTest(t *testing.T) func(locked bool) {
	t.Helper()
	previous := appLockSource.Load()
	var locked atomic.Bool
	SetAppLockSource(locked.Load)
	t.Cleanup(func() { appLockSource.Store(previous) })
	return locked.Store
}

func TestRaiseRefusedWhileLocked(t *testing.T) {
	events := captureHostUI(t)
	raised := captureViewRaise(t)
	setLocked := unlockAppForTest(t)
	t.Cleanup(func() { revokeActivationTokens("") })
	token := issueActivationToken("notes", "capture")
	if token == "" {
		t.Fatal("no token while unlocked")
	}
	setLocked(true)
	_, err := uiCall(hostViewOpen, uiRuntime("notes"), `{"viewId":"main","raise":true,"activation":"`+token+`"}`)
	if err == nil || rpcErrorDataCode(err) != "locked" {
		t.Fatalf("raise while locked: %+v", err)
	}
	if raised.Load() != 0 || viewOpenEvents(events) != 0 {
		t.Fatalf("locked raise had effects: raised %d", raised.Load())
	}
}

// activationTokensFor counts pluginID's live tokens.
func activationTokensFor(store *activationTokenStore, pluginID string) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	count := 0
	for _, entry := range store.tokens {
		if entry.pluginID == pluginID {
			count++
		}
	}
	return count
}

func TestActivationIssueRefusedWhenLocked(t *testing.T) {
	store := &activationTokenStore{now: time.Now, tokens: map[string]activationTokenEntry{}}
	if token, _, ok := store.issue("acme.shot", "capture", func() bool { return true }); ok || token != "" || activationTokensFor(store, "acme.shot") != 0 {
		t.Fatal("token issued while locked")
	}
	// A lock that lands while issue runs: locking sets the state first and
	// then revokes every token, so either issue sees the lock or the
	// revocation sees the token. Nothing usable may survive.
	for i := 0; i < 500; i++ {
		var locked atomic.Bool
		done := make(chan struct{})
		go func() {
			defer close(done)
			store.issue("acme.shot", "capture", locked.Load)
		}()
		locked.Store(true)
		store.revoke("")
		<-done
		if n := activationTokensFor(store, "acme.shot"); n != 0 {
			t.Fatalf("iteration %d: %d token(s) survived the lock", i, n)
		}
	}
}

func TestGlobalShortcutLockBeforeIssueLeavesNoToken(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"))
	var h *shortcutHarness
	h = newShortcutHarness(t, shortcutManager(t, record), "mac", func(context.Context, contributedActionCall) (bool, error) {
		// The action ran; the app locks before the token is issued.
		h.setLocked(true)
		return true, nil
	})
	t.Cleanup(func() { revokeActivationTokens("") })
	enableShortcut(t, h.registry, "acme.shot", "capture")
	h.press(t, "CmdOrCtrl+Shift+A")
	if _, _, _, activations := h.snapshot(); len(activations) != 0 || activationTokensFor(globalShortcutActivations, "acme.shot") != 0 {
		t.Fatalf("activations %v", activations)
	}
}

func TestGlobalShortcutTokenRevokedWhenDeliveryFails(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", func(context.Context, contributedActionCall) (bool, error) { return true, nil })
	t.Cleanup(func() { revokeActivationTokens("") })
	h.registry.onActivation = func(string, string, string, time.Time, globalShortcutRun) bool { return false }
	enableShortcut(t, h.registry, "acme.shot", "capture")
	h.press(t, "CmdOrCtrl+Shift+A")
	if n := activationTokensFor(globalShortcutActivations, "acme.shot"); n != 0 {
		t.Fatalf("%d undelivered token(s) kept", n)
	}
}

func TestAppLockFailsClosedWithoutSource(t *testing.T) {
	previous := appLockSource.Load()
	t.Cleanup(func() { appLockSource.Store(previous) })
	SetAppLockSource(nil)
	if !appLocked() {
		t.Fatal("no lock source reads as unlocked")
	}
	SetAppLockSource(func() bool { return false })
	if appLocked() {
		t.Fatal("injected source ignored")
	}
}
