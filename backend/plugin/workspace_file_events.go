package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mincode/app/backend/filewatch"
	backendfs "mincode/app/backend/fs"
	"mincode/app/plugins/api"
)

const (
	hostEventFileCreated     = "workspace.fileCreated"
	hostEventFileDeleted     = "workspace.fileDeleted"
	hostEventFileRenamed     = "workspace.fileRenamed"
	hostEventRescanRequired  = "workspace.rescanRequired"
	workspaceEventPendingMax = 2048
	workspaceEventBatchMax   = 128
	workspaceEventDebounce   = 150 * time.Millisecond
	workspaceEventDedupeTTL  = 2 * time.Second
)

type workspaceRename struct {
	OldPath string `json:"oldPath"`
	NewPath string `json:"newPath"`
}
type workspaceEventBatch struct {
	Paths          []string          `json:"paths,omitempty"`
	Renames        []workspaceRename `json:"renames,omitempty"`
	ScopeRevision  string            `json:"scopeRevision"`
	Origin         string            `json:"origin"`
	RescanRequired bool              `json:"rescanRequired"`
	queuedAt       []time.Time       // host-only; aligns with paths OR rename pairs
}
type workspaceEventLoss struct {
	generation          uint64
	revision            string
	epoch, acknowledged uint64
	reason              string
}
type workspaceFileChange struct {
	event, origin, path string
	rename              workspaceRename
	queuedAt            time.Time
}
type workspaceFileSource struct {
	manager *pluginManager
	scope   workspaceScopeSnapshot
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	wake    chan struct{}
	watcher *filewatch.Watcher
	mu      sync.Mutex
	pending map[string]workspaceFileChange
	dedupe  map[string]time.Time
	reason  string
}

func isWorkspaceFileEvent(event string) bool {
	return event == hostEventFileCreated || event == hostEventFileDeleted || event == hostEventFileRenamed || event == hostEventRescanRequired
}
func init() {
	for _, event := range []string{hostEventFileCreated, hostEventFileDeleted, hostEventFileRenamed, hostEventRescanRequired} {
		hostEvents[event] = api.CapabilityWorkspaceRead
	}
}

// Requested names AND frozen canonical names must be safe. Never resolve an
// unknown vanished old name using the editor event path's permissive fallback.
func workspaceFilePathAllowed(root, relative, canonical string) bool {
	clean, err := backendfs.CleanProjectRelativePath(relative)
	if err != nil || clean != relative || len(relative) > hostEventPathMaxLength || canonical == "" || !filepath.IsAbs(canonical) {
		return false
	}
	resolved, err := filepath.Rel(root, canonical)
	if err != nil || resolved == "." || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
		return false
	}
	if filepath.Clean(canonical) != filepath.Join(root, filepath.FromSlash(relative)) {
		return false
	}
	for _, name := range []string{relative, filepath.ToSlash(resolved)} {
		if workspacePathDenied(name, true) {
			return false
		}
		for _, part := range strings.Split(name, "/") {
			if isFileTemplateSensitiveName(part) {
				return false
			}
			switch strings.ToLower(part) {
			case ".cache", "__pycache__", ".venv", ".gradle", ".next", ".nuxt", "target":
				return false
			}
		}
	}
	return true
}

// Only effective, enabled, actually running consumers keep the source alive.
func (m *pluginManager) hasWorkspaceFileSubscriber() bool {
	m.mu.RLock()
	records := m.orderedRecordsLocked()
	closed := m.shuttingDown
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
			_, allowed := rt.capabilities[api.CapabilityWorkspaceRead]
			if allowed && rt.cmd != nil && !rt.stopping {
				for event := range rt.eventSubscriptions {
					if isWorkspaceFileEvent(event) {
						rt.mu.Unlock()
						r.mu.Unlock()
						return true
					}
				}
			}
			rt.mu.Unlock()
		}
		r.mu.Unlock()
	}
	return false
}

// Serializes ownership changes, never called by a watcher emitter. A source
// owns its queue timer and joins its watcher on exit. No permanent poller.
func (m *pluginManager) syncWorkspaceFileEvents() error {
	m.syncGitStatusEvents()
	m.fileEventsMu.Lock()
	defer m.fileEventsMu.Unlock()
	if m.fileEventsUnsubscribe == nil {
		m.fileEventsUnsubscribe = m.OnWorkspaceScopeChange(func(workspaceScopeSnapshot) { m.syncWorkspaceFileEvents() })
	}
	scope := m.workspaceScope()
	active := scope.Ready && m.hasWorkspaceFileSubscriber()
	if current := m.fileEvents.Load(); current != nil {
		if active && current.scope == scope && current.ctx.Err() == nil {
			return nil
		}
		current.cancel()
		<-current.done
		m.fileEvents.Store(nil)
	}
	if !active {
		return nil
	}
	parent, err := m.workspaceScopeContext(scope)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	s := &workspaceFileSource{manager: m, scope: scope, ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), pending: map[string]workspaceFileChange{}, dedupe: map[string]time.Time{}}
	failed := func() error {
		cancel()
		// No source loop was started. Use the scope-owned context for bounded
		// delivery only; each RPC owns its own 3s child and no timer is retained.
		s.ctx = parent
		m.publishWorkspaceRescan(s, "sourceLost")
		return errors.New("workspace file observation source is unavailable")
	}
	watcher, err := filewatch.New(scope.Root, filewatch.Options{MaxFiles: 50000, MaxDirectories: 20000, MaxChanges: 2048, Debounce: workspaceEventDebounce, PollInterval: 2 * time.Second, Allow: func(relative, canonical string) bool {
		if relative == "." {
			return canonical == scope.Root
		}
		return workspaceFilePathAllowed(scope.Root, relative, canonical)
	}}, s.observeWatchBatch)
	if err != nil {
		return failed()
	}
	s.watcher = watcher
	if err = watcher.Register("plugin-workspace", []filewatch.Pattern{{Base: scope.Root, Match: func(string) bool { return true }, Kinds: 1 | 4}}); err != nil {
		_ = watcher.Close()
		return failed()
	}
	m.fileEvents.Store(s)
	go s.run()
	return nil
}
func (m *pluginManager) closeWorkspaceFileEvents() {
	m.closeGitStatusEvents()
	m.fileEventsMu.Lock()
	defer m.fileEventsMu.Unlock()
	if m.fileEventsUnsubscribe != nil {
		m.fileEventsUnsubscribe()
		m.fileEventsUnsubscribe = nil
	}
	if s := m.fileEvents.Load(); s != nil {
		s.cancel()
		<-s.done
		m.fileEvents.Store(nil)
	}
}

// ObserveWorkspaceMutation is injected by backend/register_all. fs remains
// independent of plugin. Unregister is not a cancellation/join barrier.
// Mutation callbacks must not initialize or wait for discovery of the lazy host.
var workspaceMutationHost atomic.Pointer[pluginManager]

func ObserveWorkspaceMutation(before backendfs.Mutation) func(backendfs.Mutation) {
	if manager := workspaceMutationHost.Load(); manager != nil {
		return manager.observeWorkspaceMutation(before)
	}
	return nil
}
func (m *pluginManager) observeWorkspaceMutation(before backendfs.Mutation) func(backendfs.Mutation) {
	// Preparation must not wait for watcher shutdown or baseline IO. A captured
	// source remains immutable and its context rejects late completions.
	s := m.fileEvents.Load()
	if s == nil || before.Root != s.scope.Root || s.ctx.Err() != nil {
		return nil
	}
	// Existence is captured BEFORE IO, not guessed from Kind or the completed
	// path. Ambiguous prestate yields rescan, never a fabricated create.
	existed, known := false, true
	if before.Kind == "write" || before.Kind == "paste" {
		target := before.NewCanonical
		if target == "" {
			known = false
		} else if _, err := os.Lstat(target); err == nil {
			existed = true
		} else if !os.IsNotExist(err) {
			known = false
		}
	}
	return func(after backendfs.Mutation) {
		if s.ctx.Err() != nil || m.workspaceScope() != s.scope {
			return
		}
		if before.IsDir {
			s.enqueue(nil, "sourceLost")
			return
		}
		oldOK := workspaceFilePathAllowed(s.scope.Root, before.OldPath, before.OldCanonical)
		newOK := workspaceFilePathAllowed(s.scope.Root, before.NewPath, after.NewCanonical)
		change := workspaceFileChange{origin: "host"}
		switch before.Kind {
		case "write", "paste":
			if !known {
				s.enqueue(nil, "sourceLost")
				return
			}
			if existed || !newOK {
				return
			}
			change.event, change.path = hostEventFileCreated, before.NewPath
		case "delete":
			if before.OldCanonical == "" {
				s.enqueue(nil, "sourceLost")
				return
			}
			if !oldOK {
				return
			}
			change.event, change.path = hostEventFileDeleted, before.OldPath
		case "rename":
			if before.OldCanonical == "" {
				s.enqueue(nil, "sourceLost")
				return
			}
			switch {
			case oldOK && newOK:
				change.event, change.rename = hostEventFileRenamed, workspaceRename{before.OldPath, before.NewPath}
			case oldOK:
				change.event, change.path = hostEventFileDeleted, before.OldPath
			case newOK:
				change.event, change.path = hostEventFileCreated, before.NewPath
			default:
				return
			}
		default:
			return
		}
		s.enqueue([]workspaceFileChange{change}, "")
	}
}

func (s *workspaceFileSource) observeWatchBatch(batch filewatch.Batch) {
	if s.ctx.Err() != nil {
		return
	}
	changes := make([]workspaceFileChange, 0, len(batch.Changes))
	reason := batch.RescanReason
	for _, c := range batch.Changes {
		switch c.Kind {
		case filewatch.Created:
			file, err := backendfs.ResolveProjectFile(s.scope.Root, c.Path)
			if err != nil {
				reason = "sourceLost"
				continue
			}
			if workspaceFilePathAllowed(s.scope.Root, c.Path, file.Path) && file.RelativePath == file.ResolvedRelativePath {
				changes = append(changes, workspaceFileChange{event: hostEventFileCreated, origin: "watcher", path: c.Path})
			}
		case filewatch.Deleted:
			if c.Before == nil || c.Before.Path != c.Path || c.Before.Canonical == "" {
				reason = "sourceLost"
				continue
			}
			if workspaceFilePathAllowed(s.scope.Root, c.Path, c.Before.Canonical) {
				changes = append(changes, workspaceFileChange{event: hostEventFileDeleted, origin: "watcher", path: c.Path})
			}
		}
	}
	s.enqueue(changes, reason)
}
func fileChangeKey(c workspaceFileChange) string {
	return c.event + "\x00" + c.path + "\x00" + c.rename.OldPath + "\x00" + c.rename.NewPath
}
func (s *workspaceFileSource) enqueue(changes []workspaceFileChange, reason string) {
	s.manager.mu.RLock()
	closed := s.manager.shuttingDown
	s.manager.mu.RUnlock()
	if closed {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || s.manager.workspaceScope() != s.scope {
		return
	}
	now := time.Now()
	for key, expiry := range s.dedupe {
		if !now.Before(expiry) {
			delete(s.dedupe, key)
		}
	}
	if reason != "" {
		s.reason = reason
	}
	for _, c := range changes {
		key := fileChangeKey(c)
		if c.origin == "watcher" {
			if _, seen := s.dedupe[key]; seen {
				continue
			}
		}
		if c.event == hostEventFileRenamed && c.origin == "host" {
			// A host pair supersedes same-window external delete/create on either side.
			for _, side := range []workspaceFileChange{{event: hostEventFileDeleted, path: c.rename.OldPath}, {event: hostEventFileCreated, path: c.rename.NewPath}} {
				sideKey := fileChangeKey(side)
				delete(s.pending, sideKey)
				s.remember(sideKey, now)
			}
		}
		if existing, ok := s.pending[key]; ok && existing.origin == "host" {
			continue
		}
		if len(s.pending) >= workspaceEventPendingMax {
			s.reason = "overflow"
			continue
		}
		c.queuedAt = now
		s.pending[key] = c
		if c.origin == "host" {
			s.remember(key, now)
		}
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *workspaceFileSource) remember(key string, now time.Time) {
	if len(s.dedupe) >= workspaceEventPendingMax { // Bounded eviction, no unbounded history.
		var oldest string
		var expiry time.Time
		for k, v := range s.dedupe {
			if oldest == "" || v.Before(expiry) {
				oldest, expiry = k, v
			}
		}
		delete(s.dedupe, oldest)
	}
	s.dedupe[key] = now.Add(workspaceEventDedupeTTL)
}
func (s *workspaceFileSource) run() {
	defer close(s.done)
	defer func() {
		s.mu.Lock()
		s.pending, s.dedupe, s.reason = nil, nil, ""
		s.mu.Unlock()
	}()
	defer s.watcher.Close()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		}
		timer := time.NewTimer(workspaceEventDebounce)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.flush()
	}
}
func (s *workspaceFileSource) flush() {
	s.mu.Lock()
	pending, reason := s.pending, s.reason
	s.pending = map[string]workspaceFileChange{}
	s.reason = ""
	s.mu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	if reason != "" {
		s.manager.publishWorkspaceRescan(s, reason)
	}
	keys := make([]string, 0, len(pending))
	for key := range pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var batch workspaceEventBatch
	event := ""
	send := func() {
		if event != "" {
			s.manager.publishWorkspaceFileBatch(s, event, batch)
		}
	}
	for _, key := range keys {
		c := pending[key]
		if event != c.event || batch.Origin != c.origin {
			send()
			event = c.event
			batch = workspaceEventBatch{ScopeRevision: s.scope.Revision, Origin: c.origin}
		}
		candidate := batch
		candidate.Paths = append(append([]string(nil), batch.Paths...), c.path)
		if c.event == hostEventFileRenamed {
			candidate.Paths = nil
			candidate.Renames = append(append([]workspaceRename(nil), batch.Renames...), c.rename)
		}
		encoded, _ := json.Marshal(map[string]any{"event": event, "data": candidate})
		if len(candidate.Paths)+len(candidate.Renames) > workspaceEventBatchMax || len(encoded) >= hostEventParamsMaxBytes-64 {
			send()
			batch = workspaceEventBatch{ScopeRevision: s.scope.Revision, Origin: c.origin}
		}
		batch.queuedAt = append(batch.queuedAt, c.queuedAt)
		if c.event == hostEventFileRenamed {
			batch.Renames = append(batch.Renames, c.rename)
		} else {
			batch.Paths = append(batch.Paths, c.path)
		}
	}
	send()
}

func (m *pluginManager) workspaceFileRecipients(s *workspaceFileSource, fn func(*pluginRecord, *processRuntime, uint64)) {
	if s.ctx.Err() != nil || m.workspaceScope() != s.scope {
		return
	}
	m.mu.RLock()
	records := m.orderedRecordsLocked()
	closed := m.shuttingDown
	m.mu.RUnlock()
	if closed {
		return
	}
	for _, r := range records {
		r.mu.Lock()
		rt := r.runtime
		ok := r.enabled && r.state == pluginStateRunning && m.workspaceActiveLocked(r) && rt != nil
		r.mu.Unlock()
		if !ok {
			continue
		}
		rt.mu.Lock()
		_, allowed := rt.capabilities[api.CapabilityWorkspaceRead]
		generation := rt.generation
		running := rt.cmd != nil && !rt.stopping
		rt.mu.Unlock()
		if allowed && running {
			fn(r, rt, generation)
		}
	}
}
func (rt *processRuntime) fileLossLocked(generation uint64, revision string) *workspaceEventLoss {
	if rt.fileEventLoss.generation != generation || rt.fileEventLoss.revision != revision {
		rt.fileEventLoss = workspaceEventLoss{generation: generation, revision: revision}
	}
	return &rt.fileEventLoss
}
func (m *pluginManager) publishWorkspaceRescan(s *workspaceFileSource, reason string) {
	m.workspaceFileRecipients(s, func(r *pluginRecord, rt *processRuntime, g uint64) {
		rt.mu.Lock()
		if rt.generation != g || s.ctx.Err() != nil || m.workspaceScope() != s.scope {
			rt.mu.Unlock()
			return
		}
		loss := rt.fileLossLocked(g, s.scope.Revision)
		loss.epoch++
		loss.reason = reason
		rt.mu.Unlock()
		if rt.subscribedTo(hostEventRescanRequired) {
			m.deliverWorkspaceFileEvent(s, r, rt, g, hostEventRescanRequired, map[string]any{"scopeRevision": s.scope.Revision, "reason": reason})
		}
	})
}
func (m *pluginManager) publishWorkspaceFileBatch(s *workspaceFileSource, event string, batch workspaceEventBatch) {
	m.workspaceFileRecipients(s, func(r *pluginRecord, rt *processRuntime, g uint64) {
		if !rt.subscribedTo(event) {
			return
		}
		rt.mu.Lock()
		if rt.generation != g || s.ctx.Err() != nil || m.workspaceScope() != s.scope {
			rt.mu.Unlock()
			return
		}
		loss := rt.fileLossLocked(g, s.scope.Revision)
		delivery := batch
		delivery.RescanRequired = loss.epoch != loss.acknowledged
		reason, subscribedAt := loss.reason, rt.fileEventsSubscribedAt
		rt.mu.Unlock()
		if len(batch.queuedAt) > 0 {
			delivery.Paths, delivery.Renames = nil, nil
			for i, queuedAt := range batch.queuedAt {
				if queuedAt.Before(subscribedAt) {
					continue
				}
				if event == hostEventFileRenamed {
					delivery.Renames = append(delivery.Renames, batch.Renames[i])
				} else {
					delivery.Paths = append(delivery.Paths, batch.Paths[i])
				}
			}
			if len(delivery.Paths)+len(delivery.Renames) == 0 {
				return
			}
		}
		m.deliverWorkspaceFileEvent(s, r, rt, g, event, delivery)
		if delivery.RescanRequired && rt.subscribedTo(hostEventRescanRequired) {
			m.deliverWorkspaceFileEvent(s, r, rt, g, hostEventRescanRequired, map[string]any{"scopeRevision": s.scope.Revision, "reason": reason})
		}
	})
}
func (m *pluginManager) deliverWorkspaceFileEvent(s *workspaceFileSource, r *pluginRecord, rt *processRuntime, g uint64, event string, data any) {
	informed := event == hostEventRescanRequired
	if batch, ok := data.(workspaceEventBatch); ok {
		informed = batch.RescanRequired
	}
	rt.mu.Lock()
	if rt.generation != g || s.ctx.Err() != nil || m.workspaceScope() != s.scope {
		rt.mu.Unlock()
		return
	}
	epoch := rt.fileLossLocked(g, s.scope.Revision).epoch
	rt.mu.Unlock()
	lost := func() {
		if s.ctx.Err() != nil {
			return
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if rt.generation != g || s.ctx.Err() != nil || m.workspaceScope() != s.scope {
			return
		}
		loss := rt.fileLossLocked(g, s.scope.Revision)
		loss.epoch++
		loss.reason = "deliveryLost"
	}
	params, err := json.Marshal(map[string]any{"event": event, "data": data})
	if err != nil || len(params) >= hostEventParamsMaxBytes {
		lost()
		return
	}
	if rt.eventsInflight.Add(1) > hostEventInflightMax {
		rt.eventsInflight.Add(-1)
		lost()
		r.appendLog("warn", "event dropped while the plugin was busy: "+event)
		return
	}
	go func() {
		defer rt.eventsInflight.Add(-1)
		// Recheck generation, record admission and scope at the asynchronous boundary.
		if s.ctx.Err() != nil || m.workspaceScope() != s.scope || m.validateRuntimeIdentity(r.id, rt.identity(g)) != nil {
			return
		}
		ctx, cancel := context.WithTimeout(s.ctx, hostEventTimeout)
		defer cancel()
		if _, err := rt.callGeneration(ctx, g, hostEventMethod, params); err != nil {
			lost()
			r.appendLog("warn", "event delivery failed ("+event+"): "+err.Error())
			return
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if rt.generation == g && s.ctx.Err() == nil && m.workspaceScope() == s.scope {
			loss := rt.fileLossLocked(g, s.scope.Revision)
			if informed && loss.epoch == epoch {
				loss.acknowledged = epoch
			}
		}
	}()
}
