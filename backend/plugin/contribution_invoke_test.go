package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

func contributionInvokeManager(t *testing.T) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := activationManager(t, "")
	if err := manager.SetEnabled(context.Background(), record.id, true); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{"formatters": {}}
	record.mu.Unlock()
	return manager, record
}

func contributionErrorCode(err error) string {
	var coded *contributionError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func assertNoContributionRequestsLeft(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		contributionRequests.mu.Lock()
		requests, slots := len(contributionRequests.requests), len(contributionRequests.slots)
		contributionRequests.mu.Unlock()
		if requests == 0 && slots == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("leaked %d requests and %d slots", requests, slots)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestOptInFeatureOffers(t *testing.T) {
	for _, capabilities := range []map[api.Capability]struct{}{nil, {api.CapabilityWorkspaceRead: {}}} {
		offer := offeredRuntimeFeatures(capabilities)
		_, canRead := capabilities[api.CapabilityWorkspaceRead]
		if version, offered := offer["searchFileTargets"]; offered != canRead || (offered && version != 1) {
			t.Errorf("searchFileTargets offer = %d/%v, workspace.read = %v", version, offered, canRead)
		}
		if offer["serviceCallerMetadata"] != 1 {
			t.Error("serviceCallerMetadata was not offered at version 1")
		}
		for _, reply := range []struct {
			raw  string
			want bool
		}{
			{`{}`, false},
			{`{"features":{"searchFileTargets":2,"serviceCallerMetadata":2}}`, false},
			{`{"features":{"searchFileTargets":true,"serviceCallerMetadata":true}}`, false},
			{`{"features":{"searchFileTargets":1,"serviceCallerMetadata":1}}`, true},
		} {
			features := negotiateRuntimeFeatures(offer, []byte(reply.raw))
			if features.has("searchFileTargets") != (reply.want && canRead) || features.has("serviceCallerMetadata") != reply.want {
				t.Errorf("negotiation of %s with workspace.read=%v = %v", reply.raw, canRead, features.versions)
			}
		}
	}
}

// This real subprocess echoes negotiated features and payloads. If a rejected
// payload reaches plugin.invoke, its marker file makes the dispatch observable.
func TestPrepareAdmittedHelperProcess(t *testing.T) {
	if os.Getenv("MINCODE_PLUGIN_ID") != "runtime.test" {
		return
	}
	scanner, encoder := bufio.NewScanner(os.Stdin), json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request rpcEnvelope
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		response := rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`true`)}
		switch request.Method {
		case "plugin.initialize":
			var params struct {
				Features map[string]int `json:"features"`
			}
			_ = json.Unmarshal(request.Params, &params)
			response.Result, _ = json.Marshal(map[string]any{"features": params.Features})
		case "plugin.invoke":
			_ = os.WriteFile("invoked", []byte("yes"), 0600)
			var params struct {
				Payload string `json:"payload"`
			}
			_ = json.Unmarshal(request.Params, &params)
			response.Result, _ = json.Marshal(map[string]any{"result": params.Payload})
		case "plugin.shutdown":
			_ = encoder.Encode(response)
			os.Exit(0)
		}
		if encoder.Encode(response) != nil {
			os.Exit(3)
		}
	}
	os.Exit(0)
}

func TestPrepareAdmittedFeaturesAndLimit(t *testing.T) {
	for _, scenario := range []string{"features", "both builders", "too large", "invalid JSON", "cancelled", "restarted", "action changed", "legacy prepare", "legacy payload"} {
		t.Run(scenario, func(t *testing.T) {
			manager, record := contributionInvokeManager(t)
			record.mu.Lock()
			record.manifest.Runtime.Args = []string{"-test.run=^TestPrepareAdmittedHelperProcess$"}
			record.manifest.Capabilities = []api.Capability{api.CapabilityWorkspaceRead}
			record.runtime = manager.newRecordRuntime(record, record.root, *record.manifest)
			record.mu.Unlock()
			call := contributedActionCall{PluginID: record.id, Point: "formatters", ActionID: "run", RequestID: "prepare-admitted-01"}
			built := 0
			call.PrepareAdmitted = func(ctx context.Context, admission *contributedAdmission) ([]byte, error) {
				built++
				if ctx != admission.Ctx || admission.Generation != 1 || !admission.Runtime.featureAtGeneration(admission.Generation, "searchFileTargets") || !admission.Runtime.featureAtGeneration(admission.Generation, "serviceCallerMetadata") {
					t.Error("builder did not observe features from the initialized admission")
				}
				if admission.Runtime.featureAtGeneration(admission.Generation+1, "searchFileTargets") {
					t.Error("feature leaked into a different generation")
				}
				switch scenario {
				case "too large":
					return []byte(`"` + strings.Repeat("x", 1023) + `"`), nil
				case "invalid JSON":
					return []byte(`{`), nil
				case "cancelled":
					cancelContributionRequest(call.RequestID)
				case "restarted":
					if err := admission.Runtime.Stop(context.Background()); err != nil {
						t.Error(err)
					}
					if admission.Runtime.featureAtGeneration(admission.Generation, "searchFileTargets") {
						t.Error("stopped feature visible")
					}
					if err := admission.Runtime.Start(context.Background()); err != nil {
						t.Error(err)
					}
				case "action changed":
					record.mu.Lock()
					record.manifest.Contributes.Actions = nil
					record.mu.Unlock()
				}
				return []byte(`{"bound":true}`), nil
			}
			wantCode, wantPayload := "", `"{\"bound\":true}"`
			switch scenario {
			case "both builders":
				call.Prepare = func(context.Context) ([]byte, error) { t.Error("conflicting builder ran"); return nil, nil }
				wantCode = contributionErrInvalidRequest
			case "too large":
				wantCode = contributionErrTooLarge
			case "invalid JSON":
				wantCode = contributionErrInvalidRequest
			case "cancelled":
				wantCode = contributionErrCancelled
			case "restarted", "action changed":
				wantCode = contributionErrUnavailable
			case "legacy prepare":
				call.PrepareAdmitted = nil
				call.Prepare = func(context.Context) ([]byte, error) { return []byte("legacy raw"), nil }
				wantPayload = `"legacy raw"`
			case "legacy payload":
				call.PrepareAdmitted = nil
				call.Payload = []byte("legacy raw")
				wantPayload = `"legacy raw"`
			}
			result, err := manager.invokeContributedAction(context.Background(), call)
			if contributionErrorCode(err) != wantCode || (wantCode == "" && (err != nil || string(result) != wantPayload)) {
				t.Fatalf("result=%s error=%v, want payload=%s code=%s", result, err, wantPayload, wantCode)
			}
			if wantCode != "" {
				if _, err := os.Stat(record.root + "/invoked"); !os.IsNotExist(err) {
					t.Fatalf("rejected payload reached plugin.invoke: %v", err)
				}
			}
			if scenario == "both builders" && (built != 0 || record.runtime.Generation() != 0) {
				t.Fatal("conflicting builders activated the process")
			}
			assertNoContributionRequestsLeft(t)
		})
	}
}

func TestInvokeContributedAction(t *testing.T) {
	manager, record := contributionInvokeManager(t)
	base := contributedActionCall{PluginID: record.id, Point: "formatters", EntryID: "fmt", ActionID: "run", RequestID: "request-0001", Payload: []byte(`{"text":"x"}`)}
	var validateCalls atomic.Int32
	call := base
	call.Validate = func(*pluginRecord) error { validateCalls.Add(1); return nil }
	result, err := manager.invokeContributedAction(context.Background(), call)
	if err != nil || string(result) != `{"ok":true}` {
		t.Fatalf("result = %s, %v", result, err)
	}
	// Once up front, once before lazy activation, once before dispatch.
	if validateCalls.Load() != 3 {
		t.Fatalf("validate ran %d times", validateCalls.Load())
	}
	assertNoContributionRequestsLeft(t)

	cases := map[string]struct {
		mutate func(*contributedActionCall)
		code   string
	}{
		"bad request id":    {func(c *contributedActionCall) { c.RequestID = "short" }, contributionErrInvalidRequest},
		"unknown plugin":    {func(c *contributedActionCall) { c.PluginID = "missing.plugin" }, contributionErrNotFound},
		"undeclared action": {func(c *contributedActionCall) { c.ActionID = "nope" }, contributionErrNotFound},
		"payload too large": {func(c *contributedActionCall) { c.Payload = []byte(strings.Repeat("x", 1025)) }, contributionErrTooLarge},
		"result too large":  {func(c *contributedActionCall) { c.MaxResultBytes = 5 }, contributionErrInvalidResult},
		"plain validate error": {func(c *contributedActionCall) {
			c.Validate = func(*pluginRecord) error { return errors.New("entry gone") }
		}, contributionErrUnavailable},
		"coded validate error": {func(c *contributedActionCall) {
			c.Validate = func(*pluginRecord) error { return newContributionError(contributionErrNotFound, "entry gone") }
		}, contributionErrNotFound},
		"timeout": {func(c *contributedActionCall) { c.Timeout = time.Nanosecond }, contributionErrTimeout},
	}
	for name, testCase := range cases {
		call := base
		testCase.mutate(&call)
		_, err := manager.invokeContributedAction(context.Background(), call)
		if code := contributionErrorCode(err); code != testCase.code || !strings.HasPrefix(err.Error(), testCase.code+": ") {
			t.Fatalf("%s: err = %v, want code %s", name, err, testCase.code)
		}
		assertNoContributionRequestsLeft(t)
	}

	// Cancellation between admission and dispatch (the last Validate run).
	for name, want := range map[string]string{"cancel": contributionErrCancelled, "lifecycle": contributionErrUnavailable} {
		call := base
		var runs atomic.Int32
		call.Validate = func(record *pluginRecord) error {
			if runs.Add(1) == 3 {
				if name == "cancel" {
					if !cancelContributionRequest(call.RequestID) {
						t.Error("request not registered")
					}
				} else {
					cancelPluginContributionRequests(record.id)
				}
			}
			return nil
		}
		if _, err := manager.invokeContributedAction(context.Background(), call); contributionErrorCode(err) != want {
			t.Fatalf("%s: err = %v, want %s", name, err, want)
		}
		assertNoContributionRequestsLeft(t)
	}

	// A full per-plugin, per-point semaphore and a duplicate request ID are busy.
	slot := record.id + "\x00formatters"
	contributionRequests.mu.Lock()
	contributionRequests.slots[slot] = 1
	contributionRequests.mu.Unlock()
	if _, err := manager.invokeContributedAction(context.Background(), base); contributionErrorCode(err) != contributionErrBusy {
		t.Fatalf("full semaphore: %v", err)
	}
	other := base
	other.Point = "dashboardWidgets"
	if _, err := manager.invokeContributedAction(context.Background(), other); err != nil {
		t.Fatalf("other point shares the semaphore: %v", err)
	}
	contributionRequests.mu.Lock()
	delete(contributionRequests.slots, slot)
	contributionRequests.requests[base.RequestID] = &contributionRequest{pluginID: record.id, cancel: func() {}}
	contributionRequests.mu.Unlock()
	if _, err := manager.invokeContributedAction(context.Background(), base); contributionErrorCode(err) != contributionErrBusy {
		t.Fatalf("duplicate request: %v", err)
	}
	contributionRequests.mu.Lock()
	delete(contributionRequests.requests, base.RequestID)
	contributionRequests.mu.Unlock()
	assertNoContributionRequestsLeft(t)

	// Contributions that are not listed, and plugins without a process runtime.
	record.mu.Lock()
	record.manifest.contributions = nil
	record.mu.Unlock()
	if _, err := manager.invokeContributedAction(context.Background(), base); contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("unlisted: %v", err)
	}
	// plugins.invoke targets contributes.actions and needs no contribution point.
	actionsOnly := base
	actionsOnly.ActionsOnly = true
	if result, err := manager.invokeContributedAction(context.Background(), actionsOnly); err != nil || string(result) != `{"ok":true}` {
		t.Fatalf("actions only without points = %s, %v", result, err)
	}
	assertNoContributionRequestsLeft(t)
	record.mu.Lock()
	record.manifest.Runtime.Type = pluginRuntimeDeclarative
	record.mu.Unlock()
	if _, err := manager.invokeContributedAction(context.Background(), base); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("declarative: %v", err)
	}
	builtinManager, builtinRecord := testManagerWithPlugin(&testLifecyclePlugin{}, true)
	builtinCall := base
	builtinCall.PluginID = builtinRecord.id
	if _, err := builtinManager.invokeContributedAction(context.Background(), builtinCall); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("builtin: %v", err)
	}
}

func TestContributedActionSlotGroupDefaultKey(t *testing.T) {
	plain := contributedActionCall{PluginID: "acme.fmt", Point: "formatters"}
	if key := contributedActionSlotKey(plain); key != "acme.fmt\x00formatters" {
		t.Fatalf("empty SlotGroup key = %q, want the pre-SlotGroup key", key)
	}
	grouped := plain
	grouped.SlotGroup = "passive"
	if key := contributedActionSlotKey(grouped); key != "acme.fmt\x00formatters\x00passive" {
		t.Fatalf("grouped key = %q", key)
	}

	for _, heldGroup := range []string{"", "passive"} {
		name := heldGroup
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			manager, record := contributionInvokeManager(t)
			if err := manager.startRecord(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			base := contributedActionCall{PluginID: record.id, Point: "formatters", EntryID: "fmt", ActionID: "run", Payload: []byte(`{"text":"x"}`)}
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			releaseCall := func() { once.Do(func() { close(release) }) }
			held := base
			held.RequestID, held.SlotGroup = "slot-held-0001", heldGroup
			held.Prepare = func(ctx context.Context) ([]byte, error) {
				close(entered) // admission really owns this group's slot now
				select {
				case <-release:
					return held.Payload, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			var heldResult json.RawMessage
			var heldErr error
			go func() {
				heldResult, heldErr = manager.invokeContributedAction(context.Background(), held)
				close(done)
			}()
			t.Cleanup(func() {
				releaseCall()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("held contribution did not join")
				}
				assertNoContributionRequestsLeft(t)
			})
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("held contribution never entered preparation")
			}
			blocked := base
			blocked.RequestID, blocked.SlotGroup = "slot-blocked-0001", heldGroup
			if _, err := manager.invokeContributedAction(context.Background(), blocked); contributionErrorCode(err) != contributionErrBusy {
				t.Fatalf("full %s group: %v", name, err)
			}
			free := base
			free.RequestID = "slot-free-0001"
			if heldGroup == "" {
				free.SlotGroup = "passive"
			}
			if result, err := manager.invokeContributedAction(context.Background(), free); err != nil || string(result) != `{"ok":true}` {
				t.Fatalf("other group while %s is full = %s, %v", name, result, err)
			}
			releaseCall()
			select {
			case <-done:
				if heldErr != nil || string(heldResult) != `{"ok":true}` {
					t.Fatalf("released %s call = %s, %v", name, heldResult, heldErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("released contribution did not return")
			}
		})
	}
}

func TestPluginHostContributionCancelHandler(t *testing.T) {
	if _, err := pluginHostContributionCancelHandler("", `{"requestId":"bad id"}`); contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Fatalf("invalid id: %v", err)
	}
	if _, err := pluginHostContributionCancelHandler("", `nope`); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if raw, err := pluginHostContributionCancelHandler("", `{"requestId":"request-unknown"}`); err != nil || raw != `{"cancelled":false}` {
		t.Fatalf("unknown = %s, %v", raw, err)
	}
	cancelled := false
	contributionRequests.mu.Lock()
	contributionRequests.requests["request-known"] = &contributionRequest{pluginID: "p", cancel: func() { cancelled = true }}
	contributionRequests.mu.Unlock()
	if raw, err := pluginHostContributionCancelHandler("", `{"requestId":"request-known"}`); err != nil || raw != `{"cancelled":true}` || !cancelled {
		t.Fatalf("known = %s, %v, %v", raw, err, cancelled)
	}
	assertNoContributionRequestsLeft(t)
}

func TestClassifyContributionInvokeErrorMapsSDKBusy(t *testing.T) {
	request := &contributionRequest{pluginID: "example"}
	busy := classifyContributionInvokeError(context.Background(), request, &processRPCError{Code: pluginRPCBusyCode, Message: "already running"})
	var coded *contributionError
	if !errors.As(busy, &coded) || coded.Code != contributionErrBusy {
		t.Fatalf("SDK busy = %v, want %s", busy, contributionErrBusy)
	}
	other := classifyContributionInvokeError(context.Background(), request, &processRPCError{Code: -32000, Message: "boom"})
	if !errors.As(other, &coded) || coded.Code != contributionErrPluginError {
		t.Fatalf("plugin error = %v, want %s", other, contributionErrPluginError)
	}
}
