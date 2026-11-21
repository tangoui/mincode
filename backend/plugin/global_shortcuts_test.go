package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/desktop"
)

// fakeShortcutDesktop stands in for the desktop global shortcut adapter.
type fakeShortcutDesktop struct {
	mu         sync.Mutex
	registered map[string]func()
	fail       map[string]error
	// failUnregister makes the next n releases of an accelerator fail.
	failUnregister map[string]int
	host           string
	wayland        bool
	unavailable    bool
	log            []string
}

func newFakeShortcutDesktop() *fakeShortcutDesktop {
	return &fakeShortcutDesktop{registered: map[string]func(){}, fail: map[string]error{}}
}

func (f *fakeShortcutDesktop) RegisterPluginShortcut(accelerator string, fn func()) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[accelerator]; err != nil {
		return err
	}
	if _, exists := f.registered[accelerator]; exists {
		return errors.New("already registered")
	}
	f.registered[accelerator] = fn
	f.log = append(f.log, "register "+accelerator)
	return nil
}

func (f *fakeShortcutDesktop) UnregisterPluginShortcut(accelerator string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failUnregister[accelerator] > 0 {
		f.failUnregister[accelerator]--
		return errors.New("unregister refused")
	}
	delete(f.registered, accelerator)
	f.log = append(f.log, "unregister "+accelerator)
	return nil
}

func (f *fakeShortcutDesktop) HostShortcut() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.host
}

func (f *fakeShortcutDesktop) IsWayland() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wayland
}

func (f *fakeShortcutDesktop) Available() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.unavailable
}

func (f *fakeShortcutDesktop) isRegistered(accelerator string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.registered[accelerator]
	return ok
}

func (f *fakeShortcutDesktop) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registered)
}

// callback returns the function registered for accelerator (nil when none).
func (f *fakeShortcutDesktop) callback(accelerator string) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registered[accelerator]
}

type shortcutHarness struct {
	manager     *pluginManager
	registry    *globalShortcutRegistry
	desk        *fakeShortcutDesktop
	mu          sync.Mutex
	locked      bool
	navigated   []desktop.NavigationTarget
	notices     []pluginNotification
	calls       []contributedActionCall
	activations []string
	events      int
}

func (h *shortcutHarness) setLocked(locked bool) {
	h.mu.Lock()
	h.locked = locked
	h.mu.Unlock()
}

func (h *shortcutHarness) snapshot() (navigated []desktop.NavigationTarget, notices []pluginNotification, calls []contributedActionCall, activations []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append(navigated, h.navigated...), append(notices, h.notices...), append(calls, h.calls...), append(activations, h.activations...)
}

// newShortcutHarness starts a registry for manager with fakes for every
// native seam. invoke nil keeps the real action path.
func newShortcutHarness(t *testing.T, manager *pluginManager, platform string, invoke func(context.Context, contributedActionCall) (bool, error)) *shortcutHarness {
	t.Helper()
	h := &shortcutHarness{manager: manager, desk: newFakeShortcutDesktop()}
	registry := newGlobalShortcutRegistry(manager)
	registry.desk = h.desk
	registry.platform = platform
	registry.locked = func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.locked }
	registry.navigate = func(target desktop.NavigationTarget) error {
		h.mu.Lock()
		h.navigated = append(h.navigated, target)
		h.mu.Unlock()
		return nil
	}
	registry.notify = func(notice pluginNotification) {
		h.mu.Lock()
		h.notices = append(h.notices, notice)
		h.mu.Unlock()
	}
	if invoke != nil {
		registry.invoke = func(ctx context.Context, call contributedActionCall) (globalShortcutRun, error) {
			h.mu.Lock()
			h.calls = append(h.calls, call)
			h.mu.Unlock()
			reached, err := invoke(ctx, call)
			return globalShortcutRun{reached: reached}, err
		}
	}
	registry.onActivation = func(pluginID, shortcutID, token string, _ time.Time, _ globalShortcutRun) bool {
		h.mu.Lock()
		h.activations = append(h.activations, pluginID+"/"+shortcutID+"="+token)
		h.mu.Unlock()
		return true
	}
	registry.emit = func() { h.mu.Lock(); h.events++; h.mu.Unlock() }
	manager.shortcuts.Store(registry)
	registry.start()
	t.Cleanup(func() {
		registry.stop()
		activeGlobalShortcutRegistry.CompareAndSwap(registry, nil)
	})
	h.registry = registry
	return h
}

// press fires accelerator like the OS would and waits for the dispatch.
func (h *shortcutHarness) press(t *testing.T, accelerator string) {
	t.Helper()
	fn := h.desk.callback(accelerator)
	if fn == nil {
		t.Fatalf("%s is not registered", accelerator)
	}
	fn()
	h.registry.wait()
}

func shortcutEntry(id, key, runType, target string) GlobalShortcutEntry {
	run := GlobalShortcutRun{Type: runType}
	if runType == "action" {
		run.Action = target
	} else {
		run.View = target
	}
	return GlobalShortcutEntry{ID: id, Title: "Shortcut " + id, Key: key, Run: run}
}

func shortcutPluginRecord(id string, entries ...GlobalShortcutEntry) *pluginRecord {
	resolved := &resolvedContribution{}
	for _, entry := range entries {
		resolved.Entries = append(resolved.Entries, entry)
	}
	manifest := &Manifest{
		ID: id, Name: "Plugin " + id, APIVersion: 2, Runtime: ProcessRuntimeManifest{Type: "process", Entry: "bin/plugin"},
		Contributes: ManifestContributions{
			Views: []ManifestView{
				{ID: "main", Title: "Main", Entry: "ui/index.html"},
				{ID: "pet", Title: "Pet", Entry: "ui/index.html", Floating: &ManifestFloating{Width: 200, Height: 240}},
			},
			Actions: []ManifestUIAction{{ID: "capture", Risk: "write", UICallable: true, MaxPayloadBytes: 1024}},
		},
	}
	manifest.contributions = map[string]*resolvedContribution{globalShortcutPointKey: resolved}
	return &pluginRecord{id: id, source: pluginSourceLocal, enabled: true, state: pluginStateStopped, manifest: manifest, runtime: &processRuntime{}}
}

func shortcutManager(t *testing.T, records ...*pluginRecord) *pluginManager {
	t.Helper()
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	for _, record := range records {
		manager.records[record.id] = record
		manager.order = append(manager.order, record.id)
	}
	return manager
}

func shortcutStatus(t *testing.T, registry *globalShortcutRegistry, pluginID, id string) GlobalShortcutStatus {
	t.Helper()
	for _, status := range registry.list() {
		if status.PluginID == pluginID && status.ID == id {
			return status
		}
	}
	t.Fatalf("no status for %s/%s", pluginID, id)
	return GlobalShortcutStatus{}
}

func enableShortcut(t *testing.T, registry *globalShortcutRegistry, pluginID, id string) GlobalShortcutStatus {
	t.Helper()
	enabled := true
	status, err := registry.set(pluginID, id, &enabled, nil)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestGlobalShortcutsDefaultOffAndEnableRegisters(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	status := shortcutStatus(t, h.registry, "acme.shot", "capture")
	if status.Enabled || status.State != globalShortcutStateDisabled || h.desk.count() != 0 {
		t.Fatalf("default status %+v, registered %d", status, h.desk.count())
	}
	if status.PluginName != "Plugin acme.shot" || status.DefaultKey != "CmdOrCtrl+Shift+A" || status.Key != "CmdOrCtrl+Shift+A" || status.Title != "Shortcut capture" {
		t.Fatalf("status %+v", status)
	}
	status = enableShortcut(t, h.registry, "acme.shot", "capture")
	if !status.Enabled || status.State != globalShortcutStateEnabled || !h.desk.isRegistered("CmdOrCtrl+Shift+A") {
		t.Fatalf("enabled status %+v", status)
	}
	raw, found, err := pluginKVGetRaw(hostPluginID, globalShortcutSettingKey("acme.shot", "capture"))
	if err != nil || !found {
		t.Fatalf("setting not stored: %v %v", found, err)
	}
	var stored map[string]any
	if json.Unmarshal(raw, &stored) != nil || stored["enabled"] != true || stored["key"] != nil {
		t.Fatalf("stored %s", raw)
	}
	h.mu.Lock()
	events := h.events
	h.mu.Unlock()
	if events == 0 {
		t.Fatal("no change event")
	}

	// A fresh registry reads the stored setting and registers on its own.
	h.registry.stop()
	again := newShortcutHarness(t, h.manager, "mac", nil)
	if !again.desk.isRegistered("CmdOrCtrl+Shift+A") {
		t.Fatal("stored setting not registered on start")
	}
}

func TestGlobalShortcutsSetValidatesAndResets(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	enabled := true
	for name, args := range map[string]struct {
		plugin, id, key string
	}{
		"unknown plugin": {"nope", "capture", ""},
		"unknown entry":  {"acme.shot", "nope", ""},
		"bad key":        {"acme.shot", "capture", "Shift+A"},
		"reserved key":   {"acme.shot", "capture", "Cmd+Q"},
		"garbage key":    {"acme.shot", "capture", "Hyper+A"},
	} {
		key := args.key
		var keyArg *string
		if key != "" {
			keyArg = &key
		}
		if _, err := h.registry.set(args.plugin, args.id, &enabled, keyArg); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	key := "CmdOrCtrl+Alt+S"
	status, err := h.registry.set("acme.shot", "capture", &enabled, &key)
	if err != nil || status.Key != key || status.DefaultKey != "CmdOrCtrl+Shift+A" || !h.desk.isRegistered(key) {
		t.Fatalf("custom key %+v %v", status, err)
	}
	status, err = h.registry.reset("acme.shot", "capture")
	if err != nil || status.Key != "CmdOrCtrl+Shift+A" || !status.Enabled || h.desk.isRegistered(key) || !h.desk.isRegistered("CmdOrCtrl+Shift+A") {
		t.Fatalf("reset %+v %v", status, err)
	}
	disabled := false
	if status, err = h.registry.set("acme.shot", "capture", &disabled, nil); err != nil || status.State != globalShortcutStateDisabled || h.desk.count() != 0 {
		t.Fatalf("disable %+v %v", status, err)
	}
	if _, found, _ := pluginKVGetRaw(hostPluginID, globalShortcutSettingKey("acme.shot", "capture")); found {
		t.Fatal("default setting kept in KV")
	}
}

func TestGlobalShortcutsSamePhysicalComboConflicts(t *testing.T) {
	first := shortcutPluginRecord("acme.first", shortcutEntry("go", "CmdOrCtrl+Shift+K", "view", "main"))
	second := shortcutPluginRecord("acme.second", shortcutEntry("go", "Super+Shift+K", "view", "main"))
	h := newShortcutHarness(t, shortcutManager(t, second, first), "mac", nil)
	enableShortcut(t, h.registry, "acme.first", "go")
	status := enableShortcut(t, h.registry, "acme.second", "go")
	// On macOS CmdOrCtrl and Super are both Command: the later one conflicts.
	if status.State != globalShortcutStateConflict || status.ConflictWith != "acme.first/go" || h.desk.isRegistered("Super+Shift+K") {
		t.Fatalf("second %+v", status)
	}
	if first := shortcutStatus(t, h.registry, "acme.first", "go"); first.State != globalShortcutStateEnabled {
		t.Fatalf("first %+v", first)
	}
	// The earlier claim survives a full re-evaluation even though the later
	// plugin comes first in manager order.
	h.registry.reconcile(true)
	if shortcutStatus(t, h.registry, "acme.second", "go").State != globalShortcutStateConflict {
		t.Fatal("claim order flipped")
	}
	// Turning the first off frees the combo for the second.
	disabled := false
	if _, err := h.registry.set("acme.first", "go", &disabled, nil); err != nil {
		t.Fatal(err)
	}
	if shortcutStatus(t, h.registry, "acme.second", "go").State != globalShortcutStateEnabled || !h.desk.isRegistered("Super+Shift+K") {
		t.Fatal("second not registered after the first was turned off")
	}

	// On Windows the same two keys are different physical combos.
	other := newShortcutHarness(t, shortcutManager(t,
		shortcutPluginRecord("acme.first", shortcutEntry("go", "CmdOrCtrl+Shift+K", "view", "main")),
		shortcutPluginRecord("acme.second", shortcutEntry("go", "Super+Shift+K", "view", "main"))), "windows", nil)
	enableShortcut(t, other.registry, "acme.first", "go")
	if status := enableShortcut(t, other.registry, "acme.second", "go"); status.State != globalShortcutStateEnabled {
		t.Fatalf("windows %+v", status)
	}
}

func TestGlobalShortcutsHostShortcutConflict(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "Cmd+Shift+Space", "action", "capture"))
	manager := shortcutManager(t, record)
	h := newShortcutHarness(t, manager, "mac", nil)
	h.desk.mu.Lock()
	h.desk.host = "CmdOrCtrl+Shift+Space"
	h.desk.mu.Unlock()
	status := enableShortcut(t, h.registry, "acme.shot", "capture")
	if status.State != globalShortcutStateConflict || status.ConflictWith != globalShortcutHostConflict || h.desk.count() != 0 {
		t.Fatalf("status %+v", status)
	}
}

func TestRegistryHostShortcutChangeEvictsPlugin(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+K", "action", "capture"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	enableShortcut(t, h.registry, "acme.shot", "capture")
	if !h.desk.isRegistered("CmdOrCtrl+Shift+K") {
		t.Fatal("plugin shortcut not registered")
	}
	// Drive the real desktop_set_global_shortcut command: the plugin's key
	// must be gone before MinCode registers its own.
	var pluginHeldKey bool
	desktop.SetAdapter(desktop.Adapter{SetGlobalShortcut: func(enabled bool, accelerator string) error {
		pluginHeldKey = h.desk.isRegistered("CmdOrCtrl+Shift+K")
		h.desk.mu.Lock()
		if enabled {
			h.desk.host = accelerator
		} else {
			h.desk.host = ""
		}
		h.desk.mu.Unlock()
		return nil
	}})
	t.Cleanup(func() { desktop.SetAdapter(desktop.Adapter{}) })
	if _, err := core.Dispatch("desktop_set_global_shortcut", `{"enabled":true,"accelerator":"Cmd+Shift+K"}`); err != nil {
		t.Fatal(err)
	}
	if pluginHeldKey {
		t.Fatal("plugin still held the key when MinCode registered it")
	}
	status := shortcutStatus(t, h.registry, "acme.shot", "capture")
	if status.State != globalShortcutStateConflict || status.ConflictWith != globalShortcutHostConflict || h.desk.isRegistered("CmdOrCtrl+Shift+K") {
		t.Fatalf("after host change %+v", status)
	}
	// MinCode moves away again: the plugin gets its key back.
	if _, err := core.Dispatch("desktop_set_global_shortcut", `{"enabled":true,"accelerator":"CmdOrCtrl+Shift+Space"}`); err != nil {
		t.Fatal(err)
	}
	if status := shortcutStatus(t, h.registry, "acme.shot", "capture"); status.State != globalShortcutStateEnabled || !h.desk.isRegistered("CmdOrCtrl+Shift+K") {
		t.Fatalf("after host moved away %+v", status)
	}
}

func TestGlobalShortcutsRegisterFailureIsOccupied(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	h.desk.mu.Lock()
	h.desk.fail["CmdOrCtrl+Shift+A"] = errors.New("taken by another application")
	h.desk.mu.Unlock()
	if status := enableShortcut(t, h.registry, "acme.shot", "capture"); status.State != globalShortcutStateOccupied {
		t.Fatalf("status %+v", status)
	}
	// The other application lets go; turning it on again retries.
	h.desk.mu.Lock()
	delete(h.desk.fail, "CmdOrCtrl+Shift+A")
	h.desk.mu.Unlock()
	if status := enableShortcut(t, h.registry, "acme.shot", "capture"); status.State != globalShortcutStateEnabled {
		t.Fatalf("retry %+v", status)
	}
}

func TestGlobalShortcutsWaylandAndUnavailableAreUnsupported(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"), shortcutEntry("pet", "CmdOrCtrl+Shift+P", "floating", "pet"))
	h := newShortcutHarness(t, shortcutManager(t, record), "linux", nil)
	h.desk.mu.Lock()
	h.desk.wayland = true
	h.desk.mu.Unlock()
	h.registry.reconcile(true)
	enableShortcut(t, h.registry, "acme.shot", "capture")
	for _, status := range h.registry.list() {
		if status.State != globalShortcutStateUnsupported {
			t.Fatalf("wayland %+v", status)
		}
	}
	if h.desk.count() != 0 {
		t.Fatal("registered on Wayland")
	}
	h.desk.mu.Lock()
	h.desk.wayland, h.desk.unavailable = false, true
	h.desk.mu.Unlock()
	h.registry.reconcile(true)
	if status := shortcutStatus(t, h.registry, "acme.shot", "capture"); status.State != globalShortcutStateUnsupported {
		t.Fatalf("no desktop adapter %+v", status)
	}
}

func waitForShortcut(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestGlobalShortcutsFollowPluginLifecycle(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	enableShortcut(t, h.registry, "acme.shot", "capture")

	// Disable: unregistered, setting kept.
	if err := h.manager.SetEnabled(context.Background(), "acme.shot", false); err != nil {
		t.Fatal(err)
	}
	waitForShortcut(t, "disable to unregister", func() bool { return !h.desk.isRegistered("CmdOrCtrl+Shift+A") })
	if status := shortcutStatus(t, h.registry, "acme.shot", "capture"); status.State != globalShortcutStateInactive || !status.Enabled {
		t.Fatalf("disabled plugin %+v", status)
	}
	// Enable: restored.
	if err := h.manager.SetEnabled(context.Background(), "acme.shot", true); err != nil {
		t.Fatal(err)
	}
	waitForShortcut(t, "enable to register", func() bool { return h.desk.isRegistered("CmdOrCtrl+Shift+A") })

	// Not effective in the active workspace: unregistered.
	record.mu.Lock()
	record.scope, record.workspaces = pluginScopeWorkspace, map[string]struct{}{"ws-a": {}}
	record.mu.Unlock()
	setWorkspaceScopeFixture(h.manager, "ws-b")
	h.manager.emitContributionsChanged("", nil)
	waitForShortcut(t, "workspace switch to unregister", func() bool { return !h.desk.isRegistered("CmdOrCtrl+Shift+A") })
	setWorkspaceScopeFixture(h.manager, "ws-a")
	h.manager.emitContributionsChanged("", nil)
	waitForShortcut(t, "workspace switch back to register", func() bool { return h.desk.isRegistered("CmdOrCtrl+Shift+A") })

	// Unrelated point updates do not re-evaluate.
	before := h.registry.reconciles.Load()
	h.manager.emitContributionsChanged("acme.shot", []string{statusBarPointKey})
	h.registry.wait()
	if h.registry.reconciles.Load() != before {
		t.Fatal("status bar update re-evaluated global shortcuts")
	}

	// Uninstall: unregistered and the setting is gone.
	h.manager.mu.Lock()
	delete(h.manager.records, "acme.shot")
	h.manager.order = nil
	h.manager.mu.Unlock()
	h.manager.runRecordRemovalHooks("acme.shot", removalReasonUninstall)
	h.registry.wait()
	if h.desk.isRegistered("CmdOrCtrl+Shift+A") {
		t.Fatal("uninstalled plugin still registered")
	}
	if _, found, _ := pluginKVGetRaw(hostPluginID, globalShortcutSettingKey("acme.shot", "capture")); found {
		t.Fatal("uninstall kept the setting")
	}
	if len(h.registry.list()) != 0 {
		t.Fatal("uninstalled plugin still listed")
	}
}

func TestGlobalShortcutsClearDataKeepsSettings(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	enableShortcut(t, h.registry, "acme.shot", "capture")
	h.manager.runRecordRemovalHooks("acme.shot", removalReasonClearData)
	h.registry.wait()
	if _, found, _ := pluginKVGetRaw(hostPluginID, globalShortcutSettingKey("acme.shot", "capture")); !found {
		t.Fatal("Clear Plugin Data removed a host-owned shortcut setting")
	}
}

func TestLockUnregistersAndRestores(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("open", "CmdOrCtrl+Shift+O", "view", "main"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	enableShortcut(t, h.registry, "acme.shot", "open")
	stale := h.desk.callback("CmdOrCtrl+Shift+O")
	token := issueActivationToken("acme.shot", "open")

	h.setLocked(true)
	AppLockChanged()
	if h.desk.isRegistered("CmdOrCtrl+Shift+O") {
		t.Fatal("locked app kept the plugin shortcut")
	}
	if _, ok := consumeActivationToken("acme.shot", token); ok {
		t.Fatal("locking kept an activation token")
	}
	// A press that raced the lock does nothing.
	stale()
	h.registry.wait()
	if navigated, notices, _, activations := h.snapshot(); len(navigated) != 0 || len(activations) != 0 || len(notices) != 0 {
		t.Fatalf("locked press dispatched: %v %v %v", navigated, notices, activations)
	}

	h.setLocked(false)
	AppLockChanged()
	if !h.desk.isRegistered("CmdOrCtrl+Shift+O") {
		t.Fatal("unlock did not restore the shortcut")
	}
	h.press(t, "CmdOrCtrl+Shift+O")
	if navigated, _, _, _ := h.snapshot(); len(navigated) != 1 {
		t.Fatalf("navigated %v", navigated)
	}
}

func TestGlobalShortcutTriggersViewAndFloating(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("open", "CmdOrCtrl+Shift+O", "view", "main"), shortcutEntry("pet", "CmdOrCtrl+Shift+P", "floating", "pet"))
	record.state = pluginStateRunning
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	driver := newFakeFloatingDriver(t)
	enableShortcut(t, h.registry, "acme.shot", "open")
	enableShortcut(t, h.registry, "acme.shot", "pet")

	h.press(t, "CmdOrCtrl+Shift+O")
	navigated, _, _, _ := h.snapshot()
	if len(navigated) != 1 || navigated[0].Kind != "plugin" || navigated[0].Value != `{"pluginId":"acme.shot","viewId":"main"}` {
		t.Fatalf("navigated %+v", navigated)
	}
	name := floatingWindowName("acme.shot", "pet")
	h.press(t, "CmdOrCtrl+Shift+P")
	if !driver.isOpen(name) {
		t.Fatal("floating view not shown")
	}
	h.press(t, "CmdOrCtrl+Shift+P")
	if driver.isOpen(name) {
		t.Fatal("floating view not hidden")
	}
}

func TestGlobalShortcutActionPayloadCarriesTrigger(t *testing.T) {
	entry := shortcutEntry("capture-region", "CmdOrCtrl+Shift+A", "action", "capture")
	entry.Run.Payload = json.RawMessage(`{"mode":"region"}`)
	bare := shortcutEntry("capture", "CmdOrCtrl+Shift+B", "action", "capture")
	record := shortcutPluginRecord("acme.shot", entry, bare)
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", func(context.Context, contributedActionCall) (bool, error) { return true, nil })
	enableShortcut(t, h.registry, "acme.shot", "capture-region")
	enableShortcut(t, h.registry, "acme.shot", "capture")
	h.press(t, "CmdOrCtrl+Shift+A")
	h.press(t, "CmdOrCtrl+Shift+B")
	_, notices, calls, activations := h.snapshot()
	if len(calls) != 2 || len(notices) != 0 {
		t.Fatalf("calls %+v notices %+v", calls, notices)
	}
	for index, want := range []string{
		`{"trigger":{"type":"globalShortcut","id":"capture-region"},"mode":"region"}`,
		`{"trigger":{"type":"globalShortcut","id":"capture"}}`,
	} {
		call := calls[index]
		if string(call.Payload) != want || call.Point != globalShortcutPointKey || call.ActionID != "capture" || call.gesture == nil {
			t.Fatalf("call %d: %+v payload %s", index, call, call.Payload)
		}
		if strings.Contains(string(call.Payload), "token") || strings.Contains(string(call.Payload), "activation") {
			t.Fatal("activation token leaked into the payload")
		}
	}
	// The token is issued after the action, separately from the payload.
	if len(activations) != 2 || !strings.HasPrefix(activations[0], "acme.shot/capture-region=") {
		t.Fatalf("activations %v", activations)
	}
}

func TestGlobalShortcutTriggerFitsReserve(t *testing.T) {
	id := strings.Repeat("a", 32)
	added := len(globalShortcutActionPayload(json.RawMessage(`{"x":1}`), id)) - len(`{"x":1}`)
	if added > globalShortcutTriggerReserve {
		t.Fatalf("trigger adds %d bytes, reserve is %d", added, globalShortcutTriggerReserve)
	}
	if bare := len(globalShortcutActionPayload(nil, id)); bare > globalShortcutTriggerReserve {
		t.Fatalf("bare trigger is %d bytes", bare)
	}
}

func TestGlobalShortcutHostRejectionNotifies(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("open", "CmdOrCtrl+Shift+O", "view", "main"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	enableShortcut(t, h.registry, "acme.shot", "open")
	stale := h.desk.callback("CmdOrCtrl+Shift+O")
	// The plugin fails between registration and the press.
	record.mu.Lock()
	record.state = pluginStateError
	record.mu.Unlock()
	stale()
	h.registry.wait()
	navigated, notices, _, _ := h.snapshot()
	if len(navigated) != 0 || len(notices) != 1 || !notices[0].System || notices[0].PluginID != "acme.shot" || notices[0].PluginName != "Plugin acme.shot" || notices[0].Code != "globalShortcut.unavailable" {
		t.Fatalf("navigated %v notices %+v", navigated, notices)
	}
}

// shortcutProcessManager is a stopped, enabled on-demand process plugin
// (the test helper binary) with one global shortcut per action.
func shortcutProcessManager(t *testing.T, actions ...ManifestUIAction) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := activationManager(t, "")
	if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
		t.Fatal(err)
	}
	resolved := &resolvedContribution{}
	record.mu.Lock()
	for index, action := range actions {
		record.manifest.Contributes.Actions = append(record.manifest.Contributes.Actions, action)
		resolved.Entries = append(resolved.Entries, shortcutEntry(action.ID, "CmdOrCtrl+Shift+"+string(rune('1'+index)), "action", action.ID))
	}
	record.manifest.contributions = map[string]*resolvedContribution{globalShortcutPointKey: resolved}
	record.mu.Unlock()
	return manager, record
}

func TestTriggerStartsStoppedPlugin(t *testing.T) {
	manager, record := shortcutProcessManager(t, ManifestUIAction{ID: "save", Risk: "write", UICallable: true, MaxPayloadBytes: 1024})
	h := newShortcutHarness(t, manager, "mac", nil)
	enableShortcut(t, h.registry, record.id, "save")
	record.mu.Lock()
	state := record.state
	record.mu.Unlock()
	if state == pluginStateRunning {
		t.Fatal("plugin already running")
	}
	h.press(t, "CmdOrCtrl+Shift+1")
	_, notices, _, activations := h.snapshot()
	if len(notices) != 0 {
		t.Fatalf("notices %+v", notices)
	}
	record.mu.Lock()
	state = record.state
	record.mu.Unlock()
	if state != pluginStateRunning {
		t.Fatalf("state %s", state)
	}
	// The write action ran without any host confirmation and a token followed.
	if len(activations) != 1 || !strings.HasPrefix(activations[0], record.id+"/save=") {
		t.Fatalf("activations %v", activations)
	}
	token := strings.TrimPrefix(activations[0], record.id+"/save=")
	if shortcutID, ok := consumeActivationToken(record.id, token); !ok || shortcutID != "save" {
		t.Fatal("issued token is not valid")
	}
}

func TestTriggerNotifiesOnStartFailure(t *testing.T) {
	manager, record := shortcutProcessManager(t, ManifestUIAction{ID: "save", Risk: "write", UICallable: true, MaxPayloadBytes: 1024})
	h := newShortcutHarness(t, manager, "mac", nil)
	enableShortcut(t, h.registry, record.id, "save")
	if err := os.Remove(filepath.Join(record.root, "helper.exe")); err != nil {
		t.Fatal(err)
	}
	h.press(t, "CmdOrCtrl+Shift+1")
	_, notices, _, activations := h.snapshot()
	if len(notices) != 1 || !notices[0].System || notices[0].Level != "error" || notices[0].Code != "globalShortcut.startFailed" || len(activations) != 0 {
		t.Fatalf("notices %+v activations %v", notices, activations)
	}
}

func TestGlobalShortcutGestureIsRequiredAndSingleUse(t *testing.T) {
	manager, record := shortcutProcessManager(t,
		ManifestUIAction{ID: "save", Risk: "write", UICallable: true, MaxPayloadBytes: 1024},
		ManifestUIAction{ID: "wipe", Risk: "dangerous", UICallable: true, MaxPayloadBytes: 1024})
	call := func(entryID, actionID, point string, gesture *globalShortcutGesture) error {
		c := contributedActionCall{PluginID: record.id, Point: point, EntryID: entryID, ActionID: actionID,
			RequestID: globalShortcutRequestID(), Payload: globalShortcutActionPayload(nil, entryID), gesture: gesture}
		if point == globalShortcutPointKey {
			c.Validate = func(record *pluginRecord) error { return globalShortcutEntryValidator(entryID, actionID)(record) }
		}
		_, err := manager.invokeContributedAction(context.Background(), c)
		return err
	}
	if err := call("save", "save", globalShortcutPointKey, nil); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("no gesture: %v", err)
	}
	if err := call("save", "save", globalShortcutPointKey, newGlobalShortcutGesture(record.id, "other", "save")); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("gesture for another entry: %v", err)
	}
	if err := call("save", "save", globalShortcutPointKey, newGlobalShortcutGesture("other.plugin", "save", "save")); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("gesture for another plugin: %v", err)
	}
	gesture := newGlobalShortcutGesture(record.id, "save", "save")
	if err := call("save", "save", globalShortcutPointKey, gesture); err != nil {
		t.Fatalf("valid gesture: %v", err)
	}
	if err := call("save", "save", globalShortcutPointKey, gesture); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("reused gesture: %v", err)
	}
	if err := call("save", "save", "formatters", newGlobalShortcutGesture(record.id, "save", "save")); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("gesture on another point: %v", err)
	}
	// Install validation already refuses dangerous actions; the gate refuses
	// them again even with a fresh gesture.
	if err := call("wipe", "wipe", globalShortcutPointKey, newGlobalShortcutGesture(record.id, "wipe", "wipe")); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("dangerous action: %v", err)
	}
	assertNoContributionRequestsLeft(t)
}

func TestGlobalShortcutGestureCannotComeFromOutside(t *testing.T) {
	// Pages, commands and plugins reach actions through JSON (frontend commands,
	// JSON-RPC); the gesture is an unexported pointer with unexported fields,
	// so no decoded request can carry one.
	field, ok := reflect.TypeOf(contributedActionCall{}).FieldByName("gesture")
	if !ok || field.IsExported() || field.Type.Kind() != reflect.Pointer {
		t.Fatalf("gesture field %+v", field)
	}
	for index := 0; index < field.Type.Elem().NumField(); index++ {
		if field.Type.Elem().Field(index).IsExported() {
			t.Fatalf("gesture field %s is exported", field.Type.Elem().Field(index).Name)
		}
	}
	// No plugin-facing host method touches global shortcut settings.
	for _, registration := range registeredHostMethods() {
		if strings.Contains(strings.ToLower(registration.name), "shortcut") {
			t.Fatalf("host method %s exposes global shortcuts to plugins", registration.name)
		}
	}
}

func TestGlobalShortcutInAppOverlap(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	if err := h.registry.setInAppKeys([]string{"Cmd+Shift+A", "not a key", ""}); err != nil {
		t.Fatal(err)
	}
	status := shortcutStatus(t, h.registry, "acme.shot", "capture")
	if !status.InAppOverlap {
		t.Fatalf("status %+v", status)
	}
	if err := h.registry.setInAppKeys(make([]string, globalShortcutInAppKeysMax+1)); err == nil {
		t.Fatal("unbounded in-app key list accepted")
	}
}

func TestGlobalShortcutSettingsStayLocal(t *testing.T) {
	record := shortcutPluginRecord("acme.shot", shortcutEntry("capture", "CmdOrCtrl+Shift+A", "action", "capture"))
	h := newShortcutHarness(t, shortcutManager(t, record), "mac", nil)
	enableShortcut(t, h.registry, "acme.shot", "capture")
	key := globalShortcutSettingKey("acme.shot", "capture")
	if !strings.HasPrefix(key, globalShortcutSettingPrefix) {
		t.Fatalf("key %q", key)
	}
	// Settings export (and config sync, which ships the same export) carries
	// only plugin namespaces and .configuration documents, never this key.
	target := filepath.Join(t.TempDir(), "export.json")
	if _, err := writeConfigExportWithPlugins(target, map[string]json.RawMessage{}); err != nil {
		t.Fatal(err)
	}
	exported, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(exported), globalShortcutSettingPrefix) {
		t.Fatalf("export carries global shortcut settings: %s", exported)
	}
	ids, err := pluginIDsWithData()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == hostPluginID {
			t.Fatal("host namespace is exportable plugin data")
		}
	}
	entries, _, err := h.manager.collectPluginBackupEntries(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Path, globalShortcutSettingPrefix) || strings.Contains(string(entry.Data), globalShortcutSettingPrefix) {
			t.Fatalf("backup entry %s carries global shortcut settings", entry.Path)
		}
	}
}

func TestActivationTokenSingleUseExpiryAndOwner(t *testing.T) {
	unlockAppForTest(t)
	now := time.Unix(1_800_000_000, 0)
	var mu sync.Mutex
	restore := globalShortcutActivations.setClock(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	t.Cleanup(restore)
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	token := issueActivationToken("acme.shot", "capture")
	if len(token) < 32 {
		t.Fatalf("token %q", token)
	}
	if _, ok := consumeActivationToken("other.plugin", token); ok {
		t.Fatal("another plugin used the token")
	}
	if id, ok := consumeActivationToken("acme.shot", token); !ok || id != "capture" {
		t.Fatal("owner could not use the token")
	}
	if _, ok := consumeActivationToken("acme.shot", token); ok {
		t.Fatal("token used twice")
	}

	expiring := issueActivationToken("acme.shot", "capture")
	advance(globalShortcutActivationTTL - time.Second)
	if !activationTokenValid("acme.shot", expiring) {
		t.Fatal("token expired early")
	}
	advance(2 * time.Second)
	if _, ok := consumeActivationToken("acme.shot", expiring); ok {
		t.Fatal("expired token accepted")
	}

	revoked := issueActivationToken("acme.shot", "capture")
	kept := issueActivationToken("other.plugin", "x")
	revokeActivationTokens("acme.shot")
	if activationTokenValid("acme.shot", revoked) || !activationTokenValid("other.plugin", kept) {
		t.Fatal("revocation is not per plugin")
	}
	if _, ok := consumeActivationToken("acme.shot", ""); ok {
		t.Fatal("empty token accepted")
	}
}
