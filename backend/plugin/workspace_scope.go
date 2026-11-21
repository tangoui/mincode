package plugin

import (
	"context"
	"errors"
	"log"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"mincode/app/backend/core"
)

// workspaceScopeSnapshot is published as a single immutable value. Revision is
// host-owned and opaque on the wire; even a same-workspace reload invalidates it.
type workspaceScopeSnapshot struct {
	WorkspaceID string `json:"workspaceId"`
	Root        string `json:"root"`
	Revision    string `json:"revision"`
	Ready       bool   `json:"ready"`
}

type workspaceScopeState struct {
	snapshot  workspaceScopeSnapshot
	input     string
	ctx       context.Context
	cancel    context.CancelFunc
	committed bool
}

func (manager *pluginManager) workspaceScope() workspaceScopeSnapshot {
	if state := manager.scopeState.Load(); state != nil {
		return state.snapshot
	}
	return workspaceScopeSnapshot{Revision: "0"}
}

func (manager *pluginManager) scopeMatches(scope workspaceScopeSnapshot) bool {
	current := manager.workspaceScope()
	return current.WorkspaceID == scope.WorkspaceID && current.Revision == scope.Revision
}

func (manager *pluginManager) workspaceScopeContext(scope workspaceScopeSnapshot) (context.Context, error) {
	manager.scopeMu.Lock()
	defer manager.scopeMu.Unlock()
	state := manager.scopeState.Load()
	if manager.scopeClosed || state == nil || !scope.Ready || state.snapshot != scope || state.ctx.Err() != nil {
		return nil, newContributionError(contributionErrInvalidRequest, "workspace scope is not ready or is stale")
	}
	return state.ctx, nil
}

// BeginWorkspaceScope only publishes invalidation and schedules reconciliation;
// native plugin shutdown/startup never runs on the caller's critical path.
func (manager *pluginManager) BeginWorkspaceScope(ctx context.Context, workspaceID string) (workspaceScopeSnapshot, error) {
	return manager.beginWorkspaceScope(ctx, workspaceID, true)
}

func (manager *pluginManager) beginWorkspaceScope(ctx context.Context, workspaceID string, reconcile bool) (workspaceScopeSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return workspaceScopeSnapshot{}, err
	}
	manager.scopeMu.Lock()
	if manager.scopeClosed {
		manager.scopeMu.Unlock()
		return workspaceScopeSnapshot{}, newContributionError(contributionErrUnavailable, "plugin host is shutting down")
	}
	if manager.scopeRevision == math.MaxUint64 {
		manager.scopeMu.Unlock()
		return workspaceScopeSnapshot{}, newContributionError(contributionErrUnavailable, "workspace scope revision space is exhausted; restart MinCode")
	}
	previous := manager.scopeState.Load()
	manager.scopeRevision++
	scope := workspaceScopeSnapshot{WorkspaceID: strings.TrimSpace(workspaceID), Revision: strconv.FormatUint(manager.scopeRevision, 10)}
	scopeCtx, cancel := context.WithCancel(context.Background())
	state := &workspaceScopeState{snapshot: scope, ctx: scopeCtx, cancel: cancel}
	manager.scopeState.Store(state)
	listeners := manager.scopeListenersLocked()
	startWorker := reconcile && !manager.scopeWorker
	if startWorker {
		manager.scopeWorker = true
		manager.scopeWG.Add(1)
	}
	manager.scopeMu.Unlock()
	if previous != nil {
		previous.cancel()
	}
	// Start before callbacks: observers may synchronously begin another switch.
	if startWorker {
		go manager.runWorkspaceScopeReconciler()
	}
	manager.notifyWorkspaceScope(scope, listeners)
	if previous != nil && previous.snapshot.Root != "" && manager.workspaceScope() == scope {
		manager.publishHostEvent(hostEventWorkspaceRootChanged, map[string]any{"root": nil})
	}
	return scope, nil
}

func (manager *pluginManager) CommitWorkspaceScope(workspaceID, expectedRevision, root string) (workspaceScopeSnapshot, error) {
	return manager.commitWorkspaceScope(workspaceID, expectedRevision, root, canonicalWorkspaceRoot)
}

func (manager *pluginManager) commitWorkspaceScope(workspaceID, expectedRevision, root string, canonicalize func(string) (string, error)) (workspaceScopeSnapshot, error) {
	ticket := workspaceScopeSnapshot{WorkspaceID: workspaceID, Revision: expectedRevision}
	manager.scopeMu.Lock()
	valid := !manager.scopeClosed && expectedRevision != "0" && expectedRevision != "" && manager.scopeMatches(ticket)
	manager.scopeMu.Unlock()
	if !valid {
		return workspaceScopeSnapshot{}, newContributionError(contributionErrInvalidRequest, "stale workspace scope ticket")
	}
	// Filesystem IO deliberately outside scopeMu; repeat the CAS afterwards.
	canonical, err := canonicalize(root)
	if err != nil {
		return workspaceScopeSnapshot{}, newContributionError(contributionErrInvalidRequest, "%v", err)
	}
	input := ""
	if canonical != "" {
		input = filepath.Clean(strings.TrimSpace(root))
	}
	manager.scopeMu.Lock()
	if manager.scopeClosed || !manager.scopeMatches(ticket) {
		manager.scopeMu.Unlock()
		return workspaceScopeSnapshot{}, newContributionError(contributionErrInvalidRequest, "stale workspace scope ticket")
	}
	previous := manager.scopeState.Load()
	// One commit per ticket: changing the root needs a fresh begin/revision.
	if previous.committed {
		manager.scopeMu.Unlock()
		return workspaceScopeSnapshot{}, newContributionError(contributionErrInvalidRequest, "workspace scope already committed; begin a new scope")
	}
	scope := workspaceScopeSnapshot{WorkspaceID: workspaceID, Revision: expectedRevision, Root: canonical, Ready: canonical != ""}
	manager.scopeState.Store(&workspaceScopeState{snapshot: scope, input: input, ctx: previous.ctx, cancel: previous.cancel, committed: true})
	listeners := manager.scopeListenersLocked()
	manager.scopeMu.Unlock()
	manager.notifyWorkspaceScope(scope, listeners)
	if canonical != "" && manager.workspaceScope() == scope {
		manager.publishHostEvent(hostEventWorkspaceRootChanged, map[string]any{"root": canonical})
	}
	return scope, nil
}

// Observers run without scopeMu and may reenter the manager. Since publication
// can overtake a callback, consumers must recheck the snapshot before doing IO.
func (manager *pluginManager) OnWorkspaceScopeChange(fn func(workspaceScopeSnapshot)) func() {
	manager.scopeMu.Lock()
	if manager.scopeClosed || fn == nil {
		manager.scopeMu.Unlock()
		return func() {}
	}
	if manager.scopeListeners == nil {
		manager.scopeListeners = make(map[uint64]func(workspaceScopeSnapshot))
	}
	manager.scopeListenerID++
	id := manager.scopeListenerID
	manager.scopeListeners[id] = fn
	manager.scopeMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { manager.scopeMu.Lock(); delete(manager.scopeListeners, id); manager.scopeMu.Unlock() })
	}
}
func (manager *pluginManager) scopeListenersLocked() []func(workspaceScopeSnapshot) {
	listeners := make([]func(workspaceScopeSnapshot), 0, len(manager.scopeListeners))
	for _, fn := range manager.scopeListeners {
		listeners = append(listeners, fn)
	}
	return listeners
}
func (manager *pluginManager) notifyWorkspaceScope(scope workspaceScopeSnapshot, listeners []func(workspaceScopeSnapshot)) {
	for _, fn := range listeners {
		fn(scope)
	}
}

// At most one worker exists. Rapid switches coalesce, but reconciliation reads
// actual record state, never just the previous desired workspace's delta.
func (manager *pluginManager) runWorkspaceScopeReconciler() {
	defer manager.scopeWG.Done()
	for {
		state := manager.scopeState.Load()
		manager.operationMu.Lock()
		if err := manager.reconcileWorkspaceScope(state.ctx, manager.scopeReconciledWorkspace, state.snapshot); err != nil && state.ctx.Err() == nil {
			log.Printf("plugin workspace reconcile: %v", err)
		}
		manager.operationMu.Unlock()
		manager.scopeMu.Lock()
		if manager.scopeClosed || manager.workspaceScope().Revision == state.snapshot.Revision {
			manager.scopeWorker = false
			manager.scopeMu.Unlock()
			return
		}
		manager.scopeMu.Unlock()
	}
}

// scopeDoneLocked returns the channel closed when the scope system closes.
func (manager *pluginManager) scopeDoneLocked() chan struct{} {
	if manager.scopeDone == nil {
		manager.scopeDone = make(chan struct{})
	}
	return manager.scopeDone
}

func (manager *pluginManager) closeWorkspaceScope() {
	manager.scopeMu.Lock()
	if !manager.scopeClosed {
		close(manager.scopeDoneLocked())
	}
	manager.scopeClosed = true
	state := manager.scopeState.Load()
	manager.scopeListeners = nil
	manager.scopeMu.Unlock()
	if state != nil {
		state.cancel()
	}
	// No Add can follow scopeClosed. Shutdown owns and joins this worker.
	manager.scopeWG.Wait()
}

func decodeWorkspaceScopeArgs(raw string, destination any) error {
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return newContributionError(contributionErrInvalidRequest, "workspace scope arguments must be an object")
	}
	if err := decodeStrictJSONValue([]byte(raw), destination, true, "args"); err != nil {
		return newContributionError(contributionErrInvalidRequest, "workspace scope: invalid arguments: %v", err)
	}
	return nil
}

func init() {
	core.RegisterCtx("plugin_host_workspace_scope_begin", func(ctx context.Context, _, raw string) (string, error) {
		var args struct {
			WorkspaceID *string `json:"workspaceId"`
		}
		if err := decodeWorkspaceScopeArgs(raw, &args); err != nil {
			return "", err
		}
		if args.WorkspaceID == nil {
			return "", newContributionError(contributionErrInvalidRequest, "workspaceId is required")
		}
		scope, err := host().BeginWorkspaceScope(ctx, *args.WorkspaceID)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(scope)
	})
	core.Register("plugin_host_workspace_scope_commit", func(_, raw string) (string, error) {
		var args struct {
			WorkspaceID      *string `json:"workspaceId"`
			ExpectedRevision *string `json:"expectedRevision"`
			Root             *string `json:"root"`
		}
		if err := decodeWorkspaceScopeArgs(raw, &args); err != nil {
			return "", err
		}
		if args.WorkspaceID == nil || args.ExpectedRevision == nil || args.Root == nil {
			return "", newContributionError(contributionErrInvalidRequest, "workspaceId, expectedRevision and root are required")
		}
		scope, err := host().CommitWorkspaceScope(*args.WorkspaceID, *args.ExpectedRevision, *args.Root)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(scope)
	})
	core.Register("plugin_host_workspace_scope_get", func(_, raw string) (string, error) {
		var args struct{}
		if err := decodeWorkspaceScopeArgs(raw, &args); err != nil {
			return "", err
		}
		return core.EncodeResult(host().workspaceScope())
	})
}

func legacyWorkspaceScopeError() error {
	return newContributionError(contributionErrInvalidRequest, "use plugin_host_workspace_scope_begin and plugin_host_workspace_scope_commit instead of legacy workspace setters")
}

// Keep formatting of internal errors separate from wire contribution codes.
func staleWorkspaceReconcileError() error { return errors.New("workspace reconciliation superseded") }
