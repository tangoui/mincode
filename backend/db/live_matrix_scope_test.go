package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"mincode/app/backend/db/sqlsafety"
)

// Additional SQL 安全网 steps of TestLiveDatabaseMatrix: a read without a
// token, a grid group (db_apply_changes) and its undo, an import with
// explicit keys and its undo, and the name-resolution probes (scopes): a
// DELETE against a table that does not exist in the selected database, and
// against tables in a non-default database / schema or with quoted
// mixed-case names.

// liveScope is one name-resolution probe. Setup creates a table holding the
// rows id 1 and 2 (run with SetupDatabase, default the case database);
// the probe checks and runs "DELETE FROM <Target> WHERE id <= 1" with the
// database argument Database. Count counts the rows with id 1 (run with the
// case database). Expect is "resolved" (low risk, exact count 1, undoable,
// undo restores the row) or "notFound" (high risk, not undoable, reason
// tableNotFound; executing it must not change Count).
type liveScope struct {
	Name          string   `json:"name"`
	Setup         []string `json:"setup"`
	SetupDatabase string   `json:"setupDatabase"`
	Cleanup       []string `json:"cleanup"`
	Database      string   `json:"database"`
	Target        string   `json:"target"`
	Count         string   `json:"count"`
	Expect        string   `json:"expect"`
	// Prime is a read run with PrimeDatabase right before the check (to
	// leave a pooled connection in another database).
	Prime         string `json:"prime"`
	PrimeDatabase string `json:"primeDatabase"`
	// SingleConn limits the session pool to one connection during the
	// probe, so the primed connection is the one the check and execution use.
	SingleConn bool `json:"singleConn"`
}

// liveExtraCase holds the per-case switches of these steps.
type liveExtraCase struct {
	// SingleRowInsert: the dialect has no multi-row VALUES (Firebird).
	SingleRowInsert bool `json:"singleRowInsert"`
	// IDColumn / NameColumn: the case table's columns as metadata spells
	// them (default id / name), for grid changes and imports.
	IDColumn   string      `json:"idColumn"`
	NameColumn string      `json:"nameColumn"`
	Scopes     []liveScope `json:"scopes"`
	// NoGrid skips db_apply_changes; NoKeyedImport skips the keyed import.
	NoGrid        bool `json:"noGrid"`
	NoKeyedImport bool `json:"noKeyedImport"`
	// NonSQL cases (kind "nonsql"): ReadQuery runs without a token,
	// WriteQueries each need one and are recorded (when at all) as not
	// undoable; GridChanges is a db_apply_changes group on Table.
	ReadQuery    string           `json:"readQuery"`
	WriteQueries []string         `json:"writeQueries"`
	GridChanges  []map[string]any `json:"gridChanges"`
	GridSetup    []string         `json:"gridSetup"`
	// Fidelity probes: a row whose values must survive backup and undo.
	Fidelity []liveFidelity `json:"fidelity"`
}

// liveFidelity: Setup creates a table with row id 1; Write changes it
// through the safety net (any undoable statement); after undo Read must
// return Want (its first row rendered with fmt "%v" per cell, joined by "|").
type liveFidelity struct {
	Name    string   `json:"name"`
	Setup   []string `json:"setup"`
	Cleanup []string `json:"cleanup"`
	Write   string   `json:"write"`
	Read    string   `json:"read"`
	Want    string   `json:"want"`
}

// seedRows inserts rows 2 'b' and 3 'c' into the case table.
func (l *liveSafety) seedRows() error {
	if !l.c.SingleRowInsert {
		_, err := l.exec(fmt.Sprintf("INSERT INTO %s (id, name) VALUES (2, 'b'), (3, 'c')", l.table))
		return err
	}
	for _, v := range []string{"(2, 'b')", "(3, 'c')"} {
		if _, err := l.exec(fmt.Sprintf("INSERT INTO %s (id, name) VALUES %s", l.table, v)); err != nil {
			return err
		}
	}
	return nil
}

func (l *liveSafety) runIn(database, query, checkID, confirm string) (json.RawMessage, error) {
	a := map[string]any{"sessionId": l.session, "query": query}
	if database != "" {
		a["database"] = database
	}
	if checkID != "" {
		a["checkId"] = checkID
	}
	if confirm != "" {
		a["confirmText"] = confirm
	}
	return dispatchErr("db_execute_query", a)
}

func (l *liveSafety) execIn(database, query string) (json.RawMessage, error) {
	a := map[string]any{"sessionId": l.session, "query": query}
	if database != "" {
		a["database"] = database
	}
	raw, _, err := liveGuarded("db_execute_query", a, database, nil)
	return raw, err
}

func (l *liveSafety) countQuery(query string) (int64, error) {
	raw, err := l.exec(query)
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

func liveReasons(c *sqlsafety.Check) []sqlsafety.Reason {
	var out []sqlsafety.Reason
	for _, sc := range c.Statements {
		out = append(out, sc.Reasons...)
	}
	return out
}

// liveExtraSteps runs the read, grid, keyed-import and scope steps; the
// case table holds only row id 1 ("已更新 ✓") before and after.
func liveExtraSteps(t *testing.T, l *liveSafety, mark func(string, error) bool) {
	// read: plain SELECT runs without any token.
	_, err := l.run(fmt.Sprintf("SELECT name FROM %s WHERE id = 1", l.table), "", "")
	mark("read", err)

	if !l.c.NoGrid {
		mark("grid", liveGrid(t, l))
	}
	if !l.c.NoKeyedImport {
		mark("import-keyed", liveKeyedImport(t, l))
	}
	for _, sc := range l.c.Scopes {
		mark("scope-"+sc.Name, liveScopeProbe(t, l, sc))
	}
	for _, f := range l.c.Fidelity {
		mark("fidelity-"+f.Name, liveFidelityProbe(t, l, f))
	}
}

func (l *liveSafety) firstRow(query string) (string, error) {
	raw, err := l.exec(query)
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
	cells := make([]string, len(res.Rows[0]))
	for i, v := range res.Rows[0] {
		cells[i] = fmt.Sprint(v)
	}
	return strings.Join(cells, "|"), nil
}

func liveFidelityProbe(t *testing.T, l *liveSafety, f liveFidelity) error {
	for _, s := range f.Setup {
		if _, err := l.exec(s); err != nil {
			return fmt.Errorf("setup %q: %w", s, err)
		}
	}
	defer func() {
		for _, s := range f.Cleanup {
			_, _ = l.exec(s)
		}
	}()
	before, err := l.firstRow(f.Read)
	if err != nil {
		return err
	}
	if before != f.Want {
		return fmt.Errorf("before = %q, want %q", before, f.Want)
	}
	seen := l.ids()
	if _, err := l.exec(f.Write); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	rec, err := l.newRecord(seen)
	if err != nil {
		return err
	}
	if !rec.Undoable {
		return fmt.Errorf("record not undoable: %+v", rec)
	}
	p, err := l.preview(rec.ID)
	if err != nil {
		return fmt.Errorf("preview: %w", err)
	}
	if _, err := dispatchErr("db_safety_undo", map[string]any{"sessionId": l.session, "id": rec.ID, "mode": "skipConflicts",
		"previewToken": p.PreviewToken, "confirmText": rec.Table.Name}); err != nil {
		return fmt.Errorf("undo: %w", err)
	}
	after, err := l.firstRow(f.Read)
	if err != nil {
		return err
	}
	t.Logf("fidelity %s: before %q after undo %q", f.Name, before, after)
	if after != f.Want {
		return fmt.Errorf("after undo = %q, want %q", after, f.Want)
	}
	return nil
}

func (c liveExtraCase) columns() (id, name string) {
	id, name = c.IDColumn, c.NameColumn
	if id == "" {
		id = "id"
	}
	if name == "" {
		name = "name"
	}
	return id, name
}

func liveGrid(t *testing.T, l *liveSafety) error {
	id, name := l.c.columns()
	if err := l.seedRows(); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	defer func() { _, _ = l.exec(fmt.Sprintf("DELETE FROM %s WHERE id <> 1", l.table)) }()
	changes := []map[string]any{
		{"op": "delete", "primaryKey": map[string]any{id: 2}},
		{"op": "update", "primaryKey": map[string]any{id: 3}, "values": map[string]any{name: "g3"}},
		{"op": "insert", "values": map[string]any{id: 4, name: "g4"}},
	}
	args := map[string]any{"sessionId": l.session, "database": l.c.Database, "table": l.c.Table, "changes": changes}
	op := map[string]any{"kind": "rows", "table": l.c.Table, "changes": changes}
	if l.c.Namespace != "" {
		args["namespace"], op["namespace"] = l.c.Namespace, l.c.Namespace
	}
	// gate
	_, err := dispatchErr("db_apply_changes", args)
	if w, ok := liveSafetyError(err); !ok || w.Code != safetyCodeCheckRequired {
		return fmt.Errorf("without token: %v", err)
	}
	seen := l.ids()
	if _, _, err := liveGuarded("db_apply_changes", args, l.c.Database, op); err != nil {
		return err
	}
	if err := l.wantCount(l.table, 3); err != nil {
		return err
	}
	if n, err := l.countQuery(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = 3 AND name = 'g3'", l.table)); err != nil || n != 1 {
		return fmt.Errorf("update not applied: %d %v", n, err)
	}
	rec, err := l.newRecord(seen)
	if err != nil {
		return err
	}
	if !rec.Undoable || rec.Kind != sqlsafety.KindRows || rec.Affected != 3 {
		return fmt.Errorf("record = %+v", rec)
	}
	if _, res, err := l.undo(rec.ID, "skipConflicts"); err != nil || res.Restored != 3 {
		return fmt.Errorf("undo = %+v, %v", res, err)
	}
	if err := l.wantCount(l.table, 3); err != nil {
		return err
	}
	n, err := l.countQuery(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE (id = 2 AND name = 'b') OR (id = 3 AND name = 'c')", l.table))
	if err == nil && n != 2 {
		err = fmt.Errorf("undo left %d of rows 2/3 restored", n)
	}
	return err
}

func liveKeyedImport(t *testing.T, l *liveSafety) error {
	id, name := l.c.columns()
	data := fmt.Sprintf(`[{%q:7,%q:"i7"},{%q:8,%q:"i8"}]`, id, name, id, name)
	args := map[string]any{"sessionId": l.session, "database": l.c.Database, "table": l.c.Table, "format": "json", "data": data}
	op := map[string]any{"kind": "import", "table": l.c.Table, "format": "json", "data": data}
	_, err := dispatchErr("db_import_data", args)
	if w, ok := liveSafetyError(err); !ok || w.Code != safetyCodeCheckRequired {
		return fmt.Errorf("without token: %v", err)
	}
	seen := l.ids()
	if _, _, err := liveGuarded("db_import_data", args, l.c.Database, op); err != nil {
		return err
	}
	defer func() { _, _ = l.exec(fmt.Sprintf("DELETE FROM %s WHERE id <> 1", l.table)) }()
	if err := l.wantCount(l.table, 3); err != nil {
		return err
	}
	rec, err := l.newRecord(seen)
	if err != nil {
		return err
	}
	t.Logf("keyed import record: kind=%s undoable=%v reason=%s affected=%d", rec.Kind, rec.Undoable, rec.UndoReason, rec.Affected)
	if !rec.Undoable || rec.Affected != 2 {
		return fmt.Errorf("record = %+v", rec)
	}
	if _, res, err := l.undo(rec.ID, "skipConflicts"); err != nil || res.Restored != 2 {
		return fmt.Errorf("undo = %+v, %v", res, err)
	}
	return l.wantCount(l.table, 1)
}

func liveScopeProbe(t *testing.T, l *liveSafety, sc liveScope) error {
	setupDB := sc.SetupDatabase
	if setupDB == "" {
		setupDB = l.c.Database
	}
	for _, s := range sc.Setup {
		if _, err := l.execIn(setupDB, s); err != nil {
			return fmt.Errorf("setup %q: %w", s, err)
		}
	}
	defer func() {
		for _, s := range sc.Cleanup {
			if _, err := l.execIn(setupDB, s); err != nil {
				t.Logf("scope cleanup %q: %v", s, err)
			}
		}
	}()
	before := int64(-1)
	if sc.Count != "" {
		n, err := l.countQuery(sc.Count)
		if err != nil {
			return fmt.Errorf("count: %w", err)
		}
		before = n
	}
	if sc.SingleConn {
		if s, err := requireSession(l.session); err == nil {
			s.db.SetMaxOpenConns(1)

			defer func() {
				if s, err := requireSession(l.session); err == nil {
					s.db.SetMaxOpenConns(remoteSQLMaxOpen)
				}
			}()
		}
	}
	if sc.Prime != "" {
		if _, err := l.runIn(sc.PrimeDatabase, sc.Prime, "", ""); err != nil {
			return fmt.Errorf("prime: %w", err)
		}
	}
	query := fmt.Sprintf("DELETE FROM %s WHERE id <= 1", sc.Target)
	c, _, err := liveCheck(l.session, sc.Database, query, nil)
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}
	st := c.Statements[0]
	t.Logf("scope %s: database=%q target=%s → risk=%s undoable=%v estimate=%+v reasons=%v confirm=%q",
		sc.Name, sc.Database, sc.Target, c.Risk, c.Undoable, st.Estimate, liveReasons(c), c.ConfirmTarget)
	switch sc.Expect {
	case "resolved":
		if c.Risk != sqlsafety.RiskLow || !c.Undoable || st.Estimate.Rows != 1 || st.Estimate.Source != "count" {
			return fmt.Errorf("check = risk %s undoable %v estimate %+v reasons %v", c.Risk, c.Undoable, st.Estimate, liveReasons(c))
		}
		seen := l.ids()
		if _, err := l.runIn(sc.Database, query, c.CheckID, c.ConfirmTarget); err != nil {
			return fmt.Errorf("execute: %w", err)
		}
		if n, err := l.countQuery(sc.Count); err != nil || n != before-1 {
			return fmt.Errorf("after delete count %d, %v", n, err)
		}
		rec, err := l.newRecord(seen)
		if err != nil {
			return err
		}
		if !rec.Undoable || rec.Affected != 1 {
			return fmt.Errorf("record = %+v", rec)
		}
		p, err := l.preview(rec.ID)
		if err != nil {
			return fmt.Errorf("preview: %w", err)
		}
		raw, err := dispatchErr("db_safety_undo", map[string]any{"sessionId": l.session, "id": rec.ID, "mode": "skipConflicts",
			"previewToken": p.PreviewToken, "confirmText": rec.Table.Name})
		if err != nil {
			return fmt.Errorf("undo: %w", err)
		}
		var res safetyUndoResult
		if err := json.Unmarshal(raw, &res); err != nil || res.Restored != 1 {
			return fmt.Errorf("undo = %s, %v", raw, err)
		}
		if n, err := l.countQuery(sc.Count); err != nil || n != before {
			return fmt.Errorf("after undo count %d, %v", n, err)
		}
		return nil
	case "notFound":
		reasons := liveReasons(c)
		var checkErr error
		if c.Risk != sqlsafety.RiskHigh || c.Undoable || !hasLiveReason(reasons, liveReasonTableNotFound) || hasLiveReason(reasons, sqlsafety.ReasonUnparsed) {
			checkErr = fmt.Errorf("check = risk %s undoable %v estimate %+v reasons %v", c.Risk, c.Undoable, st.Estimate, reasons)
		}
		_, err := l.runIn(sc.Database, query, c.CheckID, c.ConfirmTarget)
		t.Logf("scope %s execute: %v", sc.Name, err)
		if sc.Count != "" {
			if n, cerr := l.countQuery(sc.Count); cerr != nil || n != before {
				return fmt.Errorf("unresolved delete changed the table: count %d → %d (%v; exec err %v)", before, n, cerr, err)
			}
		}
		return checkErr
	case "observe":
		// Only report what happens; executing must either fail or delete
		// exactly the probed row, and an undoable record must undo.
		seen := l.ids()
		_, err := l.runIn(sc.Database, query, c.CheckID, c.ConfirmTarget)
		t.Logf("scope %s execute: %v", sc.Name, err)
		if sc.Count == "" {
			return nil
		}
		n, cerr := l.countQuery(sc.Count)
		if cerr != nil {
			return cerr
		}
		if err != nil {
			if n != before {
				return fmt.Errorf("failed delete changed the table: %d → %d", before, n)
			}
			return nil
		}
		if n != before-1 {
			return fmt.Errorf("delete count %d → %d", before, n)
		}
		fresh, _ := l.newRecords(seen)
		for _, rec := range fresh {
			if rec.Undoable {
				p, err := l.preview(rec.ID)
				if err != nil {
					return fmt.Errorf("preview: %w", err)
				}
				if _, err := dispatchErr("db_safety_undo", map[string]any{"sessionId": l.session, "id": rec.ID, "mode": "skipConflicts",
					"previewToken": p.PreviewToken, "confirmText": rec.Table.Name}); err != nil {
					return fmt.Errorf("undo: %w", err)
				}
				if n, err := l.countQuery(sc.Count); err != nil || n != before {
					return fmt.Errorf("after undo count %d, %v", n, err)
				}
			}
		}
		return nil
	}
	return fmt.Errorf("unknown expect %q", sc.Expect)
}

func hasLiveReason(reasons []sqlsafety.Reason, r sqlsafety.Reason) bool {
	for _, have := range reasons {
		if have == r {
			return true
		}
	}
	return false
}

// liveNonSQL runs the token gate on a store that cannot be undone.
func liveNonSQL(t *testing.T, session string, c liveCase, store *sqlsafety.Store, mark func(string, error) bool) []string {
	db := c.Database
	if c.ReadQuery != "" {
		_, err := dispatchErr("db_execute_query", map[string]any{"sessionId": session, "query": c.ReadQuery, "database": &db})
		mark("read", err)
		check, _, err := liveCheck(session, c.Database, c.ReadQuery, nil)
		if err == nil && (check.Risk != sqlsafety.RiskNone || check.CheckID != "") {
			err = fmt.Errorf("read check = %+v", check)
		}
		mark("read-check", err)
	}
	for i, q := range c.WriteQueries {
		step := fmt.Sprintf("write%d", i+1)
		args := map[string]any{"sessionId": session, "query": q, "database": &db}
		_, err := dispatchErr("db_execute_query", args)
		if w, ok := liveSafetyError(err); !ok || w.Code != safetyCodeCheckRequired {
			mark(step+"-gate", fmt.Errorf("without token: %v", err))
			continue
		}
		mark(step+"-gate", nil)
		check, _, err := liveCheck(session, c.Database, q, nil)
		if err == nil {
			t.Logf("%s check: risk=%s undoable=%v reasons=%v", step, check.Risk, check.Undoable, liveReasons(check))
			reasons := liveReasons(check)
			if check.Undoable || check.CheckID == "" ||
				(!hasLiveReason(reasons, sqlsafety.ReasonNotUndoableStore) && !hasLiveReason(reasons, sqlsafety.ReasonNoTransaction)) {
				err = fmt.Errorf("check = %+v", check)
			}
		}
		if mark(step+"-check", err) {
			args["checkId"], args["confirmText"] = check.CheckID, check.ConfirmTarget
			_, err = dispatchErr("db_execute_query", args)
			mark(step+"-exec", err)
		}
	}
	if len(c.GridChanges) > 0 {
		mark("grid", liveNonSQLGrid(t, session, c, store))
	}
	return nil
}

func liveNonSQLGrid(t *testing.T, session string, c liveCase, store *sqlsafety.Store) error {
	args := map[string]any{"sessionId": session, "database": c.Database, "table": c.Table, "changes": c.GridChanges}
	op := map[string]any{"kind": "rows", "table": c.Table, "changes": c.GridChanges}
	_, err := dispatchErr("db_apply_changes", args)
	if w, ok := liveSafetyError(err); !ok || w.Code != safetyCodeCheckRequired {
		return fmt.Errorf("without token: %v", err)
	}
	before, _ := store.List("", 0)
	seen := map[string]bool{}
	for _, r := range before {
		seen[r.ID] = true
	}
	_, check, err := liveGuarded("db_apply_changes", args, c.Database, op)
	if err != nil {
		return err
	}
	t.Logf("grid check: risk=%s undoable=%v reasons=%v", check.Risk, check.Undoable, liveReasons(check))
	after, _ := store.List("", 0)
	var fresh []sqlsafety.Record
	for _, r := range after {
		if !seen[r.ID] && r.Source != "undo" {
			fresh = append(fresh, r)
		}
	}
	if !check.Undoable {
		for _, r := range fresh {
			if r.Undoable {
				return fmt.Errorf("record undoable: %+v", r)
			}
		}
		return nil
	}
	// A SQL store underneath (pgvector): the group must undo.
	if len(fresh) != 1 || !fresh[0].Undoable {
		return fmt.Errorf("records = %+v", fresh)
	}
	raw, err := dispatchErr("db_safety_undo_preview", map[string]any{"sessionId": session, "id": fresh[0].ID})
	if err != nil {
		return fmt.Errorf("preview: %w", err)
	}
	var p safetyUndoPreview
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	raw, err = dispatchErr("db_safety_undo", map[string]any{"sessionId": session, "id": fresh[0].ID, "mode": "skipConflicts",
		"previewToken": p.PreviewToken, "confirmText": c.Table})
	if err != nil {
		return fmt.Errorf("undo: %w", err)
	}
	var res safetyUndoResult
	if err := json.Unmarshal(raw, &res); err != nil || res.Restored != len(c.GridChanges) {
		return fmt.Errorf("undo = %s, %v", raw, err)
	}
	t.Logf("grid undo restored %d", res.Restored)
	return nil
}

// liveReasonTableNotFound is the reason a check gives a target table the
// selected database does not contain.
const liveReasonTableNotFound = sqlsafety.Reason("tableNotFound")

// liveWarnSteps covers a SQL engine the safety net cannot back up (no
// primary keys or no transactions): reads run without a token, writes need
// one, high risk needs the typed table, records are not undoable and a
// missing table is reported as such. The case table has columns id, name.
func liveWarnSteps(t *testing.T, l *liveSafety, mark func(string, error) bool) []string {
	insert := fmt.Sprintf("INSERT INTO %s (id, name) VALUES (1, 'a'), (2, 'b'), (3, 'c')", l.table)
	_, err := l.run(insert, "", "")
	if w, ok := liveSafetyError(err); !ok || w.Code != safetyCodeCheckRequired {
		err = fmt.Errorf("insert without token: %v", err)
	} else {
		_, err = l.exec(insert)
	}
	mark("gate", err)
	_, err = l.run(fmt.Sprintf("SELECT name FROM %s WHERE id = 1", l.table), "", "")
	mark("read", err)

	update := fmt.Sprintf("UPDATE %s SET name = 'safe' WHERE id = 1", l.table)
	c, _, err := liveCheck(l.session, l.c.Database, update, nil)
	if err == nil {
		t.Logf("warn check: risk=%s undoable=%v estimate=%+v reasons=%v", c.Risk, c.Undoable, c.Statements[0].Estimate, liveReasons(c))
		if c.Undoable || c.CheckID == "" {
			err = fmt.Errorf("check = %+v", c)
		}
	}
	if mark("check", err) {
		seen := l.ids()
		_, err = l.run(update, c.CheckID, c.ConfirmTarget)
		if err == nil {
			err = l.wantValue("safe")
		}
		if err == nil {
			var fresh []sqlsafety.Record
			fresh, err = l.newRecords(seen)
			for _, r := range fresh {
				if r.Undoable {
					err = fmt.Errorf("record undoable: %+v", r)
				}
			}
			t.Logf("warn exec records: %d", len(fresh))
		}
		mark("exec", err)
	}
	deleteAll := "DELETE FROM " + l.table
	c, _, err = liveCheck(l.session, l.c.Database, deleteAll, nil)
	if err == nil && (c.Risk != sqlsafety.RiskHigh || c.Undoable) {
		err = fmt.Errorf("check = %+v", c)
	}
	if err == nil {
		_, err = l.run(deleteAll, c.CheckID, "")
		if w, ok := liveSafetyError(err); !ok || w.Code != safetyCodeConfirmMismatch {
			err = fmt.Errorf("without confirmText: %v", err)
		} else {
			err = nil
		}
	}
	mark("high-reject", err)
	if err == nil {
		c, _, err = liveCheck(l.session, l.c.Database, deleteAll, nil)
		if err == nil {
			_, err = l.run(deleteAll, c.CheckID, c.ConfirmTarget)
		}
		mark("high-exec", err)
	}
	for _, sc := range l.c.Scopes {
		mark("scope-"+sc.Name, liveScopeProbe(t, l, sc))
	}
	return nil
}
