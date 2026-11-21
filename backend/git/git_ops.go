// Git 域命令处理器：注册到命令表，实现 status/log/diff/branches/blame 等。
package git

import (
	"mincode/app/backend/core"

	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

func init() {
	core.Register("git_check_cli", gitCheckCliHandler)
	core.Register("git_list_project_repositories", gitListProjectRepositoriesHandler)
	core.Register("git_get_status", gitGetStatusHandler)
	core.Register("git_get_tracking_status", gitGetTrackingStatusHandler)
	core.Register("git_get_repository_summaries", gitGetRepositorySummariesHandler)
	core.Register("git_stage_file", gitStageFileHandler)
	core.Register("git_unstage_file", gitUnstageFileHandler)
	core.Register("git_stage_all", gitStageAllHandler)
	core.Register("git_unstage_all", gitUnstageAllHandler)
	core.Register("git_commit", gitCommitHandler)
	core.Register("git_commit_amend", gitCommitAmendHandler)
	core.Register("git_get_diff", gitGetDiffHandler)
	core.Register("git_list_branches", gitListBranchesHandler)
	core.Register("git_checkout_branch", gitCheckoutBranchHandler)
	core.Register("git_get_log", gitGetLogHandler)
	core.Register("git_get_commit_detail", gitGetCommitDetailHandler)
	core.Register("git_get_commit_file_diff", gitGetCommitFileDiffHandler)
	core.Register("git_get_staged_patch", gitGetStagedPatchHandler)
	core.Register("git_get_commit_message_context", gitGetCommitMessageContextHandler)
	core.Register("git_list_tags", gitListTagsHandler)
	core.Register("git_stash_list", gitStashListHandler)
	core.Register("git_merge_branch", gitMergeBranchHandler)
	core.Register("git_push", gitPushHandler)
	core.Register("git_pull", gitPullHandler)
	core.Register("git_fetch", gitFetchHandler)
	core.Register("git_fetch_tags", gitFetchTagsHandler)
	core.Register("git_revert", gitRevertHandler)
	core.Register("git_blame", gitBlameHandler)
	core.Register("git_get_remote_url", gitGetRemoteUrlHandler)
	core.Register("git_read_file_at_head", gitReadFileAtHeadHandler)
	core.Register("git_read_file_at_ref", gitReadFileAtRefHandler)
	core.Register("git_list_conflicts", gitListConflictsHandler)
	core.Register("git_discard_file", gitDiscardFileHandler)
	core.Register("git_discard_all", gitDiscardAllHandler)
	core.Register("git_discard_unstaged", gitDiscardUnstagedHandler)
	core.Register("git_clean_untracked", gitCleanUntrackedHandler)
}

// ---- project repositories ----

// gitProjectRepositoryEntry represents a Git repository available directly under a
// local project root. The UI deliberately scans only this shallow scope so opening
// a large workspace never turns into an unbounded disk crawl.
type gitProjectRepositoryEntry struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
}

type projectRootArgs struct {
	ProjectRoot string `json:"projectRoot"`
}

func gitListProjectRepositoriesHandler(_, argsJson string) (string, error) {
	var a projectRootArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	projectRoot := resolveRepoDir(a.ProjectRoot)
	if projectRoot == "" {
		return core.EncodeResult([]gitProjectRepositoryEntry{})
	}

	candidates, err := gitProjectRepositoryCandidates(projectRoot)
	if err != nil {
		return "", err
	}
	entries := make([]gitProjectRepositoryEntry, 0, len(candidates))
	for _, candidate := range candidates {
		// A .git marker is a cheap filter only. Validate it with Git because linked
		// worktrees and broken/stale markers need the same behaviour as all other
		// Git commands before they are shown in the source selector.
		if err := gitEnsureRepo(candidate.path); err != nil {
			continue
		}
		entries = append(entries, gitProjectRepositoryEntry{
			Name:         candidate.name,
			Path:         candidate.path,
			RelativePath: candidate.relativePath,
		})
	}
	return core.EncodeResult(entries)
}

type gitProjectRepositoryCandidate struct {
	name         string
	path         string
	relativePath string
}

func gitProjectRepositoryCandidates(projectRoot string) ([]gitProjectRepositoryCandidate, error) {
	root := resolveRepoDir(projectRoot)
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, core.ErrString("工程路径不是目录: " + root)
	}

	candidates := make([]gitProjectRepositoryCandidate, 0)
	appendCandidate := func(path, relativePath string) {
		marker, markerErr := os.Stat(filepath.Join(path, ".git"))
		if markerErr != nil || (!marker.IsDir() && !marker.Mode().IsRegular()) {
			return
		}
		name := filepath.Base(path)
		if relativePath == "." {
			name = filepath.Base(root)
		}
		candidates = append(candidates, gitProjectRepositoryCandidate{
			name:         name,
			path:         path,
			relativePath: relativePath,
		})
	}

	appendCandidate(root, ".")
	children, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		if !child.IsDir() || strings.HasPrefix(child.Name(), ".") {
			continue
		}
		appendCandidate(filepath.Join(root, child.Name()), child.Name())
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].relativePath == "." {
			return true
		}
		if candidates[j].relativePath == "." {
			return false
		}
		return strings.ToLower(candidates[i].name) < strings.ToLower(candidates[j].name)
	})
	return candidates, nil
}

// ---- check_cli ----

func gitCheckCliHandler(_, _ string) (string, error) {
	res := gitCliCheck{Os: goOsName()}
	o, err := runGit("", gitDefaultTimeout, "--version")
	if err != nil {
		res.Available = false
		res.Error = strPtr(err.Error())
		return core.EncodeResult(res)
	}
	if o.exitCode == 0 && !o.timedOut {
		v := strings.TrimSpace(gitStdoutString(o))
		res.Available = true
		if v != "" {
			res.Version = strPtr(v)
		}
	} else {
		res.Available = false
		msg := gitStderrString(o)
		if msg == "" {
			msg = "git --version 执行失败"
		}
		res.Error = strPtr(msg)
	}
	return core.EncodeResult(res)
}

func goOsName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	default:
		return runtime.GOOS
	}
}

// ---- status ----

type repoOnlyArgs struct {
	RepoPath string `json:"repoPath"`
}

func gitGetStatusHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	st, err := gitGetStatus(a.RepoPath)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(st)
}

func gitGetStatus(repoPath string) (gitStatus, error) {
	if err := gitEnsureRepo(repoPath); err != nil {
		return gitStatus{}, err
	}
	o, err := runGit(repoPath, gitDefaultTimeout,
		"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return gitStatus{}, err
	}
	if err := gitEnsureSuccess(o, "git status"); err != nil {
		return gitStatus{}, err
	}
	return parsePorcelainV2(gitStdoutString(o)), nil
}

func gitGetTrackingStatusHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	st, err := gitGetStatus(a.RepoPath)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(gitTrackingStatus{
		Branch:   st.Branch,
		Upstream: st.Upstream,
		Ahead:    st.Ahead,
		Behind:   st.Behind,
	})
}

// ---- repository summaries ----

// gitRepositorySummary 是仓库列表每一行需要的最小信息。只回变更计数而不是文件条目，
// 避免几千个改动的仓库把整份 entry 列表通过桥接传给前端。
type gitRepositorySummary struct {
	Path      string  `json:"path"`
	Branch    string  `json:"branch"`
	Upstream  *string `json:"upstream"`
	Ahead     int     `json:"ahead"`
	Behind    int     `json:"behind"`
	Staged    int     `json:"staged"`
	Unstaged  int     `json:"unstaged"`
	Untracked int     `json:"untracked"`
}

type repoPathsArgs struct {
	RepoPaths []string `json:"repoPaths"`
}

// gitRepositorySummaryWorkers 限制同时执行的 git status 数量：仓库列表刷新是后台行为，
// 不该在机械硬盘或大仓库上一次性占满磁盘和 CPU。
const gitRepositorySummaryWorkers = 4

func gitGetRepositorySummariesHandler(_, argsJson string) (string, error) {
	var a repoPathsArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	return core.EncodeResult(gitRepositorySummaries(a.RepoPaths))
}

// gitRepositorySummaries 并发读取每个仓库的状态，按入参顺序返回。单个仓库失败
// （权限、损坏的 .git、目录正被移除）只跳过该条，前端会退回到只显示仓库名。
func gitRepositorySummaries(repoPaths []string) []gitRepositorySummary {
	collected := make([]*gitRepositorySummary, len(repoPaths))
	workers := gitRepositorySummaryWorkers
	if len(repoPaths) < workers {
		workers = len(repoPaths)
	}
	if workers > 0 {
		jobs := make(chan int)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range jobs {
					requested := strings.TrimSpace(repoPaths[index])
					if requested == "" {
						continue
					}
					st, err := gitGetStatus(resolveRepoDir(requested))
					if err != nil {
						continue
					}
					collected[index] = &gitRepositorySummary{
						// 回传调用方给的路径，前端才能直接用它作为列表行的 key。
						Path:      requested,
						Branch:    st.Branch,
						Upstream:  st.Upstream,
						Ahead:     st.Ahead,
						Behind:    st.Behind,
						Staged:    len(st.Staged),
						Unstaged:  len(st.Unstaged),
						Untracked: len(st.Untracked),
					}
				}
			}()
		}
		for index := range repoPaths {
			jobs <- index
		}
		close(jobs)
		wg.Wait()
	}

	summaries := make([]gitRepositorySummary, 0, len(repoPaths))
	for _, summary := range collected {
		if summary != nil {
			summaries = append(summaries, *summary)
		}
	}
	return summaries
}

// parsePorcelainV2 解析 `git status --porcelain=v2 -z` 输出（对齐 parser.rs）。
func parsePorcelainV2(stdout string) gitStatus {
	st := gitStatus{
		Branch:    "HEAD",
		Staged:    []gitFileEntry{},
		Unstaged:  []gitFileEntry{},
		Untracked: []gitFileEntry{},
	}
	toks := []string{}
	for _, t := range strings.Split(stdout, "\x00") {
		if t != "" {
			toks = append(toks, t)
		}
	}
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		switch {
		case strings.HasPrefix(tok, "# branch.head "):
			st.Branch = strings.TrimPrefix(tok, "# branch.head ")
		case strings.HasPrefix(tok, "# branch.upstream "):
			up := strings.TrimPrefix(tok, "# branch.upstream ")
			st.Upstream = strPtr(up)
		case strings.HasPrefix(tok, "# branch.ab "):
			rest := strings.TrimPrefix(tok, "# branch.ab ")
			parts := strings.Fields(rest)
			if len(parts) >= 1 {
				st.Ahead = parseIntDefault(strings.TrimPrefix(parts[0], "+"), 0)
			}
			if len(parts) >= 2 {
				st.Behind = parseIntDefault(strings.TrimPrefix(parts[1], "-"), 0)
			}
		case strings.HasPrefix(tok, "# "):
			// 其他 header，忽略
		case strings.HasPrefix(tok, "1 "):
			if xy, path, ok := parseOrdinary(strings.TrimPrefix(tok, "1 ")); ok {
				pushXY(&st, xy, path, nil)
			}
		case strings.HasPrefix(tok, "2 "):
			var orig string
			if i+1 < len(toks) {
				orig = toks[i+1]
				i++
			}
			// Type 2 has one extra score field before the path. Remove only
			// that metadata, preserving the path exactly (including spaces).
			parts := strings.SplitN(strings.TrimPrefix(tok, "2 "), " ", 9)
			if len(parts) == 9 {
				xy, path := parts[0], parts[8]
				var oldPath *string
				if orig != "" {
					oldPath = strPtr(orig)
				}
				pushXY(&st, xy, path, oldPath)
			}
		case strings.HasPrefix(tok, "u "):
			if path, ok := parseUnmergedPath(strings.TrimPrefix(tok, "u ")); ok {
				st.Unstaged = append(st.Unstaged, gitFileEntry{Path: path, Status: "conflicted", xy: tok[2:4]})
			}
		case strings.HasPrefix(tok, "? "):
			st.Untracked = append(st.Untracked, gitFileEntry{
				Path:   strings.TrimPrefix(tok, "? "),
				Status: "untracked",
			})
		}
	}
	return st
}

func parseUnmergedPath(rest string) (string, bool) {
	// porcelain v2 unmerged records contain ten fixed metadata fields followed by path.
	parts := strings.SplitN(strings.TrimLeft(rest, " "), " ", 10)
	if len(parts) < 10 {
		return "", false
	}
	path := parts[9]
	return path, path != ""
}

func parseOrdinary(rest string) (string, string, bool) {
	rest = strings.TrimLeft(rest, " ")
	if len(rest) < 2 {
		return "", "", false
	}
	xy := rest[:2]
	afterXY := strings.TrimLeft(rest[2:], " ")
	if afterXY == "" {
		return "", "", false
	}
	var path string
	if looksLikeV2OrdinaryMeta(afterXY) {
		p, ok := parseV2OrdinaryPath(afterXY)
		if !ok {
			return "", "", false
		}
		path = p
	} else {
		path = strings.TrimSpace(afterXY)
	}
	if path == "" {
		return "", "", false
	}
	return xy, path, true
}

func looksLikeV2OrdinaryMeta(afterXY string) bool {
	return len(afterXY) >= 5 && afterXY[4] == ' '
}

func parseV2OrdinaryPath(afterXY string) (string, bool) {
	parts := strings.SplitN(afterXY, " ", 7)
	if len(parts) < 7 {
		return "", false
	}
	path := parts[6]
	if path == "" {
		return "", false
	}
	return path, true
}

func isIndexDirty(x byte) bool {
	switch x {
	case 'M', 'A', 'D', 'R', 'C', 'T', 'U':
		return true
	}
	return false
}

func isWorktreeDirty(y byte) bool {
	switch y {
	case 'M', 'D', 'U':
		return true
	}
	return false
}

func pushXY(st *gitStatus, xy, path string, oldPath *string) {
	var x, y byte = ' ', ' '
	if len(xy) >= 1 {
		x = xy[0]
	}
	if len(xy) >= 2 {
		y = xy[1]
	}
	if x == '?' && y == '?' {
		st.Untracked = append(st.Untracked, gitFileEntry{Path: path, Status: "untracked"})
		return
	}
	if isIndexDirty(x) {
		st.Staged = append(st.Staged, gitFileEntry{
			Path:    path,
			Status:  mapIndexStatus(x),
			xy:      xy,
			OldPath: oldPath,
		})
	}
	if isWorktreeDirty(y) {
		st.Unstaged = append(st.Unstaged, gitFileEntry{
			Path:    path,
			Status:  mapWorktreeStatus(y),
			xy:      xy,
			OldPath: oldPath,
		})
	}
}

// ---- stage / unstage / discard ----

type repoRelArgs struct {
	RepoPath     string `json:"repoPath"`
	RelativePath string `json:"relativePath"`
}

func gitStageFileHandler(_, argsJson string) (string, error) {
	var a repoRelArgs
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
	// 文件存在则 add，否则 add -u（删除场景）。
	if fileExists(a.RepoPath, path) {
		o, err := runGit(a.RepoPath, gitDefaultTimeout, "add", "--", path)
		if err != nil {
			return "", err
		}
		if err := gitEnsureSuccess(o, "git add"); err != nil {
			return "", err
		}
	} else {
		o, err := runGit(a.RepoPath, gitDefaultTimeout, "add", "-u", "--", path)
		if err != nil {
			return "", err
		}
		if err := gitEnsureSuccess(o, "git add -u"); err != nil {
			return "", err
		}
	}
	return "", nil
}

func gitUnstageFileHandler(_, argsJson string) (string, error) {
	var a repoRelArgs
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
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "restore", "--staged", "--", path)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git restore --staged"); err != nil {
		return "", err
	}
	return "", nil
}

func gitStageAllHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "add", "-A")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git add -A"); err != nil {
		return "", err
	}
	return "", nil
}

func gitUnstageAllHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	if _, ok, _ := gitLine(a.RepoPath, "rev-parse", "HEAD"); !ok {
		return "", nil // 无 HEAD（空仓库），无需 reset
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "reset", "HEAD")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git reset HEAD"); err != nil {
		return "", err
	}
	return "", nil
}

func gitDiscardFileHandler(_, argsJson string) (string, error) {
	var a repoRelArgs
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
	// 已跟踪：restore 工作区；未跟踪：删除文件。
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "restore", "--", path)
	if err != nil {
		return "", err
	}
	if !gitSucceeded(o) {
		// 可能是未跟踪文件，尝试 clean。
		o2, err := runGit(a.RepoPath, gitDefaultTimeout, "clean", "-fd", "--", path)
		if err != nil {
			return "", err
		}
		if err := gitEnsureSuccess(o2, "git clean"); err != nil {
			return "", err
		}
	}
	return "", nil
}

func gitDiscardAllHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	if o, err := runGit(a.RepoPath, gitDefaultTimeout, "restore", "--", "."); err != nil {
		return "", err
	} else if err := gitEnsureSuccess(o, "git restore ."); err != nil {
		return "", err
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "clean", "-fd")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git clean -fd"); err != nil {
		return "", err
	}
	return "", nil
}

func gitDiscardUnstagedHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	// 仅还原已跟踪文件的工作区修改；未跟踪文件属于另一个分组，不能动。
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "restore", "--", ".")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git restore ."); err != nil {
		return "", err
	}
	return "", nil
}

func gitCleanUntrackedHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	// 仅删除未跟踪文件/目录；已跟踪文件的未暂存修改必须保留。
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "clean", "-fd")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git clean -fd"); err != nil {
		return "", err
	}
	return "", nil
}

// ---- commit ----

type commitArgs struct {
	RepoPath string `json:"repoPath"`
	Message  string `json:"message"`
}

func gitCommitHandler(_, argsJson string) (string, error) {
	var a commitArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Message) == "" {
		return "", core.ErrString("commit message 不能为空")
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	o, err := runGit(a.RepoPath, gitCommitTimeout, "commit", "-m", a.Message)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git commit"); err != nil {
		return "", err
	}
	hash, ok, err := gitLine(a.RepoPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", core.ErrString("无法读取新 commit hash")
	}
	return core.EncodeResult(hash)
}

func gitCommitAmendHandler(_, argsJson string) (string, error) {
	var a commitArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	args := []string{"commit", "--amend"}
	if strings.TrimSpace(a.Message) == "" {
		args = append(args, "--no-edit")
	} else {
		args = append(args, "-m", a.Message)
	}
	o, err := runGit(a.RepoPath, gitCommitTimeout, args...)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git commit --amend"); err != nil {
		return "", err
	}
	hash, ok, err := gitLine(a.RepoPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", core.ErrString("无法读取 commit hash")
	}
	return core.EncodeResult(hash)
}

// ---- diff ----

type getDiffArgs struct {
	RepoPath     string `json:"repoPath"`
	RelativePath string `json:"relativePath"`
	OldPath      string `json:"oldPath"`
	Staged       bool   `json:"staged"`
}

func gitGetDiffHandler(_, argsJson string) (string, error) {
	var a getDiffArgs
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
	language := langFromPath(path)

	var original, modified string
	originalIsText := true
	modifiedIsText := true
	worktreeContent, worktreeIsText, worktreeExists := readWorktreeFile(a.RepoPath, path)
	if a.Staged {
		originalPath := path
		if strings.TrimSpace(a.OldPath) != "" {
			oldPath, normalizeErr := normalizeGitRelativePath(a.RepoPath, a.OldPath)
			if normalizeErr != nil {
				return "", normalizeErr
			}
			originalPath = oldPath
		}
		original, originalIsText = gitShowDecodedText(a.RepoPath, "HEAD:"+originalPath)
		modified, modifiedIsText = gitShowDecodedText(a.RepoPath, ":"+path)
	} else {
		original, originalIsText = gitShowDecodedText(a.RepoPath, ":"+path)
		modified, modifiedIsText = worktreeContent, worktreeIsText
	}

	return core.EncodeResult(gitFileDiff{
		Original:        original,
		Modified:        modified,
		Language:        language,
		Binary:          !originalIsText || !modifiedIsText,
		Hunks:           []gitDiffHunkHeader{},
		WorktreeContent: worktreeContent,
		WorktreeExists:  worktreeExists,
		WorktreeIsText:  worktreeIsText,
	})
}

// ---- branches ----

func gitListBranchesHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	repositoryRoot, err := resolveGitRepositoryRoot(a.RepoPath)
	if err != nil {
		return "", err
	}
	current := "HEAD"
	if c, ok, _ := gitLine(repositoryRoot, "rev-parse", "--abbrev-ref", "HEAD"); ok {
		current = c
	}
	lines, err := gitLines(repositoryRoot, "for-each-ref",
		"--format=%(refname:short)|%(HEAD)|%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return "", err
	}
	branches := []gitBranchEntry{}
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 3)
		name := ""
		head := ""
		refname := ""
		if len(parts) >= 1 {
			name = strings.TrimSpace(parts[0])
		}
		if len(parts) >= 2 {
			head = strings.TrimSpace(parts[1])
		}
		if len(parts) >= 3 {
			refname = strings.TrimSpace(parts[2])
		}
		if name == "" || strings.HasSuffix(name, "/HEAD") {
			continue
		}
		isRemote := strings.HasPrefix(refname, "refs/remotes/")
		branches = append(branches, gitBranchEntry{
			Name:      name,
			IsCurrent: !isRemote && head == "*",
			IsRemote:  isRemote,
		})
	}
	return core.EncodeResult(gitBranches{Current: current, Branches: branches})
}

type checkoutArgs struct {
	RepoPath     string `json:"repoPath"`
	BranchName   string `json:"branchName"`
	Create       bool   `json:"create"`
	RemoteBranch bool   `json:"remoteBranch"`
	StashChanges bool   `json:"stashChanges"`
}

func gitCheckoutBranchHandler(_, argsJson string) (string, error) {
	var a checkoutArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	// The UI only sets StashChanges after the user explicitly agrees. Include
	// untracked files as well, otherwise checkout can still be blocked by files
	// that would be created by the target branch.
	if a.StashChanges && !a.Create {
		stashMessage := "MinCode: before switching to " + a.BranchName
		o, err := runGit(a.RepoPath, gitDefaultTimeout,
			"stash", "push", "--include-untracked", "-m", stashMessage)
		if err != nil {
			return "", err
		}
		if err := gitEnsureSuccess(o, "git stash"); err != nil {
			return "", err
		}
	}
	var args []string
	if a.Create {
		args = []string{"checkout", "-b", a.BranchName}
	} else if a.RemoteBranch {
		remoteRef := "refs/remotes/" + a.BranchName
		localName, ok, err := gitLine(a.RepoPath, "for-each-ref", "--format=%(refname:strip=3)", remoteRef)
		if err != nil {
			return "", err
		}
		if !ok || localName == "" {
			return "", core.ErrString("远程分支不存在: " + a.BranchName)
		}
		if _, localExists, err := gitLine(a.RepoPath, "rev-parse", "--verify", "refs/heads/"+localName); err != nil {
			return "", err
		} else if localExists {
			args = []string{"checkout", localName}
		} else {
			args = []string{"checkout", "--track", "-b", localName, a.BranchName}
		}
	} else {
		args = []string{"checkout", a.BranchName}
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, args...)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git checkout"); err != nil {
		return "", err
	}
	return "", nil
}

// ---- log / commit detail ----

type getLogArgs struct {
	RepoPath string `json:"repoPath"`
	Limit    uint32 `json:"limit"`
}

func gitGetLogHandler(_, argsJson string) (string, error) {
	var a getLogArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	refMap := gitBuildRefMap(a.RepoPath)
	count := a.Limit
	if count < 1 {
		count = 1
	}
	if count > 500 {
		count = 500
	}
	lines, err := gitLines(a.RepoPath, "log",
		"--format="+gitLogFormat, "-n"+strconv.FormatUint(uint64(count), 10), "--no-color")
	if err != nil {
		return "", err
	}
	entries := []gitLogEntry{}
	for _, line := range lines {
		if e, ok := parseLogLine(line, refMap); ok {
			entries = append(entries, e)
		}
	}
	return core.EncodeResult(entries)
}

func parseLogLine(line string, refMap map[string][]string) (gitLogEntry, bool) {
	parts := strings.Split(line, "\x1f")
	if len(parts) < 7 {
		return gitLogEntry{}, false
	}
	hash := parts[0]
	t, err := strconv.ParseInt(parts[5], 10, 64)
	if err != nil {
		return gitLogEntry{}, false
	}
	parents := []string{}
	for _, p := range strings.Fields(parts[6]) {
		if p != "" {
			parents = append(parents, p)
		}
	}
	refs := refMap[hash]
	if refs == nil {
		refs = []string{}
	}
	return gitLogEntry{
		Hash:      hash,
		ShortHash: parts[1],
		Author:    parts[2],
		Email:     parts[3],
		Message:   parts[4],
		Time:      t,
		Parents:   parents,
		Refs:      refs,
	}, true
}

type commitHashArgs struct {
	RepoPath   string `json:"repoPath"`
	CommitHash string `json:"commitHash"`
}

func gitGetCommitDetailHandler(_, argsJson string) (string, error) {
	var a commitHashArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	line, ok, err := gitLine(a.RepoPath, "show", "-s", "--format="+gitLogFormat, a.CommitHash)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", core.ErrString("找不到 commit: " + a.CommitHash)
	}
	refMap := gitBuildRefMap(a.RepoPath)
	commit, ok := parseLogLine(line, refMap)
	if !ok {
		return "", core.ErrString("解析 commit 失败: " + a.CommitHash)
	}
	nameLines, err := gitLines(a.RepoPath, "show", "--name-status", "--format=", a.CommitHash)
	if err != nil {
		return "", err
	}
	files := []gitCommitFile{}
	for _, nl := range nameLines {
		fields := strings.Fields(nl)
		if len(fields) == 0 {
			continue
		}
		code := fields[0]
		status := nameStatusToStatus(code[0])
		if strings.HasPrefix(code, "R") || strings.HasPrefix(code, "C") {
			var oldPath *string
			path := ""
			if len(fields) >= 3 {
				oldPath = strPtr(fields[1])
				path = fields[2]
			} else if len(fields) >= 2 {
				path = fields[1]
			}
			files = append(files, gitCommitFile{Path: path, OldPath: oldPath, Status: status})
		} else {
			path := ""
			if len(fields) >= 2 {
				path = fields[1]
			}
			files = append(files, gitCommitFile{Path: path, Status: status})
		}
	}
	return core.EncodeResult(gitCommitDetail{Commit: commit, Files: files})
}

type commitFileDiffArgs struct {
	RepoPath     string `json:"repoPath"`
	CommitHash   string `json:"commitHash"`
	RelativePath string `json:"relativePath"`
	OldPath      string `json:"oldPath"`
}

func gitGetCommitFileDiffHandler(_, argsJson string) (string, error) {
	var a commitFileDiffArgs
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
	originalPath := path
	if a.OldPath != "" {
		originalPath, err = normalizeGitRelativePath(a.RepoPath, a.OldPath)
		if err != nil {
			return "", err
		}
	}
	var original string
	originalIsText := true
	if parent, ok, _ := gitLine(a.RepoPath, "rev-parse", a.CommitHash+"^"); ok {
		original, originalIsText = gitShowDecodedText(a.RepoPath, parent+":"+originalPath)
	}
	modified, modifiedIsText := gitShowDecodedText(a.RepoPath, a.CommitHash+":"+path)
	return core.EncodeResult(gitFileDiff{
		Original: original,
		Modified: modified,
		Language: langFromPath(path),
		Binary:   !originalIsText || !modifiedIsText,
		Hunks:    []gitDiffHunkHeader{},
	})
}

func truncateGitContext(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "\n... (diff truncated)"
}

// gitGetCommitMessageContextHandler 只返回生成提交信息所需的紧凑上下文。
// 文件统计位于 patch 前，即使大 diff 被截断，模型仍能看到完整变更范围。
func gitGetCommitMessageContextHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout,
		"-c", "core.quotepath=false", "diff", "--cached", "--stat=120", "--patch", "--unified=1", "--no-ext-diff")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git diff --cached --stat --patch"); err != nil {
		return "", err
	}
	context := truncateGitContext(gitStdoutString(o), gitCommitMessageContextMax)
	return core.EncodeResult(context)
}

func gitGetStagedPatchHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "diff", "--cached")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git diff --cached"); err != nil {
		return "", err
	}
	patch := gitStdoutString(o)
	if len(patch) > gitStagedPatchMax {
		patch = patch[:gitStagedPatchMax] + "\n... (diff truncated)"
	}
	return core.EncodeResult(patch)
}

// ---- tags / stash ----

func gitListTagsHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	// ref-filter 的十六进制转义是 %1f，不是 git log 的 %x1f；写错时分隔符原样输出，
	// 每一行都切不开，列表就永远是空的。主题放最后，里面即使出现分隔符也不会错位。
	lines, err := gitLines(a.RepoPath, "tag", "-l", "--sort=-creatordate",
		"--format=%(refname:short)%1f%(objectname)%1f%(objecttype)%1f%(*objectname)%1f%(creatordate:iso-strict)%1f%(contents:subject)")
	if err != nil {
		return "", err
	}
	entries := []gitTagEntry{}
	for _, line := range lines {
		parts := strings.SplitN(line, "\x1f", 6)
		if len(parts) < 6 || parts[0] == "" {
			continue
		}
		annotated := parts[2] == "tag"
		commit := parts[1]
		if annotated && parts[3] != "" {
			commit = parts[3]
		}
		var msg *string
		if annotated && parts[5] != "" {
			msg = strPtr(parts[5])
		}
		entries = append(entries, gitTagEntry{
			Name:       parts[0],
			TargetHash: parts[1],
			CommitHash: commit,
			Annotated:  annotated,
			Date:       parts[4],
			Message:    msg,
		})
	}
	return core.EncodeResult(entries)
}

func gitStashListHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	lines, err := gitLines(a.RepoPath, "stash", "list", "--format=%gd%x1f%H%x1f%s")
	if err != nil {
		return "", err
	}
	entries := []gitStashEntry{}
	for _, line := range lines {
		parts := strings.SplitN(line, "\x1f", 3)
		gd := ""
		oid := ""
		msg := ""
		if len(parts) >= 1 {
			gd = parts[0]
		}
		if len(parts) >= 2 {
			oid = parts[1]
		}
		if len(parts) >= 3 {
			msg = parts[2]
		}
		idxStr := strings.TrimSuffix(strings.TrimPrefix(gd, "stash@{"), "}")
		idx := parseIntDefault(idxStr, len(entries))
		entries = append(entries, gitStashEntry{
			Index: idx, Message: msg, Oid: oid,
			Files: gitStashFiles(a.RepoPath, gd),
		})
	}
	return core.EncodeResult(entries)
}

// ---- merge / push / pull / revert ----

type mergeArgs struct {
	RepoPath   string `json:"repoPath"`
	BranchName string `json:"branchName"`
}

func gitMergeBranchHandler(_, argsJson string) (string, error) {
	var a mergeArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	// 始终保留合并结果供用户审阅并手动提交：
	// --no-ff 防止可快进合并直接移动 HEAD，--no-commit 防止自动创建 merge commit。
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "merge", "--no-ff", "--no-commit", a.BranchName)
	if err != nil {
		return "", err
	}
	if o.exitCode != 0 {
		detail := strings.TrimSpace(strings.Join([]string{gitStdoutString(o), gitStderrString(o)}, "\n"))
		conflicts, conflictErr := gitConflictPaths(a.RepoPath)
		if conflictErr == nil && len(conflicts) > 0 {
			return "", core.ErrString("合并冲突，请手动解决；解决结果不会自动提交或推送")
		}
		if detail == "" {
			detail = "exit code " + strconv.Itoa(o.exitCode)
		}
		return "", core.ErrString("git merge 失败: " + detail)
	}
	msg := strings.TrimSpace(gitStdoutString(o))
	message := "已将 " + a.BranchName + " 的合并结果放入本地更改，请检查后手动提交"
	if msg != "" {
		if first := firstLine(msg); first != "" {
			message = first
		}
	}
	return core.EncodeResult(gitSyncResult{Ok: true, Message: message})
}

type pushPullArgs struct {
	RepoPath string  `json:"repoPath"`
	Remote   *string `json:"remote"`
	Branch   *string `json:"branch"`
	Username *string `json:"username"`
	Token    *string `json:"token"`
}

func (a pushPullArgs) resolveRemoteBranch(repoPath string) (string, string) {
	remote := "origin"
	if a.Remote != nil && *a.Remote != "" {
		remote = *a.Remote
	}
	branch := ""
	if a.Branch != nil && *a.Branch != "" {
		branch = *a.Branch
	} else if b, ok, _ := gitLine(repoPath, "rev-parse", "--abbrev-ref", "HEAD"); ok {
		branch = b
	} else {
		branch = "main"
	}
	return remote, branch
}

func gitPushHandler(_, argsJson string) (string, error) {
	var a pushPullArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	remote, branch := a.resolveRemoteBranch(a.RepoPath)
	o, err := runGit(a.RepoPath, gitNetworkTimeout, "push", remote, branch)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git push"); err != nil {
		return "", err
	}
	return core.EncodeResult(gitSyncResult{Ok: true, Message: "已推送 " + branch + " 到 " + remote})
}

func gitPullHandler(_, argsJson string) (string, error) {
	var a pushPullArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	remote, branch := a.resolveRemoteBranch(a.RepoPath)
	o, err := runGit(a.RepoPath, gitNetworkTimeout, "pull", remote, branch)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git pull"); err != nil {
		return "", err
	}
	msg := gitStdoutString(o)
	message := "Pull 成功"
	switch {
	case strings.Contains(msg, "Already up to date") || strings.Contains(msg, "已经是最新"):
		message = "已是最新"
	case strings.Contains(msg, "Fast-forward"):
		message = "Fast-forward 成功"
	case strings.TrimSpace(msg) != "":
		if last := lastLine(msg); last != "" {
			message = last
		}
	}
	return core.EncodeResult(gitSyncResult{Ok: true, Message: message})
}

// gitFetchHandler 只同步远程引用（--prune 清理已删除的远程分支），
// 不改动工作区与当前分支：远端新增分支要先 fetch 才会出现在分支列表里。
func gitFetchHandler(_, argsJson string) (string, error) {
	var a pushPullArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	remote, _ := a.resolveRemoteBranch(a.RepoPath)
	o, err := runGit(a.RepoPath, gitNetworkTimeout, "fetch", "--prune", remote)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git fetch"); err != nil {
		return "", err
	}
	return core.EncodeResult(gitSyncResult{Ok: true, Message: "已同步 " + remote + " 的远程引用"})
}

// gitFetchTagsHandler 从远程拉取全部标签到本地，不改动分支和工作区。
func gitFetchTagsHandler(_, argsJson string) (string, error) {
	var a pushPullArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	remotes, err := gitLines(a.RepoPath, "remote")
	if err != nil {
		return "", err
	}
	if len(remotes) == 0 {
		return "", core.ErrString("仓库没有配置远程仓库，无法拉取标签")
	}
	remote, _ := a.resolveRemoteBranch(a.RepoPath)
	if a.Remote == nil || *a.Remote == "" {
		// 没有 origin 时退回第一个远程，而不是让 git 报"origin 不是仓库"。
		found := false
		for _, name := range remotes {
			if name == remote {
				found = true
				break
			}
		}
		if !found {
			remote = remotes[0]
		}
	}
	o, err := runGit(a.RepoPath, gitNetworkTimeout, "fetch", "--tags", remote)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git fetch --tags"); err != nil {
		return "", err
	}
	return core.EncodeResult(gitSyncResult{Ok: true, Message: "已从 " + remote + " 拉取标签"})
}

func gitRevertHandler(_, argsJson string) (string, error) {
	var a commitHashArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	o, err := runGit(a.RepoPath, gitDefaultTimeout, "revert", "--no-edit", a.CommitHash)
	if err != nil {
		return "", err
	}
	if o.exitCode != 0 {
		_, _ = runGit(a.RepoPath, gitDefaultTimeout, "revert", "--abort")
		return "", core.ErrString("revert 失败: " + gitStderrString(o))
	}
	hash, ok, err := gitLine(a.RepoPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", core.ErrString("无法读取 revert commit hash")
	}
	return core.EncodeResult(hash)
}

// ---- blame ----

func gitBlameHandler(_, argsJson string) (string, error) {
	var a repoRelArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	repositoryRoot, err := resolveGitRepositoryRoot(a.RepoPath)
	if err != nil {
		return "", err
	}
	path, err := normalizeGitRelativePathInRepository(a.RepoPath, repositoryRoot, a.RelativePath)
	if err != nil {
		return "", err
	}
	o, err := runGit(repositoryRoot, gitDefaultTimeout, "blame", "--line-porcelain", "--", path)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(o, "git blame"); err != nil {
		return "", err
	}
	return core.EncodeResult(parseBlamePorcelain(gitStdoutString(o)))
}

func parseBlamePorcelain(text string) []blameHunk {
	hunks := []blameHunk{}
	lines := strings.Split(text, "\n")
	i := 0
	for i < len(lines) {
		header := lines[i]
		i++
		fields := strings.Fields(header)
		// porcelain 头是 "<sha> <原始行号> <最终行号> [<块行数>]"，行号按最终文件算，
		// 提交哈希就是第一个字段（porcelain 没有 "commit " 前缀行）。
		if len(fields) < 3 {
			continue
		}
		commitHash := fields[0]
		lineNo := uint32(parseIntDefault(fields[2], 0))
		if lineNo == 0 {
			continue
		}
		var author, authorEmail, date, message string
		for i < len(lines) {
			l := lines[i]
			if strings.HasPrefix(l, "\t") {
				i++ // 跳过内容行
				break
			}
			i++
			switch {
			case strings.HasPrefix(l, "author "):
				author = strings.TrimPrefix(l, "author ")
			case strings.HasPrefix(l, "author-mail "):
				authorEmail = strings.TrimSuffix(strings.TrimPrefix(
					strings.TrimPrefix(l, "author-mail "), "<"), ">")
			case strings.HasPrefix(l, "author-time "):
				date = strings.TrimPrefix(l, "author-time ")
			case strings.HasPrefix(l, "summary "):
				message = strings.TrimPrefix(l, "summary ")
			}
		}
		short := commitHash
		if len(short) > 7 {
			short = short[:7]
		}
		if n := len(hunks); n > 0 {
			last := &hunks[n-1]
			if last.CommitHash == commitHash && last.LineEnd+1 == lineNo {
				last.LineEnd = lineNo
				continue
			}
		}
		hunks = append(hunks, blameHunk{
			LineStart:       lineNo,
			LineEnd:         lineNo,
			CommitHash:      commitHash,
			CommitHashShort: short,
			Author:          author,
			AuthorEmail:     authorEmail,
			Date:            date,
			Message:         message,
		})
	}
	return hunks
}

// ---- remote / read file at ref ----

type remoteUrlArgs struct {
	RepoPath   string  `json:"repoPath"`
	RemoteName *string `json:"remoteName"`
}

func gitGetRemoteUrlHandler(_, argsJson string) (string, error) {
	var a remoteUrlArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	name := "origin"
	if a.RemoteName != nil && *a.RemoteName != "" {
		name = *a.RemoteName
	}
	url, ok, err := gitLine(a.RepoPath, "remote", "get-url", name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", core.ErrString("找不到 remote '" + name + "'")
	}
	return core.EncodeResult(url)
}

func gitReadFileAtHeadHandler(_, argsJson string) (string, error) {
	var a repoRelArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	return gitReadFileAtRef(a.RepoPath, a.RelativePath, "HEAD")
}

type readFileAtRefArgs struct {
	RepoPath     string `json:"repoPath"`
	RelativePath string `json:"relativePath"`
	GitRef       string `json:"gitRef"`
}

func gitReadFileAtRefHandler(_, argsJson string) (string, error) {
	var a readFileAtRefArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	return gitReadFileAtRef(a.RepoPath, a.RelativePath, a.GitRef)
}

func gitReadFileAtRef(repoPath, relativePath, gitRef string) (string, error) {
	repositoryRoot, err := resolveGitRepositoryRoot(repoPath)
	if err != nil {
		return "", err
	}
	path, err := normalizeGitRelativePathInRepository(repoPath, repositoryRoot, relativePath)
	if err != nil {
		return "", err
	}
	gitRef = strings.TrimSpace(gitRef)
	if gitRef == "" {
		return "", core.ErrString("Git ref 不能为空")
	}
	if _, ok, _ := gitLine(repositoryRoot, "rev-parse", "--verify", gitRef); !ok {
		return "", core.ErrString("无效的 Git ref: " + gitRef)
	}
	return core.EncodeResult(gitShowText(repositoryRoot, gitRef+":"+path))
}

// ---- conflicts ----

func gitListConflictsHandler(_, argsJson string) (string, error) {
	var a repoOnlyArgs
	if err := core.DecodeArgs(argsJson, &a); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(a.RepoPath); err != nil {
		return "", err
	}
	lines, err := gitLines(a.RepoPath, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return "", err
	}
	if lines == nil {
		lines = []string{}
	}
	return core.EncodeResult(lines)
}

// ---- 小工具 ----

func parseIntDefault(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimRight(s[:idx], "\r")
	}
	return s
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}

// fileExists 判断仓库内相对路径对应的文件是否存在（用于区分 add / add -u）。
func fileExists(repoPath, relPath string) bool {
	info, err := os.Stat(filepath.Join(repoPath, filepath.FromSlash(relPath)))
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// readWorktreeFile 读取工作区文件；返回内容、是否为文本、是否存在。
func readWorktreeFile(repoPath, relPath string) (string, bool, bool) {
	data, err := os.ReadFile(filepath.Join(repoPath, filepath.FromSlash(relPath)))
	if err != nil {
		return "", true, false
	}
	content, isText := decodeGitText(data)
	return content, isText, true
}

var _ = time.Second
