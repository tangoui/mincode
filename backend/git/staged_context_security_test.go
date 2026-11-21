package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertStagedDiffClean fails when the diff contains any of the leaked
// strings.
func assertStagedDiffClean(t *testing.T, diff string, leaked ...string) {
	t.Helper()
	for _, value := range leaked {
		if strings.Contains(diff, value) {
			t.Fatalf("diff leaks %q:\n%s", value, diff)
		}
	}
}

// A kept file whose path is a directory prefix of denied files: the
// pathspec for the kept file must not pull the denied files into the diff.
func TestStagedChangesForPluginDirectoryPrefixDoesNotLeak(t *testing.T) {
	t.Run("kept file replaced by a directory of secrets", func(t *testing.T) {
		repository := stagedTestRepository(t)
		writeStagedTestFile(t, repository, "config", "old config file\n")
		runGitTestCommand(t, repository, "add", "config")
		runGitTestCommand(t, repository, "commit", "-m", "config file")
		runGitTestCommand(t, repository, "rm", "-q", "config")
		writeStagedTestFile(t, repository, "config/.env", "TOKEN=prefix-env-secret\n")
		writeStagedTestFile(t, repository, "config/id_rsa", "prefix-key-secret\n")
		runGitTestCommand(t, repository, "add", "config")

		changes, err := StagedChangesForPlugin(repository, testDeniedPath, 500, 65536)
		if err != nil {
			t.Fatal(err)
		}
		if len(changes.Files) != 1 || changes.Files[0] != (StagedFile{Path: "config", Status: "deleted"}) || changes.OmittedFiles != 2 {
			t.Fatalf("changes = %+v", changes)
		}
		if !strings.Contains(changes.Diff, "-old config file") {
			t.Fatalf("diff misses the kept file: %q", changes.Diff)
		}
		assertStagedDiffClean(t, changes.Diff, "prefix-env-secret", "prefix-key-secret", ".env", "id_rsa")
	})
	t.Run("directory of secrets replaced by a kept file", func(t *testing.T) {
		repository := stagedTestRepository(t)
		writeStagedTestFile(t, repository, "secret/.env", "TOKEN=reverse-env-secret\n")
		writeStagedTestFile(t, repository, "secret/id_rsa", "reverse-key-secret\n")
		runGitTestCommand(t, repository, "add", "secret")
		runGitTestCommand(t, repository, "commit", "-m", "secrets")
		runGitTestCommand(t, repository, "rm", "-q", "-r", "secret")
		writeStagedTestFile(t, repository, "secret", "now a plain file\n")
		runGitTestCommand(t, repository, "add", "secret")

		changes, err := StagedChangesForPlugin(repository, testDeniedPath, 500, 65536)
		if err != nil {
			t.Fatal(err)
		}
		if len(changes.Files) != 1 || changes.Files[0] != (StagedFile{Path: "secret", Status: "added"}) || changes.OmittedFiles != 2 {
			t.Fatalf("changes = %+v", changes)
		}
		if !strings.Contains(changes.Diff, "+now a plain file") {
			t.Fatalf("diff misses the kept file: %q", changes.Diff)
		}
		assertStagedDiffClean(t, changes.Diff, "reverse-env-secret", "reverse-key-secret", ".env", "id_rsa")
	})
	t.Run("files past filesMax under a kept prefix", func(t *testing.T) {
		repository := stagedTestRepository(t)
		writeStagedTestFile(t, repository, "lib", "lib file\n")
		runGitTestCommand(t, repository, "add", "lib")
		runGitTestCommand(t, repository, "commit", "-m", "lib file")
		runGitTestCommand(t, repository, "rm", "-q", "lib")
		writeStagedTestFile(t, repository, "lib/extra.txt", "past-files-max\n")
		runGitTestCommand(t, repository, "add", "lib")

		changes, err := StagedChangesForPlugin(repository, testDeniedPath, 1, 65536)
		if err != nil {
			t.Fatal(err)
		}
		if len(changes.Files) != 1 || changes.Files[0].Path != "lib" || !changes.FilesTruncated {
			t.Fatalf("changes = %+v", changes)
		}
		assertStagedDiffClean(t, changes.Diff, "past-files-max", "extra.txt")
	})
}

func TestFilterStagedDiffKeepsOnlyListedSections(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/config b/config",
		"deleted file mode 100644",
		"-old",
		"diff --git a/config/.env b/config/.env",
		"+TOKEN=secret",
		"diff --git \"a/we\\\"ird\" \"b/we\\\"ird\"",
		"+quoted kept",
		"diff --git a/old.txt b/new.txt",
		"rename from old.txt",
		"diff --git a/other b/other",
		"+not listed",
		"",
	}, "\n")
	files := []StagedFile{
		{Path: "config", Status: "deleted"},
		{Path: `we"ird`, Status: "added"},
		{Path: "new.txt", Status: "renamed", OldPath: "old.txt"},
	}
	got := filterStagedDiff(diff, files)
	want := strings.Join([]string{
		"diff --git a/config b/config",
		"deleted file mode 100644",
		"-old",
		"diff --git \"a/we\\\"ird\" \"b/we\\\"ird\"",
		"+quoted kept",
		"diff --git a/old.txt b/new.txt",
		"rename from old.txt",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("filtered =\n%s\nwant\n%s", got, want)
	}
	// Text before the first section header is dropped too.
	if got := filterStagedDiff("+stray\n", files); got != "" {
		t.Fatalf("stray = %q", got)
	}
}

func TestStagedChangesForPluginQuotedPath(t *testing.T) {
	repository := stagedTestRepository(t)
	writeStagedTestFile(t, repository, `we"ird\name.txt`, "quoted content\n")
	runGitTestCommand(t, repository, "add", ".")
	changes, err := StagedChangesForPlugin(repository, testDeniedPath, 500, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Files) != 1 || changes.Files[0].Path != `we"ird\name.txt` || !strings.Contains(changes.Diff, "+quoted content") {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestStagedChangesForPluginIgnoresGitEnvironment(t *testing.T) {
	repository := stagedTestRepository(t)
	writeStagedTestFile(t, repository, "real.txt", "real content\n")
	runGitTestCommand(t, repository, "add", "real.txt")
	other := stagedTestRepository(t)
	writeStagedTestFile(t, other, "other.txt", "other content\n")
	runGitTestCommand(t, other, "add", "other.txt")
	markers := t.TempDir()
	marker := filepath.Join(markers, "env")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	t.Setenv("GIT_EXTERNAL_DIFF", "sh -c 'touch "+marker+"' --")
	t.Setenv("GIT_PAGER", "sh -c 'touch "+marker+"'")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "diff.external")
	t.Setenv("GIT_CONFIG_VALUE_0", "sh -c 'touch "+marker+"' --")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.fsmonitor'='touch "+marker+"; false'")

	changes, err := StagedChangesForPlugin(repository, testDeniedPath, 500, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Files) != 1 || changes.Files[0].Path != "real.txt" || strings.Contains(changes.Diff, "other content") {
		t.Fatalf("changes = %+v", changes)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("an inherited GIT_* variable ran a command")
	}
	if env := stagedEnvironment([]string{"GIT_DIR=x", "git_ssh_command=y", "GIT_CONFIG_GLOBAL=z", "GIT_SSH=s", "GIT_ASKPASS=a", "GIT_EXEC_PATH=e", "HOME=/h", "GIT_TERMINAL_PROMPT=0"}); strings.Join(env, " ") != "HOME=/h GIT_TERMINAL_PROMPT=0 GIT_PAGER=cat GIT_NO_LAZY_FETCH=1" {
		t.Fatalf("environment = %v", env)
	}
}

func TestStagedChangesForPluginCancelled(t *testing.T) {
	repository := stagedTestRepository(t)
	writeStagedTestFile(t, repository, "a.txt", "a\n")
	runGitTestCommand(t, repository, "add", "a.txt")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := StagedChangesForPluginContext(ctx, repository, testDeniedPath, 500, 65536); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled err = %v", err)
	}
	if _, err := RepositoryLayoutContext(ctx, repository); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled layout err = %v", err)
	}
}

func TestRepositoryLayout(t *testing.T) {
	repository := stagedTestRepository(t)
	canonical, _ := filepath.EvalSymlinks(repository)
	layout, err := RepositoryLayoutContext(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if layout.TopLevel != canonical || layout.GitDir != filepath.Join(canonical, ".git") || layout.CommonDir != filepath.Join(canonical, ".git") {
		t.Fatalf("layout = %+v", layout)
	}

	// A work tree whose .git file points elsewhere.
	outside := t.TempDir()
	separate := filepath.Join(t.TempDir(), "separate")
	runGitTestCommand(t, outside, "init", "-q", "--separate-git-dir", separate)
	layout, err = RepositoryLayoutContext(context.Background(), outside)
	if err != nil {
		t.Fatal(err)
	}
	canonicalSeparate, _ := filepath.EvalSymlinks(separate)
	if layout.GitDir != canonicalSeparate || layout.CommonDir != canonicalSeparate {
		t.Fatalf("separate layout = %+v", layout)
	}

	// A linked worktree: its own git dir under the main one, the common dir is the main one.
	worktree := filepath.Join(t.TempDir(), "wt")
	runGitTestCommand(t, repository, "worktree", "add", "-q", worktree)
	layout, err = RepositoryLayoutContext(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	canonicalWorktree, _ := filepath.EvalSymlinks(worktree)
	if layout.TopLevel != canonicalWorktree || !strings.HasPrefix(layout.GitDir, filepath.Join(canonical, ".git", "worktrees")+string(filepath.Separator)) || layout.CommonDir != filepath.Join(canonical, ".git") {
		t.Fatalf("worktree layout = %+v", layout)
	}
	if _, err := RepositoryLayoutContext(context.Background(), t.TempDir()); err == nil {
		t.Fatal("a plain folder has a layout")
	}
}
