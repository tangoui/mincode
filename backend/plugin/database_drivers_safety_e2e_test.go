package plugin

// Opt-in end-to-end check of the SQL 安全网 through real driver plugins: each
// case installs a driver package with this package's manager, connects to a
// disposable server and runs the safety-net steps through the db_* command
// handlers the desktop dispatches.
//
//	MINCODE_E2E_DB_DRIVERS=1          required gate (shared with TestDatabaseDriverPluginsEndToEnd)
//	MINCODE_E2E_SAFETY_DRIVERS=<file> JSON array of e2eSafetyCase
//
// A driver declaring transaction.snapshotWrite must back up and undo; one
// without it (warnOnly) must still require a token and record the write as
// not undoable.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/db"
	"mincode/app/backend/db/sqlsafety"
	"mincode/app/backend/license"
)

// e2eSafetyCase is one driver and server. The throwaway table has an
// integer key IDColumn and a text column NameColumn; Seed inserts rows 1..3
// with names 'a', 'b', 'c'.
type e2eSafetyCase struct {
	Name       string                `json:"name"`
	Package    string                `json:"package"`
	PluginID   string                `json:"pluginId"`
	DriverID   string                `json:"driverId"`
	Config     db.DbConnectionConfig `json:"config"`
	Database   string                `json:"database"`
	Table      string                `json:"table"`     // as metadata names it
	Qualified  string                `json:"qualified"` // as statements name it
	IDColumn   string                `json:"idColumn"`
	NameColumn string                `json:"nameColumn"`
	CreateSQL  string                `json:"createSql"`
	DropSQL    string                `json:"dropSql"`
	Seed       []string              `json:"seed"`
	// UpdateSQL sets row 1's name to 'safe' (default UPDATE … WHERE id = 1).
	UpdateSQL string `json:"updateSql"`
	// UpdateRisk is the risk the check gives UpdateSQL (default low; Phoenix
	// UPSERT is not parsed, so it is high with the CONFIRM literal).
	UpdateRisk sqlsafety.Risk `json:"updateRisk"`
	// WarnOnly: the driver does not declare transaction.snapshotWrite.
	WarnOnly bool `json:"warnOnly"`
	// ConfirmLiteral: the dialect is not parsed (sqlsafety family none), so
	// high risk asks for the CONFIRM literal instead of the table name.
	ConfirmLiteral bool `json:"confirmLiteral"`
	// NoGrid skips db_apply_changes (drivers without rows.* features).
	NoGrid bool `json:"noGrid"`
	// Probe-only case (no table steps): Reads run without a token; Writes
	// are refused without one, then run with their check (the server may
	// still reject them) and are never recorded as undoable.
	Reads  []string `json:"reads"`
	Writes []string `json:"writes"`
	// Scopes are name-resolution probes: "DELETE FROM <Target> WHERE <id> <= 1"
	// checked and run with the database argument Database (see e2eScope).
	Scopes []e2eScope `json:"scopes"`
}

// e2eScope: Setup (run with the case database) creates a table holding
// rows 1 and 2; Count counts its rows with id 1. Expect "resolved" (exact
// estimate 1; undoable and undone unless WarnOnly) or "notFound" (high
// risk, not undoable, reason tableNotFound, nothing changed).
type e2eScope struct {
	Name     string   `json:"name"`
	Setup    []string `json:"setup"`
	Cleanup  []string `json:"cleanup"`
	Database string   `json:"database"`
	Target   string   `json:"target"`
	Count    string   `json:"count"`
	Expect   string   `json:"expect"`
}

func TestDatabaseDriverPluginsSafetyEndToEnd(t *testing.T) {
	path := os.Getenv("MINCODE_E2E_SAFETY_DRIVERS")
	if os.Getenv("MINCODE_E2E_DB_DRIVERS") != "1" || path == "" {
		t.Skip("set MINCODE_E2E_DB_DRIVERS=1 and MINCODE_E2E_SAFETY_DRIVERS")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []e2eSafetyCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	// Backups go under a throwaway user data directory.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("APPDATA", home)
	useTemporaryPluginData(t)
	previousQuota := enabledPluginQuota
	enabledPluginQuota = func() (license.QuotaPolicy, string, bool) { return license.QuotaPolicy{}, "", false }
	managerOnce.Do(func() {})
	previousManager := managerInst
	managerInst = newPluginManager()
	t.Cleanup(func() {
		for _, id := range managerInst.order {
			if record, err := managerInst.record(id); err == nil {
				_ = managerInst.stopRecord(context.Background(), record, false)
			}
		}
		managerInst = previousManager
		enabledPluginQuota = previousQuota
	})
	installed := map[string]bool{}
	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			if !installed[c.PluginID] {
				e2eInstall(t, c.Package, c.PluginID)
				installed[c.PluginID] = true
			}
			e2eSafetySteps(t, c)
		})
	}
}

// e2eSafetyError decodes a "sqlsafety:{...}" error.
func e2eSafetyError(err error) (code string, check *sqlsafety.Check) {
	if err == nil {
		return "", nil
	}
	msg := err.Error()
	i := strings.Index(msg, "sqlsafety:")
	if i < 0 {
		return "", nil
	}
	var w struct {
		Code  string           `json:"code"`
		Check *sqlsafety.Check `json:"check"`
	}
	if json.Unmarshal([]byte(msg[i+len("sqlsafety:"):]), &w) != nil {
		return "", nil
	}
	return w.Code, w.Check
}

func e2eSafetyCheck(t *testing.T, session, database, query string, op map[string]any) (*sqlsafety.Check, error) {
	t.Helper()
	a := map[string]any{"sessionId": session, "database": database}
	if op != nil {
		a["operation"] = op
	} else {
		a["query"] = query
	}
	var c sqlsafety.Check
	if err := e2eCall(t, "db_safety_check", a, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// e2eGuarded runs command; when a check is required it obtains one for op
// (or for args["query"]) and runs command again with the token, typing the
// confirmTarget for high risk.
func e2eGuarded(t *testing.T, command string, args map[string]any, database string, op map[string]any, out any) (*sqlsafety.Check, error) {
	t.Helper()
	err := e2eCall(t, command, args, out)
	code, check := e2eSafetyError(err)
	if code != "checkRequired" {
		return nil, err
	}
	if check == nil {
		query, _ := args["query"].(string)
		session, ok := args["targetSessionId"].(string)
		if !ok {
			session, _ = args["sessionId"].(string)
		}
		if check, err = e2eSafetyCheck(t, session, database, query, op); err != nil {
			return nil, err
		}
	}
	retry := map[string]any{"checkId": check.CheckID, "confirmText": check.ConfirmTarget}
	for k, v := range args {
		retry[k] = v
	}
	return check, e2eCall(t, command, retry, out)
}

type e2eSafety struct {
	t       *testing.T
	c       e2eSafetyCase
	session string
}

func (s *e2eSafety) exec(query string) (db.QueryResult, error) {
	var res db.QueryResult
	_, err := e2eGuarded(s.t, "db_execute_query", map[string]any{"sessionId": s.session, "query": query, "database": s.c.Database}, s.c.Database, nil, &res)
	return res, err
}

func (s *e2eSafety) run(query, checkID, confirm string) error {
	a := map[string]any{"sessionId": s.session, "query": query, "database": s.c.Database}
	if checkID != "" {
		a["checkId"] = checkID
	}
	if confirm != "" {
		a["confirmText"] = confirm
	}
	return e2eCall(s.t, "db_execute_query", a, nil)
}

func (s *e2eSafety) records() ([]sqlsafety.Record, error) {
	var recs []sqlsafety.Record
	err := e2eCall(s.t, "db_safety_history_list", map[string]any{"connectionId": s.c.Config.ConnectionID, "limit": 200}, &recs)
	return recs, err
}

func (s *e2eSafety) seen() map[string]bool {
	recs, err := s.records()
	if err != nil {
		s.t.Fatal(err)
	}
	out := map[string]bool{}
	for _, r := range recs {
		out[r.ID] = true
	}
	return out
}

func (s *e2eSafety) newRecord(seen map[string]bool) (sqlsafety.Record, error) {
	recs, err := s.records()
	if err != nil {
		return sqlsafety.Record{}, err
	}
	var fresh []sqlsafety.Record
	for _, r := range recs {
		if !seen[r.ID] && r.Source != "undo" {
			fresh = append(fresh, r)
		}
	}
	if len(fresh) != 1 {
		return sqlsafety.Record{}, fmt.Errorf("want one new record, got %+v", fresh)
	}
	return fresh[0], nil
}

func (s *e2eSafety) undo(id string) (int, error) {
	var p struct {
		ConflictCount int    `json:"conflictCount"`
		PreviewToken  string `json:"previewToken"`
	}
	if err := e2eCall(s.t, "db_safety_undo_preview", map[string]any{"sessionId": s.session, "id": id}, &p); err != nil {
		return 0, fmt.Errorf("preview: %w", err)
	}
	if p.ConflictCount != 0 {
		return 0, fmt.Errorf("preview conflicts %d", p.ConflictCount)
	}
	var res struct {
		Restored int `json:"restored"`
	}
	err := e2eCall(s.t, "db_safety_undo", map[string]any{"sessionId": s.session, "id": id, "mode": "skipConflicts",
		"previewToken": p.PreviewToken, "confirmText": s.c.Table}, &res)
	return res.Restored, err
}

// names returns the name column by id, as text.
func (s *e2eSafety) names() (map[string]string, error) {
	res, err := s.exec(fmt.Sprintf("SELECT %s, %s FROM %s", s.c.IDColumn, s.c.NameColumn, s.c.Qualified))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, row := range res.Rows {
		out[fmt.Sprint(row[0])] = fmt.Sprint(row[1])
	}
	return out, nil
}

func (s *e2eSafety) want(want string) error {
	got, err := s.names()
	if err != nil {
		return err
	}
	parts := []string{}
	for _, id := range []string{"1", "2", "3", "4"} {
		if v, ok := got[id]; ok {
			parts = append(parts, id+"="+v)
		}
	}
	if have := strings.Join(parts, ","); have != want {
		return fmt.Errorf("rows %s, want %s", have, want)
	}
	return nil
}

func e2eSafetySteps(t *testing.T, c e2eSafetyCase) {
	if c.Qualified == "" {
		c.Qualified = c.Table
	}
	if c.UpdateRisk == "" {
		c.UpdateRisk = sqlsafety.RiskLow
	}
	if c.UpdateSQL == "" {
		c.UpdateSQL = fmt.Sprintf("UPDATE %s SET %s = 'safe' WHERE %s = 1", c.Qualified, c.NameColumn, c.IDColumn)
	}
	if err := managerInst.grantDatabaseDriver(c.PluginID, c.DriverID, c.Config.ConnectionID); err != nil {
		t.Fatal(err)
	}
	c.Config.Driver = &db.ExternalDriverRef{PluginID: c.PluginID, DriverID: c.DriverID}
	s := &e2eSafety{t: t, c: c, session: e2eConnect(t, c.Config)}
	step := func(name string, err error) bool {
		if err != nil {
			t.Errorf("%s: %v", name, err)
			return false
		}
		t.Logf("%s: ok", name)
		return true
	}

	if c.CreateSQL == "" {
		e2eProbe(t, s, step)
		return
	}
	_, _ = s.exec(c.DropSQL)
	if _, err := s.exec(c.CreateSQL); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = s.exec(c.DropSQL) })
	seed := func() error {
		for _, q := range c.Seed {
			if _, err := s.exec(q); err != nil {
				return err
			}
		}
		return s.want("1=a,2=b,3=c")
	}
	if !step("seed", seed()) {
		return
	}

	// check + gate + exec (+ undo).
	start := time.Now()
	check, err := e2eSafetyCheck(t, s.session, c.Database, c.UpdateSQL, nil)
	t.Logf("db_safety_check through the plugin: %v", time.Since(start).Round(time.Millisecond))
	if err == nil && (check.Risk != c.UpdateRisk || check.Undoable == c.WarnOnly) {
		err = fmt.Errorf("check = %+v", check)
	}
	step("check", err)
	err = s.run(c.UpdateSQL, "", "")
	if code, _ := e2eSafetyError(err); code != "checkRequired" {
		err = fmt.Errorf("without token: %v", err)
	} else {
		err = s.want("1=a,2=b,3=c")
	}
	step("gate", err)
	seen := s.seen()
	var rec sqlsafety.Record
	if check != nil {
		err = s.run(c.UpdateSQL, check.CheckID, check.ConfirmTarget)
	}
	if err == nil {
		err = s.want("1=safe,2=b,3=c")
	}
	if err == nil {
		rec, err = s.newRecord(seen)
	}
	if err == nil && rec.Undoable == c.WarnOnly {
		err = fmt.Errorf("record = %+v", rec)
	}
	if step("exec", err) && !c.WarnOnly {
		n, err := s.undo(rec.ID)
		if err == nil && n != 1 {
			err = fmt.Errorf("restored %d", n)
		}
		if err == nil {
			err = s.want("1=a,2=b,3=c")
		}
		step("undo", err)
	} else if c.WarnOnly {
		if _, err := s.exec(fmt.Sprintf("DELETE FROM %s WHERE %s = 1", c.Qualified, c.IDColumn)); err != nil {
			t.Errorf("reset: %v", err)
		}
		_, _ = s.exec(c.Seed[0])
	}

	// High risk: whole-table DELETE needs the typed table name.
	deleteAll := "DELETE FROM " + c.Qualified
	check, err = e2eSafetyCheck(t, s.session, c.Database, deleteAll, nil)
	wantTarget := c.Table
	if c.ConfirmLiteral {
		wantTarget = "CONFIRM"
	}
	if err == nil && (check.Risk != sqlsafety.RiskHigh || !sqlsafety.ConfirmTargetMatches(check.ConfirmTarget, wantTarget)) {
		err = fmt.Errorf("check = %+v", check)
	}
	if err == nil {
		err = s.run(deleteAll, check.CheckID, "")
		if code, _ := e2eSafetyError(err); code != "confirmMismatch" {
			err = fmt.Errorf("without confirmText: %v", err)
		} else {
			err = s.want("1=a,2=b,3=c")
		}
	}
	step("high-reject", err)
	seen = s.seen()
	check, err = e2eSafetyCheck(t, s.session, c.Database, deleteAll, nil)
	if err == nil {
		err = s.run(deleteAll, check.CheckID, check.ConfirmTarget)
	}
	if err == nil {
		err = s.want("")
	}
	if err == nil {
		rec, err = s.newRecord(seen)
	}
	if err == nil && rec.Undoable == c.WarnOnly {
		err = fmt.Errorf("record = %+v", rec)
	}
	if err == nil {
		if c.WarnOnly {
			err = seed()
		} else {
			var n int
			if n, err = s.undo(rec.ID); err == nil && n != 3 {
				err = fmt.Errorf("restored %d", n)
			}
			if err == nil {
				err = s.want("1=a,2=b,3=c")
			}
		}
	}
	step("high-undo", err)

	if c.NoGrid {
		for _, sc := range c.Scopes {
			step("scope-"+sc.Name, s.scope(sc))
		}
		return
	}
	// Grid group: update + insert + delete in one db_apply_changes.
	changes := []map[string]any{
		{"op": "update", "primaryKey": map[string]any{c.IDColumn: 1}, "values": map[string]any{c.NameColumn: "g1"}},
		{"op": "insert", "values": map[string]any{c.IDColumn: 4, c.NameColumn: "g4"}},
		{"op": "delete", "primaryKey": map[string]any{c.IDColumn: 2}},
	}
	seen = s.seen()
	args := map[string]any{"sessionId": s.session, "database": c.Database, "table": c.Table, "changes": changes}
	op := map[string]any{"kind": "rows", "table": c.Table, "changes": changes}
	_, err = e2eGuarded(t, "db_apply_changes", args, c.Database, op, nil)
	if err == nil {
		err = s.want("1=g1,3=c,4=g4")
	}
	if err == nil {
		rec, err = s.newRecord(seen)
	}
	if err == nil && rec.Undoable == c.WarnOnly {
		err = fmt.Errorf("record = %+v", rec)
	}
	if err == nil && !c.WarnOnly {
		var n int
		if n, err = s.undo(rec.ID); err == nil && n != 3 {
			err = fmt.Errorf("restored %d", n)
		}
		if err == nil {
			err = s.want("1=a,2=b,3=c")
		}
	}
	step("grid", err)

	for _, sc := range c.Scopes {
		step("scope-"+sc.Name, s.scope(sc))
	}

	// Structured clear table: confirmed, recorded, never backed up.
	if c.WarnOnly {
		return
	}
	seen = s.seen()
	destructiveOp := map[string]any{"kind": "destructive", "action": "clear_table", "table": c.Table}
	destructiveArgs := map[string]any{"sessionId": s.session, "action": "clear_table",
		"database": c.Database, "table": c.Table, "confirmation": c.Database + "." + c.Table}
	// Gate: without a token nothing is cleared.
	err = e2eCall(t, "db_destructive_operation", destructiveArgs, nil)
	if code, _ := e2eSafetyError(err); code != "checkRequired" {
		err = fmt.Errorf("without token: %v", err)
	} else {
		err = s.want("1=a,2=b,3=c")
	}
	if err == nil {
		_, err = e2eGuarded(t, "db_destructive_operation", destructiveArgs, c.Database, destructiveOp, nil)
	}
	if err == nil {
		err = s.want("")
	}
	if err == nil {
		rec, err = s.newRecord(seen)
	}
	if err == nil && (rec.Undoable || rec.Source != "destructive") {
		err = fmt.Errorf("record = %+v", rec)
	}
	step("truncate", err)
}

func (s *e2eSafety) count(query string) (int64, error) {
	res, err := s.exec(query)
	if err != nil {
		return 0, err
	}
	if len(res.Rows) != 1 || len(res.Rows[0]) == 0 {
		return 0, fmt.Errorf("rows = %v", res.Rows)
	}
	var n int64
	_, err = fmt.Sscan(fmt.Sprint(res.Rows[0][0]), &n)
	return n, err
}

func (s *e2eSafety) scope(sc e2eScope) error {
	for _, q := range sc.Setup {
		if _, err := s.exec(q); err != nil {
			return fmt.Errorf("setup %q: %w", q, err)
		}
	}
	defer func() {
		for _, q := range sc.Cleanup {
			_, _ = s.exec(q)
		}
	}()
	before := int64(-1)
	if sc.Count != "" {
		n, err := s.count(sc.Count)
		if err != nil {
			return fmt.Errorf("count: %w", err)
		}
		before = n
	}
	query := fmt.Sprintf("DELETE FROM %s WHERE %s <= 1", sc.Target, s.c.IDColumn)
	check, err := e2eSafetyCheck(s.t, s.session, sc.Database, query, nil)
	if err != nil {
		return err
	}
	var reasons []sqlsafety.Reason
	for _, st := range check.Statements {
		reasons = append(reasons, st.Reasons...)
	}
	has := func(r sqlsafety.Reason) bool {
		for _, x := range reasons {
			if x == r {
				return true
			}
		}
		return false
	}
	s.t.Logf("scope %s: database=%q target=%s → risk=%s undoable=%v estimate=%+v reasons=%v", sc.Name, sc.Database, sc.Target, check.Risk, check.Undoable, check.Statements[0].Estimate, reasons)
	run := func() error {
		a := map[string]any{"sessionId": s.session, "query": query, "database": sc.Database, "checkId": check.CheckID, "confirmText": check.ConfirmTarget}
		return e2eCall(s.t, "db_execute_query", a, nil)
	}
	switch sc.Expect {
	case "notFound":
		var checkErr error
		if check.Risk != sqlsafety.RiskHigh || check.Undoable || !has(sqlsafety.ReasonTableNotFound) || has(sqlsafety.ReasonUnparsed) {
			checkErr = fmt.Errorf("check = risk %s undoable %v reasons %v", check.Risk, check.Undoable, reasons)
		}
		err := run()
		s.t.Logf("scope %s execute: %v", sc.Name, err)
		if sc.Count != "" {
			if n, cerr := s.count(sc.Count); cerr != nil || n != before {
				return fmt.Errorf("unresolved delete changed the table: %d → %d (%v)", before, n, cerr)
			}
		}
		return checkErr
	case "resolved":
		est := check.Statements[0].Estimate
		if est.Rows != 1 || est.Source != "count" || check.Undoable == s.c.WarnOnly {
			return fmt.Errorf("check = risk %s undoable %v estimate %+v reasons %v", check.Risk, check.Undoable, est, reasons)
		}
		seen := s.seen()
		if err := run(); err != nil {
			return fmt.Errorf("execute: %w", err)
		}
		if n, err := s.count(sc.Count); err != nil || n != before-1 {
			return fmt.Errorf("after delete %d, %v", n, err)
		}
		rec, err := s.newRecord(seen)
		if err != nil {
			return err
		}
		if s.c.WarnOnly {
			return nil
		}
		var p struct {
			PreviewToken string `json:"previewToken"`
		}
		if err := e2eCall(s.t, "db_safety_undo_preview", map[string]any{"sessionId": s.session, "id": rec.ID}, &p); err != nil {
			return fmt.Errorf("preview: %w", err)
		}
		if err := e2eCall(s.t, "db_safety_undo", map[string]any{"sessionId": s.session, "id": rec.ID, "mode": "skipConflicts",
			"previewToken": p.PreviewToken, "confirmText": rec.Table.Name}, nil); err != nil {
			return fmt.Errorf("undo: %w", err)
		}
		if n, err := s.count(sc.Count); err != nil || n != before {
			return fmt.Errorf("after undo %d, %v", n, err)
		}
		return nil
	}
	return fmt.Errorf("unknown expect %q", sc.Expect)
}

func e2eProbe(t *testing.T, s *e2eSafety, step func(string, error) bool) {
	for i, q := range s.c.Reads {
		step(fmt.Sprintf("read%d", i+1), s.run(q, "", ""))
	}
	for i, q := range s.c.Writes {
		name := fmt.Sprintf("write%d", i+1)
		err := s.run(q, "", "")
		if code, _ := e2eSafetyError(err); code != "checkRequired" {
			step(name+"-gate", fmt.Errorf("without token: %v", err))
			continue
		}
		step(name+"-gate", nil)
		check, err := e2eSafetyCheck(t, s.session, s.c.Database, q, nil)
		if err == nil && (check.Undoable || check.CheckID == "") {
			err = fmt.Errorf("check = %+v", check)
		}
		if !step(name+"-check", err) {
			continue
		}
		seen := s.seen()
		err = s.run(q, check.CheckID, check.ConfirmTarget)
		t.Logf("%s execute: %v", name, err)
		if recs, rerr := s.records(); rerr == nil {
			for _, r := range recs {
				if !seen[r.ID] && r.Undoable {
					err = fmt.Errorf("record undoable: %+v", r)
				}
			}
		}
		if code, _ := e2eSafetyError(err); code != "" {
			step(name+"-exec", err)
		} else {
			step(name+"-exec", nil) // a server-side refusal is the store's, not the safety net's
		}
	}
}
