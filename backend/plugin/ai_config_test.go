package plugin

import (
	"slices"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

// ai.config gates two plugin-page requests answered by the host frame; it is
// never an action permission and omitted permissions never inherit it.
func TestAIConfigIsNotAnActionPermission(t *testing.T) {
	declared := []api.Capability{api.CapabilityAIConfig, api.CapabilityNetwork}
	got, err := resolveAIActionPermissions(ManifestUIAction{ID: "probe", Risk: "read"}, declared)
	if err != nil || got.Risk != "network" || !slices.Equal(got.Permissions, []api.Capability{api.CapabilityNetwork}) {
		t.Fatalf("inherit skipped ai.config: %+v %v", got, err)
	}
	explicit := ManifestUIAction{ID: "probe", Risk: "dangerous", Permissions: []api.Capability{api.CapabilityAIConfig}}
	if _, err := resolveAIActionPermissions(explicit, declared); err == nil || !strings.Contains(err.Error(), "not an action permission") {
		t.Fatalf("explicit ai.config permission accepted: %v", err)
	}
}

// The frame offers features.aiConfig only to sessions whose plugin declared
// ai.config; the session carries the declared capabilities for that check.
func TestBeginUIReportsDeclaredCapabilities(t *testing.T) {
	manager, record := enabledOnDemand(t, "")
	session, err := manager.BeginUI(record.id, "view", "tango://app")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(session.Capabilities, api.CapabilityAIConfig) {
		t.Fatalf("undeclared ai.config reported: %v", session.Capabilities)
	}
	record.mu.Lock()
	record.manifest.Capabilities = append(record.manifest.Capabilities, api.CapabilityAIConfig)
	record.mu.Unlock()
	session, err = manager.BeginUI(record.id, "view", "tango://app")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(session.Capabilities, api.CapabilityAIConfig) {
		t.Fatalf("declared ai.config missing: %v", session.Capabilities)
	}
	record.mu.Lock()
	record.manifest.Capabilities[len(record.manifest.Capabilities)-1] = api.CapabilityUI
	record.mu.Unlock()
	if !slices.Contains(session.Capabilities, api.CapabilityAIConfig) {
		t.Fatal("the session shares the manifest's capability slice")
	}
}
