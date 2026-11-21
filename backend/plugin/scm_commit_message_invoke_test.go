package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mincode/app/backend/git"
	"mincode/app/plugins/api"
)

// scmCommitMessageManager is a running helper plugin contributing the commit
// message provider "conventional" backed by its read action "run".
func scmCommitMessageManager(t *testing.T, capabilities ...api.Capability) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := startedContributionManager(t)
	record.mu.Lock()
	record.manifest.Capabilities = capabilities
	for index := range record.manifest.Contributes.Actions {
		if record.manifest.Contributes.Actions[index].ID == "run" {
			record.manifest.Contributes.Actions[index].MaxPayloadBytes = scmCommitMessagePayloadMaxBytes
		}
	}
	record.manifest.contributions = map[string]*resolvedContribution{scmCommitMessagePointKey: {Entries: []any{
		ScmCommitMessageContribution{ID: "conventional", Title: "Ticket", Action: "run"},
	}}}
	record.mu.Unlock()
	return manager, record
}

func scmGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", directory}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func scmInitRepository(t *testing.T, directory string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	scmGit(t, directory, "init", "-q", "-b", "main")
	scmGit(t, directory, "config", "user.name", "MinCode Test")
	scmGit(t, directory, "config", "user.email", "mincode-test@example.invalid")
}

func scmWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func TestScmCommitMessageRepoMustBeUnderProject(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	project := filepath.Join(base, "demo")
	scmInitRepository(t, project)
	nested := filepath.Join(project, "packages", "nested")
	scmInitRepository(t, nested)
	if err := os.MkdirAll(filepath.Join(project, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	scmInitRepository(t, outside)
	if err := os.Symlink(outside, filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	canonicalProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	// A work tree in the project whose .git file points outside it.
	separate := filepath.Join(project, "separate")
	if err := os.MkdirAll(separate, 0o755); err != nil {
		t.Fatal(err)
	}
	scmGit(t, separate, "init", "-q", "--separate-git-dir", filepath.Join(base, "elsewhere.git"))
	// Linked worktrees: of the outside repository (common dir outside the
	// project) and of the project repository (everything inside).
	scmWrite(t, filepath.Join(outside, "seed"), "seed\n")
	scmGit(t, outside, "add", ".")
	scmGit(t, outside, "commit", "-q", "-m", "seed")
	scmGit(t, outside, "worktree", "add", "-q", filepath.Join(project, "foreign-wt"))
	scmWrite(t, filepath.Join(project, "seed"), "seed\n")
	scmGit(t, project, "add", "seed")
	scmGit(t, project, "commit", "-q", "-m", "seed")
	scmGit(t, project, "worktree", "add", "-q", filepath.Join(project, "own-wt"))

	for name, repoPath := range map[string]string{"project root": project, "nested repository": nested, "worktree of the project": filepath.Join(project, "own-wt")} {
		root, repo, err := canonicalRepoUnderProject(project, repoPath)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want, _ := filepath.EvalSymlinks(repoPath)
		if root != canonicalProject || repo != want {
			t.Fatalf("%s: root %q repo %q", name, root, repo)
		}
	}
	for name, repoPath := range map[string]string{
		"outside the project":      outside,
		"symlink to outside":       filepath.Join(project, "link"),
		"subdirectory":             filepath.Join(project, "src"),
		"relative path":            "demo",
		"parent through dotdot":    filepath.Join(project, "..", "outside"),
		"missing":                  filepath.Join(project, "missing"),
		"project parent":           base,
		"git dir outside":          separate,
		"worktree of outside repo": filepath.Join(project, "foreign-wt"),
	} {
		if _, _, err := canonicalRepoUnderProject(project, repoPath); contributionErrorCode(err) != contributionErrForbidden {
			t.Errorf("%s: err = %v, want forbidden", name, err)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := canonicalRepoUnderProject(home, home); contributionErrorCode(err) != contributionErrForbidden {
		t.Errorf("home project: err = %v, want forbidden", err)
	}
}

func TestScmCommitMessagePayloadAndResult(t *testing.T) {
	requireGit(t)
	project := filepath.Join(t.TempDir(), "demo")
	scmInitRepository(t, project)
	scmWrite(t, filepath.Join(project, "README.md"), "seed\n")
	scmGit(t, project, "add", ".")
	scmGit(t, project, "commit", "-q", "-m", "seed")
	scmGit(t, project, "checkout", "-q", "-b", "feature/x")

	manager, record := scmCommitMessageManager(t, api.CapabilityWorkspaceRead)
	request := scmCommitMessageRequest{PluginID: record.id, ProviderID: "conventional", RequestID: "request-scm-0001", ProjectRoot: project, RepoPath: project, Locale: "zh-CN"}
	if _, err := manager.scmCommitMessage(context.Background(), request); contributionErrorCode(err) != contributionErrInvalidRequest || !strings.Contains(err.Error(), "no staged changes") {
		t.Fatalf("empty index err = %v", err)
	}

	scmWrite(t, filepath.Join(project, "src", "a.go"), "package a\n")
	scmWrite(t, filepath.Join(project, ".env"), "TOKEN=leak-me\n")
	scmGit(t, project, "add", "src/a.go", ".env")
	request.RequestID = "request-scm-0002"
	response, err := manager.scmCommitMessage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response != (scmCommitMessageResponse{Message: "feat: x\n\nbody", OmittedFiles: 1}) {
		t.Fatalf("response = %+v", response)
	}
	line := waitForLog(t, record, "helper scm payload ")
	_, payloadJSON, _ := strings.Cut(line, "helper scm payload ")
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("payload %q: %v", payloadJSON, err)
	}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if want := []string{"branch", "diff", "files", "filesTruncated", "locale", "omittedFiles", "provider", "repository", "truncated"}; !slices.Equal(keys, want) {
		t.Fatalf("payload keys = %v, want %v", keys, want)
	}
	for key, want := range map[string]string{
		"provider": `"conventional"`, "repository": `{"name":"demo"}`, "branch": `"feature/x"`,
		"files": `[{"path":"src/a.go","status":"added"}]`, "omittedFiles": `1`, "truncated": `false`, "filesTruncated": `false`, "locale": `"zh-CN"`,
	} {
		if string(payload[key]) != want {
			t.Errorf("payload %s = %s, want %s", key, payload[key], want)
		}
	}
	canonical, _ := filepath.EvalSymlinks(project)
	if strings.Contains(payloadJSON, canonical) || strings.Contains(payloadJSON, project) || strings.Contains(payloadJSON, ".env") || strings.Contains(payloadJSON, "leak-me") {
		t.Fatalf("payload leaks a path or a sensitive file: %s", payloadJSON)
	}
	assertNoContributionRequestsLeft(t)

	// Without workspace.read the provider is never called.
	manager, record = scmCommitMessageManager(t)
	request.PluginID, request.RequestID = record.id, "request-scm-0003"
	if _, err := manager.scmCommitMessage(context.Background(), request); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("no capability err = %v", err)
	}
	request.ProviderID = "other"
	if _, err := manager.scmCommitMessage(context.Background(), request); contributionErrorCode(err) != contributionErrNotFound {
		t.Fatalf("unknown provider err = %v", err)
	}

	for _, args := range []string{
		`{"pluginId":"runtime.test","providerId":"conventional","requestId":"request-scm-0004","projectRoot":"/tmp","repoPath":"/tmp","draft":"wip"}`,
		`{"pluginId":"runtime.test","providerId":"conventional","requestId":"request-scm-0004","projectRoot":"","repoPath":"/tmp"}`,
		`nope`,
	} {
		if _, err := pluginHostScmCommitMessageHandler("", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("handler %s: err = %v", args, err)
		}
	}
}

func TestScmCommitMessageResultValidation(t *testing.T) {
	for raw, want := range map[string]scmCommitMessageResponse{
		`{"message":"feat: x\r\n\r\nbody\u202e"}`:         {Message: "feat: x\n\nbody"},
		`{"message":"  fix: y\u001b[31m red \t\n  "}`:     {Message: "fix: y red"},
		`{"message":"` + strings.Repeat("中", 4097) + `"}`: {Message: strings.Repeat("中", 4096), Truncated: true},
	} {
		got, err := parseScmCommitMessageResult(json.RawMessage(raw))
		if err != nil || got != want {
			t.Fatalf("%.40s: got %+v %v, want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{`{"message":"  "}`, `{"message":"\u200b\u202e"}`, `{"message":3}`, `{}`, `{"message":"x","extra":1}`, `[]`, `{"message":null}`} {
		if _, err := parseScmCommitMessageResult(json.RawMessage(raw)); err == nil {
			t.Fatalf("%s accepted", raw)
		}
	}
}

func TestScmCommitMessagePayloadBudget(t *testing.T) {
	var diff strings.Builder
	for diff.Len() < 20000 {
		diff.WriteString("+line \"quoted\" 中文\n")
	}
	payload := scmCommitMessagePayload{
		Provider: "conventional", Repository: scmRepositoryInfo{Name: "demo"},
		Files:  []git.StagedFile{{Path: "a.txt", Status: "modified"}},
		Diff:   diff.String(),
		Locale: "en",
	}
	sent, encoded, err := fitScmCommitMessagePayload(payload, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 4096 || len(encoded) < 3000 {
		t.Fatalf("encoded = %d bytes", len(encoded))
	}
	var decoded scmCommitMessagePayload
	if err := json.Unmarshal(encoded, &decoded); err != nil || !decoded.Truncated || !strings.HasSuffix(decoded.Diff, "\n") || !strings.HasPrefix(diff.String(), decoded.Diff) {
		t.Fatalf("decoded = truncated %v, %v", decoded.Truncated, err)
	}
	if !sent.Truncated || sent.Diff != decoded.Diff {
		t.Fatalf("returned payload = truncated %v", sent.Truncated)
	}

	// Fits: unchanged and not truncated.
	small := payload
	small.Diff = "+x\n"
	if _, encoded, err := fitScmCommitMessagePayload(small, 4096); err != nil || !strings.Contains(string(encoded), `"diff":"+x\n","truncated":false`) {
		t.Fatalf("small = %s %v", encoded, err)
	}

	// Less than 1 KiB left for the diff once the rest is encoded.
	crowded := payload
	for index := 0; index < 60; index++ {
		crowded.Files = append(crowded.Files, git.StagedFile{Path: strings.Repeat("p", 40), Status: "added"})
	}
	if _, _, err := fitScmCommitMessagePayload(crowded, 4096); contributionErrorCode(err) != contributionErrTooLarge {
		t.Fatalf("crowded err = %v", err)
	}
}

func TestScmCommitMessagePathDeniedVariants(t *testing.T) {
	for _, path := range []string{
		".env", "app/.ENV", ".env.production", "config/.env.local", "secrets.env", "deploy/Prod.ENV",
		"certs/server.pem", "tls/site.KEY", "a/cert.p12", "b.pfx", "release.jks", "app.keystore", "vault.kdbx",
		"terraform.tfstate", "infra/terraform.tfstate.backup",
		"id_rsa", "home/.ssh/id_rsa.pub", "id_dsa", "id_ecdsa_sk", "keys/ID_ED25519",
		".git-credentials", "pkg/.pypirc",
		".envrc", "dev/.ENVRC", "credentials.json", "gcp/Credentials.JSON", "deploy/my-service-account-key.json", "SERVICE-ACCOUNT.json",
		".ssh/known_hosts", "home/.ssh/config", "a/.SSH/b/c.txt", ".kube/config", "home/.Kube/Config", ".docker/config.json",
		"home/.docker/CONFIG.JSON", ".aws/credentials", "home/.aws/sso/cache/token.json", "x/.AWS/config",
		// The shared rules still apply.
		".git/config", "sub/.svn/entries", ".npmrc", "credentials",
	} {
		if !scmCommitMessagePathDenied(path) {
			t.Errorf("%s is not denied", path)
		}
	}
	for _, path := range []string{
		"src/a.go", "environment.md", "env/settings.go", "docs/env.txt", "monkey", "keys.txt", "keyboard.key.md",
		"pem/readme.md", "tfstate.md", "id_rsa_notes/readme.md", "README.md", ".gitignore", "pypirc.md",
		"envrc.md", "credentials.json.md", "service-account.md", "docs/service-account.yaml", "ssh/config", "my.ssh/notes",
		".kube/cache.txt", "kube/config", ".docker/daemon.json", "docker/config.json", "aws/config", ".awsome/readme.md",
	} {
		if scmCommitMessagePathDenied(path) {
			t.Errorf("%s is denied", path)
		}
	}
}

func TestScmCommitMessageRenameIntoAndOutOfSecretsEnv(t *testing.T) {
	requireGit(t)
	for name, move := range map[string][2]string{
		"into secrets.env":   {"notes.md", "secrets.env"},
		"out of secrets.env": {"secrets.env", "docs/plain.md"},
		"into .envrc":        {"notes.md", ".envrc"},
		"out of credentials": {"credentials.json", "docs/plain.md"},
		"into service acct":  {"notes.md", "gcp/ci-service-account.json"},
		"into .ssh":          {"notes.md", "home/.ssh/authorized_keys"},
		"out of .ssh":        {".ssh/config", "docs/plain.md"},
		"into .kube/config":  {"notes.md", ".kube/config"},
		"out of .docker":     {".docker/config.json", "docs/plain.md"},
		"into .aws":          {"notes.md", ".aws/credentials"},
		"out of .aws":        {"home/.aws/config", "docs/plain.md"},
	} {
		t.Run(name, func(t *testing.T) {
			repository := filepath.Join(t.TempDir(), "demo")
			scmInitRepository(t, repository)
			scmWrite(t, filepath.Join(repository, move[0]), "TOKEN=renamed-secret-content\n")
			scmGit(t, repository, "add", ".")
			scmGit(t, repository, "commit", "-q", "-m", "seed")
			scmWrite(t, filepath.Join(repository, move[1]), "TOKEN=renamed-secret-content\n")
			scmGit(t, repository, "rm", "-q", "--cached", move[0])
			scmGit(t, repository, "add", move[1])
			scmWrite(t, filepath.Join(repository, "kept.txt"), "kept\n")
			scmGit(t, repository, "add", "kept.txt")

			changes, err := git.StagedChangesForPlugin(repository, scmCommitMessagePathDenied, scmCommitMessageFilesMax, scmCommitMessageDiffMaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			if len(changes.Files) != 1 || changes.Files[0].Path != "kept.txt" || changes.OmittedFiles != 1 {
				t.Fatalf("changes = %+v", changes)
			}
			for _, leaked := range []string{move[0], move[1], "notes.md", "plain.md", "renamed-secret-content"} {
				if strings.Contains(changes.Diff, leaked) {
					t.Fatalf("diff leaks %q: %s", leaked, changes.Diff)
				}
			}
		})
	}
}

// Reading the repository happens inside the admitted call: with the
// plugin's only slot taken the request is busy before git runs (an empty
// index would otherwise answer "no staged changes"), and a cancelled request
// never reads it.
func TestScmCommitMessageReadsRepositoryInsideTheSlot(t *testing.T) {
	requireGit(t)
	project := filepath.Join(t.TempDir(), "demo")
	scmInitRepository(t, project)
	manager, record := scmCommitMessageManager(t, api.CapabilityWorkspaceRead)
	slot := contributedActionSlotKey(contributedActionCall{PluginID: record.id, Point: scmCommitMessagePointKey})
	contributionRequests.mu.Lock()
	contributionRequests.slots[slot] = scmCommitMessageConcurrency
	contributionRequests.mu.Unlock()
	request := scmCommitMessageRequest{PluginID: record.id, ProviderID: "conventional", RequestID: "request-scm-slot-01", ProjectRoot: project, RepoPath: project}
	_, err := manager.scmCommitMessage(context.Background(), request)
	contributionRequests.mu.Lock()
	delete(contributionRequests.slots, slot)
	contributionRequests.mu.Unlock()
	if contributionErrorCode(err) != contributionErrBusy {
		t.Fatalf("busy slot err = %v", err)
	}
	request.RequestID = "request-scm-slot-02"
	if _, err := manager.scmCommitMessage(context.Background(), request); contributionErrorCode(err) != contributionErrInvalidRequest || !strings.Contains(err.Error(), "no staged changes") {
		t.Fatalf("free slot err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request.RequestID = "request-scm-slot-03"
	if _, err := manager.scmCommitMessage(ctx, request); contributionErrorCode(err) != contributionErrCancelled {
		t.Fatalf("cancelled err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
}
