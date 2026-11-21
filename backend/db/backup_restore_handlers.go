// 备份恢复命令：
//
//	db_backup_restore_plan   {connectionId, path}            -> RestorePlan
//	db_backup_restore_start  {planId, idempotencyKey, unrestrictedRiskAccepted}   -> {restoreId}
//	db_backup_restore_cancel {restoreId}                     -> bool
//
// 恢复在后台运行，通过 db-backup:restore 事件报告进度，结束时发送带
// done=true 的事件（含结果或错误）。恢复会写入目标库，只能由用户在
// 确认对话框中显式启动，从不由计划任务触发。
package db

import (
	"context"
	"fmt"
	"time"

	"mincode/app/backend/core"
)

// RestoreEvent is the db-backup:restore payload.
type RestoreEvent struct {
	RestoreProgress
	Done   bool           `json:"done"`
	Result *RestoreResult `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

func init() {
	core.RegisterCtx("db_backup_restore_plan", func(ctx context.Context, _, raw string) (string, error) {
		var a RestorePrepareRequest
		if err := strictTransferJSON([]byte(raw), &a); err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		plan, err := prepareRestore(ctx, a)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(plan)
	})
	core.RegisterCtx("db_backup_restore_start", func(ctx context.Context, _, raw string) (string, error) {
		var a RestoreSubmitRequest
		if err := strictTransferJSON([]byte(raw), &a); err != nil {
			return "", fmt.Errorf("restore_review_required")
		}
		task, err := submitRestore(ctx, a)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(map[string]string{"restoreId": task.ID, "taskId": task.ID})
	})
	core.RegisterCtx("db_backup_restore_cancel", func(ctx context.Context, _, raw string) (string, error) {
		var a struct {
			RestoreID string `json:"restoreId"`
		}
		if err := strictTransferJSON([]byte(raw), &a); err != nil {
			return "", err
		}
		m, _, err := databaseTransferManager()
		if err != nil {
			return "", err
		}
		_, err = m.Cancel(ctx, a.RestoreID)
		return core.EncodeResult(err == nil)
	})
}
