// Package plugin owns MinCode's trusted built-in plugins and locally installed
// cross-platform process plugins. All frontend access goes through core.Dispatch.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

func init() {
	core.Register("plugin_host_development_publish_list", pluginHostDevelopmentPublishListHandler)
	core.Register("plugin_host_development_publish_confirm", pluginHostDevelopmentPublishConfirmHandler)
	core.Register("plugin_host_development_publish_reject", pluginHostDevelopmentPublishRejectHandler)
	core.Register("plugin_host_development_publish_cancel", pluginHostDevelopmentPublishCancelHandler)
	core.Register("plugin_host_development_publish_refresh", pluginHostDevelopmentPublishRefreshHandler)
	core.Register("plugin_host_list", pluginHostListHandler)
	core.Register("plugin_host_categories", func(_, _ string) (string, error) { return core.EncodeResult(api.Categories()) })
	core.Register("plugin_host_install_preview", pluginHostInstallPreviewHandler)
	core.Register("plugin_host_commands", pluginHostCommandsHandler)
	core.Register("plugin_host_contexts", pluginHostContextsHandler)
	core.Register("plugin_host_views", pluginHostViewsHandler)
	core.Register("plugin_host_ui_begin", pluginHostUIBeginHandler)
	core.Register("plugin_host_ui_close", pluginHostUICloseHandler)
	core.Register("plugin_host_ui_invoke", pluginHostUIInvokeHandler)
	core.Register("plugin_host_set_enabled", pluginHostSetEnabledHandler)
	core.Register("plugin_host_set_scope", pluginHostSetScopeHandler)
	core.Register("plugin_host_set_active_workspace", pluginHostSetActiveWorkspaceHandler)
	core.Register("plugin_host_invoke", pluginHostInvokeHandler)
	core.Register("plugin_host_reload", pluginHostReloadHandler)
	core.Register("plugin_host_logs", pluginHostLogsHandler)
	core.Register("plugin_host_install_local", pluginHostInstallLocalHandler)
	core.Register("plugin_host_uninstall", pluginHostUninstallHandler)
	core.Register("plugin_host_clear_data", pluginHostClearDataHandler)
	core.Register("plugin_host_development_pairings", pluginHostDevelopmentPairingsHandler)
	core.Register("plugin_host_development_sessions", pluginHostDevelopmentSessionsHandler)
	core.Register("plugin_host_development_approve", pluginHostDevelopmentApproveHandler)
	core.Register("plugin_host_development_reject", pluginHostDevelopmentRejectHandler)
	core.Register("plugin_host_development_disconnect", pluginHostDevelopmentDisconnectHandler)
}

// Startup starts enabled built-ins and explicit background process plugins.
func Startup(ctx context.Context) error { return host().Startup(ctx) }

// StartupInBackground activates startup plugins without blocking plugin commands;
// onDone receives the activation result.
func StartupInBackground(timeout time.Duration, onDone func(error)) error {
	return host().StartupInBackground(timeout, onDone)
}

// Shutdown stops process plugins and invokes built-in lifecycle hooks.
func Shutdown(ctx context.Context) { host().Shutdown(ctx) }

type pluginMeta struct {
	Category    api.Category `json:"category"`
	Tags        []string     `json:"tags"`
	Homepage    string       `json:"homepage,omitempty"`
	Icon        string       `json:"icon,omitempty"`
	Cover       string       `json:"cover,omitempty"`
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Description string       `json:"description"`
	Author      string       `json:"author,omitempty"`
	Enabled     bool         `json:"enabled"`
	Scope       string       `json:"scope"`
	Workspaces  []string     `json:"workspaces,omitempty"`
	// ActiveInWorkspace 表示插件在后端记录的当前活动工作区是否生效。
	ActiveInWorkspace bool             `json:"activeInWorkspace"`
	Source            string           `json:"source"`
	Runtime           string           `json:"runtime"`
	Activation        string           `json:"activation,omitempty"`
	State             string           `json:"state"`
	LastError         string           `json:"lastError,omitempty"`
	Capabilities      []api.Capability `json:"capabilities"`
	APIVersion        int              `json:"apiVersion"`
	// RequiresPaidHost mirrors the cloud marketplace flag recorded at install time.
	RequiresPaidHost bool `json:"requiresPaidHost,omitempty"`
	// FullTrust reports full trust that still holds (fullTrustStatus).
	FullTrust bool `json:"fullTrust,omitempty"`
}

func recordMeta(record *pluginRecord) pluginMeta {
	meta, root, cover := recordMetaLocked(record)
	// Cover I/O runs outside record.mu so catalog listing never blocks lifecycle work.
	meta.Cover = localCoverData(root, cover)
	if meta.Source == pluginSourceLocal {
		meta.RequiresPaidHost = pluginRequiresPaidHost(meta.ID)
	}
	return meta
}

func recordMetaLocked(record *pluginRecord) (meta pluginMeta, root, cover string) {
	record.mu.Lock()
	defer record.mu.Unlock()
	meta = pluginMeta{
		Category: api.CategoryOther, Tags: []string{}, Name: record.id,
		ID: record.id, Enabled: record.enabled, Source: record.source, State: record.state,
		LastError: record.lastError, Capabilities: append([]api.Capability(nil), record.capabilities...),
	}
	if record.manifest != nil {
		meta.Category = record.manifest.Category
		meta.Tags = append([]string{}, record.manifest.Tags...)
		meta.Homepage = record.manifest.Homepage
		meta.Icon = record.manifest.Icon
		root, cover = record.root, record.manifest.Cover
		meta.Name = record.manifest.Name
		meta.Version = record.manifest.Version
		meta.Description = record.manifest.Description
		meta.Author = record.manifest.Author
		meta.Runtime = record.manifest.Runtime.Type
		meta.Activation = record.manifest.Runtime.Activation
		if meta.Activation == "" && !record.manifest.isDeclarative() {
			meta.Activation = pluginActivationOnDemand
		}
		meta.APIVersion = record.manifest.APIVersion
	} else if record.builtin != nil {
		builtinMeta := record.builtin.Meta()
		meta.Name = builtinMeta.Name
		meta.Version = builtinMeta.Version
		meta.Description = builtinMeta.Description
		meta.Runtime = "go"
		meta.APIVersion = api.ManifestAPIVersionV2
	}
	return meta, root, cover
}

func pluginHostListHandler(_, _ string) (string, error) {
	manager := host()
	ctx, cancel := context.WithTimeout(context.Background(), pluginShutdownTimeout)
	defer cancel()
	if err := manager.syncInstalled(ctx); err != nil {
		return "", err
	}
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	metas := make([]pluginMeta, 0, len(records))
	for _, record := range records {
		metas = append(metas, manager.recordMeta(record))
	}
	sort.SliceStable(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	return core.EncodeResult(metas)
}

// recordMeta 在基础元数据上补充工作区作用域信息。
func (manager *pluginManager) recordMeta(record *pluginRecord) pluginMeta {
	meta := recordMeta(record)
	record.mu.Lock()
	scope := record.scope
	if scope == "" {
		scope = pluginScopeGlobal
	}
	meta.Scope = scope
	if scope == pluginScopeWorkspace {
		meta.Workspaces = sortedWorkspaceIDs(record.workspaces)
	}
	meta.ActiveInWorkspace = manager.workspaceActiveLocked(record)
	record.mu.Unlock()
	if meta.Source == pluginSourceLocal {
		// Re-checks the record against the package: a lapse is found (and
		// announced) when the plugin list is read.
		if status, err := manager.fullTrustStatus(meta.ID); err == nil {
			meta.FullTrust = status.Trusted
		}
	}
	return meta
}

type pluginHostCommand struct {
	PluginID       string            `json:"pluginId"`
	PluginName     string            `json:"pluginName"`
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	TitleLocalized map[string]string `json:"titleLocalized,omitempty"`
	Description    string            `json:"description,omitempty"`
	Keywords       []string          `json:"keywords,omitempty"`
	Action         string            `json:"action,omitempty"`
	Payload        string            `json:"payload,omitempty"`
	Risk           string            `json:"risk,omitempty"`
	// EffectiveRisk is the higher of Risk and the risk of the action the
	// command runs (contributionCommandEffectiveRisk): a command without a
	// risk that runs a dangerous action is dangerous. Risk keeps the declared
	// value for existing consumers.
	EffectiveRisk string                 `json:"effectiveRisk,omitempty"`
	Target        *ManifestCommandTarget `json:"target,omitempty"`
	When          string                 `json:"when,omitempty"`
	InputSchema   map[string]any         `json:"inputSchema,omitempty"`
}

var pluginCommandIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func normalizePluginCommand(pluginID, pluginName string, command ManifestCommand) (pluginHostCommand, bool) {
	id := strings.TrimSpace(command.ID)
	title := strings.TrimSpace(command.Title)
	action := strings.TrimSpace(command.Action)
	if !pluginCommandIDPattern.MatchString(pluginID) || !pluginCommandIDPattern.MatchString(id) ||
		title == "" || len(title) > 200 || len(command.Description) > 500 || len(command.Payload) > 4_096 ||
		(action != "" && !pluginCommandIDPattern.MatchString(action)) || (action == "" && command.Target == nil) {
		return pluginHostCommand{}, false
	}
	keywords := make([]string, 0, min(len(command.Keywords), 20))
	for _, keyword := range command.Keywords {
		if len(keywords) >= 20 {
			break
		}
		if keyword = strings.TrimSpace(keyword); keyword != "" && len(keyword) <= 64 {
			keywords = append(keywords, keyword)
		}
	}
	risk, err := normalizeRisk(command.Risk)
	if err != nil {
		return pluginHostCommand{}, false
	}
	when := strings.TrimSpace(command.When)
	if when != "" && validateWhenExpression(when) != nil {
		return pluginHostCommand{}, false
	}
	if command.InputSchema != nil && validateCommandInputSchema(command.InputSchema) != nil {
		return pluginHostCommand{}, false
	}
	return pluginHostCommand{
		PluginID: pluginID, PluginName: pluginName, ID: id, Title: title,
		Description: strings.TrimSpace(command.Description), Keywords: keywords,
		TitleLocalized: cloneStringMap(command.TitleLocalized), Action: action, Payload: command.Payload, Risk: risk,
		Target: command.Target, When: when, InputSchema: command.InputSchema,
	}, true
}

func pluginHostCommandsHandler(_, _ string) (string, error) {
	manager := host()
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	commands := make([]pluginHostCommand, 0)
	for _, record := range records {
		record.mu.Lock()
		if !manager.contributionsAvailableLocked(record) {
			record.mu.Unlock()
			continue
		}
		meta := recordMetaUnlocked(record)
		var contributions []ManifestCommand
		_, _, actions := manifestCommandMaps(record.manifest)
		if record.manifest != nil {
			contributions = append(contributions, record.manifest.Contributes.Commands...)
		} else if provider, ok := record.builtin.(api.CommandProvider); ok {
			builtinCommands, commandErr := safePluginCommands(provider)
			for _, command := range builtinCommands {
				contributions = append(contributions, ManifestCommand{
					ID: command.ID, Title: command.Title, Description: command.Description,
					Keywords: command.Keywords, Action: command.Action, Payload: command.Payload, Risk: command.Risk,
				})
			}
			if commandErr != nil {
				record.lastError = commandErr.Error()
				record.state = pluginStateError
				record.appendLogLocked("error", commandErr.Error())
			}
		}
		record.mu.Unlock()
		commands = append(commands, hostCommandsForRecord(meta, contributions, actions)...)
	}
	sort.SliceStable(commands, func(i, j int) bool {
		if commands[i].PluginName != commands[j].PluginName {
			return commands[i].PluginName < commands[j].PluginName
		}
		return commands[i].Title < commands[j].Title
	})
	return core.EncodeResult(commands)
}

// hostCommandsForRecord normalizes one plugin's commands for the frontend
// command table (invalid and duplicate ids skipped) and sets EffectiveRisk
// from the plugin's manifest actions.
func hostCommandsForRecord(meta pluginMeta, contributions []ManifestCommand, actions map[string]ManifestUIAction) []pluginHostCommand {
	commands := make([]pluginHostCommand, 0, len(contributions))
	seen := map[string]struct{}{}
	for _, command := range contributions {
		normalized, valid := normalizePluginCommand(meta.ID, meta.Name, command)
		if !valid {
			continue
		}
		if _, duplicate := seen[normalized.ID]; duplicate {
			continue
		}
		seen[normalized.ID] = struct{}{}
		normalized.EffectiveRisk = contributionCommandEffectiveRisk(ManifestCommand{Risk: normalized.Risk, Action: normalized.Action}, actions)
		commands = append(commands, normalized)
	}
	return commands
}

func pluginHostContextsHandler(_, _ string) (string, error) {
	return core.EncodeResult(host().Contexts())
}

func safePluginCommands(provider api.CommandProvider) (commands []api.Command, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("plugin command provider panic: %v", recovered)
		}
	}()
	return provider.Commands(), nil
}

func recordMetaUnlocked(record *pluginRecord) pluginMeta {
	meta := pluginMeta{ID: record.id}
	if record.manifest != nil {
		meta.Name = record.manifest.Name
	} else if record.builtin != nil {
		meta.Name = record.builtin.Meta().Name
	}
	return meta
}

func pluginHostViewsHandler(_, _ string) (string, error) {
	return core.EncodeResult(host().Views())
}

type hostUIBeginArgs struct {
	PluginID     string `json:"pluginId"`
	ViewID       string `json:"viewId"`
	ParentOrigin string `json:"parentOrigin"`
}

func pluginHostUIBeginHandler(_, argsJSON string) (string, error) {
	var args hostUIBeginArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	session, err := host().BeginUI(strings.TrimSpace(args.PluginID), strings.TrimSpace(args.ViewID), args.ParentOrigin)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(session)
}

type hostUICloseArgs struct {
	SessionID string `json:"sessionId"`
}

func pluginHostUICloseHandler(_, argsJSON string) (string, error) {
	var args hostUICloseArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	host().CloseUI(args.SessionID)
	return core.EncodeResult(nil)
}

type hostUIInvokeArgs struct {
	SessionID string `json:"sessionId"`
	Nonce     string `json:"nonce"`
	Action    string `json:"action"`
	Payload   string `json:"payload"`
}

func pluginHostUIInvokeHandler(_, argsJSON string) (string, error) {
	var args hostUIInvokeArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.SessionID) == "" || strings.TrimSpace(args.Nonce) == "" || strings.TrimSpace(args.Action) == "" {
		return "", errors.New("plugin_host_ui_invoke requires sessionId, nonce, and action")
	}
	result, err := host().InvokeUI(context.Background(), args.SessionID, args.Nonce, args.Action, args.Payload)
	if err != nil {
		var rpcErr *processRPCError
		if errors.As(err, &rpcErr) {
			return core.EncodeResult(map[string]any{
				"__mincodePluginError": true,
				"code":                 rpcErr.Code,
				"message":              rpcErr.Message,
				"data":                 rpcErr.Data,
			})
		}
		return "", err
	}
	// InvokeUI always ends in processRuntime.InvokeGeneration, whose result is
	// a slice of the JSON-RPC reply that already passed json.Unmarshal with its
	// envelope (pluginInvokeResultJSON). Pass those bytes through unchanged
	// instead of validating and re-encoding them again; they remain untrusted
	// data for the frontend, which only JSON.parses them.
	return result, nil
}

type hostSetEnabledArgs struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

func pluginHostSetEnabledHandler(_, argsJSON string) (string, error) {
	var args hostSetEnabledArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := host().SetEnabled(ctx, args.ID, args.Enabled); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type hostSetScopeArgs struct {
	ID         string   `json:"id"`
	Scope      string   `json:"scope"`
	Workspaces []string `json:"workspaces"`
}

func pluginHostSetScopeHandler(_, argsJSON string) (string, error) {
	var args hostSetScopeArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := host().SetScope(ctx, args.ID, args.Scope, args.Workspaces); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func pluginHostSetActiveWorkspaceHandler(_, _ string) (string, error) {
	return "", legacyWorkspaceScopeError()
}

type hostInvokeArgs struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Payload string `json:"payload"`
}

func pluginHostInvokeHandler(_, argsJSON string) (string, error) {
	var args hostInvokeArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.ID) == "" || strings.TrimSpace(args.Action) == "" {
		return "", errors.New("plugin_host_invoke requires id and action")
	}
	result, err := host().Invoke(context.Background(), args.ID, strings.TrimSpace(args.Action), args.Payload)
	if err != nil {
		return "", err
	}
	// Built-in plugins can return arbitrary strings, so keep the syntax check
	// here, but return the validated bytes as they are: re-encoding them through
	// EncodeResult would only compact them again.
	if !json.Valid([]byte(result)) {
		return "", fmt.Errorf("plugin %q returned invalid JSON", args.ID)
	}
	return result, nil
}

type hostIDArgs struct {
	ID string `json:"id"`
}

func pluginHostReloadHandler(_, argsJSON string) (string, error) {
	var args hostIDArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := host().Reload(ctx, args.ID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func pluginHostLogsHandler(_, argsJSON string) (string, error) {
	var args hostIDArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	logs, err := host().Logs(args.ID)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(logs)
}

type hostInstallLocalArgs struct {
	SourcePath string `json:"sourcePath"`
}

func pluginHostInstallLocalHandler(_, argsJSON string) (string, error) {
	var args hostInstallLocalArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.SourcePath) == "" {
		return "", errors.New("plugin_host_install_local requires sourcePath")
	}
	if err := requirePluginInstallSource(args.SourcePath); err != nil {
		return "", err
	}
	manifest, err := host().InstallLocal(args.SourcePath)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(manifest)
}

type pluginHostUninstallArgs struct {
	ID        string `json:"id"`
	PurgeData bool   `json:"purgeData"`
}

func pluginHostUninstallHandler(_, argsJSON string) (string, error) {
	var args pluginHostUninstallArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := host().Uninstall(ctx, args.ID, args.PurgeData); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func pluginHostClearDataHandler(_, argsJSON string) (string, error) {
	var args hostIDArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := host().ClearPluginData(ctx, args.ID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type pluginHostDevelopmentIDArgs struct {
	ID string `json:"id"`
}

func pluginHostDevelopmentPairingsHandler(_, _ string) (string, error) {
	return core.EncodeResult(host().devBridge.Pending())
}

func pluginHostDevelopmentSessionsHandler(_, _ string) (string, error) {
	return core.EncodeResult(host().devBridge.Sessions())
}

func pluginHostDevelopmentApproveHandler(_, argsJSON string) (string, error) {
	var args pluginHostDevelopmentIDArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := host().devBridge.Approve(args.ID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func pluginHostDevelopmentRejectHandler(_, argsJSON string) (string, error) {
	var args pluginHostDevelopmentIDArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := host().devBridge.Reject(args.ID); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func pluginHostDevelopmentDisconnectHandler(_, argsJSON string) (string, error) {
	var args pluginHostDevelopmentIDArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	host().devBridge.disconnect(args.ID)
	return core.EncodeResult(nil)
}
