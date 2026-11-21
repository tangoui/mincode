package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/db/sqlsafety"
)

// useSafetyStore points the backup store at a temp dir (and the machine key
// at a temp profile) for one test.
func useSafetyStore(t *testing.T) *sqlsafety.Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("APPDATA", filepath.Join(home, "data"))
	store := sqlsafety.OpenStore(t.TempDir(), time.Now)
	useSafetyRecords(t, store)
	return store
}

func useSafetyRecords(t *testing.T, store safetyRecordStore) {
	t.Helper()
	restore := setSafetyRecords(store)
	t.Cleanup(restore)
}

// failingStore refuses every record that carries rows (or every record
// when all is set) with err.
type failingStore struct {
	inner safetyRecordStore
	err   error
	all   bool
}

func (f *failingStore) Put(rec sqlsafety.Record, rows sqlsafety.Rows) error {
	if f.all || len(rows.Before)+len(rows.After)+len(rows.Inserted) > 0 {
		return f.err
	}
	return f.inner.Put(rec, rows)
}

func (f *failingStore) Delete(id string) error { return f.inner.Delete(id) }

func (f *failingStore) MarkUndone(id, state string) error { return f.inner.MarkUndone(id, state) }

// registerSafetySession registers s so handlers can find it and checks bind to its ID.
func registerSafetySession(t *testing.T, s *dbSession) string {
	t.Helper()
	id := "safety-" + sqlsafety.NewRecordID()
	s.connectionID = "conn-1"
	registry.put(id, s)
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.sessions, id)
		delete(registry.timestamps, id)
		registry.mu.Unlock()
	})
	return id
}

func execArgs(sessionID, query, checkID, confirm string) string {
	a := map[string]any{"sessionId": sessionID, "query": query}
	if checkID != "" {
		a["checkId"] = checkID
	}
	if confirm != "" {
		a["confirmText"] = confirm
	}
	b, _ := json.Marshal(a)
	return string(b)
}

func execute(t *testing.T, sessionID, query, checkID, confirm string) (*QueryResult, error) {
	t.Helper()
	out, err := dbExecuteQueryHandler(context.Background(), "", execArgs(sessionID, query, checkID, confirm))
	if err != nil {
		return nil, err
	}
	var res QueryResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode result %q: %v", out, err)
	}
	return &res, nil
}

type wireSafetyError struct {
	Code        string           `json:"code"`
	Check       *sqlsafety.Check `json:"check"`
	Applied     *int             `json:"applied"`
	FailedIndex *int             `json:"failedIndex"`
	Message     string           `json:"message"`
}

func safetyErrorOf(t *testing.T, err error, code string) wireSafetyError {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want sqlsafety %s", code)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "sqlsafety:") {
		t.Fatalf("err = %v, want sqlsafety %s", err, code)
	}
	var w wireSafetyError
	if err := json.Unmarshal([]byte(strings.TrimPrefix(msg, "sqlsafety:")), &w); err != nil {
		t.Fatalf("decode %q: %v", msg, err)
	}
	if w.Code != code {
		t.Fatalf("code = %q (%s), want %q", w.Code, msg, code)
	}
	return w
}

func countWhere(t *testing.T, s *dbSession, where string) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM t WHERE " + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func listRecords(t *testing.T, store *sqlsafety.Store) []sqlsafety.Record {
	t.Helper()
	recs, err := store.List("", 0)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestGuardedExecuteSnapshotsAndCommits(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	query := "UPDATE t SET a=1 WHERE id <= 10; SELECT a FROM t WHERE id = 1"
	c := mustCheck(t, s, query)
	res, err := execute(t, id, query, c.CheckID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != float64(1) {
		t.Fatalf("last result = %+v, want the SELECT's row", res)
	}
	if n := countWhere(t, s, "a = 1"); n != 10 {
		t.Fatalf("updated rows = %d, want 10", n)
	}
	recs := listRecords(t, store)
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	rec, rows, err := store.Get(recs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Undoable || rec.UndoState != "available" || rec.Affected != 10 || rec.Kind != sqlsafety.KindData ||
		rec.ConnectionID != "conn-1" || rec.Source != "query" || rec.DbType != "sqlite" || rec.Table.Name != "t" ||
		rec.Statement != "UPDATE t SET a=1 WHERE id <= 10" || strings.Join(rec.PrimaryKey, ",") != "id" || strings.Join(rec.Columns, ",") != "id,a" {
		t.Fatalf("record = %+v", rec)
	}
	if len(rows.Before) != 10 || len(rows.After) != 10 {
		t.Fatalf("rows before %d after %d, want 10/10", len(rows.Before), len(rows.After))
	}
	for i := range rows.Before {
		if rows.Before[i][1] != int64(0) || rows.After[i][1] != int64(1) {
			t.Fatalf("row %d before %v after %v", i, rows.Before[i], rows.After[i])
		}
	}
}

func TestGuardedExecuteReconfirmRollsBack(t *testing.T) {
	settings := sqlsafety.DefaultSettings()
	settings.HighRiskRows = 5
	useSafetySettings(t, settings)
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	query := "UPDATE t SET a=9 WHERE id<=3"
	c := mustCheck(t, s, query)
	if c.Risk != sqlsafety.RiskLow {
		t.Fatalf("check risk = %s, want low", c.Risk)
	}
	if _, err := s.db.Exec("INSERT INTO t (id, a) VALUES (0, 0), (-1, 0), (-2, 0), (-3, 0), (-4, 0)"); err != nil {
		t.Fatal(err)
	}
	_, err := execute(t, id, query, c.CheckID, "")
	w := safetyErrorOf(t, err, "reconfirm")
	if n := countWhere(t, s, "a = 9"); n != 0 {
		t.Fatalf("rows changed despite reconfirm: %d", n)
	}
	if recs := listRecords(t, store); len(recs) != 0 {
		t.Fatalf("records = %d, want 0", len(recs))
	}
	nc := w.Check
	if nc == nil || nc.CheckID == "" || nc.CheckID == c.CheckID || nc.Risk != sqlsafety.RiskHigh || nc.ConfirmTarget != "t" ||
		nc.Statements[0].Estimate.Rows != 8 || !hasReason(nc.Statements[0].Reasons, sqlsafety.ReasonOverThreshold) || !nc.Undoable {
		t.Fatalf("reconfirm check = %+v", nc)
	}

	// The re-issued check runs the same query once the table name is typed.
	if _, err := execute(t, id, query, nc.CheckID, "t"); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, s, "a = 9"); n != 8 {
		t.Fatalf("rows updated after reconfirm = %d, want 8", n)
	}
	if recs := listRecords(t, store); len(recs) != 1 || !recs[0].Undoable {
		t.Fatalf("records after reconfirm = %+v", recs)
	}
}

func TestExecuteRequiresCheck(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	query := "UPDATE t SET a=2 WHERE id = 1"
	_, err := execute(t, id, query, "", "")
	safetyErrorOf(t, err, "checkRequired")

	c := mustCheck(t, s, query)
	if _, err := execute(t, id, "UPDATE t SET a=3 WHERE id = 1", c.CheckID, ""); err == nil {
		t.Fatal("a token for another query was accepted")
	} else {
		safetyErrorOf(t, err, "checkInvalid")
	}
	if _, err := execute(t, id, query, c.CheckID, ""); err != nil {
		t.Fatal(err)
	}
	_, err = execute(t, id, query, c.CheckID, "")
	safetyErrorOf(t, err, "checkInvalid")

	c = mustCheck(t, s, "DELETE FROM t")
	_, err = execute(t, id, "DELETE FROM t", c.CheckID, "wrong")
	safetyErrorOf(t, err, "confirmMismatch")
	if n := countRows(t, s, "t"); n != 1500 {
		t.Fatalf("rows left = %d, want 1500", n)
	}

	res, err := execute(t, id, "SELECT a FROM t WHERE id = 1", "", "")
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0] != float64(2) {
		t.Fatalf("select = %+v, %v", res, err)
	}

	// Streaming: reads stream without a token, writes need one.
	stream := func(query, checkID string) error {
		b, _ := json.Marshal(map[string]any{"sessionId": id, "query": query, "queryId": "q-" + sqlsafety.NewRecordID(), "checkId": checkID})
		_, err := dbExecuteQueryStreamHandler(context.Background(), "", string(b))
		return err
	}
	if err := stream("SELECT a FROM t", ""); err != nil {
		t.Fatal(err)
	}
	safetyErrorOf(t, stream(query, ""), "checkRequired")
	c = mustCheck(t, s, "UPDATE t SET a=4 WHERE id = 1")
	if err := stream("UPDATE t SET a=4 WHERE id = 1", c.CheckID); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, s, "a = 4"); n != 1 {
		t.Fatalf("streamed update rows = %d", n)
	}
}

func TestExecuteFailsClosedWithoutPlan(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	query := "UPDATE t SET a=2 WHERE id = 1"
	c := mustCheck(t, s, query)
	takeSafetyPlan(c.CheckID) // evicted
	_, err := execute(t, id, query, c.CheckID, "")
	safetyErrorOf(t, err, "checkInvalid")
	if n := countWhere(t, s, "a = 2"); n != 0 {
		t.Fatalf("rows changed without a plan: %d", n)
	}
}

func TestPerStatementStopsOnError(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	query := "UPDATE t SET a=1 WHERE id=1; UPDATE nope SET a=1; UPDATE t SET a=1 WHERE id=2"
	c := mustCheck(t, s, query)
	_, err := execute(t, id, query, c.CheckID, c.ConfirmTarget)
	w := safetyErrorOf(t, err, "partial")
	if w.Applied == nil || *w.Applied != 1 || w.FailedIndex == nil || *w.FailedIndex != 1 || w.Message == "" {
		t.Fatalf("partial = %+v", w)
	}
	if countWhere(t, s, "id = 1 AND a = 1") != 1 || countWhere(t, s, "id = 2 AND a = 1") != 0 {
		t.Fatal("statement 1 must be committed and statement 3 never run")
	}
	if recs := listRecords(t, store); len(recs) != 1 || !recs[0].Undoable {
		t.Fatalf("records = %+v, want the first statement's", recs)
	}
}

func TestSnapshotWriteFailureRollsBack(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	useSafetyRecords(t, &failingStore{inner: store, err: errors.New("disk full")})
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	query := "UPDATE t SET a=5 WHERE id <= 10"
	c := mustCheck(t, s, query)
	if _, err := execute(t, id, query, c.CheckID, ""); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want the store failure", err)
	}
	if n := countWhere(t, s, "a = 5"); n != 0 {
		t.Fatalf("rows changed without a backup: %d", n)
	}
}

func TestStoreOverLimitAsksToRunWithoutUndo(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	useSafetyRecords(t, &failingStore{inner: store, err: sqlsafety.ErrOverStoreLimit})
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	query := "DELETE FROM t WHERE id <= 10"
	c := mustCheck(t, s, query)
	_, err := execute(t, id, query, c.CheckID, "")
	w := safetyErrorOf(t, err, "reconfirm")
	st := w.Check.Statements[0]
	if w.Check.Undoable || st.Undoable || !hasReason(st.Reasons, sqlsafety.ReasonOverSnapshotLimit) {
		t.Fatalf("reconfirm check = %+v", w.Check)
	}
	if n := countRows(t, s, "t"); n != 1500 {
		t.Fatalf("rows deleted without a backup: %d left", n)
	}
	if _, err := execute(t, id, query, w.Check.CheckID, ""); err != nil {
		t.Fatal(err)
	}
	recs := listRecords(t, store)
	if n := countRows(t, s, "t"); n != 1490 || len(recs) != 1 || recs[0].Undoable || recs[0].UndoReason != sqlsafety.ReasonOverSnapshotLimit {
		t.Fatalf("rows %d, records %+v", n, recs)
	}
}

func TestGuardedEnforcesSnapshotLimitWithoutEstimate(t *testing.T) {
	settings := sqlsafety.DefaultSettings()
	settings.MaxSnapshotRows = 5
	useSafetySettings(t, settings)
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)

	// A high-risk statement whose estimate failed (Source none) but is undoable.
	st := sqlsafety.Classify(sqlsafety.FamilySQLite, "DELETE FROM t WHERE id <= 8")
	sc := sqlsafety.StatementCheck{Index: 0, Text: st.Text, Kind: st.Kind, Table: st.Table, Estimate: sqlsafety.Estimate{Source: "none"},
		Risk: sqlsafety.RiskHigh, Reasons: []sqlsafety.Reason{sqlsafety.ReasonNoEstimate}, Undoable: true}
	check := &sqlsafety.Check{SessionID: s.sessionID, Statements: []sqlsafety.StatementCheck{sc}, Risk: sqlsafety.RiskHigh, ConfirmTarget: "t", Undoable: true}
	safetyTokens.Issue(check, queryHash(st.Text))
	consumed, err := safetyTokens.Consume(check.CheckID, s.sessionID, "", queryHash(st.Text))
	if err != nil {
		t.Fatal(err)
	}
	steps, err := queryGuardedSteps(s, consumed, safetyPlan{statements: []safetyResolvedStatement{{parsed: st, primaryKey: []string{"id"}, resolved: true}}}, st.Text)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runGuarded(context.Background(), s, consumed, steps, execPerStatement, "query")
	var se *safetyError
	if !errors.As(err, &se) || se.Code != "reconfirm" {
		t.Fatalf("err = %v, want reconfirm", err)
	}
	got := se.Check.Statements[0]
	if got.Undoable || !hasReason(got.Reasons, sqlsafety.ReasonOverSnapshotLimit) || got.Estimate.Rows <= 5 {
		t.Fatalf("reconfirm statement = %+v", got)
	}
	if n := countRows(t, s, "t"); n != 1500 || len(listRecords(t, store)) != 0 {
		t.Fatalf("rows %d after an over-limit snapshot", n)
	}
}

func TestGuardedBatchRollsBackAll(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)

	query := "UPDATE t SET a=3 WHERE id=1; UPDATE nope SET a=1"
	c := mustCheck(t, s, query)
	check, plan, err := requireCheck(s, executeQueryArgs{Query: query, CheckID: &c.CheckID, ConfirmText: &c.ConfirmTarget})
	if err != nil {
		t.Fatal(err)
	}
	steps, err := queryGuardedSteps(s, check, plan, query)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runGuarded(context.Background(), s, check, steps, execBatch, "query"); err == nil {
		t.Fatal("batch with a failing step succeeded")
	}
	if countWhere(t, s, "a = 3") != 0 || len(listRecords(t, store)) != 0 {
		t.Fatal("batch failure must roll back every step and keep no record")
	}
}

func TestGuardedInsertRecordsKeys(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	query := "INSERT INTO t (a) VALUES (7), (8);"
	c := mustCheck(t, s, query)
	res, err := execute(t, id, query, c.CheckID, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.AffectedRows == nil || *res.AffectedRows != 2 {
		t.Fatalf("insert result = %+v", res)
	}
	recs := listRecords(t, store)
	if len(recs) != 1 || !recs[0].Undoable || recs[0].Kind != sqlsafety.KindInsert || recs[0].Affected != 2 {
		t.Fatalf("insert records = %+v", recs)
	}
	_, rows, err := store.Get(recs[0].ID)
	if err != nil || len(rows.Inserted) != 2 || rows.Inserted[0][0] != int64(1501) || rows.Inserted[1][0] != int64(1502) {
		t.Fatalf("inserted keys = %+v, %v", rows.Inserted, err)
	}

	// INSERT OR REPLACE deletes conflicting rows: never an undoable insert.
	query = "INSERT OR REPLACE INTO t (id, a) VALUES (1, 42)"
	c = mustCheck(t, s, query)
	if _, err := execute(t, id, query, c.CheckID, c.ConfirmTarget); err != nil {
		t.Fatal(err)
	}
	recs = listRecords(t, store)
	if len(recs) != 2 || recs[0].Undoable {
		t.Fatalf("insert or replace record = %+v", recs[0])
	}
}

func TestGuardedDisabledKeepsNoRecords(t *testing.T) {
	settings := sqlsafety.DefaultSettings()
	settings.Enabled = false
	useSafetySettings(t, settings)
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	_, err := execute(t, id, "DELETE FROM t WHERE id = 1", "", "")
	safetyErrorOf(t, err, "checkRequired")
	c := mustCheck(t, s, "DELETE FROM t WHERE id = 1")
	if _, err := execute(t, id, "DELETE FROM t WHERE id = 1", c.CheckID, ""); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "t"); n != 1499 || len(listRecords(t, store)) != 0 {
		t.Fatalf("rows %d, records kept while disabled", n)
	}
}

func TestSafetyCheckKeyUpdateNotUndoable(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	c := mustCheck(t, s, "UPDATE t SET id = id + 10000 WHERE id <= 3")
	if c.Undoable || c.Statements[0].Undoable || !hasReason(c.Statements[0].Reasons, sqlsafety.ReasonNoPrimaryKey) {
		t.Fatalf("key update check = %+v", c)
	}
}

func TestSQLServerDeclareRunsAsOneScript(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	s := newSafetyTestSession(t)
	s.dialect = "sqlserver" // checked only; nothing is sent to the database
	query := "DECLARE @n int = 1; UPDATE t SET a = @n WHERE id = 1"
	segments := sqlsafety.Split(sqlsafety.FamilySQLServer, query)
	check := &sqlsafety.Check{Statements: make([]sqlsafety.StatementCheck, len(segments))}
	plan := safetyPlan{statements: make([]safetyResolvedStatement, len(segments))}
	for i, seg := range segments {
		st := sqlsafety.Classify(sqlsafety.FamilySQLServer, seg)
		check.Statements[i] = sqlsafety.StatementCheck{Index: i, Kind: st.Kind, Reasons: []sqlsafety.Reason{sqlsafety.ReasonExplicitTransaction}}
		plan.statements[i] = safetyResolvedStatement{index: i, parsed: st}
	}
	if !safetyScriptStatement(sqlsafety.FamilySQLServer, len(segments), sqlsafety.Classify(sqlsafety.FamilySQLServer, segments[0])) {
		t.Fatal("T-SQL DECLARE in a multi-statement query must run as one script")
	}
	steps, err := queryGuardedSteps(s, check, plan, query)
	if err != nil || len(steps) != 1 || steps[0].Parsed.Text != query {
		t.Fatalf("steps = %+v, %v; want the whole batch as one step", steps, err)
	}
	// The script step can reach the frontend in a reconfirm check: its
	// estimate must name a source the parser accepts.
	if steps[0].Statement.Estimate.Source != "none" {
		t.Fatalf("script step estimate = %+v, want source none", steps[0].Statement.Estimate)
	}
}

func TestReadGuardRollsBackMisclassifiedWrite(t *testing.T) {
	s := newSafetyTestSession(t)
	// Simulate a write the classifier wrongly called a read, run under the
	// SQL Server style always-rolled-back guard on SQLite.
	if _, err := runReadOnlyQueryMode(context.Background(), s, "UPDATE t SET a = 77 WHERE id = 1", nil, readGuardRollback); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, s, "a = 77"); n != 0 {
		t.Fatalf("read guard kept a write: %d rows", n)
	}
	res, err := runReadOnlyQueryMode(context.Background(), s, "SELECT COUNT(*) FROM t", nil, readGuardRollback)
	if err != nil || res.Rows[0][0] != int64(1500) {
		t.Fatalf("guarded read = %+v, %v", res, err)
	}
	for dialect, want := range map[string]readGuardMode{
		"postgres": readGuardReadOnlyTx, "mysql": readGuardReadOnlyTx, "oracle": readGuardOracle, "yashandb": readGuardOracle,
		"dm": readGuardOracle, "sqlserver": readGuardRollback, "sqlite": readGuardNone, "clickhouse": readGuardNone,
	} {
		if got := readGuardFor(dialect); got != want {
			t.Errorf("readGuardFor(%s) = %v, want %v", dialect, got, want)
		}
	}
}

func TestSnapshotValueTypes(t *testing.T) {
	type named string
	now := time.Now()
	for _, c := range []struct {
		in, want any
	}{
		{nil, nil}, {true, true}, {int32(3), int32(3)}, {uint8(2), uint8(2)}, {float32(1.5), float64(1.5)},
		{json.Number("1.25"), json.Number("1.25")}, {"x", "x"}, {[]byte{0, 1}, []byte{0, 1}}, {now, now}, {named("n"), "n"},
	} {
		got, err := snapshotValue(c.in)
		if err != nil {
			t.Fatalf("snapshotValue(%T): %v", c.in, err)
		}
		if b, ok := c.want.([]byte); ok {
			if gb, ok := got.([]byte); !ok || string(gb) != string(b) {
				t.Fatalf("snapshotValue(%v) = %#v", c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Fatalf("snapshotValue(%#v) = %#v, want %#v", c.in, got, c.want)
		}
	}
	if _, err := snapshotValue(struct{ A int }{1}); err == nil {
		t.Fatal("struct values must be rejected")
	}
}

func TestOracleBlockKeepsTerminator(t *testing.T) {
	for text, want := range map[string]string{
		"BEGIN p; END": "BEGIN p; END;",
		"-- c\nDECLARE x NUMBER; BEGIN NULL; END":          "-- c\nDECLARE x NUMBER; BEGIN NULL; END;",
		"CREATE OR REPLACE PROCEDURE p AS BEGIN NULL; END": "CREATE OR REPLACE PROCEDURE p AS BEGIN NULL; END;",
		"UPDATE t SET a = 1":                               "UPDATE t SET a = 1",
	} {
		if got := safetyExecText(sqlsafety.FamilyOracle, "oracle", text); got != want {
			t.Errorf("safetyExecText(%q) = %q, want %q", text, got, want)
		}
	}
	if got := safetyExecText(sqlsafety.FamilyPostgres, "postgres", "BEGIN"); got != "BEGIN" {
		t.Errorf("postgres BEGIN changed: %q", got)
	}
}

// ── fix round 1 ──

type fakeSQLStateError struct{ code string }

func (e fakeSQLStateError) Error() string {
	return "ERROR: cannot execute in a read-only transaction (SQLSTATE " + e.code + ")"
}
func (e fakeSQLStateError) SQLState() string { return e.code }

type fakeMySQLError struct {
	Number  uint16
	Message string
}

func (e *fakeMySQLError) Error() string { return fmt.Sprintf("Error %d: %s", e.Number, e.Message) }

func TestReadOnlyViolationMapping(t *testing.T) {
	cases := []struct {
		dialect string
		err     error
		want    bool
	}{
		{"postgres", fakeSQLStateError{"25006"}, true},
		{"postgres", fmt.Errorf("wrapped: %w", fakeSQLStateError{"25006"}), true},
		{"postgres", fakeSQLStateError{"42P01"}, false},
		{"mysql", &fakeMySQLError{Number: 1792, Message: "Cannot execute statement in a READ ONLY transaction."}, true},
		{"mysql", errors.New("Error 1792 (25006): Cannot execute statement in a READ ONLY transaction."), true},
		{"mysql", &fakeMySQLError{Number: 1146, Message: "Table doesn't exist"}, false},
		{"oracle", errors.New("ORA-01456: may not perform insert/delete/update operation inside a READ ONLY transaction"), true},
		{"yashandb", errors.New("YAS-00000 operation not allowed in read only transaction"), true},
		{"dm", errors.New("[-6407]:只读事务中不能执行该操作"), true},
		{"oracle", errors.New("ORA-00942: table or view does not exist"), false},
		{"sqlserver", fakeSQLStateError{"25006"}, false}, // rolled back, never refused
		{"sqlite", fakeSQLStateError{"25006"}, false},
		{"postgres", nil, false},
	}
	for _, c := range cases {
		if got := isReadOnlyViolation(c.dialect, c.err); got != c.want {
			t.Errorf("isReadOnlyViolation(%s, %v) = %v, want %v", c.dialect, c.err, got, c.want)
		}
	}
}

func TestReadOnlyViolationIssuesForceWriteCheck(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	safetyReadHook = func(*dbSession, string) error { return errReadOnlyViolation }
	t.Cleanup(func() { safetyReadHook = nil })

	query := "SELECT a FROM t WHERE id = 1"
	_, err := execute(t, id, query, "", "")
	w := safetyErrorOf(t, err, "checkRequired")
	c := w.Check
	if c == nil || c.CheckID == "" || c.Risk != sqlsafety.RiskLow || c.Undoable || len(c.Statements) != 1 {
		t.Fatalf("force-write check = %+v", c)
	}
	st := c.Statements[0]
	if st.Kind != sqlsafety.KindUnknown || st.Risk != sqlsafety.RiskLow || st.Undoable || !hasReason(st.Reasons, sqlsafety.ReasonUnparsed) {
		t.Fatalf("force-write statement = %+v", st)
	}
	// Bound to the query text.
	_, err = execute(t, id, "SELECT a FROM t WHERE id = 2", c.CheckID, "")
	safetyErrorOf(t, err, "checkInvalid")

	res, err := execute(t, id, query, c.CheckID, "")
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("force-write run = %+v, %v", res, err)
	}
	recs := listRecords(t, store)
	if len(recs) != 1 || recs[0].Undoable || recs[0].Kind != sqlsafety.KindUnknown || recs[0].Statement != query {
		t.Fatalf("force-write records = %+v", recs)
	}
	_, err = execute(t, id, query, c.CheckID, "")
	safetyErrorOf(t, err, "checkInvalid")

	// The streaming command maps the refusal the same way.
	b, _ := json.Marshal(map[string]any{"sessionId": id, "query": query, "queryId": "q-" + sqlsafety.NewRecordID()})
	_, err = dbExecuteQueryStreamHandler(context.Background(), "", string(b))
	if w := safetyErrorOf(t, err, "checkRequired"); w.Check == nil || w.Check.CheckID == "" {
		t.Fatalf("stream force-write check = %+v", w)
	}
}

func TestCommitFailureKeepsUncertainBackup(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	// Ambiguous: the commit may have reached the database.
	safetyCommitHook = func(tx *sql.Tx) error {
		_ = tx.Commit()
		return errors.New("connection reset by peer")
	}
	t.Cleanup(func() { safetyCommitHook = nil })
	query := "UPDATE t SET a=6 WHERE id <= 2"
	c := mustCheck(t, s, query)
	if _, err := execute(t, id, query, c.CheckID, ""); err == nil {
		t.Fatal("ambiguous commit reported success")
	}
	recs := listRecords(t, store)
	if len(recs) != 1 || recs[0].UndoState != "uncertain" || countWhere(t, s, "a = 6") != 2 {
		t.Fatalf("ambiguous commit: records %+v", recs)
	}

	// Definite rollback: the backup is dropped.
	safetyCommitHook = func(tx *sql.Tx) error {
		_ = tx.Rollback()
		return sql.ErrTxDone
	}
	query = "UPDATE t SET a=7 WHERE id <= 2"
	c = mustCheck(t, s, query)
	if _, err := execute(t, id, query, c.CheckID, ""); err == nil {
		t.Fatal("rolled back commit reported success")
	}
	if recs := listRecords(t, store); len(recs) != 1 || countWhere(t, s, "a = 7") != 0 {
		t.Fatalf("definite rollback: records %+v", recs)
	}
}

func TestGuardedFailsClosedOnUnbackedUndoableStep(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	st := sqlsafety.Classify(sqlsafety.FamilySQLite, "DELETE FROM t WHERE id = 1")
	sc := sqlsafety.StatementCheck{Kind: st.Kind, Risk: sqlsafety.RiskLow, Undoable: true}
	check := &sqlsafety.Check{Statements: []sqlsafety.StatementCheck{sc}, Risk: sqlsafety.RiskLow, Undoable: true}

	// Undoable but no snapshot.
	if _, err := runGuarded(context.Background(), s, check, []guardedStep{{Statement: sc, Parsed: st}}, execPerStatement, "query"); err == nil {
		t.Fatal("undoable step without a snapshot ran")
	}
	// Undoable in a run without transactions (an explicit-transaction script).
	steps, err := queryGuardedSteps(s, check, safetyPlan{statements: []safetyResolvedStatement{{parsed: st, primaryKey: []string{"id"}, resolved: true}}}, st.Text)
	if err != nil {
		t.Fatal(err)
	}
	begin := guardedStep{Statement: sqlsafety.StatementCheck{Index: 1, Kind: sqlsafety.KindScript}, Parsed: sqlsafety.Statement{Text: "BEGIN", Kind: sqlsafety.KindScript}}
	if _, err := runGuarded(context.Background(), s, check, append([]guardedStep{begin}, steps...), execPerStatement, "query"); err == nil {
		t.Fatal("undoable step ran without a transaction")
	}
	if countRows(t, s, "t") != 1500 || len(listRecords(t, store)) != 0 {
		t.Fatal("fail-closed run changed data")
	}
}

func TestPerStatementReadsBeforeFailureAreNotPartial(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	query := "SELECT a FROM t WHERE id = 1; UPDATE nope SET a=1"
	c := mustCheck(t, s, query)
	_, err := execute(t, id, query, c.CheckID, c.ConfirmTarget)
	if err == nil || strings.HasPrefix(err.Error(), "sqlsafety:") {
		t.Fatalf("err = %v, want the raw database error", err)
	}
}

func TestMySQLTextTypeKeepsBinaryColumnsAsBytes(t *testing.T) {
	for name, want := range map[string]bool{
		"VARCHAR": true, "CHAR": true, "TEXT": true, "LONGTEXT": true, "ENUM": true, "SET": true, "JSON": true,
		"INT": true, "DECIMAL": true, "DATETIME": true,
		"VARBINARY": false, "BINARY": false, "BLOB": false, "LONGBLOB": false, "BIT": false, "GEOMETRY": false, "": false,
	} {
		if got := mysqlTextType(name); got != want {
			t.Errorf("mysqlTextType(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSQLServerDecimalTypesAreText(t *testing.T) {
	for name, want := range map[string]bool{"DECIMAL": true, "NUMERIC": true, "MONEY": true, "SMALLMONEY": true, "VARBINARY": false, "UNIQUEIDENTIFIER": false, "INT": false} {
		if got := sqlServerDecimalType(name); got != want {
			t.Errorf("sqlServerDecimalType(%q) = %v, want %v", name, got, want)
		}
	}
}
