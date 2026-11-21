package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestConstrainedSchemaRoundTrip(t *testing.T) {
	source := openSQLiteSession(t, filepath.Join(t.TempDir(), "source.db"))
	if _, err := source.db.Exec(`CREATE TABLE "odd;name" (id INTEGER PRIMARY KEY, value TEXT NOT NULL DEFAULT 'safe'); CREATE INDEX idx_value ON "odd;name"(value)`); err != nil {
		t.Fatal(err)
	}
	schema, err := readRestoreSchema(context.Background(), source, "main", "odd;name")
	if err != nil {
		t.Fatal(err)
	}
	statements, err := schema.Statements("sqlite", "main", "copied;name")
	if err != nil {
		t.Fatal(err)
	}
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	for _, statement := range statements {
		if _, err := target.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := target.db.Exec(`INSERT INTO "copied;name"(id) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := target.db.QueryRow(`SELECT value FROM "copied;name"`).Scan(&value); err != nil || value != "safe" {
		t.Fatalf("literal default = %q %v", value, err)
	}
}
func TestConstrainedRejectsExecutableMetadata(t *testing.T) {
	schema := restoreSchema{Version: 2, Dialect: "sqlite", Columns: []restoreColumn{{Name: "id", Type: "INTEGER); DROP TABLE important;--"}}}
	if _, err := schema.Statements("sqlite", "main", "items"); err == nil {
		t.Fatal("executable type accepted")
	}
	schema.Columns[0].Type = "INTEGER"
	schema.Columns[0].Default = &restoreDefault{Kind: "expression", Value: "(SELECT 1)"}
	if _, err := schema.Statements("sqlite", "main", "items"); err == nil {
		t.Fatal("executable default accepted")
	}
}
func TestConstrainedDialectMismatch(t *testing.T) {
	schema := restoreSchema{Version: 2, Dialect: "sqlite", Columns: []restoreColumn{{Name: "id", Type: "INTEGER"}}}
	if _, err := schema.Statements("mysql", "target", "items"); err == nil {
		t.Fatal("cross dialect accepted")
	}
}
func TestConstrainedUnsupportedSchema(t *testing.T) {
	for _, ddl := range []string{
		`CREATE TABLE items(id INTEGER, computed INTEGER GENERATED ALWAYS AS (id+1))`,
		`CREATE TABLE items(id INTEGER CHECK(id>0))`,
		`CREATE TABLE items(id INTEGER REFERENCES parents(id))`,
		`CREATE TABLE items(id INTEGER); CREATE TRIGGER changed AFTER INSERT ON items BEGIN DELETE FROM items; END`,
	} {
		t.Run(ddl, func(t *testing.T) {
			source := openSQLiteSession(t, filepath.Join(t.TempDir(), "source.db"))
			if _, err := source.db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			if _, err := readRestoreSchema(context.Background(), source, "main", "items"); err == nil {
				t.Fatal("unsupported effects accepted")
			}
		})
	}
}
func TestConstrainedIdentifiers(t *testing.T) {
	schema := restoreSchema{Version: 2, Dialect: "mysql", Columns: []restoreColumn{{Name: "a`b", Type: "BIGINT", PrimaryKey: 1}}, Engine: "InnoDB", Charset: "utf8mb4", Collation: "utf8mb4_bin"}
	statements, err := schema.Statements("mysql", "safe`db", "table")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statements[0], "`safe``db`.`table`") || !strings.Contains(statements[0], "`a``b`") {
		t.Fatal(statements)
	}
}

func TestConstrainedArchiveCarriesStructuredSchema(t *testing.T) {
	path, _ := backupFixture(t, "archive")
	z, m, err := openRestoreArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	if m.Version != 2 {
		t.Fatalf("archive version = %d", m.Version)
	}
	found := false
	for _, e := range m.Metadata {
		for _, f := range z.File {
			if f.Name != e.Path {
				continue
			}
			var doc struct {
				Definition struct {
					Schema *restoreSchema `json:"schema"`
				}
			}
			if err := readZipJSON(f, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Definition.Schema != nil {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("missing structural schema")
	}
}

func TestConstrainedSQLiteScopeAndNullability(t *testing.T) {
	schema := restoreSchema{Version: 2, Dialect: "sqlite", Columns: []restoreColumn{{Name: "id", Type: "INTEGER", Nullable: false, PrimaryKey: 1}}}
	if _, err := schema.Statements("sqlite", "other", "items"); err == nil {
		t.Fatal("ignored SQLite target namespace")
	}
}

func TestMySQLTriggerVisibilityRequired(t *testing.T) {
	for _, grants := range [][]string{{"GRANT SELECT ON *.* TO `reader`@`%`"}, {"GRANT TRIGGER ON `other`.* TO `reader`@`%`"}} {
		if mysqlTriggerVisible(grants, "target") {
			t.Fatal("missing visibility treated as absence")
		}
	}
	for _, grants := range [][]string{{"GRANT ALL PRIVILEGES ON *.* TO `reader`@`%`"}, {"GRANT SELECT, TRIGGER ON `target`.* TO `reader`@`%`"}} {
		if !mysqlTriggerVisible(grants, "target") {
			t.Fatal("explicit visibility refused")
		}
	}
}
