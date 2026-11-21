package e2esmoke

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultBuildIgnoresHarnessEnvironment(t *testing.T) {
	if buildEnabled {
		t.Skip("built with -tags e2e")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(`{"scenarios":["terminal"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configEnv, config)
	t.Setenv(reportEnv, filepath.Join(dir, "report.json"))
	if IsolatedInstanceID() != "" {
		t.Fatal("default build exposed an isolated test instance")
	}
	if Enabled() {
		t.Fatal("a default build must not enable the harness")
	}
	raw, err := configHandler("", "{}")
	if err != nil {
		t.Fatal(err)
	}
	var result configResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil || result.Enabled || result.Config != nil {
		t.Fatalf("config leaked from a default build: %s", raw)
	}
	if _, err := reportHandler("", `{"report":{"ok":true}}`); err == nil {
		t.Fatal("a default build must refuse reports")
	}
	if _, err := os.Stat(filepath.Join(dir, "report.json")); !os.IsNotExist(err) {
		t.Fatal("a default build wrote a report")
	}
}

func TestReportBoundsAndAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := writeAtomic(path, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != `{"ok":true}` {
		t.Fatalf("report = %q, %v", raw, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, make([]byte, maxDocumentBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBounded(big); err == nil {
		t.Fatal("oversized config accepted")
	}
}
