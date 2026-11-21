package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSQLiteDatabaseTablesIncludeRowCountsAndSizes(t *testing.T) {
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, statement := range []string{
		`CREATE TABLE "order items" (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE INDEX order_items_name ON "order items"(name)`,
		`INSERT INTO "order items"(name) VALUES ('a'), ('b'), ('c')`,
		`CREATE TABLE empty (id INTEGER PRIMARY KEY)`,
		`CREATE VIEW recent AS SELECT * FROM "order items"`,
	} {
		if _, err := conn.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	tables, err := getDatabaseTables(context.Background(), &dbSession{kind: kindSQL, db: conn, dialect: "sqlite"}, "main")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]SchemaTable{}
	for _, table := range tables {
		byName[table.Name] = table
	}
	items := byName["order items"]
	if items.RowCount == nil || *items.RowCount != 3 || items.SizeBytes == nil || *items.SizeBytes <= 0 {
		t.Fatalf("order items stats = %+v", items)
	}
	if empty := byName["empty"]; empty.RowCount == nil || *empty.RowCount != 0 {
		t.Fatalf("empty table stats = %+v", empty)
	}
	if view := byName["recent"]; view.Kind != "view" || view.RowCount != nil || view.SizeBytes != nil {
		t.Fatalf("views must not report table statistics: %+v", view)
	}
}
