package plugin

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// invalidManifestRecheckInterval bounds how long an unchanged invalid package
// is trusted to stay invalid; validation also depends on files (runtime entry,
// UI entry) that the directory/manifest fingerprint does not cover.
const invalidManifestRecheckInterval = 10 * time.Second

// manifestFingerprint captures the plugin directory and plugin.json revisions.
type manifestFingerprint struct {
	dirModTime, manifestModTime time.Time
	manifestSize                int64
	manifestMode                fs.FileMode
	manifestFound               bool
}

type invalidManifestEntry struct {
	fingerprint manifestFingerprint
	checkedAt   time.Time
}

// invalidManifests remembers packages that failed readManifest so repeated
// plugin_host_list calls skip re-parsing and re-validating them.
var invalidManifests = struct {
	sync.Mutex
	entries map[string]invalidManifestEntry
}{entries: map[string]invalidManifestEntry{}}

func readManifestFingerprint(root string) (manifestFingerprint, bool) {
	dirInfo, err := os.Stat(root)
	if err != nil {
		return manifestFingerprint{}, false
	}
	fingerprint := manifestFingerprint{dirModTime: dirInfo.ModTime()}
	if info, err := os.Lstat(filepath.Join(root, manifestFilename)); err == nil {
		fingerprint.manifestFound = true
		fingerprint.manifestModTime = info.ModTime()
		fingerprint.manifestSize = info.Size()
		fingerprint.manifestMode = info.Mode()
	}
	return fingerprint, true
}

func invalidManifestUnchanged(root string, fingerprint manifestFingerprint, now time.Time) bool {
	invalidManifests.Lock()
	defer invalidManifests.Unlock()
	entry, ok := invalidManifests.entries[root]
	return ok && entry.fingerprint == fingerprint && now.Sub(entry.checkedAt) < invalidManifestRecheckInterval
}

func rememberInvalidManifest(root string, fingerprint manifestFingerprint, valid bool, now time.Time) {
	invalidManifests.Lock()
	defer invalidManifests.Unlock()
	if valid {
		delete(invalidManifests.entries, root)
		return
	}
	invalidManifests.entries[root] = invalidManifestEntry{fingerprint: fingerprint, checkedAt: now}
}

// Reconcile directory membership only. Existing manifests are refreshed through
// the explicit upgrade/reload flow; discovery never starts executable code.
func (manager *pluginManager) syncInstalled(ctx context.Context) error {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	entries, err := os.ReadDir(pluginsDirectory())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	present := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		present[entry.Name()] = true
	}
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	for _, record := range records {
		record.mu.Lock()
		local, id, root := record.source == pluginSourceLocal, record.id, record.root
		record.mu.Unlock()
		if !local || filepath.Dir(root) != pluginsDirectory() || present[filepath.Base(root)] {
			continue
		}
		// Retire the record before waiting for activation: stale callers must not
		// restart a package between stop and removal from the index.
		record.mu.Lock()
		record.approvalEpoch++
		record.enabled = false
		record.mu.Unlock()
		if err := manager.stopRecord(ctx, record, true); err != nil {
			return err
		}
		manager.mu.Lock()
		delete(manager.records, id)
		for i, existing := range manager.order {
			if existing == id {
				manager.order = append(manager.order[:i], manager.order[i+1:]...)
				break
			}
		}
		manager.mu.Unlock()
		manager.revokePluginUI(id)
		// The folder was removed outside the app: that is an uninstall for full
		// trust, which must not return if the same bytes reappear later.
		manager.revokeFullTrust(id, fullTrustLapseUninstalled)
		// The legacy per-version "dangerous actions" trust goes with it, as on an in-app uninstall.
		_ = pluginKVDeleteRaw(hostPluginID, pluginDangerousActionTrustKey(id))
		// Preserve plugin storage, but never silently re-enable a reappearing package.
		if err := writePluginEnabled(id, false); err != nil {
			return err
		}
		manager.emitUninstalled(id)
	}
	for name := range present {
		existing, _ := manager.record(name)
		if existing != nil {
			existing.mu.Lock()
			invalid := existing.source == pluginSourceLocal && existing.runtime == nil && existing.manifest == nil
			existing.mu.Unlock()
			if !invalid {
				continue
			}
		}
		root := filepath.Join(pluginsDirectory(), name)
		now := time.Now()
		fingerprint, fingerprinted := readManifestFingerprint(root)
		if existing != nil && fingerprinted && invalidManifestUnchanged(root, fingerprint, now) {
			continue
		}
		manifest, err := readManifest(root)
		if err != nil || manifest.ID != name {
			if fingerprinted {
				rememberInvalidManifest(root, fingerprint, false, now)
			}
			manager.registerInvalidLocal(root, err)
			continue
		}
		rememberInvalidManifest(root, fingerprint, true, now)
		if err := writePluginEnabled(manifest.ID, false); err != nil {
			return err
		}
		if existing != nil {
			manager.mu.Lock()
			delete(manager.records, name)
			for i, id := range manager.order {
				if id == name {
					manager.order = append(manager.order[:i], manager.order[i+1:]...)
					break
				}
			}
			manager.mu.Unlock()
		}
		// A package that appears while the app runs is a new install: no full
		// trust carries over from an earlier copy (e.g. one deleted while the
		// app was closed). Startup scanning does not revoke.
		manager.revokeFullTrust(manifest.ID, fullTrustLapseUninstalled)
		_ = pluginKVDeleteRaw(hostPluginID, pluginDangerousActionTrustKey(manifest.ID))
		if err := manager.registerLocal(root, manifest); err != nil {
			return err
		}
	}
	return nil
}
