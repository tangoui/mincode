package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectorySyncAddsRemovesAndRetries(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	source := writeTestManifest(t, nil)
	target := filepath.Join(pluginsDirectory(), "example.plugin")
	sync := func() {
		t.Helper()
		if err := manager.syncInstalled(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	sync()
	if err := copyPluginTree(source, target); err != nil {
		t.Fatal(err)
	}
	sync()
	record, err := manager.record("example.plugin")
	if err != nil {
		t.Fatal(err)
	}
	if record.enabled || record.state != pluginStateDisabled {
		t.Fatal("discovery must not enable code")
	}
	sync()
	if len(manager.order) != 1 {
		t.Fatal("duplicate registration")
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	sync()
	if len(manager.order) != 0 || len(manager.records) != 0 {
		t.Fatal("deleted directory retained")
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	sync()
	record, err = manager.record("example.plugin")
	if err != nil || record.state != pluginStateError || record.runtime != nil {
		t.Fatal("incomplete package must be visible but inert")
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := copyPluginTree(source, target); err != nil {
		t.Fatal(err)
	}
	sync()
	record, err = manager.record("example.plugin")
	if err != nil || record.enabled || record.runtime == nil || record.state != pluginStateDisabled {
		t.Fatal("reappearing package not rediscovered disabled")
	}
}

func TestDirectorySyncSkipsUnchangedInvalidManifest(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	entry := filepath.Join("bin", "plugin-runtime")
	source := writeTestManifest(t, func(manifest *Manifest) { manifest.Runtime.Entry = filepath.ToSlash(entry) })
	target := filepath.Join(pluginsDirectory(), "example.plugin")
	if err := copyPluginTree(source, target); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	sync := func() *pluginRecord {
		t.Helper()
		if err := manager.syncInstalled(context.Background()); err != nil {
			t.Fatal(err)
		}
		record, err := manager.record("example.plugin")
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	if record := sync(); record.manifest != nil || record.state != pluginStateError {
		t.Fatal("package with missing runtime entry must be invalid")
	}
	// A nested change does not alter the directory/manifest fingerprint, so the
	// cached verdict is reused instead of re-parsing on every list.
	if err := os.WriteFile(filepath.Join(target, entry), []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if record := sync(); record.manifest != nil {
		t.Fatal("unchanged invalid package should not be re-parsed within the recheck interval")
	}
	invalidManifests.Lock()
	cached := invalidManifests.entries[target]
	cached.checkedAt = cached.checkedAt.Add(-invalidManifestRecheckInterval)
	invalidManifests.entries[target] = cached
	invalidManifests.Unlock()
	if record := sync(); record.manifest == nil || record.state != pluginStateDisabled {
		t.Fatal("repaired package not rediscovered after recheck interval")
	}
	invalidManifests.Lock()
	_, stillCached := invalidManifests.entries[target]
	invalidManifests.Unlock()
	if stillCached {
		t.Fatal("valid package left in invalid manifest cache")
	}
}

func TestDirectorySyncRereadsChangedInvalidManifest(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	source := writeTestManifest(t, nil)
	target := filepath.Join(pluginsDirectory(), "example.plugin")
	if err := copyPluginTree(source, target); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(filepath.Join(target, manifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, manifestFilename), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.syncInstalled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if record, err := manager.record("example.plugin"); err != nil || record.manifest != nil {
		t.Fatal("broken manifest must register an invalid record")
	}
	if err := os.WriteFile(filepath.Join(target, manifestFilename), valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.syncInstalled(context.Background()); err != nil {
		t.Fatal(err)
	}
	if record, err := manager.record("example.plugin"); err != nil || record.manifest == nil {
		t.Fatal("rewritten manifest must be re-read immediately")
	}
}

// Deleting a plugin folder outside the app is an uninstall: like the in-app
// uninstall, it must drop the "dangerous actions" trust, so the same package
// copied back does not run dangerous actions without confirmation again.
func TestDirectorySyncRemovalDropsDangerousActionTrust(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	source := writeTestManifest(t, nil)
	target := filepath.Join(pluginsDirectory(), "example.plugin")
	sync := func() {
		t.Helper()
		if err := manager.syncInstalled(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	trusted := func() bool {
		t.Helper()
		_, found, err := pluginKVGetRaw(hostPluginID, pluginDangerousActionTrustKey("example.plugin"))
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	if err := copyPluginTree(source, target); err != nil {
		t.Fatal(err)
	}
	sync()
	if err := pluginKVSetRaw(hostPluginID, pluginDangerousActionTrustKey("example.plugin"), []byte(`{"version":1,"pluginVersion":"1.0.0"}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	sync()
	if trusted() {
		t.Fatal("removing the folder outside the app kept the dangerous-actions trust")
	}

	// A package that appears while the app runs is a new install, too.
	if err := pluginKVSetRaw(hostPluginID, pluginDangerousActionTrustKey("example.plugin"), []byte(`{"version":1,"pluginVersion":"1.0.0"}`)); err != nil {
		t.Fatal(err)
	}
	if err := copyPluginTree(source, target); err != nil {
		t.Fatal(err)
	}
	sync()
	if trusted() {
		t.Fatal("a package reappearing while the app runs inherited the dangerous-actions trust")
	}
}
