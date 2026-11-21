package storage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setMaintainTestDB(t *testing.T) (string, *int) {
	t.Helper()
	root := t.TempDir()
	originalUserDataDir := userDataDir
	originalIntegrityCheck := integrityCheck
	userDataDir = func() string { return root }
	checks := 0
	integrityCheck = func(ctx context.Context, database *sql.DB) error {
		checks++
		return verifyIntegrity(ctx, database)
	}
	t.Cleanup(func() {
		_ = Close()
		userDataDir = originalUserDataDir
		integrityCheck = originalIntegrityCheck
	})
	if _, err := DB(); err != nil {
		t.Fatal(err)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	return path, &checks
}

func TestMaintainRunsIntegrityCheckOnlyWhenDue(t *testing.T) {
	path, checks := setMaintainTestDB(t)
	ctx := context.Background()

	if err := Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if *checks != 1 {
		t.Fatalf("first maintain checks = %d, want 1", *checks)
	}
	if _, err := os.Stat(path + integrityMarkerSuffix); err != nil {
		t.Fatalf("integrity marker missing: %v", err)
	}
	if err := Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if *checks != 1 {
		t.Fatalf("maintain within interval checks = %d, want 1", *checks)
	}

	stale := time.Now().Add(-integrityCheckInterval - time.Hour)
	if err := os.Chtimes(path+integrityMarkerSuffix, stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if *checks != 2 {
		t.Fatalf("maintain after interval checks = %d, want 2", *checks)
	}
}

func TestIntegrityCheckDueWhenWALIsLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), databaseFileName)
	now := time.Now()
	recordIntegrityCheck(path, now)
	if integrityCheckDue(path, now) {
		t.Fatal("fresh marker should suppress the check")
	}
	wal, err := os.Create(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if err := wal.Truncate(integrityCheckWALTrigger); err != nil {
		t.Fatal(err)
	}
	wal.Close()
	if !integrityCheckDue(path, now) {
		t.Fatal("large WAL should force the check")
	}
}

func TestMaintainStillCheckpointsWAL(t *testing.T) {
	path, _ := setMaintainTestDB(t)
	database, err := DB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO app_kv(key, value) VALUES('probe', '1')"); err != nil {
		t.Fatal(err)
	}
	if err := Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("WAL size after maintain = %d, want 0", info.Size())
	}
}

func TestMaintainHonoursCancelledContext(t *testing.T) {
	setMaintainTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Maintain(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Maintain with cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestSchemaCompleteDetectsMissingObjects(t *testing.T) {
	for _, statement := range schemaStatements {
		if strings.HasPrefix(strings.TrimSpace(statement), "CREATE") && schemaObjectPattern.FindStringSubmatch(statement) == nil {
			t.Fatalf("schema statement is invisible to schemaComplete: %s", statement)
		}
	}
	path := filepath.Join(t.TempDir(), databaseFileName)
	database, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	complete, err := schemaComplete(database)
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("freshly migrated schema reported incomplete")
	}
	if _, err := database.Exec("DROP TABLE plugin_secrets"); err != nil {
		t.Fatal(err)
	}
	if complete, err = schemaComplete(database); err != nil || complete {
		t.Fatalf("schema without plugin_secrets complete=%v err=%v, want false", complete, err)
	}
	if err := migrate(database); err != nil {
		t.Fatal(err)
	}
	if complete, err = schemaComplete(database); err != nil || !complete {
		t.Fatalf("after migrate complete=%v err=%v, want true", complete, err)
	}
	if _, err := database.Exec("CREATE TABLE storage_migrations (id TEXT)"); err != nil {
		t.Fatal(err)
	}
	if complete, _ = schemaComplete(database); complete {
		t.Fatal("legacy storage_migrations table must force migration")
	}
}
