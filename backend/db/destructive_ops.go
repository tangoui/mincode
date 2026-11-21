package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mincode/app/backend/db/sqlsafety"
)

func executeDestructiveOperation(
	ctx context.Context,
	s *dbSession,
	action string,
	database string,
	table *string,
	confirmation string,
) error {
	return runDestructiveOperation(ctx, s, nil, action, database, table, confirmation)
}

// runDestructiveOperation validates and runs a destructive operation. With
// guard (a consumed SQL safety check) emptying or dropping a SQL table backs
// the table up first, and database-wide operations leave an audit record.
// confirmation must name the target (database or database.table) by the
// same rule as confirmText (sqlsafety.ConfirmTargetMatches).
func runDestructiveOperation(
	ctx context.Context,
	s *dbSession,
	guard *destructiveGuard,
	action string,
	database string,
	table *string,
	confirmation string,
) error {
	if s.kind == kindPlugin {
		// Structured driver actions: confirmed and recorded, never backed up.
		if err := externalDestructive(ctx, s, action, strings.TrimSpace(database), table, confirmation); err != nil {
			return err
		}
		guard.recordDone(s)
		return nil
	}
	if s.kind == kindVector {
		if action != "drop_table" {
			return fmt.Errorf("向量数据库当前仅支持删除 Collection/Index")
		}
		database = strings.TrimSpace(database)
		tableName := strings.TrimSpace(optionalString(table))
		if database == "" || tableName == "" {
			return fmt.Errorf("Collection/Index 名称不能为空")
		}
		target := database + "." + tableName
		if !sqlsafety.ConfirmTargetMatches(target, confirmation) {
			return fmt.Errorf("人工确认内容与目标对象不一致")
		}
		ctx, cancel := context.WithTimeout(ctx, time.Duration(dbDMLTimeoutSecs)*time.Second)
		defer cancel()
		schema, err := getSchema(ctx, s)
		if err != nil {
			return fmt.Errorf("校验目标对象: %w", err)
		}
		databaseInfo, ok := findSchemaDatabase(schema, database)
		if !ok || !containsSchemaObject(databaseInfo.Tables, tableName) {
			return fmt.Errorf("目标 Collection/Index 不存在")
		}
		return vectorDeleteCollection(ctx, s, tableName)
	}
	if s.kind != kindSQL || !supportsDestructiveOperation(s.dialect, action) {
		return fmt.Errorf("当前数据库类型不支持该危险操作")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(dbDMLTimeoutSecs)*time.Second)
	defer cancel()

	if strings.TrimSpace(database) == "" {
		return fmt.Errorf("数据库名称不能为空")
	}
	tableName := optionalString(table)
	if table != nil && strings.TrimSpace(tableName) == "" {
		return fmt.Errorf("数据表名称不能为空")
	}
	target := database
	if tableName != "" {
		target += "." + tableName
	}
	if !sqlsafety.ConfirmTargetMatches(target, confirmation) {
		return fmt.Errorf("人工确认内容与目标对象不一致")
	}

	schema, err := getSchema(ctx, s)
	if err != nil {
		return fmt.Errorf("校验目标对象: %w", err)
	}
	databaseInfo, ok := findSchemaDatabase(schema, database)
	if !ok {
		return fmt.Errorf("目标数据库或模式不存在")
	}

	switch action {
	case "drop_database":
		if tableName != "" {
			return fmt.Errorf("删除数据库操作不能指定数据表")
		}
		if s.dialect == "duckdb" && strings.EqualFold(database, "main") {
			return fmt.Errorf("DuckDB main 模式不能删除")
		}
		statement, err := dropDatabaseStatement(s.dialect, database)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("执行危险操作: %w", err)
		}
		guard.recordDone(s)
		return nil
	case "clear_database":
		if tableName != "" {
			return fmt.Errorf("清空数据库操作不能指定数据表")
		}
		if err := clearDatabase(ctx, s, database, databaseInfo.Tables); err != nil {
			return err
		}
		guard.recordDone(s)
		return nil
	case "drop_table", "clear_table":
		if tableName == "" {
			return fmt.Errorf("数据表名称不能为空")
		}
		tableExists := containsDataTable(databaseInfo.Tables, tableName)
		if s.vector != nil && s.vector.provider == "pgvector" {
			tableExists = containsSchemaObject(databaseInfo.Tables, tableName)
		}
		if !tableExists {
			return fmt.Errorf("目标数据表不存在或不是普通数据表")
		}
		statement, err := destructiveTableStatement(s.dialect, action, database, tableName)
		if err != nil {
			return err
		}
		if guard != nil {
			err = guard.runTable(ctx, s, statement)
		} else {
			_, err = s.db.ExecContext(ctx, statement)
		}
		var se *safetyError
		if err != nil && !errors.As(err, &se) {
			return fmt.Errorf("执行危险操作: %w", err)
		}
		return err
	default:
		return fmt.Errorf("不支持的危险操作: %s", action)
	}
}

func supportsDestructiveOperation(dialect, action string) bool {
	switch action {
	case "clear_database":
		return dialect == "mysql" || dialect == "postgres" || dialect == "sqlite" || dialect == "duckdb"
	case "drop_database":
		return dialect == "mysql" || dialect == "postgres" || dialect == "duckdb"
	case "clear_table", "drop_table":
		switch dialect {
		case "mysql", "postgres", "sqlite", "duckdb", "sqlserver", "oracle", "yashandb", "firebird", "dm", "clickhouse":
			return true
		}
	}
	return false
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func findSchemaDatabase(schema *DbSchemaInfo, database string) (SchemaDatabase, bool) {
	if schema == nil {
		return SchemaDatabase{}, false
	}
	for _, candidate := range schema.Databases {
		if candidate.Name == database {
			return candidate, true
		}
	}
	return SchemaDatabase{}, false
}

func containsSchemaObject(tables []SchemaTable, name string) bool {
	for _, candidate := range tables {
		if candidate.Name == name {
			return true
		}
	}
	return false
}

func containsDataTable(tables []SchemaTable, table string) bool {
	for _, candidate := range tables {
		if candidate.Name == table && strings.EqualFold(candidate.Kind, "table") {
			return true
		}
	}
	return false
}

func destructiveQuoteIdentifier(dialect string, identifier string) string {
	if dialect == "mysql" {
		return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func dropDatabaseStatement(dialect string, database string) (string, error) {
	switch dialect {
	case "postgres", "duckdb":
		return "DROP SCHEMA " + destructiveQuoteIdentifier(dialect, database) + " CASCADE", nil
	case "mysql":
		return "DROP DATABASE " + destructiveQuoteIdentifier(dialect, database), nil
	default:
		return "", fmt.Errorf("当前数据库类型不支持删除数据库或模式")
	}
}

func clearDatabase(ctx context.Context, s *dbSession, database string, tables []SchemaTable) error {
	tableNames := make([]string, 0, len(tables))
	for _, table := range tables {
		if strings.EqualFold(table.Kind, "table") {
			tableNames = append(tableNames, table.Name)
		}
	}
	if len(tableNames) == 0 {
		return nil
	}

	switch s.dialect {
	case "postgres":
		qualified, err := qualifyDestructiveTables(s.dialect, database, tableNames)
		if err != nil {
			return err
		}
		statement := "TRUNCATE TABLE " + strings.Join(qualified, ", ") + " RESTART IDENTITY"
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("清空数据库: %w", err)
		}
		return nil
	case "mysql":
		return clearMySQLDatabase(ctx, s, database, tableNames)
	case "sqlite":
		return clearSQLiteDatabase(ctx, s, tableNames)
	case "duckdb":
		ordered, err := orderDuckDBTablesForClear(ctx, s, database, tableNames)
		if err != nil {
			return err
		}
		// DuckDB currently checks foreign-key references against the transaction's
		// original state, so clearing related tables in one transaction still fails.
		// Truncate child tables first and commit each statement before its parent.
		for _, table := range ordered {
			qualified, err := qualifiedTable(s.dialect, database, table)
			if err != nil {
				return err
			}
			if _, err := s.db.ExecContext(ctx, "TRUNCATE TABLE "+qualified); err != nil {
				return fmt.Errorf("清空数据表 %s: %w", qualified, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("当前数据库类型不支持清空数据库或模式")
	}
}

func qualifyDestructiveTables(dialect, database string, tables []string) ([]string, error) {
	qualified := make([]string, 0, len(tables))
	for _, table := range tables {
		name, err := qualifiedTable(dialect, database, table)
		if err != nil {
			return nil, err
		}
		qualified = append(qualified, name)
	}
	return qualified, nil
}

func clearMySQLDatabase(ctx context.Context, s *dbSession, database string, tables []string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取数据库连接: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		return fmt.Errorf("禁用外键检查: %w", err)
	}
	defer restoreMySQLForeignKeyChecks(conn)
	qualifiedTables, err := qualifyDestructiveTables(s.dialect, database, tables)
	if err != nil {
		return err
	}
	for _, table := range qualifiedTables {
		if _, err := conn.ExecContext(ctx, "TRUNCATE TABLE "+table); err != nil {
			return fmt.Errorf("清空数据表 %s: %w", table, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=1"); err != nil {
		return fmt.Errorf("恢复外键检查: %w", err)
	}
	return nil
}

func clearSQLiteDatabase(ctx context.Context, s *dbSession, tables []string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取数据库连接: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("禁用外键检查: %w", err)
	}
	defer restoreSQLiteForeignKeys(conn)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始清空 SQLite 数据库事务: %w", err)
	}
	defer tx.Rollback()
	for _, table := range tables {
		qualified, err := qualifiedTable(s.dialect, "", table)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+qualified); err != nil {
			return fmt.Errorf("清空数据表 %s: %w", qualified, err)
		}
	}
	_, _ = tx.ExecContext(ctx, "DELETE FROM sqlite_sequence")
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交清空 SQLite 数据库事务: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("恢复外键检查: %w", err)
	}
	return nil
}

func orderDuckDBTablesForClear(ctx context.Context, s *dbSession, database string, tables []string) ([]string, error) {
	known := make(map[string]bool, len(tables))
	parents := make(map[string][]string, len(tables))
	incoming := make(map[string]int, len(tables))
	for _, table := range tables {
		known[table] = true
		incoming[table] = 0
	}
	for _, table := range tables {
		foreignKeys, err := duckdbForeignKeys(ctx, s, database, table)
		if err != nil {
			return nil, fmt.Errorf("读取 DuckDB 外键依赖 %s: %w", table, err)
		}
		seenParents := map[string]bool{}
		for _, foreignKey := range foreignKeys {
			parent := foreignKey.ReferencedTable
			if parent == table || !known[parent] || seenParents[parent] {
				continue
			}
			seenParents[parent] = true
			parents[table] = append(parents[table], parent)
			incoming[parent]++
		}
		sort.Strings(parents[table])
	}

	ready := make([]string, 0, len(tables))
	for table, count := range incoming {
		if count == 0 {
			ready = append(ready, table)
		}
	}
	sort.Strings(ready)
	ordered := make([]string, 0, len(tables))
	for len(ready) > 0 {
		table := ready[0]
		ready = ready[1:]
		ordered = append(ordered, table)
		for _, parent := range parents[table] {
			incoming[parent]--
			if incoming[parent] == 0 {
				ready = append(ready, parent)
				sort.Strings(ready)
			}
		}
	}
	if len(ordered) != len(tables) {
		return nil, fmt.Errorf("DuckDB 表存在循环外键，无法安全清空整个模式")
	}
	return ordered, nil
}

func restoreMySQLForeignKeyChecks(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=1")
}

func restoreSQLiteForeignKeys(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
}
