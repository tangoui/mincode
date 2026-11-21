package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/filewatch"
	backendfs "mincode/app/backend/fs"
	"mincode/app/plugins/api"
)

type fileEventWire struct {
	Event string `json:"event"`
	Data  struct {
		Paths   []string `json:"paths"`
		Renames []struct {
			OldPath string `json:"oldPath"`
			NewPath string `json:"newPath"`
		} `json:"renames"`
		ScopeRevision  string `json:"scopeRevision"`
		Origin         string `json:"origin"`
		RescanRequired bool   `json:"rescanRequired"`
		Reason         string `json:"reason"`
	} `json:"data"`
}

// Exercise real event serialization, inflight admission and generation RPC. Only
// the external process pipe is replaced with a deterministic acknowledging peer.
func fileEventsFixture(t *testing.T) (*pluginManager, *processRuntime, string, chan []byte) {
	return fileEventsFixtureWithAck(t, func() bool { return true })
}
func fileEventsFixtureWithAck(t *testing.T, acknowledge func() bool) (*pluginManager, *processRuntime, string, chan []byte) {
	t.Helper()
	root, err := canonicalWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := &pluginManager{records: map[string]*pluginRecord{}}
	rt := newProcessRuntime(root, Manifest{ID: "files.test", Capabilities: []api.Capability{api.CapabilityWorkspaceRead}}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	rt.cmd, rt.stdin, rt.generation, rt.runContext = &exec.Cmd{}, writer, 1, ctx
	rt.pending, rt.writes = map[string]chan rpcEnvelope{}, make(chan rpcWriteRequest, 32)
	rt.eventSubscriptions = map[string]struct{}{"workspace.fileCreated": {}, "workspace.fileDeleted": {}, "workspace.fileRenamed": {}, "workspace.rescanRequired": {}}
	rec := &pluginRecord{id: "files.test", enabled: true, state: pluginStateRunning, runtime: rt}
	rt.ownerManager, rt.ownerRecord = m, rec
	m.records[rec.id], m.order = rec, []string{rec.id}
	ticket, err := m.beginWorkspaceScope(ctx, "A", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.CommitWorkspaceScope("A", ticket.Revision, root); err != nil {
		t.Fatal(err)
	}
	output := make(chan []byte, 256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-rt.writes:
				var msg rpcEnvelope
				_ = json.Unmarshal(req.data, &msg)
				output <- append([]byte(nil), msg.Params...)
				req.result <- nil
				if acknowledge() {
					rt.deliverResponse(1, rpcEnvelope{ID: msg.ID, Result: json.RawMessage(`{}`)}, false)
				}
			}
		}
	}()
	t.Cleanup(func() {
		rec.mu.Lock()
		rec.state = pluginStateStopped
		rec.mu.Unlock()
		rt.mu.Lock()
		rt.cmd, rt.stdin = nil, nil
		rt.mu.Unlock()
		m.Shutdown(context.Background())
		cancel()
		_ = reader.Close()
		_ = writer.Close()
		<-done
	})
	m.syncWorkspaceFileEvents()
	return m, rt, root, output
}

func prepareFileEvent(t *testing.T, m *pluginManager, before backendfs.Mutation) func(backendfs.Mutation) {
	t.Helper()
	observer, ok := any(m).(interface {
		observeWorkspaceMutation(backendfs.Mutation) func(backendfs.Mutation)
	})
	if !ok {
		t.Fatal("workspace mutation observation seam is missing")
	}
	completion := observer.observeWorkspaceMutation(before)
	if completion == nil {
		t.Fatal("ready scope mutation was not observed")
	}
	return completion
}
func readFileEvents(t *testing.T, output <-chan []byte, wait time.Duration) []fileEventWire {
	t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var result []fileEventWire
	for {
		select {
		case raw := <-output:
			var event fileEventWire
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			result = append(result, event)
		case <-timer.C:
			return result
		}
	}
}

// Removing either side's lexical/canonical filter would leak the hidden name.
func TestWorkspaceFileEventsSensitiveOldPath(t *testing.T) {
	for _, tc := range []struct{ old, next, event string }{{".env", "public.txt", "workspace.fileCreated"}, {"public.txt", ".env", "workspace.fileDeleted"}} {
		t.Run(tc.event, func(t *testing.T) {
			m, _, root, output := fileEventsFixture(t)
			writeWorkspaceFile(t, root, tc.old, "x")
			before := backendfs.Mutation{Root: root, Kind: "rename", OldPath: tc.old, NewPath: tc.next, OldCanonical: filepath.Join(root, tc.old), NewCanonical: filepath.Join(root, tc.next)}
			finish := prepareFileEvent(t, m, before)
			if err := os.Rename(filepath.Join(root, tc.old), filepath.Join(root, tc.next)); err != nil {
				t.Fatal(err)
			}
			finish(before)
			events := readFileEvents(t, output, 400*time.Millisecond)
			if len(events) != 1 || events[0].Event != tc.event || len(events[0].Data.Paths) != 1 || events[0].Data.Paths[0] != "public.txt" {
				t.Fatalf("filtered rename = %+v", events)
			}
			raw, _ := json.Marshal(events)
			if strings.Contains(string(raw), ".env") {
				t.Fatalf("sensitive substring in %s", raw)
			}
		})
	}
}

// Capturing scope at success instead of prepare mislabels this old operation.
func TestWorkspaceFileEventsScopeLifecycle(t *testing.T) {
	m, _, root, output := fileEventsFixture(t)
	before := backendfs.Mutation{Root: root, Kind: "write", NewPath: "new.txt", NewCanonical: filepath.Join(root, "new.txt")}
	finish := prepareFileEvent(t, m, before)
	ticket, err := m.beginWorkspaceScope(context.Background(), "B", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.CommitWorkspaceScope("B", ticket.Revision, root); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, root, "new.txt", "x")
	finish(before)
	if events := readFileEvents(t, output, 400*time.Millisecond); len(events) != 0 {
		t.Fatalf("stale mutation delivered: %+v", events)
	}
}

// Losing the 128-entry split or dedupe turns one burst into duplicates/oversize.
func TestWorkspaceFileEventsDedupeOverflow(t *testing.T) {
	m, _, root, output := fileEventsFixture(t)
	for i := 0; i < 129; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		mutation := backendfs.Mutation{Root: root, Kind: "write", NewPath: name, NewCanonical: filepath.Join(root, name)}
		finish := prepareFileEvent(t, m, mutation)
		writeWorkspaceFile(t, root, name, "x")
		finish(mutation)
		finish(mutation)
	}
	events := readFileEvents(t, output, 500*time.Millisecond)
	count := 0
	for _, event := range events {
		if event.Event != "workspace.fileCreated" {
			continue
		}
		count += len(event.Data.Paths)
		raw, _ := json.Marshal(event)
		if len(event.Data.Paths) > 128 || len(raw) >= 65536 {
			t.Fatalf("oversized batch: %d %d", len(event.Data.Paths), len(raw))
		}
	}
	if count != 129 {
		t.Fatalf("created count=%d want 129; events=%+v", count, events)
	}
}

func TestWorkspaceFileEventsHostSymlinkExcluded(t *testing.T) {
	m, _, root, output := fileEventsFixture(t)
	if err := os.Mkdir(filepath.Join(root, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "src"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	mutation := backendfs.Mutation{Root: root, Kind: "write", NewPath: "alias/file.txt", NewCanonical: filepath.Join(root, "src/file.txt")}
	finish := prepareFileEvent(t, m, mutation)
	writeWorkspaceFile(t, root, "src/file.txt", "x")
	finish(mutation)
	for _, event := range readFileEvents(t, output, 400*time.Millisecond) {
		if len(event.Data.Paths) > 0 {
			t.Fatalf("symlink path announced: %+v", event)
		}
	}
}

func TestWorkspaceFileEventsUnknownOldAndCanonicalDenied(t *testing.T) {
	m, _, root, output := fileEventsFixture(t)
	m.syncWorkspaceFileEvents()
	s := m.fileEvents.Load()
	s.observeWatchBatch(filewatch.Batch{Changes: []filewatch.Change{{Path: "unknown-secret.txt", Kind: filewatch.Deleted}}})
	events := readFileEvents(t, output, 350*time.Millisecond)
	if len(events) != 1 || events[0].Event != "workspace.rescanRequired" || events[0].Data.Reason != "sourceLost" {
		t.Fatalf("unknown delete = %+v", events)
	}
	for _, entry := range []filewatch.Entry{{Path: "ordinary.txt", Canonical: filepath.Join(root, ".env")}, {Path: ".env", Canonical: filepath.Join(root, "ordinary.txt")}} {
		s.observeWatchBatch(filewatch.Batch{Changes: []filewatch.Change{{Path: entry.Path, Kind: filewatch.Deleted, Before: &entry}}})
	}
	if events = readFileEvents(t, output, 250*time.Millisecond); len(events) != 0 {
		t.Fatalf("denied canonical/requested path delivered: %+v", events)
	}
}

func TestWorkspaceFileEventsHostWatchPairDedupe(t *testing.T) {
	m, _, root, output := fileEventsFixture(t)
	writeWorkspaceFile(t, root, "old.txt", "x")
	mutation := backendfs.Mutation{Root: root, Kind: "rename", OldPath: "old.txt", NewPath: "new.txt", OldCanonical: filepath.Join(root, "old.txt"), NewCanonical: filepath.Join(root, "new.txt")}
	finish := prepareFileEvent(t, m, mutation)
	if err := os.Rename(mutation.OldCanonical, mutation.NewCanonical); err != nil {
		t.Fatal(err)
	}
	finish(mutation)
	s := m.fileEvents.Load()
	s.observeWatchBatch(filewatch.Batch{Changes: []filewatch.Change{{Path: "old.txt", Kind: filewatch.Deleted, Before: &filewatch.Entry{Path: "old.txt", Canonical: mutation.OldCanonical}}, {Path: "new.txt", Kind: filewatch.Created}}})
	events := readFileEvents(t, output, 400*time.Millisecond)
	if len(events) != 1 || events[0].Event != "workspace.fileRenamed" || len(events[0].Data.Renames) != 1 || events[0].Data.Renames[0].OldPath != "old.txt" || events[0].Data.Renames[0].NewPath != "new.txt" {
		t.Fatalf("host/watch rename = %+v", events)
	}
}

func TestWorkspaceFileEventsPendingBound(t *testing.T) {
	m, _, _, output := fileEventsFixture(t)
	m.syncWorkspaceFileEvents()
	s := m.fileEvents.Load()
	changes := make([]workspaceFileChange, 2049)
	for i := range changes {
		changes[i] = workspaceFileChange{event: hostEventFileCreated, origin: "host", path: fmt.Sprintf("file%04d.txt", i)}
	}
	s.enqueue(changes, "")
	s.mu.Lock()
	pending, dedupe, reason := len(s.pending), len(s.dedupe), s.reason
	s.mu.Unlock()
	if pending != 2048 || dedupe > 2048 || reason != "overflow" {
		t.Fatalf("bounds: pending=%d dedupe=%d reason=%q", pending, dedupe, reason)
	}
	events := readFileEvents(t, output, 500*time.Millisecond)
	informed := false
	for _, event := range events {
		if event.Event == "workspace.rescanRequired" && event.Data.Reason == "overflow" {
			informed = true
		}
		if event.Data.RescanRequired {
			informed = true
		}
	}
	if !informed {
		t.Fatalf("overflow never signalled: %+v", events)
	}
}

func TestWorkspaceFileEventsDeliveryLost(t *testing.T) {
	var ack atomic.Bool
	m, rt, _, output := fileEventsFixtureWithAck(t, ack.Load)
	m.syncWorkspaceFileEvents()
	s := m.fileEvents.Load()
	for i := 0; i < 8; i++ {
		m.publishWorkspaceFileBatch(s, hostEventFileCreated, workspaceEventBatch{Paths: []string{fmt.Sprintf("first%d.txt", i)}, ScopeRevision: s.scope.Revision, Origin: "host"})
	}
	first := readFileEvents(t, output, 100*time.Millisecond)
	if len(first) != 8 || rt.eventsInflight.Load() != 8 {
		t.Fatalf("expected eight blocked deliveries: %d inflight=%d", len(first), rt.eventsInflight.Load())
	}
	m.publishWorkspaceFileBatch(s, hostEventFileCreated, workspaceEventBatch{Paths: []string{"dropped.txt"}, ScopeRevision: s.scope.Revision, Origin: "host"})
	ack.Store(true)
	rt.mu.Lock()
	ids := make([]string, 0, len(rt.pending))
	for id := range rt.pending {
		ids = append(ids, id)
	}
	rt.mu.Unlock()
	for _, id := range ids {
		raw, _ := json.Marshal(id)
		rt.deliverResponse(1, rpcEnvelope{ID: raw, Result: json.RawMessage(`{}`)}, false)
	}
	deadline := time.Now().Add(time.Second)
	for rt.eventsInflight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("deliveries did not drain")
		}
		time.Sleep(time.Millisecond)
	}
	m.publishWorkspaceFileBatch(s, hostEventFileCreated, workspaceEventBatch{Paths: []string{"next.txt"}, ScopeRevision: s.scope.Revision, Origin: "host"})
	events := readFileEvents(t, output, 150*time.Millisecond)
	batch, rescan := false, false
	for _, event := range events {
		if event.Event == "workspace.fileCreated" && event.Data.RescanRequired {
			batch = true
		}
		if event.Event == "workspace.rescanRequired" && event.Data.Reason == "deliveryLost" {
			rescan = true
		}
	}
	if !batch || !rescan {
		t.Fatalf("loss not carried into next delivery: %+v", events)
	}
}

func TestWorkspaceFileEventsWriteOverwriteAndDirectory(t *testing.T) {
	m, _, root, output := fileEventsFixture(t)
	for _, kind := range []string{"write", "paste"} {
		name := kind + ".txt"
		writeWorkspaceFile(t, root, name, "before")
		mutation := backendfs.Mutation{Root: root, Kind: kind, NewPath: name, NewCanonical: filepath.Join(root, name)}
		finish := prepareFileEvent(t, m, mutation)
		writeWorkspaceFile(t, root, name, "after")
		finish(mutation)
	}
	if events := readFileEvents(t, output, 350*time.Millisecond); len(events) != 0 {
		t.Fatalf("overwrite announced as create: %+v", events)
	}
	mutation := backendfs.Mutation{Root: root, Kind: "delete", OldPath: "dir", OldCanonical: filepath.Join(root, "dir"), IsDir: true}
	prepareFileEvent(t, m, mutation)(mutation)
	events := readFileEvents(t, output, 300*time.Millisecond)
	if len(events) != 1 || events[0].Event != "workspace.rescanRequired" {
		t.Fatalf("directory operation enumerated files: %+v", events)
	}
}

func TestWorkspaceFileEventsWorkspaceWriteSource(t *testing.T) {
	m, rt, root, output := fileEventsFixture(t)
	previous := pluginWorkspaceRoot
	pluginWorkspaceRoot = m.workspaceRootPath
	t.Cleanup(func() { pluginWorkspaceRoot = previous })
	params, _ := json.Marshal(map[string]any{"path": "created.txt", "data": base64.StdEncoding.EncodeToString([]byte("content must not be sent"))})
	if _, rpcErr := hostWorkspaceWrite(rt, params); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	events := readFileEvents(t, output, 350*time.Millisecond)
	if len(events) != 1 || events[0].Event != "workspace.fileCreated" || events[0].Data.Origin != "host" || events[0].Data.Paths[0] != "created.txt" {
		t.Fatalf("workspace.write did not share host observation: %+v", events)
	}
	if _, rpcErr := hostWorkspaceWrite(rt, params); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if events = readFileEvents(t, output, 250*time.Millisecond); len(events) != 0 {
		t.Fatalf("workspace overwrite became create: %+v", events)
	}
	if _, err := os.Stat(filepath.Join(root, "created.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceFileEventsSourceOwnership(t *testing.T) {
	m, rt, root, output := fileEventsFixture(t)
	first := m.fileEvents.Load()
	if first == nil {
		t.Fatal("first effective running subscriber did not start source")
	}
	request, _ := json.Marshal(map[string]any{"events": []string{"workspace.fileCreated"}})
	for i := 0; i < 2; i++ {
		if _, err := hostEventsSubscribe(rt, request); err != nil {
			t.Fatal(err)
		}
	}
	if m.fileEvents.Load() != first {
		t.Fatal("subscription duplicated watcher")
	}
	before := backendfs.Mutation{Root: root, Kind: "write", NewPath: "late.txt", NewCanonical: filepath.Join(root, "late.txt")}
	finish := prepareFileEvent(t, m, before)
	finish(before) // already queued work must also be discarded, not retained
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	if m.fileEvents.Load() != nil {
		t.Fatal("last unsubscribe retained source")
	}
	select {
	case <-first.done:
	default:
		t.Fatal("unsubscribe did not join watcher/timer owner")
	}
	first.mu.Lock()
	retained := len(first.pending) + len(first.dedupe)
	first.mu.Unlock()
	if retained != 0 {
		t.Fatalf("closed source retained %d queued/history entries", retained)
	}
	finish(before)
	if events := readFileEvents(t, output, 200*time.Millisecond); len(events) != 0 {
		t.Fatalf("late completion after unsubscribe: %+v", events)
	}
	for _, state := range []string{pluginStateStopped, pluginStateDisabled, pluginStateError} {
		rt.ownerRecord.mu.Lock()
		rt.ownerRecord.state = state
		rt.ownerRecord.mu.Unlock()
		if _, err := hostEventsSubscribe(rt, request); err != nil {
			t.Fatal(err)
		}
		if m.fileEvents.Load() != nil {
			t.Fatalf("%s subscriber started source", state)
		}
	}
	rt.ownerRecord.mu.Lock()
	rt.ownerRecord.state = pluginStateRunning
	rt.ownerRecord.scope = pluginScopeWorkspace
	rt.ownerRecord.workspaces = map[string]struct{}{"other": {}}
	rt.ownerRecord.mu.Unlock()
	m.syncWorkspaceFileEvents()
	if m.fileEvents.Load() != nil {
		t.Fatal("workspace-inactive subscriber started source")
	}
	rt.ownerRecord.mu.Lock()
	rt.ownerRecord.scope = pluginScopeGlobal
	rt.ownerRecord.mu.Unlock()
	m.syncWorkspaceFileEvents()
	source := m.fileEvents.Load()
	if source == nil {
		t.Fatal("effective subscriber did not restart source")
	}
	rt.ownerRecord.mu.Lock()
	rt.ownerRecord.state = pluginStateStopped
	rt.ownerRecord.mu.Unlock()
	m.Shutdown(context.Background())
	if m.fileEvents.Load() != nil {
		t.Fatal("shutdown retained source")
	}
	select {
	case <-source.done:
	default:
		t.Fatal("shutdown did not join source")
	}
	finish(before)
	if events := readFileEvents(t, output, 200*time.Millisecond); len(events) != 0 {
		t.Fatalf("completion after shutdown: %+v", events)
	}
}

func TestWorkspaceFileEventsUnflaggedSuccessDoesNotRepairLoss(t *testing.T) {
	m, rt, _, output := fileEventsFixture(t)
	s := m.fileEvents.Load()
	rt.eventsInflight.Store(8)
	m.publishWorkspaceFileBatch(s, hostEventFileCreated, workspaceEventBatch{Paths: []string{"lost.txt"}, ScopeRevision: s.scope.Revision, Origin: "host"})
	rt.eventsInflight.Store(0)
	// Models a batch built before another publisher detected loss, but admitted
	// after it. An unflagged success cannot acknowledge an undelivered warning.
	m.deliverWorkspaceFileEvent(s, rt.ownerRecord, rt, 1, hostEventFileCreated, workspaceEventBatch{Paths: []string{"racing.txt"}, ScopeRevision: s.scope.Revision, Origin: "host"})
	_ = readFileEvents(t, output, 100*time.Millisecond)
	m.publishWorkspaceFileBatch(s, hostEventFileCreated, workspaceEventBatch{Paths: []string{"next.txt"}, ScopeRevision: s.scope.Revision, Origin: "host"})
	events := readFileEvents(t, output, 150*time.Millisecond)
	for _, event := range events {
		if event.Event == hostEventFileCreated && event.Data.RescanRequired {
			return
		}
	}
	t.Fatalf("unflagged success erased pending loss: %+v", events)
}

func TestWorkspaceFileEventsNativeMutations(t *testing.T) {
	m, _, root, output := fileEventsFixture(t)
	off := backendfs.OnMutation(m.observeWorkspaceMutation)
	defer off()
	invoke := func(command string, args map[string]any, want string) {
		t.Helper()
		args["projectRoot"] = root
		raw, _ := json.Marshal(args)
		if _, err := core.Dispatch(command, string(raw)); err != nil {
			t.Fatal(err)
		}
		events := readFileEvents(t, output, 350*time.Millisecond)
		if want == "" {
			if len(events) != 0 {
				t.Fatalf("%s overwrite events: %+v", command, events)
			}
			return
		}
		if len(events) != 1 || events[0].Event != want || events[0].Data.Origin != "host" || events[0].Data.ScopeRevision != "1" {
			t.Fatalf("%s events: %+v", command, events)
		}
	}
	invoke("local_fs_write_text_file", map[string]any{"filePath": "new.txt", "content": "private body"}, "workspace.fileCreated")
	invoke("local_fs_write_text_file", map[string]any{"filePath": "new.txt", "content": "edited"}, "")
	invoke("local_fs_rename", map[string]any{"oldPath": "new.txt", "newName": "renamed.txt"}, "workspace.fileRenamed")
	invoke("local_fs_delete", map[string]any{"path": "renamed.txt"}, "workspace.fileDeleted")
	external := filepath.Join(t.TempDir(), "paste.txt")
	if err := os.WriteFile(external, []byte("private body"), 0600); err != nil {
		t.Fatal(err)
	}
	invoke("local_fs_paste_files", map[string]any{"destDir": "", "srcPaths": []string{external}}, "workspace.fileCreated")
	off()
	// A completion captured before unregister is separately guarded by source
	// cancellation (SourceOwnership and ScopeLifecycle), not by off() joining it.
}

func TestWorkspaceFileEventsEncodingBound(t *testing.T) {
	m, _, _, output := fileEventsFixture(t)
	s := m.fileEvents.Load()
	changes := make([]workspaceFileChange, 90)
	for i := range changes {
		name := strings.Repeat("directory/", 80) + fmt.Sprintf("file%03d.txt", i)
		changes[i] = workspaceFileChange{event: hostEventFileRenamed, origin: "host", rename: workspaceRename{name + "old", name + "new"}}
	}
	s.enqueue(changes, "")
	events := readFileEvents(t, output, 400*time.Millisecond)
	count := 0
	batches := 0
	for _, event := range events {
		if event.Event != "workspace.fileRenamed" {
			continue
		}
		raw, _ := json.Marshal(event)
		if len(raw) >= 65536 || len(event.Data.Renames) > 128 {
			t.Fatalf("encoded bound: %d bytes, %d pairs", len(raw), len(event.Data.Renames))
		}
		count += len(event.Data.Renames)
		batches++
	}
	if count != 90 || batches < 2 {
		t.Fatalf("encoding split lost pairs: count=%d batches=%d", count, batches)
	}
}

func TestWorkspaceFileEventsDeliveryTimeoutWithoutRescanSubscription(t *testing.T) {
	var ack atomic.Bool
	m, rt, _, output := fileEventsFixtureWithAck(t, ack.Load)
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":["workspace.fileCreated"]}`)); err != nil {
		t.Fatal(err)
	}
	s := m.fileEvents.Load()
	started := time.Now()
	m.publishWorkspaceFileBatch(s, hostEventFileCreated, workspaceEventBatch{Paths: []string{"timeout.txt"}, ScopeRevision: s.scope.Revision, Origin: "host"})
	_ = readFileEvents(t, output, 100*time.Millisecond)
	deadline := started.Add(4 * time.Second)
	for rt.eventsInflight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("3s RPC timeout did not release inflight")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if time.Since(started) < 2900*time.Millisecond {
		t.Fatal("delivery did not exercise RPC timeout")
	}
	ack.Store(true)
	m.publishWorkspaceFileBatch(s, hostEventFileCreated, workspaceEventBatch{Paths: []string{"after.txt"}, ScopeRevision: s.scope.Revision, Origin: "host"})
	events := readFileEvents(t, output, 150*time.Millisecond)
	if len(events) != 1 || events[0].Event != hostEventFileCreated || !events[0].Data.RescanRequired {
		t.Fatalf("loss unavailable without rescan subscription: %+v", events)
	}
	m.publishWorkspaceFileBatch(s, hostEventFileCreated, workspaceEventBatch{Paths: []string{"repaired.txt"}, ScopeRevision: s.scope.Revision, Origin: "host"})
	events = readFileEvents(t, output, 100*time.Millisecond)
	if len(events) != 1 || events[0].Data.RescanRequired {
		t.Fatalf("successful flagged delivery did not clear loss: %+v", events)
	}
}

func TestWorkspaceFileEventsExternalWatcher(t *testing.T) {
	m, _, root, output := fileEventsFixture(t)
	// Let the shared watcher's silent baseline settle, then mutate outside the
	// host seam. This exercises the native/FSEvents or bounded polling path.
	if events := readFileEvents(t, output, 300*time.Millisecond); len(events) != 0 {
		t.Fatalf("baseline replay: %+v", events)
	}
	writeWorkspaceFile(t, root, "external.txt", "x")
	events := readFileEvents(t, output, 2500*time.Millisecond)
	found := false
	for _, event := range events {
		if event.Event == hostEventFileCreated && event.Data.Origin == "watcher" && len(event.Data.Paths) == 1 && event.Data.Paths[0] == "external.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("external persistent create missing: %+v (source=%p)", events, m.fileEvents.Load())
	}
	if err := os.Remove(filepath.Join(root, "external.txt")); err != nil {
		t.Fatal(err)
	}
	events = readFileEvents(t, output, 2500*time.Millisecond)
	found = false
	for _, event := range events {
		if event.Event == hostEventFileDeleted && event.Data.Origin == "watcher" && len(event.Data.Paths) == 1 && event.Data.Paths[0] == "external.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("safe-snapshot external delete missing: %+v", events)
	}
}

func TestWorkspaceFileEventsSubscriptionDoesNotReplayQueuedBatch(t *testing.T) {
	m, rt, root, output := fileEventsFixture(t)
	mutation := backendfs.Mutation{Root: root, Kind: "write", NewPath: "prior-generation.txt", NewCanonical: filepath.Join(root, "prior-generation.txt")}
	prepareFileEvent(t, m, mutation)(mutation)
	// A fresh subscription (including one made by a replacement generation)
	// must not inherit a shared source's already pending per-consumer batch.
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":["workspace.fileCreated"]}`)); err != nil {
		t.Fatal(err)
	}
	if events := readFileEvents(t, output, 300*time.Millisecond); len(events) != 0 {
		t.Fatalf("new subscription inherited old queued event: %+v", events)
	}
}

func TestWorkspaceFileEventsShutdownRejectsQueue(t *testing.T) {
	m, _, root, _ := fileEventsFixture(t)
	s := m.fileEvents.Load()
	mutation := backendfs.Mutation{Root: root, Kind: "write", NewPath: "late.txt", NewCanonical: filepath.Join(root, "late.txt")}
	finish := prepareFileEvent(t, m, mutation)
	m.mu.Lock()
	m.shuttingDown = true
	m.mu.Unlock()
	finish(mutation)
	s.mu.Lock()
	pending := len(s.pending)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatalf("shutdown completion queued %d changes before scope cancellation", pending)
	}
}

func TestWorkspaceFileEventsUnavailableSource(t *testing.T) {
	_, rt, root, _ := fileEventsFixture(t)
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":["workspace.fileCreated"]}`)); err == nil {
		t.Fatal("accepted subscription despite failed source registration")
	}
}

func TestWorkspaceFileEventsOldScopeCannotEraseNewLoss(t *testing.T) {
	m, rt, root, output := fileEventsFixture(t)
	old := m.fileEvents.Load()
	ticket, err := m.beginWorkspaceScope(context.Background(), "B", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.CommitWorkspaceScope("B", ticket.Revision, root); err != nil {
		t.Fatal(err)
	}
	current := m.fileEvents.Load()
	rt.eventsInflight.Store(8)
	m.publishWorkspaceFileBatch(current, hostEventFileCreated, workspaceEventBatch{Paths: []string{"lost.txt"}, ScopeRevision: current.scope.Revision, Origin: "host"})
	rt.eventsInflight.Store(0)
	// Even admission of an old scope's delayed delivery must not reset the new
	// revision's pending loss before the asynchronous stale-scope check runs.
	m.deliverWorkspaceFileEvent(old, rt.ownerRecord, rt, 1, hostEventFileCreated, workspaceEventBatch{Paths: []string{"stale.txt"}, ScopeRevision: old.scope.Revision, Origin: "host"})
	m.publishWorkspaceFileBatch(current, hostEventFileCreated, workspaceEventBatch{Paths: []string{"next.txt"}, ScopeRevision: current.scope.Revision, Origin: "host"})
	events := readFileEvents(t, output, 200*time.Millisecond)
	for _, event := range events {
		if event.Event == hostEventFileCreated && event.Data.RescanRequired && event.Data.ScopeRevision == ticket.Revision {
			return
		}
	}
	t.Fatalf("old source erased new scope loss: %+v", events)
}

func TestWorkspaceFileEventsTwoConsumersShareSource(t *testing.T) {
	m, rt, root, _ := fileEventsFixture(t)
	first := m.fileEvents.Load()
	second := newProcessRuntime(root, Manifest{ID: "second.test", Capabilities: []api.Capability{api.CapabilityWorkspaceRead}}, nil, nil)
	record := &pluginRecord{id: "second.test", enabled: true, state: pluginStateRunning, runtime: second}
	second.cmd, second.generation, second.ownerManager, second.ownerRecord = &exec.Cmd{}, 1, m, record
	m.mu.Lock()
	m.records[record.id] = record
	m.order = append(m.order, record.id)
	m.mu.Unlock()
	t.Cleanup(func() { m.mu.Lock(); delete(m.records, record.id); m.mu.Unlock() })
	request := json.RawMessage(`{"events":["workspace.fileCreated"]}`)
	if _, err := hostEventsSubscribe(second, request); err != nil {
		t.Fatal(err)
	}
	if m.fileEvents.Load() != first {
		t.Fatal("second plugin created another watcher")
	}
	if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	if m.fileEvents.Load() != first {
		t.Fatal("first unsubscribe stopped the second consumer's source")
	}
	if _, err := hostEventsSubscribe(second, json.RawMessage(`{"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	if m.fileEvents.Load() != nil {
		t.Fatal("last of two consumers did not stop source")
	}
	select {
	case <-first.done:
	default:
		t.Fatal("shared watcher not joined")
	}
}

func TestWorkspaceFileEventsRetiredGenerationCannotEraseLoss(t *testing.T) {
	m, rt, _, output := fileEventsFixtureWithAck(t, func() bool { return false })
	s := m.fileEvents.Load()
	m.publishWorkspaceFileBatch(s, hostEventFileCreated, workspaceEventBatch{Paths: []string{"old.txt"}, ScopeRevision: s.scope.Revision, Origin: "host"})
	if events := readFileEvents(t, output, 100*time.Millisecond); len(events) != 1 {
		t.Fatalf("old RPC not started: %+v", events)
	}
	rt.mu.Lock()
	oldPending := rt.pending
	rt.generation = 2
	rt.pending = map[string]chan rpcEnvelope{}
	rt.mu.Unlock()
	// One old RPC remains in flight. Fill the other slots to force a loss for the
	// replacement generation, then retire the old RPC with a successful result.
	rt.eventsInflight.Add(7)
	m.publishWorkspaceFileBatch(s, hostEventFileCreated, workspaceEventBatch{Paths: []string{"lost.txt"}, ScopeRevision: s.scope.Revision, Origin: "host"})
	rt.eventsInflight.Add(-7)
	for _, reply := range oldPending {
		reply <- rpcEnvelope{Result: json.RawMessage(`{}`)}
	}
	deadline := time.Now().Add(time.Second)
	for rt.eventsInflight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("old RPC did not drain")
		}
		time.Sleep(time.Millisecond)
	}
	m.publishWorkspaceFileBatch(s, hostEventFileCreated, workspaceEventBatch{Paths: []string{"new.txt"}, ScopeRevision: s.scope.Revision, Origin: "host"})
	events := readFileEvents(t, output, 150*time.Millisecond)
	for _, event := range events {
		if event.Event == hostEventFileCreated && event.Data.RescanRequired {
			return
		}
	}
	t.Fatalf("retired generation erased replacement loss: %+v", events)
}

// The old request passes the real RPC entry check at generation 1, then pauses
// in the registered-method gate and waits for eventSubscribeMu while generation
// 2 installs its own state. This is not a request with an invented stale ID.
func TestWorkspaceFileEventsDelayedSubscriptionGeneration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		old     json.RawMessage
		current string
	}{
		{"subscribe", json.RawMessage(`{"events":["workspace.fileCreated"]}`), hostEventFileSaved},
		{"clear", json.RawMessage(`{"events":[]}`), hostEventFileCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rt, _, _ := fileEventsFixture(t)
			if _, err := hostEventsSubscribe(rt, json.RawMessage(`{"events":[]}`)); err != nil {
				t.Fatal(err)
			}
			admitted, release := make(chan struct{}), make(chan struct{})
			var paused atomic.Bool
			hostMethodsMu.Lock()
			original := hostMethods["host.events.subscribe"]
			registration := original
			registration.gate = func(runtimeHost *processRuntime) bool {
				if runtimeHost == rt && paused.CompareAndSwap(false, true) {
					close(admitted)
					<-release
				}
				return original.gate == nil || original.gate(runtimeHost)
			}
			hostMethods["host.events.subscribe"] = registration
			hostMethodsMu.Unlock()
			released := false
			t.Cleanup(func() {
				if !released {
					close(release)
				}
				hostMethodsMu.Lock()
				hostMethods["host.events.subscribe"] = original
				hostMethodsMu.Unlock()
			})
			type reply struct {
				served bool
				err    *rpcError
			}
			finished := make(chan reply, 1)
			go func() {
				var rpcErr *rpcError
				served := rt.serveHostRequest(1, rpcEnvelope{Method: "host.events.subscribe", Params: tc.old}, func(_ any, err *rpcError) { rpcErr = err })
				finished <- reply{served, rpcErr}
			}()
			select {
			case <-admitted:
			case <-time.After(time.Second):
				t.Fatal("old request never passed RPC generation admission")
			}
			rt.eventSubscribeMu.Lock()
			rt.mu.Lock()
			rt.generation = 2
			rt.eventSubscriptions = map[string]struct{}{tc.current: {}}
			rt.fileEventsSubscribedAt = time.Now()
			rt.fileEventLoss = workspaceEventLoss{generation: 2, revision: m.workspaceScope().Revision, epoch: 7, acknowledged: 2, reason: "deliveryLost"}
			subscribedAt, loss := rt.fileEventsSubscribedAt, rt.fileEventLoss
			rt.mu.Unlock()
			if err := m.syncWorkspaceFileEvents(); err != nil {
				rt.eventSubscribeMu.Unlock()
				t.Fatal(err)
			}
			source := m.fileEvents.Load()
			close(release)
			released = true
			rt.eventSubscribeMu.Unlock()
			select {
			case result := <-finished:
				if !result.served || result.err == nil || result.err.Code != -32001 {
					t.Errorf("delayed old request was not rejected: served=%v error=%+v", result.served, result.err)
				}
			case <-time.After(time.Second):
				t.Fatal("delayed old request did not finish")
			}
			rt.mu.Lock()
			_, kept := rt.eventSubscriptions[tc.current]
			if !kept || len(rt.eventSubscriptions) != 1 {
				t.Errorf("old request changed new generation subscriptions: %v", rt.eventSubscriptions)
			}
			if rt.fileEventsSubscribedAt != subscribedAt {
				t.Error("old request changed new generation subscription frontier")
			}
			if rt.fileEventLoss != loss {
				t.Errorf("old request changed pending rescan: %+v want %+v", rt.fileEventLoss, loss)
			}
			rt.mu.Unlock()
			if m.fileEvents.Load() != source {
				t.Error("old request started/stopped the new generation watcher")
			}
			// The current generation still subscribes and clears through the same real
			// registration/dispatch path after rejecting the delayed request.
			invokeCurrent := func(raw json.RawMessage) {
				t.Helper()
				var rpcErr *rpcError
				if !rt.serveHostRequest(2, rpcEnvelope{Method: "host.events.subscribe", Params: raw}, func(_ any, err *rpcError) { rpcErr = err }) || rpcErr != nil {
					t.Fatalf("current generation request failed: %+v", rpcErr)
				}
			}
			invokeCurrent(json.RawMessage(`{"events":["workspace.fileDeleted"]}`))
			if !rt.subscribedTo(hostEventFileDeleted) || m.fileEvents.Load() == nil {
				t.Error("current generation subscribe did not start/retain source")
			}
			invokeCurrent(json.RawMessage(`{"events":[]}`))
			if rt.subscribedTo(hostEventFileDeleted) || m.fileEvents.Load() != nil {
				t.Error("current generation clear did not stop source")
			}
		})
	}
}

func TestWorkspaceFileEventsRegistered(t *testing.T) {
	for _, event := range []string{"workspace.fileCreated", "workspace.fileDeleted", "workspace.fileRenamed", "workspace.rescanRequired"} {
		if cap, ok := hostEvents[event]; !ok || cap != api.CapabilityWorkspaceRead {
			t.Errorf("missing workspace.read event %s", event)
		}
		if frontendHostEvents[event] {
			t.Errorf("native event admitted from frontend: %s", event)
		}
	}
}
