package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"mincode/app/backend/db/sqlsafety"
)

// newRowsTestSession is the safety test session plus g(id pk, name NOT NULL, a)
// holding ids 1..5.
func newRowsTestSession(t *testing.T) (*dbSession, string) {
	t.Helper()
	s := newSafetyTestSession(t)
	for _, stmt := range []string{
		`CREATE TABLE g (id INTEGER PRIMARY KEY, name TEXT NOT NULL, a INTEGER)`,
		`INSERT INTO g (id, name, a) VALUES (1, 'one', 1), (2, 'two', 2), (3, 'three', 3), (4, 'four', 4), (5, 'five', 5)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return s, registerSafetySession(t, s)
}

func rowsOp(table string, changes []map[string]any) map[string]any {
	return map[string]any{"kind": "rows", "table": table, "changes": changes}
}

type applyChangesResult struct {
	Applied  int    `json:"applied"`
	RecordID string `json:"recordId"`
}

func applyChangesJSON(t *testing.T, argsJSON string) (applyChangesResult, error) {
	t.Helper()
	out, err := dbApplyChangesHandler(context.Background(), "", argsJSON)
	if err != nil {
		return applyChangesResult{}, err
	}
	var res applyChangesResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return res, nil
}

func applyChanges(t *testing.T, sessionID, table string, changes []map[string]any, checkID, confirm string) (applyChangesResult, error) {
	t.Helper()
	a := map[string]any{"sessionId": sessionID, "database": "main", "table": table, "changes": changes}
	return applyChangesJSON(t, withToken(a, checkID, confirm))
}

// checkAndApply checks changes and submits them with the check's token.
func checkAndApply(t *testing.T, sessionID, table string, changes []map[string]any, confirm string) applyChangesResult {
	t.Helper()
	c := opCheck(t, sessionID, rowsOp(table, changes), "main")
	res, err := applyChanges(t, sessionID, table, changes, c.CheckID, confirm)
	if err != nil {
		t.Fatalf("db_apply_changes: %v", err)
	}
	return res
}

// tableDump renders every row of g in key order.
func tableDump(t *testing.T, s *dbSession) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT id, name, a FROM g ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := ""
	for rows.Next() {
		var id int64
		var name string
		var a *int64
		if err := rows.Scan(&id, &name, &a); err != nil {
			t.Fatal(err)
		}
		av := "null"
		if a != nil {
			av = fmt.Sprint(*a)
		}
		out += fmt.Sprintf("%d:%s:%s;", id, name, av)
	}
	return out
}

func TestApplyChangesAtomic(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s, id := newRowsTestSession(t)
	before := tableDump(t, s)

	changes := []map[string]any{
		{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"name": "uno"}},
		{"op": "update", "primaryKey": map[string]any{"id": 2}, "values": map[string]any{"a": 20}},
		{"op": "update", "primaryKey": map[string]any{"id": 3}, "values": map[string]any{"name": nil}}, // NOT NULL
	}
	c := opCheck(t, id, rowsOp("g", changes), "main")
	if c.Risk != sqlsafety.RiskLow || !c.Undoable {
		t.Fatalf("check = %+v", c)
	}
	_, err := applyChanges(t, id, "g", changes, c.CheckID, "")
	w := safetyErrorOf(t, err, "partial")
	if w.Applied == nil || *w.Applied != 0 || w.FailedIndex == nil || *w.FailedIndex != 2 || w.Message == "" {
		t.Fatalf("partial = %+v", w)
	}
	if after := tableDump(t, s); after != before {
		t.Fatalf("table changed: %s, want %s", after, before)
	}
	if recs := listRecords(t, store); len(recs) != 0 {
		t.Fatalf("records = %+v, want none", recs)
	}
}

func TestApplyChangesFailedIndexAcrossKinds(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)
	s, id := newRowsTestSession(t)
	before := tableDump(t, s)
	changes := []map[string]any{
		{"op": "delete", "primaryKey": map[string]any{"id": 5}},
		{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"a": 9}},
		{"op": "insert", "values": map[string]any{"id": 10, "name": "ten"}},
		{"op": "insert", "values": map[string]any{"id": 2, "name": "dup"}}, // duplicate key
	}
	c := opCheck(t, id, rowsOp("g", changes), "main")
	_, err := applyChanges(t, id, "g", changes, c.CheckID, "")
	w := safetyErrorOf(t, err, "partial")
	if *w.Applied != 0 || *w.FailedIndex != 3 {
		t.Fatalf("partial = %+v", w)
	}
	if after := tableDump(t, s); after != before {
		t.Fatalf("table changed: %s", after)
	}
}

func TestApplyChangesSnapshotsAndUndo(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s, id := newRowsTestSession(t)
	before := tableDump(t, s)

	changes := []map[string]any{
		{"op": "delete", "primaryKey": map[string]any{"id": 5}},
		{"op": "delete", "primaryKey": map[string]any{"id": 4}},
		{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"name": "uno", "a": 11}},
		{"op": "update", "primaryKey": map[string]any{"id": 2}, "values": map[string]any{"a": nil}},
		{"op": "insert", "values": map[string]any{"id": 100, "name": "hundred", "a": 100}}, // explicit key
		{"op": "insert", "values": map[string]any{"name": "auto"}},                         // generated key
		{"op": "insert", "values": map[string]any{"id": 4, "name": "four again"}},          // deleted key reused
	}
	c := opCheck(t, id, rowsOp("g", changes), "main")
	if len(c.Statements) != 3 || c.Statements[0].Estimate.Rows != 2 || c.Statements[1].Estimate.Rows != 2 || c.Statements[2].Estimate.Rows != 3 {
		t.Fatalf("check statements = %+v", c.Statements)
	}
	res, err := applyChanges(t, id, "g", changes, c.CheckID, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 7 || res.RecordID == "" {
		t.Fatalf("result = %+v", res)
	}
	if got := tableDump(t, s); got != "1:uno:11;2:two:null;3:three:3;4:four again:null;100:hundred:100;101:auto:null;" {
		t.Fatalf("after apply: %s", got)
	}
	rec := onlyRecord(t, store)
	if rec.ID != res.RecordID || rec.Kind != sqlsafety.KindRows || rec.Source != "grid" || !rec.Undoable ||
		rec.UndoState != "available" || rec.Affected != 7 || rec.Table.Name != "g" || len(rec.PrimaryKey) != 1 {
		t.Fatalf("record = %+v", rec)
	}
	_, rows, err := store.Get(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Before) != 4 || len(rows.After) != 2 || len(rows.Inserted) != 3 {
		t.Fatalf("rows = %+v", rows)
	}

	// Key 4 was deleted and inserted again: a replaced row, shown as changed.
	p := mustPreview(t, id, rec.ID)
	if p.Total != 6 || p.ConflictCount != 1 || conflictKinds(p)["4"] != "changed" {
		t.Fatalf("preview = %+v", p)
	}
	undone := mustUndo(t, id, rec.ID, "overwrite", "")
	if undone.Restored != 6 {
		t.Fatalf("undo = %+v", undone)
	}
	if got := tableDump(t, s); got != before {
		t.Fatalf("after undo: %s, want %s", got, before)
	}
	if st := undoState(t, store, rec.ID); st != "undone" {
		t.Fatalf("record state = %s", st)
	}
}

func TestApplyChangesHighRiskNeedsConfirm(t *testing.T) {
	useSafetySettings(t, sqlsafety.Settings{Enabled: true, HighRiskRows: 2, RetentionDays: 7, MaxSnapshotRows: 100000, MaxTotalBytes: 2 << 30})
	useSafetyStore(t)
	s, id := newRowsTestSession(t)
	changes := []map[string]any{
		{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"a": 7}},
		{"op": "update", "primaryKey": map[string]any{"id": 2}, "values": map[string]any{"a": 7}},
		{"op": "delete", "primaryKey": map[string]any{"id": 3}},
	}
	c := opCheck(t, id, rowsOp("g", changes), "main")
	if c.Risk != sqlsafety.RiskHigh || c.ConfirmTarget != "g" || !hasReason(c.Statements[0].Reasons, sqlsafety.ReasonOverThreshold) {
		t.Fatalf("check = %+v", c)
	}
	_, err := applyChanges(t, id, "g", changes, c.CheckID, "")
	safetyErrorOf(t, err, "confirmMismatch")
	if n := countRowsWhere(t, s, "g", "a = 7"); n != 0 {
		t.Fatalf("%d rows changed without confirmation", n)
	}
	if res := checkAndApply(t, id, "g", changes, "G"); res.Applied != 3 {
		t.Fatalf("result = %+v", res)
	}
}

func TestApplyChangesTokenBinding(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)
	s, id := newRowsTestSession(t)
	changes := []map[string]any{{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"a": 7, "name": "x"}}}

	_, err := applyChanges(t, id, "g", changes, "", "")
	safetyErrorOf(t, err, "checkRequired")

	// Changes other than the checked ones are refused.
	c := opCheck(t, id, rowsOp("g", changes), "main")
	other := []map[string]any{{"op": "update", "primaryKey": map[string]any{"id": 2}, "values": map[string]any{"a": 7, "name": "x"}}}
	_, err = applyChanges(t, id, "g", other, c.CheckID, "")
	safetyErrorOf(t, err, "checkInvalid")

	// The same changes serialized with other key order and spacing match.
	c = opCheck(t, id, rowsOp("g", changes), "main")
	raw := fmt.Sprintf(`{"changes":[{"values":{"name":"x","a":7},"primaryKey":{"id":1},"op":"update"}], "table":"g","database":"main","checkId":%q,"sessionId":%q}`, c.CheckID, id)
	if res, err := applyChangesJSON(t, raw); err != nil || res.Applied != 1 {
		t.Fatalf("reordered submit = %+v, %v", res, err)
	}
	if n := countRowsWhere(t, s, "g", "a = 7 AND name = 'x'"); n != 1 {
		t.Fatalf("updated rows = %d", n)
	}
	// Replay is refused.
	_, err = applyChangesJSON(t, raw)
	safetyErrorOf(t, err, "checkInvalid")
}

func TestApplyChangesNotUndoableWithoutKey(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s, id := newRowsTestSession(t)
	// A grid key that is not the table's primary key cannot be snapshotted by key.
	changes := []map[string]any{{"op": "update", "primaryKey": map[string]any{"name": "one"}, "values": map[string]any{"a": 70}}}
	c := opCheck(t, id, rowsOp("g", changes), "main")
	if c.Undoable || !hasReason(c.Statements[0].Reasons, sqlsafety.ReasonNoPrimaryKey) {
		t.Fatalf("check = %+v", c)
	}
	if res, err := applyChanges(t, id, "g", changes, c.CheckID, ""); err != nil || res.Applied != 1 {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	if n := countRowsWhere(t, s, "g", "a = 70"); n != 1 {
		t.Fatalf("updated rows = %d", n)
	}
	rec := onlyRecord(t, store)
	if rec.Undoable || rec.Kind != sqlsafety.KindRows || rec.UndoReason != sqlsafety.ReasonNoPrimaryKey {
		t.Fatalf("record = %+v", rec)
	}
	// Changing a key column is not undoable either.
	changes = []map[string]any{{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"id": 50}}}
	if c := opCheck(t, id, rowsOp("g", changes), "main"); c.Undoable {
		t.Fatalf("key change check = %+v", c)
	}
}

func countRowsWhere(t *testing.T, s *dbSession, table, where string) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE " + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSingleRowCommandsGuarded(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s, id := newRowsTestSession(t)
	before := tableDump(t, s)
	call := func(handler func(string, string) (string, error), a map[string]any) int64 {
		t.Helper()
		a["sessionId"], a["database"], a["table"] = id, "main", "g"
		raw, _ := json.Marshal(a)
		out, err := handler("", string(raw))
		if err != nil {
			t.Fatalf("%v: %v", a, err)
		}
		var n int64
		if err := json.Unmarshal([]byte(out), &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := call(dbUpdateCellHandler, map[string]any{"primaryKey": map[string]any{"id": 1}, "column": "a", "value": 42, "source": "ai"}); n != 1 {
		t.Fatalf("update affected %d", n)
	}
	rec := onlyRecord(t, store)
	if rec.Source != "ai" || rec.Kind != sqlsafety.KindRows || !rec.Undoable || rec.Affected != 1 {
		t.Fatalf("update record = %+v", rec)
	}
	mustUndo(t, id, rec.ID, "skipConflicts", "")
	if got := tableDump(t, s); got != before {
		t.Fatalf("after undo: %s", got)
	}

	if n := call(dbInsertRowHandler, map[string]any{"fields": map[string]any{"name": "new"}}); n != 1 {
		t.Fatalf("insert affected %d", n)
	}
	if n := call(dbDeleteRowHandler, map[string]any{"primaryKey": map[string]any{"id": 2}}); n != 1 {
		t.Fatalf("delete affected %d", n)
	}
	recs := listRecords(t, store)
	for _, r := range recs[:2] {
		if r.Source != "grid" || r.Kind != sqlsafety.KindRows || !r.Undoable {
			t.Fatalf("record = %+v", r)
		}
		mustUndo(t, id, r.ID, "skipConflicts", "")
	}
	if got := tableDump(t, s); got != before {
		t.Fatalf("after undo: %s", got)
	}

	// A single-row failure is the database's own error, not a partial result.
	raw, _ := json.Marshal(map[string]any{"sessionId": id, "database": "main", "table": "g", "primaryKey": map[string]any{"id": 1}, "column": "name", "value": nil})
	if _, err := dbUpdateCellHandler("", string(raw)); err == nil || strings.HasPrefix(err.Error(), "sqlsafety:") {
		t.Fatalf("err = %v, want the NOT NULL error", err)
	}
}

func TestApplyChangesSafetyNetDisabled(t *testing.T) {
	useSafetySettings(t, sqlsafety.Settings{Enabled: false, HighRiskRows: 1000, RetentionDays: 7, MaxSnapshotRows: 100000, MaxTotalBytes: 2 << 30})
	store := useSafetyStore(t)
	_, id := newRowsTestSession(t)
	changes := []map[string]any{{"op": "delete", "primaryKey": map[string]any{"id": 1}}}
	if res := checkAndApply(t, id, "g", changes, ""); res.Applied != 1 || res.RecordID != "" {
		t.Fatalf("result = %+v", res)
	}
	if recs := listRecords(t, store); len(recs) != 0 {
		t.Fatalf("records = %+v", recs)
	}
}

func TestApplyChangesRejectsBadChanges(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)
	_, id := newRowsTestSession(t)
	for _, changes := range [][]map[string]any{
		{},
		{{"op": "upsert", "values": map[string]any{"a": 1}}},
		{{"op": "update", "values": map[string]any{"a": 1}}},
		{{"op": "delete"}},
		{{"op": "insert"}},
	} {
		if _, err := opCheckErr(id, rowsOp("g", changes), "main"); err == nil {
			t.Fatalf("check of %v succeeded", changes)
		}
	}
}

func TestApplyChangesKeyedInsertsSnapshot(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s, id := newRowsTestSession(t)
	before := tableDump(t, s)
	changes := []map[string]any{
		{"op": "insert", "values": map[string]any{"id": 20, "name": "twenty"}},
		{"op": "insert", "values": map[string]any{"ID": 21, "name": "twenty-one", "a": 1}},
	}
	c := opCheck(t, id, rowsOp("g", changes), "main")
	if !c.Undoable || c.Statements[0].Kind != sqlsafety.KindInsert {
		t.Fatalf("check = %+v", c)
	}
	if res, err := applyChanges(t, id, "g", changes, c.CheckID, ""); err != nil || res.Applied != 2 {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	rec := onlyRecord(t, store)
	_, rows, err := store.Get(rec.ID)
	if err != nil || !rec.Undoable || len(rows.Inserted) != 2 || len(rows.Before) != 0 {
		t.Fatalf("record = %+v rows = %+v err = %v", rec, rows, err)
	}
	if res := mustUndo(t, id, rec.ID, "skipConflicts", ""); res.Restored != 2 {
		t.Fatalf("undo = %+v", res)
	}
	if got := tableDump(t, s); got != before {
		t.Fatalf("after undo: %s", got)
	}
}

func TestApplyChangesWithoutTransactionRunsInOrder(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s, id := newRowsTestSession(t)
	s.dialect = "clickhouse" // no transactions; SQLite accepts its backtick-quoted names
	changes := []map[string]any{
		{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"a": 31}},
		{"op": "update", "primaryKey": map[string]any{"id": 2}, "values": map[string]any{"name": nil}}, // NOT NULL
		{"op": "update", "primaryKey": map[string]any{"id": 3}, "values": map[string]any{"a": 33}},
	}
	c := opCheck(t, id, rowsOp("g", changes), "main")
	if c.Undoable || !hasReason(c.Statements[0].Reasons, sqlsafety.ReasonNoTransaction) {
		t.Fatalf("check = %+v", c)
	}
	_, err := applyChanges(t, id, "g", changes, c.CheckID, "")
	w := safetyErrorOf(t, err, "partial")
	if *w.Applied != 1 || *w.FailedIndex != 1 {
		t.Fatalf("partial = %+v", w)
	}
	if countRowsWhere(t, s, "g", "id = 1 AND a = 31") != 1 || countRowsWhere(t, s, "g", "id = 3 AND a = 33") != 0 {
		t.Fatalf("applied changes: %s", tableDump(t, s))
	}
	rec := onlyRecord(t, store)
	if rec.Undoable || rec.Kind != sqlsafety.KindRows || rec.UndoReason != sqlsafety.ReasonNoTransaction || rec.Affected != 1 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestApplyChangesSameRowTwice(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s, id := newRowsTestSession(t)
	before := tableDump(t, s)
	changes := []map[string]any{
		{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"a": 50}},
		{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"name": "again"}},
	}
	if res := checkAndApply(t, id, "g", changes, ""); res.Applied != 2 {
		t.Fatalf("result = %+v", res)
	}
	rec := onlyRecord(t, store)
	if !rec.Undoable || rec.Affected != 1 {
		t.Fatalf("record = %+v", rec)
	}
	mustUndo(t, id, rec.ID, "skipConflicts", "")
	if got := tableDump(t, s); got != before {
		t.Fatalf("after undo: %s", got)
	}
}

func TestApplyChangesOverlappingKeysNotUndoable(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s, id := newRowsTestSession(t)
	for _, tc := range []struct {
		name    string
		changes []map[string]any
		want    string // the row afterwards ("" when absent)
	}{
		{"update then delete", []map[string]any{
			{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"a": 9}},
			{"op": "delete", "primaryKey": map[string]any{"id": 1}},
		}, ""},
		{"delete, insert, update", []map[string]any{
			{"op": "delete", "primaryKey": map[string]any{"id": 4}},
			{"op": "insert", "values": map[string]any{"id": 4, "name": "new four"}},
			{"op": "update", "primaryKey": map[string]any{"id": 4}, "values": map[string]any{"name": "newer four"}},
		}, "4:newer four:null;"},
		{"insert then delete", []map[string]any{
			{"op": "insert", "values": map[string]any{"id": 100, "name": "hundred"}},
			{"op": "delete", "primaryKey": map[string]any{"id": 100}},
		}, ""},
	} {
		c := opCheck(t, id, rowsOp("g", tc.changes), "main")
		if c.Undoable || !hasReason(c.Statements[0].Reasons, sqlsafety.ReasonRowChangedTwice) {
			t.Fatalf("%s: check = %+v", tc.name, c)
		}
		if res, err := applyChanges(t, id, "g", tc.changes, c.CheckID, ""); err != nil || res.Applied != len(tc.changes) {
			t.Fatalf("%s: apply = %+v, %v", tc.name, res, err)
		}
		if rec := listRecords(t, store)[0]; rec.Undoable || rec.Kind != sqlsafety.KindRows {
			t.Fatalf("%s: record = %+v", tc.name, rec)
		}
		key := tc.changes[0]["primaryKey"]
		if key == nil {
			key = map[string]any{"id": tc.changes[0]["values"].(map[string]any)["id"]}
		}
		got := ""
		for _, row := range splitDump(tableDump(t, s)) {
			if row != "" && strings.HasPrefix(row, fmt.Sprint(key.(map[string]any)["id"])+":") {
				got = row + ";"
			}
		}
		if got != tc.want {
			t.Fatalf("%s: row = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Still all or nothing.
	before := tableDump(t, s)
	changes := []map[string]any{
		{"op": "insert", "values": map[string]any{"id": 200, "name": "x"}},
		{"op": "delete", "primaryKey": map[string]any{"id": 200}},
		{"op": "update", "primaryKey": map[string]any{"id": 2}, "values": map[string]any{"name": nil}},
	}
	c := opCheck(t, id, rowsOp("g", changes), "main")
	_, err := applyChanges(t, id, "g", changes, c.CheckID, "")
	if w := safetyErrorOf(t, err, "partial"); *w.Applied != 0 || *w.FailedIndex != 2 {
		t.Fatalf("partial = %+v", w)
	}
	if got := tableDump(t, s); got != before {
		t.Fatalf("table changed: %s", got)
	}
}

func splitDump(dump string) []string { return strings.Split(dump, ";") }

// useRowsStepsHook lets a test alter a group's steps before they run.
func useRowsStepsHook(t *testing.T, hook func(steps []guardedStep)) {
	t.Helper()
	rowsStepsHook = hook
	t.Cleanup(func() { rowsStepsHook = nil })
}

func TestApplyChangesSnapshotFailureIsPartial(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s, id := newRowsTestSession(t)
	before := tableDump(t, s)
	changes := []map[string]any{
		{"op": "delete", "primaryKey": map[string]any{"id": 5}},
		{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"a": 9}},
		{"op": "update", "primaryKey": map[string]any{"id": 2}, "values": map[string]any{"a": 9}},
		{"op": "insert", "values": map[string]any{"id": 100, "name": "hundred"}},
	}
	for _, tc := range []struct {
		name      string
		breakStep func(steps []guardedStep)
		index     int
	}{
		{"update snapshot", func(steps []guardedStep) { steps[1].Snapshot = "SELECT * FROM no_such_table" }, 1},
		{"insert after-image", func(steps []guardedStep) { steps[2].After = "SELECT * FROM no_such_table" }, 3},
	} {
		useRowsStepsHook(t, tc.breakStep)
		c := opCheck(t, id, rowsOp("g", changes), "main")
		_, err := applyChanges(t, id, "g", changes, c.CheckID, "")
		w := safetyErrorOf(t, err, "partial")
		if *w.Applied != 0 || *w.FailedIndex != tc.index || !strings.Contains(w.Message, "no_such_table") {
			t.Fatalf("%s: partial = %+v", tc.name, w)
		}
		if got := tableDump(t, s); got != before {
			t.Fatalf("%s: table changed: %s", tc.name, got)
		}
	}
	if recs := listRecords(t, store); len(recs) != 0 {
		t.Fatalf("records = %+v", recs)
	}
}

func TestApplyChangesUndoConflicts(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	s, id := newRowsTestSession(t)
	before := tableDump(t, s)
	changes := []map[string]any{
		{"op": "delete", "primaryKey": map[string]any{"id": 5}},
		{"op": "delete", "primaryKey": map[string]any{"id": 4}},
		{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"a": 10}},
		{"op": "update", "primaryKey": map[string]any{"id": 2}, "values": map[string]any{"a": 20}},
		{"op": "insert", "values": map[string]any{"id": 100, "name": "hundred"}},
		{"op": "insert", "values": map[string]any{"id": 101, "name": "hundred one"}},
	}
	rec := checkAndApply(t, id, "g", changes, "")
	for _, stmt := range []string{
		`UPDATE g SET a = 999 WHERE id = 1`,              // updated row changed again
		`INSERT INTO g (id, name, a) VALUES (5, 'x', 0)`, // deleted key exists again
		`DELETE FROM g WHERE id = 100`,                   // inserted row gone
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	p := mustPreview(t, id, rec.RecordID)
	kinds := conflictKinds(p)
	if p.Total != 6 || p.ConflictCount != 3 || kinds["1"] != "changed" || kinds["5"] != "exists" || kinds["100"] != "missing" {
		t.Fatalf("preview = %+v", p)
	}
	if res := mustUndo(t, id, rec.RecordID, "skipConflicts", ""); res.Restored != 3 || res.Skipped != 3 {
		t.Fatalf("skipConflicts = %+v", res)
	}
	if st := undoState(t, store, rec.RecordID); st != "partial" {
		t.Fatalf("state = %s", st)
	}
	if res := mustUndo(t, id, rec.RecordID, "overwrite", ""); res.Restored != 2 || res.Skipped != 1 {
		t.Fatalf("overwrite = %+v", res)
	}
	if got := tableDump(t, s); got != before {
		t.Fatalf("after undo: %s, want %s", got, before)
	}
}

func TestSingleRowReconfirmResubmits(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	useSafetyRecords(t, &failingStore{inner: store, err: sqlsafety.ErrOverStoreLimit})
	s, id := newRowsTestSession(t)
	a := map[string]any{"sessionId": id, "database": "main", "table": "g", "primaryKey": map[string]any{"id": 1}, "column": "a", "value": 77, "source": "ai"}
	raw, _ := json.Marshal(a)
	_, err := dbUpdateCellHandler("", string(raw))
	w := safetyErrorOf(t, err, "reconfirm")
	if w.Check == nil || w.Check.Undoable || !hasReason(w.Check.Statements[0].Reasons, sqlsafety.ReasonOverSnapshotLimit) {
		t.Fatalf("reconfirm = %+v", w)
	}
	if n := countRowsWhere(t, s, "g", "a = 77"); n != 0 {
		t.Fatalf("changed before resubmit: %d", n)
	}
	a["checkId"] = w.Check.CheckID
	raw, _ = json.Marshal(a)
	if out, err := dbUpdateCellHandler("", string(raw)); err != nil || out != "1" {
		t.Fatalf("resubmit = %s, %v", out, err)
	}
	if n := countRowsWhere(t, s, "g", "id = 1 AND a = 77"); n != 1 {
		t.Fatalf("updated rows = %d", n)
	}
	if rec := onlyRecord(t, store); rec.Undoable || rec.Source != "ai" {
		t.Fatalf("record = %+v", rec)
	}
	// The reissued check is spent.
	if _, err := dbUpdateCellHandler("", string(raw)); err == nil {
		t.Fatal("replayed checkId accepted")
	}
}

func TestApplyChangesTokenBindsNamespace(t *testing.T) {
	useSafetySettings(t, sqlsafety.DefaultSettings())
	useSafetyStore(t)
	_, id := newRowsTestSession(t)
	changes := []map[string]any{{"op": "update", "primaryKey": map[string]any{"id": 1}, "values": map[string]any{"a": 7}}}
	op := rowsOp("g", changes)
	op["namespace"] = "ns-a"
	c := opCheck(t, id, op, "main")
	submit := func(namespace string) error {
		a := map[string]any{"sessionId": id, "database": "main", "table": "g", "changes": changes, "namespace": namespace}
		_, err := applyChangesJSON(t, withToken(a, c.CheckID, ""))
		return err
	}
	// A token checked for one vector namespace does not write another.
	safetyErrorOf(t, submit("ns-b"), "checkInvalid")
	safetyErrorOf(t, submit(""), "checkInvalid")
	if err := submit("ns-a"); err != nil {
		t.Fatalf("submit to the checked namespace: %v", err)
	}
}
