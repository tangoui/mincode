package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func resetSessionRootOverrides(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := setAiSessionRootsHandler("", `{}`); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSessionRootsPreferOverrideThenEnvThenDefault(t *testing.T) {
	resetSessionRootOverrides(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	if dir, _ := claudeProjectsDir(); dir != filepath.Join(home, ".claude", "projects") {
		t.Fatalf("default claude dir = %q", dir)
	}
	if dir, _ := codexHomeDir(); dir != filepath.Join(home, ".codex") {
		t.Fatalf("default codex dir = %q", dir)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", "~/claude-alt")
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-alt"))
	if dir, _ := claudeProjectsDir(); dir != filepath.Join(home, "claude-alt", "projects") {
		t.Fatalf("env claude dir = %q", dir)
	}
	if dir, _ := codexHomeDir(); dir != filepath.Join(home, "codex-alt") {
		t.Fatalf("env codex dir = %q", dir)
	}

	override := filepath.Join(home, "override")
	raw, err := setAiSessionRootsHandler("", `{"roots":{"claude":"`+filepath.ToSlash(override)+`","codex":"~/codex-override","grok":"  "}}`)
	if err != nil {
		t.Fatal(err)
	}
	var resolved map[string]string
	if err := json.Unmarshal([]byte(raw), &resolved); err != nil {
		t.Fatalf("decode resolved roots %q: %v", raw, err)
	}
	if len(resolved) != 2 || resolved["codex"] != filepath.Join(home, "codex-override") || resolved["claude"] != override {
		t.Fatalf("resolved roots = %#v", resolved)
	}
	if dir, _ := claudeProjectsDir(); dir != filepath.Join(override, "projects") {
		t.Fatalf("override claude dir = %q", dir)
	}
	if dir, _ := codexHomeDir(); dir != filepath.Join(home, "codex-override") {
		t.Fatalf("override codex dir = %q", dir)
	}
	if dir, _ := grokRoot(); dir != filepath.Join(home, ".grok") {
		t.Fatalf("blank override should keep default grok dir, got %q", dir)
	}

	if _, err := setAiSessionRootsHandler("", `{}`); err != nil {
		t.Fatal(err)
	}
	if dir, _ := codexHomeDir(); dir != filepath.Join(home, "codex-alt") {
		t.Fatalf("clearing overrides should fall back to env, got %q", dir)
	}
}

func TestSetAiSessionRootsRejectsInvalidInput(t *testing.T) {
	resetSessionRootOverrides(t)
	for _, args := range []string{
		`{"roots":{"unknown":"/tmp"}}`,
		`{"roots":{"claude":"relative/path"}}`,
	} {
		if _, err := setAiSessionRootsHandler("", args); err == nil {
			t.Fatalf("expected %s to be rejected", args)
		}
	}
}

func TestPiAndOpencodeOverridesUseToolLayout(t *testing.T) {
	resetSessionRootOverrides(t)
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "pi", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "oc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "oc", "opencode.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	args := `{"roots":{"pi":"` + filepath.ToSlash(filepath.Join(base, "pi")) + `","opencode":"` + filepath.ToSlash(filepath.Join(base, "oc")) + `","kimi":"` + filepath.ToSlash(filepath.Join(base, "kimi")) + `"}}`
	if _, err := setAiSessionRootsHandler("", args); err != nil {
		t.Fatal(err)
	}
	if dir, ok := piRoot(); !ok || dir != filepath.Join(base, "pi", "sessions") {
		t.Fatalf("pi root = %q, %v", dir, ok)
	}
	if db, ok := opencodeDBPath(); !ok || db != filepath.Join(base, "oc", "opencode.db") {
		t.Fatalf("opencode db = %q, %v", db, ok)
	}
	if roots := kimiRoots(); len(roots) != 1 || roots[0] != filepath.Join(base, "kimi") {
		t.Fatalf("kimi roots = %#v", roots)
	}
}
