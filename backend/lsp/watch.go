package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"mincode/app/backend/filewatch"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// LSP WatchKind bits and FileChangeType values.
const (
	watchKindCreate = 1
	watchKindChange = 2
	watchKindDelete = 4
	watchKindAll    = watchKindCreate | watchKindChange | watchKindDelete

	fileChangeCreated = 1
	fileChangeChanged = 2
	fileChangeDeleted = 3
)

const (
	watchedFilesMethod = "workspace/didChangeWatchedFiles"
	// fileWatchLimitedMethod tells the frontend that a registration was
	// acknowledged without watching: the project exceeds the watch limits.
	fileWatchLimitedMethod = "$/mincode/fileWatchLimited"
	// fileWatchScanMethod brackets the baseline scan of a file-watch
	// registration ({"state":"begin"|"end"}); a large project takes seconds.
	fileWatchScanMethod = "$/mincode/fileWatchScan"
	// watchPollInterval is the rescan period when no native change source is available.
	watchPollInterval = 2 * time.Second
	// watchDebounce batches bursts (save, checkout, codegen) into one notification.
	watchDebounce = 150 * time.Millisecond

	maxWatchRegistrations      = 64
	maxWatchersPerRegistration = 256
	maxChangesPerNotification  = 1_000
)

// Watch bounds. With a native source (macOS FSEvents) only changed
// directories are rescanned after a one-off baseline, so whole multi-repo
// folders fit; without one every change rescans the root and the bounds stay
// small. A project beyond them is acknowledged unwatched (fileWatchLimitedMethod).
var maxWatchedDirectories, maxWatchedFiles, maxWatchScanEntries = func() (int, int, int) {
	if filewatch.NativeSource {
		return 100_000, 200_000, 1_000_000
	}
	return 20_000, 50_000, 70_000
}()

type watchPattern struct {
	regex *regexp.Regexp
	base  string
	kind  int
}
type fileChange struct {
	URI  string `json:"uri"`
	Type int    `json:"type"`
}

// fileWatcher adapts compiled LSP matchers and URI notifications only. Scanning,
// native sources and their lifetimes belong to the lower filewatch package.
type fileWatcher struct {
	mu     sync.Mutex
	root   string
	notify func(json.RawMessage) error
	logf   func(string)
	engine *filewatch.Watcher
	closed bool
}

func newFileWatcher(root string, notify func(json.RawMessage) error, logf func(string)) *fileWatcher {
	return &fileWatcher{root: root, notify: notify, logf: logf}
}
func (w *fileWatcher) register(id string, patterns []watchPattern) error {
	_, err := w.registerBaseline(context.Background(), id, patterns)
	return err
}

func (w *fileWatcher) registerBaseline(ctx context.Context, id string, patterns []watchPattern) (*filewatch.Baseline, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil, errors.New("file watcher is closed")
	}
	if w.engine == nil {
		engine, err := filewatch.New(w.root, filewatch.Options{MaxFiles: maxWatchedFiles, MaxDirectories: maxWatchedDirectories, MaxScanEntries: maxWatchScanEntries, MaxChanges: maxChangesPerNotification, PollInterval: watchPollInterval, Debounce: watchDebounce}, w.notifyBatch)
		if err != nil {
			w.mu.Unlock()
			return nil, err
		}
		w.engine = engine
	}
	converted := make([]filewatch.Pattern, 0, len(patterns))
	for _, pattern := range patterns {
		regex := pattern.regex
		converted = append(converted, filewatch.Pattern{Base: pattern.base, Kinds: uint8(pattern.kind), Match: func(path string) bool { return regex.MatchString(globPath(path)) }})
	}
	baseline, err := w.engine.RegisterBaseline(id, converted)
	w.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// No watcher/session mutex is held while the bounded scan runs. Close
	// can cancel this wait even if a scan/callback has not yet returned.
	if err := baseline.Wait(ctx); err != nil {
		baseline.Remove()
		return nil, err
	}
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		baseline.Remove()
		return nil, filewatch.ErrWatcherClosed
	}
	return baseline, nil
}
func (w *fileWatcher) notifyBatch(batch filewatch.Batch) {
	changes := make([]fileChange, 0, len(batch.Changes))
	for _, c := range batch.Changes {
		changes = append(changes, fileChange{URI: fileURI(filepath.Join(w.root, filepath.FromSlash(c.Path))), Type: int(c.Kind)})
	}
	if len(changes) == 0 {
		return
	}
	params, err := json.Marshal(map[string]any{"changes": changes})
	if err == nil {
		_ = w.notify(params)
	}
}

func (w *fileWatcher) unregister(id string) {
	w.mu.Lock()
	engine := w.engine
	w.mu.Unlock()
	if engine != nil {
		engine.Unregister(id)
	}
}
func (w *fileWatcher) requestClose() *filewatch.Watcher {
	w.mu.Lock()
	w.closed = true
	engine := w.engine
	w.mu.Unlock()
	if engine != nil {
		engine.RequestClose()
	}
	return engine
}
func (w *fileWatcher) close() {
	if engine := w.requestClose(); engine != nil {
		_ = engine.Close()
	}
}

// parseWatchers validates DidChangeWatchedFilesRegistrationOptions. Patterns
// whose base lies outside root are dropped rather than widening the watch.
func parseWatchers(root string, raw json.RawMessage) ([]watchPattern, error) {
	var options struct {
		Watchers []struct {
			GlobPattern json.RawMessage `json:"globPattern"`
			Kind        *int            `json:"kind"`
		} `json:"watchers"`
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		return nil, errors.New("invalid file watcher options")
	}
	if len(options.Watchers) > maxWatchersPerRegistration {
		return nil, errors.New("too many file watchers in one registration")
	}
	patterns := make([]watchPattern, 0, len(options.Watchers))
	for _, item := range options.Watchers {
		kind := watchKindAll
		if item.Kind != nil {
			kind = *item.Kind & watchKindAll
		}
		if kind == 0 {
			continue
		}
		var text string
		base := ""
		if json.Unmarshal(item.GlobPattern, &text) != nil {
			var relative struct {
				BaseURI json.RawMessage `json:"baseUri"`
				Pattern string          `json:"pattern"`
			}
			if json.Unmarshal(item.GlobPattern, &relative) != nil {
				continue
			}
			path, ok := relativePatternBase(root, relative.BaseURI)
			if !ok {
				continue
			}
			text, base = relative.Pattern, path
		}
		regex, err := compileGlob(text)
		if err != nil {
			continue
		}
		patterns = append(patterns, watchPattern{regex: regex, base: base, kind: kind})
	}
	return patterns, nil
}

// relativePatternBase resolves a RelativePattern baseUri (a URI or a
// WorkspaceFolder) to a host directory inside root.
func relativePatternBase(root string, raw json.RawMessage) (string, bool) {
	var uri string
	if json.Unmarshal(raw, &uri) != nil {
		var folder struct {
			URI string `json:"uri"`
		}
		if json.Unmarshal(raw, &folder) != nil {
			return "", false
		}
		uri = folder.URI
	}
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" || (parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost")) {
		return "", false
	}
	path := parsed.Path
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	path = filepath.Clean(filepath.FromSlash(path))
	if !filepath.IsAbs(path) {
		return "", false
	}
	if pathInsideRoot(root, path) {
		return path, true
	}
	// The server may report the base through a symlinked or unresolved path.
	if resolved, ok := resolveExistingPrefix(path); ok && pathInsideRoot(root, resolved) {
		return resolved, true
	}
	return "", false
}

// applyWatchRegistration mirrors dynamic registrations before forwarding them.
// Only a complete baseline may be forwarded for the frontend's success reply.
func (session *session) applyWatchRegistration(method string, params json.RawMessage) error {
	switch method {
	case "client/registerCapability":
		var request struct {
			Registrations []struct {
				ID              string          `json:"id"`
				Method          string          `json:"method"`
				RegisterOptions json.RawMessage `json:"registerOptions"`
			} `json:"registrations"`
		}
		if json.Unmarshal(params, &request) != nil {
			return errors.New("invalid file watcher registration parameters")
		}
		for _, registration := range request.Registrations {
			if registration.Method != watchedFilesMethod {
				continue
			}
			patterns, err := parseWatchers(session.root, registration.RegisterOptions)
			if err != nil {
				session.log("error category=file-watcher-registration-invalid")
				return err
			}
			watcher := session.fileWatcher()
			if watcher == nil {
				return filewatch.ErrWatcherClosed
			}
			session.reportWatchScan("begin")
			err = watcher.register(registration.ID, patterns)
			session.reportWatchScan("end")
			if err != nil {
				// An over-limit project is logged as a warning by the caller.
				if !errors.Is(err, filewatch.ErrBaselineTruncated) {
					session.log("error category=file-watcher-registration-refused")
				}
				return err
			}
		}
	case "client/unregisterCapability":
		var request struct {
			// "unregisterations" is the spelling the LSP specification uses.
			Unregistrations []struct {
				ID     string `json:"id"`
				Method string `json:"method"`
			} `json:"unregisterations"`
		}
		if json.Unmarshal(params, &request) != nil {
			return errors.New("invalid file watcher registration parameters")
		}
		for _, unregistration := range request.Unregistrations {
			if unregistration.Method != watchedFilesMethod {
				continue
			}
			session.watchMu.Lock()
			watcher := session.watcher
			session.watchMu.Unlock()
			if watcher != nil {
				watcher.unregister(unregistration.ID)
			}
		}
	}
	return nil
}

// reportWatchScan tells the frontend a registration's baseline scan began or
// ended: the server waits for its reply while a large project is scanned.
func (session *session) reportWatchScan(state string) {
	session.forward(serverEvent{Root: session.root, ServerID: session.definition.ID, Generation: session.generation,
		Method: fileWatchScanMethod, Params: json.RawMessage(`{"state":"` + state + `"}`)})
}

// fileWatcher returns the session's watcher, or nil once the session stopped.
func (session *session) fileWatcher() *fileWatcher {
	session.watchMu.Lock()
	defer session.watchMu.Unlock()
	if session.watchStopped {
		return nil
	}
	if session.watcher == nil {
		session.watcher = newFileWatcher(session.root, func(params json.RawMessage) error {
			return session.notify(watchedFilesMethod, params)
		}, session.log)
	}
	return session.watcher
}

func (session *session) requestStopWatching() *fileWatcher {
	session.watchMu.Lock()
	session.watchStopped = true
	watcher := session.watcher
	session.watchMu.Unlock()
	if watcher != nil {
		watcher.requestClose()
	}
	return watcher
}

func (session *session) stopWatching() {
	if watcher := session.requestStopWatching(); watcher != nil {
		watcher.close()
	}
}
