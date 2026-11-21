package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func upgradeFixture(t *testing.T) (*pluginManager, string, string) {
	t.Helper()
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	manifest, err := manager.InstallLocal(writeTestManifest(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	source := writeTestManifest(t, func(m *Manifest) { m.Version = "1.1.0" })
	return manager, manifest.ID, source
}

func localPackageZIP(t *testing.T, source string) string {
	t.Helper()
	var entries []zipTestEntry
	err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, zipTestEntry{name: filepath.ToSlash(relative), data: data, mode: info.Mode()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return writePluginTestZIP(t, entries)
}

func TestPluginPackageExtensionsUpgradeAndDowngrade(t *testing.T) {
	for _, extension := range []string{".min", ".MIN", ".zip", ".ZIP"} {
		t.Run(extension, func(t *testing.T) {
			manager, id, source := upgradeFixture(t)
			archive := renamePluginTestPackage(t, localPackageZIP(t, source), extension)
			if err := requirePluginZIP(archive); err != nil {
				t.Fatal(err)
			}
			if err := pluginKVSetRaw(id, "saved", json.RawMessage(`{"kept":true}`)); err != nil {
				t.Fatal(err)
			}
			if manifest, err := manager.UpgradeLocal(context.Background(), id, archive); err != nil || manifest.Version != "1.1.0" {
				t.Fatalf("upgrade: %+v %v", manifest, err)
			}
			older := writeTestManifest(t, func(m *Manifest) { m.Version = "1.0.0" })
			archive = renamePluginTestPackage(t, localPackageZIP(t, older), extension)
			if _, err := manager.DowngradeLocal(context.Background(), id, archive, false); err == nil {
				t.Fatal("unconfirmed downgrade accepted")
			}
			if manifest, err := manager.DowngradeLocal(context.Background(), id, archive, true); err != nil || manifest.Version != "1.0.0" {
				t.Fatalf("downgrade: %+v %v", manifest, err)
			}
			saved, found, err := pluginKVGetRaw(id, "saved")
			if err != nil || !found || string(saved) != `{"kept":true}` {
				t.Fatalf("package replacement lost data: %s %v", saved, err)
			}
			record, err := manager.record(id)
			if err != nil || record.enabled || record.state != pluginStateDisabled {
				t.Fatalf("package replacement enabled plugin: %+v %v", record, err)
			}
		})
	}
}

func TestPluginVersionNewer(t *testing.T) {
	for _, tt := range []struct {
		candidate, installed string
		want                 bool
	}{
		{"1.10.0", "1.9.0", true}, {"1.0.0", "1.0.0", false}, {"0.9.0", "1.0.0", false},
		{"1.0.0", "1.0.0-rc.1", true}, {"1.0.0-rc.2", "1.0.0-rc.1", true},
		{"1.0.0+new", "1.0.0+old", false}, {"bad", "1.0.0", false},
	} {
		if got := pluginVersionNewer(tt.candidate, tt.installed); got != tt.want {
			t.Fatalf("%s > %s = %v", tt.candidate, tt.installed, got)
		}
	}
	for _, tt := range []struct {
		candidate, installed string
		want                 bool
	}{
		{"0.9.0", "1.0.0", true}, {"1.0.0-rc.1", "1.0.0", true},
		{"1.0.0", "1.0.0", false}, {"1.0.0+old", "1.0.0+new", false},
		{"1.1.0", "1.0.0", false}, {"bad", "1.0.0", false},
		{"v0.9.0", "1.0.0", false}, {" 0.9.0", "1.0.0", false},
	} {
		if got := pluginVersionOlder(tt.candidate, tt.installed); got != tt.want {
			t.Fatalf("%s < %s = %v", tt.candidate, tt.installed, got)
		}
	}
}

func downgradeFixture(t *testing.T) (*pluginManager, string, string) {
	t.Helper()
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	installed := writeTestManifest(t, func(m *Manifest) { m.Version = "1.1.0" })
	manifest, err := manager.InstallLocal(installed)
	if err != nil {
		t.Fatal(err)
	}
	return manager, manifest.ID, localPackageZIP(t, writeTestManifest(t, func(m *Manifest) { m.Version = "1.0.0" }))
}

func TestDowngradeHandlerRequiresConfirmationAndLocalZIP(t *testing.T) {
	_, id, source := downgradeFixture(t)
	// Windows 路径含反斜杠，必须经 JSON 编码后再拼接。
	quoted, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{
		`{"id":"` + id + `","sourcePath":` + string(quoted) + `}`,
		`{"id":"` + id + `","sourcePath":` + string(quoted) + `,"allowDowngrade":false}`,
	} {
		if _, err := pluginHostDowngradeLocalHandler("", args); err == nil || !strings.Contains(err.Error(), "explicit confirmation") {
			t.Fatalf("accepted unconfirmed downgrade %s: %v", args, err)
		}
	}
	if _, err := pluginHostDowngradeLocalHandler("", `{"id":"`+id+`","sourcePath":"`+t.TempDir()+`","allowDowngrade":true}`); err == nil {
		t.Fatal("handler accepted a directory instead of a ZIP")
	}
}

func TestDowngradeLocalRequiresExplicitConfirmation(t *testing.T) {
	manager, id, source := downgradeFixture(t)
	if _, err := manager.DowngradeLocal(context.Background(), id, source, false); err == nil || !strings.Contains(err.Error(), "explicit confirmation") {
		t.Fatalf("expected explicit confirmation error: %v", err)
	}
	record, _ := manager.record(id)
	installed, err := readManifest(record.root)
	if err != nil || installed.Version != "1.1.0" {
		t.Fatalf("unconfirmed downgrade changed package: %v", err)
	}
}

func TestDowngradeLocalPreservesDataDisablesAndInvalidatesApproval(t *testing.T) {
	manager, id, source := downgradeFixture(t)
	if err := pluginKVSetRaw(id, "config", json.RawMessage(`{"saved":true}`)); err != nil {
		t.Fatal(err)
	}
	record, _ := manager.record(id)
	record.enabled = true
	record.state = pluginStateStopped
	if err := writePluginEnabled(id, true); err != nil {
		t.Fatal(err)
	}
	epoch := record.approvalEpoch
	runtime := record.runtime
	manifest, err := manager.DowngradeLocal(context.Background(), id, source, true)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "1.0.0" || record.manifest.Version != "1.0.0" || record.enabled || record.state != pluginStateDisabled {
		t.Fatalf("unexpected downgrade state: %+v", record)
	}
	if record.approvalEpoch <= epoch || record.runtime == runtime {
		t.Fatal("downgrade retained approval identity")
	}
	saved, found, err := pluginKVGetRaw(id, "config")
	if err != nil || !found || string(saved) != `{"saved":true}` {
		t.Fatalf("KV lost during downgrade: %s %v", saved, err)
	}
	installed, err := readManifest(record.root)
	if err != nil || installed.Version != "1.0.0" {
		t.Fatalf("downgraded package missing: %v", err)
	}
	enabled, found, err := readPluginEnabled(id)
	if err != nil || !found || enabled {
		t.Fatal("downgrade did not persist disabled state")
	}
}

func TestDowngradeLocalRejectsSameNewerWrongIDAndDirectory(t *testing.T) {
	manager, id, _ := downgradeFixture(t)
	cases := []struct {
		name   string
		mutate func(*Manifest)
		dir    bool
	}{
		{name: "same", mutate: func(m *Manifest) { m.Version = "1.1.0" }},
		{name: "newer", mutate: func(m *Manifest) { m.Version = "2.0.0" }},
		{name: "wrong-id", mutate: func(m *Manifest) { m.ID = "other.plugin" }},
		{name: "directory", dir: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source := writeTestManifest(t, test.mutate)
			if test.dir {
				if _, err := manager.DowngradeLocal(context.Background(), id, source, true); err == nil {
					t.Fatal("accepted directory downgrade")
				}
				return
			}
			if _, err := manager.DowngradeLocal(context.Background(), id, localPackageZIP(t, source), true); err == nil {
				t.Fatal("accepted invalid downgrade")
			}
		})
	}
}

func TestDowngradeReplacementRejectsDirectoryAfterZIPCheck(t *testing.T) {
	manager, id, _ := downgradeFixture(t)
	source := writeTestManifest(t, func(m *Manifest) { m.Version = "1.0.0" })
	// Simulate a ZIP path being replaced by a directory while waiting for the
	// operation lock, after DowngradeLocal's initial ZIP check.
	if _, err := manager.replaceLocal(context.Background(), id, source, os.Rename, true); err == nil || !strings.Contains(err.Error(), "ZIP") {
		t.Fatalf("accepted directory as a downgrade source: %v", err)
	}
	record, _ := manager.record(id)
	installed, err := readManifest(record.root)
	if err != nil || installed.Version != "1.1.0" {
		t.Fatalf("directory downgrade changed installed package: %v", err)
	}
}

func TestDowngradeLocalRollbackRestoresOldPackage(t *testing.T) {
	manager, id, source := downgradeFixture(t)
	record, _ := manager.record(id)
	record.enabled = true
	record.state = pluginStateStopped
	if err := writePluginEnabled(id, true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err := manager.replaceLocal(context.Background(), id, source, func(from, to string) error {
		calls++
		if calls == 2 {
			return errors.New("injected rename failure")
		}
		return os.Rename(from, to)
	}, true)
	if err == nil {
		t.Fatal("expected replacement failure")
	}
	installed, readErr := readManifest(record.root)
	if readErr != nil || installed.Version != "1.1.0" || !record.enabled {
		t.Fatalf("old package was not restored: %v", err)
	}
}

func TestUpgradeLocalPreservesKVAndDisables(t *testing.T) {
	manager, id, source := upgradeFixture(t)
	if err := pluginKVSetRaw(id, "config", json.RawMessage(`{"saved":true}`)); err != nil {
		t.Fatal(err)
	}
	record, _ := manager.record(id)
	record.enabled = true
	record.state = pluginStateStopped
	if err := writePluginEnabled(id, true); err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.UpgradeLocal(context.Background(), id, source)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "1.1.0" || record.manifest.Version != "1.1.0" || record.enabled || record.state != pluginStateDisabled {
		t.Fatalf("unexpected upgrade state: %+v", record)
	}
	saved, found, err := pluginKVGetRaw(id, "config")
	if err != nil || !found || string(saved) != `{"saved":true}` {
		t.Fatalf("KV lost: %s %v", saved, err)
	}
	enabled, found, err := readPluginEnabled(id)
	if err != nil || !found || enabled {
		t.Fatal("enabled preference not cleared")
	}
	installed, err := readManifest(record.root)
	if err != nil || installed.Version != "1.1.0" {
		t.Fatalf("new files missing: %v", err)
	}
	entries, _ := os.ReadDir(pluginsDirectory())
	if len(entries) != 1 {
		t.Fatalf("temporary directories remain: %v", entries)
	}
}

func TestUpgradeLocalRejectsInvalidPackages(t *testing.T) {
	for _, kind := range []string{"same", "older", "wrong-id", "symlink", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			manager, id, source := upgradeFixture(t)
			if kind == "same" || kind == "older" || kind == "wrong-id" {
				source = writeTestManifest(t, func(m *Manifest) {
					if kind == "older" {
						m.Version = "0.9.0"
					}
					if kind == "wrong-id" {
						m.ID = "other.plugin"
						m.Version = "2.0.0"
					}
				})
			}
			if kind == "symlink" {
				if err := os.Symlink(filepath.Join(source, manifestFilename), filepath.Join(source, "link")); err != nil {
					t.Skip(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			if _, err := manager.UpgradeLocal(ctx, id, source); err == nil {
				t.Fatal("expected rejection")
			}
			record, _ := manager.record(id)
			manifest, err := readManifest(record.root)
			if err != nil || manifest.Version != "1.0.0" {
				t.Fatal("old package changed")
			}
		})
	}
}

func TestUpgradeLocalRollback(t *testing.T) {
	for _, rollbackFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "restored", true: "backup retained"}[rollbackFails], func(t *testing.T) {
			manager, id, source := upgradeFixture(t)
			record, _ := manager.record(id)
			record.enabled = true
			record.state = pluginStateStopped
			if err := writePluginEnabled(id, true); err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, err := manager.upgradeLocal(context.Background(), id, source, func(from, to string) error {
				calls++
				if calls == 2 || (calls == 3 && rollbackFails) {
					return errors.New("injected rename failure")
				}
				return os.Rename(from, to)
			})
			if err == nil {
				t.Fatal("expected replacement failure")
			}
			if !rollbackFails {
				manifest, readErr := readManifest(record.root)
				if readErr != nil || manifest.Version != "1.0.0" || !record.enabled {
					t.Fatalf("rollback failed: %v", err)
				}
			} else {
				if record.enabled || !strings.Contains(err.Error(), "backup retained") {
					t.Fatalf("unsafe recovery state: %v", err)
				}
				backups, _ := filepath.Glob(filepath.Join(pluginsDirectory(), ".backup-*", "package", manifestFilename))
				if len(backups) != 1 {
					t.Fatal("old backup lost")
				}
			}
		})
	}
}

func TestUpgradeLocalConcurrentRequests(t *testing.T) {
	manager, id, source := upgradeFixture(t)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := manager.UpgradeLocal(context.Background(), id, source); results <- err }()
	}
	successes := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one upgrade, got %d", successes)
	}
	record, _ := manager.record(id)
	manifest, err := readManifest(record.root)
	if err != nil || manifest.Version != "1.1.0" {
		t.Fatalf("invalid installed package: %v", err)
	}
}

func TestUpgradeLocalRejectsSourceContainingInstallDirectory(t *testing.T) {
	manager, id, _ := upgradeFixture(t)
	if _, err := manager.UpgradeLocal(context.Background(), id, pluginsDirectory()); err == nil || !strings.Contains(err.Error(), "cannot contain") {
		t.Fatalf("expected recursive source rejection: %v", err)
	}
}

// The registered upgrade handler is ZIP-only; the manager must unpack it.
func TestUpgradeLocalAcceptsZIPPackage(t *testing.T) {
	manager, id, source := upgradeFixture(t)
	manifest, err := manager.UpgradeLocal(context.Background(), id, localPackageZIP(t, source))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "1.1.0" {
		t.Fatalf("ZIP upgrade failed: %+v", manifest)
	}
	record, _ := manager.record(id)
	installed, err := readManifest(record.root)
	if err != nil || installed.Version != "1.1.0" {
		t.Fatalf("new files missing: %v", err)
	}
	dirEntries, _ := os.ReadDir(pluginsDirectory())
	if len(dirEntries) != 1 {
		t.Fatalf("temporary directories remain: %v", dirEntries)
	}
}
