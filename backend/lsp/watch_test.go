package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/filewatch"
)

func TestCompileGlob(t *testing.T) {
	cases := []struct {
		pattern string
		match   []string
		reject  []string
	}{
		{"**/*.go", []string{"main.go", "a/b/c.go", "/abs/root/x.go"}, []string{"main.go.txt", "a/go"}},
		{"*.go", []string{"main.go"}, []string{"a/main.go"}},
		{"**/{go.mod,go.sum}", []string{"go.mod", "x/go.sum"}, []string{"go.work"}},
		{"**/*.{ts,tsx}", []string{"a.ts", "b/c.tsx"}, []string{"a.js"}},
		{"src/**", []string{"src/a", "src/a/b.c"}, []string{"src", "lib/src/a"}},
		{"file?.txt", []string{"file1.txt"}, []string{"file10.txt", "file/.txt"}},
		{"[abc].md", []string{"a.md", "c.md"}, []string{"d.md"}},
		{"[!abc].md", []string{"d.md"}, []string{"a.md", "/.md"}},
		{"a**b", []string{"ab", "axxb"}, []string{"a/b"}},
		{"**/node_modules/**", []string{"node_modules/x", "a/node_modules/b/c"}, []string{"node_modules"}},
		{"weird+(name).txt", []string{"weird+(name).txt"}, []string{"weird+name.txt"}},
	}
	for _, testCase := range cases {
		regex, err := compileGlob(testCase.pattern)
		if err != nil {
			t.Fatalf("%q: %v", testCase.pattern, err)
		}
		for _, path := range testCase.match {
			if !regex.MatchString(path) {
				t.Errorf("%q should match %q (regex %s)", testCase.pattern, path, regex)
			}
		}
		for _, path := range testCase.reject {
			if regex.MatchString(path) {
				t.Errorf("%q should not match %q (regex %s)", testCase.pattern, path, regex)
			}
		}
	}
	for _, invalid := range []string{"", "{a,b", "[]x", string(make([]byte, maxGlobPatternBytes+1))} {
		if _, err := compileGlob(invalid); err == nil {
			t.Errorf("expected %q to be rejected", invalid)
		}
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestParseWatchersConfinesRelativeBase(t *testing.T) {
	root := canonicalTempDir(t)
	inside := filepath.Join(root, "pkg")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Dir(root)
	raw, _ := json.Marshal(map[string]any{"watchers": []any{
		map[string]any{"globPattern": "**/*.go"},
		map[string]any{"globPattern": map[string]any{"baseUri": fileURI(inside), "pattern": "*.mod"}, "kind": watchKindCreate},
		map[string]any{"globPattern": map[string]any{"baseUri": map[string]any{"uri": fileURI(root), "name": "root"}, "pattern": "**/*.sum"}},
		map[string]any{"globPattern": map[string]any{"baseUri": fileURI(outside), "pattern": "**/*"}},
		map[string]any{"globPattern": map[string]any{"baseUri": "https://example.com/", "pattern": "**/*"}},
		map[string]any{"globPattern": "**/*.never", "kind": 0},
		map[string]any{"globPattern": "{unclosed"},
	}})
	patterns, err := parseWatchers(root, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) != 3 {
		t.Fatalf("expected 3 confined patterns, got %d", len(patterns))
	}
	if patterns[1].base != inside || patterns[1].kind != watchKindCreate || patterns[2].base != root {
		t.Fatalf("unexpected relative patterns: %+v", patterns)
	}
	tooMany := make([]any, maxWatchersPerRegistration+1)
	for index := range tooMany {
		tooMany[index] = map[string]any{"globPattern": "*"}
	}
	raw, _ = json.Marshal(map[string]any{"watchers": tooMany})
	if _, err := parseWatchers(root, raw); err == nil {
		t.Fatal("expected an oversized registration to be rejected")
	}
}

// The session mirrors the server's registerCapability and pushes
// didChangeWatchedFiles to the server through the real change source.
func TestSessionWatchRegistrationNotifiesServer(t *testing.T) {
	root := canonicalTempDir(t)
	core.SetEmitter(func(string, any) {})
	t.Cleanup(func() { core.SetEmitter(nil) })
	serverReader, serverWriter := io.Pipe()
	s := &session{root: root, stdin: serverWriter, pending: map[uint64]chan rpcReply{}}
	t.Cleanup(func() {
		s.close(nil)
		_ = serverWriter.Close()
		_ = serverReader.Close()
	})
	notifications := make(chan json.RawMessage, 16)
	go func() {
		reader := bufio.NewReader(serverReader)
		for {
			raw, err := readMessage(reader)
			if err != nil {
				return
			}
			var message struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(raw, &message) == nil && message.Method == watchedFilesMethod {
				notifications <- message.Params
			}
		}
	}()

	var input bytesBuffer
	_ = writeMessage(&input, map[string]any{"jsonrpc": "2.0", "id": 7, "method": "client/registerCapability", "params": map[string]any{
		"registrations": []any{map[string]any{"id": "watch-1", "method": watchedFilesMethod, "registerOptions": map[string]any{
			"watchers": []any{map[string]any{"globPattern": "**/*.go"}},
		}}},
	}})
	s.readLoop(&input)
	// Let the baseline land before changing the tree.
	time.Sleep(2 * watchDebounce)
	created := filepath.Join(root, "added.go")
	writeFile(t, created, "package added")

	select {
	case params := <-notifications:
		var payload struct {
			Changes []fileChange `json:"changes"`
		}
		if err := json.Unmarshal(params, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Changes) != 1 || payload.Changes[0] != (fileChange{URI: fileURI(created), Type: fileChangeCreated}) {
			t.Fatalf("unexpected changes: %+v", payload.Changes)
		}
	case <-time.After(watchPollInterval + 5*time.Second):
		t.Fatalf("no didChangeWatchedFiles notification; logs:\n%s", s.logs())
	}

	var unregister bytesBuffer
	_ = writeMessage(&unregister, map[string]any{"jsonrpc": "2.0", "id": 8, "method": "client/unregisterCapability", "params": map[string]any{
		"unregisterations": []any{map[string]any{"id": "watch-1", "method": watchedFilesMethod}},
	}})
	s.readLoop(&unregister)
	time.Sleep(2 * watchDebounce)
	writeFile(t, filepath.Join(root, "after.go"), "package after")
	select {
	case params := <-notifications:
		t.Fatalf("notification after unregistration: %s", params)
	case <-time.After(watchPollInterval + time.Second):
	}
}

// bytesBuffer is a minimal io.ReadWriter so readLoop sees EOF after the frames.
type bytesBuffer struct{ data []byte }

func (buffer *bytesBuffer) Write(p []byte) (int, error) {
	buffer.data = append(buffer.data, p...)
	return len(p), nil
}

func (buffer *bytesBuffer) Read(p []byte) (int, error) {
	if len(buffer.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, buffer.data)
	buffer.data = buffer.data[n:]
	return n, nil
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

// Exercises the real adapter with compiled RelativePatterns and the unchanged
// 1000-change JSON notification bound, including the tail of a large burst.
func TestWatcherAdapterRelativeKindsAndNotificationBound(t *testing.T) {
	root := canonicalTempDir(t)
	mod := filepath.Join(root, "go.mod")
	writeFile(t, mod, "module a")
	batches := make(chan []fileChange, 32)
	w := newFileWatcher(root, func(raw json.RawMessage) error {
		var params struct {
			Changes []fileChange `json:"changes"`
		}
		if err := json.Unmarshal(raw, &params); err != nil {
			return err
		}
		batches <- params.Changes
		return nil
	}, func(string) {})
	// Pause the actual silent baseline before it can read the directory.
	// A registration response must not be forwarded while this scan is held.
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	releaseScan := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() { releaseScan(); w.close() })
	engine, err := filewatch.New(root, filewatch.Options{
		MaxFiles: maxWatchedFiles, MaxDirectories: maxWatchedDirectories, MaxChanges: maxChangesPerNotification,
		PollInterval: watchPollInterval, Debounce: watchDebounce,
		Allow: func(string, string) bool {
			enteredOnce.Do(func() { close(entered) })
			<-release
			return true
		},
	}, w.notifyBatch)
	if err != nil {
		t.Fatal(err)
	}
	w.engine = engine
	raw, _ := json.Marshal(map[string]any{"watchers": []any{
		map[string]any{"globPattern": map[string]any{"baseUri": fileURI(root), "pattern": "bulk/*.go"}, "kind": watchKindCreate},
		map[string]any{"globPattern": map[string]any{"baseUri": fileURI(root), "pattern": "go.mod"}, "kind": watchKindDelete},
	}})
	patterns, err := parseWatchers(root, raw)
	if err != nil {
		t.Fatal(err)
	}
	registered := make(chan error, 1)
	go func() { registered <- w.register("relative", patterns) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("baseline did not enter controlled scan")
	}
	select {
	case err := <-registered:
		t.Fatalf("registration acknowledged before silent baseline completed: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	releaseScan()
	select {
	case err := <-registered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("registration did not finish after baseline release")
	}
	// This is after the real registration acknowledgement, not after a sleep
	// or a probe that could hide an acknowledgement-to-baseline gap.
	writeFile(t, mod, "module changed but not subscribed")
	for i := 0; i < 1001; i++ {
		writeFile(t, filepath.Join(root, "bulk", fmt.Sprintf("file-%04d.go", i)), "package x")
	}
	if err := os.Remove(mod); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	deadline := time.NewTimer(watchPollInterval + 5*time.Second)
	defer deadline.Stop()
	for len(seen) < 1002 {
		select {
		case batch := <-batches:
			if len(batch) > 1000 {
				t.Fatalf("notification exceeds original LSP bound: %d", len(batch))
			}
			for _, change := range batch {
				want := fileChangeCreated
				if change.URI == fileURI(mod) {
					want = fileChangeDeleted
				}
				if change.Type != want || seen[change.URI] != 0 {
					t.Fatalf("kind or duplicate: %+v", change)
				}
				seen[change.URI] = change.Type
			}
		case <-deadline.C:
			t.Fatalf("missing large-burst tail: got %d/1002", len(seen))
		}
	}
	for i := 0; i < 1001; i++ {
		uri := fileURI(filepath.Join(root, "bulk", fmt.Sprintf("file-%04d.go", i)))
		if seen[uri] != fileChangeCreated {
			t.Fatalf("missing %s", uri)
		}
	}
	if seen[fileURI(mod)] != fileChangeDeleted {
		t.Fatal("delete bit 4 was confused with output kind 3")
	}
}

// The baseline scan of a large project can take seconds before the server
// is told its registration succeeded; the frontend shows it as progress.
func TestWatcherRegistrationReportsScanProgress(t *testing.T) {
	root := canonicalTempDir(t)
	writeFile(t, filepath.Join(root, "a.go"), "package a")
	var mu sync.Mutex
	var events []string
	core.SetEmitter(func(_ string, data any) {
		if event, ok := data.(serverEvent); ok {
			mu.Lock()
			events = append(events, event.Method+string(event.Params))
			mu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	w := newFileWatcher(root, func(json.RawMessage) error { return nil }, func(string) {})
	var output bytesBuffer
	s := &session{root: root, stdin: &output, watcher: w, pending: map[uint64]chan rpcReply{}}
	t.Cleanup(func() { s.close(nil) })
	var input bytesBuffer
	_ = writeMessage(&input, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "client/registerCapability", "params": map[string]any{
		"registrations": []any{map[string]any{"id": "one", "method": watchedFilesMethod, "registerOptions": map[string]any{"watchers": []any{map[string]any{"globPattern": "**/*.go"}}}}},
	}})
	s.readLoop(&input)
	mu.Lock()
	got := strings.Join(events, "|")
	mu.Unlock()
	want := fileWatchScanMethod + `{"state":"begin"}|` + fileWatchScanMethod + `{"state":"end"}|client/registerCapability`
	if !strings.HasPrefix(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
	// Registrations of other capabilities scan nothing and report nothing.
	events = nil
	input = bytesBuffer{}
	_ = writeMessage(&input, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "client/registerCapability", "params": map[string]any{
		"registrations": []any{map[string]any{"id": "two", "method": "textDocument/formatting"}},
	}})
	s.readLoop(&input)
	if strings.Contains(strings.Join(events, "|"), fileWatchScanMethod) {
		t.Fatalf("a non-watch registration reported a scan: %q", events)
	}
}

// A project larger than the watch limits must not get an error reply: some
// servers (Intelephense) exit on a rejected registration. The registration is
// acknowledged through the frontend, nothing is watched, and the frontend is
// told external changes will not reach the server.
func TestWatcherRegistrationOverLimitIsAcknowledgedWithoutWatching(t *testing.T) {
	for _, id := range []any{"watch-request", 7, -3} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			root := canonicalTempDir(t)
			writeFile(t, filepath.Join(root, "a.go"), "package a")
			writeFile(t, filepath.Join(root, "b.go"), "package b")
			var mu sync.Mutex
			var methods []string
			core.SetEmitter(func(_ string, data any) {
				if event, ok := data.(serverEvent); ok {
					mu.Lock()
					methods = append(methods, event.Method)
					mu.Unlock()
				}
			})
			t.Cleanup(func() { core.SetEmitter(nil) })
			var notified atomic.Int32
			w := newFileWatcher(root, func(json.RawMessage) error { notified.Add(1); return nil }, func(string) {})
			engine, err := filewatch.New(root, filewatch.Options{MaxFiles: 1, Debounce: time.Millisecond}, w.notifyBatch)
			if err != nil {
				t.Fatal(err)
			}
			w.engine = engine
			var output bytesBuffer
			s := &session{root: root, stdin: &output, watcher: w, pending: map[uint64]chan rpcReply{}}
			t.Cleanup(func() { s.close(nil) })
			var input bytesBuffer
			_ = writeMessage(&input, map[string]any{"jsonrpc": "2.0", "id": id, "method": "client/registerCapability", "params": map[string]any{
				"registrations": []any{map[string]any{"id": "one", "method": watchedFilesMethod, "registerOptions": map[string]any{"watchers": []any{map[string]any{"globPattern": "**/*.go"}}}}},
			}})
			s.readLoop(&input)
			if len(output.data) != 0 {
				t.Fatalf("an over-limit registration was refused to the server: %s", output.data)
			}
			mu.Lock()
			got := strings.Join(methods, ",")
			mu.Unlock()
			if got != fileWatchScanMethod+","+fileWatchScanMethod+","+fileWatchLimitedMethod+",client/registerCapability" {
				t.Fatalf("forwarded events = %q, want the scan begin/end, the limit notice, then the registration for the frontend to acknowledge", got)
			}
			if len(s.serverRequests) != 1 {
				t.Fatalf("the acknowledged registration must stay pending for the frontend reply: %d", len(s.serverRequests))
			}
			if !strings.Contains(s.logs(), "file-watcher-limited") || strings.Contains(s.logs(), "registration-refused") {
				t.Fatalf("an over-limit project must log a warning, not a refusal: %q", s.logs())
			}
			// Nothing is watched: a later change is not reported.
			writeFile(t, filepath.Join(root, "c.go"), "package c")
			time.Sleep(watchPollInterval + 500*time.Millisecond)
			if notified.Load() != 0 {
				t.Fatalf("an over-limit registration still delivered %d change notifications", notified.Load())
			}
		})
	}
}

func TestWatcherUnscannableRootIsNotAcknowledged(t *testing.T) {
	for _, state := range []string{"deleted", "renamed", "replaced-file", "symlink-root", "symlink-parent", "policy-denied"} {
		t.Run(state, func(t *testing.T) {
			base := canonicalTempDir(t)
			parent := filepath.Join(base, "parent")
			root := filepath.Join(parent, "project")
			outside := filepath.Join(base, "outside")
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(outside, "project"), 0700); err != nil {
				t.Fatal(err)
			}
			var forwarded atomic.Int32
			core.SetEmitter(func(_ string, data any) {
				// Scan progress is reported either way; only the registration counts.
				if event, ok := data.(serverEvent); ok && event.Method != fileWatchScanMethod {
					forwarded.Add(1)
				}
			})
			t.Cleanup(func() { core.SetEmitter(nil) })
			w := newFileWatcher(root, func(json.RawMessage) error { return nil }, func(string) {})
			opts := filewatch.Options{Debounce: time.Millisecond}
			if state == "policy-denied" {
				opts.Allow = func(relative, _ string) bool { return relative != "." }
			}
			engine, err := filewatch.New(root, opts, w.notifyBatch)
			if err != nil {
				t.Fatal(err)
			}
			w.engine = engine
			var output bytesBuffer
			s := &session{root: root, stdin: &output, watcher: w, pending: map[uint64]chan rpcReply{}}
			t.Cleanup(func() { s.close(nil) })
			// Constructor validation has passed. Lose the root before any
			// registration scan; source_other cannot supply a native loss hint.
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
			var input bytesBuffer
			_ = writeMessage(&input, map[string]any{"jsonrpc": "2.0", "id": "root-request", "method": "client/registerCapability", "params": map[string]any{
				"registrations": []any{map[string]any{"id": "root", "method": watchedFilesMethod, "registerOptions": map[string]any{"watchers": []any{map[string]any{"globPattern": "**/*"}}}}},
			}})
			s.readLoop(&input)
			if forwarded.Load() != 0 || len(output.data) == 0 {
				t.Fatalf("%s root forwarded for false success: forwarded=%d response=%s", state, forwarded.Load(), output.data)
			}
			raw, err := readMessage(bufio.NewReader(&output))
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				ID    string `json:"id"`
				Error struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(raw, &response) != nil || response.ID != "root-request" || response.Error.Code != -32603 {
				t.Fatalf("root refusal reply: %s", raw)
			}
			if len(s.serverRequests) != 0 {
				t.Fatal("root refusal leaked pending request")
			}
		})
	}
}

func TestSessionCloseReleasesRegistrationWaitBeforeJoiningScan(t *testing.T) {
	root := canonicalTempDir(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	w := newFileWatcher(root, func(json.RawMessage) error { return nil }, func(string) {})
	engine, err := filewatch.New(root, filewatch.Options{Debounce: time.Millisecond, Allow: func(string, string) bool {
		once.Do(func() { close(entered) })
		<-release
		return true
	}}, w.notifyBatch)
	if err != nil {
		t.Fatal(err)
	}
	w.engine = engine
	s := &session{root: root, stdin: io.Discard, watcher: w, pending: map[uint64]chan rpcReply{}}
	readDone, closed := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { unblock(); s.close(nil); <-readDone })
	var input bytesBuffer
	_ = writeMessage(&input, map[string]any{"jsonrpc": "2.0", "id": 11, "method": "client/registerCapability", "params": map[string]any{
		"registrations": []any{map[string]any{"id": "one", "method": watchedFilesMethod, "registerOptions": map[string]any{"watchers": []any{map[string]any{"globPattern": "**/*"}}}}},
	}})
	go func() { s.readLoop(&input); close(readDone) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("scan did not enter")
	}
	go func() { s.close(io.EOF); close(closed) }()
	select {
	case <-readDone: // cancellation must not need readLoop's lock or its exit
	case <-time.After(time.Second):
		t.Fatal("Close could not release registration wait")
	}
	select {
	case <-closed:
		t.Fatal("Close did not join the held scan")
	default:
	}
	unblock()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not join after scan release")
	}
}

type failingWatchWriter struct {
	entered, release chan struct{}
	once             sync.Once
}

func (w *failingWatchWriter) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return 0, io.ErrClosedPipe
}

func registerSessionWatchForCloseTest(t *testing.T, s *session) *fileWatcher {
	t.Helper()
	w := s.fileWatcher()
	raw := json.RawMessage(`{"watchers":[{"globPattern":"**/*.go"}]}`)
	patterns, err := parseWatchers(s.root, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.register("close-test", patterns); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * watchDebounce)
	return w
}

// Exercises emitter -> notify -> write -> fail, not a synthetic engine emitter.
func TestWatcherSessionWriteFailureDoesNotSelfJoin(t *testing.T) {
	core.SetEmitter(func(string, any) {})
	t.Cleanup(func() { core.SetEmitter(nil) })
	writer := &failingWatchWriter{entered: make(chan struct{}), release: make(chan struct{})}
	s := &session{root: canonicalTempDir(t), stdin: writer, pending: map[uint64]chan rpcReply{}}
	w := registerSessionWatchForCloseTest(t, s)
	writeFile(t, filepath.Join(s.root, "failed.go"), "package failed")
	select {
	case <-writer.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("watch notification did not enter controlled writer")
	}
	close(writer.release)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for !s.closed.Load() {
		select {
		case <-deadline.C:
			t.Fatal("write failure did not close session")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	joined := make(chan struct{})
	go func() { s.close(io.EOF); _ = w.engine.Close(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("failed watch notification synchronously joined its own loop")
	}
	if s.failureText() == "" {
		t.Fatal("write failure reason lost")
	}
}

type unreadWatchStdin struct {
	io.WriteCloser
	entered, returned chan struct{}
	once              sync.Once
}

func (w *unreadWatchStdin) Write(data []byte) (int, error) {
	large := len(data) > 65536
	if large {
		w.once.Do(func() { close(w.entered) })
	}
	n, err := w.WriteCloser.Write(data)
	if large {
		close(w.returned)
	}
	return n, err
}

// The helper child never reads stdin, so the large watcher notification really
// blocks in an OS pipe. Closing must interrupt IO before joining the watcher.
func TestWatcherSessionCloseUnblocksUnreadStdin(t *testing.T) {
	if os.Getenv("MINCODE_WATCH_UNREAD_STDIN") == "1" {
		_, _ = io.WriteString(os.Stdout, "ready\n")
		for {
			time.Sleep(time.Hour)
		}
	}
	core.SetEmitter(func(string, any) {})
	t.Cleanup(func() { core.SetEmitter(nil) })
	cmd := exec.Command(os.Args[0], "-test.run=^TestWatcherSessionCloseUnblocksUnreadStdin$")
	cmd.Env = append(os.Environ(), "MINCODE_WATCH_UNREAD_STDIN=1")
	core.PrepareProcessTree(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = core.KillProcessTree(cmd) })
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && line != "ready\n" {
			err = fmt.Errorf("unexpected helper greeting %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("helper did not become ready")
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	writer := &unreadWatchStdin{WriteCloser: stdin, entered: make(chan struct{}), returned: make(chan struct{})}
	s := &session{root: canonicalTempDir(t), cmd: cmd, stdin: writer, pending: map[uint64]chan rpcReply{}}
	w := registerSessionWatchForCloseTest(t, s)
	staging := canonicalTempDir(t)
	for i := 0; i < 1000; i++ {
		writeFile(t, filepath.Join(staging, fmt.Sprintf("%04d-%s.go", i, strings.Repeat("x", 180))), "package unread")
	}
	if err := os.Rename(staging, filepath.Join(s.root, "bulk")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writer.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("large watch notification did not reach stdin")
	}
	select {
	case <-writer.returned:
		t.Fatal("helper pipe did not block the large write")
	case <-time.After(100 * time.Millisecond):
	}
	closed := make(chan struct{})
	go func() { s.close(io.EOF); close(closed) }()
	timedOut := false
	select {
	case <-closed:
	case <-time.After(time.Second):
		timedOut = true
	}
	// Always release the OS IO/child, including on the RED implementation.
	_ = stdin.Close()
	_ = core.KillProcessTree(cmd)
	for _, done := range []chan struct{}{writer.returned, closed, exited} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("session IO/process did not join after cleanup")
		}
	}
	joined := make(chan struct{})
	go func() { _ = w.engine.Close(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("watcher loop did not join")
	}
	if timedOut {
		t.Fatal("session close joined watcher before releasing blocked stdin/process")
	}
}
