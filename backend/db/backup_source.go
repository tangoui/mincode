package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// backupDumpDialect 返回会话对应的备份输出方言；不支持时返回说明原因的错误。
func backupDumpDialect(s *dbSession) (string, error) {
	if s == nil {
		return "", fmt.Errorf("会话不存在")
	}
	if s.kind != kindSQL || s.vector != nil {
		return "", fmt.Errorf("该数据库类型不是 SQL 数据库，不支持定时备份")
	}
	switch s.dialect {
	case "mysql", "postgres", "sqlite", "sqlserver", "oracle", "dm", "duckdb", "firebird":
		return s.dialect, nil
	}
	return "", fmt.Errorf("数据库方言 %q 暂不支持定时备份", s.dialect)
}

// sessionBackupSource 基于已建立会话的数据源。rowQuerier 在 MySQL / PostgreSQL 上
// 是持有一致性快照事务的独占连接，其余方言直接使用连接池。
type sessionBackupSource struct {
	s          *dbSession
	dialect    string
	rowQuerier backupQuerier
	release    func()
	preamble   []string
	warnings   []string
}

type backupQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func newSessionBackupSource(ctx context.Context, s *dbSession) (*sessionBackupSource, error) {
	dialect, err := backupDumpDialect(s)
	if err != nil {
		return nil, err
	}
	src := &sessionBackupSource{s: s, dialect: dialect, release: func() {}}
	if s.kind == kindSQL {
		src.rowQuerier = s.db
		src.beginSnapshot(ctx)
	}
	return src, nil
}

// beginSnapshot 尽力开启只读一致性快照；失败时退回普通读取并记录警告。
// MySQL 读取连接固定为 UTC 会话时区，与转储头部的 SET TIME_ZONE 对应。
func (src *sessionBackupSource) beginSnapshot(ctx context.Context) {
	if src.dialect != "mysql" && src.dialect != "postgres" {
		return
	}
	conn, err := src.s.db.Conn(ctx)
	if err != nil {
		src.warnings = append(src.warnings, "无法获取独立连接，备份未使用一致性快照")
		return
	}
	snapshot := false
	if src.dialect == "mysql" {
		if _, err := conn.ExecContext(ctx, "SET SESSION time_zone = '+00:00'"); err == nil {
			src.preamble = append(src.preamble, "SET TIME_ZONE='+00:00'")
		} else {
			src.warnings = append(src.warnings, "无法设置会话时区，时间值按服务器时区导出")
		}
		if _, err := conn.ExecContext(ctx, "SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err == nil {
			_, err = conn.ExecContext(ctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT")
			snapshot = err == nil
		}
	} else {
		_, err := conn.ExecContext(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY")
		snapshot = err == nil
		if !snapshot {
			_ = conn.Close()
			src.warnings = append(src.warnings, "数据库不支持一致性快照事务，各表数据可能不在同一时间点")
			return
		}
	}
	if !snapshot {
		src.warnings = append(src.warnings, "数据库不支持一致性快照事务，各表数据可能不在同一时间点")
	}
	src.rowQuerier = conn
	src.release = func() {
		if snapshot {
			_, _ = conn.ExecContext(context.Background(), "COMMIT")
		}
		_ = conn.Close()
	}
}

func (src *sessionBackupSource) close() {
	if src.release != nil {
		src.release()
		src.release = nil
	}
}

func (src *sessionBackupSource) dumpDialect() string { return src.dialect }

func (src *sessionBackupSource) qualifyTables() bool {
	return src.dialect != "sqlite" && src.dialect != "firebird"
}

func backupKeepTableKind(kind string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return true
	}
	if strings.Contains(kind, "view") || strings.Contains(kind, "sequence") {
		return false
	}
	return strings.Contains(kind, "table")
}

func (src *sessionBackupSource) listTables(ctx context.Context, database string) ([]string, error) {
	s := src.s
	var tables []SchemaTable
	var err error
	switch {
	case src.dialect == "sqlite":
		return src.sqliteTables(ctx)
	case src.dialect == "mysql":
		tables, err = mysqlDatabaseTables(ctx, s, database)
	default:
		var info *DbSchemaInfo
		info, err = getSchema(ctx, s)
		if err == nil {
			found := false
			for _, item := range info.Databases {
				if item.Name == database || strings.EqualFold(item.Name, database) {
					tables = item.Tables
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("找不到数据库或 Schema: %s", database)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tables))
	for _, table := range tables {
		if backupKeepTableKind(table.Kind) {
			names = append(names, table.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (src *sessionBackupSource) sqliteTables(ctx context.Context) ([]string, error) {
	rows, err := src.s.db.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := scanStringColumn(rows)
	if err != nil {
		return nil, err
	}
	return names, rows.Err()
}

func (src *sessionBackupSource) queryStrings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := src.s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		if value.Valid && strings.TrimSpace(value.String) != "" {
			out = append(out, value.String)
		}
	}
	return out, rows.Err()
}

func (src *sessionBackupSource) tableDDL(ctx context.Context, database, table string) (backupTableDDL, error) {
	s := src.s
	switch src.dialect {
	case "mysql":
		ddl, err := getTableDDL(ctx, s, database, table)
		if err != nil {
			return backupTableDDL{}, err
		}
		return backupTableDDL{Drop: "DROP TABLE IF EXISTS " + schemaQuoteIdentifier("mysql", table), Create: ddl}, nil
	case "sqlite":
		ddl, err := getTableDDL(ctx, s, database, table)
		if err != nil {
			return backupTableDDL{}, err
		}
		after, err := src.queryStrings(ctx,
			"SELECT sql FROM sqlite_master WHERE type IN ('index', 'trigger') AND tbl_name = ? AND sql IS NOT NULL ORDER BY type, name", table)
		if err != nil {
			return backupTableDDL{}, fmt.Errorf("读取索引与触发器: %w", err)
		}
		return backupTableDDL{Drop: "DROP TABLE IF EXISTS " + quoteSQLiteIdent(table), Create: ddl, After: after}, nil
	case "duckdb":
		ddl, err := getTableDDL(ctx, s, database, table)
		if err != nil {
			return backupTableDDL{}, err
		}
		qualified := schemaQualifiedTable("duckdb", duckdbSchemaName(database), table)
		return backupTableDDL{Drop: "DROP TABLE IF EXISTS " + qualified, Create: ddl}, nil
	case "firebird":
		ddl, err := getTableDDL(ctx, s, database, table)
		if err != nil {
			return backupTableDDL{}, err
		}
		return backupTableDDL{Create: ddl}, nil
	case "postgres", "sqlserver", "oracle", "dm":
		snapshot, err := backupTableSnapshot(ctx, s, database, table)
		if err != nil {
			return backupTableDDL{}, err
		}
		return buildBackupTableDDL(src.dialect, database, snapshot)
	}
	if src.dialect == "clickhouse" || src.dialect == "tdengine" || src.dialect == "trino" {
		qualified, err := qualifiedTable(src.dialect, database, table)
		if err != nil {
			return backupTableDDL{}, err
		}
		var ddl string
		if err := s.db.QueryRowContext(ctx, "SHOW CREATE TABLE "+qualified).Scan(&ddl); err != nil {
			return backupTableDDL{}, fmt.Errorf("读取原生建表定义: %w", err)
		}
		if strings.TrimSpace(ddl) == "" {
			return backupTableDDL{}, fmt.Errorf("服务器返回空建表定义")
		}
		return backupTableDDL{Create: ddl}, nil
	}
	return backupTableDDL{}, fmt.Errorf("不支持的方言 %s", src.dialect)
}

func backupTableSnapshot(ctx context.Context, s *dbSession, database, table string) (SchemaTableSnapshot, error) {
	columns, err := getTableColumns(ctx, s, database, table)
	if err != nil {
		return SchemaTableSnapshot{}, fmt.Errorf("读取字段: %w", err)
	}
	primaryKey, err := getPrimaryKeys(ctx, s, database, table)
	if err != nil {
		return SchemaTableSnapshot{}, fmt.Errorf("读取主键: %w", err)
	}
	indexes, err := listIndexes(ctx, s, database, table)
	if err != nil {
		return SchemaTableSnapshot{}, fmt.Errorf("读取索引: %w", err)
	}
	snapshot := SchemaTableSnapshot{Name: table}
	if primaryKey != nil {
		snapshot.PrimaryKey = append([]string(nil), primaryKey.Columns...)
		snapshot.PrimaryKeyName = primaryKey.ConstraintName
	}
	for _, column := range columns {
		snapshot.Columns = append(snapshot.Columns, SchemaColumnSnapshot{
			Name: column.Name, DataType: column.DataType, Nullable: column.Nullable,
			DefaultValue: column.DefaultValue, AutoIncrement: column.AutoIncrement,
		})
	}
	for _, index := range indexes {
		snapshot.Indexes = append(snapshot.Indexes, SchemaIndexSnapshot{
			Name: index.Name, Columns: append([]string(nil), index.Columns...), Unique: index.Unique,
		})
	}
	sort.Slice(snapshot.Indexes, func(i, j int) bool { return snapshot.Indexes[i].Name < snapshot.Indexes[j].Name })
	return snapshot, nil
}

// buildBackupTableDDL 由结构快照生成带正确引号的建表、索引语句。
// PostgreSQL 的 nextval 默认值改写为 SERIAL 系列（序列随表创建），数据写入后校正序列。
func buildBackupTableDDL(dialect, database string, table SchemaTableSnapshot) (backupTableDDL, error) {
	schema := database
	switch dialect {
	case "postgres":
		schema = backupPostgresSchema(database)
	case "sqlserver":
		schema = backupSQLServerSchema(database)
	}
	qualified := schemaQualifiedTable(dialect, schema, table.Name)
	out := backupTableDDL{}
	var sequenceColumns []string
	if dialect == "postgres" {
		columns := make([]SchemaColumnSnapshot, len(table.Columns))
		for i, column := range table.Columns {
			columns[i] = column
			if !column.AutoIncrement {
				continue
			}
			sequenceColumns = append(sequenceColumns, column.Name)
			if column.DefaultValue != nil && strings.Contains(strings.ToLower(*column.DefaultValue), "nextval(") {
				columns[i].DefaultValue = nil
				columns[i].AutoIncrement = false
				switch strings.ToLower(strings.TrimSpace(column.DataType)) {
				case "bigint", "int8":
					columns[i].DataType = "BIGSERIAL"
				case "smallint", "int2":
					columns[i].DataType = "SMALLSERIAL"
				default:
					columns[i].DataType = "SERIAL"
				}
			} else {
				columns[i].AutoIncrement = false
				columns[i].DataType += " GENERATED BY DEFAULT AS IDENTITY"
			}
		}
		table.Columns = columns
	}
	for _, column := range table.Columns {
		if column.AutoIncrement && dialect == "sqlserver" {
			out.IdentityInsert = true
		}
	}
	create, err := createTableSQL(dialect, schema, table)
	if err != nil {
		return backupTableDDL{}, err
	}
	out.Create = create
	switch dialect {
	case "postgres":
		out.Drop = "DROP TABLE IF EXISTS " + qualified
	case "sqlserver":
		out.Drop = "IF OBJECT_ID(" + backupSQLServerString(qualified) + ", N'U') IS NOT NULL DROP TABLE " + qualified
	}
	for _, index := range table.Indexes {
		if len(index.Columns) == 0 || strings.TrimSpace(index.Name) == "" {
			continue
		}
		stmt := "CREATE "
		if index.Unique {
			stmt += "UNIQUE "
		}
		name := schemaQuoteIdentifier(dialect, index.Name)
		if dialect == "oracle" || dialect == "dm" {
			name = schemaQualifiedTable(dialect, schema, index.Name)
		}
		stmt += "INDEX " + name + " ON " + qualified + " (" + quoteIdentifierList(dialect, index.Columns) + ")"
		out.After = append(out.After, stmt)
	}
	for _, column := range sequenceColumns {
		quotedColumn := schemaQuoteIdentifier("postgres", column)
		out.After = append(out.After, fmt.Sprintf(
			"SELECT setval(pg_get_serial_sequence(%s, %s), COALESCE(MAX(%s), 1), MAX(%s) IS NOT NULL) FROM %s",
			sqlExportQuotedString("postgres", qualified), sqlExportQuotedString("postgres", column),
			quotedColumn, quotedColumn, qualified))
	}
	return out, nil
}

// insertColumns 返回可写入的列（排除生成列 / 计算列 / rowversion）；返回 nil 表示使用 SELECT *。
func (src *sessionBackupSource) insertColumns(ctx context.Context, database, table string) []string {
	var query string
	var args []any
	switch src.dialect {
	case "sqlite":
		query = "SELECT name FROM pragma_table_info(?) ORDER BY cid"
		args = []any{table}
	case "mysql":
		query = `SELECT COLUMN_NAME FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND UPPER(EXTRA) NOT LIKE '%GENERATED%'
			ORDER BY ORDINAL_POSITION`
		args = []any{database, table}
	case "postgres":
		query = `SELECT attname FROM pg_catalog.pg_attribute
			WHERE attrelid = to_regclass($1) AND attnum > 0 AND NOT attisdropped AND attgenerated = ''
			ORDER BY attnum`
		args = []any{schemaQualifiedTable("postgres", backupPostgresSchema(database), table)}
	case "sqlserver":
		query = `SELECT c.name FROM sys.columns c
			WHERE c.object_id = OBJECT_ID(@p1) AND c.is_computed = 0 AND TYPE_NAME(c.system_type_id) <> 'timestamp'
			ORDER BY c.column_id`
		args = []any{schemaQualifiedTable("sqlserver", backupSQLServerSchema(database), table)}
	default:
		return nil
	}
	columns, err := src.queryStrings(ctx, query, args...)
	if err != nil || len(columns) == 0 {
		return nil
	}
	return columns
}

func (src *sessionBackupSource) streamRows(ctx context.Context, database, table string, onColumns func([]backupColumn) error, onRow func([]any) error) error {
	qualified, err := qualifiedTable(src.dialect, database, table)
	if err != nil {
		return err
	}
	selectList := "*"
	if columns := src.insertColumns(ctx, database, table); len(columns) > 0 {
		quoted := make([]string, len(columns))
		for i, column := range columns {
			quoted[i] = schemaQuoteIdentifier(src.dialect, column)
			if src.dialect == "sqlite" {
				// 一元 + 去掉声明类型，驱动按存储类原样返回值（不把 DATETIME 文本解析为时间）。
				quoted[i] = "+" + quoted[i]
			}
		}
		selectList = strings.Join(quoted, ", ")
		if src.dialect == "sqlite" {
			return src.streamSQLRows(ctx, "SELECT "+selectList+" FROM "+qualified, columns, onColumns, onRow)
		}
	}
	return src.streamSQLRows(ctx, "SELECT "+selectList+" FROM "+qualified, nil, onColumns, onRow)
}

func (src *sessionBackupSource) streamSQLRows(ctx context.Context, query string, names []string, onColumns func([]backupColumn) error, onRow func([]any) error) error {
	rows, err := src.rowQuerier.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	columns := make([]backupColumn, len(types))
	for i, columnType := range types {
		name := columnType.Name()
		if i < len(names) {
			name = names[i]
		}
		columns[i] = backupColumn{Name: name, Type: strings.ToUpper(columnType.DatabaseTypeName())}
	}
	if err := onColumns(columns); err != nil {
		return err
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	for rows.Next() {
		for i := range values {
			values[i] = nil
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		if err := onRow(values); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (src *sessionBackupSource) extraDatabaseDDL(ctx context.Context, _ string) ([]string, error) {
	if src.s.kind != kindSQL || src.dialect != "sqlite" {
		return nil, nil
	}
	return src.queryStrings(ctx,
		"SELECT sql FROM sqlite_master WHERE type = 'view' AND sql IS NOT NULL ORDER BY name")
}
