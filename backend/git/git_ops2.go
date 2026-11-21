// Git 域补充命令：worktree / stash push-pop-drop / 分支删除重命名 / tag 增删 /
// cherry-pick / reset / revert 变更块 / interactive rebase / 冲突读写 / changelog。
//
// 契约与 git_ops.go 一致：入参 camelCase，返回结构 snake_case。
package git

import (
	"mincode/app/backend/core"

	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func init() {
	core.Register("git_stash_push", gitStashPushHandler)
	core.Register("git_stash_pop", gitStashPopHandler)
	core.Register("git_stash_drop", gitStashDropHandler)
	core.Register("git_delete_branch", gitDeleteBranchHandler)
	core.Register("git_rename_branch", gitRenameBranchHandler)
	core.Register("git_list_worktrees", gitListWorktreesHandler)
	core.Register("git_add_worktree", gitAddWorktreeHandler)
	core.Register("git_remove_worktree", gitRemoveWorktreeHandler)
	core.Register("git_prune_worktrees", gitPruneWorktreesHandler)
	core.Register("git_lock_worktree", gitLockWorktreeHandler)
	core.Register("git_unlock_worktree", gitUnlockWorktreeHandler)
	core.Register("git_create_tag", gitCreateTagHandler)
	core.Register("git_delete_tag", gitDeleteTagHandler)
	core.Register("git_cherry_pick", gitCherryPickHandler)
	core.Register("git_reset", gitResetHandler)
	core.Register("git_undo_last_commit", gitUndoLastCommitHandler)
	core.Register("git_revert_diff_hunk", gitRevertDiffHunkHandler)
	core.Register("git_interactive_rebase", gitInteractiveRebaseHandler)
	core.Register("git_get_conflict_content", gitGetConflictContentHandler)
	core.Register("git_resolve_conflict", gitResolveConflictHandler)
	core.Register("git_get_merge_state", gitGetMergeStateHandler)
	core.Register("git_abort_merge", gitAbortMergeHandler)
	core.Register("git_generate_changelog", gitGenerateChangelogHandler)
}

// ---- stash push / pop / drop ----

type stashPushArgs struct {
	RepoPath string   `json:"repoPath"`
	Message  *string  `json:"message"`
	Paths    []string `json:"paths"`
}

func gitStashPushHandler(_, argsJson string) (string, error) {
	var a stashPushArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	msg := "WIP"
	if a.Message != nil && *a.Message != "" {
		msg = *a.Message
	}
	var selectedPaths *[]string
	if a.Paths != nil {
		selectedPaths = &a.Paths
	}
	if _, err := gitCreateStash(a.RepoPath, msg, "储藏", selectedPaths); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type stashIndexArgs struct {
	RepoPath   string `json:"repoPath"`
	StashIndex int    `json:"stashIndex"`
}

func gitStashPopHandler(_, argsJson string) (string, error) {
	var a stashIndexArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	spec := "stash@{" + itoa(a.StashIndex) + "}"
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "stash", "pop", spec)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git stash pop"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func gitStashDropHandler(_, argsJson string) (string, error) {
	var a stashIndexArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	spec := "stash@{" + itoa(a.StashIndex) + "}"
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "stash", "drop", spec)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git stash drop"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// ---- 分支删除 / 重命名 ----

type deleteBranchArgs struct {
	RepoPath   string `json:"repoPath"`
	BranchName string `json:"branchName"`
	Force      bool   `json:"force"`
}

func gitDeleteBranchHandler(_, argsJson string) (string, error) {
	var a deleteBranchArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	if cur, ok, _ := gitLine(a.RepoPath, "rev-parse", "--abbrev-ref", "HEAD"); ok && cur == a.BranchName {
		return "", core.ErrString("无法删除当前所在分支 '" + a.BranchName + "'，请先切换到其他分支")
	}
	flag := "-d"
	if a.Force {
		flag = "-D"
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "branch", flag, a.BranchName)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git branch delete"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type renameBranchArgs struct {
	RepoPath string `json:"repoPath"`
	OldName  string `json:"oldName"`
	NewName  string `json:"newName"`
}

func gitRenameBranchHandler(_, argsJson string) (string, error) {
	var a renameBranchArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "branch", "-m", a.OldName, a.NewName)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git branch -m"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// ---- worktree ----

type gitWorktreeChangeStats struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

type gitWorktreeEntry struct {
	Path       string                  `json:"path"`
	Branch     *string                 `json:"branch"`
	Head       *string                 `json:"head"`
	LockReason *string                 `json:"lock_reason,omitempty"`
	IsMain     bool                    `json:"is_main"`
	IsCurrent  bool                    `json:"is_current"`
	IsBare     bool                    `json:"is_bare"`
	IsDetached bool                    `json:"is_detached"`
	IsLocked   bool                    `json:"is_locked"`
	IsPrunable bool                    `json:"is_prunable"`
	Changes    *gitWorktreeChangeStats `json:"changes,omitempty"`
}

func gitListWorktreesHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	currentRoot, _, _ := gitLine(a.RepoPath, "rev-parse", "--show-toplevel")
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git worktree list"); err != nil {
		return "", err
	}
	entries := parseWorktreePorcelain(gitStdoutString(o))

	normPath := func(p string) string {
		return strings.ToLower(strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/"))
	}
	if len(entries) > 0 {
		entries[0].IsMain = true
	}
	if currentRoot != "" {
		cur := normPath(currentRoot)
		for i := range entries {
			if normPath(entries[i].Path) == cur {
				entries[i].IsCurrent = true
			}
		}
	}
	fillWorktreeChangeStats(entries)
	return core.EncodeResult(entries)
}

// fillWorktreeChangeStats 为每个可用 worktree 统计相对 HEAD 的增删行数。
// 并发上限固定，避免仓库 worktree 很多时一次派生过多 git 进程。
func fillWorktreeChangeStats(entries []gitWorktreeEntry) {
	const maxParallelStats = 4
	var wg sync.WaitGroup
	slots := make(chan struct{}, maxParallelStats)
	for i := range entries {
		if entries[i].IsBare || entries[i].IsPrunable {
			continue
		}
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			entries[index].Changes = worktreeChangeStats(entries[index].Path)
		}(i)
	}
	wg.Wait()
}

// git 的空树哈希，用于尚无提交（unborn）时作为 diff 基准。哈希本身依赖仓库的
// 对象格式，用错格式 git 会以 "bad revision" 拒绝，因此按格式分别取值。
const (
	gitEmptyTreeSHA1   = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	gitEmptyTreeSHA256 = "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"
)

func gitEmptyTreeObject(dir string) string {
	if format, ok, err := gitLine(dir, "rev-parse", "--show-object-format"); err == nil && ok &&
		format == "sha256" {
		return gitEmptyTreeSHA256
	}
	return gitEmptyTreeSHA1
}

const (
	maxUntrackedStatFiles = 2000
	maxUntrackedStatBytes = 1 << 20
	binarySniffBytes      = 8 * 1024
)

// worktreeChangeStats 统计工作区相对 HEAD 的增删行数，未跟踪文件按新增行计入。
//
// 直接对 HEAD 比一次，而不是把 `--cached` 与工作区两次 numstat 相加：后者是
// HEAD→index 与 index→工作区 两段变换，同一文件先暂存再修改会被重复计数。
// 任一步失败返回 nil，让调用方把该条目显示为统计不可用，而不是让整个列表失败。
func worktreeChangeStats(dir string) *gitWorktreeChangeStats {
	if dir == "" {
		return nil
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil
	}
	base := ""
	if head, ok, err := gitLine(dir, "rev-parse", "--verify", "HEAD"); err == nil && ok && head != "" {
		base = head
	} else {
		base = gitEmptyTreeObject(dir)
	}
	diff, err := runGit(dir, gitDefaultTimeout, "diff", "--numstat", "-z", base, "--")
	if err != nil || !gitSucceeded(diff) || diff.truncated {
		return nil
	}
	additions, deletions := parseNumstatZ(gitStdoutString(diff))

	untracked, err := runGit(dir, gitDefaultTimeout, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil || !gitSucceeded(untracked) || untracked.truncated {
		return nil
	}
	additions += countUntrackedLines(dir, splitNulFields(gitStdoutString(untracked)))
	return &gitWorktreeChangeStats{Additions: additions, Deletions: deletions}
}

// parseNumstatZ 解析 `git diff --numstat -z` 输出。
//
// 每条记录形如 `新增\t删除\t路径`，字段为 `-` 表示二进制文件，计 0。
// 重命名/复制的记录只带 `新增\t删除\t` 前缀，随后紧跟两条独立的 NUL 字段
// 分别是旧路径与新路径，必须一并消费掉，否则后续文件的数字会整体错位。
func parseNumstatZ(text string) (additions, deletions int) {
	fields := splitNulFields(text)
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) < 3 {
			continue
		}
		additions += parseNumstatCount(parts[0])
		deletions += parseNumstatCount(parts[1])
		if parts[2] == "" {
			i += 2
		}
	}
	return additions, deletions
}

func parseNumstatCount(field string) int {
	value, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func splitNulFields(text string) []string {
	var fields []string
	for _, field := range strings.Split(text, "\x00") {
		if field != "" {
			fields = append(fields, field)
		}
	}
	return fields
}

// countUntrackedLines 把未跟踪文本文件的行数计入新增行。
// 跳过过大文件与二进制文件，并限制文件总数，避免一次列表请求读取过多磁盘内容。
func countUntrackedLines(dir string, relativePaths []string) int {
	total := 0
	for index, relative := range relativePaths {
		if index >= maxUntrackedStatFiles {
			break
		}
		absolute := filepath.Join(dir, filepath.FromSlash(relative))
		info, err := os.Stat(absolute)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxUntrackedStatBytes {
			continue
		}
		content, err := os.ReadFile(absolute)
		if err != nil {
			continue
		}
		sniff := content
		if len(sniff) > binarySniffBytes {
			sniff = sniff[:binarySniffBytes]
		}
		if bytes.IndexByte(sniff, 0) >= 0 {
			continue
		}
		if len(content) == 0 {
			continue
		}
		total += bytes.Count(content, []byte("\n"))
		if content[len(content)-1] != '\n' {
			total++
		}
	}
	return total
}

func parseWorktreePorcelain(text string) []gitWorktreeEntry {
	var entries []gitWorktreeEntry
	var path, head, branch, lockReason string
	var hasPath, hasHead, hasBranch, hasLockReason bool
	var isBare, isDetached, isLocked, isPrunable bool

	flush := func() {
		if !hasPath {
			return
		}
		e := gitWorktreeEntry{
			Path:       path,
			IsBare:     isBare,
			IsDetached: isDetached,
			IsLocked:   isLocked,
			IsPrunable: isPrunable,
		}
		if hasBranch {
			b := branch
			e.Branch = &b
		}
		if hasHead {
			h := head
			e.Head = &h
		}
		if hasLockReason {
			r := lockReason
			e.LockReason = &r
		}
		entries = append(entries, e)
		path, head, branch, lockReason = "", "", "", ""
		hasPath, hasHead, hasBranch, hasLockReason = false, false, false, false
		isBare, isDetached, isLocked, isPrunable = false, false, false, false
	}

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			hasPath = true
		case strings.HasPrefix(line, "HEAD "):
			head = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
			hasHead = true
		case strings.HasPrefix(line, "branch "):
			b := strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			branch = strings.TrimPrefix(b, "refs/heads/")
			hasBranch = true
		case line == "bare":
			isBare = true
		case line == "detached":
			isDetached = true
		case strings.HasPrefix(line, "locked"):
			isLocked = true
			lockReason = strings.TrimSpace(strings.TrimPrefix(line, "locked"))
			if unquoted, err := strconv.Unquote(lockReason); err == nil {
				lockReason = unquoted
			}
			hasLockReason = lockReason != ""
		case strings.HasPrefix(line, "prunable"):
			isPrunable = true
		}
	}
	flush()
	return entries
}

type addWorktreeArgs struct {
	RepoPath     string  `json:"repoPath"`
	WorktreePath string  `json:"worktreePath"`
	Branch       *string `json:"branch"`
	BaseBranch   *string `json:"baseBranch"`
	NewBranch    bool    `json:"newBranch"`
}

func gitAddWorktreeHandler(_, argsJson string) (string, error) {
	var a addWorktreeArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.WorktreePath) == "" {
		return "", core.ErrString("工作树路径不能为空")
	}
	args := []string{"worktree", "add"}
	if a.NewBranch {
		name := ""
		if a.Branch != nil {
			name = strings.TrimSpace(*a.Branch)
		}
		if name == "" {
			return "", core.ErrString("新建分支名不能为空")
		}
		args = append(args, "-b", name, a.WorktreePath)
		if a.BaseBranch != nil {
			if base := strings.TrimSpace(*a.BaseBranch); base != "" {
				args = append(args, base)
			}
		}
	} else {
		args = append(args, a.WorktreePath)
		if a.Branch != nil {
			if b := strings.TrimSpace(*a.Branch); b != "" {
				args = append(args, b)
			}
		}
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, args...)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git worktree add"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type removeWorktreeArgs struct {
	RepoPath     string `json:"repoPath"`
	WorktreePath string `json:"worktreePath"`
	Force        bool   `json:"force"`
	ForceLocked  bool   `json:"forceLocked"`
}

func gitRemoveWorktreeHandler(_, argsJson string) (string, error) {
	var a removeWorktreeArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.WorktreePath) == "" {
		return "", core.ErrString("工作树路径不能为空")
	}
	args := []string{"worktree", "remove"}
	forceCount := 0
	if a.Force {
		forceCount = 1
	}
	// Git 要求两个 -f 才能移除 locked worktree。该权限必须由前端在
	// 显示额外警告并获得用户确认后显式传入，不能把普通强制删除升级。
	if a.ForceLocked {
		forceCount = 2
	}
	for range forceCount {
		args = append(args, "--force")
	}
	args = append(args, a.WorktreePath)
	o, err := runGit(a.RepoPath, gitDefaultTimeout, args...)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git worktree remove"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func gitPruneWorktreesHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "worktree", "prune")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git worktree prune"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type lockWorktreeArgs struct {
	RepoPath     string  `json:"repoPath"`
	WorktreePath string  `json:"worktreePath"`
	Reason       *string `json:"reason"`
}

// gitIsMainWorktree 判断目标是否为主工作树：关联工作树的 git-dir 落在
// 主仓库的 .git/worktrees/<name> 下，与 git-common-dir 不同；主工作树两者一致。
// 不比较路径字符串，因为 macOS 的 /var 是符号链接，git 只返回解析后的真实路径。
// 判定失败时返回 false，交回 git 自己报错。
func gitIsMainWorktree(worktreePath string) bool {
	if strings.TrimSpace(worktreePath) == "" {
		return false
	}
	gitDir, ok, err := gitLine(worktreePath, "rev-parse", "--absolute-git-dir")
	if err != nil || !ok {
		return false
	}
	commonDir, ok, err := gitLine(worktreePath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || !ok {
		return false
	}
	return gitDir == commonDir
}

func gitLockWorktreeHandler(_, argsJson string) (string, error) {
	var a lockWorktreeArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.WorktreePath) == "" {
		return "", core.ErrString("工作树路径不能为空")
	}
	// git 只会回一句英文 fatal，AI 与界面都拿不到可读原因，这里先挡下来。
	if gitIsMainWorktree(a.WorktreePath) {
		return "", core.ErrString("主工作树无法锁定，只有关联工作树支持锁定")
	}
	args := []string{"worktree", "lock"}
	if a.Reason != nil {
		if r := strings.TrimSpace(*a.Reason); r != "" {
			args = append(args, "--reason", r)
		}
	}
	args = append(args, a.WorktreePath)
	o, err := runGit(a.RepoPath, gitDefaultTimeout, args...)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git worktree lock"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func gitUnlockWorktreeHandler(_, argsJson string) (string, error) {
	var a removeWorktreeArgs // 复用 repoPath+worktreePath（force 字段忽略）
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.WorktreePath) == "" {
		return "", core.ErrString("工作树路径不能为空")
	}
	if gitIsMainWorktree(a.WorktreePath) {
		return "", core.ErrString("主工作树无需解锁，只有关联工作树支持锁定")
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "worktree", "unlock", a.WorktreePath)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git worktree unlock"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// ---- tag 增删 ----

type createTagArgs struct {
	RepoPath string  `json:"repoPath"`
	Name     string  `json:"name"`
	Message  *string `json:"message"`
}

func gitCreateTagHandler(_, argsJson string) (string, error) {
	var a createTagArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	var o gitOutput
	var err error
	if a.Message != nil {
		o, err = runGit(a.RepoPath, gitDefaultTimeout, "tag", "-a", a.Name, "-m", *a.Message)
	} else {
		o, err = runGit(a.RepoPath, gitDefaultTimeout, "tag", a.Name)
	}
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git tag"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type nameArgs struct {
	RepoPath string `json:"repoPath"`
	Name     string `json:"name"`
}

func gitDeleteTagHandler(_, argsJson string) (string, error) {
	var a nameArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "tag", "-d", a.Name)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git tag -d"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// ---- cherry-pick ----

func gitCherryPickHandler(_, argsJson string) (string, error) {
	var a commitHashArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "cherry-pick", a.CommitHash)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git cherry-pick"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// ---- reset ----

type resetArgs struct {
	RepoPath   string `json:"repoPath"`
	CommitHash string `json:"commitHash"`
	Mode       string `json:"mode"`
}

func gitResetHandler(_, argsJson string) (string, error) {
	var a resetArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	flag := "--mixed"
	switch a.Mode {
	case "soft":
		flag = "--soft"
	case "hard":
		flag = "--hard"
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "reset", flag, a.CommitHash)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git reset"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func gitUndoLastCommitHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitUndoLastCommit(a.RepoPath); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func gitUndoLastCommit(repoPath string) error {
	if err := gitEnsureRepo(repoPath); err != nil {
		return err
	}
	if _, ok, _ := gitLine(repoPath, "rev-parse", "HEAD"); !ok {
		return core.ErrString("当前仓库没有可撤销的提交")
	}

	if _, ok, _ := gitLine(repoPath, "rev-parse", "HEAD^"); ok {
		o, err := runGit(repoPath, gitDefaultTimeout, "reset", "--soft", "HEAD^")
		if err != nil {
			return err
		}
		return gitEnsureSuccess(o, "撤销上次提交")
	}

	branch, ok, err := gitLine(repoPath, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(branch) == "" {
		return core.ErrString("无法撤销 detached HEAD 的初始提交")
	}
	o, err := runGit(repoPath, gitDefaultTimeout, "update-ref", "-d", "refs/heads/"+branch)
	if err != nil {
		return err
	}
	return gitEnsureSuccess(o, "撤销初始提交")
}

// ---- revert 变更块（reverse apply 单 hunk） ----

type revertHunkArgs struct {
	RepoPath     string `json:"repoPath"`
	RelativePath string `json:"relativePath"`
	Staged       bool   `json:"staged"`
	HunkIndex    int    `json:"hunkIndex"`
}

func gitRevertDiffHunkHandler(_, argsJson string) (string, error) {
	var a revertHunkArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	path, err := normalizeGitRelativePath(a.RepoPath, a.RelativePath)
	if err != nil {
		return "", err
	}
	unified, err := gitUnifiedDiff(a.RepoPath, path, a.Staged)
	if err != nil {
		return "", err
	}
	patch, err := buildApplyPatch(unified, a.HunkIndex)
	if err != nil {
		return "", err
	}
	if a.Staged {
		o, err := runGitStdin(a.RepoPath, patch, gitDefaultTimeout, "apply", "--reverse", "--cached", "--recount")
		if err != nil {
			return "", err
		}
		if err := gitEnsureSuccess(o, "git apply --reverse --cached"); err != nil {
			return "", err
		}
		// 工作区尽力同步撤销，失败忽略（可能已有未暂存改动）。
		_, _ = runGitStdin(a.RepoPath, patch, gitDefaultTimeout, "apply", "--reverse", "--recount")
	} else {
		o, err := runGitStdin(a.RepoPath, patch, gitDefaultTimeout, "apply", "--reverse", "--recount")
		if err != nil {
			return "", err
		}
		if err := gitEnsureSuccess(o, "git apply --reverse"); err != nil {
			return "", err
		}
	}
	return core.EncodeResult(nil)
}

func gitUnifiedDiff(repoPath, path string, staged bool) (string, error) {
	args := []string{"diff", "--unified=3"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, "--", path)
	o, err := runGit(repoPath, gitDefaultTimeout, args...)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git diff"); err != nil {
		return "", err
	}
	return gitStdoutString(o), nil
}

func splitHunks(diff string) []string {
	var hunks []string
	var current strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			if current.Len() > 0 {
				hunks = append(hunks, strings.TrimRight(current.String(), "\n"))
			}
			current.Reset()
			current.WriteString(line)
			current.WriteString("\n")
		} else if current.Len() > 0 {
			current.WriteString(line)
			current.WriteString("\n")
		}
	}
	if current.Len() > 0 {
		hunks = append(hunks, strings.TrimRight(current.String(), "\n"))
	}
	return hunks
}

func buildApplyPatch(fullDiff string, hunkIndex int) (string, error) {
	hunks := splitHunks(fullDiff)
	if hunkIndex < 0 || hunkIndex >= len(hunks) {
		return "", core.ErrString("变更块 " + itoa(hunkIndex) + " 不存在")
	}
	var patch strings.Builder
	for _, line := range strings.Split(fullDiff, "\n") {
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			patch.WriteString(line)
			patch.WriteString("\n")
		}
	}
	if patch.Len() == 0 {
		return "", core.ErrString("无法解析 diff 文件头")
	}
	patch.WriteString(hunks[hunkIndex])
	patch.WriteString("\n")
	return patch.String(), nil
}

// ---- interactive rebase ----

type rebaseActionEntry struct {
	Hash    string `json:"hash"`
	Action  string `json:"action"`
	Message string `json:"message"`
}

type interactiveRebaseArgs struct {
	RepoPath string              `json:"repoPath"`
	BaseHash string              `json:"baseHash"`
	Actions  []rebaseActionEntry `json:"actions"`
}

func buildInteractiveRebaseTodo(actions []rebaseActionEntry) (string, error) {
	allowedActions := map[string]struct{}{
		"pick":   {},
		"squash": {},
		"fixup":  {},
		"drop":   {},
	}
	todoLines := make([]string, 0, len(actions))
	for _, act := range actions {
		if strings.ContainsAny(act.Action, "\r\n") || strings.ContainsAny(act.Hash, "\r\n") || strings.ContainsAny(act.Message, "\r\n") {
			return "", core.ErrString("Rebase action、hash 和 message 不能包含换行符")
		}
		if _, allowed := allowedActions[act.Action]; !allowed {
			return "", core.ErrString("不支持的 Rebase action: " + act.Action)
		}
		h := act.Hash
		if len(h) > 8 {
			h = h[:8]
		}
		todoLines = append(todoLines, act.Action+" "+h+" "+act.Message)
	}
	return strings.Join(todoLines, "\n"), nil
}

func gitInteractiveRebaseHandler(_, argsJson string) (string, error) {
	var a interactiveRebaseArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	todo, err := buildInteractiveRebaseTodo(a.Actions)
	if err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}

	todoPath := filepath.Join(os.TempDir(), "app_rebase_todo_"+itoa(os.Getpid()))
	if err := os.WriteFile(todoPath, []byte(todo), 0o600); err != nil {
		return "", core.ErrString("写入 rebase todo 失败: " + err.Error())
	}
	defer func() { _ = os.Remove(todoPath) }()

	todoUnix := strings.ReplaceAll(todoPath, "\\", "/")
	seqEditor := "cp \"" + todoUnix + "\""

	cmd, err := newGitCommand("rebase", "-i", a.BaseHash)
	if err != nil {
		return "", core.ErrString("启动 Rebase 失败: " + err.Error())
	}
	core.PrepareProcessTree(cmd)
	cmd.Dir = resolveRepoDir(a.RepoPath)
	cmd.Env = append(cmd.Env,
		"GIT_SEQUENCE_EDITOR="+seqEditor,
		"GIT_EDITOR="+seqEditor,
	)
	stdout := core.NewCappedWriter(gitMaxOutputBytes)
	stderr := core.NewCappedWriter(gitMaxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return "", core.ErrString("启动 Rebase 失败: " + err.Error())
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	var runErr error
	timer := time.NewTimer(gitDefaultTimeout)
	defer timer.Stop()
	select {
	case runErr = <-waitDone:
	case <-timer.C:
		_ = core.KillProcessTree(cmd)
		<-waitDone
		return "", core.ErrString("Rebase 超时")
	}

	stdoutBytes, stdoutTruncated := stdout.Bytes()
	stderrBytes, stderrTruncated := stderr.Bytes()
	if stdoutTruncated || stderrTruncated {
		return "", core.ErrString("Rebase 输出超过上限，已终止解析")
	}
	so := strings.TrimSpace(string(stdoutBytes))
	se := strings.TrimSpace(string(stderrBytes))
	if runErr == nil && (cmd.ProcessState == nil || cmd.ProcessState.Success()) {
		if so == "" {
			return core.EncodeResult("Rebase 完成")
		}
		return core.EncodeResult(so)
	}
	if se == "" {
		return "", core.ErrString("Rebase 失败")
	}
	return "", core.ErrString(se)
}

// ---- 冲突读取 / 解决 ----

type conflictContent struct {
	Ours   string `json:"ours"`
	Theirs string `json:"theirs"`
	Base   string `json:"base"`
	Raw    string `json:"raw"`
}

type mergeState struct {
	Active    bool     `json:"active"`
	Conflicts []string `json:"conflicts"`
}

const gitMaxConflictFileBytes = 5 * 1024 * 1024

func resolveGitWorktreeFile(repoPath, relativePath string) (string, string, error) {
	repo := resolveRepoDir(repoPath)
	if err := gitEnsureRepo(repo); err != nil {
		return "", "", err
	}
	rel, err := normalizeGitRelativePath(repo, relativePath)
	if err != nil {
		return "", "", err
	}
	root, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", "", core.ErrString("仓库路径解析失败: " + err.Error())
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", core.ErrString("仅允许操作仓库内普通文件")
	}
	canon, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", err
	}
	inside, err := filepath.Rel(root, canon)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", "", core.ErrString("冲突文件不在仓库内")
	}
	return canon, rel, nil
}

func gitConflictStageContent(repoPath, relativePath string, stage int) (string, error) {
	spec := ":" + strconv.Itoa(stage) + ":" + relativePath
	o, err := runGitWithLimit(repoPath, gitDefaultTimeout, gitMaxConflictFileBytes+1, "show", spec)
	if err != nil {
		return "", err
	}
	if o.timedOut {
		return "", core.ErrString("读取冲突版本超时")
	}
	if o.exitCode != 0 {
		// 新增/删除冲突不会同时拥有 1/2/3 三个 stage，缺少的版本按空文件展示。
		return "", nil
	}
	if o.truncated || len(o.stdout) > gitMaxConflictFileBytes {
		return "", core.ErrString("冲突文件超过 5 MiB 上限")
	}
	if bytes.IndexByte(o.stdout, 0) >= 0 {
		return "", core.ErrString("暂不支持可视化解决二进制文件冲突")
	}
	return gitStdoutString(o), nil
}

func gitGetConflictContentHandler(_, argsJson string) (string, error) {
	var a repoRelArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	fullPath, rel, err := resolveGitWorktreeFile(a.RepoPath, a.RelativePath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(fullPath)
	if err != nil || info.Size() > gitMaxConflictFileBytes {
		return "", core.ErrString("冲突文件不存在或超过 5 MiB 上限")
	}
	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return "", core.ErrString(err.Error())
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return "", core.ErrString("暂不支持可视化解决二进制文件冲突")
	}
	base, err := gitConflictStageContent(a.RepoPath, rel, 1)
	if err != nil {
		return "", err
	}
	ours, err := gitConflictStageContent(a.RepoPath, rel, 2)
	if err != nil {
		return "", err
	}
	theirs, err := gitConflictStageContent(a.RepoPath, rel, 3)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(conflictContent{
		Ours:   ours,
		Theirs: theirs,
		Base:   base,
		Raw:    string(raw),
	})
}

type resolveConflictArgs struct {
	RepoPath     string `json:"repoPath"`
	RelativePath string `json:"relativePath"`
	Content      string `json:"content"`
}

func gitResolveConflictHandler(_, argsJson string) (string, error) {
	var a resolveConflictArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if len(a.Content) > gitMaxConflictFileBytes {
		return "", core.ErrString("冲突文件内容超过 5 MiB 上限")
	}
	fullPath, rel, err := resolveGitWorktreeFile(a.RepoPath, a.RelativePath)
	if err != nil {
		return "", err
	}
	conflicts, err := gitConflictPaths(a.RepoPath)
	if err != nil {
		return "", err
	}
	isConflict := false
	for _, path := range conflicts {
		if path == rel {
			isConflict = true
			break
		}
	}
	if !isConflict {
		return "", core.ErrString("该文件不再处于冲突状态，请刷新后重试")
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(fullPath), ".mincode-conflict-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.WriteString(a.Content); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, fullPath); err != nil {
		return "", core.ErrString(err.Error())
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "add", "--", rel)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git add"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func gitConflictPaths(repoPath string) ([]string, error) {
	// 关闭 quotepath，否则非 ASCII 文件名会以八进制转义返回，前端无法据此加载或标记解决。
	lines, err := gitLines(repoPath, "-c", "core.quotepath=false", "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if lines == nil {
		return []string{}, nil
	}
	return lines, nil
}

func gitMergeActive(repoPath string) (bool, error) {
	o, err := runGit(repoPath, gitDefaultTimeout, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	if err != nil {
		return false, err
	}
	if o.timedOut {
		return false, core.ErrString("读取合并状态超时")
	}
	return o.exitCode == 0, nil
}

func readGitMergeState(repoPath string) (mergeState, error) {
	if err := gitEnsureRepo(repoPath); err != nil {
		return mergeState{}, err
	}
	active, err := gitMergeActive(repoPath)
	if err != nil {
		return mergeState{}, err
	}
	conflicts, err := gitConflictPaths(repoPath)
	if err != nil {
		return mergeState{}, err
	}
	return mergeState{Active: active, Conflicts: conflicts}, nil
}

func gitGetMergeStateHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	state, err := readGitMergeState(a.RepoPath)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(state)
}

func gitAbortMergeHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	state, err := readGitMergeState(a.RepoPath)
	if err != nil {
		return "", err
	}
	if !state.Active {
		return "", core.ErrString("当前没有进行中的合并")
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "merge", "--abort")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "中止合并"); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

// ---- CHANGELOG 生成 ----
//
// Tauri 版基于 git-cliff-core 模板引擎；Go 侧不引三方依赖，改用内置的
// conventional-commits 分组生成基础 CHANGELOG（feat/fix/... 分组 + Other）。

type changelogArgs struct {
	RepoPath   string  `json:"repoPath"`
	ConfigToml *string `json:"configToml"`
}

func gitGenerateChangelogHandler(_, argsJson string) (string, error) {
	var a changelogArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	// configToml 暂不解析（无模板引擎），仅使用内置分组规则。
	lines, err := gitLines(a.RepoPath, "log", "--no-merges", "--pretty=format:%s", "-n", "500")
	if err != nil {
		return "", err
	}
	return core.EncodeResult(buildBasicChangelog(lines))
}

// changelogGroup：分组顺序与标题（对齐 Tauri 默认 cliff 配置）。
var changelogGroups = []struct {
	title    string
	prefixes []string
}{
	{"Features", []string{"feat"}},
	{"Bug Fixes", []string{"fix"}},
	{"Documentation", []string{"doc"}},
	{"Performance", []string{"perf"}},
	{"Refactoring", []string{"refactor"}},
	{"Styling", []string{"style"}},
	{"Testing", []string{"test"}},
	{"Miscellaneous", []string{"chore", "ci", "build"}},
}

func buildBasicChangelog(subjects []string) string {
	grouped := map[string][]string{}
	var other []string

	classify := func(subject string) (string, string, bool) {
		// 解析 conventional commit：type(scope)?: message
		s := strings.TrimSpace(subject)
		colon := strings.Index(s, ":")
		if colon <= 0 {
			return "", s, false
		}
		head := s[:colon]
		msg := strings.TrimSpace(s[colon+1:])
		// 去掉 scope 与 breaking `!`
		typePart := head
		if p := strings.IndexByte(head, '('); p >= 0 {
			typePart = head[:p]
		}
		typePart = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(typePart)), "!")
		return typePart, msg, msg != ""
	}

	for _, subject := range subjects {
		typePart, msg, ok := classify(subject)
		matched := false
		if ok {
			for _, g := range changelogGroups {
				for _, pre := range g.prefixes {
					if typePart == pre {
						grouped[g.title] = append(grouped[g.title], msg)
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
		}
		if !matched {
			m := strings.TrimSpace(subject)
			if m != "" {
				other = append(other, m)
			}
		}
	}

	var b strings.Builder
	b.WriteString("# Changelog\n\n")
	b.WriteString("All notable changes to this project will be documented in this file.\n\n")
	b.WriteString("## [Unreleased]\n")

	for _, g := range changelogGroups {
		items := grouped[g.title]
		if len(items) == 0 {
			continue
		}
		b.WriteString("\n### ")
		b.WriteString(g.title)
		b.WriteByte('\n')
		for _, it := range dedupPreserve(items) {
			b.WriteString("- ")
			b.WriteString(upperFirst(it))
			b.WriteByte('\n')
		}
	}
	if len(other) > 0 {
		b.WriteString("\n### Other\n")
		for _, it := range dedupPreserve(other) {
			b.WriteString("- ")
			b.WriteString(upperFirst(it))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func dedupPreserve(items []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, it := range items {
		if _, ok := seen[it]; ok {
			continue
		}
		seen[it] = struct{}{}
		out = append(out, it)
	}
	return out
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
}

// itoa：本文件内的小工具。
func itoa(n int) string {
	return strconv.Itoa(n)
}
