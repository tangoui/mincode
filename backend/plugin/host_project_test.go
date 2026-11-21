package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHostProjectOpenValidatesDirectory(t *testing.T) {
	events := captureHostUI(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "relative/path", string(filepath.Separator), file, link, filepath.Join(dir, "missing"), dir + "\n"} {
		b, _ := json.Marshal(map[string]string{"path": p})
		if _, err := hostProjectOpen(uiRuntime("notes"), b); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	b, _ := json.Marshal(map[string]string{"path": dir})
	if _, err := hostProjectOpen(uiRuntime("notes"), b); err != nil {
		t.Fatal(err)
	}
	got := events.last(t, pluginProjectOpenEvent).(projectOpenEvent)
	canonical, _ := filepath.EvalSymlinks(dir)
	if got.Path != canonical || got.PluginID != "notes" {
		t.Fatalf("event %+v", got)
	}
	if _, err := uiCall(hostProjectOpen, uiRuntime("notes"), `{"path":"/tmp","force":true}`); err == nil {
		t.Error("unknown field accepted")
	}
}
