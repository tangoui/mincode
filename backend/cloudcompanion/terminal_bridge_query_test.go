package cloudcompanion

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCloudKeyPrefixQueriesUsePrimaryKeyIndex(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "kv.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE app_kv (key TEXT PRIMARY KEY NOT NULL, value BLOB NOT NULL, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{loadCloudBindingsQuery, cleanupCloudOperationsQuery} {
		rows, err := db.Query("EXPLAIN QUERY PLAN " + query)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		joined := strings.Join(plan, "; ")
		if !strings.Contains(joined, "USING INDEX sqlite_autoindex_app_kv_1 (key>? AND key<?)") {
			t.Fatalf("query %q plan = %q, want primary key range search", query, joined)
		}
	}
}

func TestCloudKeyPrefixQueriesMatchOnlyTheirPrefix(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "kv.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE app_kv (key TEXT PRIMARY KEY NOT NULL, value BLOB NOT NULL, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cloud-terminal:a", "cloud-terminal:z~", "cloud-terminal", "cloud-terminal;x", "cloud-terminals:x", "cloud-operation:old", "cloud-operation:new", "cloud-operation;x"} {
		if _, err := db.Exec("INSERT INTO app_kv(key, value, updated_at) VALUES(?, ?, datetime('now', '-30 days'))", key, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("UPDATE app_kv SET updated_at = CURRENT_TIMESTAMP WHERE key = 'cloud-operation:new'"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(loadCloudBindingsQuery)
	if err != nil {
		t.Fatal(err)
	}
	var loaded []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		loaded = append(loaded, value)
	}
	rows.Close()
	if strings.Join(loaded, ",") != "cloud-terminal:a,cloud-terminal:z~" {
		t.Fatalf("loaded bindings = %v", loaded)
	}
	if _, err := db.Exec(cleanupCloudOperationsQuery); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRow("SELECT COUNT(*) FROM app_kv WHERE key IN ('cloud-operation:new', 'cloud-operation;x')").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	var old int
	if err := db.QueryRow("SELECT COUNT(*) FROM app_kv WHERE key = 'cloud-operation:old'").Scan(&old); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 || old != 0 {
		t.Fatalf("after cleanup remaining=%d old=%d, want 2 and 0", remaining, old)
	}
}

func TestAfterStartupWaitsForBackgroundStartup(t *testing.T) {
	pending := make(chan struct{})
	startupMu.Lock()
	previous := startupPending
	startupPending = pending
	startupMu.Unlock()
	t.Cleanup(func() {
		startupMu.Lock()
		startupPending = previous
		startupMu.Unlock()
	})
	called := make(chan struct{})
	handler := afterStartup(func(_, _ string) (string, error) {
		close(called)
		return "ok", nil
	})
	go func() { _, _ = handler("cloud_status", "{}") }()
	select {
	case <-called:
		t.Fatal("handler ran before startup finished")
	case <-time.After(30 * time.Millisecond):
	}
	close(pending)
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("handler did not run after startup finished")
	}
}
