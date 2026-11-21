// Package filewatch provides bounded filesystem snapshots and native change sources.
// It has no dependency on a consumer protocol or filesystem mutation service.
package filewatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	Created uint8 = 1
	Changed uint8 = 2
	Deleted uint8 = 3
	// WatchKind bits are not Change kinds: delete is bit 4, not value 3.
	watchKindCreate            uint8 = 1
	watchKindChange            uint8 = 2
	watchKindDelete            uint8 = 4
	maxWatchRegistrations            = 64
	maxWatchersPerRegistration       = 256
	defaultMaxFiles                  = 50_000
	defaultMaxDirectories            = 20_000
	// maxPollDelay caps the backoff of polling rescans of a slow tree.
	maxPollDelay = 5 * time.Minute
)

type bounds struct{ files, directories, scanEntries int }

var (
	// A polling rescan reads the whole root every interval.
	pollingBounds = bounds{defaultMaxFiles, defaultMaxDirectories, defaultMaxFiles + defaultMaxDirectories}
	// A native source rescans only changed directories after the baseline;
	// 200k directories cost roughly 100 MiB of snapshot (measured ~17 MiB for
	// 35k directories and 14k matching files, a 4 s one-off baseline scan).
	nativeBounds = bounds{500_000, 200_000, 2_000_000}
)

type Options struct {
	MaxFiles, MaxDirectories, MaxChanges int
	// MaxScanEntries bounds the directory entries one scan may read, matching
	// or not (scan work, not snapshot memory). Default MaxFiles+MaxDirectories.
	MaxScanEntries         int
	PollInterval, Debounce time.Duration
	// Allow receives a slash-separated root-relative path and its verified
	// canonical path. Directories are checked before descending as well.
	Allow func(relative, canonical string) bool
}

type Pattern struct {
	// Base is an absolute directory; empty matches both absolute and relative paths.
	Base  string
	Match func(path string) bool
	Kinds uint8
}

type Entry struct{ Path, Canonical string }
type Change struct {
	Path   string
	Kind   uint8
	Before *Entry
}
type Batch struct {
	Changes      []Change
	RescanReason string
}

// Moved from LSP: dependencies, VCS internals and caches are not descended.
var watchIgnoredDirectories = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true,
	".cache": true, "__pycache__": true, ".venv": true, ".gradle": true,
	".next": true, ".nuxt": true, "target": true,
}

type watchSource interface{ close() }
type watchChanged func(path string, recursive bool, reason string)
type fileStamp struct {
	size, modified int64
	entry          Entry
}
type watchedDir struct {
	files    map[string]fileStamp
	children map[string]struct{}
}

// Watcher owns one root. Callbacks execute serially on its loop and must return
// promptly; they must not synchronously call Close. Close joins the loop and
// drains native callbacks. Register/Unregister may be called concurrently.
type Watcher struct {
	root                           string
	options                        Options
	emit                           func(Batch)
	start                          func(string, watchChanged) (watchSource, error)
	mu                             sync.Mutex
	registrations                  map[string][]Pattern
	baselines                      map[string]*Baseline
	dirty                          map[string]bool
	rebaseline                     bool
	source                         watchSource
	sourceDrains                   sync.WaitGroup
	polling, closed, loopStarted   bool
	wake, done, stopped, closeDone chan struct{}
	pendingReason                  string
	// Owned by the loop goroutine.
	patterns    []Pattern
	dirs        map[string]*watchedDir
	fileCount   int
	scanEntries int
	truncated   bool
	reason      string
}

func New(root string, options Options, emit func(Batch)) (*Watcher, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("watch root is not a directory")
	}
	if options.MaxFiles <= 0 {
		options.MaxFiles = defaultMaxFiles
	}
	if options.MaxDirectories <= 0 {
		options.MaxDirectories = defaultMaxDirectories
	}
	if options.MaxScanEntries <= 0 {
		options.MaxScanEntries = options.MaxFiles + options.MaxDirectories
	}
	// Without a native source every change rescans the whole root, so only
	// a native source may hold a larger snapshot.
	ceiling := pollingBounds
	if NativeSource {
		ceiling = nativeBounds
	}
	options.MaxFiles = min(options.MaxFiles, ceiling.files)
	options.MaxDirectories = min(options.MaxDirectories, ceiling.directories)
	options.MaxScanEntries = min(options.MaxScanEntries, ceiling.scanEntries)
	if options.MaxChanges <= 0 {
		options.MaxChanges = 2048
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 2 * time.Second
	}
	if options.Debounce <= 0 {
		options.Debounce = 150 * time.Millisecond
	}
	if emit == nil {
		return nil, errors.New("watch emitter is required")
	}
	return &Watcher{root: canonical, options: options, emit: emit, start: startNativeWatch,
		registrations: map[string][]Pattern{}, baselines: map[string]*Baseline{}, dirty: map[string]bool{}, dirs: map[string]*watchedDir{},
		wake: make(chan struct{}, 1), done: make(chan struct{}), stopped: make(chan struct{}), closeDone: make(chan struct{})}, nil
}

var (
	ErrWatcherClosed        = errors.New("file watcher is closed")
	ErrRegistrationRemoved  = errors.New("file watcher registration was removed")
	ErrRegistrationReplaced = errors.New("file watcher registration was replaced")
	ErrBaselineIncomplete   = errors.New("file watcher baseline is incomplete")
	// ErrBaselineTruncated (wrapped with ErrBaselineIncomplete) means the root
	// holds more entries than the configured bounds, not that it was lost.
	ErrBaselineTruncated = errors.New("file watcher baseline exceeds the watch limits")
)

// Baseline binds a wait to one registration revision (the ticket's identity).
// Replacement/removal/Close release waiters; a scan of an older revision cannot
// complete this ticket. Waiting is optional and never holds a watcher lock.
// Completion is a bounded initial snapshot, not a guarantee of a complete
// filesystem event log or of future source availability.
type Baseline struct {
	watcher  *Watcher
	id       string
	ready    chan struct{}
	resolved bool // protected by watcher.mu
	err      error
}

// Wait cancels only the wait, not the registration. Remove may be used to undo
// this revision without accidentally unregistering a later replacement.
func (b *Baseline) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.watcher.done:
		return ErrWatcherClosed
	case <-b.ready:
	}
	w := b.watcher
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrWatcherClosed
	}
	if current := w.baselines[b.id]; current != b {
		if current == nil {
			return ErrRegistrationRemoved
		}
		return ErrRegistrationReplaced
	}
	return b.err
}

func (b *Baseline) Remove() { b.watcher.unregister(b.id, b) }

func (w *Watcher) resolveBaselineLocked(b *Baseline, err error) {
	if b != nil && !b.resolved {
		b.resolved, b.err = true, err
		close(b.ready)
	}
}

func (w *Watcher) completeBaselines(scanned []*Baseline, reason string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if w.pendingReason != "" {
		reason = w.pendingReason
	}
	var err error
	switch reason {
	case "":
	case "truncated":
		err = fmt.Errorf("%w: %w", ErrBaselineIncomplete, ErrBaselineTruncated)
	default:
		err = fmt.Errorf("%w: %s", ErrBaselineIncomplete, reason)
	}
	for _, b := range scanned {
		if w.baselines[b.id] == b {
			w.resolveBaselineLocked(b, err)
		}
	}
}

// Register remains non-blocking; consumers which acknowledge readiness use
// RegisterBaseline and wait on the returned registration-specific ticket.
func (w *Watcher) Register(id string, patterns []Pattern) error {
	_, err := w.RegisterBaseline(id, patterns)
	return err
}

func (w *Watcher) RegisterBaseline(id string, patterns []Pattern) (*Baseline, error) {
	if len(patterns) > maxWatchersPerRegistration {
		return nil, errors.New("too many file watchers in one registration")
	}
	for _, p := range patterns {
		if p.Match == nil || (p.Base != "" && (!filepath.IsAbs(p.Base) || !pathInsideRoot(w.root, p.Base))) {
			return nil, errors.New("invalid watch pattern")
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, ErrWatcherClosed
	}
	if _, exists := w.registrations[id]; !exists && len(w.registrations) >= maxWatchRegistrations {
		return nil, errors.New("too many file watcher registrations")
	}
	w.resolveBaselineLocked(w.baselines[id], ErrRegistrationReplaced)
	baseline := &Baseline{watcher: w, id: id, ready: make(chan struct{})}
	w.baselines[id] = baseline
	w.registrations[id] = append([]Pattern(nil), patterns...)
	w.rebaseline = true
	if w.source == nil && !w.polling {
		source, err := w.start(w.root, w.nativeChanged)
		if err != nil || source == nil {
			w.polling = true
			if err != nil {
				w.pendingReason = "sourceLost"
			}
		} else {
			w.source = source
		}
	}
	if !w.loopStarted {
		w.loopStarted = true
		go w.loop()
	}
	w.signalLocked()
	return baseline, nil
}

func (w *Watcher) Unregister(id string) { w.unregister(id, nil) }

func (w *Watcher) unregister(id string, expected *Baseline) {
	w.mu.Lock()
	if _, exists := w.registrations[id]; !exists || w.closed || (expected != nil && w.baselines[id] != expected) {
		w.mu.Unlock()
		return
	}
	w.resolveBaselineLocked(w.baselines[id], ErrRegistrationRemoved)
	delete(w.baselines, id)
	delete(w.registrations, id)
	w.rebaseline = true
	var idle watchSource
	if len(w.registrations) == 0 {
		idle = w.detachSourceLocked()
		if idle != nil {
			w.sourceDrains.Add(1)
		}
	}
	w.signalLocked()
	w.mu.Unlock()
	if idle != nil {
		defer w.sourceDrains.Done()
		closeWatchSource(idle)
	}
}

// RequestClose cancels without joining, so an emitter can request its own
// shutdown. The existing loop owns final cleanup; no shutdown goroutine is added.
// Call Close outside the emitter to wait for the loop and all native drains.
func (w *Watcher) RequestClose() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	for _, b := range w.baselines {
		w.resolveBaselineLocked(b, ErrWatcherClosed)
	}
	close(w.done)
	started := w.loopStarted
	w.mu.Unlock()
	if !started {
		w.finishClose()
	}
}

func (w *Watcher) Close() error {
	w.RequestClose()
	<-w.closeDone
	return nil
}

func (w *Watcher) finishClose() {
	w.mu.Lock()
	source := w.detachSourceLocked()
	w.mu.Unlock()
	closeWatchSource(source)
	// Add happens under mu before Unregister releases a source. Once closed is
	// set, no new drain can start, so Wait also covers previously detached ones.
	w.sourceDrains.Wait()
	close(w.stopped)
	close(w.closeDone)
}
func (w *Watcher) detachSourceLocked() watchSource {
	source := w.source
	w.source = nil
	w.polling = false
	return source
}
func closeWatchSource(source watchSource) {
	if source != nil {
		source.close()
	}
}

func (w *Watcher) nativeChanged(path string, recursive bool, reason string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if reason != "" {
		w.pendingReason = reason
		if reason == "sourceLost" {
			w.polling = true
		}
		path = w.root
		recursive = true
	}
	w.markDirtyLocked(path, recursive)
}

// RequestRescan marks the current bounded source lost (for example after
// resume). It neither creates a watcher nor expands the configured bounds.
func (w *Watcher) RequestRescan() { w.nativeChanged(w.root, true, "sourceLost") }

func (w *Watcher) markDirty(path string, recursive bool) { w.nativeChanged(path, recursive, "") }
func (w *Watcher) markDirtyLocked(path string, recursive bool) {
	if !pathInsideRoot(w.root, path) {
		return
	}
	if len(w.dirty) >= w.options.MaxChanges {
		w.dirty = map[string]bool{w.root: true}
		w.pendingReason = "overflow"
	} else {
		w.dirty[path] = w.dirty[path] || recursive
	}
	w.signalLocked()
}
func (w *Watcher) signalLocked() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *Watcher) loop() {
	defer w.finishClose()
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	// A polling rescan waits at least a few times its own last duration, so a
	// large tree is never rescanned back to back.
	var nextPoll time.Time
	for {
		select {
		case <-w.done:
			return
		case <-w.wake:
		case <-ticker.C:
			w.mu.Lock()
			polling := w.polling
			w.mu.Unlock()
			if !polling || time.Now().Before(nextPoll) {
				continue
			}
			w.markDirty(w.root, true)
		}
		timer := time.NewTimer(w.options.Debounce)
		select {
		case <-w.done:
			timer.Stop()
			return
		case <-timer.C:
		}
		started := time.Now()
		w.process()
		// Only the part beyond the regular interval delays the ticker: a fast
		// scan never skips a tick.
		nextPoll = started.Add(pollDelay(w.options.PollInterval, time.Since(started)) - w.options.PollInterval)
	}
}

// pollDelay is the wait before the next polling rescan after one took scan.
func pollDelay(interval, scan time.Duration) time.Duration {
	return min(max(interval, 5*scan), maxPollDelay)
}

// Registration changes silently rebuild the bounded baseline, just as in LSP.
func (w *Watcher) process() {
	w.scanEntries = 0
	w.mu.Lock()
	active := len(w.registrations) > 0
	rebaseline := w.rebaseline
	w.rebaseline = false
	dirty := w.dirty
	w.dirty = map[string]bool{}
	w.reason = w.pendingReason
	w.pendingReason = ""
	var scanned []*Baseline
	if rebaseline {
		w.patterns = nil
		ids := make([]string, 0, len(w.registrations))
		for id := range w.registrations {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			w.patterns = append(w.patterns, w.registrations[id]...)
			if b := w.baselines[id]; b != nil {
				scanned = append(scanned, b)
			}
		}
	}
	w.mu.Unlock()
	if !active {
		w.dirs = map[string]*watchedDir{}
		w.fileCount = 0
		w.truncated = false
		return
	}
	if rebaseline {
		w.dirs = map[string]*watchedDir{}
		w.fileCount = 0
		w.truncated = false
		w.scan(w.root, true, nil)
		// Capture the reason before flush clears it. Publish readiness before
		// a consumer callback: that callback may write to the registering peer.
		w.completeBaselines(scanned, w.reason)
		w.flush(nil)
		return
	}
	var changes []Change
	for _, path := range collapseDirty(dirty) {
		if !pathInsideRoot(w.root, path) || w.ignored(path) {
			continue
		}
		recursive := dirty[path]
		if _, known := w.dirs[path]; !known {
			if _, err := os.Lstat(path); err != nil && !w.knownFile(path) {
				w.reason = "sourceLost"
			}
			path, recursive = w.nearestKnown(path), false
		}
		w.scan(path, recursive, &changes)
	}
	w.flush(changes)
}
func (w *Watcher) knownFile(path string) bool {
	dir := w.dirs[filepath.Dir(path)]
	if dir == nil {
		return false
	}
	_, ok := dir.files[filepath.Base(path)]
	return ok
}
func (w *Watcher) ignored(path string) bool {
	relative, err := filepath.Rel(w.root, path)
	if err != nil || relative == "." {
		return false
	}
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if watchIgnoredDirectories[segment] {
			return true
		}
	}
	return false
}
func (w *Watcher) nearestKnown(path string) string {
	for path != w.root {
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
		if _, known := w.dirs[path]; known {
			return path
		}
	}
	return w.root
}
func collapseDirty(dirty map[string]bool) []string {
	paths := make([]string, 0, len(dirty))
	for path := range dirty {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := paths[:0]
	var recursiveAncestors []string
	for _, path := range paths {
		covered := false
		for _, ancestor := range recursiveAncestors {
			if path != ancestor && pathInsideRoot(ancestor, path) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		result = append(result, path)
		if dirty[path] {
			recursiveAncestors = append(recursiveAncestors, path)
		}
	}
	return result
}

// safeEntry refuses every symlink component, including directories swapped since
// the last scan. It does not promise an OS filesystem sandbox against TOCTOU.
func (w *Watcher) safeEntry(path string) (Entry, bool) {
	rel, err := filepath.Rel(w.root, path)
	if err != nil || !pathInsideRoot(w.root, path) {
		return Entry{}, false
	}
	current := w.root
	parts := []string{}
	if rel != "." {
		parts = strings.Split(rel, string(filepath.Separator))
	}
	for _, part := range append([]string{""}, parts...) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&fs.ModeSymlink != 0 {
			return Entry{}, false
		}
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || !pathInsideRoot(w.root, canonical) {
		return Entry{}, false
	}
	relative := filepath.ToSlash(rel)
	if w.options.Allow != nil && !w.options.Allow(relative, canonical) {
		return Entry{}, false
	}
	return Entry{Path: relative, Canonical: canonical}, true
}

// Moved from LSP. Directory reads are now chunked instead of allocating an
// arbitrarily large listing. Snapshot, seen sets, dirty paths and output buffers
// all remain bounded. Close is checked during scans, not just between scans.
func (w *Watcher) scan(dir string, recursive bool, changes *[]Change) {
	select {
	case <-w.done:
		return
	default:
	}
	if w.scanEntries >= w.options.MaxScanEntries {
		w.truncate()
		return
	}
	if _, safe := w.safeEntry(dir); !safe {
		// An absent, symlinked or policy-denied root is not a successfully
		// scanned empty tree. Polling sources have no native loss hint to
		// supply this reason before baseline completion. Excluded children
		// remain normal policy filtering, not a whole-root source failure.
		if dir == w.root {
			w.reason = "sourceLost"
		}
		w.removeTree(dir, changes)
		if parent := w.dirs[filepath.Dir(dir)]; parent != nil && dir != w.root {
			delete(parent.children, filepath.Base(dir))
		}
		return
	}
	known := w.dirs[dir]
	if known == nil {
		if len(w.dirs) >= w.options.MaxDirectories {
			w.truncate()
			return
		}
		known = &watchedDir{files: map[string]fileStamp{}, children: map[string]struct{}{}}
		w.dirs[dir] = known
	}
	seenFiles := make(map[string]struct{}, len(known.files))
	seenDirs := make(map[string]struct{}, len(known.children))
	type subtree struct {
		path      string
		recursive bool
	}
	var descend []subtree
	// Close the listing handle BEFORE recursing: a deep tree must not hold one
	// open descriptor per ancestor. All skipped entries also consume work budget.
	complete := func() bool {
		handle, err := os.Open(dir)
		if err != nil {
			w.scanFailure(dir, err, changes)
			return false
		}
		defer handle.Close()
		for {
			entries, readErr := handle.ReadDir(256)
			for _, entry := range entries {
				select {
				case <-w.done:
					return false
				default:
				}
				if w.scanEntries >= w.options.MaxScanEntries {
					w.truncate()
					return false
				}
				w.scanEntries++
				name := entry.Name()
				full := filepath.Join(dir, name)
				mode := entry.Type()
				if mode&fs.ModeSymlink != 0 {
					continue
				}
				if entry.IsDir() {
					if watchIgnoredDirectories[name] {
						continue
					}
					if _, safe := w.safeEntry(full); !safe {
						continue
					}
					if _, descended := known.children[name]; !descended {
						if len(w.dirs) >= w.options.MaxDirectories {
							w.truncate()
							continue
						}
						w.dirs[full] = &watchedDir{files: map[string]fileStamp{}, children: map[string]struct{}{}}
						known.children[name] = struct{}{}
						descend = append(descend, subtree{full, true})
					} else if recursive {
						descend = append(descend, subtree{full, true})
					}
					seenDirs[name] = struct{}{}
					continue
				}
				if !mode.IsRegular() {
					continue
				}
				kinds := w.matchKinds(full)
				if kinds == 0 {
					continue
				}
				safe, safeOK := w.safeEntry(full)
				if !safeOK {
					continue
				}
				info, err := entry.Info()
				if err != nil || !info.Mode().IsRegular() {
					continue
				}
				stamp := fileStamp{size: info.Size(), modified: info.ModTime().UnixNano(), entry: safe}
				previous, existed := known.files[name]
				if !existed {
					if w.fileCount >= w.options.MaxFiles {
						w.truncate()
						continue
					}
					w.fileCount++
					known.files[name] = stamp
					w.record(changes, full, kinds, watchKindCreate, Created, nil)
				} else if previous != stamp {
					known.files[name] = stamp
					w.record(changes, full, kinds, watchKindChange, Changed, nil)
				}
				seenFiles[name] = struct{}{}
			}
			if readErr != nil {
				if readErr != io.EOF {
					w.scanFailure(dir, readErr, changes)
					return false
				}
				return true
			}
		}
	}()
	if complete {
		for name, previous := range known.files {
			if _, present := seenFiles[name]; !present {
				delete(known.files, name)
				w.fileCount--
				full := filepath.Join(dir, name)
				before := previous.entry
				w.record(changes, full, w.matchKinds(full), watchKindDelete, Deleted, &before)
			}
		}
		for name := range known.children {
			if _, present := seenDirs[name]; !present {
				delete(known.children, name)
				w.removeTree(filepath.Join(dir, name), changes)
			}
		}
	}
	// A partial listing must not turn unseen but previously safe entries into
	// fabricated deletes. The rescan reason communicates the observation gap.
	for _, child := range descend {
		w.scan(child.path, child.recursive, changes)
	}
}

// Preserve LSP's handling of vanished/replaced/unreadable directories; transient
// errors keep the snapshot. Only previously recorded safe files can be deleted.
func (w *Watcher) scanFailure(dir string, err error, changes *[]Change) {
	w.reason = "sourceLost"
	info, statErr := os.Lstat(dir)
	if statErr != nil || !info.IsDir() || errors.Is(err, fs.ErrPermission) {
		w.removeTree(dir, changes)
		if parent := w.dirs[filepath.Dir(dir)]; parent != nil && dir != w.root {
			delete(parent.children, filepath.Base(dir))
		}
	}
}
func (w *Watcher) removeTree(dir string, changes *[]Change) {
	known := w.dirs[dir]
	if known == nil {
		return
	}
	for name, previous := range known.files {
		w.fileCount--
		full := filepath.Join(dir, name)
		before := previous.entry
		w.record(changes, full, w.matchKinds(full), watchKindDelete, Deleted, &before)
	}
	for name := range known.children {
		w.removeTree(filepath.Join(dir, name), changes)
	}
	delete(w.dirs, dir)
}
func (w *Watcher) truncate() { w.truncated = true; w.reason = "truncated" }
func (w *Watcher) record(changes *[]Change, path string, kinds, required, kind uint8, before *Entry) {
	if changes == nil || kinds&required == 0 {
		return
	}
	rel, err := filepath.Rel(w.root, path)
	if err != nil {
		return
	}
	if len(*changes) == w.options.MaxChanges {
		w.flush(*changes)
		*changes = nil
	}
	*changes = append(*changes, Change{Path: filepath.ToSlash(rel), Kind: kind, Before: before})
}
func (w *Watcher) matchKinds(path string) uint8 {
	var kinds uint8
	var absolute, relative string
	for _, pattern := range w.patterns {
		if kinds|pattern.Kinds == kinds {
			continue
		}
		if pattern.Base != "" {
			rel, err := filepath.Rel(pattern.Base, path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			if pattern.Match(filepath.ToSlash(rel)) {
				kinds |= pattern.Kinds
			}
			continue
		}
		if absolute == "" {
			absolute = filepath.ToSlash(path)
			if rel, err := filepath.Rel(w.root, path); err == nil {
				relative = filepath.ToSlash(rel)
			}
		}
		if pattern.Match(absolute) || (relative != "" && pattern.Match(relative)) {
			kinds |= pattern.Kinds
		}
	}
	return kinds
}

// Stream bounded chunks, retaining the original LSP behavior of delivering all
// changes in notifications of at most MaxChanges rather than dropping a tail.
func (w *Watcher) flush(changes []Change) {
	if len(changes) > 0 || w.reason != "" {
		w.emit(Batch{Changes: changes, RescanReason: w.reason})
		w.reason = ""
	}
}
func pathInsideRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
