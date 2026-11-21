package plugin

// callerMetadata deliberately aliases the runtime-construction snapshot: there
// is only one schema and no later lookup of a mutable record's provenance.
type callerMetadata = runtimeCallerMetadata

type serviceCallerSnapshot struct {
	Identity runtimeIdentity
	Metadata callerMetadata
}

// serviceCallerEnvelope is the entire public caller allowlist. Internal
// instance/generation and installation/session/trust state never reach the wire.
type serviceCallerEnvelope struct {
	Kind        string `json:"kind"`
	PluginID    string `json:"pluginId,omitempty"`
	Surface     string `json:"surface,omitempty"`
	Source      string `json:"source,omitempty"`
	Development *bool  `json:"development,omitempty"`
	Version     string `json:"version,omitempty"`
}

// captureServiceCaller consumes the original RPC ingress generation, never a
// generation sampled after a queued request or an approval has resumed.
func captureServiceCaller(runtimeHost *processRuntime, generation uint64) (serviceCaller, error) {
	if runtimeHost == nil || runtimeHost.ownerManager == nil || runtimeHost.ownerRecord == nil {
		return serviceCaller{}, newContributionError(contributionErrCancelled, "service caller has no runtime owner")
	}
	manager, record := runtimeHost.ownerManager, runtimeHost.ownerRecord
	caller := serviceCaller{Kind: "plugin", PluginID: runtimeHost.pluginID,
		Snapshot: serviceCallerSnapshot{Identity: runtimeHost.identity(generation), Metadata: runtimeHost.callerMetadata}}
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.id != caller.PluginID || record.runtime != runtimeHost {
		return serviceCaller{}, newContributionError(contributionErrCancelled, "service caller runtime changed")
	}
	if err := manager.validateRuntimeIdentityLocked(record, caller.Snapshot.Identity); err != nil {
		return serviceCaller{}, newContributionError(contributionErrCancelled, "service caller runtime changed")
	}
	return caller, nil
}

func (manager *pluginManager) validateServiceCaller(caller serviceCaller) error {
	if _, err := caller.owner(); err != nil {
		return err
	}
	if caller.Kind == "host" {
		return nil
	}
	if err := manager.validateRuntimeIdentity(caller.PluginID, caller.Snapshot.Identity); err != nil {
		return newContributionError(contributionErrCancelled, "service caller runtime changed or is no longer active")
	}
	return nil
}

func (caller serviceCaller) envelope(metadata bool) serviceCallerEnvelope {
	if caller.Kind == "host" {
		return serviceCallerEnvelope{Kind: "host", Surface: caller.Surface}
	}
	envelope := serviceCallerEnvelope{Kind: "plugin", PluginID: caller.PluginID}
	if metadata {
		// The snapshot is host-filled at construction. Normalize defensively;
		// an absent boolean is unknown, not a confirmed false.
		frozen := caller.Snapshot.Metadata
		envelope.Source = "unknown"
		switch frozen.Source {
		case pluginSourceBuiltin, pluginSourceLocal, pluginSourceDevelopment:
			envelope.Source = frozen.Source
		}
		envelope.Development = frozen.Development
		if len(frozen.Version) <= 128 {
			envelope.Version = frozen.Version
		}
	}
	return envelope
}

// Called again at actual dispatch, including inside the short-path goroutine.
// Generation checks on the old runtime alone cannot detect a replacement
// runtime whose generation has restarted at 1.
func (manager *pluginManager) validateServiceDispatch(caller serviceCaller, providerID string, admission *contributedAdmission) error {
	if err := manager.validateServiceCaller(caller); err != nil {
		return err
	}
	if err := admission.Ctx.Err(); err != nil {
		return err
	}
	return manager.validateRuntimeIdentity(providerID, admission.Runtime.identity(admission.Generation))
}
