package git

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"mincode/app/backend/core"
)

const agentWorktreeReason = "MinCode subagent isolated worktree"

var agentTaskSlugPattern = regexp.MustCompile(`[^a-z0-9]+`)
var agentCommitHashPattern = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)
var agentBranchPattern = regexp.MustCompile(`^mincode/agent/[a-z0-9][a-z0-9-]{0,95}$`)

type createAgentWorktreeArgs struct {
	RepoPath   string `json:"repoPath"`
	TaskID     string `json:"taskId"`
	BaseCommit string `json:"baseCommit,omitempty"`
}

type inspectAgentWorktreeArgs struct {
	RepoPath     string `json:"repoPath"`
	WorktreePath string `json:"worktreePath"`
	BaseCommit   string `json:"baseCommit"`
}

type agentWorktreeResult struct {
	Path        string `json:"path"`
	ProjectPath string `json:"projectPath"`
	Name        string `json:"name"`
	Branch      string `json:"branch"`
	BaseCommit  string `json:"baseCommit"`
}

type agentWorktreeInspection struct {
	Name         string   `json:"name"`
	Branch       string   `json:"branch"`
	Head         string   `json:"head"`
	Clean        bool     `json:"clean"`
	CommitsAhead int      `json:"commitsAhead"`
	ChangedFiles []string `json:"changedFiles"`
}

func init() {
	core.Register("git_create_agent_worktree", gitCreateAgentWorktreeHandler)
	core.Register("git_inspect_agent_worktree", gitInspectAgentWorktreeHandler)
	core.Register("git_integrate_agent_worktree", gitIntegrateAgentWorktreeHandler)
}

func agentTaskSlug(value string) string {
	slug := strings.Trim(agentTaskSlugPattern.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-"), "-")
	if len(slug) > 32 {
		slug = strings.Trim(slug[:32], "-")
	}
	if slug == "" {
		return "task"
	}
	return slug
}

func randomAgentWorktreeSuffix() (string, error) {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func repositoryRoot(repoPath string) (string, error) {
	topLevel, err := runGit(repoPath, gitDefaultTimeout, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(topLevel, "git rev-parse --show-toplevel"); err != nil {
		return "", err
	}
	return resolveRepoDir(strings.TrimSpace(string(topLevel.stdout))), nil
}

func gitCreateAgentWorktreeHandler(_, argsJSON string) (string, error) {
	var args createAgentWorktreeArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(args.RepoPath); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.TaskID) == "" {
		return "", core.ErrString("子任务 ID 不能为空")
	}

	repositoryRoot, err := repositoryRoot(args.RepoPath)
	if err != nil {
		return "", err
	}
	requestedRoot, err := filepath.Abs(resolveRepoDir(args.RepoPath))
	if err != nil {
		return "", fmt.Errorf("解析项目目录失败：%w", err)
	}
	projectRelative, err := filepath.Rel(repositoryRoot, requestedRoot)
	if err != nil || projectRelative == ".." || strings.HasPrefix(projectRelative, ".."+string(filepath.Separator)) {
		return "", core.ErrString("项目目录必须位于 Git 仓库内")
	}
	status, err := runGit(repositoryRoot, gitDefaultTimeout, "status", "--porcelain=v1", "-z")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(status, "git status"); err != nil {
		return "", err
	}
	if len(status.stdout) != 0 {
		return "", core.ErrString("并行写任务要求当前 Git 工作区干净，请先提交、暂存到其他分支或清理现有改动")
	}
	baseRevision := strings.TrimSpace(args.BaseCommit)
	if baseRevision == "" {
		baseRevision = "HEAD"
	}
	base, err := runGit(repositoryRoot, gitDefaultTimeout, "rev-parse", "--verify", baseRevision+"^{commit}")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(base, "git rev-parse base commit"); err != nil {
		return "", fmt.Errorf("并行开发要求仓库至少有一个提交：%w", err)
	}
	baseCommit := strings.TrimSpace(string(base.stdout))

	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("定位缓存目录失败：%w", err)
	}
	repositoryHash := sha256.Sum256([]byte(repositoryRoot))
	repositoryKey := hex.EncodeToString(repositoryHash[:8])
	slug := agentTaskSlug(args.TaskID)
	suffix, err := randomAgentWorktreeSuffix()
	if err != nil {
		return "", fmt.Errorf("生成工作树标识失败：%w", err)
	}
	name := slug + "-" + suffix
	parent := filepath.Join(cacheRoot, "MinCode", "agent-worktrees", repositoryKey)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("创建 Agent 工作树目录失败：%w", err)
	}
	worktreePath := filepath.Join(parent, name)
	branch := "mincode/agent/" + name

	check, err := runGit(repositoryRoot, gitDefaultTimeout, "check-ref-format", "--branch", branch)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(check, "git check-ref-format"); err != nil {
		return "", err
	}
	created, err := runGit(repositoryRoot, gitDefaultTimeout,
		"worktree", "add", "-b", branch, worktreePath, baseCommit)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(created, "git worktree add"); err != nil {
		_ = os.RemoveAll(worktreePath)
		return "", err
	}
	locked, lockErr := runGit(repositoryRoot, gitDefaultTimeout,
		"worktree", "lock", "--reason", agentWorktreeReason, worktreePath)
	if lockErr != nil || gitEnsureSuccess(locked, "git worktree lock") != nil {
		removed, _ := runGit(repositoryRoot, gitDefaultTimeout,
			"worktree", "remove", "--force", worktreePath)
		_ = removed
		branchDelete, _ := runGit(repositoryRoot, gitDefaultTimeout, "branch", "-D", branch)
		_ = branchDelete
		if lockErr != nil {
			return "", lockErr
		}
		return "", gitEnsureSuccess(locked, "git worktree lock")
	}

	projectPath := worktreePath
	if projectRelative != "." {
		projectPath = filepath.Join(worktreePath, projectRelative)
	}
	return core.EncodeResult(agentWorktreeResult{
		Path:        worktreePath,
		ProjectPath: projectPath,
		Name:        name,
		Branch:      branch,
		BaseCommit:  baseCommit,
	})
}

func findAgentWorktree(repoPath, requestedPath string) (gitWorktreeEntry, error) {
	requested, err := filepath.Abs(strings.TrimSpace(requestedPath))
	if err != nil || strings.TrimSpace(requestedPath) == "" {
		return gitWorktreeEntry{}, fmt.Errorf("无效的 Agent 工作树路径")
	}
	listed, err := runGit(repoPath, gitDefaultTimeout, "worktree", "list", "--porcelain")
	if err != nil {
		return gitWorktreeEntry{}, err
	}
	if err := gitEnsureSuccess(listed, "git worktree list"); err != nil {
		return gitWorktreeEntry{}, err
	}
	requestedInfo, err := os.Stat(requested)
	if err != nil {
		return gitWorktreeEntry{}, fmt.Errorf("Agent 工作树路径不可用：%w", err)
	}
	for _, entry := range parseWorktreePorcelain(gitStdoutString(listed)) {
		candidateInfo, statErr := os.Stat(entry.Path)
		if statErr != nil || !os.SameFile(requestedInfo, candidateInfo) {
			continue
		}
		if !entry.IsLocked || entry.LockReason == nil || *entry.LockReason != agentWorktreeReason {
			return gitWorktreeEntry{}, fmt.Errorf("工作树不是受保护的 MinCode Agent 工作树")
		}
		if entry.Branch == nil || !strings.HasPrefix(*entry.Branch, "mincode/agent/") {
			return gitWorktreeEntry{}, fmt.Errorf("Agent 工作树分支无效")
		}
		return entry, nil
	}
	return gitWorktreeEntry{}, fmt.Errorf("Agent 工作树不存在或已从仓库移除")
}

func gitInspectAgentWorktreeHandler(_, argsJSON string) (string, error) {
	var args inspectAgentWorktreeArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(args.RepoPath); err != nil {
		return "", err
	}
	baseCommit := strings.TrimSpace(args.BaseCommit)
	if baseCommit == "" {
		return "", core.ErrString("Agent 工作树基线不能为空")
	}
	entry, err := findAgentWorktree(args.RepoPath, args.WorktreePath)
	if err != nil {
		return "", err
	}
	verifyBase, err := runGit(args.RepoPath, gitDefaultTimeout, "rev-parse", "--verify", baseCommit+"^{commit}")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(verifyBase, "git rev-parse base commit"); err != nil {
		return "", err
	}
	head, err := runGit(entry.Path, gitDefaultTimeout, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(head, "git rev-parse HEAD"); err != nil {
		return "", err
	}
	status, err := runGit(entry.Path, gitDefaultTimeout, "status", "--porcelain=v1", "-z")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(status, "git status"); err != nil {
		return "", err
	}
	count, err := runGit(entry.Path, gitDefaultTimeout, "rev-list", "--count", baseCommit+"..HEAD")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(count, "git rev-list"); err != nil {
		return "", err
	}
	commitsAhead, err := strconv.Atoi(strings.TrimSpace(gitStdoutString(count)))
	if err != nil {
		return "", fmt.Errorf("解析 Agent 提交数量失败：%w", err)
	}
	changed, err := runGit(entry.Path, gitDefaultTimeout, "diff", "--name-only", "-z", baseCommit+"..HEAD")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(changed, "git diff --name-only"); err != nil {
		return "", err
	}
	branch := strings.TrimPrefix(*entry.Branch, "refs/heads/")
	return core.EncodeResult(agentWorktreeInspection{
		Name:         filepath.Base(entry.Path),
		Branch:       branch,
		Head:         strings.TrimSpace(gitStdoutString(head)),
		Clean:        len(status.stdout) == 0,
		CommitsAhead: commitsAhead,
		ChangedFiles: splitNulFields(gitStdoutString(changed)),
	})
}

type integrateAgentWorktreeArgs struct {
	RepoPath   string `json:"repoPath"`
	Branch     string `json:"branch"`
	CommitHash string `json:"commitHash"`
	BaseCommit string `json:"baseCommit"`
}

type integrateAgentWorktreeResult struct {
	Branch            string `json:"branch"`
	SourceCommit      string `json:"sourceCommit"`
	IntegratedHead    string `json:"integratedHead"`
	AlreadyIntegrated bool   `json:"alreadyIntegrated"`
	Cleaned           bool   `json:"cleaned"`
	CleanupError      string `json:"cleanupError,omitempty"`
}

func resolveCommit(repoPath, revision, label string) (string, error) {
	result, err := runGit(repoPath, gitDefaultTimeout, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(result, "git rev-parse "+label); err != nil {
		return "", err
	}
	return strings.TrimSpace(string(result.stdout)), nil
}

func canonicalCommit(repoPath, revision, label string) (string, error) {
	revision = strings.TrimSpace(revision)
	if !agentCommitHashPattern.MatchString(revision) {
		return "", core.ErrString(label + " 必须是完整提交哈希")
	}
	return resolveCommit(repoPath, revision, label)
}

func ensureNoGitOperation(repoPath string) error {
	markers := []string{
		"CHERRY_PICK_HEAD",
		"MERGE_HEAD",
		"REVERT_HEAD",
		"rebase-merge",
		"rebase-apply",
		"sequencer",
	}
	for _, marker := range markers {
		resolved, err := runGit(repoPath, gitDefaultTimeout, "rev-parse", "--git-path", marker)
		if err != nil {
			return err
		}
		if err := gitEnsureSuccess(resolved, "git rev-parse --git-path"); err != nil {
			return err
		}
		path := strings.TrimSpace(string(resolved.stdout))
		if !filepath.IsAbs(path) {
			path = filepath.Join(repoPath, path)
		}
		if _, err := os.Stat(path); err == nil {
			return core.ErrString("目标仓库存在未完成的 Git 操作，请先继续或中止该操作")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("检查 Git 操作状态失败：%w", err)
		}
	}
	return nil
}

func ensureCleanWorktree(repoPath, label string) error {
	status, err := runGit(repoPath, gitDefaultTimeout, "status", "--porcelain=v1", "-z")
	if err != nil {
		return err
	}
	if err := gitEnsureSuccess(status, "git status"); err != nil {
		return err
	}
	if len(status.stdout) != 0 {
		return core.ErrString(label + "工作区存在未提交改动")
	}
	return nil
}

func agentWorktreeByBranch(repoPath, branch string) (*gitWorktreeEntry, error) {
	listed, err := runGit(repoPath, gitDefaultTimeout, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	if err := gitEnsureSuccess(listed, "git worktree list"); err != nil {
		return nil, err
	}
	for _, candidate := range parseWorktreePorcelain(gitStdoutString(listed)) {
		if candidate.Branch == nil || *candidate.Branch != branch {
			continue
		}
		entry := candidate
		if !entry.IsLocked || entry.LockReason == nil || *entry.LockReason != agentWorktreeReason {
			return nil, core.ErrString("目标不是受保护的 MinCode Agent 工作树")
		}
		return &entry, nil
	}
	return nil, nil
}

func agentPatchIntegrated(repoPath, upstream, commitHash, baseCommit string) (bool, error) {
	ancestor, err := runGit(repoPath, gitDefaultTimeout, "merge-base", "--is-ancestor", commitHash, upstream)
	if err != nil {
		return false, err
	}
	if ancestor.exitCode == 0 {
		return true, nil
	}
	if ancestor.exitCode != 1 {
		return false, gitEnsureSuccess(ancestor, "git merge-base --is-ancestor")
	}
	result, err := runGit(repoPath, gitDefaultTimeout, "cherry", upstream, commitHash, baseCommit)
	if err != nil {
		return false, err
	}
	if err := gitEnsureSuccess(result, "git cherry"); err != nil {
		return false, err
	}
	line := strings.TrimSpace(string(result.stdout))
	if line == "- "+commitHash {
		return true, nil
	}
	if line == "+ "+commitHash {
		return false, nil
	}
	return false, fmt.Errorf("无法确认 Agent 提交的补丁集成状态")
}

func cleanupIntegratedAgentWorktree(repositoryRoot string, entry *gitWorktreeEntry, branch string, branchExists bool) error {
	if entry != nil {
		removed, err := runGit(repositoryRoot, gitDefaultTimeout, "worktree", "remove", "--force", "--force", entry.Path)
		if err != nil {
			return err
		}
		if err := gitEnsureSuccess(removed, "git worktree remove"); err != nil {
			return err
		}
	}
	if !branchExists {
		return nil
	}
	deleted, err := runGit(repositoryRoot, gitDefaultTimeout, "branch", "-D", branch)
	if err != nil {
		return err
	}
	return gitEnsureSuccess(deleted, "git branch -D")
}

func gitIntegrateAgentWorktreeHandler(_, argsJSON string) (string, error) {
	var args integrateAgentWorktreeArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := gitEnsureRepo(args.RepoPath); err != nil {
		return "", err
	}
	branch := strings.TrimSpace(args.Branch)
	if !agentBranchPattern.MatchString(branch) {
		return "", core.ErrString("Agent 分支名称无效")
	}
	repositoryRoot, err := repositoryRoot(args.RepoPath)
	if err != nil {
		return "", err
	}
	if err := ensureNoGitOperation(repositoryRoot); err != nil {
		return "", err
	}
	if err := ensureCleanWorktree(repositoryRoot, "目标"); err != nil {
		return "", err
	}
	commitHash, err := canonicalCommit(repositoryRoot, args.CommitHash, "Agent 提交")
	if err != nil {
		return "", err
	}
	baseCommit, err := canonicalCommit(repositoryRoot, args.BaseCommit, "Agent 基线提交")
	if err != nil {
		return "", err
	}
	branchResult, err := runGit(repositoryRoot, gitDefaultTimeout, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", err
	}
	branchExists := branchResult.exitCode == 0
	if branchExists && strings.TrimSpace(string(branchResult.stdout)) != commitHash {
		return "", core.ErrString("Agent 分支 HEAD 与待集成提交不一致")
	}
	entry, err := agentWorktreeByBranch(repositoryRoot, branch)
	if err != nil {
		return "", err
	}
	if entry != nil {
		if entry.Head == nil || *entry.Head != commitHash {
			return "", core.ErrString("Agent 工作树 HEAD 与待集成提交不一致")
		}
		if err := ensureCleanWorktree(entry.Path, "Agent "); err != nil {
			return "", err
		}
		currentInfo, currentErr := os.Stat(repositoryRoot)
		sourceInfo, sourceErr := os.Stat(entry.Path)
		if currentErr != nil || sourceErr != nil {
			return "", core.ErrString("无法校验源与目标工作树")
		}
		if os.SameFile(currentInfo, sourceInfo) {
			return "", core.ErrString("不能在 Agent 源工作树中集成自身提交")
		}
	}
	ancestor, err := runGit(repositoryRoot, gitDefaultTimeout, "merge-base", "--is-ancestor", baseCommit, commitHash)
	if err != nil {
		return "", err
	}
	if ancestor.exitCode != 0 {
		return "", core.ErrString("Agent 提交不是声明基线的后代")
	}
	count, err := runGit(repositoryRoot, gitDefaultTimeout, "rev-list", "--count", baseCommit+".."+commitHash)
	if err != nil {
		return "", err
	}
	if err := gitEnsureSuccess(count, "git rev-list --count"); err != nil {
		return "", err
	}
	if strings.TrimSpace(string(count.stdout)) != "1" {
		return "", core.ErrString("Agent 结果必须相对基线恰好包含一个提交")
	}
	currentHead, err := resolveCommit(repositoryRoot, "HEAD", "目标 HEAD")
	if err != nil {
		return "", err
	}
	baseIntegrated, err := agentPatchIntegrated(repositoryRoot, currentHead, baseCommit, baseCommit+"^")
	if err != nil {
		return "", err
	}
	if !baseIntegrated {
		return "", core.ErrString("Agent 结果的前置基线尚未集成，请按依赖顺序先集成上游任务")
	}
	alreadyIntegrated, err := agentPatchIntegrated(repositoryRoot, currentHead, commitHash, baseCommit)
	if err != nil {
		return "", err
	}
	if (!branchExists || entry == nil) && !alreadyIntegrated {
		return "", core.ErrString("Agent 源分支或 Worktree 不存在，无法安全验证未集成结果")
	}
	integratedHead := currentHead
	if !alreadyIntegrated {
		picked, pickErr := runGit(repositoryRoot, gitDefaultTimeout, "cherry-pick", commitHash)
		if pickErr != nil || gitEnsureSuccess(picked, "git cherry-pick") != nil {
			aborted, abortErr := runGit(repositoryRoot, gitDefaultTimeout, "cherry-pick", "--abort")
			if abortErr != nil || gitEnsureSuccess(aborted, "git cherry-pick --abort") != nil {
				rolledBackHead, headErr := resolveCommit(repositoryRoot, "HEAD", "回滚后 HEAD")
				cleanErr := ensureCleanWorktree(repositoryRoot, "回滚后目标")
				if headErr != nil || cleanErr != nil || rolledBackHead != currentHead {
					return "", core.ErrString("Agent 提交集成失败，且自动回滚失败；请手动检查 Git 状态")
				}
			}
			return "", core.ErrString("Agent 提交集成失败，已自动回滚；源 Worktree 保留供审查")
		}
		integratedHead, err = resolveCommit(repositoryRoot, "HEAD", "集成后 HEAD")
		if err != nil {
			return "", err
		}
		confirmed, confirmErr := agentPatchIntegrated(repositoryRoot, integratedHead, commitHash, baseCommit)
		if confirmErr != nil || !confirmed {
			return "", core.ErrString("cherry-pick 完成但无法确认补丁等价，请保留现场并人工检查")
		}
	}
	result := integrateAgentWorktreeResult{
		Branch:            branch,
		SourceCommit:      commitHash,
		IntegratedHead:    integratedHead,
		AlreadyIntegrated: alreadyIntegrated,
	}
	if cleanupErr := cleanupIntegratedAgentWorktree(repositoryRoot, entry, branch, branchExists); cleanupErr != nil {
		result.CleanupError = "Agent 结果已集成，但隔离 Worktree 清理失败；请使用相同参数重试"
		return core.EncodeResult(result)
	}
	result.Cleaned = true
	return core.EncodeResult(result)
}
