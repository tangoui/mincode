package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicInstallSources(t *testing.T) {
	root := t.TempDir()
	if err := requirePluginInstallSource(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "plugin.zip")
	if err := os.WriteFile(path, []byte("not a ZIP"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := requirePluginInstallSource(path); err != nil {
		t.Fatal(err)
	}
	if err := requirePluginZIP(root); err == nil {
		t.Fatal("directory accepted as ZIP-only source")
	}
	if err := requirePluginZIP(path); err != nil {
		t.Fatal(err)
	}
	textPath := filepath.Join(root, "plugin.txt")
	if err := os.WriteFile(textPath, []byte("not a plugin"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := requirePluginInstallSource(textPath); err == nil {
		t.Fatal("non-ZIP file accepted")
	}
	linkPath := filepath.Join(root, "linked.zip")
	if err := os.Symlink(path, linkPath); err == nil {
		if err := requirePluginInstallSource(linkPath); err == nil {
			t.Fatal("ZIP symlink accepted")
		}
	}
	directoryLink := filepath.Join(root, "linked-directory")
	if err := os.Symlink(t.TempDir(), directoryLink); err == nil {
		if err := requirePluginInstallSource(directoryLink); err == nil {
			t.Fatal("directory symlink accepted")
		}
	}
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	for _, extension := range []string{".min", ".MIN", ".zip", ".ZIP"} {
		archive := filepath.Join(root, "invalid"+extension)
		if err := os.WriteFile(archive, []byte("not a ZIP"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, check := range []func(string) error{requirePluginInstallSource, requirePluginZIP} {
			if err := check(" " + archive + " "); err != nil {
				t.Fatalf("%s rejected: %v", extension, err)
			}
		}
		if _, err := manager.PreviewInstall(archive); err == nil {
			t.Fatalf("invalid %s content accepted", extension)
		}
		link := filepath.Join(root, "link"+extension)
		if err := os.Symlink(archive, link); err == nil {
			for _, check := range []func(string) error{requirePluginInstallSource, requirePluginZIP} {
				if err := check(link); err == nil {
					t.Fatalf("%s symlink accepted", extension)
				}
			}
		}
	}
	for _, name := range []string{"plugin.txt", "plugin.min.zip.exe", "plugin"} {
		archive := filepath.Join(root, name)
		if err := os.WriteFile(archive, []byte("PK"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, check := range []func(string) error{requirePluginInstallSource, requirePluginZIP} {
			if err := check(archive); err == nil {
				t.Fatalf("unsupported extension accepted: %s", name)
			}
		}
	}
}

func TestPluginDownloadURLPolicy(t *testing.T) {
	for _, raw := range []string{"http://example.com/plugin.zip", "file:///tmp/plugin.zip", "https://user:pass@example.com/plugin.zip", "https://example.com/plugin.zip#fragment", "https:///plugin.zip"} {
		if err := validatePluginDownloadURL(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if err := validatePluginDownloadURL("https://example.com/plugin.zip"); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"127.0.0.1:443", "[::1]:443", "10.0.0.1:443", "169.254.169.254:443"} {
		if conn, err := dialPluginDownload(context.Background(), "tcp", address); err == nil {
			conn.Close()
			t.Fatalf("private address accepted: %s", address)
		}
	}
}
