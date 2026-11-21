package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/lsp"
	"mincode/app/plugins/api"
)

// User-confirmed loading of contributes.jdtlsBundles.
//
// A plugin's accepted jars (its entries minus those whose commands an earlier
// installed plugin owns, see jdtlsBundleDeclarations for the persisted order)
// reach the built-in jdtls only when all of these hold at the moment jdtls
// initializes (lsp.SetJdtlsBundleProvider):
//
//   - the plugin passes jdtlsBundleSource: installed with a manifest, enabled,
//     not failed, active in the workspace, shell granted. The plugin process
//     state (starting, running, stopping, stopped, idle-stopped) does not
//     matter: the jars run inside jdtls, not in the plugin process;
//   - every jar, dependencies included, is still a regular file inside the
//     package within the size limit, and is hashed again without the hash
//     cache; one missing or invalid jar makes the whole plugin unavailable;
//   - the user confirmed exactly the resulting fingerprint (plugin ID, version,
//     development generation, each jar's relative path, SHA-256 and sorted
//     declared commands, so widening the command list needs a new
//     confirmation; dependency jars count like any other jar, marked with
//     their entry's jar, and the jar list is sorted so declaration order does
//     not matter) through
//     plugin_host_jdtls_bundles_trust. The confirmation lives in the shared
//     process-launch trust store (host KV "<pluginId>.jdtlsBundles.trust",
//     never exported) and is deleted on uninstall and Clear Plugin Data.
//
// Whenever the set of loadable jars changes (enable, disable, upgrade,
// uninstall, workspace switch, confirmation) the host emits
// project-lsp:restart-required; it never restarts jdtls itself.

const (
	jdtlsBundleTrustEntryKey      = "bundles"
	jdtlsBundleFingerprintVersion = 1
	jdtlsBundleNoun               = "jdtls bundles"
)

// Seams to the lsp package (tests replace them).
var (
	jdtlsSessionBundles        = lsp.JdtlsSessionBundles
	jdtlsReady                 = lsp.JdtlsReady
	notifyJdtlsRestartRequired = lsp.NotifyJdtlsRestartRequired
	jdtlsBundleHashJar         = hashJdtlsBundleJar
)

// JdtlsBundleJar is one hashed jar of a plugin's bundles. A dependency jar
// has no commands and names its entry's jar in DependencyOf.
type JdtlsBundleJar struct {
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	relative string
	path     string
	Commands []string `json:"commands"`
	// DependencyOf is the package-relative path of the entry's jar (the
	// value the fingerprint covers); empty for an entry's own jar.
	DependencyOf string `json:"dependencyOf,omitempty"`
}

// JdtlsBundleStatus is a plugin's loadable jars and their confirmation.
// Loaded means the running jdtls of the project was started with all of them
// while they were confirmed.
type JdtlsBundleStatus struct {
	PluginID          string           `json:"pluginId"`
	PluginName        string           `json:"pluginName"`
	PluginVersion     string           `json:"pluginVersion"`
	Jars              []JdtlsBundleJar `json:"jars"`
	Fingerprint       string           `json:"fingerprint"`
	Trusted           bool             `json:"trusted"`
	PreviouslyTrusted bool             `json:"previouslyTrusted"`
	Loaded            bool             `json:"loaded"`
	// Error explains why a plugin's jars cannot be loaded (for example a
	// jar removed from the package); such a plugin is never trusted.
	Error string `json:"error,omitempty"`
}

// JdtlsBundlesStatus answers plugin_host_jdtls_bundles_status.
type JdtlsBundlesStatus struct {
	Bundles []JdtlsBundleStatus `json:"bundles"`
	// RestartRequired: the project's running jdtls was started with another
	// set of jars than the confirmed set it would load now.
	RestartRequired bool               `json:"restartRequired"`
	Jdtls           JdtlsRuntimeStatus `json:"jdtls"`
}

// JdtlsRuntimeStatus is the project's built-in jdtls state.
type JdtlsRuntimeStatus struct {
	Running bool `json:"running"`
	Ready   bool `json:"ready"`
}

// jdtlsBundleTracker remembers the last announced set of loadable jars.
type jdtlsBundleTracker struct {
	mu    sync.Mutex
	known bool
	last  []string
}

// jdtlsBundleDeclaration is one plugin's validated entries in manager order.
type jdtlsBundleDeclaration struct {
	pluginID string
	entries  []JdtlsBundleContribution
}

// jdtlsBundleOwnership is the result of assigning commands to plugins.
type jdtlsBundleOwnership struct {
	accepted map[string][]JdtlsBundleContribution
	owners   map[string]string
	// rejected lists, per plugin, the entry indexes skipped for a conflict
	// with the message to show.
	rejected map[string][]ContributionIssue
}

// resolveJdtlsBundleOwnership gives each command to the first plugin (in
// manager order) that declares it; a later plugin's entry that declares any
// owned command is rejected as a whole.
func resolveJdtlsBundleOwnership(declarations []jdtlsBundleDeclaration) jdtlsBundleOwnership {
	result := jdtlsBundleOwnership{
		accepted: map[string][]JdtlsBundleContribution{},
		owners:   map[string]string{},
		rejected: map[string][]ContributionIssue{},
	}
	for _, declaration := range declarations {
		for index, entry := range declaration.entries {
			conflict := ""
			for _, command := range entry.Commands {
				if owner, taken := result.owners[command]; taken && owner != declaration.pluginID {
					conflict = fmt.Sprintf("command %q is already declared by plugin %q", command, owner)
					break
				}
			}
			if conflict != "" {
				result.rejected[declaration.pluginID] = append(result.rejected[declaration.pluginID], ContributionIssue{
					Point: jdtlsBundlePointKey, Entry: index, ID: entry.relative(), Message: conflict,
				})
				continue
			}
			for _, command := range entry.Commands {
				result.owners[command] = declaration.pluginID
			}
			result.accepted[declaration.pluginID] = append(result.accepted[declaration.pluginID], entry)
		}
	}
	return result
}

func jdtlsBundleEntries(manifest *Manifest) []JdtlsBundleContribution {
	if manifest == nil {
		return nil
	}
	resolved := manifest.contributions[jdtlsBundlePointKey]
	if resolved == nil {
		return nil
	}
	entries := make([]JdtlsBundleContribution, 0, len(resolved.Entries))
	for _, entry := range resolved.Entries {
		if bundle, ok := entry.(JdtlsBundleContribution); ok {
			entries = append(entries, bundle)
		}
	}
	return entries
}

// jdtlsBundleDeclarations lists every installed (or development) plugin's
// entries, enabled or not, in persisted first-seen order (jdtls_bundles_order.go):
// command ownership must survive restarts, where manager order is directory
// order, and must not follow the current enablement, so a confirmed plugin
// never loses commands (and its fingerprint) to a later one. preview, when
// non-nil, replaces the installed record with the same ID (keeping its place)
// or comes after every installed plugin; it is never persisted.
func (manager *pluginManager) jdtlsBundleDeclarations(preview *Manifest) []jdtlsBundleDeclaration {
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	declarations := make([]jdtlsBundleDeclaration, 0, len(records)+1)
	previewed := false
	for _, record := range records {
		record.mu.Lock()
		id, manifest, builtin := record.id, record.manifest, record.builtin != nil
		record.mu.Unlock()
		if preview != nil && id == preview.ID {
			manifest, builtin, previewed = preview, false, true
		}
		if builtin {
			continue
		}
		if entries := jdtlsBundleEntries(manifest); len(entries) > 0 {
			declarations = append(declarations, jdtlsBundleDeclaration{pluginID: id, entries: entries})
		}
	}
	installed := make([]string, 0, len(declarations))
	for _, declaration := range declarations {
		installed = append(installed, declaration.pluginID)
	}
	var order map[string]int64
	if preview == nil {
		order = jdtlsBundleInstallOrder(installed)
	} else {
		// Previews are read-only, including upgrades of installed plugins
		// that add bundles for the first time. Unseen plugins sort last,
		// without consuming a persisted install sequence.
		jdtlsBundleOrderMu.Lock()
		store, _ := readJdtlsBundleOrder()
		jdtlsBundleOrderMu.Unlock()
		order = make(map[string]int64, len(installed))
		for _, id := range installed {
			if sequence, ok := store.Plugins[id]; ok {
				order[id] = sequence
			} else if id == preview.ID {
				order[id] = 1<<63 - 1 // an unseen preview is always last
			} else {
				order[id] = store.Next
			}
		}
	}
	slices.SortStableFunc(declarations, func(a, b jdtlsBundleDeclaration) int {
		if order[a.pluginID] != order[b.pluginID] {
			return compareInt64(order[a.pluginID], order[b.pluginID])
		}
		return strings.Compare(a.pluginID, b.pluginID)
	})
	if preview != nil && !previewed {
		if entries := jdtlsBundleEntries(preview); len(entries) > 0 {
			declarations = append(declarations, jdtlsBundleDeclaration{pluginID: preview.ID, entries: entries})
		}
	}
	return declarations
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// jdtlsBundlePreviewIssues returns the entries of a previewed package that
// installing it would reject because other installed plugins own commands.
func (manager *pluginManager) jdtlsBundlePreviewIssues(preview Manifest) []ContributionIssue {
	if len(jdtlsBundleEntries(&preview)) == 0 {
		return nil
	}
	return resolveJdtlsBundleOwnership(manager.jdtlsBundleDeclarations(&preview)).rejected[preview.ID]
}

// jdtlsCommandOwner returns the plugin that owns a jdtls command through an
// accepted jdtlsBundles entry, provided that plugin passes jdtlsBundleSource
// and its current jars are confirmed; otherwise "". Ownership itself is
// install-level (a disabled owner keeps the command from later plugins).
func (manager *pluginManager) jdtlsCommandOwner(command string) string {
	owner, _, _ := manager.jdtlsCommandRoute(command)
	return owner
}

// jdtlsCommandRoute is jdtlsCommandOwner plus the owner's bundle status and
// the absolute path of the confirmed jar that declares the command.
func (manager *pluginManager) jdtlsCommandRoute(command string) (string, JdtlsBundleStatus, string) {
	ownership := resolveJdtlsBundleOwnership(manager.jdtlsBundleDeclarations(nil))
	owner := ownership.owners[command]
	if owner == "" {
		return "", JdtlsBundleStatus{}, ""
	}
	status, ok := manager.jdtlsBundleStatus(owner, ownership.accepted[owner], false)
	if !ok || !status.Trusted || status.Error != "" {
		return "", JdtlsBundleStatus{}, ""
	}
	for _, jar := range status.Jars {
		if slices.Contains(jar.Commands, command) {
			return owner, status, jar.path
		}
	}
	return "", JdtlsBundleStatus{}, ""
}

// jdtlsBundleSource applies the jdtlsBundles gates to one plugin and copies
// its entries. Unlike processLaunchSource it ignores the plugin process
// state, so stopping or idle-stopping the plugin never changes the jar set.
func (manager *pluginManager) jdtlsBundleSource(pluginID string) (processLaunchRecord, error) {
	pluginID = strings.TrimSpace(pluginID)
	if _, _, ok := enabledContributionPoint(jdtlsBundlePointKey); !ok {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "%s are not enabled in this host", jdtlsBundleNoun)
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q is not installed", pluginID)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.manifest == nil || record.builtin != nil || record.manifest.isDeclarative() {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q has no %s", pluginID, jdtlsBundleNoun)
	}
	if !record.enabled {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q is disabled", pluginID)
	}
	if record.state == pluginStateError {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q is not available", pluginID)
	}
	if !manager.workspaceActiveLocked(record) {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q is not active in this workspace", pluginID)
	}
	if !slices.Contains(record.capabilities, api.CapabilityShell) {
		return processLaunchRecord{}, processLaunchError(processLaunchErrForbidden, "plugin %q is not granted the shell capability", pluginID)
	}
	resolved := record.manifest.contributions[jdtlsBundlePointKey]
	if resolved == nil {
		return processLaunchRecord{}, processLaunchError(processLaunchErrUnavailable, "plugin %q has no %s", pluginID, jdtlsBundleNoun)
	}
	return processLaunchRecord{
		pluginID: pluginID, pluginName: record.manifest.Name, pluginVersion: record.manifest.Version,
		root: record.root, devGeneration: processLaunchDevGenerationLocked(record),
		entries: append([]any(nil), resolved.Entries...),
	}, nil
}

// jdtlsBundleStatuses resolves the loadable bundles of every plugin that
// passes the record gates. fresh hashes every jar without the hash cache.
// trustedOnly (the loadable-set callers) skips plugins without any stored
// confirmation: their jars could not load, so they are not hashed.
func (manager *pluginManager) jdtlsBundleStatuses(fresh, trustedOnly bool) []JdtlsBundleStatus {
	declarations := manager.jdtlsBundleDeclarations(nil)
	ownership := resolveJdtlsBundleOwnership(declarations)
	statuses := []JdtlsBundleStatus{}
	for _, declaration := range declarations {
		accepted := ownership.accepted[declaration.pluginID]
		if len(accepted) == 0 {
			continue
		}
		if trustedOnly && !hasJdtlsBundleTrust(declaration.pluginID) {
			continue
		}
		status, ok := manager.jdtlsBundleStatus(declaration.pluginID, accepted, fresh)
		if ok {
			statuses = append(statuses, status)
		}
	}
	return statuses
}

// jdtlsBundleStatus applies the record gates to one plugin and hashes the
// accepted entries that the plugin still declares. ok is false when the
// plugin does not pass the gates.
func (manager *pluginManager) jdtlsBundleStatus(pluginID string, accepted []JdtlsBundleContribution, fresh bool) (JdtlsBundleStatus, bool) {
	src, err := manager.jdtlsBundleSource(pluginID)
	if err != nil {
		return JdtlsBundleStatus{}, false
	}
	status := JdtlsBundleStatus{PluginID: src.pluginID, PluginName: src.pluginName, PluginVersion: src.pluginVersion, Jars: []JdtlsBundleJar{}}
	bundles := make([]JdtlsBundleContribution, 0, len(src.entries))
	for _, entry := range src.entries {
		bundle, ok := entry.(JdtlsBundleContribution)
		if ok && slices.ContainsFunc(accepted, func(candidate JdtlsBundleContribution) bool {
			return sameJdtlsBundle(candidate, bundle)
		}) {
			bundles = append(bundles, bundle)
		}
	}
	// The plugin total is checked from file sizes before anything is hashed.
	var total int64
	for _, bundle := range bundles {
		size, err := jdtlsBundleBytes(src.root, bundle)
		if err == nil && total+size > jdtlsBundleBytesMax {
			err = fmt.Errorf("jars exceed %d bytes in total", jdtlsBundleBytesMax)
		}
		if err != nil {
			status.Error = processLaunchError(processLaunchErrUnavailable, "%s: %v", jdtlsBundleNoun, err).Error()
			return status, true
		}
		total += size
	}
	for _, bundle := range bundles {
		jar, err := jdtlsBundleHashJar(src.root, bundle.relative(), fresh)
		if err != nil {
			status.Error = err.Error()
			status.Jars = []JdtlsBundleJar{}
			return status, true
		}
		jar.Commands = slices.Sorted(slices.Values(bundle.Commands))
		status.Jars = append(status.Jars, jar)
		for _, relative := range slices.Sorted(slices.Values(bundle.dependencyRelatives())) {
			dependency, err := jdtlsBundleHashJar(src.root, relative, fresh)
			if err != nil {
				status.Error = err.Error()
				status.Jars = []JdtlsBundleJar{}
				return status, true
			}
			dependency.Commands = []string{}
			dependency.DependencyOf = jar.relative
			status.Jars = append(status.Jars, dependency)
		}
	}
	if len(status.Jars) == 0 {
		return JdtlsBundleStatus{}, false
	}
	status.Fingerprint = jdtlsBundleFingerprint(src, status.Jars)
	store, err := readProcessLaunchTrust(src.pluginID, jdtlsBundlePointKey)
	if err != nil {
		return status, true
	}
	if stored, ok := store.Entries[jdtlsBundleTrustEntryKey]; ok {
		status.Trusted = stored.Fingerprint == status.Fingerprint
		status.PreviouslyTrusted = !status.Trusted
	}
	return status, true
}

// hasJdtlsBundleTrust reports whether the plugin has a stored confirmation
// (current or stale). An unreadable store counts as present, so the plugin
// goes through the full check.
func hasJdtlsBundleTrust(pluginID string) bool {
	store, err := readProcessLaunchTrust(pluginID, jdtlsBundlePointKey)
	if err != nil {
		return true
	}
	_, ok := store.Entries[jdtlsBundleTrustEntryKey]
	return ok
}

func hashJdtlsBundleJar(root, relative string, fresh bool) (JdtlsBundleJar, error) {
	path, size, err := jdtlsBundleJarFile(root, relative)
	if err != nil {
		return JdtlsBundleJar{}, processLaunchError(processLaunchErrUnavailable, "jdtls bundle %s: %v", relative, err)
	}
	if fresh {
		forgetProcessLaunchHash(path)
	}
	sum, err := processLaunchFileHash(path, jdtlsBundleJarMaxBytes)
	if err != nil {
		return JdtlsBundleJar{}, err
	}
	return JdtlsBundleJar{Name: filepath.Base(path), SHA256: sum, Bytes: size, relative: relative, path: path}, nil
}

// jdtlsBundleFingerprint hashes {v, point, pluginId, pluginVersion,
// devGeneration, jars:[{relative, sha256, commands (sorted), dependencyOf}]}
// with jars sorted by relative path (a jar appears once per plugin), so the
// order of entries and dependencies does not matter. dependencyOf is omitted
// for an entry's own jar, which keeps the fingerprint of a plugin with one
// entry and no dependencies unchanged.
func jdtlsBundleFingerprint(src processLaunchRecord, jars []JdtlsBundleJar) string {
	type jarIdentity struct {
		Relative     string   `json:"relative"`
		SHA256       string   `json:"sha256"`
		Commands     []string `json:"commands"`
		DependencyOf string   `json:"dependencyOf,omitempty"`
	}
	identities := make([]jarIdentity, 0, len(jars))
	for _, jar := range jars {
		identities = append(identities, jarIdentity{Relative: jar.relative, SHA256: jar.SHA256, Commands: jar.Commands, DependencyOf: jar.DependencyOf})
	}
	slices.SortStableFunc(identities, func(a, b jarIdentity) int { return strings.Compare(a.Relative, b.Relative) })
	canonical, _ := json.Marshal(struct {
		V             int           `json:"v"`
		Point         string        `json:"point"`
		PluginID      string        `json:"pluginId"`
		PluginVersion string        `json:"pluginVersion"`
		DevGeneration string        `json:"devGeneration"`
		Jars          []jarIdentity `json:"jars"`
	}{jdtlsBundleFingerprintVersion, jdtlsBundlePointKey, src.pluginID, src.pluginVersion, src.devGeneration, identities})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// loadableJdtlsBundlePaths returns the sorted absolute paths of every
// confirmed, unchanged jar.
func loadableJdtlsBundlePaths(statuses []JdtlsBundleStatus) []string {
	paths := []string{}
	for _, status := range statuses {
		if !status.Trusted || status.Error != "" {
			continue
		}
		for _, jar := range status.Jars {
			paths = append(paths, jar.path)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// jdtlsBundlePaths is the lsp bundle provider: every jar is re-validated and
// re-hashed without the cache, and only confirmed fingerprints load.
func (manager *pluginManager) jdtlsBundlePaths() []string {
	manager.awaitFirstWorkspaceScopeForBundles()
	return loadableJdtlsBundlePaths(manager.jdtlsBundleStatuses(true, true))
}

// jdtlsBundleScopeWait bounds how long a jdtls initialize waits for the
// main window's first workspace scope (see awaitFirstWorkspaceScopeForBundles).
var jdtlsBundleScopeWait = 5 * time.Second

// awaitFirstWorkspaceScopeForBundles: until the frontend begins its first
// workspace scope the active workspace is unknown, so a plugin scoped to
// workspaces would be filtered out of the jars, and the jdtls that a restored
// editor tab starts early would need a restart on the first debug. When such
// a plugin (enabled, with bundles) exists and no scope has begun, wait for
// the first begin, at most jdtlsBundleScopeWait, or until the scope system
// closes (the lsp provider callback has no context). Globally scoped plugins
// (the default) never wait.
func (manager *pluginManager) awaitFirstWorkspaceScopeForBundles() {
	if manager.workspaceScope().Revision != "0" || !manager.hasWorkspaceScopedJdtlsBundles() {
		return
	}
	begun := make(chan struct{})
	var once sync.Once
	manager.scopeMu.Lock()
	closed, done := manager.scopeClosed, manager.scopeDoneLocked()
	manager.scopeMu.Unlock()
	if closed {
		return
	}
	unsubscribe := manager.OnWorkspaceScopeChange(func(workspaceScopeSnapshot) { once.Do(func() { close(begun) }) })
	defer unsubscribe()
	// A begin between the first check and the subscription.
	if manager.workspaceScope().Revision != "0" {
		return
	}
	timer := time.NewTimer(jdtlsBundleScopeWait)
	defer timer.Stop()
	select {
	case <-begun:
	case <-done: // host shutting down
	case <-timer.C:
	}
}

// hasWorkspaceScopedJdtlsBundles reports whether an enabled installed plugin
// that declares jdtlsBundles is scoped to workspaces.
func (manager *pluginManager) hasWorkspaceScopedJdtlsBundles() bool {
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	for _, record := range records {
		record.mu.Lock()
		scoped := record.enabled && record.builtin == nil && record.scope == pluginScopeWorkspace && len(jdtlsBundleEntries(record.manifest)) > 0
		record.mu.Unlock()
		if scoped {
			return true
		}
	}
	return false
}

// jdtlsBundlesStatus answers plugin_host_jdtls_bundles_status for a project.
func (manager *pluginManager) jdtlsBundlesStatus(projectRoot string) (JdtlsBundlesStatus, error) {
	root, err := canonicalProjectLaunchRoot(projectRoot, jdtlsBundleNoun)
	if err != nil {
		return JdtlsBundlesStatus{}, err
	}
	statuses := manager.jdtlsBundleStatuses(true, false)
	result := JdtlsBundlesStatus{Bundles: statuses}
	result.Jdtls.Ready, result.Jdtls.Running = jdtlsReady(root)
	loaded, running := jdtlsSessionBundles(root)
	if !running {
		result.Jdtls = JdtlsRuntimeStatus{}
		return result, nil
	}
	result.Jdtls.Running = true
	sessionSet := slices.Clone(loaded)
	slices.Sort(sessionSet)
	sessionSet = slices.Compact(sessionSet)
	result.RestartRequired = !slices.Equal(loadableJdtlsBundlePaths(statuses), sessionSet)
	for index := range result.Bundles {
		bundle := &result.Bundles[index]
		bundle.Loaded = bundle.Trusted && bundle.Error == "" && len(bundle.Jars) > 0 &&
			!slices.ContainsFunc(bundle.Jars, func(jar JdtlsBundleJar) bool { return !slices.Contains(sessionSet, jar.path) })
	}
	return result, nil
}

// trustJdtlsBundles stores the confirmation of exactly the reviewed
// fingerprint of the plugin's freshly hashed jars.
func (manager *pluginManager) trustJdtlsBundles(pluginID, reviewed string) (JdtlsBundleStatus, error) {
	pluginID = strings.TrimSpace(pluginID)
	if _, err := manager.jdtlsBundleSource(pluginID); err != nil {
		return JdtlsBundleStatus{}, err
	}
	accepted := resolveJdtlsBundleOwnership(manager.jdtlsBundleDeclarations(nil)).accepted[pluginID]
	if len(accepted) == 0 {
		return JdtlsBundleStatus{}, processLaunchError(processLaunchErrUnavailable, "plugin %q has no loadable jdtls bundles", pluginID)
	}
	status, ok := manager.jdtlsBundleStatus(pluginID, accepted, true)
	if !ok {
		return JdtlsBundleStatus{}, processLaunchError(processLaunchErrUnavailable, "plugin %q has no loadable jdtls bundles", pluginID)
	}
	if status.Error != "" {
		return JdtlsBundleStatus{}, processLaunchError(processLaunchErrUnavailable, "%s", status.Error)
	}
	if strings.TrimSpace(reviewed) != status.Fingerprint {
		return JdtlsBundleStatus{}, processLaunchError(processLaunchErrChanged, "the jdtls bundles of plugin %q changed since they were reviewed", pluginID)
	}
	maxEntries := int(jdtlsBundleSpec.Limit("trustEntriesMax"))
	processLaunchTrustMu.Lock()
	store, err := readProcessLaunchTrust(pluginID, jdtlsBundlePointKey)
	if err == nil {
		store.Entries[jdtlsBundleTrustEntryKey] = processLaunchTrustEntry{Fingerprint: status.Fingerprint, At: time.Now().UnixMilli()}
		err = writeProcessLaunchTrust(pluginID, jdtlsBundlePointKey, store, maxEntries)
	}
	processLaunchTrustMu.Unlock()
	if err != nil {
		return JdtlsBundleStatus{}, processLaunchError(processLaunchErrUnavailable, "save jdtls bundles confirmation: %v", err)
	}
	status.Trusted, status.PreviouslyTrusted = true, false
	manager.checkJdtlsBundleSet()
	return status, nil
}

// checkJdtlsBundleSet announces project-lsp:restart-required when the set of
// loadable jars differs from the last one seen. The first check only records
// the set. Hash results may come from the cache here; loading re-hashes.
func (manager *pluginManager) checkJdtlsBundleSet() {
	if manager == nil {
		return
	}
	paths := loadableJdtlsBundlePaths(manager.jdtlsBundleStatuses(false, true))
	tracker := &manager.jdtlsBundles
	tracker.mu.Lock()
	changed := tracker.known && !slices.Equal(tracker.last, paths)
	tracker.known, tracker.last = true, paths
	tracker.mu.Unlock()
	if changed {
		notifyJdtlsRestartRequired()
	}
}

// jdtlsBundlesContributionsChanged runs on every contributions-changed signal
// (enable, disable, reload, upgrade, uninstall, workspace switch).
func (manager *pluginManager) jdtlsBundlesContributionsChanged(points []string) {
	if len(points) > 0 && !slices.Contains(points, jdtlsBundlePointKey) {
		return
	}
	manager.checkJdtlsBundleSet()
}

type jdtlsBundlesStatusRequest struct {
	ProjectRoot string `json:"projectRoot"`
}

type jdtlsBundlesTrustRequest struct {
	PluginID    string `json:"pluginId"`
	Fingerprint string `json:"fingerprint"`
}

func pluginHostJdtlsBundlesStatusHandler(_ context.Context, _, args string) (string, error) {
	var request jdtlsBundlesStatusRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	status, err := host().jdtlsBundlesStatus(request.ProjectRoot)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(status)
}

func pluginHostJdtlsBundlesTrustHandler(_ context.Context, _, args string) (string, error) {
	var request jdtlsBundlesTrustRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	status, err := host().trustJdtlsBundles(request.PluginID, request.Fingerprint)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(status)
}

func init() {
	core.RegisterCtx("plugin_host_jdtls_bundles_status", pluginHostJdtlsBundlesStatusHandler)
	core.RegisterCtx("plugin_host_jdtls_bundles_trust", pluginHostJdtlsBundlesTrustHandler)
	lsp.SetJdtlsBundleProvider(func() []string { return host().jdtlsBundlePaths() })
	// No record lifecycle hook: it also runs on every plugin stop. Enable,
	// disable, failure, reload, upgrade and development generation changes all
	// end in emitState/emitContributionsChanged, which re-checks the set.
	// The shared hook deletes the trust too (processLaunchTrustPoints); hook
	// order is not defined, so delete before re-checking the set. Uninstall
	// also forgets the plugin's install order; Clear Plugin Data keeps it.
	registerRecordRemovalHook(func(manager *pluginManager, pluginID string, reason removalReason) {
		deleteProcessLaunchTrust(pluginID)
		if reason == removalReasonUninstall {
			forgetJdtlsBundleInstallOrder(pluginID)
		}
		manager.checkJdtlsBundleSet()
	})
}
