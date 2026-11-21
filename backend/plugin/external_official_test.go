package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// Run package generation first for release validation; normal unit tests do not
// depend on generated artifacts in the source checkout.
func TestExternalOfficialPackages(t *testing.T) {
	root := os.Getenv("MINCODE_TEST_PLUGIN_PACKAGES")
	if root == "" {
		t.Skip("set MINCODE_TEST_PLUGIN_PACKAGES to validate generated release packages")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var sources []string
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join(path, manifestFilename)); err == nil {
				sources = append(sources, path)
			}
			continue
		}
		if isPluginPackagePath(entry.Name()) {
			sources = append(sources, path)
		}
	}
	if len(sources) == 0 {
		t.Fatal("no plugin packages")
	}
	for _, source := range sources {
		t.Run(filepath.Base(source), func(t *testing.T) {
			useTemporaryPluginData(t)
			manager := newPluginManager()
			if _, err := manager.PreviewInstall(source); err != nil {
				t.Fatal(err)
			}
			manifest, err := manager.InstallLocal(source)
			if err != nil {
				t.Fatal(err)
			}
			record, err := manager.record(manifest.ID)
			if err != nil {
				t.Fatal(err)
			}
			if record.enabled {
				t.Fatal("package auto-enabled")
			}
			if _, err := manager.InstallLocal(source); err == nil {
				t.Fatal("duplicate accepted")
			}
		})
	}
}
