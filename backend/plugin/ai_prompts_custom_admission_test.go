package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"mincode/app/backend/core"
	"mincode/app/plugins/api"
	"os/exec"
	"testing"
)

func TestCustomAiPromptsRejectStaleAndInactiveRuntime(t *testing.T) {
	useTemporaryPluginData(t)
	manifest := Manifest{ID: "custom-test", contributions: map[string]*resolvedContribution{aiPromptPointKey: {}}}
	manager := newPluginManager()
	record := &pluginRecord{id: manifest.ID, enabled: true, manifest: &manifest}
	rt := manager.newRecordRuntime(record, "", manifest)
	rt.capabilities[api.CapabilityUI] = struct{}{}
	rt.capabilities[api.CapabilityStorage] = struct{}{}
	rt.generation = 5
	rt.cmd = &exec.Cmd{}
	rt.runContext = context.Background()
	record.runtime = rt
	request := json.RawMessage(`{"revision":0,"prompt":{"title":"Allowed","body":"text","enabled":true}}`)
	for _, setup := range []func(){
		func() { record.enabled = false },
		func() { record.enabled = true; record.runtime = nil },
		func() { record.runtime = rt; rt.generation = 6 },
		func() { rt.generation = 5; rt.stopping = true },
		func() {
			rt.stopping = false
			record.scope = pluginScopeWorkspace
			record.workspaces = map[string]struct{}{"other": {}}
		},
	} {
		setup()
		if _, err := rt.customAiPromptRequest(5, "save", request); err == nil {
			t.Fatal("inactive runtime wrote data")
		}
	}
	// Generic storage must obey the same commit-time identity and generation.
	for _, method := range []string{"host.storage.set", "host.storage.delete"} {
		for _, generation := range []uint64{4, 5} {
			params := json.RawMessage(`{"key":"ai-prompts.custom.v1","value":{"version":1,"revision":0,"prompts":[]}}`)
			var denied *rpcError
			rt.handleStorageRequest(rpcEnvelope{Method: method, Params: params, hostGeneration: generation}, func(_ any, e *rpcError) { denied = e })
			if denied == nil {
				t.Fatalf("inactive generic storage admitted: %s %d", method, generation)
			}
		}
	}
	record.scope = pluginScopeGlobal
	events := 0
	core.SetEmitter(func(name string, data any) {
		if name != pluginContributionsChangedEvent {
			return
		}
		events++
		if !record.mu.TryLock() {
			t.Error("contribution event emitted while record locked")
			return
		}
		record.mu.Unlock()
		if !rt.mu.TryLock() {
			t.Error("contribution event emitted while runtime locked")
			return
		}
		rt.mu.Unlock()
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	result, err := rt.customAiPromptRequest(5, "save", request)
	if err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("successful save emitted %d events", events)
	}
	if result.(customAiPromptSnapshot).Revision != 1 {
		t.Fatal(result)
	}
	if _, err := rt.customAiPromptRequest(5, "list", json.RawMessage(`{"pluginId":"other"}`)); err == nil {
		t.Fatal("foreign identity accepted")
	}
	// Reusing an older document revision cannot make an open editor overwrite it.
	var rawErr *rpcError
	rt.handleStorageRequest(rpcEnvelope{Method: "host.storage.set", hostGeneration: 5, Params: json.RawMessage(`{"key":"ai-prompts.custom.v1","value":{"version":1,"revision":0,"prompts":[]}}`)}, func(_ any, e *rpcError) { rawErr = e })
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	if _, err := rt.customAiPromptRequest(5, "save", json.RawMessage(`{"revision":1,"prompt":{"title":"Stale","body":"x"}}`)); err == nil {
		t.Fatal("generic storage reused old revision")
	}
	rt.handleStorageRequest(rpcEnvelope{Method: "host.storage.set", hostGeneration: 5, Params: json.RawMessage(`{"key":"ai-prompts.custom.v1"}`)}, func(_ any, e *rpcError) { rawErr = e })
	if rawErr == nil {
		t.Fatal("missing generic storage value accepted")
	}
	loaded, readErr := readCustomAiPrompts(Manifest{ID: "other"})
	if readErr != nil || len(loaded.Prompts) != 0 {
		t.Fatalf("foreign namespace changed: %+v %v", loaded, readErr)
	}
}

func TestCustomAiPromptsBuiltinBudgetAndFailedWrite(t *testing.T) {
	useTemporaryPluginData(t)
	builtins := []any{}
	for i := 0; i < 15; i++ {
		builtins = append(builtins, AiPromptContribution{ID: fmt.Sprintf("builtin-%d", i), Kind: "prompt", Title: "Builtin", Body: "x"})
	}
	manifest := Manifest{ID: "budget-test", contributions: map[string]*resolvedContribution{aiPromptPointKey: {Entries: builtins}}}
	for i := 0; i < 17; i++ {
		raw, _ := json.Marshal(map[string]any{"revision": i, "prompt": customAiPrompt{Title: "P", Body: "x"}})
		if _, err := mutateCustomAiPrompts(manifest, "save", raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mutateCustomAiPrompts(manifest, "save", json.RawMessage(`{"revision":17,"prompt":{"title":"Overflow","body":"x"}}`)); err == nil {
		t.Fatal("builtin capacity ignored")
	}
	snapshot, err := readCustomAiPrompts(manifest)
	if err != nil || snapshot.Capacity != 17 || snapshot.Revision != 17 {
		t.Fatalf("capacity: %+v %v", snapshot, err)
	}
	// Exhaust the existing KV quota: a failed persist must not announce a change.
	for i := 0; i < pluginKVMaxKeys; i++ {
		err = pluginKVSetRaw("quota-test", fmt.Sprintf("key-%d", i), json.RawMessage(`1`))
		if err != nil {
			t.Fatal(err)
		}
	}
	before := contributionRevision.Load()
	if _, err := mutateCustomAiPrompts(Manifest{ID: "quota-test"}, "save", json.RawMessage(`{"revision":0,"prompt":{"title":"P","body":"x"}}`)); err == nil {
		t.Fatal("quota write unexpectedly succeeded")
	}
	if contributionRevision.Load() != before {
		t.Fatal("failed persist announced success")
	}
}
