package misc

import (
	"testing"
	"time"
)

func TestCliToolProbeCatalog(t *testing.T) {
	expected := []cliToolProbe{
		{name: "Node.js", command: "node"},
		{name: "Git", command: "git"},
		{name: "Claude Code", command: "claude"},
		{name: "Gemini CLI", command: "gemini"},
		{name: "Codex CLI", command: "codex"},
		{name: "Kimi CLI", command: "kimi"},
		{name: "GLM CLI", command: "glm"},
		{name: "OpenCode", command: "opencode"},
		{name: "QoderCN CLI", command: "qodercn"},
		{name: "Qoder CLI", command: "qoder"},
		{name: "Grok CLI", command: "grok"},
	}

	actual := cliToolProbeCatalog()
	if len(actual) != len(expected) {
		t.Fatalf("unexpected CLI probe count: got=%d want=%d probes=%#v", len(actual), len(expected), actual)
	}
	for i, want := range expected {
		if actual[i] != want {
			t.Errorf("unexpected CLI probe at index %d: got=%#v want=%#v", i, actual[i], want)
		}
	}

	actual[0] = cliToolProbe{name: "mutated", command: "mutated"}
	if fresh := cliToolProbeCatalog(); fresh[0] != expected[0] {
		t.Fatalf("expected isolated CLI probe catalog: got=%#v want=%#v", fresh[0], expected[0])
	}
}

func TestCliToolResultsCacheHonorsTTLAndForceRefresh(t *testing.T) {
	var cache cliToolResultsCache
	now := time.Unix(1_800_000_000, 0)
	loads := 0
	load := func() []cliToolInfo {
		loads++
		return []cliToolInfo{{Name: "Codex CLI", Version: "test", Installed: true}}
	}

	first := cache.get(false, now, load)
	if loads != 1 || len(first) != 1 {
		t.Fatalf("unexpected initial cache load: loads=%d results=%#v", loads, first)
	}

	first[0].Version = "mutated"
	cached := cache.get(false, now.Add(time.Minute), load)
	if loads != 1 || cached[0].Version != "test" {
		t.Fatalf("expected isolated cached result: loads=%d results=%#v", loads, cached)
	}

	forced := cache.get(true, now.Add(2*time.Minute), load)
	if loads != 2 || forced[0].Version != "test" {
		t.Fatalf("expected forced reload: loads=%d results=%#v", loads, forced)
	}

	expired := cache.get(false, now.Add(8*time.Minute), load)
	if loads != 3 || expired[0].Version != "test" {
		t.Fatalf("expected TTL reload: loads=%d results=%#v", loads, expired)
	}
}
