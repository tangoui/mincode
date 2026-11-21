package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// testDeniedPath mirrors the plugin host's workspacePathDenied(path, false):
// VCS directories anywhere in the path and credential file names.
func testDeniedPath(relative string) bool {
	for _, segment := range strings.Split(relative, "/") {
		for _, denied := range []string{".git", ".hg", ".svn"} {
			if strings.EqualFold(segment, denied) {
				return true
			}
		}
	}
	base := path.Base(relative)
	for _, name := range []string{".env", ".env.local", "server.pem", "server.key", "id_rsa", "id_ed25519", "cert.p12", "cert.pfx", "vault.kdbx", ".npmrc", ".netrc", ".pgpass", "app.keystore", "app.jks", "credentials", "terraform.tfstate"} {
		if strings.EqualFold(base, name) {
			return true
		}
	}
	return false
}

// stagedTestRepository is an empty repository with an identity and one
// seed commit.
func stagedTestRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repository := t.TempDir()
	runGitTestCommand(t, repository, "init", "-b", "main")
	configureGitTestIdentity(t, repository)
	writeGitTestFile(t, filepath.Join(repository, "README.md"), "seed\n")
	runGitTestCommand(t, repository, "add", ".")
	runGitTestCommand(t, repository, "commit", "-m", "seed")
	return repository
}

func writeStagedTestFile(t *testing.T, repository, relative, content string) {
	t.Helper()
	target := filepath.Join(repository, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitTestFile(t, target, content)
}

func TestStagedChangesForPluginExcludesDeniedPaths(t *testing.T) {
	repository := stagedTestRepository(t)
	// A renamed file whose old name is sensitive: neither name may leak.
	writeStagedTestFile(t, repository, ".env.local", "OLD_SECRET=rename-old-content\n")
	runGitTestCommand(t, repository, "add", ".")
	runGitTestCommand(t, repository, "commit", "-m", "old secret")
	if err := os.MkdirAll(filepath.Join(repository, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repository, "mv", ".env.local", "docs/notes.md")

	writeStagedTestFile(t, repository, "src/a.go", "package a\n")
	writeStagedTestFile(t, repository, ".env", "API_KEY=env-file-content\n")
	writeStagedTestFile(t, repository, "config/id_rsa", "PRIVATE KEY id-rsa-content\n")
	runGitTestCommand(t, repository, "add", "src/a.go", ".env", "config/id_rsa")

	changes, err := StagedChangesForPlugin(repository, testDeniedPath, 500, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Files) != 1 || changes.Files[0] != (StagedFile{Path: "src/a.go", Status: "added"}) {
		t.Fatalf("files = %+v", changes.Files)
	}
	if changes.OmittedFiles != 3 || changes.FilesTruncated || changes.DiffTruncated {
		t.Fatalf("changes = %+v", changes)
	}
	if !strings.Contains(changes.Diff, "src/a.go") || !strings.Contains(changes.Diff, "+package a") {
		t.Fatalf("diff misses the kept file: %q", changes.Diff)
	}
	for _, leaked := range []string{".env", "env-file-content", "id_rsa", "id-rsa-content", "notes.md", "rename-old-content", "OLD_SECRET"} {
		if strings.Contains(changes.Diff, leaked) {
			t.Fatalf("diff leaks %q: %q", leaked, changes.Diff)
		}
	}
}

func TestStagedChangesForPluginDisablesTextconv(t *testing.T) {
	repository := stagedTestRepository(t)
	markers := t.TempDir()
	textconvMarker := filepath.Join(markers, "textconv")
	externalMarker := filepath.Join(markers, "external")
	driverMarker := filepath.Join(markers, "driver")
	fsmonitorMarker := filepath.Join(markers, "fsmonitor")
	writeStagedTestFile(t, repository, ".gitattributes", "*.bin diff=evil\n")
	runGitTestCommand(t, repository, "add", ".gitattributes")
	runGitTestCommand(t, repository, "commit", "-m", "attributes")
	runGitTestCommand(t, repository, "config", "diff.evil.textconv", fmt.Sprintf("sh -c 'touch %s; echo TEXTCONV-OUTPUT' --", textconvMarker))
	runGitTestCommand(t, repository, "config", "diff.evil.command", fmt.Sprintf("sh -c 'touch %s' --", driverMarker))
	runGitTestCommand(t, repository, "config", "diff.external", fmt.Sprintf("sh -c 'touch %s' --", externalMarker))

	writeStagedTestFile(t, repository, "data.bin", "plain line\n")
	runGitTestCommand(t, repository, "add", "data.bin")
	// Set after staging so `git add` itself does not run the hook.
	runGitTestCommand(t, repository, "config", "core.fsmonitor", fmt.Sprintf("touch %s; false", fsmonitorMarker))

	changes, err := StagedChangesForPlugin(repository, testDeniedPath, 500, 65536)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{textconvMarker, externalMarker, driverMarker, fsmonitorMarker} {
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("repository configuration ran a command: %s exists", filepath.Base(marker))
		}
	}
	if strings.Contains(changes.Diff, "TEXTCONV-OUTPUT") {
		t.Fatalf("diff contains textconv output: %q", changes.Diff)
	}
	if !strings.Contains(changes.Diff, "+plain line") && !strings.Contains(changes.Diff, "Binary files") {
		t.Fatalf("diff is not the raw diff: %q", changes.Diff)
	}
	if len(changes.Files) != 1 || changes.Files[0].Path != "data.bin" {
		t.Fatalf("files = %+v", changes.Files)
	}
}

func TestStagedChangesForPluginTruncates(t *testing.T) {
	repository := stagedTestRepository(t)
	if _, err := StagedChangesForPlugin(repository, testDeniedPath, 500, 65536); err == nil || !errors.Is(err, ErrNoStagedChanges) || !strings.Contains(err.Error(), "no staged changes") {
		t.Fatalf("empty index err = %v", err)
	}

	var large strings.Builder
	for large.Len() < 200*1024 {
		fmt.Fprintf(&large, "line %06d 中文内容\n", large.Len())
	}
	writeStagedTestFile(t, repository, "large.txt", large.String())
	runGitTestCommand(t, repository, "add", "large.txt")
	changes, err := StagedChangesForPlugin(repository, testDeniedPath, 500, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if !changes.DiffTruncated || len(changes.Diff) > 65536 || len(changes.Diff) < 60000 {
		t.Fatalf("diff = %d bytes, truncated %v", len(changes.Diff), changes.DiffTruncated)
	}
	if !strings.HasSuffix(changes.Diff, "\n") || !utf8.ValidString(changes.Diff) {
		t.Fatalf("diff does not end with a complete line: %q", changes.Diff[len(changes.Diff)-40:])
	}
	runGitTestCommand(t, repository, "reset", "-q")

	for index := range 600 {
		writeStagedTestFile(t, repository, fmt.Sprintf("many/file-%03d.txt", index), "x\n")
	}
	runGitTestCommand(t, repository, "add", "many")
	changes, err = StagedChangesForPlugin(repository, testDeniedPath, 500, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Files) != 500 || !changes.FilesTruncated || changes.OmittedFiles != 0 {
		t.Fatalf("files = %d truncated %v omitted %d", len(changes.Files), changes.FilesTruncated, changes.OmittedFiles)
	}
	if changes.Files[0].Path != "many/file-000.txt" || changes.Files[499].Path != "many/file-499.txt" {
		t.Fatalf("files order = %s .. %s", changes.Files[0].Path, changes.Files[499].Path)
	}
	if strings.Contains(changes.Diff, "many/file-500.txt") {
		t.Fatal("diff covers a file past filesMax")
	}
}

func TestStagedChangesForPluginStatuses(t *testing.T) {
	repository := stagedTestRepository(t)
	writeStagedTestFile(t, repository, "keep.txt", strings.Repeat("same content line\n", 20))
	writeStagedTestFile(t, repository, "gone.txt", "bye\n")
	runGitTestCommand(t, repository, "add", ".")
	runGitTestCommand(t, repository, "commit", "-m", "files")
	runGitTestCommand(t, repository, "mv", "keep.txt", "moved.txt")
	runGitTestCommand(t, repository, "rm", "-q", "gone.txt")
	writeStagedTestFile(t, repository, "README.md", "changed\n")
	runGitTestCommand(t, repository, "add", "README.md")

	changes, err := StagedChangesForPlugin(repository, testDeniedPath, 500, 65536)
	if err != nil {
		t.Fatal(err)
	}
	want := []StagedFile{
		{Path: "README.md", Status: "modified"},
		{Path: "gone.txt", Status: "deleted"},
		{Path: "moved.txt", Status: "renamed", OldPath: "keep.txt"},
	}
	if len(changes.Files) != len(want) {
		t.Fatalf("files = %+v", changes.Files)
	}
	for index := range want {
		if changes.Files[index] != want[index] {
			t.Fatalf("files[%d] = %+v, want %+v", index, changes.Files[index], want[index])
		}
	}
	if !strings.Contains(changes.Diff, "rename from keep.txt") {
		t.Fatalf("diff = %q", changes.Diff)
	}
}

func TestTruncateStagedDiff(t *testing.T) {
	if got, cut := TruncateStagedDiff("a\nb\n", 10); got != "a\nb\n" || cut {
		t.Fatalf("short = %q %v", got, cut)
	}
	if got, cut := TruncateStagedDiff("aa\nbb\ncc\n", 7); got != "aa\nbb\n" || !cut {
		t.Fatalf("lines = %q %v", got, cut)
	}
	// No line end within the budget: cut on a UTF-8 boundary.
	if got, cut := TruncateStagedDiff("中文中文", 7); got != "中文" || !cut {
		t.Fatalf("runes = %q %v", got, cut)
	}
}
