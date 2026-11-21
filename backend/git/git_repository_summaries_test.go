package git

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitRepositorySummariesReportsBranchAndChangeCounts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	root := t.TempDir()
	clean := createSummaryTestRepository(t, root, "clean", "main")
	dirty := createSummaryTestRepository(t, root, "dirty", "release")
	writeGitTestFile(t, filepath.Join(dirty, "seed.txt"), "changed\n")
	writeGitTestFile(t, filepath.Join(dirty, "staged.txt"), "staged\n")
	runGitTestCommand(t, dirty, "add", "staged.txt")
	writeGitTestFile(t, filepath.Join(dirty, "untracked.txt"), "new\n")

	notARepository := filepath.Join(root, "plain")
	if err := os.MkdirAll(notARepository, 0o755); err != nil {
		t.Fatalf("create plain directory: %v", err)
	}

	summaries := decodeRepositorySummaries(t, []string{clean, notARepository, "", dirty})
	if len(summaries) != 2 {
		t.Fatalf("summaries = %#v, want 2 entries", summaries)
	}

	// 入参顺序必须保留：无效路径被跳过，不能让剩下的条目错位。
	if summaries[0].Path != clean || summaries[1].Path != dirty {
		t.Fatalf("summary order = %q/%q, want %q/%q",
			summaries[0].Path, summaries[1].Path, clean, dirty)
	}
	if summaries[0].Branch != "main" {
		t.Fatalf("clean branch = %q, want main", summaries[0].Branch)
	}
	if summaries[0].Staged+summaries[0].Unstaged+summaries[0].Untracked != 0 {
		t.Fatalf("clean repository reported changes: %#v", summaries[0])
	}
	if summaries[1].Branch != "release" {
		t.Fatalf("dirty branch = %q, want release", summaries[1].Branch)
	}
	if summaries[1].Staged != 1 || summaries[1].Unstaged != 1 || summaries[1].Untracked != 1 {
		t.Fatalf("dirty counts = %#v, want 1/1/1", summaries[1])
	}
}

func TestGitRepositorySummariesHandlesEmptyInput(t *testing.T) {
	if summaries := gitRepositorySummaries(nil); len(summaries) != 0 {
		t.Fatalf("summaries = %#v, want empty", summaries)
	}
}

func createSummaryTestRepository(t *testing.T, root, name, branch string) string {
	t.Helper()
	repository := filepath.Join(root, name)
	runGitTestCommand(t, root, "init", "-b", branch, repository)
	configureGitTestIdentity(t, repository)
	writeGitTestFile(t, filepath.Join(repository, "seed.txt"), "seed\n")
	runGitTestCommand(t, repository, "add", "seed.txt")
	runGitTestCommand(t, repository, "commit", "-m", "seed")
	return repository
}

func decodeRepositorySummaries(t *testing.T, repoPaths []string) []gitRepositorySummary {
	t.Helper()
	resultJSON, err := gitGetRepositorySummariesHandler("", marshalGitTestArgs(t, repoPathsArgs{
		RepoPaths: repoPaths,
	}))
	if err != nil {
		t.Fatalf("get repository summaries: %v", err)
	}
	var summaries []gitRepositorySummary
	if err := json.Unmarshal([]byte(resultJSON), &summaries); err != nil {
		t.Fatalf("decode repository summaries: %v", err)
	}
	return summaries
}
