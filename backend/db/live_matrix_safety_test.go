package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/db/sqlsafety"
)

// SQL 安全网 steps of TestLiveDatabaseMatrix: every write goes through
// db_safety_check and presents its token, exactly like the desktop.
//
// For each SQL case: check (timed) → refused without a token → low-risk
// execute → undo → undo over a conflicting change → high-risk without
// confirmText refused → high-risk execute and undo → clear table and undo →
// drop table and undo. Optional per-case steps: inserted-key capture and
// import on an auto-key table, the read guard, the EXPLAIN fallback and a
// schema sync.

// liveSafetyCase holds the optional per-dialect safety steps of a liveCase.
type liveSafetyCase struct {
	// Qualified is the table as statements name it (default Table).
	Qualified string `json:"qualified"`
	// AutoSetup creates AutoTable, a table with a database-generated key and a
	// name column; AutoInsert inserts two rows into it without the key.
	AutoSetup  []string `json:"autoSetup"`
	AutoTable  string   `json:"autoTable"`
	AutoInsert string   `json:"autoInsert"`
	// AutoInsertRows is how many rows AutoInsert adds (default 2).
	AutoInsertRows int    `json:"autoInsertRows"`
	AutoDrop       string `json:"autoDrop"`
	// ReadProbe is a read the guard must run inside its read-only (or
	// rolled-back) transaction; the result must contain ReadProbeWant.
	ReadProbe     string `json:"readProbe"`
	ReadProbeWant string `json:"readProbeWant"`
	// WritingReadSetup creates what WritingRead needs: a statement the
	// classifier reads as a SELECT but that writes (a function with a side
	// effect). The read-only transaction must refuse it, the host must ask
	// for a write confirmation, and the confirmed run must write.
	WritingReadSetup []string `json:"writingReadSetup"`
	WritingRead      string   `json:"writingRead"`
	// SyncSource is a second database on the same server used as the
	// schema-sync source (MySQL); SyncSetup prepares both sides.
	SyncSource  string   `json:"syncSource"`
	SyncSetup   []string `json:"syncSetup"`
	SyncCleanup []string `json:"syncCleanup"`
}

// liveSafetyError decodes a "sqlsafety:{...}" error; ok is false for any other error.
func liveSafetyError(err error) (wireSafetyError, bool) {
	if err == nil {
		return wireSafetyError{}, false
	}
	msg := err.Error()
	i := strings.Index(msg, "sqlsafety:")
	if i < 0 {
		return wireSafetyError{}, false
	}
	var w wireSafetyError
	if json.Unmarshal([]byte(msg[i+len("sqlsafety:"):]), &w) != nil {
		return wireSafetyError{}, false
	}
	return w, true
}

func liveCheck(session, database, query string, op map[string]any) (*sqlsafety.Check, time.Duration, error) {
	a := map[string]any{"sessionId": session}
	if database != "" {
		a["database"] = database
	}
	if op != nil {
		a["operation"] = op
	} else {
		a["query"] = query
	}
	start := time.Now()
	raw, err := dispatchErr("db_safety_check", a)
	elapsed := time.Since(start)
	if err != nil {
		return nil, elapsed, err
	}
	var c sqlsafety.Check
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, elapsed, err
	}
	return &c, elapsed, nil
}

// liveGuarded runs cmd; when the safety net asks for a check it obtains one
// for op (or for args["query"] when op is nil) on the session the write
// targets (args["targetSessionId"] for a schema sync, else args["sessionId"])
// and runs cmd again with the token, confirming high risk with the check's
// confirmTarget.
func liveGuarded(cmd string, args map[string]any, database string, op map[string]any) (json.RawMessage, *sqlsafety.Check, error) {
	raw, err := dispatchErr(cmd, args)
	w, ok := liveSafetyError(err)
	if !ok || w.Code != safetyCodeCheckRequired {
		return raw, nil, err
	}
	c := w.Check
	if c == nil {
		query, _ := args["query"].(string)
		session, ok := args["targetSessionId"].(string)
		if !ok {
			session, _ = args["sessionId"].(string)
		}
		if c, _, err = liveCheck(session, database, query, op); err != nil {
			return nil, nil, err
		}
	}
	retry := clone(args)
	retry["checkId"] = c.CheckID
	retry["confirmText"] = c.ConfirmTarget
	raw, err = dispatchErr(cmd, retry)
	return raw, c, err
}

type liveSafety struct {
	t       *testing.T
	session string
	c       liveCase
	store   *sqlsafety.Store
	table   string // qualified table text
}

func (l *liveSafety) exec(query string) (json.RawMessage, error) {
	db := l.c.Database
	raw, _, err := liveGuarded("db_execute_query", map[string]any{"sessionId": l.session, "query": query, "database": &db}, l.c.Database, nil)
	return raw, err
}

func (l *liveSafety) run(query, checkID, confirm string) (json.RawMessage, error) {
	a := map[string]any{"sessionId": l.session, "query": query, "database": l.c.Database}
	if checkID != "" {
		a["checkId"] = checkID
	}
	if confirm != "" {
		a["confirmText"] = confirm
	}
	return dispatchErr("db_execute_query", a)
}

func (l *liveSafety) ids() map[string]bool {
	recs, err := l.store.List("", 0)
	if err != nil {
		l.t.Fatal(err)
	}
	out := map[string]bool{}
	for _, r := range recs {
		out[r.ID] = true
	}
	return out
}

// newRecords are the records saved since seen, newest first (undo backups excluded).
func (l *liveSafety) newRecords(seen map[string]bool) ([]sqlsafety.Record, error) {
	recs, err := l.store.List("", 0)
	if err != nil {
		return nil, err
	}
	var fresh []sqlsafety.Record
	for _, r := range recs {
		if !seen[r.ID] && r.Source != "undo" {
			fresh = append(fresh, r)
		}
	}
	return fresh, nil
}

// newRecord is the one record saved since seen (undo backups excluded).
func (l *liveSafety) newRecord(seen map[string]bool) (sqlsafety.Record, error) {
	fresh, err := l.newRecords(seen)
	if err != nil {
		return sqlsafety.Record{}, err
	}
	if len(fresh) != 1 {
		return sqlsafety.Record{}, fmt.Errorf("want one new record, got %d: %+v", len(fresh), fresh)
	}
	return fresh[0], nil
}

func (l *liveSafety) preview(id string) (safetyUndoPreview, error) {
	raw, err := dispatchErr("db_safety_undo_preview", map[string]any{"sessionId": l.session, "id": id})
	if err != nil {
		return safetyUndoPreview{}, err
	}
	var p safetyUndoPreview
	return p, json.Unmarshal(raw, &p)
}

// undo previews id and undoes it in mode.
func (l *liveSafety) undo(id, mode string) (safetyUndoPreview, safetyUndoResult, error) {
	p, err := l.preview(id)
	if err != nil {
		return p, safetyUndoResult{}, fmt.Errorf("preview: %w", err)
	}
	raw, err := dispatchErr("db_safety_undo", map[string]any{"sessionId": l.session, "id": id, "mode": mode,
		"previewToken": p.PreviewToken, "confirmText": l.c.Table})
	if err != nil {
		return p, safetyUndoResult{}, fmt.Errorf("undo: %w", err)
	}
	var res safetyUndoResult
	return p, res, json.Unmarshal(raw, &res)
}

// value reads name of the row with id 1.
func (l *liveSafety) value() (string, error) {
	raw, err := l.exec(fmt.Sprintf("SELECT name FROM %s WHERE id = 1", l.table))
	if err != nil {
		return "", err
	}
	var res QueryResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", err
	}
	if len(res.Rows) != 1 {
		return "", fmt.Errorf("rows = %v", res.Rows)
	}
	return fmt.Sprint(res.Rows[0][0]), nil
}

func (l *liveSafety) count(table string) (int64, error) {
	raw, err := l.exec("SELECT COUNT(*) FROM " + table)
	if err != nil {
		return 0, err
	}
	var res QueryResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, err
	}
	if len(res.Rows) != 1 {
		return 0, fmt.Errorf("rows = %v", res.Rows)
	}
	var n int64
	_, err = fmt.Sscan(fmt.Sprint(res.Rows[0][0]), &n)
	return n, err
}

func (l *liveSafety) wantValue(want string) error {
	got, err := l.value()
	if err == nil && got != want {
		err = fmt.Errorf("name = %q, want %q", got, want)
	}
	return err
}

func (l *liveSafety) wantCount(table string, want int64) error {
	got, err := l.count(table)
	if err == nil && got != want {
		err = fmt.Errorf("count(%s) = %d, want %d", table, got, want)
	}
	return err
}

// liveRowUndo undoes the single-row update the matrix just made (a grid
// edit records it) and makes the update again.
func liveRowUndo(l *liveSafety, seen map[string]bool, edit map[string]any) error {
	rec, err := l.newRecord(seen)
	if err != nil {
		return err
	}
	if rec.Kind != sqlsafety.KindRows || !rec.Undoable || rec.Affected != 1 {
		return fmt.Errorf("grid record = %+v", rec)
	}
	if _, res, err := l.undo(rec.ID, "skipConflicts"); err != nil || res.Restored != 1 {
		return fmt.Errorf("undo = %+v, %v", res, err)
	}
	if err := l.wantValue("初始 O'Brien"); err != nil {
		return err
	}
	_, err = dispatchErr("db_update_cell", edit)
	return err
}

// liveSafetySteps runs the safety-net steps; row id 1 holds "已更新 ✓" before and after.
func liveSafetySteps(t *testing.T, l *liveSafety, mark func(string, error) bool) {
	const original = "已更新 ✓"
	update := fmt.Sprintf("UPDATE %s SET name = 'safe' WHERE id = 1", l.table)

	// check: a single-row UPDATE is low risk, undoable, estimated by COUNT.
	c, took, err := liveCheck(l.session, l.c.Database, update, nil)
	if err == nil {
		sc := c.Statements[0]
		if c.Risk != sqlsafety.RiskLow || !c.Undoable || sc.Estimate.Rows != 1 || sc.Estimate.Source != "count" {
			err = fmt.Errorf("check = %+v", c)
		}
	}
	t.Logf("db_safety_check single-row UPDATE: %v", took.Round(100*time.Microsecond))
	mark("check", err)

	// gate: no token, no write.
	_, err = l.run(update, "", "")
	if w, ok := liveSafetyError(err); !ok || w.Code != safetyCodeCheckRequired {
		err = fmt.Errorf("without token: %v", err)
	} else {
		err = l.wantValue(original)
	}
	mark("gate", err)

	// exec + undo.
	seen := l.ids()
	var rec sqlsafety.Record
	if c != nil {
		_, err = l.run(update, c.CheckID, "")
	}
	if err == nil {
		err = l.wantValue("safe")
	}
	if err == nil {
		rec, err = l.newRecord(seen)
	}
	if err == nil && (!rec.Undoable || rec.Affected != 1 || rec.Kind != sqlsafety.KindData) {
		err = fmt.Errorf("record = %+v", rec)
	}
	if mark("exec", err) {
		p, res, err := l.undo(rec.ID, "skipConflicts")
		if err == nil && (p.ConflictCount != 0 || res.Restored != 1) {
			err = fmt.Errorf("preview %+v undo %+v", p, res)
		}
		if err == nil {
			err = l.wantValue(original)
		}
		mark("undo", err)
	}

	// conflict: the row changed again after the recorded write.
	err = liveConflict(l, original)
	mark("conflict", err)

	// high risk: whole-table DELETE needs the table name typed.
	seen = l.ids()
	err = l.seedRows()
	if err == nil {
		err = l.wantCount(l.table, 3)
	}
	if err == nil {
		var recs []sqlsafety.Record
		if recs, err = l.newRecords(seen); err == nil && len(recs) > 0 {
			rec = recs[0]
			t.Logf("seed INSERT record: kind=%s undoable=%v reason=%s affected=%d (%d records)", rec.Kind, rec.Undoable, rec.UndoReason, rec.Affected, len(recs))
		}
	}
	mark("seed", err)
	deleteAll := "DELETE FROM " + l.table
	c, _, err = liveCheck(l.session, l.c.Database, deleteAll, nil)
	if err == nil && (c.Risk != sqlsafety.RiskHigh || !c.Undoable || !sqlsafety.ConfirmTargetMatches(c.ConfirmTarget, l.c.Table)) {
		err = fmt.Errorf("check = %+v", c)
	}
	if err == nil {
		_, err = l.run(deleteAll, c.CheckID, "")
		if w, ok := liveSafetyError(err); !ok || w.Code != safetyCodeConfirmMismatch {
			err = fmt.Errorf("without confirmText: %v", err)
		} else {
			err = l.wantCount(l.table, 3)
		}
	}
	mark("high-reject", err)
	seen = l.ids()
	c, _, err = liveCheck(l.session, l.c.Database, deleteAll, nil)
	if err == nil {
		_, err = l.run(deleteAll, c.CheckID, c.ConfirmTarget)
	}
	if err == nil {
		err = l.wantCount(l.table, 0)
	}
	if err == nil {
		rec, err = l.newRecord(seen)
	}
	if err == nil {
		var res safetyUndoResult
		if _, res, err = l.undo(rec.ID, "skipConflicts"); err == nil && res.Restored != 3 {
			err = fmt.Errorf("undo = %+v", res)
		}
	}
	if err == nil {
		err = l.wantCount(l.table, 3)
	}
	mark("high-undo", err)

	mark("truncate", liveDestructive(l, "clear_table", nil))
	mark("drop", liveDestructive(l, "drop_table", func() error { _, err := l.exec(l.c.CreateSQL); return err }))
	if _, err := l.exec(fmt.Sprintf("DELETE FROM %s WHERE id <> 1", l.table)); err != nil {
		t.Errorf("cleanup: %v", err)
	}

	if l.c.AutoTable != "" {
		insertErr, importErr := liveAutoKeys(t, l)
		mark("insert-keys", insertErr)
		mark("import", importErr)
	}
	if l.c.ReadProbe != "" {
		raw, err := l.run(l.c.ReadProbe, "", "")
		if err == nil && !strings.Contains(string(raw), l.c.ReadProbeWant) {
			err = fmt.Errorf("read probe: %.200s", raw)
		}
		mark("read-guard", err)
	}
	if l.c.WritingRead != "" {
		mark("force-write", liveWritingRead(l))
	}
	mark("explain", liveExplain(t, l))
	if l.c.SyncSource != "" {
		mark("schema-sync", liveSchemaSync(t, l))
	}
}

func liveConflict(l *liveSafety, original string) error {
	seen := l.ids()
	if _, err := l.exec(fmt.Sprintf("UPDATE %s SET name = 'v1' WHERE id = 1", l.table)); err != nil {
		return err
	}
	first, err := l.newRecord(seen)
	if err != nil {
		return err
	}
	if _, err := l.exec(fmt.Sprintf("UPDATE %s SET name = 'v2' WHERE id = 1", l.table)); err != nil {
		return err
	}
	p, res, err := l.undo(first.ID, "skipConflicts")
	if err != nil {
		return err
	}
	if p.ConflictCount != 1 || res.Restored != 0 || res.Skipped != 1 {
		return fmt.Errorf("skip: preview %+v undo %+v", p, res)
	}
	if err := l.wantValue("v2"); err != nil {
		return err
	}
	// Overwrite writes the recorded value back over the newer one.
	p, res, err = l.undo(first.ID, "overwrite")
	if err != nil {
		return fmt.Errorf("overwrite after skip: %w", err)
	}
	if res.Restored != 1 {
		return fmt.Errorf("overwrite: preview %+v undo %+v", p, res)
	}
	return l.wantValue(original)
}

// liveDestructive runs a whole-table action on the case table holding three
// rows through its check and undoes it (after recreate, for drop_table).
func liveDestructive(l *liveSafety, action string, recreate func() error) error {
	op := map[string]any{"kind": "destructive", "action": action, "table": l.c.Table}
	c, _, err := liveCheck(l.session, l.c.Database, "", op)
	if err != nil {
		return err
	}
	if c.Risk != sqlsafety.RiskHigh || !c.Undoable || c.Statements[0].Estimate.Rows != 3 {
		return fmt.Errorf("check = %+v", c)
	}
	seen := l.ids()
	if _, err := dispatchErr("db_destructive_operation", map[string]any{"sessionId": l.session, "action": action,
		"database": l.c.Database, "table": l.c.Table, "confirmation": l.c.Database + "." + l.c.Table,
		"checkId": c.CheckID, "confirmText": c.ConfirmTarget}); err != nil {
		return err
	}
	rec, err := l.newRecord(seen)
	if err != nil {
		return err
	}
	if !rec.Undoable || rec.Affected != 3 || rec.Source != "destructive" {
		return fmt.Errorf("record = %+v", rec)
	}
	if rec.Table.Schema != "" && !strings.EqualFold(rec.Table.Schema, l.c.Database) {
		return fmt.Errorf("record table %+v, want schema %s", rec.Table, l.c.Database)
	}
	if recreate != nil {
		if err := recreate(); err != nil {
			return fmt.Errorf("recreate: %w", err)
		}
	} else if err := l.wantCount(l.table, 0); err != nil {
		return err
	}
	if _, res, err := l.undo(rec.ID, "skipConflicts"); err != nil || res.Restored != 3 {
		return fmt.Errorf("undo = %+v, %v", res, err)
	}
	return l.wantCount(l.table, 3)
}

// liveAutoKeys inserts into the auto-key table through the query window and
// through an import; both must record the generated keys and undo must
// delete exactly those rows.
func liveAutoKeys(t *testing.T, l *liveSafety) (insertErr, importErr error) {
	c := l.c
	for _, s := range c.AutoSetup {
		if _, err := l.exec(s); err != nil {
			return fmt.Errorf("setup: %w", err), fmt.Errorf("setup failed")
		}
	}
	defer func() {
		if c.AutoDrop != "" {
			_, _ = l.exec(c.AutoDrop)
		}
	}()
	undoInserted := func(seen map[string]bool, want int) error {
		before, err := l.count(c.AutoTable)
		if err != nil {
			return err
		}
		rec, err := l.newRecord(seen)
		if err != nil {
			return err
		}
		if !rec.Undoable || rec.Kind != sqlsafety.KindInsert || rec.Affected != int64(want) {
			return fmt.Errorf("record = %+v", rec)
		}
		_, rows, err := l.store.Get(rec.ID)
		if err != nil || len(rows.Inserted) != want {
			return fmt.Errorf("inserted keys %v, %v", rows.Inserted, err)
		}
		t.Logf("%s inserted keys: %v", rec.Source, rows.Inserted)
		if _, res, err := l.undo(rec.ID, "skipConflicts"); err != nil || res.Restored != want {
			return fmt.Errorf("undo = %+v, %v", res, err)
		}
		return l.wantCount(c.AutoTable, before-int64(want))
	}

	seen := l.ids()
	rows := c.AutoInsertRows
	if rows == 0 {
		rows = 2
	}
	if _, insertErr = l.exec(c.AutoInsert); insertErr == nil {
		insertErr = undoInserted(seen, rows)
	}

	data := `[{"name":"i1"},{"name":"i2"}]`
	table := c.AutoTable
	if i := strings.LastIndex(table, "."); i >= 0 {
		table = table[i+1:]
	}
	seen = l.ids()
	args := map[string]any{"sessionId": l.session, "database": c.Database, "table": table, "format": "json", "data": data}
	op := map[string]any{"kind": "import", "table": table, "format": "json", "data": data}
	if _, _, importErr = liveGuarded("db_import_data", args, c.Database, op); importErr == nil {
		importErr = undoInserted(seen, 2)
	}
	return insertErr, importErr
}

// liveWritingRead: a SELECT that writes is refused by the read-only
// transaction, answered with a force-write check, and writes once confirmed.
func liveWritingRead(l *liveSafety) error {
	for _, s := range l.c.WritingReadSetup {
		if _, err := l.exec(s); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
	}
	defer func() { _, _ = l.exec(fmt.Sprintf("DELETE FROM %s WHERE id = 99", l.table)) }()
	_, err := l.run(l.c.WritingRead, "", "")
	w, ok := liveSafetyError(err)
	if !ok || w.Code != safetyCodeCheckRequired || w.Check == nil {
		return fmt.Errorf("writing read: %v", err)
	}
	if n, err := l.count(l.table + " WHERE id = 99"); err != nil || n != 0 {
		return fmt.Errorf("read-only tx let the write through: %d %v", n, err)
	}
	if _, err := l.run(l.c.WritingRead, w.Check.CheckID, w.Check.ConfirmTarget); err != nil {
		return fmt.Errorf("force write: %w", err)
	}
	return l.wantCount(l.table+" WHERE id = 99", 1)
}

// liveExplain reports what the EXPLAIN fallback (used when COUNT fails)
// yields on this server; it never fails the case, a failed EXPLAIN only
// means "no estimate" (high risk).
func liveExplain(t *testing.T, l *liveSafety) error {
	s, release, err := requireSession(l.session)
	if err != nil {
		return err
	}
	defer release()
	start := time.Now()
	n, ok := explainEstimate(context.Background(), s, s.safetyFamily(), l.c.Database, fmt.Sprintf("UPDATE %s SET name = 'x' WHERE id = 1", l.table))
	t.Logf("EXPLAIN fallback: rows=%d ok=%v in %v (ok=false → Source none)", n, ok, time.Since(start).Round(time.Millisecond))
	return nil
}

// liveSchemaSync applies an add-column and a drop-table change from
// SyncSource to the case database and undoes the drop.
func liveSchemaSync(t *testing.T, l *liveSafety) error {
	c := l.c
	for _, s := range c.SyncSetup {
		if _, err := l.exec(s); err != nil {
			return fmt.Errorf("setup %q: %w", s, err)
		}
	}
	defer func() {
		for _, s := range c.SyncCleanup {
			_, _ = l.exec(s)
		}
	}()
	srcCfg := c.Config
	srcDB := c.SyncSource
	srcCfg.Database = &srcDB
	srcCfg.ConnectionID = ""
	raw, err := dispatchErr("db_connect", map[string]any{"config": srcCfg})
	if err != nil {
		return err
	}
	var source string
	_ = json.Unmarshal(raw, &source)
	defer func() { _, _ = dispatchErr("db_disconnect", map[string]any{"sessionId": source}) }()

	compare := map[string]any{"sourceSessionId": source, "sourceDatabase": c.SyncSource, "targetSessionId": l.session, "targetDatabase": c.Database}
	raw, err = dispatchErr("db_compare_schema", compare)
	if err != nil {
		return err
	}
	var cmp SchemaComparison
	if err := json.Unmarshal(raw, &cmp); err != nil {
		return err
	}
	var ids []string
	for _, ch := range cmp.Changes {
		if (ch.Kind == "drop_table" && ch.Table == "mc_sync_gone") || (ch.Table == "mc_sync" && strings.Contains(ch.Kind, "column")) {
			ids = append(ids, ch.ID)
		}
	}
	if len(ids) != 2 {
		return fmt.Errorf("changes = %+v", cmp.Changes)
	}
	args := clone(compare)
	args["sourceFingerprint"], args["targetFingerprint"], args["selectedChangeIds"] = cmp.Source.Fingerprint, cmp.Target.Fingerprint, ids
	op := map[string]any{"kind": "schemaSync", "sourceSessionId": source, "sourceDatabase": c.SyncSource,
		"sourceFingerprint": cmp.Source.Fingerprint, "targetFingerprint": cmp.Target.Fingerprint, "selectedChangeIds": ids}
	seen := l.ids()
	raw, check, err := liveGuarded("db_execute_schema_sync", args, c.Database, op)
	if err != nil {
		return err
	}
	var result SchemaSyncExecutionResult
	if err := json.Unmarshal(raw, &result); err != nil || !result.Completed {
		return fmt.Errorf("result = %s, %v", raw, err)
	}
	if check == nil || check.Risk != sqlsafety.RiskHigh {
		return fmt.Errorf("check = %+v", check)
	}
	if _, err := l.exec("SELECT extra FROM mc_sync"); err != nil {
		return fmt.Errorf("added column: %w", err)
	}
	// One record per statement: the ALTER is recorded not undoable, the
	// DROP TABLE keeps its rows.
	recs, err := l.newRecords(seen)
	if err != nil {
		return err
	}
	if len(recs) != 2 || recs[1].Undoable || recs[1].UndoReason != sqlsafety.ReasonStructure {
		return fmt.Errorf("records = %+v", recs)
	}
	rec := recs[0]
	if !rec.Undoable || rec.Affected != 2 || rec.Table.Name != "mc_sync_gone" || rec.Table.Schema != c.Database {
		return fmt.Errorf("record = %+v", rec)
	}
	if _, err := l.exec("CREATE TABLE mc_sync_gone (id INT PRIMARY KEY)"); err != nil {
		return err
	}
	if _, res, err := l.undo(rec.ID, "skipConflicts"); err != nil || res.Restored != 2 {
		return fmt.Errorf("undo = %+v, %v", res, err)
	}
	return l.wantCount("mc_sync_gone", 2)
}
