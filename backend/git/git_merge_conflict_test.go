package git

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeBranchLeavesChangesForManualCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	t.Run("fast-forward candidate keeps HEAD", func(t *testing.T) {
		repository := createMergeRepository(t, false)
		before := gitTestOutput(t, repository, "rev-parse", "HEAD")
		mergeBranchForTest(t, repository, "feature")
		assertMergeWaitingForManualCommit(t, repository, before, "feature.txt")
		if _, err := gitCommitHandler("", marshalGitTestArgs(t, commitArgs{
			RepoPath: repository, Message: "manual merge",
		})); err != nil {
			t.Fatalf("manual merge commit: %v", err)
		}
		if got := gitTestOutput(t, repository, "rev-list", "--parents", "-n", "1", "HEAD"); len(strings.Fields(got)) != 3 {
			t.Fatalf("manual merge commit parents = %q", got)
		}
	})

	t.Run("diverged branches keep HEAD", func(t *testing.T) {
		repository := createMergeRepository(t, true)
		before := gitTestOutput(t, repository, "rev-parse", "HEAD")
		mergeBranchForTest(t, repository, "feature")
		assertMergeWaitingForManualCommit(t, repository, before, "feature.txt")
		if got, err := os.ReadFile(filepath.Join(repository, "main.txt")); err != nil || string(got) != "main\n" {
			t.Fatalf("current branch content changed: %q, %v", got, err)
		}
	})
}

func TestMergeConflictLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	t.Run("resolve and leave staged for manual commit", func(t *testing.T) {
		repository := createMergeConflictRepository(t)
		before := gitTestOutput(t, repository, "rev-parse", "HEAD")
		assertMergeConflictStatus(t, repository)

		state := readMergeStateForTest(t, repository)
		if !state.Active || len(state.Conflicts) != 1 || state.Conflicts[0] != "conflict.txt" {
			t.Fatalf("unexpected merge state: %#v", state)
		}

		content := readConflictContentForTest(t, repository, "conflict.txt")
		if content.Base != "header\nbase\nfooter\n" {
			t.Fatalf("base = %q", content.Base)
		}
		if content.Ours != "header\nours\nfooter\n" {
			t.Fatalf("ours = %q", content.Ours)
		}
		if content.Theirs != "header\ntheirs\nfooter\n" {
			t.Fatalf("theirs = %q", content.Theirs)
		}
		if !strings.Contains(content.Raw, "<<<<<<<") || !strings.Contains(content.Raw, ">>>>>>>") {
			t.Fatalf("raw conflict markers missing: %q", content.Raw)
		}

		resolved := "header\nours + theirs\nfooter\n"
		if _, err := gitResolveConflictHandler("", marshalGitTestArgs(t, resolveConflictArgs{
			RepoPath: repository, RelativePath: "conflict.txt", Content: resolved,
		})); err != nil {
			t.Fatalf("resolve conflict: %v", err)
		}
		state = readMergeStateForTest(t, repository)
		if !state.Active || len(state.Conflicts) != 0 {
			t.Fatalf("state after resolve must wait for manual commit: %#v", state)
		}
		if after := gitTestOutput(t, repository, "rev-parse", "HEAD"); after != before {
			t.Fatalf("HEAD changed before manual commit: %s -> %s", before, after)
		}
		if staged := gitTestOutput(t, repository, "diff", "--cached", "--name-only"); staged != "conflict.txt" {
			t.Fatalf("staged files = %q", staged)
		}
		if got, err := os.ReadFile(filepath.Join(repository, "conflict.txt")); err != nil || string(got) != resolved {
			t.Fatalf("resolved file = %q, %v", got, err)
		}
	})

	t.Run("abort", func(t *testing.T) {
		repository := createMergeConflictRepository(t)
		before := gitTestOutput(t, repository, "rev-parse", "HEAD")
		if _, err := gitAbortMergeHandler("", marshalGitTestArgs(t, repoOnlyArgs{RepoPath: repository})); err != nil {
			t.Fatalf("abort merge: %v", err)
		}
		if state := readMergeStateForTest(t, repository); state.Active || len(state.Conflicts) != 0 {
			t.Fatalf("state after abort: %#v", state)
		}
		if after := gitTestOutput(t, repository, "rev-parse", "HEAD"); after != before {
			t.Fatalf("HEAD changed on abort: %s -> %s", before, after)
		}
		if got, err := os.ReadFile(filepath.Join(repository, "conflict.txt")); err != nil || string(got) != "header\nours\nfooter\n" {
			t.Fatalf("file after abort = %q, %v", got, err)
		}
	})
}

func TestMergeConflictGuardsAndMissingStages(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	repository := createMergeConflictRepository(t)
	nonConflictPath := filepath.Join(repository, "not-conflicted.txt")
	writeGitTestFile(t, nonConflictPath, "keep\n")
	if _, err := gitResolveConflictHandler("", marshalGitTestArgs(t, resolveConflictArgs{
		RepoPath: repository, RelativePath: "not-conflicted.txt", Content: "unsafe\n",
	})); err == nil || !strings.Contains(err.Error(), "不再处于冲突状态") {
		t.Fatalf("resolve non-conflict error = %v", err)
	}
	if got, err := os.ReadFile(nonConflictPath); err != nil || string(got) != "keep\n" {
		t.Fatalf("non-conflict file was changed: %q, %v", got, err)
	}

	addAdd := t.TempDir()
	runGitTestCommand(t, addAdd, "init", "-b", "main")
	configureGitTestIdentity(t, addAdd)
	writeGitTestFile(t, filepath.Join(addAdd, "base.txt"), "base\n")
	runGitTestCommand(t, addAdd, "add", "base.txt")
	runGitTestCommand(t, addAdd, "commit", "-m", "base")
	runGitTestCommand(t, addAdd, "checkout", "-b", "feature")
	writeGitTestFile(t, filepath.Join(addAdd, "added.txt"), "incoming\n")
	runGitTestCommand(t, addAdd, "add", "added.txt")
	runGitTestCommand(t, addAdd, "commit", "-m", "incoming add")
	runGitTestCommand(t, addAdd, "checkout", "main")
	writeGitTestFile(t, filepath.Join(addAdd, "added.txt"), "current\n")
	runGitTestCommand(t, addAdd, "add", "added.txt")
	runGitTestCommand(t, addAdd, "commit", "-m", "current add")
	mergeBranchExpectConflictForTest(t, addAdd, "feature")
	content := readConflictContentForTest(t, addAdd, "added.txt")
	if content.Base != "" || content.Ours != "current\n" || content.Theirs != "incoming\n" {
		t.Fatalf("add/add stages = %#v", content)
	}
}

func TestMergeConflictPathsKeepNonASCIINames(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	const name = "中文.txt"
	repository := t.TempDir()
	runGitTestCommand(t, repository, "init", "-b", "main")
	configureGitTestIdentity(t, repository)
	// 显式开启 quotepath，确保测试不受本机全局 git 配置影响。
	runGitTestCommand(t, repository, "config", "core.quotepath", "true")
	path := filepath.Join(repository, name)
	writeGitTestFile(t, path, "header\nbase\nfooter\n")
	runGitTestCommand(t, repository, "add", name)
	runGitTestCommand(t, repository, "commit", "-m", "base")
	runGitTestCommand(t, repository, "checkout", "-b", "feature")
	writeGitTestFile(t, path, "header\ntheirs\nfooter\n")
	runGitTestCommand(t, repository, "commit", "-am", "feature")
	runGitTestCommand(t, repository, "checkout", "main")
	writeGitTestFile(t, path, "header\nours\nfooter\n")
	runGitTestCommand(t, repository, "commit", "-am", "main")
	mergeBranchExpectConflictForTest(t, repository, "feature")

	paths, err := gitConflictPaths(repository)
	if err != nil {
		t.Fatalf("conflict paths: %v", err)
	}
	if len(paths) != 1 || paths[0] != name {
		t.Fatalf("conflict paths = %q, want [%q]", paths, name)
	}

	content := readConflictContentForTest(t, repository, name)
	if content.Ours != "header\nours\nfooter\n" || content.Theirs != "header\ntheirs\nfooter\n" {
		t.Fatalf("conflict stages = %#v", content)
	}
	if _, err := gitResolveConflictHandler("", marshalGitTestArgs(t, resolveConflictArgs{
		RepoPath: repository, RelativePath: name, Content: "header\nresolved\nfooter\n",
	})); err != nil {
		t.Fatalf("resolve non-ASCII conflict: %v", err)
	}
	if state := readMergeStateForTest(t, repository); !state.Active || len(state.Conflicts) != 0 {
		t.Fatalf("state after resolve: %#v", state)
	}
}

func createMergeRepository(t *testing.T, diverged bool) string {
	t.Helper()
	repository := t.TempDir()
	runGitTestCommand(t, repository, "init", "-b", "main")
	configureGitTestIdentity(t, repository)
	writeGitTestFile(t, filepath.Join(repository, "base.txt"), "base\n")
	runGitTestCommand(t, repository, "add", "base.txt")
	runGitTestCommand(t, repository, "commit", "-m", "base")
	runGitTestCommand(t, repository, "checkout", "-b", "feature")
	writeGitTestFile(t, filepath.Join(repository, "feature.txt"), "feature\n")
	runGitTestCommand(t, repository, "add", "feature.txt")
	runGitTestCommand(t, repository, "commit", "-m", "feature")
	runGitTestCommand(t, repository, "checkout", "main")
	if diverged {
		writeGitTestFile(t, filepath.Join(repository, "main.txt"), "main\n")
		runGitTestCommand(t, repository, "add", "main.txt")
		runGitTestCommand(t, repository, "commit", "-m", "main")
	}
	return repository
}

func createMergeConflictRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	runGitTestCommand(t, repository, "init", "-b", "main")
	configureGitTestIdentity(t, repository)
	path := filepath.Join(repository, "conflict.txt")
	writeGitTestFile(t, path, "header\nbase\nfooter\n")
	runGitTestCommand(t, repository, "add", "conflict.txt")
	runGitTestCommand(t, repository, "commit", "-m", "base")
	runGitTestCommand(t, repository, "checkout", "-b", "feature")
	writeGitTestFile(t, path, "header\ntheirs\nfooter\n")
	runGitTestCommand(t, repository, "commit", "-am", "feature")
	runGitTestCommand(t, repository, "checkout", "main")
	writeGitTestFile(t, path, "header\nours\nfooter\n")
	runGitTestCommand(t, repository, "commit", "-am", "main")
	mergeBranchExpectConflictForTest(t, repository, "feature")
	return repository
}

func mergeBranchForTest(t *testing.T, repository, branch string) gitSyncResult {
	t.Helper()
	resultJSON, err := gitMergeBranchHandler("", marshalGitTestArgs(t, mergeArgs{
		RepoPath: repository, BranchName: branch,
	}))
	if err != nil {
		t.Fatalf("merge branch: %v", err)
	}
	var result gitSyncResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		t.Fatalf("decode merge result: %v", err)
	}
	if !result.Ok || !strings.Contains(result.Message, "手动提交") {
		t.Fatalf("unexpected merge result: %#v", result)
	}
	return result
}

func mergeBranchExpectConflictForTest(t *testing.T, repository, branch string) {
	t.Helper()
	if _, err := gitMergeBranchHandler("", marshalGitTestArgs(t, mergeArgs{
		RepoPath: repository, BranchName: branch,
	})); err == nil || !strings.Contains(err.Error(), "不会自动提交或推送") {
		t.Fatalf("merge conflict error = %v", err)
	}
}

func assertMergeWaitingForManualCommit(t *testing.T, repository, before, stagedPath string) {
	t.Helper()
	if after := gitTestOutput(t, repository, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD changed before manual commit: %s -> %s", before, after)
	}
	state := readMergeStateForTest(t, repository)
	if !state.Active || len(state.Conflicts) != 0 {
		t.Fatalf("merge state = %#v", state)
	}
	if staged := gitTestOutput(t, repository, "diff", "--cached", "--name-only"); staged != stagedPath {
		t.Fatalf("staged files = %q, want %q", staged, stagedPath)
	}
}

func assertMergeConflictStatus(t *testing.T, repository string) {
	t.Helper()
	status, err := gitGetStatus(repository)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	for _, entry := range status.Unstaged {
		if entry.Path == "conflict.txt" && entry.Status == "conflicted" {
			return
		}
	}
	t.Fatalf("conflict absent from status: %#v", status)
}

func readMergeStateForTest(t *testing.T, repository string) mergeState {
	t.Helper()
	result, err := gitGetMergeStateHandler("", marshalGitTestArgs(t, repoOnlyArgs{RepoPath: repository}))
	if err != nil {
		t.Fatalf("get merge state: %v", err)
	}
	var state mergeState
	if err := json.Unmarshal([]byte(result), &state); err != nil {
		t.Fatalf("decode merge state: %v", err)
	}
	return state
}

func readConflictContentForTest(t *testing.T, repository, path string) conflictContent {
	t.Helper()
	result, err := gitGetConflictContentHandler("", marshalGitTestArgs(t, repoRelArgs{
		RepoPath: repository, RelativePath: path,
	}))
	if err != nil {
		t.Fatalf("get conflict content: %v", err)
	}
	var content conflictContent
	if err := json.Unmarshal([]byte(result), &content); err != nil {
		t.Fatalf("decode conflict content: %v", err)
	}
	return content
}
