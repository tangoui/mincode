package plugin

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/mcp"
)

// Plugin MCP server launch path, a thin caller of the generic process launch
// framework (plugin_process_launch.go) and of mcp.ConnectPlugin.
//
// The WebView names a server only by (pluginId, serverId, projectRoot). The
// host looks the entry up in its own snapshot, re-validates it with the
// current contract lists and resolves it. The fingerprint covers argv, env,
// the executable and every ${pluginRoot} file an argument names;
// ${projectRoot} stays literal, so one confirmation covers every project.
// Confirmation is required in every approval mode and AI tools have no path
// to these commands (the gateway refuses "plugin:" ids from mcp_connect*).
//
// Starting also:
//   - reuses a live session of the same launch in the same project (not
//     counted as a restart); a different project replaces the session;
//   - allows restartsMax starts per restartWindowSeconds per (plugin, server,
//     project); plugin_host_mcp_server_stop clears the count;
//   - pins the plugin files until the exec (BeforeExec) and gives the session
//     a Valid check, so a session whose plugin was disabled, uninstalled,
//     upgraded, reloaded or replaced is closed when next used. Stopping or a
//     crash of the plugin's own process does not stop its MCP servers.

var (
	mcpServerRestartsMax     = int(mcpServerSpec.Limit("restartsMax"))
	mcpServerRestartWindow   = time.Duration(mcpServerSpec.Limit("restartWindowSeconds")) * time.Second
	mcpServerStderrMaxBytes  = int(mcpServerSpec.Limit("stderrMaxBytes"))
	mcpServerRestartMu       sync.Mutex
	mcpServerRestarts        = map[string][]time.Time{}
	mcpServerNow             = time.Now
	mcpServerConnect         = mcp.ConnectPlugin
	mcpServerLive            = mcp.PluginSessionLive
	mcpServerDisconnect      = mcp.DisconnectPlugin
	mcpServerDisconnectOwner = mcp.DisconnectPluginOwner
	mcpServerProcessEnv      = mcp.PluginProcessEnv
)

// McpServerLaunch is the resolved launch shown in the confirmation dialog.
// Cwd is shown as ${projectRoot}, where the server runs, or as ${pluginRoot}
// for a server that does not need a project (ProjectRoot is then empty).
type McpServerLaunch struct {
	ResolvedProcessLaunch
	ServerID             string            `json:"serverId"`
	SessionServerID      string            `json:"sessionServerId"`
	Description          string            `json:"description,omitempty"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized,omitempty"`
	ProjectRoot          string            `json:"projectRoot"`
	RequiresProject      bool              `json:"requiresProject"`
	// workDir is the process working directory: the project, or the plugin root.
	workDir string
}

// McpServerStartResult is the connected session: its gateway id and what it
// offers.
type McpServerStartResult struct {
	SessionServerID string `json:"sessionServerId"`
	Reused          bool   `json:"reused"`
	*mcp.McpConnectResult
}

type mcpServerRequest struct {
	PluginID    string `json:"pluginId"`
	ServerID    string `json:"serverId"`
	ProjectRoot string `json:"projectRoot"`
	Fingerprint string `json:"fingerprint"`
}

func mcpServerEntry(src processLaunchRecord, serverID string) (McpServerContribution, error) {
	for _, entry := range src.entries {
		server, ok := entry.(McpServerContribution)
		if !ok || server.ID != serverID {
			continue
		}
		if !slices.Contains(server.Platforms, terminalProfileGOOS) {
			return McpServerContribution{}, processLaunchError(processLaunchErrUnavailable, "MCP server %q does not support %s", serverID, terminalProfileGOOS)
		}
		return server, nil
	}
	return McpServerContribution{}, processLaunchError(processLaunchErrUnavailable, "plugin %q no longer contributes MCP server %q", src.pluginID, serverID)
}

func (manager *pluginManager) resolveMcpServerLaunch(pluginID, serverID, projectRoot string) (McpServerLaunch, processLaunchRecord, McpServerContribution, error) {
	fail := func(err error) (McpServerLaunch, processLaunchRecord, McpServerContribution, error) {
		return McpServerLaunch{}, processLaunchRecord{}, McpServerContribution{}, err
	}
	serverID = strings.TrimSpace(serverID)
	if strings.TrimSpace(pluginID) == "" || serverID == "" {
		return fail(processLaunchError(processLaunchErrNotFound, "pluginId and serverId are required"))
	}
	src, err := manager.processLaunchSource(pluginID, mcpServerPointKey, "MCP servers")
	if err != nil {
		return fail(err)
	}
	server, err := mcpServerEntry(src, serverID)
	if err != nil {
		return fail(err)
	}
	root := ""
	if server.RequiresProject {
		if root, err = canonicalProjectLaunchRoot(projectRoot, "MCP servers"); err != nil {
			return fail(err)
		}
	} else if strings.TrimSpace(projectRoot) != "" {
		return fail(processLaunchError(processLaunchErrForbidden, "MCP server %q does not use a project; start it without projectRoot", serverID))
	}
	// Re-validate with the current contract lists (a contract update applies
	// to already installed packages).
	definition, err := validateProjectProcessLaunch(mcpServerSpec, src.root, server.Command, server.Args, server.Env)
	if err != nil {
		return fail(processLaunchError(processLaunchErrForbidden, "MCP server %q %v", serverID, err))
	}
	pluginRoot, err := processLaunchPluginRoot(src.root)
	if err != nil {
		return fail(err)
	}
	// NoProject is only present for project-less servers, so confirmations of
	// project servers keep their fingerprint; switching the flag needs a new one.
	extra, _ := json.Marshal(struct {
		ServerID  string `json:"serverId"`
		NoProject bool   `json:"noProject,omitempty"`
	}{serverID, !server.RequiresProject})
	variables := map[string]string{processLaunchVarPluginRoot: pluginRoot}
	var deferred []string
	if server.RequiresProject {
		variables[languageServerVarProject] = root
		deferred = []string{languageServerVarProject}
	}
	resolveSrc := src
	resolveSrc.entries = nil
	launch, err := resolveProcessLaunch(resolveSrc,
		processLaunchSpec{Command: definition.Command, Args: definition.Args, Env: definition.Env, Cwd: processLaunchVarPluginRoot},
		processLaunchPolicy{
			Point: mcpServerPointKey, EntryKey: serverID,
			Variables:       variables,
			Deferred:        deferred,
			HashPluginFiles: true,
			Extra:           extra,
		})
	if err != nil {
		return fail(err)
	}
	launch.Title, launch.TitleLocalized = server.Title, server.TitleLocalized
	workDir := root
	launch.Cwd = languageServerVarProject
	if !server.RequiresProject {
		workDir = pluginRoot
		launch.Cwd = processLaunchVarPluginRoot
	}
	return McpServerLaunch{
		ResolvedProcessLaunch: launch,
		ServerID:              serverID,
		SessionServerID:       mcp.PluginServerID(src.pluginID, serverID),
		Description:           server.Description,
		DescriptionLocalized:  server.DescriptionLocalized,
		ProjectRoot:           root,
		RequiresProject:       server.RequiresProject,
		workDir:               workDir,
	}, src, server, nil
}

func (manager *pluginManager) prepareMcpServer(request mcpServerRequest) (McpServerLaunch, error) {
	launch, _, _, err := manager.resolveMcpServerLaunch(request.PluginID, request.ServerID, request.ProjectRoot)
	return launch, err
}

func (manager *pluginManager) trustMcpServer(request mcpServerRequest) (McpServerLaunch, error) {
	launch, _, _, err := manager.resolveMcpServerLaunch(request.PluginID, request.ServerID, request.ProjectRoot)
	if err != nil {
		return McpServerLaunch{}, err
	}
	trusted, err := trustProcessLaunch(launch.ResolvedProcessLaunch, request.Fingerprint, fmt.Sprintf("MCP server %q", launch.ServerID))
	if err != nil {
		return McpServerLaunch{}, err
	}
	launch.ResolvedProcessLaunch = trusted
	return launch, nil
}

func mcpServerRestartKey(pluginID, serverID, root string) string {
	return pluginID + "\x00" + serverID + "\x00" + root
}

// takeMcpServerStart records one start, or refuses it when the window
// already holds restartsMax starts.
func takeMcpServerStart(key string) error {
	mcpServerRestartMu.Lock()
	defer mcpServerRestartMu.Unlock()
	now := mcpServerNow()
	kept := []time.Time{}
	for _, at := range mcpServerRestarts[key] {
		if now.Sub(at) < mcpServerRestartWindow {
			kept = append(kept, at)
		}
	}
	if len(kept) >= mcpServerRestartsMax {
		mcpServerRestarts[key] = kept
		return processLaunchError(processLaunchErrLimit, "the MCP server was started %d times in %s; stop it to try again", mcpServerRestartsMax, mcpServerRestartWindow)
	}
	mcpServerRestarts[key] = append(kept, now)
	return nil
}

func forgetMcpServerStarts(match func(key string) bool) {
	mcpServerRestartMu.Lock()
	defer mcpServerRestartMu.Unlock()
	for key := range mcpServerRestarts {
		if match(key) {
			delete(mcpServerRestarts, key)
		}
	}
}

func (manager *pluginManager) mcpServerValid(src processLaunchRecord, serverID, specFingerprint string) func() bool {
	return manager.projectLaunchValid(src, mcpServerPointKey, func(entry any) (bool, bool) {
		server, ok := entry.(McpServerContribution)
		if !ok || server.ID != serverID {
			return false, false
		}
		return true, server.SpecFingerprint == specFingerprint
	})
}

// startMcpServer connects a confirmed plugin MCP server, or returns the live
// session of the same launch.
func (manager *pluginManager) startMcpServer(request mcpServerRequest) (McpServerStartResult, error) {
	launch, src, server, err := manager.resolveMcpServerLaunch(request.PluginID, request.ServerID, request.ProjectRoot)
	if err != nil {
		return McpServerStartResult{}, err
	}
	if err := checkProcessLaunchStart(launch.ResolvedProcessLaunch, request.Fingerprint, fmt.Sprintf("MCP server %q", launch.ServerID)); err != nil {
		return McpServerStartResult{}, err
	}
	spec := mcp.PluginStdioSpec{
		ServerID:       launch.SessionServerID,
		OwnerPluginID:  launch.PluginID,
		Executable:     launch.Argv[0],
		Dir:            launch.workDir,
		Fingerprint:    launch.Fingerprint,
		StderrMaxBytes: mcpServerStderrMaxBytes,
		Valid:          manager.mcpServerValid(src, launch.ServerID, server.SpecFingerprint),
	}
	if !mcpServerLive(spec.ServerID, spec.Fingerprint, spec.Dir) {
		if err := takeMcpServerStart(mcpServerRestartKey(launch.PluginID, launch.ServerID, launch.ProjectRoot)); err != nil {
			return McpServerStartResult{}, err
		}
	}
	// Verified even when a live session is expected: if it closes before the
	// connect, the new process still runs only the trusted files.
	beforeExec, err := verifyProcessLaunchFiles(launch.ResolvedProcessLaunch)
	if err != nil {
		return McpServerStartResult{}, err
	}
	spec.BeforeExec = beforeExec
	project := strings.NewReplacer(languageServerVarProject, launch.ProjectRoot)
	spec.Args = make([]string, 0, len(launch.Argv)-1)
	for _, arg := range launch.Argv[1:] {
		spec.Args = append(spec.Args, project.Replace(arg))
	}
	env := make(map[string]string, len(launch.Env))
	for name, value := range launch.Env {
		env[name] = project.Replace(value)
	}
	spec.Env = mcpServerProcessEnv(env)
	result, reused, err := mcpServerConnect(spec)
	if err != nil {
		if processLaunchErrorCode(err) != "" {
			return McpServerStartResult{}, err
		}
		return McpServerStartResult{}, processLaunchError(processLaunchErrUnavailable, "%v", err)
	}
	return McpServerStartResult{SessionServerID: spec.ServerID, Reused: reused, McpConnectResult: result}, nil
}

// stopMcpServer stops the session and clears the restart count. It needs no
// confirmation (it never starts anything).
func (manager *pluginManager) stopMcpServer(request mcpServerRequest) (bool, error) {
	pluginID, serverID := strings.TrimSpace(request.PluginID), strings.TrimSpace(request.ServerID)
	if !pluginIDPattern.MatchString(pluginID) || !languageServerIDPattern.MatchString(serverID) {
		return false, processLaunchError(processLaunchErrNotFound, "pluginId and serverId are required")
	}
	prefix := pluginID + "\x00" + serverID + "\x00"
	forgetMcpServerStarts(func(key string) bool { return strings.HasPrefix(key, prefix) })
	return mcpServerDisconnect(mcp.PluginServerID(pluginID, serverID)), nil
}

// stopPluginMcpServers stops every MCP server of pluginID and forgets its
// restart counts (disable, uninstall, reload, upgrade).
func stopPluginMcpServers(pluginID string) {
	mcpServerDisconnectOwner(pluginID)
	prefix := pluginID + "\x00"
	forgetMcpServerStarts(func(key string) bool { return strings.HasPrefix(key, prefix) })
}

func decodeMcpServerRequest(args string) (mcpServerRequest, error) {
	var request mcpServerRequest
	err := core.DecodeArgsPayload(args, &request)
	return request, err
}

func pluginHostMcpServerPrepareHandler(_, args string) (string, error) {
	request, err := decodeMcpServerRequest(args)
	if err != nil {
		return "", err
	}
	launch, err := host().prepareMcpServer(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(launch)
}

func pluginHostMcpServerTrustHandler(_, args string) (string, error) {
	request, err := decodeMcpServerRequest(args)
	if err != nil {
		return "", err
	}
	launch, err := host().trustMcpServer(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(launch)
}

func pluginHostMcpServerStartHandler(_, args string) (string, error) {
	request, err := decodeMcpServerRequest(args)
	if err != nil {
		return "", err
	}
	result, err := host().startMcpServer(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func pluginHostMcpServerStopHandler(_, args string) (string, error) {
	request, err := decodeMcpServerRequest(args)
	if err != nil {
		return "", err
	}
	stopped, err := host().stopMcpServer(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]bool{"stopped": stopped})
}

func init() {
	core.Register("plugin_host_mcp_server_prepare", pluginHostMcpServerPrepareHandler)
	core.Register("plugin_host_mcp_server_trust", pluginHostMcpServerTrustHandler)
	core.Register("plugin_host_mcp_server_start", pluginHostMcpServerStartHandler)
	core.Register("plugin_host_mcp_server_stop", pluginHostMcpServerStopHandler)
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, reason removalReason) {
		if reason == removalReasonUninstall {
			stopPluginMcpServers(pluginID)
		}
	})
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		record.mu.Lock()
		disabled := !record.enabled
		pluginID := record.id
		record.mu.Unlock()
		if disabled {
			stopPluginMcpServers(pluginID)
		}
	})
}
