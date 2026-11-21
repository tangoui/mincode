package fs

import (
	"mincode/app/backend/core"

	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 覆盖 local_fs_paste_files 的核心场景：外部文件粘贴、重名自动改名、
// 目录粘贴以及拒绝把目录粘贴进自身子树。
func TestHandlePasteFiles(t *testing.T) {
	tmp := t.TempDir()
	projectRoot := filepath.Join(tmp, "project")
	if err := os.MkdirAll(filepath.Join(projectRoot, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "dir", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTemp := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTemp(filepath.Join(outside, "a.txt"), "hello")
	writeTemp(filepath.Join(outside, "dir", "nested", "b.txt"), "world")
	writeTemp(filepath.Join(projectRoot, "sub", "a.txt"), "existing")

	paste := func(destDir string, srcs ...string) (pasted, failed int, err error) {
		args, _ := json.Marshal(map[string]any{
			"projectRoot": projectRoot,
			"destDir":     destDir,
			"srcPaths":    srcs,
		})
		out, err := core.Dispatch("local_fs_paste_files", string(args))
		if err != nil {
			return 0, 0, err
		}
		var result struct {
			Pasted int `json:"pasted"`
			Failed int `json:"failed"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			return 0, 0, err
		}
		return result.Pasted, result.Failed, nil
	}

	// 1. 外部文件粘贴到子目录；目标已有同名文件时自动改名。
	pasted, failed, err := paste("sub", filepath.Join(outside, "a.txt"))
	if err != nil || pasted != 1 || failed != 0 {
		t.Fatalf("paste file: pasted=%d failed=%d err=%v", pasted, failed, err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "sub", "a copy.txt")); err != nil {
		t.Fatalf("expected renamed copy: %v", err)
	}

	// 2. 再次粘贴同一文件，出现第二个冲突名。
	if _, _, err := paste("sub", filepath.Join(outside, "a.txt")); err != nil {
		t.Fatalf("second paste: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "sub", "a copy 2.txt")); err != nil {
		t.Fatalf("expected second renamed copy: %v", err)
	}

	// 3. 目录整体粘贴到项目根。
	pasted, failed, err = paste("", filepath.Join(outside, "dir"))
	if err != nil || pasted != 1 || failed != 0 {
		t.Fatalf("paste dir: pasted=%d failed=%d err=%v", pasted, failed, err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "dir", "nested", "b.txt")); err != nil {
		t.Fatalf("expected nested file: %v", err)
	}

	// 4. 拒绝把目录粘贴进它自身的子树（计为失败项）。
	pasted, failed, err = paste("dir/nested", filepath.Join(projectRoot, "dir"))
	if err != nil {
		t.Fatalf("self-subtree paste should degrade to failure count, got err=%v", err)
	}
	if pasted != 0 || failed != 1 {
		t.Fatalf("self-subtree paste: pasted=%d failed=%d", pasted, failed)
	}

	// 5. 不存在的来源计为失败；全部失败时返回错误。
	if _, _, err := paste("", filepath.Join(tmp, "missing.txt")); err == nil {
		t.Fatal("expected error when nothing pasted")
	}
}

func TestUniquePasteDest(t *testing.T) {
	dir := t.TempDir()
	if got := uniquePasteDest(dir, "x.txt"); got != filepath.Join(dir, "x.txt") {
		t.Fatalf("no conflict should keep name, got %s", got)
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("x.txt")
	if got := uniquePasteDest(dir, "x.txt"); got != filepath.Join(dir, "x copy.txt") {
		t.Fatalf("first conflict got %s", got)
	}
	write("x copy.txt")
	if got := uniquePasteDest(dir, "x.txt"); got != filepath.Join(dir, "x copy 2.txt") {
		t.Fatalf("second conflict got %s", got)
	}
	write(".env")
	if got := uniquePasteDest(dir, ".env"); got != filepath.Join(dir, ".env copy") {
		t.Fatalf("dotfile conflict got %s", got)
	}
}
