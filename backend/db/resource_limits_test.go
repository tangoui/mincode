package db

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestEstimateDBValueBytesIncludesJSONAndContainerOverhead(t *testing.T) {
	value := map[string]any{
		"items": []any{
			map[string]any{"name": "alpha", "enabled": true},
			map[string]any{"name": "beta", "enabled": false},
		},
	}
	if got := estimateDBValueBytes(value); got < 160 {
		t.Fatalf("container estimate is too small: %d", got)
	}
	if got := estimateDBValueBytes([]byte("payload")); got <= len("payload") {
		t.Fatalf("byte slice estimate must include encoded/container overhead: %d", got)
	}
}

func TestDatabaseResultDefaultsAreConservative(t *testing.T) {
	if dbMaxResultRows != 5000 {
		t.Fatalf("dbMaxResultRows = %d, want 5000", dbMaxResultRows)
	}
	if dbMaxResultBytes != 16*1024*1024 {
		t.Fatalf("dbMaxResultBytes = %d, want 16 MiB", dbMaxResultBytes)
	}
}

func TestConfigureRemoteSQLPool(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	configureRemoteSQLPool(db)
	if got := db.Stats().MaxOpenConnections; got != 6 {
		t.Fatalf("MaxOpenConnections = %d, want 6", got)
	}
	if remoteSQLMaxIdle != 2 || remoteSQLConnMaxIdleTime != 10*time.Minute || remoteSQLConnMaxLifetime != time.Hour {
		t.Fatalf("unexpected pool defaults: idle=%d idleTime=%s lifetime=%s", remoteSQLMaxIdle, remoteSQLConnMaxIdleTime, remoteSQLConnMaxLifetime)
	}
}
