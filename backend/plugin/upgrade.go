package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"

	"golang.org/x/mod/semver"
)

func pluginVersionNewer(candidate, installed string) bool {
	return validMarketplaceVersion(candidate) && validMarketplaceVersion(installed) && semver.Compare("v"+candidate, "v"+installed) > 0
}

func pluginVersionOlder(candidate, installed string) bool {
	return validMarketplaceVersion(candidate) && validMarketplaceVersion(installed) && semver.Compare("v"+candidate, "v"+installed) < 0
}

func init() {
	core.Register("plugin_host_upgrade_local", func(_, args string) (string, error) {
		var request struct {
			ID         string `json:"id"`
			SourcePath string `json:"sourcePath"`
		}
		if err := core.DecodeArgs(args, &request); err != nil {
			return "", err
		}
		if err := requirePluginZIP(request.SourcePath); err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(context.Background(), pluginShutdownTimeout)
		defer cancel()
		manifest, err := host().UpgradeLocal(ctx, request.ID, request.SourcePath)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(manifest)
	})
	core.Register("plugin_host_downgrade_local", pluginHostDowngradeLocalHandler)
}

func pluginHostDowngradeLocalHandler(_, args string) (string, error) {
	var request struct {
		ID             string `json:"id"`
		SourcePath     string `json:"sourcePath"`
		AllowDowngrade bool   `json:"allowDowngrade"`
	}
	if err := core.DecodeArgs(args, &request); err != nil {
		return "", err
	}
	if !request.AllowDowngrade {
		return "", errors.New("local plugin downgrade requires explicit confirmation (allowDowngrade: true)")
	}
	if err := requirePluginZIP(request.SourcePath); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pluginShutdownTimeout)
	defer cancel()
	manifest, err := host().DowngradeLocal(ctx, request.ID, request.SourcePath, request.AllowDowngrade)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(manifest)
}

// UpgradeLocal replaces only the package, never the plugin KV namespace.
// A successful upgrade is disabled until the user approves the new capabilities.
func (manager *pluginManager) UpgradeLocal(ctx context.Context, id, sourcePath string) (Manifest, error) {
	return manager.upgradeLocal(ctx, id, sourcePath, renamePluginDirectory)
}

func (manager *pluginManager) upgradeLocal(ctx context.Context, id, sourcePath string, rename func(string, string) error) (Manifest, error) {
	return manager.replaceLocal(ctx, id, sourcePath, rename, false)
}

// DowngradeLocal accepts only an explicitly confirmed ZIP file path, not a
// marketplace release or URL. Confirmation must be obtained by the caller's UI;
// this flag prevents accidental reuse of the upgrade command, not forged consent.
func (manager *pluginManager) DowngradeLocal(ctx context.Context, id, sourcePath string, allowDowngrade bool) (Manifest, error) {
	if !allowDowngrade {
		return Manifest{}, errors.New("local plugin downgrade requires explicit confirmation (allowDowngrade: true)")
	}
	if err := requirePluginZIP(sourcePath); err != nil {
		return Manifest{}, err
	}
	return manager.replaceLocal(ctx, id, sourcePath, renamePluginDirectory, true)
}

func (manager *pluginManager) replaceLocal(ctx context.Context, id, sourcePath string, rename func(string, string) error, downgrade bool) (Manifest, error) {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	record, err := manager.record(id)
	if err != nil {
		return Manifest{}, err
	}
	if id != record.id {
		return Manifest{}, errors.New("replacement plugin ID must match the installed plugin exactly")
	}
	if err := lockPluginLifecycle(ctx, record); err != nil {
		return Manifest{}, err
	}
	changed := false
	defer func() {
		record.lifecycleMu.Unlock()
		if changed {
			manager.emitState(record)
		}
	}()
	record.mu.Lock()
	root, oldManifest, oldEnabled := record.root, record.manifest, record.enabled
	local := record.source == pluginSourceLocal
	record.mu.Unlock()
	if !local || oldManifest == nil {
		return Manifest{}, errors.New("only valid installed local plugins can be replaced")
	}
	if strings.TrimSpace(sourcePath) == "" {
		return Manifest{}, errors.New("replacement requires a source directory or ZIP package")
	}
	// ZIP packages unpack only into a private, disposable directory; directory
	// sources are borrowed. Cleanup runs after the new package is staged.
	source, originalSource, cleanup, err := prepareInstallSource(sourcePath)
	if err != nil {
		return Manifest{}, err
	}
	defer cleanup()
	// A path checked as a ZIP before acquiring operationMu may have been
	// replaced by a directory. Only proceed if this call unpacked a ZIP.
	if downgrade && source == originalSource {
		return Manifest{}, errors.New("downgrade requires a local ZIP package")
	}
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return Manifest{}, err
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil {
		return Manifest{}, err
	}
	relative, err := filepath.Rel(resolvedSource, resolvedParent)
	if err != nil {
		return Manifest{}, err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return Manifest{}, errors.New("upgrade source cannot contain the plugin installation directory")
	}
	if _, err := inspectInstallDirectory(source); err != nil {
		return Manifest{}, err
	}
	manifest, err := readManifest(source)
	if err != nil {
		return Manifest{}, err
	}
	if manifest.ID != record.id {
		return Manifest{}, errors.New("upgrade plugin ID must match the installed plugin")
	}
	validVersion := pluginVersionNewer
	versionError := "upgrade requires a newer semantic version"
	operation := "upgrade"
	if downgrade {
		validVersion = pluginVersionOlder
		versionError = "downgrade requires an older semantic version"
		operation = "downgrade"
	}
	if !validVersion(manifest.Version, oldManifest.Version) {
		return Manifest{}, errors.New(versionError)
	}
	staging, err := os.MkdirTemp(filepath.Dir(root), ".upgrade-*")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(staging)
	if err := copyPluginTree(source, staging); err != nil {
		return Manifest{}, err
	}
	staged, err := readManifest(staging)
	if err != nil {
		return Manifest{}, err
	}
	if _, err := inspectInstallDirectory(staging); err != nil {
		return Manifest{}, err
	}
	if staged.ID != record.id || !validVersion(staged.Version, oldManifest.Version) || staged.Version != manifest.Version {
		return Manifest{}, fmt.Errorf("staged %s has invalid ID or version", operation)
	}
	backupDir, err := os.MkdirTemp(filepath.Dir(root), ".backup-*")
	if err != nil {
		return Manifest{}, err
	}
	backup := filepath.Join(backupDir, "package")
	preserveBackup := false
	defer func() {
		if !preserveBackup {
			_ = os.RemoveAll(backupDir)
		}
	}()
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	changed = true
	// Full trust ends with the old package. Revoke before the plugin turns
	// disabled below, so a concurrent read cannot lapse it as "disabled"
	// first and the audit names the upgrade. A failed upgrade does not bring
	// it back: the user reviews and trusts the plugin again.
	manager.revokeFullTrust(record.id, fullTrustLapseUpgraded)
	if err := manager.stopRecordLocked(ctx, record, true); err != nil {
		return Manifest{}, err
	}
	// Disable (and persist) before stopping the plugin's servers and agents so
	// no new launch passes the gates in between.
	persistErr := writePluginEnabled(record.id, false)
	record.mu.Lock()
	if persistErr != nil {
		record.state = pluginStateError
		record.lastError = fmt.Sprintf("%s could not persist disabled state: %v", operation, persistErr)
	} else {
		record.enabled = false
	}
	record.mu.Unlock()
	stopPluginLanguageServers(record.id, "upgrade")
	stopPluginDebugSessions(record.id, "upgrade")
	stopPluginMcpServers(record.id)
	stopPluginAcpAgents(record.id)
	if persistErr != nil {
		return Manifest{}, persistErr
	}

	restoreEnabled := func(cause error) (Manifest, error) {
		restoreErr := writePluginEnabled(record.id, oldEnabled)
		record.mu.Lock()
		if restoreErr == nil {
			record.enabled = oldEnabled
		}
		if record.enabled {
			record.state = pluginStateStopped
		} else {
			record.state = pluginStateDisabled
		}
		record.mu.Unlock()
		return Manifest{}, errors.Join(cause, restoreErr)
	}
	if err := rename(root, backup); err != nil {
		return restoreEnabled(fmt.Errorf("backup plugin: %w", err))
	}
	if err := rename(staging, root); err != nil {
		if rollbackErr := rename(backup, root); rollbackErr != nil {
			preserveBackup = true
			record.mu.Lock()
			record.state = pluginStateError
			record.lastError = fmt.Sprintf("%s failed; recover old package from %s: %v", operation, backup, rollbackErr)
			record.mu.Unlock()
			return Manifest{}, fmt.Errorf("%s failed: %w; rollback failed, backup retained at %s: %v", operation, err, backup, rollbackErr)
		}
		return restoreEnabled(fmt.Errorf("replace plugin (old package restored): %w", err))
	}
	record.mu.Lock()
	record.manifest = &staged
	record.capabilities = append([]api.Capability(nil), staged.Capabilities...)
	record.state = pluginStateDisabled
	record.lastError = ""
	record.runtime = nil
	if manifestGlobalOnlyReason(&staged) != "" {
		// Data-only packages and scope-guarded points are always global.
		record.scope, record.workspaces = pluginScopeGlobal, nil
	}
	if !staged.isDeclarative() {
		record.runtime = manager.newRecordRuntime(record, root, staged)
	}
	record.mu.Unlock()
	record.appendLog("info", "plugin "+operation+"d to "+staged.Version+"; enable to approve capabilities")
	return staged, nil
}
