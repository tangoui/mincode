package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// legacySessionFileCwd 是改为只读文件头之前的实现，用于对照结果。
func legacySessionFileCwd(path string) string {
	prefix, _, _ := readJsonlPrefixAndCount(path, 96*1024, unknownFileSize)
	cwd := ""
	jsonLines(prefix, func(v map[string]json.RawMessage) {
		if cwd == "" {
			if c := rawString(v, "cwd"); strings.TrimSpace(c) != "" {
				cwd = strings.TrimSpace(c)
			}
		}
	})
	return cwd
}

func writeStampedTranscript(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("set mtime: %v", err)
	}
}

func TestReadSessionFileCwdOnlyNeedsHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	// 首行即有 cwd，其后是远超 96KB 的非 JSON 垃圾；头部读取必须直接命中。
	content := `{"type":"user","cwd":"/work/proj"}` + "\n" + strings.Repeat("garbage-without-newline", 20_000)
	writeStampedTranscript(t, path, content, time.Unix(1_800_000_000, 0))
	if got := readSessionFileCwd(path); got != "/work/proj" {
		t.Fatalf("cwd = %q", got)
	}

	// cwd 在 96KB 之后时与旧实现一致：找不到。
	late := filepath.Join(dir, "late.jsonl")
	writeStampedTranscript(t, late, strings.Repeat(`{"type":"system"}`+"\n", 8000)+`{"cwd":"/late"}`+"\n", time.Unix(1_800_000_000, 0))
	if got, want := readSessionFileCwd(late), legacySessionFileCwd(late); got != want || got != "" {
		t.Fatalf("late cwd = %q, legacy %q", got, want)
	}
}

func TestReadSessionFileCwdMatchesLegacy(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"normal.jsonl":  `{"type":"summary"}` + "\n" + `{"type":"user","cwd":"  /a/b  "}` + "\n" + `{"type":"user","cwd":"/c"}` + "\n",
		"none.jsonl":    `{"type":"user"}` + "\n",
		"empty.jsonl":   "",
		"no-nl.jsonl":   `{"cwd":"/x"}`,
		"blank.jsonl":   `{"cwd":"   "}` + "\n" + `{"cwd":"/y"}` + "\n",
		"broken.jsonl":  `{"cwd":` + "\n" + `{"cwd":"/z"}` + "\n",
		"missing.jsonl": "",
	}
	for name, content := range cases {
		path := filepath.Join(dir, name)
		if name != "missing.jsonl" {
			writeStampedTranscript(t, path, content, time.Unix(1_800_000_000, 0))
		}
		if got, want := readSessionFileCwd(path), legacySessionFileCwd(path); got != want {
			t.Errorf("%s: got %q want %q", name, got, want)
		}
	}
}

func TestClaudeFileCacheReusesUnchangedFilesAndInvalidatesChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	base := time.Unix(1_800_000_000, 0)
	var calls atomic.Int32
	cache := newClaudeFileCache(func(p string) string {
		calls.Add(1)
		return readSessionFileCwd(p)
	})

	writeStampedTranscript(t, path, `{"cwd":"/aaaa"}`+"\n", base)
	if got := cache.get(path); got != "/aaaa" {
		t.Fatalf("first = %q", got)
	}
	// 同尺寸同 mtime：命中缓存，不重读。
	writeStampedTranscript(t, path, `{"cwd":"/bbbb"}`+"\n", base)
	if got := cache.get(path); got != "/aaaa" || calls.Load() != 1 {
		t.Fatalf("expected cache hit, got %q after %d calls", got, calls.Load())
	}
	// mtime 变化：失效重读。
	writeStampedTranscript(t, path, `{"cwd":"/bbbb"}`+"\n", base.Add(time.Second))
	if got := cache.get(path); got != "/bbbb" {
		t.Fatalf("after mtime change = %q", got)
	}
	// 尺寸变化（mtime 不变）：同样失效。
	writeStampedTranscript(t, path, `{"cwd":"/cc"}`+"\n", base.Add(time.Second))
	if got := cache.get(path); got != "/cc" || calls.Load() != 3 {
		t.Fatalf("after size change = %q (%d calls)", got, calls.Load())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := cache.get(path); got != "" {
		t.Fatalf("removed file = %q", got)
	}
	if len(cache.entries) != 0 {
		t.Fatalf("removed file should be evicted, entries=%d", len(cache.entries))
	}
}

func TestClaudeFileCacheIsBounded(t *testing.T) {
	cache := newClaudeFileCache(func(string) int { return 1 })
	gone := filepath.Join(t.TempDir(), "gone")
	for i := 0; i < claudeFileCacheLimit; i++ {
		cache.entries[filepath.Join(gone, strconv.Itoa(i))] = claudeFileCacheEntry[int]{}
	}
	path := filepath.Join(t.TempDir(), "live.jsonl")
	writeStampedTranscript(t, path, "{}\n", time.Now())
	cache.get(path)
	if len(cache.entries) != 1 {
		t.Fatalf("stale entries should be pruned, got %d", len(cache.entries))
	}
}

func TestClaudeFileCacheUsesSuppliedStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	base := time.Unix(1_800_000_000, 0)
	cache := newClaudeFileCache(readSessionFileCwd)
	writeStampedTranscript(t, path, "{\"cwd\":\"/first\"}\n", base)
	stamp, ok := statFileStamp(path)
	if !ok || cache.get(path) != "/first" {
		t.Fatal("could not prime the cache")
	}

	writeStampedTranscript(t, path, "{\"cwd\":\"/second\"}\n", base)
	if got := cache.getWithStamp(path, stamp, true); got != "/first" {
		t.Fatalf("snapshot should use its cached value, got %q", got)
	}
	if got := cache.get(path); got != "/second" {
		t.Fatalf("independent lookup should observe the size change, got %q", got)
	}

	// A failed stat evicts the old result and preserves the uncached read in
	// case the file reappears between stat and read.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing, ok := statFileStamp(path)
	if ok {
		t.Fatal("expected a missing file")
	}
	writeStampedTranscript(t, path, "{\"cwd\":\"/third\"}\n", base)
	if got := cache.getWithStamp(path, missing, ok); got != "/third" || len(cache.entries) != 0 {
		t.Fatalf("missing snapshot should compute without caching, got %q with %d entries", got, len(cache.entries))
	}
}

func TestClaudeTranscriptSummaryMatchesLegacy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	content := strings.Join([]string{
		`{"type":"user","cwd":"/fallback","message":{"role":"user","content":"first prompt"}}`,
		`{"type":"assistant","cwd":"/fallback","message":{"role":"assistant","model":"claude-x","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"user","cwd":"/real/dir","message":{"role":"user","content":"second prompt"}}`,
		"",
	}, "\n")
	writeStampedTranscript(t, path, content, time.Unix(1_800_000_000, 0))

	prefix, lastPrompt, count := readJsonlPrefixAndCount(path, 96*1024, unknownFileSize)
	legacyPath, legacyTitle, legacyModel := "/fallback", "", ""
	jsonLines(prefix, func(v map[string]json.RawMessage) {
		if legacyPath == "/fallback" {
			if cwd := rawString(v, "cwd"); strings.TrimSpace(cwd) != "" {
				legacyPath = strings.TrimSpace(cwd)
			}
		}
		if legacyTitle == "" {
			legacyTitle = claudePromptTitleFromEntry(v)
		}
		if legacyModel == "" {
			if msgRaw, ok := v["message"]; ok {
				var msg map[string]json.RawMessage
				if json.Unmarshal(msgRaw, &msg) == nil {
					legacyModel = rawString(msg, "model")
				}
			}
		}
	})

	s := readClaudeTranscriptSummary(path, unknownFileSize)
	got := []any{s.decodedPath("/fallback"), s.FirstTitle, s.Model, s.LastPrompt, s.MessageCount}
	want := []any{legacyPath, legacyTitle, legacyModel, lastPrompt, count}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summary %v, legacy %v", got, want)
	}
	if got := s.decodedPath("/other"); got != "/fallback" {
		t.Fatalf("decodedPath(/other) = %q", got)
	}
}

func TestLiveSessionCwdsSharesRecentProbe(t *testing.T) {
	prev := livePlatformProbe
	t.Cleanup(func() {
		livePlatformProbe = prev
		liveCwdsCacheMu.Lock()
		clear(liveCwdsCache)
		liveCwdsCacheMu.Unlock()
	})
	var calls atomic.Int32
	dir := t.TempDir()
	livePlatformProbe = func(map[string]struct{}) map[string]struct{} {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return map[string]struct{}{dir: {}}
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := liveSessionCwds("test-cli-shared"); !cwdIsLive(dir, got) {
				t.Errorf("missing live cwd: %v", got)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("expected one probe for concurrent callers, got %d", calls.Load())
	}

	liveCwdsCacheMu.Lock()
	liveCwdsCache["test-cli-shared"].at = time.Now().Add(-liveCwdsTTL)
	liveCwdsCacheMu.Unlock()
	liveSessionCwds("test-cli-shared")
	if calls.Load() != 2 {
		t.Fatalf("expected re-probe after TTL, got %d", calls.Load())
	}
}
