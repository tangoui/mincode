package git

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitCheckoutRemoteBranchCreatesLocalTrackingBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	consumer := filepath.Join(root, "consumer")
	runGitTestCommand(t, root, "init", "--bare", remote)
	runGitTestCommand(t, root, "init", "-b", "main", seed)
	configureGitTestIdentity(t, seed)
	writeGitTestFile(t, filepath.Join(seed, "README.md"), "main\n")
	runGitTestCommand(t, seed, "add", "README.md")
	runGitTestCommand(t, seed, "commit", "-m", "seed")
	runGitTestCommand(t, seed, "checkout", "-b", "feature/remote-only")
	writeGitTestFile(t, filepath.Join(seed, "feature.txt"), "remote\n")
	runGitTestCommand(t, seed, "add", "feature.txt")
	runGitTestCommand(t, seed, "commit", "-m", "remote feature")
	runGitTestCommand(t, seed, "remote", "add", "origin", remote)
	runGitTestCommand(t, seed, "push", "origin", "main", "feature/remote-only")
	runGitTestCommand(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	runGitTestCommand(t, root, "clone", remote, consumer)

	payload, err := json.Marshal(map[string]any{
		"repoPath":     consumer,
		"branchName":   "origin/feature/remote-only",
		"create":       false,
		"remoteBranch": true,
	})
	if err != nil {
		t.Fatalf("marshal checkout arguments: %v", err)
	}
	if _, err := gitCheckoutBranchHandler("", string(payload)); err != nil {
		t.Fatalf("checkout remote branch: %v", err)
	}

	if got := gitTestOutput(t, consumer, "branch", "--show-current"); got != "feature/remote-only" {
		t.Fatalf("current branch = %q, want feature/remote-only", got)
	}
	if got := gitTestOutput(t, consumer, "rev-parse", "--abbrev-ref", "@{upstream}"); got != "origin/feature/remote-only" {
		t.Fatalf("upstream = %q, want origin/feature/remote-only", got)
	}
}
