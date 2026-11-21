package db

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteBackupDumpRestoresExactly(t *testing.T) {
	dir := t.TempDir()
	source, err := sql.Open("sqlite", filepath.Join(dir, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, statement := range []string{
		`CREATE TABLE "order ""items""" (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			score REAL,
			data BLOB,
			created DATETIME,
			shout TEXT GENERATED ALWAYS AS (upper(name)) VIRTUAL
		)`,
		`CREATE INDEX idx_items_name ON "order ""items"""(name)`,
		`CREATE TRIGGER items_touch AFTER INSERT ON "order ""items""" BEGIN SELECT 1; END`,
		`CREATE TABLE notes (k TEXT PRIMARY KEY, v)`,
		`CREATE VIEW item_names AS SELECT name FROM "order ""items"""`,
		`INSERT INTO "order ""items"""(name, score, data, created) VALUES
			('O''Brien; DROP TABLE x; --', 1.5, X'00FF10', '2024-01-02 03:04:05'),
			('多行
文本 \ 反斜杠', NULL, NULL, '2024-01-02T03:04:05Z'),
			('plain', -0.25, X'', NULL)`,
		`INSERT INTO notes VALUES ('int', 42), ('float', 3.25), ('text', 'x'), ('blob', X'CAFE'), ('null', NULL)`,
	} {
		if _, err := source.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	session := &dbSession{kind: kindSQL, db: source, dialect: "sqlite"}
	src, err := newSessionBackupSource(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer src.close()
	job := BackupJob{
		Name: "sqlite job", Destination: dir, FileNamePrefix: "sqlite",
		Targets: []BackupTarget{{Database: "main"}},
	}
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.Local)
	result, err := writeBackupFile(context.Background(), job, src, backupDumpMeta{JobName: job.Name, ConnectionName: "local", DbType: "sqlite", StartedAt: now}, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Tables != 2 || result.Rows != 8 || result.SizeBytes <= 0 || filepath.Base(result.File) != "sqlite_20260203-040506.sql" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %v", result.Warnings)
	}
	dump, err := os.ReadFile(result.File)
	if err != nil {
		t.Fatal(err)
	}
	text := string(dump)
	for _, want := range []string{"-- Job: sqlite job", "PRAGMA foreign_keys=OFF;", "CREATE INDEX idx_items_name", "CREATE TRIGGER items_touch", "CREATE VIEW item_names", "X'00FF10'", "'2024-01-02 03:04:05'", "COMMIT;"} {
		if !strings.Contains(text, want) {
			t.Errorf("dump missing %q", want)
		}
	}
	if strings.Contains(text, `"shout") VALUES`) || strings.Contains(text, `, "shout"`) {
		t.Error("generated columns must not be inserted")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".sqlite-*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("temp files left: %v", matches)
	}

	restored, err := sql.Open("sqlite", filepath.Join(dir, "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.Exec(text); err != nil {
		t.Fatalf("restore: %v\n%s", err, text)
	}
	const itemsQuery = `SELECT id, name, quote(score), quote(data), quote(created), typeof(created), shout FROM "order ""items""" ORDER BY id`
	const notesQuery = `SELECT k, quote(v), typeof(v) FROM notes ORDER BY k`
	for _, query := range []string{itemsQuery, notesQuery, `SELECT name FROM item_names ORDER BY name`,
		`SELECT name FROM sqlite_master WHERE type IN ('index','trigger') ORDER BY name`} {
		want := backupQueryRows(t, source, query)
		got := backupQueryRows(t, restored, query)
		if want != got {
			t.Errorf("%s\nsource:   %s\nrestored: %s", query, want, got)
		}
	}
}

func backupQueryRows(t *testing.T, conn *sql.DB, query string) string {
	t.Helper()
	rows, err := conn.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, _ := rows.Columns()
	var out []string
	for rows.Next() {
		values := make([]sql.NullString, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(values))
	}
	return strings.Join(out, "\n")
}

func TestSQLiteBackupSelectedTablesAndGzip(t *testing.T) {
	dir := t.TempDir()
	conn, err := sql.Open("sqlite", filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, statement := range []string{
		`CREATE TABLE keep (id INTEGER PRIMARY KEY, v TEXT)`,
		`CREATE TABLE skip (id INTEGER PRIMARY KEY)`,
		`CREATE VIEW v AS SELECT * FROM keep`,
		`INSERT INTO keep(v) VALUES ('a'), ('b')`,
	} {
		if _, err := conn.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	src, err := newSessionBackupSource(context.Background(), &dbSession{kind: kindSQL, db: conn, dialect: "sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	job := BackupJob{
		Name: "gz", Destination: dir, FileNamePrefix: "gz", Gzip: true,
		Targets: []BackupTarget{{Database: "main", Tables: []string{"keep", "missing"}}},
	}
	now := time.Now()
	result, err := writeBackupFile(context.Background(), job, src, backupDumpMeta{StartedAt: now}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(result.File, ".sql.gz") || result.Tables != 1 || result.Rows != 2 || len(result.Warnings) != 1 {
		t.Fatalf("result = %+v", result)
	}
	file, err := os.Open(result.File)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `INSERT INTO "keep" ("id", "v") VALUES (1, 'a'),`) || strings.Contains(text, `"skip"`) || strings.Contains(text, "CREATE VIEW") {
		t.Fatalf("unexpected selected-table dump:\n%s", text)
	}
}

func TestBackupCancelledDumpLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	src := &fakeBackupSource{dialect: "mysql", qualify: true, tables: map[string][]string{"app": {"t"}},
		columns: []backupColumn{{Name: "id", Type: "INT"}},
		rows:    map[string][][]any{"t": {{int64(1)}, {int64(2)}}},
		onRow:   cancel,
	}
	job := BackupJob{Name: "c", Destination: dir, FileNamePrefix: "c", Targets: []BackupTarget{{Database: "app"}}}
	if _, err := writeBackupFile(ctx, job, src, backupDumpMeta{StartedAt: time.Now()}, time.Now()); err == nil {
		t.Fatal("cancelled dump must fail")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("files left after cancel: %v", entries)
	}
}

// fakeBackupSource 以固定表结构和行数据模拟其他方言的数据源。
type fakeBackupSource struct {
	dialect string
	qualify bool
	tables  map[string][]string
	ddl     backupTableDDL
	columns []backupColumn
	rows    map[string][][]any
	onRow   func()
}

func (f *fakeBackupSource) dumpDialect() string { return f.dialect }
func (f *fakeBackupSource) qualifyTables() bool { return f.qualify }
func (f *fakeBackupSource) listTables(_ context.Context, database string) ([]string, error) {
	return f.tables[database], nil
}
func (f *fakeBackupSource) tableDDL(_ context.Context, _, table string) (backupTableDDL, error) {
	ddl := f.ddl
	if ddl.Create == "" {
		ddl.Create = "CREATE TABLE " + table + " (x INT)"
	}
	return ddl, nil
}
func (f *fakeBackupSource) streamRows(ctx context.Context, _, table string, onColumns func([]backupColumn) error, onRow func([]any) error) error {
	if err := onColumns(f.columns); err != nil {
		return err
	}
	for _, row := range f.rows[table] {
		if f.onRow != nil {
			f.onRow()
		}
		if err := onRow(row); err != nil {
			return err
		}
	}
	return ctx.Err()
}
func (f *fakeBackupSource) extraDatabaseDDL(context.Context, string) ([]string, error) {
	return nil, nil
}

func dumpFake(t *testing.T, src *fakeBackupSource, targets []BackupTarget) (string, backupDumpStats) {
	t.Helper()
	var buf bytes.Buffer
	stats, err := writeBackupDump(context.Background(), &buf, src, backupDumpMeta{
		JobName: "job\nname", ConnectionName: "conn", DbType: src.dialect, StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, targets)
	if err != nil {
		t.Fatal(err)
	}
	return buf.String(), stats
}

func TestMySQLBackupDumpWithFakeSource(t *testing.T) {
	when := time.Date(2024, 5, 6, 7, 8, 9, 120000000, time.UTC)
	src := &fakeBackupSource{
		dialect: "mysql", qualify: true,
		tables: map[string][]string{"shop`db": {"orders"}},
		ddl:    backupTableDDL{Drop: "DROP TABLE IF EXISTS `orders`", Create: "CREATE TABLE `orders` (`id` int)"},
		columns: []backupColumn{
			{Name: "id", Type: "BIGINT"}, {Name: "amount", Type: "DECIMAL"}, {Name: "note", Type: "VARCHAR"},
			{Name: "raw", Type: "BLOB"}, {Name: "day", Type: "DATE"}, {Name: "at", Type: "DATETIME"}, {Name: "flag", Type: "BIT"},
		},
	}
	rows := make([][]any, 0, backupInsertBatchRows+1)
	rows = append(rows, []any{[]byte("1"), []byte("12.50"), []byte("it's a \\ \"test\"\n\x00"), []byte{0, 1, 2}, when, when, []byte{1}})
	rows = append(rows, []any{[]byte("2"), nil, []byte("x"), nil, time.Time{}, time.Time{}, []byte{0}})
	for i := 3; i <= backupInsertBatchRows+1; i++ {
		rows = append(rows, []any{int64(i), nil, nil, nil, nil, nil, nil})
	}
	src.rows = map[string][][]any{"orders": rows}
	text, stats := dumpFake(t, src, []BackupTarget{{Database: "shop`db"}})
	for _, want := range []string{
		"-- Job: job name",
		"SET NAMES utf8mb4;",
		"SET FOREIGN_KEY_CHECKS=0;",
		"CREATE DATABASE IF NOT EXISTS `shop``db`;",
		"USE `shop``db`;",
		"DROP TABLE IF EXISTS `orders`;",
		"INSERT INTO `orders` (`id`, `amount`, `note`, `raw`, `day`, `at`, `flag`) VALUES (1, 12.50, 'it\\'s a \\\\ \"test\"\\n\\0', X'000102', '2024-05-06', '2024-05-06 07:08:09.12', X'01')",
		"(2, NULL, 'x', NULL, '0000-00-00', '0000-00-00 00:00:00', X'00')",
		"SET FOREIGN_KEY_CHECKS=1;",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("mysql dump missing %q\n%s", want, text[:min(len(text), 2000)])
		}
	}
	if got := strings.Count(text, "INSERT INTO"); got != 2 {
		t.Errorf("expected 2 batched INSERT statements, got %d", got)
	}
	if stats.Rows != int64(len(rows)) || stats.Tables != 1 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestPostgresBackupDumpWithFakeSource(t *testing.T) {
	ddl, err := buildBackupTableDDL("postgres", "sales", SchemaTableSnapshot{
		Name: `Order"s`,
		Columns: []SchemaColumnSnapshot{
			{Name: "id", DataType: "bigint", AutoIncrement: true, DefaultValue: backupStringPtr("nextval('orders_id_seq'::regclass)")},
			{Name: "code", DataType: "integer", AutoIncrement: true},
			{Name: "payload", DataType: "jsonb", Nullable: true},
		},
		PrimaryKey: []string{"id"},
		Indexes:    []SchemaIndexSnapshot{{Name: "orders_payload", Columns: []string{"payload"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("", -5*3600)
	src := &fakeBackupSource{
		dialect: "postgres", qualify: true, ddl: ddl,
		tables: map[string][]string{"sales": {`Order"s`}},
		columns: []backupColumn{
			{Name: "id", Type: "INT8"}, {Name: "bin", Type: "BYTEA"}, {Name: "doc", Type: "JSONB"},
			{Name: "at", Type: "TIMESTAMPTZ"}, {Name: "f", Type: "FLOAT8"}, {Name: "ok", Type: "BOOL"}, {Name: "n", Type: "NUMERIC"},
		},
		rows: map[string][][]any{`Order"s`: {
			{int64(1), []byte{0xde, 0xad}, []byte(`{"a":"it's"}`), time.Date(2024, 1, 2, 3, 4, 5, 0, loc), math.NaN(), true, "10.5"},
		}},
	}
	text, _ := dumpFake(t, src, []BackupTarget{{Database: "sales"}})
	for _, want := range []string{
		"SET standard_conforming_strings = on;",
		`CREATE SCHEMA IF NOT EXISTS "sales";`,
		`DROP TABLE IF EXISTS "sales"."Order""s";`,
		`CREATE TABLE "sales"."Order""s" ("id" BIGSERIAL NOT NULL, "code" integer GENERATED BY DEFAULT AS IDENTITY NOT NULL, "payload" jsonb, PRIMARY KEY ("id"));`,
		`INSERT INTO "sales"."Order""s" ("id", "bin", "doc", "at", "f", "ok", "n") VALUES (1, '\xDEAD'::bytea, '{"a":"it''s"}', '2024-01-02 03:04:05-05:00', 'NaN', TRUE, '10.5');`,
		`CREATE INDEX "orders_payload" ON "sales"."Order""s" ("payload");`,
		`SELECT setval(pg_get_serial_sequence('"sales"."Order""s"', 'id'), COALESCE(MAX("id"), 1), MAX("id") IS NOT NULL) FROM "sales"."Order""s";`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("postgres dump missing %q\n%s", want, text)
		}
	}
	if strings.Index(text, "CREATE INDEX") < strings.Index(text, "INSERT INTO") {
		t.Error("indexes must be created after data")
	}
}

func TestSQLServerBackupDumpWithFakeSource(t *testing.T) {
	ddl, err := buildBackupTableDDL("sqlserver", "", SchemaTableSnapshot{
		Name: "t]x",
		Columns: []SchemaColumnSnapshot{
			{Name: "id", DataType: "int", AutoIncrement: true},
			{Name: "name", DataType: "nvarchar(50)", Nullable: true},
		},
		PrimaryKey: []string{"id"},
	})
	if err != nil {
		t.Fatal(err)
	}
	guid := []byte{0x33, 0x22, 0x11, 0x00, 0x55, 0x44, 0x77, 0x66, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	src := &fakeBackupSource{
		dialect: "sqlserver", qualify: true, ddl: ddl,
		tables: map[string][]string{"dbo": {"t]x"}},
		columns: []backupColumn{
			{Name: "id", Type: "INT"}, {Name: "name", Type: "NVARCHAR"}, {Name: "g", Type: "UNIQUEIDENTIFIER"},
			{Name: "m", Type: "MONEY"}, {Name: "bin", Type: "VARBINARY"}, {Name: "dt", Type: "DATETIME"}, {Name: "b", Type: "BIT"},
		},
		rows: map[string][][]any{"t]x": {
			{int64(1), "中文'", guid, []byte("12.3400"), []byte{0xab}, time.Date(2024, 1, 2, 3, 4, 5, 6000000, time.UTC), true},
		}},
	}
	text, _ := dumpFake(t, src, []BackupTarget{{Database: "dbo"}})
	for _, want := range []string{
		"IF SCHEMA_ID(N'dbo') IS NULL EXEC(N'CREATE SCHEMA [dbo]');",
		"IF OBJECT_ID(N'[dbo].[t]]x]', N'U') IS NOT NULL DROP TABLE [dbo].[t]]x];",
		"CREATE TABLE [dbo].[t]]x] ([id] int IDENTITY(1,1) NOT NULL, [name] nvarchar(50), PRIMARY KEY ([id]));",
		"SET IDENTITY_INSERT [dbo].[t]]x] ON;",
		"VALUES (1, N'中文''', '00112233-4455-6677-8899-AABBCCDDEEFF', 12.3400, 0xAB, '2024-01-02T03:04:05.006', 1);",
		"SET IDENTITY_INSERT [dbo].[t]]x] OFF;",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("sqlserver dump missing %q\n%s", want, text)
		}
	}
}

func TestOracleBackupDumpWithFakeSource(t *testing.T) {
	long := strings.Repeat("é", 2500) // 5000 字节
	src := &fakeBackupSource{
		dialect: "oracle", qualify: true,
		tables: map[string][]string{"HR": {"EMP"}},
		columns: []backupColumn{
			{Name: "ID", Type: "NUMBER"}, {Name: "HIRED", Type: "DATE"}, {Name: "TS", Type: "TIMESTAMP"},
			{Name: "PHOTO", Type: "RAW"}, {Name: "BIO", Type: "CLOB"}, {Name: "BIG", Type: "BLOB"},
		},
		rows: map[string][][]any{"EMP": {
			{int64(1), time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), time.Date(2020, 1, 2, 3, 4, 5, 123000000, time.UTC), []byte{1, 2}, long, []byte{3, 4}},
			{int64(2), nil, nil, nil, "short", nil},
		}},
	}
	text, stats := dumpFake(t, src, []BackupTarget{{Database: "HR"}})
	for _, want := range []string{
		`INSERT INTO "HR"."EMP" ("ID", "HIRED", "TS", "PHOTO", "BIO", "BIG") VALUES (1, TO_DATE('2020-01-02 03:04:05', 'YYYY-MM-DD HH24:MI:SS'), TIMESTAMP '2020-01-02 03:04:05.123', HEXTORAW('0102'), TO_CLOB('`,
		`') || TO_CLOB('`,
		`HEXTORAW('0304'));`,
		`INSERT INTO "HR"."EMP" ("ID", "HIRED", "TS", "PHOTO", "BIO", "BIG") VALUES (2, NULL, NULL, NULL, 'short', NULL);`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("oracle dump missing %q", want)
		}
	}
	if strings.Contains(text, "DROP TABLE") {
		t.Error("oracle dumps must not emit DROP TABLE without IF EXISTS support")
	}
	if len(stats.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", stats.Warnings)
	}
}

func TestBackupLiteralEdgeCases(t *testing.T) {
	cases := []struct {
		dialect string
		col     backupColumn
		value   any
		want    string
	}{
		{"mysql", backupColumn{Type: "INT"}, []byte("1; DROP"), "'1; DROP'"},
		{"mysql", backupColumn{Type: "UNSIGNED BIGINT"}, []byte("18446744073709551615"), "18446744073709551615"},
		{"mysql", backupColumn{Type: "TEXT"}, []byte{0xff, 0xfe}, "X'FFFE'"},
		{"postgres", backupColumn{Type: "TIMESTAMP"}, time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), "'2024-01-02 03:04:05'"},
		{"postgres", backupColumn{Type: "DATE"}, time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), "'2024-01-02'"},
		{"postgres", backupColumn{}, math.Inf(-1), "'-Infinity'"},
		{"sqlite", backupColumn{}, true, "1"},
		{"sqlite", backupColumn{}, 1.0e21, "1e+21"},
		{"duckdb", backupColumn{Type: "BLOB"}, []byte{0x0a, 0xff}, `'\x0A\xFF'::BLOB`},
		{"duckdb", backupColumn{Type: "TIMESTAMP WITH TIME ZONE"}, time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), "TIMESTAMPTZ '2024-01-02 03:04:05+00:00'"},
		{"firebird", backupColumn{Type: "TIMESTAMP"}, time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), "TIMESTAMP '2024-01-02 03:04:05.0000'"},
		{"firebird", backupColumn{Type: "BOOLEAN"}, false, "FALSE"},
		{"clickhouse", backupColumn{}, `a\'b`, `'a\\''b'`},
		{"postgres", backupColumn{}, map[string]any{"k": []any{1.0, "v"}}, `'{"k":[1,"v"]}'`},
	}
	for _, tc := range cases {
		got, err := backupLiteral(tc.dialect, tc.col, tc.value)
		if err != nil || got != tc.want {
			t.Errorf("%s %s %#v = %q, %v; want %q", tc.dialect, tc.col.Type, tc.value, got, err, tc.want)
		}
	}
	for _, dialect := range []string{"mysql", "sqlserver", "oracle", "sqlite"} {
		if _, err := backupLiteral(dialect, backupColumn{}, math.NaN()); err == nil {
			t.Errorf("%s must reject NaN", dialect)
		}
	}
	if _, err := backupLiteral("mysql", backupColumn{}, struct{}{}); err == nil {
		t.Error("unknown Go types must be reported as unsupported")
	}
}

func TestBackupDumpDialectSupport(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres", "sqlite", "sqlserver", "oracle", "dm", "duckdb", "firebird"} {
		if got, err := backupDumpDialect(&dbSession{kind: kindSQL, dialect: dialect}); err != nil || got != dialect {
			t.Errorf("%s: %s %v", dialect, got, err)
		}
	}
	for _, dialect := range []string{"clickhouse", "tdengine", "trino", "yashandb"} {
		if _, err := backupDumpDialect(&dbSession{kind: kindSQL, dialect: dialect}); err == nil {
			t.Errorf("%s must be unsupported", dialect)
		}
	}
	if _, err := backupDumpDialect(&dbSession{kind: kindRedis}); err == nil {
		t.Error("non-SQL sessions must be unsupported")
	}
}

func backupStringPtr(value string) *string { return &value }

func TestBackupRefusesLossySQLValues(t *testing.T) {
	for _, value := range []any{make([]byte, backupOracleRawMax+1), struct{ Value string }{"unknown"}} {
		src := &fakeBackupSource{dialect: "oracle", qualify: true,
			tables: map[string][]string{"HR": {"EMP"}}, columns: []backupColumn{{Name: "BIG", Type: "BLOB"}},
			rows: map[string][][]any{"EMP": {{value}}},
		}
		job := BackupJob{Destination: t.TempDir(), FileNamePrefix: "strict", Targets: []BackupTarget{{Database: "HR"}}}
		_, err := writeBackupFile(context.Background(), job, src, backupDumpMeta{}, time.Now())
		if err == nil || !strings.Contains(err.Error(), "refusing lossy") {
			t.Fatalf("error: %v", err)
		}
		entries, err := os.ReadDir(job.Destination)
		if err != nil || len(entries) != 0 {
			t.Fatalf("failed backup published files: %v %v", entries, err)
		}
	}
}
