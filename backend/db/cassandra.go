//go:build !lite

// Cassandra / ScyllaDB 连接（gocql，CQL）。
//
// Schema：system_schema.keyspaces / tables（跳过系统 keyspace）；
// 浏览：SELECT * FROM ks.tbl LIMIT n（CQL 不支持 OFFSET，offset 忽略）；
// 查询：任意 CQL，动态解析列。
package db

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/gocql/gocql"
	inf "gopkg.in/inf.v0"
)

var cassandraSystemKeyspaces = map[string]struct{}{
	"system": {}, "system_auth": {}, "system_schema": {},
	"system_distributed": {}, "system_traces": {}, "system_views": {},
	"system_virtual_schema": {},
}

func buildCassandraSession(cfg *DbConnectionConfig) (*dbSession, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 9042
	}
	cluster := gocql.NewCluster(host)
	cluster.Port = int(port)
	cluster.Timeout = 15 * time.Second
	cluster.ConnectTimeout = 15 * time.Second
	cluster.Consistency = gocql.LocalOne
	if cfg.SSL {
		cluster.SslOpts = &gocql.SslOptions{EnableHostVerification: true}
	}
	if u := derefOr(cfg.Username, ""); u != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: u,
			Password: derefOr(cfg.Password, ""),
		}
	}
	keyspace := ""
	if d := trimmedOptional(cfg.Database); d != nil {
		keyspace = *d
		cluster.Keyspace = keyspace
	}
	sess, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("Cassandra 连接失败: %w", err)
	}
	return &dbSession{
		kind:            kindCQL,
		cql:             sess,
		closeExtra:      sess.Close,
		defaultDatabase: keyspace,
	}, nil
}

// cassandraSchema 列出 keyspace 与表。
func cassandraSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	byKs := map[string][]SchemaTable{}
	iter := s.cql.(*gocql.Session).Query(
		"SELECT keyspace_name, table_name FROM system_schema.tables").WithContext(ctx).Iter()
	var ks, tbl string
	for iter.Scan(&ks, &tbl) {
		if _, sys := cassandraSystemKeyspaces[ks]; sys {
			continue
		}
		byKs[ks] = append(byKs[ks], SchemaTable{Name: tbl, Kind: "table"})
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("Cassandra Schema 查询失败: %w", err)
	}
	// 补上没有表的 keyspace
	ksIter := s.cql.(*gocql.Session).Query(
		"SELECT keyspace_name FROM system_schema.keyspaces").WithContext(ctx).Iter()
	for ksIter.Scan(&ks) {
		if _, sys := cassandraSystemKeyspaces[ks]; sys {
			continue
		}
		if _, ok := byKs[ks]; !ok {
			byKs[ks] = []SchemaTable{}
		}
	}
	_ = ksIter.Close()

	names := make([]string, 0, len(byKs))
	for name := range byKs {
		names = append(names, name)
	}
	sort.Strings(names)
	databases := make([]SchemaDatabase, 0, len(names))
	for _, name := range names {
		databases = append(databases, SchemaDatabase{Name: name, Tables: byKs[name]})
	}
	return &DbSchemaInfo{Databases: databases}, nil
}

// cassandraColumns 查询列信息（system_schema.columns）。
func cassandraColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	ks := strings.TrimSpace(database)
	if ks == "" {
		ks = s.defaultDatabase
	}
	iter := s.cql.(*gocql.Session).Query(
		"SELECT column_name, type, kind FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ?",
		ks, table).WithContext(ctx).Iter()
	var name, dtype, kind string
	var cols []ColumnInfo
	for iter.Scan(&name, &dtype, &kind) {
		cols = append(cols, ColumnInfo{
			Name:         name,
			DataType:     dtype,
			Nullable:     kind == "regular",
			IsPrimaryKey: kind == "partition_key" || kind == "clustering",
		})
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("Cassandra 列查询失败: %w", err)
	}
	if cols == nil {
		cols = []ColumnInfo{}
	}
	return cols, nil
}

// cassandraTableRows 分页浏览（CQL 无 OFFSET，仅 LIMIT）。
func cassandraTableRows(ctx context.Context, s *dbSession, database, table string, limit uint32) (*QueryResult, error) {
	ks := strings.TrimSpace(database)
	if ks == "" {
		ks = s.defaultDatabase
	}
	target := quoteCQLIdent(table)
	if ks != "" {
		target = quoteCQLIdent(ks) + "." + target
	}
	return cassandraExecuteQuery(ctx, s, fmt.Sprintf("SELECT * FROM %s LIMIT %d", target, limit))
}

func quoteCQLIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func cassandraUpdateCell(ctx context.Context, s *dbSession, database, table string, primaryKey map[string]any, column string, value any) (int64, error) {
	ks := strings.TrimSpace(database)
	if ks == "" {
		ks = s.defaultDatabase
	}
	columns, err := cassandraColumns(ctx, s, ks, table)
	if err != nil {
		return 0, err
	}
	columnTypes := make(map[string]string, len(columns))
	primaryKeyColumns := make(map[string]struct{})
	for _, info := range columns {
		columnTypes[info.Name] = info.DataType
		if info.IsPrimaryKey {
			primaryKeyColumns[info.Name] = struct{}{}
		}
	}
	if _, ok := columnTypes[column]; !ok {
		return 0, fmt.Errorf("Cassandra 表 %q 中不存在列 %q", table, column)
	}
	if _, ok := primaryKeyColumns[column]; ok {
		return 0, fmt.Errorf("Cassandra 主键列 %q 不允许直接修改", column)
	}
	if strings.EqualFold(strings.TrimSpace(columnTypes[column]), "counter") {
		return 0, fmt.Errorf("Cassandra counter 列 %q 只支持增减操作，不支持直接赋值", column)
	}
	if len(primaryKey) != len(primaryKeyColumns) {
		return 0, fmt.Errorf("Cassandra 更新需要完整主键，期望 %d 列，实际 %d 列", len(primaryKeyColumns), len(primaryKey))
	}
	convertedPrimaryKey := make(map[string]any, len(primaryKey))
	for name, keyValue := range primaryKey {
		if _, ok := primaryKeyColumns[name]; !ok {
			return 0, fmt.Errorf("Cassandra 更新条件包含非主键列 %q", name)
		}
		convertedPrimaryKey[name], err = cassandraJSONValue(columnTypes[name], keyValue)
		if err != nil {
			return 0, fmt.Errorf("转换 Cassandra 主键列 %q: %w", name, err)
		}
	}
	convertedValue, err := cassandraJSONValue(columnTypes[column], value)
	if err != nil {
		return 0, fmt.Errorf("转换 Cassandra 列 %q: %w", column, err)
	}
	statement, values, err := buildCassandraUpdate(ks, table, convertedPrimaryKey, column, convertedValue)
	if err != nil {
		return 0, err
	}
	if err := s.cql.(*gocql.Session).Query(statement, values...).WithContext(ctx).Exec(); err != nil {
		return 0, fmt.Errorf("Cassandra 更新单元格: %w", err)
	}
	return 1, nil
}

func cassandraDeleteRow(ctx context.Context, s *dbSession, database, table string, primaryKey map[string]any) (int64, error) {
	ks := strings.TrimSpace(database)
	if ks == "" {
		ks = s.defaultDatabase
	}
	columns, err := cassandraColumns(ctx, s, ks, table)
	if err != nil {
		return 0, err
	}
	columnTypes := make(map[string]string, len(columns))
	primaryKeyColumns := make(map[string]struct{})
	for _, info := range columns {
		columnTypes[info.Name] = info.DataType
		if info.IsPrimaryKey {
			primaryKeyColumns[info.Name] = struct{}{}
		}
	}
	if len(primaryKey) != len(primaryKeyColumns) {
		return 0, fmt.Errorf("Cassandra 删除需要完整主键，期望 %d 列，实际 %d 列", len(primaryKeyColumns), len(primaryKey))
	}
	convertedPrimaryKey := make(map[string]any, len(primaryKey))
	for name, keyValue := range primaryKey {
		if _, ok := primaryKeyColumns[name]; !ok {
			return 0, fmt.Errorf("Cassandra 删除条件包含非主键列 %q", name)
		}
		convertedPrimaryKey[name], err = cassandraJSONValue(columnTypes[name], keyValue)
		if err != nil {
			return 0, fmt.Errorf("转换 Cassandra 主键列 %q: %w", name, err)
		}
	}
	statement, values, err := buildCassandraDelete(ks, table, convertedPrimaryKey)
	if err != nil {
		return 0, err
	}
	if err := s.cql.(*gocql.Session).Query(statement, values...).WithContext(ctx).Exec(); err != nil {
		return 0, fmt.Errorf("Cassandra 删除行: %w", err)
	}
	return 1, nil
}

func buildCassandraDelete(database, table string, primaryKey map[string]any) (string, []any, error) {
	if strings.TrimSpace(database) == "" {
		return "", nil, fmt.Errorf("Cassandra 删除需要 keyspace")
	}
	if strings.TrimSpace(table) == "" {
		return "", nil, fmt.Errorf("Cassandra 删除需要表名")
	}
	if len(primaryKey) == 0 {
		return "", nil, fmt.Errorf("Cassandra 删除需要主键")
	}
	keyColumns := make([]string, 0, len(primaryKey))
	for name := range primaryKey {
		if strings.TrimSpace(name) == "" {
			return "", nil, fmt.Errorf("Cassandra 删除包含空主键列名")
		}
		keyColumns = append(keyColumns, name)
	}
	sort.Strings(keyColumns)
	conditions := make([]string, len(keyColumns))
	values := make([]any, 0, len(keyColumns))
	for i, name := range keyColumns {
		conditions[i] = quoteCQLIdent(name) + " = ?"
		values = append(values, primaryKey[name])
	}
	statement := fmt.Sprintf("DELETE FROM %s.%s WHERE %s",
		quoteCQLIdent(database), quoteCQLIdent(table), strings.Join(conditions, " AND "))
	return statement, values, nil
}

func buildCassandraUpdate(database, table string, primaryKey map[string]any, column string, value any) (string, []any, error) {
	if strings.TrimSpace(database) == "" {
		return "", nil, fmt.Errorf("Cassandra 更新需要 keyspace")
	}
	if strings.TrimSpace(table) == "" {
		return "", nil, fmt.Errorf("Cassandra 更新需要表名")
	}
	if strings.TrimSpace(column) == "" {
		return "", nil, fmt.Errorf("Cassandra 更新需要列名")
	}
	if len(primaryKey) == 0 {
		return "", nil, fmt.Errorf("Cassandra 更新需要主键")
	}
	if _, ok := primaryKey[column]; ok {
		return "", nil, fmt.Errorf("Cassandra 主键列 %q 不允许直接修改", column)
	}
	keyColumns := make([]string, 0, len(primaryKey))
	for name := range primaryKey {
		if strings.TrimSpace(name) == "" {
			return "", nil, fmt.Errorf("Cassandra 更新包含空主键列名")
		}
		keyColumns = append(keyColumns, name)
	}
	sort.Strings(keyColumns)
	conditions := make([]string, len(keyColumns))
	values := make([]any, 0, len(keyColumns)+1)
	values = append(values, value)
	for i, name := range keyColumns {
		conditions[i] = quoteCQLIdent(name) + " = ?"
		values = append(values, primaryKey[name])
	}
	statement := fmt.Sprintf("UPDATE %s.%s SET %s = ? WHERE %s",
		quoteCQLIdent(database), quoteCQLIdent(table), quoteCQLIdent(column), strings.Join(conditions, " AND "))
	return statement, values, nil
}

func cassandraJSONValue(dataType string, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(dataType)) {
	case "tinyint", "smallint", "int", "bigint", "counter", "time":
		if number, ok := value.(float64); ok {
			integer := int64(number)
			if float64(integer) != number {
				return nil, fmt.Errorf("%v 不是整数", number)
			}
			return integer, nil
		}
	case "varint":
		if text, ok := value.(string); ok {
			integer, parsed := new(big.Int).SetString(text, 10)
			if !parsed {
				return nil, fmt.Errorf("无效 varint %q", text)
			}
			return integer, nil
		}
		if number, ok := value.(float64); ok {
			if number != math.Trunc(number) {
				return nil, fmt.Errorf("%v 不是整数", number)
			}
			return big.NewInt(int64(number)), nil
		}
	case "float":
		if number, ok := value.(float64); ok {
			return float32(number), nil
		}
	case "decimal":
		text := fmt.Sprint(value)
		decimal := new(inf.Dec)
		if _, ok := decimal.SetString(text); !ok {
			return nil, fmt.Errorf("无效 decimal %q", text)
		}
		return decimal, nil
	case "timestamp":
		if text, ok := value.(string); ok {
			parsed, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				return nil, fmt.Errorf("无效 timestamp %q", text)
			}
			return parsed, nil
		}
	}
	return value, nil
}

// cassandraExecuteQuery 执行任意 CQL，动态解析列。
func cassandraExecuteQuery(ctx context.Context, s *dbSession, query string) (*QueryResult, error) {
	start := time.Now()
	q := s.cql.(*gocql.Session).Query(query).WithContext(ctx)
	if !isResultProducing(query) {
		if err := q.Exec(); err != nil {
			return nil, err
		}
		var zero int64
		return &QueryResult{
			Columns: []string{}, Rows: [][]any{},
			AffectedRows: &zero,
			ExecutionMs:  time.Since(start).Milliseconds(),
		}, nil
	}
	iter := q.Iter()
	colInfos := iter.Columns()
	columns := make([]string, len(colInfos))
	for i, c := range colInfos {
		columns[i] = c.Name
	}
	rows := [][]any{}
	for {
		rowMap := map[string]any{}
		if !iter.MapScan(rowMap) {
			break
		}
		row := make([]any, len(columns))
		for i, col := range columns {
			row[i] = normalizeCQLValue(rowMap[col])
		}
		rows = append(rows, row)
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return &QueryResult{
		Columns:     columns,
		Rows:        rows,
		RowCount:    len(rows),
		ExecutionMs: time.Since(start).Milliseconds(),
	}, nil
}

// normalizeCQLValue 将 gocql 扫描值转为可 JSON 序列化的形式。
func normalizeCQLValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case gocql.UUID:
		return x.String()
	case fmt.Stringer:
		return x.String()
	default:
		return x
	}
}
