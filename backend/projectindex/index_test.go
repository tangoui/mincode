package projectindex

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func resetStore() {
	store.mu.Lock()
	store.roots = map[string]rootIndex{}
	store.mu.Unlock()
}

func writeTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshIndexesMetadataAndIgnoresDirectoriesAndSymlinks(t *testing.T) {
	resetStore()
	root := t.TempDir()
	writeTestFile(t, root, "src/main.ts", "main")
	writeTestFile(t, root, "node_modules/pkg/index.js", "ignored")
	writeTestFile(t, root, ".git/config", "ignored")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "src"), filepath.Join(root, "linked-src")); err != nil {
			t.Fatal(err)
		}
	}

	result, err := refreshRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Roots) != 1 || result.Roots[0].Files != 1 || result.Roots[0].Changed != 1 {
		t.Fatalf("unexpected refresh result: %#v", result)
	}
	files, err := searchFiles([]string{root}, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "src/main.ts" || files[0].Size != 4 {
		t.Fatalf("unexpected index entries: %#v", files)
	}
}

func TestRefreshReportsAddedUpdatedAndDeletedFiles(t *testing.T) {
	resetStore()
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "a")
	writeTestFile(t, root, "b.txt", "b")
	first, err := refreshRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if first.Roots[0].Changed != 2 {
		t.Fatalf("first changed=%d", first.Roots[0].Changed)
	}

	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	writeTestFile(t, root, "b.txt", "updated")
	writeTestFile(t, root, "c.txt", "c")
	second, err := refreshRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if second.Roots[0].Changed != 3 {
		t.Fatalf("second changed=%d, want 3", second.Roots[0].Changed)
	}
}

func TestSearchRanksExactPrefixContainsAndFuzzyMatches(t *testing.T) {
	resetStore()
	root := t.TempDir()
	writeTestFile(t, root, "src/app.ts", "app")
	writeTestFile(t, root, "src/apple.ts", "apple")
	writeTestFile(t, root, "docs/my-app-guide.md", "guide")
	writeTestFile(t, root, "src/application.test.ts", "test")
	if _, err := refreshRoots([]string{root}); err != nil {
		t.Fatal(err)
	}

	files, err := searchFiles([]string{root}, "app.ts", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 || files[0].Name != "app.ts" {
		t.Fatalf("exact match not first: %#v", files)
	}
	files, err = searchFiles([]string{root}, "appl", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 || files[0].Name != "apple.ts" {
		t.Fatalf("prefix match not first: %#v", files)
	}
	files, err = searchFiles([]string{root}, "mappg", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "my-app-guide.md" {
		t.Fatalf("fuzzy match failed: %#v", files)
	}
}

func TestRefreshOnlyUpdatesSpecifiedRoots(t *testing.T) {
	resetStore()
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	writeTestFile(t, firstRoot, "first.go", "first")
	writeTestFile(t, secondRoot, "second.go", "second")
	if _, err := refreshRoots([]string{firstRoot, secondRoot}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, firstRoot, "changed.go", "changed")
	if _, err := refreshRoots([]string{firstRoot}); err != nil {
		t.Fatal(err)
	}
	files, err := searchFiles([]string{secondRoot}, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "second.go" {
		t.Fatalf("refreshing one root removed another index: %#v", files)
	}
}

func TestScanRootGitSkipsSensitiveGeneratedAndSymlinkPaths(t *testing.T) {
	resetStore()
	root := t.TempDir()
	writeTestFile(t, root, "src/main.go", "main")
	writeTestFile(t, root, ".env", "secret")
	writeTestFile(t, root, "node_modules/pkg/index.js", "generated")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "src/main.go"), filepath.Join(root, "src/linked.go")); err != nil {
			t.Fatal(err)
		}
	}
	if err := runGit(t, root, "init", "-q"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	if err := runGit(t, root, "add", "src/main.go"); err != nil {
		t.Fatal(err)
	}
	result, err := refreshRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Roots[0].Files != 1 {
		t.Fatalf("git scan indexed excluded path: %#v", result)
	}
	files, err := searchFiles([]string{root}, "", 20)
	if err != nil || len(files) != 1 || files[0].Path != "src/main.go" {
		t.Fatalf("unexpected git index entries: %#v, %v", files, err)
	}
}

func runGit(t *testing.T, root string, args ...string) error {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	return command.Run()
}

func TestRejectsRelativeRootsAndCapsSearchLimit(t *testing.T) {
	resetStore()
	if _, err := refreshRoots([]string{"relative"}); err == nil {
		t.Fatal("expected relative root error")
	}
	roots := make([]string, maxRoots+1)
	for index := range roots {
		roots[index] = filepath.Join(string(filepath.Separator), "tmp", "root")
	}
	if _, err := searchFiles(roots, "", maxSearchLimit+1); err == nil {
		t.Fatal("expected too many roots error")
	}
}
