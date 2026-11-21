//go:build !lite

// Trino / Presto 连接（trinodb/trino-go-client，纯 Go，database/sql）。
//
// DSN 形如 http(s)://user@host:port?catalog=...&schema=...；
// `database` 字段可填写 catalog，也兼容 catalog/schema 简写。
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/trinodb/trino-go-client/trino" // 注册 "trino" 驱动
)

func trinoCatalogAndSchema(database string) (catalog, schema string) {
	value := strings.Trim(strings.TrimSpace(database), "/")
	if value == "" {
		return "", ""
	}
	parts := strings.SplitN(value, "/", 2)
	catalog = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		schema = strings.TrimSpace(parts[1])
	}
	return catalog, schema
}

// normalizeTrinoQuery 移除编辑器常见的末尾分号。
// trino-go-client 每次请求只能执行一条语句，且会拒绝终止分号。
func normalizeTrinoQuery(query string) string {
	query = strings.TrimSpace(query)
	for strings.HasSuffix(query, ";") {
		query = strings.TrimSpace(strings.TrimSuffix(query, ";"))
	}
	return query
}

func buildTrinoSession(cfg *DbConnectionConfig) (*dbSession, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 8080
	}
	scheme := "http"
	if cfg.SSL {
		scheme = "https"
	}
	user := derefOr(cfg.Username, "")
	if user == "" {
		user = "mincode"
	}
	pass := derefOr(cfg.Password, "")
	database := ""
	if d := trimmedOptional(cfg.Database); d != nil {
		database = *d
	}
	catalog, schema := trinoCatalogAndSchema(database)

	u := &url.URL{
		Scheme: scheme,
		Host:   fmt.Sprintf("%s:%d", host, port),
	}
	if pass != "" {
		u.User = url.UserPassword(user, pass)
	} else {
		u.User = url.User(user)
	}
	q := url.Values{}
	q.Set("source", "mincode")
	if catalog != "" {
		q.Set("catalog", catalog)
	}
	if schema != "" {
		q.Set("schema", schema)
	}
	u.RawQuery = q.Encode()

	db, err := sql.Open("trino", u.String())
	if err != nil {
		return nil, fmt.Errorf("Trino 打开失败: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("Trino 连接失败: %w", err)
	}
	return &dbSession{
		kind:            kindSQL,
		db:              db,
		dialect:         "trino",
		defaultDatabase: catalog,
	}, nil
}

// trinoSchema 列出 schema 与表（information_schema，默认 catalog 作用域）。
func trinoSchema(ctx context.Context, s *dbSession) (*DbSchemaInfo, error) {
	catalog := strings.TrimSpace(s.defaultDatabase)
	query := `SELECT table_schema, table_name, table_type FROM information_schema.tables
		WHERE table_schema NOT IN ('information_schema') ORDER BY table_schema, table_name`
	if catalog != "" {
		query = fmt.Sprintf(`SELECT table_schema, table_name, table_type FROM "%s".information_schema.tables
			WHERE table_schema NOT IN ('information_schema') ORDER BY table_schema, table_name`,
			escapeDoubleQuote(catalog))
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bySchema := map[string][]SchemaTable{}
	var order []string
	for rows.Next() {
		var schema, name, tableType string
		if err := rows.Scan(&schema, &name, &tableType); err != nil {
			return nil, err
		}
		kind := "table"
		if strings.Contains(strings.ToUpper(tableType), "VIEW") {
			kind = "view"
		}
		if _, ok := bySchema[schema]; !ok {
			order = append(order, schema)
		}
		bySchema[schema] = append(bySchema[schema], SchemaTable{Name: name, Kind: kind})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var databases []SchemaDatabase
	for _, schema := range order {
		databases = append(databases, SchemaDatabase{Name: schema, Tables: bySchema[schema]})
	}
	if databases == nil {
		databases = []SchemaDatabase{}
	}
	info := &DbSchemaInfo{Databases: databases}
	if catalog != "" {
		c := catalog
		info.Catalog = &c
	}
	return info, nil
}

// trinoColumns 查询列信息（information_schema.columns）。
func trinoColumns(ctx context.Context, s *dbSession, database, table string) ([]ColumnInfo, error) {
	catalog := strings.TrimSpace(s.defaultDatabase)
	prefix := ""
	if catalog != "" {
		prefix = fmt.Sprintf(`"%s".`, escapeDoubleQuote(catalog))
	}
	query := fmt.Sprintf(`SELECT column_name, data_type, is_nullable, column_default
		FROM %sinformation_schema.columns
		WHERE table_schema = ? AND table_name = ? ORDER BY ordinal_position`, prefix)
	rows, err := s.db.QueryContext(ctx, query, database, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []ColumnInfo
	for rows.Next() {
		var name, dataType, nullable string
		var def sql.NullString
		if err := rows.Scan(&name, &dataType, &nullable, &def); err != nil {
			return nil, err
		}
		col := ColumnInfo{
			Name:     name,
			DataType: dataType,
			Nullable: strings.EqualFold(nullable, "YES"),
		}
		if def.Valid {
			v := def.String
			col.DefaultValue = &v
		}
		cols = append(cols, col)
	}
	if cols == nil {
		cols = []ColumnInfo{}
	}
	return cols, rows.Err()
}
