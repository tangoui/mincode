package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/backend/git"
	"mincode/app/plugins/api"
)

// plugin_host_scm_commit_message calls a commit message provider when the
// user clicks its button in the Git commit box. The WebView names the
// provider, the open project and the repository; the host checks that the
// repository is a work tree root inside the project, reads the staged
// changes itself (sensitive and VCS paths left out with their names, no
// repository-configured command run) and sends
//
//	{provider, repository: {name}, branch?, files, omittedFiles, diff, truncated, filesTruncated, locale}
//
// never the repository's absolute path, remotes, authors or the user's
// draft. Bounds: payload ≤ min(action maxPayloadBytes, payloadMaxBytes) with
// the diff cut to what fits (less than 1 KiB left is too_large), timeoutMs,
// concurrentPerPlugin, result ≤ resultMaxBytes; the answer is one plain-text
// message of at most messageMaxRunes. Diffs and messages are never logged.
var (
	scmCommitMessageFilesMax        = int(scmCommitMessageSpec.Limit("filesMax"))
	scmCommitMessagePathMaxBytes    = int(scmCommitMessageSpec.Limit("pathMaxBytes"))
	scmCommitMessageDiffMaxBytes    = int(scmCommitMessageSpec.Limit("diffMaxBytes"))
	scmCommitMessagePayloadMaxBytes = int(scmCommitMessageSpec.Limit("payloadMaxBytes"))
	scmCommitMessageResultMaxBytes  = int(scmCommitMessageSpec.Limit("resultMaxBytes"))
	scmCommitMessageMessageMaxRunes = int(scmCommitMessageSpec.Limit("messageMaxRunes"))
	scmCommitMessageTimeout         = time.Duration(scmCommitMessageSpec.Limit("timeoutMs")) * time.Millisecond
	scmCommitMessageConcurrency     = int(scmCommitMessageSpec.Limit("concurrentPerPlugin"))
)

// scmCommitMessageDiffMinBytes is the least room the diff must have in the
// payload; less is too_large rather than a useless empty diff.
const scmCommitMessageDiffMinBytes = 1024

type scmCommitMessageRequest struct {
	PluginID    string `json:"pluginId"`
	ProviderID  string `json:"providerId"`
	RequestID   string `json:"requestId"`
	ProjectRoot string `json:"projectRoot"`
	RepoPath    string `json:"repoPath"`
	Locale      string `json:"locale,omitempty"`
}

type scmRepositoryInfo struct {
	Name string `json:"name"`
}

type scmCommitMessagePayload struct {
	Provider       string            `json:"provider"`
	Repository     scmRepositoryInfo `json:"repository"`
	Branch         string            `json:"branch,omitempty"`
	Files          []git.StagedFile  `json:"files"`
	OmittedFiles   int               `json:"omittedFiles"`
	Diff           string            `json:"diff"`
	Truncated      bool              `json:"truncated"`
	FilesTruncated bool              `json:"filesTruncated"`
	Locale         string            `json:"locale"`
}

type scmCommitMessageResponse struct {
	Message       string `json:"message"`
	Truncated     bool   `json:"truncated"`
	DiffTruncated bool   `json:"diffTruncated"`
	OmittedFiles  int    `json:"omittedFiles"`
}

func (manager *pluginManager) scmCommitMessage(ctx context.Context, request scmCommitMessageRequest) (scmCommitMessageResponse, error) {
	if err := normalizeScmCommitMessageRequest(&request); err != nil {
		return scmCommitMessageResponse{}, err
	}
	// The provider is checked before the filesystem is touched, so an
	// unknown plugin learns nothing about paths.
	provider, actionLimit, err := manager.lookupScmCommitMessageProvider(request.PluginID, request.ProviderID)
	if err != nil {
		return scmCommitMessageResponse{}, err
	}
	limit := scmCommitMessagePayloadMaxBytes
	if actionLimit > 0 && actionLimit < limit {
		limit = actionLimit
	}
	// Every git process runs inside the admitted call (Prepare): bounded by
	// the plugin's concurrency slot and killed when the request is cancelled.
	var sent scmCommitMessagePayload
	prepare := func(ctx context.Context) ([]byte, error) {
		_, repo, err := canonicalRepoUnderProjectContext(ctx, request.ProjectRoot, request.RepoPath)
		if err != nil {
			return nil, err
		}
		changes, err := git.StagedChangesForPluginContext(ctx, repo, scmCommitMessagePathDenied, scmCommitMessageFilesMax, scmCommitMessageDiffMaxBytes)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, git.ErrNoStagedChanges) {
			return nil, newContributionError(contributionErrInvalidRequest, "no staged changes")
		}
		if err != nil {
			return nil, newContributionError(contributionErrUnavailable, "read staged changes: %v", err)
		}
		payload := scmCommitMessagePayload{
			Provider:       provider.ID,
			Repository:     scmRepositoryInfo{Name: filepath.Base(repo)},
			Files:          changes.Files,
			OmittedFiles:   changes.OmittedFiles,
			Diff:           changes.Diff,
			Truncated:      changes.DiffTruncated,
			FilesTruncated: changes.FilesTruncated,
			Locale:         request.Locale,
		}
		if head, ok := readGitHead(repo); ok && head.Branch != "" && !strings.ContainsFunc(head.Branch, contextRuneRefused) {
			payload.Branch = head.Branch
		}
		payload, encoded, err := fitScmCommitMessagePayload(payload, limit)
		if err != nil {
			return nil, err
		}
		sent = payload
		return encoded, nil
	}
	raw, err := manager.invokeContributedAction(ctx, contributedActionCall{
		PluginID: request.PluginID, Point: scmCommitMessagePointKey, EntryID: provider.ID, ActionID: provider.Action,
		RequestID: request.RequestID, Prepare: prepare, Timeout: scmCommitMessageTimeout,
		MaxResultBytes: scmCommitMessageResultMaxBytes, Concurrency: scmCommitMessageConcurrency,
		Validate: scmCommitMessageUnchanged(provider),
	})
	if err != nil {
		return scmCommitMessageResponse{}, err
	}
	response, err := parseScmCommitMessageResult(raw)
	if err != nil {
		return scmCommitMessageResponse{}, newContributionError(contributionErrInvalidResult, "commit message provider %q: %v", provider.ID, err)
	}
	response.DiffTruncated = sent.Truncated
	response.OmittedFiles = sent.OmittedFiles
	return response, nil
}

// fitScmCommitMessagePayload encodes the payload within limit bytes: the
// rest is encoded with an empty diff first, then the diff is cut (on a line
// boundary) to the room left. Less than scmCommitMessageDiffMinBytes of room
// is too_large. Returns the payload as sent (Truncated set when the diff was
// cut here or before) and its encoding.
func fitScmCommitMessagePayload(payload scmCommitMessagePayload, limit int) (scmCommitMessagePayload, []byte, error) {
	diff := strings.ToValidUTF8(payload.Diff, "�")
	payload.Diff = ""
	base, err := encodeContributionJSON(payload)
	if err != nil {
		return payload, nil, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	room := limit - len(base)
	if room < scmCommitMessageDiffMinBytes {
		return payload, nil, newContributionError(contributionErrTooLarge, "payload without the diff is %d bytes, commit message provider %q allows %d", len(base), payload.Provider, limit)
	}
	// JSON escaping can make the encoded diff longer than the diff: shrink
	// the budget by the overflow until it fits.
	budget := room
	for {
		cut, truncated := git.TruncateStagedDiff(diff, budget)
		payload.Diff = cut
		payload.Truncated = payload.Truncated || truncated
		encoded, err := encodeContributionJSON(payload)
		if err != nil {
			return payload, nil, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
		}
		if len(encoded) <= limit {
			return payload, encoded, nil
		}
		budget = max(0, budget-(len(encoded)-limit))
	}
}

// lookupScmCommitMessageProvider finds the provider in the plugin's current
// generation and returns it with its action's maxPayloadBytes. A plugin
// without workspace.read is never given repository contents.
func (manager *pluginManager) lookupScmCommitMessageProvider(pluginID, providerID string) (ScmCommitMessageContribution, int, error) {
	if _, _, ok := enabledContributionPoint(scmCommitMessagePointKey); !ok {
		return ScmCommitMessageContribution{}, 0, newContributionError(contributionErrUnavailable, "commit message providers are not enabled in this host")
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return ScmCommitMessageContribution{}, 0, newContributionError(contributionErrNotFound, "plugin %q not found", pluginID)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	provider, err := scmCommitMessageProviderLocked(record, providerID)
	if err != nil {
		return ScmCommitMessageContribution{}, 0, err
	}
	if !manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead) {
		return ScmCommitMessageContribution{}, 0, newContributionError(contributionErrForbidden, "commit message provider %q requires capability workspace.read", providerID)
	}
	actionLimit := 0
	if action, ok := contributedActionLocked(record, provider.Action); ok {
		actionLimit = action.MaxPayloadBytes
	}
	return provider, actionLimit, nil
}

// scmCommitMessageUnchanged is the Validate callback of a provider call: the
// entry must still be contributed, with the same action, by the generation
// that will answer, and that generation must still declare workspace.read.
func scmCommitMessageUnchanged(provider ScmCommitMessageContribution) func(*pluginRecord) error {
	return func(record *pluginRecord) error {
		current, err := scmCommitMessageProviderLocked(record, provider.ID)
		if err != nil {
			return newContributionError(contributionErrUnavailable, "commit message provider %q is no longer contributed", provider.ID)
		}
		if current.Action != provider.Action || !manifestHasCapability(record.manifest, api.CapabilityWorkspaceRead) {
			return newContributionError(contributionErrUnavailable, "commit message provider %q changed", provider.ID)
		}
		return nil
	}
}

// scmCommitMessageProviderLocked finds the provider in the record's current
// generation. Called with record.mu held.
func scmCommitMessageProviderLocked(record *pluginRecord, providerID string) (ScmCommitMessageContribution, error) {
	if record.manifest != nil {
		if resolved := record.manifest.contribution(scmCommitMessagePointKey); resolved != nil {
			for _, entry := range resolved.Entries {
				if provider, ok := entry.(ScmCommitMessageContribution); ok && provider.ID == providerID {
					return provider, nil
				}
			}
		}
	}
	return ScmCommitMessageContribution{}, newContributionError(contributionErrNotFound, "commit message provider %q is not contributed by %q", providerID, record.id)
}

// canonicalRepoUnderProject is canonicalRepoUnderProjectContext without a
// context.
func canonicalRepoUnderProject(projectRoot, repoPath string) (root, repo string, err error) {
	return canonicalRepoUnderProjectContext(context.Background(), projectRoot, repoPath)
}

// canonicalRepoUnderProjectContext checks a WebView-supplied repository
// against the open project: the project root passes canonicalTaskProjectRoot
// (no filesystem root, home or application data folder); the repository
// path is absolute and, with symlinks resolved, the project root or inside
// it; it is the root of its Git work tree; and its git directory and common
// directory are inside the project too. A subdirectory, a plain folder, a
// symlink leading out of the project or a ".git" file pointing outside it
// (separate git dir, worktree of a repository elsewhere) is forbidden. This
// also refuses legitimate linked worktrees of a repository outside the
// project (v1 limitation). Returns both canonical paths.
func canonicalRepoUnderProjectContext(ctx context.Context, projectRoot, repoPath string) (root, repo string, err error) {
	project, err := contributionProjectFor(projectRoot, true)
	if err != nil {
		return "", "", err
	}
	root = project.Root
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" || !filepath.IsAbs(repoPath) || strings.ContainsRune(repoPath, 0) {
		return "", "", newContributionError(contributionErrForbidden, "repoPath must be an absolute path inside the project")
	}
	repo, err = filepath.EvalSymlinks(filepath.Clean(repoPath))
	if err != nil {
		return "", "", newContributionError(contributionErrForbidden, "repoPath is not a folder inside the project")
	}
	if !pathWithin(root, repo) {
		return "", "", newContributionError(contributionErrForbidden, "repoPath is outside the project")
	}
	if info, statErr := os.Stat(repo); statErr != nil || !info.IsDir() {
		return "", "", newContributionError(contributionErrForbidden, "repoPath is not a folder inside the project")
	}
	layout, err := git.RepositoryLayoutContext(ctx, repo)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", "", ctxErr
	}
	if err != nil || layout.TopLevel != repo {
		return "", "", newContributionError(contributionErrForbidden, "repoPath is not the root of a Git work tree")
	}
	if !pathWithin(root, layout.GitDir) || !pathWithin(root, layout.CommonDir) {
		return "", "", newContributionError(contributionErrForbidden, "the repository's git directory is outside the project")
	}
	return root, repo, nil
}

// pathWithin reports whether the canonical path is root or inside it.
func pathWithin(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// scmSensitiveSuffixes, scmSensitivePrefixes and scmSensitiveNames are this
// point's own credential name variants, matched case-insensitively against
// the base name in addition to the shared sensitiveFilenames list.
var (
	scmSensitiveSuffixes = []string{".env", ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", ".tfstate"}
	scmSensitivePrefixes = []string{".env.", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519"}
	scmSensitiveNames    = []string{".env", ".envrc", ".git-credentials", ".pypirc", "credentials.json"}
	// scmSensitiveDirectories are directory components whose whole content
	// is credentials; scmSensitivePaths are files known by directory and name.
	scmSensitiveDirectories = []string{".ssh", ".aws"}
	scmSensitivePaths       = []string{".kube/config", ".docker/config.json"}
)

// scmSensitiveName reports whether a base name looks like a credential
// file: .env, .env.*, *.env, .envrc, keys and keystores (*.pem, *.key,
// *.p12, *.pfx, *.jks, *.keystore, *.kdbx), Terraform state (*.tfstate,
// *.tfstate.*), SSH keys (id_rsa*, id_dsa*, id_ecdsa*, id_ed25519*),
// .git-credentials, .pypirc, credentials.json and *service-account*.json.
func scmSensitiveName(base string) bool {
	name := strings.ToLower(base)
	if slices.Contains(scmSensitiveNames, name) || strings.Contains(name, ".tfstate.") ||
		(strings.Contains(name, "service-account") && strings.HasSuffix(name, ".json")) {
		return true
	}
	for _, suffix := range scmSensitiveSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	for _, prefix := range scmSensitivePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// scmSensitivePath reports whether a slash-separated relative path is under
// a credential directory (.ssh/, .aws/) or is a known credential file of a
// tool directory (.kube/config, .docker/config.json), case-insensitively and
// at any depth.
func scmSensitivePath(relative string) bool {
	lower := strings.ToLower(relative)
	segments := strings.Split(lower, "/")
	for _, segment := range segments[:len(segments)-1] {
		if slices.Contains(scmSensitiveDirectories, segment) {
			return true
		}
	}
	for _, known := range scmSensitivePaths {
		if lower == known || strings.HasSuffix(lower, "/"+known) {
			return true
		}
	}
	return false
}

// scmCommitMessagePathDenied leaves out of the payload, name included, any
// staged path host.workspace.* refuses (VCS directories, credential file
// names), any base name scmSensitiveName or path scmSensitivePath matches,
// and any path the payload cannot carry faithfully (longer than
// pathMaxBytes, invalid UTF-8, control or format characters).
func scmCommitMessagePathDenied(relative string) bool {
	return workspacePathDenied(relative, false) || scmSensitiveName(path.Base(relative)) || scmSensitivePath(relative) || len(relative) > scmCommitMessagePathMaxBytes ||
		!utf8.ValidString(relative) || strings.ContainsFunc(relative, contextRuneRefused)
}

// parseScmCommitMessageResult validates {"message": string}: line ends
// become LF, format characters, ANSI sequences and other controls are
// removed (tabs and newlines kept), surrounding blanks are trimmed and the
// text is cut to messageMaxRunes. An empty message is an error.
func parseScmCommitMessageResult(raw json.RawMessage) (scmCommitMessageResponse, error) {
	var result struct {
		Message *string `json:"message"`
	}
	if err := decodeStrictJSON(raw, &result, true); err != nil {
		return scmCommitMessageResponse{}, fmt.Errorf("result must be {\"message\": string}: %v", err)
	}
	if result.Message == nil {
		return scmCommitMessageResponse{}, errors.New("result must contain message")
	}
	text := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, *result.Message)
	text, _ = sanitizeContributionText(text, len(text))
	text = strings.TrimSpace(text)
	if text == "" {
		return scmCommitMessageResponse{}, errors.New("message is empty")
	}
	message, truncated := sanitizeContributionText(text, scmCommitMessageMessageMaxRunes)
	return scmCommitMessageResponse{Message: message, Truncated: truncated}, nil
}

func normalizeScmCommitMessageRequest(request *scmCommitMessageRequest) error {
	if !pluginIDPattern.MatchString(request.PluginID) {
		return newContributionError(contributionErrInvalidRequest, "pluginId %q is invalid", request.PluginID)
	}
	if !scmCommitMessageIDPattern.MatchString(request.ProviderID) {
		return newContributionError(contributionErrInvalidRequest, "providerId %q is invalid", request.ProviderID)
	}
	if strings.TrimSpace(request.ProjectRoot) == "" || strings.TrimSpace(request.RepoPath) == "" {
		return newContributionError(contributionErrInvalidRequest, "projectRoot and repoPath are required")
	}
	if request.Locale == "" {
		request.Locale = "en"
	}
	if len(request.Locale) > 35 || !searchLocalePattern.MatchString(request.Locale) {
		return newContributionError(contributionErrInvalidRequest, "locale %q is invalid", request.Locale)
	}
	return nil
}

func pluginHostScmCommitMessageHandler(_, args string) (string, error) {
	var request scmCommitMessageRequest
	if err := decodeStrictJSON([]byte(args), &request, true); err != nil {
		return "", newContributionError(contributionErrInvalidRequest, "plugin_host_scm_commit_message: invalid arguments: %v", err)
	}
	response, err := host().scmCommitMessage(context.Background(), request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(response)
}

func init() {
	core.Register("plugin_host_scm_commit_message", pluginHostScmCommitMessageHandler)
}
