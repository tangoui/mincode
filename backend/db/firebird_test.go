//go:build !lite

package db

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestBuildFirebirdDSN(t *testing.T) {
	username := "SYS DBA"
	password := "p@ss:#/?&=% word"
	database := "/var/lib/firebird/data file.fdb"
	dsn, err := buildFirebirdDSN(&DbConnectionConfig{
		Host:     "2001:db8::1",
		Username: &username,
		Password: &password,
		Database: &database,
		SSL:      true,
	})
	if err != nil {
		t.Fatalf("buildFirebirdDSN() error = %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Scheme != "firebird" || parsed.Host != "[2001:db8::1]:3050" {
		t.Fatalf("unexpected endpoint: %s://%s", parsed.Scheme, parsed.Host)
	}
	if got := parsed.User.Username(); got != username {
		t.Fatalf("username = %q, want %q", got, username)
	}
	if got, _ := parsed.User.Password(); got != password {
		t.Fatalf("password = %q, want %q", got, password)
	}
	if parsed.Path != database {
		t.Fatalf("path = %q, want %q", parsed.Path, database)
	}
	if parsed.Query().Get("charset") != "UTF8" || parsed.Query().Get("wire_crypt") != "true" {
		t.Fatalf("unexpected query: %s", parsed.RawQuery)
	}
	if strings.Contains(dsn, password) {
		t.Fatalf("password was not percent-encoded: %s", dsn)
	}
}

func TestBuildFirebirdDSNDefaults(t *testing.T) {
	database := "MY_DATABASE"
	dsn, err := buildFirebirdDSN(&DbConnectionConfig{Database: &database})
	if err != nil {
		t.Fatalf("buildFirebirdDSN() error = %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Host != "localhost:3050" || parsed.Path != "/MY_DATABASE" {
		t.Fatalf("unexpected default DSN: %s", dsn)
	}
	if parsed.User.Username() != "SYSDBA" || parsed.Query().Get("wire_crypt") != "false" {
		t.Fatalf("unexpected default credentials/options: %s", dsn)
	}
}

func TestFirebirdTableSQL(t *testing.T) {
	query, err := selectAllSQL("firebird", "/ignored/database.fdb", `order"items`, 25, 50)
	if err != nil {
		t.Fatalf("selectAllSQL() error = %v", err)
	}
	want := `SELECT * FROM "order""items" ROWS 51 TO 75`
	if query != want {
		t.Fatalf("query = %q, want %q", query, want)
	}

	qualified, err := qualifiedTable("firebird", "/ignored/database.fdb", `order"items`)
	if err != nil {
		t.Fatalf("qualifiedTable() error = %v", err)
	}
	if qualified != `"order""items"` {
		t.Fatalf("qualified table = %q", qualified)
	}
}

func TestFirebirdColumnType(t *testing.T) {
	tests := []struct {
		name       string
		fieldType  int64
		subtype    sql.NullInt64
		length     sql.NullInt64
		charLength sql.NullInt64
		precision  sql.NullInt64
		scale      sql.NullInt64
		want       string
	}{
		{name: "varchar", fieldType: 37, length: validFirebirdInt(40), charLength: validFirebirdInt(10), want: "VARCHAR(10)"},
		{name: "decimal", fieldType: 16, subtype: validFirebirdInt(2), precision: validFirebirdInt(18), scale: validFirebirdInt(-2), want: "DECIMAL(18,2)"},
		{name: "text blob", fieldType: 261, subtype: validFirebirdInt(1), want: "BLOB SUB_TYPE TEXT"},
		{name: "timezone timestamp", fieldType: 29, want: "TIMESTAMP WITH TIME ZONE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := firebirdColumnType(
				test.fieldType, test.subtype, test.length, test.charLength, test.precision, test.scale,
			)
			if got != test.want {
				t.Fatalf("firebirdColumnType() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFirebirdTriggerTimingEvent(t *testing.T) {
	timing, event := firebirdTriggerTimingEvent(113)
	if timing != "BEFORE" || event != "INSERT OR UPDATE OR DELETE" {
		t.Fatalf("got (%q, %q)", timing, event)
	}
}

func validFirebirdInt(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}

func TestFirebirdIntegration(t *testing.T) {
	database := os.Getenv("MINCODE_FIREBIRD_TEST_DATABASE")
	if database == "" {
		t.Skip("MINCODE_FIREBIRD_TEST_DATABASE is not set")
	}
	port := 3050
	if value := os.Getenv("MINCODE_FIREBIRD_TEST_PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("invalid MINCODE_FIREBIRD_TEST_PORT: %v", err)
		}
		port = parsed
	}
	username := os.Getenv("MINCODE_FIREBIRD_TEST_USERNAME")
	if username == "" {
		username = "SYSDBA"
	}
	password := os.Getenv("MINCODE_FIREBIRD_TEST_PASSWORD")
	cfg := &DbConnectionConfig{
		DbType:   "firebird",
		Host:     os.Getenv("MINCODE_FIREBIRD_TEST_HOST"),
		Port:     port,
		Username: &username,
		Password: &password,
		Database: &database,
		SSL:      true,
	}
	session, err := buildFirebirdSession(cfg)
	if err != nil {
		t.Fatalf("buildFirebirdSession() error = %v", err)
	}
	defer session.close()
	ctx := context.Background()

	_, _ = session.db.ExecContext(ctx, "DROP TABLE MINCODE_FB_CHILD")
	_, _ = session.db.ExecContext(ctx, "DROP TABLE MINCODE_FB_PARENT")
	defer func() {
		_, _ = session.db.ExecContext(ctx, "DROP TABLE MINCODE_FB_CHILD")
		_, _ = session.db.ExecContext(ctx, "DROP TABLE MINCODE_FB_PARENT")
	}()

	statements := []string{
		`CREATE TABLE MINCODE_FB_PARENT (
			ID BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			NAME VARCHAR(100) NOT NULL
		)`,
		`CREATE TABLE MINCODE_FB_CHILD (
			ID BIGINT NOT NULL PRIMARY KEY,
			PARENT_ID BIGINT,
			UPDATED_AT TIMESTAMP,
			CONSTRAINT MINCODE_FB_CHILD_FK FOREIGN KEY (PARENT_ID) REFERENCES MINCODE_FB_PARENT (ID)
		)`,
		`CREATE INDEX MINCODE_FB_CHILD_PARENT_IDX ON MINCODE_FB_CHILD (PARENT_ID)`,
		`CREATE TRIGGER MINCODE_FB_CHILD_BU FOR MINCODE_FB_CHILD
			ACTIVE BEFORE UPDATE POSITION 0
			AS BEGIN NEW.UPDATED_AT = CURRENT_TIMESTAMP; END`,
	}
	for _, statement := range statements {
		if _, err := session.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("execute setup statement %q: %v", statement, err)
		}
	}

	schema, err := firebirdSchema(ctx, session)
	if err != nil {
		t.Fatalf("firebirdSchema() error = %v", err)
	}
	if len(schema.Databases) != 1 || !containsSchemaTable(schema.Databases[0].Tables, "MINCODE_FB_PARENT") {
		t.Fatalf("schema does not contain integration table: %#v", schema)
	}
	columns, err := firebirdColumns(ctx, session, "MINCODE_FB_PARENT")
	if err != nil {
		t.Fatalf("firebirdColumns() error = %v", err)
	}
	if len(columns) != 2 || !columns[0].IsPrimaryKey || !columns[0].AutoIncrement {
		t.Fatalf("unexpected columns: %#v", columns)
	}
	foreignKeys, err := firebirdForeignKeys(ctx, session, "MINCODE_FB_CHILD")
	if err != nil || len(foreignKeys) != 1 || foreignKeys[0].ReferencedTable != "MINCODE_FB_PARENT" {
		t.Fatalf("unexpected foreign keys: %#v, error = %v", foreignKeys, err)
	}
	indexes, err := firebirdIndexes(ctx, session, "MINCODE_FB_CHILD")
	if err != nil || !containsFirebirdIndex(indexes, "MINCODE_FB_CHILD_PARENT_IDX") {
		t.Fatalf("unexpected indexes: %#v, error = %v", indexes, err)
	}
	triggers, err := firebirdTriggers(ctx, session, "MINCODE_FB_CHILD")
	if err != nil || len(triggers) != 1 || triggers[0].Timing != "BEFORE" || triggers[0].Event != "UPDATE" {
		t.Fatalf("unexpected triggers: %#v, error = %v", triggers, err)
	}
	ddl, err := firebirdTableDDL(ctx, session, "MINCODE_FB_PARENT")
	if err != nil || !strings.Contains(ddl, `GENERATED BY DEFAULT AS IDENTITY`) {
		t.Fatalf("unexpected DDL: %q, error = %v", ddl, err)
	}

	if _, err := insertRow(ctx, session, database, "MINCODE_FB_PARENT", map[string]any{"NAME": "first"}); err != nil {
		t.Fatalf("insertRow() error = %v", err)
	}
	rows, err := getTableRows(ctx, session, database, "MINCODE_FB_PARENT", 10, 0)
	if err != nil || rows.RowCount != 1 {
		t.Fatalf("unexpected rows: %#v, error = %v", rows, err)
	}
	if _, err := updateCellByPrimaryKey(ctx, session, database, "MINCODE_FB_PARENT", map[string]any{"ID": 1}, "NAME", "updated"); err != nil {
		t.Fatalf("updateCellByPrimaryKey() error = %v", err)
	}
	if _, err := deleteRowByPrimaryKey(ctx, session, database, "MINCODE_FB_PARENT", map[string]any{"ID": 1}); err != nil {
		t.Fatalf("deleteRowByPrimaryKey() error = %v", err)
	}
}

func containsSchemaTable(tables []SchemaTable, name string) bool {
	for _, table := range tables {
		if table.Name == name {
			return true
		}
	}
	return false
}

func containsFirebirdIndex(indexes []DbIndexInfo, name string) bool {
	for _, index := range indexes {
		if index.Name == name {
			return true
		}
	}
	return false
}
