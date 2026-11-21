package git

import "testing"

func TestGitCommitTimeoutAllowsRepositoryHooks(t *testing.T) {
	if gitCommitTimeout <= gitDefaultTimeout {
		t.Fatalf("gitCommitTimeout = %s, must exceed default timeout %s", gitCommitTimeout, gitDefaultTimeout)
	}
}
