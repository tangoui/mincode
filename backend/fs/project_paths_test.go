package fs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanProjectRelativePath(t *testing.T) {
	valid := map[string]string{"a.txt": "a.txt", "dir/./b.txt": "dir/b.txt", `dir\c.txt`: "dir/c.txt", " d/e.txt ": "d/e.txt"}
	for input, want := range valid {
		got, err := CleanProjectRelativePath(input)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"", ".", "/abs.txt", "../x", "a/../../x", "a/../b", "a\x00b", `\\server\share`} {
		if _, err := CleanProjectRelativePath(input); !errors.Is(err, ErrProjectPathInvalid) {
			t.Errorf("%q: expected ErrProjectPathInvalid, got %v", input, err)
		}
	}
}

func TestResolveProjectFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "sub", "a.txt"), filepath.Join(root, "alias.txt")); err != nil {
		t.Fatal(err)
	}

	file, err := ResolveProjectFile(root, "sub/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, _ := filepath.EvalSymlinks(root)
	if file.Root != canonicalRoot || file.RelativePath != "sub/a.txt" || file.ResolvedRelativePath != "sub/a.txt" ||
		file.Path != filepath.Join(canonicalRoot, "sub", "a.txt") {
		t.Fatalf("unexpected %+v", file)
	}
	file, err = ResolveProjectFile(root, "alias.txt")
	if err != nil || file.ResolvedRelativePath != "sub/a.txt" || file.RelativePath != "alias.txt" {
		t.Fatalf("alias %+v %v", file, err)
	}
	if _, err := ResolveProjectFile(root, "escape.txt"); !errors.Is(err, ErrProjectPathEscapesRoot) {
		t.Fatalf("escape: %v", err)
	}
	if _, err := ResolveProjectFile(root, "missing.txt"); !IsProjectPathNotExist(err) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := ResolveProjectFile(root, "../x"); !errors.Is(err, ErrProjectPathInvalid) {
		t.Fatalf("dotdot: %v", err)
	}
	if _, err := ResolveProjectFile(filepath.Join(root, "sub", "a.txt"), "x"); err == nil {
		t.Fatal("file root accepted")
	}
}
