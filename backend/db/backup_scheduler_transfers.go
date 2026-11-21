package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"mincode/app/backend/tasks"
)

func (m *backupManager) submitDurableLocked(job BackupJob, trigger string, occurrence ...string) (BackupRun, error) {
	manager, store, err := databaseTransferManager()
	if err != nil {
		return BackupRun{}, err
	}
	key := uuid.NewString()
	if trigger == backupTriggerManual {
		if len(occurrence) > 0 && occurrence[0] != "" {
			key = "manual:" + job.ID + ":" + occurrence[0]
		}
	} else {
		key = fmt.Sprintf("%s:%d", job.ID, job.State.LastScheduledAt)
		if len(occurrence) > 0 {
			key = occurrence[0]
		}
	}
	if old, e := store.Submission(context.Background(), tasks.KindBackup, key, ""); e == nil {
		return backupRunFromTask(old), nil
	} else if !errors.Is(e, tasks.ErrNotFound) {
		return BackupRun{}, e
	}
	active, err := manager.List(context.Background(), tasks.Filter{Kind: tasks.KindBackup, JobID: job.ID, ActiveOnly: true, Limit: 1})
	if err != nil {
		return BackupRun{}, err
	}
	if len(active.Tasks) > 0 {
		return BackupRun{}, fmt.Errorf("backup_already_running")
	}
	saved, err := loadSavedConnection(job.ConnectionID)
	if err != nil {
		return BackupRun{}, err
	}
	keys, err := transferResourceKeys(saved)
	if err != nil {
		return BackupRun{}, err
	}
	keys = append(keys, "backup-job:"+job.ID)
	now := m.clock.Now()
	payload, _ := json.Marshal(backupTransferPayload{Job: cloneBackupJob(job), Trigger: trigger, StartedAt: now.UnixMilli()})
	task, err := manager.Submit(context.Background(), tasks.Request{Kind: tasks.KindBackup, IdempotencyKey: key, Fingerprint: transferSchemaHash(string(payload)), JobID: job.ID, ConnectionID: job.ConnectionID, Resources: keys, Payload: payload})
	if err != nil {
		return BackupRun{}, err
	}
	return backupRunFromTask(task), nil
}
func (m *backupManager) durableRunsLocked(id string) ([]BackupRun, error) {
	manager, _, err := databaseTransferManager()
	if err != nil {
		return nil, err
	}
	page, err := manager.List(context.Background(), tasks.Filter{Kind: tasks.KindBackup, JobID: id, Limit: backupHistoryLimit})
	if err != nil {
		return nil, err
	}
	runs := []BackupRun{}
	seen := map[string]bool{}
	for _, task := range page.Tasks {
		runs = append(runs, backupRunFromTask(task))
		seen[task.ID] = true
	}
	for _, old := range m.history[id] {
		if !seen[old.ID] && len(runs) < backupHistoryLimit {
			runs = append(runs, old)
		}
	}
	return runs, nil
}
func (m *backupManager) durableBusyLocked(id string) bool {
	manager, _, err := databaseTransferManager()
	if err != nil {
		return true
	}
	page, err := manager.List(context.Background(), tasks.Filter{Kind: tasks.KindBackup, JobID: id, ActiveOnly: true, Limit: 1})
	return err != nil || len(page.Tasks) > 0
}
func (m *backupManager) cancelDurable(id string) bool {
	manager, _, err := databaseTransferManager()
	if err != nil {
		return false
	}
	page, err := manager.List(context.Background(), tasks.Filter{Kind: tasks.KindBackup, JobID: id, ActiveOnly: true, Limit: backupHistoryLimit})
	if err != nil {
		return false
	}
	for _, task := range page.Tasks {
		_, _ = manager.Cancel(context.Background(), task.ID)
	}
	return len(page.Tasks) > 0
}

func backupOccurrenceKey(job BackupJob, now time.Time, loc *time.Location) string {
	// The earliest unconsumed occurrence is stable across catch-up retries even
	// though the scheduler advances its next anchor to the current catch-up time.
	next, ok := nextBackupRun(job.Schedule, backupScheduleAnchor(job), loc)
	if !ok || next.After(now) {
		return ""
	}
	return fmt.Sprintf("%s:%d:%d", job.ID, job.UpdatedAt, next.UnixMilli())
}

func legacyBackupTask(jobID string, run BackupRun) tasks.Task {
	state := tasks.StateFailed
	switch run.Status {
	case backupStatusSuccess:
		state = tasks.StateSucceeded
	case backupStatusCancelled:
		state = tasks.StateCancelled
	}
	payload, _ := json.Marshal(backupTransferPayload{Job: BackupJob{ID: jobID}, Trigger: run.Trigger, StartedAt: run.StartedAt})
	return tasks.Task{ID: run.ID, Kind: tasks.KindBackup, JobID: jobID, State: state, Revision: 1, CreatedAt: run.StartedAt, UpdatedAt: run.FinishedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, ErrorCode: mapLegacyBackupError(run), Result: tasks.Result{Records: run.Rows, Objects: int64(run.Tables), Files: boolCount(run.File != ""), Bytes: run.SizeBytes, Artifact: run.File, Removed: run.Removed, Warnings: run.Warnings, Certainty: tasks.EffectsKnown}, Request: tasks.Request{Payload: payload}}
}
func boolCount(ok bool) int64 {
	if ok {
		return 1
	}
	return 0
}
func mapLegacyBackupError(run BackupRun) string {
	if run.Error != "" {
		return "backup_legacy_failure"
	}
	return ""
}
