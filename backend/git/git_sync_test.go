package git

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitPullFastForwardsAndReportsUpToDate(t *testing.T) {
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
	writeGitTestFile(t, filepath.Join(seed, "version.txt"), "one\n")
	runGitTestCommand(t, seed, "add", "version.txt")
	runGitTestCommand(t, seed, "commit", "-m", "seed")
	runGitTestCommand(t, seed, "remote", "add", "origin", remote)
	runGitTestCommand(t, seed, "push", "-u", "origin", "main")
	runGitTestCommand(t, root, "clone", "--branch", "main", remote, consumer)
	configureGitTestIdentity(t, consumer)

	writeGitTestFile(t, filepath.Join(seed, "version.txt"), "two\n")
	runGitTestCommand(t, seed, "add", "version.txt")
	runGitTestCommand(t, seed, "commit", "-m", "advance remote")
	runGitTestCommand(t, seed, "push")

	first := pullGitTestRepository(t, consumer)
	if !first.Ok || first.Message == "" {
		t.Fatalf("unexpected pull result: %#v", first)
	}
	content, err := os.ReadFile(filepath.Join(consumer, "version.txt"))
	if err != nil {
		t.Fatalf("read pulled file: %v", err)
	}
	if string(content) != "two\n" {
		t.Fatalf("pulled file = %q, want %q", content, "two\\n")
	}
	seedHead := gitTestOutput(t, seed, "rev-parse", "HEAD")
	consumerHead := gitTestOutput(t, consumer, "rev-parse", "HEAD")
	if consumerHead != seedHead {
		t.Fatalf("consumer HEAD = %q, want %q", consumerHead, seedHead)
	}

	second := pullGitTestRepository(t, consumer)
	if !second.Ok || second.Message != "已是最新" {
		t.Fatalf("up-to-date pull result: %#v", second)
	}
}

func pullGitTestRepository(t *testing.T, repository string) gitSyncResult {
	t.Helper()
	resultJSON, err := gitPullHandler("", marshalGitTestArgs(t, pushPullArgs{
		RepoPath: repository,
	}))
	if err != nil {
		t.Fatalf("pull repository: %v", err)
	}
	var result gitSyncResult
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		t.Fatalf("decode pull result: %v", err)
	}
	return result
}

func configureGitTestIdentity(t *testing.T, repository string) {
	t.Helper()
	runGitTestCommand(t, repository, "config", "user.name", "MinCode Test")
	runGitTestCommand(t, repository, "config", "user.email", "mincode-test@example.invalid")
}

func writeGitTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func gitTestOutput(t *testing.T, repository string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repository}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
