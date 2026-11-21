package plugin

import (
	"strings"
	"sync"
	"testing"

	"mincode/app/backend/core"
	"mincode/app/backend/license"
)

func captureStateEvents(t *testing.T) func() []pluginStateEvent {
	t.Helper()
	var mu sync.Mutex
	var events []pluginStateEvent
	core.SetEmitter(func(name string, data any) {
		if event, ok := data.(pluginStateEvent); ok && name == pluginStateChangedEvent {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	return func() []pluginStateEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]pluginStateEvent(nil), events...)
	}
}

func keymapPresetIDs(contributions []pluginHostKeymapContribution) []string {
	ids := []string{}
	for _, contribution := range contributions {
		for _, preset := range contribution.Presets {
			ids = append(ids, preset.ID)
		}
	}
	return ids
}

func TestDeclarativeDevelopmentSessionReloadsKeymaps(t *testing.T) {
	useTemporaryPluginData(t)
	// Unlimited plan: this test covers keymap reloads; quota_test.go covers the plugin quota.
	previous := enabledPluginQuota
	enabledPluginQuota = func() (license.QuotaPolicy, string, bool) { return license.QuotaPolicy{}, "", false }
	t.Cleanup(func() { enabledPluginQuota = previous })
	events := captureStateEvents(t)

	first := writeKeymapPackage(t, "", nil, []any{map[string]string{"path": "keymaps/a.json"}},
		map[string]string{"keymaps/a.json": keymapFile(t, keymapPreset("keymap-test.a", map[string]any{"key": "f3", "command": "builtin:ai"}))})
	manager := newPluginManager()
	session := pluginDevSession{ID: "dev-1", PluginID: "keymap-test", PackageRoot: first}
	if err := manager.attachDevelopmentSession(session); err != nil {
		t.Fatalf("attach declarative session: %v", err)
	}
	record, err := manager.record("keymap-test")
	if err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	hasRuntime := record.runtime != nil
	record.mu.Unlock()
	if hasRuntime {
		t.Fatal("declarative development session created a runtime")
	}
	if ids := keymapPresetIDs(manager.Keymaps()); strings.Join(ids, ",") != "keymap-test.a" {
		t.Fatalf("keymaps after attach = %v", ids)
	}

	// New generation: one preset edited, one added, one invalid (skipped, reported).
	second := writeKeymapPackage(t, "", nil, []any{
		map[string]string{"path": "keymaps/a.json"},
		map[string]string{"path": "keymaps/b.json"},
		map[string]string{"path": "keymaps/broken.json"},
	}, map[string]string{
		"keymaps/a.json":      keymapFile(t, keymapPreset("keymap-test.a", map[string]any{"key": "f4", "command": "builtin:ai"})),
		"keymaps/b.json":      keymapFile(t, keymapPreset("keymap-test.b")),
		"keymaps/broken.json": keymapFile(t, keymapPreset("other.c")),
	})
	before := len(events())
	if err := manager.switchDevelopmentGeneration(session, second, 2); err != nil {
		t.Fatalf("switch generation: %v", err)
	}
	if len(events()) <= before {
		t.Fatal("generation switch did not emit plugin:state-changed")
	}
	contributions := manager.Keymaps()
	if ids := keymapPresetIDs(contributions); strings.Join(ids, ",") != "keymap-test.a,keymap-test.b" {
		t.Fatalf("keymaps after reload = %v", ids)
	}
	if contributions[0].Presets[0].Rules[0].Key != "f4" {
		t.Fatalf("edited preset not reloaded: %+v", contributions[0].Presets[0])
	}
	if len(contributions[0].Issues) != 1 {
		t.Fatalf("issues = %+v, want one skipped preset", contributions[0].Issues)
	}
	logs, err := manager.Logs("keymap-test")
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, entry := range logs {
		if entry.Level == "warn" && strings.Contains(entry.Message, "other.c") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("skipped preset missing from logs: %+v", logs)
	}

	summary := summarizePluginDevManifest(readManifestForTest(t, second))
	if summary.Activation != "" || summary.KeymapCount != 2 || len(summary.KeymapIssues) != 1 {
		t.Fatalf("unexpected declarative summary: %+v", summary)
	}

	// Switching runtime.type inside one session is refused.
	process := writeKeymapPackage(t, "", func(manifest map[string]any) {
		manifest["runtime"] = map[string]any{"type": "process", "entry": "bin/plugin"}
		delete(manifest["contributes"].(map[string]any), "keymaps")
	}, nil, map[string]string{"bin/plugin": "#!/bin/sh\n"})
	if err := manager.switchDevelopmentGeneration(session, process, 3); err == nil {
		t.Fatal("runtime.type change accepted")
	}

	before = len(events())
	manager.detachDevelopmentSession(session)
	if len(events()) <= before {
		t.Fatal("detach did not emit plugin:state-changed")
	}
	if contributions := manager.Keymaps(); len(contributions) != 0 {
		t.Fatalf("keymaps after detach = %+v", contributions)
	}
}

func TestDeclarativeDevelopmentSessionYieldsToInstalledPlugin(t *testing.T) {
	useTemporaryPluginData(t)
	source := writeKeymapPackage(t, "", nil, []any{keymapPreset("keymap-test.a")}, nil)
	manager := newPluginManager()
	if _, err := manager.InstallLocal(source); err != nil {
		t.Fatal(err)
	}
	session := pluginDevSession{ID: "dev-1", PluginID: "keymap-test", PackageRoot: source}
	if err := manager.attachDevelopmentSession(session); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("attach over installed plugin = %v, want already registered", err)
	}
}

func readManifestForTest(t *testing.T, root string) Manifest {
	t.Helper()
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}
