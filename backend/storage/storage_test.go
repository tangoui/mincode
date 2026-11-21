package storage

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenConfiguresSchemaWALAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "namespace", databaseFileName)
	database, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
	var journalMode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var busyTimeout int
	if err := database.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}
	var synchronous int
	if err := database.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 1 {
		t.Fatalf("synchronous = %d, want NORMAL (1)", synchronous)
	}
	for _, name := range []string{"app_kv", "plugin_kv", "plugin_secrets", "claude_session_titles"} {
		var count int
		if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", name, count)
		}
	}
	var storageMigrationsCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'storage_migrations'").Scan(&storageMigrationsCount); err != nil {
		t.Fatal(err)
	}
	if storageMigrationsCount != 0 {
		t.Fatalf("storage_migrations table count = %d, want 0 (retired table must not exist)", storageMigrationsCount)
	}
	var pluginPrimaryKeyColumns int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('plugin_kv')
		WHERE name IN ('plugin_id', 'key') AND pk > 0`).Scan(&pluginPrimaryKeyColumns); err != nil {
		t.Fatal(err)
	}
	if pluginPrimaryKeyColumns != 2 {
		t.Fatalf("plugin_kv primary key columns = %d, want 2", pluginPrimaryKeyColumns)
	}
	for _, table := range []string{"app_kv", "plugin_kv"} {
		var updatedAtColumns int
		if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'updated_at'`, table).Scan(&updatedAtColumns); err != nil {
			t.Fatal(err)
		}
		if updatedAtColumns != 1 {
			t.Fatalf("%s updated_at columns = %d, want 1", table, updatedAtColumns)
		}
	}
	var claudeTitlePrimaryKeyColumns int
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('claude_session_titles')
		WHERE name IN ('encoded_dir', 'session_uuid') AND pk > 0`).Scan(&claudeTitlePrimaryKeyColumns); err != nil {
		t.Fatal(err)
	}
	if claudeTitlePrimaryKeyColumns != 2 {
		t.Fatalf("claude_session_titles primary key columns = %d, want 2", claudeTitlePrimaryKeyColumns)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database permissions = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory permissions = %o, want 700", dirInfo.Mode().Perm())
	}
}

// TestOpenIsIdempotentAcrossReopens 验证重复打开同一个库不会重建表或丢数据，
// 且已存在的旧结构（比如手工建的 storage_migrations 遗留表）会在下次打开时被清理掉。
func TestOpenIsIdempotentAcrossReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.sqlite3")
	database, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO app_kv(key, value) VALUES('probe', '1')"); err != nil {
		t.Fatal(err)
	}
	// 模拟历史遗留的 storage_migrations 表：确认它会在下次打开时被清理，而不会阻塞打开。
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS storage_migrations (
		migration_id TEXT PRIMARY KEY NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	var value string
	if err := database.QueryRow("SELECT value FROM app_kv WHERE key = 'probe'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "1" {
		t.Fatalf("probe value = %q, want 1 (reopen must not lose existing data)", value)
	}
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'storage_migrations'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("storage_migrations table survived a reopen")
	}
}

func TestDBIsSingletonAndCloseAllowsReopen(t *testing.T) {
	originalUserDataDir := userDataDir
	userDataDir = func() string { return t.TempDir() }
	t.Cleanup(func() {
		_ = Close()
		userDataDir = originalUserDataDir
	})

	first, err := DB()
	if err != nil {
		t.Fatal(err)
	}
	second, err := DB()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("DB() did not return the shared connection")
	}
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	third, err := DB()
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("DB() reused the closed connection")
	}
}

func TestArchiveLegacyJSONStoreCreatesRecoverableBackup(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "app.store.json")
	original := []byte(`{"app_settings":{"uiTheme":"dark"}}`)
	if err := os.WriteFile(sourcePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := archiveLegacyJSONStore(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Fatalf("legacy source still exists or stat failed: %v", err)
	}
	archivePath := sourcePath + ".legacy-backup.gz"
	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	reader, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("restored legacy data = %q, want %q", restored, original)
	}
	if err := archiveLegacyJSONStore(directory); err != nil {
		t.Fatalf("archive must be idempotent: %v", err)
	}
}

func TestArchiveLegacyJSONStorePreservesSourceWhenBackupCannotBeCreated(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "app.store.json")
	original := []byte(`{"app_settings":{"uiTheme":"dark"}}`)
	if err := os.WriteFile(sourcePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := sourcePath + ".legacy-backup.gz"
	temporaryPath := archivePath + ".tmp"
	if err := os.Mkdir(temporaryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := archiveLegacyJSONStore(directory); err == nil {
		t.Fatal("预期备份创建失败")
	}
	preserved, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(preserved) != string(original) {
		t.Fatalf("备份失败后原数据发生变化: %q", preserved)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("备份失败后不应有已提交的归档: %v", err)
	}
	// 失败路径也必须关闭源句柄，Windows 才能重命名以便恢复。
	if err := os.Rename(sourcePath, sourcePath+".recovered"); err != nil {
		t.Fatalf("备份失败后源文件仍被占用: %v", err)
	}
}

func TestArchiveLegacyJSONStoreKeepsExistingBackupAndSource(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "app.store.json")
	archivePath := sourcePath + ".legacy-backup.gz"
	original := []byte(`{"app_settings":{"uiTheme":"dark"}}`)
	existingBackup := []byte("existing backup")
	for name, content := range map[string][]byte{sourcePath: original, archivePath: existingBackup} {
		if err := os.WriteFile(name, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := archiveLegacyJSONStore(directory); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{sourcePath: original, archivePath: existingBackup} {
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("现有数据不应被归档覆盖: %s = %q", name, got)
		}
	}
}

func TestOpenCreatesNormalizedAIStorageTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "namespace", databaseFileName)
	database, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, table := range []string{
		"ai_workspaces",
		"ai_conversations",
		"ai_conversation_items",
		"ai_attachments",
		"ai_attachment_refs",
	} {
		var count int
		if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("normalized table %s count = %d, want 1", table, count)
		}
	}
	var foreignKeys int
	if err := database.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
}

func TestOpenCreatesNotesTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "namespace", databaseFileName)
	database, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, object := range []struct{ kind, name string }{
		{"table", "note_folders"},
		{"table", "notes"},
		{"table", "note_tags"},
		{"table", "note_tag_links"},
		{"table", "note_links"},
		{"table", "note_assets"},
		{"index", "idx_notes_folder"},
		{"index", "idx_note_folders_parent"},
		{"index", "idx_note_links_object"},
		{"index", "idx_note_tag_links_tag"},
	} {
		var count int
		if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?", object.kind, object.name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("notes %s %s count = %d, want 1", object.kind, object.name, count)
		}
	}
}

func TestOpenAddsAttachmentRefsHashIndexToExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.sqlite3")
	database, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟加索引之前创建的数据库：user_version 已是当前版本，但没有该索引。
	if _, err := database.Exec("DROP INDEX idx_ai_attachment_refs_hash"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_ai_attachment_refs_hash' AND tbl_name = 'ai_attachment_refs'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("idx_ai_attachment_refs_hash count = %d, want 1", count)
	}
}

func TestReadDBIsQueryOnlyAndSeesCommittedWrites(t *testing.T) {
	dataDir := t.TempDir()
	originalUserDataDir := userDataDir
	userDataDir = func() string { return dataDir }
	t.Cleanup(func() {
		_ = Close()
		userDataDir = originalUserDataDir
	})

	writer, err := DB()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := ReadDB()
	if err != nil {
		t.Fatal(err)
	}
	if reader == writer {
		t.Fatal("ReadDB() returned the writer pool")
	}
	if _, err := writer.Exec("INSERT INTO app_kv(key, value) VALUES('k', '1')"); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := reader.QueryRow("SELECT value FROM app_kv WHERE key = 'k'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "1" {
		t.Fatalf("reader value = %q, want 1", value)
	}
	if _, err := reader.Exec("DELETE FROM app_kv"); err == nil {
		t.Fatal("read pool accepted a write")
	}
	if err := Maintain(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := ReadDB()
	if err != nil {
		t.Fatal(err)
	}
	if reopened == reader {
		t.Fatal("Maintain() did not release the read pool")
	}
	if err := reopened.QueryRow("SELECT value FROM app_kv WHERE key = 'k'").Scan(&value); err != nil {
		t.Fatal(err)
	}
}

func TestTransferSchemaPreservesLegacyBackupData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.sqlite")
	db, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"db_backup_jobs_v1", "db_backup_history_v1"} {
		if _, err := db.Exec("INSERT INTO app_kv(key,value) VALUES(?,?)", key, `[{"id":"legacy"}]`); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"transfer_tasks", "transfer_preparations", "transfer_resource_locks", "transfer_effects"} {
		if _, err := db.Exec("SELECT * FROM " + table + " LIMIT 0"); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"db_backup_jobs_v1", "db_backup_history_v1"} {
		var got string
		if err := db.QueryRow("SELECT value FROM app_kv WHERE key=?", key).Scan(&got); err != nil || got != `[{"id":"legacy"}]` {
			t.Fatalf("legacy %s = %s, %v", key, got, err)
		}
	}
}
