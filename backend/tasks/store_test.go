package tasks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/storage"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	db, err := storage.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	return NewStore(db)
}
func request(key string) Request {
	return Request{Kind: KindBackup, IdempotencyKey: key, Fingerprint: "frozen-job-v1", Resources: []string{"job:one"}}
}

func TestAdmissionRequiresPersistence(t *testing.T) {
	s := testStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(context.Background(), request("closed")); err == nil {
		t.Fatal("admitted without a persisted record")
	}
}
func TestIdempotentSubmit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := s.Admit(ctx, request("same"))
			if err != nil {
				failures <- err
				return
			}
			ids <- task.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("duplicate execution records")
		}
		id = got
	}
	r := request("same")
	r.Fingerprint = "different"
	if _, err := s.Admit(ctx, r); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict = %v", err)
	}
	page, err := s.List(ctx, Filter{Kind: KindBackup, Limit: 10})
	if err != nil || len(page.Tasks) != 1 {
		t.Fatalf("list = %+v, %v", page, err)
	}
}
func TestPreparationConsumedOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	prep := Preparation{ID: "reviewed", Kind: KindRestore, Fingerprint: "prepared-hash", ExpiresAt: time.Now().Add(time.Minute).UnixMilli(), Payload: []byte(`{"spool":"opaque"}`)}
	if err := s.Prepare(ctx, prep); err != nil {
		t.Fatal(err)
	}
	r := Request{Kind: KindRestore, IdempotencyKey: "click", Fingerprint: "prepared-hash", PreparationID: "reviewed"}
	first, err := s.Admit(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Admit(ctx, r)
	if err != nil || again.ID != first.ID {
		t.Fatalf("retry = %+v %v", again, err)
	}
	r.IdempotencyKey = "second-click"
	if _, err := s.Admit(ctx, r); !errors.Is(err, ErrPreparationUnavailable) {
		t.Fatalf("reuse = %v", err)
	}
	prep.ID = "expired"
	prep.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
	if err := s.Prepare(ctx, prep); err != nil {
		t.Fatal(err)
	}
	r.PreparationID = "expired"
	if _, err := s.Admit(ctx, r); !errors.Is(err, ErrPreparationUnavailable) {
		t.Fatalf("expired = %v", err)
	}
	prep.ID = "changed"
	prep.ExpiresAt = time.Now().Add(time.Minute).UnixMilli()
	if err := s.Prepare(ctx, prep); err != nil {
		t.Fatal(err)
	}
	r.PreparationID = "changed"
	r.Fingerprint = "unreviewed"
	if _, err := s.Admit(ctx, r); !errors.Is(err, ErrPreparationUnavailable) {
		t.Fatalf("changed = %v", err)
	}
}
func TestTaskStoreReopen(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	task, err := s.Admit(ctx, request("reopen"))
	if err != nil {
		t.Fatal(err)
	}
	next := task
	next.State = StateRunning
	next.Result.Records = 7
	updated, err := s.Update(ctx, next, task.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, next, task.Revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale write = %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := storage.DB()
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewStore(db).Get(ctx, task.ID)
	if err != nil || got.Revision != updated.Revision || got.State != StateRunning || got.Result.Records != 7 {
		t.Fatalf("reopen = %+v %v", got, err)
	}
}

func TestPreparationLimitAndRetryLookup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < 16; i++ {
		if err := s.Prepare(ctx, Preparation{ID: fmt.Sprint(i), Kind: KindRestore, Fingerprint: "hash", ExpiresAt: time.Now().Add(time.Minute).UnixMilli(), Payload: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Prepare(ctx, Preparation{ID: "overflow", Kind: KindRestore, Fingerprint: "hash", ExpiresAt: time.Now().Add(time.Minute).UnixMilli(), Payload: []byte(`{}`)}); err == nil {
		t.Fatal("unbounded preparations")
	}
	task, err := s.Admit(ctx, Request{Kind: KindRestore, IdempotencyKey: "key", Fingerprint: "hash", PreparationID: "0"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Submission(ctx, KindRestore, "key", "0")
	if err != nil || got.ID != task.ID {
		t.Fatalf("lost response retry: %+v %v", got, err)
	}
	if _, err := s.Submission(ctx, KindRestore, "key", "1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
}

func TestPreparationReleaseCannotDeleteAdmittedInput(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := Preparation{ID: "release", Kind: KindRestore, Fingerprint: "hash", ExpiresAt: time.Now().Add(time.Minute).UnixMilli(), Payload: []byte(`{}`)}
	if err := s.Prepare(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(ctx, Request{Kind: KindRestore, PreparationID: p.ID, IdempotencyKey: "consume", Fingerprint: "hash"}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.ReleasePreparation(ctx, p.ID)
	if err != nil || removed {
		t.Fatalf("removed admitted spool: %v %v", removed, err)
	}
	p.ID = "unused"
	if err := s.Prepare(ctx, p); err != nil {
		t.Fatal(err)
	}
	removed, err = s.ReleasePreparation(ctx, p.ID)
	if err != nil || !removed {
		t.Fatalf("unused preparation retained: %v %v", removed, err)
	}
}

func TestLegacyHistoryImportIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	old := Task{ID: "old-run", Kind: KindBackup, JobID: "old-job", State: StateSucceeded, Revision: 1, CreatedAt: 123, UpdatedAt: 124, FinishedAt: 124, Result: Result{Records: 9, Certainty: EffectsKnown}, Request: Request{Kind: KindBackup, IdempotencyKey: "legacy:old-run", Fingerprint: "legacy", JobID: "old-job"}}
	if err := s.ImportLegacy(ctx, old); err != nil {
		t.Fatal(err)
	}
	old.Result.Records = 99
	if err := s.ImportLegacy(ctx, old); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "old-run")
	if err != nil || got.Result.Records != 9 {
		t.Fatalf("legacy overwritten %+v %v", got, err)
	}
}
