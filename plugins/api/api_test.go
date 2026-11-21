package api

import (
	"reflect"
	"slices"
	"testing"
)

func TestCapabilitiesV1ReturnsIndependentSlices(t *testing.T) {
	want := []Capability{
		CapabilityCommands,
		CapabilityStorage,
		CapabilityNetwork,
		CapabilityShell,
		CapabilityWorkspaceRead,
		CapabilityWorkspaceWrite,
		CapabilitySecrets,
	}
	first := CapabilitiesV1()
	second := CapabilitiesV1()
	if !reflect.DeepEqual(first, want) || !reflect.DeepEqual(second, want) {
		t.Fatalf("unexpected v1 vocabulary: first=%v, second=%v", first, second)
	}
	first[0] = CapabilityUI
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("mutating one result changed another: %v", second)
	}
	if got := CapabilitiesV1(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating a result changed future calls: %v", got)
	}
}

func TestCapabilitiesV1IgnoresLegacyMutations(t *testing.T) {
	original := KnownCapabilities
	saved := append([]Capability(nil), original...)
	t.Cleanup(func() {
		copy(original, saved)
		KnownCapabilities = original
	})
	want := CapabilitiesV1()
	if !reflect.DeepEqual(KnownCapabilities, want) {
		t.Fatalf("legacy vocabulary differs from v1: %v", KnownCapabilities)
	}

	KnownCapabilities[0] = CapabilityUI
	if got := CapabilitiesV1(); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy element mutation changed v1 vocabulary: %v", got)
	}
	KnownCapabilities = []Capability{CapabilityUI}
	if got := CapabilitiesV1(); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy reassignment changed v1 vocabulary: %v", got)
	}
}

func TestCapabilitiesV1ExcludesUI(t *testing.T) {
	for _, capability := range CapabilitiesV1() {
		if capability == CapabilityUI {
			t.Fatal("UI capability must remain exclusive to manifest API v2")
		}
	}
}

func TestAIConfigIsAnAPIV2OnlyCapability(t *testing.T) {
	if !slices.Contains(CapabilitiesV2Only(), Capability("ai.config")) {
		t.Fatal("ai.config missing from CapabilitiesV2Only")
	}
	if slices.Contains(CapabilitiesV1(), Capability("ai.config")) {
		t.Fatal("ai.config must stay exclusive to manifest API v2")
	}
}

func TestBackupIsAnAPIV2OnlyCapability(t *testing.T) {
	if !slices.Contains(CapabilitiesV2Only(), Capability("backup")) {
		t.Fatal("backup missing from CapabilitiesV2Only")
	}
	if slices.Contains(CapabilitiesV1(), Capability("backup")) {
		t.Fatal("backup must stay exclusive to manifest API v2")
	}
}

func TestRemoteContextIsAnAPIV2OnlyCapability(t *testing.T) {
	if CapabilityRemoteContext != "remote.context" || !slices.Contains(CapabilitiesV2Only(), CapabilityRemoteContext) {
		t.Fatal("remote.context missing from CapabilitiesV2Only")
	}
	if slices.Contains(CapabilitiesV1(), CapabilityRemoteContext) {
		t.Fatal("remote.context must stay exclusive to manifest API v2")
	}
}

func TestUIWasmIsAnAPIV2OnlyCapability(t *testing.T) {
	if CapabilityUIWasm != "ui.wasm" || !slices.Contains(CapabilitiesV2Only(), CapabilityUIWasm) {
		t.Fatal("ui.wasm missing from CapabilitiesV2Only")
	}
	if slices.Contains(CapabilitiesV1(), CapabilityUIWasm) {
		t.Fatal("ui.wasm must stay exclusive to manifest API v2")
	}
}
