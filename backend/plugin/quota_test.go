package plugin

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mincode/app/backend/license"
)

func useEnabledPluginQuota(t *testing.T, limit int64, available bool) {
	t.Helper()
	previous := enabledPluginQuota
	enabledPluginQuota = func() (license.QuotaPolicy, string, bool) {
		return license.QuotaPolicy{MaxEnabledPlugins: limit}, "free", available
	}
	t.Cleanup(func() { enabledPluginQuota = previous })
}

// quotaTestManager holds development-source plugins (which count toward the cap)
// plus one builtin plugin (which never counts).
func quotaTestManager(enabled ...bool) (*pluginManager, []*pluginRecord) {
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	records := make([]*pluginRecord, 0, len(enabled))
	for index, on := range enabled {
		state := pluginStateDisabled
		if on {
			state = pluginStateStopped
		}
		record := &pluginRecord{id: "quota.plugin" + string(rune('a'+index)), source: pluginSourceDevelopment,
			builtin: &testLifecyclePlugin{}, enabled: on, scope: pluginScopeGlobal, state: state}
		manager.records[record.id] = record
		manager.order = append(manager.order, record.id)
		records = append(records, record)
	}
	builtin := &pluginRecord{id: "quota.builtin", source: pluginSourceBuiltin, builtin: &testLifecyclePlugin{},
		enabled: true, scope: pluginScopeGlobal, state: pluginStateStopped}
	manager.records[builtin.id] = builtin
	manager.order = append(manager.order, builtin.id)
	return manager, records
}

func TestSetEnabledRefusesPluginsBeyondPlanLimit(t *testing.T) {
	useTemporaryPluginData(t)
	useEnabledPluginQuota(t, 2, true)
	manager, records := quotaTestManager(true, true, false)
	err := manager.SetEnabled(context.Background(), records[2].id, true)
	if err == nil || !strings.Contains(err.Error(), "plugin limit reached") || !strings.Contains(err.Error(), "allows 2") {
		t.Fatalf("third plugin enabled past a limit of 2: %v", err)
	}
	if records[2].enabled || records[2].state != pluginStateDisabled {
		t.Fatalf("refused plugin changed state: enabled=%v state=%s", records[2].enabled, records[2].state)
	}
	// Plugins enabled before a downgrade stay usable, and disabling is never limited.
	if err := manager.SetEnabled(context.Background(), records[0].id, true); err != nil {
		t.Fatalf("re-enabling an already enabled plugin was refused: %v", err)
	}
	if err := manager.SetEnabled(context.Background(), records[1].id, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled(context.Background(), records[2].id, true); err != nil {
		t.Fatalf("plugin refused after another was disabled: %v", err)
	}
}

func TestSetEnabledIgnoresUnlimitedOrUnknownQuota(t *testing.T) {
	for name, quota := range map[string]struct {
		limit     int64
		available bool
	}{"zero means unlimited": {0, true}, "no policy never blocks": {1, false}} {
		t.Run(name, func(t *testing.T) {
			useTemporaryPluginData(t)
			useEnabledPluginQuota(t, quota.limit, quota.available)
			manager, records := quotaTestManager(true, true, false)
			if err := manager.SetEnabled(context.Background(), records[2].id, true); err != nil {
				t.Fatalf("enable refused: %v", err)
			}
		})
	}
}

func TestSetEnabledRefusesEveryPluginWhenPlanForbidsThem(t *testing.T) {
	useTemporaryPluginData(t)
	previous := enabledPluginQuota
	enabledPluginQuota = func() (license.QuotaPolicy, string, bool) {
		return license.QuotaPolicy{PluginsDisabled: true}, "free", true
	}
	t.Cleanup(func() { enabledPluginQuota = previous })
	manager, records := quotaTestManager(true, false)
	err := manager.SetEnabled(context.Background(), records[1].id, true)
	if err == nil || !strings.Contains(err.Error(), "not available on the free plan") || records[1].enabled {
		t.Fatalf("plugin enabled on a plan without plugins: %v", err)
	}
	// A plugin enabled before the switch was turned off keeps working and can be toggled off.
	if err := manager.SetEnabled(context.Background(), records[0].id, true); err != nil {
		t.Fatalf("already enabled plugin refused: %v", err)
	}
	if err := manager.SetEnabled(context.Background(), records[0].id, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.admitEnabledPlugin(nil, license.QuotaPolicy{PluginsDisabled: true}, "free"); err == nil {
		t.Fatal("a development session could attach on a plan without plugins")
	}
}

func TestInstallLocalRefusesCodePluginsPastPlanLimit(t *testing.T) {
	useTemporaryPluginData(t)
	useEnabledPluginQuota(t, 2, true)
	manager := newPluginManager()
	_, enabled := quotaTestManager(true, true)
	for _, record := range enabled {
		manager.records[record.id] = record
		manager.order = append(manager.order, record.id)
	}
	_, err := manager.InstallLocal(writeTestManifest(t, nil))
	if err == nil || !strings.Contains(err.Error(), "plugin limit reached") {
		t.Fatalf("code plugin installed past a limit of 2: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(pluginsDirectory(), "example.plugin")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("refused plugin was written to disk: %v", statErr)
	}
	if _, recordErr := manager.record("example.plugin"); recordErr == nil {
		t.Fatal("refused plugin was registered")
	}
	// Data-only plugins are plugins too, so they are refused at the limit as well.
	if _, err := manager.InstallLocal(officialPackageRoot("lang-zh-hant")); err == nil || !strings.Contains(err.Error(), "plugin limit reached") {
		t.Fatalf("declarative plugin installed past a limit of 2: %v", err)
	}
	// A free slot admits the code plugin again.
	enabled[0].enabled = false
	if _, err := manager.InstallLocal(writeTestManifest(t, nil)); err != nil {
		t.Fatalf("code plugin refused below the limit: %v", err)
	}
}

func TestDevelopmentPluginsCountTowardPlanLimit(t *testing.T) {
	manager, _ := quotaTestManager(true, false)
	policy := license.QuotaPolicy{MaxEnabledPlugins: 1}
	if err := manager.admitEnabledPlugin(nil, policy, "free"); err == nil {
		t.Fatal("a development session could attach past the plan limit")
	}
	policy.MaxEnabledPlugins = 2
	if err := manager.admitEnabledPlugin(nil, policy, "free"); err != nil {
		t.Fatalf("development session refused below the limit: %v", err)
	}
}

func TestDeclarativePluginsCountTowardPlanLimit(t *testing.T) {
	useTemporaryPluginData(t)
	useEnabledPluginQuota(t, 2, true)
	manager, records := quotaTestManager(true, false, false)
	// A data-only plugin that is enabled takes a slot like any other plugin.
	// Real declarative plugins are local packages with no runtime.
	for _, record := range records {
		record.source, record.builtin = pluginSourceLocal, nil
		record.manifest = &Manifest{Runtime: ProcessRuntimeManifest{Type: pluginRuntimeDeclarative}}
	}
	if err := manager.SetEnabled(context.Background(), records[1].id, true); err != nil {
		t.Fatalf("second plugin refused below a limit of 2: %v", err)
	}
	err := manager.SetEnabled(context.Background(), records[2].id, true)
	if err == nil || !strings.Contains(err.Error(), "plugin limit reached") || !strings.Contains(err.Error(), "(2 enabled)") {
		t.Fatalf("plugin enabled while two declarative plugins filled a limit of 2: %v", err)
	}
	// Enabling a declarative plugin at the limit is refused too.
	if err := manager.SetEnabled(context.Background(), records[2].id, true); err == nil {
		t.Fatal("declarative plugin enabled past the limit")
	}
}

func TestPlanWithoutPluginsRefusesDeclarativePlugins(t *testing.T) {
	useTemporaryPluginData(t)
	previous := enabledPluginQuota
	enabledPluginQuota = func() (license.QuotaPolicy, string, bool) {
		return license.QuotaPolicy{PluginsDisabled: true}, "free", true
	}
	t.Cleanup(func() { enabledPluginQuota = previous })
	manager := newPluginManager()
	_, err := manager.InstallLocal(officialPackageRoot("lang-zh-hant"))
	if err == nil || !strings.Contains(err.Error(), "not available on the free plan") {
		t.Fatalf("declarative plugin installed on a plan without plugins: %v", err)
	}
	keymaps := writeKeymapPackage(t, "", nil, []any{keymapPreset("keymap-test.a", map[string]any{"key": "f3", "command": "builtin:ai"})}, nil)
	session := pluginDevSession{ID: "dev-1", PluginID: "keymap-test", PackageRoot: keymaps}
	if err := manager.attachDevelopmentSession(session); err == nil {
		t.Fatal("declarative development session attached on a plan without plugins")
	}
}
