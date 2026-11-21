package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func codexSessionFixture(title string) string {
	return fmt.Sprintf(
		"%s\n%s\n",
		`{"type":"session_meta","payload":{"id":"session-1","model":"gpt-test","cwd":"/tmp/project"}}`,
		fmt.Sprintf(`{"type":"response_item","payload":{"role":"user","content":%q}}`, title),
	)
}

func TestCodexSessionMetadataCacheReusesUnchangedFilesAndInvalidatesChanges(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "2026", "08", "03")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session dir: %v", err)
	}
	path := filepath.Join(sessionDir, "session-1.jsonl")
	baseTime := time.Unix(1_800_000_000, 0)
	writeFixture := func(title string, modifiedAt time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(codexSessionFixture(title)), 0o600); err != nil {
			t.Fatalf("write session: %v", err)
		}
		if err := os.Chtimes(path, modifiedAt, modifiedAt); err != nil {
			t.Fatalf("set session mtime: %v", err)
		}
	}

	cache := newCodexSessionMetadataCache()
	writeFixture("Alpha", baseTime)
	first := cache.list(root)
	if len(first) != 1 || first[0].Title != "Alpha" {
		t.Fatalf("unexpected first result: %#v", first)
	}

	// Same path, size, and mtime must reuse the cached metadata.
	writeFixture("Bravo", baseTime)
	cached := cache.list(root)
	if len(cached) != 1 || cached[0].Title != "Alpha" {
		t.Fatalf("expected cached title, got %#v", cached)
	}

	// Changing mtime invalidates the entry and reparses the transcript.
	writeFixture("Bravo", baseTime.Add(2*time.Second))
	updated := cache.list(root)
	if len(updated) != 1 || updated[0].Title != "Bravo" {
		t.Fatalf("expected refreshed title, got %#v", updated)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove session: %v", err)
	}
	if sessions := cache.list(root); len(sessions) != 0 {
		t.Fatalf("expected no sessions after removal, got %#v", sessions)
	}
	if len(cache.entries) != 0 {
		t.Fatalf("expected removed cache entry to be pruned, got %d", len(cache.entries))
	}
}
