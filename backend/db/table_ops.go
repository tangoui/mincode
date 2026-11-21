// 表数据操作：分页查询、单元格/行编辑、导出、导入。
// 单行写入的 SQL 分支（updateCellOn / insertRowOn / deleteRowOn）接收执行器，
// 整组提交 db_apply_changes 在同一事务里调用它们（见 safety_rows.go）。
package db

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ── db_get_table_rows ──

func getTableRows(ctx context.Context, s *dbSession, database, table string, limit, offset uint32) (*QueryResult, error) {
	return getTableRowsPage(ctx, s, database, table, limit, offset, "", "")
}

func getTableRowsPage(ctx context.Context, s *dbSession, database, table string, limit, offset uint32, pageToken, namespace string) (*QueryResult, error) {
	if limit == 0 {
		limit = 500
	}
	if limit > 10000 {
		limit = 10000
	}
	if s.vector != nil && s.kind == kindVector {
		return vectorTableRowsPage(ctx, s, database, table, limit, offset, pageToken, namespace)
	}
	if s.kind == kindMongo {
		return mongoTableRows(ctx, s, database, table, int64(limit), int64(offset))
	}
	if s.kind == kindHTTP {
		switch s.httpKind {
		case httpElastic:
			return elasticsearchTableRows(ctx, s, table, limit, offset)
		case httpInflux:
			return influxTableRows(ctx, s, database, table, limit, offset)
		case httpCouch:
			return couchTableRows(ctx, s, table, limit, offset)
		case httpMeili:
			return meilisearchTableRows(ctx, s, table, limit, offset)
		case httpDatabricks, httpBigQuery, httpDremio, httpKylin, httpPinot, httpSnowflake:
			return warehouseTableRows(ctx, s, database, table, limit, offset)
		default:
			return nil, fmt.Errorf("db_get_table_rows 不支持该数据库类型")
		}
	}
	if s.kind == kindCQL {
		return cassandraTableRows(ctx, s, database, table, limit)
	}
	if s.kind == kindNeo4j {
		return neo4jTableRows(ctx, s, table, limit, offset)
	}
	if s.kind != kindSQL {
		return nil, fmt.Errorf("db_get_table_rows 不支持该数据库类型")
	}
	if s.dialect == "postgres" {
		var err error
		database, table, err = resolvePostgresTableTarget(ctx, s, database, table)
		if err != nil {
			return nil, err
		}
	}
	sql, err := selectAllSQL(s.dialect, database, table, limit, offset)
	if err != nil {
		return nil, err
	}
	// 表名已由 selectAllSQL 完整限定，避免为 MySQL 再执行一次冗余 USE 往返。
	return runQuery(ctx, s, sql, nil)
}

// resolvePostgresTableTarget 规范化 PostgreSQL 表引用。
//
// 正常情况下前端会将 schema 放到 database 参数中；但旧表签、跨运行时 IPC
// 或外部调用可能只传表名。过去这种情况会在 selectAllSQL 中静默回退 public，
// 使非 public schema 的表报 42P01。这里从 information_schema 找到唯一匹配的
// schema；若同名表属于多个 schema，则返回明确错误，避免读到错误的数据。
func resolvePostgresTableTarget(ctx context.Context, s *dbSession, database, table string) (string, string, error) {
	schema := strings.TrimSpace(database)
	name := strings.TrimSpace(table)
	if name == "" {
		return "", "", fmt.Errorf("PostgreSQL 表名不能为空")
	}
	if schema == "" {
		if dot := strings.Index(name, "."); dot > 0 && dot < len(name)-1 {
			schema = name[:dot]
			name = name[dot+1:]
		}
	}
	if schema != "" && schema != "public" {
		return schema, name, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT table_schema
		 FROM information_schema.tables
		 WHERE table_name = $1
		   AND table_schema NOT IN ('pg_catalog', 'information_schema')
		 ORDER BY CASE WHEN table_schema = ANY(current_schemas(false)) THEN 0 ELSE 1 END, table_schema
		 LIMIT 2`, name)
	if err != nil {
		return "", "", fmt.Errorf("解析 PostgreSQL 表 schema 失败: %w", err)
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var found string
		if err := rows.Scan(&found); err != nil {
			return "", "", err
		}
		schemas = append(schemas, found)
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	if schema == "public" {
		for _, found := range schemas {
			if found == "public" {
				return schema, name, nil
			}
		}
		// 兼容旧前端：旧版 PostgreSQL schema 树把 schema 放在父节点，
		// 而旧版前端会把裸表名强制标为 public。仅在 public 中不存在且
		// 其他 schema 中唯一命中时才回退，避免静默读取错误的同名表。
		if len(schemas) == 1 {
			return schemas[0], name, nil
		}
	}

	switch len(schemas) {
	case 0:
		return "", "", fmt.Errorf("PostgreSQL 中找不到表 %q", name)
	case 1:
		return schemas[0], name, nil
	default:
		return "", "", fmt.Errorf("表 %q 同时存在于多个 schema（%s）；请重新从左侧 schema 树打开该表", name, strings.Join(schemas, ", "))
	}
}

// resolveSQLWriteTarget 让写路径与读路径解析出同一张 PostgreSQL 表，避免写入 public 下的同名表或报 42P01。
func resolveSQLWriteTarget(ctx context.Context, s *dbSession, database, table string) (string, string, error) {
	if s.dialect != "postgres" {
		return database, table, nil
	}
	return resolvePostgresTableTarget(ctx, s, database, table)
}

func selectAllSQL(dialect, database, table string, limit, offset uint32) (string, error) {
	switch dialect {
	case "mysql":
		return fmt.Sprintf("SELECT * FROM `%s`.`%s` LIMIT %d OFFSET %d",
			escapeBacktick(database), escapeBacktick(table), limit, offset), nil
	case "postgres":
		schema := database
		if strings.TrimSpace(schema) == "" {
			schema = "public"
		}
		return fmt.Sprintf(`SELECT * FROM "%s"."%s" LIMIT %d OFFSET %d`,
			escapeDoubleQuote(schema), escapeDoubleQuote(table), limit, offset), nil
	case "sqlite":
		return fmt.Sprintf("SELECT * FROM %s LIMIT %d OFFSET %d",
			quoteSQLiteIdent(table), limit, offset), nil
	case "duckdb":
		schema := duckdbSchemaName(database)
		return fmt.Sprintf(`SELECT * FROM "%s"."%s" LIMIT %d OFFSET %d`,
			escapeDoubleQuote(schema), escapeDoubleQuote(table), limit, offset), nil
	case "clickhouse":
		// ClickHouse 支持 LIMIT n OFFSET m
		if strings.TrimSpace(database) != "" {
			return fmt.Sprintf("SELECT * FROM `%s`.`%s` LIMIT %d OFFSET %d",
				escapeBacktick(database), escapeBacktick(table), limit, offset), nil
		}
		return fmt.Sprintf("SELECT * FROM `%s` LIMIT %d OFFSET %d",
			escapeBacktick(table), limit, offset), nil
	case "firebird":
		// Firebird 2.5+ 使用 ROWS start TO end，行号从 1 开始。
		start := uint64(offset) + 1
		end := uint64(offset) + uint64(limit)
		return fmt.Sprintf(`SELECT * FROM "%s" ROWS %d TO %d`,
			escapeDoubleQuote(table), start, end), nil
	case "oracle", "yashandb":
		// Oracle 12c+ / YashanDB 支持 OFFSET .. FETCH；库名即 schema，用双引号
		schema := strings.TrimSpace(database)
		if schema != "" {
			return fmt.Sprintf(`SELECT * FROM "%s"."%s" OFFSET %d ROWS FETCH NEXT %d ROWS ONLY`,
				escapeDoubleQuote(schema), escapeDoubleQuote(table), offset, limit), nil
		}
		return fmt.Sprintf(`SELECT * FROM "%s" OFFSET %d ROWS FETCH NEXT %d ROWS ONLY`,
			escapeDoubleQuote(table), offset, limit), nil
	case "sqlserver":
		// SQL Server 2012+ 需 ORDER BY 才能用 OFFSET/FETCH，这里用 (SELECT NULL) 稳定排序
		schema := strings.TrimSpace(database)
		if schema == "" {
			schema = "dbo"
		}
		return fmt.Sprintf(`SELECT * FROM [%s].[%s] ORDER BY (SELECT NULL) OFFSET %d ROWS FETCH NEXT %d ROWS ONLY`,
			escapeSQLServerIdent(schema), escapeSQLServerIdent(table), offset, limit), nil
	case "dm":
		// 达梦 DM8 支持 LIMIT n OFFSET m；database 即 schema，用双引号标识符
		schema := strings.TrimSpace(database)
		if schema != "" {
			return fmt.Sprintf(`SELECT * FROM "%s"."%s" LIMIT %d OFFSET %d`,
				escapeDoubleQuote(schema), escapeDoubleQuote(table), limit, offset), nil
		}
		return fmt.Sprintf(`SELECT * FROM "%s" LIMIT %d OFFSET %d`,
			escapeDoubleQuote(table), limit, offset), nil
	case "tdengine":
		// TDengine 支持 LIMIT n OFFSET m；database.table，用反引号标识符
		if strings.TrimSpace(database) != "" {
			return fmt.Sprintf("SELECT * FROM `%s`.`%s` LIMIT %d OFFSET %d",
				escapeBacktick(database), escapeBacktick(table), limit, offset), nil
		}
		return fmt.Sprintf("SELECT * FROM `%s` LIMIT %d OFFSET %d",
			escapeBacktick(table), limit, offset), nil
	case "trino":
		// Trino 支持 LIMIT（OFFSET 需 ORDER BY，用子查询无法稳定，采用 OFFSET ... LIMIT）
		schema := strings.TrimSpace(database)
		if schema != "" {
			return fmt.Sprintf(`SELECT * FROM "%s"."%s" OFFSET %d LIMIT %d`,
				escapeDoubleQuote(schema), escapeDoubleQuote(table), offset, limit), nil
		}
		return fmt.Sprintf(`SELECT * FROM "%s" OFFSET %d LIMIT %d`,
			escapeDoubleQuote(table), offset, limit), nil
	default:
		return "", fmt.Errorf("db_get_table_rows 不支持该数据库类型")
	}
}

// escapeSQLServerIdent 转义 SQL Server 方括号标识符中的 ]。
func escapeSQLServerIdent(s string) string {
	return strings.ReplaceAll(s, "]", "]]")
}

// ── db_update_cell ──

func updateCell(ctx context.Context, s *dbSession, database, table, pkColumn string, pkValue any, column string, value any) (int64, error) {
	return updateCellByPrimaryKey(ctx, s, database, table, map[string]any{pkColumn: pkValue}, column, value)
}

func updateCellByPrimaryKey(ctx context.Context, s *dbSession, database, table string, primaryKey map[string]any, column string, value any) (int64, error) {
	if s.kind == kindCQL {
		return cassandraUpdateCell(ctx, s, database, table, primaryKey, column, value)
	}
	if len(primaryKey) == 0 {
		return 0, fmt.Errorf("db_update_cell 需要主键")
	}
	if s.kind == kindMongo {
		if len(primaryKey) != 1 {
			return 0, fmt.Errorf("MongoDB 单元格更新仅支持单字段主键")
		}
		for pkColumn, pkValue := range primaryKey {
			return mongoUpdateCell(ctx, s, database, table, pkColumn, pkValue, column, value)
		}
	}
	if s.kind != kindSQL {
		return 0, fmt.Errorf("db_update_cell 不支持该数据库类型")
	}
	database, table, err := resolveSQLWriteTarget(ctx, s, database, table)
	if err != nil {
		return 0, err
	}
	return updateCellOn(ctx, s.db, s, database, table, primaryKey, column, value)
}

// updateCellOn runs the single-cell UPDATE of a SQL session on ex (the
// session's pool or a transaction). database and table are already resolved
// (resolveSQLWriteTarget); pgvector values are converted here.
func updateCellOn(ctx context.Context, ex sqlExecer, s *dbSession, database, table string, primaryKey map[string]any, column string, value any) (int64, error) {
	if len(primaryKey) == 0 {
		return 0, fmt.Errorf("db_update_cell 需要主键")
	}
	tbl, err := qualifiedTable(s.dialect, database, table)
	if err != nil {
		return 0, err
	}
	keyColumns := make([]string, 0, len(primaryKey))
	for name := range primaryKey {
		keyColumns = append(keyColumns, name)
	}
	sort.Strings(keyColumns)
	conditions := make([]string, len(keyColumns))
	args := make([]any, 0, len(keyColumns)+1)
	if s.vector != nil && s.vector.provider == "pgvector" {
		value, err = pgvectorMutationValue(ctx, s, database, table, column, value)
		if err != nil {
			return 0, err
		}
	}
	args = append(args, jsonToSQLValue(value))
	for i, name := range keyColumns {
		conditions[i] = fmt.Sprintf("%s = %s", quoteColumn(s.dialect, name), placeholder(s.dialect, i+2))
		args = append(args, jsonToSQLValue(primaryKey[name]))
	}
	sql := fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s", tbl, quoteColumn(s.dialect, column), placeholder(s.dialect, 1), strings.Join(conditions, " AND "))
	res, err := ex.ExecContext(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ── db_insert_row ──

func insertRow(ctx context.Context, s *dbSession, database, table string, fields map[string]any) (int64, error) {
	if s.kind == kindMongo {
		return mongoInsertRow(ctx, s, database, table, fields)
	}
	if s.kind != kindSQL {
		return 0, fmt.Errorf("db_insert_row 不支持该数据库类型")
	}
	database, table, err := resolveSQLWriteTarget(ctx, s, database, table)
	if err != nil {
		return 0, err
	}
	return insertRowOn(ctx, s.db, s, database, table, fields)
}

// insertRowOn runs the single-row INSERT of a SQL session on ex. database
// and table are already resolved (resolveSQLWriteTarget).
func insertRowOn(ctx context.Context, ex sqlExecer, s *dbSession, database, table string, fields map[string]any) (int64, error) {
	query, args, err := insertRowStatement(ctx, s, database, table, fields)
	if err != nil {
		return 0, err
	}
	res, err := ex.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// insertRowStatement builds the INSERT of one row: DM and Firebird leave
// out auto-increment columns, pgvector values are converted.
func insertRowStatement(ctx context.Context, s *dbSession, database, table string, fields map[string]any) (string, []any, error) {
	fields, err := insertRowFields(ctx, s, database, table, fields)
	if err != nil {
		return "", nil, err
	}
	if s.vector != nil && s.vector.provider == "pgvector" {
		fields, err = pgvectorMutationFields(ctx, s, database, table, fields)
		if err != nil {
			return "", nil, err
		}
	}
	tbl, err := qualifiedTable(s.dialect, database, table)
	if err != nil {
		return "", nil, err
	}
	cols := make([]string, 0, len(fields))
	vals := make([]any, 0, len(fields))
	for k, v := range fields {
		cols = append(cols, k)
		vals = append(vals, jsonToSQLValue(v))
	}
	colIdents := make([]string, len(cols))
	phs := make([]string, len(cols))
	for i, c := range cols {
		colIdents[i] = quoteColumn(s.dialect, c)
		phs[i] = placeholder(s.dialect, i+1)
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tbl, strings.Join(colIdents, ", "), strings.Join(phs, ", ")), vals, nil
}

// insertRowFields is the fields an INSERT actually writes: DM and Firebird
// leave out auto-increment columns. Empty is an error.
func insertRowFields(ctx context.Context, s *dbSession, database, table string, fields map[string]any) (map[string]any, error) {
	if s.dialect == "dm" {
		columns, err := dmColumns(ctx, s, database, table)
		if err != nil {
			return nil, fmt.Errorf("读取达梦表列信息: %w", err)
		}
		fields = withoutAutoIncrementFields(fields, columns)
	}
	if s.dialect == "firebird" {
		columns, err := firebirdColumns(ctx, s, table)
		if err != nil {
			return nil, fmt.Errorf("读取 Firebird 表列信息: %w", err)
		}
		fields = withoutAutoIncrementFields(fields, columns)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("插入字段为空")
	}
	return fields, nil
}

func withoutAutoIncrementFields(fields map[string]any, columns []ColumnInfo) map[string]any {
	autoIncrement := make(map[string]struct{})
	for _, column := range columns {
		if column.AutoIncrement {
			autoIncrement[strings.ToLower(column.Name)] = struct{}{}
		}
	}
	if len(autoIncrement) == 0 {
		return fields
	}
	filtered := make(map[string]any, len(fields))
	for name, value := range fields {
		if _, excluded := autoIncrement[strings.ToLower(name)]; !excluded {
			filtered[name] = value
		}
	}
	return filtered
}

// ── db_delete_row ──

func deleteRow(ctx context.Context, s *dbSession, database, table, pkColumn string, pkValue any) (int64, error) {
	return deleteRowByPrimaryKey(ctx, s, database, table, map[string]any{pkColumn: pkValue})
}

func deleteRowByPrimaryKey(ctx context.Context, s *dbSession, database, table string, primaryKey map[string]any) (int64, error) {
	if s.kind == kindCQL {
		return cassandraDeleteRow(ctx, s, database, table, primaryKey)
	}
	if len(primaryKey) == 0 {
		return 0, fmt.Errorf("db_delete_row 需要主键")
	}
	if s.kind == kindMongo {
		if len(primaryKey) != 1 {
			return 0, fmt.Errorf("MongoDB 行删除仅支持单字段主键")
		}
		for pkColumn, pkValue := range primaryKey {
			return mongoDeleteRow(ctx, s, database, table, pkColumn, pkValue)
		}
	}
	if s.kind != kindSQL {
		return 0, fmt.Errorf("db_delete_row 不支持该数据库类型")
	}
	database, table, err := resolveSQLWriteTarget(ctx, s, database, table)
	if err != nil {
		return 0, err
	}
	return deleteRowOn(ctx, s.db, s, database, table, primaryKey)
}

// deleteRowOn runs the by-key DELETE of a SQL session on ex. database and
// table are already resolved (resolveSQLWriteTarget).
func deleteRowOn(ctx context.Context, ex sqlExecer, s *dbSession, database, table string, primaryKey map[string]any) (int64, error) {
	if len(primaryKey) == 0 {
		return 0, fmt.Errorf("db_delete_row 需要主键")
	}
	tbl, err := qualifiedTable(s.dialect, database, table)
	if err != nil {
		return 0, err
	}
	keyColumns := make([]string, 0, len(primaryKey))
	for name := range primaryKey {
		keyColumns = append(keyColumns, name)
	}
	sort.Strings(keyColumns)
	conditions := make([]string, len(keyColumns))
	args := make([]any, 0, len(keyColumns))
	for i, name := range keyColumns {
		conditions[i] = fmt.Sprintf("%s = %s", quoteColumn(s.dialect, name), placeholder(s.dialect, i+1))
		args = append(args, jsonToSQLValue(primaryKey[name]))
	}
	sql := fmt.Sprintf("DELETE FROM %s WHERE %s", tbl, strings.Join(conditions, " AND "))
	res, err := ex.ExecContext(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ── db_export_table ──

func exportTable(ctx context.Context, s *dbSession, database, table, format, mode string, limit uint32) (string, error) {
	if s.kind != kindSQL {
		return "", fmt.Errorf("db_export_table 不支持该数据库类型")
	}
	dialect := s.dialect
	if limit == 0 {
		limit = 10000
	}
	result, err := getTableRows(ctx, s, database, table, limit, 0)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "csv":
		return exportCSV(result)
	case "json":
		return exportJSON(result)
	case "markdown", "md":
		return exportMarkdown(result), nil
	case "txt":
		return exportTXT(result), nil
	case "sql":
		return exportInsertSQL(result, dialect, database, table, mode)
	case "xlsx":
		// 二进制格式以 base64 返回，前端解码后走二进制写盘。
		return exportXLSX(result)
	default:
		return "", fmt.Errorf("不支持的导出格式: %s", format)
	}
}

func exportCSV(r *QueryResult) (string, error) {
	var b strings.Builder
	w := csv.NewWriter(&b)
	if err := w.Write(r.Columns); err != nil {
		return "", err
	}
	for _, row := range r.Rows {
		rec := make([]string, len(row))
		for i, v := range row {
			rec[i] = valueToCSVString(v)
		}
		if err := w.Write(rec); err != nil {
			return "", err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func exportJSON(r *QueryResult) (string, error) {
	objs := make([]map[string]any, 0, len(r.Rows))
	for _, row := range r.Rows {
		obj := map[string]any{}
		for i, col := range r.Columns {
			if i < len(row) {
				obj[col] = row[i]
			}
		}
		objs = append(objs, obj)
	}
	b, err := json.MarshalIndent(objs, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ── db_import_data ──

func importData(ctx context.Context, s *dbSession, database, table, format, data string) (int64, error) {
	if s.kind != kindSQL {
		return 0, fmt.Errorf("db_import_data 不支持该数据库类型")
	}
	var records []map[string]any
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "csv":
		recs, err := parseCSVImport(data)
		if err != nil {
			return 0, err
		}
		records = recs
	case "json":
		if err := json.Unmarshal([]byte(data), &records); err != nil {
			return 0, fmt.Errorf("解析 JSON 失败: %w", err)
		}
	default:
		return 0, fmt.Errorf("不支持的导入格式: %s", format)
	}
	var inserted int64
	for _, rec := range records {
		n, err := insertRow(ctx, s, database, table, rec)
		if err != nil {
			return inserted, err
		}
		inserted += n
	}
	return inserted, nil
}

func parseCSVImport(data string) ([]map[string]any, error) {
	r := csv.NewReader(strings.NewReader(data))
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("解析 CSV 失败: %w", err)
	}
	if len(rows) < 1 {
		return nil, nil
	}
	header := rows[0]
	out := make([]map[string]any, 0, len(rows)-1)
	for _, row := range rows[1:] {
		obj := map[string]any{}
		for i, col := range header {
			if i < len(row) {
				obj[col] = row[i]
			}
		}
		out = append(out, obj)
	}
	return out, nil
}

// ── 辅助 ──

func qualifiedTable(dialect, database, table string) (string, error) {
	switch dialect {
	case "mysql":
		return fmt.Sprintf("`%s`.`%s`", escapeBacktick(database), escapeBacktick(table)), nil
	case "postgres":
		schema := database
		if strings.TrimSpace(schema) == "" {
			schema = "public"
		}
		return fmt.Sprintf(`"%s"."%s"`, escapeDoubleQuote(schema), escapeDoubleQuote(table)), nil
	case "sqlite":
		return quoteSQLiteIdent(table), nil
	case "firebird":
		return fmt.Sprintf(`"%s"`, escapeDoubleQuote(table)), nil
	case "duckdb":
		schema := duckdbSchemaName(database)
		return fmt.Sprintf(`"%s"."%s"`, escapeDoubleQuote(schema), escapeDoubleQuote(table)), nil
	case "clickhouse":
		database = strings.TrimSpace(database)
		if database == "" {
			return "`" + escapeBacktick(table) + "`", nil
		}
		return fmt.Sprintf("`%s`.`%s`", escapeBacktick(database), escapeBacktick(table)), nil
	case "oracle", "yashandb", "dm":
		// Oracle / YashanDB / 达梦 DM8：database 即 schema，用双引号标识符
		schema := strings.TrimSpace(database)
		if schema != "" {
			return fmt.Sprintf(`"%s"."%s"`, escapeDoubleQuote(schema), escapeDoubleQuote(table)), nil
		}
		return fmt.Sprintf(`"%s"`, escapeDoubleQuote(table)), nil
	case "sqlserver":
		schema := strings.TrimSpace(database)
		if schema == "" {
			schema = "dbo"
		}
		return fmt.Sprintf(`[%s].[%s]`, escapeSQLServerIdent(schema), escapeSQLServerIdent(table)), nil
	default:
		return "", fmt.Errorf("不支持该数据库类型")
	}
}

func quoteColumn(dialect, col string) string {
	switch dialect {
	case "mysql":
		return "`" + escapeBacktick(col) + "`"
	case "sqlserver":
		return "[" + escapeSQLServerIdent(col) + "]"
	default:
		return "\"" + escapeDoubleQuote(col) + "\""
	}
}

// placeholder 返回各数据库驱动支持的参数占位符。
func placeholder(dialect string, n int) string {
	switch dialect {
	case "postgres":
		return fmt.Sprintf("$%d", n)
	case "oracle":
		return fmt.Sprintf(":%d", n)
	case "sqlserver":
		return fmt.Sprintf("@p%d", n)
	default:
		return "?"
	}
}

func escapeBacktick(s string) string    { return strings.ReplaceAll(s, "`", "``") }
func escapeDoubleQuote(s string) string { return strings.ReplaceAll(s, "\"", "\"\"") }

// jsonToSQLValue 将来自 JSON 的值转为可绑定的 SQL 参数。
func jsonToSQLValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case float64:
		// JSON 数字统一为 float64；若是整数则转为 int64 避免小数点
		if x == float64(int64(x)) {
			return int64(x)
		}
		return x
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	case bool, string:
		return x
	case map[string]any, []any:
		encoded, err := json.Marshal(x)
		if err == nil {
			return string(encoded)
		}
		return fmt.Sprintf("%v", x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func valueToCSVString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}
