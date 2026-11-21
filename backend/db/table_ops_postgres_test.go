package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// pgSchemaFakeDriver 模拟 information_schema 查询并记录写语句，用于验证写路径的 schema 解析。
type pgSchemaFakeDriver struct {
	mu      sync.Mutex
	schemas map[string][]string // table -> schemas
	execs   []string
}

func (d *pgSchemaFakeDriver) Open(string) (driver.Conn, error) { return &pgSchemaFakeConn{d: d}, nil }

func (d *pgSchemaFakeDriver) lastExec() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.execs) == 0 {
		return ""
	}
	return d.execs[len(d.execs)-1]
}

type pgSchemaFakeConn struct{ d *pgSchemaFakeDriver }

func (c *pgSchemaFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}
func (c *pgSchemaFakeConn) Close() error              { return nil }
func (c *pgSchemaFakeConn) Begin() (driver.Tx, error) { return nil, errors.New("tx not supported") }

func (c *pgSchemaFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "information_schema.tables") || len(args) != 1 {
		return nil, errors.New("unexpected query: " + query)
	}
	name, _ := args[0].Value.(string)
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	return &pgSchemaFakeRows{values: append([]string(nil), c.d.schemas[name]...)}, nil
}

func (c *pgSchemaFakeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.execs = append(c.d.execs, query)
	return driver.RowsAffected(1), nil
}

type pgSchemaFakeRows struct {
	values []string
	pos    int
}

func (r *pgSchemaFakeRows) Columns() []string { return []string{"table_schema"} }
func (r *pgSchemaFakeRows) Close() error      { return nil }
func (r *pgSchemaFakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.values) {
		return io.EOF
	}
	dest[0] = r.values[r.pos]
	r.pos++
	return nil
}

var registerPgSchemaFake sync.Once
var pgSchemaFake = &pgSchemaFakeDriver{}

func newPgSchemaFakeSession(t *testing.T, schemas map[string][]string) *dbSession {
	t.Helper()
	registerPgSchemaFake.Do(func() { sql.Register("mincode-pg-schema-fake", pgSchemaFake) })
	pgSchemaFake.mu.Lock()
	pgSchemaFake.schemas = schemas
	pgSchemaFake.execs = nil
	pgSchemaFake.mu.Unlock()
	conn, err := sql.Open("mincode-pg-schema-fake", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &dbSession{kind: kindSQL, db: conn, dialect: "postgres"}
}

func TestPostgresWritePathsResolveSchemaLikeReads(t *testing.T) {
	ctx := context.Background()
	s := newPgSchemaFakeSession(t, map[string][]string{"orders": {"sales"}})
	pk := map[string]any{"id": float64(7)}

	for _, database := range []string{"", "public"} {
		if _, err := updateCellByPrimaryKey(ctx, s, database, "orders", pk, "status", "paid"); err != nil {
			t.Fatalf("update (database=%q): %v", database, err)
		}
		if got := pgSchemaFake.lastExec(); !strings.HasPrefix(got, `UPDATE "sales"."orders" SET "status" = $1`) {
			t.Fatalf("update (database=%q) SQL = %q", database, got)
		}

		if _, err := insertRow(ctx, s, database, "orders", map[string]any{"status": "new"}); err != nil {
			t.Fatalf("insert (database=%q): %v", database, err)
		}
		if got := pgSchemaFake.lastExec(); !strings.HasPrefix(got, `INSERT INTO "sales"."orders" ("status")`) {
			t.Fatalf("insert (database=%q) SQL = %q", database, got)
		}

		if _, err := deleteRowByPrimaryKey(ctx, s, database, "orders", pk); err != nil {
			t.Fatalf("delete (database=%q): %v", database, err)
		}
		if got := pgSchemaFake.lastExec(); got != `DELETE FROM "sales"."orders" WHERE "id" = $1` {
			t.Fatalf("delete (database=%q) SQL = %q", database, got)
		}
	}

	// 显式非 public schema 与 schema.table 形式不应被改写。
	if _, err := deleteRowByPrimaryKey(ctx, s, "audit", "orders", pk); err != nil {
		t.Fatal(err)
	}
	if got := pgSchemaFake.lastExec(); got != `DELETE FROM "audit"."orders" WHERE "id" = $1` {
		t.Fatalf("explicit schema SQL = %q", got)
	}
	if _, err := deleteRowByPrimaryKey(ctx, s, "", "audit.orders", pk); err != nil {
		t.Fatal(err)
	}
	if got := pgSchemaFake.lastExec(); got != `DELETE FROM "audit"."orders" WHERE "id" = $1` {
		t.Fatalf("dotted table SQL = %q", got)
	}
}

func TestPostgresWritePathsRejectAmbiguousOrMissingTable(t *testing.T) {
	ctx := context.Background()
	s := newPgSchemaFakeSession(t, map[string][]string{"dup": {"a", "b"}})
	pk := map[string]any{"id": float64(1)}

	if _, err := updateCellByPrimaryKey(ctx, s, "", "dup", pk, "c", 1.0); err == nil {
		t.Fatal("expected ambiguity error for update")
	}
	if _, err := insertRow(ctx, s, "", "missing", map[string]any{"c": 1.0}); err == nil {
		t.Fatal("expected missing-table error for insert")
	}
	if _, err := deleteRowByPrimaryKey(ctx, s, "public", "dup", pk); err == nil {
		t.Fatal("expected ambiguity error for delete")
	}
	if got := pgSchemaFake.lastExec(); got != "" {
		t.Fatalf("no write should be executed, got %q", got)
	}
}
