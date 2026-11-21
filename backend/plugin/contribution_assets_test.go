package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

// assetTestSpec is a list point with assets under "assets/" (.json/.txt).
func assetTestSpec(maxCount int) api.ContributionPointSpec {
	return api.ContributionPointSpec{
		Key: "assetProbe", Status: api.ContributionStatusEnabled, Kinds: []string{"declarative", "process"},
		MinAPIVersion: 2, SinceHost: "0.2.0", Shape: api.ContributionShapeList, MaxEntries: 8, Scope: "global",
		Assets: &api.ContributionAssets{
			Dirs: []string{"assets"}, Ext: []string{".json", ".txt"}, MaxBytes: 1536 << 10, MaxPathLength: 64,
			MaxCount: maxCount, MaxTotalBytes: 8 << 20, Selectors: []string{"file"},
		},
	}
}

// assetTestPoint reads entry.file as an asset; ValidateAsset trims spaces
// (the canonical form) and rejects content containing "bad". Finalize drops
// entries named "drop".
func assetTestPoint() *contributionPoint {
	return &contributionPoint{
		Key:         "assetProbe",
		IssuePrefix: "invalid assetProbe contribution",
		Decode: func(ctx *pointContext, _ int, _ string, raw json.RawMessage) (any, string, error) {
			var entry struct {
				Name  string   `json:"name"`
				File  string   `json:"file"`
				Extra []string `json:"extra"`
			}
			if err := json.Unmarshal(raw, &entry); err != nil {
				return nil, "", err
			}
			key, err := ctx.readAsset(entry.File, "file")
			if err != nil {
				return nil, entry.Name, err
			}
			for _, extra := range entry.Extra {
				if _, err := ctx.readAsset(extra, "extra"); err != nil {
					return nil, entry.Name, err
				}
			}
			return map[string]string{"name": entry.Name, "file": key}, entry.Name, nil
		},
		Finalize: func(_ *pointContext, entries []contributionEntry, issues []ContributionIssue) ([]contributionEntry, []ContributionIssue) {
			kept := entries[:0]
			for _, entry := range entries {
				if entry.ID == "drop" {
					issues = append(issues, ContributionIssue{Point: "assetProbe", Entry: entry.Index, ID: entry.ID, Message: "dropped"})
					continue
				}
				kept = append(kept, entry)
			}
			return kept, issues
		},
		ValidateAsset: func(_ string, ext string, data []byte) ([]byte, string, error) {
			if bytes.Contains(data, []byte("bad")) {
				return nil, "", errors.New("bad content")
			}
			mime := "text/plain"
			if ext == ".json" {
				mime = "application/json"
			}
			return bytes.TrimSpace(data), mime, nil
		},
	}
}

func writeAssetFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func resolveAssetProbe(t *testing.T, root string, spec api.ContributionPointSpec, raw string) *resolvedContribution {
	t.Helper()
	point := assetTestPoint()
	ctx := &pointContext{root: root, manifest: &Manifest{}, spec: spec, validateAsset: point.ValidateAsset}
	resolved, err := resolveContributionPoint(ctx, point, json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestContributionReadAssetTracksAcceptedEntries(t *testing.T) {
	root := writeAssetFiles(t, map[string]string{
		"assets/a.json":   " {\"a\":1} ",
		"assets/a2.json":  "{\"a\":1}", // same canonical bytes as a.json
		"assets/b.txt":    "b",
		"assets/bad.txt":  "bad",
		"assets/drop.txt": "drop",
		"assets/x.txt":    "x",
		"assets/y.txt":    "y",
		"other/c.txt":     "c",
		"assets/d.png":    "d",
	})
	resolved := resolveAssetProbe(t, root, assetTestSpec(3), `[
		{"name":"a","file":"assets/a.json"},
		{"name":"a2","file":"assets/a2.json"},
		{"name":"b","file":"assets/b.txt","extra":["assets/bad.txt"]},
		{"name":"outside","file":"other/c.txt"},
		{"name":"ext","file":"assets/d.png"},
		{"name":"escape","file":"assets/../../c.txt"},
		{"name":"drop","file":"assets/drop.txt"},
		{"name":"x","file":"assets/x.txt","extra":["assets/y.txt"]}
	]`)
	aKey := contributionAssetKey([]byte(`{"a":1}`))
	dropKey := contributionAssetKey([]byte("drop"))
	if len(resolved.Entries) != 2 {
		t.Fatalf("entries = %+v issues = %+v", resolved.Entries, resolved.Issues)
	}
	if got := resolved.Entries[0].(map[string]string)["file"]; got != aKey || len(got) != 64 {
		t.Fatalf("asset key = %q", got)
	}
	// a (deduplicated with a2) and drop count toward maxCount 3; b's assets were
	// discarded with its entry; x+y would exceed the cap.
	if len(resolved.Assets) != 1 || resolved.Assets[aKey].Path != "assets/a.json" || resolved.Assets[aKey].Mime != "application/json" {
		t.Fatalf("assets = %+v", resolved.Assets)
	}
	if _, kept := resolved.Assets[dropKey]; kept {
		t.Fatal("asset of an entry dropped by Finalize stayed servable")
	}
	messages := []string{}
	for _, issue := range resolved.Issues {
		messages = append(messages, issue.ID+": "+issue.Message)
	}
	joined := strings.Join(messages, "\n")
	for _, want := range []string{"b: extra file \"assets/bad.txt\": bad content", "outside: file path must be", "ext: file path must be", "escape: file path", "drop: dropped", "x: contributes.assetProbe must reference at most 3 assets"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues missing %q:\n%s", want, joined)
		}
	}
	if encoded, _ := json.Marshal(resolved.Entries); strings.Contains(string(encoded), "assets/") {
		t.Fatalf("snapshot entries leak package paths: %s", encoded)
	}
}

func TestContributionReadAssetRejectsSymlinkAndTotal(t *testing.T) {
	root := writeAssetFiles(t, map[string]string{"assets/a.txt": "aaaa", "assets/b.txt": "bbbb"})
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "assets", "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	spec := assetTestSpec(8)
	spec.Assets.MaxTotalBytes = 6
	resolved := resolveAssetProbe(t, root, spec, `[{"name":"link","file":"assets/link.txt"},{"name":"a","file":"assets/a.txt"},{"name":"b","file":"assets/b.txt"}]`)
	if len(resolved.Entries) != 1 || len(resolved.Issues) != 2 {
		t.Fatalf("entries = %+v issues = %+v", resolved.Entries, resolved.Issues)
	}
	if !strings.Contains(resolved.Issues[0].Message, "symlink") || !strings.Contains(resolved.Issues[1].Message, "at most 6 bytes") {
		t.Fatalf("issues = %+v", resolved.Issues)
	}
}

func TestContributionPointRequiresCapabilities(t *testing.T) {
	spec := api.ContributionPointSpec{Key: "probe", RequiresCapabilities: []string{"shell"}}
	manifest := &Manifest{Capabilities: []api.Capability{api.CapabilityUI}}
	if err := requireContributionCapabilities(manifest, spec); err == nil || err.Error() != "contributes.probe requires capability shell" {
		t.Fatalf("err = %v", err)
	}
	manifest.Capabilities = append(manifest.Capabilities, api.CapabilityShell)
	if err := requireContributionCapabilities(manifest, spec); err != nil {
		t.Fatal(err)
	}
}

func TestContributionAssetsRegistrationNeedsValidateAsset(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a point with contract assets registered without ValidateAsset")
		}
	}()
	// localizations declares assets in the contract.
	registerContributionPoint(&contributionPoint{Key: "localizations", IssuePrefix: "x", Decode: func(*pointContext, int, string, json.RawMessage) (any, string, error) {
		return nil, "", nil
	}})
}

// installAssetProbeRecord registers a declarative record whose assetProbe
// point is resolved from root, and routes the asset command to the probe.
func installAssetProbeRecord(t *testing.T, manager *pluginManager, root, raw string) *pluginRecord {
	t.Helper()
	spec := assetTestSpec(8)
	point := assetTestPoint()
	previous := assetContributionPoint
	assetContributionPoint = func(key string) (*contributionPoint, api.ContributionPointSpec, bool) {
		if key == spec.Key {
			return point, spec, true
		}
		return previous(key)
	}
	t.Cleanup(func() { assetContributionPoint = previous })
	resolved := resolveAssetProbe(t, root, spec, raw)
	manifest := &Manifest{ID: "asset.probe", Name: "Asset probe", Version: "1.0.0", APIVersion: 2}
	manifest.Runtime.Type = pluginRuntimeDeclarative
	manifest.contributions = map[string]*resolvedContribution{spec.Key: resolved}
	record := &pluginRecord{id: manifest.ID, source: pluginSourceLocal, root: root, manifest: manifest, enabled: true, state: pluginStateStopped, scope: pluginScopeGlobal}
	manager.mu.Lock()
	manager.records[record.id] = record
	manager.order = append(manager.order, record.id)
	manager.mu.Unlock()
	t.Cleanup(func() { contributionAssets.purgePlugin(record.id) })
	return record
}

func TestContributionAssetsCommand(t *testing.T) {
	useTemporaryPluginData(t)
	manager := newPluginManager()
	root := writeAssetFiles(t, map[string]string{"assets/a.json": " {\"a\":1} ", "assets/b.txt": "b"})
	record := installAssetProbeRecord(t, manager, root, `[{"name":"a","file":"assets/a.json"},{"name":"b","file":"assets/b.txt"}]`)
	aKey := contributionAssetKey([]byte(`{"a":1}`))
	bKey := contributionAssetKey([]byte("b"))
	unknown := strings.Repeat("0", 64)

	response, err := manager.ContributionAssets(contributionAssetsRequest{PluginID: record.id, Point: "assetProbe", Keys: []string{aKey, unknown, "../x", aKey, bKey}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Assets) != 2 || response.Assets[0].Key != aKey || response.Assets[0].Mime != "application/json" || response.Assets[1].Key != bKey {
		t.Fatalf("response = %+v", response)
	}
	if data, _ := base64.StdEncoding.DecodeString(response.Assets[0].Data); string(data) != `{"a":1}` {
		t.Fatalf("data = %q", data)
	}
	if strings.Join(response.Missing, ",") != unknown+",../x" || len(response.Stale) != 0 || len(response.Deferred) != 0 {
		t.Fatalf("missing/stale/deferred = %+v", response)
	}
	if _, cached := contributionAssets.get(contributionAssetCacheKey(record.id, "assetProbe", aKey)); !cached {
		t.Fatal("served asset not cached")
	}

	// A changed file is revalidated and reported stale, never served.
	if err := os.WriteFile(filepath.Join(root, "assets", "b.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	response, _ = manager.ContributionAssets(contributionAssetsRequest{PluginID: record.id, Point: "assetProbe", Keys: []string{bKey}})
	if len(response.Assets) != 0 || strings.Join(response.Stale, ",") != bKey {
		t.Fatalf("stale response = %+v", response)
	}

	if _, err := manager.ContributionAssets(contributionAssetsRequest{PluginID: record.id, Point: "assetProbe", Keys: make([]string, 65)}); err == nil {
		t.Fatal("65 keys accepted")
	}
	if _, err := manager.ContributionAssets(contributionAssetsRequest{PluginID: record.id, Point: "keymaps", Keys: []string{aKey}}); err == nil {
		t.Fatal("point without assets served")
	}

	// Only plugins listed by the snapshot are served.
	record.mu.Lock()
	record.enabled = false
	record.mu.Unlock()
	response, _ = manager.ContributionAssets(contributionAssetsRequest{PluginID: record.id, Point: "assetProbe", Keys: []string{aKey}})
	if len(response.Assets) != 0 || strings.Join(response.Missing, ",") != aKey {
		t.Fatalf("disabled plugin served: %+v", response)
	}
	response, _ = manager.ContributionAssets(contributionAssetsRequest{PluginID: "not.installed", Point: "assetProbe", Keys: []string{aKey}})
	if len(response.Missing) != 1 {
		t.Fatalf("unknown plugin = %+v", response)
	}

	// The removal hook purges the plugin's cache entries.
	manager.runRecordRemovalHooks(record.id, removalReasonClearData)
	if _, cached := contributionAssets.get(contributionAssetCacheKey(record.id, "assetProbe", aKey)); cached {
		t.Fatal("cache not purged by the removal hook")
	}
}

func TestContributionAssetsResponseBudget(t *testing.T) {
	useTemporaryPluginData(t)
	manager := newPluginManager()
	files := map[string]string{}
	for _, name := range []string{"a", "b", "c"} {
		files["assets/"+name+".txt"] = strings.Repeat(name, 1<<20)
	}
	root := writeAssetFiles(t, files)
	record := installAssetProbeRecord(t, manager, root, `[{"name":"a","file":"assets/a.txt"},{"name":"b","file":"assets/b.txt"},{"name":"c","file":"assets/c.txt"}]`)
	keys := []string{}
	for _, name := range []string{"a", "b", "c"} {
		keys = append(keys, contributionAssetKey([]byte(files["assets/"+name+".txt"])))
	}
	response, err := manager.ContributionAssets(contributionAssetsRequest{PluginID: record.id, Point: "assetProbe", Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Assets) != 1 || response.Assets[0].Key != keys[0] || strings.Join(response.Deferred, ",") != keys[1]+","+keys[2] {
		t.Fatalf("assets=%d deferred=%v", len(response.Assets), response.Deferred)
	}
}

func TestContributionAssetCacheEvictsByBytes(t *testing.T) {
	cache := newContributionAssetCache(10)
	cache.put(contributionAssetCacheEntry{cacheKey: "p\x00x\x00a", canonical: []byte("123456")})
	cache.put(contributionAssetCacheEntry{cacheKey: "p\x00x\x00b", canonical: []byte("123456")})
	if _, ok := cache.get("p\x00x\x00a"); ok || cache.bytes != 6 {
		t.Fatalf("oldest entry not evicted: bytes=%d", cache.bytes)
	}
	cache.put(contributionAssetCacheEntry{cacheKey: "q\x00x\x00c", canonical: []byte("12")})
	cache.purgePlugin("p")
	if _, ok := cache.get("p\x00x\x00b"); ok || cache.bytes != 2 {
		t.Fatalf("purge failed: bytes=%d", cache.bytes)
	}
}

func TestRecordRemovalHooksRunOnUninstallAndClearData(t *testing.T) {
	useTemporaryPluginData(t)
	var calls []string
	recordRemovalHooksMu.Lock()
	previous := len(recordRemovalHooks)
	recordRemovalHooksMu.Unlock()
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, reason removalReason) {
		calls = append(calls, pluginID+":"+string(reason))
	})
	t.Cleanup(func() {
		recordRemovalHooksMu.Lock()
		recordRemovalHooks = recordRemovalHooks[:previous]
		recordRemovalHooksMu.Unlock()
	})
	source := writeKeymapPackage(t, "", nil, []any{keymapPreset("keymap-test.a", map[string]any{"key": "f3", "command": "builtin:ai"})}, nil)
	manager := newPluginManager()
	manifest, err := manager.InstallLocal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ClearPluginData(context.Background(), manifest.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), manifest.ID, false); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != manifest.ID+":clearData,"+manifest.ID+":uninstall" {
		t.Fatalf("calls = %v", calls)
	}
}
