package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

// Older service tests use real provider JSONL with a controlled running
// caller marker; lifecycle tests below also exercise real caller processes.
func bindServiceTestCaller(t *testing.T, manager *pluginManager, rt *processRuntime) *processRuntime {
	t.Helper()
	if rt.ownerRecord != nil {
		return rt
	}
	manager.mu.Lock()
	record := manager.records[rt.pluginID]
	if record == nil {
		record = &pluginRecord{id: rt.pluginID, source: pluginSourceLocal, enabled: true, state: pluginStateRunning, manifest: &Manifest{ID: rt.pluginID, Version: "1.0.0"}}
		manager.records[rt.pluginID] = record
	}
	manager.mu.Unlock()
	record.mu.Lock()
	rt.ownerManager, rt.ownerRecord = manager, record
	rt.instance, rt.generation, rt.cmd = allocateRuntimeInstance(), 1, &exec.Cmd{}
	rt.callerMetadata = freezeRuntimeCallerMetadata(record.source, record.manifest.Version)
	record.runtime, record.state = rt, pluginStateRunning
	record.mu.Unlock()
	t.Cleanup(func() { rt.mu.Lock(); rt.cmd = nil; rt.mu.Unlock() })
	return rt
}

func (fixture *serviceCallFixture) caller(id string) serviceCaller {
	rtRecord, _ := fixture.manager.record(id)
	var rt *processRuntime
	if rtRecord != nil {
		rtRecord.mu.Lock()
		rt = rtRecord.runtime
		rtRecord.mu.Unlock()
	}
	if rt == nil {
		rt = bindServiceTestCaller(fixture.t, fixture.manager, servicesCaller(id, api.CapabilityPlugins))
	}
	caller, err := captureServiceCaller(rt, rt.Generation())
	if err != nil {
		fixture.t.Fatal(err)
	}
	return caller
}

// Legacy unit-test adapters only. Production registration always receives the
// original generation from RPC ingress, never Generation() at handler execution.
func hostServicesInvoke(rt *processRuntime, raw json.RawMessage) (any, *rpcError) {
	return hostServicesInvokeGeneration(rt, rt.Generation(), raw)
}
func hostServicesStart(rt *processRuntime, raw json.RawMessage) (any, *rpcError) {
	return hostServicesStartGeneration(rt, rt.Generation(), raw)
}

func realServiceCaller(t *testing.T, manager *pluginManager) *pluginRecord {
	t.Helper()
	caller := addServiceProcessProvider(t, manager, "service.short", "TestServiceHelperProcess", serviceTestEntry(), 16384)
	caller.record.mu.Lock()
	caller.record.manifest.contributions = nil
	caller.record.mu.Unlock()
	if err := manager.startRecord(context.Background(), caller.record); err != nil {
		t.Fatal(err)
	}
	return caller.record
}

// Real factory and subprocess startup: different instances, both generation 1.
func replaceServiceTestRuntime(t *testing.T, manager *pluginManager, record *pluginRecord) *processRuntime {
	t.Helper()
	old := record.runtime
	t.Cleanup(func() { _ = old.Stop(context.Background()) })
	record.mu.Lock()
	manifest := *record.manifest
	manifest.Version = "2.0.0"
	replacement := manager.newRecordRuntime(record, record.root, manifest)
	record.manifest, record.runtime, record.state = &manifest, replacement, pluginStateStarting
	record.mu.Unlock()
	if err := replacement.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	record.state = pluginStateRunning
	record.mu.Unlock()
	if old.Generation() != 1 || replacement.Generation() != 1 || old.instance == replacement.instance {
		t.Fatal("fixture did not create generation-1 replacement")
	}
	return replacement
}

func TestServiceCallerReloadDuringApprovalAndPreparation(t *testing.T) {
	t.Run("approval", func(t *testing.T) {
		events := captureHostUI(t)
		fixture := serviceCallProvider(t, "service.tasks", serviceTestEntry(), 16384)
		useServicesManager(t, fixture.manager)
		record := realServiceCaller(t, fixture.manager)
		old := record.runtime
		go answerNext(t, events, func(promptEvent) promptReply {
			replaceServiceTestRuntime(t, fixture.manager, record)
			return promptReply{IDs: []string{"once"}}
		})
		_, rpcErr := servicesCall(t, hostServicesInvoke, old, `{"service":"demo.text.reverse","provider":{"pluginId":"service.tasks"},"input":"echo"}`)
		if rpcErr == nil {
			t.Fatal("old caller dispatched after approval across runtime replacement")
		}
		if providerStarted(fixture) {
			t.Fatal("stale approved caller activated provider")
		}
	})
	t.Run("async input", func(t *testing.T) {
		fixture := serviceProcessProvider(t, serviceFilesHelperID, "TestServiceFilesHelperProcess", checksumServiceEntry(), 16384)
		request := fixture.request("caller.a", "")
		request.Input = nil
		entered, release := make(chan struct{}), make(chan struct{})
		request.PrepareInputsAsync = func(_ context.Context, callID string, _ func(int64, int64)) ([]exchangeFile, error) {
			close(entered)
			<-release
			file, err := serviceFiles.snapshotBytes(callID, fixture.provider.PluginID, "x.bin", []byte("x"))
			return []exchangeFile{file}, err
		}
		started, err := fixture.manager.startServiceCall(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		<-entered
		record, _ := fixture.manager.record("caller.a")
		record.mu.Lock()
		replacement := fixture.manager.newRecordRuntime(record, "", *record.manifest)
		replacement.cmd, replacement.generation = &exec.Cmd{}, 1
		record.runtime = replacement
		record.mu.Unlock()
		close(release)
		final := waitServiceCall(t, "caller.a", started.CallID)
		if final.State != serviceCallCancelled {
			t.Fatalf("stale async caller = %+v", final)
		}
		if providerStarted(fixture) {
			t.Fatal("stale async caller activated provider")
		}
	})
}

func decodedServiceCaller(t *testing.T, snapshot ServiceCallSnapshot) map[string]any {
	t.Helper()
	var envelope struct {
		Caller map[string]any `json:"caller"`
	}
	if err := json.Unmarshal([]byte(outputText(t, snapshot)), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Caller
}

func TestServiceCallerUnknownAndSpoofing(t *testing.T) {
	for _, tc := range []struct {
		name, source, version, wantSource string
		development                       *bool
		wantVersion                       string
	}{
		{"builtin", "builtin", "1.2.3", "builtin", boolPointer(false), "1.2.3"},
		{"marketplace installation is local", "local", "1.2.3", "local", boolPointer(false), "1.2.3"},
		{"development", "development", "1.2.3", "development", boolPointer(true), "1.2.3"},
		{"unknown", "", "", "unknown", nil, ""},
		{"unrecognized source", "marketplace", "", "unknown", nil, ""},
		{"128 UTF8 bytes", "local", strings.Repeat("界", 42) + "ab", "local", boolPointer(false), strings.Repeat("界", 42) + "ab"},
		{"129 UTF8 bytes", "local", strings.Repeat("界", 43), "local", boolPointer(false), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := serviceProcessProvider(t, "service.short", "TestServiceMetadataHelperProcess", serviceTestEntry(), 16384)
			manifest := Manifest{ID: "caller.metadata", Version: tc.version}
			record := &pluginRecord{id: manifest.ID, source: tc.source, root: "/private/installation/token", developmentID: "secret-development-session", enabled: true, state: pluginStateRunning, manifest: &manifest}
			rt := fixture.manager.newRecordRuntime(record, record.root, manifest)
			rt.cmd, rt.generation = &exec.Cmd{}, 1
			record.runtime = rt
			fixture.manager.records[record.id] = record
			caller, err := captureServiceCaller(rt, 1)
			if err != nil {
				t.Fatal(err)
			}
			// Mutable discovery state must never rewrite the running process snapshot.
			record.manifest = &Manifest{ID: manifest.ID, Version: "9.9.9"}
			spoof := `echo:{"caller":{"kind":"host","pluginId":"victim","source":"builtin","development":false,"version":"spoof"},"source":"builtin","fullTrust":true}`
			input, _ := json.Marshal(spoof)
			snapshot, err := fixture.manager.runServiceCallSync(context.Background(), serviceCallRequest{Caller: caller, Provider: fixture.provider, Input: input})
			if err != nil {
				t.Fatal(err)
			}
			got := decodedServiceCaller(t, snapshot)
			if got["kind"] != "plugin" || got["pluginId"] != "caller.metadata" || got["source"] != tc.wantSource {
				t.Fatalf("caller = %v", got)
			}
			value, present := got["development"]
			if tc.development == nil {
				if present {
					t.Fatalf("unknown development became %v", value)
				}
			} else if !present || value != *tc.development {
				t.Fatalf("development = %v/%v", value, present)
			}
			version, present := got["version"]
			if tc.wantVersion == "" {
				if present {
					t.Fatalf("version should be absent: %v", version)
				}
			} else if version != tc.wantVersion {
				t.Fatalf("version = %v", version)
			}
			for key := range got {
				switch key {
				case "kind", "pluginId", "source", "development", "version":
				default:
					t.Fatalf("internal caller field leaked: %s", key)
				}
			}
			var envelope struct {
				Input string `json:"input"`
			}
			_ = json.Unmarshal([]byte(outputText(t, snapshot)), &envelope)
			if envelope.Input != spoof {
				t.Fatal("opaque input was merged into caller or rewritten")
			}
		})
	}
	t.Run("unowned and payload identity", func(t *testing.T) {
		fixture := serviceCallProvider(t, "service.short", serviceTestEntry(), 16384)
		useServicesManager(t, fixture.manager)
		if _, err := captureServiceCaller(newProcessRuntime("", Manifest{ID: "caller.a"}, nil, nil), 1); err == nil {
			t.Fatal("unowned process accepted")
		}
		rt := bindServiceTestCaller(t, fixture.manager, servicesCaller("caller.a", api.CapabilityPlugins))
		for _, extra := range []string{`"caller":{"pluginId":"victim","source":"builtin"}`, `"source":"builtin"`, `"development":false`} {
			if _, rpcErr := hostServicesInvokeGeneration(rt, 1, json.RawMessage(`{"service":"demo.text.reverse","input":"echo",`+extra+`}`)); rpcErr == nil || rpcErr.Code != -32602 {
				t.Fatalf("spoofed request accepted: %s / %v", extra, rpcErr)
			}
		}
		if providerStarted(fixture) {
			t.Fatal("invalid caller request activated provider")
		}
	})
}

func boolPointer(value bool) *bool { return &value }

func TestServiceCallerFrozenAcrossRecordReload(t *testing.T) {
	fixture := serviceProcessProvider(t, "service.tasks", "TestServiceMetadataHelperProcess", serviceTestEntry(), 16384)
	record := realServiceCaller(t, fixture.manager)
	caller, err := captureServiceCaller(record.runtime, 1)
	if err != nil {
		t.Fatal(err)
	}
	if caller.Snapshot.Metadata.Source != "local" || caller.Snapshot.Metadata.Version != "1.0.0" {
		t.Fatalf("original snapshot = %+v", caller.Snapshot)
	}
	replacement := replaceServiceTestRuntime(t, fixture.manager, record)
	if err := fixture.manager.validateServiceCaller(caller); err == nil {
		t.Fatal("old generation 1 accepted for new instance generation 1")
	}
	if caller.Snapshot.Metadata.Version != "1.0.0" {
		t.Fatal("reload rewrote captured metadata")
	}
	current, err := captureServiceCaller(replacement, 1)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.manager.runServiceCallSync(context.Background(), serviceCallRequest{Caller: current, Provider: fixture.provider, Input: json.RawMessage(`"echo"`)})
	if err != nil {
		t.Fatal(err)
	}
	got := decodedServiceCaller(t, snapshot)
	if got["version"] != "2.0.0" || got["source"] != "local" {
		t.Fatalf("replacement metadata = %v", got)
	}
}

func TestServiceProviderMetadataDowngrade(t *testing.T) {
	for _, id := range []string{"service.short", "service.tasks"} {
		t.Run(id, func(t *testing.T) {
			fixture := serviceProcessProvider(t, id, "TestServiceMetadataHelperProcess", serviceTestEntry(), 16384)
			if err := fixture.manager.startRecord(context.Background(), fixture.record); err != nil {
				t.Fatal(err)
			}
			record := fixture.record
			record.mu.Lock()
			legacy := *record.manifest
			legacy.Runtime.Args = []string{"-test.run=^TestServiceHelperProcess$"}
			next := fixture.manager.newRecordRuntime(record, record.root, legacy)
			old := record.runtime
			record.mu.Unlock()
			if err := next.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = old.Stop(context.Background()); _ = next.Stop(context.Background()) })
			request := fixture.request("caller.a", "echo")
			checks := 0
			request.Revalidate = func(record *pluginRecord) error {
				checks++
				// Already-running admission validates at entry, fast-path start, and
				// return. Check four is PrepareAdmitted's final validation, AFTER the
				// expanded payload has been built for the admitted old runtime.
				if checks == 4 {
					record.runtime = next
				}
				return nil
			}
			_, err := fixture.manager.startServiceCall(context.Background(), request)
			if checks != 4 || contributionErrorCode(err) != contributionErrUnavailable {
				t.Fatalf("downgrade checks=%d / %v", checks, err)
			}
			if !old.featureAtGeneration(1, "serviceCallerMetadata") || next.featureAtGeneration(1, "serviceCallerMetadata") {
				t.Fatal("fixture did not negotiate a real provider downgrade")
			}
			if _, err := os.Stat(filepath.Join(record.root, "service-dispatched")); !os.IsNotExist(err) {
				t.Fatalf("stale expanded payload dispatched: %v", err)
			}
			assertNoContributionRequestsLeft(t)
		})
	}
}

func TestServiceCallerOriginalRPCGeneration(t *testing.T) {
	for _, method := range []string{"host.services.invoke", "host.services.start"} {
		t.Run(method, func(t *testing.T) {
			fixture := serviceCallProvider(t, "service.short", serviceTestEntry(), 16384)
			useServicesManager(t, fixture.manager)
			rt := bindServiceTestCaller(t, fixture.manager, servicesCaller("caller.a", api.CapabilityPlugins))
			rt.runContext = context.Background()
			admitted, release := make(chan struct{}), make(chan struct{})
			var paused atomic.Bool
			hostMethodsMu.Lock()
			original := hostMethods[method]
			registration := original
			registration.gate = func(current *processRuntime) bool {
				if current == rt && paused.CompareAndSwap(false, true) {
					close(admitted)
					<-release
				}
				return true
			}
			hostMethods[method] = registration
			hostMethodsMu.Unlock()
			released := false
			t.Cleanup(func() {
				if !released {
					close(release)
				}
				hostMethodsMu.Lock()
				hostMethods[method] = original
				hostMethodsMu.Unlock()
			})
			done := make(chan *rpcError, 1)
			go func() {
				rt.serveHostRequest(1, rpcEnvelope{Method: method, Params: json.RawMessage(`{"service":"demo.text.reverse","input":"echo","interactive":false}`)}, func(_ any, rpcErr *rpcError) { done <- rpcErr })
			}()
			select {
			case <-admitted:
			case <-time.After(time.Second):
				t.Fatal("request never passed RPC ingress")
			}
			rt.mu.Lock()
			rt.generation = 2
			rt.mu.Unlock()
			close(release)
			released = true
			select {
			case rpcErr := <-done:
				if rpcErrorCode(rpcErr) != contributionErrCancelled {
					t.Fatalf("old ingress rebound to new generation: %v", rpcErr)
				}
			case <-time.After(time.Second):
				t.Fatal("old request did not finish")
			}
			if providerStarted(fixture) {
				t.Fatal("delayed RPC activated provider")
			}
		})
	}
}

func TestServiceCallerFinalPayloadBudget(t *testing.T) {
	// Old caller shape fits exactly. The same action limit must reject expanded
	// metadata, not enlarge the budget or silently truncate the caller identity.
	legacy := `{"callId":"sc_` + strings.Repeat("0", 48) + `","service":"demo.text.reverse","entry":"reverse","input":"echo","params":{"keep":true},"caller":{"kind":"plugin","pluginId":"caller.a"}}`
	for _, metadata := range []bool{false, true} {
		t.Run(strconv.FormatBool(metadata), func(t *testing.T) {
			helper := "TestServiceHelperProcess"
			if metadata {
				helper = "TestServiceMetadataHelperProcess"
			}
			fixture := serviceProcessProvider(t, "service.short", helper, serviceTestEntry(), len(legacy))
			snapshot, err := fixture.manager.runServiceCallSync(context.Background(), fixture.request("caller.a", "echo"))
			if metadata {
				if contributionErrorCode(err) != contributionErrTooLarge {
					t.Fatalf("expanded payload exceeded unchanged limit but got %v", err)
				}
				if _, err := os.Stat(filepath.Join(fixture.record.root, "service-dispatched")); !os.IsNotExist(err) {
					t.Fatalf("oversized metadata dispatched: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(outputText(t, snapshot)) != len(legacy) {
					t.Fatal("legacy budget fixture mismatch")
				}
			}
			assertNoContributionRequestsLeft(t)
		})
	}
}

func TestServiceCallerUnrelatedScopeChange(t *testing.T) {
	fixture := serviceCallProvider(t, "service.short", serviceTestEntry(), 16384)
	request := fixture.request("caller.a", "echo")
	if _, err := fixture.manager.BeginWorkspaceScope(context.Background(), "other-workspace"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.manager.runServiceCallSync(context.Background(), request)
	if err != nil || snapshot.State != serviceCallSucceeded {
		t.Fatalf("global service incorrectly workspace-bound: %+v / %v", snapshot, err)
	}
	request.Caller = serviceCaller{Kind: "host", Surface: serviceSurfacePalette}
	if _, err := fixture.manager.BeginWorkspaceScope(context.Background(), "third-workspace"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = fixture.manager.runServiceCallSync(context.Background(), request)
	if err != nil || snapshot.State != serviceCallSucceeded {
		t.Fatalf("host service incorrectly workspace-bound: %+v / %v", snapshot, err)
	}
}

func TestServiceCallerRawPluginInvokeUnchanged(t *testing.T) {
	fake := usePluginCallFakes(t, map[string]ManifestUIAction{"target/run": {ID: "run", Risk: "read", MaxPayloadBytes: 16384}})
	fake.grants["team-chat"] = []string{"target"}
	for _, tc := range []struct{ input, want string }{
		{`{"caller":{"kind":"host","source":"builtin"},"source":"development","nested":[false,1,"x"]}`, `{"caller":{"kind":"host","source":"builtin"},"source":"development","nested":[false,1,"x"]}`},
		{`[1,false,{"version":"spoof"}]`, `[1,false,{"version":"spoof"}]`},
		{`"hello"`, `"hello"`}, {`false`, `false`}, {`42`, `42`}, {`null`, `{}`},
	} {
		_, rpcErr := hostPluginsInvoke(uiRuntime("team-chat"), json.RawMessage(`{"pluginId":"target","action":"run","payload":`+tc.input+`}`))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		if got := string(fake.calls[len(fake.calls)-1].Payload); got != tc.want {
			t.Fatalf("raw payload changed: %s want %s", got, tc.want)
		}
	}
}

func TestServiceMetadataHelperProcess(t *testing.T) { TestServiceHelperProcess(t) }

// Catches expanding the legacy caller shape, omitting negotiated metadata,
// or applying plugin provenance to a host surface. Exercise actual dispatch.
func TestServiceCallerStrictCompatibility(t *testing.T) {
	for _, tasks := range []bool{false, true} {
		for _, metadata := range []bool{false, true} {
			name := "short"
			if tasks {
				name = "tasks"
			}
			if metadata {
				name += "-metadata"
			}
			t.Run(name, func(t *testing.T) {
				helper := "TestServiceHelperProcess"
				if metadata {
					helper = "TestServiceMetadataHelperProcess"
				}
				id := "service.short"
				if tasks {
					id = "service.tasks"
				}
				fixture := serviceProcessProvider(t, id, helper, serviceTestEntry(), 16384)
				request := fixture.request("caller.a", "echo")
				for _, host := range []bool{false, true} {
					if host {
						request.Caller = serviceCaller{Kind: "host", Surface: serviceSurfacePalette}
					}
					final, err := fixture.manager.runServiceCallSync(context.Background(), request)
					if err != nil {
						t.Fatal(err)
					}
					var envelope struct {
						Caller json.RawMessage `json:"caller"`
					}
					if err := json.Unmarshal([]byte(outputText(t, final)), &envelope); err != nil {
						t.Fatal(err)
					}
					if host || !metadata {
						var old struct {
							Kind     string `json:"kind"`
							PluginID string `json:"pluginId,omitempty"`
							Surface  string `json:"surface,omitempty"`
						}
						decoder := json.NewDecoder(strings.NewReader(string(envelope.Caller)))
						decoder.DisallowUnknownFields()
						if err := decoder.Decode(&old); err != nil {
							t.Fatalf("strict old decoder: %s: %v", envelope.Caller, err)
						}
						want := `{"kind":"plugin","pluginId":"caller.a"}`
						if host {
							want = `{"kind":"host","surface":"palette"}`
						}
						var gotMap, wantMap map[string]any
						_ = json.Unmarshal(envelope.Caller, &gotMap)
						_ = json.Unmarshal([]byte(want), &wantMap)
						got, _ := json.Marshal(gotMap)
						expected, _ := json.Marshal(wantMap)
						if string(got) != string(expected) {
							t.Fatalf("caller = %s, want %s", got, expected)
						}
					} else {
						var caller map[string]any
						_ = json.Unmarshal(envelope.Caller, &caller)
						if caller["source"] != "local" || caller["development"] != false || caller["version"] != "1.0.0" {
							t.Fatalf("negotiated caller metadata missing: %s", envelope.Caller)
						}
					}
				}
			})
		}
	}
}
