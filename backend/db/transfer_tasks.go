package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mincode/app/backend/tasks"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/storage"
)

// A coarse claim deliberately serializes unrecognized endpoint aliases. It is
// an in-app exclusion mechanism, not protection from external database writers.
func transferResourceKeys(saved *backupSavedConnection) ([]string, error) {
	engine := strings.ToLower(saved.DbType)
	if saved.Driver != nil {
		engine = "plugin"
	}
	switch engine {
	case "mariadb", "tidb", "oceanbase", "polardb", "greatsql", "dolt":
		engine = "mysql"
	case "postgresql", "cockroachdb", "pgvector":
		engine = "postgres"
	}
	if engine == "" {
		return nil, fmt.Errorf("transfer_invalid_engine")
	}
	keys := []string{"database-transfers", "database-engine:" + engine}
	if saved.FilePath != nil && *saved.FilePath != "" {
		path, err := filepath.Abs(*saved.FilePath)
		if err != nil {
			return nil, err
		}
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		if own, err := storage.Path(); err == nil {
			a, ae := os.Stat(own)
			b, be := os.Stat(path)
			if filepath.Clean(path) == filepath.Clean(own) || (ae == nil && be == nil && os.SameFile(a, b)) {
				return nil, fmt.Errorf("transfer_application_storage_forbidden")
			}
		}
		sum := sha256.Sum256([]byte(filepath.Clean(path)))
		keys = append(keys, "database-file:"+hex.EncodeToString(sum[:]))
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(saved.Host)), ".")
	port := saved.Port
	if port == 0 {
		switch engine {
		case "mysql":
			port = 3306
		case "postgres":
			port = 5432
		case "redis":
			port = 6379
		case "mongodb":
			port = 27017
		}
	}
	endpoint, _ := json.Marshal(struct {
		Host   string
		Port   int
		Tunnel json.RawMessage
	}{host, port, saved.Tunnel})
	sum := sha256.Sum256(endpoint)
	keys = append(keys, "database-endpoint:"+hex.EncodeToString(sum[:]))
	return keys, nil
}

func transferSchemaHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func connectionFingerprint(saved *backupSavedConnection) string {
	copy := *saved
	copy.Name = ""
	data, _ := json.Marshal(copy)

	return transferSchemaHash(string(data))
}

type backupTransferPayload struct {
	Job       BackupJob `json:"job"`
	Trigger   string    `json:"trigger"`
	StartedAt int64     `json:"startedAt"`
}

func executeBackupTransfer(ctx context.Context, task tasks.Task, r *tasks.Reporter) (tasks.Result, error) {
	var p backupTransferPayload
	if err := json.Unmarshal(task.Request.Payload, &p); err != nil {
		return tasks.Result{}, err
	}
	ctx = withTransferReporter(ctx, r)
	result, err := executeBackupJob(ctx, p.Job, time.UnixMilli(p.StartedAt))
	if err != nil {
		if ctx.Err() != nil {
			return tasks.Result{}, ctx.Err()
		}
		return tasks.Result{}, fmt.Errorf("backup_execution_failed")
	}

	return tasks.Result{Artifact: result.File, Removed: result.Removed, Warnings: result.Warnings}, nil
}
func backupRunFromTask(task tasks.Task) BackupRun {
	run := BackupRun{ID: task.ID, JobID: task.JobID, Status: backupStatusRunning, Trigger: backupTriggerManual, StartedAt: task.CreatedAt, FinishedAt: task.FinishedAt, Rows: task.Result.Records, Tables: int(task.Result.Objects), File: task.Result.Artifact, SizeBytes: task.Result.Bytes, Removed: task.Result.Removed, Warnings: task.Result.Warnings, Error: task.ErrorCode}
	var p backupTransferPayload
	if json.Unmarshal(task.Request.Payload, &p) == nil {
		run.Trigger = p.Trigger
	}
	if task.StartedAt > 0 {
		run.StartedAt = task.StartedAt
	}
	if task.FinishedAt > 0 {
		run.DurationMs = task.FinishedAt - run.StartedAt
	}
	switch task.State {
	case tasks.StateSucceeded:
		run.Status = backupStatusSuccess
	case tasks.StateCancelled:
		run.Status = backupStatusCancelled
	case tasks.StateFailed, tasks.StateInterrupted:
		run.Status = backupStatusFailed
	}
	if task.Result.Certainty == tasks.EffectsUncertain {
		run.Warnings = append(run.Warnings, "task_effects_uncertain")
	}
	return run
}

var targetFilePins = struct {
	sync.Mutex
	files map[string]os.FileInfo
}{files: map[string]os.FileInfo{}}

func captureTransferTargetFile(saved *backupSavedConnection) (os.FileInfo, error) {
	if saved.FilePath == nil || *saved.FilePath == "" {
		return nil, nil
	}
	info, err := os.Stat(*saved.FilePath)
	if err != nil {
		return nil, fmt.Errorf("restore_target_file_unavailable")
	}
	return info, nil
}
func verifyTransferTargetFile(planID string, saved *backupSavedConnection) error {
	if saved.FilePath == nil || *saved.FilePath == "" {
		return nil
	}
	targetFilePins.Lock()
	before := targetFilePins.files[planID]
	targetFilePins.Unlock()
	after, err := os.Stat(*saved.FilePath)
	if err != nil || before == nil || !os.SameFile(before, after) {
		return fmt.Errorf("restore_target_file_changed")
	}
	return nil
}
func releaseTargetFilePin(planID string) {
	targetFilePins.Lock()
	delete(targetFilePins.files, planID)
	targetFilePins.Unlock()
}
