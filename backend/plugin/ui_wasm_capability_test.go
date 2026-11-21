package plugin

import (
	"slices"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

func TestManifestUIWasmRequiresUI(t *testing.T) {
	if _, err := readManifest(writeTestManifest(t, func(manifest *Manifest) {
		manifest.Capabilities = []api.Capability{api.CapabilityUIWasm}
	})); err == nil || !strings.Contains(err.Error(), "ui.wasm requires the ui capability") {
		t.Fatalf("ui.wasm without ui: %v", err)
	}
	manifest, err := readManifest(writeTestManifestV2(t, func(manifest *Manifest) {
		manifest.Capabilities = append(manifest.Capabilities, api.CapabilityUIWasm)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(manifest.Capabilities, api.CapabilityUIWasm) {
		t.Fatalf("ui.wasm dropped: %v", manifest.Capabilities)
	}
}

func TestUIWasmIsNeverAnActionPermission(t *testing.T) {
	capabilities := []api.Capability{api.CapabilityUI, api.CapabilityUIWasm, api.CapabilityStorage}
	inherited, err := resolveAIActionPermissions(ManifestUIAction{ID: "render", Risk: "read"}, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(inherited.Permissions, api.CapabilityUIWasm) {
		t.Fatalf("omitted permissions inherited ui.wasm: %v", inherited.Permissions)
	}
	if _, err := resolveAIActionPermissions(ManifestUIAction{ID: "render", Risk: "read", Permissions: []api.Capability{api.CapabilityUIWasm}}, capabilities); err == nil || !strings.Contains(err.Error(), "not an action permission") {
		t.Fatalf("explicit ui.wasm permission: %v", err)
	}
}
