package git

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitEditorActionsResolveNestedProjectDirectory(t *testing.T) {
	repositoryRoot, nestedProjectDir, nestedFile := createNestedGitRepository(t)

	resolvedRoot, err := resolveGitRepositoryRoot(nestedProjectDir)
	if err != nil {
		t.Fatalf("resolve nested repository root: %v", err)
	}
	if want := resolveRepoDir(repositoryRoot); resolvedRoot != want {
		t.Fatalf("repository root = %q, want %q", resolvedRoot, want)
	}

	gotRelativePath, err := normalizeGitRelativePathInRepository(
		nestedProjectDir,
		resolvedRoot,
		"00-产品设计思路-总览.md",
	)
	if err != nil {
		t.Fatalf("normalize nested relative path: %v", err)
	}
	if want := "01 设计思路/00-产品设计思路-总览.md"; gotRelativePath != want {
		t.Fatalf("normalized relative path = %q, want %q", gotRelativePath, want)
	}

	gotAbsolutePath, err := normalizeGitRelativePathInRepository(
		nestedProjectDir,
		resolvedRoot,
		nestedFile,
	)
	if err != nil {
		t.Fatalf("normalize absolute document path: %v", err)
	}
	if gotAbsolutePath != gotRelativePath {
		t.Fatalf("absolute path = %q, want %q", gotAbsolutePath, gotRelativePath)
	}

	if _, err := normalizeGitRelativePathInRepository(
		nestedProjectDir,
		resolvedRoot,
		"../../../outside.ts",
	); err == nil {
		t.Fatal("expected a path outside the repository to be rejected")
	}

	fileResult, err := gitReadFileAtRef(nestedProjectDir, nestedFile, "HEAD")
	if err != nil {
		t.Fatalf("read file at HEAD from nested project directory: %v", err)
	}
	var fileContent string
	if err := json.Unmarshal([]byte(fileResult), &fileContent); err != nil {
		t.Fatalf("decode file content: %v", err)
	}
	if fileContent != "export const scope = 'nested';\n" {
		t.Fatalf("file content = %q", fileContent)
	}

	branchesResult, err := gitListBranchesHandler("", marshalGitTestArgs(t, repoOnlyArgs{
		RepoPath: nestedProjectDir,
	}))
	if err != nil {
		t.Fatalf("list branches from nested project directory: %v", err)
	}
	var branches gitBranches
	if err := json.Unmarshal([]byte(branchesResult), &branches); err != nil {
		t.Fatalf("decode branches: %v", err)
	}
	if branches.Current == "" || !hasLocalBranch(branches.Branches, branches.Current) {
		t.Fatalf("current branch %q was not returned as a local branch: %#v", branches.Current, branches.Branches)
	}

	blameResult, err := gitBlameHandler("", marshalGitTestArgs(t, repoRelArgs{
		RepoPath:     nestedProjectDir,
		RelativePath: nestedFile,
	}))
	if err != nil {
		t.Fatalf("blame nested file from nested project directory: %v", err)
	}
	var hunks []blameHunk
	if err := json.Unmarshal([]byte(blameResult), &hunks); err != nil {
		t.Fatalf("decode blame response: %v", err)
	}
	if len(hunks) != 1 || hunks[0].LineStart != 1 || hunks[0].Author != "MinCode Test" {
		t.Fatalf("unexpected blame hunks: %#v", hunks)
	}
}

func createNestedGitRepository(t *testing.T) (repositoryRoot, nestedProjectDir, nestedFile string) {
	t.Helper()
	outerWorkspace := t.TempDir()
	repositoryRoot = filepath.Join(outerWorkspace, "claw-specs")
	nestedProjectDir = filepath.Join(repositoryRoot, "01 设计思路")
	nestedFile = filepath.Join(nestedProjectDir, "00-产品设计思路-总览.md")
	if err := os.MkdirAll(filepath.Dir(nestedFile), 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	if err := os.WriteFile(nestedFile, []byte("export const scope = 'nested';\n"), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	runGitTestCommand(t, repositoryRoot, "init")
	runGitTestCommand(t, repositoryRoot, "config", "user.name", "MinCode Test")
	runGitTestCommand(t, repositoryRoot, "config", "user.email", "mincode-test@example.invalid")
	runGitTestCommand(t, repositoryRoot, "add", ".")
	runGitTestCommand(t, repositoryRoot, "commit", "-m", "test nested Git project")
	return repositoryRoot, nestedProjectDir, nestedFile
}

func runGitTestCommand(t *testing.T, directory string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", directory}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func marshalGitTestArgs(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test arguments: %v", err)
	}
	return string(payload)
}

func hasLocalBranch(branches []gitBranchEntry, name string) bool {
	for _, branch := range branches {
		if branch.Name == name && !branch.IsRemote {
			return true
		}
	}
	return false
}
