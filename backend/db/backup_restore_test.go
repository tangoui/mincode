package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const restoreFixtureSQL = `
CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL, price REAL, data BLOB, big INTEGER);
CREATE INDEX idx_items_name ON items(name);
INSERT INTO items VALUES (1, 'quote''; DROP', 1.5, X'00FF80', 9223372036854775807);
INSERT INTO items VALUES (2, '中文', NULL, NULL, -9223372036854775808);
`

func openSQLiteSession(t *testing.T, path string) *dbSession {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &dbSession{kind: kindSQL, dialect: "sqlite", db: db, defaultDatabase: "main"}
}

func restoreRows(t *testing.T, s *dbSession) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT id, name, quote(price), quote(data), big FROM items ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, big int64
		var name, price, data string
		if err := rows.Scan(&id, &name, &price, &data, &big); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d|%s|%s|%s|%d", id, name, price, data, big))
	}
	return strings.Join(out, "\n")
}

// backupFixture backs up a fixture SQLite database in the given format.
func backupFixture(t *testing.T, format string) (string, *dbSession) {
	t.Helper()
	dir := t.TempDir()
	source := openSQLiteSession(t, filepath.Join(dir, "source.db"))
	if _, err := source.db.Exec(restoreFixtureSQL); err != nil {
		t.Fatal(err)
	}
	job := BackupJob{ID: "restore-" + format, FileNamePrefix: "fixture", Format: format, Destination: dir,
		Targets: []BackupTarget{{Database: "main"}}, Gzip: format == "sql"}
	var result backupResult
	var err error
	if format == "archive" {
		result, err = writeBackupArchive(context.Background(), job, source, "sqlite", time.Now())
	} else {
		src, e := newSessionBackupSource(context.Background(), source)
		if e != nil {
			t.Fatal(e)
		}
		defer src.close()
		result, err = writeBackupFile(context.Background(), job, src, backupDumpMeta{DbType: "sqlite", StartedAt: time.Now()}, time.Now())
	}
	if err != nil {
		t.Fatal(err)
	}
	return result.File, source
}

func TestRestoreRoundTripSQLite(t *testing.T) {
	for _, format := range []string{"sql", "archive"} {
		t.Run(format, func(t *testing.T) {
			file, source := backupFixture(t, format)
			target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
			plan, err := planRestore(context.Background(), target, file)
			if err != nil || !plan.Supported {
				t.Fatalf("plan: %+v %v", plan, err)
			}
			if len(plan.Conflicts) != 0 {
				t.Fatalf("an empty target must have no conflicts: %v", plan.Conflicts)
			}
			var events int
			opts := RestoreOptions{AllowDestructive: plan.Destructive}
			result, err := runRestore(context.Background(), target, file, opts, func(RestoreProgress) { events++ })
			if err != nil {
				t.Fatalf("restore: %v", err)
			}
			if events == 0 {
				t.Fatal("no progress was reported")
			}
			if got, want := restoreRows(t, target), restoreRows(t, source); got != want {
				t.Fatalf("restored data differs\nsource:\n%s\ntarget:\n%s", want, got)
			}
			var index string
			if err := target.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_items_name'`).Scan(&index); err != nil {
				t.Fatalf("index not restored: %v (result %+v)", err, result)
			}
		})
	}
}

func TestRestoreRefusesExistingObjectsWithoutConsent(t *testing.T) {
	file, _ := backupFixture(t, "archive")
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	if _, err := target.db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, price REAL, data BLOB, big INTEGER); INSERT INTO items VALUES (99, 'keep', 0, NULL, 0)`); err != nil {
		t.Fatal(err)
	}
	plan, err := planRestore(context.Background(), target, file)
	if err != nil || len(plan.Conflicts) != 1 || plan.Conflicts[0] != "main.items" {
		t.Fatalf("conflict not detected: %+v %v", plan, err)
	}
	if _, err := runRestore(context.Background(), target, file, RestoreOptions{}, nil); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("existing objects must stop the restore, got %v", err)
	}
	var n int
	_ = target.db.QueryRow(`SELECT count(*) FROM items`).Scan(&n)
	if n != 1 {
		t.Fatalf("the refused restore wrote rows: %d", n)
	}
	// With consent, rows are added and existing data is kept (never dropped).
	if _, err := runRestore(context.Background(), target, file, RestoreOptions{AllowExisting: true}, nil); err != nil {
		t.Fatal(err)
	}
	_ = target.db.QueryRow(`SELECT count(*) FROM items`).Scan(&n)
	if n != 3 {
		t.Fatalf("want 1 kept + 2 restored rows, got %d", n)
	}
}

func TestRestoreSQLScriptNeedsConsentForDrops(t *testing.T) {
	file, _ := backupFixture(t, "sql")
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	plan, err := planRestore(context.Background(), target, file)
	if err != nil || !plan.Destructive || plan.Statements == 0 {
		t.Fatalf("MinCode SQL dumps hold DROP TABLE IF EXISTS: %+v %v", plan, err)
	}
	if _, err := runRestore(context.Background(), target, file, RestoreOptions{}, nil); err == nil {
		t.Fatal("a script with DROP ran without consent")
	}
}

func TestRestoreRejectsCorruptArchiveBeforeWriting(t *testing.T) {
	file, _ := backupFixture(t, "archive")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// Flip one byte inside a stored object: the checksum must catch it.
	corrupt := filepath.Join(t.TempDir(), "corrupt.zip")
	idx := strings.Index(string(data), "quote")
	if idx < 0 {
		t.Skip("object data is compressed; cannot flip a known byte")
	}
	data[idx] ^= 0x20
	if err := os.WriteFile(corrupt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	// Caught by the ZIP CRC or the manifest SHA256, whichever reads it first.
	if _, err := runRestore(context.Background(), target, corrupt, RestoreOptions{}, nil); err == nil ||
		!strings.Contains(err.Error(), "校验和不一致") && !strings.Contains(err.Error(), "checksum error") {
		t.Fatalf("a corrupt archive must fail verification, got %v", err)
	}
	var n int
	_ = target.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='items'`).Scan(&n)
	if n != 0 {
		t.Fatal("a corrupt archive created tables before failing verification")
	}
}

func TestRestoreCancellation(t *testing.T) {
	file, _ := backupFixture(t, "archive")
	target := openSQLiteSession(t, filepath.Join(t.TempDir(), "target.db"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runRestore(ctx, target, file, RestoreOptions{}, nil); err == nil || !strings.Contains(err.Error(), "取消") && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("a cancelled restore must report cancellation, got %v", err)
	}
}
