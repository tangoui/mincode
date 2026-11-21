package db

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/db/sqlsafety"
)

// TestLiveDatabaseMatrix exercises real database servers through the same
// commands the desktop calls: connect, navigation, tables, columns, primary
// keys, preview rows, a query, insert / update / delete of one row, DDL, a
// backup dump and the SQL 安全网 steps (live_matrix_safety_test.go): every
// write presents a db_safety_check token and backups are undone. It runs
// only when MINCODE_LIVE_DB_MATRIX names a JSON file with the servers to
// test (see liveCase); every case works in its own throwaway table and drops
// it again. Never point it at a server you care about.
func TestLiveDatabaseMatrix(t *testing.T) {
	path := os.Getenv("MINCODE_LIVE_DB_MATRIX")
	if path == "" {
		t.Skip("set MINCODE_LIVE_DB_MATRIX to a JSON file of disposable servers")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []liveCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	// Backups go to a throwaway store; the default settings apply.
	useSafetySettings(t, sqlsafety.DefaultSettings())
	store := useSafetyStore(t)
	useTransferManager(t)
	only := os.Getenv("MINCODE_LIVE_DB_ONLY")
	report := []string{}
	for _, c := range cases {
		if only != "" && !strings.Contains(","+only+",", ","+c.Name+",") {
			continue
		}
		c := c
		t.Run(c.Name, func(t *testing.T) {
			steps := runLiveCase(t, c, store)
			report = append(report, fmt.Sprintf("%-14s %s", c.Name, strings.Join(steps, " ")))
		})
	}
	if out := os.Getenv("MINCODE_LIVE_DB_REPORT"); out != "" {
		_ = os.WriteFile(out, []byte(strings.Join(report, "\n")+"\n"), 0o600)
	}
}

// liveCase is one server. SQL cases create Table with the given columns and
// edit it; Kind "redis" and "mongo" use their own steps.
type liveCase struct {
	Name     string             `json:"name"`
	Kind     string             `json:"kind"` // sql | sqlwarn | mongo | redis | nonsql
	Config   DbConnectionConfig `json:"config"`
	Database string             `json:"database"`
	// Namespace is the schema a table lives in when it is not Database
	// (PostgreSQL family "public", SQL Server "dbo"); empty when none.
	Namespace string `json:"namespace"`
	// Setup statements run before the table is created (e.g. CREATE DATABASE).
	Setup     []string `json:"setup"`
	CreateSQL string   `json:"createSql"` // CREATE TABLE for the throwaway table
	DropSQL   string   `json:"dropSql"`
	Table     string   `json:"table"`
	Query     string   `json:"query"` // a simple query against the table
	// Backup asks for a backup dump of Database (only for dialects that support it).
	Backup bool `json:"backup"`
	// IndexSQL creates a two-column index on (name, id) after the table; the
	// indexes step then expects it with its columns in that order.
	IndexSQL string `json:"indexSql"`
	// NoDDL skips db_get_table_ddl for dialects the app does not offer it on (SQL Server).
	NoDDL bool `json:"noDdl"`
	liveSafetyCase
	liveExtraCase
}

func dispatch(t *testing.T, cmd string, args any) json.RawMessage {
	t.Helper()
	raw, err := dispatchErr(cmd, args)
	if err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	return raw
}

func dispatchErr(cmd string, args any) (json.RawMessage, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	out, err := core.DispatchContext(context.Background(), cmd, string(body))
	return json.RawMessage(out), err
}

// runLiveCase returns one "step:ok|FAIL|skip" token per step; a failing
// step is reported with t.Errorf and later steps still run where they can.
func runLiveCase(t *testing.T, c liveCase, store *sqlsafety.Store) []string {
	steps := []string{}
	mark := func(step string, err error) bool {
		if err != nil {
			t.Errorf("%s: %v", step, err)
			steps = append(steps, step+":FAIL")
			return false
		}
		steps = append(steps, step+":ok")
		return true
	}
	start := time.Now()
	if c.Config.ConnectionID == "" {
		c.Config.ConnectionID = "live-" + c.Name
	}
	raw, err := dispatchErr("db_connect", map[string]any{"config": c.Config})
	if !mark("connect", err) {
		return steps
	}
	var session string
	_ = json.Unmarshal(raw, &session)
	defer func() { _, _ = dispatchErr("db_disconnect", map[string]any{"sessionId": session}) }()
	t.Logf("connected in %v", time.Since(start).Round(time.Millisecond))

	_, err = dispatchErr("db_get_navigation_schema", map[string]any{"sessionId": session})
	mark("navigation", err)

	switch c.Kind {
	case "redis":
		return append(steps, liveRedis(t, session, mark)...)
	case "mongo":
		steps = append(steps, liveMongo(t, session, c, mark)...)
		return append(steps, liveNonSQL(t, session, c, store, mark)...)
	case "nonsql":
		return append(steps, liveNonSQL(t, session, c, store, mark)...)
	}

	if c.Qualified == "" {
		c.Qualified = c.Table
	}
	safety := &liveSafety{t: t, session: session, c: c, store: store, table: c.Qualified}
	exec := func(sql string) error {
		_, err := safety.exec(sql)
		return err
	}
	for _, s := range c.Setup {
		_ = exec(s)
	}
	_ = exec(c.DropSQL)
	if !mark("create", exec(c.CreateSQL)) {
		return steps
	}
	defer func() { _ = exec(c.DropSQL) }()

	if c.Kind == "sqlwarn" {
		return append(steps, liveWarnSteps(t, safety, mark)...)
	}
	tableArgs := map[string]any{"sessionId": session, "database": c.Database, "table": c.Table}
	raw, err = dispatchErr("db_get_database_tables", map[string]any{"sessionId": session, "database": c.Database})
	if err == nil && !strings.Contains(strings.ToLower(string(raw)), strings.ToLower(c.Table)) {
		err = fmt.Errorf("table %q not listed", c.Table)
	}
	mark("tables", err)
	_, err = dispatchErr("db_get_table_columns", tableArgs)
	mark("columns", err)
	raw, err = dispatchErr("db_get_primary_keys", tableArgs)
	if err == nil && !strings.Contains(strings.ToLower(string(raw)), "id") {
		err = fmt.Errorf("primary key not reported: %s", raw)
	}
	mark("pk", err)

	edit := map[string]any{"sessionId": session, "database": c.Database, "table": c.Table, "namespace": c.Namespace}
	idCol, nameCol := c.columns()
	insert := clone(edit)
	insert["fields"] = map[string]any{idCol: 1, nameCol: "初始 O'Brien"}
	_, err = dispatchErr("db_insert_row", insert)
	mark("insert", err)
	update := clone(edit)
	update["primaryKey"] = map[string]any{idCol: 1}
	update["column"] = nameCol
	update["value"] = "已更新 ✓"
	seen := safety.ids()
	_, err = dispatchErr("db_update_cell", update)
	if mark("update", err) {
		mark("row-undo", liveRowUndo(safety, seen, update))
	}

	limit := uint32(10)
	rowsArgs := clone(tableArgs)
	rowsArgs["limit"] = &limit
	if c.Namespace != "" {
		rowsArgs["namespace"] = c.Namespace
	}
	raw, err = dispatchErr("db_get_table_rows", rowsArgs)
	if err == nil && !strings.Contains(string(raw), "已更新 ✓") {
		err = fmt.Errorf("updated value not read back: %.300s", raw)
	}
	mark("preview", err)
	db := c.Database
	raw, err = dispatchErr("db_execute_query", map[string]any{"sessionId": session, "query": c.Query, "database": &db})
	if err == nil && !strings.Contains(string(raw), "已更新 ✓") {
		err = fmt.Errorf("query result: %.300s", raw)
	}
	mark("query", err)
	if c.NoDDL {
		steps = append(steps, "ddl:skip")
	} else {
		_, err = dispatchErr("db_get_table_ddl", tableArgs)
		mark("ddl", err)
	}
	if c.IndexSQL != "" {
		err = exec(c.IndexSQL)
		if err == nil {
			raw, err = dispatchErr("db_list_indexes", tableArgs)
		}
		if err == nil && !strings.Contains(string(raw), `["name","id"]`) {
			err = fmt.Errorf("index columns: %.300s", raw)
		}
		mark("indexes", err)
	}
	liveSafetySteps(t, safety, mark)
	liveExtraSteps(t, safety, mark)

	if c.Backup {
		mark("backup", liveBackup(t, session, c))
	} else {
		steps = append(steps, "backup:skip")
	}

	del := clone(edit)
	del["primaryKey"] = map[string]any{idCol: 1}
	_, err = dispatchErr("db_delete_row", del)
	mark("delete", err)
	return steps
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+4)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// liveBackup dumps the case's database through the session backup source,
// the path a scheduled SQL backup takes, and checks the row is in it.
func liveBackup(t *testing.T, session string, c liveCase) error {
	s, release, err := requireSession(session)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	src, err := newSessionBackupSource(ctx, s)
	if err != nil {
		return err
	}
	defer src.close()
	dir := t.TempDir()
	job := BackupJob{Name: "live", Destination: dir, FileNamePrefix: "live",
		Targets: []BackupTarget{{Database: c.Database, Tables: []string{c.Table}}}}
	now := time.Now()
	result, err := writeBackupFile(ctx, job, src, backupDumpMeta{JobName: "live", ConnectionName: c.Name, DbType: c.Config.DbType, StartedAt: now, Preamble: src.preamble}, now)
	if err != nil {
		return err
	}
	dump, err := os.ReadFile(result.File)
	if err != nil {
		return err
	}
	if !strings.Contains(string(dump), "已更新 ✓") {
		return fmt.Errorf("dump %s (%d rows) lacks the row", filepath.Base(result.File), result.Rows)
	}
	if len(src.warnings)+len(result.Warnings) > 0 {
		t.Logf("backup warnings: %v %v", src.warnings, result.Warnings)
	}
	return nil
}

func liveRedis(t *testing.T, session string, mark func(string, error) bool) []string {
	key := fmt.Sprintf("mincode:live:%d", time.Now().UnixNano())
	redisOp := func(action string) map[string]any {
		return map[string]any{"kind": "redis", "action": action, "dbIndex": 0, "keys": []string{key}}
	}
	_, _, err := liveGuarded("redis_add_key", map[string]any{"sessionId": session, "key": key, "keyType": "string", "value": "值 ✓"}, "", redisOp("add"))
	mark("insert", err)
	raw, err := dispatchErr("redis_get_key", map[string]any{"sessionId": session, "key": key})
	if err == nil && !strings.Contains(string(raw), "值 ✓") {
		err = fmt.Errorf("value: %.200s", raw)
	}
	mark("preview", err)
	_, _, err = liveGuarded("redis_set_ttl", map[string]any{"sessionId": session, "key": key, "ttlSeconds": 600}, "", redisOp("ttl"))
	mark("update", err)
	raw, err = dispatchErr("redis_list_keys", map[string]any{"sessionId": session, "pattern": "mincode:live:*", "count": 100})
	if err == nil && !strings.Contains(string(raw), key) {
		err = fmt.Errorf("key not listed: %.200s", raw)
	}
	mark("tables", err)
	// gate: no token, no write; the check estimates and is never undoable.
	_, err = dispatchErr("redis_set_key", map[string]any{"sessionId": session, "key": key, "keyType": "string", "value": "x"})
	if w, ok := liveSafetyError(err); !ok || w.Code != safetyCodeCheckRequired {
		err = fmt.Errorf("without token: %v", err)
	} else {
		err = nil
	}
	mark("gate", err)
	for _, op := range []map[string]any{
		{"kind": "redis", "action": "delete", "dbIndex": 0, "pattern": "mincode:live:*"},
		{"kind": "redis", "action": "flushdb", "dbIndex": 0},
		{"kind": "redis", "action": "flushall", "dbIndex": 0},
	} {
		c, _, err := liveCheck(session, "", "", op)
		if err == nil {
			sc := c.Statements[0]
			t.Logf("redis %s check: risk=%s undoable=%v estimate=%+v reasons=%v", op["action"], c.Risk, c.Undoable, sc.Estimate, sc.Reasons)
			if c.Undoable || !hasLiveReason(sc.Reasons, sqlsafety.ReasonNotUndoableStore) || (op["action"] != "flushall" && sc.Estimate.Source != "count") {
				err = fmt.Errorf("check = %+v", c)
			}
		}
		mark(fmt.Sprintf("check-%s", op["action"]), err)
	}
	_, _, err = liveGuarded("redis_delete_key", map[string]any{"sessionId": session, "key": key}, "", redisOp("delete"))
	mark("delete", err)
	return nil
}

func liveMongo(t *testing.T, session string, c liveCase, mark func(string, error) bool) []string {
	edit := map[string]any{"sessionId": session, "database": c.Database, "table": c.Table}
	insert := clone(edit)
	insert["fields"] = map[string]any{"_id": 1, "name": "初始"}
	_, err := dispatchErr("db_insert_row", insert)
	mark("insert", err)
	update := clone(edit)
	update["primaryKey"] = map[string]any{"_id": 1}
	update["column"] = "name"
	update["value"] = "已更新 ✓"
	_, err = dispatchErr("db_update_cell", update)
	mark("update", err)
	raw, err := dispatchErr("db_get_database_tables", map[string]any{"sessionId": session, "database": c.Database})
	if err == nil && !strings.Contains(string(raw), c.Table) {
		err = fmt.Errorf("collection not listed: %.200s", raw)
	}
	mark("tables", err)
	limit := uint32(10)
	raw, err = dispatchErr("db_get_table_rows", map[string]any{"sessionId": session, "database": c.Database, "table": c.Table, "limit": &limit})
	if err == nil && !strings.Contains(string(raw), "已更新 ✓") {
		err = fmt.Errorf("rows: %.300s", raw)
	}
	mark("preview", err)
	db := c.Database
	raw, err = dispatchErr("db_execute_query", map[string]any{"sessionId": session, "query": c.Query, "database": &db})
	if err == nil && !strings.Contains(string(raw), "已更新 ✓") {
		err = fmt.Errorf("query: %.300s", raw)
	}
	mark("query", err)
	del := clone(edit)
	del["primaryKey"] = map[string]any{"_id": 1}
	_, err = dispatchErr("db_delete_row", del)
	mark("delete", err)
	// The MongoDB query window only reads (find specs), so the emptied
	// collection stays on the disposable server.
	return nil
}
