package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One operation covers ten projects of 100 sessions each. Each synthetic
// transcript has eight turns, or 128 turns for every tenth session.
func BenchmarkClaudeProjectSessions(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	b.Setenv("USERPROFILE", "")
	b.Setenv("CLAUDE_CONFIG_DIR", "")

	repository, _ := setTestClaudeTitles(b)
	originalTitles, originalProbe := claudeTitles, livePlatformProbe
	claudeTitles = repository
	livePlatformProbe = func(map[string]struct{}) map[string]struct{} { return nil }
	b.Cleanup(func() {
		claudeTitles = originalTitles
		livePlatformProbe = originalProbe
		liveCwdsCacheMu.Lock()
		delete(liveCwdsCache, "claude")
		liveCwdsCacheMu.Unlock()
		clear(claudeSummaryCache.entries)
		clear(claudeStatusCache.entries)
	})

	const projectCount, filesPerProject = 10, 100
	args := make([]string, 0, projectCount)
	paths := make([]string, 0, projectCount*filesPerProject)
	turn := `{"type":"user","cwd":"/synthetic/project","message":{"role":"user","content":"Synthetic fixture prompt"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","model":"fixture-model","content":[{"type":"text","text":"` +
		strings.Repeat("x", 896) + `"}]}}` + "\n"
	short := []byte(strings.Repeat(turn, 8))
	long := []byte(strings.Repeat(turn, 128))
	for project := 0; project < projectCount; project++ {
		encodedDir := fmt.Sprintf("fixture-project-%02d", project)
		args = append(args, fmt.Sprintf(`{"encodedDir":%q}`, encodedDir))
		dir := filepath.Join(home, ".claude", "projects", encodedDir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			b.Fatal(err)
		}
		for session := 0; session < filesPerProject; session++ {
			path := filepath.Join(dir, fmt.Sprintf("session-%03d.jsonl", session))
			transcript := short
			if session%10 == 0 {
				transcript = long
			}
			if err := os.WriteFile(path, transcript, 0o600); err != nil {
				b.Fatal(err)
			}
			paths = append(paths, path)
		}
	}

	listAll := func(b *testing.B) {
		b.Helper()
		for _, arg := range args {
			if _, err := getProjectSessionsHandler("", arg); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("Cold", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			b.StopTimer()
			clear(claudeSummaryCache.entries)
			clear(claudeStatusCache.entries)
			b.StartTimer()
			listAll(b)
		}
	})
	b.Run("Warm", func(b *testing.B) {
		listAll(b)
		b.ReportAllocs()
		for b.Loop() {
			listAll(b)
		}
	})

	// Measure the cost of each part in isolation, without directory enumeration,
	// title queries, liveness reconciliation, sorting, or response encoding.
	parts := []struct {
		name string
		run  func(string)
	}{
		{"OneStat", func(path string) { _, _ = statFileStamp(path) }},
		{"ThreeStats", func(path string) {
			fileMtimeMs(path)
			statFileStamp(path)
			statFileStamp(path)
		}},
		{"ReadSummary", func(path string) { readClaudeTranscriptSummary(path, unknownFileSize) }},
		{"ReadStatus", func(path string) { inferClaudeSessionStatus(path) }},
	}
	for _, part := range parts {
		b.Run(part.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, path := range paths {
					part.run(path)
				}
			}
		})
	}
}
