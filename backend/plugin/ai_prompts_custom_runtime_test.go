package plugin

import (
	"encoding/json"
	"mincode/app/plugins/api"
	"testing"
)

func TestCustomAiPromptsOverlayAndAdmission(t *testing.T) {
	useTemporaryPluginData(t)
	manifest := Manifest{ID: "custom-test", contributions: map[string]*resolvedContribution{aiPromptPointKey: {Entries: []any{AiPromptContribution{ID: "builtin", Kind: "prompt", Title: "Builtin", Body: "Builtin"}}}}}
	runtimeHost := newProcessRuntime("", manifest, nil, nil)
	runtimeHost.capabilities[api.CapabilityUI] = struct{}{}
	runtimeHost.capabilities[api.CapabilityStorage] = struct{}{}
	if !customAiPromptsGate(runtimeHost) {
		t.Fatal("valid declaration denied")
	}
	delete(runtimeHost.capabilities, api.CapabilityUI)
	if customAiPromptsGate(runtimeHost) {
		t.Fatal("missing UI capability admitted")
	}
	_, err := mutateCustomAiPrompts(manifest, "save", json.RawMessage(`{"revision":0,"prompt":{"title":"Persistent","body":"Text","enabled":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	record := &pluginRecord{manifest: &manifest}
	overlay := customAiPromptsOverlay(record).(customAiPromptsRuntime)
	if overlay.Revision != 1 || len(overlay.Entries) != 1 || overlay.Entries[0].Title != "Persistent" {
		t.Fatalf("overlay: %+v", overlay)
	}
	if err := pluginKVSetRaw(manifest.ID, customAiPromptsKey, json.RawMessage(`{"version":9}`)); err != nil {
		t.Fatal(err)
	}
	overlay = customAiPromptsOverlay(record).(customAiPromptsRuntime)
	if overlay.Error == "" || len(overlay.Entries) != 0 {
		t.Fatalf("invalid overlay: %+v", overlay)
	}
}

func TestCustomAiPromptsConcurrentRevision(t *testing.T) {
	useTemporaryPluginData(t)
	manifest := Manifest{ID: "custom-test"}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := mutateCustomAiPrompts(manifest, "save", json.RawMessage(`{"revision":0,"prompt":{"title":"P","body":"x"}}`))
			results <- err
		}()
	}
	a, b := <-results, <-results
	if (a == nil) == (b == nil) {
		t.Fatalf("expected one success, got %v %v", a, b)
	}
	loaded, err := readCustomAiPrompts(manifest)
	if err != nil || len(loaded.Prompts) != 1 || loaded.Revision != 1 {
		t.Fatalf("concurrent write: %+v %v", loaded, err)
	}
}
