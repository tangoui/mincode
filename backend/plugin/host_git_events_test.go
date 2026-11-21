package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadGitHeadBranchDetachedAndWorktree(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFile(t, root, ".git/HEAD", "ref: refs/heads/feature/login\n")
	if got, ok := readGitHead(root); !ok || got.Branch != "feature/login" || got.Detached {
		t.Fatalf("branch %+v %v", got, ok)
	}
	writeWorkspaceFile(t, root, ".git/HEAD", "0123456789abcdef0123456789abcdef01234567\n")
	if got, ok := readGitHead(root); !ok || !got.Detached || got.Commit != "0123456789ab" {
		t.Fatalf("detached %+v %v", got, ok)
	}
	worktree := t.TempDir()
	linked := filepath.Join(root, ".git", "worktrees", "wt")
	writeWorkspaceFile(t, linked, "HEAD", "ref: refs/heads/hotfix\n")
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+linked+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := readGitHead(worktree); !ok || got.Branch != "hotfix" {
		t.Fatalf("worktree %+v %v", got, ok)
	}
	if _, ok := readGitHead(t.TempDir()); ok {
		t.Fatal("non-repository reported a HEAD")
	}
	writeWorkspaceFile(t, root, ".git/HEAD", "ref: refs/tags/v1\n")
	if _, ok := readGitHead(root); ok {
		t.Fatal("non-branch ref accepted")
	}
}

func TestEventSelectionValidation(t *testing.T) {
	if !(&eventSelection{1, 1, 3, 4}).valid() || (&eventSelection{0, 1, 1, 1}).valid() || (*eventSelection)(nil).valid() {
		t.Fatal("selection validation")
	}
	if _, known := hostEvents[hostEventGitHeadChanged]; !known || !frontendHostEvents[hostEventSelectionChanged] || frontendHostEvents[hostEventGitHeadChanged] {
		t.Fatal("event registry")
	}
}
