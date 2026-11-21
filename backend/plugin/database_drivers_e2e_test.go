package plugin

// Opt-in end-to-end check of the plugin-driver features that replace the
// built-in DuckDB / Oracle / YashanDB / ClickHouse drivers: real driver
// plugins are installed, enabled and granted by this package's manager and
// started as processes, and the same db_* command handlers the desktop bridge
// dispatches (plus the cloud companion and database.schema entry points)
// drive them.
//
//	MINCODE_E2E_DB_DRIVERS=1        required gate
//	MINCODE_E2E_DUCKDB_PACKAGE      installable duckdb-driver package directory
//	MINCODE_E2E_SQLITE_PACKAGE      installable edge-sqlite-driver package directory (rqlite driver)
//	MINCODE_E2E_RQLITE_HOST/_PORT   a disposable rqlite server (default 127.0.0.1:4001)
//
// Build the packages with
// `node vue-app/scripts/build-go-plugins.mjs --package <id> <new-dir>`.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mincode/app/backend/core"
	"mincode/app/backend/db"
	"mincode/app/backend/license"
)

func TestDatabaseDriverPluginsEndToEnd(t *testing.T) {
	if os.Getenv("MINCODE_E2E_DB_DRIVERS") != "1" {
		t.Skip("set MINCODE_E2E_DB_DRIVERS=1 and the package directories")
	}
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

	if pkg := os.Getenv("MINCODE_E2E_DUCKDB_PACKAGE"); pkg != "" {
		t.Run("duckdb", func(t *testing.T) { e2eDuckDB(t, pkg) })
	}
	if pkg := os.Getenv("MINCODE_E2E_SQLITE_PACKAGE"); pkg != "" {
		t.Run("rqlite", func(t *testing.T) { e2eRqlite(t, pkg) })
	}
}

func e2eInstall(t *testing.T, pkg, pluginID string) {
	t.Helper()
	if _, err := managerInst.InstallLocal(pkg); err != nil {
		t.Fatalf("install %s: %v", pluginID, err)
	}
	if err := managerInst.SetEnabled(context.Background(), pluginID, true); err != nil {
		t.Fatalf("enable %s: %v", pluginID, err)
	}
}

// e2eCall dispatches a db_* command; it returns the error instead of failing.
func e2eCall(t *testing.T, command string, args any, out any) error {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := core.Dispatch(command, string(encoded))
	if err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal([]byte(result), out); err != nil {
			t.Fatalf("%s: decode %s: %v", command, result, err)
		}
	}
	return nil
}

func e2eMust(t *testing.T, command string, args any, out any) {
	t.Helper()
	if err := e2eCall(t, command, args, out); err != nil {
		t.Fatalf("%s: %v", command, err)
	}
}

func e2eConnect(t *testing.T, config db.DbConnectionConfig) string {
	t.Helper()
	var id string
	e2eMust(t, "db_connect", map[string]any{"config": config}, &id)
	t.Cleanup(func() { _ = e2eCall(t, "db_disconnect", map[string]any{"sessionId": id}, nil) })
	return id
}

// e2eExec runs sql, presenting a db_safety_check token when it writes.
func e2eExec(t *testing.T, session, sql string) db.QueryResult {
	t.Helper()
	var result db.QueryResult
	if _, err := e2eGuarded(t, "db_execute_query", map[string]any{"sessionId": session, "query": sql}, "", nil, &result); err != nil {
		t.Fatalf("db_execute_query: %v", err)
	}
	return result
}

func e2eCount(t *testing.T, session, sql string) int64 {
	t.Helper()
	result := e2eExec(t, session, sql)
	if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
		t.Fatalf("%s: rows %v", sql, result.Rows)
	}
	switch v := result.Rows[0][0].(type) {
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	t.Fatalf("%s: count %T %v", sql, result.Rows[0][0], result.Rows[0][0])
	return 0
}

func e2eSchemaNames(t *testing.T, session string) []string {
	t.Helper()
	var info db.DbSchemaInfo
	e2eMust(t, "db_get_schema", map[string]any{"sessionId": session}, &info)
	names := []string{}
	for _, database := range info.Databases {
		names = append(names, database.Name)
	}
	return names
}

// DuckDB: database-level clear / drop (destructive.clearDatabase /
// destructive.dropDatabase) through db_destructive_operation.
func e2eDuckDB(t *testing.T, pkg string) {
	const pluginID, driverID, connectionID = "duckdb-driver", "duckdb", "e2e-duckdb"
	e2eInstall(t, pkg, pluginID)
	if err := managerInst.grantDatabaseDriver(pluginID, driverID, connectionID); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "e2e.duckdb")
	config := db.DbConnectionConfig{
		DbType: "duckdb", FilePath: &file, ConnectionID: connectionID,
		Driver:  &db.ExternalDriverRef{PluginID: pluginID, DriverID: driverID},
		Options: map[string]any{"accessMode": "readWrite"},
	}
	session := e2eConnect(t, config)
	for _, statement := range []string{
		"CREATE SCHEMA shop",
		"CREATE TABLE shop.orders (id INTEGER PRIMARY KEY)",
		"CREATE TABLE shop.lines (id INTEGER PRIMARY KEY, order_id INTEGER REFERENCES shop.orders(id))",
		"INSERT INTO shop.orders VALUES (1), (2)",
		"INSERT INTO shop.lines VALUES (1, 1), (2, 2)",
		"CREATE TABLE main.keep (a INTEGER)",
		"INSERT INTO main.keep VALUES (1)",
	} {
		e2eExec(t, session, statement)
	}
	destructive := func(action, database, confirmation string) error {
		_, err := e2eGuarded(t, "db_destructive_operation", map[string]any{
			"sessionId": session, "action": action, "database": database, "confirmation": confirmation,
		}, database, map[string]any{"kind": "destructive", "action": action}, nil)
		return err
	}

	if err := destructive("clear_database", "shop", "wrong"); err == nil || !strings.Contains(err.Error(), "人工确认") {
		t.Fatalf("wrong confirmation: %v", err)
	}
	if err := destructive("clear_database", "shop", "shop"); err != nil {
		t.Fatalf("clear_database: %v", err)
	}
	if n := e2eCount(t, session, "SELECT count(*) FROM shop.orders") + e2eCount(t, session, "SELECT count(*) FROM shop.lines"); n != 0 {
		t.Fatalf("rows left after clear_database: %d", n)
	}
	if n := e2eCount(t, session, "SELECT count(*) FROM main.keep"); n != 1 {
		t.Fatalf("clear_database touched another schema: main.keep has %d rows", n)
	}
	if err := destructive("drop_database", "main", "main"); err == nil {
		t.Fatal("drop_database main must fail")
	} else {
		t.Logf("drop main refused: %v", err)
	}
	if err := destructive("drop_database", "shop", "shop"); err != nil {
		t.Fatalf("drop_database: %v", err)
	}
	for _, name := range e2eSchemaNames(t, session) {
		if name == "shop" {
			t.Fatal("shop schema still listed after drop_database")
		}
	}

	// A read-only connection refuses both actions in the driver.
	readOnly := config
	readOnly.Options = map[string]any{"accessMode": "readOnly"}
	e2eExec(t, session, "CHECKPOINT")
	_ = e2eCall(t, "db_disconnect", map[string]any{"sessionId": session}, nil)
	roSession := e2eConnect(t, readOnly)
	if _, err := e2eGuarded(t, "db_destructive_operation", map[string]any{
		"sessionId": roSession, "action": "clear_database", "database": "main", "confirmation": "main",
	}, "main", map[string]any{"kind": "destructive", "action": "clear_database"}, nil); err == nil {
		t.Fatal("clear_database on a read-only connection must fail")
	}
}

// rqlite (sqlite dialect): schema sync from a built-in SQLite file into a
// plugin session, and cloud read-only access through the plugin driver.
func e2eRqlite(t *testing.T, pkg string) {
	const pluginID, driverID = "edge-sqlite-driver", "rqlite"
	e2eInstall(t, pkg, pluginID)
	host := os.Getenv("MINCODE_E2E_RQLITE_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := 4001
	if raw := os.Getenv("MINCODE_E2E_RQLITE_PORT"); raw != "" {
		port, _ = strconv.Atoi(raw)
	}
	config := func(connectionID string) db.DbConnectionConfig {
		return db.DbConnectionConfig{
			DbType: "plugin", Host: host, Port: port, ConnectionID: connectionID,
			Driver: &db.ExternalDriverRef{PluginID: pluginID, DriverID: driverID},
		}
	}
	if err := managerInst.grantDatabaseDriver(pluginID, driverID, "e2e-rq-a"); err != nil {
		t.Fatal(err)
	}
	target := e2eConnect(t, config("e2e-rq-a"))
	for _, statement := range []string{
		"DROP TABLE IF EXISTS e2e_users",
		"CREATE TABLE e2e_users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)",
		"INSERT INTO e2e_users (id, email) VALUES (1, 'a@example.com')",
	} {
		e2eExec(t, target, statement)
	}
	t.Cleanup(func() {
		_, _ = e2eGuarded(t, "db_execute_query", map[string]any{"sessionId": target, "query": "DROP TABLE IF EXISTS e2e_users"}, "", nil, nil)
	})

	// Source: a built-in SQLite file with one more column and an index. A
	// plugin session (sqlite dialect) and a built-in session pair in sync.
	file := filepath.Join(t.TempDir(), "source.db")
	source := e2eConnect(t, db.DbConnectionConfig{DbType: "sqlite", FilePath: &file})
	for _, statement := range []string{
		"CREATE TABLE e2e_users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, nickname TEXT)",
		"CREATE INDEX e2e_users_email ON e2e_users(email)",
	} {
		e2eExec(t, source, statement)
	}

	args := map[string]any{"sourceSessionId": source, "sourceDatabase": "main", "targetSessionId": target, "targetDatabase": "main"}
	var comparison db.SchemaComparison
	e2eMust(t, "db_compare_schema", args, &comparison)
	if comparison.Source.Dialect != "sqlite" || comparison.Target.Dialect != "sqlite" {
		t.Fatalf("snapshot dialects %q / %q", comparison.Source.Dialect, comparison.Target.Dialect)
	}
	var selected []string
	kinds := []string{}
	for _, change := range comparison.Changes {
		if change.Table != "e2e_users" {
			continue
		}
		kinds = append(kinds, change.Kind+":"+change.ObjectName)
		if change.Kind == "add_column" || change.Kind == "create_index" {
			selected = append(selected, change.ID)
		}
	}
	t.Logf("e2e_users changes: %v", kinds)
	if len(selected) != 2 {
		t.Fatalf("expected add_column and create_index, got %v", kinds)
	}
	syncArgs := map[string]any{
		"sourceSessionId": source, "sourceDatabase": "main", "targetSessionId": target, "targetDatabase": "main",
		"sourceFingerprint": comparison.Source.Fingerprint, "targetFingerprint": comparison.Target.Fingerprint,
		"selectedChangeIds": selected,
	}
	var plan db.SchemaSyncPlan
	e2eMust(t, "db_generate_schema_sync", syncArgs, &plan)
	for _, statement := range plan.Statements {
		t.Logf("plan: %s", statement.SQL)
	}
	var execution db.SchemaSyncExecutionResult
	syncOp := map[string]any{"kind": "schemaSync", "sourceSessionId": source, "sourceDatabase": "main",
		"sourceFingerprint": comparison.Source.Fingerprint, "targetFingerprint": comparison.Target.Fingerprint, "selectedChangeIds": selected}
	if _, err := e2eGuarded(t, "db_execute_schema_sync", syncArgs, "main", syncOp, &execution); err != nil {
		t.Fatalf("db_execute_schema_sync: %v", err)
	}
	if !execution.Completed {
		t.Fatalf("schema sync did not complete: %+v", execution)
	}
	// The DDL ran on the rqlite server through the plugin.
	columns := e2eExec(t, target, "SELECT name FROM pragma_table_info('e2e_users') ORDER BY cid")
	if len(columns.Rows) != 3 || columns.Rows[2][0] != "nickname" {
		t.Fatalf("target columns after sync = %v", columns.Rows)
	}
	if n := e2eCount(t, target, "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'e2e_users_email'"); n != 1 {
		t.Fatal("index was not created on the target")
	}
	e2eMust(t, "db_compare_schema", args, &comparison)
	for _, change := range comparison.Changes {
		if change.Table == "e2e_users" {
			t.Fatalf("difference left after sync: %s %s", change.Kind, change.ObjectName)
		}
	}

	// Cloud companion: only a granted connection connects, read-only.
	password := ""
	cloudConfig := config("e2e-rq-a")
	cloudConfig.Password = &password
	cloudSession, err := db.ConnectCloudReadOnly(db.CloudReadOnlyConnection{Config: cloudConfig})
	if err != nil {
		t.Fatalf("cloud connect: %v", err)
	}
	t.Cleanup(func() { db.CloseCloudReadOnly(cloudSession) })
	if _, err := db.CloudSchema(cloudSession); err != nil {
		t.Fatalf("cloud schema: %v", err)
	}
	result, err := db.CloudReadOnlyQuery(cloudSession, "SELECT email FROM e2e_users")
	if err != nil || result.RowCount != 1 {
		t.Fatalf("cloud query = %+v, %v", result, err)
	}
	if _, err := db.CloudReadOnlyQuery(cloudSession, "DELETE FROM e2e_users"); err == nil {
		t.Fatal("cloud DELETE must be rejected")
	}
	ungranted := config("e2e-rq-ungranted")
	ungranted.Password = &password
	if _, err := db.ConnectCloudReadOnly(db.CloudReadOnlyConnection{Config: ungranted}); err == nil {
		t.Fatal("cloud connect without a grant must fail")
	} else {
		t.Logf("ungranted cloud connect refused: %v", err)
	}
	if !db.ExternalDriverReleaseAllowed(db.ExternalDriverRef{PluginID: pluginID, DriverID: driverID}, "e2e-rq-a") ||
		db.ExternalDriverReleaseAllowed(db.ExternalDriverRef{PluginID: pluginID, DriverID: driverID}, "e2e-rq-ungranted") {
		t.Fatal("ExternalDriverReleaseAllowed does not follow the grants")
	}
}
