package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// Plugin-to-plugin calls. A plugin with the "plugins" capability may call
// the AI-callable actions of another installed plugin: those actions already
// publish a closed input schema, examples and a risk level, so the callee
// needs no extra declaration and is called exactly like the assistant calls
// it. The rules are generic:
//
//   - the user authorizes each caller → target pair once ("always", read-risk
//     actions only) or per call; every other risk is confirmed on every call;
//   - a fully trusted side (caller or target) skips the confirmation
//     (full_trust.go); the skip is audited on the trusted side;
//   - payloads are validated against the action's inputSchema;
//   - the call is bounded like other host-initiated invocations
//     (invokeContributedAction: activation, workspace scope, timeout, size).
const (
	pluginCallPoint          = "plugins.invoke"
	pluginCallResultMaxBytes = 256 << 10
	pluginCallConcurrency    = 2
	pluginCallPayloadPreview = 2048
)

// pluginCallGrantsKVKey contains "/", which plugin IDs never do, so it cannot
// collide with the host KV keys named after plugin IDs (id, id.ai, id.scope).
func pluginCallGrantsKVKey(id string) string { return "calls/" + id }

// pluginCallGrantsMu serialises the read-modify-write of grant lists.
var pluginCallGrantsMu sync.Mutex

type pluginCallGrants struct {
	Targets []string `json:"targets"`
}

func readPluginCallGrants(caller string) pluginCallGrants {
	raw, found, err := pluginKVGetRaw(hostPluginID, pluginCallGrantsKVKey(caller))
	var grants pluginCallGrants
	if err != nil || !found || json.Unmarshal(raw, &grants) != nil {
		return pluginCallGrants{}
	}
	return grants
}

func writePluginCallGrants(caller string, grants pluginCallGrants) error {
	slices.Sort(grants.Targets)
	grants.Targets = slices.Compact(grants.Targets)
	if len(grants.Targets) == 0 {
		return pluginKVDeleteRaw(hostPluginID, pluginCallGrantsKVKey(caller))
	}
	raw, _ := json.Marshal(grants)
	return pluginKVSetRaw(hostPluginID, pluginCallGrantsKVKey(caller), raw)
}

// Test seams over the host KV.
var (
	pluginCallGranted = func(caller, target string) bool {
		return slices.Contains(readPluginCallGrants(caller).Targets, target)
	}
	grantPluginCall = func(caller, target string) error {
		pluginCallGrantsMu.Lock()
		defer pluginCallGrantsMu.Unlock()
		grants := readPluginCallGrants(caller)
		grants.Targets = append(grants.Targets, target)
		return writePluginCallGrants(caller, grants)
	}
	pluginCallInvoke = func(ctx context.Context, call contributedActionCall) (json.RawMessage, error) {
		return host().invokeContributedAction(ctx, call)
	}
	pluginCallTarget = func(target, action string) (pluginCallTargetInfo, error) {
		record, err := host().record(target)
		if err != nil {
			return pluginCallTargetInfo{}, fmt.Errorf("plugin %q is not installed", target)
		}
		record.mu.Lock()
		defer record.mu.Unlock()
		if record.manifest == nil || !record.enabled {
			return pluginCallTargetInfo{}, fmt.Errorf("plugin %q is not enabled", target)
		}
		declared, ok := contributedActionLocked(record, action)
		if !ok || !declared.AICallable {
			return pluginCallTargetInfo{}, fmt.Errorf("plugin %q has no callable action %q", target, action)
		}
		resolved, err := resolveAIActionPermissions(declared, record.manifest.Capabilities)
		if err != nil {
			return pluginCallTargetInfo{}, err
		}
		return pluginCallTargetInfo{Name: record.manifest.Name, Action: resolved}, nil
	}
)

// pluginCallFullTrustSkip reports whether a plugin-to-plugin or service call
// may skip its approval because the caller or the target is fully trusted
// (full_trust.go). Each trusted side is audited with the other as
// counterpart. A test seam; production asks servicesManager().
var pluginCallFullTrustSkip = func(caller, target string, entry FullTrustAuditEntry) bool {
	return servicesManager().pluginCallFullTrustSkip(caller, target, entry)
}

// pluginCallFullTrustSkip checks both sides (no short circuit, so a call
// between two trusted plugins is audited on both).
func (manager *pluginManager) pluginCallFullTrustSkip(caller, target string, entry FullTrustAuditEntry) bool {
	callerEntry, targetEntry := entry, entry
	callerEntry.Counterpart, targetEntry.Counterpart = target, caller
	callerTrusted := manager.fullTrustSkip(caller, callerEntry)
	targetTrusted := manager.fullTrustSkip(target, targetEntry)
	return callerTrusted || targetTrusted
}

// pluginCallTargetInfo describes the target of an approval prompt. Service
// and ServiceTitle are set for service calls.
type pluginCallTargetInfo struct {
	Name                  string
	Action                ManifestUIAction
	Service, ServiceTitle string
}

func init() {
	registerHostMethod("host.plugins.list", nil, api.CapabilityPlugins, hostPluginsList)
	registerHostMethod("host.plugins.invoke", nil, api.CapabilityPlugins, hostPluginsInvoke)
	core.Register("plugin_call_grants_get", pluginCallGrantsGetHandler)
	core.Register("plugin_call_grants_revoke", pluginCallGrantsRevokeHandler)
}

// hostPluginsList lists the other installed plugins with callable actions.
func hostPluginsList(runtimeHost *processRuntime, _ json.RawMessage) (any, *rpcError) {
	plugins, err := aiCallablePlugins(runtimeHost.pluginID)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: err.Error()}
	}
	granted := readPluginCallGrants(runtimeHost.pluginID).Targets
	out := make([]map[string]any, 0, len(plugins))
	for _, plugin := range plugins {
		out = append(out, map[string]any{"id": plugin.ID, "name": plugin.Name, "description": plugin.Description,
			"enabled": plugin.Enabled, "actions": plugin.Actions, "granted": slices.Contains(granted, plugin.ID)})
	}
	return map[string]any{"plugins": out}, nil
}

func newPluginCallRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "pcall-" + hex.EncodeToString(b[:])
}

func payloadPreview(payload []byte) string {
	text := string(payload)
	if len(text) > pluginCallPayloadPreview {
		text = strings.ToValidUTF8(text[:pluginCallPayloadPreview], "") + "…"
	}
	return text
}

func hostPluginsInvoke(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	var request struct {
		PluginID  string          `json:"pluginId"`
		Action    string          `json:"action"`
		Payload   json.RawMessage `json:"payload"`
		TimeoutMs int             `json:"timeoutMs"`
	}
	if err := decodeHostUIParams(params, &request); err != nil {
		return nil, err
	}
	approvalTimeout, rpcErr := promptTimeout(request.TimeoutMs)
	if rpcErr != nil {
		return nil, rpcErr
	}
	target := strings.TrimSpace(request.PluginID)
	if !pluginIDPattern.MatchString(target) || target == runtimeHost.pluginID || !contributionIDPattern.MatchString(request.Action) {
		return nil, hostUIParamsError("pluginId must name another plugin and action must be an action id")
	}
	payload := []byte(strings.TrimSpace(string(request.Payload)))
	if len(payload) == 0 || string(payload) == "null" {
		payload = []byte("{}")
	}
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return nil, hostUIParamsError("payload must be JSON")
	}
	info, err := pluginCallTarget(target, request.Action)
	if err != nil {
		return nil, &rpcError{Code: -32002, Message: err.Error(), Data: map[string]string{"code": "not_found"}}
	}
	if info.Action.InputSchema != nil {
		if err := validateAIValue(info.Action.InputSchema, value, "payload"); err != nil {
			return nil, hostUIParamsError(err.Error())
		}
	}
	if rpcErr := confirmPluginCall(runtimeHost, info, target, request.Action, payloadPreview(payload), approvalTimeout, true); rpcErr != nil {
		return nil, rpcErr
	}
	ctx := runtimeHost.promptContext()
	callCtx, cancel := context.WithTimeout(ctx, pluginInvokeTimeout+5*time.Second)
	defer cancel()
	action := request.Action
	approvedRisk := info.Action.Risk
	result, err := pluginCallInvoke(callCtx, contributedActionCall{
		PluginID: target, Point: pluginCallPoint, EntryID: runtimeHost.pluginID, ActionID: action,
		RequestID: newPluginCallRequestID(), Payload: payload, MaxResultBytes: pluginCallResultMaxBytes, Concurrency: pluginCallConcurrency, ActionsOnly: true,
		// The target may have been updated while the approval was open:
		// re-check the risk the user approved and the payload schema.
		Validate: func(record *pluginRecord) error {
			declared, ok := contributedActionLocked(record, action)
			if !ok || !declared.AICallable {
				return newContributionError(contributionErrNotFound, "action %q is no longer callable", action)
			}
			resolved, err := resolveAIActionPermissions(declared, record.manifest.Capabilities)
			if err != nil || resolved.Risk != approvedRisk {
				return newContributionError(contributionErrForbidden, "action %q changed since it was approved; call it again", action)
			}
			if resolved.InputSchema != nil {
				if err := validateAIValue(resolved.InputSchema, value, "payload"); err != nil {
					return newContributionError(contributionErrInvalidRequest, "%v", err)
				}
			}
			return nil
		},
	})
	if err != nil {
		var coded *contributionError
		if errors.As(err, &coded) {
			return nil, &rpcError{Code: -32000, Message: coded.Error(), Data: map[string]string{"code": coded.Code}}
		}
		return nil, &rpcError{Code: -32000, Message: err.Error()}
	}
	return map[string]any{"result": result}, nil
}

// confirmPluginCall asks the user to approve runtimeHost's call of action on
// targetID unless a remembered grant covers it. Only read-risk calls may be
// remembered ("always", stored in calls/<caller>); network, write and
// dangerous calls are confirmed every time. Service calls (host.services.*)
// set target.Service so the prompt names the service as well. A call is not
// asked at all while the caller or the target is fully trusted (and the app
// unlocked); the read grants are neither consulted nor written then. A call
// that is not interactive fails with interaction_required instead of being
// asked: nothing is shown, audited or granted.
func confirmPluginCall(runtimeHost *processRuntime, target pluginCallTargetInfo, targetID, action string, preview string, approvalTimeout time.Duration, interactive bool) *rpcError {
	surface, subject := "pluginCall", action
	if target.Service != "" {
		surface, subject = "serviceCall", target.Service
	}
	if pluginCallFullTrustSkip(runtimeHost.pluginID, targetID, FullTrustAuditEntry{Surface: surface, Risk: target.Action.Risk, Subject: subject}) {
		return nil
	}
	granted := pluginCallGranted(runtimeHost.pluginID, targetID)
	confirmEach := target.Action.Risk != "read"
	if granted && !confirmEach {
		return nil
	}
	if !interactive {
		return interactionRequiredError()
	}
	items := []quickPickItem{{ID: "once", Label: "once"}}
	if !granted && !confirmEach {
		items = append(items, quickPickItem{ID: "always", Label: "always"})
	}
	ids := map[string]bool{}
	for _, item := range items {
		ids[item.ID] = true
	}
	event := promptEvent{Kind: "pluginCall", PluginID: runtimeHost.pluginID, PluginName: runtimeHost.displayName(),
		Title: cleanHostText(target.Name, hostUITitleMaxRunes, false), Items: items,
		Call: &pluginCallDetails{TargetPluginID: targetID, TargetName: cleanHostText(target.Name, hostUITitleMaxRunes, false), Action: action,
			Description: cleanHostText(target.Action.Description, hostUITextMaxRunes, false), Risk: target.Action.Risk, Payload: preview,
			Service: target.Service, ServiceTitle: cleanHostText(target.ServiceTitle, hostUITitleMaxRunes, false)}}
	reply, rpcErr := hostPrompts.ask(runtimeHost.promptContext(), event, &pendingPrompt{pluginID: runtimeHost.pluginID, kind: "pluginCall", items: ids}, approvalTimeout)
	if rpcErr != nil {
		return rpcErr
	}
	if reply.Cancelled {
		return pluginCallDeclined()
	}
	if slices.Contains(reply.IDs, "always") {
		if err := grantPluginCall(runtimeHost.pluginID, targetID); err != nil {
			return &rpcError{Code: -32000, Message: "could not save the authorization"}
		}
	}
	return nil
}

func pluginCallDeclined() *rpcError {
	return &rpcError{Code: -32003, Message: "the user declined the call", Data: map[string]string{"code": "declined"}}
}

// pluginCallDetails describes a plugin-to-plugin call in its approval prompt.
type pluginCallDetails struct {
	TargetPluginID string `json:"targetPluginId"`
	TargetName     string `json:"targetName"`
	Action         string `json:"action"`
	Description    string `json:"description,omitempty"`
	Risk           string `json:"risk"`
	Payload        string `json:"payload"`
	// Service and ServiceTitle are set for service calls (host.services.*).
	Service      string `json:"service,omitempty"`
	ServiceTitle string `json:"serviceTitle,omitempty"`
}

// revokePluginCallTarget removes target from every installed caller's
// grants, so a later plugin with the same ID does not inherit them.
func (manager *pluginManager) revokePluginCallTarget(target string) {
	manager.mu.RLock()
	records := manager.orderedRecordsLocked()
	manager.mu.RUnlock()
	pluginCallGrantsMu.Lock()
	defer pluginCallGrantsMu.Unlock()
	for _, record := range records {
		grants := readPluginCallGrants(record.id)
		if !slices.Contains(grants.Targets, target) {
			continue
		}
		grants.Targets = slices.DeleteFunc(grants.Targets, func(item string) bool { return item == target })
		_ = writePluginCallGrants(record.id, grants)
	}
}

// pluginCallGrantsGetHandler lists the plugins a caller may call without asking.
func pluginCallGrantsGetHandler(_, args string) (string, error) {
	var input struct {
		ID string `json:"id"`
	}
	if err := core.DecodeArgs(args, &input); err != nil {
		return "", err
	}
	grants := readPluginCallGrants(strings.TrimSpace(input.ID))
	if grants.Targets == nil {
		grants.Targets = []string{}
	}
	return core.EncodeResult(grants)
}

// pluginCallGrantsRevokeHandler removes one target ("" removes all). Only the
// desktop UI calls it; plugins cannot change their own grants.
func pluginCallGrantsRevokeHandler(_, args string) (string, error) {
	var input struct {
		ID     string `json:"id"`
		Target string `json:"target"`
	}
	if err := core.DecodeArgs(args, &input); err != nil {
		return "", err
	}
	id := strings.TrimSpace(input.ID)
	pluginCallGrantsMu.Lock()
	defer pluginCallGrantsMu.Unlock()
	grants := readPluginCallGrants(id)
	if target := strings.TrimSpace(input.Target); target != "" {
		grants.Targets = slices.DeleteFunc(grants.Targets, func(item string) bool { return item == target })
	} else {
		grants.Targets = nil
	}
	if err := writePluginCallGrants(id, grants); err != nil {
		return "", err
	}
	if grants.Targets == nil {
		grants.Targets = []string{}
	}
	return core.EncodeResult(grants)
}
