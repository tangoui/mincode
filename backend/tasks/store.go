package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }
func validKind(k Kind) bool      { return k == KindBackup || k == KindRestore || k == KindImport }
func normalizeRequest(r Request) (Request, error) {
	if !validKind(r.Kind) || r.IdempotencyKey == "" || len(r.IdempotencyKey) > 256 || r.Fingerprint == "" || len(r.Fingerprint) > 256 || len(r.Payload) > 1<<20 || len(r.Resources) > 128 || len(r.WorkspaceID) > 128 || len(r.ConnectionID) > 128 || len(r.JobID) > 128 || len(r.PreparationID) > 128 {
		return r, ErrInvalidRequest
	}
	r.Resources = append([]string(nil), r.Resources...)
	sort.Strings(r.Resources)
	unique := r.Resources[:0]
	for _, key := range r.Resources {
		if key == "" || len(key) > 4096 || strings.ContainsRune(key, 0) {
			return r, ErrInvalidRequest
		}
		if len(unique) == 0 || unique[len(unique)-1] != key {
			unique = append(unique, key)
		}
	}
	r.Resources = unique
	if len(r.Payload) == 0 {
		r.Payload = json.RawMessage(`{}`)
	}
	if !json.Valid(r.Payload) {
		return r, ErrInvalidRequest
	}
	return r, nil
}
func (s *Store) Prepare(ctx context.Context, p Preparation) error {
	if p.ID == "" || len(p.ID) > 128 || !validKind(p.Kind) || p.Fingerprint == "" || len(p.Fingerprint) > 256 || len(p.Payload) > 1<<20 || !json.Valid(p.Payload) {
		return ErrInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM transfer_preparations WHERE consumed_by IS NULL AND expires_at>?", time.Now().UnixMilli()).Scan(&count); err != nil {
		return err
	}
	if count >= 16 {
		return fmt.Errorf("task_preparation_limit")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO transfer_preparations(id,kind,fingerprint,expires_at,payload) VALUES(?,?,?,?,?)", p.ID, p.Kind, p.Fingerprint, p.ExpiresAt, []byte(p.Payload)); err != nil {
		return err
	}
	return tx.Commit()
}

// Admit consumes a preparation and persists a queued task in one transaction.
func (s *Store) Admit(ctx context.Context, r Request) (Task, error) {
	r, err := normalizeRequest(r)
	if err != nil {
		return Task{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	var snapshot, request, payload []byte
	var fingerprint string
	err = tx.QueryRowContext(ctx, "SELECT fingerprint,snapshot,request,payload FROM transfer_tasks WHERE kind=? AND idempotency_key=?", r.Kind, r.IdempotencyKey).Scan(&fingerprint, &snapshot, &request, &payload)
	if err == nil {
		existing, e := decodeTask(snapshot, request, payload)
		if e != nil {
			return Task{}, e
		}
		// The caller's hash is not sufficient: bind all public request identity fields too.
		expected, _ := json.Marshal(r)
		actual, _ := json.Marshal(existing.Request)
		if fingerprint != r.Fingerprint || string(expected) != string(actual) || (r.PreparationID == "" && string(r.Payload) != string(payload)) {
			return Task{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Task{}, err
	}
	now := time.Now().UnixMilli()
	if r.PreparationID != "" {
		var kind Kind
		var hash string
		var expires int64
		var consumed sql.NullString
		err = tx.QueryRowContext(ctx, "SELECT kind,fingerprint,expires_at,payload,consumed_by FROM transfer_preparations WHERE id=?", r.PreparationID).Scan(&kind, &hash, &expires, &payload, &consumed)
		if errors.Is(err, sql.ErrNoRows) {
			return Task{}, ErrPreparationUnavailable
		}
		if err != nil {
			return Task{}, err
		}
		if kind != r.Kind || hash != r.Fingerprint || expires <= now || consumed.Valid {
			return Task{}, ErrPreparationUnavailable
		}
		r.Payload = append(json.RawMessage(nil), payload...)
	}
	task := Task{ID: uuid.NewString(), Kind: r.Kind, WorkspaceID: r.WorkspaceID, ConnectionID: r.ConnectionID, JobID: r.JobID, State: StateQueued, Revision: 1, CreatedAt: now, UpdatedAt: now, Result: Result{Certainty: EffectsNone}, Request: r}
	snapshot, err = json.Marshal(task)
	if err != nil {
		return Task{}, err
	}
	request, err = json.Marshal(r)
	if err != nil {
		return Task{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO transfer_tasks(id,kind,idempotency_key,fingerprint,workspace_id,job_id,state,revision,created_at,request,payload,snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", task.ID, r.Kind, r.IdempotencyKey, r.Fingerprint, r.WorkspaceID, r.JobID, task.State, task.Revision, now, request, []byte(r.Payload), snapshot)
	if err != nil {
		return Task{}, err
	}
	if r.PreparationID != "" {
		res, e := tx.ExecContext(ctx, "UPDATE transfer_preparations SET consumed_by=? WHERE id=? AND consumed_by IS NULL", task.ID, r.PreparationID)
		if e != nil {
			return Task{}, e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return Task{}, e
		}
		if n != 1 {
			return Task{}, ErrPreparationUnavailable
		}
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return task, nil
}
func decodeTask(snapshot, request, payload []byte) (Task, error) {
	var task Task
	if err := json.Unmarshal(snapshot, &task); err != nil {
		return task, fmt.Errorf("decode task: %w", err)
	}
	if err := json.Unmarshal(request, &task.Request); err != nil {
		return task, fmt.Errorf("decode task request: %w", err)
	}
	task.Request.Payload = append(json.RawMessage(nil), payload...)
	return task, nil
}
func (s *Store) Get(ctx context.Context, id string) (Task, error) {
	var snapshot, request, payload []byte
	err := s.db.QueryRowContext(ctx, "SELECT snapshot,request,payload FROM transfer_tasks WHERE id=?", id).Scan(&snapshot, &request, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return decodeTask(snapshot, request, payload)
}
func (s *Store) List(ctx context.Context, f Filter) (Page, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 1000 {
		f.Limit = 1000
	}
	query := "SELECT snapshot,request,payload FROM transfer_tasks WHERE 1=1"
	args := []any{}
	if f.Kind != "" {
		query += " AND kind=?"
		args = append(args, f.Kind)
	}
	if f.JobID != "" {
		query += " AND job_id=?"
		args = append(args, f.JobID)
	}
	if f.WorkspaceID != "" {
		query += " AND workspace_id=?"
		args = append(args, f.WorkspaceID)
	}
	if f.ActiveOnly {
		query += " AND state IN ('queued','running','cancelling')"
	}
	if f.Cursor != "" {
		query += " AND (created_at,id)<(SELECT created_at,id FROM transfer_tasks WHERE id=?)"
		args = append(args, f.Cursor)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, f.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page := Page{Tasks: []Task{}}
	for rows.Next() {
		var snapshot, request, payload []byte
		if err := rows.Scan(&snapshot, &request, &payload); err != nil {
			return Page{}, err
		}
		task, err := decodeTask(snapshot, request, payload)
		if err != nil {
			return Page{}, err
		}
		page.Tasks = append(page.Tasks, task)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Tasks) > f.Limit {
		page.Tasks = page.Tasks[:f.Limit]
		page.NextCursor = page.Tasks[len(page.Tasks)-1].ID
	}
	return page, nil
}
func allowedTransition(from, to State) bool {
	if from.Terminal() {
		return false
	}
	if from == to {
		return true
	}
	switch from {
	case StateQueued:
		return to == StateRunning || to == StateCancelled || to == StateInterrupted
	case StateRunning:
		return to == StateCancelling || to == StateSucceeded || to == StateFailed || to == StateCancelled || to == StateInterrupted
	case StateCancelling:
		return to == StateSucceeded || to == StateFailed || to == StateCancelled || to == StateInterrupted
	}
	return false
}
func (s *Store) Update(ctx context.Context, next Task, revision int64) (Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	next, err = updateTask(ctx, tx, next, revision)
	if err != nil {
		return Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return Task{}, err
	}
	return next, nil
}
func updateTask(ctx context.Context, tx *sql.Tx, next Task, revision int64) (Task, error) {
	var snapshot, request, payload []byte
	err := tx.QueryRowContext(ctx, "SELECT snapshot,request,payload FROM transfer_tasks WHERE id=?", next.ID).Scan(&snapshot, &request, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	old, err := decodeTask(snapshot, request, payload)
	if err != nil {
		return Task{}, err
	}
	if old.Revision != revision {
		return Task{}, ErrRevisionConflict
	}
	if !allowedTransition(old.State, next.State) || next.Result.Records < old.Result.Records || next.Result.Objects < old.Result.Objects || next.Result.Files < old.Result.Files || next.Result.Bytes < old.Result.Bytes || len(next.Result.Warnings) > 50 || len(next.ErrorCode) > 128 {
		return Task{}, ErrInvalidRequest
	}
	// Execution cannot mutate admitted identity or private parameters.
	next.Request = old.Request
	next.Kind = old.Kind
	next.WorkspaceID = old.WorkspaceID
	next.ConnectionID = old.ConnectionID
	next.JobID = old.JobID
	next.CreatedAt = old.CreatedAt
	next.Revision = revision + 1
	next.UpdatedAt = time.Now().UnixMilli()
	data, err := json.Marshal(next)
	if err != nil {
		return Task{}, err
	}
	res, err := tx.ExecContext(ctx, "UPDATE transfer_tasks SET snapshot=?,state=?,revision=? WHERE id=? AND revision=?", data, next.State, next.Revision, next.ID, revision)
	if err != nil {
		return Task{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Task{}, err
	}
	if n != 1 {
		return Task{}, ErrRevisionConflict
	}
	return next, nil
}

func (s *Store) Preparation(ctx context.Context, id string) (Preparation, error) {
	var p Preparation
	var consumed sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT id,kind,fingerprint,expires_at,payload,consumed_by FROM transfer_preparations WHERE id=?", id).Scan(&p.ID, &p.Kind, &p.Fingerprint, &p.ExpiresAt, &p.Payload, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrPreparationUnavailable
	}
	if err != nil {
		return p, err
	}
	if consumed.Valid || p.ExpiresAt <= time.Now().UnixMilli() {
		return p, ErrPreparationUnavailable
	}
	return p, nil
}

// Submission lets a client recover an admitted task after losing the start response.
func (s *Store) Submission(ctx context.Context, kind Kind, key, preparationID string) (Task, error) {
	var snapshot, request, payload []byte
	err := s.db.QueryRowContext(ctx, "SELECT snapshot,request,payload FROM transfer_tasks WHERE kind=? AND idempotency_key=?", kind, key).Scan(&snapshot, &request, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	task, err := decodeTask(snapshot, request, payload)
	if err != nil {
		return Task{}, err
	}
	if task.Request.PreparationID != preparationID {
		return Task{}, ErrIdempotencyConflict
	}
	return task, nil
}

// ReleasePreparation atomically surrenders only unconsumed input ownership.
func (s *Store) ReleasePreparation(ctx context.Context, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM transfer_preparations WHERE id=? AND consumed_by IS NULL", id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// ImportLegacy preserves stable old run IDs. Repeating startup never rewrites an
// imported result or creates a second independently authoritative history.
func (s *Store) ImportLegacy(ctx context.Context, task Task) error {
	if task.ID == "" || !task.State.Terminal() || task.Kind != KindBackup {
		return ErrInvalidRequest
	}
	task.Request.Kind = task.Kind
	task.Request.JobID = task.JobID
	task.Request.IdempotencyKey = "legacy:" + task.ID
	task.Request.Fingerprint = "legacy"
	if len(task.Request.Payload) == 0 {
		task.Request.Payload = json.RawMessage(`{}`)
	}
	snapshot, err := json.Marshal(task)
	if err != nil {
		return err
	}
	request, err := json.Marshal(task.Request)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO transfer_tasks(id,kind,idempotency_key,fingerprint,workspace_id,job_id,state,revision,created_at,request,payload,snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING", task.ID, task.Kind, task.Request.IdempotencyKey, task.Request.Fingerprint, task.WorkspaceID, task.JobID, task.State, task.Revision, task.CreatedAt, request, []byte(task.Request.Payload), snapshot)
	return err
}
