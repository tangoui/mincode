package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func createGitTagTestRepository(t *testing.T) (string, string) {
	t.Helper()
	repository := t.TempDir()
	runGitTestCommand(t, repository, "init")
	runGitTestCommand(t, repository, "config", "user.name", "MinCode Test")
	runGitTestCommand(t, repository, "config", "user.email", "mincode-test@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repository, "add", ".")
	runGitTestCommand(t, repository, "commit", "-m", "first")
	head := gitTestOutput(t, repository, "rev-parse", "HEAD")
	return repository, head
}

func listGitTestTags(t *testing.T, repository string) []gitTagEntry {
	t.Helper()
	payload, err := gitListTagsHandler("", marshalGitTestArgs(t, repoOnlyArgs{RepoPath: repository}))
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	var tags []gitTagEntry
	if err := json.Unmarshal([]byte(payload), &tags); err != nil {
		t.Fatalf("decode tags: %v", err)
	}
	return tags
}

func TestGitListTagsReturnsLightweightAndAnnotatedTags(t *testing.T) {
	repository, head := createGitTagTestRepository(t)
	runGitTestCommand(t, repository, "-c", "user.name=MinCode Test", "tag", "v0.1.0")
	runGitTestCommand(t, repository, "tag", "-a", "v0.2.0", "-m", "Release 0.2.0 (中文说明)")

	tags := listGitTestTags(t, repository)
	if len(tags) != 2 {
		t.Fatalf("expected both tags, got %#v", tags)
	}
	byName := map[string]gitTagEntry{}
	for _, tag := range tags {
		byName[tag.Name] = tag
		if tag.CommitHash != head {
			t.Fatalf("tag %s must point at %s, got %q", tag.Name, head, tag.CommitHash)
		}
		if tag.Date == "" {
			t.Fatalf("tag %s has no date", tag.Name)
		}
	}
	if byName["v0.1.0"].Annotated || byName["v0.1.0"].Message != nil {
		t.Fatalf("lightweight tag misreported: %#v", byName["v0.1.0"])
	}
	annotated := byName["v0.2.0"]
	if !annotated.Annotated || annotated.Message == nil || *annotated.Message != "Release 0.2.0 (中文说明)" {
		t.Fatalf("annotated tag misreported: %#v", annotated)
	}
}

func TestGitFetchTagsBringsRemoteTagsIntoTheList(t *testing.T) {
	upstream, _ := createGitTagTestRepository(t)
	runGitTestCommand(t, upstream, "tag", "-a", "v1.0.0", "-m", "remote release")
	clone := filepath.Join(t.TempDir(), "clone")
	runGitTestCommand(t, filepath.Dir(clone), "clone", "--no-tags", upstream, clone)
	if tags := listGitTestTags(t, clone); len(tags) != 0 {
		t.Fatalf("clone without tags should start empty, got %#v", tags)
	}

	if _, err := gitFetchTagsHandler("", marshalGitTestArgs(t, pushPullArgs{RepoPath: clone})); err != nil {
		t.Fatalf("fetch tags: %v", err)
	}
	tags := listGitTestTags(t, clone)
	if len(tags) != 1 || tags[0].Name != "v1.0.0" {
		t.Fatalf("expected the remote tag after fetching, got %#v", tags)
	}
}

func TestGitFetchTagsExplainsAMissingRemote(t *testing.T) {
	repository, _ := createGitTagTestRepository(t)
	if _, err := gitFetchTagsHandler("", marshalGitTestArgs(t, pushPullArgs{RepoPath: repository})); err == nil {
		t.Fatal("fetching tags without a remote must fail")
	}
}
