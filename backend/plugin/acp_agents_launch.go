package plugin

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"mincode/app/backend/acp"
	"mincode/app/backend/core"
)

// Plugin ACP agent launch path, a thin caller of the generic process launch
// framework (plugin_process_launch.go) and of the acp gateway.
//
// The WebView names an agent only by (pluginId, agentId, projectRoot). The
// host looks the entry up in its own snapshot, re-validates it with the
// current contract lists and resolves it. The fingerprint (v2) covers argv,
// env, the ${pluginRoot} executable, every ${pluginRoot} file an argument
// names and the unresolved entry (acpAgentSpecFingerprint, which includes the
// platform list); ${projectRoot} stays literal, so one confirmation covers
// every project. Confirmations are stored per (pluginId, agentId) in the
// shared trust store and are required in every approval mode.
//
// acp_launch_plugin calls the resolver registered here
// (acp.SetPluginAgentResolver): it re-resolves the launch at every start
// and returns it only when exactly that launch is confirmed, then the acp
// package starts it with launchAgent in the conversation's project root. The
// agent inherits the user's full environment like the built-in agents.
//
// A plugin's agents are disconnected when it is disabled, uninstalled,
// reloaded, upgraded or fails (stopPluginAcpAgents); switching workspaces
// does not disconnect running agents.

var acpAgentDisconnectOwner = acp.DisconnectPluginAgents

// AcpAgentLaunch is the resolved launch shown in the confirmation dialog.
// Cwd is shown as ${projectRoot}: the agent always runs there.
type AcpAgentLaunch struct {
	ResolvedProcessLaunch
	AgentID              string            `json:"agentId"`
	Description          string            `json:"description,omitempty"`
	DescriptionLocalized map[string]string `json:"descriptionLocalized,omitempty"`
	ProjectRoot          string            `json:"projectRoot"`
}

type acpAgentRequest struct {
	PluginID    string `json:"pluginId"`
	AgentID     string `json:"agentId"`
	ProjectRoot string `json:"projectRoot"`
	Fingerprint string `json:"fingerprint"`
}

func acpAgentEntry(src processLaunchRecord, agentID string) (AcpAgentContribution, error) {
	for _, entry := range src.entries {
		agent, ok := entry.(AcpAgentContribution)
		if !ok || agent.ID != agentID {
			continue
		}
		if !slices.Contains(agent.Platforms, terminalProfileGOOS) {
			return AcpAgentContribution{}, processLaunchError(processLaunchErrUnavailable, "ACP agent %q does not support %s", agentID, terminalProfileGOOS)
		}
		return agent, nil
	}
	return AcpAgentContribution{}, processLaunchError(processLaunchErrUnavailable, "plugin %q no longer contributes ACP agent %q", src.pluginID, agentID)
}

func (manager *pluginManager) resolveAcpAgentLaunch(pluginID, agentID, projectRoot string) (AcpAgentLaunch, error) {
	agentID = strings.TrimSpace(agentID)
	if strings.TrimSpace(pluginID) == "" || agentID == "" {
		return AcpAgentLaunch{}, processLaunchError(processLaunchErrNotFound, "pluginId and agentId are required")
	}
	src, err := manager.processLaunchSource(pluginID, acpAgentPointKey, "ACP agents")
	if err != nil {
		return AcpAgentLaunch{}, err
	}
	agent, err := acpAgentEntry(src, agentID)
	if err != nil {
		return AcpAgentLaunch{}, err
	}
	root, err := canonicalProjectLaunchRoot(projectRoot, "ACP agents")
	if err != nil {
		return AcpAgentLaunch{}, err
	}
	// Re-validate with the current contract lists (a contract update applies
	// to already installed packages).
	definition, err := validateProjectProcessLaunch(acpAgentSpec, src.root, agent.Command, agent.Args, agent.Env)
	if err != nil {
		return AcpAgentLaunch{}, processLaunchError(processLaunchErrForbidden, "ACP agent %q %v", agentID, err)
	}
	pluginRoot, err := processLaunchPluginRoot(src.root)
	if err != nil {
		return AcpAgentLaunch{}, err
	}
	extra, _ := json.Marshal(struct {
		Spec string `json:"spec"`
	}{acpAgentSpecFingerprint(agent)})
	resolveSrc := src
	resolveSrc.entries = nil
	launch, err := resolveProcessLaunch(resolveSrc,
		processLaunchSpec{Command: definition.Command, Args: definition.Args, Env: definition.Env, Cwd: processLaunchVarPluginRoot},
		processLaunchPolicy{
			Point: acpAgentPointKey, EntryKey: agentID,
			Variables:       map[string]string{processLaunchVarPluginRoot: pluginRoot, languageServerVarProject: root},
			Deferred:        []string{languageServerVarProject},
			HashPluginFiles: true,
			Extra:           extra,
		})
	if err != nil {
		return AcpAgentLaunch{}, err
	}
	launch.Title, launch.TitleLocalized = agent.Title, agent.TitleLocalized
	launch.Cwd = languageServerVarProject
	return AcpAgentLaunch{
		ResolvedProcessLaunch: launch,
		AgentID:               agentID,
		Description:           agent.Description,
		DescriptionLocalized:  agent.DescriptionLocalized,
		ProjectRoot:           root,
	}, nil
}

func (manager *pluginManager) prepareAcpAgent(request acpAgentRequest) (AcpAgentLaunch, error) {
	return manager.resolveAcpAgentLaunch(request.PluginID, request.AgentID, request.ProjectRoot)
}

func (manager *pluginManager) trustAcpAgent(request acpAgentRequest) (AcpAgentLaunch, error) {
	launch, err := manager.resolveAcpAgentLaunch(request.PluginID, request.AgentID, request.ProjectRoot)
	if err != nil {
		return AcpAgentLaunch{}, err
	}
	trusted, err := trustProcessLaunch(launch.ResolvedProcessLaunch, request.Fingerprint, fmt.Sprintf("ACP agent %q", launch.AgentID))
	if err != nil {
		return AcpAgentLaunch{}, err
	}
	launch.ResolvedProcessLaunch = trusted
	return launch, nil
}

// resolveTrustedAcpAgent is the acp.PluginAgentResolver: it resolves the
// launch afresh and returns it with ${projectRoot} expanded only when the
// user confirmed exactly this launch. Any unconfirmed launch, new or changed,
// is "untrusted" so the WebView shows the confirmation. The returned
// beforeExec (nil when no plugin file is pinned) runs immediately before the
// process is created and refuses a plugin file swapped since this check.
func (manager *pluginManager) resolveTrustedAcpAgent(pluginID, agentID, projectRoot string) (string, []string, map[string]string, func() error, error) {
	launch, err := manager.resolveAcpAgentLaunch(pluginID, agentID, projectRoot)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if !launch.Trusted {
		if launch.PreviouslyTrusted {
			return "", nil, nil, nil, processLaunchError(processLaunchErrUntrusted, "ACP agent %q changed and must be confirmed again", launch.AgentID)
		}
		return "", nil, nil, nil, processLaunchError(processLaunchErrUntrusted, "ACP agent %q has not been confirmed", launch.AgentID)
	}
	// Re-hash the confirmed plugin files without the cache and pin them until
	// the exec.
	beforeExec, err := verifyProcessLaunchFiles(launch.ResolvedProcessLaunch)
	if err != nil {
		return "", nil, nil, nil, err
	}
	project := strings.NewReplacer(languageServerVarProject, launch.ProjectRoot)
	args := make([]string, 0, len(launch.Argv)-1)
	for _, arg := range launch.Argv[1:] {
		args = append(args, project.Replace(arg))
	}
	env := make(map[string]string, len(launch.Env))
	for name, value := range launch.Env {
		env[name] = project.Replace(value)
	}
	return launch.Argv[0], args, env, beforeExec, nil
}

// stopPluginAcpAgents disconnects every ACP agent of pluginID (disable,
// uninstall, reload, upgrade, error).
func stopPluginAcpAgents(pluginID string) {
	acpAgentDisconnectOwner(pluginID)
}

func decodeAcpAgentRequest(args string) (acpAgentRequest, error) {
	var request acpAgentRequest
	err := core.DecodeArgsPayload(args, &request)
	return request, err
}

func pluginHostAcpAgentPrepareHandler(_, args string) (string, error) {
	request, err := decodeAcpAgentRequest(args)
	if err != nil {
		return "", err
	}
	launch, err := host().prepareAcpAgent(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(launch)
}

func pluginHostAcpAgentTrustHandler(_, args string) (string, error) {
	request, err := decodeAcpAgentRequest(args)
	if err != nil {
		return "", err
	}
	launch, err := host().trustAcpAgent(request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(launch)
}

func init() {
	core.Register("plugin_host_acp_agent_prepare", pluginHostAcpAgentPrepareHandler)
	core.Register("plugin_host_acp_agent_trust", pluginHostAcpAgentTrustHandler)
	acp.SetPluginAgentResolver(func(pluginID, agentID, projectRoot string) (string, []string, map[string]string, func() error, error) {
		return host().resolveTrustedAcpAgent(pluginID, agentID, projectRoot)
	})
	registerRecordRemovalHook(func(_ *pluginManager, pluginID string, reason removalReason) {
		if reason == removalReasonUninstall {
			stopPluginAcpAgents(pluginID)
		}
	})
	// Unlike MCP servers and language servers, an agent also disconnects when
	// its plugin fails (the spec ties agents to a working plugin). A plain
	// stop of an on-demand plugin keeps them.
	registerRecordLifecycleHook(func(_ *pluginManager, record *pluginRecord) {
		record.mu.Lock()
		stop := !record.enabled || record.state == pluginStateError
		pluginID := record.id
		record.mu.Unlock()
		if stop {
			stopPluginAcpAgents(pluginID)
		}
	})
}
