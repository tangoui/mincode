package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

// updateShortcutEntries replaces the plugin's declared entries the way an
// update (a new manifest) does.
func updateShortcutEntries(record *pluginRecord, entries ...GlobalShortcutEntry) {
	resolved := &resolvedContribution{}
	for _, entry := range entries {
		resolved.Entries = append(resolved.Entries, entry)
	}
	record.mu.Lock()
	next := *record.manifest
	next.contributions = map[string]*resolvedContribution{globalShortcutPointKey: resolved}
	record.manifest = &next
	record.mu.Unlock()
}

func storedShortcutSetting(t *testing.T, pluginID, id string) globalShortcutSetting {
	t.Helper()
	raw, found, err := pluginKVGetRaw(hostPluginID, globalShortcutSettingKey(pluginID, id))
	if err != nil || !found {
		t.Fatalf("setting %s/%s not stored: %v %v", pluginID, id, found, err)
	}
	var setting globalShortcutSetting
	if err := json.Unmarshal(raw, &setting); err != nil {
		t.Fatal(err)
	}
	return setting
}

// F1: an update that changes what an enabled entry presses or does turns it
// off until the user confirms it again; a new title does not.
func TestGlobalShortcutsUpdateChangingEntryTurnsItOff(t *testing.T) {
	base := shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture")
	base.Run.Payload = json.RawMessage(`{"mode":"region","n":1}`)
	retitled := base
	retitled.Title, retitled.TitleLocalized = "Grab region", map[string]string{"zh": "框选"}
	reordered := base
	reordered.Run.Payload = json.RawMessage(`{"n":1,"mode":"region"}`)
	rekeyed := base
	rekeyed.Key = "CmdOrCtrl+Shift+B"
	otherAction := base
	otherAction.Run.Action = "status"
	otherPayload := base
	otherPayload.Run.Payload = json.RawMessage(`{"mode":"screen","n":1}`)
	noPayload := base
	noPayload.Run.Payload = nil
	toView := shortcutEntry("capture", "CmdOrCtrl+Shift+A", "view", "main")
	respelled := func(key string) GlobalShortcutEntry {
		next := base
		next.Key = key
		return next
	}

	for _, tc := range []struct {
		name string
		next GlobalShortcutEntry
		off  bool
	}{
		{"title", retitled, false},
		{"payload key order", reordered, false},
		{"key", rekeyed, true},
		{"modifier order", respelled("Shift+CmdOrCtrl+a"), false},
		{"lower case", respelled("cmdorctrl+shift+a"), false},
		{"modifier alias", respelled("Command+Shift+A"), false},
		{"added modifier", respelled("CmdOrCtrl+Shift+Option+A"), true},
		{"ctrl instead of cmdorctrl", respelled("Ctrl+Shift+A"), true},
		{"super instead of cmdorctrl", respelled("Super+Shift+A"), true},
		{"action", otherAction, true},
		{"payload", otherPayload, true},
		{"payload removed", noPayload, true},
		{"run type", toView, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := shortcutPluginRecord("acme.shot", base)
			h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
			enableShortcut(t, h.registry, "acme.shot", "capture")
			if stored := storedShortcutSetting(t, "acme.shot", "capture"); stored.Fingerprint != globalShortcutFingerprint(base) || stored.Fingerprint == "" {
				t.Fatalf("enable stored fingerprint %q, want %q", stored.Fingerprint, globalShortcutFingerprint(base))
			}

			updateShortcutEntries(record, tc.next)
			h.registry.reconcile(false)
			status := shortcutStatus(t, h.registry, "acme.shot", "capture")
			if !tc.off {
				if !status.Enabled || status.State != globalShortcutStateEnabled || status.ChangedByUpdate || !h.desk.isRegistered(tc.next.Key) {
					t.Fatalf("status %+v after %s change", status, tc.name)
				}
				return
			}
			if status.Enabled || status.State != globalShortcutStateDisabled || !status.ChangedByUpdate || h.desk.count() != 0 {
				t.Fatalf("status %+v (registered %d) after %s change", status, h.desk.count(), tc.name)
			}
			// A full re-evaluation (restart, lock/unlock) keeps it off.
			h.registry.reconcile(true)
			if status := shortcutStatus(t, h.registry, "acme.shot", "capture"); status.Enabled || h.desk.count() != 0 {
				t.Fatalf("turned on again by reconcile: %+v", status)
			}
			// Changing only the key while it is off for the update does not
			// turn it back on.
			key := "CmdOrCtrl+Alt+K"
			if status, err := h.registry.set("acme.shot", "capture", nil, &key); err != nil || status.Enabled || h.desk.count() != 0 {
				t.Fatalf("key change re-enabled: %+v %v", status, err)
			}
			// Turning it on again confirms the new entry.
			status = enableShortcut(t, h.registry, "acme.shot", "capture")
			if !status.Enabled || status.State != globalShortcutStateEnabled || status.ChangedByUpdate || !h.desk.isRegistered(key) {
				t.Fatalf("re-enabled %+v", status)
			}
			if stored := storedShortcutSetting(t, "acme.shot", "capture"); stored.Fingerprint != globalShortcutFingerprint(tc.next) {
				t.Fatalf("re-enable stored %q, want %q", stored.Fingerprint, globalShortcutFingerprint(tc.next))
			}
			h.registry.reconcile(true)
			if status := shortcutStatus(t, h.registry, "acme.shot", "capture"); !status.Enabled || status.ChangedByUpdate {
				t.Fatalf("after re-enable and reconcile %+v", status)
			}
		})
	}
}

// F1: the install preview of an update lists the enabled entries it would
// turn off.
func TestGlobalShortcutsPreviewListsEntriesChangedByUpdate(t *testing.T) {
	installed := validGlobalShortcutEntry()
	other := installed
	other.ID, other.Key = "open-main", "CmdOrCtrl+Option+M"
	other.Run = GlobalShortcutRun{Type: "view", View: "main"}
	record := shortcutPluginRecord("acme-shortcuts", installed, other)
	manager := shortcutManager(t, record)
	h := newShortcutHarness(t, manager, "mac", nil)
	enableShortcut(t, h.registry, "acme-shortcuts", "capture-region")

	// The update changes both entries; only the enabled one is listed.
	changed := validGlobalShortcut()
	changed["key"] = "CmdOrCtrl+Shift+B"
	view := map[string]any{"id": "open-main", "title": "Main", "key": "CmdOrCtrl+Option+N", "run": map[string]any{"type": "view", "view": "main"}}
	preview, err := manager.PreviewInstall(writeGlobalShortcutPackage(t, []any{changed, view}))
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Installed || !slices.Equal(preview.GlobalShortcutsChanged, []string{"capture-region"}) {
		t.Fatalf("preview installed=%v changed=%v", preview.Installed, preview.GlobalShortcutsChanged)
	}

	// An update that only renames it changes nothing.
	renamed := validGlobalShortcut()
	renamed["title"] = "Grab region"
	preview, err = manager.PreviewInstall(writeGlobalShortcutPackage(t, []any{renamed}))
	if err != nil || len(preview.GlobalShortcutsChanged) != 0 {
		t.Fatalf("renamed: %v %v", preview.GlobalShortcutsChanged, err)
	}
}

// validGlobalShortcutEntry is validGlobalShortcut() as the host parses it.
func validGlobalShortcutEntry() GlobalShortcutEntry {
	return GlobalShortcutEntry{
		ID: "capture-region", Title: "Capture region", TitleLocalized: map[string]string{"zh": "框选截图", "en": "Capture region"},
		Key: "CmdOrCtrl+Shift+A", Run: GlobalShortcutRun{Type: "action", Action: "capture", Payload: json.RawMessage(`{"mode":"region"}`)},
	}
}

// F3: an OS release that fails keeps the binding (the OS still holds the
// callback); presses of it do nothing and the next pass retries.
func TestGlobalShortcutsUnregisterFailureIsRetried(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"))
	var calls int
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", func(context.Context, contributedActionCall) (bool, error) {
		calls++
		return true, nil
	})
	enableShortcut(t, h.registry, "acme.shot", "capture")
	old := h.desk.callback("CmdOrCtrl+Shift+A")
	h.desk.mu.Lock()
	h.desk.failUnregister = map[string]int{"CmdOrCtrl+Shift+A": 1}
	h.desk.mu.Unlock()

	key := "CmdOrCtrl+Alt+K"
	enabled := true
	status, err := h.registry.set("acme.shot", "capture", &enabled, &key)
	if err != nil || status.State != globalShortcutStateEnabled || !h.desk.isRegistered(key) {
		t.Fatalf("new key %+v %v", status, err)
	}
	if !h.desk.isRegistered("CmdOrCtrl+Shift+A") {
		t.Fatal("fake lost the binding it failed to release")
	}
	h.registry.mu.Lock()
	_, stale := h.registry.stale["CmdOrCtrl+Shift+A"]
	h.registry.mu.Unlock()
	if !stale {
		t.Fatal("the binding the OS still holds was dropped")
	}
	// The OS still calls the old callback: it must not run anything.
	old()
	h.registry.wait()
	if calls != 0 {
		t.Fatalf("stale binding ran the action %d times", calls)
	}
	// The next pass releases it.
	h.registry.reconcile(false)
	h.registry.mu.Lock()
	staleCount := len(h.registry.stale)
	h.registry.mu.Unlock()
	if staleCount != 0 || h.desk.isRegistered("CmdOrCtrl+Shift+A") || !h.desk.isRegistered(key) {
		t.Fatalf("retry: stale %d, old held %v", staleCount, h.desk.isRegistered("CmdOrCtrl+Shift+A"))
	}
}

// F3: a stale combo is not handed to another entry until it is released.
func TestGlobalShortcutsStaleComboIsNotReused(t *testing.T) {
	record := shortcutPluginRecord("acme.shot",
		shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"),
		shortcutEntry("open", "CmdOrCtrl+Shift+O", "view", "main"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	enableShortcut(t, h.registry, "acme.shot", "capture")
	h.desk.mu.Lock()
	h.desk.failUnregister = map[string]int{"CmdOrCtrl+Shift+A": 1}
	h.desk.mu.Unlock()
	disabled := false
	if _, err := h.registry.set("acme.shot", "capture", &disabled, nil); err != nil {
		t.Fatal(err)
	}
	enabled := true
	key := "CmdOrCtrl+Shift+A"
	status, err := h.registry.set("acme.shot", "open", &enabled, &key)
	if err != nil {
		t.Fatal(err)
	}
	// The release is retried first in this pass, so the combo moves.
	if status.State != globalShortcutStateEnabled {
		t.Fatalf("open %+v", status)
	}
	h.desk.mu.Lock()
	h.desk.failUnregister = map[string]int{"CmdOrCtrl+Shift+A": 5}
	h.desk.mu.Unlock()
	if _, err := h.registry.set("acme.shot", "open", &disabled, nil); err != nil {
		t.Fatal(err)
	}
	status, err = h.registry.set("acme.shot", "capture", &enabled, nil)
	if err != nil || status.State != globalShortcutStateOccupied {
		t.Fatalf("combo still held by a stale binding: %+v %v", status, err)
	}
}

// F4: a plugin holding an unspent activation token is not idle-stopped
// (stopping would revoke the token); it stops once the token is gone.
func TestIdleStopWaitsForActivationToken(t *testing.T) {
	manager, record := enabledOnDemand(t, "")
	if _, err := manager.Invoke(context.Background(), record.id, "run", "{}"); err != nil {
		t.Fatal(err)
	}
	token, _, ok := globalShortcutActivations.issue(record.id, "capture", nil)
	if !ok {
		t.Fatal("token not issued")
	}
	t.Cleanup(func() { globalShortcutActivations.revokeToken(token) })
	time.Sleep(10 * pluginIdleAfterInvokeDelay)
	if recordState(record) != pluginStateRunning {
		t.Fatal("stopped while holding an activation token")
	}
	if _, ok := consumeActivationToken(record.id, token); !ok {
		t.Fatal("token revoked before it was spent")
	}
	waitRecordState(t, record, pluginStateStopped)
}

// F4: an expired token no longer holds the plugin.
func TestActivationTokenHeldIgnoresExpired(t *testing.T) {
	now := time.Now()
	restore := globalShortcutActivations.setClock(func() time.Time { return now })
	t.Cleanup(restore)
	token, _, _ := globalShortcutActivations.issue("acme.held", "capture", nil)
	t.Cleanup(func() { globalShortcutActivations.revokeToken(token) })
	if !activationTokenHeld("acme.held") || activationTokenHeld("acme.other") {
		t.Fatal("held state wrong")
	}
	now = now.Add(globalShortcutActivationTTL)
	if activationTokenHeld("acme.held") {
		t.Fatal("expired token still holds the plugin")
	}
}

// F8: the launch path registers stored shortcuts without any plugin command.
func TestStartGlobalShortcutsAtLaunch(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"))
	manager := shortcutManager(t, record)
	h := newShortcutHarness(t, manager, "mac", nil)
	enableShortcut(t, h.registry, "acme.shot", "capture")
	h.registry.stop()
	if h.desk.count() != 0 {
		t.Fatal("stop kept registrations")
	}
	<-startGlobalShortcutsInBackground(func() *pluginManager { return manager })
	if !h.desk.isRegistered("CmdOrCtrl+Shift+A") {
		t.Fatal("stored shortcut not registered at launch")
	}
	// A panic while building the manager is contained.
	<-startGlobalShortcutsInBackground(func() *pluginManager { panic(errors.New("scan failed")) })
}

// The fingerprint hashes the logical combo, not its spelling: aliases, case
// and modifier order match, different logical modifiers do not (CmdOrCtrl and
// Ctrl are one key on Windows but not on macOS).
func TestGlobalShortcutFingerprintCanonicalKey(t *testing.T) {
	fingerprint := func(key string) string {
		return globalShortcutFingerprint(shortcutEntry("capture", key, "action", "capture"))
	}
	base := fingerprint("CmdOrCtrl+Shift+A")
	for _, key := range []string{"Shift+CmdOrCtrl+a", "cmdorctrl+shift+a", "Command+Shift+A", "Cmd+SHIFT+A"} {
		if fingerprint(key) != base {
			t.Errorf("%q fingerprint differs from CmdOrCtrl+Shift+A", key)
		}
	}
	if fingerprint("OptionOrAlt+Ctrl+Enter") != fingerprint("ctrl+option+return") {
		t.Error("Alt/Option and Enter/Return spellings differ")
	}
	for _, key := range []string{"Ctrl+Shift+A", "Super+Shift+A", "CmdOrCtrl+Shift+B", "CmdOrCtrl+Alt+A", "CmdOrCtrl+A"} {
		if fingerprint(key) == base {
			t.Errorf("%q fingerprint equals CmdOrCtrl+Shift+A", key)
		}
	}
}
