package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"mincode/app/backend/db/sqlsafety"
	"mincode/app/backend/storage"
	"mincode/app/backend/tasks"
)

// opCheck runs db_safety_check with an operation instead of a query.
func opCheck(t *testing.T, sessionID string, op map[string]any, database string) *sqlsafety.Check {
	t.Helper()
	c, err := opCheckErr(sessionID, op, database)
	if err != nil {
		t.Fatalf("db_safety_check %v: %v", op, err)
	}
	return c
}

func opCheckErr(sessionID string, op map[string]any, database string) (*sqlsafety.Check, error) {
	a := map[string]any{"sessionId": sessionID, "operation": op}
	if database != "" {
		a["database"] = database
	}
	raw, _ := json.Marshal(a)
	out, err := dbSafetyCheckHandler(context.Background(), "", string(raw))
	if err != nil {
		return nil, err
	}
	var c sqlsafety.Check
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func withToken(a map[string]any, checkID, confirm string) string {
	if checkID != "" {
		a["checkId"] = checkID
	}
	if confirm != "" {
		a["confirmText"] = confirm
	}
	raw, _ := json.Marshal(a)
	return string(raw)
}

func destructive(sessionID, action, database, table, confirmation, checkID, confirm string) error {
	a := map[string]any{"sessionId": sessionID, "action": action, "database": database, "confirmation": confirmation}
	if table != "" {
		a["table"] = table
	}
	_, err := dbDestructiveOperationHandler("", withToken(a, checkID, confirm))
	return err
}

func tableCount(t *testing.T, s *dbSession, table string) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func onlyRecord(t *testing.T, store *sqlsafety.Store) sqlsafety.Record {
	t.Helper()
	recs := listRecords(t, store)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want 1", recs)
	}
	return recs[0]
}

func TestDestructiveClearTableSnapshots(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	// A whole-table snapshot needs no primary key.
	c := opCheck(t, id, map[string]any{"kind": "destructive", "action": "clear_table", "table": "n"}, "main")
	sc := c.Statements[0]
	if c.Risk != sqlsafety.RiskHigh || !c.Undoable || c.ConfirmTarget != "n" || sc.Kind != sqlsafety.KindStructure ||
		sc.Estimate != (sqlsafety.Estimate{Rows: 2, Exact: true, Source: "count"}) {
		t.Fatalf("check = %+v", c)
	}
	if err := destructive(id, "clear_table", "main", "n", "main.n", c.CheckID, "n"); err != nil {
		t.Fatal(err)
	}
	if n := tableCount(t, s, "n"); n != 0 {
		t.Fatalf("n still has %d rows", n)
	}
	rec := onlyRecord(t, store)
	if rec.Kind != sqlsafety.KindStructure || !rec.Undoable || rec.Source != "destructive" || rec.Affected != 2 || rec.Table.Name != "n" {
		t.Fatalf("record = %+v", rec)
	}
	if res := mustUndo(t, id, rec.ID, "skipConflicts", ""); res.Restored != 2 {
		t.Fatalf("undo = %+v", res)
	}
	if n := tableCount(t, s, "n"); n != 2 {
		t.Fatalf("restored %d rows, want 2", n)
	}

	// DROP TABLE keeps the rows; undo needs the table back first.
	c = opCheck(t, id, map[string]any{"kind": "destructive", "action": "drop_table", "table": "t"}, "main")
	if !c.Undoable || c.Statements[0].Estimate.Rows != 1500 {
		t.Fatalf("drop check = %+v", c)
	}
	if err := destructive(id, "drop_table", "main", "t", "t", c.CheckID, "t"); err != nil {
		t.Fatal(err)
	}
	dropped := listRecords(t, store)[0]
	if dropped.Kind != sqlsafety.KindStructure || !dropped.Undoable || len(dropped.PrimaryKey) != 1 || dropped.Affected != 1500 {
		t.Fatalf("drop record = %+v", dropped)
	}
	_, err := undo(t, id, dropped.ID, "skipConflicts", "t")
	safetyErrorOf(t, err, "tableMissing")
	if _, err := s.db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, a INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if res := mustUndo(t, id, dropped.ID, "skipConflicts", "t"); res.Restored != 1500 {
		t.Fatalf("undo drop = %+v", res)
	}

	// Over the snapshot limit: still allowed after typing the name, but not undoable.
	useSafetySettings(t, sqlsafety.Settings{Enabled: true, HighRiskRows: 1000, RetentionDays: 7, MaxSnapshotRows: 1000, MaxTotalBytes: 2 << 30})
	c = opCheck(t, id, map[string]any{"kind": "destructive", "action": "clear_table", "table": "t"}, "main")
	if c.Undoable || !hasReason(c.Statements[0].Reasons, sqlsafety.ReasonOverSnapshotLimit) {
		t.Fatalf("over-limit check = %+v", c)
	}
}

func TestDestructiveRequiresCheck(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	op := map[string]any{"kind": "destructive", "action": "clear_table", "table": "n"}

	safetyErrorOf(t, destructive(id, "clear_table", "main", "n", "main.n", "", ""), "checkRequired")

	other := opCheck(t, id, op, "main")
	safetyErrorOf(t, destructive(id, "clear_table", "main", "t", "main.t", other.CheckID, "t"), "checkInvalid")

	c := opCheck(t, id, op, "main")
	safetyErrorOf(t, destructive(id, "clear_table", "main", "n", "main.n", c.CheckID, "t"), "confirmMismatch")
	safetyErrorOf(t, destructive(id, "clear_table", "main", "n", "main.n", c.CheckID, "n"), "checkInvalid") // spent

	c = opCheck(t, id, op, "main")
	if err := destructive(id, "clear_table", "main", "n", "main.t", c.CheckID, "n"); err == nil || !strings.Contains(err.Error(), "人工确认") {
		t.Fatalf("wrong confirmation: %v", err)
	}
	if n := tableCount(t, s, "n"); n != 2 {
		t.Fatalf("n has %d rows after refused operations", n)
	}

	// Database-wide operations are high risk, confirmed by the database name, never undoable.
	c = opCheck(t, id, map[string]any{"kind": "destructive", "action": "clear_database"}, "main")
	if c.Risk != sqlsafety.RiskHigh || c.Undoable || c.ConfirmTarget != "main" {
		t.Fatalf("clear database check = %+v", c)
	}
	if err := destructive(id, "clear_database", "main", "", "main", c.CheckID, "main"); err != nil {
		t.Fatal(err)
	}
	if n := tableCount(t, s, "t"); n != 0 {
		t.Fatalf("t has %d rows after clear database", n)
	}
}

func TestDestructiveSnapshotOverLimitReconfirms(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	op := map[string]any{"kind": "destructive", "action": "clear_table", "table": "n"}
	c := opCheck(t, id, op, "main")
	if !c.Undoable {
		t.Fatalf("check = %+v", c)
	}
	// The table outgrew the snapshot limit after the check: nothing runs.
	useSafetySettings(t, sqlsafety.Settings{Enabled: true, HighRiskRows: 1000, RetentionDays: 7, MaxSnapshotRows: 1, MaxTotalBytes: 2 << 30})
	w := safetyErrorOf(t, destructive(id, "clear_table", "main", "n", "main.n", c.CheckID, "n"), "reconfirm")
	if w.Check == nil || w.Check.Undoable || !hasReason(w.Check.Statements[0].Reasons, sqlsafety.ReasonOverSnapshotLimit) {
		t.Fatalf("reconfirm check = %+v", w.Check)
	}
	if n := tableCount(t, s, "n"); n != 2 || len(listRecords(t, store)) != 0 {
		t.Fatalf("after reconfirm: %d rows, records %+v", n, listRecords(t, store))
	}
	if err := destructive(id, "clear_table", "main", "n", "main.n", w.Check.CheckID, "n"); err != nil {
		t.Fatal(err)
	}
	if rec := onlyRecord(t, store); rec.Undoable || rec.UndoReason != sqlsafety.ReasonOverSnapshotLimit || tableCount(t, s, "n") != 0 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestWholeTableImplicitCommitBacksUpFirst(t *testing.T) {
	previous := wholeTableInTransaction
	wholeTableInTransaction = func(string) bool { return false }
	t.Cleanup(func() { wholeTableInTransaction = previous })
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	op := map[string]any{"kind": "destructive", "action": "clear_table", "table": "n"}

	// The backup cannot be saved: the statement never runs.
	useSafetyRecords(t, &failingStore{inner: store, err: errors.New("disk full")})
	c := opCheck(t, id, op, "main")
	if err := destructive(id, "clear_table", "main", "n", "main.n", c.CheckID, "n"); err == nil || !strings.Contains(err.Error(), "留底写入失败") {
		t.Fatalf("err = %v", err)
	}
	if n := tableCount(t, s, "n"); n != 2 {
		t.Fatalf("n has %d rows after a failed backup", n)
	}
	useSafetyRecords(t, store)

	// The statement fails after the backup was saved: the backup is dropped.
	if _, err := s.db.Exec(`CREATE TRIGGER keep_n BEFORE DELETE ON n BEGIN SELECT RAISE(ABORT, 'kept'); END`); err != nil {
		t.Fatal(err)
	}
	c = opCheck(t, id, op, "main")
	if err := destructive(id, "clear_table", "main", "n", "main.n", c.CheckID, "n"); err == nil {
		t.Fatal("clear with an aborting trigger succeeded")
	}
	if recs := listRecords(t, store); len(recs) != 0 || tableCount(t, s, "n") != 2 {
		t.Fatalf("records after failed statement = %+v", recs)
	}

	if _, err := s.db.Exec(`DROP TRIGGER keep_n`); err != nil {
		t.Fatal(err)
	}
	c = opCheck(t, id, op, "main")
	if err := destructive(id, "clear_table", "main", "n", "main.n", c.CheckID, "n"); err != nil {
		t.Fatal(err)
	}
	rec := onlyRecord(t, store)
	if !rec.Undoable || rec.Affected != 2 || rec.UndoState != "available" || tableCount(t, s, "n") != 0 {
		t.Fatalf("record = %+v", rec)
	}
	if !hasReason(c.Statements[0].Reasons, sqlsafety.ReasonNoTransaction) {
		t.Fatalf("implicit-commit check reasons = %v", c.Statements[0].Reasons)
	}
}

func TestWholeTableUndoDecision(t *testing.T) {
	cases := []struct {
		dialect, text         string
		partitioned, children bool
		undoable              bool
		reason                sqlsafety.Reason
	}{
		{"postgres", `TRUNCATE TABLE "public"."t"`, false, false, true, ""},
		{"postgres", `TRUNCATE TABLE "public"."t"`, false, true, false, sqlsafety.ReasonCascade},
		{"postgres", `TRUNCATE ONLY "public"."t"`, false, true, true, ""},
		{"postgres", `TRUNCATE t`, true, false, false, sqlsafety.ReasonCascade},
		{"postgres", `TRUNCATE t CASCADE`, false, false, false, sqlsafety.ReasonCascade},
		{"postgres", `DROP TABLE t CASCADE`, false, false, false, sqlsafety.ReasonCascade},
		{"sqlite", `DELETE FROM "t"`, false, false, true, ""},
		{"mysql", "TRUNCATE TABLE `d`.`t`", false, false, true, ""},
		{"oracle", `TRUNCATE TABLE "S"."T"`, false, false, true, sqlsafety.ReasonNoTransaction},
		{"dm", `DROP TABLE "S"."T"`, false, false, true, sqlsafety.ReasonNoTransaction},
	}
	for _, c := range cases {
		undoable, reasons := wholeTableUndo(c.dialect, c.text, c.partitioned, c.children)
		if undoable != c.undoable || (c.reason == "") != (len(reasons) == 0) || (c.reason != "" && !hasReason(reasons, c.reason)) {
			t.Errorf("%s %q partitioned=%v children=%v: %v %v", c.dialect, c.text, c.partitioned, c.children, undoable, reasons)
		}
	}
}

func TestPartialTruncateNotUndoable(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	// Anything after the table may remove only part of it (PARTITION lists).
	c := mustCheck(t, s, "DROP TABLE n PARTITION (p0)")
	if c.Undoable || !hasReason(c.Statements[0].Reasons, sqlsafety.ReasonUnparsed) {
		t.Fatalf("check = %+v", c)
	}
}

func TestImportKeyCaptureFailureFallsBack(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	if _, err := s.db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	c := newImportCapture(context.Background(), s, "main", "items")
	c.quotedPK = []string{"no_such_column"} // RETURNING fails, as without privilege (unquoted: SQLite reads "x" as a string)
	if _, err := runJournaledFixture(t, func(ctx context.Context, r *tasks.Reporter) (tasks.Result, error) {
		return tasks.Result{}, importSQLBatch(ctx, s, "main", "items", []map[string]any{{"id": "1"}, {"id": "2"}, {"name": "c"}}, r, c)
	}); err != nil {
		t.Fatal(err)
	}
	if n := tableCount(t, s, "items"); n != 3 {
		t.Fatalf("imported %d rows, want 3", n)
	}
	if c.reason != sqlsafety.ReasonNotUndoableStore || c.affected != 3 || c.keys != nil {
		t.Fatalf("capture = %+v", c)
	}
}

func TestQueryDropTableSnapshotsWholeTable(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	c := mustCheck(t, s, "DROP TABLE n")
	if !c.Undoable || c.Risk != sqlsafety.RiskHigh || c.Statements[0].Estimate.Rows != 2 {
		t.Fatalf("check = %+v", c)
	}
	if _, err := execute(t, id, "DROP TABLE n", c.CheckID, "n"); err != nil {
		t.Fatal(err)
	}
	rec := onlyRecord(t, store)
	if rec.Kind != sqlsafety.KindStructure || !rec.Undoable || rec.Source != "query" || len(rec.Columns) != 1 {
		t.Fatalf("record = %+v", rec)
	}
	if _, err := s.db.Exec(`CREATE TABLE n (a INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if res := mustUndo(t, id, rec.ID, "skipConflicts", ""); res.Restored != 2 || tableCount(t, s, "n") != 2 {
		t.Fatalf("undo = %+v", res)
	}
}

func TestWholeTableSnapshotSQL(t *testing.T) {
	table := sqlsafety.TableRef{Schema: "app", Name: "orders"}
	for dialect, want := range map[string]string{
		"postgres":  `SELECT * FROM ONLY "app"."orders"`,
		"sqlserver": `SELECT * FROM [app].[orders] WITH (TABLOCK, HOLDLOCK)`,
		"mysql":     "SELECT * FROM `app`.`orders`",
	} {
		if got := wholeTableSnapshotSQL(&dbSession{kind: kindSQL, dialect: dialect}, table); got != want {
			t.Errorf("%s: %q, want %q", dialect, got, want)
		}
	}
}

// useTransferManager gives the test its own transfer task manager.
func useTransferManager(t *testing.T) {
	t.Helper()
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
}

func importArgs(sessionID, table, data string) map[string]any {
	return map[string]any{"sessionId": sessionID, "database": "main", "table": table, "format": "json", "data": data}
}

func importOp(table, data string) map[string]any {
	return map[string]any{"kind": "import", "table": table, "format": "json", "data": data}
}

func TestImportRecordsInsertedKeys(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	useTransferManager(t)
	s := newSafetyTestSession(t)
	if _, err := s.db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	id := registerSafetySession(t, s)
	data := `[{"id":5,"name":"a"},{"name":"b"}]`

	_, err := dbImportDataHandler(context.Background(), "", withToken(importArgs(id, "items", data), "", ""))
	safetyErrorOf(t, err, "checkRequired")

	c := opCheck(t, id, importOp("items", data), "main")
	sc := c.Statements[0]
	if c.Risk != sqlsafety.RiskLow || sc.Kind != sqlsafety.KindInsert || sc.Estimate != (sqlsafety.Estimate{Rows: 2, Exact: true, Source: "count"}) {
		t.Fatalf("check = %+v", c)
	}
	// The token is bound to the data.
	_, err = dbImportDataHandler(context.Background(), "", withToken(importArgs(id, "items", `[{"id":7}]`), c.CheckID, ""))
	safetyErrorOf(t, err, "checkInvalid")

	c = opCheck(t, id, importOp("items", data), "main")
	if out, err := dbImportDataHandler(context.Background(), "", withToken(importArgs(id, "items", data), c.CheckID, "")); err != nil || out != "2" {
		t.Fatalf("import = %s, %v", out, err)
	}
	rec := onlyRecord(t, store)
	_, rows, err := store.Get(rec.ID)
	if err != nil || rec.Kind != sqlsafety.KindInsert || rec.Source != "import" || !rec.Undoable || rec.Affected != 2 || len(rows.Inserted) != 2 {
		t.Fatalf("record = %+v rows %+v, %v", rec, rows, err)
	}
	if res := mustUndo(t, id, rec.ID, "skipConflicts", ""); res.Restored != 2 || tableCount(t, s, "items") != 0 {
		t.Fatalf("undo = %+v", res)
	}

	// Without a primary key the import only warns and records it without undo.
	c = opCheck(t, id, importOp("n", `[{"a":9}]`), "main")
	if c.Undoable || !hasReason(c.Statements[0].Reasons, sqlsafety.ReasonNoPrimaryKey) {
		t.Fatalf("key-less check = %+v", c)
	}
	if _, err := dbImportDataHandler(context.Background(), "", withToken(importArgs(id, "n", `[{"a":9}]`), c.CheckID, "")); err != nil {
		t.Fatal(err)
	}
	if recs := listRecords(t, store); recs[0].Undoable || recs[0].UndoReason != sqlsafety.ReasonNoPrimaryKey {
		t.Fatalf("key-less record = %+v", recs[0])
	}
}

func TestImportStartRequiresCheck(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)
	useTransferManager(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	plan, err := prepareTableImport(context.Background(), importPrepareRequest{SessionID: id, Database: "main", Table: "n", Format: "json", Data: `[{"a":3},{"a":4},{"a":5}]`})
	if err != nil {
		t.Fatal(err)
	}
	start := func(checkID string) (string, error) {
		return dbDataImportStartHandler(context.Background(), "", withToken(map[string]any{"planId": plan, "idempotencyKey": "k1"}, checkID, ""))
	}
	_, err = start("")
	safetyErrorOf(t, err, "checkRequired")
	c := opCheck(t, id, map[string]any{"kind": "import", "planId": plan}, "")
	if c.Statements[0].Estimate.Rows != 3 || c.Database != "main" {
		t.Fatalf("plan check = %+v", c)
	}
	if _, err := start(c.CheckID); err != nil {
		t.Fatal(err)
	}
	// Repeating the same submission returns the task without a new token.
	if _, err := start(""); err != nil {
		t.Fatalf("idempotent repeat: %v", err)
	}
}

func TestSchemaSyncDropTableSnapshots(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	target := newSafetyTestSession(t)
	targetID := registerSafetySession(t, target)
	source := openSQLiteSession(t, filepath.Join(t.TempDir(), "source.db"))
	if _, err := source.db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, a INTEGER)`); err != nil {
		t.Fatal(err)
	}
	sourceID := registerSafetySession(t, source)
	comparison, err := compareSchemaSessions(context.Background(), source, "main", target, "main")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, change := range comparison.Changes {
		if change.Kind == "drop_table" && change.Table == "n" {
			ids = append(ids, change.ID)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("changes = %+v", comparison.Changes)
	}
	args := map[string]any{"sourceSessionId": sourceID, "sourceDatabase": "main", "targetSessionId": targetID, "targetDatabase": "main",
		"sourceFingerprint": comparison.Source.Fingerprint, "targetFingerprint": comparison.Target.Fingerprint, "selectedChangeIds": ids}
	_, err = dbExecuteSchemaSyncHandler("", withToken(args, "", ""))
	safetyErrorOf(t, err, "checkRequired")

	c := opCheck(t, targetID, map[string]any{"kind": "schemaSync", "sourceSessionId": sourceID, "sourceDatabase": "main",
		"sourceFingerprint": comparison.Source.Fingerprint, "targetFingerprint": comparison.Target.Fingerprint, "selectedChangeIds": ids}, "main")
	if len(c.Statements) != 1 || !c.Undoable || c.Risk != sqlsafety.RiskHigh || c.ConfirmTarget != "n" {
		t.Fatalf("check = %+v", c)
	}
	out, err := dbExecuteSchemaSyncHandler("", withToken(args, c.CheckID, "n"))
	if err != nil {
		t.Fatal(err)
	}
	var result SchemaSyncExecutionResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || !result.Completed {
		t.Fatalf("result = %s, %v", out, err)
	}
	rec := onlyRecord(t, store)
	if rec.Kind != sqlsafety.KindStructure || !rec.Undoable || rec.Affected != 2 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestRedisPatternDeleteEstimate(t *testing.T) {
	settings := sqlsafety.DefaultSettings()
	risk, reasons := redisRiskForPattern("user:*", 10, true, settings)
	if risk != sqlsafety.RiskLow || !hasReason(reasons, sqlsafety.ReasonNotUndoableStore) {
		t.Fatalf("small pattern = %s %v", risk, reasons)
	}
	risk, reasons = redisRiskForPattern("user:*", 5000, true, settings)
	if risk != sqlsafety.RiskHigh || !hasReason(reasons, sqlsafety.ReasonOverThreshold) {
		t.Fatalf("large pattern = %s %v", risk, reasons)
	}
	risk, reasons = redisRiskForPattern("user:*", 10, false, settings)
	if risk != sqlsafety.RiskLow || !hasReason(reasons, sqlsafety.ReasonEstimated) {
		t.Fatalf("timed-out scan = %s %v", risk, reasons)
	}
	if risk, _ := redisRiskForPattern("*", 3, true, settings); risk != sqlsafety.RiskHigh {
		t.Fatalf("match-all pattern = %s", risk)
	}
}

func TestRedisEditRequiresCheck(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	srv := startFakeRedisDBServer(t) // DBSIZE answers the selected database index
	s := &dbSession{kind: kindRedis, rdb: redis.NewClient(&redis.Options{Addr: srv.ln.Addr().String(), Protocol: 2, DisableIdentity: true, PoolSize: 1, MaxRetries: -1})}
	t.Cleanup(func() { _ = s.rdb.Close() })
	id := registerSafetySession(t, s)
	args := map[string]any{"sessionId": id, "dbIndex": 2, "key": "k"}

	_, err := redisDeleteKeyHandler("", withToken(args, "", ""))
	safetyErrorOf(t, err, "checkRequired")

	c := opCheck(t, id, map[string]any{"kind": "redis", "action": "delete", "dbIndex": 2, "keys": []string{"k"}}, "")
	sc := c.Statements[0]
	if c.Risk != sqlsafety.RiskLow || c.Undoable || sc.Estimate.Rows != 1 || !hasReason(sc.Reasons, sqlsafety.ReasonNotUndoableStore) {
		t.Fatalf("check = %+v", c)
	}
	// A token for another key or database index is refused.
	_, err = redisDeleteKeyHandler("", withToken(map[string]any{"sessionId": id, "dbIndex": 2, "key": "other"}, c.CheckID, ""))
	safetyErrorOf(t, err, "checkInvalid")

	c = opCheck(t, id, map[string]any{"kind": "redis", "action": "delete", "dbIndex": 2, "keys": []string{"k"}}, "")
	_, err = redisDeleteKeyHandler("", withToken(args, c.CheckID, ""))
	if err == nil || strings.HasPrefix(err.Error(), "sqlsafety:") {
		t.Fatalf("checked delete should pass the gate and reach the server (which refuses DEL), got %v", err)
	}

	c = opCheck(t, id, map[string]any{"kind": "redis", "action": "flushdb", "dbIndex": 7}, "")
	if c.Risk != sqlsafety.RiskHigh || c.ConfirmTarget != sqlsafety.ConfirmLiteral ||
		c.Statements[0].Estimate != (sqlsafety.Estimate{Rows: 7, Exact: true, Source: "count"}) {
		t.Fatalf("flushdb check = %+v", c)
	}
}

func TestNonSQLWriteRequiresCheck(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := &dbSession{kind: kindHTTP, httpKind: httpDatabricks}
	id := registerSafetySession(t, s)

	_, err := execute(t, id, "DELETE FROM sales.orders WHERE id = 1", "", "")
	safetyErrorOf(t, err, "checkRequired")
	if _, err := execute(t, id, "SELECT * FROM sales.orders", "", ""); err == nil || strings.HasPrefix(err.Error(), "sqlsafety:") {
		t.Fatalf("read should pass the gate, got %v", err)
	}

	c, err := checkArgs(id, "DELETE FROM sales.orders WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	sc := c.Statements[0]
	if c.Risk != sqlsafety.RiskHigh || c.Undoable || !hasReason(sc.Reasons, sqlsafety.ReasonNotUndoableStore) ||
		!hasReason(sc.Reasons, sqlsafety.ReasonNoEstimate) || c.ConfirmTarget != "orders" {
		t.Fatalf("check = %+v", c)
	}
	_, err = execute(t, id, "DELETE FROM sales.orders WHERE id = 1", c.CheckID, "x")
	safetyErrorOf(t, err, "confirmMismatch")
	c, _ = checkArgs(id, "DELETE FROM sales.orders WHERE id = 1")
	if _, err := execute(t, id, "DELETE FROM sales.orders WHERE id = 1", c.CheckID, "orders"); err == nil || strings.HasPrefix(err.Error(), "sqlsafety:") {
		t.Fatalf("checked write should pass the gate, got %v", err)
	}
}

func checkArgs(sessionID, query string) (*sqlsafety.Check, error) {
	raw, _ := json.Marshal(map[string]any{"sessionId": sessionID, "query": query})
	out, err := dbSafetyCheckHandler(context.Background(), "", string(raw))
	if err != nil {
		return nil, err
	}
	var c sqlsafety.Check
	return &c, json.Unmarshal([]byte(out), &c)
}

func TestNonSQLStatementKinds(t *testing.T) {
	cases := []struct {
		s     *dbSession
		query string
		want  sqlsafety.Kind
	}{
		{&dbSession{kind: kindMongo}, `{"collection":"c","filter":{}}`, sqlsafety.KindRead},
		{&dbSession{kind: kindHTTP, httpKind: httpElastic}, `{"index":"i"}`, sqlsafety.KindRead},
		{&dbSession{kind: kindHTTP, httpKind: httpInflux}, `from(bucket:"a") |> range(start: -1h)`, sqlsafety.KindRead},
		{&dbSession{kind: kindHTTP, httpKind: httpInflux}, `from(bucket:"a") |> to(bucket: "b")`, sqlsafety.KindData},
		{&dbSession{kind: kindHTTP, httpKind: httpInflux}, `from(bucket:"a") |> experimental.wideTo(bucket: "b")`, sqlsafety.KindData},
		{&dbSession{kind: kindHTTP, httpKind: httpInflux}, `from(bucket:"a") |> toString()`, sqlsafety.KindRead},
		{&dbSession{kind: kindNeo4j}, `MATCH (n) RETURN n.name // CREATE`, sqlsafety.KindRead},
		{&dbSession{kind: kindNeo4j}, `MATCH (n) WHERE n.note = 'DELETE' RETURN n`, sqlsafety.KindRead},
		{&dbSession{kind: kindNeo4j}, `CREATE (n:Person {name: 'a'})`, sqlsafety.KindInsert},
		{&dbSession{kind: kindNeo4j}, `MATCH (n) DETACH DELETE n`, sqlsafety.KindData},
		{&dbSession{kind: kindCQL}, `SELECT * FROM ks.t`, sqlsafety.KindRead},
		{&dbSession{kind: kindCQL}, `INSERT INTO ks.t (id) VALUES (1)`, sqlsafety.KindInsert},
		{&dbSession{kind: kindCQL}, `UPDATE ks.t SET a = 1 WHERE id = 1`, sqlsafety.KindData},
	}
	for _, c := range cases {
		statements := nonSQLStatements(c.s, c.query)
		kind := sqlsafety.KindRead
		for _, st := range statements {
			if st.Kind != sqlsafety.KindRead {
				kind = st.Kind
			}
		}
		if kind != c.want {
			t.Errorf("%q: kind %s, want %s", c.query, kind, c.want)
		}
	}
}

func TestImportCaptureReadsBackNamedKeys(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	if _, err := s.db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	// Dialects without RETURNING / OUTPUT read the keys rows named back before commit.
	c := newImportCapture(context.Background(), s, "main", "items")
	c.mode = importKeysByValue
	var afterNamed importCapture
	if _, err := runJournaledFixture(t, func(ctx context.Context, r *tasks.Reporter) (tasks.Result, error) {
		if err := importSQLBatch(ctx, s, "main", "items", []map[string]any{{"id": "7", "name": "a"}, {"id": "8"}}, r, c); err != nil {
			return tasks.Result{}, err
		}
		afterNamed = *c
		// A row without its key (and no auto-increment to report it) drops undo.
		return tasks.Result{}, importSQLBatch(ctx, s, "main", "items", []map[string]any{{"name": "b"}}, r, c)
	}); err != nil {
		t.Fatal(err)
	}
	if afterNamed.reason != "" || afterNamed.affected != 2 || len(afterNamed.keys) != 2 || afterNamed.keys[0][0] != int64(7) || afterNamed.keys[1][0] != int64(8) {
		t.Fatalf("capture = %+v", afterNamed)
	}
	if c.reason != sqlsafety.ReasonNotUndoableStore || c.keys != nil || c.affected != 3 {
		t.Fatalf("capture after key-less row = %+v", c)
	}
}

func TestImportSavepointFailureRedoesBatch(t *testing.T) {
	previous := importSavepoint
	importSavepoint = func(context.Context, *sql.Tx, string) (bool, error) {
		return true, errors.New("savepoints are not supported")
	}
	t.Cleanup(func() { importSavepoint = previous })
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	if _, err := s.db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	c := newImportCapture(context.Background(), s, "main", "items")
	if _, err := runJournaledFixture(t, func(ctx context.Context, r *tasks.Reporter) (tasks.Result, error) {
		return tasks.Result{}, importSQLBatch(ctx, s, "main", "items", []map[string]any{{"id": "1"}, {"id": "2"}, {"name": "c"}}, r, c)
	}); err != nil {
		t.Fatal(err)
	}
	if n := tableCount(t, s, "items"); n != 3 {
		t.Fatalf("imported %d rows, want 3", n)
	}
	if c.reason != sqlsafety.ReasonNotUndoableStore || c.affected != 3 || c.keys != nil {
		t.Fatalf("capture = %+v", c)
	}
}

// useImplicitCommitLock runs the implicit-commit whole-table path on SQLite
// with the given lock statements.
func useImplicitCommitLock(t *testing.T, lock, unlock string) {
	t.Helper()
	inTx, locks := wholeTableInTransaction, wholeTableLock
	wholeTableInTransaction = func(string) bool { return false }
	wholeTableLock = func(string, string) (string, string) { return lock, unlock }
	t.Cleanup(func() { wholeTableInTransaction, wholeTableLock = inTx, locks })
}

func TestWholeTableLockFailureContinuesWithWarning(t *testing.T) {
	useImplicitCommitLock(t, "LOCK TABLES nope WRITE", "UNLOCK TABLES")
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	c := opCheck(t, id, map[string]any{"kind": "destructive", "action": "clear_table", "table": "n"}, "main")
	if err := destructive(id, "clear_table", "main", "n", "main.n", c.CheckID, "n"); err != nil {
		t.Fatal(err)
	}
	rec := onlyRecord(t, store)
	if !rec.Undoable || rec.UndoState != "available" || rec.UndoReason != sqlsafety.ReasonNoTransaction || tableCount(t, s, "n") != 0 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestWholeTableUnlockFailureDiscardsConnection(t *testing.T) {
	useImplicitCommitLock(t, "SELECT 1", "UNLOCK nonsense")
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	c := opCheck(t, id, map[string]any{"kind": "destructive", "action": "clear_table", "table": "n"}, "main")
	if err := destructive(id, "clear_table", "main", "n", "main.n", c.CheckID, "n"); err != nil {
		t.Fatal(err)
	}
	if rec := onlyRecord(t, store); !rec.Undoable || rec.UndoReason != "" {
		t.Fatalf("record = %+v", rec)
	}
	// SQLite sessions hold one connection: discarded, the pool is empty.
	if open := s.db.Stats().OpenConnections; open != 0 {
		t.Fatalf("open connections = %d, want the possibly locked one discarded", open)
	}
}

func TestPostgresInheritanceLookupFailsClosed(t *testing.T) {
	base := newSafetyTestSession(t)
	// A Postgres-dialect session whose catalog lookup fails (SQLite has no pg_class).
	s := &dbSession{kind: kindSQL, dialect: "postgres", db: base.db}
	_, undoable, reasons := assessWholeTable(context.Background(), s, "public", sqlsafety.TableRef{Schema: "public", Name: "n"}, `TRUNCATE TABLE "public"."n"`)
	if undoable || !hasReason(reasons, sqlsafety.ReasonUnparsed) {
		t.Fatalf("undoable %v reasons %v", undoable, reasons)
	}
}

func TestImportSavepointFailureThenBeginFailureReturnsError(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	if _, err := s.db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	previous := importSavepoint
	importSavepoint = func(context.Context, *sql.Tx, string) (bool, error) {
		// The batch must start over, but no new transaction can be opened.
		_ = s.db.Close()
		return true, errors.New("savepoints are not supported")
	}
	t.Cleanup(func() { importSavepoint = previous })
	c := newImportCapture(context.Background(), s, "main", "items")
	var batchErr error
	panicked := false
	_, _ = runJournaledFixture(t, func(ctx context.Context, r *tasks.Reporter) (tasks.Result, error) {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		batchErr = importSQLBatch(ctx, s, "main", "items", []map[string]any{{"id": "1"}}, r, c)
		return tasks.Result{}, batchErr
	})
	if panicked {
		t.Fatal("importSQLBatch panicked when the batch transaction could not be reopened")
	}
	if batchErr == nil {
		t.Fatalf("batch error = %v, want the BeginTx failure", batchErr)
	}
}
