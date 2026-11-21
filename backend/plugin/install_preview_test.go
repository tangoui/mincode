package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func TestCatalogMetadataContract(t *testing.T) {
	for _, version := range []int{api.ManifestAPIVersionV2} {
		for _, category := range api.Categories() {
			root := writeTestManifest(t, func(m *Manifest) {
				m.APIVersion = version
				m.Category = category
				m.Tags = []string{" Go ", "Go", "数据库", " "}
				m.Homepage = " https://example.com/docs "
			})
			m, err := readManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(m.Tags, []string{"Go", "数据库"}) || m.Homepage != "https://example.com/docs" || m.Category != category {
				t.Fatalf("metadata: %+v", m)
			}
			meta := recordMeta(&pluginRecord{manifest: &m})
			if meta.Category != category || !reflect.DeepEqual(meta.Tags, m.Tags) || meta.Homepage != m.Homepage {
				t.Fatal("list lost metadata")
			}
		}
	}
	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"missing category", func(m *Manifest) { m.Category = "" }},
		{"unknown category", func(m *Manifest) { m.Category = "Database" }},
		{"too many tags", func(m *Manifest) { m.Tags = make([]string, 9) }},
		{"long tag", func(m *Manifest) { m.Tags = []string{strings.Repeat("界", 25)} }},
		{"credentials", func(m *Manifest) { m.Homepage = "https://user:secret@example.com" }},
		{"empty credentials", func(m *Manifest) { m.Homepage = "https://@example.com" }},
		{"relative homepage", func(m *Manifest) { m.Homepage = "/docs" }},
		{"script homepage", func(m *Manifest) { m.Homepage = "javascript:alert(1)" }},
		{"missing host", func(m *Manifest) { m.Homepage = "https:///docs" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readManifest(writeTestManifest(t, tc.mutate)); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	if _, err := readManifest(writeTestManifest(t, func(m *Manifest) { m.Tags = []string{strings.Repeat("界", 24)}; m.Homepage = "http://localhost:8080" })); err != nil {
		t.Fatal(err)
	}
}

func TestInstallPreviewReadOnlyAndRevalidation(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	root := writeTestManifestV2(t, func(m *Manifest) { m.Author = "Author" })
	preview, err := manager.PreviewInstall(root)
	if err != nil {
		t.Fatal(err)
	}
	if preview.FileCount != 3 || preview.TotalBytes == 0 || preview.Installed || preview.Manifest.Author != "Author" {
		t.Fatalf("preview: %+v", preview)
	}
	if len(manager.records) != 0 {
		t.Fatal("preview registered plugin")
	}
	if _, err := os.Stat(pluginsDirectory()); !os.IsNotExist(err) {
		t.Fatalf("preview created installation directory: %v", err)
	}
	if _, found, err := readPluginEnabled(preview.Manifest.ID); err != nil || found {
		t.Fatalf("preview wrote enabled state: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "ui", "index.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InstallLocal(root); err == nil {
		t.Fatal("install trusted stale preview")
	}
}

func TestInstallDirectorySkipsDevelopmentDirectoriesButRejectsOtherSymlinks(t *testing.T) {
	root := writeTestManifest(t, nil)
	for _, directory := range []string{"node_modules/.bin", ".git/objects", ".mincode/dev", "coverage/tmp"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "dependency.js"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "plugin.mjs"), filepath.Join(root, "node_modules", ".bin", "tool")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	preview, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if preview.FileCount != 2 {
		t.Fatalf("ignored development files were counted: %+v", preview)
	}
	staged := filepath.Join(t.TempDir(), "staged")
	if err := copyPluginTree(root, staged); err != nil {
		t.Fatal(err)
	}
	for _, ignored := range []string{"node_modules", ".git", ".mincode", "coverage"} {
		if _, err := os.Lstat(filepath.Join(staged, ignored)); !os.IsNotExist(err) {
			t.Fatalf("ignored directory %q was copied: %v", ignored, err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "plugin.mjs"), filepath.Join(root, "runtime-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := inspectInstallDirectory(root); err == nil || !strings.Contains(err.Error(), "contains symlink") {
		t.Fatalf("non-ignored symlink accepted: %v", err)
	}
	if err := copyPluginTree(root, filepath.Join(t.TempDir(), "rejected")); err == nil || !strings.Contains(err.Error(), "contains symlink") {
		t.Fatalf("copy accepted non-ignored symlink: %v", err)
	}
}

func TestInstallDirectoryUsesBuiltUIInsteadOfDuplicateSourceAssets(t *testing.T) {
	root := writeTestManifestV2(t, nil)
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"private":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "public", "editor"), 0o700); err != nil {
		t.Fatal(err)
	}
	duplicate, err := os.Create(filepath.Join(root, "public", "editor", "duplicate.js"))
	if err != nil {
		t.Fatal(err)
	}
	if err := duplicate.Truncate(pluginInstallMaxBytes + 1); err != nil {
		duplicate.Close()
		t.Fatal(err)
	}
	if err := duplicate.Close(); err != nil {
		t.Fatal(err)
	}
	preview, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if preview.TotalBytes >= pluginInstallMaxBytes {
		t.Fatalf("source assets were counted: %+v", preview)
	}
	staged := filepath.Join(t.TempDir(), "staged")
	if err := copyPluginTree(root, staged); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(staged, "public")); !os.IsNotExist(err) {
		t.Fatalf("source public directory was copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staged, "ui", "index.html")); err != nil {
		t.Fatalf("built UI was not copied: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "ui", "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ui", "docs", "runtime.txt"), []byte("required"), 0o600); err != nil {
		t.Fatal(err)
	}
	stagedNested := filepath.Join(t.TempDir(), "staged-nested")
	if err := copyPluginTree(root, stagedNested); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stagedNested, "ui", "docs", "runtime.txt")); err != nil {
		t.Fatalf("nested runtime directory was incorrectly filtered: %v", err)
	}
}

func TestInstallPreviewRejectsUnsafeAndIncompletePackages(t *testing.T) {
	for _, kind := range []string{"legacy", "symlink", "missing-runtime", "oversized", "empty"} {
		t.Run(kind, func(t *testing.T) {
			root := writeTestManifest(t, nil)
			switch kind {
			case "legacy":
				root = legacyManifestFixture(t, "1.0.0")
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
					t.Skip(err)
				}
			case "missing-runtime":
				m, err := readManifest(root)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(root, m.Runtime.Entry)); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				f, err := os.Create(filepath.Join(root, "large"))
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate(pluginInstallMaxBytes + 1)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "empty":
				root = ""
			}
			if _, err := inspectInstallSource(root); err == nil {
				t.Fatal("unsafe/incomplete package accepted")
			}
		})
	}
}

func TestInvalidInstalledPackagesRemainVisibleAndPreserveData(t *testing.T) {
	for _, sync := range []bool{false, true} {
		t.Run(map[bool]string{false: "scan", true: "sync"}[sync], func(t *testing.T) {
			useTemporaryPluginData(t)
			root := filepath.Join(pluginsDirectory(), "example.plugin")
			if err := copyPluginTree(legacyManifestFixture(t, "1.0.0"), root); err != nil {
				t.Fatal(err)
			}
			if err := pluginKVSetRaw("example.plugin", "document", json.RawMessage(`"keep"`)); err != nil {
				t.Fatal(err)
			}
			if err := writePluginEnabled("example.plugin", true); err != nil {
				t.Fatal(err)
			}
			manager := &pluginManager{records: map[string]*pluginRecord{}}
			if sync {
				if err := manager.syncInstalled(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				manager.scanInstalled()
			}
			record, err := manager.record("example.plugin")
			if err != nil {
				t.Fatal(err)
			}
			meta := recordMeta(record)
			if meta.State != pluginStateError || meta.LastError == "" || meta.Enabled || record.runtime != nil {
				t.Fatalf("bad record: %+v", meta)
			}
			if err := manager.SetEnabled(context.Background(), record.id, true); err == nil {
				t.Fatal("enabled invalid package")
			}
			if err := manager.startRecord(context.Background(), record); err == nil {
				t.Fatal("started invalid package")
			}
			if _, err := os.Stat(filepath.Join(root, legacyManifestFilename)); err != nil {
				t.Fatal(err)
			}
			value, found, err := pluginKVGetRaw(record.id, "document")
			if err != nil || !found || string(value) != `"keep"` {
				t.Fatal("lost plugin data")
			}
			enabled, found, err := readPluginEnabled(record.id)
			if err != nil || !found || !enabled {
				t.Fatal("scan rewrote persisted state")
			}
		})
	}
}

func TestInstallPreviewSessionSources(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	root := writeSessionSourcesManifest(t, `[{"id":"team","title":"Team Agent","titleLocalized":{"zh":"团队 Agent"},"action":"read"}]`)
	preview, err := manager.PreviewInstall(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.DataProviders) != 1 {
		t.Fatalf("data providers = %+v", preview.DataProviders)
	}
	line := preview.DataProviders[0]
	if line.Point != "sessionSources" || line.ID != "team" || line.Title != "Team Agent" {
		t.Fatalf("line = %+v", line)
	}
	if got := line.LocalizedTitle([]string{"zh-CN", "zh"}); got != "团队 Agent" {
		t.Fatalf("zh title = %q", got)
	}
	if got := line.LocalizedTitle([]string{"en"}); got != "Team Agent" {
		t.Fatalf("en title = %q", got)
	}
	encoded, err := json.Marshal(line)
	if err != nil || string(encoded) != `{"point":"sessionSources","id":"team","title":"Team Agent","titleLocalized":{"zh":"团队 Agent"}}` {
		t.Fatalf("encoded = %s %v", encoded, err)
	}
	plain, err := manager.PreviewInstall(writeTestManifestV2(t, nil))
	if err != nil || plain.DataProviders != nil {
		t.Fatalf("plain preview data providers = %+v %v", plain.DataProviders, err)
	}
}

func TestInstallPreviewScmCommitMessage(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	root := writeScmCommitMessageManifest(t, true, `[{"id":"conventional","title":"Ticket format","titleLocalized":{"zh":"工单格式"},"action":"draft"}]`)
	preview, err := manager.PreviewInstall(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.DataProviders) != 1 {
		t.Fatalf("data providers = %+v", preview.DataProviders)
	}
	encoded, err := json.Marshal(preview.DataProviders[0])
	if err != nil || string(encoded) != `{"point":"scmCommitMessage","id":"conventional","title":"Ticket format","titleLocalized":{"zh":"工单格式"}}` {
		t.Fatalf("encoded = %s %v", encoded, err)
	}
	if got := preview.DataProviders[0].LocalizedTitle([]string{"zh"}); got != "工单格式" {
		t.Fatalf("zh title = %q", got)
	}
}

func TestInstallPreviewFileDecorations(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	root := writeFileDecorationsManifest(t, true, `[{"id":"lint","title":"Lint status","titleLocalized":{"zh":"Lint 状态"},"action":"decorate"}]`)
	preview, err := manager.PreviewInstall(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.DataProviders) != 1 {
		t.Fatalf("data providers = %+v", preview.DataProviders)
	}
	encoded, err := json.Marshal(preview.DataProviders[0])
	if err != nil || string(encoded) != `{"point":"fileDecorations","id":"lint","title":"Lint status","titleLocalized":{"zh":"Lint 状态"}}` {
		t.Fatalf("encoded = %s %v", encoded, err)
	}
}
