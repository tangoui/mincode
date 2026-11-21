package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	projectfs "mincode/app/backend/fs"
	"mincode/app/backend/lsp"
	"mincode/app/plugins/api"
)

// Plugin language server launch path, a thin caller of the generic process
// launch framework (plugin_process_launch.go) and of lsp.StartExternal.
//
// The WebView names a server only by (pluginId, serverId, projectRoot). The
// host looks the entry up in its own snapshot, re-validates it with the
// current contract lists and resolves it. The fingerprint (v2) covers argv,
// env, the executable, every ${pluginRoot} file an argument names, the
// language list and the initializationOptions; ${projectRoot} stays literal,
// so one confirmation covers every project. Confirmation is required in every
// approval mode and AI tools have no path to these commands.
//
// Starting also:
//   - reuses a live session of the same launch (not counted as a restart);
//   - allows restartsMax starts per restartWindowSeconds per (plugin, server,
//     project); plugin_host_language_server_reset (the user's Restart) clears
//     the count and stops the session;
//   - pins the plugin files until the exec (BeforeExec) and gives the session
//     a Valid check, so a session whose plugin was disabled, uninstalled,
//     upgraded, reloaded or replaced by a new development generation stops
//     with a final $/serverStopped. Stopping or a crash of the plugin's own
//     process does not stop its language servers.

var (
	languageServerRestartsMax      = int(languageServerSpec.Limit("restartsMax"))
	languageServerRestartWindow    = time.Duration(languageServerSpec.Limit("restartWindowSeconds")) * time.Second
	languageServerSessionsMax      = int(languageServerSpec.Limit("sessionsPerPlugin"))
	languageServerMessageMaxBytes  = int(languageServerSpec.Limit("messageMaxBytes"))
	languageServerStderrMaxBytes   = int(languageServerSpec.Limit("stderrMaxBytes"))
	languageServerRestartMu        sync.Mutex
	languageServerRestarts         = map[string][]time.Time{}
	languageServerNow              = time.Now
	languageServerStartExternal    = lsp.StartExternal
	languageServerExternalLive     = lsp.ExternalSessionLive
	languageServerStopExternal     = lsp.StopExternal
	languageServerStopForOwnerFunc = lsp.StopExternalForOwner
)

// LanguageServerLaunch is the resolved launch shown in the confirmation
// dialog. Cwd is shown as ${projectRoot}: the server always runs there.
type LanguageServerLaunch struct {
	ResolvedProcessLaunch
	ServerID              string          `json:"serverId"`
	SessionServerID       string          `json:"sessionServerId"`
	Languages             []string        `json:"languages"`
	InitializationOptions json.RawMessage `json:"initializationOptions,omitempty"`
	ProjectRoot           string          `json:"projectRoot"`
}

type languageServerRequest struct {
	PluginID    string `json:"pluginId"`
	ServerID    string `json:"serverId"`
	ProjectRoot string `json:"projectRoot"`
	Fingerprint string `json:"fingerprint"`
}

// canonicalLanguageServerRoot mirrors the task project-root rules.
func canonicalLanguageServerRoot(projectRoot string) (string, error) {
	return canonicalProjectLaunchRoot(projectRoot, "language servers")
}

// canonicalProjectLaunchRoot canonicalizes the project a plugin process runs
// in, refusing the filesystem root, the home folder and the app data folders.
func canonicalProjectLaunchRoot(projectRoot, noun string) (string, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" || !filepath.IsAbs(projectRoot) || strings.ContainsRune(projectRoot, 0) {
		return "", processLaunchError(processLaunchErrForbidden, "projectRoot must be an absolute path")
	}
	root, err := projectfs.CanonicalProjectRoot(projectRoot)
	if err != nil {
		return "", processLaunchError(processLaunchErrNotFound, "project folder %q does not exist", projectRoot)
	}
	if filepath.Dir(root) == root {
		return "", processLaunchError(processLaunchErrForbidden, "%s cannot run in the filesystem root", noun)
	}
	canonical := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return filepath.Clean(path)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" && canonical(home) == root {
		return "", processLaunchError(processLaunchErrForbidden, "%s cannot run in the user home folder", noun)
	}
	for _, protected := range []string{userDataDir(), pluginsDirectory()} {
		if strings.TrimSpace(protected) == "" {
			continue
		}
		base := canonical(protected)
		if root == base || strings.HasPrefix(root, base+string(filepath.Separator)) {
			return "", processLaunchError(processLaunchErrForbidden, "%s cannot run inside the application data folder", noun)
		}
	}
	return root, nil
}

func languageServerEntry(src processLaunchRecord, serverID string) (LanguageServerContribution, error) {
	for _, entry := range src.entries {
		server, ok := entry.(LanguageServerContribution)
		if !ok || server.ID != serverID {
			continue
		}
		if !slices.Contains(server.Platforms, terminalProfileGOOS) {
			return LanguageServerContribution{}, processLaunchError(processLaunchErrUnavailable, "language server %q does not support %s", serverID, terminalProfileGOOS)
		}
		return server, nil
	}
	return LanguageServerContribution{}, processLaunchError(processLaunchErrUnavailable, "plugin %q no longer contributes language server %q", src.pluginID, serverID)
}

func (manager *pluginManager) resolveLanguageServerLaunch(pluginID, serverID, projectRoot string) (LanguageServerLaunch, processLaunchRecord, LanguageServerContribution, error) {
	fail := func(err error) (LanguageServerLaunch, processLaunchRecord, LanguageServerContribution, error) {
		return LanguageServerLaunch{}, processLaunchRecord{}, LanguageServerContribution{}, err
	}
	serverID = strings.TrimSpace(serverID)
	if strings.TrimSpace(pluginID) == "" || serverID == "" {
		return fail(processLaunchError(processLaunchErrNotFound, "pluginId and serverId are required"))
	}
	root, err := canonicalLanguageServerRoot(projectRoot)
	if err != nil {
		return fail(err)
	}
	src, err := manager.processLaunchSource(pluginID, languageServerPointKey, "language servers")
	if err != nil {
		return fail(err)
	}
	server, err := languageServerEntry(src, serverID)
	if err != nil {
		return fail(err)
	}
	// Re-validate with the current contract lists (a contract update applies
	// to already installed packages).
	definition, err := validateLanguageServerLaunch(src.root, server.Command, server.Args, server.Env)
	if err != nil {
		return fail(processLaunchError(processLaunchErrForbidden, "language server %q %v", serverID, err))
	}
	pluginRoot, err := processLaunchPluginRoot(src.root)
	if err != nil {
		return fail(err)
	}
	extra, _ := json.Marshal(struct {
		ServerID              string          `json:"serverId"`
		Languages             []string        `json:"languages"`
		InitializationOptions json.RawMessage `json:"initializationOptions,omitempty"`
	}{serverID, server.Languages, server.InitializationOptions})
	resolveSrc := src
	resolveSrc.entries = nil
	launch, err := resolveProcessLaunch(resolveSrc,
		processLaunchSpec{Command: definition.Command, Args: definition.Args, Env: definition.Env, Cwd: processLaunchVarPluginRoot},
		processLaunchPolicy{
			Point: languageServerPointKey, EntryKey: serverID,
			Variables: map[string]string{
				processLaunchVarPluginRoot: pluginRoot,
				languageServerVarProject:   root,
			},
			Deferred:        []string{languageServerVarProject},
			HashPluginFiles: true,
			Extra:           extra,
		})
	if err != nil {
		return fail(err)
	}
	launch.Title, launch.TitleLocalized = server.Title, server.TitleLocalized
	launch.Cwd = languageServerVarProject
	return LanguageServerLaunch{
		ResolvedProcessLaunch: launch,
		ServerID:              serverID,
		SessionServerID:       lsp.ExternalServerID(src.pluginID, serverID),
		Languages:             slices.Clone(server.Languages),
		InitializationOptions: server.InitializationOptions,
		ProjectRoot:           root,
	}, src, server, nil
}

func (manager *pluginManager) prepareLanguageServer(request languageServerRequest) (LanguageServerLaunch, error) {
	launch, _, _, err := manager.resolveLanguageServerLaunch(request.PluginID, request.ServerID, request.ProjectRoot)
	return launch, err
}

func (manager *pluginManager) trustLanguageServer(request languageServerRequest) (LanguageServerLaunch, error) {
	launch, _, _, err := manager.resolveLanguageServerLaunch(request.PluginID, request.ServerID, request.ProjectRoot)
	if err != nil {
		return LanguageServerLaunch{}, err
	}
	trusted, err := trustProcessLaunch(launch.ResolvedProcessLaunch, request.Fingerprint, fmt.Sprintf("language server %q", launch.ServerID))
	if err != nil {
		return LanguageServerLaunch{}, err
	}
	launch.ResolvedProcessLaunch = trusted
	return launch, nil
}

func languageServerRestartKey(pluginID, serverID, root string) string {
	return pluginID + "\x00" + serverID + "\x00" + root
}

// takeLanguageServerStart records one start, or refuses it when the window
// already holds restartsMax starts.
func takeLanguageServerStart(key string) error {
	languageServerRestartMu.Lock()
	defer languageServerRestartMu.Unlock()
	now := languageServerNow()
	kept := []time.Time{}
	for _, at := range languageServerRestarts[key] {
		if now.Sub(at) < languageServerRestartWindow {
			kept = append(kept, at)
		}
	}
	if len(kept) >= languageServerRestartsMax {
		languageServerRestarts[key] = kept
		return processLaunchError(processLaunchErrLimit, "the language server was started %d times in %s; use Restart to try again", languageServerRestartsMax, languageServerRestartWindow)
	}
	languageServerRestarts[key] = append(kept, now)
	return nil
}

func forgetLanguageServerStarts(match func(key string) bool) {
	languageServerRestartMu.Lock()
	defer languageServerRestartMu.Unlock()
	for key := range languageServerRestarts {
		if match(key) {
			delete(languageServerRestarts, key)
		}
	}
}

// languageServerValid builds the Valid check of a started session.
func (manager *pluginManager) languageServerValid(src processLaunchRecord, serverID, specFingerprint string) func() bool {
	return manager.projectLaunchValid(src, languageServerPointKey, func(entry any) (bool, bool) {
		server, ok := entry.(LanguageServerContribution)
		if !ok || server.ID != serverID {
			return false, false
		}
		return true, server.SpecFingerprint == specFingerprint
	})
}

// projectLaunchValid builds the Valid check of a started plugin session: the
// point must be enabled and the plugin still enabled, active, granted shell,
// at the same root, version and development generation, and offer the same
// entry (match reports found, same). The plugin process state (running,
// stopped, crashed) is deliberately ignored.
func (manager *pluginManager) projectLaunchValid(src processLaunchRecord, point string, match func(entry any) (bool, bool)) func() bool {
	return func() bool {
		if _, _, ok := enabledContributionPoint(point); !ok {
			return false
		}
		record, err := manager.record(src.pluginID)
		if err != nil {
			return false
		}
		record.mu.Lock()
		defer record.mu.Unlock()
		if record.manifest == nil || record.builtin != nil || record.manifest.isDeclarative() || !record.enabled ||
			!manager.workspaceActiveLocked(record) || !slices.Contains(record.capabilities, api.CapabilityShell) ||
			record.root != src.root || record.manifest.Version != src.pluginVersion {
			return false
		}
		resolved := record.manifest.contributions[point]
		if resolved == nil {
			return false
		}
		for _, entry := range resolved.Entries {
			if found, same := match(entry); found {
				return same
			}
		}
		return false
	}
}

// startLanguageServer starts a confirmed plugin language server, or returns
// the live session of the same launch.
func (manager *pluginManager) startLanguageServer(request languageServerRequest) (lsp.ServerStatus, error) {
	launch, src, server, err := manager.resolveLanguageServerLaunch(request.PluginID, request.ServerID, request.ProjectRoot)
	if err != nil {
		return lsp.ServerStatus{}, err
	}
	if err := checkProcessLaunchStart(launch.ResolvedProcessLaunch, request.Fingerprint, fmt.Sprintf("language server %q", launch.ServerID)); err != nil {
		return lsp.ServerStatus{}, err
	}
	spec := lsp.ExternalServerSpec{
		ServerID: launch.SessionServerID, OwnerPluginID: launch.PluginID,
		Name: launch.Title, Languages: launch.Languages,
		InitializationOptions: launch.InitializationOptions,
		Fingerprint:           launch.Fingerprint,
		Valid:                 manager.languageServerValid(src, launch.ServerID, server.SpecFingerprint),
		MessageMaxBytes:       languageServerMessageMaxBytes,
		StderrMaxBytes:        languageServerStderrMaxBytes,
		MaxSessionsPerOwner:   languageServerSessionsMax,
	}
	if languageServerExternalLive(launch.ProjectRoot, spec.ServerID, spec.Fingerprint) {
		// The same launch is running: hand back its status (not a restart).
		spec.Executable = launch.Argv[0]
		return languageServerStartExternal(launch.ProjectRoot, spec)
	}
	if err := takeLanguageServerStart(languageServerRestartKey(launch.PluginID, launch.ServerID, launch.ProjectRoot)); err != nil {
		return lsp.ServerStatus{}, err
	}
	beforeExec, err := verifyProcessLaunchFiles(launch.ResolvedProcessLaunch)
	if err != nil {
		return lsp.ServerStatus{}, err
	}
	project := strings.NewReplacer(languageServerVarProject, launch.ProjectRoot)
	spec.Executable = launch.Argv[0]
	spec.Args = make([]string, 0, len(launch.Argv)-1)
	for _, arg := range launch.Argv[1:] {
		spec.Args = append(spec.Args, project.Replace(arg))
	}
	spec.Env = make(map[string]string, len(launch.Env))
	for name, value := range launch.Env {
		spec.Env[name] = project.Replace(value)
	}
	spec.BeforeExec = beforeExec
	status, err := languageServerStartExternal(launch.ProjectRoot, spec)
	if err != nil {
		if processLaunchErrorCode(err) != "" {
			return lsp.ServerStatus{}, err
		}
		return lsp.ServerStatus{}, processLaunchError(processLaunchErrUnavailable, "%v", err)
	}
	return status, nil
}

// resetLanguageServer is the user's Restart: it stops the session and clears
// the restart count. It needs no confirmation (it never starts anything).
func (manager *pluginManager) resetLanguageServer(request languageServerRequest) (bool, error) {
	pluginID, serverID := strings.TrimSpace(request.PluginID), strings.TrimSpace(request.ServerID)
	if !pluginIDPattern.MatchString(pluginID) || !languageServerIDPattern.MatchString(serverID) {
		return false, processLaunchError(processLaunchErrNotFound, "pluginId and serverId are required")
	}
	root, err := canonicalLanguageServerRoot(request.ProjectRoot)
	if err != nil {
		return false, err
	}
	key := languageServerRestartKey(pluginID, serverID, root)
	forgetLanguageServerStarts(func(candidate string) bool { return candidate == key })
	return languageServerStopExternal(root, lsp.ExternalServerID(pluginID, serverID), "restart"), nil
}

// stopPluginLanguageServers stops every language server of pluginID and
// forgets its restart counts (disable, uninstall, reload, upgrade).
func stopPluginLanguageServers(pluginID, reason string) {
	languageServerStopForOwnerFunc(pluginID, reason)
	prefix := pluginID + "\x00"
	forgetLanguageServerStarts(func(key string) bool { return strings.HasPrefix(key, prefix) })
}

func decodeLanguageServerRequest(args string) (languageServerRequest, error) {
	var request languageServerRequest
	err := core.DecodeArgsPayload(args, &request)
	return request, err
}

func pluginHostLanguageServerPrepareHandler(_, args string) (string, error) {
	request, err := decodeLanguageServerRequest(args)
	if err != nil {
		return "", err
	}
	launch, err := host().prepareLanguageServer(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(launch)
}

func pluginHostLanguageServerTrustHandler(_, args string) (string, error) {
	request, err := decodeLanguageServerRequest(args)
	if err != nil {
		return "", err
	}
	launch, err := host().trustLanguageServer(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(launch)
}

func pluginHostLanguageServerStartHandler(_, args string) (string, error) {
	request, err := decodeLanguageServerRequest(args)
	if err != nil {
		return "", err
	}
	status, err := host().startLanguageServer(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(status)
}

func pluginHostLanguageServerResetHandler(_, args string) (string, error) {
	request, err := decodeLanguageServerRequest(args)
	if err != nil {
		return "", err
	}
	stopped, err := host().resetLanguageServer(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]bool{"stopped": stopped})
}

func init() {
	core.Register("plugin_host_language_server_prepare", pluginHostLanguageServerPrepareHandler)
	core.Register("plugin_host_language_server_trust", pluginHostLanguageServerTrustHandler)
	core.Register("plugin_host_language_server_start", pluginHostLanguageServerStartHandler)
	core.Register("plugin_host_language_server_reset", pluginHostLanguageServerResetHandler)
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, reason removalReason) {
		if reason == removalReasonUninstall {
			stopPluginLanguageServers(pluginID, "uninstall")
		}
	})
	// Disable (and any change that makes the session invalid, such as a new
	// development generation) stops the servers; a stop or crash of the
	// plugin process does not.
	registerRecordLifecycleHook(func(manager *pluginManager, record *pluginRecord) {
		record.mu.Lock()
		disabled := !record.enabled
		pluginID := record.id
		record.mu.Unlock()
		if disabled {
			stopPluginLanguageServers(pluginID, "disabled")
		}
	})
}
