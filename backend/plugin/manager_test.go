package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

type testLifecyclePlugin struct {
	startupCalls  atomic.Int64
	shutdownCalls atomic.Int64
	panicStartup  bool
	panicInvoke   bool
	waitForCancel bool
}

func (plugin *testLifecyclePlugin) Meta() api.Meta {
	return api.Meta{ID: "test.plugin", Name: "Test Plugin", Version: "1.0.0"}
}
func (plugin *testLifecyclePlugin) OnStartup(ctx context.Context) error {
	plugin.startupCalls.Add(1)
	if plugin.panicStartup {
		panic("startup failed")
	}
	if plugin.waitForCancel {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func (plugin *testLifecyclePlugin) OnShutdown(context.Context) error {
	plugin.shutdownCalls.Add(1)
	return nil
}
func (plugin *testLifecyclePlugin) Invoke(context.Context, string, string) (string, error) {
	if plugin.panicInvoke {
		panic("invoke failed")
	}
	return `{"ok":true}`, nil
}

func testManagerWithPlugin(plugin api.Plugin, enabled bool) (*pluginManager, *pluginRecord) {
	state := pluginStateDisabled
	if enabled {
		state = pluginStateStopped
	}
	record := &pluginRecord{id: plugin.Meta().ID, source: pluginSourceBuiltin, builtin: plugin, enabled: enabled, state: state}
	return &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}}, record
}

func TestManagerLifecycleOnlyStartsEnabledPlugins(t *testing.T) {
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, false)
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if plugin.startupCalls.Load() != 0 {
		t.Fatal("disabled plugin was started")
	}
	record.enabled = true
	if err := manager.startRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if plugin.startupCalls.Load() != 1 || record.state != pluginStateRunning {
		t.Fatalf("unexpected lifecycle calls=%d state=%s", plugin.startupCalls.Load(), record.state)
	}
	if err := manager.stopRecord(context.Background(), record, true); err != nil {
		t.Fatal(err)
	}
	if plugin.shutdownCalls.Load() != 1 || record.state != pluginStateDisabled {
		t.Fatalf("unexpected shutdown calls=%d state=%s", plugin.shutdownCalls.Load(), record.state)
	}
}

func TestManagerRecoversPluginPanics(t *testing.T) {
	plugin := &testLifecyclePlugin{panicStartup: true}
	manager, record := testManagerWithPlugin(plugin, true)
	if err := manager.startRecord(context.Background(), record); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("expected startup panic error, got %v", err)
	}
	if record.state != pluginStateError {
		t.Fatalf("expected error state, got %s", record.state)
	}
	plugin.panicStartup = false
	plugin.panicInvoke = true
	record.state = pluginStateRunning
	if _, err := manager.Invoke(context.Background(), record.id, "run", ""); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("expected invoke panic error, got %v", err)
	}
}

func TestManagerBoundsLifecycleStartup(t *testing.T) {
	plugin := &testLifecyclePlugin{waitForCancel: true}
	manager, record := testManagerWithPlugin(plugin, true)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	err := manager.startRecord(ctx, record)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded startup error, got %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("startup timeout took too long: %s", elapsed)
	}
	// The caller stops waiting, but activation keeps running until it finishes,
	// hits pluginStartupTimeout, or the manager shuts down.
	record.mu.Lock()
	state := record.state
	record.mu.Unlock()
	if state != pluginStateStarting {
		t.Fatalf("expected activation to keep starting, got %s", state)
	}
	manager.Shutdown(context.Background())
	deadline := time.Now().Add(time.Second)
	for {
		record.mu.Lock()
		state = record.state
		record.mu.Unlock()
		if state == pluginStateError || state == pluginStateStopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shutdown did not cancel activation, state %s", state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInstallLocalRejectsSymlinksAndRegistersDisabled(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	source := writeTestManifest(t, nil)
	manifest, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.record(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.enabled || record.state != pluginStateDisabled {
		t.Fatalf("installed plugin must be disabled: enabled=%v state=%s", record.enabled, record.state)
	}
	if _, err := os.Stat(filepath.Join(pluginsDirectory(), manifest.ID, manifestFilename)); err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS != "windows" {
		symlinkSource := writeTestManifest(t, func(manifest *Manifest) { manifest.ID = "symlink.plugin" })
		if err := os.Symlink(filepath.Join(symlinkSource, manifestFilename), filepath.Join(symlinkSource, "manifest-link")); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.InstallLocal(symlinkSource); err == nil {
			t.Fatal("expected symlink package rejection")
		}
	}
}

func TestManagerRecordMissing(t *testing.T) {
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	if _, err := manager.record("missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBeginUIWaitsForPluginLifecycle(t *testing.T) {
	uiHost := newPluginUIHost()
	t.Cleanup(uiHost.Shutdown)
	runtimeHost := &processRuntime{generation: 1}
	record := &pluginRecord{
		id:      "visual.plugin",
		source:  pluginSourceLocal,
		root:    t.TempDir(),
		enabled: true,
		state:   pluginStateRunning,
		manifest: &Manifest{
			ID: "visual.plugin",
			Contributes: ManifestContributions{Views: []ManifestView{{
				ID: "dashboard", Title: "Dashboard", Entry: "ui/index.html",
			}}},
		},
		runtime: runtimeHost,
	}
	manager := &pluginManager{
		records: map[string]*pluginRecord{record.id: record},
		order:   []string{record.id},
		uiHost:  uiHost,
	}

	record.lifecycleMu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := manager.BeginUI(record.id, "dashboard", "tango://app:9250")
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		record.lifecycleMu.Unlock()
		t.Fatalf("UI session crossed an active lifecycle transition: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	record.lifecycleMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("UI session did not resume after lifecycle transition")
	}
}

func TestNormalizePluginCommandRejectsUnknownRisk(t *testing.T) {
	command := ManifestCommand{ID: "example.run", Title: "Run", Action: "run"}
	normalized, valid := normalizePluginCommand("example.plugin", "Example", command)
	if !valid || normalized.Risk != "read" {
		t.Fatalf("empty risk should normalize to read: valid=%v risk=%q", valid, normalized.Risk)
	}
	command.Risk = "unexpected"
	if _, valid := normalizePluginCommand("example.plugin", "Example", command); valid {
		t.Fatal("unknown command risk must be rejected")
	}
}

func TestHostCommandsCarryEffectiveRisk(t *testing.T) {
	actions := map[string]ManifestUIAction{
		"deleteAll": {ID: "deleteAll", Risk: "dangerous"},
		"fetch":     {ID: "fetch", Risk: "network"},
		"list":      {ID: "list", Risk: "read"},
	}
	commands := hostCommandsForRecord(pluginMeta{ID: "example.plugin", Name: "Example"}, []ManifestCommand{
		{ID: "wipe", Title: "Wipe", Action: "deleteAll"},
		{ID: "pull", Title: "Pull", Action: "fetch", Risk: "write"},
		{ID: "show", Title: "Show", Action: "list"},
		{ID: "save", Title: "Save", Action: "list", Risk: "write"},
		{ID: "builtin", Title: "Builtin", Action: "unknownAction"},
		{ID: "wipe", Title: "Duplicate", Action: "list"},
	}, actions)
	got := map[string][2]string{}
	for _, command := range commands {
		got[command.ID] = [2]string{command.Risk, command.EffectiveRisk}
	}
	want := map[string][2]string{
		"wipe":    {"read", "dangerous"},
		"pull":    {"write", "write"},
		"show":    {"read", "read"},
		"save":    {"write", "write"},
		"builtin": {"read", "read"},
	}
	if len(commands) != len(want) {
		t.Fatalf("commands = %+v", commands)
	}
	for id, risks := range want {
		if got[id] != risks {
			t.Fatalf("%s: risk/effectiveRisk = %v, want %v", id, got[id], risks)
		}
	}
	encoded, err := json.Marshal(commands[0])
	if err != nil || !strings.Contains(string(encoded), `"risk":"read"`) || !strings.Contains(string(encoded), `"effectiveRisk":"dangerous"`) {
		t.Fatalf("encoded command = %s, %v", encoded, err)
	}
}

func TestUninstallPreservesPluginDataForReinstall(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	source := writeTestManifest(t, nil)
	manifest, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := pluginKVSetRaw(manifest.ID, "doc.example", []byte(`{"title":"kept"}`)); err != nil {
		t.Fatal(err)
	}
	if err := pluginSecretSet(manifest.ID, "profile.password", "kept"); err != nil {
		t.Fatal(err)
	}
	if err := writePluginEnabled(manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	record, err := manager.record(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	installedRoot := record.root

	if err := manager.Uninstall(context.Background(), manifest.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(installedRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin files still exist after uninstall: %v", err)
	}
	if _, err := manager.record(manifest.ID); err == nil {
		t.Fatal("plugin record still exists after uninstall")
	}
	if _, found, err := readPluginEnabled(manifest.ID); err != nil || found {
		t.Fatalf("host install state was not removed: found=%v err=%v", found, err)
	}
	saved, found, err := pluginKVGetRaw(manifest.ID, "doc.example")
	if err != nil || !found || string(saved) != `{"title":"kept"}` {
		t.Fatalf("plugin data was lost during uninstall: saved=%s found=%v err=%v", saved, found, err)
	}
	if keys, err := pluginSecretList(manifest.ID); err != nil || len(keys) != 1 {
		t.Fatalf("saved secrets were lost during uninstall: %v %v", keys, err)
	}

	if _, err := manager.InstallLocal(source); err != nil {
		t.Fatal(err)
	}
	saved, found, err = pluginKVGetRaw(manifest.ID, "doc.example")
	if err != nil || !found || string(saved) != `{"title":"kept"}` {
		t.Fatalf("plugin data was not restored after reinstall: saved=%s found=%v err=%v", saved, found, err)
	}
	if value, found, err := pluginSecretGet(manifest.ID, "profile.password"); err != nil || !found || value != "kept" {
		t.Fatalf("saved secret was not restored after reinstall: %q found=%v err=%v", value, found, err)
	}
}

func TestUninstallWithPurgeDeletesPluginData(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	source := writeTestManifest(t, nil)
	manifest, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := pluginKVSetRaw(manifest.ID, "doc.example", []byte(`{"title":"gone"}`)); err != nil {
		t.Fatal(err)
	}
	if err := pluginSecretSet(manifest.ID, "profile.password", "gone"); err != nil {
		t.Fatal(err)
	}
	storage, err := pluginStorageRoot(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storage, "note.txt"), []byte("gone"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := manager.Uninstall(context.Background(), manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, found, err := pluginKVGetRaw(manifest.ID, "doc.example"); err != nil || found {
		t.Fatalf("plugin KV survived purge: found=%v err=%v", found, err)
	}
	if keys, err := pluginSecretList(manifest.ID); err != nil || len(keys) != 0 {
		t.Fatalf("saved secrets survived purge: %v %v", keys, err)
	}
	if _, err := os.Stat(storage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin file storage survived purge: %v", err)
	}
	if _, err := manager.record(manifest.ID); err == nil {
		t.Fatal("plugin record still exists after uninstall")
	}
}

func TestClearPluginDataRemovesKVButKeepsInstall(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	source := writeTestManifest(t, nil)
	manifest, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := pluginKVSetRaw(manifest.ID, "doc.example", []byte(`{"title":"secret"}`)); err != nil {
		t.Fatal(err)
	}
	if err := pluginKVSetRaw(manifest.ID, "profile.main", []byte(`{"host":"broker:9092"}`)); err != nil {
		t.Fatal(err)
	}
	if err := writePluginEnabled(manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	record, err := manager.record(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	installedRoot := record.root

	if err := manager.ClearPluginData(context.Background(), manifest.ID); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"doc.example", "profile.main"} {
		if _, found, err := pluginKVGetRaw(manifest.ID, key); err != nil || found {
			t.Fatalf("plugin data key %q survived clear: found=%v err=%v", key, found, err)
		}
	}
	if _, err := manager.record(manifest.ID); err != nil {
		t.Fatalf("plugin record lost after clearing data: %v", err)
	}
	if _, err := os.Stat(installedRoot); err != nil {
		t.Fatalf("plugin files were removed while clearing data: %v", err)
	}
	enabled, found, err := readPluginEnabled(manifest.ID)
	if err != nil || !found || !enabled {
		t.Fatalf("enabled state changed while clearing data: enabled=%v found=%v err=%v", enabled, found, err)
	}
	if err := manager.ClearPluginData(context.Background(), "missing.plugin"); err == nil {
		t.Fatal("clearing data of an unknown plugin must fail")
	}
}

type gatedStartupPlugin struct {
	testLifecyclePlugin
	release chan struct{}
}

func (plugin *gatedStartupPlugin) OnStartup(ctx context.Context) error {
	select {
	case <-plugin.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestStartupInBackgroundDoesNotBlockAndBuiltinAdmissionWaits(t *testing.T) {
	plugin := &gatedStartupPlugin{release: make(chan struct{})}
	manager, record := testManagerWithPlugin(plugin, true)
	finished := make(chan error, 1)
	startedAt := time.Now()
	if err := manager.StartupInBackground(5*time.Second, func(err error) { finished <- err }); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("background startup blocked the caller for %s", elapsed)
	}
	admitted := make(chan error, 1)
	go func() { admitted <- manager.ensureRunning(context.Background(), record.id) }()
	select {
	case err := <-admitted:
		t.Fatalf("builtin admission returned before startup finished: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(plugin.release)
	if err := <-admitted; err != nil {
		t.Fatalf("builtin admission failed after startup: %v", err)
	}
	if err := <-finished; err != nil {
		t.Fatalf("background startup failed: %v", err)
	}
	if record.state != pluginStateRunning {
		t.Fatalf("expected running state, got %s", record.state)
	}
}
