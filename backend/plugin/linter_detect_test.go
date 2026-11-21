package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func detectTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".stylelintrc.json")           // root, hidden file
	write("web/eslint.config.js")        // first-level subdirectory
	write(".github/workflows/ci.yml")    // nested path
	write("node_modules/x/.rubocop.yml") // skipped directory
	write("deep/inner/.swiftlint.yml")   // second level: not scanned
	write(".hidden/.luacheckrc")         // hidden directories are not scanned
	return root
}

func TestDetectLinterProjectFiles(t *testing.T) {
	root := detectTestRoot(t)
	candidates := []linterDetectCandidate{
		{Ref: "p/stylelint", Files: []string{"stylelint.config.js", ".stylelintrc.json"}},
		{Ref: "p/eslint", Files: []string{"eslint.config.js"}},
		{Ref: "p/actions", Files: []string{".github/workflows"}},
		{Ref: "p/rubocop", Files: []string{".rubocop.yml"}},
		{Ref: "p/swiftlint", Files: []string{".swiftlint.yml"}},
		{Ref: "p/luacheck", Files: []string{".luacheckrc"}},
		{Ref: "p/none", Files: []string{"missing.toml", "web/missing"}},
	}
	got := detectLinterProjectFiles(root, candidates)
	want := []string{"p/stylelint", "p/eslint", "p/actions"}
	if !slices.Equal(got, want) {
		t.Fatalf("detected %v, want %v", got, want)
	}
	if got := detectLinterProjectFiles("", candidates); len(got) != 0 {
		t.Fatalf("no root detected %v", got)
	}
	if got := detectLinterProjectFiles(filepath.Join(root, "missing"), candidates); len(got) != 0 {
		t.Fatalf("missing root detected %v", got)
	}
}

func TestDetectLinterProjectFilesDoesNotFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, ".rubocop.yml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A linked subdirectory is not scanned, and a linked intermediate
	// directory never satisfies a nested path.
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".github")); err != nil {
		t.Fatal(err)
	}
	got := detectLinterProjectFiles(root, []linterDetectCandidate{
		{Ref: "p/rubocop", Files: []string{".rubocop.yml"}},
		{Ref: "p/actions", Files: []string{".github/workflows"}},
	})
	if len(got) != 0 {
		t.Fatalf("followed a symlink: %v", got)
	}
}

func TestDetectLinterProjectFilesBoundsSubdirectories(t *testing.T) {
	root := t.TempDir()
	for index := range linterDetectSubdirsMax + 10 {
		if err := os.MkdirAll(filepath.Join(root, "d"+string(rune('a'+index%26))+string(rune('a'+index/26%26))+string(rune('a'+index/676))), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dirs := linterDetectDirs(root)
	if len(dirs) != linterDetectSubdirsMax+1 || dirs[0] != root {
		t.Fatalf("scanned %d directories", len(dirs))
	}
}

func TestManagerDetectsLintersOfActivePlugins(t *testing.T) {
	root := detectTestRoot(t)
	manager, record := linterInvokeManager(t, LinterContribution{ID: "eslint", Languages: []string{"javascript"}, Action: "run",
		Triggers: []string{"open"}, Detect: &LinterDetect{Files: []string{"eslint.config.js"}}})
	record.mu.Lock()
	record.manifest.contributions[linterPointKey].Entries = append(record.manifest.contributions[linterPointKey].Entries,
		LinterContribution{ID: "plain", Languages: []string{"json"}, Action: "run", Triggers: []string{"open"}})
	record.mu.Unlock()
	if err := manager.SetWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	result := manager.detectLinters()
	if result.Root == "" || !slices.Equal(result.Linters, []string{record.id + "/eslint"}) {
		t.Fatalf("result = %+v", result)
	}
	if err := manager.SetWorkspaceRoot(""); err != nil {
		t.Fatal(err)
	}
	if result := manager.detectLinters(); result.Root != "" || len(result.Linters) != 0 {
		t.Fatalf("without a root = %+v", result)
	}
}
