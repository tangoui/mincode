// SQL 安全网：导入数据（db_import_data、db_data_import_start）。
//
// 导入只做 INSERT（主键冲突时整批失败回滚），不会覆盖已有行，所以没有“被覆盖的行”要留底：
// 检查估算导入行数，执行时记录新增行的主键（每次导入一条记录，Source "import"），撤回时按主键
// 删除。主键来自 RETURNING（Postgres、SQLite）、OUTPUT（无触发器的 SQL Server）、
// LastInsertId（MySQL 自增主键），或在每行都给出主键时于提交前按主键分批回读；做不到的
// 导入只记录、不可撤回。无主键只警告。日志只记录记录 ID 与行数。
package db

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"mincode/app/backend/db/sqlsafety"
	"mincode/app/backend/tasks"
)

// importOperation is the operation db_import_data performs.
func importOperation(table, format, data string) safetyOperation {
	return safetyOperation{Kind: "import", Table: table, Format: format, Data: data}
}

// checkImport assesses an import: inline data (table, format, data) or a
// prepared plan (planId, which names its own session, database and table).
func checkImport(ctx context.Context, s *dbSession, op safetyOperation, database string) (*sqlsafety.Check, safetyPlan, error) {
	table := op.Table
	var rows int64
	var countErr error
	if op.PlanID != "" {
		p, err := loadPreparedImport(ctx, op.PlanID)
		if err != nil {
			return nil, safetyPlan{}, err
		}
		if p.SessionID != s.sessionID {
			return nil, safetyPlan{}, fmt.Errorf("导入计划不属于该会话")
		}
		database, table = strings.TrimSpace(p.Database), p.Table
		rows, countErr = countPreparedRows(p.Path)
	} else {
		if table == "" {
			return nil, safetyPlan{}, fmt.Errorf("导入目标表不能为空")
		}
		rows, countErr = countImportRecords(op.Format, op.Data)
	}
	if s.kind != kindSQL {
		return nil, safetyPlan{}, fmt.Errorf("import_engine_unsupported")
	}
	est := sqlsafety.Estimate{Source: "none"}
	if countErr == nil {
		est = sqlsafety.Estimate{Rows: rows, Exact: true, Source: "count"}
	}
	st := sqlsafety.Statement{Kind: sqlsafety.KindInsert, Table: &sqlsafety.TableRef{Name: table}, InsertRows: rows}
	undoable := false
	var extra []sqlsafety.Reason
	qualified := table
	switch {
	case !importKeyCapture(s):
		extra = append(extra, sqlsafety.ReasonNotUndoableStore)
	case s.safetyWithoutTransactions():
		extra = append(extra, sqlsafety.ReasonNoTransaction)
	default:
		db, tbl, err := resolveSQLWriteTarget(ctx, s, database, table)
		if err != nil {
			return nil, safetyPlan{}, err
		}
		if q, err := qualifiedTable(s.dialect, db, tbl); err == nil {
			qualified = q
		}
		if info, err := getPrimaryKeys(ctx, s, db, tbl); err != nil || info == nil || len(info.Columns) == 0 {
			extra = append(extra, sqlsafety.ReasonNoPrimaryKey)
		} else {
			undoable = true
		}
	}
	st.Text = importStatementText(qualified)
	sc := sqlsafety.StatementCheck{Table: st.Table}
	assessed(&sc, st, est, undoable, extra)
	check := &sqlsafety.Check{SessionID: s.sessionID, Database: database, Statements: []sqlsafety.StatementCheck{sc}}
	return check, safetyPlan{statements: []safetyResolvedStatement{{parsed: st}}}, nil
}

func importStatementText(qualified string) string {
	return "INSERT INTO " + qualified + " /* import */"
}

// importKeyCapture: the import runs in batched transactions where keys can
// be captured (DM, Firebird and vector sessions insert row by row).
func importKeyCapture(s *dbSession) bool {
	return s.kind == kindSQL && s.vector == nil && s.dialect != "dm" && s.dialect != "firebird"
}

func loadPreparedImport(ctx context.Context, planID string) (preparedImport, error) {
	var p preparedImport
	_, store, err := databaseTransferManager()
	if err != nil {
		return p, err
	}
	prep, err := store.Preparation(ctx, planID)
	if err != nil {
		return p, err
	}
	if prep.Kind != tasks.KindImport {
		return p, fmt.Errorf("import_plan_invalid")
	}
	if err := json.Unmarshal(prep.Payload, &p); err != nil {
		return p, err
	}
	return p, nil
}

// countPreparedRows counts the records of a prepared import (one JSON line each).
func countPreparedRows(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), defaultTransferLimits().RecordBytes)
	var n int64
	for scanner.Scan() {
		n++
	}
	return n, scanner.Err()
}

// countImportRecords counts the records of inline import data: CSV rows
// after the header, or the elements of a JSON array.
func countImportRecords(format, data string) (int64, error) {
	var n int64
	switch format {
	case "csv":
		reader := csv.NewReader(strings.NewReader(data))
		reader.FieldsPerRecord = -1
		for {
			_, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return 0, err
			}
			n++
		}
		return max(n-1, 0), nil
	case "json":
		decoder := json.NewDecoder(strings.NewReader(data))
		if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
			return 0, fmt.Errorf("import_json_array_required")
		}
		for decoder.More() {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return 0, err
			}
			n++
		}
		return n, nil
	}
	return 0, fmt.Errorf("import_format_unsupported")
}

// requireImportStartCheck consumes the token db_data_import_start must
// present. Repeating a submission already made needs none (it starts
// nothing new).
func requireImportStartCheck(ctx context.Context, planID, key string, checkID, confirmText *string) error {
	_, store, err := databaseTransferManager()
	if err != nil {
		return err
	}
	if _, err := store.Submission(ctx, tasks.KindImport, key, planID); err == nil {
		return nil
	}
	p, err := loadPreparedImport(ctx, planID)
	if err != nil {
		return err
	}
	s, err := requireSession(p.SessionID)
	if err != nil {
		return err
	}

	_, _, err = consumeCheck(s, checkID, confirmText, strings.TrimSpace(p.Database), operationHash(safetyOperation{Kind: "import", PlanID: planID}))
	return err
}

// ── 执行时记录新增主键 ──

type importKeyMode int

const (
	importKeysByValue   importKeyMode = iota // rows name their keys: read them back before commit
	importKeysReturning                      // INSERT ... RETURNING <pk>
	importKeysOutput                         // INSERT ... OUTPUT INSERTED.<pk> VALUES ...
)

// errImportCount: an insert ran but its row count is unavailable.
var errImportCount = errors.New("import_count_unavailable")

// importCapture collects the primary keys an import inserts, batch by
// batch, and saves one record when the import ends. nil while the safety
// net is disabled; reason is set once the import cannot be undone.
type importCapture struct {
	s             *dbSession
	settings      sqlsafety.Settings
	database      string
	table         sqlsafety.TableRef
	qualified     string
	pk            []string
	quotedPK      []string
	mode          importKeyMode
	autoIncrement bool             // MySQL single-column AUTO_INCREMENT key: LastInsertId
	probed        bool             // the first RETURNING / OUTPUT insert ran
	reason        sqlsafety.Reason // why the import cannot be undone; "" while keys are collected
	keys          [][]any          // keys of committed batches
	batch         [][]any          // keys of the open batch
	lookup        [][]any          // key values rows of the open batch named, read back before commit
	affected      int64
}

func newImportCapture(ctx context.Context, s *dbSession, database, table string) *importCapture {
	settings := safetySettings()
	if !settings.Enabled {
		return nil
	}
	c := &importCapture{s: s, settings: settings, database: database, table: sqlsafety.TableRef{Name: table}, qualified: table}
	switch {
	case !importKeyCapture(s):
		c.reason = sqlsafety.ReasonNotUndoableStore
		return c
	case s.safetyWithoutTransactions():
		c.reason = sqlsafety.ReasonNoTransaction
		return c
	}
	db, tbl, err := resolveSQLWriteTarget(ctx, s, database, table)
	if err != nil {
		c.reason = sqlsafety.ReasonNotUndoableStore
		return c
	}
	c.table = destructiveTableRef(s.dialect, db, tbl)
	if q, err := qualifiedTable(s.dialect, db, tbl); err == nil {
		c.qualified = q
	}
	info, err := getPrimaryKeys(ctx, s, db, tbl)
	if err != nil || info == nil || len(info.Columns) == 0 {
		c.reason = sqlsafety.ReasonNoPrimaryKey
		return c
	}
	c.pk = append([]string(nil), info.Columns...)
	for _, col := range c.pk {
		c.quotedPK = append(c.quotedPK, quoteColumn(s.dialect, col))
	}
	switch s.dialect {
	case "postgres", "sqlite":
		c.mode = importKeysReturning
	case "sqlserver":
		if !sqlServerTableHasTriggers(ctx, s, c.table) {
			c.mode = importKeysOutput
		}
	case "mysql":
		c.autoIncrement = len(c.pk) == 1 && mysqlAutoIncrement(ctx, s.db, c.table, c.pk[0])
	}
	return c
}

// sqlServerTableHasTriggers reports triggers on table; a failed lookup
// counts as having them (OUTPUT without INTO fails on such tables).
func sqlServerTableHasTriggers(ctx context.Context, s *dbSession, table sqlsafety.TableRef) bool {
	schema := table.Schema
	if strings.TrimSpace(schema) == "" {
		schema = "dbo"
	}
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sys.triggers tr
		JOIN sys.tables t ON t.object_id = tr.parent_id JOIN sys.schemas sc ON sc.schema_id = t.schema_id
		WHERE sc.name = @p1 AND t.name = @p2`, schema, table.Name).Scan(&n)
	return err != nil || n > 0
}

func (c *importCapture) fail(reason sqlsafety.Reason) {
	if c.reason == "" {
		c.reason = reason
	}
	c.keys, c.batch, c.lookup = nil, nil, nil
}

// insert runs one INSERT of row (columns/marks/values already rendered)
// in tx, capturing its key, and returns the rows it inserted.
func (c *importCapture) insert(ctx context.Context, tx *sql.Tx, qualified string, columns, marks []string, values []any, row map[string]any) (int64, error) {
	head := "INSERT INTO " + qualified + " (" + strings.Join(columns, ",") + ")"
	tail := " VALUES (" + strings.Join(marks, ",") + ")"
	if c != nil && c.reason == "" && c.mode != importKeysByValue {
		query := head + tail + " RETURNING " + strings.Join(c.quotedPK, ", ")
		if c.mode == importKeysOutput {
			output := make([]string, len(c.quotedPK))
			for i, q := range c.quotedPK {
				output[i] = "INSERTED." + q
			}
			query = head + " OUTPUT " + strings.Join(output, ", ") + tail
		}
		if c.probed {
			return c.queryKeys(ctx, tx, query, values)
		}
		// The first keyed insert of the import shows whether key capture
		// works at all (it can fail on privileges or an old server). If it
		// fails, the row is inserted plainly and the import goes on without
		// undo; a plain insert that fails too is the row's own error.
		c.probed = true
		n, captured, err := c.firstKeyed(ctx, tx, query, values)
		if err != nil || captured {
			return n, err
		}
		c.fail(sqlsafety.ReasonNotUndoableStore)
	}
	result, err := tx.ExecContext(ctx, head+tail, values...)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, errImportCount
	}
	if c == nil || c.reason != "" {
		return n, nil
	}
	if key, ok := c.rowKey(row); ok {
		c.lookup = append(c.lookup, key)
	} else if id, err := result.LastInsertId(); c.autoIncrement && n == 1 && err == nil && id > 0 {
		c.batch = append(c.batch, []any{id})
	} else {
		c.fail(sqlsafety.ReasonNotUndoableStore)
	}
	return n, nil
}

// errKeyCaptureUnavailable: the probe could not even open its savepoint;
// the batch transaction is unusable and nothing was written in it yet.
var errKeyCaptureUnavailable = errors.New("import key capture unavailable")

// importSavepoint opens the probe's savepoint where a failed statement would
// abort the transaction (Postgres) and reports whether it did. A variable
// so tests can make it fail.
var importSavepoint = func(ctx context.Context, tx *sql.Tx, dialect string) (bool, error) {
	if dialect != "postgres" {
		return false, nil
	}
	_, err := tx.ExecContext(ctx, "SAVEPOINT mincode_import_keys")
	return true, err
}

// firstKeyed runs the first keyed insert so that its failure leaves tx
// usable: inside a savepoint on Postgres, whose transaction a failed
// statement would abort (SQLite and SQL Server roll back just the
// statement). captured false with no error: the insert did not happen and
// should be retried without key capture.
func (c *importCapture) firstKeyed(ctx context.Context, tx *sql.Tx, query string, values []any) (int64, bool, error) {
	use, err := importSavepoint(ctx, tx, c.s.dialect)
	if err != nil {
		// Engines sharing the Postgres dialect may lack savepoints; the
		// failed statement aborted tx, so the batch starts over without keys.
		c.fail(sqlsafety.ReasonNotUndoableStore)
		return 0, false, errKeyCaptureUnavailable
	}
	if !use {
		n, err := c.queryKeys(ctx, tx, query, values)
		return n, err == nil, nil
	}
	n, err := c.queryKeys(ctx, tx, query, values)
	if err != nil {
		if _, rbErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT mincode_import_keys"); rbErr != nil {
			return 0, false, rbErr
		}
		return 0, false, nil
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT mincode_import_keys"); err != nil {
		return 0, false, err
	}
	return n, true, nil
}

// queryKeys runs an INSERT that returns its keys; the inserted row count is
// the number of keys. A key the store cannot keep drops undo, not the row.
func (c *importCapture) queryKeys(ctx context.Context, tx *sql.Tx, query string, values []any) (int64, error) {
	rows, err := tx.QueryContext(ctx, query, values...)
	if err != nil {
		return 0, err
	}
	keys, n, keyErr, err := scanKeyRows(rows)
	if err != nil {
		return 0, err
	}
	if keyErr {
		c.fail(sqlsafety.ReasonNotUndoableStore)
	} else if c.reason == "" {
		c.batch = append(c.batch, keys...)
	}
	return n, nil
}

// scanKeyRows reads and closes rows of key columns. keyErr reports a value
// the store cannot keep (the rows are still counted).
func scanKeyRows(rows *sql.Rows) (keys [][]any, n int64, keyErr bool, err error) {
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, 0, false, err
	}
	holders := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range holders {
		pointers[i] = &holders[i]
	}
	for rows.Next() {
		n++
		if err := rows.Scan(pointers...); err != nil {
			return nil, 0, false, err
		}
		key := make([]any, len(holders))
		for i, v := range holders {
			if key[i], err = snapshotValue(v); err != nil {
				keyErr = true
			}
		}
		keys = append(keys, key)
	}
	return keys, n, keyErr, rows.Err()
}

// rowKey returns the key values row names (as the INSERT bound them), or
// false when a key column is missing or null.
func (c *importCapture) rowKey(row map[string]any) ([]any, bool) {
	key := make([]any, len(c.pk))
	for i, col := range c.pk {
		v, ok := row[col]
		if !ok || v == nil {
			return nil, false
		}
		key[i] = jsonToSQLValue(v)
	}
	return key, true
}

// readBack reads the keys the open batch's rows named back from tx (as the
// database stores them), in batches bounded by key and parameter count.
// Any shortfall drops undo for the whole import.
func (c *importCapture) readBack(ctx context.Context, tx *sql.Tx) {
	if c == nil || c.reason != "" || len(c.lookup) == 0 {
		return
	}
	per := safetyKeysPerBatch(len(c.pk))
	ph := func(n int) string { return placeholder(c.s.dialect, n) }
	var found [][]any
	for lo := 0; lo < len(c.lookup); lo += per {
		chunk := c.lookup[lo:min(lo+per, len(c.lookup))]
		query := sqlsafety.ByKeysSQL(c.s.safetyFamily(), c.s.safetyQualify, c.s.safetyQuote, c.table, c.pk, c.pk, len(chunk), ph)
		args := make([]any, 0, len(chunk)*len(c.pk))
		for _, key := range chunk {
			args = append(args, key...)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			c.fail(sqlsafety.ReasonNotUndoableStore)
			return
		}
		keys, _, keyErr, err := scanKeyRows(rows)
		if err != nil || keyErr {
			c.fail(sqlsafety.ReasonNotUndoableStore)
			return
		}
		found = append(found, keys...)
	}
	if len(found) != len(c.lookup) {
		c.fail(sqlsafety.ReasonNotUndoableStore)
		return
	}
	c.batch = append(c.batch, found...)
	c.lookup = nil
}

// committed takes the open batch's keys once n rows committed.
func (c *importCapture) committed(n int64) {
	if c == nil {
		return
	}
	c.affected += n
	if c.reason == "" {
		c.keys = append(c.keys, c.batch...)
		if int64(len(c.keys)) > c.settings.MaxSnapshotRows {
			c.fail(sqlsafety.ReasonOverSnapshotLimit)
		}
	}
	c.batch, c.lookup = nil, nil
}

// rolledBack forgets the open batch's keys.
func (c *importCapture) rolledBack() {
	if c != nil {
		c.batch, c.lookup = nil, nil
	}
}

// save stores the import's record: undoable with the inserted keys when
// every committed row's key is known, else audit-only.
func (c *importCapture) save() {
	if c == nil || c.affected <= 0 {
		return
	}
	undoable := c.reason == "" && int64(len(c.keys)) == c.affected
	rec := sqlsafety.Record{ID: sqlsafety.NewRecordID(), ConnectionID: c.s.connectionID, DbType: c.s.dialect, Database: c.database,
		Table: c.table, Statement: importStatementText(c.qualified), Kind: sqlsafety.KindInsert, Source: "import", Affected: c.affected}
	var rows sqlsafety.Rows
	if undoable {
		rec.Undoable, rec.UndoState = true, "available"
		rec.PrimaryKey, rec.Columns = c.pk, c.pk
		rows.Inserted = c.keys
	} else {
		rec.UndoReason, rec.UndoState = c.reason, "none"
		if rec.UndoReason == "" {
			rec.UndoReason = sqlsafety.ReasonNotUndoableStore
		}
	}
	err := safetyRecords().Put(rec, rows)
	if errors.Is(err, sqlsafety.ErrOverStoreLimit) {
		rec.Undoable, rec.UndoReason, rec.UndoState, rec.PrimaryKey, rec.Columns = false, sqlsafety.ReasonOverSnapshotLimit, "none", nil, nil
		rows = sqlsafety.Rows{}
		err = safetyRecords().Put(rec, rows)
	}
	if err != nil {
		slog.Warn("sql safety import record not saved", "record", rec.ID, "error", err)
		return
	}
	slog.Info("sql safety import recorded", "record", rec.ID, "rows", c.affected, "keys", len(rows.Inserted))
}
