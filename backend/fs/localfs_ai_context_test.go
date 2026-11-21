package fs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAIContextTreeKeepsRelativeFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "components"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.ts"), []byte("secret content must not be returned"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "components", "App.vue"), []byte("<template />"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := listAIContextTree(root, "src")
	if err != nil {
		t.Fatal(err)
	}
	if result.Truncated {
		t.Fatal("expected small tree not to be truncated")
	}
	if len(result.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %#v", result.Entries)
	}
	if result.Entries[0].Path != "src/components" || !result.Entries[0].IsDir {
		t.Fatalf("expected components directory first, got %#v", result.Entries[0])
	}
	if result.Entries[1].Path != "src/components/App.vue" || result.Entries[1].IsDir {
		t.Fatalf("expected nested file, got %#v", result.Entries[1])
	}
	if result.Entries[2].Path != "src/main.ts" || result.Entries[2].IsDir {
		t.Fatalf("expected main file, got %#v", result.Entries[2])
	}
}

func TestListAIContextTreeSkipsGeneratedDirectories(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		"src/main.ts", "node_modules/pkg/index.js", "vendor/pkg/source.go", "dist/app.js",
		"coverage/report.json", ".git/config", ".venv/bin/python",
	} {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(path), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := listAIContextTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	paths := aiContextPaths(result.Entries)
	if !paths["src/main.ts"] {
		t.Fatalf("expected source file, got %#v", result.Entries)
	}
	for _, ignored := range []string{"node_modules", "vendor", "dist", "coverage", ".git", ".venv"} {
		for path := range paths {
			if path == ignored || strings.HasPrefix(path, ignored+"/") {
				t.Fatalf("expected generated directory %q to be skipped, got %q", ignored, path)
			}
		}
	}
}

func TestListAIContextTreeUsesGitIgnoreRules(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	files := map[string]string{
		".gitignore":      "ignored.log\ncache/\n",
		"src/tracked.ts":  "tracked",
		"src/visible.ts":  "visible",
		"ignored.log":     "ignored",
		"cache/data.json": "ignored directory",
	}
	for path, content := range files {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if output, err := exec.Command("git", "-C", root, "add", ".gitignore", "src/tracked.ts").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}

	result, err := listAIContextTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	paths := aiContextPaths(result.Entries)
	for _, expected := range []string{".gitignore", "src", "src/tracked.ts", "src/visible.ts"} {
		if !paths[expected] {
			t.Fatalf("expected %q in Git-aware tree, got %#v", expected, result.Entries)
		}
	}
	for _, ignored := range []string{"ignored.log", "cache", "cache/data.json"} {
		if paths[ignored] {
			t.Fatalf("expected gitignored path %q to be omitted", ignored)
		}
	}

	nestedResult, err := listAIContextTree(root, "src")
	if err != nil {
		t.Fatal(err)
	}
	nestedPaths := aiContextPaths(nestedResult.Entries)
	for _, expected := range []string{"src/tracked.ts", "src/visible.ts"} {
		if !nestedPaths[expected] {
			t.Fatalf("expected nested Git scan to include %q, got %#v", expected, nestedResult.Entries)
		}
	}
	if nestedPaths[".gitignore"] {
		t.Fatal("expected nested Git scan to stay inside the selected directory")
	}
}

func TestListAIContextTreeReportsEntryLimit(t *testing.T) {
	root := t.TempDir()
	filesDir := filepath.Join(root, "files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxAIContextTreeEntries; index++ {
		path := filepath.Join(filesDir, "file-"+leftPad(index, 4)+".txt")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := listAIContextTree(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Fatal("expected tree to report truncation")
	}
	if len(result.Entries) != maxAIContextTreeEntries {
		t.Fatalf("expected %d entries, got %d", maxAIContextTreeEntries, len(result.Entries))
	}
}

func TestReadAIContextTextFileCompactsLargeFiles(t *testing.T) {
	root := t.TempDir()
	content := append([]byte("HEAD-LINE\n"), bytes.Repeat([]byte("x"), 6*1024*1024)...)
	content = append(content, []byte("\nTAIL-LINE")...)
	if err := os.WriteFile(filepath.Join(root, "large.log"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := readAIContextTextFile(root, "large.log")
	if err != nil {
		t.Fatal(err)
	}
	if result.Size != int64(len(content)) || !result.Truncated {
		t.Fatalf("unexpected result metadata: %#v", result)
	}
	if len(result.Content) > maxAIContextFileContentBytes {
		t.Fatalf("preview exceeded budget: %d", len(result.Content))
	}
	for _, expected := range []string{"HEAD-LINE", "middle of file omitted", "TAIL-LINE"} {
		if !strings.Contains(result.Content, expected) {
			t.Fatalf("expected compact preview to contain %q", expected)
		}
	}
}

func TestListAIContextTreeRejectsPathsOutsideProjectRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := listAIContextTree(root, "../outside"); err == nil {
		t.Fatal("expected path outside project root to be rejected")
	}
}

func aiContextPaths(entries []localFsAIContextEntry) map[string]bool {
	paths := make(map[string]bool, len(entries))
	for _, entry := range entries {
		paths[entry.Path] = true
	}
	return paths
}

func leftPad(value, width int) string {
	text := string(rune('0' + value%10))
	for value /= 10; value > 0; value /= 10 {
		text = string(rune('0'+value%10)) + text
	}
	for len(text) < width {
		text = "0" + text
	}
	return text
}
