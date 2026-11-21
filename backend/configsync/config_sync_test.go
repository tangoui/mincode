package configsync

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSyncPushAndPullLocalRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	originalUserDataDir := userDataDir
	originalAllowLocal := allowLocalRemote
	t.Cleanup(func() {
		userDataDir = originalUserDataDir
		allowLocalRemote = originalAllowLocal
	})
	root := t.TempDir()
	userDataDir = func() string { return filepath.Join(root, "data") }
	allowLocalRemote = true

	remote := filepath.Join(root, "remote.git")
	runTestGit(t, root, "init", "--bare", remote)
	args := repositoryArgs{
		RemoteURL: remote,
		Branch:    "main",
		Credentials: repositoryCredentials{
			Mode: "system",
		},
	}

	connectionJSON, err := testConnectionHandler("", marshalArgs(t, args))
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	var connection connectionResult
	if err := json.Unmarshal([]byte(connectionJSON), &connection); err != nil {
		t.Fatal(err)
	}
	if !connection.OK || !connection.RepositoryEmpty {
		t.Fatalf("unexpected connection result: %#v", connection)
	}

	payload := "mc-aes-v1:c2FsdA==:aXY=:Y2lwaGVy"
	pushJSON, err := pushHandler("", marshalArgs(t, pushArgs{
		RemoteURL:   remote,
		Branch:      "main",
		Credentials: args.Credentials,
		Payload:     payload,
	}))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	var pushed pushResult
	if err := json.Unmarshal([]byte(pushJSON), &pushed); err != nil {
		t.Fatal(err)
	}
	if !pushed.Changed || pushed.Commit == "" {
		t.Fatalf("unexpected push result: %#v", pushed)
	}

	pullJSON, err := pullHandler("", marshalArgs(t, args))
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	var pulled pullResult
	if err := json.Unmarshal([]byte(pullJSON), &pulled); err != nil {
		t.Fatal(err)
	}
	if !pulled.Exists || pulled.Payload != payload || pulled.Commit != pushed.Commit {
		t.Fatalf("unexpected pull result: %#v", pulled)
	}

	clone := filepath.Join(root, "clone")
	runTestGit(t, root, "clone", "--branch", "main", remote, clone)
	stored, err := os.ReadFile(filepath.Join(clone, payloadFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(stored)) != payload {
		t.Fatalf("stored payload = %q", stored)
	}
}

func TestValidateRemoteURLRejectsEmbeddedCredentials(t *testing.T) {
	if err := validateRemoteURL("https://user:token@example.com/config.git"); err == nil {
		t.Fatal("expected embedded credentials to be rejected")
	}
	if err := validateRemoteURL("https://example.com/config.git"); err != nil {
		t.Fatalf("valid HTTPS remote rejected: %v", err)
	}
	if err := validateRemoteURL("git@example.com:team/config.git"); err != nil {
		t.Fatalf("valid SSH remote rejected: %v", err)
	}
}

func runTestGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func marshalArgs(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}
