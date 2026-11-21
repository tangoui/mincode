package db

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	backupInsertBatchRows  = 500
	backupInsertBatchBytes = 1 << 20
	backupOracleStringMax  = 4000
	backupOracleChunkRunes = 1000
	backupOracleRawMax     = 2000
)

// backupColumn 描述结果集中的一列；Type 为驱动报告的数据库类型名（大写）。
type backupColumn struct {
	Name string
	Type string
}

// backupTableDDL 一张表的结构语句：Drop/Create 在数据前输出，After 在数据后输出（索引、触发器、序列校正等）。
type backupTableDDL struct {
	Drop   string
	Create string
	After  []string
	// IdentityInsert 为 true 时（SQL Server 自增列）插入前后切换 IDENTITY_INSERT。
	IdentityInsert bool
}

// backupSource 抽象被备份的数据源，便于用假实现测试各方言输出。
type backupSource interface {
	// dumpDialect 是输出 SQL 采用的方言（mysql/postgres/sqlite/sqlserver/oracle/dm/firebird/duckdb/clickhouse）。
	dumpDialect() string
	// qualifyTables 为 false 时 INSERT 使用不带库名的表名（SQLite、Firebird、插件驱动）。
	qualifyTables() bool
	listTables(ctx context.Context, database string) ([]string, error)
	tableDDL(ctx context.Context, database, table string) (backupTableDDL, error)
	streamRows(ctx context.Context, database, table string, onColumns func([]backupColumn) error, onRow func([]any) error) error
	// extraDatabaseDDL 返回整库备份时在所有表之后输出的对象（如 SQLite 视图）。
	extraDatabaseDDL(ctx context.Context, database string) ([]string, error)
}

type backupDumpMeta struct {
	JobName        string
	ConnectionName string
	DbType         string
	StartedAt      time.Time
	Preamble       []string
}

type backupDumpStats struct {
	Tables   int
	Rows     int64
	Warnings []string
}

type backupDumper struct {
	src      backupSource
	dialect  string
	w        *bufio.Writer
	stats    backupDumpStats
	warnSeen map[string]bool
	progress func(table string, rows int64)
}

func (d *backupDumper) warn(message string) {
	if d.warnSeen[message] {
		return
	}
	d.warnSeen[message] = true
	if len(d.stats.Warnings) < backupMaxWarnings {
		d.stats.Warnings = append(d.stats.Warnings, message)
	} else if len(d.stats.Warnings) == backupMaxWarnings {
		d.stats.Warnings = append(d.stats.Warnings, "更多警告已省略")
	}
}

func (d *backupDumper) printf(format string, args ...any) error {
	_, err := fmt.Fprintf(d.w, format, args...)
	return err
}

func (d *backupDumper) statement(sql string) error {
	sql = strings.TrimSpace(sql)
	sql = strings.TrimRight(sql, ";")
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return nil
	}
	_, err := d.w.WriteString(sql + ";\n")
	return err
}

// writeBackupDump 以流式方式把 targets 写成可移植的 SQL 文本。内存占用受单批 INSERT 上限约束。
func writeBackupDump(ctx context.Context, out io.Writer, src backupSource, meta backupDumpMeta, targets []BackupTarget) (backupDumpStats, error) {
	d := &backupDumper{
		src:      src,
		dialect:  src.dumpDialect(),
		w:        bufio.NewWriterSize(out, 256<<10),
		warnSeen: map[string]bool{},
	}
	if err := d.header(meta, targets); err != nil {
		return d.stats, err
	}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return d.stats, err
		}
		if err := d.database(ctx, target); err != nil {
			return d.stats, err
		}
	}
	if err := d.footer(); err != nil {
		return d.stats, err
	}
	if err := d.w.Flush(); err != nil {
		return d.stats, fmt.Errorf("写入备份文件: %w", err)
	}
	return d.stats, nil
}

func backupCommentValue(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r < 0x20 {
			return ' '
		}
		return r
	}, value)
	return strings.TrimSpace(value)
}

func (d *backupDumper) header(meta backupDumpMeta, targets []BackupTarget) error {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.Database)
	}
	lines := []string{
		"-- MinCode database backup",
		"-- Job: " + backupCommentValue(meta.JobName),
		"-- Connection: " + backupCommentValue(meta.ConnectionName),
		"-- Type: " + backupCommentValue(meta.DbType) + " (dialect " + d.dialect + ")",
		"-- Started: " + meta.StartedAt.Format(time.RFC3339),
		"-- Databases: " + backupCommentValue(strings.Join(names, ", ")),
		"",
	}
	if _, err := d.w.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		return err
	}
	switch d.dialect {
	case "mysql":
		for _, line := range []string{"SET NAMES utf8mb4", "SET FOREIGN_KEY_CHECKS=0", "SET UNIQUE_CHECKS=0", "SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO'"} {
			if err := d.statement(line); err != nil {
				return err
			}
		}
	case "postgres":
		for _, line := range []string{"SET client_encoding = 'UTF8'", "SET standard_conforming_strings = on"} {
			if err := d.statement(line); err != nil {
				return err
			}
		}
	case "sqlite":
		for _, line := range []string{"PRAGMA foreign_keys=OFF", "BEGIN TRANSACTION"} {
			if err := d.statement(line); err != nil {
				return err
			}
		}
	}
	for _, line := range meta.Preamble {
		if err := d.statement(line); err != nil {
			return err
		}
	}
	return d.printf("\n")
}

func (d *backupDumper) footer() error {
	var lines []string
	switch d.dialect {
	case "mysql":
		lines = []string{"SET FOREIGN_KEY_CHECKS=1", "SET UNIQUE_CHECKS=1"}
	case "sqlite":
		lines = []string{"COMMIT"}
	}
	for _, line := range lines {
		if err := d.statement(line); err != nil {
			return err
		}
	}
	return d.printf("-- Backup completed\n")
}

func (d *backupDumper) database(ctx context.Context, target BackupTarget) error {
	all, err := d.src.listTables(ctx, target.Database)
	if err != nil {
		return fmt.Errorf("读取 %s 的表列表: %w", target.Database, err)
	}
	tables := all
	if len(target.Tables) > 0 {
		present := make(map[string]bool, len(all))
		for _, name := range all {
			present[name] = true
		}
		tables = make([]string, 0, len(target.Tables))
		for _, name := range target.Tables {
			if !present[name] {
				d.warn(fmt.Sprintf("%s.%s 不存在，已跳过", target.Database, name))
				continue
			}
			tables = append(tables, name)
		}
	}
	if err := d.printf("-- Database: %s\n", backupCommentValue(target.Database)); err != nil {
		return err
	}
	for _, line := range d.databasePrelude(target.Database) {
		if err := d.statement(line); err != nil {
			return err
		}
	}
	for _, table := range tables {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.table(ctx, target.Database, table); err != nil {
			return fmt.Errorf("备份表 %s.%s: %w", target.Database, table, err)
		}
	}
	if len(target.Tables) == 0 {
		extra, err := d.src.extraDatabaseDDL(ctx, target.Database)
		if err != nil {
			return fmt.Errorf("读取 %s 的附加对象: %w", target.Database, err)
		}
		for _, stmt := range extra {
			if err := d.statement(stmt); err != nil {
				return err
			}
		}
	}
	return d.printf("\n")
}

func (d *backupDumper) databasePrelude(database string) []string {
	switch d.dialect {
	case "mysql":
		quoted := schemaQuoteIdentifier("mysql", database)
		if d.src.qualifyTables() {
			return []string{"CREATE DATABASE IF NOT EXISTS " + quoted, "USE " + quoted}
		}
		return []string{"USE " + quoted}
	case "postgres":
		if d.src.qualifyTables() {
			return []string{"CREATE SCHEMA IF NOT EXISTS " + schemaQuoteIdentifier("postgres", backupPostgresSchema(database))}
		}
	case "duckdb":
		if d.src.qualifyTables() {
			schema := duckdbSchemaName(database)
			return []string{"CREATE SCHEMA IF NOT EXISTS " + schemaQuoteIdentifier("duckdb", schema), "SET schema = " + sqlExportQuotedString("duckdb", schema)}
		}
	case "sqlserver":
		if d.src.qualifyTables() {
			schema := backupSQLServerSchema(database)
			create := "CREATE SCHEMA " + schemaQuoteIdentifier("sqlserver", schema)
			return []string{"IF SCHEMA_ID(" + backupSQLServerString(schema) + ") IS NULL EXEC(" + backupSQLServerString(create) + ")"}
		}
	}
	return nil
}

func backupPostgresSchema(database string) string {
	if strings.TrimSpace(database) == "" {
		return "public"
	}
	return database
}

func backupSQLServerSchema(database string) string {
	if strings.TrimSpace(database) == "" {
		return "dbo"
	}
	return database
}

// backupInsertTarget 返回 INSERT 使用的表名。
func (d *backupDumper) backupInsertTarget(database, table string) string {
	if !d.src.qualifyTables() {
		return schemaQuoteIdentifier(d.dialect, table)
	}
	switch d.dialect {
	case "mysql":
		// 已通过 USE 切换库，使用不带库名的表名便于恢复到其他库。
		return schemaQuoteIdentifier("mysql", table)
	case "postgres":
		return schemaQualifiedTable("postgres", backupPostgresSchema(database), table)
	case "duckdb":
		return schemaQualifiedTable("duckdb", duckdbSchemaName(database), table)
	case "sqlserver":
		return schemaQualifiedTable("sqlserver", backupSQLServerSchema(database), table)
	default:
		return schemaQualifiedTable(d.dialect, database, table)
	}
}

// backupSingleRowInsert 这些方言不使用多行 VALUES，逐行 INSERT 以保证兼容。
func backupSingleRowInsert(dialect string) bool {
	switch dialect {
	case "oracle", "dm", "firebird":
		return true
	}
	return false
}

func (d *backupDumper) table(ctx context.Context, database, table string) error {
	ddl, err := d.src.tableDDL(ctx, database, table)
	if err != nil {
		return fmt.Errorf("读取表结构: %w", err)
	}
	if err := d.printf("\n-- Table: %s\n", backupCommentValue(table)); err != nil {
		return err
	}
	if err := d.statement(ddl.Drop); err != nil {
		return err
	}
	if err := d.statement(ddl.Create); err != nil {
		return err
	}
	target := d.backupInsertTarget(database, table)
	var (
		columns  []backupColumn
		prefix   string
		batch    strings.Builder
		inBatch  int
		rows     int64
		identity bool
	)
	flush := func() error {
		if inBatch == 0 {
			return nil
		}
		if _, err := d.w.WriteString(prefix); err != nil {
			return err
		}
		if _, err := d.w.WriteString(batch.String()); err != nil {
			return err
		}
		if _, err := d.w.WriteString(";\n"); err != nil {
			return err
		}
		batch.Reset()
		inBatch = 0
		return nil
	}
	onColumns := func(cols []backupColumn) error {
		columns = cols
		names := make([]string, len(cols))
		for i, col := range cols {
			names[i] = schemaQuoteIdentifier(d.dialect, col.Name)
		}
		prefix = "INSERT INTO " + target + " (" + strings.Join(names, ", ") + ") VALUES "
		return nil
	}
	onRow := func(values []any) error {
		if len(values) != len(columns) {
			return fmt.Errorf("结果列数不一致")
		}
		if rows == 0 && ddl.IdentityInsert {
			identity = true
			if err := d.statement("SET IDENTITY_INSERT " + target + " ON"); err != nil {
				return err
			}
		}
		if inBatch > 0 {
			batch.WriteString(",\n  ")
		}
		batch.WriteByte('(')
		for i, value := range values {
			if i > 0 {
				batch.WriteString(", ")
			}
			literal, err := backupLiteral(d.dialect, columns[i], value)
			if err != nil {
				return fmt.Errorf("%s.%s.%s: refusing lossy SQL backup: %w", database, table, columns[i].Name, err)
			}
			batch.WriteString(literal)
		}
		batch.WriteByte(')')
		inBatch++
		rows++
		if backupSingleRowInsert(d.dialect) || inBatch >= backupInsertBatchRows || batch.Len() >= backupInsertBatchBytes {
			if err := flush(); err != nil {
				return err
			}
			if d.progress != nil && rows%10000 == 0 {
				d.progress(table, rows)
			}
		}
		return ctx.Err()
	}
	if err := d.src.streamRows(ctx, database, table, onColumns, onRow); err != nil {
		return fmt.Errorf("读取数据: %w", err)
	}
	if err := flush(); err != nil {
		return err
	}
	if identity {
		if err := d.statement("SET IDENTITY_INSERT " + target + " OFF"); err != nil {
			return err
		}
	}
	for _, stmt := range ddl.After {
		if err := d.statement(stmt); err != nil {
			return err
		}
	}
	d.stats.Tables++
	d.stats.Rows += rows
	return nil
}

// ── literals ──

var backupNumericText = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

func backupTypeHas(typeName string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(typeName, part) {
			return true
		}
	}
	return false
}

// backupIsBinaryType 判断列类型是否为二进制，驱动以 []byte 返回时需按十六进制输出。
func backupIsBinaryType(dialect, typeName string) bool {
	switch dialect {
	case "mysql":
		return backupTypeHas(typeName, "BLOB", "BINARY", "GEOMETRY") || typeName == "BIT"
	case "postgres":
		return typeName == "BYTEA"
	case "sqlserver":
		return backupTypeHas(typeName, "BINARY", "IMAGE") || typeName == "TIMESTAMP" || typeName == "ROWVERSION"
	case "oracle", "dm":
		return backupTypeHas(typeName, "RAW", "BLOB", "BINARY", "IMAGE", "BFILE")
	case "duckdb":
		return backupTypeHas(typeName, "BLOB", "BYTEA")
	case "firebird":
		return typeName == "BLOB" || backupTypeHas(typeName, "OCTETS")
	case "sqlite":
		// SQLite 按存储类返回：TEXT 为 string，BLOB 为 []byte。
		return true
	}
	return false
}

// backupIsNumericType 判断列类型是否为数值；驱动以文本字节返回数值时可直接输出。
func backupIsNumericType(dialect, typeName string) bool {
	switch dialect {
	case "mysql":
		switch strings.TrimPrefix(typeName, "UNSIGNED ") {
		case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL", "YEAR":
			return true
		}
	case "sqlserver":
		switch typeName {
		case "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY":
			return true
		}
	case "postgres":
		return typeName == "NUMERIC"
	}
	return false
}

func backupLiteral(dialect string, col backupColumn, value any) (string, error) {
	typeName := strings.ToUpper(strings.TrimSpace(col.Type))
	switch v := value.(type) {
	case nil:
		return "NULL", nil
	case bool:
		switch dialect {
		case "sqlserver", "oracle", "dm", "sqlite", "mysql":
			if v {
				return "1", nil
			}
			return "0", nil
		}
		if v {
			return "TRUE", nil
		}
		return "FALSE", nil
	case int:
		return strconv.FormatInt(int64(v), 10), nil
	case int8:
		return strconv.FormatInt(int64(v), 10), nil
	case int16:
		return strconv.FormatInt(int64(v), 10), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case float32:
		return backupFloatLiteral(dialect, float64(v), 32)
	case float64:
		return backupFloatLiteral(dialect, v, 64)
	case string:
		return backupStringLiteral(dialect, v), nil
	case []byte:
		return backupBytesLiteral(dialect, typeName, v)
	case time.Time:
		return backupTimeLiteral(dialect, typeName, v), nil
	case json.Number:
		if backupNumericText.MatchString(string(v)) {
			return string(v), nil
		}
		return backupStringLiteral(dialect, string(v)), nil
	case map[string]any, []any:
		data, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("无法序列化复合值")
		}
		return backupStringLiteral(dialect, string(data)), nil
	case fmt.Stringer:
		return backupStringLiteral(dialect, v.String()), nil
	default:
		return "", fmt.Errorf("不支持的值类型 %T", value)
	}
}

func backupFloatLiteral(dialect string, value float64, bits int) (string, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		text := "NaN"
		if math.IsInf(value, 1) {
			text = "Infinity"
		} else if math.IsInf(value, -1) {
			text = "-Infinity"
		}
		switch dialect {
		case "postgres", "duckdb":
			return "'" + text + "'", nil
		}
		return "", fmt.Errorf("该数据库不支持浮点特殊值 %s", text)
	}
	return strconv.FormatFloat(value, 'g', -1, bits), nil
}

// backupStringLiteral 按方言转义字符串；MySQL 采用与 mysqldump 一致的反斜杠转义。
func backupStringLiteral(dialect, value string) string {
	switch dialect {
	case "mysql":
		var b strings.Builder
		b.Grow(len(value) + 2)
		b.WriteByte('\'')
		for i := 0; i < len(value); i++ {
			switch c := value[i]; c {
			case 0:
				b.WriteString(`\0`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case 0x1a:
				b.WriteString(`\Z`)
			case '\\':
				b.WriteString(`\\`)
			case '\'':
				b.WriteString(`\'`)
			default:
				b.WriteByte(c)
			}
		}
		b.WriteByte('\'')
		return b.String()
	case "sqlserver":
		return "N" + sqlExportQuotedString("sqlserver", value)
	case "oracle", "dm":
		if len(value) <= backupOracleStringMax {
			return sqlExportQuotedString(dialect, value)
		}
		// 超过 4000 字节的字符串字面量需拆为 CLOB 片段拼接。
		var parts []string
		runes := []rune(value)
		for start := 0; start < len(runes); start += backupOracleChunkRunes {
			end := start + backupOracleChunkRunes
			if end > len(runes) {
				end = len(runes)
			}
			parts = append(parts, "TO_CLOB("+sqlExportQuotedString(dialect, string(runes[start:end]))+")")
		}
		return strings.Join(parts, " || ")
	default:
		return sqlExportQuotedString(dialect, value)
	}
}

func backupHexLiteral(dialect string, data []byte) (string, error) {
	text := strings.ToUpper(hex.EncodeToString(data))
	switch dialect {
	case "postgres":
		return "'\\x" + text + "'::bytea", nil
	case "sqlserver":
		return "0x" + text, nil
	case "oracle", "dm":
		if len(data) == 0 {
			return "NULL", nil
		}
		if len(data) > backupOracleRawMax {
			return "", fmt.Errorf("二进制值超过 %d 字节，无法以字面量导出", backupOracleRawMax)
		}
		return "HEXTORAW('" + text + "')", nil
	case "duckdb":
		var b strings.Builder
		b.WriteByte('\'')
		for i := 0; i < len(text); i += 2 {
			b.WriteString(`\x`)
			b.WriteString(text[i : i+2])
		}
		b.WriteString("'::BLOB")
		return b.String(), nil
	case "clickhouse":
		return "unhex('" + text + "')", nil
	default:
		return "X'" + text + "'", nil
	}
}

func backupBytesLiteral(dialect, typeName string, data []byte) (string, error) {
	if dialect == "sqlserver" && typeName == "UNIQUEIDENTIFIER" && len(data) == 16 {
		return "'" + backupSQLServerGUID(data) + "'", nil
	}
	if dialect == "duckdb" && typeName == "UUID" && len(data) == 16 {
		return "'" + backupUUIDString(data) + "'", nil
	}
	if backupIsBinaryType(dialect, typeName) {
		return backupHexLiteral(dialect, data)
	}
	text := string(data)
	if backupIsNumericType(dialect, typeName) && backupNumericText.MatchString(text) {
		return text, nil
	}
	if !utf8.Valid(data) {
		// 文本列中出现非 UTF-8 字节时按二进制输出，避免写出损坏的字符串。
		return backupHexLiteral(dialect, data)
	}
	return backupStringLiteral(dialect, text), nil
}

// backupSQLServerGUID 把 SQL Server 混合字节序的 UNIQUEIDENTIFIER 转为标准文本。
func backupSQLServerGUID(b []byte) string {
	ordered := []byte{b[3], b[2], b[1], b[0], b[5], b[4], b[7], b[6], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15]}
	return strings.ToUpper(backupUUIDString(ordered))
}

func backupUUIDString(b []byte) string {
	text := hex.EncodeToString(b)
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:32]
}

func backupTimeLiteral(dialect, typeName string, t time.Time) string {
	withZone := backupTypeHas(typeName, "TZ", "TIME ZONE", "OFFSET")
	switch dialect {
	case "mysql":
		if t.IsZero() {
			if typeName == "DATE" {
				return "'0000-00-00'"
			}
			return "'0000-00-00 00:00:00'"
		}
		if typeName == "DATE" {
			return "'" + t.Format("2006-01-02") + "'"
		}
		return "'" + t.Format("2006-01-02 15:04:05.999999") + "'"
	case "postgres":
		switch {
		case typeName == "DATE":
			return "'" + t.Format("2006-01-02") + "'"
		case withZone || (typeName == "" && t.Location() != time.UTC):
			return "'" + t.Format("2006-01-02 15:04:05.999999Z07:00") + "'"
		default:
			return "'" + t.Format("2006-01-02 15:04:05.999999") + "'"
		}
	case "sqlserver":
		switch typeName {
		case "DATE":
			return "'" + t.Format("2006-01-02") + "'"
		case "TIME":
			return "'" + t.Format("15:04:05.9999999") + "'"
		case "DATETIME":
			return "'" + t.Format("2006-01-02T15:04:05.000") + "'"
		case "SMALLDATETIME":
			return "'" + t.Format("2006-01-02T15:04:05") + "'"
		case "DATETIMEOFFSET":
			return "'" + t.Format("2006-01-02T15:04:05.9999999-07:00") + "'"
		default:
			return "'" + t.Format("2006-01-02T15:04:05.9999999") + "'"
		}
	case "oracle", "dm":
		if typeName == "DATE" {
			return "TO_DATE('" + t.Format("2006-01-02 15:04:05") + "', 'YYYY-MM-DD HH24:MI:SS')"
		}
		if withZone {
			return "TIMESTAMP '" + t.Format("2006-01-02 15:04:05.999999999 -07:00") + "'"
		}
		return "TIMESTAMP '" + t.Format("2006-01-02 15:04:05.999999999") + "'"
	case "firebird":
		switch {
		case typeName == "DATE":
			return "DATE '" + t.Format("2006-01-02") + "'"
		case strings.HasPrefix(typeName, "TIME") && !strings.HasPrefix(typeName, "TIMESTAMP"):
			return "TIME '" + t.Format("15:04:05.0000") + "'"
		default:
			return "TIMESTAMP '" + t.Format("2006-01-02 15:04:05.0000") + "'"
		}
	case "duckdb":
		switch {
		case typeName == "DATE":
			return "DATE '" + t.Format("2006-01-02") + "'"
		case typeName == "TIME":
			return "TIME '" + t.Format("15:04:05.999999") + "'"
		case withZone:
			return "TIMESTAMPTZ '" + t.Format("2006-01-02 15:04:05.999999-07:00") + "'"
		default:
			return "TIMESTAMP '" + t.Format("2006-01-02 15:04:05.999999") + "'"
		}
	default:
		return backupStringLiteral(dialect, t.Format("2006-01-02 15:04:05.999999"))
	}
}

func backupSQLServerString(value string) string {
	return "N'" + strings.ReplaceAll(value, "'", "''") + "'"
}
