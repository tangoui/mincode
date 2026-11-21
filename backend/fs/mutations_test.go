package fs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mutationArgs(v any) string { b, _ := json.Marshal(v); return string(b) }

// Missing prepare/success hooks or capturing old canonical after IO breaks this test.
func TestMutationSuccessOnly(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "old.txt")
	if err := os.WriteFile(old, []byte("private content"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var prepared, completed []Mutation
	off := OnMutation(func(m Mutation) func(Mutation) {
		if m.OldPath != "" {
			if _, err := os.Lstat(old); err != nil {
				t.Errorf("prepare must precede IO: %v", err)
			}
		}
		mu.Lock()
		prepared = append(prepared, m)
		mu.Unlock()
		return func(m Mutation) { mu.Lock(); completed = append(completed, m); mu.Unlock() }
	})
	defer off()
	if _, err := handleRename("", mutationArgs(map[string]any{"projectRoot": root, "oldPath": "old.txt", "newName": "new.txt"})); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prepared) != 1 || len(completed) != 1 {
		t.Fatalf("prepare/success = %d/%d, want 1/1", len(prepared), len(completed))
	}
	if prepared[0].OldCanonical != old || completed[0].OldCanonical != old || completed[0].NewCanonical != filepath.Join(root, "new.txt") {
		t.Fatalf("old canonical not frozen: before=%+v after=%+v", prepared, completed)
	}
}

func TestMutationEveryListedOperationAndUnregister(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []Mutation
	off := OnMutation(func(m Mutation) func(Mutation) {
		// Re-enter the registry from the observer to detect callbacks under locks.
		remove := OnMutation(func(Mutation) func(Mutation) { return nil })
		remove()
		return func(m Mutation) { mu.Lock(); got = append(got, m); mu.Unlock() }
	})
	invoke := func(handler func(string, string) (string, error), args map[string]any) {
		t.Helper()
		args["projectRoot"] = root
		if _, err := handler("", mutationArgs(args)); err != nil {
			t.Fatal(err)
		}
	}
	invoke(handleWriteTextFile, map[string]any{"filePath": "new.txt", "content": "not in Mutation", "createOnly": true})
	invoke(handleWriteTextFile, map[string]any{"filePath": "new.txt", "content": "edit"})
	invoke(handleMkdir, map[string]any{"dirPath": "dir"})
	invoke(handleRename, map[string]any{"oldPath": "dir", "newName": "renamed"})
	invoke(handleDelete, map[string]any{"path": "renamed"})
	invoke(handlePasteFiles, map[string]any{"destDir": "", "srcPaths": []string{filepath.Join(root, "new.txt")}})
	off()
	off()
	invoke(handleWriteTextFile, map[string]any{"filePath": "after.txt", "content": "x"})
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 6 {
		t.Fatalf("operations completed: %+v", got)
	}
	for i, kind := range []string{"write", "write", "mkdir", "rename", "delete", "paste"} {
		if got[i].Kind != kind || got[i].Root != root {
			t.Fatalf("operation %d: %+v", i, got[i])
		}
	}
	if got[0].OldPath != "" || got[1].OldPath != "new.txt" || got[5].NewPath != "new copy.txt" {
		t.Fatalf("incorrect paths: %+v", got)
	}
	for _, i := range []int{2, 3, 4} {
		if !got[i].IsDir {
			t.Fatalf("directory operation lacks rescan hint: %+v", got[i])
		}
	}
}

func TestMutationFailuresHaveNoCompletion(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "locked")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old")
	if err := os.WriteFile(old, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	var prepared, completed atomic.Int32
	off := OnMutation(func(Mutation) func(Mutation) { prepared.Add(1); return func(Mutation) { completed.Add(1) } })
	defer off()
	for _, op := range []struct {
		handler func(string, string) (string, error)
		args    map[string]any
	}{
		{handleRename, map[string]any{"projectRoot": root, "oldPath": "locked/old", "newName": "new"}},
		{handleDelete, map[string]any{"projectRoot": root, "path": "locked/old"}},
	} {
		if _, err := op.handler("", mutationArgs(op.args)); err == nil {
			t.Fatal("expected OS permission failure (run as non-root)")
		}
	}
	if prepared.Load() != 2 || completed.Load() != 0 {
		t.Fatalf("failed IO prepare/complete = %d/%d", prepared.Load(), completed.Load())
	}
	for _, op := range []struct {
		handler func(string, string) (string, error)
		args    map[string]any
	}{
		{handleRename, map[string]any{"projectRoot": root, "oldPath": "missing", "newName": "new"}},
		{handleDelete, map[string]any{"projectRoot": root, "path": "missing"}},
	} {
		if _, err := op.handler("", mutationArgs(op.args)); err == nil {
			t.Fatal("expected resolution failure")
		}
	}
	if prepared.Load() != 2 || completed.Load() != 0 {
		t.Fatal("failed resolution reached observer")
	}
}

func TestMutationObserversPanicAndTimeoutDoNotBreakIO(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	panicPrepare := OnMutation(func(Mutation) func(Mutation) { panic("prepare") })
	defer panicPrepare()
	panicComplete := OnMutation(func(Mutation) func(Mutation) { return func(Mutation) { panic("complete") } })
	defer panicComplete()
	block := make(chan struct{})
	exited := make(chan struct{})
	var calls atomic.Int32
	off := OnMutation(func(Mutation) func(Mutation) { calls.Add(1); defer close(exited); <-block; return nil })
	defer off()
	start := time.Now()
	for _, name := range []string{"first", "second"} {
		if _, err := handleWriteTextFile("", mutationArgs(map[string]any{"projectRoot": root, "filePath": name, "content": "x"})); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("slow observer blocked IO")
	}
	if calls.Load() != 1 {
		t.Fatal("busy observer spawned unbounded callbacks")
	}
	close(block)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("observer did not exit")
	}
	off()
	blockDone := make(chan struct{})
	finished := make(chan struct{})
	off2 := OnMutation(func(Mutation) func(Mutation) { return func(Mutation) { defer close(finished); <-blockDone } })
	defer off2()
	if _, err := handleMkdir("", mutationArgs(map[string]any{"projectRoot": root, "dirPath": "done"})); err != nil {
		t.Fatal(err)
	}
	close(blockDone)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("completion did not exit")
	}
}
