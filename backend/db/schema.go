// Schema 元数据查询：库/表结构、列、主键、索引、外键、触发器、DDL。
// 按 SQLite / PostgreSQL / MySQL 三方言实现；其余方言返回“暂不支持”。
package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ── 返回结构（camelCase） ──

// DbIndexInfo 索引信息。
type DbIndexInfo struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}

// DbForeignKeyInfo 外键信息。
type DbForeignKeyInfo struct {
	Name              string   `json:"name"`
	Columns           []string `json:"columns"`
	ReferencedTable   string   `json:"referencedTable"`
	ReferencedColumns []string `json:"referencedColumns"`
}

// DbTriggerInfo 触发器信息。
type DbTriggerInfo struct {
	Name   string `json:"name"`
	Timing string `json:"timing"`
	Event  string `json:"event"`
}

// PrimaryKeyInfo 主键列及其真实约束名。
type PrimaryKeyInfo struct {
	Columns        []string `json:"columns"`
	ConstraintName string   `json:"constraintName,omitempty"`
}

// ── db_get_schema ──

func getSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	if s.kind == kindPlugin {
		return externalSchema(ctx, s)
	}
	info, err := dispatchGetSchema(ctx, s)
	if err != nil {
		return nil, err
	}
	normalizeSchemaInfo(info)
	return info, nil
}

func getNavigationSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	if s.kind == kindPlugin {
		return externalNavigationSchema(ctx, s)
	}
	if s.kind == kindSQL && s.dialect == "mysql" {
		info, err := mysqlNavigationSchema(ctx, s)
		if err != nil {
			return nil, err
		}
		normalizeSchemaInfo(info)
		return info, nil
	}
	if s.kind == kindRedis {
		info, err := redisNavigationSchema(ctx, s)
		if err != nil {
			return nil, err
		}
		normalizeSchemaInfo(info)
		return info, nil
	}
	if s.kind == kindMongo {
		info, err := mongoNavigationSchema(ctx, s)
		if err != nil {
			return nil, err
		}
		normalizeSchemaInfo(info)
		return info, nil
	}
	if s.kind == kindVector && s.vector != nil {
		if info := s.vector.takeInitialSchema(); info != nil {
			normalizeSchemaInfo(info)
			return info, nil
		}
	}
	return getSchema(ctx, s)
}

func getDatabaseTables(ctx context.Context, s *dbSession, database string) ([]SchemaTable, error) {
	if s.kind == kindPlugin {
		return externalDatabaseTables(ctx, s, database)
	}
	if s.kind == kindMongo {
		return mongoDatabaseTables(ctx, s, database)
	}
	if s.kind == kindSQL && s.dialect == "sqlite" {
		return sqliteDatabaseTables(ctx, s)
	}
	if s.kind == kindSQL && supportsTableStats(s.dialect) {
		return sqlDatabaseTablesWithStats(ctx, s, database)
	}
	if s.kind != kindSQL || s.dialect != "mysql" {
		return nil, fmt.Errorf("当前数据库类型不支持按库加载对象")
	}
	return mysqlDatabaseTables(ctx, s, database)
}

// normalizeSchemaInfo 把各驱动可能产生的 nil slice（`var x []T` 零值）统一替换为
// 空 slice，避免 JSON 序列化为 null——前端对 databases/tables 做展开运算符（...）时
// 若拿到 null 会直接抛 TypeError（如 ES 索引全部为系统索引/空库时曾复现）。
func normalizeSchemaInfo(info *DbSchemaInfo) {
	if info == nil {
		return
	}
	if info.Databases == nil {
		info.Databases = []SchemaDatabase{}
		return
	}
	for i := range info.Databases {
		if info.Databases[i].Tables == nil {
			info.Databases[i].Tables = []SchemaTable{}
		}
	}
}

func dispatchGetSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	if s.vector != nil {
		return vectorSchema(ctx, s)
	}
	if s.kind == kindMongo {
		return mongoSchema(ctx, s)
	}
	if s.kind == kindHTTP {
		return httpSchema(ctx, s)
	}
	if s.kind == kindCQL {
		return cassandraSchema(ctx, s)
	}
	if s.kind == kindNeo4j {
		return neo4jSchema(ctx, s)
	}
	if s.kind == kindRedis {
		return redisSchema(ctx, s)
	}
	if s.kind != kindSQL {
		return nil, fmt.Errorf("该会话类型不支持 Schema 查询")
	}
	switch s.dialect {
	case "sqlite":
		return sqliteSchema(ctx, s)
	case "duckdb":
		return duckdbSchema(ctx, s)
	case "mysql":
		return mysqlSchema(ctx, s)
	case "postgres":
		return postgresSchema(ctx, s)
	case "dm":
		return dmSchema(ctx, s)
	case "tdengine":
		return tdengineSchema(ctx, s)
	case "oracle", "yashandb":
		return oracleSchema(ctx, s)
	case "firebird":
		return firebirdSchema(ctx, s)
	case "sqlserver":
		return sqlserverSchema(ctx, s)
	case "clickhouse":
		return clickhouseSchema(ctx, s)
	case "trino":
		return trinoSchema(ctx, s)
	default:
		return nil, fmt.Errorf("db_get_schema 不支持该数据库类型")
	}
}

func sqliteSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT name, type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []SchemaTable
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, err
		}
		tables = append(tables, SchemaTable{Name: name, Kind: kind})
	}
	dbName := s.defaultDatabase
	if dbName == "" {
		dbName = "main"
	}
	return &DbSchemaInfo{
		Databases: []SchemaDatabase{{Name: dbName, Tables: tables}},
	}, nil
}

// sqliteDatabaseTables 为对象概览补充行数与磁盘占用。SQLite 不保存表统计，只能逐表 COUNT(*)
// 并通过 dbstat 汇总页大小（含该表索引）；两者都需扫描数据，各自限时，超时的表不显示统计。
// SQLite 没有表级创建/更新时间和注释。
func sqliteDatabaseTables(ctx context.Context, s *dbSession) ([]SchemaTable, error) {
	info, err := sqliteSchema(ctx, s)
	if err != nil {
		return nil, err
	}
	tables := info.Databases[0].Tables
	if tables == nil {
		return []SchemaTable{}, nil
	}

	countCtx, cancelCount := context.WithTimeout(ctx, 3*time.Second)
	defer cancelCount()
	for index := range tables {
		if tables[index].Kind != "table" || countCtx.Err() != nil {
			continue
		}
		var count int64
		if err := s.db.QueryRowContext(countCtx, "SELECT COUNT(*) FROM "+quoteSQLiteIdent(tables[index].Name)).Scan(&count); err == nil {
			tables[index].RowCount = &count
		}
	}

	sizeCtx, cancelSize := context.WithTimeout(ctx, 2*time.Second)
	defer cancelSize()
	rows, err := s.db.QueryContext(sizeCtx, `
		SELECT m.tbl_name, SUM(d.pgsize)
		FROM dbstat AS d JOIN sqlite_master AS m ON m.name = d.name
		GROUP BY m.tbl_name`)
	if err != nil {
		return tables, nil
	}
	defer rows.Close()
	sizes := make(map[string]int64)
	for rows.Next() {
		var name string
		var size int64
		if rows.Scan(&name, &size) == nil {
			sizes[name] = size
		}
	}
	if rows.Err() != nil {
		return tables, nil
	}
	for index := range tables {
		if size, ok := sizes[tables[index].Name]; ok && tables[index].Kind == "table" {
			tables[index].SizeBytes = &size
		}
	}
	return tables, nil
}

func mysqlSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	info, err := mysqlNavigationSchema(ctx, s)
	if err != nil {
		return nil, err
	}
	indexes := make(map[string]int, len(info.Databases))
	for index, database := range info.Databases {
		indexes[database.Name] = index
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA NOT IN ('information_schema','mysql','performance_schema','sys')
		ORDER BY TABLE_SCHEMA, TABLE_NAME`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var databaseName, name string
		var tableType sql.NullString
		if err := rows.Scan(&databaseName, &name, &tableType); err != nil {
			return nil, err
		}
		index, ok := indexes[databaseName]
		if !ok {
			index = len(info.Databases)
			indexes[databaseName] = index
			info.Databases = append(info.Databases, SchemaDatabase{Name: databaseName, Tables: []SchemaTable{}})
		}
		kind := "table"
		if strings.Contains(strings.ToUpper(tableType.String), "VIEW") {
			kind = "view"
		}
		info.Databases[index].Tables = append(info.Databases[index].Tables, SchemaTable{Name: name, Kind: kind})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(info.Databases, func(i, j int) bool {
		return info.Databases[i].Name < info.Databases[j].Name
	})
	return info, nil
}

func mysqlNavigationSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME NOT IN ('information_schema','mysql','performance_schema','sys') ORDER BY SCHEMA_NAME")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	databases := make([]SchemaDatabase, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		databases = append(databases, SchemaDatabase{Name: name, Tables: []SchemaTable{}})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &DbSchemaInfo{Databases: databases}, nil
}

func mysqlDatabaseTables(ctx context.Context, s *dbSession, database string) ([]SchemaTable, error) {
	database = strings.TrimSpace(database)
	if database == "" {
		return nil, fmt.Errorf("MySQL 表列表需要指定数据库")
	}
	// 单库对象列表用于对象概览，需要行数、大小、时间和注释；按单个 TABLE_SCHEMA 过滤，统计开销
	// 远小于导航首屏的全库扫描。Doris、StarRocks 等兼容库只实现基础列时回退为仅名称和类型。
	rows, err := s.db.QueryContext(ctx, `
		SELECT TABLE_NAME, TABLE_TYPE, TABLE_ROWS,
		       COALESCE(DATA_LENGTH, 0) + COALESCE(INDEX_LENGTH, 0),
		       CREATE_TIME, UPDATE_TIME, TABLE_COMMENT
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ?
		ORDER BY TABLE_NAME`, database)
	if err != nil {
		rows, err = s.db.QueryContext(ctx, `
			SELECT TABLE_NAME, TABLE_TYPE, NULL, NULL, NULL, NULL, NULL
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ?
			ORDER BY TABLE_NAME`, database)
		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()

	tables := make([]SchemaTable, 0)
	for rows.Next() {
		var name string
		var tableType, comment sql.NullString
		var rowCount, sizeBytes sql.NullInt64
		var createdAt, updatedAt sql.NullTime
		if err := rows.Scan(&name, &tableType, &rowCount, &sizeBytes, &createdAt, &updatedAt, &comment); err != nil {
			return nil, err
		}
		kind := "table"
		if strings.Contains(strings.ToUpper(tableType.String), "VIEW") {
			kind = "view"
		}
		item := SchemaTable{Name: name, Kind: kind}
		if rowCount.Valid {
			item.RowCount = &rowCount.Int64
		}
		if sizeBytes.Valid && kind == "table" {
			item.SizeBytes = &sizeBytes.Int64
		}
		if createdAt.Valid {
			formatted := createdAt.Time.Format("2006-01-02 15:04:05")
			item.CreatedAt = &formatted
		}
		if updatedAt.Valid {
			formatted := updatedAt.Time.Format("2006-01-02 15:04:05")
			item.UpdatedAt = &formatted
		}
		if comment.Valid && strings.TrimSpace(comment.String) != "" && !strings.EqualFold(strings.TrimSpace(comment.String), "VIEW") {
			value := comment.String
			item.Comment = &value
		}
		tables = append(tables, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func postgresSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT table_schema, table_name, table_type FROM information_schema.tables
		 WHERE table_schema NOT IN ('pg_catalog','information_schema')
		 ORDER BY table_schema, table_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// PostgreSQL 以 schema 作为“库”维度展示
	grouped := map[string][]SchemaTable{}
	var order []string
	for rows.Next() {
		var schema, name, tblType string
		if err := rows.Scan(&schema, &name, &tblType); err != nil {
			return nil, err
		}
		kind := "table"
		if strings.Contains(strings.ToUpper(tblType), "VIEW") {
			kind = "view"
		}
		if _, ok := grouped[schema]; !ok {
			order = append(order, schema)
		}
		grouped[schema] = append(grouped[schema], SchemaTable{Name: name, Kind: kind})
	}
	var databases []SchemaDatabase
	for _, schema := range order {
		databases = append(databases, SchemaDatabase{Name: schema, Tables: grouped[schema]})
	}
	catalog := s.defaultDatabase
	info := &DbSchemaInfo{Databases: databases}
	if catalog != "" {
		info.Catalog = &catalog
	}
	return info, nil
}

// dmSchema 达梦 DM：以 schema（OWNER）为“库”维度。达梦不支持标准 INFORMATION_SCHEMA
// （会报 Error -2103 无效的模式名），需使用 Oracle 兼容视图 ALL_TABLES / ALL_VIEWS。
func dmSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT OWNER, TABLE_NAME, 'table' AS KIND FROM ALL_TABLES
		 WHERE OWNER NOT IN ('SYS','SYSSSO','SYSAUDITOR','CTISYS')
		UNION ALL
		SELECT OWNER, VIEW_NAME, 'view' AS KIND FROM ALL_VIEWS
		 WHERE OWNER NOT IN ('SYS','SYSSSO','SYSAUDITOR','CTISYS')
		ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := map[string][]SchemaTable{}
	var order []string
	for rows.Next() {
		var schema, name, kind string
		if err := rows.Scan(&schema, &name, &kind); err != nil {
			return nil, err
		}
		if _, ok := grouped[schema]; !ok {
			order = append(order, schema)
		}
		grouped[schema] = append(grouped[schema], SchemaTable{Name: name, Kind: kind})
	}
	var databases []SchemaDatabase
	for _, schema := range order {
		databases = append(databases, SchemaDatabase{Name: schema, Tables: grouped[schema]})
	}
	catalog := s.defaultDatabase
	info := &DbSchemaInfo{Databases: databases}
	if catalog != "" {
		info.Catalog = &catalog
	}
	return info, nil
}

// tdengineSchema TDengine：以数据库为“库”维度，通过 information_schema.ins_tables 列出表。
func tdengineSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT db_name, table_name FROM information_schema.ins_tables
		 WHERE db_name NOT IN ('information_schema','performance_schema')
		 ORDER BY db_name, table_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := map[string][]SchemaTable{}
	var order []string
	for rows.Next() {
		var dbName, name string
		if err := rows.Scan(&dbName, &name); err != nil {
			return nil, err
		}
		if _, ok := grouped[dbName]; !ok {
			order = append(order, dbName)
		}
		grouped[dbName] = append(grouped[dbName], SchemaTable{Name: name, Kind: "table"})
	}
	var databases []SchemaDatabase
	for _, dbName := range order {
		databases = append(databases, SchemaDatabase{Name: dbName, Tables: grouped[dbName]})
	}
	return &DbSchemaInfo{Databases: databases}, nil
}

// oracleSchema Oracle：以 OWNER 为“库”维度，通过 ALL_TABLES / ALL_VIEWS 列出表与视图。
func oracleSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT OWNER, TABLE_NAME, 'table' AS KIND FROM ALL_TABLES
		 WHERE OWNER NOT IN ('SYS','SYSTEM','OUTLN','XDB','MDSYS','CTXSYS','DBSNMP',
		   'ORDSYS','ORDDATA','WMSYS','LBACSYS','APPQOSSYS','AUDSYS','GSMADMIN_INTERNAL',
		   'OJVMSYS','DVSYS','DBSFWUSER','REMOTE_SCHEDULER_AGENT')
		UNION ALL
		SELECT OWNER, VIEW_NAME, 'view' AS KIND FROM ALL_VIEWS
		 WHERE OWNER NOT IN ('SYS','SYSTEM','OUTLN','XDB','MDSYS','CTXSYS','DBSNMP',
		   'ORDSYS','ORDDATA','WMSYS','LBACSYS','APPQOSSYS','AUDSYS','GSMADMIN_INTERNAL',
		   'OJVMSYS','DVSYS','DBSFWUSER','REMOTE_SCHEDULER_AGENT')
		ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := map[string][]SchemaTable{}
	var order []string
	for rows.Next() {
		var owner, name, kind string
		if err := rows.Scan(&owner, &name, &kind); err != nil {
			return nil, err
		}
		if _, ok := grouped[owner]; !ok {
			order = append(order, owner)
		}
		grouped[owner] = append(grouped[owner], SchemaTable{Name: name, Kind: kind})
	}
	var databases []SchemaDatabase
	for _, owner := range order {
		databases = append(databases, SchemaDatabase{Name: owner, Tables: grouped[owner]})
	}
	catalog := s.defaultDatabase
	info := &DbSchemaInfo{Databases: databases}
	if catalog != "" {
		info.Catalog = &catalog
	}
	return info, nil
}

// sqlserverSchema SQL Server：以 schema 为“库”维度，通过 INFORMATION_SCHEMA.TABLES 列出表/视图。
func sqlserverSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE FROM INFORMATION_SCHEMA.TABLES
		 WHERE TABLE_SCHEMA NOT IN ('sys','INFORMATION_SCHEMA')
		 ORDER BY TABLE_SCHEMA, TABLE_NAME`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := map[string][]SchemaTable{}
	var order []string
	for rows.Next() {
		var schema, name, tblType string
		if err := rows.Scan(&schema, &name, &tblType); err != nil {
			return nil, err
		}
		kind := "table"
		if strings.Contains(strings.ToUpper(tblType), "VIEW") {
			kind = "view"
		}
		if _, ok := grouped[schema]; !ok {
			order = append(order, schema)
		}
		grouped[schema] = append(grouped[schema], SchemaTable{Name: name, Kind: kind})
	}
	var databases []SchemaDatabase
	for _, schema := range order {
		databases = append(databases, SchemaDatabase{Name: schema, Tables: grouped[schema]})
	}
	catalog := s.defaultDatabase
	info := &DbSchemaInfo{Databases: databases}
	if catalog != "" {
		info.Catalog = &catalog
	}
	return info, nil
}

// clickhouseSchema ClickHouse：以 database 为“库”维度，通过 system.tables 列出表/视图。
func clickhouseSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT database, name, engine FROM system.tables
		 WHERE database NOT IN ('system','INFORMATION_SCHEMA','information_schema')
		 ORDER BY database, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := map[string][]SchemaTable{}
	var order []string
	for rows.Next() {
		var dbName, name, engine string
		if err := rows.Scan(&dbName, &name, &engine); err != nil {
			return nil, err
		}
		kind := "table"
		if strings.Contains(strings.ToUpper(engine), "VIEW") {
			kind = "view"
		}
		if _, ok := grouped[dbName]; !ok {
			order = append(order, dbName)
		}
		grouped[dbName] = append(grouped[dbName], SchemaTable{Name: name, Kind: kind})
	}
	var databases []SchemaDatabase
	for _, dbName := range order {
		databases = append(databases, SchemaDatabase{Name: dbName, Tables: grouped[dbName]})
	}
	return &DbSchemaInfo{Databases: databases}, nil
}

// ── db_get_table_columns ──

func getTableColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	if s.kind == kindPlugin {
		return externalTableColumns(ctx, s, database, table)
	}
	if s.vector != nil && s.kind == kindVector {
		return vectorTableColumns(ctx, s, database, table)
	}
	if s.kind == kindCQL {
		return cassandraColumns(ctx, s, database, table)
	}
	if s.kind == kindMongo {
		return mongoColumns(ctx, s, database, table)
	}
	if s.kind == kindRedis {
		return redisColumns(ctx, s, database, table)
	}
	if s.kind == kindHTTP || s.kind == kindNeo4j {
		return deriveColumnsFromRows(ctx, s, database, table)
	}
	if s.kind != kindSQL {
		return nil, fmt.Errorf("该会话类型不支持列查询")
	}
	switch s.dialect {
	case "sqlite":
		return sqliteColumns(ctx, s, table)
	case "duckdb":
		return duckdbColumns(ctx, s, database, table)
	case "mysql":
		return mysqlColumns(ctx, s, database, table)
	case "postgres":
		return postgresColumns(ctx, s, database, table)
	case "oracle", "yashandb":
		return oracleColumns(ctx, s, database, table)
	case "firebird":
		return firebirdColumns(ctx, s, table)
	case "dm":
		return dmColumns(ctx, s, database, table)
	case "sqlserver":
		return sqlserverColumns(ctx, s, database, table)
	case "clickhouse":
		return clickhouseColumns(ctx, s, database, table)
	case "tdengine":
		return tdengineColumns(ctx, s, database, table)
	case "trino":
		return trinoColumns(ctx, s, database, table)
	default:
		return nil, fmt.Errorf("db_get_table_columns 不支持该数据库类型")
	}
}

// deriveColumnsFromRows 通过查询一行数据来推导列信息。
// 适用于没有专用 schema  introspection API 的数据源（InfluxDB / Elasticsearch / CouchDB / Neo4j）。
func deriveColumnsFromRows(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	result, err := getTableRows(ctx, s, database, table, 1, 0)
	if err != nil {
		return nil, fmt.Errorf("推导列信息失败: %w", err)
	}
	if len(result.Columns) == 0 {
		return []ColumnInfo{}, nil
	}
	out := make([]ColumnInfo, 0, len(result.Columns))
	for _, name := range result.Columns {
		out = append(out, ColumnInfo{Name: name, DataType: "", Nullable: true})
	}
	return out, nil
}

// oracleColumns Oracle / YashanDB：ALL_TAB_COLUMNS + 主键约束。
// 查询占位符由会话方言决定：go-ora 使用 :n，yashandb-go 使用 ?。
func oracleColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	owner := strings.TrimSpace(database)
	// 主键列集合
	pkCols := map[string]struct{}{}
	pkQuery := fmt.Sprintf(`SELECT acc.COLUMN_NAME
		FROM ALL_CONSTRAINTS ac
		JOIN ALL_CONS_COLUMNS acc
		  ON ac.CONSTRAINT_NAME = acc.CONSTRAINT_NAME AND ac.OWNER = acc.OWNER
		WHERE ac.CONSTRAINT_TYPE = 'P' AND ac.TABLE_NAME = %s`, placeholder(s.dialect, 1))
	pkArgs := []any{table}
	if owner != "" {
		pkQuery += " AND ac.OWNER = " + placeholder(s.dialect, 2)
		pkArgs = append(pkArgs, owner)
	}
	if rows, err := s.db.QueryContext(ctx, pkQuery, pkArgs...); err == nil {
		for rows.Next() {
			var c string
			if rows.Scan(&c) == nil {
				pkCols[c] = struct{}{}
			}
		}
		rows.Close()
	}

	colQuery := fmt.Sprintf(`SELECT COLUMN_NAME, DATA_TYPE, NULLABLE, DATA_DEFAULT,
		DATA_LENGTH, CHAR_LENGTH, DATA_PRECISION, DATA_SCALE,
		CASE WHEN IDENTITY_COLUMN = 'YES' THEN 1 ELSE 0 END
		FROM ALL_TAB_COLUMNS WHERE TABLE_NAME = %s`, placeholder(s.dialect, 1))
	colArgs := []any{table}
	if owner != "" {
		colQuery += " AND OWNER = " + placeholder(s.dialect, 2)
		colArgs = append(colArgs, owner)
	}
	colQuery += " ORDER BY COLUMN_ID"
	rows, err := s.db.QueryContext(ctx, colQuery, colArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ColumnInfo
	for rows.Next() {
		var name, dataType, nullable string
		var def any
		var dataLength, charLength, precision, scale sql.NullInt64
		var identity int
		if err := rows.Scan(&name, &dataType, &nullable, &def, &dataLength, &charLength, &precision, &scale, &identity); err != nil {
			return nil, err
		}
		col := ColumnInfo{
			Name:          name,
			DataType:      oracleCompatibleColumnType(dataType, dataLength, charLength, precision, scale),
			Nullable:      strings.EqualFold(nullable, "Y"),
			AutoIncrement: identity == 1,
		}
		if _, ok := pkCols[name]; ok {
			col.IsPrimaryKey = true
		}
		if def != nil {
			if dv := strings.TrimSpace(fmt.Sprintf("%v", normalizeValue(def))); dv != "" {
				col.DefaultValue = &dv
			}
		}
		out = append(out, col)
	}
	return out, nil
}

// dmColumns 达梦：ALL_TAB_COLUMNS + 主键约束（ALL_CONSTRAINTS/ALL_CONS_COLUMNS）。
// 达梦不支持标准 INFORMATION_SCHEMA（Error -2103 无效的模式名），须使用 Oracle 兼容视图；
// chunanyong/dm 驱动使用 ? 占位符。
func dmColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	schema := strings.TrimSpace(database)
	pkCols := map[string]struct{}{}
	pkQuery := `SELECT acc.COLUMN_NAME
		FROM ALL_CONSTRAINTS ac
		JOIN ALL_CONS_COLUMNS acc
		  ON ac.CONSTRAINT_NAME = acc.CONSTRAINT_NAME AND ac.OWNER = acc.OWNER
		WHERE ac.CONSTRAINT_TYPE = 'P' AND ac.TABLE_NAME = ?`
	pkArgs := []any{table}
	if schema != "" {
		pkQuery += " AND ac.OWNER = ?"
		pkArgs = append(pkArgs, schema)
	}
	if rows, err := s.db.QueryContext(ctx, pkQuery, pkArgs...); err == nil {
		for rows.Next() {
			var c string
			if rows.Scan(&c) == nil {
				pkCols[c] = struct{}{}
			}
		}
		rows.Close()
	}

	colQuery := `SELECT c.COLUMN_NAME, c.DATA_TYPE, c.NULLABLE, c.DATA_DEFAULT,
		c.DATA_LENGTH, c.CHAR_LENGTH, c.DATA_PRECISION, c.DATA_SCALE,
		CASE WHEN EXISTS (
			SELECT 1
			FROM SYS.SYSCOLUMNS sc
			JOIN SYS.SYSOBJECTS obj ON obj.ID = sc.ID
			JOIN SYS.SYSOBJECTS sch ON sch.ID = obj.SCHID
			WHERE sc.NAME = c.COLUMN_NAME
			  AND obj.NAME = c.TABLE_NAME
			  AND sch.NAME = c.OWNER
			  AND (sc.INFO2 & 1) = 1
		) THEN 1 ELSE 0 END AS IS_AUTO_INCREMENT
		FROM ALL_TAB_COLUMNS c WHERE c.TABLE_NAME = ?`
	colArgs := []any{table}
	if schema != "" {
		colQuery += " AND c.OWNER = ?"
		colArgs = append(colArgs, schema)
	}
	colQuery += " ORDER BY c.COLUMN_ID"
	rows, err := s.db.QueryContext(ctx, colQuery, colArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ColumnInfo
	for rows.Next() {
		var name, dataType, nullable string
		var def any
		var dataLength, charLength, precision, scale sql.NullInt64
		var autoIncrement int
		if err := rows.Scan(&name, &dataType, &nullable, &def, &dataLength, &charLength, &precision, &scale, &autoIncrement); err != nil {
			return nil, err
		}
		col := ColumnInfo{
			Name:          name,
			DataType:      oracleCompatibleColumnType(dataType, dataLength, charLength, precision, scale),
			Nullable:      strings.EqualFold(nullable, "Y"),
			AutoIncrement: autoIncrement == 1,
		}
		if _, ok := pkCols[name]; ok {
			col.IsPrimaryKey = true
		}
		if def != nil {
			if dv := strings.TrimSpace(fmt.Sprintf("%v", normalizeValue(def))); dv != "" {
				col.DefaultValue = &dv
			}
		}
		out = append(out, col)
	}
	return out, nil
}

// sqlserverColumns SQL Server：INFORMATION_SCHEMA.COLUMNS + 主键约束。
func sqlserverColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	schema := strings.TrimSpace(database)
	if schema == "" {
		schema = "dbo"
	}
	pkCols := map[string]struct{}{}
	if rows, err := s.db.QueryContext(ctx,
		`SELECT kcu.COLUMN_NAME
		 FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
		 JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu
		   ON tc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME AND tc.TABLE_SCHEMA = kcu.TABLE_SCHEMA
		 WHERE tc.CONSTRAINT_TYPE = 'PRIMARY KEY' AND tc.TABLE_SCHEMA = @p1 AND tc.TABLE_NAME = @p2`,
		schema, table); err == nil {
		for rows.Next() {
			var c string
			if rows.Scan(&c) == nil {
				pkCols[c] = struct{}{}
			}
		}
		rows.Close()
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT c.COLUMN_NAME, c.DATA_TYPE, c.IS_NULLABLE, c.COLUMN_DEFAULT,
		        c.CHARACTER_MAXIMUM_LENGTH, c.NUMERIC_PRECISION, c.NUMERIC_SCALE,
		        c.DATETIME_PRECISION,
		        COLUMNPROPERTY(OBJECT_ID(QUOTENAME(c.TABLE_SCHEMA) + '.' + QUOTENAME(c.TABLE_NAME)), c.COLUMN_NAME, 'IsIdentity')
		 FROM INFORMATION_SCHEMA.COLUMNS c
		 WHERE c.TABLE_SCHEMA = @p1 AND c.TABLE_NAME = @p2 ORDER BY c.ORDINAL_POSITION`,
		schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ColumnInfo
	for rows.Next() {
		var name, dataType, nullable string
		var def any
		var charLength, precision, scale, datetimePrecision sql.NullInt64
		var identity sql.NullInt64
		if err := rows.Scan(&name, &dataType, &nullable, &def, &charLength, &precision, &scale, &datetimePrecision, &identity); err != nil {
			return nil, err
		}
		col := ColumnInfo{
			Name:          name,
			DataType:      sqlserverColumnType(dataType, charLength, precision, scale, datetimePrecision),
			Nullable:      strings.EqualFold(nullable, "YES"),
			AutoIncrement: identity.Valid && identity.Int64 == 1,
		}
		if _, ok := pkCols[name]; ok {
			col.IsPrimaryKey = true
		}
		if def != nil {
			dv := fmt.Sprintf("%v", normalizeValue(def))
			col.DefaultValue = &dv
		}
		out = append(out, col)
	}
	return out, nil
}

// clickhouseColumns ClickHouse：system.columns。
func clickhouseColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	dbName := strings.TrimSpace(database)
	if dbName == "" {
		dbName = s.defaultDatabase
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, type, default_expression, is_in_primary_key, comment
		 FROM system.columns WHERE database = ? AND table = ? ORDER BY position`,
		dbName, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ColumnInfo
	for rows.Next() {
		var name, ctype, defExpr, comment string
		var isPK uint8
		if err := rows.Scan(&name, &ctype, &defExpr, &isPK, &comment); err != nil {
			return nil, err
		}
		col := ColumnInfo{
			Name:         name,
			DataType:     ctype,
			Nullable:     strings.HasPrefix(ctype, "Nullable("),
			IsPrimaryKey: isPK == 1,
		}
		if defExpr != "" {
			col.DefaultValue = &defExpr
		}
		if comment != "" {
			col.Comment = &comment
		}
		out = append(out, col)
	}
	return out, nil
}

// tdengineColumns TDengine：DESCRIBE db.table。
func tdengineColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	target := "`" + escapeBacktick(table) + "`"
	if d := strings.TrimSpace(database); d != "" {
		target = "`" + escapeBacktick(d) + "`." + target
	}
	rows, err := s.db.QueryContext(ctx, "DESCRIBE "+target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []ColumnInfo
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		// DESCRIBE 返回：field, type, length, note[, ...]
		if len(vals) < 2 {
			continue
		}
		name := fmt.Sprintf("%v", normalizeValue(vals[0]))
		dataType := fmt.Sprintf("%v", normalizeValue(vals[1]))
		out = append(out, ColumnInfo{Name: name, DataType: dataType, Nullable: true})
	}
	return out, nil
}

func sqliteColumns(ctx context.Context, s *dbSession, table string) ([]ColumnInfo, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quoteSQLiteIdent(table)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ColumnInfo
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		col := ColumnInfo{
			Name:         name,
			DataType:     ctype,
			Nullable:     notnull == 0,
			IsPrimaryKey: pk > 0,
		}
		if dflt != nil {
			dv := fmt.Sprintf("%v", dflt)
			col.DefaultValue = &dv
		}
		out = append(out, col)
	}
	return out, nil
}

func mysqlColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	database = strings.TrimSpace(database)
	table = strings.TrimSpace(table)
	if database == "" || table == "" {
		return nil, fmt.Errorf("MySQL 列查询需要指定数据库和数据表")
	}

	columns, informationSchemaErr := mysqlColumnsFromInformationSchema(ctx, s, database, table)
	if informationSchemaErr == nil && len(columns) > 0 {
		return columns, nil
	}

	// Some managed MySQL deployments allow reading a table but restrict
	// information_schema.COLUMNS. SHOW FULL COLUMNS reads the selected table
	// directly and is the compatible fallback for the table-detail panel.
	fallbackColumns, fallbackErr := mysqlColumnsFromShowFullColumns(ctx, s, database, table)
	if fallbackErr == nil && len(fallbackColumns) > 0 {
		return fallbackColumns, nil
	}
	if informationSchemaErr != nil && fallbackErr != nil {
		return nil, fmt.Errorf("读取 MySQL 表 %s.%s 的字段失败（information_schema：%v；SHOW FULL COLUMNS：%w）", database, table, informationSchemaErr, fallbackErr)
	}
	if fallbackErr != nil {
		return nil, fmt.Errorf("读取 MySQL 表 %s.%s 的字段失败：%w", database, table, fallbackErr)
	}
	return nil, fmt.Errorf("未读取到 MySQL 表 %s.%s 的字段，请确认表存在且当前账号具有查看表结构的权限", database, table)
}

func mysqlColumnsFromInformationSchema(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_KEY, COLUMN_DEFAULT, COLUMN_COMMENT, EXTRA
		 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`,
		database, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMySQLColumns(rows, false)
}

func mysqlColumnsFromShowFullColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	query := fmt.Sprintf("SHOW FULL COLUMNS FROM `%s`.`%s`", strings.ReplaceAll(database, "`", "``"), strings.ReplaceAll(table, "`", "``"))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMySQLColumns(rows, true)
}

func scanMySQLColumns(rows *sql.Rows, showFullColumns bool) ([]ColumnInfo, error) {
	columns := make([]ColumnInfo, 0)
	for rows.Next() {
		var name, ctype string
		var nullable, key, extra sql.NullString
		var def, comment any
		if showFullColumns {
			var collation, privileges sql.NullString
			if err := rows.Scan(&name, &ctype, &collation, &nullable, &key, &def, &extra, &privileges, &comment); err != nil {
				return nil, err
			}
		} else if err := rows.Scan(&name, &ctype, &nullable, &key, &def, &comment, &extra); err != nil {
			return nil, err
		}
		col := ColumnInfo{
			Name:          name,
			DataType:      ctype,
			Nullable:      strings.EqualFold(nullable.String, "YES"),
			IsPrimaryKey:  strings.EqualFold(key.String, "PRI"),
			AutoIncrement: strings.Contains(strings.ToLower(extra.String), "auto_increment"),
		}
		if def != nil {
			dv := fmt.Sprintf("%v", normalizeValue(def))
			col.DefaultValue = &dv
		}
		if comment != nil {
			if c := fmt.Sprintf("%v", normalizeValue(comment)); c != "" {
				col.Comment = &c
			}
		}
		columns = append(columns, col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func postgresColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	schema := database
	if strings.TrimSpace(schema) == "" {
		schema = "public"
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.column_name, pg_catalog.format_type(a.atttypid, a.atttypmod), c.is_nullable, c.column_default,
		        COALESCE(pgd.description, '') AS col_comment,
		        CASE WHEN pk.column_name IS NOT NULL THEN true ELSE false END AS is_pk,
		        c.is_identity
		 FROM information_schema.columns c
		 LEFT JOIN (
		     SELECT kcu.column_name
		     FROM information_schema.table_constraints tc
		     JOIN information_schema.key_column_usage kcu
		       ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
		     WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = $1 AND tc.table_name = $2
		 ) pk ON pk.column_name = c.column_name
		 JOIN pg_catalog.pg_namespace ns ON ns.nspname = c.table_schema
		 JOIN pg_catalog.pg_class cls ON cls.relnamespace = ns.oid AND cls.relname = c.table_name
		 JOIN pg_catalog.pg_attribute a ON a.attrelid = cls.oid AND a.attname = c.column_name AND a.attnum > 0 AND NOT a.attisdropped
		 LEFT JOIN pg_catalog.pg_statio_all_tables st ON st.schemaname = c.table_schema AND st.relname = c.table_name
		 LEFT JOIN pg_catalog.pg_description pgd ON pgd.objoid = st.relid AND pgd.objsubid = a.attnum
		 WHERE c.table_schema = $1 AND c.table_name = $2
		 ORDER BY c.ordinal_position`,
		schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ColumnInfo
	for rows.Next() {
		var name, dataType, nullable, comment string
		var def any
		var isPK bool
		var isIdentity string
		if err := rows.Scan(&name, &dataType, &nullable, &def, &comment, &isPK, &isIdentity); err != nil {
			return nil, err
		}
		col := ColumnInfo{
			Name:          name,
			DataType:      dataType,
			Nullable:      strings.EqualFold(nullable, "YES"),
			IsPrimaryKey:  isPK,
			AutoIncrement: strings.EqualFold(isIdentity, "YES"),
		}
		if def != nil {
			dv := fmt.Sprintf("%v", normalizeValue(def))
			col.DefaultValue = &dv
			if strings.Contains(strings.ToLower(dv), "nextval(") {
				col.AutoIncrement = true
			}
		}
		if comment != "" {
			col.Comment = &comment
		}
		out = append(out, col)
	}
	return out, nil
}

// ── db_get_primary_keys ──

func getPrimaryKeys(ctx context.Context, s *dbSession, database, table string) (*PrimaryKeyInfo, error) {
	if s.kind == kindPlugin {
		return externalPrimaryKeys(ctx, s, database, table)
	}
	if s.vector != nil && s.kind == kindVector {
		return &PrimaryKeyInfo{Columns: []string{"id"}}, nil
	}
	if s.kind == kindCQL {
		columns, err := cassandraColumns(ctx, s, database, table)
		if err != nil {
			return nil, err
		}
		primaryKeys := make([]string, 0)
		for _, column := range columns {
			if column.IsPrimaryKey {
				primaryKeys = append(primaryKeys, column.Name)
			}
		}
		sort.Strings(primaryKeys)
		return &PrimaryKeyInfo{Columns: primaryKeys}, nil
	}
	if s.kind != kindSQL {
		return &PrimaryKeyInfo{Columns: []string{}}, nil
	}
	switch s.dialect {
	case "sqlite":
		cols, err := sqlitePrimaryKeys(ctx, s, table)
		if err != nil {
			return nil, err
		}
		return &PrimaryKeyInfo{Columns: cols}, nil
	case "duckdb":
		return duckdbPrimaryKeys(ctx, s, database, table)
	case "mysql":
		cols, err := mysqlPrimaryKeys(ctx, s, database, table)
		if err != nil {
			return nil, err
		}
		return &PrimaryKeyInfo{Columns: cols}, nil
	case "postgres":
		return postgresPrimaryKeys(ctx, s, database, table)
	case "sqlserver":
		return sqlserverPrimaryKeys(ctx, s, database, table)
	case "oracle", "yashandb":
		return oraclePrimaryKeys(ctx, s, database, table)
	case "firebird":
		return firebirdPrimaryKeys(ctx, s, table)
	case "dm":
		return dmPrimaryKeys(ctx, s, database, table)
	default:
		return nil, fmt.Errorf("db_get_primary_keys 不支持该数据库类型")
	}
}

func sqlitePrimaryKeys(ctx context.Context, s *dbSession, table string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quoteSQLiteIdent(table)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type pkCol struct {
		name string
		seq  int
	}
	var pks []pkCol
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		if pk > 0 {
			pks = append(pks, pkCol{name: name, seq: pk})
		}
	}
	// 按 pk 序号排序
	for i := 0; i < len(pks); i++ {
		for j := i + 1; j < len(pks); j++ {
			if pks[j].seq < pks[i].seq {
				pks[i], pks[j] = pks[j], pks[i]
			}
		}
	}
	cols := make([]string, 0, len(pks))
	for _, p := range pks {
		cols = append(cols, p.name)
	}
	return cols, nil
}

func mysqlPrimaryKeys(ctx context.Context, s *dbSession, database, table string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY'
		 ORDER BY ORDINAL_POSITION`, database, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringColumn(rows)
}

func postgresPrimaryKeys(ctx context.Context, s *dbSession, database, table string) (*PrimaryKeyInfo, error) {
	schema := database
	if strings.TrimSpace(schema) == "" {
		schema = "public"
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT tc.constraint_name, kcu.column_name
		 FROM information_schema.table_constraints tc
		 JOIN information_schema.key_column_usage kcu
		   ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
		 WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = $1 AND tc.table_name = $2
		 ORDER BY kcu.ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPrimaryKeyInfo(rows)
}

func sqlserverPrimaryKeys(ctx context.Context, s *dbSession, database, table string) (*PrimaryKeyInfo, error) {
	schema := strings.TrimSpace(database)
	if schema == "" {
		schema = "dbo"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tc.CONSTRAINT_NAME, kcu.COLUMN_NAME
		FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
		JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu
		  ON tc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME AND tc.TABLE_SCHEMA = kcu.TABLE_SCHEMA
		WHERE tc.CONSTRAINT_TYPE = 'PRIMARY KEY' AND tc.TABLE_SCHEMA = @p1 AND tc.TABLE_NAME = @p2
		ORDER BY kcu.ORDINAL_POSITION`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPrimaryKeyInfo(rows)
}

func oraclePrimaryKeys(ctx context.Context, s *dbSession, database, table string) (*PrimaryKeyInfo, error) {
	owner := strings.TrimSpace(database)
	if owner == "" {
		return nil, fmt.Errorf("主键查询需要指定 Schema")
	}
	query := fmt.Sprintf(`SELECT ac.CONSTRAINT_NAME, acc.COLUMN_NAME
		FROM ALL_CONSTRAINTS ac
		JOIN ALL_CONS_COLUMNS acc ON ac.CONSTRAINT_NAME = acc.CONSTRAINT_NAME AND ac.OWNER = acc.OWNER
		WHERE ac.CONSTRAINT_TYPE = 'P' AND ac.OWNER = %s AND ac.TABLE_NAME = %s
		ORDER BY acc.POSITION`, placeholder(s.dialect, 1), placeholder(s.dialect, 2))
	rows, err := s.db.QueryContext(ctx, query, owner, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPrimaryKeyInfo(rows)
}

func dmPrimaryKeys(ctx context.Context, s *dbSession, database, table string) (*PrimaryKeyInfo, error) {
	owner := strings.TrimSpace(database)
	if owner == "" {
		return nil, fmt.Errorf("达梦主键查询需要指定 Schema")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ac.CONSTRAINT_NAME, acc.COLUMN_NAME
		FROM ALL_CONSTRAINTS ac
		JOIN ALL_CONS_COLUMNS acc ON ac.CONSTRAINT_NAME = acc.CONSTRAINT_NAME AND ac.OWNER = acc.OWNER
		WHERE ac.CONSTRAINT_TYPE = 'P' AND ac.OWNER = ? AND ac.TABLE_NAME = ?
		ORDER BY acc.POSITION`, owner, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPrimaryKeyInfo(rows)
}

func scanPrimaryKeyInfo(rows *sql.Rows) (*PrimaryKeyInfo, error) {
	info := &PrimaryKeyInfo{Columns: []string{}}
	for rows.Next() {
		var constraintName, column string
		if err := rows.Scan(&constraintName, &column); err != nil {
			return nil, err
		}
		if info.ConstraintName == "" {
			info.ConstraintName = constraintName
		}
		info.Columns = append(info.Columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return info, nil
}

// ── db_list_indexes / db_list_foreign_keys / db_list_triggers ──

func listIndexes(ctx context.Context, s *dbSession, database, table string) ([]DbIndexInfo, error) {
	if s.kind == kindPlugin {
		return externalIndexes(ctx, s, database, table)
	}
	if s.vector != nil && s.kind == kindVector {
		return []DbIndexInfo{}, nil
	}
	if s.kind == kindMongo {
		return mongoIndexes(ctx, s, database, table)
	}
	if s.kind != kindSQL {
		return nil, fmt.Errorf("该会话类型不支持索引查询")
	}
	switch s.dialect {
	case "sqlite":
		return sqliteIndexes(ctx, s, table)
	case "duckdb":
		return duckdbIndexes(ctx, s, database, table)
	case "postgres":
		return postgresIndexes(ctx, s, database, table)
	case "mysql":
		return mysqlIndexes(ctx, s, database, table)
	case "sqlserver":
		return sqlserverIndexes(ctx, s, database, table)
	case "oracle", "yashandb":
		return oracleIndexes(ctx, s, database, table)
	case "firebird":
		return firebirdIndexes(ctx, s, table)
	case "dm":
		return dmIndexes(ctx, s, database, table)
	default:
		return nil, fmt.Errorf("db_list_indexes 不支持该数据库类型")
	}
}

func mysqlIndexes(ctx context.Context, s *dbSession, database, table string) ([]DbIndexInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT INDEX_NAME, COLUMN_NAME, CASE WHEN NON_UNIQUE = 0 THEN 1 ELSE 0 END FROM information_schema.STATISTICS
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND INDEX_NAME <> 'PRIMARY' AND COLUMN_NAME IS NOT NULL
		 ORDER BY INDEX_NAME, SEQ_IN_INDEX`,
		database, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIndexes(rows)
}

func sqlserverIndexes(ctx context.Context, s *dbSession, database, table string) ([]DbIndexInfo, error) {
	schema := strings.TrimSpace(database)
	if schema == "" {
		schema = "dbo"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.name, c.name, i.is_unique
		FROM sys.tables t
		JOIN sys.schemas sc ON sc.schema_id = t.schema_id
		JOIN sys.indexes i ON i.object_id = t.object_id
		JOIN sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id
		JOIN sys.columns c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
		WHERE sc.name = @p1 AND t.name = @p2 AND i.is_primary_key = 0
		  AND i.is_hypothetical = 0 AND ic.is_included_column = 0
		ORDER BY i.name, ic.key_ordinal`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIndexes(rows)
}

func oracleIndexes(ctx context.Context, s *dbSession, database, table string) ([]DbIndexInfo, error) {
	owner := strings.TrimSpace(database)
	if owner == "" {
		return nil, fmt.Errorf("索引查询需要指定 Schema")
	}
	query := fmt.Sprintf(`SELECT ai.INDEX_NAME, aic.COLUMN_NAME,
		CASE WHEN ai.UNIQUENESS = 'UNIQUE' THEN 1 ELSE 0 END
		FROM ALL_INDEXES ai
		JOIN ALL_IND_COLUMNS aic ON aic.INDEX_OWNER = ai.OWNER AND aic.INDEX_NAME = ai.INDEX_NAME
		LEFT JOIN ALL_CONSTRAINTS ac ON ac.OWNER = ai.OWNER AND ac.INDEX_NAME = ai.INDEX_NAME AND ac.CONSTRAINT_TYPE = 'P'
		WHERE ai.TABLE_OWNER = %s AND ai.TABLE_NAME = %s AND ac.CONSTRAINT_NAME IS NULL
		ORDER BY ai.INDEX_NAME, aic.COLUMN_POSITION`, placeholder(s.dialect, 1), placeholder(s.dialect, 2))
	rows, err := s.db.QueryContext(ctx, query, owner, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIndexes(rows)
}

func dmIndexes(ctx context.Context, s *dbSession, database, table string) ([]DbIndexInfo, error) {
	owner := strings.TrimSpace(database)
	if owner == "" {
		return nil, fmt.Errorf("达梦索引查询需要指定 Schema")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ai.INDEX_NAME, aic.COLUMN_NAME,
		CASE WHEN ai.UNIQUENESS = 'UNIQUE' THEN 1 ELSE 0 END
		FROM ALL_INDEXES ai
		JOIN ALL_IND_COLUMNS aic ON aic.INDEX_OWNER = ai.OWNER AND aic.INDEX_NAME = ai.INDEX_NAME
		LEFT JOIN ALL_CONSTRAINTS ac ON ac.OWNER = ai.OWNER AND ac.INDEX_NAME = ai.INDEX_NAME AND ac.CONSTRAINT_TYPE = 'P'
		WHERE ai.TABLE_OWNER = ? AND ai.TABLE_NAME = ? AND ac.CONSTRAINT_NAME IS NULL
		ORDER BY ai.INDEX_NAME, aic.COLUMN_POSITION`, owner, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIndexes(rows)
}

func scanIndexes(rows *sql.Rows) ([]DbIndexInfo, error) {
	indexes := map[string]*DbIndexInfo{}
	order := make([]string, 0)
	for rows.Next() {
		var name, column string
		var uniqueValue any
		if err := rows.Scan(&name, &column, &uniqueValue); err != nil {
			return nil, err
		}
		index, exists := indexes[name]
		if !exists {
			index = &DbIndexInfo{Name: name, Unique: databaseBool(uniqueValue), Columns: []string{}}
			indexes[name] = index
			order = append(order, name)
		}
		index.Columns = append(index.Columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]DbIndexInfo, 0, len(order))
	for _, name := range order {
		result = append(result, *indexes[name])
	}
	return result, nil
}

func sqlserverColumnType(dataType string, charLength, precision, scale, datetimePrecision sql.NullInt64) string {
	base := strings.ToLower(strings.TrimSpace(dataType))
	switch base {
	case "char", "varchar", "binary", "varbinary":
		if charLength.Valid {
			if charLength.Int64 == -1 {
				return base + "(max)"
			}
			return fmt.Sprintf("%s(%d)", base, charLength.Int64)
		}
	case "nchar", "nvarchar":
		if charLength.Valid {
			if charLength.Int64 == -1 {
				return base + "(max)"
			}
			return fmt.Sprintf("%s(%d)", base, charLength.Int64)
		}
	case "decimal", "numeric":
		if precision.Valid && scale.Valid {
			return fmt.Sprintf("%s(%d,%d)", base, precision.Int64, scale.Int64)
		}
	case "datetime2", "datetimeoffset", "time":
		if datetimePrecision.Valid {
			return fmt.Sprintf("%s(%d)", base, datetimePrecision.Int64)
		}
	}
	return base
}

func oracleCompatibleColumnType(dataType string, dataLength, charLength, precision, scale sql.NullInt64) string {
	base := strings.ToUpper(strings.TrimSpace(dataType))
	switch base {
	case "CHAR", "VARCHAR", "VARCHAR2", "NCHAR", "NVARCHAR2":
		length := dataLength
		if charLength.Valid {
			length = charLength
		}
		if length.Valid {
			return fmt.Sprintf("%s(%d)", base, length.Int64)
		}
	case "RAW":
		if dataLength.Valid {
			return fmt.Sprintf("%s(%d)", base, dataLength.Int64)
		}
	case "NUMBER", "DECIMAL", "NUMERIC":
		if precision.Valid {
			if scale.Valid {
				return fmt.Sprintf("%s(%d,%d)", base, precision.Int64, scale.Int64)
			}
			return fmt.Sprintf("%s(%d)", base, precision.Int64)
		}
	case "FLOAT":
		if precision.Valid {
			return fmt.Sprintf("%s(%d)", base, precision.Int64)
		}
	case "TIMESTAMP", "TIMESTAMP WITH TIME ZONE", "TIMESTAMP WITH LOCAL TIME ZONE":
		if scale.Valid {
			return fmt.Sprintf("%s(%d)", base, scale.Int64)
		}
	}
	return base
}

func databaseBool(value any) bool {
	switch typed := normalizeValue(value).(type) {
	case bool:
		return typed
	case int64:
		return typed != 0
	case int:
		return typed != 0
	case string:
		return typed == "1" || strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes")
	default:
		return fmt.Sprintf("%v", typed) == "1"
	}
}

func listForeignKeys(ctx context.Context, s *dbSession, database, table string) ([]DbForeignKeyInfo, error) {
	if s.kind == kindPlugin {
		return externalForeignKeys(ctx, s, database, table)
	}
	if s.kind != kindSQL {
		return nil, fmt.Errorf("该会话类型不支持外键查询")
	}
	switch s.dialect {
	case "sqlite":
		return sqliteForeignKeys(ctx, s, table)
	case "duckdb":
		return duckdbForeignKeys(ctx, s, database, table)
	case "postgres":
		return postgresForeignKeys(ctx, s, database, table)
	case "mysql":
		return mysqlForeignKeys(ctx, s, database, table)
	case "firebird":
		return firebirdForeignKeys(ctx, s, table)
	default:
		return nil, fmt.Errorf("db_list_foreign_keys 不支持该数据库类型")
	}
}

func mysqlForeignKeys(ctx context.Context, s *dbSession, database, table string) ([]DbForeignKeyInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
		 FROM information_schema.KEY_COLUMN_USAGE
		 WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL
		 ORDER BY CONSTRAINT_NAME, ORDINAL_POSITION`, database, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fkMap := map[string]*DbForeignKeyInfo{}
	var order []string
	for rows.Next() {
		var name, col, refTable, refCol string
		if err := rows.Scan(&name, &col, &refTable, &refCol); err != nil {
			return nil, err
		}
		fk, ok := fkMap[name]
		if !ok {
			fk = &DbForeignKeyInfo{Name: name, ReferencedTable: refTable}
			fkMap[name] = fk
			order = append(order, name)
		}
		fk.Columns = append(fk.Columns, col)
		fk.ReferencedColumns = append(fk.ReferencedColumns, refCol)
	}
	out := make([]DbForeignKeyInfo, 0, len(order))
	for _, name := range order {
		out = append(out, *fkMap[name])
	}
	return out, nil
}

func listTriggers(ctx context.Context, s *dbSession, database, table string) ([]DbTriggerInfo, error) {
	if s.kind == kindPlugin {
		return externalTriggers(ctx, s, database, table)
	}
	if s.kind != kindSQL {
		return nil, fmt.Errorf("该会话类型不支持触发器查询")
	}
	switch s.dialect {
	case "sqlite":
		return sqliteTriggers(ctx, s, table)
	case "postgres":
		return postgresTriggers(ctx, s, database, table)
	case "mysql":
		return mysqlTriggers(ctx, s, database, table)
	case "firebird":
		return firebirdTriggers(ctx, s, table)
	default:
		return nil, fmt.Errorf("db_list_triggers 不支持该数据库类型")
	}
}

func mysqlTriggers(ctx context.Context, s *dbSession, database, table string) ([]DbTriggerInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT TRIGGER_NAME, ACTION_TIMING, EVENT_MANIPULATION FROM information_schema.TRIGGERS
		 WHERE EVENT_OBJECT_SCHEMA = ? AND EVENT_OBJECT_TABLE = ? ORDER BY TRIGGER_NAME`,
		database, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DbTriggerInfo
	for rows.Next() {
		var name, timing, event string
		if err := rows.Scan(&name, &timing, &event); err != nil {
			return nil, err
		}
		out = append(out, DbTriggerInfo{Name: name, Timing: timing, Event: event})
	}
	return out, nil
}

func sqliteIndexes(ctx context.Context, s *dbSession, table string) ([]DbIndexInfo, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_list(%s)", quoteSQLiteIdent(table)))
	if err != nil {
		return nil, err
	}
	type sqliteIndex struct {
		name   string
		unique bool
	}
	var indexes []sqliteIndex
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return nil, err
		}
		if origin != "pk" {
			indexes = append(indexes, sqliteIndex{name: name, unique: unique != 0})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]DbIndexInfo, 0, len(indexes))
	for _, index := range indexes {
		columnRows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_info(%s)", quoteSQLiteIdent(index.name)))
		if err != nil {
			return nil, err
		}
		info := DbIndexInfo{Name: index.name, Unique: index.unique, Columns: []string{}}
		for columnRows.Next() {
			var seqno, cid int
			var name string
			if err := columnRows.Scan(&seqno, &cid, &name); err != nil {
				columnRows.Close()
				return nil, err
			}
			info.Columns = append(info.Columns, name)
		}
		if err := columnRows.Close(); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

func postgresIndexes(ctx context.Context, s *dbSession, database, table string) ([]DbIndexInfo, error) {
	schema := strings.TrimSpace(database)
	if schema == "" {
		schema = "public"
	}
	// openGauss and GaussDB lack WITH ORDINALITY, so the key positions come
	// from generate_series over the int2vector read as an array (index 0 up).
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.relname, a.attname, ix.indisunique
		FROM pg_class t
		JOIN pg_namespace ns ON ns.oid = t.relnamespace
		JOIN pg_index ix ON ix.indrelid = t.oid
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN generate_series(0, 31) AS pos(n) ON pos.n < array_length(ix.indkey::int2[], 1)
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = (ix.indkey::int2[])[pos.n]
		WHERE ns.nspname = $1 AND t.relname = $2 AND ix.indisprimary = false
		ORDER BY i.relname, pos.n`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indexes := map[string]*DbIndexInfo{}
	var order []string
	for rows.Next() {
		var name, column string
		var unique bool
		if err := rows.Scan(&name, &column, &unique); err != nil {
			return nil, err
		}
		index, ok := indexes[name]
		if !ok {
			index = &DbIndexInfo{Name: name, Unique: unique}
			indexes[name] = index
			order = append(order, name)
		}
		index.Columns = append(index.Columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DbIndexInfo, 0, len(order))
	for _, name := range order {
		out = append(out, *indexes[name])
	}
	return out, nil
}

func sqliteForeignKeys(ctx context.Context, s *dbSession, table string) ([]DbForeignKeyInfo, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(%s)", quoteSQLiteIdent(table)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]*DbForeignKeyInfo{}
	var order []string
	for rows.Next() {
		var id, seq int
		var referencedTable, column, referencedColumn, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &referencedTable, &column, &referencedColumn, &onUpdate, &onDelete, &match); err != nil {
			return nil, err
		}
		name := fmt.Sprintf("fk_%s_%d", table, id)
		key, ok := keys[name]
		if !ok {
			key = &DbForeignKeyInfo{Name: name, ReferencedTable: referencedTable}
			keys[name] = key
			order = append(order, name)
		}
		key.Columns = append(key.Columns, column)
		key.ReferencedColumns = append(key.ReferencedColumns, referencedColumn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DbForeignKeyInfo, 0, len(order))
	for _, name := range order {
		out = append(out, *keys[name])
	}
	return out, nil
}

func postgresForeignKeys(ctx context.Context, s *dbSession, database, table string) ([]DbForeignKeyInfo, error) {
	schema := strings.TrimSpace(database)
	if schema == "" {
		schema = "public"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT tc.constraint_name, kcu.column_name, ccu.table_name, ccu.column_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.key_column_usage kcu
		  ON tc.constraint_name = kcu.constraint_name AND tc.constraint_schema = kcu.constraint_schema
		JOIN information_schema.constraint_column_usage ccu
		  ON ccu.constraint_name = tc.constraint_name AND ccu.constraint_schema = tc.constraint_schema
		WHERE tc.constraint_type = 'FOREIGN KEY' AND tc.table_schema = $1 AND tc.table_name = $2
		ORDER BY tc.constraint_name, kcu.ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanForeignKeys(rows)
}

func scanForeignKeys(rows *sql.Rows) ([]DbForeignKeyInfo, error) {
	keys := map[string]*DbForeignKeyInfo{}
	var order []string
	for rows.Next() {
		var name, column, referencedTable, referencedColumn string
		if err := rows.Scan(&name, &column, &referencedTable, &referencedColumn); err != nil {
			return nil, err
		}
		key, ok := keys[name]
		if !ok {
			key = &DbForeignKeyInfo{Name: name, ReferencedTable: referencedTable}
			keys[name] = key
			order = append(order, name)
		}
		key.Columns = append(key.Columns, column)
		key.ReferencedColumns = append(key.ReferencedColumns, referencedColumn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DbForeignKeyInfo, 0, len(order))
	for _, name := range order {
		out = append(out, *keys[name])
	}
	return out, nil
}

func postgresTriggers(ctx context.Context, s *dbSession, database, table string) ([]DbTriggerInfo, error) {
	schema := strings.TrimSpace(database)
	if schema == "" {
		schema = "public"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT trigger_name, action_timing, event_manipulation
		FROM information_schema.triggers
		WHERE event_object_schema = $1 AND event_object_table = $2
		ORDER BY trigger_name, event_manipulation`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTriggers(rows)
}

func scanTriggers(rows *sql.Rows) ([]DbTriggerInfo, error) {
	var out []DbTriggerInfo
	for rows.Next() {
		var name, timing, event string
		if err := rows.Scan(&name, &timing, &event); err != nil {
			return nil, err
		}
		out = append(out, DbTriggerInfo{Name: name, Timing: timing, Event: event})
	}
	return out, rows.Err()
}

func sqliteTriggers(ctx context.Context, s *dbSession, table string) ([]DbTriggerInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ? ORDER BY name", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DbTriggerInfo
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			return nil, err
		}
		upper := strings.ToUpper(definition)
		timing := ""
		for _, candidate := range []string{"INSTEAD OF", "BEFORE", "AFTER"} {
			if strings.Contains(upper, " "+candidate+" ") {
				timing = candidate
				break
			}
		}
		event := ""
		for _, candidate := range []string{"INSERT", "UPDATE", "DELETE"} {
			if strings.Contains(upper, " "+candidate+" ") {
				event = candidate
				break
			}
		}
		out = append(out, DbTriggerInfo{Name: name, Timing: timing, Event: event})
	}
	return out, rows.Err()
}

// ── db_get_table_ddl ──

func getTableDDL(ctx context.Context, s *dbSession, database, table string) (string, error) {
	if s.kind == kindPlugin {
		return externalTableDDL(ctx, s, database, table)
	}
	if s.vector != nil && s.kind == kindVector {
		return vectorCollectionDefinition(ctx, s, database, table)
	}
	if s.kind != kindSQL {
		return "", fmt.Errorf("该会话类型不支持 DDL 查询")
	}
	switch s.dialect {
	case "sqlite":
		var ddl string
		err := s.db.QueryRowContext(ctx,
			"SELECT sql FROM sqlite_master WHERE name = ? AND type IN ('table','view')", table).Scan(&ddl)
		if err != nil {
			return "", err
		}
		return ddl, nil
	case "duckdb":
		return duckdbTableDDL(ctx, s, database, table)
	case "mysql":
		var name, ddl string
		q := fmt.Sprintf("SHOW CREATE TABLE `%s`.`%s`",
			strings.ReplaceAll(database, "`", "``"), strings.ReplaceAll(table, "`", "``"))
		if err := s.db.QueryRowContext(ctx, q).Scan(&name, &ddl); err != nil {
			return "", err
		}
		return ddl, nil
	case "postgres":
		return postgresTableDDL(ctx, s, database, table)
	case "firebird":
		return firebirdTableDDL(ctx, s, table)
	default:
		return "", fmt.Errorf("db_get_table_ddl 不支持该数据库类型")
	}
}

// postgresTableDDL 由列信息拼装近似的 CREATE TABLE 语句。
func postgresTableDDL(ctx context.Context, s *dbSession, database, table string) (string, error) {
	schema := database
	if strings.TrimSpace(schema) == "" {
		schema = "public"
	}
	cols, err := postgresColumns(ctx, s, schema, table)
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return "", fmt.Errorf("表 %s.%s 不存在或无列", schema, table)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE \"%s\".\"%s\" (\n", schema, table)
	var pkCols []string
	for i, c := range cols {
		fmt.Fprintf(&b, "  \"%s\" %s", c.Name, c.DataType)
		if !c.Nullable {
			b.WriteString(" NOT NULL")
		}
		if c.DefaultValue != nil {
			fmt.Fprintf(&b, " DEFAULT %s", *c.DefaultValue)
		}
		if c.IsPrimaryKey {
			pkCols = append(pkCols, c.Name)
		}
		if i < len(cols)-1 || len(pkCols) > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	if len(pkCols) > 0 {
		quoted := make([]string, len(pkCols))
		for i, c := range pkCols {
			quoted[i] = "\"" + c + "\""
		}
		fmt.Fprintf(&b, "  PRIMARY KEY (%s)\n", strings.Join(quoted, ", "))
	}
	b.WriteString(");")
	return b.String(), nil
}

// ── 辅助 ──

func scanStringColumn(rows interface {
	Next() bool
	Scan(...any) error
}) ([]string, error) {
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// quoteSQLiteIdent 为 SQLite 标识符加双引号并转义。
func quoteSQLiteIdent(ident string) string {
	return "\"" + strings.ReplaceAll(ident, "\"", "\"\"") + "\""
}
