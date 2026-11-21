// Package storage 提供后端功能域共享的应用 SQLite 数据库。
package storage

import (
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"

	_ "modernc.org/sqlite"
)

const appDataDirectory = core.AppDataNamespace

const (
	databaseFileName = "mincode.sqlite3"
	schemaVersion    = 1
)

const (
	// 完整性校验要读全库，退出时只在距上次成功校验超过该间隔或 WAL 偏大时执行。
	integrityCheckInterval   = 7 * 24 * time.Hour
	integrityCheckWALTrigger = 64 << 20
	integrityMarkerSuffix    = ".integrity-checked"
)

var (
	mu          sync.Mutex
	db          *sql.DB
	openErr     error
	readDB      *sql.DB
	userDataDir = core.UserDataDir
)

// readPoolSize 只读连接池大小。WAL 下读不阻塞写，单写连接 + 多读连接可避免
// 读请求排在长事务写入之后。
const readPoolSize = 4

// Path 返回当前构建命名空间使用的数据库路径。
func Path() (string, error) {
	root := userDataDir()
	if root == "" {
		return "", errors.New("应用数据目录不可用")
	}
	return filepath.Join(root, appDataDirectory, databaseFileName), nil
}

// DB 惰性打开并返回进程内共享的 SQLite 连接池。
func DB() (*sql.DB, error) {
	mu.Lock()
	defer mu.Unlock()
	if db != nil || openErr != nil {
		return db, openErr
	}
	path, err := Path()
	if err != nil {
		return nil, err
	}
	db, openErr = open(path)
	return db, openErr
}

// ReadDB 返回只读连接池（query_only），供纯查询使用；写入、事务仍须使用 DB()。
// 只读池在写连接完成迁移后惰性打开，打开失败时回退到写连接以保持可用。
func ReadDB() (*sql.DB, error) {
	writer, err := DB()
	if err != nil {
		return nil, err
	}
	mu.Lock()
	defer mu.Unlock()
	if db != writer {
		// 并发 Close 之后不再返回旧连接池派生的只读池。
		return nil, errors.New("共享存储已关闭")
	}
	if readDB != nil {
		return readDB, nil
	}
	path, err := Path()
	if err != nil {
		return writer, nil
	}
	reader, err := openReader(path)
	if err != nil {
		log.Printf("storage: open read pool failed, falling back to writer: %v", err)
		return writer, nil
	}
	readDB = reader
	return readDB, nil
}

func openReader(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=query_only(1)", filepath.ToSlash(path))
	opened, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	opened.SetMaxOpenConns(readPoolSize)
	opened.SetMaxIdleConns(readPoolSize)
	opened.SetConnMaxIdleTime(5 * time.Minute)
	if err := opened.Ping(); err != nil {
		_ = opened.Close()
		return nil, err
	}
	return opened, nil
}

// closeReaderLocked 关闭只读池；调用方须持有 mu。
func closeReaderLocked() error {
	if readDB == nil {
		return nil
	}
	err := readDB.Close()
	readDB = nil
	return err
}

func open(path string) (*sql.DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建存储目录: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("设置存储目录权限: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", filepath.ToSlash(path))
	opened, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开共享存储: %w", err)
	}
	opened.SetMaxOpenConns(1)
	if err := opened.Ping(); err != nil {
		_ = opened.Close()
		return nil, fmt.Errorf("连接共享存储: %w", err)
	}
	if err := migrate(opened); err != nil {
		_ = opened.Close()
		return nil, err
	}
	if err := restrictDatabaseFiles(path); err != nil {
		_ = opened.Close()
		return nil, err
	}
	return opened, nil
}

func restrictDatabaseFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(candidate, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("设置存储文件权限 %s: %w", filepath.Base(candidate), err)
		}
	}
	return nil
}

// schemaStatements 直接描述最终期望的表结构（幂等、可重复执行）。
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS transfer_tasks (
		id TEXT PRIMARY KEY NOT NULL,
		kind TEXT NOT NULL,
		idempotency_key TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		workspace_id TEXT NOT NULL DEFAULT '',
		job_id TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		revision INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		request BLOB NOT NULL,
		payload BLOB NOT NULL,
		snapshot BLOB NOT NULL,
		UNIQUE(kind, idempotency_key)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_transfer_tasks_created ON transfer_tasks(created_at, id)`,
	`CREATE INDEX IF NOT EXISTS idx_transfer_tasks_job ON transfer_tasks(job_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS transfer_preparations (
		id TEXT PRIMARY KEY NOT NULL,
		kind TEXT NOT NULL,
		fingerprint TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		payload BLOB NOT NULL,
		consumed_by TEXT REFERENCES transfer_tasks(id)
	)`,
	`CREATE TABLE IF NOT EXISTS transfer_resource_locks (
		resource TEXT PRIMARY KEY NOT NULL,
		task_id TEXT NOT NULL REFERENCES transfer_tasks(id)
	)`,
	`CREATE TABLE IF NOT EXISTS transfer_effects (
		id TEXT PRIMARY KEY NOT NULL,
		task_id TEXT NOT NULL REFERENCES transfer_tasks(id),
		kind TEXT NOT NULL,
		outcome BLOB,
		created_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_transfer_effects_task ON transfer_effects(task_id)`,
	`CREATE TABLE IF NOT EXISTS app_kv (
		key TEXT PRIMARY KEY NOT NULL,
		value BLOB NOT NULL,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS plugin_kv (
		plugin_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value BLOB NOT NULL,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (plugin_id, key)
	)`,
	`CREATE TABLE IF NOT EXISTS plugin_secrets (
		plugin_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (plugin_id, key)
	)`,
	`CREATE TABLE IF NOT EXISTS claude_session_titles (
		encoded_dir TEXT NOT NULL,
		session_uuid TEXT NOT NULL,
		title TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (encoded_dir, session_uuid)
	)`,
	`CREATE TABLE IF NOT EXISTS ai_workspaces (
		scope_id TEXT PRIMARY KEY NOT NULL,
		active_conversation_id TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS ai_conversations (
		scope_id TEXT NOT NULL,
		conversation_id TEXT NOT NULL,
		position INTEGER NOT NULL,
		payload BLOB NOT NULL,
		payload_hash BLOB NOT NULL,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (scope_id, conversation_id),
		FOREIGN KEY (scope_id) REFERENCES ai_workspaces(scope_id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_conversations_scope_position
		ON ai_conversations(scope_id, position)`,
	`CREATE TABLE IF NOT EXISTS ai_conversation_items (
		scope_id TEXT NOT NULL,
		conversation_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		position INTEGER NOT NULL,
		payload BLOB NOT NULL,
		payload_hash BLOB NOT NULL,
		PRIMARY KEY (scope_id, conversation_id, kind, position),
		FOREIGN KEY (scope_id, conversation_id)
			REFERENCES ai_conversations(scope_id, conversation_id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS ai_attachments (
		hash TEXT PRIMARY KEY NOT NULL,
		kind TEXT NOT NULL,
		mime_type TEXT NOT NULL,
		encoding TEXT NOT NULL,
		data BLOB NOT NULL,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS ai_attachment_refs (
		scope_id TEXT NOT NULL,
		conversation_id TEXT NOT NULL,
		attachment_hash TEXT NOT NULL,
		PRIMARY KEY (scope_id, conversation_id, attachment_hash),
		FOREIGN KEY (scope_id, conversation_id)
			REFERENCES ai_conversations(scope_id, conversation_id) ON DELETE CASCADE,
		FOREIGN KEY (attachment_hash) REFERENCES ai_attachments(hash) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ai_attachment_refs_hash
		ON ai_attachment_refs(attachment_hash)`,
	// 内置笔记（backend/notes）：全局数据，不按工作区隔离；时间为 Unix 毫秒。
	// 删除文件夹经外键级联删除子文件夹与其中笔记；删除笔记级联删除标签与对象关联。
	`CREATE TABLE IF NOT EXISTS note_folders (
		id TEXT PRIMARY KEY NOT NULL,
		parent_id TEXT NULL REFERENCES note_folders(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		position REAL NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_note_folders_parent
		ON note_folders(parent_id, position)`,
	`CREATE TABLE IF NOT EXISTS notes (
		id TEXT PRIMARY KEY NOT NULL,
		folder_id TEXT NULL REFERENCES note_folders(id) ON DELETE CASCADE,
		title TEXT NOT NULL,
		body TEXT NOT NULL,
		favorite INTEGER NOT NULL DEFAULT 0,
		position REAL NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_notes_folder
		ON notes(folder_id, position)`,
	`CREATE TABLE IF NOT EXISTS note_tags (
		id TEXT PRIMARY KEY NOT NULL,
		name TEXT NOT NULL UNIQUE COLLATE NOCASE
	)`,
	`CREATE TABLE IF NOT EXISTS note_tag_links (
		note_id TEXT NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
		tag_id TEXT NOT NULL REFERENCES note_tags(id) ON DELETE CASCADE,
		PRIMARY KEY (note_id, tag_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_note_tag_links_tag
		ON note_tag_links(tag_id)`,
	`CREATE TABLE IF NOT EXISTS note_links (
		note_id TEXT NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
		object_type TEXT NOT NULL,
		object_id TEXT NOT NULL,
		parent_id TEXT NOT NULL DEFAULT '',
		scope_id TEXT NOT NULL DEFAULT '',
		name_snapshot TEXT NOT NULL,
		scope_name_snapshot TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		PRIMARY KEY (note_id, object_type, object_id, scope_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_note_links_object
		ON note_links(object_type, object_id)`,
	`CREATE TABLE IF NOT EXISTS note_assets (
		hash TEXT PRIMARY KEY NOT NULL,
		ext TEXT NOT NULL,
		size INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	)`,
	// storage_migrations 是已退役的一次性 JSON→SQLite 迁移记录表，不再需要。
	`DROP TABLE IF EXISTS storage_migrations`,
}

// schemaObjectPattern 从 schemaStatements 提取应存在的表/索引名，供打开时快速判断是否需要跑 DDL。
var schemaObjectPattern = regexp.MustCompile(`(?i)^\s*CREATE\s+(TABLE|INDEX)\s+IF\s+NOT\s+EXISTS\s+([A-Za-z0-9_]+)`)

// schemaComplete 用一次 sqlite_master 查询确认 user_version 为当前版本、所有表/索引齐全
// 且遗留表已清除；为真时跳过整批 DDL 与写事务。历史上曾在不升 schemaVersion 的情况下
// 追加表和索引（如 plugin_secrets、idx_ai_attachment_refs_hash），因此不能只看 user_version。
func schemaComplete(opened *sql.DB) (bool, error) {
	var version int
	if err := opened.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return false, fmt.Errorf("读取存储 schema 版本: %w", err)
	}
	if version != schemaVersion {
		return false, nil
	}
	rows, err := opened.Query("SELECT type, name FROM sqlite_master WHERE type IN ('table', 'index')")
	if err != nil {
		return false, fmt.Errorf("读取存储 schema: %w", err)
	}
	defer rows.Close()
	existing := make(map[string]struct{})
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			return false, fmt.Errorf("读取存储 schema: %w", err)
		}
		existing[kind+":"+name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("读取存储 schema: %w", err)
	}
	if _, ok := existing["table:storage_migrations"]; ok {
		return false, nil
	}
	for _, statement := range schemaStatements {
		match := schemaObjectPattern.FindStringSubmatch(statement)
		if match == nil {
			continue
		}
		if _, ok := existing[strings.ToLower(match[1])+":"+match[2]]; !ok {
			return false, nil
		}
	}
	return true, nil
}

// migrate 确保共享存储持有当前 schema 需要的全部表结构。
//
// 只有 MinCode 自己一个用户，因此这里不维护历史版本迁移链：schemaStatements 直接描述
// 最终期望的表结构，旧版本遗留的 storage_migrations 表按需清除。
func migrate(opened *sql.DB) error {
	if complete, err := schemaComplete(opened); err != nil {
		return err
	} else if complete {
		return nil
	}
	tx, err := opened.Begin()
	if err != nil {
		return fmt.Errorf("开始存储 schema 迁移: %w", err)
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("读取存储 schema 版本: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("存储 schema 版本 %d 高于当前支持版本 %d", version, schemaVersion)
	}

	for _, statement := range schemaStatements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("应用存储 schema: %w", err)
		}
	}
	if version != schemaVersion {
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("更新存储 schema 版本: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交存储 schema 迁移: %w", err)
	}
	return nil
}

// Maintain checkpoints the WAL and rebuilds the database only when a meaningful
// amount of free space exists. It is intended for orderly application shutdown;
// the full integrity check only runs when due, and all work honours ctx.
func Maintain(ctx context.Context) error {
	mu.Lock()
	defer mu.Unlock()
	if db == nil {
		return nil
	}
	// checkpoint(TRUNCATE)/VACUUM 需要没有其它读连接；只读池下次使用时重新打开。
	if err := closeReaderLocked(); err != nil {
		return fmt.Errorf("关闭共享存储只读池: %w", err)
	}
	path, err := Path()
	if err != nil {
		return err
	}
	now := time.Now()
	verified := false
	if integrityCheckDue(path, now) {
		if err := integrityCheck(ctx, db); err != nil {
			return err
		}
		verified = true
		recordIntegrityCheck(path, now)
	}
	var busy, logFrames, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint 共享存储: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("checkpoint 共享存储被占用: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointed)
	}
	var pageCount, freelistCount int
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return fmt.Errorf("读取共享存储页数: %w", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freelistCount); err != nil {
		return fmt.Errorf("读取共享存储空闲页: %w", err)
	}
	// 小库不值得在退出时重建；大库仅在至少 8 MiB 且空闲页达到 20% 时回收。
	if pageCount >= 2048 && freelistCount >= 1024 && freelistCount*5 >= pageCount {
		// VACUUM 只在确认完好的库上执行。
		if !verified {
			if err := integrityCheck(ctx, db); err != nil {
				return err
			}
			recordIntegrityCheck(path, now)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("回收共享存储空闲页: %w", err)
		}
		backupPath := path + ".pre-vacuum-backup"
		if err := copyFile(path, backupPath, 0o600); err != nil {
			return fmt.Errorf("备份共享存储: %w", err)
		}
		if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
			return fmt.Errorf("回收共享存储空闲页（备份保留在 %s）: %w", backupPath, err)
		}
		if err := integrityCheck(ctx, db); err != nil {
			return fmt.Errorf("回收后校验共享存储（备份保留在 %s）: %w", backupPath, err)
		}
		if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("清理共享存储临时备份: %w", err)
		}
	}
	if err := archiveLegacyJSONStore(filepath.Dir(path)); err != nil {
		return err
	}
	return nil
}

// integrityCheckDue 在从未校验、上次成功校验已超过间隔或 WAL 偏大（可能有异常写入）时为真。
func integrityCheckDue(path string, now time.Time) bool {
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() >= integrityCheckWALTrigger {
		return true
	}
	info, err := os.Stat(path + integrityMarkerSuffix)
	if err != nil {
		return true
	}
	age := now.Sub(info.ModTime())
	return age < 0 || age >= integrityCheckInterval
}

// recordIntegrityCheck 以标记文件的 mtime 记录最近一次成功校验；写失败只会让下次退出再校验一次。
func recordIntegrityCheck(path string, now time.Time) {
	marker := path + integrityMarkerSuffix
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return
	}
	_ = os.Chtimes(marker, now, now)
}

var integrityCheck = verifyIntegrity

func verifyIntegrity(ctx context.Context, database *sql.DB) error {
	var result string
	if err := database.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("校验共享存储: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("共享存储完整性校验失败: %s", result)
	}
	return nil
}

func copyFile(sourcePath, destinationPath string, mode os.FileMode) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	completed := false
	defer func() {
		_ = destination.Close()
		if !completed {
			_ = os.Remove(destinationPath)
		}
	}()
	if _, err := io.Copy(destination, source); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	completed = true
	return nil
}

func archiveLegacyJSONStore(directory string) error {
	sourcePath := filepath.Join(directory, "app.store.json")
	archivePath := sourcePath + ".legacy-backup.gz"
	if _, err := os.Stat(archivePath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查旧版 JSON 备份: %w", err)
	}
	source, err := os.Open(sourcePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("打开旧版 JSON 存储: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return fmt.Errorf("读取旧版 JSON 存储信息: %w", err)
	}
	if info.Size() <= 2 {
		return nil
	}
	temporaryPath := archivePath + ".tmp"
	archive, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建旧版 JSON 备份: %w", err)
	}
	writer := gzip.NewWriter(archive)
	if _, err := io.Copy(writer, source); err != nil {
		writer.Close()
		archive.Close()
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("压缩旧版 JSON 存储: %w", err)
	}
	if err := writer.Close(); err != nil {
		archive.Close()
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("完成旧版 JSON 压缩: %w", err)
	}
	if err := archive.Sync(); err != nil {
		archive.Close()
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("同步旧版 JSON 备份: %w", err)
	}
	if err := archive.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("关闭旧版 JSON 备份: %w", err)
	}
	// Windows 不允许删除仍打开的源文件；仅在备份同步并关闭成功后释放源句柄。
	if err := source.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("关闭旧版 JSON 存储: %w", err)
	}
	if err := os.Rename(temporaryPath, archivePath); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("提交旧版 JSON 备份: %w", err)
	}
	if err := os.Remove(sourcePath); err != nil {
		return fmt.Errorf("移除已归档的旧版 JSON 存储: %w", err)
	}
	return nil
}

// Close 在所有使用存储的功能域停止后关闭共享连接；可重复调用。
func Close() error {
	mu.Lock()
	defer mu.Unlock()
	readErr := closeReaderLocked()
	if db == nil {
		openErr = nil
		return nil
	}
	err := db.Close()
	db = nil
	openErr = nil
	if err != nil {
		return fmt.Errorf("关闭共享存储: %w", err)
	}
	if readErr != nil {
		return fmt.Errorf("关闭共享存储只读池: %w", readErr)
	}
	return nil
}
