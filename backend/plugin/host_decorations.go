package plugin

import (
	"encoding/json"
	"sync"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// host.decorations.refresh (host 0.3.0) is a data-free hint: a plugin that
// declares contributes.fileDecorations and holds workspace.read asks the
// project tree to re-request the decorations of the directories it already
// shows (for example after its own lint run or a workspace.changed event).
// It takes no parameters ({} or none), reads nothing and returns nothing
// about files: the host only emits plugin:decorations-refresh {pluginId},
// and the frontend re-queues that plugin's expanded directories through the
// usual debounced, bounded request path. At most one hint per plugin takes
// effect every refreshHintMinIntervalMs; a hint sooner than that answers
// {"scheduled": false} without an error. Plugins that do not declare the
// point get -32601, like a host without the method.
const (
	decorationsRefreshMethod = "host.decorations.refresh"
	decorationsRefreshEvent  = "plugin:decorations-refresh"
)

var (
	decorationsRefreshInterval = time.Duration(fileDecorationSpec.Limit("refreshHintMinIntervalMs")) * time.Millisecond

	// decorationsRefreshNow is the hint clock; tests replace it.
	decorationsRefreshNow = time.Now
	// emitDecorationsRefresh tells the frontend to re-request the plugin's
	// visible directories; tests replace it.
	emitDecorationsRefresh = func(pluginID string) {
		core.Emit(decorationsRefreshEvent, map[string]string{"pluginId": pluginID})
	}

	// decorationsRefreshes records when each plugin's last hint took effect.
	decorationsRefreshes = struct {
		mu   sync.Mutex
		last map[string]time.Time
	}{last: map[string]time.Time{}}
)

type decorationsRefreshResult struct {
	Scheduled bool `json:"scheduled"`
}

func init() {
	registerHostMethod(decorationsRefreshMethod, declaresFileDecorations, api.CapabilityWorkspaceRead, hostDecorationsRefresh)
	markHostMethodGateNotFound(decorationsRefreshMethod)
	registerRecordRemovalHook(forgetDecorationsRefresh)
}

// declaresFileDecorations is the method's gate: the runtime's manifest
// contributes at least one file decoration provider.
func declaresFileDecorations(runtimeHost *processRuntime) bool {
	return len(runtimeHost.manifest.FileDecorationProviders()) > 0
}

func hostDecorationsRefresh(runtimeHost *processRuntime, params json.RawMessage) (any, *rpcError) {
	if len(params) > 0 && string(params) != "null" {
		var empty struct{}
		if err := decodeStrictJSON(params, &empty, true); err != nil {
			return nil, &rpcError{Code: -32602, Message: "host.decorations.refresh takes no parameters"}
		}
	}
	now := decorationsRefreshNow()
	decorationsRefreshes.mu.Lock()
	last, seen := decorationsRefreshes.last[runtimeHost.pluginID]
	scheduled := !seen || now.Sub(last) >= decorationsRefreshInterval
	if scheduled {
		decorationsRefreshes.last[runtimeHost.pluginID] = now
	}
	decorationsRefreshes.mu.Unlock()
	if scheduled {
		emitDecorationsRefresh(runtimeHost.pluginID)
	}
	return decorationsRefreshResult{Scheduled: scheduled}, nil
}

// forgetDecorationsRefresh drops an uninstalled (or data-cleared) plugin's
// last hint time.
func forgetDecorationsRefresh(_ *pluginManager, pluginID string, _ removalReason) {
	decorationsRefreshes.mu.Lock()
	delete(decorationsRefreshes.last, pluginID)
	decorationsRefreshes.mu.Unlock()
}
