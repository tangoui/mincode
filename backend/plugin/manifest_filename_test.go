package plugin

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const legacyManifestFilename = "mincode.plugin.json"

func legacyManifestFixture(t *testing.T, version string) string {
	t.Helper()
	root := writeTestManifest(t, func(m *Manifest) { m.Version = version })
	if err := os.Rename(filepath.Join(root, manifestFilename), filepath.Join(root, legacyManifestFilename)); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestManifestFilenameCompatibility(t *testing.T) {
	for _, kind := range []string{"legacy", "new", "both", "invalid-json", "invalid-contract", "oversized", "directory", "symlink", "dangling-symlink", "legacy-symlink", "missing"} {
		t.Run(kind, func(t *testing.T) {
			root := legacyManifestFixture(t, "1.0.0")
			path := filepath.Join(root, manifestFilename)
			legacy := filepath.Join(root, legacyManifestFilename)
			switch kind {
			case "new", "both":
				source := writeTestManifest(t, func(m *Manifest) { m.Version = "2.0.0" })
				data, err := os.ReadFile(filepath.Join(source, manifestFilename))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "new" {
					if err := os.Remove(legacy); err != nil {
						t.Fatal(err)
					}
				}
			case "invalid-json", "invalid-contract", "oversized":
				data := []byte("{")
				if kind == "invalid-contract" {
					data = []byte(`{"apiVersion":99}`)
				}
				if kind == "oversized" {
					data = []byte(strings.Repeat(" ", 256*1024+1))
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling-symlink", "legacy-symlink":
				target := legacy
				if kind == "dangling-symlink" {
					target = filepath.Join(root, "missing")
				}
				if kind == "legacy-symlink" {
					target = filepath.Join(t.TempDir(), "manifest")
					if err := os.Rename(legacy, target); err != nil {
						t.Fatal(err)
					}
					path = legacy
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skip(err)
				}
			case "missing":
				if err := os.Remove(legacy); err != nil {
					t.Fatal(err)
				}
			}
			manifest, err := readManifest(root)
			valid := kind == "new" || kind == "both"
			if !valid {
				if err == nil {
					t.Fatal("invalid manifest bypassed through legacy fallback")
				}
				if strings.Contains(kind, "symlink") && kind != "legacy-symlink" && !strings.Contains(err.Error(), "symlink") {
					t.Fatalf("expected symlink rejection: %v", err)
				}
				return
			}
			want := "2.0.0"
			if kind == "legacy" {
				want = "1.0.0"
			}
			if err != nil || manifest.Version != want {
				t.Fatalf("version=%s want=%s err=%v", manifest.Version, want, err)
			}
		})
	}
}

func TestManifestFilenamePermissionErrorDoesNotFallback(t *testing.T) {
	calls := 0
	_, err := selectManifestFilename(func(name string) (fs.FileInfo, error) {
		calls++
		if name != manifestFilename {
			t.Fatal("permission error caused legacy fallback")
		}
		return nil, fs.ErrPermission
	})
	if calls != 1 || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func readEmbeddedManifest(packages fs.FS, root string) ([]byte, error) {
	name, err := selectManifestFilename(func(name string) (fs.FileInfo, error) { return fs.Stat(packages, root+"/"+name) })
	if err != nil {
		return nil, err
	}
	return fs.ReadFile(packages, root+"/"+name)
}

func TestEmbeddedManifestFilenameCompatibility(t *testing.T) {
	for _, kind := range []string{"legacy", "new", "both", "invalid", "directory", "missing"} {
		t.Run(kind, func(t *testing.T) {
			files := fstest.MapFS{}
			if kind != "new" && kind != "missing" {
				files["package/"+legacyManifestFilename] = &fstest.MapFile{Data: []byte("legacy")}
			}
			if kind != "legacy" && kind != "missing" {
				files["package/"+manifestFilename] = &fstest.MapFile{Data: []byte("new")}
				if kind == "invalid" {
					files["package/"+manifestFilename].Data = []byte("{")
				}
				if kind == "directory" {
					files["package/"+manifestFilename].Mode = fs.ModeDir
				}
			}
			data, err := readEmbeddedManifest(files, "package")
			if kind == "directory" || kind == "missing" || kind == "legacy" {
				if err == nil {
					t.Fatal("expected rejection")
				}
				return
			}
			want := "new"
			if kind == "legacy" {
				want = "legacy"
			}
			if kind == "invalid" {
				want = "{"
			}
			if err != nil || string(data) != want {
				t.Fatalf("data=%q want=%q err=%v", data, want, err)
			}
		})
	}
}

func TestManifestFilenameInstallUpgradeAndDiscovery(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "new"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			useTemporaryPluginData(t)
			fixture := func(version string) string {
				if legacy {
					return legacyManifestFixture(t, version)
				}
				return writeTestManifest(t, func(m *Manifest) { m.Version = version })
			}
			manager := &pluginManager{records: map[string]*pluginRecord{}}
			installed, err := manager.InstallLocal(fixture("1.0.0"))
			if legacy {
				if err == nil {
					t.Fatal("legacy package installed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.UpgradeLocal(context.Background(), installed.ID, fixture("1.1.0")); err != nil {
				t.Fatal(err)
			}
			for _, sync := range []bool{false, true} {
				discovered := &pluginManager{records: map[string]*pluginRecord{}}
				if sync {
					if err := discovered.syncInstalled(context.Background()); err != nil {
						t.Fatal(err)
					}
				} else {
					discovered.scanInstalled()
				}
				record, err := discovered.record(installed.ID)
				if err != nil || record.manifest.Version != "1.1.0" {
					t.Fatalf("discovery failed: %v", err)
				}
			}
			// Also exercise migration from a legacy installation to a new-name package.
			source := writeTestManifest(t, func(m *Manifest) { m.Version = "2.0.0" })
			if _, err := manager.UpgradeLocal(context.Background(), installed.ID, source); err != nil {
				t.Fatal(err)
			}
			for _, sync := range []bool{false, true} {
				discovered := &pluginManager{records: map[string]*pluginRecord{}}
				if sync {
					if err := discovered.syncInstalled(context.Background()); err != nil {
						t.Fatal(err)
					}
				} else {
					discovered.scanInstalled()
				}
				record, err := discovered.record(installed.ID)
				if err != nil || record.manifest.Version != "2.0.0" {
					t.Fatalf("discovery failed: %v", err)
				}
			}
		})
	}
}

func TestManifestFilenameInvalidNewRejectedByAllPaths(t *testing.T) {
	for _, kind := range []string{"install", "upgrade", "scan", "sync"} {
		t.Run(kind, func(t *testing.T) {
			useTemporaryPluginData(t)
			manager := &pluginManager{records: map[string]*pluginRecord{}}
			source := legacyManifestFixture(t, "2.0.0")
			if err := os.WriteFile(filepath.Join(source, manifestFilename), []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "install":
				if _, err := manager.InstallLocal(source); err == nil {
					t.Fatal("invalid new manifest installed")
				}
			case "upgrade":
				installed, err := manager.InstallLocal(writeTestManifest(t, nil))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := manager.UpgradeLocal(context.Background(), installed.ID, source); err == nil {
					t.Fatal("invalid new manifest upgraded")
				}
				manifest, err := readManifest(filepath.Join(pluginsDirectory(), installed.ID))
				if err != nil || manifest.Version != "1.0.0" {
					t.Fatalf("old package changed: %v", err)
				}
			default:
				if err := copyPluginTree(source, filepath.Join(pluginsDirectory(), "example.plugin")); err != nil {
					t.Fatal(err)
				}
				if kind == "scan" {
					manager.scanInstalled()
				} else if err := manager.syncInstalled(context.Background()); err != nil {
					t.Fatal(err)
				}
				record, err := manager.record("example.plugin")
				if err != nil || record.state != pluginStateError || record.runtime != nil || record.enabled {
					t.Fatal("invalid package must remain visible and inert")
				}
			}
		})
	}
}
