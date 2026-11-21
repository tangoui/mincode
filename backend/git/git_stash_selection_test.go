package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGitStashPushStoresOnlySelectedFilesAndListsThem(t *testing.T) {
	repository := createGitSelectionTestRepository(t)
	selectedPath := filepath.Join(repository, "selected.txt")
	keptPath := filepath.Join(repository, "kept.txt")
	literalName := "literal [draft].txt"
	literalPath := filepath.Join(repository, literalName)
	writeGitSelectionTestFile(t, selectedPath, "selected change\n")
	writeGitSelectionTestFile(t, keptPath, "kept change\n")
	runGitTestCommand(t, repository, "add", "kept.txt")
	writeGitSelectionTestFile(t, literalPath, "new selected file\n")
	message := "selected stash"

	if _, err := gitStashPushHandler("", marshalGitTestArgs(t, stashPushArgs{
		RepoPath: repository,
		Message:  &message,
		Paths:    []string{"selected.txt", literalName},
	})); err != nil {
		t.Fatalf("stash selected files: %v", err)
	}

	assertGitSelectionTestContent(t, selectedPath, "selected base\n")
	assertGitSelectionTestContent(t, keptPath, "kept change\n")
	if _, err := os.Stat(literalPath); !os.IsNotExist(err) {
		t.Fatalf("selected untracked file must be removed from the working tree, stat err = %v", err)
	}
	assertOnlyGitSelectionTestChange(t, repository, "kept.txt")
	assertGitSelectionTestStaged(t, repository, "kept.txt")

	listJSON, err := gitStashListHandler("", marshalGitTestArgs(t, repoOnlyArgs{RepoPath: repository}))
	if err != nil {
		t.Fatalf("list stashes: %v", err)
	}
	var stashes []gitStashEntry
	if err := json.Unmarshal([]byte(listJSON), &stashes); err != nil {
		t.Fatalf("decode stash list: %v", err)
	}
	if len(stashes) != 1 {
		t.Fatalf("stash count = %d, want 1", len(stashes))
	}
	assertGitSelectionTestFileList(t, stashes[0].Files, []string{"selected.txt", literalName}, []string{"kept.txt"})

	if _, err := gitStashPopHandler("", marshalGitTestArgs(t, stashIndexArgs{
		RepoPath: repository, StashIndex: stashes[0].Index,
	})); err != nil {
		t.Fatalf("pop selected stash: %v", err)
	}
	assertGitSelectionTestContent(t, selectedPath, "selected change\n")
	assertGitSelectionTestContent(t, keptPath, "kept change\n")
	assertGitSelectionTestContent(t, literalPath, "new selected file\n")
}

func TestGitShelfStoresOnlySelectedFilesAndPersistsFileList(t *testing.T) {
	repository := createGitSelectionTestRepository(t)
	selectedPath := filepath.Join(repository, "selected.txt")
	keptPath := filepath.Join(repository, "kept.txt")
	literalName := "literal [shelf].txt"
	literalPath := filepath.Join(repository, literalName)
	writeGitSelectionTestFile(t, selectedPath, "shelf selected change\n")
	writeGitSelectionTestFile(t, keptPath, "shelf kept change\n")
	runGitTestCommand(t, repository, "add", "kept.txt")
	writeGitSelectionTestFile(t, literalPath, "new shelf file\n")
	message := "selected shelf"

	createdJSON, err := gitShelfCreateHandler("", marshalGitTestArgs(t, shelfCreateArgs{
		RepoPath: repository,
		Message:  &message,
		Paths:    []string{"selected.txt", literalName},
	}))
	if err != nil {
		t.Fatalf("shelf selected files: %v", err)
	}
	var created gitShelfEntry
	if err := json.Unmarshal([]byte(createdJSON), &created); err != nil {
		t.Fatalf("decode created shelf: %v", err)
	}
	assertGitSelectionTestFileList(t, created.Files, []string{"selected.txt", literalName}, []string{"kept.txt"})
	assertGitSelectionTestContent(t, selectedPath, "selected base\n")
	assertGitSelectionTestContent(t, keptPath, "shelf kept change\n")
	if _, err := os.Stat(literalPath); !os.IsNotExist(err) {
		t.Fatalf("selected untracked file must be removed after shelving, stat err = %v", err)
	}
	assertOnlyGitSelectionTestChange(t, repository, "kept.txt")
	assertGitSelectionTestStaged(t, repository, "kept.txt")

	stashJSON, err := gitStashListHandler("", marshalGitTestArgs(t, repoOnlyArgs{RepoPath: repository}))
	if err != nil {
		t.Fatalf("list temporary stashes: %v", err)
	}
	var temporaryStashes []gitStashEntry
	if err := json.Unmarshal([]byte(stashJSON), &temporaryStashes); err != nil {
		t.Fatalf("decode temporary stash list: %v", err)
	}
	if len(temporaryStashes) != 0 {
		t.Fatalf("temporary stash must be removed after shelf creation: %+v", temporaryStashes)
	}

	listJSON, err := gitShelfListHandler("", marshalGitTestArgs(t, repoOnlyArgs{RepoPath: repository}))
	if err != nil {
		t.Fatalf("list shelves: %v", err)
	}
	var shelves []gitShelfEntry
	if err := json.Unmarshal([]byte(listJSON), &shelves); err != nil {
		t.Fatalf("decode shelf list: %v", err)
	}
	if len(shelves) != 1 {
		t.Fatalf("shelf count = %d, want 1", len(shelves))
	}
	assertGitSelectionTestFileList(t, shelves[0].Files, []string{"selected.txt", literalName}, []string{"kept.txt"})

	if _, err := gitShelfApplyHandler("", marshalGitTestArgs(t, shelfIDArgs{
		RepoPath: repository, ShelfID: created.ID,
	})); err != nil {
		t.Fatalf("apply selected shelf: %v", err)
	}
	assertGitSelectionTestContent(t, selectedPath, "shelf selected change\n")
	assertGitSelectionTestContent(t, keptPath, "shelf kept change\n")
	assertGitSelectionTestContent(t, literalPath, "new shelf file\n")
}

func TestGitSelectedStashRejectsEmptyAndUnknownPaths(t *testing.T) {
	repository := createGitSelectionTestRepository(t)
	writeGitSelectionTestFile(t, filepath.Join(repository, "selected.txt"), "changed\n")
	message := "invalid selection"

	_, err := gitStashPushHandler("", marshalGitTestArgs(t, stashPushArgs{
		RepoPath: repository, Message: &message, Paths: []string{},
	}))
	if err == nil {
		t.Fatal("empty selection must be rejected")
	}

	_, err = gitStashPushHandler("", marshalGitTestArgs(t, stashPushArgs{
		RepoPath: repository, Message: &message, Paths: []string{"missing.txt"},
	}))
	if err == nil {
		t.Fatal("a path outside the current changes must be rejected")
	}
	assertGitSelectionTestContent(t, filepath.Join(repository, "selected.txt"), "changed\n")
}

func createGitSelectionTestRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	runGitTestCommand(t, repository, "init")
	runGitTestCommand(t, repository, "config", "user.email", "test@example.com")
	runGitTestCommand(t, repository, "config", "user.name", "Test User")
	writeGitSelectionTestFile(t, filepath.Join(repository, "selected.txt"), "selected base\n")
	writeGitSelectionTestFile(t, filepath.Join(repository, "kept.txt"), "kept base\n")
	runGitTestCommand(t, repository, "add", ".")
	runGitTestCommand(t, repository, "commit", "-m", "initial")
	return repository
}

func writeGitSelectionTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertGitSelectionTestContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(content) != want {
		t.Fatalf("content of %s = %q, want %q", path, content, want)
	}
}

func assertOnlyGitSelectionTestChange(t *testing.T, repository, wantPath string) {
	t.Helper()
	status, err := gitGetStatus(repository)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	files := gitUniqueChangedFiles(status)
	if len(files) != 1 || files[0].Path != wantPath {
		t.Fatalf("remaining changes = %+v, want only %s", files, wantPath)
	}
}

func assertGitSelectionTestStaged(t *testing.T, repository, wantPath string) {
	t.Helper()
	status, err := gitGetStatus(repository)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if len(status.Staged) != 1 || status.Staged[0].Path != wantPath {
		t.Fatalf("staged changes = %+v, want only %s", status.Staged, wantPath)
	}
	if len(status.Unstaged) != 0 || len(status.Untracked) != 0 {
		t.Fatalf("unexpected unstaged or untracked changes: %+v %+v", status.Unstaged, status.Untracked)
	}
}

func assertGitSelectionTestFileList(t *testing.T, files []gitFileEntry, included, excluded []string) {
	t.Helper()
	paths := make(map[string]bool, len(files))
	for _, file := range files {
		paths[file.Path] = true
	}
	for _, path := range included {
		if !paths[path] {
			t.Fatalf("file list %+v does not contain %s", files, path)
		}
	}
	for _, path := range excluded {
		if paths[path] {
			t.Fatalf("file list %+v unexpectedly contains %s", files, path)
		}
	}
	if len(files) != len(included) {
		t.Fatalf("file list length = %d, want %d: %+v", len(files), len(included), files)
	}
}
