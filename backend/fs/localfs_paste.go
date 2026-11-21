package fs

import (
	"mincode/app/backend/core"

	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// local_fs_paste_files：把剪贴板里的文件（绝对路径，可来自项目外）复制进项目目录。
// 目标目录限制在项目根内；重名时按 Finder 习惯自动改名为 "名称 copy.ext"。
//
// 命令契约：
//
//	local_fs_paste_files {projectRoot, destDir, srcPaths[]} -> {pasted, failed}
func init() {
	core.Register("local_fs_paste_files", handlePasteFiles)
}

const pasteMaxSources = 100

func handlePasteFiles(_ string, argsJson string) (string, error) {
	var a struct {
		ProjectRoot string   `json:"projectRoot"`
		DestDir     string   `json:"destDir"`
		SrcPaths    []string `json:"srcPaths"`
	}
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if len(a.SrcPaths) == 0 {
		return "", fmt.Errorf("paste: empty_sources")
	}
	if len(a.SrcPaths) > pasteMaxSources {
		return "", fmt.Errorf("paste: too_many_sources")
	}
	root, err := canonicalLocalProjectRoot(a.ProjectRoot)
	if err != nil {
		return "", err
	}
	destDir, err := resolveUnderRoot(root, a.DestDir)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(destDir); statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("paste: dest_not_a_directory")
	}

	pasted := 0
	failed := 0
	var firstErr error
	for _, raw := range a.SrcPaths {
		src := filepath.Clean(strings.TrimSpace(raw))
		if src == "" || !filepath.IsAbs(src) {
			failed++
			continue
		}
		canon, resolveErr := filepath.EvalSymlinks(src)
		if resolveErr != nil {
			failed++
			if firstErr == nil {
				firstErr = resolveErr
			}
			continue
		}
		if canon == root {
			failed++
			continue
		}
		// 拒绝把目录粘贴进它自身或它的子树。
		if withinRoot(canon, destDir) {
			failed++
			continue
		}
		dest := uniquePasteDest(destDir, filepath.Base(canon))
		info, _ := os.Lstat(canon)
		rel, _ := filepath.Rel(root, dest)
		success := prepareMutation(root, "paste", "", rel, "", dest, info != nil && info.IsDir())
		if err := copyPath(canon, dest); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		success()
		pasted++
	}
	if pasted == 0 && firstErr != nil {
		return "", fmt.Errorf("paste: %w", firstErr)
	}
	return core.EncodeResult(struct {
		Pasted int `json:"pasted"`
		Failed int `json:"failed"`
	}{Pasted: pasted, Failed: failed})
}

// uniquePasteDest 在目标目录中为 name 找一个不冲突的名字：
// 冲突时依次尝试 "name copy"、"name copy 2"、"name copy 3"……
func uniquePasteDest(dir, name string) string {
	candidate := filepath.Join(dir, name)
	if _, err := os.Lstat(candidate); err != nil {
		return candidate
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // ".env" 这类以点开头的名字
		stem = name
		ext = ""
	}
	for i := 1; i <= 5000; i++ {
		suffix := " copy"
		if i > 1 {
			suffix = fmt.Sprintf(" copy %d", i)
		}
		candidate = filepath.Join(dir, stem+suffix+ext)
		if _, err := os.Lstat(candidate); err != nil {
			return candidate
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-paste-%d%s", stem, os.Getpid(), ext))
}
