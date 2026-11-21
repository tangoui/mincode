package tasks

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func startManager(t *testing.T, s *Store, executor Executor) *Manager {
	t.Helper()
	m, err := NewManager(context.Background(), s, map[Kind]Executor{KindBackup: executor}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return m
}
func waitState(t *testing.T, m *Manager, id string, state State) Task {
	t.Helper()
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		task, err := m.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if task.State == state {
			return task
		}
		select {
		case <-deadline:
			t.Fatalf("state = %s, want %s", task.State, state)
		case <-tick.C:
		}
	}
}
func TestManagerConcurrency(t *testing.T) {
	s := testStore(t)
	entered := make(chan string, 3)
	release := make(chan struct{})
	defer close(release)
	var live, maximum atomic.Int32
	m := startManager(t, s, func(ctx context.Context, task Task, _ *Reporter) (Result, error) {
		n := live.Add(1)
		defer live.Add(-1)
		for {
			old := maximum.Load()
			if n <= old || maximum.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- task.ID
		select {
		case <-release:
			return Result{}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	})
	var submitted []Task
	for _, key := range []string{"a", "b", "c"} {
		r := request(key)
		r.Resources = []string{"endpoint:" + key}
		task, err := m.Submit(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		submitted = append(submitted, task)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("worker not started")
		}
	}
	select {
	case <-entered:
		t.Fatal("third worker ran before a slot opened")
	case <-time.After(30 * time.Millisecond):
	}
	if maximum.Load() != 2 {
		t.Fatalf("workers = %d", maximum.Load())
	}
	for _, task := range submitted {
		if _, err := m.Cancel(context.Background(), task.ID); err != nil {
			t.Fatal(err)
		}
	}
}
func TestAliasResourceExclusion(t *testing.T) {
	s := testStore(t)
	entered := make(chan string, 2)
	m := startManager(t, s, func(ctx context.Context, task Task, _ *Reporter) (Result, error) {
		entered <- task.ID
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	a, err := m.Submit(context.Background(), request("alias-a"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first worker absent")
	}
	b, err := m.Submit(context.Background(), request("alias-b"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
		t.Fatal("same resource executed concurrently")
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := m.Cancel(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, a.ID, StateCancelled)
	select {
	case id := <-entered:
		if id != b.ID {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("lock not released")
	}
}
func TestCancelRaces(t *testing.T) {
	s := testStore(t)
	entered := make(chan struct{}, 1)
	m := startManager(t, s, func(ctx context.Context, _ Task, _ *Reporter) (Result, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	a, _ := m.Submit(context.Background(), request("running"))
	<-entered
	b, _ := m.Submit(context.Background(), request("queued"))
	got, err := m.Cancel(context.Background(), b.ID)
	if err != nil || got.State != StateCancelled {
		t.Fatalf("queued = %+v %v", got, err)
	}
	if _, err = m.Cancel(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, a.ID, StateCancelled)
	got, err = m.Cancel(context.Background(), b.ID)
	if err != nil || got.State != StateCancelled {
		t.Fatalf("repeat = %+v %v", got, err)
	}
	select {
	case <-entered:
		t.Fatal("cancelled queued work executed")
	default:
	}
}
func TestEffectCrashWindow(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncertain", true: "acknowledged"}[ack], func(t *testing.T) {
			s := testStore(t)
			m := startManager(t, s, func(ctx context.Context, _ Task, r *Reporter) (Result, error) {
				id, err := r.BeginEffect(ctx, EffectIntent{Kind: "insert-batch"})
				if err != nil {
					return Result{}, err
				}
				if ack {
					err = r.FinishEffect(ctx, id, EffectOutcome{Records: 7})
				}
				return Result{}, err
			})
			task, err := m.Submit(context.Background(), request("effect"))
			if err != nil {
				t.Fatal(err)
			}
			state := StateFailed
			if ack {
				state = StateSucceeded
			}
			got := waitState(t, m, task.ID, state)
			if ack {
				if got.Result.Certainty != EffectsKnown || got.Result.Records != 7 {
					t.Fatalf("ack = %+v", got.Result)
				}
			} else if got.Result.Certainty != EffectsUncertain {
				t.Fatalf("missing ack = %+v", got.Result)
			}
		})
	}
}
func TestExecutorPanic(t *testing.T) {
	s := testStore(t)
	m := startManager(t, s, func(context.Context, Task, *Reporter) (Result, error) { panic("secret payload") })
	task, err := m.Submit(context.Background(), request("panic"))
	if err != nil {
		t.Fatal(err)
	}
	got := waitState(t, m, task.ID, StateFailed)
	if got.ErrorCode != "task_executor_panic" {
		t.Fatalf("panic diagnostic = %q", got.ErrorCode)
	}
}
func TestRestartInterrupted(t *testing.T) {
	s := testStore(t)
	task, err := s.Admit(context.Background(), request("before-crash"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	m := startManager(t, s, func(context.Context, Task, *Reporter) (Result, error) { calls.Add(1); return Result{}, nil })
	got, err := m.Get(context.Background(), task.ID)
	if err != nil || got.State != StateInterrupted || calls.Load() != 0 {
		t.Fatalf("restart = %+v %v calls=%d", got, err, calls.Load())
	}
}
func TestShutdownDeadline(t *testing.T) {
	s := testStore(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	m := startManager(t, s, func(context.Context, Task, *Reporter) (Result, error) {
		close(entered)
		<-release
		return Result{}, nil
	})
	task, err := m.Submit(context.Background(), request("hung"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown = %v", err)
	}
	got, err := m.Get(context.Background(), task.ID)
	if err != nil || got.State == StateCancelled {
		t.Fatalf("reported stopped while executor live = %+v %v", got, err)
	}
	if _, err := m.Submit(context.Background(), request("late")); !errors.Is(err, ErrStopped) {
		t.Fatalf("late admission = %v", err)
	}
}

func TestEffectAcknowledgementFailureStopsFurtherEffects(t *testing.T) {
	s := testStore(t)
	var writes atomic.Int32
	m := startManager(t, s, func(ctx context.Context, _ Task, r *Reporter) (Result, error) {
		id, err := r.BeginEffect(ctx, EffectIntent{Kind: "batch"})
		if err != nil {
			return Result{}, err
		}
		writes.Add(1)
		if _, err := s.db.Exec("CREATE TRIGGER reject_ack BEFORE UPDATE OF outcome ON transfer_effects BEGIN SELECT RAISE(FAIL,'unavailable'); END"); err != nil {
			return Result{}, err
		}
		if err := r.FinishEffect(ctx, id, EffectOutcome{Records: 1}); err == nil {
			t.Error("acknowledgement unexpectedly succeeded")
		}
		_, err = r.BeginEffect(ctx, EffectIntent{Kind: "must-not-start"})
		if err == nil {
			writes.Add(1)
		}
		return Result{}, err
	})
	task, err := m.Submit(context.Background(), request("ack-failure"))
	if err != nil {
		t.Fatal(err)
	}
	got := waitState(t, m, task.ID, StateFailed)
	if writes.Load() != 1 || got.Result.Certainty != EffectsUncertain {
		t.Fatalf("continued after journal failure: writes=%d result=%+v", writes.Load(), got.Result)
	}
}

func TestPendingEffectCannotReportSuccess(t *testing.T) {
	s := testStore(t)
	m := startManager(t, s, func(ctx context.Context, _ Task, r *Reporter) (Result, error) {
		_, err := r.BeginEffect(ctx, EffectIntent{Kind: "unacknowledged"})
		return Result{}, err
	})
	task, err := m.Submit(context.Background(), request("unacknowledged"))
	if err != nil {
		t.Fatal(err)
	}
	got := waitState(t, m, task.ID, StateFailed)
	if got.Result.Certainty != EffectsUncertain {
		t.Fatal(got.Result)
	}
}

func TestQueuedCancellationRunsTerminalCleanup(t *testing.T) {
	s := testStore(t)
	entered := make(chan struct{}, 1)
	var cleaned atomic.Int32
	m := startManager(t, s, func(ctx context.Context, _ Task, _ *Reporter) (Result, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return Result{}, ctx.Err()
	})
	m.SetCleanup(func(task Task) {
		if task.Request.IdempotencyKey == "queued-cleanup" {
			cleaned.Add(1)
		}
	})
	_, err := m.Submit(context.Background(), request("blocking"))
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	task, err := m.Submit(context.Background(), request("queued-cleanup"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Cancel(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if cleaned.Load() != 1 {
		t.Fatal("queued input ownership not released")
	}
}

func TestSafeDiagnosticSurvivesTaskRecovery(t *testing.T) {
	s := testStore(t)
	m := startManager(t, s, func(context.Context, Task, *Reporter) (Result, error) {
		return Result{}, Diagnostic("restore_target_changed")
	})
	task, err := m.Submit(context.Background(), request("safe-diagnostic"))
	if err != nil {
		t.Fatal(err)
	}
	got := waitState(t, m, task.ID, StateFailed)
	if got.ErrorCode != "restore_target_changed" {
		t.Fatalf("diagnostic lost: %q", got.ErrorCode)
	}
}
