package fs

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Mutation contains metadata only, never file content. Paths are slash-separated
// root-relative requested names; Canonical fields are independently resolved.
// Kind is write, mkdir, rename, delete or paste. An empty canonical value means
// no safe regular entry is known (notably symlinks). Directory operations are a
// rescan hint, not an enumeration of their descendants.
type Mutation struct {
	Root, OldPath, NewPath, OldCanonical, NewCanonical string
	Kind                                               string
	IsDir                                              bool
}

const mutationObserverTimeout = 25 * time.Millisecond

type mutationObserver struct {
	prepare func(Mutation) func(Mutation)
	// At most one callback per observer can remain running after a timeout.
	busy chan struct{}
}

var mutationObservers = struct {
	sync.Mutex
	next    uint64
	entries map[uint64]*mutationObserver
}{entries: map[uint64]*mutationObserver{}}

// OnMutation registers a two-phase observer. Prepare runs after validation and
// before IO; its returned callback runs only after successful IO, outside locks.
// Panics/timeouts are isolated from IO results. A timed-out/busy observer misses
// the operation. Observers must eventually return: Go cannot kill arbitrary
// callback code. Unregister prevents future preparation, not in-flight callbacks.
func OnMutation(prepare func(Mutation) func(Mutation)) func() {
	if prepare == nil {
		return func() {}
	}
	observer := &mutationObserver{prepare: prepare, busy: make(chan struct{}, 1)}
	mutationObservers.Lock()
	mutationObservers.next++
	id := mutationObservers.next
	mutationObservers.entries[id] = observer
	mutationObservers.Unlock()
	return func() { mutationObservers.Lock(); delete(mutationObservers.entries, id); mutationObservers.Unlock() }
}
func (o *mutationObserver) call(callback func()) bool {
	select {
	case o.busy <- struct{}{}:
	default:
		return false
	}
	done := make(chan struct{})
	go func() { defer func() { _ = recover(); <-o.busy; close(done) }(); callback() }()
	timer := time.NewTimer(mutationObserverTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// mutationCanonical freezes a canonical entry before destructive IO. It never
// follows a final symlink and never invents an old canonical path after deletion.
func mutationCanonical(root, path string, create bool) string {
	if path == "" {
		return ""
	}
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return ""
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err == nil && withinRoot(root, canonical) {
			return canonical
		}
		return ""
	}
	if !create || !os.IsNotExist(err) {
		return ""
	}
	parent := filepath.Dir(path)
	var rest = []string{filepath.Base(path)}
	for {
		if _, err := os.Lstat(parent); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return ""
		}
		next := filepath.Dir(parent)
		if next == parent {
			return ""
		}
		rest = append(rest, filepath.Base(parent))
		parent = next
	}
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || !withinRoot(root, canonical) {
		return ""
	}
	for i := len(rest) - 1; i >= 0; i-- {
		canonical = filepath.Join(canonical, rest[i])
	}
	if withinRoot(root, canonical) {
		return canonical
	}
	return ""
}
func mutationPath(root, sub string) string {
	if sub == "" {
		return ""
	}
	joined, err := joinUnderRoot(root, sub)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}
func prepareMutation(root, kind, oldSub, newSub, oldAbs, newAbs string, isDir bool) func() {
	before := Mutation{Root: root, Kind: kind, IsDir: isDir, OldPath: mutationPath(root, oldSub), NewPath: mutationPath(root, newSub), OldCanonical: mutationCanonical(root, oldAbs, false), NewCanonical: mutationCanonical(root, newAbs, true)}
	mutationObservers.Lock()
	observers := make([]*mutationObserver, 0, len(mutationObservers.entries))
	for _, o := range mutationObservers.entries {
		observers = append(observers, o)
	}
	mutationObservers.Unlock()
	type completion struct {
		o  *mutationObserver
		fn func(Mutation)
	}
	var completions []completion
	for _, o := range observers {
		var fn func(Mutation)
		if o.call(func() { fn = o.prepare(before) }) && fn != nil {
			completions = append(completions, completion{o, fn})
		}
	}
	return func() {
		after := before
		after.NewCanonical = mutationCanonical(root, newAbs, false)
		for _, c := range completions {
			c.o.call(func() { c.fn(after) })
		}
	}
}
