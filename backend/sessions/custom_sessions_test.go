package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeCustomSessionFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func withoutLiveProcesses(t *testing.T) {
	t.Helper()
	previous := liveCwdsForProcess
	liveCwdsForProcess = func(string) map[string]struct{} { return map[string]struct{}{} }
	t.Cleanup(func() { liveCwdsForProcess = previous })
}

func TestListCustomJsonlSessionsUsesDefaultFieldMapping(t *testing.T) {
	withoutLiveProcesses(t)
	root := t.TempDir()
	writeCustomSessionFile(t, filepath.Join(root, "proj", "abc.jsonl"), `{"type":"summary","summary":"ignored"}
{"sessionId":"s-1","cwd":"/work/app","type":"user","message":{"role":"user","content":"Fix the login bug. Then add tests."},"timestamp":"2025-01-01T00:00:00Z"}
{"type":"assistant","message":{"role":"assistant","model":"m-1","content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"Done."}]}}
not json
`)
	writeCustomSessionFile(t, filepath.Join(root, "notes.txt"), `{"role":"user","content":"skip"}`)

	sessions, err := listCustomSessions(&customSessionConfig{Dir: root, ProcessName: "mycli"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %#v", sessions)
	}
	got := sessions[0]
	if got.ID != "s-1" || got.Cwd != "/work/app" || got.MessageCount != 2 || got.Model != "m-1" {
		t.Fatalf("unexpected session metadata: %#v", got)
	}
	if got.Title != "Fix the login bug." {
		t.Fatalf("title = %q", got.Title)
	}
	if got.Status != "completed" {
		t.Fatalf("status = %q", got.Status)
	}

	messages, err := customSessionMessages(&customSessionConfig{Dir: root}, got.FilePath, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Content != "Done." || messages[0].Timestamp == nil {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestListCustomJsonSessionsWithMappedFields(t *testing.T) {
	withoutLiveProcesses(t)
	root := t.TempDir()
	writeCustomSessionFile(t, filepath.Join(root, "chats", "one.json"), `{
  "meta": {"uuid": "chat-9", "dir": "/repo"},
  "name": "Explicit title",
  "history": [
    {"speaker": "Me", "body": {"parts": ["hello"]}, "at": 1735689600},
    {"speaker": "Bot", "body": {"parts": ["hi"]}}
  ]
}`)
	cfg := &customSessionConfig{
		Dir:    root,
		Glob:   "chats/*.json",
		Format: "json",
		Fields: customSessionFields{
			SessionID: "meta.uuid",
			Cwd:       "meta.dir",
			Title:     "name",
			Messages:  "history",
			Role:      "speaker",
			Content:   "body",
			Timestamp: "at",
		},
		UserRoles:      "me",
		AssistantRoles: "bot",
	}
	sessions, err := listCustomSessions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %#v", sessions)
	}
	got := sessions[0]
	if got.ID != "chat-9" || got.Cwd != "/repo" || got.Title != "Explicit title" || got.MessageCount != 2 {
		t.Fatalf("unexpected session: %#v", got)
	}
	messages, err := customSessionMessages(cfg, got.FilePath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[0].Content != "hello" {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].Timestamp == nil || *messages[0].Timestamp != "2025-01-01T00:00:00Z" {
		t.Fatalf("timestamp = %v", messages[0].Timestamp)
	}
}

func TestCustomSessionFallsBackToFileStemAndInProgressStatus(t *testing.T) {
	withoutLiveProcesses(t)
	root := t.TempDir()
	writeCustomSessionFile(t, filepath.Join(root, "1234567890ab.jsonl"), `{"role":"user","content":"   "}
{"role":"user","content":"still waiting"}
`)
	sessions, err := listCustomSessions(&customSessionConfig{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "1234567890ab" || sessions[0].Status != "in_progress" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestCustomSessionRejectsUnsafeConfigAndPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	for _, cfg := range []*customSessionConfig{
		nil,
		{Dir: "relative/dir"},
		{Dir: string(filepath.Separator)},
		{Dir: home},
		{Dir: filepath.Join(home, "missing")},
		{Dir: t.TempDir(), Format: "xml"},
		{Dir: t.TempDir(), Glob: "../*.jsonl"},
		{Dir: t.TempDir(), Glob: "[.jsonl"},
	} {
		if _, err := newCustomSessionSpec(cfg); err == nil {
			t.Fatalf("expected config %#v to be rejected", cfg)
		}
	}

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	writeCustomSessionFile(t, outside, `{"role":"user","content":"x"}`)
	other := filepath.Join(root, "other.txt")
	writeCustomSessionFile(t, other, `{"role":"user","content":"x"}`)
	cfg := &customSessionConfig{Dir: root}
	if _, err := customSessionMessages(cfg, outside, 10); err == nil {
		t.Fatal("expected outside path to be rejected")
	}
	if err := deleteCustomSession(cfg, other); err == nil {
		t.Fatal("expected non-matching file to be rejected")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("non-matching file should not be deleted: %v", err)
	}

	inside := filepath.Join(root, "s.jsonl")
	writeCustomSessionFile(t, inside, `{"role":"user","content":"x"}`)
	if err := deleteCustomSession(cfg, inside); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("expected session file to be deleted, stat err = %v", err)
	}
}

func TestListAiCliSessionsHandlerRoutesCustomAdapter(t *testing.T) {
	withoutLiveProcesses(t)
	root := t.TempDir()
	writeCustomSessionFile(t, filepath.Join(root, "a.jsonl"), `{"role":"user","content":"hello"}`)
	args, _ := json.Marshal(map[string]any{"adapter": "custom", "custom": map[string]any{"dir": root}})
	raw, err := listAiCliSessionsHandler("", string(args))
	if err != nil {
		t.Fatal(err)
	}
	var sessions []codexSession
	if err := json.Unmarshal([]byte(raw), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Title != "hello" {
		t.Fatalf("sessions = %#v", sessions)
	}

	if _, err := listAiCliSessionsHandler("", `{"adapter":"custom"}`); err == nil {
		t.Fatal("expected missing custom config to fail")
	}
}
