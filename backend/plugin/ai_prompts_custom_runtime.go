package plugin

import (
	"encoding/json"
	"mincode/app/plugins/api"
)

// The overlay deliberately does not depend on a running process. Snapshot
// discovery must restore saved templates without activating local code.
type customAiPromptsRuntime struct {
	Revision uint64                 `json:"revision"`
	Entries  []AiPromptContribution `json:"entries"`
	Error    string                 `json:"error,omitempty"`
}

func customAiPromptsOverlay(record *pluginRecord) any {
	if record.manifest == nil {
		return nil
	}
	snapshot, err := readCustomAiPrompts(*record.manifest)
	if err != nil {
		return customAiPromptsRuntime{Entries: []AiPromptContribution{}, Error: "Saved custom prompts are invalid or unavailable; open the plugin to inspect or clear its data."}
	}
	return customAiPromptsRuntime{Revision: snapshot.Revision, Entries: snapshot.Entries}
}

func customAiPromptsGate(runtimeHost *processRuntime) bool {
	_, ui := runtimeHost.capabilities[api.CapabilityUI]
	_, storage := runtimeHost.capabilities[api.CapabilityStorage]
	return ui && storage && runtimeHost.manifest.contribution(aiPromptPointKey) != nil
}

func init() {
	for _, action := range []string{"list", "save", "delete"} {
		registerGenerationHostMethod("host.aiPrompts."+action, customAiPromptsGate, api.CapabilityStorage, func(runtimeHost *processRuntime, generation uint64, raw json.RawMessage) (any, *rpcError) {
			return runtimeHost.customAiPromptRequest(generation, action, raw)
		})
	}
}

// Lock order matches snapshot/lifecycle: record -> runtime -> document. A
// stopped, replaced, disabled or workspace-inactive runtime cannot commit.
func (runtimeHost *processRuntime) customAiPromptRequest(generation uint64, action string, raw json.RawMessage) (any, *rpcError) {
	manager, record := runtimeHost.ownerManager, runtimeHost.ownerRecord
	denied := func() (any, *rpcError) {
		return nil, &rpcError{Code: -32001, Message: "AI prompt management is unavailable for this runtime"}
	}
	if manager == nil || record == nil || !customAiPromptsGate(runtimeHost) {
		return denied()
	}
	changed := false
	defer func() {
		if changed {
			notifyCustomAiPrompts(runtimeHost.pluginID)
		}
	}()
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.runtime != runtimeHost || !manager.workspaceActiveLocked(record) || record.state == pluginStateError || record.manifest == nil {
		return denied()
	}
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	if runtimeHost.generation != generation || runtimeHost.cmd == nil || runtimeHost.runContext == nil || runtimeHost.runContext.Err() != nil || runtimeHost.stopping {
		return denied()
	}
	var result customAiPromptSnapshot
	var err error
	if action == "storage.set" || action == "storage.delete" {
		var params struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		if err = decodeStrictJSON(raw, &params, true); err == nil {
			if params.Key != customAiPromptsKey {
				return denied()
			}
			err = replaceCustomAiPromptsRaw(runtimeHost.pluginID, params.Value, action == "storage.delete")
		}
		if err != nil {
			return nil, &rpcError{Code: -32602, Message: err.Error()}
		}
		changed = true
		return true, nil
	} else if action == "list" {
		var params struct{}
		if err = decodeStrictJSON(raw, &params, true); err == nil {
			result, err = readCustomAiPrompts(runtimeHost.manifest)
		}
	} else {
		result, err = mutateCustomAiPrompts(runtimeHost.manifest, action, raw)
	}
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}
	changed = action != "list"
	return result, nil
}

func notifyCustomAiPrompts(pluginID string) {
	var manager *pluginManager
	manager.emitContributionsChanged(pluginID, []string{aiPromptPointKey})
}
