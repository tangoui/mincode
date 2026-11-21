package plugin

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
)

func TestPluginScopePersistenceRoundtrip(t *testing.T) {
	useTemporaryPluginData(t)
	scope, workspaces := readPluginScope("runtime.test")
	if scope != pluginScopeGlobal || workspaces != nil {
		t.Fatalf("missing scope must default to global, got %q %v", scope, workspaces)
	}
	if err := writePluginScope("runtime.test", pluginScopeWorkspace, map[string]struct{}{"ws-b": {}, "ws-a": {}}); err != nil {
		t.Fatal(err)
	}
	scope, workspaces = readPluginScope("runtime.test")
	if scope != pluginScopeWorkspace || len(workspaces) != 2 {
		t.Fatalf("scope not persisted: %q %v", scope, workspaces)
	}
	if _, ok := workspaces["ws-a"]; !ok {
		t.Fatal("workspace membership lost")
	}
	if err := writePluginScope("runtime.test", pluginScopeGlobal, nil); err != nil {
		t.Fatal(err)
	}
	scope, workspaces = readPluginScope("runtime.test")
	if scope != pluginScopeGlobal || len(workspaces) != 0 {
		t.Fatalf("global scope must drop workspace list: %q %v", scope, workspaces)
	}
}

func TestNormalizePluginScope(t *testing.T) {
	scope, _ := normalizePluginScope("bogus", []string{"ws-a"})
	if scope != pluginScopeGlobal {
		t.Fatal("invalid scope must fall back to global")
	}
	scope, workspaces := normalizePluginScope(pluginScopeWorkspace, []string{" ws-a ", "", "ws-a", "ws-b"})
	if scope != pluginScopeWorkspace || len(workspaces) != 2 {
		t.Fatalf("workspace list not normalized: %q %v", scope, workspaces)
	}
	oversized := make([]string, 0, pluginScopeMaxWorkspaces+10)
	for range pluginScopeMaxWorkspaces + 10 {
		oversized = append(oversized, strings.Repeat("w", 4))
	}
	_, workspaces = normalizePluginScope(pluginScopeWorkspace, oversized)
	if len(workspaces) != 1 {
		t.Fatal("identical trimmed IDs must deduplicate before the cap")
	}
	unique := make([]string, 0, pluginScopeMaxWorkspaces+10)
	for index := range pluginScopeMaxWorkspaces + 10 {
		unique = append(unique, strings.Repeat("w", index+1))
	}
	_, workspaces = normalizePluginScope(pluginScopeWorkspace, unique)
	if len(workspaces) != pluginScopeMaxWorkspaces {
		t.Fatalf("workspace cap not enforced: %d", len(workspaces))
	}
}

func TestWorkspaceScopeLifecycleReconcile(t *testing.T) {
	useTemporaryPluginData(t)
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	if err := manager.SetScope(context.Background(), record.id, pluginScopeWorkspace, []string{"ws-a"}); err != nil {
		t.Fatal(err)
	}
	// 未通告工作区时，工作区级插件即使在 Startup 也不得启动。
	if err := manager.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if plugin.startupCalls.Load() != 0 {
		t.Fatal("workspace-scoped plugin started without an active workspace")
	}
	if err := manager.SetActiveWorkspace(context.Background(), "ws-b"); err != nil {
		t.Fatal(err)
	}
	if plugin.startupCalls.Load() != 0 {
		t.Fatal("plugin started in a workspace where it is not enabled")
	}
	if err := manager.startRecord(context.Background(), record); err == nil ||
		!strings.Contains(err.Error(), "not enabled in the active workspace") {
		t.Fatalf("inactive workspace activation must be rejected: %v", err)
	}
	if err := manager.SetActiveWorkspace(context.Background(), "ws-a"); err != nil {
		t.Fatal(err)
	}
	if plugin.startupCalls.Load() != 1 || record.state != pluginStateRunning {
		t.Fatalf("plugin not started in its workspace: calls=%d state=%s", plugin.startupCalls.Load(), record.state)
	}
	if err := manager.SetActiveWorkspace(context.Background(), "ws-b"); err != nil {
		t.Fatal(err)
	}
	if plugin.shutdownCalls.Load() != 1 || record.state != pluginStateStopped {
		t.Fatalf("plugin not stopped when leaving its workspace: calls=%d state=%s", plugin.shutdownCalls.Load(), record.state)
	}
	if !record.enabled {
		t.Fatal("workspace switch must not flip the master enabled state")
	}
	if err := manager.SetActiveWorkspace(context.Background(), "ws-a"); err != nil {
		t.Fatal(err)
	}
	if plugin.startupCalls.Load() != 2 || record.state != pluginStateRunning {
		t.Fatalf("plugin not restarted when returning to its workspace: calls=%d state=%s", plugin.startupCalls.Load(), record.state)
	}
}

func TestWorkspaceScopeContributionGating(t *testing.T) {
	useTemporaryPluginData(t)
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	if err := manager.SetScope(context.Background(), record.id, pluginScopeWorkspace, []string{"ws-a"}); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	availableWithoutWorkspace := manager.contributionsAvailableLocked(record)
	record.mu.Unlock()
	if availableWithoutWorkspace {
		t.Fatal("contributions exposed without an active workspace")
	}
	if err := manager.SetActiveWorkspace(context.Background(), "ws-a"); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	available := manager.contributionsAvailableLocked(record)
	record.mu.Unlock()
	if !available {
		t.Fatal("contributions hidden in the plugin's own workspace")
	}
	if err := manager.SetScope(context.Background(), record.id, pluginScopeGlobal, nil); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetActiveWorkspace(context.Background(), "ws-b"); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	available = manager.contributionsAvailableLocked(record)
	record.mu.Unlock()
	if !available {
		t.Fatal("global plugin contributions must not depend on the workspace")
	}
}

func TestSetScopeStopsRunningPluginAndRejectsInvalidScope(t *testing.T) {
	useTemporaryPluginData(t)
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	if err := manager.SetActiveWorkspace(context.Background(), "ws-a"); err != nil {
		t.Fatal(err)
	}
	if err := manager.startRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetScope(context.Background(), record.id, "bogus", nil); err == nil {
		t.Fatal("invalid scope accepted")
	}
	if err := manager.SetScope(context.Background(), record.id, pluginScopeWorkspace, []string{"ws-b"}); err != nil {
		t.Fatal(err)
	}
	if plugin.shutdownCalls.Load() != 1 || record.state != pluginStateStopped {
		t.Fatalf("running plugin not stopped when scope excluded the active workspace: %d %s", plugin.shutdownCalls.Load(), record.state)
	}
	// 在当前工作区不生效时打开主开关也不得启动进程。
	if err := manager.SetEnabled(context.Background(), record.id, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
		t.Fatal(err)
	}
	if plugin.startupCalls.Load() != 1 {
		t.Fatal("enable in an inactive workspace must not start the plugin")
	}
	if record.state != pluginStateStopped {
		t.Fatalf("enabled but inactive plugin must sit stopped, got %s", record.state)
	}
}

type scopeWire struct {
	WorkspaceID string `json:"workspaceId"`
	Root        string `json:"root"`
	Revision    string `json:"revision"`
	Ready       bool   `json:"ready"`
}

func scopeCommand(t *testing.T, command string, args any) scopeWire {
	t.Helper()
	raw, _ := json.Marshal(args)
	result, err := core.Dispatch("plugin_host_workspace_scope_"+command, string(raw))
	if err != nil {
		t.Fatalf("scope %s: %v", command, err)
	}
	var scope scopeWire
	if err := json.Unmarshal([]byte(result), &scope); err != nil {
		t.Fatal(err)
	}
	return scope
}

// Missing ticket validation permits a delayed root from A to attach to B.
func TestWorkspaceScopeOutOfOrder(t *testing.T) {
	useScopeRPCManager(t)
	a := scopeCommand(t, "begin", map[string]string{"workspaceId": "A"})
	b := scopeCommand(t, "begin", map[string]string{"workspaceId": "B"})
	root := t.TempDir()
	raw, _ := json.Marshal(map[string]string{"workspaceId": "A", "expectedRevision": a.Revision, "root": root})
	if _, err := core.Dispatch("plugin_host_workspace_scope_commit", string(raw)); contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Fatalf("stale commit: %v", err)
	}
	current := scopeCommand(t, "get", map[string]string{})
	if current.WorkspaceID != "B" || current.Ready || current.Root != "" {
		t.Fatalf("mixed scope: %+v", current)
	}
	committed := scopeCommand(t, "commit", map[string]string{"workspaceId": "B", "expectedRevision": b.Revision, "root": root})
	canonical, _ := canonicalWorkspaceRoot(root)
	if !committed.Ready || committed.Root != canonical || committed.Revision != b.Revision {
		t.Fatalf("commit: %+v", committed)
	}
}

func TestWorkspaceScopeSameIDReload(t *testing.T) {
	useScopeRPCManager(t)
	a := scopeCommand(t, "begin", map[string]string{"workspaceId": "A"})
	scopeCommand(t, "commit", map[string]string{"workspaceId": "A", "expectedRevision": a.Revision, "root": t.TempDir()})
	b := scopeCommand(t, "begin", map[string]string{"workspaceId": "A"})
	if b.Revision == a.Revision || b.Ready || b.Root != "" {
		t.Fatalf("reload reused scope: %+v", b)
	}
}

func TestWorkspaceScopeLegacyNoBypass(t *testing.T) {
	useScopeRPCManager(t)
	before := scopeCommand(t, "begin", map[string]string{"workspaceId": "B"})
	rootArgs, _ := json.Marshal(map[string]string{"root": t.TempDir()})
	for command, args := range map[string]string{"plugin_host_set_active_workspace": `{"workspaceId":"A"}`, "plugin_host_set_workspace_root": string(rootArgs)} {
		if _, err := core.Dispatch(command, args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("legacy command %s must reject with invalid_request, got %v", command, err)
		}
	}
	after := scopeCommand(t, "get", map[string]string{})
	if after != before || after.Ready {
		t.Fatalf("legacy setters changed scope or restored readiness: before=%+v after=%+v", before, after)
	}
}

// Snapshot-only fixtures intentionally do not run lifecycle reconciliation:
// callers test admission or registry filtering, sometimes holding record.mu.
func setWorkspaceScopeFixture(manager *pluginManager, id string) {
	manager.scopeState.Store(&workspaceScopeState{snapshot: workspaceScopeSnapshot{WorkspaceID: id, Revision: "0"}, ctx: context.Background(), cancel: func() {}})
}

func TestWorkspaceScopeCanonicalizationCAS(t *testing.T) {
	manager := &pluginManager{}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	a, err := manager.BeginWorkspaceScope(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	root := t.TempDir()
	go func() {
		_, err := manager.commitWorkspaceScope("A", a.Revision, root, func(root string) (string, error) { close(entered); <-release; return canonicalWorkspaceRoot(root) })
		result <- err
	}()
	<-entered
	b, err := manager.BeginWorkspaceScope(context.Background(), "B")
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Fatalf("canonicalization raced begin: %v", err)
	}
	if got := manager.workspaceScope(); got != b || got.Ready || got.Root != "" {
		t.Fatalf("stale root published: %+v", got)
	}
}

func TestWorkspaceScopeContextsAndObservers(t *testing.T) {
	manager := &pluginManager{}
	if got := manager.workspaceScope(); got.Revision != "0" || got.Ready {
		t.Fatalf("initial scope: %+v", got)
	}
	if _, err := manager.workspaceScopeContext(manager.workspaceScope()); err == nil {
		t.Fatal("initial scope accepted")
	}
	var snapshots []workspaceScopeSnapshot
	dispose := manager.OnWorkspaceScopeChange(func(scope workspaceScopeSnapshot) {
		stop := manager.OnWorkspaceScopeChange(func(workspaceScopeSnapshot) {})
		stop()
		snapshots = append(snapshots, scope)
	})
	a, _ := manager.BeginWorkspaceScope(context.Background(), "A")
	if _, err := manager.workspaceScopeContext(a); err == nil {
		t.Fatal("unready scope accepted")
	}
	ready, err := manager.CommitWorkspaceScope("A", a.Revision, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := manager.workspaceScopeContext(ready)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := manager.BeginWorkspaceScope(context.Background(), "A")
	if ctx.Err() == nil {
		t.Fatal("begin did not cancel previous revision")
	}
	if _, err := manager.workspaceScopeContext(ready); err == nil {
		t.Fatal("stale scope accepted")
	}
	dispose()
	dispose()
	if len(snapshots) != 3 {
		t.Fatalf("observer notifications: %+v", snapshots)
	}
	empty, err := manager.CommitWorkspaceScope("A", b.Revision, "")
	if err != nil || empty.Ready {
		t.Fatalf("empty root: %+v %v", empty, err)
	}
	if _, err := manager.CommitWorkspaceScope("A", b.Revision, t.TempDir()); contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Fatalf("ticket replay: %v", err)
	}
	c, _ := manager.BeginWorkspaceScope(context.Background(), "A")
	ready, err = manager.CommitWorkspaceScope("A", c.Revision, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = manager.workspaceScopeContext(ready)
	if err != nil {
		t.Fatal(err)
	}
	manager.Shutdown(context.Background())
	if ctx.Err() == nil {
		t.Fatal("shutdown did not cancel scope")
	}
	if _, err = manager.workspaceScopeContext(ready); err == nil {
		t.Fatal("scope accepted after shutdown")
	}
	if _, err = manager.BeginWorkspaceScope(context.Background(), "B"); err == nil {
		t.Fatal("begin accepted after shutdown")
	}
}

func TestWorkspaceScopeRapidABCReconcilesActualState(t *testing.T) {
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	record.scope = pluginScopeWorkspace
	record.workspaces = map[string]struct{}{"A": {}}
	if err := manager.SetActiveWorkspace(context.Background(), "A"); err != nil {
		t.Fatal(err)
	}
	manager.operationMu.Lock()
	b, err := manager.BeginWorkspaceScope(context.Background(), "B")
	if err != nil {
		manager.operationMu.Unlock()
		t.Fatal(err)
	}
	c, err := manager.BeginWorkspaceScope(context.Background(), "C")
	manager.operationMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if b.Revision == c.Revision {
		t.Fatal("revision reused")
	}
	deadline := time.After(3 * time.Second)
	for {
		record.mu.Lock()
		stopped := record.state == pluginStateStopped
		record.mu.Unlock()
		if stopped {
			break
		}
		select {
		case <-deadline:
			t.Fatal("A-only process survived C")
		case <-time.After(time.Millisecond):
		}
	}
	if plugin.shutdownCalls.Load() != 1 {
		t.Fatalf("shutdown calls: %d", plugin.shutdownCalls.Load())
	}
}

type scopeSlowStopPlugin struct {
	testLifecyclePlugin
	entered chan struct{}
	release chan struct{}
}

func (plugin *scopeSlowStopPlugin) OnShutdown(context.Context) error {
	close(plugin.entered)
	<-plugin.release
	return nil
}

func TestWorkspaceScopeBeginDoesNotWaitForNativeStop(t *testing.T) {
	plugin := &scopeSlowStopPlugin{entered: make(chan struct{}), release: make(chan struct{})}
	manager, record := testManagerWithPlugin(plugin, true)
	record.scope = pluginScopeWorkspace
	record.workspaces = map[string]struct{}{"A": {}}
	if err := manager.SetActiveWorkspace(context.Background(), "A"); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := manager.BeginWorkspaceScope(context.Background(), "B"); finished <- err }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(plugin.release)
		t.Fatal("begin waited for native stop")
	}
	<-plugin.entered
	_, err := manager.BeginWorkspaceScope(context.Background(), "C")
	close(plugin.release)
	manager.Shutdown(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceScopeInternalSetterHonorsCallerContext(t *testing.T) {
	plugin := &testLifecyclePlugin{waitForCancel: true}
	manager, record := testManagerWithPlugin(plugin, true)
	// Workspace-scoped and newly effective in A, so the switch starts it.
	record.scope = pluginScopeWorkspace
	record.workspaces = map[string]struct{}{"A": {}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- manager.SetActiveWorkspace(ctx, "A") }()
	select {
	case err := <-finished:
		if err == nil {
			t.Error("canceled internal setter succeeded")
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("internal setter ignored the caller's deadline")
	}
	manager.Shutdown(context.Background())
}

type scopeWorkspaceEventRecorder struct {
	mu     sync.Mutex
	events []string
}

func recordScopeWorkspaceEvents(t *testing.T) *scopeWorkspaceEventRecorder {
	t.Helper()
	recorder := &scopeWorkspaceEventRecorder{}
	core.SetEmitter(func(name string, _ any) {
		if name == pluginWorkspaceChangedEvent || name == pluginContributionsChangedEvent {
			recorder.mu.Lock()
			recorder.events = append(recorder.events, name)
			recorder.mu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	return recorder
}

func (recorder *scopeWorkspaceEventRecorder) snapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.events...)
}

func waitWorkspaceScopeReconciled(t *testing.T, manager *pluginManager, scope workspaceScopeSnapshot) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		manager.operationMu.Lock()
		done := manager.scopeReconciledRevision == scope.Revision
		manager.operationMu.Unlock()
		if done {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("revision %s was not reconciled", scope.Revision)
}

// Background startup owns delayed/concurrency-limited activation. A first
// bridge begin must not turn built-ins/global startup plugins into eager starts.
func TestWorkspaceScopeFirstBeginDoesNotBypassStartup(t *testing.T) {
	builtinPlugin := &testLifecyclePlugin{}
	manager, builtin := testManagerWithPlugin(builtinPlugin, true)
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	manifest := &Manifest{ID: "global.startup", Runtime: ProcessRuntimeManifest{Type: "process", Entry: "missing", Activation: pluginActivationStartup}}
	// A start attempt would fail and move this record to error, so remaining
	// stopped proves reconcile did not attempt activation.
	global := &pluginRecord{id: manifest.ID, source: pluginSourceLocal, manifest: manifest, runtime: newProcessRuntime(t.TempDir(), *manifest, nil, nil), enabled: true, scope: pluginScopeGlobal, state: pluginStateStopped}
	manager.records[global.id] = global
	manager.order = append(manager.order, global.id)
	scope, err := manager.BeginWorkspaceScope(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	waitWorkspaceScopeReconciled(t, manager, scope)
	if builtinPlugin.startupCalls.Load() != 0 || builtin.state != pluginStateStopped || global.state != pluginStateStopped {
		t.Fatalf("first begin bypassed startup: builtin calls=%d builtin=%s global=%s", builtinPlugin.startupCalls.Load(), builtin.state, global.state)
	}
}

func TestWorkspaceScopeRapidABCStartsOnlyNewlyEffectivePlugin(t *testing.T) {
	plugin := &testLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	record.scope = pluginScopeWorkspace
	record.workspaces = map[string]struct{}{"C": {}}
	if err := manager.SetActiveWorkspace(context.Background(), "A"); err != nil {
		t.Fatal(err)
	}
	manager.operationMu.Lock()
	_, err := manager.BeginWorkspaceScope(context.Background(), "B")
	if err != nil {
		manager.operationMu.Unlock()
		t.Fatal(err)
	}
	c, err := manager.BeginWorkspaceScope(context.Background(), "C")
	manager.operationMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitWorkspaceScopeReconciled(t, manager, c)
	if plugin.startupCalls.Load() != 1 || record.state != pluginStateRunning {
		t.Fatalf("C-only startup plugin calls=%d state=%s", plugin.startupCalls.Load(), record.state)
	}
}

func TestWorkspaceScopeSameIDRootReloadKeepsWorkspaceEventCadence(t *testing.T) {
	recorder := recordScopeWorkspaceEvents(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	first, err := manager.BeginWorkspaceScope(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	waitWorkspaceScopeReconciled(t, manager, first)
	before := recorder.snapshot()
	reload, err := manager.BeginWorkspaceScope(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	waitWorkspaceScopeReconciled(t, manager, reload)
	if after := recorder.snapshot(); len(after) != len(before) {
		t.Fatalf("same-ID reload emitted workspace events: before=%v after=%v", before, after)
	}
}

// R6: restore does not require an already running process. Its page joins the
// existing BeginUI/ensureRunning lazy activation path.
func TestFloatingRestoreDoesNotWaitForWorkspaceReconcile(t *testing.T) {
	manager, record, driver := newFloatingFixture(t)
	record.state = pluginStateStopped
	if err := updateFloatingView("pet.plugin", "pet", func(view *floatingViewState) { view.Visible = true }); err != nil {
		t.Fatal(err)
	}
	if opened := manager.RestoreFloatingViews(); opened != 1 || !driver.isOpen(floatingWindowName("pet.plugin", "pet")) {
		t.Fatalf("stopped effective plugin was not restored: opened=%d", opened)
	}
	if record.state != pluginStateStopped {
		t.Fatalf("restore itself activated the plugin: %s", record.state)
	}
}

// A superseded B may already have stopped a plugin effective in A and C;
// transition-only starts from the last reconciled A must still restore it.
func TestWorkspaceScopeRestartsPluginStoppedBySupersededReconcile(t *testing.T) {
	for _, final := range []string{"C", "A"} {
		t.Run(final, func(t *testing.T) {
			plugin := &scopeSlowStopPlugin{entered: make(chan struct{}), release: make(chan struct{})}
			manager, record := testManagerWithPlugin(plugin, true)
			t.Cleanup(func() { manager.Shutdown(context.Background()) })
			record.scope = pluginScopeWorkspace
			record.workspaces = map[string]struct{}{"A": {}, "C": {}}
			if err := manager.SetActiveWorkspace(context.Background(), "A"); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.BeginWorkspaceScope(context.Background(), "B"); err != nil {
				t.Fatal(err)
			}
			<-plugin.entered
			last, err := manager.BeginWorkspaceScope(context.Background(), final)
			close(plugin.release)
			if err != nil {
				t.Fatal(err)
			}
			waitWorkspaceScopeReconciled(t, manager, last)
			if record.state != pluginStateRunning || plugin.startupCalls.Load() != 2 {
				t.Fatalf("plugin stopped by superseded B stayed stopped in %s: state=%s starts=%d", final, record.state, plugin.startupCalls.Load())
			}
		})
	}
}

func TestWorkspaceScopeRequiredCoreFields(t *testing.T) {
	useScopeRPCManager(t)
	for _, raw := range []string{`{}`, `{"workspaceId":null}`} {
		if _, err := core.Dispatch("plugin_host_workspace_scope_begin", raw); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("begin %s: %v", raw, err)
		}
	}
	a := scopeCommand(t, "begin", map[string]string{"workspaceId": "A"})
	raw, _ := json.Marshal(map[string]any{"workspaceId": "A", "expectedRevision": a.Revision, "root": nil})
	if _, err := core.Dispatch("plugin_host_workspace_scope_commit", string(raw)); contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Errorf("null root: %v", err)
	}
}

func TestWorkspaceScopeStrictCoreArgs(t *testing.T) {
	useScopeRPCManager(t)
	for _, command := range []string{"begin", "commit", "get"} {
		for _, raw := range []string{`{"unknown":true}`, `{} {}`, `null`, `[]`, `{"workspaceId":12}`} {
			if _, err := core.Dispatch("plugin_host_workspace_scope_"+command, raw); contributionErrorCode(err) != contributionErrInvalidRequest {
				t.Errorf("%s %s: %v", command, raw, err)
			}
		}
	}
}

// No package tests run in parallel. Replace only the singleton manager pointer,
// then join its worker before restoring it, so RPC tests cannot outlive fixtures.
func useScopeRPCManager(t *testing.T) {
	t.Helper()
	previous := host()
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	managerInst = manager
	t.Cleanup(func() { manager.Shutdown(context.Background()); managerInst = previous })
}

func TestWorkspaceScopeRevisionExhaustion(t *testing.T) {
	manager := &pluginManager{}
	manager.scopeRevision = math.MaxUint64 - 1
	last, err := manager.beginWorkspaceScope(context.Background(), "last", false)
	if err != nil || last.Revision != "18446744073709551615" {
		t.Fatalf("last scope: %+v %v", last, err)
	}
	last, err = manager.CommitWorkspaceScope("last", last.Revision, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := manager.scopeState.Load()
	defer state.cancel()
	notifications := 0
	off := manager.OnWorkspaceScopeChange(func(workspaceScopeSnapshot) { notifications++ })
	defer off()
	baseline := notifications
	for range 2 {
		_, err := manager.beginWorkspaceScope(context.Background(), "rejected", false)
		if contributionErrorCode(err) != contributionErrUnavailable || !strings.Contains(err.Error(), "revision space is exhausted") {
			t.Errorf("exhaustion: %v", err)
		}
		if manager.scopeState.Load() != state || manager.workspaceScope() != last || state.ctx.Err() != nil || notifications != baseline || manager.scopeRevision != math.MaxUint64 {
			t.Error("exhausted begin mutated/cancelled/published state")
		}
	}
}
