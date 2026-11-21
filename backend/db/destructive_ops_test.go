package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteDestructiveOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "destructive.sqlite")
	session, err := buildSession(&DbConnectionConfig{DbType: "sqlite", FilePath: &path})
	if err != nil {
		t.Fatalf("build SQLite session: %v", err)
	}
	defer session.close()
	ctx := context.Background()
	for _, statement := range []string{
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE parents (id INTEGER PRIMARY KEY AUTOINCREMENT)`,
		`CREATE TABLE children (id INTEGER PRIMARY KEY AUTOINCREMENT, parent_id INTEGER REFERENCES parents(id))`,
		`INSERT INTO parents DEFAULT VALUES`,
		`INSERT INTO children(parent_id) VALUES (1)`,
	} {
		if _, err := session.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("execute %q: %v", statement, err)
		}
	}
	if err := executeDestructiveOperation(ctx, session, "clear_database", "main", nil, "main"); err != nil {
		t.Fatalf("clear SQLite database: %v", err)
	}
	for _, table := range []string{"parents", "children"} {
		var count int
		if err := session.db.QueryRowContext(ctx, `SELECT count(*) FROM "`+table+`"`).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s still has %d rows", table, count)
		}
	}
	if err := executeDestructiveOperation(ctx, session, "drop_database", "main", nil, "main"); err == nil {
		t.Fatal("expected SQLite drop database to be rejected")
	}
}

func TestDestructiveOperationCapabilities(t *testing.T) {
	tests := []struct {
		dialect string
		action  string
		want    bool
	}{
		{"duckdb", "clear_table", true},
		{"duckdb", "clear_database", true},
		{"sqlite", "drop_database", false},
		{"sqlserver", "clear_table", true},
		{"oracle", "drop_table", true},
		{"dm", "clear_table", true},
		{"clickhouse", "drop_table", true},
		{"trino", "clear_table", false},
		{"tdengine", "drop_table", false},
	}
	for _, test := range tests {
		if got := supportsDestructiveOperation(test.dialect, test.action); got != test.want {
			t.Errorf("supportsDestructiveOperation(%q, %q) = %v, want %v", test.dialect, test.action, got, test.want)
		}
	}
}

func TestSQLServerMutationSyntax(t *testing.T) {
	if got := quoteColumn("sqlserver", "odd]name"); got != "[odd]]name]" {
		t.Fatalf("quoteColumn(sqlserver) = %q", got)
	}
	if got := placeholder("sqlserver", 2); got != "@p2" {
		t.Fatalf("placeholder(sqlserver) = %q", got)
	}
}

func TestClickHouseQualifiedTable(t *testing.T) {
	tests := []struct {
		database string
		table    string
		want     string
	}{
		{database: "analytics", table: "events", want: "`analytics`.`events`"},
		{database: "", table: "events", want: "`events`"},
		{database: "odd`db", table: "odd`table", want: "`odd``db`.`odd``table`"},
	}
	for _, test := range tests {
		got, err := qualifiedTable("clickhouse", test.database, test.table)
		if err != nil {
			t.Fatalf("qualifiedTable(clickhouse): %v", err)
		}
		if got != test.want {
			t.Errorf("qualifiedTable(clickhouse, %q, %q) = %q, want %q", test.database, test.table, got, test.want)
		}
	}
}
