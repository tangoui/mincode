package plugin

import (
	"math"
	"strconv"
	"sync/atomic"
)

var nextRuntimeInstance atomic.Uint64

// allocateRuntimeInstance never wraps: after MaxUint64 it returns 0, the
// invalid instance, so no identity can be reused for a later runtime.
func allocateRuntimeInstance() uint64 {
	for {
		current := nextRuntimeInstance.Load()
		if current == math.MaxUint64 {
			return 0
		}
		if nextRuntimeInstance.CompareAndSwap(current, current+1) {
			return current + 1
		}
	}
}

// parseRuntimeIdentityPart accepts only canonical non-zero decimal strings,
// so one runtime cannot be represented by several wire identities.
func parseRuntimeIdentityPart(value string) (uint64, bool) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return parsed, err == nil && parsed != 0 && strconv.FormatUint(parsed, 10) == value
}

// runtimeIdentity binds a process generation to one runtime construction.
type runtimeIdentity struct {
	Instance   string `json:"instance"`
	Generation string `json:"generation"`
}

// runtimeCallerMetadata is a path-free snapshot, not an authentication claim.
type runtimeCallerMetadata struct {
	Source      string
	Development *bool
	Version     string
}

func (runtimeHost *processRuntime) identity(generation uint64) runtimeIdentity {
	// instance is immutable; the caller supplies the generation it admitted,
	// never a later generation read opportunistically from this runtime.
	if runtimeHost.instance == 0 {
		return runtimeIdentity{}
	}
	return runtimeIdentity{Instance: strconv.FormatUint(runtimeHost.instance, 10), Generation: strconv.FormatUint(generation, 10)}
}

func (manager *pluginManager) validateRuntimeIdentity(pluginID string, id runtimeIdentity) error {
	record, err := manager.record(pluginID)
	if err != nil {
		return newContributionError(contributionErrUnavailable, "plugin %q is no longer available", pluginID)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	return manager.validateRuntimeIdentityLocked(record, id)
}

// Caller holds record.mu; lock order is record.mu -> runtime.mu, as in admission.
func (manager *pluginManager) validateRuntimeIdentityLocked(record *pluginRecord, id runtimeIdentity) error {
	runtimeHost := record.runtime
	instance, instanceOK := parseRuntimeIdentityPart(id.Instance)
	generation, generationOK := parseRuntimeIdentityPart(id.Generation)
	if !instanceOK || !generationOK || !record.enabled || record.state != pluginStateRunning ||
		!manager.workspaceActiveLocked(record) || runtimeHost == nil || runtimeHost.instance != instance {
		return newContributionError(contributionErrUnavailable, "plugin %q runtime changed or is not active", record.id)
	}
	runtimeHost.mu.Lock()
	defer runtimeHost.mu.Unlock()
	if runtimeHost.cmd == nil || runtimeHost.stopping || runtimeHost.generation != generation {
		return newContributionError(contributionErrUnavailable, "plugin %q process generation is no longer running", record.id)
	}
	return nil
}

func freezeRuntimeCallerMetadata(source, version string) runtimeCallerMetadata {
	metadata := runtimeCallerMetadata{Source: "unknown"}
	switch source {
	case pluginSourceBuiltin, pluginSourceLocal, pluginSourceDevelopment:
		development := source == pluginSourceDevelopment
		metadata.Source, metadata.Development = source, &development
	}
	// Count UTF-8 bytes, not runes; omission must not invent a truncated version.
	if len(version) <= 128 {
		metadata.Version = version
	}
	return metadata
}
