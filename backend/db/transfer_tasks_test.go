package db

import (
	"context"
	"mincode/app/backend/storage"
	"mincode/app/backend/tasks"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTransferResourceAliases(t *testing.T) {
	a := &backupSavedConnection{ID: "a", DbType: "mysql", Host: "LOCALHOST.", Port: 0}
	b := &backupSavedConnection{ID: "b", DbType: "mysql", Host: "localhost", Port: 3306}
	ka, err := transferResourceKeys(a)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := transferResourceKeys(b)
	if err != nil {
		t.Fatal(err)
	}
	common := false
	for _, x := range ka {
		for _, y := range kb {
			if x == y {
				common = true
			}
		}
	}
	if !common {
		t.Fatal("aliases can execute concurrently")
	}
	path := filepath.Join(t.TempDir(), "db.sqlite")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "alias.sqlite")
	if err := os.Link(path, link); err != nil {
		t.Fatal(err)
	}
	a = &backupSavedConnection{DbType: "sqlite", FilePath: &path}
	b = &backupSavedConnection{DbType: "sqlite", FilePath: &link}
	ka, err = transferResourceKeys(a)
	if err != nil {
		t.Fatal(err)
	}
	kb, err = transferResourceKeys(b)
	if err != nil {
		t.Fatal(err)
	}
	common = false
	for _, x := range ka {
		for _, y := range kb {
			if x == y {
				common = true
			}
		}
	}
	if !common {
		t.Fatal("hardlink aliases do not conflict")
	}
}

func TestBackupScheduleSlotIdempotency(t *testing.T) {
	job := BackupJob{ID: "job", UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), Enabled: true, Schedule: BackupSchedule{Kind: backupScheduleDaily, Time: "09:00"}}
	a := backupOccurrenceKey(job, time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC), time.UTC)
	b := backupOccurrenceKey(job, time.Date(2026, 1, 4, 12, 1, 0, 0, time.UTC), time.UTC)
	if a == "" || a != b {
		t.Fatalf("same overdue occurrence duplicated: %q %q", a, b)
	}
}

func TestManualBackupRetryReturnsOriginalTask(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	transferMu.Lock()
	oldM, oldS, oldStop := transferManager, transferStore, transferStopping
	transferManager, transferStore, transferStopping = nil, nil, false
	transferMu.Unlock()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		StopDatabaseTransfers(ctx)
		transferMu.Lock()
		transferManager, transferStore, transferStopping = oldM, oldS, oldStop
		transferMu.Unlock()
		_ = storage.Close()
	})
	_, store, err := databaseTransferManager()
	if err != nil {
		t.Fatal(err)
	}
	original, err := store.Admit(context.Background(), tasks.Request{Kind: tasks.KindBackup, IdempotencyKey: "manual:job:click", Fingerprint: "frozen", JobID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	original.State = tasks.StateCancelled
	if _, err := store.Update(context.Background(), original, original.Revision); err != nil {
		t.Fatal(err)
	}
	m := newBackupManager(nil, newFakeBackupClock(time.Now()), nil, nil)
	// No saved connection remains: recovery must return the original snapshot,
	// not inspect/reconstruct a new job or execute against edited settings.
	got, err := m.submitDurableLocked(BackupJob{ID: "job", ConnectionID: "deleted"}, backupTriggerManual, "click")
	if err != nil || got.ID != original.ID {
		t.Fatalf("manual retry = %+v %v", got, err)
	}
}
