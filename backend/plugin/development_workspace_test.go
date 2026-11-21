package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevelopmentWorkspaceDiscovery(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	descriptor := `{"id":"demo-plugin","name":"Demo","version":"1.0.0"}`
	for _, name := range []string{"group/demo/plugin.template.json", "group/demo/dist-plugin/plugin.json", "node_modules/ignored/plugin.json", "broken/plugin.json"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		content := descriptor
		if strings.HasPrefix(name, "broken/") {
			content = "{"
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	projects, err := scanPluginDevProjects(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected two projects, got %+v", projects)
	}
	if projects[0].Status != "error" {
		t.Fatal("invalid manifest not reported")
	}
	if projects[1].Kind != "source" || projects[1].PackageRoot == "" {
		t.Fatalf("source package not merged: %+v", projects[1])
	}
}

// A monorepo such as official-plugins holds well over a hundred plugins.
func TestDevelopmentWorkspaceDiscoveryHandlesLargeMonorepos(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		dir := filepath.Join(root, fmt.Sprintf("plugin-%03d", i))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		descriptor := fmt.Sprintf(`{"id":"plugin-%03d","name":"Plugin %d","version":"1.0.0"}`, i, i)
		if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(descriptor), 0600); err != nil {
			t.Fatal(err)
		}
	}
	projects, err := scanPluginDevProjects(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 300 {
		t.Fatalf("expected 300 projects, got %d", len(projects))
	}
}

func TestDevelopmentDescriptorRejectsUnsafeFiles(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(root, "large.json")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", 256*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPluginDevDescriptor(root, oversized); err == nil {
		t.Fatal("oversized descriptor accepted")
	}
	outside := filepath.Join(t.TempDir(), "plugin.json")
	if err := os.WriteFile(outside, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPluginDevDescriptor(root, outside); err == nil {
		t.Fatal("outside descriptor accepted")
	}
	link := filepath.Join(root, "plugin.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := readPluginDevDescriptor(root, link); err == nil {
		t.Fatal("symlink accepted")
	}
}
