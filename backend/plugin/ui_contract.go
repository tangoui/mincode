package plugin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

const pluginStateChangedEvent = "plugin:state-changed"

// pluginWorkspaceChangedEvent 在后端完成工作区切换 reconcile 后发出，
// 前端据此重新拉取插件命令、视图与列表。
const pluginWorkspaceChangedEvent = "plugin:workspace-changed"

// pluginUIRevokedEvent 在插件的全部 UI 会话被撤销后发出：插件 ID 与被关闭的会话 ID（不含 nonce 或资源令牌）。
const pluginUIRevokedEvent = "plugin:ui-revoked"

type pluginUIRevokedEventData struct {
	PluginID   string   `json:"pluginId"`
	SessionIDs []string `json:"sessionIds"`
}

type pluginStateEvent struct {
	ID                string `json:"id"`
	State             string `json:"state"`
	Enabled           bool   `json:"enabled"`
	ActiveInWorkspace bool   `json:"activeInWorkspace"`
	Generation        uint64 `json:"generation,omitempty"`
}

type pluginViewDTO struct {
	PluginID          string            `json:"pluginId"`
	PluginName        string            `json:"pluginName"`
	ID                string            `json:"id"`
	Title             string            `json:"title"`
	TitleLocalized    map[string]string `json:"titleLocalized,omitempty"`
	Icon              string            `json:"icon,omitempty"`
	Enabled           bool              `json:"enabled"`
	ActiveInWorkspace bool              `json:"activeInWorkspace"`
	State             string            `json:"state"`
	Floating          *ManifestFloating `json:"floating,omitempty"`
}

func (manager *pluginManager) emitState(record *pluginRecord) {
	if record == nil {
		return
	}
	record.mu.Lock()
	event := pluginStateEvent{ID: record.id, State: record.state, Enabled: record.enabled, ActiveInWorkspace: manager.workspaceActiveLocked(record)}
	if record.runtime != nil {
		event.Generation = record.runtime.Generation()
	}
	record.mu.Unlock()
	core.Emit(pluginStateChangedEvent, event)
	// Any state change can add or remove contributions (enable, stop, crash,
	// development generation swap); the frontend re-reads the snapshot.
	manager.emitContributionsChanged(record.id, nil)
}

func (manager *pluginManager) revokePluginUI(pluginID string) {
	if manager != nil && manager.uiHost != nil {
		revoked := manager.uiHost.RevokePlugin(pluginID)
		// Every revocation path (disable, reload, crash/error, stop, uninstall,
		// upgrade/downgrade, Clear Plugin Data, development generation swap)
		// passes here; the frontend destroys warm (closed but parked) view
		// frames of the plugin on this event so none of them is ever reused,
		// and open frames whose session is listed fail closed. Session IDs
		// (never nonces) let a frame ignore a late event about older sessions.
		core.Emit(pluginUIRevokedEvent, pluginUIRevokedEventData{PluginID: pluginID, SessionIDs: revoked})
	}
}

func (manager *pluginManager) Views() []pluginViewDTO {
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	views := make([]pluginViewDTO, 0)
	for _, record := range records {
		record.mu.Lock()
		if record.manifest == nil {
			record.mu.Unlock()
			continue
		}
		for _, view := range record.manifest.Contributes.Views {
			views = append(views, pluginViewDTO{
				PluginID: record.id, PluginName: record.manifest.Name, ID: view.ID,
				Title: view.Title, TitleLocalized: cloneStringMap(view.TitleLocalized), Icon: resolvePluginViewIcon(view.Icon, record.manifest.Icon),
				Enabled: record.enabled, ActiveInWorkspace: manager.workspaceActiveLocked(record), State: record.state,
				Floating: cloneFloating(view.Floating),
			})
		}
		record.mu.Unlock()
	}
	return views
}

func (manager *pluginManager) BeginUI(pluginID, viewID, parentOrigin string) (pluginUISessionDTO, error) {
	if _, err := validatePluginUIParentOrigin(parentOrigin); err != nil {
		return pluginUISessionDTO{}, err
	}
	if manager.uiHost == nil {
		return pluginUISessionDTO{}, errors.New("plugin UI host is unavailable")
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return pluginUISessionDTO{}, err
	}
	// Validate the static view before activation so invalid requests execute no code.
	record.mu.Lock()
	validView := false
	if manager.contributionsAvailableLocked(record) && record.manifest != nil {
		for _, view := range record.manifest.Contributes.Views {
			if view.ID == strings.TrimSpace(viewID) {
				validView = true
				break
			}
		}
	}
	record.mu.Unlock()
	if !validView {
		return pluginUISessionDTO{}, errors.New("plugin view is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), pluginStartupTimeout)
	defer cancel()
	if err := manager.ensureRunning(ctx, pluginID); err != nil {
		return pluginUISessionDTO{}, err
	}
	if err := lockPluginLifecycle(ctx, record); err != nil {
		return pluginUISessionDTO{}, err
	}
	defer record.lifecycleMu.Unlock()
	record.mu.Lock()
	if !manager.workspaceActiveLocked(record) || record.state != pluginStateRunning || record.manifest == nil || record.runtime == nil {
		state := record.state
		record.mu.Unlock()
		return pluginUISessionDTO{}, fmt.Errorf("plugin %q is not available (state: %s)", pluginID, state)
	}
	var selected *ManifestView
	for index := range record.manifest.Contributes.Views {
		if record.manifest.Contributes.Views[index].ID == strings.TrimSpace(viewID) {
			copy := record.manifest.Contributes.Views[index]
			selected = &copy
			break
		}
	}
	if selected == nil {
		record.mu.Unlock()
		return pluginUISessionDTO{}, fmt.Errorf("plugin view %q not found", viewID)
	}
	actions := make([]pluginUIActionDTO, 0)
	for _, action := range record.manifest.Contributes.Actions {
		if action.UICallable {
			actions = append(actions, pluginUIActionDTO{
				ID: action.ID, Risk: action.Risk, MaxPayloadBytes: action.MaxPayloadBytes,
				ResultHandling: action.ResultHandling,
			})
		}
	}
	generation := record.runtime.Generation()
	binary := record.manifest.Runtime.transport() == pluginTransportFramed
	capabilities := slices.Clone(record.manifest.Capabilities)
	root := record.root
	entry := selected.Entry
	developmentUIURL := record.developmentUIURL
	isDevelopment := record.source == pluginSourceDevelopment
	record.mu.Unlock()
	var session pluginUISessionDTO
	if isDevelopment && developmentUIURL != "" {
		session, err = manager.uiHost.BeginDevelopment(pluginID, selected.ID, generation, developmentUIURL, parentOrigin, actions)
	} else {
		session, err = manager.uiHost.Begin(pluginID, selected.ID, generation, root, entry, parentOrigin, actions)
	}
	if err != nil {
		return pluginUISessionDTO{}, err
	}
	session.FileDrop = cloneFileDrop(selected.FileDrop)
	session.Binary = binary
	if binary {
		manager.uiHost.MarkSessionFramed(session.SessionID)
	}
	if slices.Contains(capabilities, api.CapabilityUIWasm) {
		manager.uiHost.MarkSessionWebAssembly(session.SessionID)
	}
	session.Prefetch = viewPrefetchDTO(selected)
	session.Capabilities = capabilities
	return session, nil
}

func (manager *pluginManager) CloseUI(sessionID string) {
	if manager.uiHost == nil {
		return
	}
	// Closing the plugin's last view lets its process stop (idle_stop.go).
	if pluginID := manager.uiHost.CloseSession(sessionID); pluginID != "" && manager.uiHost.SessionCount(pluginID) == 0 {
		manager.scheduleIdleStop(pluginID, pluginIdleStopDelay)
	}
}

func (manager *pluginManager) InvokeUI(ctx context.Context, sessionID, nonce, action, payload string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, pluginInvokeTimeout)
	defer cancel()
	admitted, err := manager.admitUISession(ctx, cancel, sessionID, nonce)
	if err != nil {
		return "", err
	}
	defer admitted.release()
	var allowed *ManifestUIAction
	for index := range admitted.manifest.Contributes.Actions {
		candidate := &admitted.manifest.Contributes.Actions[index]
		if candidate.ID == strings.TrimSpace(action) && candidate.UICallable {
			copy := *candidate
			allowed = &copy
			break
		}
	}
	if allowed == nil {
		return "", fmt.Errorf("plugin UI action %q is not allowed", action)
	}
	if len(payload) > allowed.MaxPayloadBytes {
		return "", fmt.Errorf("plugin UI action %q payload exceeds %d bytes", action, allowed.MaxPayloadBytes)
	}
	if err := admitted.session.ctx.Err(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Keep the invocation pinned even if reload follows admission.
	return admitted.runtime.InvokeGeneration(ctx, admitted.session.Generation, allowed.ID, payload)
}

// admittedUISession is a live plugin view session bound to the runtime
// generation it was opened against. release stops tying ctx to the session.
type admittedUISession struct {
	session  *pluginUISession
	runtime  *processRuntime
	manifest Manifest
	release  func() bool
}

// admitUISession checks that a view session is still current (workspace
// active, plugin running, same process generation) and cancels ctx through
// cancel when the session closes. Every UI-originated call goes through it.
func (manager *pluginManager) admitUISession(ctx context.Context, cancel context.CancelFunc, sessionID, nonce string) (*admittedUISession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if manager.uiHost == nil {
		return nil, errors.New("plugin UI host is unavailable")
	}
	session, err := manager.uiHost.Session(sessionID, nonce)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(session.ctx, cancel)
	if err := session.ctx.Err(); err != nil {
		stop()
		return nil, err
	}
	record, err := manager.record(session.PluginID)
	if err != nil {
		stop()
		manager.uiHost.CloseSession(session.ID)
		return nil, err
	}
	// Only admission holds the record lock; lifecycle operations must not wait
	// for a plugin RPC. Session cancellation covers revocation after admission.
	record.mu.Lock()
	if !manager.workspaceActiveLocked(record) || record.state != pluginStateRunning || record.runtime == nil || record.runtime.Generation() != session.Generation || record.manifest == nil {
		record.mu.Unlock()
		stop()
		manager.uiHost.CloseSession(session.ID)
		return nil, errors.New("plugin UI session is stale")
	}
	admitted := &admittedUISession{session: session, runtime: record.runtime, manifest: *record.manifest, release: stop}
	record.mu.Unlock()
	return admitted, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}
