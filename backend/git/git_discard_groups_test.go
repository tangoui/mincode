package git

import (
	"os"
	"path/filepath"
	"testing"
)

func createDiscardGroupTestRepository(t *testing.T) string {
	t.Helper()
	repositoryRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repositoryRoot, 0o755); err != nil {
		t.Fatalf("create repository directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runGitTestCommand(t, repositoryRoot, "init")
	runGitTestCommand(t, repositoryRoot, "config", "user.name", "MinCode Test")
	runGitTestCommand(t, repositoryRoot, "config", "user.email", "mincode-test@example.invalid")
	runGitTestCommand(t, repositoryRoot, "add", ".")
	runGitTestCommand(t, repositoryRoot, "commit", "-m", "base commit")
	return repositoryRoot
}

func writeWorkingTreeChanges(t *testing.T, repositoryRoot string) (tracked, untracked string) {
	t.Helper()
	tracked = filepath.Join(repositoryRoot, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("modified\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	untracked = filepath.Join(repositoryRoot, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	return tracked, untracked
}

func TestGitDiscardUnstagedKeepsUntrackedFiles(t *testing.T) {
	repositoryRoot := createDiscardGroupTestRepository(t)
	tracked, untracked := writeWorkingTreeChanges(t, repositoryRoot)

	if _, err := gitDiscardUnstagedHandler("", marshalGitTestArgs(t, repoOnlyArgs{
		RepoPath: repositoryRoot,
	})); err != nil {
		t.Fatalf("discard unstaged changes: %v", err)
	}

	content, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatalf("read tracked file: %v", err)
	}
	if string(content) != "base\n" {
		t.Fatalf("tracked file = %q, want unstaged change reverted", content)
	}
	if _, err := os.Stat(untracked); err != nil {
		t.Fatalf("untracked file must survive an unstaged discard: %v", err)
	}
}

func TestGitCleanUntrackedKeepsUnstagedChanges(t *testing.T) {
	repositoryRoot := createDiscardGroupTestRepository(t)
	tracked, untracked := writeWorkingTreeChanges(t, repositoryRoot)

	if _, err := gitCleanUntrackedHandler("", marshalGitTestArgs(t, repoOnlyArgs{
		RepoPath: repositoryRoot,
	})); err != nil {
		t.Fatalf("clean untracked files: %v", err)
	}

	if _, err := os.Stat(untracked); !os.IsNotExist(err) {
		t.Fatalf("untracked file must be deleted, stat err = %v", err)
	}
	content, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatalf("read tracked file: %v", err)
	}
	if string(content) != "modified\n" {
		t.Fatalf("tracked file = %q, want unstaged change preserved", content)
	}
}
