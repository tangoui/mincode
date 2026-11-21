package sessions

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

const (
	testEncodedDir  = "host:workspace"
	testSessionUUID = "123e4567-e89b-12d3-a456-426614174000"
)

func TestClaudeTitleRepositoryUpsertListDeleteAndErrors(t *testing.T) {
	repository, _ := setTestClaudeTitles(t)
	if err := repository.upsert(testEncodedDir, testSessionUUID, "first"); err != nil {
		t.Fatal(err)
	}
	if err := repository.upsert(testEncodedDir, testSessionUUID, "second"); err != nil {
		t.Fatal(err)
	}
	titles, err := repository.list(testEncodedDir)
	if err != nil {
		t.Fatal(err)
	}
	if titles[testSessionUUID] != "second" || len(titles) != 1 {
		t.Fatalf("titles = %#v", titles)
	}
	if err := repository.delete(testEncodedDir, testSessionUUID); err != nil {
		t.Fatal(err)
	}
	titles, err = repository.list(testEncodedDir)
	if err != nil || len(titles) != 0 {
		t.Fatalf("titles after delete = %#v, err=%v", titles, err)
	}

	broken := claudeTitleRepository{openDB: func() (*sql.DB, error) { return nil, errors.New("storage unavailable") }}
	if _, err := broken.list(testEncodedDir); err == nil {
		t.Fatal("list silently treated storage error as empty")
	}
	if err := broken.upsert(testEncodedDir, testSessionUUID, "title"); err == nil {
		t.Fatal("upsert silently ignored storage error")
	}
}

func TestClaudeSessionListAndRenameSurfaceStorageErrors(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", testEncodedDir)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, testSessionUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	originalRepository := claudeTitles
	claudeTitles = claudeTitleRepository{openDB: func() (*sql.DB, error) { return nil, errors.New("storage unavailable") }}
	t.Cleanup(func() { claudeTitles = originalRepository })

	if _, err := getProjectSessionsHandler("", `{"encodedDir":"`+testEncodedDir+`"}`); err == nil {
		t.Fatal("list silently treated storage error as empty")
	}
	if _, err := renameClaudeSessionHandler("", `{"encodedDir":"`+testEncodedDir+`","uuid":"`+testSessionUUID+`","title":"title"}`); err == nil {
		t.Fatal("rename silently ignored storage error")
	}
}

func TestClaudeSessionHandlersUseRepositoryAndPreserveTranscript(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", testEncodedDir)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(projectDir, testSessionUUID+".jsonl")
	transcript := []byte("{\"type\":\"user\",\"message\":{\"content\":\"original\"}}\n")
	if err := os.WriteFile(transcriptPath, transcript, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	repository, db := setTestClaudeTitles(t)
	originalRepository := claudeTitles
	claudeTitles = repository
	t.Cleanup(func() { claudeTitles = originalRepository })

	renameArgs := `{"encodedDir":"` + testEncodedDir + `","uuid":"` + testSessionUUID + `","title":"  renamed  "}`
	if _, err := renameClaudeSessionHandler("", renameArgs); err != nil {
		t.Fatal(err)
	}
	if current, err := os.ReadFile(transcriptPath); err != nil || !bytes.Equal(current, transcript) {
		t.Fatalf("rename changed transcript: %q, err=%v", current, err)
	}
	listed, err := getProjectSessionsHandler("", `{"encodedDir":"`+testEncodedDir+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var sessions []claudeSessionMeta
	if err := json.Unmarshal([]byte(listed), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Title != "renamed" {
		t.Fatalf("sessions = %#v", sessions)
	}
	if _, err := deleteClaudeSessionHandler("", `{"encodedDir":"`+testEncodedDir+`","uuid":"`+testSessionUUID+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(transcriptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transcript still exists: %v", err)
	}
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM claude_session_titles").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("title rows = %d, err=%v", rows, err)
	}
}

func TestRenameClaudeSessionFailsWhenDatabaseWriteFails(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", testEncodedDir)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, testSessionUUID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	repository, db := setTestClaudeTitles(t)
	if _, err := db.Exec(`CREATE TRIGGER reject_claude_title_insert
		BEFORE INSERT ON claude_session_titles
		BEGIN SELECT RAISE(FAIL, 'write rejected'); END`); err != nil {
		t.Fatal(err)
	}
	originalRepository := claudeTitles
	claudeTitles = repository
	t.Cleanup(func() { claudeTitles = originalRepository })

	if _, err := renameClaudeSessionHandler("", `{"encodedDir":"`+testEncodedDir+`","uuid":"`+testSessionUUID+`","title":"title"}`); err == nil {
		t.Fatal("rename silently ignored database write failure")
	}
}

func TestDeleteClaudeSessionCleanupIsBestEffort(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", testEncodedDir)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(projectDir, testSessionUUID+".jsonl")
	if err := os.WriteFile(transcriptPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	originalRepository := claudeTitles
	claudeTitles = claudeTitleRepository{openDB: func() (*sql.DB, error) { return nil, errors.New("cleanup failed") }}
	t.Cleanup(func() { claudeTitles = originalRepository })

	if _, err := deleteClaudeSessionHandler("", `{"encodedDir":"`+testEncodedDir+`","uuid":"`+testSessionUUID+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(transcriptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transcript still exists: %v", err)
	}
}

func setTestClaudeTitles(t testing.TB) (claudeTitleRepository, *sql.DB) {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "titles.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE claude_session_titles (
		encoded_dir TEXT NOT NULL,
		session_uuid TEXT NOT NULL,
		title TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (encoded_dir, session_uuid)
	)`); err != nil {
		t.Fatal(err)
	}
	repository := claudeTitleRepository{
		openDB: func() (*sql.DB, error) { return db, nil },
	}
	t.Cleanup(func() { _ = db.Close() })
	return repository, db
}
