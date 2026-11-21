package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectFileDatabaseRecognizesSQLiteHeader(t *testing.T) {
	path := writeDatabaseHeader(t, "shared.db", []byte(sqliteHeader))
	info, err := detectFileDatabase(path)
	if err != nil {
		t.Fatalf("detectFileDatabase() error = %v", err)
	}
	if info.DbType != "sqlite" || info.FilePath != path || info.FileName != "shared.db" || !info.BuiltinDriver {
		t.Fatalf("detectFileDatabase() = %#v", info)
	}
}

// DuckDB files are recognised in every profile; builds without the built-in
// driver report BuiltinDriver=false so the UI can offer the driver plugin.
func TestDetectFileDatabaseRecognizesDuckDBHeader(t *testing.T) {
	header := make([]byte, 16)
	copy(header[8:12], duckDBMagic)
	path := writeDatabaseHeader(t, "shared.db", header)
	info, err := detectFileDatabase(path)
	if err != nil {
		t.Fatalf("detectFileDatabase() error = %v", err)
	}
	if info.DbType != "duckdb" {
		t.Fatalf("DbType = %q, want duckdb", info.DbType)
	}
	if info.BuiltinDriver != databaseTypeSupported("duckdb") {
		t.Fatalf("BuiltinDriver = %v, want %v", info.BuiltinDriver, databaseTypeSupported("duckdb"))
	}
}

func TestDetectFileDatabaseRejectsUnknownFile(t *testing.T) {
	path := writeDatabaseHeader(t, "notes.db", []byte("not a database"))
	_, err := detectFileDatabase(path)
	if err == nil || !strings.Contains(err.Error(), "无法识别") {
		t.Fatalf("error = %v, want unrecognized database error", err)
	}
}

func writeDatabaseHeader(t *testing.T, name string, header []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, header, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
