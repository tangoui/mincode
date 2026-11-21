// 定时备份命令：
//
//	db_backup_list                       -> [BackupJobView]
//	db_backup_save    {job}              -> BackupJobView
//	db_backup_delete  {id}               -> void
//	db_backup_run_now {id}               -> BackupRun
//	db_backup_cancel  {id}               -> bool
//	db_backup_history {id}               -> [BackupRun]
package db

import (
	"fmt"
	"strings"

	"mincode/app/backend/core"
)

func init() {
	core.Register("db_backup_list", dbBackupListHandler)
	core.Register("db_backup_save", dbBackupSaveHandler)
	core.Register("db_backup_delete", dbBackupDeleteHandler)
	core.Register("db_backup_run_now", dbBackupRunNowHandler)
	core.Register("db_backup_cancel", dbBackupCancelHandler)
	core.Register("db_backup_history", dbBackupHistoryHandler)
}

type backupJobArgs struct {
	Job BackupJob `json:"job"`
}

type backupIDArgs struct {
	ID             string `json:"id"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

func decodeBackupID(argsJSON string) (string, error) {
	var args backupIDArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	id := strings.TrimSpace(args.ID)
	if id == "" || len(id) > 128 {
		return "", fmt.Errorf("备份任务 ID 无效")
	}
	return id, nil
}

func dbBackupListHandler(_, _ string) (string, error) {
	jobs, err := activeBackupManager().list()
	if err != nil {
		return "", fmt.Errorf("读取备份任务: %w", err)
	}
	return core.EncodeResult(jobs)
}

func dbBackupSaveHandler(_, argsJSON string) (string, error) {
	var args backupJobArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	// 状态与时间戳只由后端维护，忽略前端传入值。
	job := args.Job
	job.ID = strings.TrimSpace(job.ID)
	job.State = BackupJobState{}
	job.CreatedAt, job.UpdatedAt = 0, 0
	view, err := activeBackupManager().save(job)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(view)
}

func dbBackupDeleteHandler(_, argsJSON string) (string, error) {
	id, err := decodeBackupID(argsJSON)
	if err != nil {
		return "", err
	}
	if err := activeBackupManager().remove(id); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func dbBackupRunNowHandler(_, argsJSON string) (string, error) {
	id, err := decodeBackupID(argsJSON)
	if err != nil {
		return "", err
	}
	var args backupIDArgs
	if err := strictTransferJSON([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	if len(args.IdempotencyKey) > 128 {
		return "", fmt.Errorf("backup_invalid_request_key")
	}
	run, err := activeBackupManager().runNow(id, args.IdempotencyKey)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(run)
}

func dbBackupCancelHandler(_, argsJSON string) (string, error) {
	id, err := decodeBackupID(argsJSON)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(activeBackupManager().cancelRun(id))
}

func dbBackupHistoryHandler(_, argsJSON string) (string, error) {
	id, err := decodeBackupID(argsJSON)
	if err != nil {
		return "", err
	}
	runs, err := activeBackupManager().runs(id)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(runs)
}
