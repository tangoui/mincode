package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func stubSchemaConnections(t *testing.T, raw string) {
	t.Helper()
	previous := loadSchemaSavedConnections
	t.Cleanup(func() { loadSchemaSavedConnections = previous })
	loadSchemaSavedConnections = func() ([]schemaSavedConnection, error) {
		var connections []schemaSavedConnection
		err := json.Unmarshal([]byte(raw), &connections)
		return connections, err
	}
}

func TestListSchemaConnectionsFiltersAndHidesCredentials(t *testing.T) {
	stubSchemaConnections(t, `[
		{"id":"a","name":"Prod MySQL","dbType":"mysql","host":"10.0.0.1","username":"root","password":"enc:v2:x","database":"shop"},
		{"id":"b","name":"PG","dbType":"PostgreSQL","host":"h"},
		{"id":"c","name":"Cache","dbType":"redis","host":"h"},
		{"id":"d","name":"Tunnel","dbType":"mysql","tunnel":{"host":"jump"}},
		{"id":"e","name":"Plugin","dbType":"plugin","driver":{"pluginId":"x","driverId":"y"}},
		{"id":"","name":"No id","dbType":"mysql"}
	]`)
	connections, err := ListSchemaConnections()
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 3 || connections[0].ID != "a" || connections[1].ID != "b" || connections[1].Kind != "postgres" || connections[2].ID != "e" {
		t.Fatalf("connections = %+v", connections)
	}
	encoded, _ := json.Marshal(connections)
	for _, secret := range []string{"10.0.0.1", "root", "enc:v2", "password", "host"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("connection list leaks %q: %s", secret, encoded)
		}
	}
}

func TestSchemaAccessReadsSQLiteMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	handle, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT NOT NULL, nickname TEXT DEFAULT 'guest')`,
		`CREATE UNIQUE INDEX idx_users_email ON users(email)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id), total REAL)`,
	} {
		if _, err := handle.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	_ = handle.Close()
	encodedPath, _ := json.Marshal(path)
	stubSchemaConnections(t, `[{"id":"lite","name":"Local","dbType":"sqlite","filePath":`+string(encodedPath)+`}]`)

	listing, err := ListSchemaTables(context.Background(), "lite", "")
	if err != nil {
		t.Fatal(err)
	}
	if listing.Database == "" || len(listing.Tables) != 2 || listing.Tables[0].Name != "orders" || listing.Tables[1].Name != "users" {
		t.Fatalf("listing = %+v", listing)
	}
	tables, err := DescribeSchemaTables(context.Background(), "lite", listing.Database, []string{"users", "orders", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 3 {
		t.Fatalf("tables = %+v", tables)
	}
	users := tables[0]
	if users.Error != "" || len(users.Columns) != 3 || !users.Columns[0].PrimaryKey || users.Columns[1].Nullable || len(users.PrimaryKey) != 1 {
		t.Fatalf("users = %+v", users)
	}
	foundUnique := false
	for _, index := range users.Indexes {
		foundUnique = foundUnique || (index.Unique && strings.Join(index.Columns, ",") == "email")
	}
	if !foundUnique {
		t.Fatalf("users indexes = %+v", users.Indexes)
	}
	if orders := tables[1]; len(orders.ForeignKeys) != 1 || orders.ForeignKeys[0].ReferencedTable != "users" {
		t.Fatalf("orders = %+v", orders)
	}
	if missing := tables[2]; missing.Error == "" && len(missing.Columns) != 0 {
		t.Fatalf("missing table = %+v", missing)
	}
}

func TestSchemaAccessRejectsIneligibleConnections(t *testing.T) {
	stubSchemaConnections(t, `[{"id":"r","dbType":"redis"},{"id":"t","dbType":"mysql","tunnel":{}}]`)
	for _, id := range []string{"r", "t", "nope"} {
		if _, err := ListSchemaTables(context.Background(), id, ""); !errors.Is(err, ErrSchemaConnectionNotFound) {
			t.Fatalf("%s: err = %v", id, err)
		}
	}
	if _, err := DescribeSchemaTables(context.Background(), "r", "main", nil); err == nil {
		t.Fatal("empty table list must be rejected")
	}
}

func TestSchemaAccessReadsPluginDriverConnections(t *testing.T) {
	stubSchemaConnections(t, `[
		{"id":"duck","name":"Duck","dbType":"duckdb","driver":{"pluginId":"acme.db","driverId":"acme"}},
		{"id":"generic","name":"Generic","dbType":"plugin","driver":{"pluginId":"acme.db","driverId":"acme"}},
		{"id":"broken","name":"Broken","dbType":"duckdb","driver":{"pluginId":"","driverId":"acme"}}
	]`)
	connections, err := ListSchemaConnections()
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 2 || connections[0].ID != "duck" || connections[1].ID != "generic" {
		t.Fatalf("connections = %+v", connections)
	}

	conn := newFakeExternalConn(externalFeatureMetadataTables, externalFeatureMetadataColumns, externalFeatureMetadataPKs)
	conn.info.Dialect = "duckdb"
	conn.metadata[ExternalMetadataNavigation] = `{"databases":[{"name":"main","tables":[{"name":"users","kind":"table","comment":"People"}]}]}`
	conn.metadata[ExternalMetadataColumns] = `[{"name":"id","dataType":"INTEGER","nullable":false}]`
	conn.metadata[ExternalMetadataPrimaryKeys] = `{"columns":["id"]}`
	genericConn := newFakeExternalConn(externalFeatureMetadataColumns)
	genericConn.info.Dialect = "generic"
	provider := &fakeExternalProvider{conns: []*fakeExternalConn{conn, conn, genericConn}}
	withExternalProvider(t, provider)

	listing, err := ListSchemaTables(context.Background(), "duck", "")
	if err != nil {
		t.Fatal(err)
	}
	if listing.Database != "main" || len(listing.Tables) != 1 || listing.Tables[0].Comment != "People" {
		t.Fatalf("listing = %+v", listing)
	}
	tables, err := DescribeSchemaTables(context.Background(), "duck", "main", []string{"users"})
	if err != nil {
		t.Fatal(err)
	}
	// Indexes and foreign keys are optional: the driver does not declare them.
	if len(tables) != 1 || tables[0].Error != "" || len(tables[0].Columns) != 1 || len(tables[0].PrimaryKey) != 1 {
		t.Fatalf("tables = %+v", tables)
	}
	if req := provider.reqs[0]; req.ConnectionID != "duck" || req.Ref.PluginID != "acme.db" {
		t.Fatalf("schema access must connect through the bound driver: %+v", req)
	}
	// A generic-dialect driver has no relational schema guarantee.
	if _, err := ListSchemaTables(context.Background(), "generic", ""); !errors.Is(err, ErrSchemaConnectionNotFound) {
		t.Fatalf("generic driver err = %v", err)
	}
}
