package db

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"mincode/app/backend/db/sqlsafety"
)

// guardedExec checks and runs query on the registered session, returning
// the ID of the newest record it saved.
func guardedExec(t *testing.T, s *dbSession, sessionID string, store *sqlsafety.Store, query, confirm string) string {
	t.Helper()
	c := mustCheck(t, s, query)
	if _, err := execute(t, sessionID, query, c.CheckID, confirm); err != nil {
		t.Fatalf("execute %q: %v", query, err)
	}
	recs := listRecords(t, store)
	if len(recs) == 0 {
		t.Fatalf("no record after %q", query)
	}
	return recs[0].ID
}

func undoArgs(sessionID, id, mode, confirm string) string {
	return undoArgsWithToken(sessionID, id, mode, confirm, "")
}

func undoArgsWithToken(sessionID, id, mode, confirm, token string) string {
	a := map[string]any{"sessionId": sessionID, "id": id}
	if token != "" {
		a["previewToken"] = token
	}
	if mode != "" {
		a["mode"] = mode
	}
	if confirm != "" {
		a["confirmText"] = confirm
	}
	b, _ := json.Marshal(a)
	return string(b)
}

// undo previews the record and undoes it with that preview's token.
func undo(t *testing.T, sessionID, id, mode, confirm string) (safetyUndoResult, error) {
	t.Helper()
	token := ""
	if p, err := preview(t, sessionID, id); err == nil {
		token = p.PreviewToken
	}
	return undoWithToken(t, sessionID, id, mode, confirm, token)
}

func undoWithToken(t *testing.T, sessionID, id, mode, confirm, token string) (safetyUndoResult, error) {
	t.Helper()
	out, err := dbSafetyUndoHandler(context.Background(), "", undoArgsWithToken(sessionID, id, mode, confirm, token))
	if err != nil {
		return safetyUndoResult{}, err
	}
	var res safetyUndoResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode undo result %q: %v", out, err)
	}
	return res, nil
}

func mustUndo(t *testing.T, sessionID, id, mode, confirm string) safetyUndoResult {
	t.Helper()
	res, err := undo(t, sessionID, id, mode, confirm)
	if err != nil {
		t.Fatalf("undo %s: %v", id, err)
	}
	return res
}

func preview(t *testing.T, sessionID, id string) (safetyUndoPreview, error) {
	t.Helper()
	out, err := dbSafetyUndoPreviewHandler(context.Background(), "", undoArgs(sessionID, id, "", ""))
	if err != nil {
		return safetyUndoPreview{}, err
	}
	var p safetyUndoPreview
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("decode preview %q: %v", out, err)
	}
	return p, nil
}

func mustPreview(t *testing.T, sessionID, id string) safetyUndoPreview {
	t.Helper()
	p, err := preview(t, sessionID, id)
	if err != nil {
		t.Fatalf("preview %s: %v", id, err)
	}
	return p
}

func undoState(t *testing.T, store *sqlsafety.Store, id string) string {
	t.Helper()
	rec, _, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return rec.UndoState
}

func valueOf(t *testing.T, s *dbSession, id int) int64 {
	t.Helper()
	var a int64
	if err := s.db.QueryRow("SELECT a FROM t WHERE id = ?", id).Scan(&a); err != nil {
		t.Fatalf("read t.id=%d: %v", id, err)
	}
	return a
}

func conflictKinds(p safetyUndoPreview) map[string]string {
	out := map[string]string{}
	for _, c := range p.Conflicts {
		b, _ := json.Marshal(c.PrimaryKey["id"])
		out[string(b)] = c.Kind
	}
	return out
}

func TestUndoRestoresUpdateAndDelete(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	updateID := guardedExec(t, s, id, store, "UPDATE t SET a=5 WHERE id <= 3", "")
	if p := mustPreview(t, id, updateID); p.Total != 3 || len(p.Conflicts) != 0 || p.Record.ID != updateID {
		t.Fatalf("preview = %+v", p)
	}
	res := mustUndo(t, id, updateID, "skipConflicts", "")
	if res.Restored != 3 || res.Skipped != 0 || res.RecordID == "" {
		t.Fatalf("undo update = %+v", res)
	}
	if n := countWhere(t, s, "id <= 3 AND a = 0"); n != 3 {
		t.Fatalf("restored rows = %d, want 3", n)
	}
	if st := undoState(t, store, updateID); st != "undone" {
		t.Fatalf("original state = %q, want undone", st)
	}
	child, rows, err := store.Get(res.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID != updateID || child.Source != "undo" || !child.Undoable || child.UndoState != "available" ||
		child.Kind != sqlsafety.KindData || len(rows.Before) != 3 || len(rows.After) != 3 {
		t.Fatalf("undo record = %+v rows %d/%d", child, len(rows.Before), len(rows.After))
	}
	if _, err := undo(t, id, updateID, "skipConflicts", ""); err == nil || !strings.Contains(err.Error(), "已撤回") {
		t.Fatalf("second undo of the same record: err = %v", err)
	}
	// The undo is itself undoable: undoing it re-applies the update.
	if res := mustUndo(t, id, child.ID, "skipConflicts", ""); res.Restored != 3 {
		t.Fatalf("undo of undo = %+v", res)
	}
	if n := countWhere(t, s, "id <= 3 AND a = 5"); n != 3 {
		t.Fatalf("re-applied rows = %d, want 3", n)
	}

	deleteID := guardedExec(t, s, id, store, "DELETE FROM t WHERE id IN (10, 11)", "")
	if n := countWhere(t, s, "id IN (10, 11)"); n != 0 {
		t.Fatalf("rows left after delete = %d", n)
	}
	res = mustUndo(t, id, deleteID, "skipConflicts", "")
	if res.Restored != 2 || res.Skipped != 0 {
		t.Fatalf("undo delete = %+v", res)
	}
	if n := countWhere(t, s, "id IN (10, 11) AND a = 0"); n != 2 {
		t.Fatalf("re-inserted rows = %d, want 2", n)
	}
	child, rows, err = store.Get(res.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID != deleteID || child.Kind != sqlsafety.KindInsert || len(rows.Inserted) != 2 || !child.Undoable {
		t.Fatalf("re-insert record = %+v inserted %v", child, rows.Inserted)
	}
	// Undoing the re-insert deletes the rows again.
	if res := mustUndo(t, id, child.ID, "skipConflicts", ""); res.Restored != 2 {
		t.Fatalf("undo of re-insert = %+v", res)
	}
	if n := countWhere(t, s, "id IN (10, 11)"); n != 0 {
		t.Fatalf("rows after undoing the re-insert = %d, want 0", n)
	}
}

func TestUndoDetectsConflicts(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	recID := guardedExec(t, s, id, store, "UPDATE t SET a=7 WHERE id <= 4", "")
	if _, err := s.db.Exec("UPDATE t SET a = 99 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM t WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	p := mustPreview(t, id, recID)
	if p.Total != 4 || len(p.Conflicts) != 2 {
		t.Fatalf("preview = %+v", p)
	}
	if kinds := conflictKinds(p); kinds["1"] != "changed" || kinds["2"] != "missing" {
		t.Fatalf("conflicts = %+v", p.Conflicts)
	}

	res := mustUndo(t, id, recID, "skipConflicts", "")
	if res.Restored != 2 || res.Skipped != 2 {
		t.Fatalf("skipConflicts = %+v", res)
	}
	if valueOf(t, s, 1) != 99 || valueOf(t, s, 3) != 0 || valueOf(t, s, 4) != 0 {
		t.Fatal("skipConflicts must restore only the unchanged rows")
	}
	if st := undoState(t, store, recID); st != "partial" {
		t.Fatalf("state = %q, want partial", st)
	}

	// A partial record can be undone again for the rows still outstanding.
	p = mustPreview(t, id, recID)
	if p.Total != 2 || len(p.Conflicts) != 2 || p.Record.UndoState != "partial" {
		t.Fatalf("partial preview = %+v", p)
	}
	res = mustUndo(t, id, recID, "overwrite", "")
	if res.Restored != 1 || res.Skipped != 1 {
		t.Fatalf("overwrite after partial = %+v", res)
	}
	if valueOf(t, s, 1) != 0 {
		t.Fatal("overwrite must restore the changed row")
	}

	// overwrite from the start restores every row that still exists.
	recID = guardedExec(t, s, id, store, "UPDATE t SET a=8 WHERE id BETWEEN 20 AND 23", "")
	if _, err := s.db.Exec("UPDATE t SET a = 99 WHERE id = 20"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DELETE FROM t WHERE id = 21"); err != nil {
		t.Fatal(err)
	}
	res = mustUndo(t, id, recID, "overwrite", "")
	if res.Restored != 3 || res.Skipped != 1 {
		t.Fatalf("overwrite = %+v", res)
	}
	if n := countWhere(t, s, "id BETWEEN 20 AND 23 AND a = 0"); n != 3 {
		t.Fatalf("overwritten rows = %d, want 3", n)
	}
}

func TestUndoRejectsOtherConnection(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	recID := guardedExec(t, s, id, store, "UPDATE t SET a=5 WHERE id = 1", "")

	other := newSafetyTestSession(t)
	otherID := registerSafetySession(t, other)
	other.connectionID = "conn-2"
	if _, err := undo(t, otherID, recID, "skipConflicts", ""); err == nil {
		t.Fatal("undo from another connection must fail")
	}
	if _, err := preview(t, otherID, recID); err == nil {
		t.Fatal("preview from another connection must fail")
	}
	if valueOf(t, s, 1) != 5 || undoState(t, store, recID) != "available" {
		t.Fatal("a refused undo must change nothing")
	}
}

func TestUndoInsertDeletesNewRows(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	recID := guardedExec(t, s, id, store, "INSERT INTO t (id, a) VALUES (2001, 1), (2002, 2), (2003, 3)", "")
	if _, err := s.db.Exec("DELETE FROM t WHERE id = 2003"); err != nil {
		t.Fatal(err)
	}
	p := mustPreview(t, id, recID)
	if kinds := conflictKinds(p); p.Total != 3 || len(kinds) != 1 || kinds["2003"] != "missing" {
		t.Fatalf("preview = %+v", p)
	}
	res := mustUndo(t, id, recID, "skipConflicts", "")
	if res.Restored != 2 || res.Skipped != 1 {
		t.Fatalf("undo insert = %+v", res)
	}
	if n := countWhere(t, s, "id > 2000"); n != 0 {
		t.Fatalf("rows left = %d", n)
	}
	child, rows, err := store.Get(res.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Kind != sqlsafety.KindData || len(rows.Before) != 2 || len(rows.After) != 0 {
		t.Fatalf("undo record = %+v", child)
	}
}

func TestUndoDeleteExistingKey(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	recID := guardedExec(t, s, id, store, "DELETE FROM t WHERE id IN (30, 31)", "")
	if _, err := s.db.Exec("INSERT INTO t (id, a) VALUES (30, 77)"); err != nil {
		t.Fatal(err)
	}
	p := mustPreview(t, id, recID)
	if kinds := conflictKinds(p); kinds["30"] != "exists" || len(kinds) != 1 {
		t.Fatalf("preview = %+v", p)
	}
	res := mustUndo(t, id, recID, "overwrite", "")
	if res.Restored != 2 || res.Skipped != 0 {
		t.Fatalf("overwrite = %+v", res)
	}
	if valueOf(t, s, 30) != 0 || valueOf(t, s, 31) != 0 {
		t.Fatal("overwrite must restore both rows")
	}
	if st := undoState(t, store, recID); st != "undone" {
		t.Fatalf("state = %q", st)
	}
}

func TestUndoHighRiskNeedsConfirm(t *testing.T) {
	settings := sqlsafety.DefaultSettings()
	settings.HighRiskRows = 2
	useSafetySettings(t, settings)
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	recID := guardedExec(t, s, id, store, "UPDATE t SET a=3 WHERE id <= 3", "t")
	_, err := undo(t, id, recID, "skipConflicts", "")
	if w := safetyErrorOf(t, err, "confirmMismatch"); w.Check == nil || w.Check.ConfirmTarget != "t" {
		t.Fatalf("mismatch error = %+v", w)
	}
	if valueOf(t, s, 1) != 3 {
		t.Fatal("a refused undo must change nothing")
	}
	if res := mustUndo(t, id, recID, "skipConflicts", "t"); res.Restored != 3 {
		t.Fatalf("confirmed undo = %+v", res)
	}
}

func TestUndoLargeRecordIsOneUndoRecord(t *testing.T) {
	settings := sqlsafety.DefaultSettings()
	settings.HighRiskRows = 10000
	useSafetySettings(t, settings)
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	recID := guardedExec(t, s, id, store, "UPDATE t SET a=1 WHERE id <= 1200", "")
	res := mustUndo(t, id, recID, "skipConflicts", "")
	if res.Restored != 1200 || len(res.RecordIDs) != 1 {
		t.Fatalf("undo = %+v", res)
	}
	if n := countWhere(t, s, "a = 1"); n != 0 {
		t.Fatalf("rows not restored = %d", n)
	}
	_, rows, err := store.Get(res.RecordID)
	if err != nil || len(rows.Before) != 1200 || len(rows.After) != 1200 {
		t.Fatalf("undo record rows %d/%d, %v", len(rows.Before), len(rows.After), err)
	}
}

func putRecord(t *testing.T, store *sqlsafety.Store, rec sqlsafety.Record, rows sqlsafety.Rows) string {
	t.Helper()
	rec.ID = sqlsafety.NewRecordID()
	if rec.ConnectionID == "" {
		rec.ConnectionID = "conn-1"
	}
	rec.DbType = "sqlite"
	if err := store.Put(rec, rows); err != nil {
		t.Fatal(err)
	}
	return rec.ID
}

func TestUndoRefusesUncertainAndUndone(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	base := sqlsafety.Record{Table: sqlsafety.TableRef{Name: "t"}, Kind: sqlsafety.KindData, Statement: "UPDATE t SET a = 9 WHERE id = 1",
		Undoable: true, PrimaryKey: []string{"id"}, Columns: []string{"id", "a"}}
	rows := sqlsafety.Rows{Before: [][]any{{int64(1), int64(0)}}, After: [][]any{{int64(1), int64(9)}}}
	for _, state := range []string{"uncertain", "undone"} {
		rec := base
		rec.UndoState = state
		recID := putRecord(t, store, rec, rows)
		if _, err := undo(t, id, recID, "overwrite", ""); err == nil {
			t.Fatalf("undo of a %s record must fail", state)
		}
		if state == "uncertain" {
			p := mustPreview(t, id, recID)
			if p.Record.UndoState != "uncertain" {
				t.Fatalf("uncertain preview = %+v", p)
			}
		}
	}
	rec := base
	rec.Undoable, rec.UndoState = false, "none"
	if _, err := undo(t, id, putRecord(t, store, rec, sqlsafety.Rows{}), "overwrite", ""); err == nil {
		t.Fatal("undo of a record without backup must fail")
	}
	if _, err := undo(t, id, "missing-id", "overwrite", ""); err == nil {
		t.Fatal("undo of an unknown record must fail")
	}
	if _, err := undo(t, id, putRecord(t, store, base, rows), "bogus", ""); err == nil {
		t.Fatal("unknown mode must fail")
	}
}

func TestUndoWholeTableSnapshot(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	keyed := putRecord(t, store, sqlsafety.Record{Table: sqlsafety.TableRef{Name: "w"}, Kind: sqlsafety.KindStructure,
		Statement: "DROP TABLE w", Undoable: true, UndoState: "available", PrimaryKey: []string{"id"}, Columns: []string{"id", "b"}},
		sqlsafety.Rows{Before: [][]any{{int64(1), "x"}, {int64(2), nil}, {int64(3), []byte{0, 1}}}})
	_, err := preview(t, id, keyed)
	safetyErrorOf(t, err, "tableMissing")
	_, err = undo(t, id, keyed, "skipConflicts", "")
	safetyErrorOf(t, err, "tableMissing")

	if _, err := s.db.Exec("CREATE TABLE w (id INTEGER PRIMARY KEY, b BLOB)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO w VALUES (2, 'new')"); err != nil {
		t.Fatal(err)
	}
	if kinds := conflictKinds(mustPreview(t, id, keyed)); kinds["2"] != "exists" || len(kinds) != 1 {
		t.Fatalf("conflicts = %+v", kinds)
	}
	res := mustUndo(t, id, keyed, "skipConflicts", "")
	if res.Restored != 2 || res.Skipped != 1 {
		t.Fatalf("whole-table undo = %+v", res)
	}
	var b any
	if err := s.db.QueryRow("SELECT b FROM w WHERE id = 3").Scan(&b); err != nil || string(b.([]byte)) != "\x00\x01" {
		t.Fatalf("restored blob = %v, %v", b, err)
	}
	if st := undoState(t, store, keyed); st != "partial" {
		t.Fatalf("state = %q", st)
	}

	// Without a primary key the rows are inserted as they are, recorded without undo.
	if _, err := s.db.Exec("CREATE TABLE nk (a INTEGER, b TEXT)"); err != nil {
		t.Fatal(err)
	}
	unkeyed := putRecord(t, store, sqlsafety.Record{Table: sqlsafety.TableRef{Name: "nk"}, Kind: sqlsafety.KindStructure,
		Statement: "TRUNCATE nk", Undoable: true, UndoState: "available", Columns: []string{"a", "b"}},
		sqlsafety.Rows{Before: [][]any{{int64(1), "x"}, {int64(1), "x"}}})
	res = mustUndo(t, id, unkeyed, "skipConflicts", "")
	var n int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM nk WHERE a = 1 AND b = 'x'").Scan(&n); err != nil || n != 2 || res.Restored != 2 {
		t.Fatalf("unkeyed undo = %+v, rows %d, %v", res, n, err)
	}
	if child, _, err := store.Get(res.RecordID); err != nil || child.Undoable || child.ParentID != unkeyed {
		t.Fatalf("unkeyed undo record = %+v, %v", child, err)
	}
}

func TestSafetyHistoryListGetAndPurge(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	recID := guardedExec(t, s, id, store, "UPDATE t SET a=2 WHERE id <= 30", "")
	putRecord(t, store, sqlsafety.Record{ConnectionID: "conn-9", Kind: sqlsafety.KindData, UndoState: "none"}, sqlsafety.Rows{})

	out, err := dbSafetyHistoryListHandler("", `{"connectionId":"conn-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	var list []sqlsafety.Record
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 || list[0].ID != recID {
		t.Fatalf("list = %s, %v", out, err)
	}
	if _, err := dbSafetyHistoryListHandler("", `{}`); err == nil {
		t.Fatal("list without connectionId must fail")
	}
	out, err = dbSafetyHistoryListHandler("", `{"connectionId":"nobody"}`)
	if err != nil || out != "[]" {
		t.Fatalf("empty list = %s, %v", out, err)
	}

	out, err = dbSafetyHistoryGetHandler("", `{"id":"`+recID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var got safetyHistoryDetail
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Record.ID != recID || !slices.Equal(got.Sample.Columns, []string{"id", "a"}) ||
		len(got.Sample.Before) != 20 || len(got.Sample.After) != 20 {
		t.Fatalf("detail = %+v", got)
	}

	if _, err := dbSafetyPurgeConnectionHandler("", `{"connectionId":"conn-1"}`); err != nil {
		t.Fatal(err)
	}
	if recs := listRecords(t, store); len(recs) != 1 || recs[0].ConnectionID != "conn-9" {
		t.Fatalf("records after purge = %+v", recs)
	}
}

func TestSafetySettingsGetSet(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)

	want := sqlsafety.DefaultSettings()
	want.HighRiskRows = 50
	b, _ := json.Marshal(map[string]any{"settings": want})
	if _, err := dbSafetySettingsSetHandler("", string(b)); err != nil {
		t.Fatal(err)
	}
	out, err := dbSafetySettingsGetHandler("", "{}")
	if err != nil {
		t.Fatal(err)
	}
	var got sqlsafety.Settings
	if err := json.Unmarshal([]byte(out), &got); err != nil || got != want {
		t.Fatalf("settings = %s, %v", out, err)
	}
	if loaded, err := sqlsafety.LoadSettings(sqlsafety.Dir()); err != nil || loaded != want {
		t.Fatalf("saved settings = %+v, %v", loaded, err)
	}
	bad := want
	bad.HighRiskRows = 0
	b, _ = json.Marshal(map[string]any{"settings": bad})
	if _, err := dbSafetySettingsSetHandler("", string(b)); err == nil {
		t.Fatal("invalid settings must be refused")
	}
}

func TestSafetyKeysPerBatch(t *testing.T) {
	for _, tc := range []struct {
		pk, want int
	}{{1, 500}, {3, 500}, {5, 400}, {7, 285}, {3000, 1}} {
		if got := safetyKeysPerBatch(tc.pk); got != tc.want {
			t.Fatalf("safetyKeysPerBatch(%d) = %d, want %d", tc.pk, got, tc.want)
		}
	}
	s := &dbSession{kind: kindSQL, dialect: "sqlserver"}
	q, err := undoKeyQuery(s, sqlsafety.TableRef{Schema: "dbo", Name: "t"}, []string{"a", "b"}, nil, [][]any{{1, 2}, {3, 4}, {5, 6}}, true)
	if err != nil || len(q.batches) != 1 || len(q.batches[0]) != 6 || !strings.Contains(q.sql, "WITH (UPDLOCK, HOLDLOCK) WHERE") {
		t.Fatalf("query = %+v", q)
	}
	if q, err := undoKeyQuery(s, sqlsafety.TableRef{Name: "t"}, nil, nil, [][]any{{1}}, false); err != nil || q.sql != "" || q.batches != nil {
		t.Fatalf("no primary key must build no query: %+v", q)
	}
	if q, err := undoKeyQuery(s, sqlsafety.TableRef{Name: "t"}, []string{"a"}, nil, nil, false); err != nil || q.sql != "" || q.batches != nil {
		t.Fatalf("no keys must build no query: %+v", q)
	}
}

func TestUndoRollsBackWhenRowsChangeAfterCheck(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	recID := guardedExec(t, s, id, store, "UPDATE t SET a=6 WHERE id <= 2", "")
	p, err := loadUndoPlan(context.Background(), s, store, recID)
	if err != nil {
		t.Fatal(err)
	}
	// Changed after the conflict check, before the undo transaction.
	if _, err := s.db.Exec("UPDATE t SET a = 50 WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	_, err = runUndo(context.Background(), s, store, p, "skipConflicts", "", p.previewToken())
	safetyErrorOf(t, err, "previewStale")
	if valueOf(t, s, 1) != 6 || valueOf(t, s, 2) != 50 {
		t.Fatal("a rolled back undo must change nothing")
	}
	if st := undoState(t, store, recID); st != "available" {
		t.Fatalf("state = %q, want available", st)
	}
	if recs := listRecords(t, store); len(recs) != 1 {
		t.Fatalf("records = %d, want only the original", len(recs))
	}
}

func TestUndoRefusesStalePreview(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)

	recID := guardedExec(t, s, id, store, "UPDATE t SET a=4 WHERE id <= 3", "")
	if _, err := s.db.Exec("UPDATE t SET a = 40 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	p := mustPreview(t, id, recID)
	if len(p.Conflicts) != 1 || p.PreviewToken == "" {
		t.Fatalf("preview = %+v", p)
	}
	// One more row changes after the preview: overwrite must not touch it unseen.
	if _, err := s.db.Exec("UPDATE t SET a = 41 WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	_, err := undoWithToken(t, id, recID, "overwrite", "", p.PreviewToken)
	safetyErrorOf(t, err, "previewStale")
	// A conflicting row that changes again is also stale.
	p = mustPreview(t, id, recID)
	if _, err := s.db.Exec("UPDATE t SET a = 42 WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	_, err = undoWithToken(t, id, recID, "skipConflicts", "", p.PreviewToken)
	safetyErrorOf(t, err, "previewStale")
	_, err = undoWithToken(t, id, recID, "overwrite", "", "")
	safetyErrorOf(t, err, "previewStale")
	if valueOf(t, s, 1) != 42 || valueOf(t, s, 2) != 41 || valueOf(t, s, 3) != 4 || undoState(t, store, recID) != "available" {
		t.Fatal("a stale undo must change nothing")
	}
	if recs := listRecords(t, store); len(recs) != 1 {
		t.Fatalf("records = %d, want only the original", len(recs))
	}

	// Unchanged since the preview: the undo runs.
	p = mustPreview(t, id, recID)
	res, err := undoWithToken(t, id, recID, "overwrite", "", p.PreviewToken)
	if err != nil || res.Restored != 3 {
		t.Fatalf("undo = %+v, %v", res, err)
	}
	if n := countWhere(t, s, "id <= 3 AND a = 0"); n != 3 {
		t.Fatalf("restored rows = %d, want 3", n)
	}
}

func TestUndoBacksUpWithSafetyNetDisabled(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	recID := guardedExec(t, s, id, store, "UPDATE t SET a=5 WHERE id <= 2", "")

	off := sqlsafety.DefaultSettings()
	off.Enabled = false
	useSafetySettings(t, off)
	res := mustUndo(t, id, recID, "skipConflicts", "")
	if res.Restored != 2 || res.RecordID == "" {
		t.Fatalf("undo = %+v", res)
	}
	child, rows, err := store.Get(res.RecordID)
	if err != nil || child.ParentID != recID || !child.Undoable || len(rows.Before) != 2 || len(rows.After) != 2 {
		t.Fatalf("undo record = %+v rows %d/%d, %v", child, len(rows.Before), len(rows.After), err)
	}
}

func TestUndoReconfirmBecomesPreviewStale(t *testing.T) {
	r := &guardedRun{source: "undo", check: &sqlsafety.Check{}, settings: sqlsafety.DefaultSettings()}
	safetyErrorOf(t, r.reconfirm(guardedStep{}, &sqlsafety.Estimate{Rows: 5}, ""), "previewStale")
}

func TestUndoSQLServerLockFailsClosed(t *testing.T) {
	s := &dbSession{kind: kindSQL, dialect: "sqlserver"}
	table := sqlsafety.TableRef{Schema: "dbo", Name: "t"}
	if q, err := lockedByKeys(s, "SELECT * FROM [dbo].[t] WHERE ([a] = @p1)", table); err != nil || !strings.Contains(q, "[dbo].[t] WITH (UPDLOCK, HOLDLOCK) WHERE") {
		t.Fatalf("locked = %q, %v", q, err)
	}
	if q, err := lockedByKeys(s, "SELECT * FROM [dbo].[other] WHERE ([a] = @p1)", table); err == nil {
		t.Fatalf("a query the hint cannot be placed in must fail, got %q", q)
	}
}

// overLimitStore refuses every new record as over the store limit.
type overLimitStore struct{ *sqlsafety.Store }

func (overLimitStore) Put(sqlsafety.Record, sqlsafety.Rows) error { return sqlsafety.ErrOverStoreLimit }

func TestUndoOverStoreLimitIsNotPreviewStale(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s := newSafetyTestSession(t)
	id := registerSafetySession(t, s)
	recID := guardedExec(t, s, id, store, "UPDATE t SET a=7 WHERE id <= 2", "")

	// The undo's own backup cannot be stored: re-previewing would not help.
	useSafetyRecords(t, overLimitStore{store})
	p := mustPreview(t, id, recID)
	_, err := undoWithToken(t, id, recID, "skipConflicts", "", p.PreviewToken)
	if err == nil || strings.HasPrefix(err.Error(), "sqlsafety:") || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("err = %v, want a plain over-limit error, not a re-preview loop", err)
	}
	if valueOf(t, s, 1) != 7 || valueOf(t, s, 2) != 7 || undoState(t, store, recID) != "available" {
		t.Fatal("an undo without its backup must change nothing")
	}
}

func TestUndoReconfirmWithoutUndoIsNotPreviewStale(t *testing.T) {
	r := &guardedRun{source: "undo", check: &sqlsafety.Check{}, settings: sqlsafety.DefaultSettings()}
	for _, err := range []error{
		r.reconfirm(guardedStep{}, nil, sqlsafety.ReasonNotUndoableStore),
		r.reconfirm(guardedStep{}, &sqlsafety.Estimate{Rows: 5}, sqlsafety.ReasonOverSnapshotLimit),
	} {
		if err == nil || strings.HasPrefix(err.Error(), "sqlsafety:") {
			t.Fatalf("err = %v, want a plain error naming why the undo cannot be backed up", err)
		}
	}
}
