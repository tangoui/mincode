package filewatch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func watchRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func put(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
}
func nextBatch(t *testing.T, ch <-chan Batch) Batch {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(4 * time.Second):
		t.Fatal("watch produced no batch")
		return Batch{}
	}
}

// Catches missing safe pre-delete snapshots, following symlinks, and fabricated old paths.
func TestWatchExternalRenameAndSafeBefore(t *testing.T) {
	root := watchRoot(t)
	old := filepath.Join(root, "old.txt")
	put(t, old)
	ch := make(chan Batch, 32)
	w, err := New(root, Options{PollInterval: 50 * time.Millisecond}, func(b Batch) { ch <- b })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Register("all", []Pattern{{Match: func(string) bool { return true }, Kinds: 7}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	newPath := filepath.Join(root, "new.txt")
	if err := os.Rename(old, newPath); err != nil {
		t.Fatal(err)
	}
	b := nextBatch(t, ch)
	foundDelete, foundCreate := false, false
	for _, c := range b.Changes {
		switch c.Kind {
		case Deleted:
			foundDelete = c.Path == "old.txt" && c.Before != nil && c.Before.Path == "old.txt" && c.Before.Canonical == old
		case Created:
			foundCreate = c.Path == "new.txt"
		}
	}
	if !foundDelete || !foundCreate {
		t.Fatalf("rename must carry safe Before and create: %+v", b)
	}
	if err := os.Symlink(newPath, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	select {
	case b := <-ch:
		if len(b.Changes) != 0 {
			t.Fatalf("symlink became an Entry: %+v", b)
		}
	default:
	}
	// An unknown native old path has no safe Entry, even if lexically inside root.
	w.markDirty(filepath.Join(root, "unknown.txt"), false)
	b = nextBatch(t, ch)
	if b.RescanReason != "sourceLost" || len(b.Changes) != 0 {
		t.Fatalf("unknown old path must only rescan: %+v", b)
	}
}

// Catches missing default bounds and defaults that would turn fallback into a busy loop.
func TestWatchLimitsAndFallback(t *testing.T) {
	root := watchRoot(t)
	w, err := New(root, Options{}, func(Batch) {})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.options.MaxFiles != 50000 || w.options.MaxDirectories != 20000 || w.options.PollInterval != 2*time.Second || w.options.Debounce != 150*time.Millisecond {
		t.Fatalf("unsafe defaults: %+v", w.options)
	}
}

// The original deterministic LSP scanner regression tests moved with the scanner.
// Their assertions are retained; protocol parsing and live registration stay in LSP.
type fileWatcher = Watcher
type fileChange struct {
	URI  string
	Type int
}

const (
	fileChangeCreated = 1
	fileChangeChanged = 2
	fileChangeDeleted = 3
	maxWatchedFiles   = 50000
)

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}
func canonicalTempDir(t *testing.T) string { return watchRoot(t) }

// testWatcher drives process() directly so the diff is deterministic.
type testWatcher struct {
	*fileWatcher
	mu      sync.Mutex
	changes []fileChange
}

func newTestWatcher(t *testing.T, root string, watchers ...map[string]any) *testWatcher {
	t.Helper()
	result := &testWatcher{}
	var err error
	result.fileWatcher, err = New(root, Options{MaxChanges: 1000}, func(b Batch) {
		for _, c := range b.Changes {
			result.changes = append(result.changes, fileChange{URI: fileURI(filepath.Join(root, filepath.FromSlash(c.Path))), Type: int(c.Kind)})
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var patterns []Pattern
	for _, item := range watchers {
		text, base := "", ""
		if value, ok := item["globPattern"].(string); ok {
			text = value
		} else {
			value := item["globPattern"].(map[string]any)
			uri, _ := url.Parse(value["baseUri"].(string))
			base = uri.Path
			text = value["pattern"].(string)
		}
		kind := uint8(7)
		if value, ok := item["kind"].(uint8); ok {
			kind = value
		}
		expression := regexp.QuoteMeta(text)
		expression = strings.ReplaceAll(expression, `\*\*/`, `(?:.*/)?`)
		expression = strings.ReplaceAll(expression, `\*`, `[^/]*`)
		regex := regexp.MustCompile("^" + expression + "$")
		patterns = append(patterns, Pattern{Base: base, Match: regex.MatchString, Kinds: kind})
	}
	result.registrations["test"] = patterns
	result.rebaseline = true
	result.process()
	return result
}
func (w *testWatcher) close() { _ = w.Close() }
func (watcher *testWatcher) flush(path string, recursive bool) []fileChange {
	watcher.markDirty(path, recursive)
	watcher.process()
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	changes := watcher.changes
	watcher.changes = nil
	sort.Slice(changes, func(a, b int) bool {
		if changes[a].URI != changes[b].URI {
			return changes[a].URI < changes[b].URI
		}
		return changes[a].Type < changes[b].Type
	})
	return changes
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func expectChanges(t *testing.T, got []fileChange, want ...fileChange) {
	t.Helper()
	sort.Slice(want, func(a, b int) bool {
		if want[a].URI != want[b].URI {
			return want[a].URI < want[b].URI
		}
		return want[a].Type < want[b].Type
	})
	if len(got) != len(want) {
		t.Fatalf("changes = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("changes = %+v, want %+v", got, want)
		}
	}
}

func TestWatcherReportsCreateChangeDelete(t *testing.T) {
	root := canonicalTempDir(t)
	existing := filepath.Join(root, "main.go")
	writeFile(t, existing, "package main")
	writeFile(t, filepath.Join(root, "README.md"), "ignored by pattern")
	watcher := newTestWatcher(t, root, map[string]any{"globPattern": "**/*.go"})
	defer watcher.close()

	if changes := watcher.flush(root, true); len(changes) != 0 {
		t.Fatalf("baseline must not report existing files: %+v", changes)
	}
	created := filepath.Join(root, "pkg", "nested", "util.go")
	writeFile(t, created, "package nested")
	// A non-recursive event for the root still discovers the new subtree.
	expectChanges(t, watcher.flush(root, false), fileChange{URI: fileURI(created), Type: fileChangeCreated})

	writeFile(t, existing, "package main\n\nfunc main() {}\n")
	expectChanges(t, watcher.flush(root, false), fileChange{URI: fileURI(existing), Type: fileChangeChanged})

	writeFile(t, filepath.Join(root, "README.md"), "still ignored, longer content")
	if changes := watcher.flush(root, false); len(changes) != 0 {
		t.Fatalf("unmatched file reported: %+v", changes)
	}

	if err := os.RemoveAll(filepath.Join(root, "pkg")); err != nil {
		t.Fatal(err)
	}
	expectChanges(t, watcher.flush(filepath.Join(root, "pkg"), true), fileChange{URI: fileURI(created), Type: fileChangeDeleted})
	if watcher.fileCount != 1 || len(watcher.dirs) != 1 {
		t.Fatalf("snapshot not pruned: files=%d dirs=%d", watcher.fileCount, len(watcher.dirs))
	}
}

func TestWatcherHonoursKindsAndRelativePatterns(t *testing.T) {
	root := canonicalTempDir(t)
	writeFile(t, filepath.Join(root, "go.mod"), "module a")
	watcher := newTestWatcher(t, root,
		map[string]any{"globPattern": map[string]any{"baseUri": fileURI(root), "pattern": "go.mod"}, "kind": watchKindDelete},
		map[string]any{"globPattern": map[string]any{"baseUri": fileURI(root), "pattern": "*.sum"}, "kind": watchKindCreate | watchKindChange},
	)
	defer watcher.close()

	writeFile(t, filepath.Join(root, "go.mod"), "module a\n\ngo 1.26\n")
	writeFile(t, filepath.Join(root, "go.sum"), "sum")
	expectChanges(t, watcher.flush(root, false), fileChange{URI: fileURI(filepath.Join(root, "go.sum")), Type: fileChangeCreated})

	if err := os.Remove(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "go.sum")); err != nil {
		t.Fatal(err)
	}
	expectChanges(t, watcher.flush(root, false), fileChange{URI: fileURI(filepath.Join(root, "go.mod")), Type: fileChangeDeleted})
}

func TestWatcherSkipsIgnoredDirectoriesAndSymlinks(t *testing.T) {
	root := canonicalTempDir(t)
	outside := canonicalTempDir(t)
	writeFile(t, filepath.Join(outside, "secret.go"), "package outside")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	watcher := newTestWatcher(t, root, map[string]any{"globPattern": "**/*.go"})
	defer watcher.close()

	writeFile(t, filepath.Join(root, "node_modules", "dep", "index.go"), "package dep")
	writeFile(t, filepath.Join(root, ".git", "hooks", "x.go"), "package hooks")
	writeFile(t, filepath.Join(outside, "new.go"), "package outside")
	if changes := watcher.flush(root, true); len(changes) != 0 {
		t.Fatalf("ignored or symlinked files reported: %+v", changes)
	}
	// A native event inside an ignored tree must not start tracking it.
	if changes := watcher.flush(filepath.Join(root, "node_modules", "dep"), true); len(changes) != 0 {
		t.Fatalf("ignored tree reported: %+v", changes)
	}
	if _, tracked := watcher.dirs[filepath.Join(root, "node_modules", "dep")]; tracked {
		t.Fatal("ignored directory entered the snapshot")
	}
}

func TestWatcherFileLimitTruncates(t *testing.T) {
	root := canonicalTempDir(t)
	watcher := newTestWatcher(t, root, map[string]any{"globPattern": "**/*.txt"})
	defer watcher.close()
	watcher.fileCount = maxWatchedFiles
	writeFile(t, filepath.Join(root, "over.txt"), "x")
	if changes := watcher.flush(root, false); len(changes) != 0 || !watcher.truncated {
		t.Fatalf("limit not enforced: changes=%+v truncated=%v", changes, watcher.truncated)
	}
}

// Rejects a scan which bounds only matching files but enumerates arbitrarily many others.
func TestWatchScanWorkBound(t *testing.T) {
	root := watchRoot(t)
	for i := 0; i < 100; i++ {
		put(t, filepath.Join(root, fmt.Sprint(i)))
	}
	calls := 0
	w, err := New(root, Options{MaxFiles: 2, MaxDirectories: 1}, func(Batch) {})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.registrations["test"] = []Pattern{{Match: func(string) bool { calls++; return false }, Kinds: 7}}
	w.rebaseline = true
	w.process()
	if calls > 6 || !w.truncated {
		t.Fatalf("unbounded unmatched scan: calls=%d truncated=%v", calls, w.truncated)
	}
}

func TestWatchRealDefaultLimits(t *testing.T) {
	for _, isDir := range []bool{false, true} {
		t.Run(fmt.Sprint(isDir), func(t *testing.T) {
			root := watchRoot(t)
			count := 50001
			if isDir {
				count = 20001
			}
			for i := 0; i < count; i++ {
				path := filepath.Join(root, fmt.Sprintf("entry-%05d", i))
				if isDir {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					put(t, path)
				}
			}
			var batches []Batch
			w, err := New(root, Options{}, func(b Batch) { batches = append(batches, b) })
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			w.registrations["test"] = []Pattern{{Match: func(string) bool { return true }, Kinds: 7}}
			w.rebaseline = true
			w.process()
			if !w.truncated || len(batches) != 1 || batches[0].RescanReason != "truncated" {
				t.Fatalf("missing truncation: %+v", batches)
			}
			if w.fileCount > 50000 || len(w.dirs) > 20000 {
				t.Fatalf("snapshot escaped bounds: %d/%d", w.fileCount, len(w.dirs))
			}
			if !isDir && w.fileCount != 50000 {
				t.Fatalf("file boundary: %d", w.fileCount)
			}
			if isDir && len(w.dirs) != 20000 {
				t.Fatalf("directory boundary: %d", len(w.dirs))
			}
		})
	}
}

func TestWatchCloseJoinsGoroutinesAndNativeSource(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 12; i++ {
		root := watchRoot(t)
		ch := make(chan Batch, 8)
		// Hold the initial silent scan until the first write. This models a
		// descheduled baseline deterministically, rather than assuming 30 ms
		// is enough for registration to finish on a loaded race builder.
		baselineGate := make(chan struct{})
		var releaseOnce sync.Once
		releaseBaseline := func() { releaseOnce.Do(func() { close(baselineGate) }) }
		w, err := New(root, Options{Debounce: 10 * time.Millisecond, Allow: func(string, string) bool {
			<-baselineGate
			return true
		}}, func(b Batch) { ch <- b })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { releaseBaseline(); _ = w.Close() })
		if err := w.Register("test", []Pattern{{Match: func(string) bool { return true }, Kinds: 7}}); err != nil {
			t.Fatal(err)
		}
		w.mu.Lock()
		native := w.source != nil
		if i == 0 {
			t.Logf("watch source: %T (native=%v)", w.source, native)
		}
		w.mu.Unlock()
		if runtime.GOOS == "darwin" && !native {
			t.Fatal("test requires real FSEvents, not fallback")
		}
		// A live native batch, not a registration return or a sleep, is the
		// readiness barrier. The first probe may be absorbed into the silent
		// baseline; later writes must be observed by the real native source.
		probe := filepath.Join(root, "ready")
		ticker := time.NewTicker(20 * time.Millisecond)
		watchdog := time.NewTimer(4 * time.Second)
		t.Cleanup(func() { ticker.Stop(); watchdog.Stop() })
		ready := false
		for attempt := 1; !ready; attempt++ {
			if err := os.WriteFile(probe, []byte(strings.Repeat("x", attempt)), 0600); err != nil {
				t.Fatal(err)
			}
			releaseBaseline()
			select {
			case batch := <-ch:
				for _, change := range batch.Changes {
					ready = ready || change.Path == "ready"
				}
			case <-ticker.C:
			case <-watchdog.C:
				ticker.Stop()
				t.Fatal("native source never observed a readiness probe after baseline")
			}
		}
		ticker.Stop()
		watchdog.Stop()
		put(t, filepath.Join(root, "new"))
		watchdog.Reset(4 * time.Second)
		found := false
		for !found {
			select {
			case batch := <-ch:
				for _, change := range batch.Changes {
					found = found || change.Path == "new" && change.Kind == Created
				}
			case <-watchdog.C:
				t.Fatal("native source lost post-readiness create")
			}
		}
		watchdog.Stop()
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-w.stopped:
		default:
			t.Fatal("Close did not join loop")
		}
		if err := w.Register("closed", nil); err == nil {
			t.Fatal("registration after Close")
		}
		_ = w.Close()
	}
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	t.Logf("12 create/change/Close cycles: goroutines before=%d after=%d", before, after)
	if after > before+2 {
		t.Fatalf("goroutines leaked: before=%d after=%d", before, after)
	}
}

func TestWatchBoundedBatchesOverflowAndSourceLoss(t *testing.T) {
	root := watchRoot(t)
	var batches []Batch
	w, err := New(root, Options{MaxChanges: 2}, func(b Batch) { batches = append(batches, b) })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.registrations["test"] = []Pattern{{Match: func(string) bool { return true }, Kinds: 7}}
	w.rebaseline = true
	w.process()
	for i := 0; i < 5; i++ {
		put(t, filepath.Join(root, fmt.Sprint(i)))
	}
	w.markDirty(root, true)
	w.process()
	total := 0
	for _, b := range batches {
		if len(b.Changes) > 2 {
			t.Fatal("oversized batch")
		}
		total += len(b.Changes)
	}
	if total != 5 {
		t.Fatalf("LSP tail lost: %d", total)
	}
	batches = nil
	for i := 0; i < 20; i++ {
		w.markDirty(filepath.Join(root, fmt.Sprint(i)), false)
	}
	w.process()
	if len(batches) == 0 || batches[0].RescanReason != "overflow" {
		t.Fatalf("overflow not signalled: %+v", batches)
	}
	batches = nil
	w.nativeChanged(root, true, "sourceLost")
	w.process()
	if !w.polling || len(batches) != 1 || batches[0].RescanReason != "sourceLost" {
		t.Fatalf("source loss not falling back: %+v", batches)
	}
}

// Controlled real scans prove that completion belongs to the registration
// revision actually scanned, not to an old scan or a different registration.
func TestBaselineTicketsBindRevisionAndReleaseWaiters(t *testing.T) {
	root := watchRoot(t)
	gates := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	var releases [3]sync.Once
	release := func(i int) { releases[i].Do(func() { close(gates[i]) }) }
	entered := make(chan int, 8)
	var scans atomic.Int32
	w, err := New(root, Options{Debounce: time.Millisecond, Allow: func(relative, _ string) bool {
		if relative == "." {
			i := int(scans.Add(1)) - 1
			if i < len(gates) {
				entered <- i
				<-gates[i]
			}
		}
		return true
	}}, func(Batch) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := range gates {
			release(i)
		}
		_ = w.Close()
	})
	waitScan := func(want int) {
		t.Helper()
		select {
		case got := <-entered:
			if got != want {
				t.Fatalf("scan %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("controlled baseline did not start")
		}
	}
	patterns := []Pattern{{Match: func(string) bool { return true }, Kinds: 7}}
	register := func(id string) *Baseline {
		t.Helper()
		b, err := w.RegisterBaseline(id, patterns)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	wait := func(b *Baseline, want error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := b.Wait(ctx); !errors.Is(err, want) {
			t.Fatalf("wait = %v, want %v", err, want)
		}
	}
	pending := func(b *Baseline) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if err := b.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unscanned revision was ready: %v", err)
		}
	}
	old := register("a")
	waitScan(0)
	replacement := register("a")
	other := register("b")
	wait(old, ErrRegistrationReplaced)
	old.Remove() // must not remove the replacement
	release(0)
	waitScan(1)
	pending(replacement)
	pending(other)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := replacement.Wait(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	w.Unregister("b")
	wait(other, ErrRegistrationRemoved)
	newer := register("c") // not included in scan 1
	release(1)
	waitScan(2)
	wait(replacement, nil)
	pending(newer) // scan 1 must not acknowledge c
	w.RequestClose()
	wait(newer, ErrWatcherClosed) // close releases waits before a blocked scan joins
}

func TestBaselineIncompleteNeverReportsReady(t *testing.T) {
	for _, scenario := range []string{"truncated", "source-start", "source-during-scan"} {
		t.Run(scenario, func(t *testing.T) {
			root := watchRoot(t)
			put(t, filepath.Join(root, "a"))
			put(t, filepath.Join(root, "b"))
			entered, release := make(chan struct{}), make(chan struct{})
			var once, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			opts := Options{Debounce: time.Millisecond}
			if scenario == "truncated" {
				opts.MaxFiles = 1
			}
			if scenario == "source-during-scan" {
				opts.Allow = func(string, string) bool { once.Do(func() { close(entered) }); <-release; return true }
			}
			w, err := New(root, opts, func(Batch) {})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { unblock(); _ = w.Close() })
			if scenario == "source-start" {
				w.start = func(string, watchChanged) (watchSource, error) { return nil, errors.New("source unavailable") }
			}
			b, err := w.RegisterBaseline("one", []Pattern{{Match: func(string) bool { return true }, Kinds: 7}})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "source-during-scan" {
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("scan did not start")
				}
				w.RequestRescan()
				unblock()
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := b.Wait(ctx); !errors.Is(err, ErrBaselineIncomplete) {
				t.Fatalf("incomplete baseline = %v", err)
			}
			b.Remove()
		})
	}
}

func TestBaselineRejectsUnscannableRoot(t *testing.T) {
	for _, state := range []string{"deleted", "renamed", "replaced-file", "symlink-root", "symlink-parent", "policy-denied"} {
		t.Run(state, func(t *testing.T) {
			base := watchRoot(t)
			parent := filepath.Join(base, "parent")
			root := filepath.Join(parent, "project")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(base, "outside")
			if err := os.MkdirAll(filepath.Join(outside, "project"), 0700); err != nil {
				t.Fatal(err)
			}
			opts := Options{Debounce: time.Millisecond}
			if state == "policy-denied" {
				opts.Allow = func(relative, _ string) bool { return relative != "." }
			}
			w, err := New(root, opts, func(Batch) {})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = w.Close() })
			// New has verified the real directory, but the first scan has not
			// started. No native source override: CGO_ENABLED=0 runs the real
			// source_other.go polling path, with no loss hint to mask the bug.
			switch state {
			case "deleted":
				err = os.Remove(root)
			case "renamed":
				err = os.Rename(root, root+"-moved")
			case "replaced-file":
				if err = os.Remove(root); err == nil {
					err = os.WriteFile(root, []byte("not a directory"), 0600)
				}
			case "symlink-root":
				if err = os.Remove(root); err == nil {
					err = os.Symlink(outside, root)
				}
			case "symlink-parent":
				if err = os.Rename(parent, parent+"-moved"); err == nil {
					err = os.Symlink(outside, parent)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := w.RegisterBaseline("root", []Pattern{{Match: func(string) bool { return true }, Kinds: 7}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := b.Wait(ctx); !errors.Is(err, ErrBaselineIncomplete) {
				t.Fatalf("%s root falsely ready: %v", state, err)
			}
			w.mu.Lock()
			t.Logf("source=%T polling=%v", w.source, w.polling)
			w.mu.Unlock()
		})
	}
}

func TestBaselineAllowsEmptyRootAndExcludedChild(t *testing.T) {
	for _, excludedChild := range []bool{false, true} {
		t.Run(fmt.Sprint(excludedChild), func(t *testing.T) {
			root := watchRoot(t)
			if excludedChild {
				if err := os.Mkdir(filepath.Join(root, "excluded"), 0700); err != nil {
					t.Fatal(err)
				}
				put(t, filepath.Join(root, "excluded", "private.txt"))
			}
			w, err := New(root, Options{Debounce: time.Millisecond, Allow: func(relative, _ string) bool { return relative != "excluded" }}, func(Batch) {})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = w.Close() })
			b, err := w.RegisterBaseline("valid", []Pattern{{Match: func(string) bool { return true }, Kinds: 7}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := b.Wait(ctx); err != nil {
				t.Fatalf("valid root rejected: %v", err)
			}
		})
	}
}

type closingSource struct{ entered, release chan struct{} }

func (s *closingSource) close() { close(s.entered); <-s.release }
func TestWatchConcurrentCloseWaitsForNativeHandle(t *testing.T) {
	w, err := New(watchRoot(t), Options{}, func(Batch) {})
	if err != nil {
		t.Fatal(err)
	}
	source := &closingSource{make(chan struct{}), make(chan struct{})}
	w.source = source
	first, second := make(chan struct{}), make(chan struct{})
	go func() { _ = w.Close(); close(first) }()
	<-source.entered
	go func() { _ = w.Close(); close(second) }()
	early := false
	select {
	case <-second:
		early = true
	case <-time.After(50 * time.Millisecond):
	}
	close(source.release)
	<-first
	<-second
	if early {
		t.Fatal("concurrent Close returned before native source drained")
	}
}
func TestWatchFallbackTimingAndDebounce(t *testing.T) {
	root := watchRoot(t)
	ch := make(chan Batch, 16)
	w, err := New(root, Options{}, func(b Batch) { ch <- b })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.start = func(string, watchChanged) (watchSource, error) { return nil, nil }
	if err := w.Register("all", []Pattern{{Match: func(string) bool { return true }, Kinds: 7}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond)
	start := time.Now()
	put(t, filepath.Join(root, "first"))
	select {
	case b := <-ch:
		t.Fatalf("poll fired before 2s fallback: %+v", b)
	case <-time.After(900 * time.Millisecond):
	}
	b := nextBatch(t, ch)
	if len(b.Changes) != 1 || time.Since(start) < time.Second {
		t.Fatalf("bad fallback: %+v", b)
	}
	put(t, filepath.Join(root, "second"))
	start = time.Now()
	w.markDirty(root, false)
	time.Sleep(50 * time.Millisecond)
	put(t, filepath.Join(root, "third"))
	w.markDirty(root, false)
	b = nextBatch(t, ch)
	if len(b.Changes) != 2 || time.Since(start) < 140*time.Millisecond {
		t.Fatalf("150ms burst not batched: %+v", b)
	}
}
func TestWatchRecreatedDirectory(t *testing.T) {
	root := watchRoot(t)
	path := filepath.Join(root, "dir", "file.go")
	writeFile(t, path, "old")
	w := newTestWatcher(t, root, map[string]any{"globPattern": "**/*.go"})
	defer w.close()
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	expectChanges(t, w.flush(filepath.Dir(path), true), fileChange{URI: fileURI(path), Type: fileChangeDeleted})
	writeFile(t, path, "new")
	expectChanges(t, w.flush(root, false), fileChange{URI: fileURI(path), Type: fileChangeCreated})
}

func TestWatchUnreadableDirectoryKeepsOriginalDeletionSemantics(t *testing.T) {
	root := watchRoot(t)
	path := filepath.Join(root, "dir", "file.go")
	writeFile(t, path, "old")
	w := newTestWatcher(t, root, map[string]any{"globPattern": "**/*.go"})
	defer w.close()
	if err := os.Chmod(filepath.Dir(path), 0100); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Dir(path), 0700)
	expectChanges(t, w.flush(filepath.Dir(path), true), fileChange{URI: fileURI(path), Type: fileChangeDeleted})
}

// Last Unregister can own the detached source while Close sees source == nil.
func TestWatchCloseWaitsForUnregisterDrain(t *testing.T) {
	w, err := New(watchRoot(t), Options{}, func(Batch) {})
	if err != nil {
		t.Fatal(err)
	}
	source := &closingSource{make(chan struct{}), make(chan struct{})}
	w.start = func(string, watchChanged) (watchSource, error) { return source, nil }
	if err := w.Register("last", nil); err != nil {
		t.Fatal(err)
	}
	unregistered, closed := make(chan struct{}), make(chan struct{})
	go func() { w.Unregister("last"); close(unregistered) }()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("Unregister did not start source drain")
	}
	go func() { _ = w.Close(); close(closed) }()
	early := false
	select {
	case <-closed:
		early = true
	case <-time.After(100 * time.Millisecond):
	}
	close(source.release)
	for _, done := range []chan struct{}{unregistered, closed} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("source cleanup did not join")
		}
	}
	if early {
		t.Fatal("Close returned before concurrent Unregister drained native source")
	}
}

// Larger bounds are honoured only with a native source: without one every
// change rescans the whole root, so the polling bounds stay as they were.
func TestWatchBoundCeilingsDependOnNativeSource(t *testing.T) {
	w, err := New(watchRoot(t), Options{MaxFiles: 400_000, MaxDirectories: 150_000, MaxScanEntries: 1_500_000}, func(Batch) {})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	got := [3]int{w.options.MaxFiles, w.options.MaxDirectories, w.options.MaxScanEntries}
	want := [3]int{50_000, 20_000, 70_000}
	if NativeSource {
		want = [3]int{400_000, 150_000, 1_500_000}
	}
	if got != want {
		t.Fatalf("bounds = %v, want %v (native source %v)", got, want, NativeSource)
	}
	defaults, err := New(watchRoot(t), Options{}, func(Batch) {})
	if err != nil {
		t.Fatal(err)
	}
	defer defaults.Close()
	if got := [3]int{defaults.options.MaxFiles, defaults.options.MaxDirectories, defaults.options.MaxScanEntries}; got != [3]int{50_000, 20_000, 70_000} {
		t.Fatalf("default bounds changed: %v", got)
	}
}

// Unmatched files cost scan work but not snapshot memory: a separate scan
// budget lets a large tree with few matching files complete its baseline.
func TestWatchScanBudgetIsIndependentOfSnapshotBounds(t *testing.T) {
	root := watchRoot(t)
	for i := range 100 {
		put(t, filepath.Join(root, fmt.Sprint(i)))
	}
	put(t, filepath.Join(root, "match.php"))
	w, err := New(root, Options{MaxFiles: 2, MaxDirectories: 1, MaxScanEntries: 200}, func(Batch) {})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.registrations["test"] = []Pattern{{Match: func(path string) bool { return strings.HasSuffix(path, ".php") }, Kinds: 7}}
	w.rebaseline = true
	w.process()
	if w.truncated || w.fileCount != 1 {
		t.Fatalf("scan budget truncated a tree within it: truncated=%v files=%d", w.truncated, w.fileCount)
	}
}

// A polling rescan of a large tree must not run back to back.
func TestWatchPollDelayBacksOffForSlowScans(t *testing.T) {
	for _, tc := range []struct{ interval, scan, want time.Duration }{
		{2 * time.Second, 10 * time.Millisecond, 2 * time.Second},
		{2 * time.Second, time.Second, 5 * time.Second},
		{2 * time.Second, 4 * time.Second, 20 * time.Second},
		{2 * time.Second, time.Hour, 5 * time.Minute},
	} {
		if got := pollDelay(tc.interval, tc.scan); got != tc.want {
			t.Errorf("pollDelay(%s, %s) = %s, want %s", tc.interval, tc.scan, got, tc.want)
		}
	}
}

// Over-limit baselines are distinguishable from lost roots.
func TestBaselineTruncationIsDistinguishable(t *testing.T) {
	root := watchRoot(t)
	for i := range 3 {
		put(t, filepath.Join(root, fmt.Sprintf("%d.txt", i)))
	}
	w, err := New(root, Options{MaxFiles: 1, Debounce: time.Millisecond}, func(Batch) {})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	baseline, err := w.RegisterBaseline("all", []Pattern{{Match: func(string) bool { return true }, Kinds: 7}})
	if err != nil {
		t.Fatal(err)
	}
	err = baseline.Wait(context.Background())
	if !errors.Is(err, ErrBaselineIncomplete) || !errors.Is(err, ErrBaselineTruncated) {
		t.Fatalf("Wait = %v, want an incomplete, truncated baseline", err)
	}
}
