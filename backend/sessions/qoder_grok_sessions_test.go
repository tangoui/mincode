package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCliTranscript(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseQoderCNJsonlReadsConversationMetadata(t *testing.T) {
	path := writeCliTranscript(t, "session.jsonl", `
{"type":"session_meta","sessionId":"qoder-session","timestamp":"2026-08-19T08:29:11Z","cwd":"/tmp/qoder"}
{"type":"user","sessionId":"qoder-session","timestamp":"2026-08-19T08:29:12Z","message":{"role":"user","content":"Fix the session reader"}}
{"type":"assistant","sessionId":"qoder-session","timestamp":"2026-08-19T08:29:13Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"I will fix it."}]}}
`)

	session := parseQoderCNJsonl(path)
	if session == nil {
		t.Fatal("expected session")
	}
	if session.ID != "qoder-session" || session.Cwd != "/tmp/qoder" {
		t.Fatalf("unexpected session metadata: %#v", session)
	}
	if session.Title != "Fix the session reader" || session.MessageCount != 2 {
		t.Fatalf("unexpected session title/count: %#v", session)
	}

	messages := qoderCNMessagesFromJsonl(path, 200)
	if len(messages) != 2 || messages[1].Content != "I will fix it." {
		t.Fatalf("unexpected parsed messages: %#v", messages)
	}
}

func TestGrokMessagesSkipsSystemAndReasoningRecords(t *testing.T) {
	path := writeCliTranscript(t, "chat_history.jsonl", `
{"type":"system","content":"system prompt"}
{"type":"user","content":[{"type":"text","text":"Review this change"}]}
{"type":"reasoning","summary":[{"type":"summary_text","text":"internal"}]}
{"type":"assistant","content":[{"type":"text","text":"Here is the review."}]}
`)

	messages := grokMessagesFromJsonl(path, 200)
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2: %#v", len(messages), messages)
	}
	if messages[0].Role != "user" || messages[0].Content != "Review this change" {
		t.Fatalf("unexpected user message: %#v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "Here is the review." {
		t.Fatalf("unexpected assistant message: %#v", messages[1])
	}
}

func TestParseGrokSummaryReadsSessionMetadata(t *testing.T) {
	path := writeCliTranscript(t, "summary.json", `{"info":{"id":"grok-session","cwd":"/tmp/grok"},"generated_title":"Inspect repository","updated_at":"2026-08-17T03:28:15.257540Z","num_chat_messages":6,"current_model_id":"grok-4.6"}`)
	session := parseGrokSummary(path)
	if session == nil {
		t.Fatal("expected session")
	}
	if session.ID != "grok-session" || session.Title != "Inspect repository" || session.Cwd != "/tmp/grok" || session.MessageCount != 6 || session.Model != "grok-4.6" {
		t.Fatalf("unexpected Grok summary: %#v", session)
	}
}
