package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestClaudeProjectSessionsRefreshAfterTranscriptChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	repository, _ := setTestClaudeTitles(t)
	originalTitles, originalProbe := claudeTitles, livePlatformProbe
	claudeTitles = repository
	livePlatformProbe = func(map[string]struct{}) map[string]struct{} { return nil }
	t.Cleanup(func() {
		claudeTitles = originalTitles
		livePlatformProbe = originalProbe
		liveCwdsCacheMu.Lock()
		delete(liveCwdsCache, "claude")
		liveCwdsCacheMu.Unlock()
	})

	const encodedDir = "fixture-project"
	const args = `{"encodedDir":"fixture-project"}`
	dir := filepath.Join(home, ".claude", "projects", encodedDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, testSessionUUID+".jsonl")
	base := time.Unix(1_800_000_000, 0)
	reply := `{"type":"assistant","message":{"model":"fixture-model"}}` + "\n"
	writeStampedTranscript(t, path, reply, base)

	checkList := func(title, status string, lastAt time.Time, count uint32) {
		t.Helper()
		raw, err := getProjectSessionsHandler("", args)
		if err != nil {
			t.Error(err)
			return
		}
		var rows []claudeSessionMeta
		if err := json.Unmarshal([]byte(raw), &rows); err != nil {
			t.Error(err)
			return
		}
		if len(rows) != 1 || rows[0].Status != status || rows[0].Title != title ||
			rows[0].LastAt != uint64(lastAt.UnixMilli()) || rows[0].MessageCount != count {
			t.Errorf("unexpected list metadata for %s", status)
		}
	}
	checkPoll := func(status string) {
		t.Helper()
		raw, err := claudeSessionStatusesHandler("", args)
		if err != nil {
			t.Error(err)
			return
		}
		var statuses map[string]string
		if err := json.Unmarshal([]byte(raw), &statuses); err != nil {
			t.Error(err)
			return
		}
		if got := statuses[testSessionUUID]; got != status {
			t.Errorf("poll = %q, want %q", got, status)
		}
	}

	checkList("123e4567...", "completed", base, 1)

	appendPrompt := `{"type":"user","message":{"content":"Second prompt"}}` + "\n"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(appendPrompt)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append fixture: %v, close: %v", writeErr, closeErr)
	}
	if err := os.Chtimes(path, base, base); err != nil {
		t.Fatal(err)
	}
	checkPoll("interrupted")
	checkList("Second prompt", "interrupted", base, 2)

	// Change only mtime and access the cold entries from concurrent requests.
	updated := base.Add(time.Second)
	writeStampedTranscript(t, path, reply+`{"type":"user","message":{"content":"Latest prompt"}}`+"\n", updated)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { checkList("Latest prompt", "interrupted", updated, 2) })
		wg.Go(func() { checkPoll("interrupted") })
	}
	wg.Wait()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	got, err := getProjectSessionsHandler("", args)
	if err != nil || got != "[]" {
		t.Fatalf("expected empty list after removal, err=%v", err)
	}
}
