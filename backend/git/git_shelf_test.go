package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitShelfCreateApplyAndDelete(t *testing.T) {
	repositoryRoot := createShelfTestRepository(t)
	trackedPath := filepath.Join(repositoryRoot, "tracked.txt")
	untrackedPath := filepath.Join(repositoryRoot, "untracked.txt")
	if err := os.WriteFile(trackedPath, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untrackedPath, []byte("new file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	createdJSON, err := gitShelfCreateHandler("", `{"repoPath":`+quoteJSON(repositoryRoot)+`,"message":"feature draft"}`)
	if err != nil {
		t.Fatalf("create shelf: %v", err)
	}
	var created gitShelfEntry
	if err := json.Unmarshal([]byte(createdJSON), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Message != "feature draft" {
		t.Fatalf("unexpected shelf: %+v", created)
	}
	assertShelfStatusClean(t, repositoryRoot)

	listJSON, err := gitShelfListHandler("", `{"repoPath":`+quoteJSON(repositoryRoot)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	var shelves []gitShelfEntry
	if err := json.Unmarshal([]byte(listJSON), &shelves); err != nil {
		t.Fatal(err)
	}
	if len(shelves) != 1 || shelves[0].ID != created.ID {
		t.Fatalf("unexpected shelf list: %+v", shelves)
	}

	if _, err := gitShelfApplyHandler("", `{"repoPath":`+quoteJSON(repositoryRoot)+`,"shelfId":`+quoteJSON(created.ID)+`}`); err != nil {
		t.Fatalf("apply shelf: %v", err)
	}
	if data, err := os.ReadFile(trackedPath); err != nil || string(data) != "changed\n" {
		t.Fatalf("tracked content not restored: %q, %v", data, err)
	}
	if data, err := os.ReadFile(untrackedPath); err != nil || string(data) != "new file\n" {
		t.Fatalf("untracked content not restored: %q, %v", data, err)
	}

	if _, err := gitShelfDeleteHandler("", `{"repoPath":`+quoteJSON(repositoryRoot)+`,"shelfId":`+quoteJSON(created.ID)+`}`); err != nil {
		t.Fatalf("delete shelf: %v", err)
	}
	listJSON, err = gitShelfListHandler("", `{"repoPath":`+quoteJSON(repositoryRoot)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(listJSON), &shelves); err != nil {
		t.Fatal(err)
	}
	if len(shelves) != 0 {
		t.Fatalf("expected empty shelf list, got %+v", shelves)
	}
}

func TestGitShelfRejectsEmptyWorkingTree(t *testing.T) {
	repositoryRoot := createShelfTestRepository(t)
	_, err := gitShelfCreateHandler("", `{"repoPath":`+quoteJSON(repositoryRoot)+`,"message":"empty"}`)
	if err == nil || !strings.Contains(err.Error(), "没有可搁置") {
		t.Fatalf("expected empty working tree error, got %v", err)
	}
}

// stash push 未保存任何内容（仅子仓库内容变更，退出码仍为 0）时，不能把用户已有的 stash@{0} 当作搁置并删除。
func TestGitShelfCreateKeepsUnrelatedStashWhenPushSavesNothing(t *testing.T) {
	repositoryRoot := createShelfTestRepository(t)
	nestedRoot := filepath.Join(repositoryRoot, "nested")
	runGitTestCommand(t, repositoryRoot, "init", "nested")
	runGitTestCommand(t, nestedRoot, "config", "user.email", "test@example.com")
	runGitTestCommand(t, nestedRoot, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(nestedRoot, "inner.txt"), []byte("inner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, nestedRoot, "add", "inner.txt")
	runGitTestCommand(t, nestedRoot, "commit", "-m", "inner")
	runGitTestCommand(t, repositoryRoot, "add", "nested")
	runGitTestCommand(t, repositoryRoot, "commit", "-m", "add nested")

	if err := os.WriteFile(filepath.Join(repositoryRoot, "tracked.txt"), []byte("user work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repositoryRoot, "stash", "push", "-m", "user stash")
	userStash, err := gitStashTip(repositoryRoot)
	if err != nil || userStash == "" {
		t.Fatalf("user stash tip = %q, %v", userStash, err)
	}
	if err := os.WriteFile(filepath.Join(nestedRoot, "inner.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = gitShelfCreateHandler("", `{"repoPath":`+quoteJSON(repositoryRoot)+`,"message":"nested only"}`)
	if err == nil || !strings.Contains(err.Error(), "未创建新的 Stash") {
		t.Fatalf("expected no-new-stash error, got %v", err)
	}
	stashes, err := gitListStashes(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(stashes) != 1 || stashes[0].sha != userStash || !strings.HasSuffix(stashes[0].subject, "user stash") {
		t.Fatalf("user stash was modified: %+v", stashes)
	}
	listJSON, err := gitShelfListHandler("", `{"repoPath":`+quoteJSON(repositoryRoot)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if listJSON != "[]" {
		t.Fatalf("unexpected shelf list: %s", listJSON)
	}
}

// 其他进程在本次 push 之后又 push 时，按临时消息定位本次 Stash，而不是 stash@{0}。
func TestGitCreatedShelfStashIgnoresConcurrentStash(t *testing.T) {
	repositoryRoot := createShelfTestRepository(t)
	before, err := gitStashTip(repositoryRoot)
	if err != nil || before != "" {
		t.Fatalf("empty stash tip = %q, %v", before, err)
	}
	trackedPath := filepath.Join(repositoryRoot, "tracked.txt")
	if err := os.WriteFile(trackedPath, []byte("shelf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repositoryRoot, "stash", "push", "-m", "mincode-shelf:42")
	ours, err := gitStashTip(repositoryRoot)
	if err != nil || ours == "" {
		t.Fatalf("shelf stash tip = %q, %v", ours, err)
	}
	if err := os.WriteFile(trackedPath, []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repositoryRoot, "stash", "push", "-m", "concurrent")

	got, err := gitCreatedShelfStash(repositoryRoot, before, "mincode-shelf:42")
	if err != nil || got != ours {
		t.Fatalf("created stash = %q, %v; want %q", got, err, ours)
	}
	if _, err := gitCreatedShelfStash(repositoryRoot, before, "mincode-shelf:missing"); err == nil {
		t.Fatal("expected error for missing shelf stash")
	}
}

func createShelfTestRepository(t *testing.T) string {
	t.Helper()
	repositoryRoot := t.TempDir()
	runGitTestCommand(t, repositoryRoot, "init")
	runGitTestCommand(t, repositoryRoot, "config", "user.email", "test@example.com")
	runGitTestCommand(t, repositoryRoot, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repositoryRoot, "add", "tracked.txt")
	runGitTestCommand(t, repositoryRoot, "commit", "-m", "initial")
	return repositoryRoot
}

func assertShelfStatusClean(t *testing.T, repositoryRoot string) {
	t.Helper()
	output, err := runGit(repositoryRoot, gitDefaultTimeout, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		t.Fatal(err)
	}
	if err := gitEnsureSuccess(output, "git status"); err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(output.stdout))) != 0 {
		t.Fatalf("working tree is not clean: %s", output.stdout)
	}
}

func quoteJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
