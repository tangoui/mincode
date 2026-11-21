package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

type fakePluginCalls struct {
	grants map[string][]string
	calls  []contributedActionCall
}

func usePluginCallFakes(t *testing.T, actions map[string]ManifestUIAction) *fakePluginCalls {
	t.Helper()
	fake := &fakePluginCalls{grants: map[string][]string{}}
	previousGranted, previousGrant, previousInvoke, previousTarget := pluginCallGranted, grantPluginCall, pluginCallInvoke, pluginCallTarget
	previousSkip := pluginCallFullTrustSkip
	// The fake plugins are not installed anywhere, so nothing is trusted.
	pluginCallFullTrustSkip = func(string, string, FullTrustAuditEntry) bool { return false }
	pluginCallGranted = func(caller, target string) bool {
		for _, item := range fake.grants[caller] {
			if item == target {
				return true
			}
		}
		return false
	}
	grantPluginCall = func(caller, target string) error {
		fake.grants[caller] = append(fake.grants[caller], target)
		return nil
	}
	pluginCallInvoke = func(_ context.Context, call contributedActionCall) (json.RawMessage, error) {
		fake.calls = append(fake.calls, call)
		return json.RawMessage(`{"ok":true}`), nil
	}
	pluginCallTarget = func(target, action string) (pluginCallTargetInfo, error) {
		declared, ok := actions[target+"/"+action]
		if !ok {
			return pluginCallTargetInfo{}, errors.New("no such action")
		}
		return pluginCallTargetInfo{Name: "Work Report", Action: declared}, nil
	}
	t.Cleanup(func() {
		pluginCallGranted, grantPluginCall, pluginCallInvoke, pluginCallTarget = previousGranted, previousGrant, previousInvoke, previousTarget
		pluginCallFullTrustSkip = previousSkip
	})
	return fake
}

func TestHostPluginsInvokeAsksOnceForReadAndEveryTimeForWrite(t *testing.T) {
	events := captureHostUI(t)
	schema := map[string]any{"type": "object", "properties": map[string]any{"period": map[string]any{"type": "string"}}, "additionalProperties": false}
	fake := usePluginCallFakes(t, map[string]ManifestUIAction{
		"report/activity":   {ID: "activity", AICallable: true, Risk: "read", InputSchema: schema},
		"report/saveReport": {ID: "saveReport", AICallable: true, Risk: "write"},
		"report/fetch":      {ID: "fetch", AICallable: true, Risk: "network"},
	})
	caller := uiRuntime("team-chat")

	// First read call: the user allows it permanently.
	go answerNext(t, events, func(event promptEvent) promptReply {
		if event.Kind != "pluginCall" || event.Call == nil || event.Call.TargetPluginID != "report" || event.Call.Risk != "read" || len(event.Items) != 2 {
			t.Errorf("approval %+v %+v", event, event.Call)
		}
		return promptReply{IDs: []string{"always"}}
	})
	result, err := uiCall(hostPluginsInvoke, caller, `{"pluginId":"report","action":"activity","payload":{"period":"thisWeek"}}`)
	if err != nil || string(result.(map[string]any)["result"].(json.RawMessage)) != `{"ok":true}` {
		t.Fatalf("%v %v", result, err)
	}
	call := fake.calls[0]
	if call.PluginID != "report" || call.EntryID != "team-chat" || call.Point != pluginCallPoint || string(call.Payload) != `{"period":"thisWeek"}` {
		t.Fatalf("call %+v", call)
	}
	// Granted: the next read call runs without a prompt.
	if _, err := uiCall(hostPluginsInvoke, caller, `{"pluginId":"report","action":"activity"}`); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("calls %d", len(fake.calls))
	}
	// Write actions are confirmed every time and cannot be remembered.
	go answerNext(t, events, func(event promptEvent) promptReply {
		if len(event.Items) != 1 || event.Items[0].ID != "once" {
			t.Errorf("write approval offers %+v", event.Items)
		}
		return promptReply{Cancelled: true}
	})
	if _, err := uiCall(hostPluginsInvoke, caller, `{"pluginId":"report","action":"saveReport"}`); err == nil || err.Code != -32003 {
		t.Fatalf("declined call: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatal("declined call ran")
	}
	// Network actions are confirmed every time too.
	go answerNext(t, events, func(event promptEvent) promptReply {
		if len(event.Items) != 1 {
			t.Errorf("network approval offers %+v", event.Items)
		}
		return promptReply{IDs: []string{"once"}}
	})
	if _, err := uiCall(hostPluginsInvoke, caller, `{"pluginId":"report","action":"fetch"}`); err != nil {
		t.Fatal(err)
	}
	for name, params := range map[string]string{
		"bad timeout":    `{"pluginId":"report","action":"activity","timeoutMs":1}`,
		"self":           `{"pluginId":"team-chat","action":"x"}`,
		"unknown action": `{"pluginId":"report","action":"deleteReport"}`,
		"schema":         `{"pluginId":"report","action":"activity","payload":{"period":1}}`,
		"extra field":    `{"pluginId":"report","action":"activity","payload":{"secret":"x"}}`,
		"not json":       `{"pluginId":"report","action":"activity","payload":"{"}`,
	} {
		if _, err := uiCall(hostPluginsInvoke, caller, params); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestPluginCallApprovalRepliesAreValidated(t *testing.T) {
	broker := &promptBroker{pending: map[string]*pendingPrompt{"p": {pluginID: "a", kind: "pluginCall", items: map[string]bool{"once": true}, reply: make(chan promptReply, 1)}}, byPlugin: map[string]string{}}
	for _, bad := range []promptReply{{IDs: []string{"always"}}, {IDs: []string{"once", "once"}}, {}} {
		if broker.answer("p", bad) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if err := broker.answer("p", promptReply{IDs: []string{"once"}}); err != nil {
		t.Fatal(err)
	}
}

func TestPluginCallValidateRejectsChangedRiskAndPayload(t *testing.T) {
	captureHostUI(t)
	schema := map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}, "additionalProperties": false}
	fake := usePluginCallFakes(t, map[string]ManifestUIAction{"report/activity": {ID: "activity", AICallable: true, Risk: "read", InputSchema: schema}})
	fake.grants["team-chat"] = []string{"report"}
	var validate func(*pluginRecord) error
	pluginCallInvoke = func(_ context.Context, call contributedActionCall) (json.RawMessage, error) {
		validate = call.Validate
		return json.RawMessage(`{}`), nil
	}
	if _, err := uiCall(hostPluginsInvoke, uiRuntime("team-chat"), `{"pluginId":"report","action":"activity","payload":{"n":1}}`); err != nil {
		t.Fatal(err)
	}
	record := func(action ManifestUIAction) *pluginRecord {
		return &pluginRecord{manifest: &Manifest{Contributes: ManifestContributions{Actions: []ManifestUIAction{action}}}}
	}
	if err := validate(record(ManifestUIAction{ID: "activity", AICallable: true, Risk: "read", Permissions: []api.Capability{}, InputSchema: schema})); err != nil {
		t.Fatalf("unchanged action refused: %v", err)
	}
	if err := validate(record(ManifestUIAction{ID: "activity", AICallable: true, Risk: "write", Permissions: []api.Capability{}, InputSchema: schema})); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("risk change: %v", err)
	}
	strict := map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "string"}}, "additionalProperties": false}
	if err := validate(record(ManifestUIAction{ID: "activity", AICallable: true, Risk: "read", Permissions: []api.Capability{}, InputSchema: strict})); contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Fatalf("schema change: %v", err)
	}
}

func TestPluginCallGrantKeysAndTargetCleanup(t *testing.T) {
	if key := pluginCallGrantsKVKey("acme"); !strings.Contains(key, "/") || pluginIDPattern.MatchString(key) {
		t.Fatalf("grant key %q may collide with plugin ids", key)
	}
	manager, record := contributionInvokeManager(t)
	if err := writePluginCallGrants(record.id, pluginCallGrants{Targets: []string{"report", "other"}}); err != nil {
		t.Fatal(err)
	}
	manager.revokePluginCallTarget("report")
	if got := readPluginCallGrants(record.id).Targets; len(got) != 1 || got[0] != "other" {
		t.Fatalf("grants %v", got)
	}
}
