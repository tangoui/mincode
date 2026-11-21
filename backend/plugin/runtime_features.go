package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"mincode/app/plugins/api"
)

// runtimeFeature is one opt-in runtime protocol v1 extension negotiated in
// plugin.initialize. The host offers {"features": {name: version}}; a plugin
// opts in by echoing the same version in its initialize result. Anything else
// (missing, different version, wrong type) leaves the feature off.
type runtimeFeature struct {
	Version int
	// RequiresCapability, when set, restricts the offer to plugins whose
	// manifest declares that capability. Other plugins never see the feature.
	RequiresCapability api.Capability
	// Details marks features whose plugin-side description travels as a
	// top-level member of the initialize result named after the feature (for
	// example {"databaseDrivers": {"protocol": 1, "drivers": [...]}}). The raw
	// member is kept per process generation for the owning point to validate.
	Details bool
	// MaxMessage, when set, returns the stdout line cap (bytes) of a
	// generation that negotiated the feature, from its details member. The
	// result is clamped to [pluginRPCMaxMessage, pluginRPCMaxLine], and lines
	// above pluginRPCMaxMessage are still accepted only as responses to calls
	// that allowed a large reply (callGenerationLarge) or, with largeStorage,
	// as host.storage.set requests.
	MaxMessage func(details json.RawMessage) int
}

// runtimeFeatureDetailsMaxBytes bounds a retained details member. Larger
// members disable the feature for that generation instead of being truncated.
const runtimeFeatureDetailsMaxBytes = 64 * 1024

// hostRuntimeFeatures is the single table behind the plugin.initialize offer
// and the host.api.describe feature list. Add a row here, never a literal.
var hostRuntimeFeatures = map[string]runtimeFeature{
	// Opt-in payload extensions only; neither feature grants host permissions.
	"searchFileTargets":     {Version: 1, RequiresCapability: api.CapabilityWorkspaceRead},
	"serviceCallerMetadata": {Version: 1},
	"tasks":                 {Version: 1},
	// taskEvents: the plugin sends plugin.task.changed {"taskId"}
	// notifications so the host refreshes a task at once instead of on its
	// next status poll (tasks.go). Meaningful only together with tasks.
	"taskEvents":    {Version: 1},
	"configuration": {Version: 1},
	// databaseDrivers: see docs/architecture/database-driver-protocol.md. The
	// driver methods (db.*) are only called after this feature negotiated.
	"databaseDrivers": {Version: 1, RequiresCapability: api.CapabilityDatabaseDriver, Details: true, MaxMessage: databaseDriverRuntimeMaxMessage},
	// largeStorage: the plugin reads lines up to pluginRPCMaxStorageMessage,
	// so host.storage.get replies (and any other reply) may carry values up to
	// pluginKVMaxValueBytes, and its host.storage.set requests may be that
	// large. Without it messages stay within 1 MiB.
	"largeStorage": {Version: 1, RequiresCapability: api.CapabilityStorage, MaxMessage: func(json.RawMessage) int { return pluginRPCMaxStorageMessage }},
	// aiTurnHistory: advertisement only (nothing to echo). The host honours
	// host.ai.turn.start "history": "none" (ai_turns.go); older hosts ignore
	// the field and replay the conversation, so plugins check this first.
	"aiTurnHistory": {Version: 1, RequiresCapability: api.CapabilityAI},
}

// offeredRuntimeFeatures returns the {name: version} offer for a plugin with
// the given capability set.
func offeredRuntimeFeatures(capabilities map[api.Capability]struct{}) map[string]int {
	offer := make(map[string]int, len(hostRuntimeFeatures))
	for name, feature := range hostRuntimeFeatures {
		if feature.RequiresCapability != "" {
			if _, ok := capabilities[feature.RequiresCapability]; !ok {
				continue
			}
		}
		offer[name] = feature.Version
	}
	return offer
}

// negotiatedRuntimeFeatures is the outcome of one plugin.initialize exchange.
type negotiatedRuntimeFeatures struct {
	versions map[string]int
	details  map[string]json.RawMessage
	// problems are human-readable reasons a Details feature was dropped; they
	// are logged once per generation and never include the member content.
	problems []string
}

func (features negotiatedRuntimeFeatures) has(name string) bool {
	_, ok := features.versions[name]
	return ok
}

// maxMessage is the stdout line cap for the negotiated features (the 1 MiB
// runtime cap unless a feature's MaxMessage raises it).
func (features negotiatedRuntimeFeatures) maxMessage() int {
	limit := pluginRPCMaxMessage
	for name := range features.versions {
		feature := hostRuntimeFeatures[name]
		if feature.MaxMessage == nil {
			continue
		}
		limit = max(limit, min(feature.MaxMessage(features.details[name]), pluginRPCMaxLine))
	}
	return limit
}

// negotiateRuntimeFeatures parses an initialize result against an offer. It
// never fails: malformed results simply negotiate nothing.
func negotiateRuntimeFeatures(offer map[string]int, initializeResult json.RawMessage) negotiatedRuntimeFeatures {
	result := negotiatedRuntimeFeatures{versions: map[string]int{}, details: map[string]json.RawMessage{}}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(initializeResult, &envelope) != nil {
		return result
	}
	var echoed map[string]json.RawMessage
	if json.Unmarshal(envelope["features"], &echoed) != nil {
		return result
	}
	names := make([]string, 0, len(offer))
	for name := range offer {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var version int
		if raw, ok := echoed[name]; !ok || json.Unmarshal(raw, &version) != nil || version != offer[name] {
			continue
		}
		if hostRuntimeFeatures[name].Details {
			raw := bytes.TrimSpace(envelope[name])
			switch {
			case len(raw) == 0 || raw[0] != '{':
				result.problems = append(result.problems, fmt.Sprintf("feature %s negotiated without an object %q member; disabled", name, name))
				continue
			case len(raw) > runtimeFeatureDetailsMaxBytes:
				result.problems = append(result.problems, fmt.Sprintf("feature %s details exceed %d bytes; disabled", name, runtimeFeatureDetailsMaxBytes))
				continue
			}
			result.details[name] = append(json.RawMessage(nil), raw...)
		}
		result.versions[name] = version
	}
	return result
}

// replyLimit is the size limit of host replies to the running generation:
// pluginRPCMaxStorageMessage once it negotiated largeStorage, else 1 MiB.
func (runtimeHost *processRuntime) replyLimit() int {
	if runtimeHost.featureNegotiated("largeStorage") {
		return pluginRPCMaxStorageMessage
	}
	return pluginRPCMaxMessage
}

// featureNegotiated reports whether the running generation negotiated name.
func (runtimeHost *processRuntime) featureNegotiated(name string) bool {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	return runtimeHost.cmd != nil && !runtimeHost.stopping && runtimeHost.features.has(name)
}

// featureAtGeneration atomically checks the process and its negotiated feature.
// Callers must use the admitted generation, never resample Generation on reload.
func (runtimeHost *processRuntime) featureAtGeneration(generation uint64, name string) bool {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	return generation != 0 && runtimeHost.generation == generation && runtimeHost.cmd != nil &&
		!runtimeHost.stopping && runtimeHost.features.has(name)
}

// featureDetails returns a copy of the details member retained for name and
// the process generation it belongs to. Callers must revalidate the
// generation before acting on the details (a restart renegotiates).
func (runtimeHost *processRuntime) featureDetails(name string) (json.RawMessage, uint64, bool) {
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	if runtimeHost.cmd == nil || runtimeHost.stopping || !runtimeHost.features.has(name) {
		return nil, 0, false
	}
	raw, ok := runtimeHost.features.details[name]
	if !ok {
		return nil, 0, false
	}
	return append(json.RawMessage(nil), raw...), runtimeHost.generation, true
}
