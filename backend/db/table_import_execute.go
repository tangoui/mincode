package db

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"mincode/app/backend/core"
	"mincode/app/backend/storage"
	"mincode/app/backend/tasks"
)

type importPrepareRequest struct {
	SessionID   string `json:"sessionId"`
	Database    string `json:"database"`
	Table       string `json:"table"`
	Format      string `json:"format"`
	Path        string `json:"path,omitempty"`
	Data        string `json:"data,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`
}
type preparedImport struct {
	SessionID   string `json:"sessionId"`
	Database    string `json:"database"`
	Table       string `json:"table"`
	Path        string `json:"path"`
	Digest      string `json:"digest"`
	WorkspaceID string `json:"workspaceId"`
	ColumnsHash string `json:"columnsHash"`
}

var importLeases = struct {
	sync.Mutex
	releases map[string]func()
}{releases: map[string]func(){}}

func prepareTableImport(ctx context.Context, a importPrepareRequest) (string, error) {
	_, store, err := databaseTransferManager()
	if err != nil {
		return "", err
	}
	s, release, err := requireSession(a.SessionID)
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			release()
		}
	}()
	if s.kind != kindSQL && s.kind != kindPlugin {
		return "", fmt.Errorf("import_engine_unsupported")
	}
	if err := validateImportTarget(ctx, s); err != nil {
		return "", err
	}
	columns, err := getTableColumns(ctx, s, a.Database, a.Table)
	if err != nil {
		return "", fmt.Errorf("import_metadata_unavailable")
	}
	names := []string{}
	for _, c := range columns {
		names = append(names, c.Name)
	}
	input, err := importInput(a.Path, a.Data)
	if err != nil {
		return "", err
	}
	defer input.Close()
	own, err := storage.Path()
	if err != nil {
		return "", err
	}
	path, err := prepareImportRows(ctx, input, a.Format, names, filepath.Join(filepath.Dir(own), "transfer-staging"), defaultTransferLimits())
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	digest, err := digestTransferReader(ctx, f)
	_ = f.Close()
	if err != nil {
		return "", err
	}
	cols, _ := json.Marshal(columns)
	p := preparedImport{SessionID: a.SessionID, Database: a.Database, Table: a.Table, Path: path, Digest: digest, WorkspaceID: a.WorkspaceID, ColumnsHash: transferSchemaHash(string(cols))}
	payload, _ := json.Marshal(p)
	id := uuid.NewString()
	if err := store.Prepare(ctx, tasks.Preparation{ID: id, Kind: tasks.KindImport, Fingerprint: transferSchemaHash(string(payload)), ExpiresAt: time.Now().Add(defaultTransferLimits().Expiry).UnixMilli(), Payload: payload}); err != nil {
		return "", err
	}
	importLeases.Lock()
	importLeases.releases[id] = release
	importLeases.Unlock()
	time.AfterFunc(defaultTransferLimits().Expiry, func() {
		released, err := store.ReleasePreparation(context.Background(), id)
		if err != nil || !released {
			return
		}
		importLeases.Lock()
		r := importLeases.releases[id]
		delete(importLeases.releases, id)
		importLeases.Unlock()
		if r != nil {
			r()
			_ = os.Remove(path)
		}
	})
	keep = true
	ok = true
	return id, nil
}
func submitTableImport(ctx context.Context, id, key string) (tasks.Task, error) {
	m, store, err := databaseTransferManager()
	if err != nil {
		return tasks.Task{}, err
	}
	if task, e := store.Submission(ctx, tasks.KindImport, key, id); e == nil {
		return task, nil
	} else if !errors.Is(e, tasks.ErrNotFound) {
		return tasks.Task{}, e
	}
	p, err := store.Preparation(ctx, id)
	if err != nil {
		return tasks.Task{}, err
	}
	var input preparedImport
	if err = json.Unmarshal(p.Payload, &input); err != nil {
		return tasks.Task{}, err
	}
	// Session-backed imports share a conservative global claim: two session IDs can
	// still address the same database. This intentionally favors safety over parallelism.
	return m.Submit(ctx, tasks.Request{Kind: tasks.KindImport, PreparationID: id, IdempotencyKey: key, Fingerprint: p.Fingerprint, WorkspaceID: input.WorkspaceID, Resources: []string{"database-transfers"}})
}
func executeTableImport(ctx context.Context, task tasks.Task, r *tasks.Reporter) (tasks.Result, error) {
	var p preparedImport
	if err := json.Unmarshal(task.Request.Payload, &p); err != nil {
		return tasks.Result{}, err
	}
	defer os.Remove(p.Path)
	importLeases.Lock()
	release := importLeases.releases[task.Request.PreparationID]
	delete(importLeases.releases, task.Request.PreparationID)
	importLeases.Unlock()
	if release == nil {
		return tasks.Result{}, fmt.Errorf("import_session_expired")
	}
	defer release()
	s, unlease, err := requireSession(p.SessionID)
	if err != nil {
		return tasks.Result{}, err
	}
	defer unlease()
	if err := validateImportTarget(ctx, s); err != nil {
		return tasks.Result{}, err
	}
	columns, err := getTableColumns(ctx, s, p.Database, p.Table)
	if err != nil {
		return tasks.Result{}, fmt.Errorf("import_metadata_unavailable")
	}
	cols, _ := json.Marshal(columns)
	if transferSchemaHash(string(cols)) != p.ColumnsHash {
		return tasks.Result{}, fmt.Errorf("import_target_changed")
	}
	f, err := os.Open(p.Path)
	if err != nil {
		return tasks.Result{}, err
	}
	defer f.Close()
	h, err := digestTransferReader(ctx, f)
	if err != nil || h != p.Digest {
		return tasks.Result{}, fmt.Errorf("import_prepared_changed")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return tasks.Result{}, err
	}
	// SQL 安全网：记录新增行主键，导入结束（成功或中途失败）时保存一条记录。
	capture := newImportCapture(ctx, s, p.Database, p.Table)
	defer capture.save()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), defaultTransferLimits().RecordBytes)
	batch := []map[string]any{}
	for scanner.Scan() {
		var row map[string]any
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		if err := decoder.Decode(&row); err != nil {
			return tasks.Result{}, err
		}
		for key, value := range row {
			if number, ok := value.(json.Number); ok {
				row[key] = number.String()
			}
		}
		if s.kind == kindSQL && s.vector == nil && s.dialect != "dm" && s.dialect != "firebird" {
			batch = append(batch, row)
			if len(batch) >= restoreBatchRows {
				if err := importSQLBatch(ctx, s, p.Database, p.Table, batch, r, capture); err != nil {
					return tasks.Result{}, err
				}
				batch = nil
			}
			continue
		}
		id, err := r.BeginEffect(ctx, tasks.EffectIntent{Kind: "insert-row"})
		if err != nil {
			return tasks.Result{}, err
		}
		n, err := insertRow(ctx, s, p.Database, p.Table, row)
		if err != nil {
			return tasks.Result{}, fmt.Errorf("import_insert_failed")
		}
		capture.committed(n)
		if err = r.FinishEffect(ctx, id, tasks.EffectOutcome{Records: n}); err != nil {
			return tasks.Result{}, err
		}
	}
	if err := scanner.Err(); err != nil {
		return tasks.Result{}, err
	}
	if len(batch) > 0 {
		if err := importSQLBatch(ctx, s, p.Database, p.Table, batch, r, capture); err != nil {
			return tasks.Result{}, err
		}
	}
	return tasks.Result{}, nil
}
func init() {
	core.RegisterCtx("db_data_import_prepare", func(ctx context.Context, _, raw string) (string, error) {
		var a importPrepareRequest
		if err := strictTransferJSON([]byte(raw), &a); err != nil {
			return "", err
		}
		id, err := prepareTableImport(ctx, a)
		if err != nil {
			return "", err
		}
		return core.EncodeResult(map[string]string{"planId": id})
	})
	core.RegisterCtx("db_data_import_start", dbDataImportStartHandler)
}

// dbDataImportStartHandler: db_data_import_start {planId, idempotencyKey,
// checkId?, confirmText?}. A new submission must present the token of
// db_safety_check operation {kind: "import", planId} (plugin sessions excepted).
func dbDataImportStartHandler(ctx context.Context, _, raw string) (string, error) {
	var a struct {
		PlanID         string  `json:"planId"`
		IdempotencyKey string  `json:"idempotencyKey"`
		CheckID        *string `json:"checkId"`
		ConfirmText    *string `json:"confirmText"`
	}
	if err := strictTransferJSON([]byte(raw), &a); err != nil {
		return "", err
	}
	if err := requireImportStartCheck(ctx, a.PlanID, a.IdempotencyKey, a.CheckID, a.ConfirmText); err != nil {
		return "", err
	}
	task, err := submitTableImport(ctx, a.PlanID, a.IdempotencyKey)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(task)
}

func digestTransferReader(ctx context.Context, r io.Reader) (string, error) {
	h := sha256.New()
	_, err := io.Copy(h, &transferReader{ctx: ctx, r: r, left: defaultTransferLimits().ExpandedBytes})
	return hex.EncodeToString(h.Sum(nil)), err
}

func validateImportTarget(ctx context.Context, s *dbSession) error {
	if s.kind != kindSQL || s.dialect != "sqlite" {
		return nil
	}
	own, err := storage.Path()
	if err != nil {
		return err
	}
	ownInfo, err := os.Stat(own)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return fmt.Errorf("import_target_identity_unavailable")
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int
		var name, path string
		if err := rows.Scan(&sequence, &name, &path); err != nil {
			return err
		}
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("import_target_identity_unavailable")
		}
		if os.SameFile(ownInfo, info) {
			return fmt.Errorf("transfer_application_storage_forbidden")
		}
	}
	return rows.Err()
}

func importSQLBatch(ctx context.Context, s *dbSession, database, table string, rows []map[string]any, r *tasks.Reporter, capture *importCapture) error {
	database, table, err := resolveSQLWriteTarget(ctx, s, database, table)
	if err != nil {
		return err
	}
	qualified, err := qualifiedTable(s.dialect, database, table)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	effect, err := r.BeginEffect(ctx, tasks.EffectIntent{Kind: "import-insert-batch"})
	if err != nil {
		return err
	}
	var count int64
	for i := 0; i < len(rows); i++ {
		row := rows[i]
		names := []string{}
		for name := range row {
			names = append(names, name)
		}
		sort.Strings(names)
		columns, marks := []string{}, []string{}
		values := []any{}
		for i, name := range names {
			columns = append(columns, quoteColumn(s.dialect, name))
			marks = append(marks, placeholder(s.dialect, i+1))
			values = append(values, jsonToSQLValue(row[name]))
		}
		n, err := capture.insert(ctx, tx, qualified, columns, marks, values, row)
		if errors.Is(err, errKeyCaptureUnavailable) && i == 0 {
			// The key-capture probe broke the (still empty) transaction:
			// start the batch over in a fresh one, without key capture.
			_ = tx.Rollback()
			// Begin into a fresh variable: on failure tx keeps the rolled-back
			// transaction, so the deferred Rollback never sees a nil tx.
			next, beginErr := s.db.BeginTx(ctx, nil)
			if beginErr != nil {
				capture.rolledBack()
				if ackErr := r.FinishEffect(ctx, effect, tasks.EffectOutcome{NoEffect: true}); ackErr != nil {
					return ackErr
				}
				return beginErr
			}
			tx = next
			i--
			continue
		}
		if errors.Is(err, errImportCount) {
			capture.rolledBack()
			return fmt.Errorf("import_count_unavailable")
		}
		if err != nil {
			capture.rolledBack()
			if rollbackErr := tx.Rollback(); rollbackErr == nil {
				if ackErr := r.FinishEffect(ctx, effect, tasks.EffectOutcome{NoEffect: true}); ackErr != nil {
					return ackErr
				}
			}
			return fmt.Errorf("import_insert_failed")
		}
		count += n
	}
	capture.readBack(ctx, tx)
	if err := tx.Commit(); err != nil {
		capture.rolledBack()
		return fmt.Errorf("import_commit_uncertain")
	}
	capture.committed(count)
	return r.FinishEffect(ctx, effect, tasks.EffectOutcome{Records: count})
}
