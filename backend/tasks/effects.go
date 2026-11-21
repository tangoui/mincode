package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type EffectIntent struct{ Kind string }
type EffectOutcome struct {
	Records int64 `json:"records"`
	Objects int64 `json:"objects"`
	Files   int64 `json:"files"`
	Bytes   int64 `json:"bytes"`
	// NoEffect acknowledges a known rollback/refusal, not an unknown network outcome.
	NoEffect bool `json:"noEffect,omitempty"`
}
type Reporter struct {
	manager *Manager
	id      string
	failed  bool
}

func (r *Reporter) BeginEffect(ctx context.Context, intent EffectIntent) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if intent.Kind == "" || len(intent.Kind) > 64 {
		return "", ErrInvalidRequest
	}
	m := r.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.failed {
		return "", fmt.Errorf("task_effect_journal_failed")
	}
	task, err := m.store.Get(ctx, r.id)
	if err != nil {
		return "", err
	}
	if task.State != StateRunning {
		return "", context.Canceled
	}
	id := uuid.NewString()
	_, err = m.store.db.ExecContext(ctx, "INSERT INTO transfer_effects(id,task_id,kind,created_at) VALUES(?,?,?,?)", id, r.id, intent.Kind, time.Now().UnixMilli())
	return id, err
}
func (r *Reporter) FinishEffect(ctx context.Context, id string, outcome EffectOutcome) (retErr error) {
	defer func() {
		if retErr != nil {
			r.manager.mu.Lock()
			r.failed = true
			r.manager.mu.Unlock()
		}
	}()
	if outcome.Records < 0 || outcome.Objects < 0 || outcome.Files < 0 || outcome.Bytes < 0 || (outcome.NoEffect && (outcome.Records != 0 || outcome.Objects != 0 || outcome.Files != 0 || outcome.Bytes != 0)) {
		return ErrInvalidRequest
	}
	// Persist a remote acknowledgement even if cancellation raced the successful commit.
	local, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	m := r.manager
	m.mu.Lock()
	tx, err := m.store.db.BeginTx(local, nil)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	defer tx.Rollback()
	var taskSnapshot, request, payload []byte
	var previous []byte
	err = tx.QueryRowContext(local, "SELECT outcome FROM transfer_effects WHERE id=? AND task_id=?", id, r.id).Scan(&previous)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	data, err := json.Marshal(outcome)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if previous != nil {
		m.mu.Unlock()
		if string(previous) == string(data) {
			return nil
		}
		return ErrRevisionConflict
	}
	err = tx.QueryRowContext(local, "SELECT snapshot,request,payload FROM transfer_tasks WHERE id=?", r.id).Scan(&taskSnapshot, &request, &payload)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	task, err := decodeTask(taskSnapshot, request, payload)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if task.State.Terminal() {
		m.mu.Unlock()
		return ErrInvalidRequest
	}
	if !outcome.NoEffect {
		task.Result.Records += outcome.Records
		task.Result.Objects += outcome.Objects
		task.Result.Files += outcome.Files
		task.Result.Bytes += outcome.Bytes
		task.Result.Certainty = EffectsKnown
	}
	if task.Result.Records < 0 || task.Result.Objects < 0 || task.Result.Files < 0 || task.Result.Bytes < 0 {
		m.mu.Unlock()
		return ErrInvalidRequest
	}
	task, err = updateTask(local, tx, task, task.Revision)
	if err == nil {
		_, err = tx.ExecContext(local, "UPDATE transfer_effects SET outcome=? WHERE id=? AND outcome IS NULL", data, id)
	}
	if err == nil {
		err = tx.Commit()
	}
	m.mu.Unlock()
	if err == nil {
		m.emit(task)
	}
	return err
}
func (s *Store) claim(ctx context.Context, task Task) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	for _, key := range task.Request.Resources {
		var owner string
		err = tx.QueryRowContext(ctx, "SELECT task_id FROM transfer_resource_locks WHERE resource=?", key).Scan(&owner)
		if err == nil {
			return Task{}, ErrRevisionConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Task{}, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO transfer_resource_locks(resource,task_id) VALUES(?,?)", key, task.ID); err != nil {
			return Task{}, err
		}
	}
	task, err = updateTask(ctx, tx, task, task.Revision)
	if err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return task, nil
}
func (s *Store) finish(ctx context.Context, task Task) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM transfer_effects WHERE task_id=? AND outcome IS NULL", task.ID).Scan(&pending); err != nil {
		return Task{}, err
	}
	if pending > 0 {
		task.Result.Certainty = EffectsUncertain
		if task.State == StateSucceeded {
			task.State = StateFailed
			task.ErrorCode = "task_effect_unacknowledged"
		}
	}
	task, err = updateTask(ctx, tx, task, task.Revision)
	if err != nil {
		return Task{}, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM transfer_resource_locks WHERE task_id=?", task.ID); err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return task, nil
}
func (s *Store) recoverInterrupted(ctx context.Context) error {
	// Startup runs before admission, so pagination and individual short transactions
	// do not race a local executor. Recovery is idempotent across failed startups.
	cursor := ""
	for {
		page, err := s.List(ctx, Filter{ActiveOnly: true, Limit: 100, Cursor: cursor})
		if err != nil {
			return err
		}
		for _, task := range page.Tasks {
			task.State = StateInterrupted
			task.ErrorCode = "task_process_interrupted"
			task.FinishedAt = time.Now().UnixMilli()
			if _, err := s.finish(ctx, task); err != nil {
				return err
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM transfer_preparations WHERE consumed_by IS NULL")
	return err
}
