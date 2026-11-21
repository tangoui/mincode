package plugin

import (
	"database/sql"
	"path/filepath"
	"testing"

	backendstorage "mincode/app/backend/storage"
)

func useTemporaryPluginData(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	previousUserDataDir := userDataDir
	previousOpenPluginDB := openPluginDB
	userDataDir = func() string { return directory }

	db, err := sql.Open("sqlite", filepath.Join(directory, "plugin-test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE plugin_kv (
			plugin_id TEXT NOT NULL,
			key TEXT NOT NULL,
			value BLOB NOT NULL,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (plugin_id, key)
		)`,
		`CREATE TABLE plugin_secrets (
			plugin_id TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (plugin_id, key)
		)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	previousOpenPluginReadDB := openPluginReadDB
	openPluginDB = func() (*sql.DB, error) { return db, nil }
	openPluginReadDB = openPluginDB
	t.Cleanup(func() {
		openPluginReadDB = previousOpenPluginReadDB
		_ = db.Close()
		userDataDir = previousUserDataDir
		openPluginDB = previousOpenPluginDB
		_ = backendstorage.Close()
	})
	return directory
}
