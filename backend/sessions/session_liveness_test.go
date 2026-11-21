package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApplySessionLiveness(t *testing.T) {
	cases := []struct {
		transcript string
		live       bool
		want       string
	}{
		{"in_progress", true, "in_progress"},
		{"completed", true, "completed"},
		{"failed", true, "failed"},
		{"in_progress", false, "interrupted"},
		{"completed", false, "completed"},
		{"failed", false, "failed"},
		{"", false, ""},
	}
	for _, c := range cases {
		if got := applySessionLiveness(c.transcript, c.live); got != c.want {
			t.Fatalf("applySessionLiveness(%q, %v) = %q, want %q", c.transcript, c.live, got, c.want)
		}
	}
}

func TestFreshestLiveSessionPicksNewestPerLiveCwd(t *testing.T) {
	dir := t.TempDir()
	live := map[string]struct{}{normalizeLiveCwd(dir): {}}
	candidates := []sessionLiveCandidate{
		{UUID: "old", Cwd: dir, LastAt: 1},
		{UUID: "new", Cwd: dir, LastAt: 9},
		{UUID: "elsewhere", Cwd: t.TempDir(), LastAt: 99},
	}
	running := freshestLiveSession(candidates, live)
	if _, ok := running["new"]; !ok {
		t.Fatalf("expected newest session in live cwd to be running, got %v", running)
	}
	if _, ok := running["old"]; ok {
		t.Fatal("stale session in live cwd must not be marked running")
	}
	if _, ok := running["elsewhere"]; ok {
		t.Fatal("session outside live cwds must not be marked running")
	}
}

func TestReconcileAiCliStatusesUsesAdapterProcessName(t *testing.T) {
	dir := t.TempDir()
	previous := liveCwdsForProcess
	t.Cleanup(func() { liveCwdsForProcess = previous })
	asked := make([]string, 0, 2)
	liveCwdsForProcess = func(processName string) map[string]struct{} {
		asked = append(asked, processName)
		if processName != "gemini" {
			return nil
		}
		return map[string]struct{}{normalizeLiveCwd(dir): {}}
	}

	sessions := []codexSession{
		{ID: "running", Cwd: dir, LastAt: 9, Status: "in_progress"},
		{ID: "stale", Cwd: dir, LastAt: 1, Status: "in_progress"},
		{ID: "done", Cwd: dir, LastAt: 8, Status: "completed"},
	}
	reconcileAiCliStatuses("gemini", sessions)
	if sessions[0].Status != "in_progress" {
		t.Fatalf("running = %q, want in_progress", sessions[0].Status)
	}
	if sessions[1].Status != "interrupted" {
		t.Fatalf("stale = %q, want interrupted", sessions[1].Status)
	}
	if sessions[2].Status != "completed" {
		t.Fatalf("done = %q, want completed", sessions[2].Status)
	}

	unknown := []codexSession{{ID: "x", Cwd: dir, LastAt: 9, Status: "in_progress"}}
	reconcileAiCliStatuses("unsupported", unknown)
	if unknown[0].Status != "in_progress" {
		t.Fatalf("unknown adapter status = %q, want untouched in_progress", unknown[0].Status)
	}
	if len(asked) != 1 || asked[0] != "gemini" {
		t.Fatalf("probed process names = %v, want [gemini]", asked)
	}
}

func TestAiCliProcessNamesCoverSupportedAdapters(t *testing.T) {
	expected := map[string]string{
		"pi": "pi", "gemini": "gemini", "qodercn": "qoderclicn",
		"qoder": "qodercli", "grok": "grok", "kimi": "kimi", "opencode": "opencode",
	}
	for adapter, want := range expected {
		if name, ok := aiCliProcessNames[adapter]; !ok || name != want {
			t.Fatalf("aiCliProcessNames[%q] = %q (%v), want %q", adapter, name, ok, want)
		}
	}
}

func TestInferClaudeSessionStatusTranscriptTail(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := inferClaudeSessionStatus(write("a.jsonl", "{\"type\":\"user\"}\n")); got != "in_progress" {
		t.Fatalf("user tail = %q, want in_progress", got)
	}
	if got := inferClaudeSessionStatus(write("b.jsonl",
		"{\"type\":\"user\"}\n{\"type\":\"assistant\",\"message\":{\"stop_reason\":\"end_turn\"}}\n")); got != "completed" {
		t.Fatalf("assistant tail = %q, want completed", got)
	}
	if got := inferClaudeSessionStatus(write("c.jsonl",
		"{\"type\":\"assistant\",\"message\":{\"stop_reason\":\"error\"}}\n")); got != "failed" {
		t.Fatalf("error tail = %q, want failed", got)
	}
}

func TestClaudeSessionStatusesHandlerInterruptedWithoutLiveProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	projDir := filepath.Join(home, ".claude", "projects", "-tmp-demo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// cwd 指向不存在目录，确保不会命中任何存活进程。
	if err := os.WriteFile(filepath.Join(projDir, "aaa.jsonl"),
		[]byte("{\"type\":\"user\",\"cwd\":\"/nonexistent-mincode-demo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "bbb.jsonl"),
		[]byte("{\"type\":\"assistant\",\"message\":{\"stop_reason\":\"end_turn\"}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := claudeSessionStatusesHandler("", `{"encodedDir":"-tmp-demo"}`)
	if err != nil {
		t.Fatal(err)
	}
	var statuses map[string]string
	if err := json.Unmarshal([]byte(out), &statuses); err != nil {
		t.Fatal(err)
	}
	if statuses["aaa"] != "interrupted" {
		t.Fatalf("aaa = %q, want interrupted (user tail without live process)", statuses["aaa"])
	}
	if statuses["bbb"] != "completed" {
		t.Fatalf("bbb = %q, want completed", statuses["bbb"])
	}
}
