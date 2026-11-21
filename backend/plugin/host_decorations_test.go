package plugin

import (
	"encoding/json"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

// useDecorationsRefreshClock replaces the refresh hint clock and event sink
// and forgets earlier hints; it returns the clock and the emitted plugin IDs.
func useDecorationsRefreshClock(t *testing.T) (*time.Time, *[]string) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	var emitted []string
	previousNow, previousEmit := decorationsRefreshNow, emitDecorationsRefresh
	decorationsRefreshNow = func() time.Time { return now }
	emitDecorationsRefresh = func(pluginID string) { emitted = append(emitted, pluginID) }
	forgetDecorationsRefreshes()
	t.Cleanup(func() {
		decorationsRefreshNow, emitDecorationsRefresh = previousNow, previousEmit
		forgetDecorationsRefreshes()
	})
	return &now, &emitted
}

func forgetDecorationsRefreshes() {
	decorationsRefreshes.mu.Lock()
	clear(decorationsRefreshes.last)
	decorationsRefreshes.mu.Unlock()
}

func TestDecorationsRefreshHintGatedAndRateLimited(t *testing.T) {
	now, emitted := useDecorationsRefreshClock(t)
	var result any
	var rpcErr *rpcError
	respond := func(value any, err *rpcError) { result, rpcErr = value, err }
	call := func(runtimeHost *processRuntime, params string) {
		t.Helper()
		result, rpcErr = nil, nil
		message := rpcEnvelope{Method: decorationsRefreshMethod}
		if params != "" {
			message.Params = json.RawMessage(params)
		}
		if !runtimeHost.serveRegisteredHostMethod(message, respond) {
			t.Fatalf("%s not registered", decorationsRefreshMethod)
		}
	}

	declared, err := readManifest(writeFileDecorationsManifest(t, true, `[{"id":"lint","title":"Lint","action":"decorate"}]`))
	if err != nil {
		t.Fatal(err)
	}
	// Gate: a plugin that does not declare fileDecorations sees no such method.
	undeclared, err := readManifest(writeTestManifestV2(t, func(manifest *Manifest) {
		manifest.Capabilities = append(manifest.Capabilities, api.CapabilityWorkspaceRead)
	}))
	if err != nil {
		t.Fatal(err)
	}
	call(newProcessRuntime(t.TempDir(), undeclared, nil, nil), `{}`)
	if rpcErr == nil || rpcErr.Code != -32601 {
		t.Fatalf("undeclared plugin: %+v", rpcErr)
	}
	// Capability: workspace.read is required.
	withoutRead := newProcessRuntime(t.TempDir(), declared, nil, nil)
	delete(withoutRead.capabilities, api.CapabilityWorkspaceRead)
	call(withoutRead, `{}`)
	if rpcErr == nil || rpcErr.Code != -32001 {
		t.Fatalf("without workspace.read: %+v", rpcErr)
	}

	runtimeHost := newProcessRuntime(t.TempDir(), declared, nil, nil)
	for _, params := range []string{`{"x":1}`, `[]`, `"refresh"`, `{"directories":["src"]}`} {
		call(runtimeHost, params)
		if rpcErr == nil || rpcErr.Code != -32602 {
			t.Fatalf("params %s: %+v", params, rpcErr)
		}
	}
	if len(*emitted) != 0 {
		t.Fatalf("invalid calls emitted %v", *emitted)
	}
	call(runtimeHost, `{}`)
	if rpcErr != nil || result != (decorationsRefreshResult{Scheduled: true}) || len(*emitted) != 1 || (*emitted)[0] != declared.ID {
		t.Fatalf("first hint: %+v %+v %v", result, rpcErr, *emitted)
	}
	*now = now.Add(1999 * time.Millisecond)
	call(runtimeHost, ``)
	if rpcErr != nil || result != (decorationsRefreshResult{Scheduled: false}) || len(*emitted) != 1 {
		t.Fatalf("second hint within 2 s: %+v %+v %v", result, rpcErr, *emitted)
	}
	*now = now.Add(time.Millisecond)
	call(runtimeHost, `null`)
	if rpcErr != nil || result != (decorationsRefreshResult{Scheduled: true}) || len(*emitted) != 2 {
		t.Fatalf("hint after 2 s: %+v %+v %v", result, rpcErr, *emitted)
	}
	encoded, _ := json.Marshal(result)
	if string(encoded) != `{"scheduled":true}` {
		t.Fatalf("encoded = %s", encoded)
	}

	// Uninstalling the plugin forgets its last hint.
	forgetDecorationsRefresh(nil, declared.ID, removalReasonUninstall)
	call(runtimeHost, `{}`)
	if result != (decorationsRefreshResult{Scheduled: true}) || len(*emitted) != 3 {
		t.Fatalf("after removal: %+v %v", result, *emitted)
	}
}
