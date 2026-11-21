package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	backendfs "mincode/app/backend/fs"
	backendgit "mincode/app/backend/git"
	"mincode/app/plugins/api"
)

const (
	hostEventGitStatusChanged = "git.statusChanged"
	hostEventAppLockChanged   = "app.lockChanged"
	hostEventAppSuspend       = "app.suspend"
	hostEventAppResume        = "app.resume"
	gitStatusInterval         = 3 * time.Second
)

type hostStatusEvents struct {
	gitMu             sync.Mutex
	gitSource         *gitStatusSource
	unsubscribe       func()
	power             atomic.Uint32
	lockMu            sync.Mutex
	lockKnown, locked bool
}

type gitStatusSource struct {
	manager   *pluginManager
	scope     workspaceScopeSnapshot
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	sample    func(context.Context, string, func(string) bool) (backendgit.PluginStatusSnapshot, error)
	mu        sync.Mutex
	sampling  bool
	lastStart time.Time
	baseline  string
}

func init() {
	hostEvents[hostEventGitStatusChanged] = api.CapabilityWorkspaceRead
	hostEvents[hostEventAppLockChanged] = ""
	hostEvents[hostEventAppSuspend] = ""
	hostEvents[hostEventAppResume] = ""
}

// describe and subscribe share exactly this actual-source predicate. The
// workspace source is capability-gated separately and may be idle until ready.
func (m *pluginManager) eventSourceAvailable(event string) bool {
	switch event {
	case hostEventAppSuspend:
		return m.statusEvents.power.Load()&1 != 0
	case hostEventAppResume:
		return m.statusEvents.power.Load()&2 != 0
	default:
		return true
	}
}
func (rt *processRuntime) eventSourceAvailable(event string) bool {
	if rt.ownerManager != nil {
		return rt.ownerManager.eventSourceAvailable(event)
	}
	// Discovery must not initialize the lazy host just to describe old events.
	bits := installedPowerAvailability.Load()
	switch event {
	case hostEventAppSuspend:
		return bits&1 != 0
	case hostEventAppResume:
		return bits&2 != 0
	default:
		return true
	}
}

// Serializes singleton publication with startup/native availability updates.
var installedPowerMu sync.Mutex
var installedPowerAvailability atomic.Uint32

func SetPowerEventAvailability(suspend, resume bool) {
	installedPowerMu.Lock()
	defer installedPowerMu.Unlock()
	var bits uint32
	if suspend {
		bits |= 1
	}
	if resume {
		bits |= 2
	}
	installedPowerAvailability.Store(bits)
	if m := workspaceMutationHost.Load(); m != nil {
		m.statusEvents.power.Store(bits)
	}
}
func (m *pluginManager) setPowerEventAvailability(suspend, resume bool) {
	var bits uint32
	if suspend {
		bits |= 1
	}
	if resume {
		bits |= 2
	}
	m.statusEvents.power.Store(bits)
}
func PublishPowerEvent(event string) {
	if m := workspaceMutationHost.Load(); m != nil {
		m.publishPowerEvent(event)
	}
}
func (m *pluginManager) publishPowerEvent(event string) {
	if (event != hostEventAppSuspend && event != hostEventAppResume) || !m.eventSourceAvailable(event) {
		return
	}
	m.publishStatusEvent(nil, event, map[string]any{})
	if event == hostEventAppResume {
		// Wake only the existing bounded source; never start a watcher for power.
		if source := m.fileEvents.Load(); source != nil && source.ctx.Err() == nil {
			source.enqueue(nil, "sourceLost")
			source.watcher.RequestRescan()
		}
	}
}

var appLockObservation struct {
	mu       sync.Mutex
	sequence uint64
	locked   bool
}

// Read outside the coordination mutex: applock's initial snapshot may invoke
// its observer reentrantly. An observation that overtakes that read wins over
// the stale value; seeding the manager and publishing it are one critical step.
func (m *pluginManager) publishStatusHost(readLocked func() bool) {
	// Once published, host() may be called while a record lock is held. Do not
	// wait for an observer which could itself be publishing to that record.
	if workspaceMutationHost.Load() == m {
		return
	}
	appLockObservation.mu.Lock()
	if workspaceMutationHost.Load() == m {
		appLockObservation.mu.Unlock()
		return
	}
	sequence := appLockObservation.sequence
	appLockObservation.mu.Unlock()
	locked := readLocked()
	appLockObservation.mu.Lock()
	defer appLockObservation.mu.Unlock()
	if sequence == appLockObservation.sequence {
		appLockObservation.locked = locked
	}
	m.initAppLockState(appLockObservation.locked)
	installedPowerMu.Lock()
	workspaceMutationHost.Store(m)
	m.statusEvents.power.Store(installedPowerAvailability.Load())
	installedPowerMu.Unlock()
}

// Go's applock observer owns this input; no WebView event or grant is involved.
// Keep the existing shortcut/token/tray reconciliation unchanged. Retain the
// boolean even before the lazy singleton is published, so bootstrap cannot
// drop a transition or turn the next preference update into a fake change.
func AppLockStateChanged(locked bool) {
	AppLockChanged()
	appLockObservation.mu.Lock()
	defer appLockObservation.mu.Unlock()
	appLockObservation.sequence++
	appLockObservation.locked = locked
	if m := workspaceMutationHost.Load(); m != nil {
		m.appLockStateChanged(locked)
	}
}
func (m *pluginManager) initAppLockState(locked bool) {
	m.statusEvents.lockMu.Lock()
	if !m.statusEvents.lockKnown {
		m.statusEvents.lockKnown = true
		m.statusEvents.locked = locked
	}
	m.statusEvents.lockMu.Unlock()
}
func (m *pluginManager) appLockStateChanged(locked bool) {
	m.statusEvents.lockMu.Lock()
	previous, known := m.statusEvents.locked, m.statusEvents.lockKnown
	m.statusEvents.locked, m.statusEvents.lockKnown = locked, true
	m.statusEvents.lockMu.Unlock()
	if known && previous != locked {
		m.publishStatusEvent(nil, hostEventAppLockChanged, map[string]any{"locked": locked})
	}
}

func (m *pluginManager) hasGitStatusSubscriber() bool {
	m.mu.RLock()
	records, closed := m.orderedRecordsLocked(), m.shuttingDown
	m.mu.RUnlock()
	if closed {
		return false
	}
	for _, r := range records {
		r.mu.Lock()
		rt := r.runtime
		eligible := r.enabled && r.state == pluginStateRunning && m.workspaceActiveLocked(r) && rt != nil
		if eligible {
			rt.mu.Lock()
			_, sub := rt.eventSubscriptions[hostEventGitStatusChanged]
			_, grant := rt.capabilities[api.CapabilityWorkspaceRead]
			running := rt.cmd != nil && !rt.stopping
			rt.mu.Unlock()
			if sub && grant && running {
				r.mu.Unlock()
				return true
			}
		}
		r.mu.Unlock()
	}
	return false
}
func (m *pluginManager) syncGitStatusEvents() {
	state := &m.statusEvents
	state.gitMu.Lock()
	defer state.gitMu.Unlock()
	if state.unsubscribe == nil {
		state.unsubscribe = m.OnWorkspaceScopeChange(func(workspaceScopeSnapshot) { m.syncGitStatusEvents() })
	}
	scope := m.workspaceScope()
	active := scope.Ready && m.hasGitStatusSubscriber()
	if current := state.gitSource; current != nil {
		if active && current.scope == scope && current.ctx.Err() == nil {
			return
		}
		current.cancel()
		<-current.done
		state.gitSource = nil
	}
	if !active {
		return
	}
	parent, err := m.workspaceScopeContext(scope)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	source := &gitStatusSource{manager: m, scope: scope, ctx: ctx, cancel: cancel, done: make(chan struct{}), sample: backendgit.SamplePluginStatus}
	state.gitSource = source
	go source.run()
}
func (m *pluginManager) closeGitStatusEvents() {
	state := &m.statusEvents
	state.gitMu.Lock()
	defer state.gitMu.Unlock()
	if state.unsubscribe != nil {
		state.unsubscribe()
		state.unsubscribe = nil
	}
	if s := state.gitSource; s != nil {
		s.cancel()
		<-s.done
		state.gitSource = nil
	}
}
func (s *gitStatusSource) run() {
	defer close(s.done)
	ticker := time.NewTicker(gitStatusInterval)
	defer ticker.Stop()
	s.poll(time.Now())
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.poll(time.Now())
		}
	}
}

// poll is also guarded against concurrent invocations and a queued old tick
// after a slow sample: starts are never closer than three seconds.
func (s *gitStatusSource) poll(now time.Time) {
	s.mu.Lock()
	if s.sampling || (!s.lastStart.IsZero() && now.Sub(s.lastStart) < gitStatusInterval) || s.ctx.Err() != nil || s.manager.workspaceScope() != s.scope || !s.manager.hasGitStatusSubscriber() {
		s.mu.Unlock()
		return
	}
	s.sampling, s.lastStart = true, now
	s.mu.Unlock()
	snapshot, err := s.sample(s.ctx, s.scope.Root, func(relative string) bool { return gitStatusPathAllowed(s.scope.Root, relative) })
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sampling = false
	if err != nil || s.ctx.Err() != nil || s.manager.workspaceScope() != s.scope || !s.manager.hasGitStatusSubscriber() {
		return
	}
	previous := s.baseline
	s.baseline = snapshot.Digest
	if previous == "" || previous == snapshot.Digest {
		return
	}
	s.manager.publishStatusEvent(s, hostEventGitStatusChanged, map[string]any{"scopeRevision": s.scope.Revision, "staged": snapshot.Staged, "unstaged": snapshot.Unstaged, "untracked": snapshot.Untracked, "conflicted": snapshot.Conflicted})
}
func gitStatusPathAllowed(root, relative string) bool {
	clean, err := backendfs.CleanProjectRelativePath(relative)
	if err != nil || clean != relative || relative == "." || len(relative) > hostEventPathMaxLength || workspacePathDenied(relative, false) {
		return false
	}
	for _, segment := range strings.Split(relative, "/") {
		if isFileTemplateSensitiveName(segment) {
			return false
		}
	}
	// Deleted/renamed old paths may no longer exist. Check every existing
	// component without following links; absent suffixes carry no file access.
	current := root
	for _, segment := range append([]string{"."}, strings.Split(relative, "/")...) {
		current = filepath.Join(current, filepath.FromSlash(segment))
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return true
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

// New sources use generation-bound admission at both publication and the async
// boundary; unlike legacy events they never relabel a late scope sample.
func (m *pluginManager) publishStatusEvent(source *gitStatusSource, event string, data any) {
	valid := func() bool { return source == nil || (source.ctx.Err() == nil && m.workspaceScope() == source.scope) }
	if !valid() {
		return
	}
	params, err := json.Marshal(map[string]any{"event": event, "data": data})
	if err != nil || len(params) >= hostEventParamsMaxBytes {
		return
	}
	m.mu.RLock()
	records, closed := m.orderedRecordsLocked(), m.shuttingDown
	m.mu.RUnlock()
	if closed {
		return
	}
	for _, r := range records {
		r.mu.Lock()
		rt := r.runtime
		eligible := r.enabled && r.state == pluginStateRunning && m.workspaceActiveLocked(r) && rt != nil
		r.mu.Unlock()
		if !eligible {
			continue
		}
		rt.mu.Lock()
		g := rt.generation
		_, sub := rt.eventSubscriptions[event]
		_, grant := rt.capabilities[api.CapabilityWorkspaceRead]
		running := rt.cmd != nil && !rt.stopping
		rt.mu.Unlock()
		if !sub || !running || (source != nil && !grant) {
			continue
		}
		if rt.eventsInflight.Add(1) > hostEventInflightMax {
			rt.eventsInflight.Add(-1)
			r.appendLog("warn", "event dropped while the plugin was busy: "+event)
			continue
		}
		go func() {
			defer rt.eventsInflight.Add(-1)
			if !valid() || !m.eventSourceAvailable(event) || m.validateRuntimeIdentity(r.id, rt.identity(g)) != nil {
				return
			}
			rt.mu.Lock()
			_, subscribed := rt.eventSubscriptions[event]
			same := rt.generation == g
			rt.mu.Unlock()
			if !same || !subscribed {
				return
			}
			parent := context.Background()
			if source != nil {
				parent = source.ctx
			}
			ctx, cancel := context.WithTimeout(parent, hostEventTimeout)
			defer cancel()
			if _, err := rt.callGeneration(ctx, g, hostEventMethod, params); err != nil {
				r.appendLog("warn", "event delivery failed ("+event+")")
			}
		}()
	}
}
