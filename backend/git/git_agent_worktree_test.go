package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentTaskSlugIsBoundedAndSafe(t *testing.T) {
	cases := map[string]string{
		"API / 用户模块":                   "api",
		"../../escape":                 "escape",
		"___":                          "task",
		strings.Repeat("a", 80) + " z": strings.Repeat("a", 32),
	}
	for input, want := range cases {
		if got := agentTaskSlug(input); got != want {
			t.Fatalf("agentTaskSlug(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCreateAgentWorktreeUsesHeadAndLocksIt(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	payload, err := json.Marshal(createAgentWorktreeArgs{
		RepoPath: repository,
		TaskID:   "Backend API",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := gitCreateAgentWorktreeHandler("", string(payload))
	if err != nil {
		t.Fatalf("create agent worktree: %v", err)
	}
	var result agentWorktreeResult
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Branch, "mincode/agent/backend-api-") {
		t.Fatalf("unexpected branch: %s", result.Branch)
	}
	if filepath.Base(result.Path) != result.Name {
		t.Fatalf("path/name mismatch: %#v", result)
	}
	if info, err := os.Stat(result.Path); err != nil || !info.IsDir() {
		t.Fatalf("worktree unavailable: %v", err)
	}
	head, err := runGit(result.Path, gitDefaultTimeout, "rev-parse", "HEAD")
	if err != nil || gitEnsureSuccess(head, "git rev-parse HEAD") != nil {
		t.Fatalf("read worktree HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(head.stdout)); got != result.BaseCommit {
		t.Fatalf("worktree HEAD = %s, want %s", got, result.BaseCommit)
	}
	branch, err := runGit(result.Path, gitDefaultTimeout, "branch", "--show-current")
	if err != nil || gitEnsureSuccess(branch, "git branch --show-current") != nil {
		t.Fatalf("read worktree branch: %v", err)
	}
	if got := strings.TrimSpace(string(branch.stdout)); got != result.Branch {
		t.Fatalf("worktree branch = %s, want %s", got, result.Branch)
	}
	listed, err := runGit(repository, gitDefaultTimeout, "worktree", "list", "--porcelain")
	if err != nil || gitEnsureSuccess(listed, "git worktree list") != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	porcelain := string(listed.stdout)
	if !strings.Contains(porcelain, "worktree "+result.Path) ||
		!strings.Contains(porcelain, "locked "+agentWorktreeReason) {
		t.Fatalf("agent worktree is not locked:\n%s", porcelain)
	}

	// Tests must explicitly unlock before cleanup; production keeps completed worktrees
	// for review until the user or parent Agent removes them.
	runGitTestCommand(t, repository, "worktree", "unlock", result.Path)
	runGitTestCommand(t, repository, "worktree", "remove", "--force", result.Path)
	runGitTestCommand(t, repository, "branch", "-D", result.Branch)
}

func TestCreateAgentWorktreeRejectsRepositoryWithoutCommit(t *testing.T) {
	repository := t.TempDir()
	runGitTestCommand(t, repository, "init")
	payload, _ := json.Marshal(createAgentWorktreeArgs{RepoPath: repository, TaskID: "task"})
	if _, err := gitCreateAgentWorktreeHandler("", string(payload)); err == nil {
		t.Fatal("expected an unborn repository error")
	}
}

func TestInspectAgentWorktreeRequiresCleanCommit(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	payload, _ := json.Marshal(createAgentWorktreeArgs{RepoPath: repository, TaskID: "inspect"})
	encoded, err := gitCreateAgentWorktreeHandler("", string(payload))
	if err != nil {
		t.Fatal(err)
	}
	var created agentWorktreeResult
	if err := json.Unmarshal([]byte(encoded), &created); err != nil {
		t.Fatal(err)
	}
	defer func() {
		runGitTestCommand(t, repository, "worktree", "unlock", created.Path)
		runGitTestCommand(t, repository, "worktree", "remove", "--force", created.Path)
		runGitTestCommand(t, repository, "branch", "-D", created.Branch)
	}()

	inspect := func() agentWorktreeInspection {
		request, _ := json.Marshal(inspectAgentWorktreeArgs{
			RepoPath: repository, WorktreePath: created.Path, BaseCommit: created.BaseCommit,
		})
		result, inspectErr := gitInspectAgentWorktreeHandler("", string(request))
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		var value agentWorktreeInspection
		if err := json.Unmarshal([]byte(result), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}

	initial := inspect()
	if !initial.Clean || initial.CommitsAhead != 0 || len(initial.ChangedFiles) != 0 {
		t.Fatalf("unexpected initial inspection: %#v", initial)
	}
	writeWorktreeTestFile(t, created.Path, "agent.txt", "pending\n")
	dirty := inspect()
	if dirty.Clean || dirty.CommitsAhead != 0 {
		t.Fatalf("dirty worktree inspection: %#v", dirty)
	}
	runGitTestCommand(t, created.Path, "add", "agent.txt")
	runGitTestCommand(t, created.Path, "commit", "-m", "agent change")
	committed := inspect()
	if !committed.Clean || committed.CommitsAhead != 1 || committed.Head == created.BaseCommit {
		t.Fatalf("committed worktree inspection: %#v", committed)
	}
	if len(committed.ChangedFiles) != 1 || committed.ChangedFiles[0] != "agent.txt" {
		t.Fatalf("changed files: %#v", committed.ChangedFiles)
	}
}

func TestCreateAgentWorktreeFromExplicitBaseCommit(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	baseResult, err := runGit(repository, gitDefaultTimeout, "rev-parse", "HEAD")
	if err != nil || gitEnsureSuccess(baseResult, "git rev-parse HEAD") != nil {
		t.Fatalf("read base HEAD: %v", err)
	}
	base := strings.TrimSpace(string(baseResult.stdout))
	writeWorktreeTestFile(t, repository, "later.txt", "later\n")
	runGitTestCommand(t, repository, "add", "later.txt")
	runGitTestCommand(t, repository, "commit", "-m", "later")

	payload, _ := json.Marshal(createAgentWorktreeArgs{
		RepoPath: repository, TaskID: "explicit-base", BaseCommit: base,
	})
	encoded, err := gitCreateAgentWorktreeHandler("", string(payload))
	if err != nil {
		t.Fatal(err)
	}
	var result agentWorktreeResult
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	defer func() {
		runGitTestCommand(t, repository, "worktree", "unlock", result.Path)
		runGitTestCommand(t, repository, "worktree", "remove", "--force", result.Path)
		runGitTestCommand(t, repository, "branch", "-D", result.Branch)
	}()
	if result.BaseCommit != base {
		t.Fatalf("base commit = %q, want %q", result.BaseCommit, base)
	}
	headResult, err := runGit(result.Path, gitDefaultTimeout, "rev-parse", "HEAD")
	if err != nil || gitEnsureSuccess(headResult, "git rev-parse HEAD") != nil {
		t.Fatalf("read worktree HEAD: %v", err)
	}
	head := strings.TrimSpace(string(headResult.stdout))
	if head != base {
		t.Fatalf("worktree HEAD = %q, want explicit base %q", head, base)
	}
}

func TestCreateAgentWorktreeRejectsDirtyRepository(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	writeWorktreeTestFile(t, repository, "dirty.txt", "dirty\n")
	payload, _ := json.Marshal(createAgentWorktreeArgs{RepoPath: repository, TaskID: "dirty"})
	if _, err := gitCreateAgentWorktreeHandler("", string(payload)); err == nil || !strings.Contains(err.Error(), "工作区干净") {
		t.Fatalf("expected dirty repository rejection, got %v", err)
	}
}

func TestCreateAgentWorktreePreservesNestedProjectScope(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	nested := filepath.Join(repository, "apps", "web")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorktreeTestFile(t, repository, "apps/web/app.txt", "app\n")
	runGitTestCommand(t, repository, "add", "apps/web/app.txt")
	runGitTestCommand(t, repository, "commit", "-m", "nested app")
	payload, _ := json.Marshal(createAgentWorktreeArgs{RepoPath: nested, TaskID: "nested"})
	encoded, err := gitCreateAgentWorktreeHandler("", string(payload))
	if err != nil {
		t.Fatal(err)
	}
	var result agentWorktreeResult
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	defer func() {
		runGitTestCommand(t, repository, "worktree", "unlock", result.Path)
		runGitTestCommand(t, repository, "worktree", "remove", "--force", result.Path)
		runGitTestCommand(t, repository, "branch", "-D", result.Branch)
	}()
	want := filepath.Join(result.Path, "apps", "web")
	if result.ProjectPath != want {
		t.Fatalf("project path = %q, want %q", result.ProjectPath, want)
	}
	if _, err := os.Stat(filepath.Join(result.ProjectPath, "app.txt")); err != nil {
		t.Fatalf("nested project file unavailable: %v", err)
	}
}

func createCommittedAgentWorktree(t *testing.T, repository, taskID, fileName, content string) (agentWorktreeResult, string) {
	t.Helper()
	payload, _ := json.Marshal(createAgentWorktreeArgs{RepoPath: repository, TaskID: taskID})
	encoded, err := gitCreateAgentWorktreeHandler("", string(payload))
	if err != nil {
		t.Fatal(err)
	}
	var created agentWorktreeResult
	if err := json.Unmarshal([]byte(encoded), &created); err != nil {
		t.Fatal(err)
	}
	writeWorktreeTestFile(t, created.Path, fileName, content)
	runGitTestCommand(t, created.Path, "add", fileName)
	runGitTestCommand(t, created.Path, "commit", "-m", "agent result")
	head, err := resolveCommit(created.Path, "HEAD", "agent HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return created, head
}

func integrateAgentTestPayload(t *testing.T, repository string, created agentWorktreeResult, commitHash string) string {
	t.Helper()
	payload, err := json.Marshal(integrateAgentWorktreeArgs{
		RepoPath: repository, Branch: created.Branch, CommitHash: commitHash, BaseCommit: created.BaseCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestIntegrateAgentWorktreeCherryPicksAndCleansSource(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	created, commitHash := createCommittedAgentWorktree(t, repository, "integrate", "agent.txt", "agent\n")
	encoded, err := gitIntegrateAgentWorktreeHandler("", integrateAgentTestPayload(t, repository, created, commitHash))
	if err != nil {
		t.Fatal(err)
	}
	var result integrateAgentWorktreeResult
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Cleaned || result.AlreadyIntegrated || result.SourceCommit != commitHash || result.IntegratedHead == created.BaseCommit {
		t.Fatalf("unexpected integration result: %#v", result)
	}
	if content, err := os.ReadFile(filepath.Join(repository, "agent.txt")); err != nil || string(content) != "agent\n" {
		t.Fatalf("integrated file unavailable: content=%q err=%v", content, err)
	}
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Fatalf("agent worktree still exists: %v", err)
	}
	branch, _ := runGit(repository, gitDefaultTimeout, "show-ref", "--verify", "refs/heads/"+created.Branch)
	if branch.exitCode == 0 {
		t.Fatalf("agent branch still exists: %s", created.Branch)
	}
	retried, err := gitIntegrateAgentWorktreeHandler("", integrateAgentTestPayload(t, repository, created, commitHash))
	if err != nil {
		t.Fatalf("retry completed integration: %v", err)
	}
	var retryResult integrateAgentWorktreeResult
	if err := json.Unmarshal([]byte(retried), &retryResult); err != nil {
		t.Fatal(err)
	}
	if !retryResult.AlreadyIntegrated || !retryResult.Cleaned || retryResult.IntegratedHead != result.IntegratedHead {
		t.Fatalf("integration retry was not idempotent: %#v", retryResult)
	}
}

func TestIntegrateAgentWorktreeRecognizesEquivalentPatchAndOnlyCleans(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	created, commitHash := createCommittedAgentWorktree(t, repository, "equivalent", "equivalent.txt", "same patch\n")
	runGitTestCommand(t, repository, "cherry-pick", commitHash)
	before, err := resolveCommit(repository, "HEAD", "target HEAD")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := gitIntegrateAgentWorktreeHandler("", integrateAgentTestPayload(t, repository, created, commitHash))
	if err != nil {
		t.Fatal(err)
	}
	var result integrateAgentWorktreeResult
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Cleaned || !result.AlreadyIntegrated || result.IntegratedHead != before {
		t.Fatalf("unexpected equivalent integration result: %#v", result)
	}
}

func TestIntegrateAgentWorktreeAbortsConflictAndKeepsSource(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	created, commitHash := createCommittedAgentWorktree(t, repository, "conflict", "base.txt", "agent\n")
	defer func() {
		if _, err := os.Stat(created.Path); err == nil {
			runGitTestCommand(t, repository, "worktree", "unlock", created.Path)
			runGitTestCommand(t, repository, "worktree", "remove", "--force", created.Path)
		}
		runGitTestCommand(t, repository, "branch", "-D", created.Branch)
	}()
	writeWorktreeTestFile(t, repository, "base.txt", "target\n")
	runGitTestCommand(t, repository, "add", "base.txt")
	runGitTestCommand(t, repository, "commit", "-m", "conflicting target")
	before, err := resolveCommit(repository, "HEAD", "target HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitIntegrateAgentWorktreeHandler("", integrateAgentTestPayload(t, repository, created, commitHash)); err == nil || !strings.Contains(err.Error(), "已自动回滚") {
		t.Fatalf("expected rolled-back conflict, got %v", err)
	}
	after, err := resolveCommit(repository, "HEAD", "target HEAD")
	if err != nil || after != before {
		t.Fatalf("target HEAD changed after conflict: before=%s after=%s err=%v", before, after, err)
	}
	if err := ensureCleanWorktree(repository, "target"); err != nil {
		t.Fatalf("target worktree not clean after conflict: %v", err)
	}
	if _, err := os.Stat(created.Path); err != nil {
		t.Fatalf("source worktree was not retained: %v", err)
	}
}

func TestIntegrateAgentWorktreeEnforcesDependencyOrder(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	upstream, upstreamHash := createCommittedAgentWorktree(t, repository, "upstream", "upstream.txt", "upstream\n")
	payload, _ := json.Marshal(createAgentWorktreeArgs{
		RepoPath: repository, TaskID: "downstream", BaseCommit: upstreamHash,
	})
	encoded, err := gitCreateAgentWorktreeHandler("", string(payload))
	if err != nil {
		t.Fatal(err)
	}
	var downstream agentWorktreeResult
	if err := json.Unmarshal([]byte(encoded), &downstream); err != nil {
		t.Fatal(err)
	}
	writeWorktreeTestFile(t, downstream.Path, "downstream.txt", "downstream\n")
	runGitTestCommand(t, downstream.Path, "add", "downstream.txt")
	runGitTestCommand(t, downstream.Path, "commit", "-m", "downstream result")
	downstreamHash, err := resolveCommit(downstream.Path, "HEAD", "downstream HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, created := range []agentWorktreeResult{upstream, downstream} {
			if _, err := os.Stat(created.Path); err == nil {
				runGitTestCommand(t, repository, "worktree", "unlock", created.Path)
				runGitTestCommand(t, repository, "worktree", "remove", "--force", created.Path)
			}
			branch, _ := runGit(repository, gitDefaultTimeout, "show-ref", "--verify", "refs/heads/"+created.Branch)
			if branch.exitCode == 0 {
				runGitTestCommand(t, repository, "branch", "-D", created.Branch)
			}
		}
	}()

	before, err := resolveCommit(repository, "HEAD", "target HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitIntegrateAgentWorktreeHandler("", integrateAgentTestPayload(t, repository, downstream, downstreamHash)); err == nil || !strings.Contains(err.Error(), "前置基线尚未集成") {
		t.Fatalf("expected dependency-order rejection, got %v", err)
	}
	afterReject, err := resolveCommit(repository, "HEAD", "target HEAD")
	if err != nil || afterReject != before {
		t.Fatalf("out-of-order integration changed target: before=%s after=%s err=%v", before, afterReject, err)
	}
	if _, err := gitIntegrateAgentWorktreeHandler("", integrateAgentTestPayload(t, repository, upstream, upstreamHash)); err != nil {
		t.Fatalf("integrate upstream: %v", err)
	}
	if _, err := gitIntegrateAgentWorktreeHandler("", integrateAgentTestPayload(t, repository, downstream, downstreamHash)); err != nil {
		t.Fatalf("integrate downstream: %v", err)
	}
	for _, fileName := range []string{"upstream.txt", "downstream.txt"} {
		if _, err := os.Stat(filepath.Join(repository, fileName)); err != nil {
			t.Fatalf("integrated file %s unavailable: %v", fileName, err)
		}
	}
}

func TestIntegrateAgentWorktreeRejectsUnsafeIdentifiersBeforeMutation(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	created, commitHash := createCommittedAgentWorktree(t, repository, "unsafe", "safe.txt", "safe\n")
	defer func() {
		if _, err := os.Stat(created.Path); err == nil {
			runGitTestCommand(t, repository, "worktree", "unlock", created.Path)
			runGitTestCommand(t, repository, "worktree", "remove", "--force", created.Path)
		}
		runGitTestCommand(t, repository, "branch", "-D", created.Branch)
	}()
	before, err := resolveCommit(repository, "HEAD", "target HEAD")
	if err != nil {
		t.Fatal(err)
	}
	cases := []integrateAgentWorktreeArgs{
		{RepoPath: repository, Branch: created.Branch + "~1", CommitHash: commitHash, BaseCommit: created.BaseCommit},
		{RepoPath: repository, Branch: created.Branch, CommitHash: "HEAD", BaseCommit: created.BaseCommit},
		{RepoPath: repository, Branch: created.Branch, CommitHash: commitHash, BaseCommit: created.BaseCommit + "^"},
	}
	for _, testCase := range cases {
		payload, _ := json.Marshal(testCase)
		if _, err := gitIntegrateAgentWorktreeHandler("", string(payload)); err == nil {
			t.Fatalf("unsafe integration input was accepted: %#v", testCase)
		}
		after, headErr := resolveCommit(repository, "HEAD", "target HEAD")
		if headErr != nil || after != before {
			t.Fatalf("unsafe input changed target: before=%s after=%s err=%v", before, after, headErr)
		}
		if cleanErr := ensureCleanWorktree(repository, "target"); cleanErr != nil {
			t.Fatalf("unsafe input dirtied target: %v", cleanErr)
		}
	}
}

func TestIntegrateAgentWorktreeRejectsExistingGitOperation(t *testing.T) {
	repository := createWorktreeTestRepository(t)
	created, commitHash := createCommittedAgentWorktree(t, repository, "operation", "operation.txt", "operation\n")
	defer func() {
		if _, err := os.Stat(created.Path); err == nil {
			runGitTestCommand(t, repository, "worktree", "unlock", created.Path)
			runGitTestCommand(t, repository, "worktree", "remove", "--force", created.Path)
		}
		runGitTestCommand(t, repository, "branch", "-D", created.Branch)
	}()
	markerResult, err := runGit(repository, gitDefaultTimeout, "rev-parse", "--git-path", "CHERRY_PICK_HEAD")
	if err != nil || gitEnsureSuccess(markerResult, "git rev-parse --git-path") != nil {
		t.Fatalf("resolve operation marker: %v", err)
	}
	marker := strings.TrimSpace(string(markerResult.stdout))
	if !filepath.IsAbs(marker) {
		marker = filepath.Join(repository, marker)
	}
	if err := os.WriteFile(marker, []byte(commitHash+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(marker)
	before, err := resolveCommit(repository, "HEAD", "target HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitIntegrateAgentWorktreeHandler("", integrateAgentTestPayload(t, repository, created, commitHash)); err == nil || !strings.Contains(err.Error(), "未完成的 Git 操作") {
		t.Fatalf("expected existing-operation rejection, got %v", err)
	}
	after, err := resolveCommit(repository, "HEAD", "target HEAD")
	if err != nil || after != before {
		t.Fatalf("existing operation changed target: before=%s after=%s err=%v", before, after, err)
	}
}
