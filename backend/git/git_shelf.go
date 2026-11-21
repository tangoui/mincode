package git

import (
	"mincode/app/backend/core"

	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const gitShelfPatchMaxBytes = 50 * 1024 * 1024

type gitShelfEntry struct {
	ID        string         `json:"id"`
	Message   string         `json:"message"`
	CreatedAt int64          `json:"created_at"`
	Files     []gitFileEntry `json:"files"`
}

type shelfCreateArgs struct {
	RepoPath string   `json:"repoPath"`
	Message  *string  `json:"message"`
	Paths    []string `json:"paths"`
}

type shelfIDArgs struct {
	RepoPath string `json:"repoPath"`
	ShelfID  string `json:"shelfId"`
}

func init() {
	core.Register("git_shelf_list", gitShelfListHandler)
	core.Register("git_shelf_create", gitShelfCreateHandler)
	core.Register("git_shelf_apply", gitShelfApplyHandler)
	core.Register("git_shelf_delete", gitShelfDeleteHandler)
}

func gitShelfListHandler(_, argsJSON string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	dir, err := gitShelfDirectory(a.RepoPath)
	if err != nil {
		return "", err
	}
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return core.EncodeResult([]gitShelfEntry{})
	}
	if err != nil {
		return "", err
	}
	entries := make([]gitShelfEntry, 0, len(files))
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, file.Name()))
		if readErr != nil {
			continue
		}
		var entry gitShelfEntry
		if json.Unmarshal(data, &entry) == nil && validShelfID(entry.ID) {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt > entries[j].CreatedAt })
	return core.EncodeResult(entries)
}

func gitShelfCreateHandler(_, argsJSON string) (string, error) {
	var a shelfCreateArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	message := "Shelf"
	if a.Message != nil && strings.TrimSpace(*a.Message) != "" {
		message = strings.TrimSpace(*a.Message)
	}
	now := time.Now()
	id := fmt.Sprintf("%d", now.UnixNano())
	temporaryMessage := "mincode-shelf:" + id
	var selectedPaths *[]string
	if a.Paths != nil {
		selectedPaths = &a.Paths
	}
	stashBefore, err := gitStashTip(a.RepoPath)
	if err != nil {
		return "", err
	}
	files, err := gitCreateStash(a.RepoPath, temporaryMessage, "搁置", selectedPaths)
	if err != nil {
		return "", err
	}
	stashSHA, err := gitCreatedShelfStash(a.RepoPath, stashBefore, temporaryMessage)
	if err != nil {
		return "", err
	}

	patch, err := runGitWithLimit(a.RepoPath, gitDefaultTimeout, gitShelfPatchMaxBytes, "stash", "show", "--binary", "--include-untracked", "--patch", stashSHA)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(patch, "git stash show"); err != nil {
		return "", err
	}
	if patch.truncated {
		return "", fmt.Errorf("搁置内容超过 %d MiB，改动已保留在 Git Stash 中", gitShelfPatchMaxBytes/(1024*1024))
	}
	if len(patch.stdout) == 0 {
		return "", fmt.Errorf("无法生成搁置补丁，改动已保留在 Git Stash 中")
	}

	dir, err := gitShelfDirectory(a.RepoPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	entry := gitShelfEntry{ID: id, Message: message, CreatedAt: now.UnixMilli(), Files: files}
	metadata, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	patchPath, metadataPath := gitShelfPaths(dir, id)
	if err := writeShelfFile(patchPath, patch); err != nil {
		return "", err
	}
	if err := writeShelfBytes(metadataPath, metadata); err != nil {
		_ = os.Remove(patchPath)
		return "", err
	}

	// Stash 栈可能已被其他进程改变，drop 前按 SHA 重新定位 stash@{n}。
	stashRef := ""
	stashes, err := gitListStashes(a.RepoPath)
	if err != nil {
		return "", fmt.Errorf("搁置已保存，但临时 Stash 清理失败：%w", err)
	}
	for _, stash := range stashes {
		if stash.sha == stashSHA {
			stashRef = stash.ref
			break
		}
	}
	if stashRef == "" {
		return "", fmt.Errorf("搁置已保存，但未在 Stash 列表中找到临时 Stash %s", stashSHA)
	}
	dropped, err := runGit(a.RepoPath, gitDefaultTimeout, "stash", "drop", stashRef)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(dropped, "git stash drop"); err != nil {
		return "", fmt.Errorf("搁置已保存，但临时 Stash 清理失败：%w", err)
	}
	return core.EncodeResult(entry)
}

type gitStashRecord struct {
	ref     string
	sha     string
	subject string
}

// gitStashTip 返回 refs/stash 当前指向的提交；尚无 Stash 时返回空字符串。
func gitStashTip(repoPath string) (string, error) {
	o, err := runGit(repoPath, gitDefaultTimeout, "rev-parse", "-q", "--verify", "refs/stash")
	if err != nil {
		return "", err
	}
	if !o.timedOut && o.exitCode == 1 && strings.TrimSpace(gitStdoutString(o)) == "" {
		return "", nil
	}
	if err := gitEnsureSuccess(o, "git rev-parse refs/stash"); err != nil {
		return "", err
	}
	return strings.TrimSpace(gitStdoutString(o)), nil
}

func gitListStashes(repoPath string) ([]gitStashRecord, error) {
	o, err := runGit(repoPath, gitDefaultTimeout, "stash", "list", "--format=%gd%x1f%H%x1f%s")
	if err != nil {
		return nil, err
	}
	if err := gitEnsureSuccess(o, "git stash list"); err != nil {
		return nil, err
	}
	var records []gitStashRecord
	for _, line := range strings.Split(gitStdoutString(o), "\n") {
		parts := strings.SplitN(strings.TrimRight(line, "\r"), "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		records = append(records, gitStashRecord{ref: parts[0], sha: parts[1], subject: parts[2]})
	}
	return records, nil
}

// gitCreatedShelfStash 确认 stash push 确实新建了 Stash，并按唯一临时消息返回其 SHA。
// push 可能未保存任何内容（退出码仍为 0），或其他进程同时 push，因此不能假定 stash@{0} 属于本次搁置。
func gitCreatedShelfStash(repoPath, before, temporaryMessage string) (string, error) {
	after, err := gitStashTip(repoPath)
	if err != nil {
		return "", err
	}
	if after == "" || after == before {
		return "", fmt.Errorf("Git 未创建新的 Stash（可能没有可保存的更改），已取消搁置")
	}
	stashes, err := gitListStashes(repoPath)
	if err != nil {
		return "", err
	}
	for _, stash := range stashes {
		if stash.subject == temporaryMessage || strings.HasSuffix(stash.subject, ": "+temporaryMessage) {
			return stash.sha, nil
		}
	}
	return "", fmt.Errorf("未找到本次搁置创建的 Stash，已取消搁置")
}

func gitShelfApplyHandler(_, argsJSON string) (string, error) {
	var a shelfIDArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	if !validShelfID(a.ShelfID) {
		return "", fmt.Errorf("无效的搁置 ID")
	}
	dir, err := gitShelfDirectory(a.RepoPath)
	if err != nil {
		return "", err
	}
	patchPath, _ := gitShelfPaths(dir, a.ShelfID)
	patch, err := os.ReadFile(patchPath)
	if err != nil {
		return "", err
	}
	applied, err := runGitStdin(a.RepoPath, string(patch), gitDefaultTimeout, "apply", "--3way", "--whitespace=nowarn")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(applied, "git apply"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func gitShelfDeleteHandler(_, argsJSON string) (string, error) {
	var a shelfIDArgs
	if err := core.DecodeArgs(argsJSON, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	if !validShelfID(a.ShelfID) {
		return "", fmt.Errorf("无效的搁置 ID")
	}
	dir, err := gitShelfDirectory(a.RepoPath)
	if err != nil {
		return "", err
	}
	patchPath, metadataPath := gitShelfPaths(dir, a.ShelfID)
	if err := os.Remove(patchPath); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Remove(metadataPath); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return core.EncodeResult(nil)
}

func gitShelfDirectory(repoPath string) (string, error) {
	output, err := runGit(repoPath, gitDefaultTimeout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(output, "git rev-parse --git-common-dir"); err != nil {
		return "", err
	}
	commonDir := strings.TrimSpace(string(output.stdout))
	if commonDir == "" {
		return "", fmt.Errorf("无法定位 Git 元数据目录")
	}
	return filepath.Join(commonDir, "mincode", "shelves"), nil
}

func gitShelfPaths(dir, id string) (string, string) {
	return filepath.Join(dir, id+".patch"), filepath.Join(dir, id+".json")
}

func validShelfID(id string) bool {
	if id == "" {
		return false
	}
	for _, char := range id {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func writeShelfFile(path string, output gitOutput) error {
	return writeShelfBytes(path, output.stdout)
}

func writeShelfBytes(path string, data []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
