package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var backupTestLoc = time.FixedZone("UTC+8", 8*3600)

func TestNextBackupRunSchedules(t *testing.T) {
	anchor := time.Date(2026, 1, 5, 10, 30, 0, 0, backupTestLoc) // Monday
	cases := []struct {
		name     string
		schedule BackupSchedule
		want     time.Time
	}{
		{"interval", BackupSchedule{Kind: backupScheduleInterval, IntervalHours: 6}, anchor.Add(6 * time.Hour)},
		{"daily later today", BackupSchedule{Kind: backupScheduleDaily, Time: "23:15"}, time.Date(2026, 1, 5, 23, 15, 0, 0, backupTestLoc)},
		{"daily tomorrow", BackupSchedule{Kind: backupScheduleDaily, Time: "10:30"}, time.Date(2026, 1, 6, 10, 30, 0, 0, backupTestLoc)},
		{"weekly next listed day", BackupSchedule{Kind: backupScheduleWeekly, Time: "02:00", Weekdays: []int{3, 0}}, time.Date(2026, 1, 7, 2, 0, 0, 0, backupTestLoc)},
		{"weekly same day later", BackupSchedule{Kind: backupScheduleWeekly, Time: "11:00", Weekdays: []int{1}}, time.Date(2026, 1, 5, 11, 0, 0, 0, backupTestLoc)},
		{"weekly wraps a week", BackupSchedule{Kind: backupScheduleWeekly, Time: "09:00", Weekdays: []int{1}}, time.Date(2026, 1, 12, 9, 0, 0, 0, backupTestLoc)},
	}
	for _, tc := range cases {
		got, ok := nextBackupRun(tc.schedule, anchor, backupTestLoc)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("%s: got %v (ok=%v), want %v", tc.name, got, ok, tc.want)
		}
	}
	if _, ok := nextBackupRun(BackupSchedule{Kind: backupScheduleWeekly, Time: "09:00"}, anchor, backupTestLoc); ok {
		t.Error("weekly schedule without days must not produce a run")
	}
}

func TestBackupDueDecisionCatchUp(t *testing.T) {
	updated := time.Date(2026, 1, 1, 0, 0, 0, 0, backupTestLoc)
	job := BackupJob{Enabled: true, Schedule: BackupSchedule{Kind: backupScheduleDaily, Time: "09:00"}, UpdatedAt: updated.UnixMilli()}

	onTime := time.Date(2026, 1, 1, 9, 1, 0, 0, backupTestLoc)
	due, trigger, consumed := backupDueDecision(job, onTime, backupTestLoc)
	if !due || trigger != backupTriggerSchedule || !consumed.Equal(time.Date(2026, 1, 1, 9, 0, 0, 0, backupTestLoc)) {
		t.Fatalf("on-time decision = %v %s %v", due, trigger, consumed)
	}
	late := time.Date(2026, 1, 4, 12, 0, 0, 0, backupTestLoc)
	due, trigger, consumed = backupDueDecision(job, late, backupTestLoc)
	if !due || trigger != backupTriggerCatchup || !consumed.Equal(late) {
		t.Fatalf("late decision = %v %s %v", due, trigger, consumed)
	}
	// 补跑只发生一次：以补跑时刻为锚点后，下一次是次日 09:00。
	job.State.LastScheduledAt = consumed.UnixMilli()
	if due, _, _ := backupDueDecision(job, late.Add(time.Minute), backupTestLoc); due {
		t.Fatal("catch-up must run only once")
	}
	job.Enabled = false
	if due, _, _ := backupDueDecision(job, late.Add(48*time.Hour), backupTestLoc); due {
		t.Fatal("disabled jobs are never due")
	}
}

type backupCall struct {
	job     BackupJob
	started time.Time
}

func newTestBackupManager(t *testing.T, now time.Time, jobs []BackupJob, exec backupExecutor) (*backupManager, *fakeBackupClock, *memoryBackupStore) {
	t.Helper()
	clock := newFakeBackupClock(now)
	store := &memoryBackupStore{jobs: jobs, history: map[string][]BackupRun{}}
	m := newBackupManager(store, clock, exec, nil)
	m.loc = backupTestLoc
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.stop(ctx)
	})
	return m, clock, store
}

func waitBackupCall(t *testing.T, calls <-chan backupCall) backupCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("backup executor was not called")
		return backupCall{}
	}
}

func expectNoBackupCall(t *testing.T, calls <-chan backupCall) {
	t.Helper()
	select {
	case call := <-calls:
		t.Fatalf("unexpected backup run for %s at %v", call.job.ID, call.started)
	case <-time.After(50 * time.Millisecond):
	}
}

func waitBackupRunStatus(t *testing.T, m *backupManager, jobID string, index int, status string) BackupRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := m.runs(jobID)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) > index && runs[index].Status == status {
			return runs[index]
		}
		time.Sleep(2 * time.Millisecond)
	}
	runs, _ := m.runs(jobID)
	t.Fatalf("run %d of %s never reached %s: %+v", index, jobID, status, runs)
	return BackupRun{}
}

func TestBackupSchedulerRunsDailyJobWithFakeClock(t *testing.T) {
	start := time.Date(2026, 1, 5, 8, 0, 0, 0, backupTestLoc)
	job := BackupJob{
		ID: "daily", Name: "daily", Enabled: true,
		Schedule:  BackupSchedule{Kind: backupScheduleDaily, Time: "09:00"},
		CreatedAt: start.Add(-time.Hour).UnixMilli(), UpdatedAt: start.Add(-time.Hour).UnixMilli(),
	}
	calls := make(chan backupCall, 8)
	m, clock, store := newTestBackupManager(t, start, []BackupJob{job}, func(_ context.Context, job BackupJob, at time.Time) (backupResult, error) {
		calls <- backupCall{job: job, started: at}
		return backupResult{File: "/tmp/x.sql", SizeBytes: 42, Tables: 2, Rows: 10}, nil
	})
	m.start(context.Background(), 0)
	if wait := clock.waitPending(t); wait != backupMaxWait {
		t.Fatalf("first wait = %v, want capped %v", wait, backupMaxWait)
	}
	expectNoBackupCall(t, calls)

	clock.Advance(59 * time.Minute)
	clock.waitPending(t)
	expectNoBackupCall(t, calls)

	clock.Advance(time.Minute)
	call := waitBackupCall(t, calls)
	if !call.started.Equal(time.Date(2026, 1, 5, 9, 0, 0, 0, backupTestLoc)) {
		t.Fatalf("run started at %v", call.started)
	}
	run := waitBackupRunStatus(t, m, "daily", 0, backupStatusSuccess)
	if run.Trigger != backupTriggerSchedule || run.SizeBytes != 42 || run.Rows != 10 || run.File != "/tmp/x.sql" {
		t.Fatalf("run = %+v", run)
	}
	clock.waitPending(t)
	jobs, _ := store.loadJobs()
	if jobs[0].State.LastStatus != backupStatusSuccess || jobs[0].State.LastScheduledAt != call.started.UnixMilli() {
		t.Fatalf("persisted state = %+v", jobs[0].State)
	}
	views, err := m.list()
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 6, 9, 0, 0, 0, backupTestLoc).UnixMilli(); views[0].NextRunAt != want {
		t.Fatalf("next run = %v, want %v", time.UnixMilli(views[0].NextRunAt), time.UnixMilli(want))
	}
}

func TestBackupSchedulerCatchesUpMissedRunOnce(t *testing.T) {
	now := time.Date(2026, 1, 8, 12, 0, 0, 0, backupTestLoc)
	job := BackupJob{
		ID: "missed", Name: "missed", Enabled: true,
		Schedule:  BackupSchedule{Kind: backupScheduleDaily, Time: "09:00"},
		CreatedAt: now.Add(-10 * 24 * time.Hour).UnixMilli(), UpdatedAt: now.Add(-10 * 24 * time.Hour).UnixMilli(),
		State: BackupJobState{LastScheduledAt: now.Add(-4 * 24 * time.Hour).UnixMilli()},
	}
	disabled := job
	disabled.ID, disabled.Enabled = "off", false
	calls := make(chan backupCall, 8)
	m, clock, _ := newTestBackupManager(t, now, []BackupJob{job, disabled}, func(_ context.Context, job BackupJob, at time.Time) (backupResult, error) {
		calls <- backupCall{job: job, started: at}
		return backupResult{}, nil
	})
	m.start(context.Background(), 0)
	call := waitBackupCall(t, calls)
	if call.job.ID != "missed" || !call.started.Equal(now) {
		t.Fatalf("catch-up call = %s at %v", call.job.ID, call.started)
	}
	run := waitBackupRunStatus(t, m, "missed", 0, backupStatusSuccess)
	if run.Trigger != backupTriggerCatchup {
		t.Fatalf("trigger = %s", run.Trigger)
	}
	for i := 0; i < 4; i++ {
		clock.waitPending(t)
		clock.Advance(backupMaxWait)
	}
	clock.waitPending(t)
	expectNoBackupCall(t, calls)
	if runs, _ := m.runs("off"); len(runs) != 0 {
		t.Fatalf("disabled job ran: %+v", runs)
	}
}

func TestBackupSchedulerSkipsOverlappingRuns(t *testing.T) {
	start := time.Date(2026, 1, 5, 8, 0, 0, 0, backupTestLoc)
	job := BackupJob{
		ID: "hourly", Name: "hourly", Enabled: true,
		Schedule:  BackupSchedule{Kind: backupScheduleInterval, IntervalHours: 1},
		CreatedAt: start.UnixMilli(), UpdatedAt: start.UnixMilli(),
	}
	release := make(chan struct{})
	calls := make(chan backupCall, 8)
	m, clock, _ := newTestBackupManager(t, start, []BackupJob{job}, func(ctx context.Context, job BackupJob, at time.Time) (backupResult, error) {
		calls <- backupCall{job: job, started: at}
		select {
		case <-release:
			return backupResult{}, nil
		case <-ctx.Done():
			return backupResult{}, ctx.Err()
		}
	})
	m.start(context.Background(), 0)
	clock.waitPending(t)
	clock.Advance(time.Hour)
	waitBackupCall(t, calls)
	if _, err := m.runNow("hourly"); err == nil {
		t.Fatal("run now must be rejected while the job is running")
	}
	// 第二个计划时刻到达时上一轮仍在运行：跳过，不并发执行。
	clock.waitPending(t)
	clock.Advance(time.Hour)
	clock.waitPending(t)
	expectNoBackupCall(t, calls)
	close(release)
	waitBackupRunStatus(t, m, "hourly", 0, backupStatusSuccess)
	if runs, _ := m.runs("hourly"); len(runs) != 1 {
		t.Fatalf("history = %+v", runs)
	}
}

func TestBackupSchedulerCancelsRunsOnStop(t *testing.T) {
	now := time.Date(2026, 1, 5, 8, 0, 0, 0, backupTestLoc)
	job := BackupJob{ID: "manual", Name: "manual", Schedule: BackupSchedule{Kind: backupScheduleDaily, Time: "01:00"}, UpdatedAt: now.UnixMilli()}
	calls := make(chan backupCall, 1)
	m, _, store := newTestBackupManager(t, now, []BackupJob{job}, func(ctx context.Context, job BackupJob, at time.Time) (backupResult, error) {
		calls <- backupCall{job: job, started: at}
		<-ctx.Done()
		return backupResult{}, fmt.Errorf("dump: %w", ctx.Err())
	})
	m.start(context.Background(), time.Hour)
	run, err := m.runNow("manual")
	if err != nil || run.Trigger != backupTriggerManual || run.Status != backupStatusRunning {
		t.Fatalf("run now = %+v, %v", run, err)
	}
	waitBackupCall(t, calls)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.stop(ctx)
	history, _ := store.loadHistory()
	if got := history["manual"]; len(got) != 1 || got[0].Status != backupStatusCancelled {
		t.Fatalf("history after stop = %+v", got)
	}
	if _, err := m.runNow("manual"); err == nil {
		t.Fatal("run now must fail after shutdown")
	}
}

func TestBackupManagerRepairsInterruptedRunsAndCapsHistory(t *testing.T) {
	now := time.Date(2026, 1, 5, 8, 0, 0, 0, backupTestLoc)
	store := &memoryBackupStore{
		jobs:    []BackupJob{{ID: "j", Name: "j", State: BackupJobState{LastStatus: backupStatusRunning}}},
		history: map[string][]BackupRun{"j": {{ID: "r", JobID: "j", Status: backupStatusRunning}}},
	}
	m := newBackupManager(store, newFakeBackupClock(now), nil, nil)
	runs, err := m.runs("j")
	if err != nil || runs[0].Status != backupStatusFailed || runs[0].Error == "" {
		t.Fatalf("interrupted run not repaired: %+v %v", runs, err)
	}
	m.mu.Lock()
	for i := 0; i < backupHistoryLimit+5; i++ {
		m.appendHistoryLocked(BackupRun{ID: fmt.Sprint(i), JobID: "j", Status: backupStatusSuccess})
	}
	m.mu.Unlock()
	runs, _ = m.runs("j")
	if len(runs) != backupHistoryLimit || runs[0].ID != fmt.Sprint(backupHistoryLimit+4) {
		t.Fatalf("history cap failed: len=%d first=%s", len(runs), runs[0].ID)
	}
}

func TestBackupManagerSaveValidatesAndPreservesState(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 5, 8, 0, 0, 0, backupTestLoc)
	m, clock, _ := newTestBackupManager(t, now, nil, nil)
	input := BackupJob{
		Name: "  Nightly / prod  ", ConnectionID: "conn-1",
		Targets:     []BackupTarget{{Database: "app"}, {Database: "app", Tables: []string{"users"}}, {Database: "logs", Tables: []string{"a", "a"}}},
		Schedule:    BackupSchedule{Kind: backupScheduleWeekly, Time: "02:30", Weekdays: []int{5, 1, 5}},
		Destination: dir, Retention: BackupRetention{KeepLast: 7}, Enabled: true,
	}
	view, err := m.save(input)
	if err != nil {
		t.Fatal(err)
	}
	if view.ID == "" || view.Name != "Nightly / prod" || view.FileNamePrefix != "Nightly_prod" {
		t.Fatalf("saved = %+v", view.BackupJob)
	}
	if len(view.Targets) != 2 || view.Targets[0].Tables != nil || len(view.Targets[1].Tables) != 1 {
		t.Fatalf("targets = %+v", view.Targets)
	}
	if fmt.Sprint(view.Schedule.Weekdays) != "[1 5]" || view.NextRunAt == 0 {
		t.Fatalf("schedule = %+v next=%d", view.Schedule, view.NextRunAt)
	}
	m.mu.Lock()
	m.jobs[0].State.LastStatus = backupStatusSuccess
	m.mu.Unlock()
	clock.Advance(time.Minute)
	input.ID = view.ID
	input.Name = "renamed"
	updated, err := m.save(input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CreatedAt != view.CreatedAt || updated.State.LastStatus != backupStatusSuccess || updated.UpdatedAt <= view.UpdatedAt {
		t.Fatalf("update lost state: %+v", updated.BackupJob)
	}

	bad := []BackupJob{
		{Name: "", ConnectionID: "c", Targets: []BackupTarget{{Database: "d"}}, Schedule: BackupSchedule{Kind: backupScheduleDaily, Time: "01:00"}, Destination: dir},
		{Name: "x", ConnectionID: "c", Schedule: BackupSchedule{Kind: backupScheduleDaily, Time: "01:00"}, Destination: dir},
		{Name: "x", ConnectionID: "c", Targets: []BackupTarget{{Database: "d"}}, Schedule: BackupSchedule{Kind: backupScheduleDaily, Time: "24:00"}, Destination: dir},
		{Name: "x", ConnectionID: "c", Targets: []BackupTarget{{Database: "d"}}, Schedule: BackupSchedule{Kind: backupScheduleInterval, IntervalHours: 0}, Destination: dir},
		{Name: "x", ConnectionID: "c", Targets: []BackupTarget{{Database: "d"}}, Schedule: BackupSchedule{Kind: backupScheduleDaily, Time: "01:00"}, Destination: "relative/dir"},
		{Name: "x", ConnectionID: "c", Targets: []BackupTarget{{Database: "d"}}, Schedule: BackupSchedule{Kind: backupScheduleDaily, Time: "01:00"}, Destination: filepath.Join(dir, "missing")},
		{Name: "x", ConnectionID: "c", Targets: []BackupTarget{{Database: "d"}}, Schedule: BackupSchedule{Kind: backupScheduleDaily, Time: "01:00"}, Destination: dir, Retention: BackupRetention{KeepLast: -1}},
	}
	for i, job := range bad {
		if _, err := m.save(job); err == nil {
			t.Errorf("invalid job %d accepted", i)
		}
	}
	if err := m.remove(view.ID); err != nil {
		t.Fatal(err)
	}
	if views, _ := m.list(); len(views) != 0 {
		t.Fatalf("job not removed: %+v", views)
	}
}

func TestSanitizeBackupPrefix(t *testing.T) {
	cases := map[[2]string]string{
		{"../../etc/passwd", ""}: "etc_passwd",
		{"", "每日 备份"}:            "每日_备份",
		{"  ", "***"}:            "backup",
		{"a.b-c", ""}:            "a_b-c",
	}
	for input, want := range cases {
		if got := sanitizeBackupPrefix(input[0], input[1]); got != want {
			t.Errorf("sanitize(%q, %q) = %q, want %q", input[0], input[1], got, want)
		}
	}
}

func TestApplyBackupRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.Local)
	names := []string{
		"nightly_20260110-120000.sql.gz", // current
		"nightly_20260109-120000.sql.gz",
		"nightly_20260108-120000-2.sql",
		"nightly_20260108-120000.sql",
		"nightly_20251201-120000.sql.gz", // older than keepDays
		"nightly_notes.sql",              // not a backup name
		"other_20250101-000000.sql",      // other prefix
		"nightly_20250101-000000.sql.bak",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, warnings := applyBackupRetention(dir, "nightly", filepath.Join(dir, names[0]), BackupRetention{KeepLast: 3, KeepDays: 30}, now)
	if removed != 2 || len(warnings) != 0 {
		t.Fatalf("removed=%d warnings=%v", removed, warnings)
	}
	for _, name := range []string{names[3], names[4]} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should be removed", name)
		}
	}
	for _, name := range []string{names[0], names[1], names[2], names[5], names[6], names[7]} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s should be kept: %v", name, err)
		}
	}
	// keepDays 很短时仍保留最新一份。
	removed, _ = applyBackupRetention(dir, "nightly", filepath.Join(dir, names[0]), BackupRetention{KeepDays: 1}, now.Add(72*time.Hour))
	if removed != 2 {
		t.Fatalf("second pass removed %d", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, names[0])); err != nil {
		t.Fatal("newest backup must always be kept")
	}
	if n, _ := applyBackupRetention(dir, "nightly", "", BackupRetention{}, now); n != 0 {
		t.Fatal("no retention policy must not delete files")
	}
}

func TestBackupFileNameCollision(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.Local)
	first := nextBackupFilePath(dir, "p", at, true)
	if filepath.Base(first) != "p_20260304-050607.sql.gz" {
		t.Fatalf("first = %s", first)
	}
	if err := os.WriteFile(first, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if second := nextBackupFilePath(dir, "p", at, true); filepath.Base(second) != "p_20260304-050607-2.sql.gz" {
		t.Fatalf("second = %s", second)
	}
	if !strings.HasSuffix(backupFileName("p", at, 1, false), ".sql") {
		t.Fatal("plain backups use .sql")
	}
}
