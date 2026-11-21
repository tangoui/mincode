package sessions

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// openOpencodeFixture creates <dir>/opencode.db in WAL mode and returns a writer
// that stays open: like a running OpenCode, the newest rows then live only in
// the -wal file, which an immutable reader would not see.
func openOpencodeFixture(t *testing.T, schema ...string) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	setOpencodeRoot(t, dir)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "opencode.db")))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range append([]string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0"}, schema...) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	return db
}

func setOpencodeRoot(t *testing.T, dir string) {
	t.Helper()
	resetSessionRootOverrides(t)
	if _, err := setAiSessionRootsHandler("", `{"roots":{"opencode":"`+filepath.ToSlash(dir)+`"}}`); err != nil {
		t.Fatal(err)
	}
}

// OpenCode 2.x (session_v2 / session_message), with the rows still in the WAL.
func TestOpencodeV2SessionsInWAL(t *testing.T) {
	db := openOpencodeFixture(t,
		"CREATE TABLE session_v2 (id text PRIMARY KEY, project_id text NOT NULL, parent_id text, directory text NOT NULL, title text, model text, time_updated integer NOT NULL, time_archived integer)",
		"CREATE TABLE session_message (id text PRIMARY KEY, session_id text NOT NULL, type text NOT NULL, seq integer NOT NULL, time_created integer NOT NULL, data text NOT NULL)",
	)
	for _, statement := range []string{
		`INSERT INTO session_v2 VALUES ('ses_new','p',NULL,'/work/app','你好问候','{"id":"big-pickle","providerID":"opencode"}',2000,NULL)`,
		`INSERT INTO session_v2 VALUES ('ses_old','p',NULL,'/work/app','',NULL,1000,NULL)`,
		`INSERT INTO session_v2 VALUES ('ses_child','p','ses_new','/work/app','subagent',NULL,3000,NULL)`,
		`INSERT INTO session_v2 VALUES ('ses_archived','p',NULL,'/work/app','gone',NULL,4000,5000)`,
		`INSERT INTO session_message VALUES ('m1','ses_new','user',4,10,'{"text":"你好","files":[]}')`,
		`INSERT INTO session_message VALUES ('m2','ses_new','assistant',7,11,'{"model":{"id":"big-pickle"},"content":[{"type":"text","text":"你好！"},{"type":"reasoning","text":"thinking"}]}')`,
		`INSERT INTO session_message VALUES ('m3','ses_new','idle',12,12,'{"outcome":"succeeded"}')`,
		`INSERT INTO session_message VALUES ('m4','ses_old','user',1,5,'{"text":"still waiting"}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if info, err := os.Stat(filepath.Join(filepath.Dir(mustOpencodeDB(t)), "opencode.db-wal")); err != nil || info.Size() == 0 {
		t.Fatalf("fixture rows are not in the WAL: %v", err)
	}

	sessions := listOpencodeSessions()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v", sessions)
	}
	got := sessions[0]
	if got.ID != "ses_new" || got.Title != "你好问候" || got.Cwd != "/work/app" || got.LastAt != 2000 ||
		got.MessageCount != 2 || got.Model != "big-pickle" || got.Status != "completed" || got.FilePath != "opencode://ses_new" {
		t.Fatalf("newest session = %+v", got)
	}
	if sessions[1].ID != "ses_old" || sessions[1].Status != "in_progress" || sessions[1].Title == "" {
		t.Fatalf("older session = %+v", sessions[1])
	}

	messages := opencodeMessages("ses_new", 50)
	if len(messages) != 2 || messages[0].Role != "user" || messages[0].Content != "你好" ||
		messages[1].Role != "assistant" || messages[1].Content != "你好！" || messages[1].Model == nil || *messages[1].Model != "big-pickle" {
		t.Fatalf("messages = %+v", messages)
	}
}

// OpenCode 1.x (session / message / part) keeps working.
func TestOpencodeLegacySessions(t *testing.T) {
	db := openOpencodeFixture(t,
		"CREATE TABLE session (id text PRIMARY KEY, parent_id text, title text, directory text, time_updated integer, model text)",
		"CREATE TABLE message (id text PRIMARY KEY, session_id text, role text, time_created integer, data text)",
		"CREATE TABLE part (id text PRIMARY KEY, message_id text, data text)",
	)
	for _, statement := range []string{
		`INSERT INTO session VALUES ('s1',NULL,'legacy','/work',100,'{"id":"gpt"}')`,
		`INSERT INTO message VALUES ('m1','s1','user',1,'{"role":"user"}')`,
		`INSERT INTO part VALUES ('p1','m1','{"text":"hi"}')`,
		`INSERT INTO message VALUES ('m2','s1','assistant',2,'{"role":"assistant","modelID":"gpt"}')`,
		`INSERT INTO part VALUES ('p2','m2','{"text":"hello"}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	sessions := listOpencodeSessions()
	if len(sessions) != 1 || sessions[0].ID != "s1" || sessions[0].Status != "completed" || sessions[0].MessageCount != 2 || sessions[0].Model != "gpt" {
		t.Fatalf("sessions = %+v", sessions)
	}
	if messages := opencodeMessages("s1", 50); len(messages) != 2 || messages[1].Content != "hello" {
		t.Fatalf("messages = %+v", messages)
	}
}

func mustOpencodeDB(t *testing.T) string {
	t.Helper()
	path, ok := opencodeDBPath()
	if !ok {
		t.Fatal("opencode db not found")
	}
	return path
}
