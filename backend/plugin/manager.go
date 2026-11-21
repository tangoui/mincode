package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/license"
	"mincode/app/plugins/api"
)

const (
	hostPluginID             = "__host__"
	pluginSourceBuiltin      = "builtin"
	pluginSourceLocal        = "local"
	pluginSourceDevelopment  = "development"
	pluginStateDisabled      = "disabled"
	pluginStateStopped       = "stopped"
	pluginStateStarting      = "starting"
	pluginStateRunning       = "running"
	pluginStateStopping      = "stopping"
	pluginStateError         = "error"
	pluginScopeGlobal        = "global"
	pluginScopeWorkspace     = "workspace"
	pluginStartupTimeout     = 15 * time.Second
	pluginInvokeTimeout      = 30 * time.Second
	pluginShutdownTimeout    = 5 * time.Second
	pluginInstallMaxFiles    = 2_000
	pluginInstallMaxBytes    = 64 * 1024 * 1024
	pluginScopeMaxWorkspaces = 64
)

// Activation timing.
//
// pluginStartupTimeout (above) bounds how long one caller (an invocation, a
// UI session, a startup slot) waits for a plugin to become running; it does
// not bound the activation itself.
const (
	// pluginActivationTimeout bounds one activation once it has started:
	// spawning the process plus plugin.initialize, which process_runtime.go
	// bounds separately with pluginInitializeTimeout (60s, sized for the macOS
	// first-launch scan of large unsigned binaries). The activation context is
	// the parent of that initialize context, so it must be longer or the 60s
	// budget is silently cut to the caller's 15s and a slow first launch lands
	// in the error state. The 5s margin covers process spawn and pipe setup.
	// Callers still stop waiting after pluginStartupTimeout; the activation
	// keeps running in the background (startRecordValidated) and Shutdown,
	// stop and disable still cancel it.
	pluginActivationTimeout = pluginInitializeTimeout + 5*time.Second
	// defaultStartupActivationDelay postpones cold starts of startup plugins
	// requested by StartupInBackground so spawning processes does not compete
	// with the first screen. On-demand activation (commands, views, AI) is not
	// delayed and a startup plugin activated that way is simply skipped later.
	defaultStartupActivationDelay = 1500 * time.Millisecond
	// defaultStartupActivationConcurrency caps concurrent startup cold starts.
	defaultStartupActivationConcurrency = 2
)

type PluginLogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type pluginRecord struct {
	mu               sync.Mutex
	lifecycleMu      sync.Mutex
	approvalEpoch    uint64
	activationCancel context.CancelFunc
	id               string
	source           string
	root             string
	builtin          api.Plugin
	manifest         *Manifest
	runtime          *processRuntime
	enabled          bool
	// scope 决定插件在所有工作区运行（global）还是仅在 workspaces 列出的
	// 工作区运行（workspace）；持久化在宿主 KV 的 "<id>.scope" 键。
	scope            string
	workspaces       map[string]struct{}
	state            string
	lastError        string
	capabilities     []api.Capability
	logs             []PluginLogEntry
	developmentID    string
	developmentUIURL string
	// In-memory command context reported through host.context.set. Cleared on
	// stop, disable, reload, crash, and uninstall; never persisted.
	contextValues map[string]any
	// inflight counts invocations between admission and reply; idle stop waits for 0.
	inflight atomic.Int32
}

type pluginManager struct {
	operationMu       sync.Mutex
	mu                sync.RWMutex
	records           map[string]*pluginRecord
	order             []string
	uiHost            *pluginUIHost
	idleStops         idleStopTimers
	devBridge         *pluginDevBridge
	shuttingDown      bool
	activationContext context.Context
	cancelActivations context.CancelFunc
	// startupDone is closed once background startup activation finishes; nil
	// until StartupInBackground begins.
	startupDone chan struct{}
	// startupDelay postpones StartupInBackground's activation of startup
	// plugins (newPluginManager sets defaultStartupActivationDelay; zero means
	// no delay). startupConcurrency caps concurrent startup activations
	// (zero means defaultStartupActivationConcurrency). Both exist so tests can
	// inject deterministic values.
	startupDelay       time.Duration
	startupConcurrency int
	// Lifecycle changes join the source under fileEventsMu; mutation callbacks
	// only load its immutable identity and never wait on native cleanup.
	statusEvents          hostStatusEvents
	fileEventsMu          sync.Mutex
	fileEvents            atomic.Pointer[workspaceFileSource]
	fileEventsUnsubscribe func()
	// scopeMu protects publication and worker ownership only, never IO/records.
	scopeMu       sync.Mutex
	scopeState    atomic.Pointer[workspaceScopeState]
	scopeRevision uint64
	scopeClosed   bool
	// scopeDone is closed with scopeClosed (lazily made, under scopeMu) and
	// wakes waiters such as awaitFirstWorkspaceScopeForBundles.
	scopeDone       chan struct{}
	scopeWorker     bool
	scopeWG         sync.WaitGroup
	scopeListeners  map[uint64]func(workspaceScopeSnapshot)
	scopeListenerID uint64
	// Reconciliation bookkeeping is protected by operationMu.
	scopeReconciledRevision  string
	scopeReconciledWorkspace string
	// Records stopped by a reconcile that was superseded before completing;
	// the next completed reconcile restarts them if still effective/startup.
	scopeStoppedUnreconciled map[*pluginRecord]struct{}
	// shortcuts is the global shortcut registry (global_shortcuts.go), set
	// on first use; plugin domain startup starts it.
	shortcuts atomic.Pointer[globalShortcutRegistry]
	// jdtlsBundles remembers the last announced set of loadable jdtls
	// bundle jars (jdtls_bundles_trust.go).
	jdtlsBundles jdtlsBundleTracker
}

var (
	managerOnce sync.Once
	managerInst *pluginManager
)

func builtinHostPlugins() []api.Plugin {
	// Development examples remain in plugins/hello and are not registered in the desktop app.
	return nil
}

func host() *pluginManager {
	managerOnce.Do(func() {
		managerInst = newPluginManager()
	})
	managerInst.publishStatusHost(appLocked)
	return managerInst
}

// PrewarmInBackground builds the plugin manager on a background goroutine
// after the window exists, so the static package scan (manifest validation,
// contribution assets) is not paid by the first plugin IPC. It only performs
// static discovery: no plugin process, built-in lifecycle hook, development
// bridge or schedule runner is started here; startup activation still begins
// with StartupInBackground. host() shares the sync.Once, so a plugin IPC that
// arrives during the scan waits for it instead of scanning twice, and when
// prewarming has not run (or has not reached the Once yet) the first IPC
// builds the manager lazily exactly as before.
func PrewarmInBackground() {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("plugin manager prewarm failed: %v", recovered)
			}
		}()
		host()
	}()
}

func newPluginManager() *pluginManager {
	manager := &pluginManager{
		records:      map[string]*pluginRecord{},
		uiHost:       newPluginUIHost(),
		devBridge:    newPluginDevBridge(),
		startupDelay: defaultStartupActivationDelay,
	}
	manager.wireDirectChannel()
	manager.devBridge.onApproved = manager.attachDevelopmentSession
	manager.devBridge.onDisconnected = manager.detachDevelopmentSession
	manager.devBridge.onGenerationReady = manager.switchDevelopmentGeneration
	for _, builtin := range builtinHostPlugins() {
		manager.registerBuiltin(builtin)
	}
	manager.scanInstalled()
	return manager
}

func pluginsDirectory() string {
	return filepath.Join(userDataDir(), pluginDataDirectory, "plugins")
}

func (manager *pluginManager) registerBuiltin(plugin api.Plugin) {
	if plugin == nil {
		return
	}
	meta := plugin.Meta()
	id := strings.TrimSpace(meta.ID)
	if !pluginIDPattern.MatchString(id) {
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, exists := manager.records[id]; exists {
		return
	}
	capabilities := capabilitiesForBuiltin(plugin)
	enabled, found, err := readPluginEnabled(id)
	if err != nil || !found {
		enabled = true
		_ = writePluginEnabled(id, enabled)
	}
	scope, workspaces := readPluginScope(id)
	state := pluginStateStopped
	if !enabled {
		state = pluginStateDisabled
	}
	manager.records[id] = &pluginRecord{
		id: id, source: pluginSourceBuiltin, builtin: plugin, enabled: enabled,
		scope: scope, workspaces: workspaces, state: state, capabilities: capabilities,
	}
	manager.order = append(manager.order, id)
}

func capabilitiesForBuiltin(plugin api.Plugin) []api.Capability {
	seen := map[api.Capability]struct{}{}
	result := make([]api.Capability, 0)
	if provider, ok := plugin.(api.CapabilityProvider); ok {
		for _, capability := range provider.Capabilities() {
			if !isKnownCapability(capability) {
				continue
			}
			if _, duplicate := seen[capability]; duplicate {
				continue
			}
			seen[capability] = struct{}{}
			result = append(result, capability)
		}
	}
	if _, commandProvider := plugin.(api.CommandProvider); commandProvider {
		if _, exists := seen[api.CapabilityCommands]; !exists {
			result = append(result, api.CapabilityCommands)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func isKnownCapability(capability api.Capability) bool {
	for _, known := range api.CapabilitiesV1() {
		if capability == known {
			return true
		}
	}
	return false
}

func (manager *pluginManager) scanInstalled() {
	root := pluginsDirectory()
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		pluginRoot := filepath.Join(root, entry.Name())
		manifest, manifestErr := readManifest(pluginRoot)
		if manifestErr != nil || manifest.ID != entry.Name() {
			manager.registerInvalidLocal(pluginRoot, manifestErr)
			continue
		}
		manager.registerLocal(pluginRoot, manifest)
	}
}

// Invalid packages remain visible without trusting their manifest or touching user data.
func (manager *pluginManager) registerInvalidLocal(root string, cause error) {
	id := filepath.Base(root)
	if !pluginIDPattern.MatchString(id) {
		return
	}
	if cause == nil {
		cause = errors.New("plugin manifest ID does not match installation directory")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, exists := manager.records[id]; exists {
		return
	}
	manager.records[id] = &pluginRecord{id: id, root: root, source: pluginSourceLocal, state: pluginStateError, lastError: cause.Error()}
	manager.order = append(manager.order, id)
}

func (manager *pluginManager) registerLocal(root string, manifest Manifest) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, exists := manager.records[manifest.ID]; exists {
		return fmt.Errorf("plugin %q already exists", manifest.ID)
	}
	enabled, found, err := readPluginEnabled(manifest.ID)
	if err != nil {
		return err
	}
	if !found {
		enabled = false
		if err := writePluginEnabled(manifest.ID, false); err != nil {
			return err
		}
	}
	scope, workspaces := readPluginScope(manifest.ID)
	if manifestGlobalOnlyReason(&manifest) != "" {
		// Declarative plugins and scope-guarded points (registerScopeGuard)
		// are always global; a persisted workspace scope stays inert.
		scope, workspaces = pluginScopeGlobal, nil
	}
	record := &pluginRecord{
		id: manifest.ID, source: pluginSourceLocal, root: root, manifest: &manifest,
		enabled: enabled, scope: scope, workspaces: workspaces, state: pluginStateDisabled,
		capabilities: append([]api.Capability(nil), manifest.Capabilities...),
	}
	if enabled {
		record.state = pluginStateStopped
	}
	if manifest.isDeclarative() {
		record.logContributionIssuesLocked(manifest)
	} else {
		record.runtime = manager.newRecordRuntime(record, root, manifest)
	}
	manager.records[manifest.ID] = record
	manager.order = append(manager.order, manifest.ID)
	return nil
}

// Runtime implementations must call the generation-aware handler outside their mutex.

func (manager *pluginManager) attachDevelopmentSession(session pluginDevSession) error {
	manifest, err := readManifest(session.PackageRoot)
	if err != nil {
		return err
	}
	// Development plugins attach already enabled, so they count toward the same cap.
	if quota, plan, limited := enabledPluginQuota(); limited {
		if err := manager.admitEnabledPlugin(nil, quota, plan); err != nil {
			return err
		}
	}
	manager.mu.Lock()
	if existing := manager.records[manifest.ID]; existing != nil {
		manager.mu.Unlock()
		return fmt.Errorf("plugin %q is already registered", manifest.ID)
	}
	// Development session plugins are transient and always workspace-global.
	record := &pluginRecord{
		id: manifest.ID, source: pluginSourceDevelopment, root: session.PackageRoot, manifest: &manifest,
		enabled: true, scope: pluginScopeGlobal, state: pluginStateStopped, capabilities: append([]api.Capability(nil), manifest.Capabilities...),
		developmentID: session.ID, developmentUIURL: session.UIURL,
	}
	if manifest.isDeclarative() {
		// Nothing to run: registering the record exposes its contributions through
		// plugin_host_contributions, and the state event below makes the frontend refresh.
		record.logContributionIssuesLocked(manifest)
	} else {
		record.runtime = manager.newRecordRuntime(record, session.PackageRoot, manifest)
	}
	manager.records[manifest.ID] = record
	manager.order = append(manager.order, manifest.ID)
	manager.mu.Unlock()
	manager.emitState(record)
	manager.syncWorkspaceFileEvents()
	return nil
}

func (manager *pluginManager) detachDevelopmentSession(session pluginDevSession) {
	manager.mu.Lock()
	record := manager.records[session.PluginID]
	if record == nil || record.source != pluginSourceDevelopment || record.developmentID != session.ID {
		manager.mu.Unlock()
		return
	}
	delete(manager.records, record.id)
	manager.order = slices.DeleteFunc(manager.order, func(id string) bool { return id == record.id })
	manager.mu.Unlock()
	manager.revokePluginUI(record.id)
	ctx, cancel := context.WithTimeout(context.Background(), pluginShutdownTimeout)
	defer cancel()
	_ = manager.stopRecord(ctx, record, false)
	// The record is gone but was stopped, not disabled: the lifecycle hook
	// keeps tray groups across a normal stop, so drop it explicitly.
	trayForgetPlugin(record.id)
	manager.emitContributionsChanged(record.id, nil)
}

func (manager *pluginManager) switchDevelopmentGeneration(session pluginDevSession, generationRoot string, _ uint64) error {
	preview, err := inspectPackageDirectory(generationRoot)
	if err != nil {
		return fmt.Errorf("inspect development generation: %w", err)
	}
	manifest := preview.Manifest
	if manifest.ID != session.PluginID {
		return errors.New("development generation plugin ID changed")
	}
	manager.mu.RLock()
	record := manager.records[session.PluginID]
	manager.mu.RUnlock()
	if record == nil || record.source != pluginSourceDevelopment || record.developmentID != session.ID {
		return errors.New("development plugin is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), pluginStartupTimeout)
	defer cancel()
	if err := lockPluginLifecycle(ctx, record); err != nil {
		return err
	}
	defer record.lifecycleMu.Unlock()
	record.mu.Lock()
	currentManifest := *record.manifest
	record.mu.Unlock()
	if currentManifest.isDeclarative() != manifest.isDeclarative() {
		return errors.New("development generation changed runtime.type; restart the development session")
	}
	if manifest.isDeclarative() {
		// Data-only reload: swap the package root and contributions, then announce
		// the state change so the frontend re-reads plugin_host_contributions.
		record.mu.Lock()
		record.root = generationRoot
		record.manifest = &manifest
		record.lastError = ""
		record.appendLogLocked("info", fmt.Sprintf("reloaded %d keymap preset(s) from development generation", len(manifest.KeymapPresets())))
		record.logContributionIssuesLocked(manifest)
		record.mu.Unlock()
		manager.emitState(record)
		manager.syncWorkspaceFileEvents()
		return nil
	}
	if developmentManifestExpandsPermission(currentManifest, manifest) {
		return errors.New("development generation expands permissions and requires a new approved session")
	}
	candidate := manager.newRecordRuntime(record, generationRoot, manifest)
	if err := candidate.Start(ctx); err != nil {
		return fmt.Errorf("start development generation: %w", err)
	}
	record.mu.Lock()
	previous := record.runtime
	record.root = generationRoot
	record.manifest = &manifest
	record.capabilities = append([]api.Capability(nil), manifest.Capabilities...)
	record.runtime = candidate
	record.state = pluginStateRunning
	record.lastError = ""
	record.mu.Unlock()
	manager.revokePluginUI(record.id)
	if previous != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), pluginShutdownTimeout)
		_ = previous.Stop(stopCtx)
		stopCancel()
	}
	manager.emitState(record)
	manager.syncWorkspaceFileEvents()
	return nil
}

func developmentManifestExpandsPermission(current, candidate Manifest) bool {
	capabilities := map[api.Capability]struct{}{}
	for _, capability := range current.Capabilities {
		capabilities[capability] = struct{}{}
	}
	for _, capability := range candidate.Capabilities {
		if _, approved := capabilities[capability]; !approved {
			return true
		}
	}
	if current.Runtime.Entry != candidate.Runtime.Entry || !mapsEqual(current.Runtime.Entrypoints, candidate.Runtime.Entrypoints) || !slices.Equal(current.Runtime.Args, candidate.Runtime.Args) {
		return true
	}
	actions := map[string]ManifestUIAction{}
	for _, action := range current.Contributes.Actions {
		actions[action.ID] = action
	}
	for _, action := range candidate.Contributes.Actions {
		approved, exists := actions[action.ID]
		if (action.UICallable || action.AICallable) && (!exists || (!approved.UICallable && action.UICallable) ||
			(!approved.AICallable && action.AICallable) || riskRank(action.Risk) > riskRank(approved.Risk) ||
			(approved.ResultHandling == "" && action.ResultHandling != "")) {
			return true
		}
		approvedPermissions := map[api.Capability]struct{}{}
		for _, permission := range approved.Permissions {
			approvedPermissions[permission] = struct{}{}
		}
		for _, permission := range action.Permissions {
			if _, exists := approvedPermissions[permission]; !exists {
				return true
			}
		}
	}
	return contributionsExpandPermission(current, candidate)
}

func mapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func riskRank(risk string) int {
	switch risk {
	case "dangerous":
		return 4
	case "write":
		return 3
	case "network":
		return 2
	default:
		return 1
	}
}

func (manager *pluginManager) newRecordRuntime(record *pluginRecord, root string, manifest Manifest) *processRuntime {
	runtimeHost := newProcessRuntime(root, manifest, record.appendLog, nil)
	// source is fixed for the record's lifetime. Use the candidate manifest,
	// not record.manifest: development reload builds before swapping the record.
	runtimeHost.callerMetadata = freezeRuntimeCallerMetadata(record.source, manifest.Version)
	runtimeHost.ownerManager, runtimeHost.ownerRecord = manager, record
	runtimeHost.SetExitHandler(func(generation uint64, err error) {
		manager.recordRuntimeExited(record, runtimeHost, generation, err)
	})
	runtimeHost.contextSet = func(key string, value any) error {
		return manager.setRecordContext(record, key, value)
	}
	return runtimeHost
}

func (manager *pluginManager) recordRuntimeExited(record *pluginRecord, runtimeHost *processRuntime, generation uint64, err error) {
	record.mu.Lock()
	if record.runtime != runtimeHost || runtimeHost.Generation() != generation ||
		(record.state != pluginStateStarting && record.state != pluginStateRunning) {
		record.mu.Unlock()
		return
	}
	record.approvalEpoch++
	record.state = pluginStateError
	record.lastError = redactPluginMessage(record.id, err.Error())
	record.mu.Unlock()
	manager.clearRecordContext(record)
	manager.revokePluginUI(record.id)
	manager.emitState(record)
	manager.syncWorkspaceFileEvents()
}

// Admission failure never claims a graceful stop or kills a later generation.
func (manager *pluginManager) forceStopRecord(record *pluginRecord, cause error) error {
	record.mu.Lock()
	record.approvalEpoch++
	if record.activationCancel != nil {
		record.activationCancel()
	}
	runtimeHost := record.runtime
	var generation uint64
	if runtimeHost != nil {
		generation = runtimeHost.Generation()
	}
	record.state = pluginStateError
	record.lastError = fmt.Sprintf("plugin stop admission failed: %v", cause)
	record.mu.Unlock()
	manager.clearRecordContext(record)
	manager.revokePluginUI(record.id)
	defer manager.emitState(record)
	manager.syncWorkspaceFileEvents()
	if runtimeHost == nil {
		return cause
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), pluginShutdownTimeout)
	defer cancel()
	return errors.Join(cause, runtimeHost.ForceStopGeneration(cleanupCtx, generation))
}

func readPluginEnabled(id string) (bool, bool, error) {
	raw, found, err := pluginKVGetRaw(hostPluginID, id)
	if err != nil || !found {
		return false, found, err
	}
	var enabled bool
	if err := json.Unmarshal(raw, &enabled); err != nil {
		return false, true, fmt.Errorf("read plugin enabled state: %w", err)
	}
	return enabled, true, nil
}

func writePluginEnabled(id string, enabled bool) error {
	value, _ := json.Marshal(enabled)
	return pluginKVSetRaw(hostPluginID, id, value)
}

// pluginScopeState 是宿主 KV 中 "<id>.scope" 键的持久化结构。
type pluginScopeState struct {
	Scope      string   `json:"scope"`
	Workspaces []string `json:"workspaces,omitempty"`
}

func pluginScopeKVKey(id string) string {
	return id + ".scope"
}

// readPluginScope 读取插件的工作区作用域；缺失或损坏时回退为全局作用域。
func readPluginScope(id string) (string, map[string]struct{}) {
	raw, found, err := pluginKVGetRaw(hostPluginID, pluginScopeKVKey(id))
	if err != nil || !found {
		return pluginScopeGlobal, nil
	}
	var stored pluginScopeState
	if err := json.Unmarshal(raw, &stored); err != nil {
		return pluginScopeGlobal, nil
	}
	return normalizePluginScope(stored.Scope, stored.Workspaces)
}

func writePluginScope(id, scope string, workspaces map[string]struct{}) error {
	stored := pluginScopeState{Scope: scope}
	if scope == pluginScopeWorkspace {
		stored.Workspaces = sortedWorkspaceIDs(workspaces)
	}
	value, _ := json.Marshal(stored)
	return pluginKVSetRaw(hostPluginID, pluginScopeKVKey(id), value)
}

// normalizePluginScope 校验并归一化作用域与工作区列表；非法作用域回退为全局。
func normalizePluginScope(scope string, workspaces []string) (string, map[string]struct{}) {
	if scope != pluginScopeWorkspace {
		return pluginScopeGlobal, nil
	}
	set := make(map[string]struct{}, min(len(workspaces), pluginScopeMaxWorkspaces))
	for _, workspace := range workspaces {
		if len(set) >= pluginScopeMaxWorkspaces {
			break
		}
		if workspace = strings.TrimSpace(workspace); workspace != "" {
			set[workspace] = struct{}{}
		}
	}
	return pluginScopeWorkspace, set
}

func sortedWorkspaceIDs(workspaces map[string]struct{}) []string {
	ids := make([]string, 0, len(workspaces))
	for id := range workspaces {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (manager *pluginManager) activeWorkspaceID() string {
	return manager.workspaceScope().WorkspaceID
}

// workspaceActiveLocked 判断插件在当前活动工作区是否生效；调用方须持有 record.mu。
func (manager *pluginManager) workspaceActiveLocked(record *pluginRecord) bool {
	if !record.enabled {
		return false
	}
	if record.scope != pluginScopeWorkspace {
		return true
	}
	_, ok := record.workspaces[manager.activeWorkspaceID()]
	return ok
}

// contributionsAvailableLocked 在静态贡献可用性上叠加活动工作区过滤；调用方须持有 record.mu。
func (manager *pluginManager) contributionsAvailableLocked(record *pluginRecord) bool {
	return record.contributionsAvailableLocked() && manager.workspaceActiveLocked(record)
}

func (record *pluginRecord) appendLog(level, message string) {
	record.mu.Lock()
	defer record.mu.Unlock()
	record.appendLogLocked(level, message)
}

func (record *pluginRecord) appendLogLocked(level, message string) {
	entry := PluginLogEntry{Time: time.Now(), Level: strings.TrimSpace(level), Message: truncateLog(redactPluginMessage(record.id, message))}
	record.logs = append(record.logs, entry)
	if len(record.logs) > pluginLogLimit {
		record.logs = append([]PluginLogEntry(nil), record.logs[len(record.logs)-pluginLogLimit:]...)
	}
}

// setRecordContext stores one plugin-reported command context value.
func (manager *pluginManager) setRecordContext(record *pluginRecord, key string, value any) error {
	key = strings.TrimSpace(key)
	if !pluginContextKeyPattern.MatchString(key) {
		return errors.New("context key must be a lowercase identifier of at most 64 characters")
	}
	if err := validateContextValue(value); err != nil {
		return err
	}
	record.mu.Lock()
	if record.contextValues == nil {
		record.contextValues = map[string]any{}
	}
	if _, exists := record.contextValues[key]; !exists && len(record.contextValues) >= pluginContextMaxKeys {
		record.mu.Unlock()
		return errors.New("context key limit reached")
	}
	record.contextValues[key] = value
	record.mu.Unlock()
	core.Emit(pluginContextChangedEvent, map[string]any{"id": record.id})
	return nil
}

// clearRecordContext drops the in-memory command context on any lifecycle
// transition that ends plugin-provided state.
func (manager *pluginManager) clearRecordContext(record *pluginRecord) {
	record.mu.Lock()
	hadValues := len(record.contextValues) > 0
	record.contextValues = nil
	record.mu.Unlock()
	if hadValues {
		core.Emit(pluginContextChangedEvent, map[string]any{"id": record.id})
	}
	manager.runRecordLifecycleHooks(record)
}

// Contexts returns a snapshot of every plugin's command context keyed by plugin ID.
func (manager *pluginManager) Contexts() map[string]map[string]any {
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	contexts := map[string]map[string]any{}
	for _, record := range records {
		record.mu.Lock()
		if len(record.contextValues) > 0 {
			snapshot := make(map[string]any, len(record.contextValues))
			for key, value := range record.contextValues {
				snapshot[key] = value
			}
			contexts[record.id] = snapshot
		}
		record.mu.Unlock()
	}
	return contexts
}

func (manager *pluginManager) Startup(ctx context.Context) error {
	startupClearServiceExchange()
	if err := manager.startDevBridge(); err != nil {
		return err
	}
	manager.ensureScheduleRunner()
	manager.startGlobalShortcuts()
	return manager.activateStartupRecords(ctx)
}

// pluginStartupCompletedEvent 在后台启动激活结束后发出，前端据此刷新插件视图/命令。
const pluginStartupCompletedEvent = "plugin:startup-completed"

// StartupInBackground starts the development bridge synchronously, then
// activates built-ins and startup plugins without blocking the caller. Catalog
// commands can run meanwhile because local plugins expose static contributions
// while stopped/starting; ensureRunning serializes on the record lifecycle lock
// and built-in admission waits for startupDone.
//
// Activation waits startupDelay first (so cold starts do not compete with the
// first screen) and then runs at most startupConcurrency activations at once.
// timeout starts after the delay and bounds the whole batch, including time a
// plugin spends queued for a slot. Shutdown during the delay skips activation.
// Startup plugins activated on demand in the meantime (or by a workspace
// switch, which is not delayed) are already running and are left alone.
func (manager *pluginManager) StartupInBackground(timeout time.Duration, onDone func(error)) error {
	startupClearServiceExchange()
	if err := manager.startDevBridge(); err != nil {
		return err
	}
	manager.ensureScheduleRunner()
	manager.startGlobalShortcuts()
	done := make(chan struct{})
	manager.mu.Lock()
	manager.startupDone = done
	delay := manager.startupDelay
	stopped := manager.activationContextLocked()
	manager.mu.Unlock()
	go func() {
		var err error
		if waitStartupDelay(stopped, delay) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			err = manager.activateStartupRecords(ctx)
			cancel()
		}
		close(done)
		core.Emit(pluginStartupCompletedEvent, map[string]any{})
		if onDone != nil {
			onDone(err)
		}
	}()
	return nil
}

// waitStartupDelay waits delay unless stopped is canceled first (Shutdown);
// it reports whether startup activation should proceed.
func waitStartupDelay(stopped context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return stopped.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return stopped.Err() == nil
	case <-stopped.Done():
		return false
	}
}

// activationContextLocked returns the manager-wide activation context that
// Shutdown cancels, creating it on first use. Callers hold manager.mu.
func (manager *pluginManager) activationContextLocked() context.Context {
	if manager.activationContext == nil {
		manager.activationContext, manager.cancelActivations = context.WithCancel(context.Background())
	}
	return manager.activationContext
}

// awaitStartup blocks until background startup activation finishes, if it is in progress.
func (manager *pluginManager) awaitStartup(ctx context.Context) error {
	manager.mu.RLock()
	done := manager.startupDone
	manager.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *pluginManager) startDevBridge() error {
	if manager.devBridge != nil {
		if err := manager.devBridge.Start(); err != nil {
			return fmt.Errorf("start plugin development bridge: %w", err)
		}
	}
	return nil
}

// activateStartupRecords starts built-ins and effective startup plugins with
// at most startupConcurrency cold starts in flight. A slot is held while the
// caller waits for startRecord (bounded by pluginStartupTimeout); a plugin
// still queued when ctx ends is reported as failed for this batch but is not
// put into the error state and stays lazily activatable.
func (manager *pluginManager) activateStartupRecords(ctx context.Context) error {
	manager.mu.Lock()
	if manager.shuttingDown {
		manager.mu.Unlock()
		return nil
	}
	records := manager.orderedRecordsLocked()
	limit := manager.startupConcurrency
	manager.mu.Unlock()
	if limit <= 0 {
		limit = defaultStartupActivationConcurrency
	}
	slots := make(chan struct{}, limit)

	type startupResult struct {
		id  string
		err error
	}
	results := make(chan startupResult, len(records))
	started := 0
	for _, record := range records {
		record.mu.Lock()
		start := (record.builtin != nil || (record.manifest != nil && record.manifest.Runtime.Activation == pluginActivationStartup)) &&
			manager.workspaceActiveLocked(record)
		record.mu.Unlock()
		if start {
			started++
			go func(record *pluginRecord) {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					results <- startupResult{id: record.id, err: ctx.Err()}
					return
				}
				defer func() { <-slots }()
				startCtx, cancel := context.WithTimeout(ctx, pluginStartupTimeout)
				defer cancel()
				results <- startupResult{id: record.id, err: manager.startRecord(startCtx, record)}
			}(record)
		}
	}

	failures := make([]error, 0)
	for range started {
		result := <-results
		if result.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", result.id, result.err))
		}
	}
	return errors.Join(failures...)
}

func (manager *pluginManager) Shutdown(ctx context.Context) {
	if registry := manager.shortcuts.Load(); registry != nil {
		registry.stop()
	}
	shutdownPluginDevelopmentRunner(ctx)
	manager.mu.Lock()
	manager.shuttingDown = true
	if manager.cancelActivations != nil {
		manager.cancelActivations()
	}
	manager.cancelIdleStops()
	records := manager.orderedRecordsLocked()
	manager.mu.Unlock()
	manager.closeWorkspaceScope()
	manager.closeWorkspaceFileEvents()
	for i := len(records) - 1; i >= 0; i-- {
		stopCtx, cancel := context.WithTimeout(ctx, pluginShutdownTimeout)
		_ = manager.stopRecord(stopCtx, records[i], false)
		cancel()
	}
	if manager.uiHost != nil {
		manager.uiHost.Shutdown()
	}
	if manager.devBridge != nil {
		manager.devBridge.Close()
	}
}

func (manager *pluginManager) orderedRecordsLocked() []*pluginRecord {
	records := make([]*pluginRecord, 0, len(manager.order))
	for _, id := range manager.order {
		if record := manager.records[id]; record != nil {
			records = append(records, record)
		}
	}
	return records
}

func (manager *pluginManager) record(id string) (*pluginRecord, error) {
	manager.mu.RLock()
	record := manager.records[strings.TrimSpace(id)]
	manager.mu.RUnlock()
	if record == nil {
		return nil, fmt.Errorf("plugin %q not found", id)
	}
	return record, nil
}

// lockPluginLifecycle bounds admission without spawning abandoned lock waiters.
//
// It polls TryLock every 5ms instead of using a condition variable. A
// sync.Cond cannot be combined with context cancellation, and a channel-based
// lock would have to replace record.lifecycleMu in every holder (stop,
// upgrade, UI sessions, development generations), each of which would need a
// matching wake-up on unlock. Contention only happens while a start or stop is
// in progress, which costs milliseconds to seconds, so the up-to-5ms wake-up
// latency is noise; the hot path (an already running plugin) no longer reaches
// this function at all (startRecordValidated's fast path).
func lockPluginLifecycle(ctx context.Context, record *pluginRecord) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if record.lifecycleMu.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// contributionsAvailableLocked includes static declarations without executing local code.
// 开发会话插件与本地安装插件一样，停止/启动中也暴露静态贡献，以保证打开视图、命令与 AI 动作校验能先触发懒激活。
func (record *pluginRecord) contributionsAvailableLocked() bool {
	return record.enabled && (record.state == pluginStateRunning ||
		((record.source == pluginSourceLocal || record.source == pluginSourceDevelopment) &&
			record.manifest != nil && record.runtime != nil &&
			(record.state == pluginStateStopped || record.state == pluginStateStarting)))
}

// ensureRunning is shared by invocation, UI and task admission. It never enables
// plugins or retries a failed activation; recovery requires explicit Reload.
func (manager *pluginManager) ensureRunning(ctx context.Context, id string) error {
	record, err := manager.record(id)
	if err != nil {
		return err
	}
	builtinStopped := func() bool {
		record.mu.Lock()
		defer record.mu.Unlock()
		return record.builtin != nil && record.state != pluginStateRunning
	}
	if builtinStopped() {
		if err := manager.awaitStartup(ctx); err != nil {
			return err
		}
	}
	if builtinStopped() {
		return fmt.Errorf("plugin %q is not running", id)
	}
	return manager.startRecord(ctx, record)
}

func (manager *pluginManager) startRecord(ctx context.Context, record *pluginRecord) error {
	return manager.startRecordValidated(ctx, record, nil)
}

// runningAdmission is startRecordValidated's fast path. When the plugin is
// already running and still admissible it returns (validate's result, true)
// without the lifecycle lock, the 5ms lock polling or the exclusive
// manager.mu. It performs the same checks the slow path applies before
// returning nil for a running plugin: the manager is not shutting down and
// still owns record, validate passes (it carries caller-specific checks such
// as session generations and approval epochs), the plugin is not
// declarative, has a runtime when local, is enabled, effective in the active
// workspace and not in the error state. The record checks run under
// record.mu, the lock every state transition holds. A stop that starts right
// after this check races exactly as it does after the slow path returns.
func (manager *pluginManager) runningAdmission(record *pluginRecord, validate func(*pluginRecord) error) (error, bool) {
	manager.mu.RLock()
	available := !manager.shuttingDown && manager.records[record.id] == record
	manager.mu.RUnlock()
	if !available {
		return nil, false
	}
	// A lifecycle operation in progress (development generation swap, stop,
	// reload, upgrade) keeps the state "running" while it replaces or retires
	// the runtime. Take the slow path then, so the caller waits for it instead
	// of being handed a runtime that is about to stop. TryLock never blocks;
	// the lock order lifecycleMu -> record.mu matches the slow path.
	if !record.lifecycleMu.TryLock() {
		return nil, false
	}
	defer record.lifecycleMu.Unlock()
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.state != pluginStateRunning || !record.enabled || record.declarativeLocked() ||
		(record.source == pluginSourceLocal && record.runtime == nil) || !manager.workspaceActiveLocked(record) {
		return nil, false
	}
	if validate != nil {
		if err := validate(record); err != nil {
			return err, true
		}
	}
	return nil, true
}

func (manager *pluginManager) startRecordValidated(ctx context.Context, record *pluginRecord, validate func(*pluginRecord) error) error {
	if err, handled := manager.runningAdmission(record, validate); handled {
		return err
	}
	record.mu.Lock()
	running := record.state == pluginStateRunning
	record.mu.Unlock()
	// Only marketplace plugins flagged by the cloud reach the plan lookup; it runs
	// before the lifecycle lock and is bounded by the cloud HTTP timeout.
	if !running {
		if err := admitPaidHostPlugin(record.id); err != nil {
			return err
		}
	}
	callerCtx, callerCancel := context.WithTimeout(ctx, pluginStartupTimeout)
	defer callerCancel()
	if err := lockPluginLifecycle(callerCtx, record); err != nil {
		return err
	}
	// Activation outlives the request that triggered it: a canceled hover or a
	// short per-call timeout (languageFeatures 2s) only stops that caller from
	// waiting and must not abort startup and strand the plugin in the error
	// state. Shutdown and stop/disable still cancel it through activationContext
	// and record.activationCancel. The activation is bounded by
	// pluginActivationTimeout (longer than pluginInitializeTimeout), while this
	// caller still waits at most pluginStartupTimeout (callerCtx).
	activation, cancel := context.WithTimeout(context.WithoutCancel(ctx), pluginActivationTimeout)
	handedOff := false
	defer func() {
		if !handedOff {
			cancel()
			record.lifecycleMu.Unlock()
		}
	}()
	manager.mu.Lock()
	unavailable := manager.shuttingDown || manager.records[record.id] != record
	activationContext := manager.activationContextLocked()
	manager.mu.Unlock()
	if unavailable {
		return errors.New("plugin manager is shutting down or plugin was removed")
	}
	stopCancellation := context.AfterFunc(activationContext, cancel)
	defer func() {
		if !handedOff {
			stopCancellation()
		}
	}()

	record.mu.Lock()
	if validate != nil {
		if err := validate(record); err != nil {
			record.mu.Unlock()
			return err
		}
	}
	if record.declarativeLocked() {
		record.mu.Unlock()
		return fmt.Errorf("plugin %q is declarative and has no runtime to start", record.id)
	}
	if record.source == pluginSourceLocal && record.runtime == nil {
		record.mu.Unlock()
		return errors.New("invalid plugin package cannot be started; reinstall a valid plugin.json package")
	}
	if !record.enabled {
		record.mu.Unlock()
		return fmt.Errorf("plugin %q is disabled", record.id)
	}
	if !manager.workspaceActiveLocked(record) {
		record.mu.Unlock()
		return fmt.Errorf("plugin %q is not enabled in the active workspace", record.id)
	}
	if record.state == pluginStateError {
		err := fmt.Errorf("plugin %q failed; reload explicitly to recover: %s", record.id, record.lastError)
		record.mu.Unlock()
		return err
	}
	if record.state == pluginStateRunning {
		record.mu.Unlock()
		return nil
	}
	record.activationCancel = cancel
	record.state = pluginStateStarting
	record.lastError = ""
	record.appendLogLocked("info", "starting plugin")
	runtimeHost := record.runtime
	builtin := record.builtin
	record.mu.Unlock()

	// The activation goroutine now owns the lifecycle lock and cancel func.
	handedOff = true
	done := make(chan error, 1)
	go func() {
		err := manager.runActivation(activation, record, runtimeHost, builtin)
		stopCancellation()
		cancel()
		record.lifecycleMu.Unlock()
		if err != nil {
			// A failed activation leaves the plugin in the error state without
			// running the lifecycle hooks; its ACP agents still disconnect.
			stopPluginAcpAgents(record.id)
		}
		manager.emitState(record)
		manager.syncWorkspaceFileEvents()
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-callerCtx.Done():
		return callerCtx.Err()
	}
}

func (manager *pluginManager) runActivation(ctx context.Context, record *pluginRecord, runtimeHost *processRuntime, builtin api.Plugin) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("plugin startup panic: %v", recovered)
		}
		record.mu.Lock()
		defer record.mu.Unlock()
		record.activationCancel = nil
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			record.state = pluginStateError
			record.lastError = err.Error()
			record.appendLogLocked("error", err.Error())
		} else if record.state == pluginStateError {
			// An exit callback may have won the race with successful initialization.
			// Preserve that failure instead of resurrecting the exited process.
			err = fmt.Errorf("plugin exited during startup: %s", record.lastError)
		} else {
			record.state = pluginStateRunning
			record.appendLogLocked("info", "plugin started")
		}
	}()
	if runtimeHost != nil {
		return runtimeHost.Start(ctx)
	}
	if lifecycle, ok := builtin.(api.LifecyclePlugin); ok {
		return runLifecycleHook(ctx, "startup", lifecycle.OnStartup)
	}
	return nil
}

func (manager *pluginManager) stopRecord(ctx context.Context, record *pluginRecord, disabling bool) (err error) {
	ctx, cancel := context.WithTimeout(ctx, pluginShutdownTimeout)
	defer cancel()
	if err := lockPluginLifecycle(ctx, record); err != nil {
		return manager.forceStopRecord(record, err)
	}
	defer func() {
		record.lifecycleMu.Unlock()
		manager.emitState(record)
		manager.syncWorkspaceFileEvents()
	}()
	return manager.stopRecordLocked(ctx, record, disabling)
}

func (manager *pluginManager) stopRecordLocked(ctx context.Context, record *pluginRecord, disabling bool) (err error) {
	manager.revokePluginUI(record.id)

	record.mu.Lock()
	record.approvalEpoch++
	if record.state != pluginStateRunning && record.state != pluginStateError && record.state != pluginStateStarting {
		if disabling {
			record.state = pluginStateDisabled
		}
		record.mu.Unlock()
		if disabling {
			// A plugin that was not running still ends its host-side state
			// on disable (registerRecordLifecycleHook promises "disable").
			manager.runRecordLifecycleHooks(record)
		}
		return nil
	}
	record.state = pluginStateStopping
	record.appendLogLocked("info", "stopping plugin")
	runtimeHost := record.runtime
	builtin := record.builtin
	record.mu.Unlock()

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("plugin shutdown panic: %v", recovered)
		}
		manager.clearRecordContext(record)
		if err != nil {
			// The lifecycle hooks ran before the error state is set below; ACP
			// agents disconnect on error, so stop them (after record.mu is
			// released).
			defer stopPluginAcpAgents(record.id)
		}
		record.mu.Lock()
		defer record.mu.Unlock()
		if err != nil {
			record.state = pluginStateError
			record.lastError = err.Error()
			record.appendLogLocked("error", err.Error())
			return
		}
		if disabling || !record.enabled {
			record.state = pluginStateDisabled
		} else {
			record.state = pluginStateStopped
		}
		record.lastError = ""
		record.appendLogLocked("info", "plugin stopped")
	}()
	if runtimeHost != nil {
		return runtimeHost.Stop(ctx)
	}
	if lifecycle, ok := builtin.(api.LifecyclePlugin); ok {
		return runLifecycleHook(ctx, "shutdown", lifecycle.OnShutdown)
	}
	return nil
}

func runLifecycleHook(ctx context.Context, operation string, hook func(context.Context) error) error {
	result := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				result <- fmt.Errorf("plugin %s panic: %v", operation, recovered)
			}
		}()
		result <- hook(ctx)
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("plugin %s timed out: %w", operation, ctx.Err())
	}
}

func (manager *pluginManager) SetEnabled(ctx context.Context, id string, enabled bool) error {
	var quota license.QuotaPolicy
	var plan string
	limited := false
	if enabled {
		if err := admitPaidHostPlugin(id); err != nil {
			return err
		}
		quota, plan, limited = enabledPluginQuota()
	}
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	record, err := manager.record(id)
	if err != nil {
		return err
	}
	if limited {
		if err := manager.admitEnabledPlugin(record, quota, plan); err != nil {
			return err
		}
	}
	record.mu.Lock()
	// Declarative plugins have no runtime by design; only a missing manifest marks an invalid package.
	if enabled && record.source == pluginSourceLocal && record.runtime == nil && !record.declarativeLocked() {
		record.mu.Unlock()
		return errors.New("invalid plugin package cannot be enabled; reinstall a valid plugin.json package")
	}
	if enabled && record.state == pluginStateError {
		record.mu.Unlock()
		return errors.New("plugin failed; reload explicitly to recover")
	}
	wasEnabled := record.enabled
	if !enabled {
		record.approvalEpoch++
	}
	record.enabled = enabled
	record.mu.Unlock()
	if err := writePluginEnabled(record.id, enabled); err != nil {
		record.mu.Lock()
		record.enabled = wasEnabled
		record.mu.Unlock()
		return err
	}
	if enabled {
		record.mu.Lock()
		startup := record.builtin != nil || (record.manifest != nil && record.manifest.Runtime.Activation == pluginActivationStartup)
		active := manager.workspaceActiveLocked(record)
		if record.state == pluginStateDisabled {
			record.state = pluginStateStopped
		}
		record.mu.Unlock()
		if startup && active {
			return manager.startRecord(ctx, record)
		}
		manager.emitState(record)
		manager.syncWorkspaceFileEvents()
		return nil
	}
	// Disabling ends full trust for good; enabling again does not restore it.
	manager.revokeFullTrust(record.id, fullTrustLapseDisabled)
	return manager.stopRecord(ctx, record, true)
}

// SetScope 更新插件的工作区作用域，并按生效变化启动或停止插件进程。
func (manager *pluginManager) SetScope(ctx context.Context, id, scope string, workspaces []string) error {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	record, err := manager.record(id)
	if err != nil {
		return err
	}
	normalizedScope, workspaceSet := normalizePluginScope(scope, workspaces)
	if scope != pluginScopeGlobal && normalizedScope != scope {
		return fmt.Errorf("plugin scope must be %q or %q", pluginScopeGlobal, pluginScopeWorkspace)
	}
	record.mu.Lock()
	if reason := manifestGlobalOnlyReason(record.manifest); reason != "" && normalizedScope != pluginScopeGlobal {
		record.mu.Unlock()
		return errors.New(reason)
	}
	wasActive := manager.workspaceActiveLocked(record)
	previousScope, previousWorkspaces := record.scope, record.workspaces
	record.scope, record.workspaces = normalizedScope, workspaceSet
	nowActive := manager.workspaceActiveLocked(record)
	startup := record.builtin != nil || (record.manifest != nil && record.manifest.Runtime.Activation == pluginActivationStartup)
	record.mu.Unlock()
	if err := writePluginScope(record.id, normalizedScope, workspaceSet); err != nil {
		record.mu.Lock()
		record.scope, record.workspaces = previousScope, previousWorkspaces
		record.mu.Unlock()
		return err
	}
	switch {
	case !wasActive && nowActive && startup:
		return manager.startRecord(ctx, record)
	case wasActive && !nowActive:
		return manager.stopRecord(ctx, record, false)
	default:
		manager.emitState(record)
		manager.syncWorkspaceFileEvents()
		return nil
	}
}

// SetActiveWorkspace 记录当前活动工作区，并 reconcile 所有插件进程：
// 在新工作区不再生效的运行中插件被停止；在新工作区生效且声明 startup 激活
// 的插件被启动。on-demand 插件不随切换启动，命令与视图列表由前端监听
// plugin:workspace-changed 后重新拉取。
func (manager *pluginManager) SetActiveWorkspace(ctx context.Context, workspaceID string) error {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	previous := manager.activeWorkspaceID()
	if previous == strings.TrimSpace(workspaceID) {
		return nil
	}
	// Only internal compatibility callers wait; core uses the asynchronous begin.
	scope, err := manager.beginWorkspaceScope(ctx, workspaceID, false)
	if err != nil {
		return err
	}
	state := manager.scopeState.Load()
	// Internal callers keep their original waiting deadline; native bridge
	// reconciliation instead belongs to the revision, not the short RPC call.
	reconcileCtx, cancel := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(state.ctx, cancel)
	defer func() { stopCancellation(); cancel() }()
	err = manager.reconcileWorkspaceScope(reconcileCtx, manager.scopeReconciledWorkspace, scope)
	return errors.Join(err, ctx.Err())
}

// Caller holds operationMu. Previous is the last reconciled/notified workspace,
// not a desired ID which may have been superseded before it ever ran.
func (manager *pluginManager) reconcileWorkspaceScope(ctx context.Context, previous string, current workspaceScopeSnapshot) error {
	valid := func() bool { return ctx.Err() == nil && manager.scopeMatches(current) }
	if !valid() {
		return staleWorkspaceReconcileError()
	}
	if manager.scopeReconciledRevision == current.Revision {
		return nil
	}
	manager.mu.RLock()
	shuttingDown := manager.shuttingDown
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	if shuttingDown {
		return nil
	}
	// Revision-only changes (same workspace: root change or frontend reload)
	// keep the original event cadence and plugin lifecycle untouched.
	sameWorkspace := manager.scopeReconciledRevision != "" && manager.scopeReconciledWorkspace == current.WorkspaceID
	var toStart, toStop []*pluginRecord
	for _, record := range records {
		record.mu.Lock()
		// Starting stays a transition from the last reconciled workspace, so a
		// first begin cannot bypass StartupInBackground's delay/concurrency and
		// a canceled intermediate B never hides a newly effective C. Stopping
		// still uses actual state (R1).
		wasActive := record.enabled && (record.scope != pluginScopeWorkspace || hasWorkspace(record.workspaces, previous))
		nowActive := record.enabled && (record.scope != pluginScopeWorkspace || hasWorkspace(record.workspaces, current.WorkspaceID))
		startup := record.builtin != nil || (record.manifest != nil && record.manifest.Runtime.Activation == pluginActivationStartup)
		running := record.state == pluginStateRunning || record.state == pluginStateStarting
		startable := record.state == pluginStateStopped || record.state == pluginStateDisabled
		record.mu.Unlock()
		if !nowActive && running {
			toStop = append(toStop, record)
		} else if ((!sameWorkspace && !wasActive) || hasStoppedUnreconciled(manager.scopeStoppedUnreconciled, record)) && nowActive && startup && startable {
			toStart = append(toStart, record)
		}
	}
	var errs []error
	for _, record := range toStop {
		if !valid() {
			return staleWorkspaceReconcileError()
		}
		// Once admitted, finish a bounded native stop even if another begin
		// cancels this reconcile. Passing the canceled revision context into
		// stop admission can strand a built-in in error without OnShutdown.
		if err := manager.stopRecord(context.WithoutCancel(ctx), record, false); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", record.id, err))
		}
		if manager.scopeStoppedUnreconciled == nil {
			manager.scopeStoppedUnreconciled = map[*pluginRecord]struct{}{}
		}
		manager.scopeStoppedUnreconciled[record] = struct{}{}
	}
	for _, record := range toStart {
		if !valid() {
			return staleWorkspaceReconcileError()
		}
		if err := manager.startRecord(ctx, record); err != nil {
			errs = append(errs, fmt.Errorf("start %s: %w", record.id, err))
		}
	}
	if !valid() {
		return staleWorkspaceReconcileError()
	}
	manager.scopeReconciledRevision = current.Revision
	manager.scopeReconciledWorkspace = current.WorkspaceID
	manager.scopeStoppedUnreconciled = nil
	if sameWorkspace {
		return errors.Join(errs...)
	}
	core.Emit(pluginWorkspaceChangedEvent, map[string]any{"workspaceId": current.WorkspaceID})
	manager.emitContributionsChanged("", nil)
	manager.notifyConfigurationWorkspaceSwitch(records, previous, current.WorkspaceID)
	if valid() {
		manager.publishHostEvent(hostEventWorkspaceChanged, map[string]any{"workspaceId": current.WorkspaceID})
	}
	return errors.Join(errs...)
}

func hasStoppedUnreconciled(records map[*pluginRecord]struct{}, record *pluginRecord) bool {
	_, ok := records[record]
	return ok
}

func hasWorkspace(workspaces map[string]struct{}, id string) bool {
	_, ok := workspaces[id]
	return ok
}

func (manager *pluginManager) Reload(ctx context.Context, id string) error {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	record, err := manager.record(id)
	if err != nil {
		return err
	}
	record.mu.Lock()
	enabled := record.enabled
	declarative := record.declarativeLocked()
	record.approvalEpoch++
	record.mu.Unlock()
	if !enabled {
		return fmt.Errorf("plugin %q is disabled", id)
	}
	if declarative {
		// Nothing runs; re-announce so listeners refresh contributed data.
		manager.emitState(record)
		manager.syncWorkspaceFileEvents()
		return nil
	}
	if err := manager.stopRecord(ctx, record, false); err != nil {
		return err
	}
	stopPluginLanguageServers(id, "reload")
	stopPluginDebugSessions(id, "reload")
	stopPluginMcpServers(id)
	stopPluginAcpAgents(id)
	return manager.startRecord(ctx, record)
}

func (manager *pluginManager) Invoke(ctx context.Context, id, action, payload string) (result string, err error) {
	return manager.invokeValidated(ctx, id, action, payload, nil)
}

func (manager *pluginManager) invokeValidated(ctx context.Context, id, action, payload string, validate func(*pluginRecord) error) (result string, err error) {
	ctx, cancel := context.WithTimeout(ctx, pluginInvokeTimeout)
	defer cancel()
	record, err := manager.record(id)
	if err != nil {
		return "", err
	}
	record.inflight.Add(1)
	defer func() {
		record.inflight.Add(-1)
		// Woken only for this call (no view open): stop again once it is quiet.
		if manager.uiHost != nil && manager.uiHost.SessionCount(id) == 0 {
			manager.scheduleIdleStop(id, pluginIdleAfterInvokeDelay)
		}
	}()
	if validate != nil {
		if err := manager.startRecordValidated(ctx, record, validate); err != nil {
			return "", err
		}
	} else if err := manager.ensureRunning(ctx, id); err != nil {
		return "", err
	}
	record.mu.Lock()
	enabled := record.enabled
	state := record.state
	builtin := record.builtin
	runtimeHost := record.runtime
	var generation uint64
	if runtimeHost != nil {
		generation = runtimeHost.Generation()
	}
	if validate != nil {
		if err := validate(record); err != nil {
			record.mu.Unlock()
			return "", err
		}
	}
	record.mu.Unlock()
	if !enabled {
		return "", fmt.Errorf("plugin %q is disabled", id)
	}
	if state != pluginStateRunning {
		return "", fmt.Errorf("plugin %q is not running (state: %s)", id, state)
	}
	invokeCtx, cancel := context.WithTimeout(ctx, pluginInvokeTimeout)
	defer cancel()
	if runtimeHost != nil {
		return runtimeHost.InvokeGeneration(invokeCtx, generation, action, payload)
	}

	type invokeResult struct {
		value string
		err   error
	}
	resultChannel := make(chan invokeResult, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				resultChannel <- invokeResult{err: fmt.Errorf("plugin invoke panic: %v", recovered)}
			}
		}()
		value, invokeErr := builtin.Invoke(invokeCtx, action, payload)
		resultChannel <- invokeResult{value: value, err: invokeErr}
	}()
	select {
	case invocation := <-resultChannel:
		if invocation.err != nil {
			record.appendLog("error", invocation.err.Error())
		}
		return invocation.value, invocation.err
	case <-invokeCtx.Done():
		err := fmt.Errorf("plugin %q invocation timed out: %w", id, invokeCtx.Err())
		record.appendLog("error", err.Error())
		return "", err
	}
}

func (manager *pluginManager) HasCapability(id string, capability api.Capability) bool {
	record, err := manager.record(id)
	if err != nil {
		return false
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if !manager.workspaceActiveLocked(record) || record.state != pluginStateRunning {
		return false
	}
	for _, item := range record.capabilities {
		if item == capability {
			return true
		}
	}
	return false
}

func (manager *pluginManager) Logs(id string) ([]PluginLogEntry, error) {
	record, err := manager.record(id)
	if err != nil {
		return nil, err
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	return append([]PluginLogEntry(nil), record.logs...), nil
}

func (manager *pluginManager) InstallLocal(sourcePath string) (Manifest, error) {
	quota, plan, limited := enabledPluginQuota()
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	source, _, cleanup, err := prepareInstallSource(sourcePath)
	if err != nil {
		return Manifest{}, err
	}
	defer cleanup()
	if _, err := inspectInstallDirectory(source); err != nil {
		return Manifest{}, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect plugin source: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Manifest{}, errors.New("plugin source must be a directory and cannot be a symlink")
	}
	manifest, err := readManifest(source)
	if err != nil {
		return Manifest{}, err
	}
	manager.mu.RLock()
	_, exists := manager.records[manifest.ID]
	manager.mu.RUnlock()
	if exists {
		return Manifest{}, fmt.Errorf("plugin %q already exists", manifest.ID)
	}
	// A new plugin of any kind needs a free enabled-plugin slot, so it is refused
	// before it is written to disk.
	if limited {
		if err := manager.admitEnabledPlugin(nil, quota, plan); err != nil {
			return Manifest{}, err
		}
	}

	root := pluginsDirectory()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create plugins directory: %w", err)
	}
	target := filepath.Join(root, manifest.ID)
	if _, err := os.Lstat(target); err == nil {
		return Manifest{}, fmt.Errorf("plugin %q is already installed", manifest.ID)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, err
	}
	staging, err := os.MkdirTemp(root, ".install-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create plugin staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := copyPluginTree(source, staging); err != nil {
		return Manifest{}, err
	}
	stagedManifest, err := readManifest(staging)
	if err != nil {
		return Manifest{}, err
	}
	if stagedManifest.ID != manifest.ID {
		return Manifest{}, errors.New("plugin manifest changed while installing")
	}
	if err := stagedManifest.contributionIssuesError(); err != nil {
		return Manifest{}, err
	}
	if err := renamePluginDirectory(staging, target); err != nil {
		return Manifest{}, fmt.Errorf("install plugin (close applications holding the package and retry if access remains denied): %w", err)
	}
	// A fresh install never inherits full trust left by an earlier copy.
	manager.revokeFullTrust(stagedManifest.ID, fullTrustLapseUninstalled)
	if err := manager.registerLocal(target, stagedManifest); err != nil {
		_ = os.RemoveAll(target)
		return Manifest{}, err
	}
	if record, recordErr := manager.record(stagedManifest.ID); recordErr == nil {
		manager.emitState(record)
		manager.syncWorkspaceFileEvents()
	}
	return stagedManifest, nil
}

// Commit by rename only: copying into the final directory could expose a partial
// package. Windows scanners may temporarily hold newly copied binaries open.
func renamePluginDirectory(source, target string) error {
	return renamePluginDirectoryWith(source, target, os.Rename, time.Sleep, retryablePluginRenameError)
}

func renamePluginDirectoryWith(source, target string, rename func(string, string) error, wait func(time.Duration), retryable func(error) bool) error {
	const attempts = 8
	for attempt := 0; ; attempt++ {
		err := rename(source, target)
		if err == nil || attempt == attempts-1 || !retryable(err) {
			return err
		}
		// Never replace a destination that appeared while waiting (another host
		// instance is not serialized by this manager's operationMu).
		if _, statErr := os.Lstat(target); statErr == nil {
			return err
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return errors.Join(err, statErr)
		}
		wait(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
}

func copyPluginTree(source, target string) error {
	fileCount := 0
	var totalBytes int64
	filter := newPluginInstallFilter(source)
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin package contains symlink: %s", path)
		}
		if filter.skip(path, entry) {
			return filepath.SkipDir
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("plugin package contains invalid path")
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin package contains unsupported file: %s", path)
		}
		fileCount++
		totalBytes += info.Size()
		if fileCount > pluginInstallMaxFiles || totalBytes > pluginInstallMaxBytes {
			return errors.New("plugin package exceeds the file count or 64 MiB size limit")
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		sourceFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer sourceFile.Close()
		opened, err := sourceFile.Stat()
		if err != nil || !os.SameFile(info, opened) {
			return errors.New("plugin package changed while copying")
		}
		destinationFile, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		copied, copyErr := io.Copy(destinationFile, io.LimitReader(sourceFile, info.Size()+1))
		closeErr := destinationFile.Close()
		if copyErr != nil {
			return copyErr
		}
		if copied != info.Size() {
			return errors.New("plugin package changed while copying")
		}
		return closeErr
	})
}

// Uninstall removes the plugin package and host-owned install state. With
// purgeData false, plugin-owned data (KV, file storage, configuration and saved
// secrets) is kept so reinstalling the same plugin restores it; with purgeData
// true it is deleted as in ClearPluginData.
func (manager *pluginManager) Uninstall(ctx context.Context, id string, purgeData bool) error {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	record, err := manager.record(id)
	if err != nil {
		return err
	}
	record.mu.Lock()
	if record.source != pluginSourceLocal {
		record.mu.Unlock()
		return errors.New("built-in plugins cannot be uninstalled")
	}
	root := record.root
	record.mu.Unlock()
	// Before the plugin turns disabled, so a concurrent read cannot lapse the
	// record as "disabled" first.
	manager.revokeFullTrust(id, fullTrustLapseUninstalled)
	record.mu.Lock()
	record.approvalEpoch++
	record.enabled = false
	record.mu.Unlock()
	if err := manager.stopRecord(ctx, record, true); err != nil {
		return err
	}
	if purgeData {
		if err := manager.purgePluginData(record); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove plugin files: %w", err)
	}
	manager.mu.Lock()
	delete(manager.records, id)
	for index, existingID := range manager.order {
		if existingID == id {
			manager.order = append(manager.order[:index], manager.order[index+1:]...)
			break
		}
	}
	manager.mu.Unlock()
	manager.revokePluginUI(id)
	_ = pluginKVDeleteRaw(hostPluginID, id)
	_ = pluginKVDeleteRaw(hostPluginID, pluginScopeKVKey(id))
	_ = pluginKVDeleteRaw(hostPluginID, aiGrantsKVKey(id))
	_ = pluginKVDeleteRaw(hostPluginID, pluginCallGrantsKVKey(id))
	_ = pluginKVDeleteRaw(hostPluginID, databaseSchemaGrantsKVKey(id))
	revokeDatabaseSchemaRun(id, "")
	_ = pluginKVDeleteRaw(hostPluginID, pluginDangerousActionTrustKey(id))
	manager.revokeFullTrust(id, fullTrustLapseUninstalled) // a grant that raced the uninstall
	_ = clearFullTrustAudit(id)
	manager.revokePluginCallTarget(id)
	manager.runRecordRemovalHooks(id, removalReasonUninstall)
	if purgeData {
		manager.runRecordRemovalHooks(id, removalReasonClearData)
	}
	manager.emitUninstalled(id)
	return nil
}

// purgePluginData deletes plugin-owned file storage, KV, saved secrets and
// configuration. The caller must have stopped the plugin.
func (manager *pluginManager) purgePluginData(record *pluginRecord) error {
	root, err := pluginStorageRoot(record.id)
	if err != nil {
		return err
	}
	pluginStorageMu.Lock()
	if info, statErr := os.Lstat(root); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			pluginStorageMu.Unlock()
			return errors.New("plugin storage root is a symlink")
		}
		err = os.RemoveAll(root)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		err = statErr
	}
	pluginStorageMu.Unlock()
	if err != nil {
		return fmt.Errorf("clear plugin file storage: %w", err)
	}
	if err := pluginKVDeleteNamespace(record.id); err != nil {
		return fmt.Errorf("clear plugin data: %w", err)
	}
	if err := pluginSecretDeleteNamespace(record.id); err != nil {
		return fmt.Errorf("clear plugin secrets: %w", err)
	}
	if err := manager.clearConfiguration(record); err != nil {
		return fmt.Errorf("clear plugin configuration: %w", err)
	}
	return nil
}

// ClearPluginData stops the plugin and deletes its plugin-owned KV namespace
// and file storage (connection profiles, documents, and other persisted state). The plugin
// package, enabled flag, and scope are preserved; the plugin restarts with
// empty data on next activation. Host-owned install records are not plugin
// data and are never touched here.
func (manager *pluginManager) ClearPluginData(ctx context.Context, id string) error {
	manager.operationMu.Lock()
	defer manager.operationMu.Unlock()
	record, err := manager.record(id)
	if err != nil {
		return err
	}
	if err := manager.stopRecord(ctx, record, false); err != nil {
		return err
	}
	if err := manager.purgePluginData(record); err != nil {
		return err
	}
	// Full trust and its audit log are host records about the plugin's data
	// era; clearing the data ends both.
	manager.revokeFullTrust(record.id, fullTrustLapseCleared)
	if err := clearFullTrustAudit(record.id); err != nil {
		return err
	}
	manager.runRecordRemovalHooks(record.id, removalReasonClearData)
	record.mu.Lock()
	record.appendLogLocked("info", "plugin data cleared")
	record.mu.Unlock()
	manager.emitState(record)
	manager.syncWorkspaceFileEvents()
	return nil
}
