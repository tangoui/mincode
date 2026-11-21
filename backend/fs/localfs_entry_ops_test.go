package fs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func entryOpArgs(t *testing.T, fields map[string]any) string {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// symlinkFixture 构造：root/releases/v2/app.txt、root/current -> releases/v2（根内）、
// root/ext -> outside（根外）、outside/data.txt。
func symlinkFixture(t *testing.T) (root, outside string) {
	t.Helper()
	root = t.TempDir()
	outside = t.TempDir()
	mustWriteFile(t, filepath.Join(root, "releases", "v2", "app.txt"), "app")
	mustWriteFile(t, filepath.Join(outside, "data.txt"), "data")
	if err := os.Symlink(filepath.Join(root, "releases", "v2"), filepath.Join(root, "current")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := os.Symlink(outside, filepath.Join(root, "ext")); err != nil {
		t.Skip("symlinks unavailable")
	}
	return root, outside
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s still exists (err=%v)", path, err)
	}
}

func assertSymlink(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink (mode %v)", path, info.Mode())
	}
}

func assertErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), code) {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func TestDeleteSymlinkRemovesOnlyLink(t *testing.T) {
	root, outside := symlinkFixture(t)

	for _, link := range []string{"current", "ext"} {
		if _, err := handleDelete("", entryOpArgs(t, map[string]any{"projectRoot": root, "path": link})); err != nil {
			t.Fatalf("delete %s: %v", link, err)
		}
		assertNotExists(t, filepath.Join(root, link))
	}
	assertFileContent(t, filepath.Join(root, "releases", "v2", "app.txt"), "app")
	assertFileContent(t, filepath.Join(outside, "data.txt"), "data")
}

func TestDeleteKeepsRootAndEscapeProtection(t *testing.T) {
	root, outside := symlinkFixture(t)

	for _, p := range []string{"", ".", "/", "releases/.."} {
		_, err := handleDelete("", entryOpArgs(t, map[string]any{"projectRoot": root, "path": p}))
		assertErrorCode(t, err, "cannot_delete_project_root")
	}
	// 父目录经 symlink 逃逸到根外时拒绝。
	_, err := handleDelete("", entryOpArgs(t, map[string]any{"projectRoot": root, "path": "ext/data.txt"}))
	assertErrorCode(t, err, "path_escapes_root")
	assertFileContent(t, filepath.Join(outside, "data.txt"), "data")

	// 父目录 symlink 仍在根内时，作用于真实条目。
	if _, err := handleDelete("", entryOpArgs(t, map[string]any{"projectRoot": root, "path": "current/app.txt"})); err != nil {
		t.Fatalf("delete through in-root link: %v", err)
	}
	assertNotExists(t, filepath.Join(root, "releases", "v2", "app.txt"))
}

func TestRenameSymlinkRenamesOnlyLink(t *testing.T) {
	root, outside := symlinkFixture(t)

	cases := map[string]string{"current": "previous", "ext": "ext-renamed"}
	for oldName, newName := range cases {
		if _, err := handleRename("", entryOpArgs(t, map[string]any{"projectRoot": root, "oldPath": oldName, "newName": newName})); err != nil {
			t.Fatalf("rename %s: %v", oldName, err)
		}
		assertNotExists(t, filepath.Join(root, oldName))
		assertSymlink(t, filepath.Join(root, newName))
	}
	assertFileContent(t, filepath.Join(root, "releases", "v2", "app.txt"), "app")
	assertFileContent(t, filepath.Join(root, "previous", "app.txt"), "app")
	assertFileContent(t, filepath.Join(outside, "data.txt"), "data")
	assertFileContent(t, filepath.Join(root, "ext-renamed", "data.txt"), "data")

	_, err := handleRename("", entryOpArgs(t, map[string]any{"projectRoot": root, "oldPath": "", "newName": "x"}))
	assertErrorCode(t, err, "cannot_rename_project_root")
}

func TestMoveSymlinkMovesOnlyLink(t *testing.T) {
	root, outside := symlinkFixture(t)
	if err := os.Mkdir(filepath.Join(root, "archive"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, link := range []string{"current", "ext"} {
		if _, err := handleMoveTo("", entryOpArgs(t, map[string]any{"projectRoot": root, "srcPath": link, "destDir": "archive"})); err != nil {
			t.Fatalf("move %s: %v", link, err)
		}
		assertNotExists(t, filepath.Join(root, link))
		assertSymlink(t, filepath.Join(root, "archive", link))
	}
	assertFileContent(t, filepath.Join(root, "releases", "v2", "app.txt"), "app")
	assertFileContent(t, filepath.Join(outside, "data.txt"), "data")
}

func TestCopySymlinkCopiesTargetUnderLinkName(t *testing.T) {
	root, _ := symlinkFixture(t)
	if err := os.Mkdir(filepath.Join(root, "backup"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := handleCopyTo("", entryOpArgs(t, map[string]any{"projectRoot": root, "srcPath": "current", "destDir": "backup"})); err != nil {
		t.Fatalf("copy: %v", err)
	}
	info, err := os.Lstat(filepath.Join(root, "backup", "current"))
	if err != nil || !info.IsDir() {
		t.Fatalf("copied entry is not a real dir: info=%v err=%v", info, err)
	}
	assertFileContent(t, filepath.Join(root, "backup", "current", "app.txt"), "app")
	assertSymlink(t, filepath.Join(root, "current"))

	// 指向根外的链接不能作为复制源。
	_, err = handleCopyTo("", entryOpArgs(t, map[string]any{"projectRoot": root, "srcPath": "ext", "destDir": "backup"}))
	assertErrorCode(t, err, "path_escapes_root")
}

func TestCopyOrMoveRejectsDestinationInsideSource(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a", "b", "file.txt"), "x")

	for _, move := range []bool{false, true} {
		for _, destDir := range []string{"a", "a/b"} {
			_, err := handleCopyOrMove(entryOpArgs(t, map[string]any{"projectRoot": root, "srcPath": "a", "destDir": destDir}), move)
			assertErrorCode(t, err, "dest_inside_source")
		}
	}
	assertNotExists(t, filepath.Join(root, "a", "a"))
	assertNotExists(t, filepath.Join(root, "a", "b", "a"))
	assertFileContent(t, filepath.Join(root, "a", "b", "file.txt"), "x")

	// 经 symlink 复制同样拒绝复制到链接目标内部。
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	_, err := handleCopyTo("", entryOpArgs(t, map[string]any{"projectRoot": root, "srcPath": "link", "destDir": "a/b"}))
	assertErrorCode(t, err, "dest_inside_source")
	assertNotExists(t, filepath.Join(root, "a", "b", "link"))

	// 同级目录之间的正常复制仍然可用。
	if err := os.Mkdir(filepath.Join(root, "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := handleCopyTo("", entryOpArgs(t, map[string]any{"projectRoot": root, "srcPath": "a", "destDir": "c"})); err != nil {
		t.Fatalf("copy a -> c: %v", err)
	}
	assertFileContent(t, filepath.Join(root, "c", "a", "b", "file.txt"), "x")
}
