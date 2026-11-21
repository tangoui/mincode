package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func writePiSessionFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "2026-01-01T00-00-00-000Z_session-id.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParsePiSessionUsesActiveBranchAndSessionName(t *testing.T) {
	path := writePiSessionFixture(t, `{"type":"session","version":3,"id":"session-id","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp/project"}
{"type":"message","id":"user0001","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"Initial request"}],"timestamp":1}}
{"type":"message","id":"assistant1","parentId":"user0001","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"First answer"}],"model":"model-a","timestamp":2}}
{"type":"message","id":"unused01","parentId":"user0001","timestamp":"2026-01-01T00:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"Abandoned branch"}],"model":"model-old","timestamp":3}}
{"type":"session_info","id":"name0001","parentId":"assistant1","timestamp":"2026-01-01T00:00:04Z","name":"Named Pi task"}
{"type":"model_change","id":"model001","parentId":"name0001","timestamp":"2026-01-01T00:00:05Z","provider":"openai","modelId":"model-b"}
`)

	session := parsePiSession(path)
	if session == nil {
		t.Fatal("expected Pi session metadata")
	}
	if session.ID != "session-id" || session.Title != "Named Pi task" || session.Cwd != "/tmp/project" {
		t.Fatalf("unexpected session metadata: %#v", session)
	}
	if session.Model != "model-b" || session.MessageCount != 2 || session.Status != "completed" {
		t.Fatalf("unexpected model/message status: %#v", session)
	}
	messages := piMessagesFromJsonl(path, 200)
	if len(messages) != 2 || messages[1].Content != "First answer" {
		t.Fatalf("expected only active-branch messages, got %#v", messages)
	}
}

func TestParsePiSessionFallsBackToFirstUserMessageTitle(t *testing.T) {
	path := writePiSessionFixture(t, `{"type":"session","version":3,"id":"fallback-id","timestamp":"2026-01-01T00:00:00Z","cwd":"/tmp/project"}
{"type":"message","id":"user0001","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"Investigate the failing build. Then fix it.","timestamp":1}}
`)
	session := parsePiSession(path)
	if session == nil || session.Title != "Investigate the failing build." || session.Status != "in_progress" {
		t.Fatalf("unexpected fallback metadata: %#v", session)
	}
}
