package plugin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

// TestServiceHelperProcess is a service provider for the tests below.
// service.short answers plugin.invoke; service.tasks negotiates features.tasks
// and runs every call as a task. The envelope's text input picks the
// behaviour: "echo" returns the envelope as text, "json" returns a json
// output, "big" a text over the result limit, "files:<token>" names an output
// file, "wrapped" returns {"result":{"text":"hi"}}, "unwrapped"
// {"text":"hi"} and "mixed" {"text":"hi","result":1}, "wait" runs until cancelled (and acknowledges the cancellation),
// "ignore" runs until the process stops; anything else returns "done".
func TestServiceHelperProcess(t *testing.T) {
	pluginID := os.Getenv("MINCODE_PLUGIN_ID")
	if pluginID != "service.short" && pluginID != "service.tasks" {
		return
	}
	var mu sync.Mutex
	writeJSON := func(message any) {
		encoded, _ := json.Marshal(message)
		mu.Lock()
		defer mu.Unlock()
		_, _ = os.Stdout.Write(append(encoded, '\n'))
	}
	modeOf := func(payload string) (string, string) {
		var envelope struct {
			Input json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal([]byte(payload), &envelope)
		var input string
		_ = json.Unmarshal(envelope.Input, &input)
		mode, arg, _ := strings.Cut(input, ":")
		return mode, arg
	}
	resultOf := func(payload string) json.RawMessage {
		mode, arg := modeOf(payload)
		var value any
		switch mode {
		case "echo":
			value = map[string]any{"text": payload}
		case "json":
			value = map[string]any{"json": map[string]int{"a": 1}}
		case "big":
			value = map[string]any{"text": strings.Repeat("x", 262145)}
		case "edge":
			value = map[string]any{"text": strings.Repeat("x", 262144)}
		case "files":
			value = map[string]any{"files": []string{arg}}
		case "wrapped":
			// What a Node runtime with formatResult: r => ({ result: r }) sends.
			value = map[string]any{"result": map[string]any{"text": "hi"}}
		case "unwrapped":
			value = map[string]any{"text": "hi"}
		case "mixed":
			// result is not the only key: not an envelope.
			value = map[string]any{"text": "hi", "result": 1}
		default:
			value = map[string]any{"text": "done"}
		}
		raw, _ := json.Marshal(value)
		return raw
	}
	type task struct {
		id, payload, mode string
		cancelled         bool
	}
	tasks := map[string]*task{}
	progress := json.RawMessage(`{"completed":1,"total":2,"message":"half"}`)
	snapshot := func(current *task) map[string]any {
		reply := map[string]any{"taskId": current.id, "state": "running", "progress": progress}
		switch {
		case current.cancelled && current.mode == "wait":
			reply["state"], reply["cancelRequested"] = "cancelled", true
		case current.cancelled:
			reply["cancelRequested"] = true
		case current.mode != "wait" && current.mode != "ignore":
			reply["state"], reply["result"] = "succeeded", resultOf(current.payload)
		}
		return reply
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var request rpcEnvelope
		if json.Unmarshal(line, &request) != nil {
			continue
		}
		response := rpcEnvelope{JSONRPC: "2.0", ID: request.ID}
		var params struct {
			TaskID  string `json:"taskId"`
			Action  string `json:"action"`
			Payload string `json:"payload"`
		}
		_ = json.Unmarshal(request.Params, &params)
		switch request.Method {
		case "plugin.initialize":
			features := map[string]int{}
			if strings.Contains(strings.Join(os.Args, " "), "TestServiceMetadataHelperProcess") {
				features["serviceCallerMetadata"] = 1
			}
			if pluginID == "service.tasks" {
				features["tasks"] = 1
			}
			response.Result, _ = json.Marshal(map[string]any{"runtimeProtocolVersion": api.RuntimeProtocolVersion, "features": features})
		case "plugin.invoke":
			_ = os.WriteFile("service-dispatched", []byte(params.Payload), 0o600)
			if mode, _ := modeOf(params.Payload); mode == "wait" || mode == "ignore" {
				continue // never answered
			}
			// The action result as is, like a Node runtime with the
			// default formatResult; "wrapped" adds the envelope itself.
			response.Result = resultOf(params.Payload)
		case "plugin.task.start":
			_ = os.WriteFile("service-dispatched", []byte(params.Payload), 0o600)
			mode, _ := modeOf(params.Payload)
			current := &task{id: params.TaskID, payload: params.Payload, mode: mode}
			tasks[current.id] = current
			response.Result, _ = json.Marshal(map[string]any{"taskId": current.id, "state": "running", "progress": progress})
		case "plugin.task.status", "plugin.task.cancel":
			current := tasks[params.TaskID]
			if current == nil {
				response.Error = &rpcError{Code: -32602, Message: "Task not found"}
				break
			}
			if request.Method == "plugin.task.cancel" {
				current.cancelled = true
			}
			response.Result, _ = json.Marshal(snapshot(current))
		case "plugin.ping", "plugin.shutdown":
			response.Result = json.RawMessage(`{}`)
		default:
			response.Error = &rpcError{Code: -32601, Message: "unknown"}
		}
		writeJSON(response)
		if request.Method == "plugin.shutdown" {
			os.Exit(0)
		}
	}
}

type serviceCallFixture struct {
	t        *testing.T
	manager  *pluginManager
	record   *pluginRecord
	provider serviceProvider
}

func serviceTestEntry() ServiceContribution {
	return ServiceContribution{
		ID: "reverse", Service: "demo.text.reverse", Action: "run", Title: "Reverse",
		Input: ServiceShape{Kind: serviceInputText, MaxBytes: 8192}, Output: ServiceShape{Kind: serviceInputText},
		Params:   map[string]any{"type": "object", "properties": map[string]any{"keep": map[string]any{"type": "boolean", "default": true}}},
		Surfaces: []string{serviceSurfacePalette}, TimeoutMs: 30000, Risk: "write",
	}
}

// serviceFilesOutputEntry takes text and returns files, so its calls hold
// an exchange directory the provider can create outputs in.
func serviceFilesOutputEntry() ServiceContribution {
	entry := serviceTestEntry()
	entry.Output = ServiceShape{Kind: serviceInputFiles}
	return entry
}

// serviceCallProvider registers pluginID ("service.short" or
// "service.tasks") as an enabled, not yet running provider of entry, backed
// by action "run" that accepts maxPayload bytes.
func serviceCallProvider(t *testing.T, pluginID string, entry ServiceContribution, maxPayload int) *serviceCallFixture {
	t.Helper()
	return serviceProcessProvider(t, pluginID, "TestServiceHelperProcess", entry, maxPayload)
}

// serviceProcessProvider registers pluginID as an enabled, not yet running
// provider of entry whose process is the helper test named helper.
func serviceProcessProvider(t *testing.T, pluginID, helper string, entry ServiceContribution, maxPayload int) *serviceCallFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	useTemporaryPluginData(t)
	manager := &pluginManager{records: map[string]*pluginRecord{}, uiHost: newPluginUIHost()}
	return addServiceProcessProvider(t, manager, pluginID, helper, entry, maxPayload)
}

// addServiceProcessProvider registers pluginID in manager like
// serviceProcessProvider, so that one manager can hold several providers.
func addServiceProcessProvider(t *testing.T, manager *pluginManager, pluginID, helper string, entry ServiceContribution, maxPayload int) *serviceCallFixture {
	t.Helper()
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A hard link avoids copying the test binary; copy across devices.
	if err := os.Link(executable, filepath.Join(root, "helper.exe")); err != nil {
		data, err := os.ReadFile(executable)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "helper.exe"), data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := Manifest{ID: pluginID, Name: "Service " + pluginID, Version: "1.0.0", APIVersion: 2,
		Runtime:     ProcessRuntimeManifest{Type: "process", Entry: "helper.exe", Args: []string{"-test.run=^" + helper + "$"}},
		Contributes: ManifestContributions{Actions: []ManifestUIAction{{ID: "run", Risk: entry.Risk, MaxPayloadBytes: maxPayload, Permissions: []api.Capability{}}}}}
	if err := manager.registerLocal(root, manifest); err != nil {
		t.Fatal(err)
	}
	record, _ := manager.record(pluginID)
	// Registered after the root's removal, so it runs before it.
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	if err := manager.SetEnabled(context.Background(), pluginID, true); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	record.manifest.contributions = map[string]*resolvedContribution{servicePointKey: {Entries: []any{entry}}}
	record.mu.Unlock()
	return &serviceCallFixture{t: t, manager: manager, record: record, provider: serviceProvider{PluginID: pluginID, PluginName: manifest.Name, Entry: entry}}
}

func (fixture *serviceCallFixture) request(caller string, input string) serviceCallRequest {
	raw, _ := json.Marshal(input)
	return serviceCallRequest{Caller: fixture.caller(caller), Provider: fixture.provider, Input: raw}
}

func waitServiceCall(t *testing.T, owner, callID string) ServiceCallSnapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := serviceCallStatus(owner, callID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.State != serviceCallRunning {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("service call did not finish")
	return ServiceCallSnapshot{}
}

func waitServiceCallProgress(t *testing.T, owner, callID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := serviceCallStatus(owner, callID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Progress != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("service call reported no progress")
}

func serviceCallTimeout(t *testing.T, callID string) time.Duration {
	t.Helper()
	serviceCalls.mu.Lock()
	defer serviceCalls.mu.Unlock()
	call := serviceCalls.calls[callID]
	if call == nil {
		t.Fatalf("call %s is not recorded", callID)
	}
	return call.timeout
}

func outputText(t *testing.T, snapshot ServiceCallSnapshot) string {
	t.Helper()
	var text string
	if err := json.Unmarshal(snapshot.Output, &text); err != nil {
		t.Fatalf("output %s: %v (%+v)", snapshot.Output, err, snapshot)
	}
	return text
}

func TestServiceCallShortPath(t *testing.T) {
	fixture := serviceCallProvider(t, "service.short", serviceTestEntry(), 16384)
	started, err := fixture.manager.startServiceCall(context.Background(), fixture.request("caller.a", "echo"))
	if err != nil {
		t.Fatal(err)
	}
	if serviceCallTimeout(t, started.CallID) != 30*time.Second {
		t.Fatalf("short timeout = %v", serviceCallTimeout(t, started.CallID))
	}
	final := waitServiceCall(t, "caller.a", started.CallID)
	if final.State != serviceCallSucceeded || final.Provider != (serviceProviderRef{PluginID: "service.short", EntryID: "reverse"}) {
		t.Fatalf("final = %+v", final)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(outputText(t, final)), &envelope); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"callId": started.CallID, "service": "demo.text.reverse", "entry": "reverse", "input": "echo",
		"params": map[string]any{"keep": true}, "caller": map[string]any{"kind": "plugin", "pluginId": "caller.a"},
	}
	encodedWant, _ := json.Marshal(want)
	encodedGot, _ := json.Marshal(envelope)
	if string(encodedWant) != string(encodedGot) {
		t.Fatalf("envelope = %s, want %s", encodedGot, encodedWant)
	}
	// Host calls name the surface.
	request := fixture.request("", "echo")
	request.Caller = serviceCaller{Kind: "host", Surface: serviceSurfacePalette}
	hostCall, err := fixture.manager.runServiceCallSync(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outputText(t, hostCall), `"caller":{"kind":"host","surface":"palette"}`) {
		t.Fatalf("host envelope = %s", hostCall.Output)
	}
}

func TestServiceCallTaskPath(t *testing.T) {
	entry := serviceTestEntry()
	entry.TimeoutMs = 60000
	fixture := serviceCallProvider(t, "service.tasks", entry, 16384)
	var mu sync.Mutex
	var events []ServiceCallSnapshot
	core.SetEmitter(func(name string, data any) {
		if name == pluginUITaskChangedEvent {
			t.Errorf("service call task pushed to pages: %+v", data)
		}
		if name == serviceCallChangedEvent {
			mu.Lock()
			events = append(events, data.(ServiceCallSnapshot))
			mu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	request := fixture.request("", "slow")
	request.Caller = serviceCaller{Kind: "host", Surface: serviceSurfacePalette}
	started, err := fixture.manager.startServiceCall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if serviceCallTimeout(t, started.CallID) != time.Minute {
		t.Fatalf("task timeout = %v", serviceCallTimeout(t, started.CallID))
	}
	// The task is invisible to the provider's page sessions.
	runtimeHost := fixture.record.runtime
	runtimeHost.mu.Lock()
	var taskID string
	for id, task := range runtimeHost.tasks {
		if task.callID == started.CallID {
			taskID = id
		}
	}
	runtimeHost.mu.Unlock()
	if taskID == "" {
		t.Fatal("no plugin.task.start for the call")
	}
	session, err := fixture.manager.uiHost.Begin(fixture.record.id, "view", runtimeHost.Generation(), t.TempDir(), "ui/index.html", "tango://app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.uiTask(context.Background(), "get", taskArgs{SessionID: session.SessionID, Nonce: session.Nonce, TaskID: taskID}); err == nil {
		t.Fatal("page session can read a service call task")
	}
	final := waitServiceCall(t, serviceCallHostOwner, started.CallID)
	if final.State != serviceCallSucceeded || outputText(t, final) != "done" {
		t.Fatalf("final = %+v", final)
	}
	mu.Lock()
	defer mu.Unlock()
	sawProgress := false
	for _, event := range events {
		if event.CallID == started.CallID && event.Progress != nil && event.Progress.Message == "half" &&
			*event.Progress.Total == 2 && string(event.RawProgress) == `{"completed":1,"total":2,"message":"half"}` {
			sawProgress = true
		}
	}
	if !sawProgress || len(events) == 0 || events[len(events)-1].State != serviceCallSucceeded {
		t.Fatalf("events = %+v", events)
	}
}

// A result that is an object whose only key is "result" is unwrapped on both
// paths, so a provider's formatResult wrapper works; any other object (here
// {"text":"hi","result":1}) is taken as is (design §2b).
func TestServiceCallPathsUnwrapResult(t *testing.T) {
	for _, pluginID := range []string{"service.short", "service.tasks"} {
		fixture := serviceCallProvider(t, pluginID, serviceTestEntry(), 16384)
		for _, input := range []string{"wrapped", "unwrapped", "mixed"} {
			final, err := fixture.manager.runServiceCallSync(context.Background(), fixture.request("caller.a", input))
			if err != nil {
				t.Fatal(err)
			}
			if final.State != serviceCallSucceeded || outputText(t, final) != "hi" {
				t.Fatalf("%s %s: final = %s %q", pluginID, input, final.State, final.Error)
			}
		}
	}
}

func TestServiceResultJSON(t *testing.T) {
	for raw, want := range map[string]string{
		`{"result":{"text":"hi"}}`: `{"text":"hi"}`,
		` { "result" : [1] } `:     `[1]`,
		`{"text":"a","result":1}`:  `{"text":"a","result":1}`,
		`{"json":{"result":1}}`:    `{"json":{"result":1}}`,
		`{"Result":{"text":"hi"}}`: `{"Result":{"text":"hi"}}`,
		`{"result":1,"result":2}`:  `{"result":1,"result":2}`,
		`{}`:                       `{}`,
		`"result"`:                 `"result"`,
		`null`:                     `null`,
		``:                         `null`,
		`{"result":null}`:          `null`,
		`{"\u0072esult":{"t":1}}`:  `{"t":1}`,
	} {
		if got := serviceResultJSON(json.RawMessage(raw)); string(got) != want {
			t.Errorf("serviceResultJSON(%s) = %s, want %s", raw, got, want)
		}
	}
}

// Finished service call tasks leave the provider's task table at once (the
// call record keeps the snapshot), so more calls than taskLimit in a row all
// run and the provider's pages can still start tasks.
func TestServiceCallTasksLeaveTaskTable(t *testing.T) {
	previous := taskPollSchedule
	taskPollSchedule = []time.Duration{2 * time.Millisecond}
	t.Cleanup(func() { taskPollSchedule = previous })
	fixture := serviceCallProvider(t, "service.tasks", serviceTestEntry(), 16384)
	var last ServiceCallSnapshot
	for i := 0; i <= taskLimit; i++ {
		final, err := fixture.manager.runServiceCallSync(context.Background(), fixture.request("caller.a", "x"))
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if final.State != serviceCallSucceeded {
			t.Fatalf("call %d: %s %q", i+1, final.State, final.Error)
		}
		last = final
	}
	// The caller still reads the finished call's snapshot.
	if snapshot, err := serviceCallStatus("caller.a", last.CallID); err != nil || snapshot.State != serviceCallSucceeded {
		t.Fatalf("status = %+v, %v", snapshot, err)
	}
	runtimeHost := fixture.record.runtime
	if _, err := runtimeHost.startTask(context.Background(), taskOwner{SessionID: "page", Generation: runtimeHost.Generation()}, "run", "{}", time.Minute); err != nil {
		t.Fatalf("page task after %d service calls: %v", taskLimit+1, err)
	}
}

// Service calls leave headroom in the provider's task table for its pages.
func TestServiceProviderConcurrencyLeavesPageTasks(t *testing.T) {
	if serviceProviderConcurrency != taskLimit-16 || serviceProviderConcurrency <= 0 {
		t.Fatalf("serviceProviderConcurrency = %d, want taskLimit-16", serviceProviderConcurrency)
	}
}

func TestServiceCallLongWithoutTasks(t *testing.T) {
	entry := serviceTestEntry()
	entry.TimeoutMs = 60000
	fixture := serviceCallProvider(t, "service.short", entry, 16384)
	_, err := fixture.manager.startServiceCall(context.Background(), fixture.request("caller.a", "echo"))
	if err == nil || !strings.Contains(err.Error(), "does not support long-running") {
		t.Fatalf("err = %v", err)
	}
	assertNoContributionRequestsLeft(t)
}

func TestServiceTextEnvelopeTooLarge(t *testing.T) {
	entry := serviceTestEntry()
	entry.Input.MaxBytes = 8192
	fixture := serviceCallProvider(t, "service.short", entry, 8192+8192)
	// Within input.maxBytes, but the JSON escaping of the envelope exceeds
	// the action's maxPayloadBytes.
	input := strings.Repeat("\x01", 4000)
	_, err := fixture.manager.startServiceCall(context.Background(), fixture.request("caller.a", input))
	wantContributionCode(t, err, contributionErrTooLarge)
	if runtimeHost := fixture.record.runtime; runtimeHost.Generation() != 0 {
		t.Fatal("the provider was started for a call that cannot be sent")
	}
	assertNoContributionRequestsLeft(t)
}

func TestServiceOutputValidation(t *testing.T) {
	// code "" expects success. Both paths admit the same output size: the
	// task path reads results up to the stdout line cap, like plugin.invoke.
	cases := []struct {
		name, provider, input, code string
		output                      string
	}{
		{"json for text", "service.short", "json", contributionErrInvalidResult, serviceInputText},
		{"text at limit", "service.short", "edge", "", serviceInputText},
		{"text too large", "service.short", "big", contributionErrTooLarge, serviceInputText},
		{"foreign file", "service.short", "files:", contributionErrInvalidResult, serviceInputFiles},
		{"task json for text", "service.tasks", "json", contributionErrInvalidResult, serviceInputText},
		{"task text at limit", "service.tasks", "edge", "", serviceInputText},
		{"task text too large", "service.tasks", "big", contributionErrTooLarge, serviceInputText},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			entry := serviceTestEntry()
			entry.Output = ServiceShape{Kind: testCase.output}
			fixture := serviceCallProvider(t, testCase.provider, entry, 16384)
			input := testCase.input
			if testCase.output == serviceInputFiles {
				// An output the provider created in another call.
				if err := serviceFiles.beginCall("other-call-0001"); err != nil {
					t.Fatal(err)
				}
				defer serviceFiles.endCall("other-call-0001", false)
				session, err := serviceFiles.createBegin("other-call-0001", fixture.provider.PluginID, "a.txt")
				if err != nil {
					t.Fatal(err)
				}
				if err := serviceFiles.createAppend(fixture.provider.PluginID, session, 0, []byte("a")); err != nil {
					t.Fatal(err)
				}
				file, err := serviceFiles.createCommit(fixture.provider.PluginID, session)
				if err != nil {
					t.Fatal(err)
				}
				input += file.Token
			}
			final, err := fixture.manager.runServiceCallSync(context.Background(), fixture.request("caller.a", input))
			if err != nil {
				t.Fatal(err)
			}
			if testCase.code == "" {
				if final.State != serviceCallSucceeded || len(outputText(t, final)) != 262144 {
					t.Fatalf("final = %s %s", final.State, final.Error)
				}
				return
			}
			if final.State != serviceCallFailed || !strings.HasPrefix(final.Error, testCase.code+":") || final.Unconfirmed {
				t.Fatalf("final = %s %q unconfirmed=%t", final.State, final.Error, final.Unconfirmed)
			}
		})
	}
}

// Calls without files do not take the exchange's fileCallsConcurrent slots:
// more than that many run at once across callers, each under its own limit.
func TestServiceCallsWithoutFilesSkipExchangeSlots(t *testing.T) {
	fixture := serviceCallProvider(t, "service.tasks", serviceTestEntry(), 16384)
	type started struct{ owner, callID string }
	var calls []started
	for index := range serviceFileCallsConcurrent + 4 {
		caller := []string{"caller.a", "caller.b", "caller.c"}[index%3]
		snapshot, err := fixture.manager.startServiceCall(context.Background(), fixture.request(caller, "wait"))
		if err != nil {
			t.Fatalf("call %d: %v", index+1, err)
		}
		calls = append(calls, started{caller, snapshot.CallID})
	}
	serviceFiles.mu.Lock()
	reserved := len(serviceFiles.calls)
	serviceFiles.mu.Unlock()
	if reserved != 0 {
		t.Fatalf("%d exchange calls reserved for calls without files", reserved)
	}
	for _, call := range calls {
		waitServiceCallProgress(t, call.owner, call.callID)
		if _, err := cancelServiceCall(call.owner, call.callID); err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range calls {
		if final := waitServiceCall(t, call.owner, call.callID); final.State != serviceCallCancelled {
			t.Fatalf("final = %+v", final)
		}
	}
}

func TestNormalizeServiceProgress(t *testing.T) {
	raw := json.RawMessage(`{"completed":3,"total":10,"message":"x","extra":1}`)
	progress, kept := normalizeServiceProgress(raw)
	if progress.Completed == nil || *progress.Completed != 3 || progress.Total == nil || *progress.Total != 10 || progress.Message != "x" || string(kept) != string(raw) {
		t.Fatalf("progress = %+v, raw %s", progress, kept)
	}
	long, _ := json.Marshal(map[string]string{"message": strings.Repeat("é", 301)})
	progress, _ = normalizeServiceProgress(long)
	if got := []rune(progress.Message); len(got) != 200 {
		t.Fatalf("message has %d runes", len(got))
	}
	huge, _ := json.Marshal(map[string]any{"completed": 1, "pad": strings.Repeat("x", 5000)})
	progress, kept = normalizeServiceProgress(huge)
	if kept != nil || progress.Completed == nil || *progress.Completed != 1 {
		t.Fatalf("oversized progress = %+v, raw %d bytes", progress, len(kept))
	}
	for _, value := range []string{`3`, `"x"`, `[1]`, `null`, ``, `{`} {
		progress, kept = normalizeServiceProgress(json.RawMessage(value))
		if kept != nil || progress != (ServiceProgress{}) {
			t.Fatalf("%q: progress = %+v, raw %s", value, progress, kept)
		}
	}
}

// createServiceOutput creates a committed output file of callID as provider.
func createServiceOutput(t *testing.T, callID, provider string) exchangeFile {
	t.Helper()
	session, err := serviceFiles.createBegin(callID, provider, "out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := serviceFiles.createAppend(provider, session, 0, []byte("partial")); err != nil {
		t.Fatal(err)
	}
	file, err := serviceFiles.createCommit(provider, session)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func serviceOutputExists(token string) bool {
	serviceFiles.mu.Lock()
	defer serviceFiles.mu.Unlock()
	return serviceFiles.files[token] != nil
}

func TestServiceCallCancel(t *testing.T) {
	for _, testCase := range []struct {
		input, state string
		unconfirmed  bool
	}{
		{"wait", serviceCallCancelled, false},
		{"ignore", serviceCallFailed, true},
	} {
		t.Run(testCase.input, func(t *testing.T) {
			fixture := serviceCallProvider(t, "service.tasks", serviceFilesOutputEntry(), 16384)
			started, err := fixture.manager.startServiceCall(context.Background(), fixture.request("caller.a", testCase.input))
			if err != nil {
				t.Fatal(err)
			}
			// Cancel once the provider accepted the task: a cancellation
			// racing plugin.task.start is unconfirmed by design.
			waitServiceCallProgress(t, "caller.a", started.CallID)
			file := createServiceOutput(t, started.CallID, fixture.provider.PluginID)
			if _, err := cancelServiceCall("caller.a", started.CallID); err != nil {
				t.Fatal(err)
			}
			final := waitServiceCall(t, "caller.a", started.CallID)
			if final.State != testCase.state || final.Unconfirmed != testCase.unconfirmed {
				t.Fatalf("final = %+v", final)
			}
			if testCase.unconfirmed && !strings.Contains(final.Error, "unconfirmed") {
				t.Fatalf("error = %q", final.Error)
			}
			if serviceOutputExists(file.Token) {
				t.Fatal("output of a cancelled call kept")
			}
			assertNoContributionRequestsLeft(t)
		})
	}
	// The short path has no cancel protocol: the outcome is never confirmed.
	fixture := serviceCallProvider(t, "service.short", serviceTestEntry(), 16384)
	started, err := fixture.manager.startServiceCall(context.Background(), fixture.request("caller.a", "wait"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cancelServiceCall("caller.a", started.CallID); err != nil {
		t.Fatal(err)
	}
	if final := waitServiceCall(t, "caller.a", started.CallID); final.State != serviceCallFailed || !final.Unconfirmed {
		t.Fatalf("short cancel = %+v", final)
	}
}

func TestServiceCallProviderStoppedMidTask(t *testing.T) {
	fixture := serviceCallProvider(t, "service.tasks", serviceFilesOutputEntry(), 16384)
	started, err := fixture.manager.startServiceCall(context.Background(), fixture.request("caller.a", "ignore"))
	if err != nil {
		t.Fatal(err)
	}
	file := createServiceOutput(t, started.CallID, fixture.provider.PluginID)
	if err := fixture.manager.SetEnabled(context.Background(), fixture.provider.PluginID, false); err != nil {
		t.Fatal(err)
	}
	final := waitServiceCall(t, "caller.a", started.CallID)
	if final.State != serviceCallFailed || !final.Unconfirmed {
		t.Fatalf("final = %+v", final)
	}
	if serviceOutputExists(file.Token) {
		t.Fatal("exchange files survived the provider")
	}
	serviceFiles.mu.Lock()
	active := serviceFiles.calls[started.CallID]
	serviceFiles.mu.Unlock()
	if active != nil {
		t.Fatal("exchange call not ended")
	}
}

func TestServiceCallOwnerIsolation(t *testing.T) {
	fixture := serviceCallProvider(t, "service.tasks", serviceTestEntry(), 16384)
	started, err := fixture.manager.startServiceCall(context.Background(), fixture.request("caller.a", "wait"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = serviceCallStatus("caller.b", started.CallID)
	wantContributionCode(t, err, contributionErrNotFound)
	_, err = cancelServiceCall("caller.b", started.CallID)
	wantContributionCode(t, err, contributionErrNotFound)
	_, err = serviceCallStatus(serviceCallHostOwner, started.CallID)
	wantContributionCode(t, err, contributionErrNotFound)
	if snapshot, err := serviceCallStatus("caller.a", started.CallID); err != nil || snapshot.State != serviceCallRunning {
		t.Fatalf("owner status = %+v, %v", snapshot, err)
	}
	if _, err := cancelServiceCall("caller.a", started.CallID); err != nil {
		t.Fatal(err)
	}
	waitServiceCall(t, "caller.a", started.CallID)
}

func TestServiceCallPerCallerLimit(t *testing.T) {
	fixture := serviceCallProvider(t, "service.tasks", serviceTestEntry(), 16384)
	var calls []string
	for range 8 {
		started, err := fixture.manager.startServiceCall(context.Background(), fixture.request("caller.a", "wait"))
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, started.CallID)
	}
	_, err := fixture.manager.startServiceCall(context.Background(), fixture.request("caller.a", "wait"))
	wantContributionCode(t, err, contributionErrBusy)
	if !strings.Contains(err.Error(), "caller") {
		t.Fatalf("err = %v", err)
	}
	for _, callID := range calls {
		if _, err := cancelServiceCall("caller.a", callID); err != nil {
			t.Fatal(err)
		}
	}
	for _, callID := range calls {
		waitServiceCall(t, "caller.a", callID)
	}
	if _, err := fixture.manager.runServiceCallSync(context.Background(), fixture.request("caller.a", "done")); err != nil {
		t.Fatalf("finished calls still count: %v", err)
	}
}

func TestServiceSnapshotRetention(t *testing.T) {
	fixture := serviceCallProvider(t, "service.short", serviceTestEntry(), 16384)
	final, err := fixture.manager.runServiceCallSync(context.Background(), fixture.request("caller.a", "done"))
	if err != nil || final.State != serviceCallSucceeded {
		t.Fatalf("final = %+v, %v", final, err)
	}
	previous := serviceCallNow
	t.Cleanup(func() { serviceCallNow = previous })
	serviceCallNow = func() time.Time { return time.Now().Add(5*time.Minute - time.Second) }
	if _, err := serviceCallStatus("caller.a", final.CallID); err != nil {
		t.Fatalf("expired early: %v", err)
	}
	serviceCallNow = func() time.Time { return time.Now().Add(5*time.Minute + time.Second) }
	_, err = serviceCallStatus("caller.a", final.CallID)
	wantContributionCode(t, err, contributionErrNotFound)
}

// providerStarted reports whether the provider was activated (lazily, by a
// dispatch) since the fixture enabled it.
func providerStarted(fixture *serviceCallFixture) bool {
	fixture.record.mu.Lock()
	defer fixture.record.mu.Unlock()
	return fixture.record.state != pluginStateStopped
}

func exchangeHasCall(callID string) bool {
	serviceFiles.mu.Lock()
	defer serviceFiles.mu.Unlock()
	return serviceFiles.calls[callID] != nil
}

// exchangeUsed is the shared exchange's byte count (other tests may leave
// retained outputs, so compare it before and after).
func exchangeUsed() int64 {
	serviceFiles.mu.Lock()
	defer serviceFiles.mu.Unlock()
	return serviceFiles.used
}

// waitServiceCallSnapshot polls the call until ok accepts its snapshot.
func waitServiceCallSnapshot(t *testing.T, owner, callID string, ok func(ServiceCallSnapshot) bool) ServiceCallSnapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := serviceCallStatus(owner, callID)
		if err != nil {
			t.Fatal(err)
		}
		if ok(snapshot) {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("service call did not reach the expected state")
	return ServiceCallSnapshot{}
}

func TestStartServiceCallAsyncPrepareReturnsInputPhase(t *testing.T) {
	fixture := serviceProcessProvider(t, serviceFilesHelperID, "TestServiceFilesHelperProcess", checksumServiceEntry(), 16384)
	content := []byte(strings.Repeat("remote bytes ", 100))
	gate := make(chan struct{})
	request := serviceCallRequest{Caller: fixture.caller("caller.a"), Provider: fixture.provider,
		PrepareInputsAsync: func(ctx context.Context, callID string, report func(done, total int64)) ([]exchangeFile, error) {
			report(0, int64(len(content)))
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			file, err := serviceFiles.snapshotBytes(callID, fixture.provider.PluginID, "remote.bin", content)
			if err != nil {
				return nil, err
			}
			report(int64(len(content)), int64(len(content)))
			return []exchangeFile{file}, nil
		}}
	started, err := fixture.manager.startServiceCall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if started.State != serviceCallRunning || started.Phase != serviceCallPhaseInput || started.Progress == nil {
		t.Fatalf("started = %+v", started)
	}
	input := waitServiceCallSnapshot(t, "caller.a", started.CallID, func(snapshot ServiceCallSnapshot) bool {
		return snapshot.Progress != nil && snapshot.Progress.Total != nil
	})
	if input.Phase != serviceCallPhaseInput || *input.Progress.Total != float64(len(content)) || *input.Progress.Completed != 0 {
		t.Fatalf("input phase = %+v", input)
	}
	encoded, _ := json.Marshal(input)
	if !strings.Contains(string(encoded), `"phase":"input"`) {
		t.Fatalf("snapshot JSON lacks the phase: %s", encoded)
	}
	if providerStarted(fixture) {
		t.Fatal("provider activated during the input phase")
	}
	close(gate)
	final := waitServiceCall(t, "caller.a", started.CallID)
	sum := sha256.Sum256(content)
	if final.State != serviceCallSucceeded || final.Phase != "" || outputText(t, final) != hex.EncodeToString(sum[:]) {
		t.Fatalf("final = %+v", final)
	}
	encoded, _ = json.Marshal(final)
	if strings.Contains(string(encoded), `"phase"`) {
		t.Fatalf("final snapshot keeps a phase: %s", encoded)
	}
	if !providerStarted(fixture) {
		t.Fatal("dispatch did not activate the provider")
	}
}

func TestServiceCallCancelDuringInputPhase(t *testing.T) {
	fixture := serviceProcessProvider(t, serviceFilesHelperID, "TestServiceFilesHelperProcess", checksumServiceEntry(), 16384)
	prepared := make(chan struct{})
	request := serviceCallRequest{Caller: fixture.caller("caller.a"), Provider: fixture.provider,
		PrepareInputsAsync: func(ctx context.Context, callID string, report func(done, total int64)) ([]exchangeFile, error) {
			defer close(prepared)
			if _, err := serviceFiles.snapshotBytes(callID, fixture.provider.PluginID, "partial.bin", []byte("partial")); err != nil {
				return nil, err
			}
			report(7, 100)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
	usedBefore := exchangeUsed()
	started, err := fixture.manager.startServiceCall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	waitServiceCallSnapshot(t, "caller.a", started.CallID, func(snapshot ServiceCallSnapshot) bool {
		return snapshot.Progress != nil && snapshot.Progress.Total != nil
	})
	if exchangeUsed() != usedBefore+7 {
		t.Fatal("the input phase's snapshot is not accounted")
	}
	if _, err := cancelServiceCall("caller.a", started.CallID); err != nil {
		t.Fatal(err)
	}
	<-prepared
	final := waitServiceCall(t, "caller.a", started.CallID)
	if final.State != serviceCallCancelled || final.Unconfirmed || final.Error != "" || final.Phase != "" {
		t.Fatalf("final = %+v", final)
	}
	if providerStarted(fixture) {
		t.Fatal("the provider ran for a call cancelled in its input phase")
	}
	if exchangeHasCall(started.CallID) {
		t.Fatal("the cancelled call kept its exchange slot")
	}
	if used := exchangeUsed(); used != usedBefore {
		t.Fatalf("exchange bytes after cancel = %d, before %d", used, usedBefore)
	}
}

func TestServiceCallInputPhaseFailure(t *testing.T) {
	fixture := serviceProcessProvider(t, serviceFilesHelperID, "TestServiceFilesHelperProcess", checksumServiceEntry(), 16384)
	request := serviceCallRequest{Caller: fixture.caller("caller.a"), Provider: fixture.provider,
		PrepareInputsAsync: func(context.Context, string, func(done, total int64)) ([]exchangeFile, error) {
			return nil, newContributionError(contributionErrTimeout, "stalled")
		}}
	started, err := fixture.manager.startServiceCall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	final := waitServiceCall(t, "caller.a", started.CallID)
	if final.State != serviceCallFailed || final.Unconfirmed || !strings.HasPrefix(final.Error, contributionErrTimeout+":") {
		t.Fatalf("final = %+v", final)
	}
	// The input itself failed: the failed snapshot keeps phase "input".
	if final.Phase != serviceCallPhaseInput {
		t.Fatalf("failed input phase lost its phase: %+v", final)
	}
	if providerStarted(fixture) || exchangeHasCall(started.CallID) {
		t.Fatal("a failed input phase activated the provider or kept its exchange slot")
	}

	// The input was read but admitting the provider failed: no phase, so the
	// failure is not reported as an input (remote read) error.
	afterRead := serviceCallRequest{Caller: fixture.caller("caller.a"), Provider: fixture.provider,
		PrepareInputsAsync: func(_ context.Context, callID string, report func(done, total int64)) ([]exchangeFile, error) {
			file, err := serviceFiles.snapshotBytes(callID, fixture.provider.PluginID, "remote.bin", []byte("read"))
			if err != nil {
				return nil, err
			}
			report(4, 4)
			return []exchangeFile{file}, nil
		},
		Revalidate: func(*pluginRecord) error {
			return newContributionError(contributionErrUnavailable, "activation failed")
		}}
	started, err = fixture.manager.startServiceCall(context.Background(), afterRead)
	if err != nil {
		t.Fatal(err)
	}
	final = waitServiceCall(t, "caller.a", started.CallID)
	if final.State != serviceCallFailed || final.Phase != "" || !strings.HasPrefix(final.Error, contributionErrUnavailable+":") {
		t.Fatalf("failure after the read = %+v", final)
	}
	// A service without file input takes no asynchronous preparation, and
	// startServiceCall then never calls it.
	text := serviceCallProvider(t, "service.short", serviceTestEntry(), 16384)
	called := false
	_, err = text.manager.startServiceCall(context.Background(), serviceCallRequest{Caller: text.caller("caller.a"), Provider: text.provider,
		Input: json.RawMessage(`"x"`), PrepareInputsAsync: func(context.Context, string, func(done, total int64)) ([]exchangeFile, error) {
			called = true
			return nil, nil
		}})
	wantContributionCode(t, err, contributionErrInvalidRequest)
	if called {
		t.Fatal("PrepareInputsAsync called for a refused call")
	}
}

func TestServiceCallInputProgressThrottled(t *testing.T) {
	previousNow, previousEmit := serviceCallNow, serviceCallEmit
	t.Cleanup(func() { serviceCallNow, serviceCallEmit = previousNow, previousEmit })
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	serviceCallNow = func() time.Time { return now }
	var emitted []ServiceCallSnapshot
	serviceCallEmit = func(name string, data any) {
		if name == serviceCallChangedEvent {
			emitted = append(emitted, data.(ServiceCallSnapshot))
		}
	}
	call := &serviceCall{id: "sc_test", host: true, snapshot: ServiceCallSnapshot{CallID: "sc_test", State: serviceCallRunning, Phase: serviceCallPhaseInput}}
	call.inputProgress(0, 100)
	call.inputProgress(10, 100)
	call.inputProgress(20, 100)
	now = now.Add(serviceInputProgressInterval - time.Millisecond)
	call.inputProgress(30, 100)
	now = now.Add(time.Millisecond)
	call.inputProgress(40, 100)
	call.inputProgress(100, 100) // the last chunk is always sent
	var completed []float64
	for _, snapshot := range emitted {
		completed = append(completed, *snapshot.Progress.Completed)
	}
	if !slices.Equal(completed, []float64{0, 40, 100}) {
		t.Fatalf("emitted progress = %v", completed)
	}
	// The status always has the latest value.
	if *call.snapshot.Progress.Completed != 100 || *call.snapshot.Progress.Total != 100 {
		t.Fatalf("snapshot progress = %+v", call.snapshot.Progress)
	}
}
