package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mincode/app/backend/license"
	"mincode/app/plugins/api"
)

// writeKeymapPackage writes a declarative plugin package. `keymaps` are the raw
// contributes.keymaps entries; `files` are extra package files (relative path → content).
func writeKeymapPackage(t *testing.T, root string, mutate func(map[string]any), keymaps []any, files map[string]string) string {
	t.Helper()
	if root == "" {
		root = t.TempDir()
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := map[string]any{
		"apiVersion": 2, "id": "keymap-test", "name": "Keymap Test", "version": "1.0.0",
		"category": "development", "tags": []string{"keymap"}, "platforms": []string{runtime.GOOS},
		"runtime":     map[string]any{"type": "declarative"},
		"contributes": map[string]any{"keymaps": keymaps},
	}
	if mutate != nil {
		mutate(manifest)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, manifestFilename), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func keymapPreset(id string, rules ...map[string]any) map[string]any {
	if rules == nil {
		rules = []map[string]any{}
	}
	return map[string]any{"id": id, "title": map[string]string{"zh": "测试", "en": "Test"}, "rules": rules}
}

func keymapFile(t *testing.T, preset map[string]any) string {
	t.Helper()
	data, err := json.Marshal(preset)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDeclarativeKeymapManifestInlineAndFile(t *testing.T) {
	file := keymapPreset("keymap-test.file",
		map[string]any{"key": "mod+shift+r", "command": "builtin:quick-open"},
		map[string]any{"key": "ctrl+w h", "command": "workbench:prev-tab", "when": "!editor.focus", "platform": []string{"mac", "linux"}},
		map[string]any{"command": "-builtin:ai"},
		map[string]any{"key": "shift shift", "command": "builtin:search-everywhere", "platform": "win"},
	)
	file["extends"] = "keymap-test.inline"
	file["editorMode"] = "vim"
	inline := keymapPreset("keymap-test.inline", map[string]any{"key": "f3", "command": "editor:editor.action.revealDefinition"})
	inline["extends"] = "mincode"
	inline["doubleModifier"] = true
	root := writeKeymapPackage(t, "", nil, []any{inline, map[string]string{"path": "keymaps/file.json"}},
		map[string]string{"keymaps/file.json": keymapFile(t, file)})

	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.keymapIssuesError(); err != nil {
		t.Fatal(err)
	}
	presets := manifest.KeymapPresets()
	if len(presets) != 2 || presets[0].ID != "keymap-test.inline" || presets[1].ID != "keymap-test.file" {
		t.Fatalf("presets = %+v", presets)
	}
	if presets[1].EditorMode != "vim" || presets[1].Extends != "keymap-test.inline" || len(presets[1].Rules) != 4 {
		t.Fatalf("file preset = %+v", presets[1])
	}
	if got := presets[1].Rules[1].Platform; len(got) != 2 || got[0] != "mac" {
		t.Fatalf("platform = %v", got)
	}
	if manifest.Runtime.Activation != "" || len(manifest.Capabilities) != 0 {
		t.Fatalf("declarative runtime normalized unexpectedly: %+v %v", manifest.Runtime, manifest.Capabilities)
	}
	encoded, _ := json.Marshal(presets[1].Rules[3])
	if !strings.Contains(string(encoded), `"platform":"win"`) {
		t.Fatalf("single platform should round-trip as a string: %s", encoded)
	}
}

func TestDeclarativeManifestRejectsExecutableOrMixedContracts(t *testing.T) {
	valid := []any{keymapPreset("keymap-test.a", map[string]any{"key": "f3", "command": "builtin:ai"})}
	cases := map[string]func(map[string]any){
		"entry":        func(m map[string]any) { m["runtime"] = map[string]any{"type": "declarative", "entry": "bin/x"} },
		"activation":   func(m map[string]any) { m["runtime"] = map[string]any{"type": "declarative", "activation": "startup"} },
		"args":         func(m map[string]any) { m["runtime"] = map[string]any{"type": "declarative", "args": []string{"x"}} },
		"capabilities": func(m map[string]any) { m["capabilities"] = []string{"storage"} },
		"commands": func(m map[string]any) {
			m["contributes"] = map[string]any{"keymaps": valid, "commands": []map[string]string{{"id": "x.run", "title": "Run", "action": "run"}}}
		},
		"no keymaps":   func(m map[string]any) { m["contributes"] = map[string]any{} },
		"api v1":       func(m map[string]any) { m["apiVersion"] = 1 },
		"unknown type": func(m map[string]any) { m["runtime"] = map[string]any{"type": "data"} },
		"too many": func(m map[string]any) {
			entries := make([]any, keymapMaxPresets+1)
			for index := range entries {
				entries[index] = keymapPreset("keymap-test.p"+string(rune('a'+index)), map[string]any{"key": "f3", "command": "builtin:ai"})
			}
			m["contributes"] = map[string]any{"keymaps": entries}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := readManifest(writeKeymapPackage(t, "", mutate, valid, nil)); err == nil {
				t.Fatal("manifest accepted")
			}
		})
	}
	// A process plugin cannot contribute keymaps.
	root := writeTestManifest(t, func(manifest *Manifest) {
		manifest.APIVersion = api.ManifestAPIVersionV2
		manifest.Contributes.Points = map[string]json.RawMessage{"keymaps": json.RawMessage(`[{"path":"keymaps/a.json"}]`)}
	})
	if _, err := readManifest(root); err == nil || !strings.Contains(err.Error(), "declarative") {
		t.Fatalf("process keymaps error = %v", err)
	}
}

func TestDeclarativeKeymapInvalidPresetsAreSkippedAndReported(t *testing.T) {
	good := keymapPreset("keymap-test.good", map[string]any{"key": "f3", "command": "builtin:ai"})
	badRule := func(id string, rule map[string]any) map[string]any { return keymapPreset(id, rule) }
	extendsOther := keymapPreset("keymap-test.other")
	extendsOther["extends"] = "vscode"
	orphan := keymapPreset("keymap-test.orphan")
	orphan["extends"] = "keymap-test.command" // dropped below, so orphan is dropped too
	emacs := keymapPreset("keymap-test.emacs")
	emacs["editorMode"] = "emacs"
	many := make([]map[string]any, keymapMaxRules+1)
	for index := range many {
		many[index] = map[string]any{"key": "f3", "command": "builtin:ai"}
	}
	entries := []any{
		good,
		badRule("keymap-test.command", map[string]any{"key": "f3", "command": "shell:rm"}),
		badRule("keymap-test.key", map[string]any{"key": "mod+hyper+x", "command": "builtin:ai"}),
		badRule("keymap-test.when", map[string]any{"key": "f3", "command": "builtin:ai", "when": "a || b"}),
		keymapPreset("other.prefix"),
		extendsOther,
		orphan,
		emacs,
	}
	root := writeKeymapPackage(t, "", nil, entries, nil)
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if presets := manifest.KeymapPresets(); len(presets) != 1 || presets[0].ID != "keymap-test.good" {
		t.Fatalf("presets = %+v", presets)
	}
	if len(manifest.keymapIssues()) != 7 {
		t.Fatalf("issues = %+v", manifest.keymapIssues())
	}
	if manifest.keymapIssuesError() == nil {
		t.Fatal("issues must be fatal for installation")
	}
	useTemporaryPluginData(t)
	if _, err := newPluginManager().PreviewInstall(root); err == nil || !strings.Contains(err.Error(), "keymap") {
		t.Fatalf("preview error = %v", err)
	}

	// Rule-count and missing-file checks.
	for name, entry := range map[string]any{
		"rules":     keymapPreset("keymap-test.many", many...),
		"missing":   map[string]string{"path": "keymaps/missing.json"},
		"traversal": map[string]string{"path": "../outside.json"},
		"not json":  map[string]string{"path": "keymaps/a.txt"},
		"extra key": map[string]any{"path": "keymaps/a.json", "id": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(writeKeymapPackage(t, "", nil, []any{good, entry}, map[string]string{"keymaps/a.txt": "{}"}))
			if err != nil {
				t.Fatal(err)
			}
			if len(manifest.keymapIssues()) != 1 || len(manifest.KeymapPresets()) != 1 {
				t.Fatalf("issues = %+v", manifest.keymapIssues())
			}
		})
	}
}

func TestDeclarativeKeymapFileRejectsSymlinkAndOversize(t *testing.T) {
	good := keymapPreset("keymap-test.good", map[string]any{"key": "f3", "command": "builtin:ai"})
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(keymapFile(t, keymapPreset("keymap-test.link"))), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "keymaps"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "keymaps", "link.json")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	big := strings.Repeat(" ", keymapMaxPresetBytes+1)
	writeKeymapPackage(t, root, nil, []any{good, map[string]string{"path": "keymaps/link.json"}, map[string]string{"path": "keymaps/big.json"}},
		map[string]string{"keymaps/big.json": big})
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.keymapIssues()) != 2 || len(manifest.KeymapPresets()) != 1 {
		t.Fatalf("issues = %+v", manifest.keymapIssues())
	}
}

func TestValidateKeySequenceMirrorsFrontendGrammar(t *testing.T) {
	for _, key := range []string{"mod+k", "cmd+k cmd+s", "shift shift", "ctrl+alt+f12", "alt+/", "mod+shift+[", "ctrl+w h", "Ctrl+Space"} {
		if err := validateKeySequence(key); err != nil {
			t.Errorf("%q rejected: %v", key, err)
		}
	}
	for _, key := range []string{"", "mod+", "mod++k", "hyper+k", "ctrl+ctrl+k", "a b c", "shift alt", "f25", strings.Repeat("a", 65)} {
		if err := validateKeySequence(key); err == nil {
			t.Errorf("%q accepted", key)
		}
	}
}

func TestDeclarativePluginLifecycleNeverStartsAnything(t *testing.T) {
	useTemporaryPluginData(t)
	// Unlimited plan: this test covers the lifecycle; quota_test.go covers the plugin quota.
	previous := enabledPluginQuota
	enabledPluginQuota = func() (license.QuotaPolicy, string, bool) { return license.QuotaPolicy{}, "", false }
	t.Cleanup(func() { enabledPluginQuota = previous })
	source := writeKeymapPackage(t, "", nil, []any{keymapPreset("keymap-test.a", map[string]any{"key": "f3", "command": "builtin:ai"})}, nil)
	manager := newPluginManager()
	if _, err := manager.PreviewInstall(source); err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.record(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.enabled || record.runtime != nil || record.scope != pluginScopeGlobal {
		t.Fatalf("installed record = enabled %v runtime %v scope %q", record.enabled, record.runtime, record.scope)
	}
	if got := manager.Keymaps(); len(got) != 0 {
		t.Fatalf("disabled plugin contributed keymaps: %+v", got)
	}
	ctx := context.Background()
	if err := manager.SetEnabled(ctx, manifest.ID, true); err != nil {
		t.Fatal(err)
	}
	if record.state != pluginStateStopped || record.runtime != nil {
		t.Fatalf("enabled state = %q runtime %v", record.state, record.runtime)
	}
	contributions := manager.Keymaps()
	if len(contributions) != 1 || contributions[0].PluginID != "keymap-test" || contributions[0].PluginName != "Keymap Test" ||
		len(contributions[0].Presets) != 1 || contributions[0].Presets[0].ID != "keymap-test.a" {
		t.Fatalf("keymaps = %+v", contributions)
	}
	if err := manager.ensureRunning(ctx, manifest.ID); err == nil || !strings.Contains(err.Error(), "declarative") {
		t.Fatalf("start error = %v", err)
	}
	if _, err := manager.Invoke(ctx, manifest.ID, "run", "{}"); err == nil {
		t.Fatal("declarative plugin invoked")
	}
	if err := manager.SetScope(ctx, manifest.ID, pluginScopeWorkspace, []string{"w1"}); err == nil {
		t.Fatal("workspace scope accepted")
	}
	if err := manager.SetScope(ctx, manifest.ID, pluginScopeGlobal, nil); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(ctx, manifest.ID); err != nil {
		t.Fatal(err)
	}
	if record.state != pluginStateStopped {
		t.Fatalf("state after reload = %q", record.state)
	}
	if err := manager.SetEnabled(ctx, manifest.ID, false); err != nil {
		t.Fatal(err)
	}
	if record.state != pluginStateDisabled || len(manager.Keymaps()) != 0 {
		t.Fatalf("disabled state = %q keymaps %+v", record.state, manager.Keymaps())
	}
	if err := manager.Uninstall(ctx, manifest.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.record(manifest.ID); err == nil {
		t.Fatal("record kept after uninstall")
	}
}

func TestInstalledDeclarativePluginLoadsLeniently(t *testing.T) {
	useTemporaryPluginData(t)
	root := filepath.Join(pluginsDirectory(), "keymap-test")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeKeymapPackage(t, root, nil, []any{
		keymapPreset("keymap-test.good", map[string]any{"key": "f3", "command": "builtin:ai"}),
		keymapPreset("keymap-test.bad", map[string]any{"key": "f3", "command": "nope"}),
	}, nil)
	if err := writePluginEnabled("keymap-test", true); err != nil {
		t.Fatal(err)
	}
	if err := writePluginScope("keymap-test", pluginScopeWorkspace, map[string]struct{}{"w1": {}}); err != nil {
		t.Fatal(err)
	}
	manager := newPluginManager()
	manager.scanInstalled()
	record, err := manager.record("keymap-test")
	if err != nil {
		t.Fatal(err)
	}
	if record.scope != pluginScopeGlobal || record.runtime != nil || record.state != pluginStateStopped {
		t.Fatalf("record = scope %q runtime %v state %q", record.scope, record.runtime, record.state)
	}
	contributions := manager.Keymaps()
	if len(contributions) != 1 || len(contributions[0].Presets) != 1 || len(contributions[0].Issues) != 1 {
		t.Fatalf("keymaps = %+v", contributions)
	}
	encoded, err := json.Marshal(contributions)
	if err != nil || !strings.Contains(string(encoded), `"pluginId":"keymap-test"`) {
		t.Fatalf("encoded = %s %v", encoded, err)
	}
}

func TestOfficialKeymapPluginValidatesWithoutIssues(t *testing.T) {
	root := filepath.Join("..", "..", "official-plugins", "keymaps")
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatalf("official keymaps plugin: %v", err)
	}
	if err := manifest.keymapIssuesError(); err != nil {
		t.Fatal(err)
	}
	if !manifest.isDeclarative() || !containsString(manifest.Tags, "keymap") {
		t.Fatal("official keymaps plugin must be declarative and tagged keymap")
	}
	presets := manifest.KeymapPresets()
	ids := make([]string, 0, len(presets))
	for _, preset := range presets {
		ids = append(ids, preset.ID)
	}
	want := []string{"keymaps.vscode", "keymaps.intellij", "keymaps.eclipse", "keymaps.vim"}
	if strings.Join(ids, ",") != strings.Join(want, ",") || len(presets) > keymapMaxPresets {
		t.Fatalf("presets = %v, want %v (at most %d)", ids, want, keymapMaxPresets)
	}
	if presets[1].DoubleModifier == nil || !*presets[1].DoubleModifier || presets[3].EditorMode != keymapEditorModeVim || presets[3].Extends != keymapBasePresetID {
		t.Fatalf("preset options changed: %+v", presets)
	}
	// The four short-lived single-preset packages were merged into this one.
	for _, legacy := range []string{"keymap-vscode", "keymap-intellij", "keymap-eclipse", "keymap-vim"} {
		if _, err := os.Stat(filepath.Join(root, "..", legacy)); !os.IsNotExist(err) {
			t.Fatalf("legacy package %s must be removed: %v", legacy, err)
		}
	}
}

// The marketplace publishes the same declarative ZIP for every OS/arch target;
// the desktop path (download → PreviewDownloadedArchive → EvaluateMarketplaceArchive
// → InstallLocal) must accept it without any runtime entry.
func TestMarketplaceDeclarativeArchiveInstalls(t *testing.T) {
	useTemporaryPluginData(t)
	root := filepath.Join("..", "..", "official-plugins", "keymaps")
	var entries []zipTestEntry
	for _, name := range []string{"plugin.json", "keymaps/vscode.json", "keymaps/intellij.json", "keymaps/eclipse.json", "keymaps/vim.json", "assets/cover.jpg"} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, zipTestEntry{name: name, data: data, mode: 0o644})
	}
	archive := writePluginTestZIP(t, entries)
	preview, err := PreviewDownloadedArchive(archive)
	if err != nil {
		t.Fatalf("preview declarative archive: %v", err)
	}
	if preview.Manifest.ID != "keymaps" || !preview.Manifest.isDeclarative() || preview.Installed {
		t.Fatalf("preview = %+v", preview)
	}
	for _, target := range [][2]string{{"windows", "amd64"}, {"linux", "amd64"}, {"darwin", "amd64"}, {"darwin", "arm64"}} {
		release := MarketplaceRelease{Manifest: preview.Manifest, Status: "published", TargetOS: target[0], TargetArch: target[1], RuntimeProtocol: api.RuntimeProtocolVersion}
		compatibility, err := EvaluateMarketplaceCompatibility(release, nil, "0.1.0", target[0], target[1])
		if err != nil || !compatibility.Compatible {
			t.Fatalf("%s/%s: compatibility = %+v, %v", target[0], target[1], compatibility, err)
		}
	}
	if marketplaceSupportedTarget(runtime.GOOS, runtime.GOARCH) {
		compatibility, err := EvaluateMarketplaceArchive(preview, MarketplaceRelease{
			Status: "published", TargetOS: runtime.GOOS, TargetArch: runtime.GOARCH, RuntimeProtocol: api.RuntimeProtocolVersion,
		}, "0.1.0")
		if err != nil || !compatibility.Compatible || compatibility.VersionChange != "install" {
			t.Fatalf("archive compatibility = %+v, %v", compatibility, err)
		}
	}
	manager := newPluginManager()
	manifest, err := manager.InstallLocal(preview.SourcePath)
	if err != nil {
		t.Fatalf("install declarative archive: %v", err)
	}
	if len(manifest.KeymapPresets()) != 4 {
		t.Fatalf("installed presets = %d, want 4", len(manifest.KeymapPresets()))
	}
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
