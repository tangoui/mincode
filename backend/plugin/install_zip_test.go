package plugin

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type zipTestEntry struct {
	name string
	data []byte
	mode fs.FileMode
}

func writePluginTestZIP(t *testing.T, entries []zipTestEntry) string {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "package.ZIP")
	if err := os.WriteFile(name, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func renamePluginTestPackage(t *testing.T, archive, extension string) string {
	t.Helper()
	name := strings.TrimSuffix(archive, filepath.Ext(archive)) + extension
	if err := os.Rename(archive, name); err != nil {
		t.Fatal(err)
	}
	return name
}

func pluginZIPFixture(t *testing.T, wrapper string) []zipTestEntry {
	t.Helper()
	root := writeTestManifestV2(t, func(m *Manifest) { m.Author = "ZIP author" })
	var entries []zipTestEntry
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		entries = append(entries, zipTestEntry{wrapper + filepath.ToSlash(rel), data, 0o755 | os.ModeSetuid})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestZIPPreviewInstallLifecycle(t *testing.T) {
	for _, fixture := range []struct{ wrapper, extension string }{
		{"", ".min"}, {"package/", ".MIN"}, {"", ".zip"}, {"package/", ".ZIP"},
	} {
		t.Run(fixture.extension+"/"+fixture.wrapper, func(t *testing.T) {
			useTemporaryPluginData(t)
			entries := pluginZIPFixture(t, fixture.wrapper)
			archive := renamePluginTestPackage(t, writePluginTestZIP(t, entries), fixture.extension)
			if err := requirePluginInstallSource(archive); err != nil {
				t.Fatal(err)
			}
			if err := requirePluginZIP(archive); err != nil {
				t.Fatal(err)
			}
			temp := t.TempDir()
			t.Setenv("TMPDIR", temp)
			t.Setenv("TMP", temp)
			t.Setenv("TEMP", temp)
			assertClean := func() {
				t.Helper()
				files, err := os.ReadDir(temp)
				if err != nil || len(files) != 0 {
					t.Fatalf("temporary extraction leaked: %v %v", files, err)
				}
			}
			manager := &pluginManager{records: map[string]*pluginRecord{}}
			preview, err := manager.PreviewInstall(archive)
			if err != nil {
				t.Fatal(err)
			}
			if preview.SourcePath != archive || preview.FileCount != len(entries) || preview.Manifest.Author != "ZIP author" || preview.Installed {
				t.Fatalf("preview: %+v", preview)
			}
			var size int64
			for _, entry := range entries {
				size += int64(len(entry.data))
			}
			if preview.TotalBytes != size {
				t.Fatalf("wrong expanded size: %d", preview.TotalBytes)
			}
			assertClean()
			if len(manager.records) != 0 {
				t.Fatal("preview registered plugin")
			}
			if _, err := os.Stat(pluginsDirectory()); !os.IsNotExist(err) {
				t.Fatalf("preview wrote plugin directory: %v", err)
			}
			if _, found, err := readPluginEnabled(preview.Manifest.ID); err != nil || found {
				t.Fatalf("preview wrote state: %v", err)
			}
			manifest, err := manager.InstallLocal(preview.SourcePath)
			if err != nil {
				t.Fatal(err)
			}
			assertClean()
			record, err := manager.record(manifest.ID)
			if err != nil {
				t.Fatal(err)
			}
			if record.enabled || record.state != pluginStateDisabled || (record.runtime != nil && record.runtime.Generation() != 0) {
				t.Fatal("installation executed or enabled plugin")
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(filepath.Join(record.root, manifest.Runtime.Entry))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm()&0o100 == 0 || info.Mode()&os.ModeSetuid != 0 || info.Mode().Perm()&0o022 != 0 {
					t.Fatalf("unsafe/lost permissions: %v", info.Mode())
				}
			}
			preview, err = manager.PreviewInstall(archive)
			if err != nil || !preview.Installed {
				t.Fatalf("installed detection: %+v %v", preview, err)
			}
			if _, err := manager.InstallLocal(archive); err == nil {
				t.Fatal("duplicate install accepted")
			}
			assertClean()
		})
	}
}

func TestZIPInstallRevalidatesSource(t *testing.T) {
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}}
	entries := pluginZIPFixture(t, "")
	archive := writePluginTestZIP(t, entries)
	if _, err := manager.PreviewInstall(archive); err != nil {
		t.Fatal(err)
	}
	for i := range entries {
		if entries[i].name == manifestFilename {
			entries[i].data = bytes.ReplaceAll(entries[i].data, []byte("ZIP author"), []byte("Changed author"))
		}
	}
	replacement, err := os.ReadFile(writePluginTestZIP(t, entries))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := manager.InstallLocal(archive)
	if err != nil || manifest.Author != "Changed author" {
		t.Fatalf("stale metadata: %+v %v", manifest, err)
	}
	if err := os.WriteFile(archive, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InstallLocal(archive); err == nil {
		t.Fatal("trusted stale archive")
	}
}

func TestZIPRejectsUnsafeEntriesAndRoots(t *testing.T) {
	fixture := pluginZIPFixture(t, "")
	cases := map[string][]zipTestEntry{}
	for _, name := range []string{"../escape", "/absolute", "C:/escape", `a\b`, "a/../b", "a//b", "./b", "NUL", "a.", "x:stream", "a\x00b"} {
		cases[name] = append(append([]zipTestEntry{}, fixture...), zipTestEntry{name, nil, 0o600})
	}
	for _, mode := range []fs.FileMode{os.ModeSymlink, os.ModeNamedPipe, os.ModeDevice, os.ModeSocket} {
		cases[mode.String()] = append(append([]zipTestEntry{}, fixture...), zipTestEntry{"special", nil, mode | 0o600})
	}
	cases["duplicate"] = append(append([]zipTestEntry{}, fixture...), fixture[0])
	cases["case alias"] = []zipTestEntry{{"A/file", nil, 0o600}, {"a/other", nil, 0o600}}
	cases["file parent"] = []zipTestEntry{{"a/b", nil, 0o600}, {"a", nil, 0o600}}
	cases["file child"] = []zipTestEntry{{"a", nil, 0o600}, {"a/b", nil, 0o600}}
	cases["ambiguous"] = append(append([]zipTestEntry{}, fixture...), pluginZIPFixture(t, "wrapper/")...)
	cases["two wrappers"] = append(pluginZIPFixture(t, "one/"), pluginZIPFixture(t, "two/")...)
	cases["wrapper siblings"] = append(pluginZIPFixture(t, "one/"), zipTestEntry{"README", nil, 0o600})
	cases["nested wrapper"] = pluginZIPFixture(t, "one/two/")
	cases["missing manifest"] = []zipTestEntry{{"other", nil, 0o600}}
	cases["empty"] = nil
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			archive := writePluginTestZIP(t, entries)
			temp := t.TempDir()
			t.Setenv("TMPDIR", temp)
			t.Setenv("TMP", temp)
			t.Setenv("TEMP", temp)
			if _, err := inspectInstallSource(archive); err == nil {
				t.Fatal("unsafe ZIP accepted")
			}
			files, err := os.ReadDir(temp)
			if err != nil || len(files) != 0 {
				t.Fatalf("failed preview leaked extraction: %v %v", files, err)
			}
		})
	}
}

func TestZIPBoundsAndCorruption(t *testing.T) {
	for _, kind := range []string{"input", "expanded", "total", "count", "encrypted", "method", "crc", "size", "truncated", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			entries := pluginZIPFixture(t, "")
			switch kind {
			case "expanded":
				entries = append(entries, zipTestEntry{"large", []byte(strings.Repeat("a", pluginInstallMaxBytes+1)), 0o600})
			case "total":
				data := bytes.Repeat([]byte("a"), pluginInstallMaxBytes/2+1)
				entries = append(entries, zipTestEntry{"one", data, 0o600}, zipTestEntry{"two", data, 0o600})
			case "count":
				for i := 0; i <= pluginInstallMaxFiles; i++ {
					entries = append(entries, zipTestEntry{strings.Repeat("x", i%100) + string(rune(0x400+i)), nil, os.ModeDir | 0o700})
					entries[len(entries)-1].name += "/"
				}
			}
			archive := writePluginTestZIP(t, entries)
			data, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			central := bytes.Index(data, []byte{'P', 'K', 1, 2})
			switch kind {
			case "input":
				file, err := os.OpenFile(archive, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				err = file.Truncate(pluginInstallMaxBytes + 1)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "encrypted":
				binary.LittleEndian.PutUint16(data[central+8:], 1)
			case "method":
				binary.LittleEndian.PutUint16(data[central+10:], 99)
			case "crc":
				data[central+16] ^= 0xff
			case "size":
				binary.LittleEndian.PutUint32(data[central+24:], 1)
			case "truncated":
				data = data[:len(data)/2]
			case "invalid":
				data = []byte("not a zip")
			}
			if kind != "input" {
				if err := os.WriteFile(archive, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := inspectInstallSource(archive); err == nil {
				t.Fatal("invalid/unbounded ZIP accepted")
			}
		})
	}
}

func TestZIPActualStreamBudget(t *testing.T) {
	archive := writePluginTestZIP(t, []zipTestEntry{{"data", bytes.Repeat([]byte("a"), 4096), 0o600}})
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	destination := filepath.Join(t.TempDir(), "data")
	n, err := extractPluginZIPFile(reader.File[0], destination, 31)
	if err == nil || n != 32 {
		t.Fatalf("stream not bounded: n=%d err=%v", n, err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Size() != 32 {
		t.Fatalf("unbounded write: %v %v", info, err)
	}
}

func TestSourceProjectInstallsFromPackageRoot(t *testing.T) {
	project := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(project, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("mincode.config.json", `{"packageRoot":"dist-plugin"}`)
	write("dist-plugin/ui/index.html", "<html></html>")

	if _, _, _, err := prepareInstallSource(project); err == nil || !strings.Contains(err.Error(), "build it first") {
		t.Fatalf("unbuilt source project error = %v", err)
	}

	write("dist-plugin/plugin.json", "{}")
	root, source, cleanup, err := prepareInstallSource(project)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if want := filepath.Join(project, "dist-plugin"); root != want || source != want {
		t.Fatalf("resolved root=%q source=%q, want %q", root, source, want)
	}

	write("plugin.json", "{}")
	if root, _, _, err := prepareInstallSource(project); err != nil || root != project {
		t.Fatalf("a directory with its own plugin.json must install as is: root=%q err=%v", root, err)
	}
}

func TestSourceProjectDefaultsToDistPlugin(t *testing.T) {
	for name, config := range map[string]string{"without config": "", "config without packageRoot": `{"uiDevServer":true}`} {
		t.Run(name, func(t *testing.T) {
			project := t.TempDir()
			write := func(path, content string) {
				t.Helper()
				full := filepath.Join(project, path)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("plugin.template.json", "{}")
			if config != "" {
				write("mincode.config.json", config)
			}
			if _, _, _, err := prepareInstallSource(project); err == nil || !strings.Contains(err.Error(), "build it first") {
				t.Fatalf("unbuilt source project error = %v", err)
			}
			write("dist-plugin/plugin.json", "{}")
			root, _, cleanup, err := prepareInstallSource(project)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if want := filepath.Join(project, "dist-plugin"); root != want {
				t.Fatalf("resolved root=%q, want %q", root, want)
			}
		})
	}
}

func TestPlainDirectoryWithoutManifestIsNotASourceProject(t *testing.T) {
	project := t.TempDir()
	if resolved, err := resolvePluginSourceProject(project); err != nil || resolved != project {
		t.Fatalf("resolved=%q err=%v, want the directory itself", resolved, err)
	}
}

func TestSourceProjectPackageRootMustStayInside(t *testing.T) {
	for _, packageRoot := range []string{"..", "../outside", "/tmp"} {
		project := t.TempDir()
		config := `{"packageRoot":` + jsonQuote(packageRoot) + `}`
		if err := os.WriteFile(filepath.Join(project, "mincode.config.json"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := prepareInstallSource(project); err == nil || !strings.Contains(err.Error(), "subdirectory") {
			t.Fatalf("packageRoot %q error = %v", packageRoot, err)
		}
	}
}

func jsonQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
