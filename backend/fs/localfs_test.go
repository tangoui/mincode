package fs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTextFileArgs(t *testing.T, root, filePath, content string, createOnly bool) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"projectRoot": root, "filePath": filePath, "content": content, "createOnly": createOnly})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// createOnly（插件文件模板）只新建：独占创建、权限 0644、不跟随 symlink、不覆盖。
func TestWriteTextFileCreateOnly(t *testing.T) {
	root := t.TempDir()
	if _, err := handleWriteTextFile("", writeTextFileArgs(t, root, "src/nested/app.ts", "export {}\n", true)); err != nil {
		t.Fatalf("create: %v", err)
	}
	target := filepath.Join(root, "src", "nested", "app.ts")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 != 0 || info.Mode().Perm()&^0o644 != 0 {
		t.Fatalf("mode = %v, want at most 0644 and never executable", info.Mode().Perm())
	}

	// An existing file is never overwritten.
	if _, err := handleWriteTextFile("", writeTextFileArgs(t, root, "src/nested/app.ts", "changed", true)); err == nil || !strings.Contains(err.Error(), "already_exists") {
		t.Fatalf("existing file err = %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "export {}\n" {
		t.Fatalf("existing file was modified: %q", data)
	}
	// An existing directory is refused.
	if _, err := handleWriteTextFile("", writeTextFileArgs(t, root, "src", "x", true)); err == nil || !strings.Contains(err.Error(), "already_exists") {
		t.Fatalf("existing directory err = %v", err)
	}

	// An existing symlink (even a dangling one) is refused and not followed.
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := handleWriteTextFile("", writeTextFileArgs(t, root, "link.txt", "x", true)); err == nil || !strings.Contains(err.Error(), "already_exists") {
		t.Fatalf("symlink err = %v", err)
	}
	if _, err := os.Lstat(outside); !os.IsNotExist(err) {
		t.Fatalf("symlink target was created: %v", err)
	}

	// A parent directory that is a symlink out of the root is rejected.
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := handleWriteTextFile("", writeTextFileArgs(t, root, "escape/new.txt", "x", true)); err == nil || !strings.Contains(err.Error(), "path_escapes_root") {
		t.Fatalf("escape err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("file created outside the root: %v", err)
	}
	// ".." is clamped to the root, never above it.
	if _, err := handleWriteTextFile("", writeTextFileArgs(t, root, "../../clamped.txt", "x", true)); err != nil {
		t.Fatalf("clamped create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "clamped.txt")); err != nil {
		t.Fatalf("clamped file not inside root: %v", err)
	}
	// The root itself is never a create target.
	if _, err := handleWriteTextFile("", writeTextFileArgs(t, root, "", "x", true)); err == nil {
		t.Fatal("root create accepted")
	}
}

// Without createOnly the existing overwrite behaviour is unchanged.
func TestWriteTextFileOverwriteUnchanged(t *testing.T) {
	root := t.TempDir()
	for _, content := range []string{"one", "two"} {
		if _, err := handleWriteTextFile("", writeTextFileArgs(t, root, "a.txt", content, false)); err != nil {
			t.Fatal(err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "two" {
		t.Fatalf("content = %q", data)
	}
}
