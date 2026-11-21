package plugin

import (
	"context"
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
	"mincode/app/backend/debug"
	"mincode/app/plugins/api"
)

const testRunProvider = "unit"

// testRunRecorder captures frontend payloads and tests.runEnded notices.
type testRunRecorder struct {
	mu       sync.Mutex
	payloads []TestRunEventPayload
	ended    []string // runId/reason
}

func (recorder *testRunRecorder) events(runID string) []TestRunEvent {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	events := []TestRunEvent{}
	for _, payload := range recorder.payloads {
		if payload.RunID == runID {
			events = append(events, payload.Events...)
		}
	}
	return events
}

func (recorder *testRunRecorder) endedNotices() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return slices.Clone(recorder.ended)
}

func useTestRunRecorder(t *testing.T) *testRunRecorder {
	t.Helper()
	recorder := &testRunRecorder{}
	previousEmit, previousNotify := testRunEmit, testRunNotifyEnded
	testRunEmit = func(payload TestRunEventPayload) {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		recorder.payloads = append(recorder.payloads, payload)
	}
	testRunNotifyEnded = func(_ *pluginManager, _, runID, reason string) {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		recorder.ended = append(recorder.ended, runID+"/"+reason)
	}
	t.Cleanup(func() {
		testRunEmit, testRunNotifyEnded = previousEmit, previousNotify
		testRuns.mu.Lock()
		for id, run := range testRuns.runs {
			if run.timer != nil {
				run.timer.Stop()
			}
			delete(testRuns.runs, id)
		}
		testRuns.mu.Unlock()
	})
	return recorder
}

// writeTestRunManifest extends the debugger fixture with a streaming test
// provider whose debugRun uses the fixture debugger "fake".
func writeTestRunManifest(t *testing.T) string {
	t.Helper()
	root := writeDebuggerManifest(t, []string{"shell", "ui", "workspace.read"})
	raw, err := os.ReadFile(filepath.Join(root, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	contributes := manifest["contributes"].(map[string]any)
	contributes["actions"] = append(contributes["actions"].([]any),
		map[string]any{"id": "tests.discover", "risk": "read"},
		map[string]any{"id": "tests.run", "risk": "read"},
		map[string]any{"id": "tests.resolveRun", "risk": "read", "maxPayloadBytes": 65536},
	)
	contributes["testProviders"] = []any{map[string]any{
		"id": testRunProvider, "title": "Unit", "discoverAction": "tests.discover", "runAction": "tests.run",
		"debugRun": map[string]any{"debuggerType": "fake", "resolveRunAction": "tests.resolveRun"},
	}}
	encoded, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

type testRunFixture struct {
	*debuggerFixture
	recorder *testRunRecorder
}

func newTestRunFixture(t *testing.T) *testRunFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses POSIX executables")
	}
	useTemporaryPluginData(t)
	recorder := useTestRunRecorder(t)
	fixture := &testRunFixture{debuggerFixture: newDebuggerFixtureAt(t, writeTestRunManifest(t)), recorder: recorder}
	fixture.setRunResult(fixture.python)
	return fixture
}

func (fixture *testRunFixture) setRunResult(python string) {
	encoded, _ := json.Marshal(python)
	fixture.setResult("tests.resolveRun", `{"variables":{"python":`+string(encoded)+`},"request":"launch","arguments":{"mainClass":"Runner","args":["-port","5000"]}}`)
}

func (fixture *testRunFixture) prepareRun(t *testing.T, debugRun bool, ids ...string) PreparedTestRun {
	t.Helper()
	prepared, err := fixture.manager.prepareTestRun(context.Background(), testRunPrepareRequest{
		PluginID: debuggerTestPlugin, ProviderID: testRunProvider, ProjectRoot: fixture.project, TestIDs: ids, Debug: debugRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func (fixture *testRunFixture) runtimeHost() *processRuntime {
	return &processRuntime{pluginID: debuggerTestPlugin, manifest: *fixture.record.manifest, generation: 1, ownerManager: fixture.manager,
		capabilities: map[api.Capability]struct{}{api.CapabilityWorkspaceRead: {}}}
}

func publishForTest(runtimeHost *processRuntime, runID string, events ...string) (TestRunPublishResult, *rpcError) {
	raw := `{"runId":` + jsonString(runID) + `,"events":[` + strings.Join(events, ",") + `]}`
	result, rpcErr := hostTestsPublish(runtimeHost, 1, json.RawMessage(raw))
	if rpcErr != nil {
		return TestRunPublishResult{}, rpcErr
	}
	return result.(TestRunPublishResult), nil
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testRunStateOf(runID string) (testRunState, bool) {
	testRuns.mu.Lock()
	defer testRuns.mu.Unlock()
	run := testRuns.runs[runID]
	if run == nil {
		return 0, false
	}
	return run.state, true
}

func TestTestProviderDebugRunDecode(t *testing.T) {
	ctx := batchTwoTestContext(testProviderSpec)
	ctx.actions["resolve"] = ManifestUIAction{ID: "resolve", Risk: "read"}
	ctx.actions["ai"] = ManifestUIAction{ID: "ai", Risk: "read", AICallable: true}
	ctx.resolved = map[string]*resolvedContribution{debuggerPointKey: {Entries: []any{DebuggerContribution{Type: "java"}}}}
	entry := func(debugRun string) json.RawMessage {
		return json.RawMessage(`{"id":"junit","title":"JUnit","discoverAction":"list","runAction":"write","debugRun":` + debugRun + `}`)
	}
	value, _, err := decodeTestProviderEntry(ctx, 0, "", entry(`{"debuggerType":" java ","resolveRunAction":"resolve"}`))
	if err != nil {
		t.Fatal(err)
	}
	provider := value.(TestProviderContribution)
	if provider.DebugRun == nil || provider.DebugRun.DebuggerType != "java" || provider.DebugRun.ResolveRunAction != "resolve" {
		t.Fatalf("provider = %+v", provider)
	}
	cases := map[string]string{
		`{"debuggerType":"python","resolveRunAction":"resolve"}`:            "not one of this plugin's contributes.debuggers",
		`{"debuggerType":"java","resolveRunAction":"write"}`:                `risk "read"`,
		`{"debuggerType":"java","resolveRunAction":"ai"}`:                   "aiCallable",
		`{"debuggerType":"java","resolveRunAction":"list"}`:                 "must differ",
		`{"debuggerType":"java","resolveRunAction":"missing"}`:              "contributes.actions",
		`{"debuggerType":"java"}`:                                           "needs debuggerType and resolveRunAction",
		`{"resolveRunAction":"resolve"}`:                                    "needs debuggerType and resolveRunAction",
		`{"debuggerType":"java","resolveRunAction":"resolve","extra":true}`: "unknown field",
		`"java"`: "parse test provider",
	}
	for raw, want := range cases {
		if _, _, err := decodeTestProviderEntry(ctx, 0, "", entry(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
	// Without accepted debuggers the type cannot be the plugin's.
	ctx.resolved = nil
	if _, _, err := decodeTestProviderEntry(ctx, 0, "", entry(`{"debuggerType":"java","resolveRunAction":"resolve"}`)); err == nil {
		t.Fatal("debugRun accepted without debuggers")
	}
}

func TestTestProviderDebugRunInstall(t *testing.T) {
	useTemporaryPluginData(t)
	preview, err := inspectInstallDirectory(writeTestRunManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	resolved := preview.Manifest.contribution(testProviderPointKey)
	if resolved == nil || len(resolved.Entries) != 1 {
		t.Fatalf("testProviders = %+v", resolved)
	}
	if provider := resolved.Entries[0].(TestProviderContribution); provider.DebugRun == nil || provider.DebugRun.DebuggerType != "fake" {
		t.Fatalf("provider = %+v", provider)
	}
	// testProviders resolves after debuggers, which resolves after jdtlsBundles.
	if contributionPointDependsRank(testProviderPointKey) <= contributionPointDependsRank(debuggerPointKey) ||
		contributionPointDependsRank(debuggerPointKey) <= contributionPointDependsRank(jdtlsBundlePointKey) {
		t.Fatal("dependency ranks are not ordered")
	}
	if spec, _ := api.ContributionPointSpecFor(testProviderPointKey); spec.KeySince("debugRun") != "0.3.0" {
		t.Fatalf("debugRun keySinceHost = %q", spec.KeySince("debugRun"))
	}
}

func TestTestProvidersExpandPermissionWithDebugRun(t *testing.T) {
	point := contributionPoints[testProviderPointKey]
	base := TestProviderContribution{ID: "a", Title: "A", DiscoverAction: "list", RunAction: "run", RunRisk: "read"}
	streaming := base
	streaming.DebugRun = &TestProviderDebugRun{DebuggerType: "java", ResolveRunAction: "resolve"}
	wrap := func(provider TestProviderContribution) *resolvedContribution {
		return &resolvedContribution{Entries: []any{provider}}
	}
	if !point.ExpandsPermission(wrap(base), wrap(streaming)) {
		t.Fatal("adding debugRun must expand permission")
	}
	if point.ExpandsPermission(wrap(streaming), wrap(streaming)) {
		t.Fatal("an unchanged debugRun must not expand permission")
	}
}

func TestPrepareTestRunCallsResolveRun(t *testing.T) {
	fixture := newTestRunFixture(t)
	prepared := fixture.prepareRun(t, false, "pkg.CalcTest", "pkg.CalcTest#adds")
	if !strings.HasPrefix(prepared.RunID, "run-") || prepared.PrepareID == "" || prepared.Type != "fake" || prepared.Request != "launch" {
		t.Fatalf("prepared = %+v", prepared)
	}
	fixture.mu.Lock()
	payloads := fixture.payloads["tests.resolveRun"]
	fixture.mu.Unlock()
	if len(payloads) != 1 {
		t.Fatalf("payloads = %v", payloads)
	}
	payload := payloads[0]
	project, _ := payload["project"].(map[string]any)
	ids, _ := payload["testIds"].([]any)
	if payload["provider"] != testRunProvider || payload["runId"] != prepared.RunID || payload["debug"] != false ||
		project["root"] == nil || len(ids) != 2 || ids[1] != "pkg.CalcTest#adds" {
		t.Fatalf("payload = %v", payload)
	}
	// A run without debugging launches with noDebug.
	var arguments map[string]any
	if err := json.Unmarshal(prepared.Arguments, &arguments); err != nil || arguments["noDebug"] != true || arguments["mainClass"] != "Runner" {
		t.Fatalf("arguments = %s", prepared.Arguments)
	}
	debugRun := fixture.prepareRun(t, true)
	arguments = nil
	if err := json.Unmarshal(debugRun.Arguments, &arguments); err != nil || arguments["noDebug"] != nil {
		t.Fatalf("debug arguments = %s", debugRun.Arguments)
	}
	// Test runs are never saved as run configurations: no configuration field.
	if _, ok := payload["configuration"]; ok {
		t.Fatalf("payload has a configuration: %v", payload)
	}
	// Each run resolves again: a second prepare called resolveRun again.
	fixture.mu.Lock()
	calls := len(fixture.payloads["tests.resolveRun"])
	fixture.mu.Unlock()
	if calls != 2 {
		t.Fatalf("resolveRun calls = %d", calls)
	}
}

func TestPrepareTestRunRejectsBadRequestsAndResults(t *testing.T) {
	fixture := newTestRunFixture(t)
	for name, request := range map[string]testRunPrepareRequest{
		"unknown provider": {PluginID: debuggerTestPlugin, ProviderID: "other", ProjectRoot: fixture.project},
		"bad test id":      {PluginID: debuggerTestPlugin, ProviderID: testRunProvider, ProjectRoot: fixture.project, TestIDs: []string{" x"}},
		"repeated id":      {PluginID: debuggerTestPlugin, ProviderID: testRunProvider, ProjectRoot: fixture.project, TestIDs: []string{"a", "a"}},
		"bad plugin":       {PluginID: "Bad Plugin", ProviderID: testRunProvider, ProjectRoot: fixture.project},
	} {
		if _, err := fixture.manager.prepareTestRun(context.Background(), request); err == nil {
			t.Errorf("%s: prepared", name)
		}
	}
	for _, result := range []string{
		`{"request":"run","arguments":{}}`,
		`{"request":"launch","arguments":[]}`,
		`{"request":"launch","arguments":{},"extra":1}`,
	} {
		fixture.setResult("tests.resolveRun", result)
		_, err := fixture.manager.prepareTestRun(context.Background(), testRunPrepareRequest{PluginID: debuggerTestPlugin, ProviderID: testRunProvider, ProjectRoot: fixture.project})
		if contributionErrorCode(err) != contributionErrInvalidResult {
			t.Errorf("%s: err = %v", result, err)
		}
	}
	// Every failed prepare closed its run and told the plugin.
	if testRunsLive(debuggerTestPlugin) {
		t.Fatal("a failed prepare left a live run")
	}
	if notices := fixture.recorder.endedNotices(); len(notices) != 3 || !strings.HasSuffix(notices[0], "/"+testRunEndFailed) {
		t.Fatalf("notices = %v", notices)
	}
}

func TestTestRunSessionLifecycle(t *testing.T) {
	fixture := newTestRunFixture(t)
	previousGrace := testRunGrace
	testRunGrace = 50 * time.Millisecond
	t.Cleanup(func() { testRunGrace = previousGrace })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixture.setRunResult(executable)
	prepared := fixture.prepareRun(t, true)
	if state, ok := testRunStateOf(prepared.RunID); !ok || state != testRunPreparing || !testRunsLive(debuggerTestPlugin) {
		t.Fatalf("state = %v %v", state, ok)
	}
	// A preparing run is in-flight work: idle stop waits.
	runtimeHost := fixture.runtimeHost()
	if !fixture.manager.pluginBusy(fixture.record, runtimeHost) {
		t.Fatal("a live test run must keep the plugin busy")
	}
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	result, err := debuggerStartForTest(fixture.manager, prepared.PreparedDebugLaunch, prepared.Launch.Fingerprint, nil)
	if err != nil {
		t.Fatal(err)
	}
	testRuns.mu.Lock()
	run := testRuns.runs[prepared.RunID]
	bound := run != nil && run.state == testRunRunning && run.sessionID == result.SessionID
	testRuns.mu.Unlock()
	if !bound {
		t.Fatalf("run not bound to session %s", result.SessionID)
	}
	if _, rpcErr := publishForTest(runtimeHost, prepared.RunID,
		`{"type":"tree","id":"pkg.CalcTest","label":"CalcTest","kind":"suite","path":"src/CalcTest.java","line":3}`,
		`{"type":"tree","id":"pkg.CalcTest#adds","parentId":"pkg.CalcTest","label":"adds()","kind":"test"}`,
		`{"type":"started","id":"pkg.CalcTest#adds"}`); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	// The session ends without finished: the plugin is told at once, late
	// results are accepted during the grace period, then the run ends unfinished.
	session, _ := debug.Lookup(result.SessionID)
	debug.StopSession(context.Background(), session, "test")
	waitFor(t, "tests.runEnded", func() bool { return len(fixture.recorder.endedNotices()) == 1 })
	if notice := fixture.recorder.endedNotices()[0]; notice != prepared.RunID+"/"+testRunEndSessionEnded {
		t.Fatalf("notice = %s", notice)
	}
	if _, rpcErr := publishForTest(runtimeHost, prepared.RunID, `{"type":"passed","id":"pkg.CalcTest#adds","durationMs":4.6}`); rpcErr != nil {
		t.Fatalf("late result during grace: %v", rpcErr)
	}
	waitFor(t, "run closed", func() bool { _, ok := testRunStateOf(prepared.RunID); return !ok })
	events := fixture.recorder.events(prepared.RunID)
	last := events[len(events)-1]
	if last.Type != "ended" || last.Finished == nil || *last.Finished || last.Reason != testRunEndSessionEnded {
		t.Fatalf("last event = %+v", last)
	}
	if events[len(events)-2].Type != "passed" || *events[len(events)-2].DurationMs != 5 {
		t.Fatalf("events = %+v", events)
	}
	// Late events after the run closed are refused; the plugin is no longer busy.
	if _, rpcErr := publishForTest(runtimeHost, prepared.RunID, `{"type":"started","id":"x"}`); rpcErr == nil || rpcErr.Code != -32001 {
		t.Fatalf("publish after close: %v", rpcErr)
	}
	if testRunsLive(debuggerTestPlugin) || fixture.manager.pluginBusy(fixture.record, runtimeHost) {
		t.Fatal("closed run still busy")
	}
	if len(fixture.recorder.endedNotices()) != 1 {
		t.Fatalf("notices = %v", fixture.recorder.endedNotices())
	}
}

func TestTestRunFinishedClosesBeforeSessionEnds(t *testing.T) {
	fixture := newTestRunFixture(t)
	executable, _ := os.Executable()
	fixture.setRunResult(executable)
	prepared := fixture.prepareRun(t, false)
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	result, err := debuggerStartForTest(fixture.manager, prepared.PreparedDebugLaunch, prepared.Launch.Fingerprint, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtimeHost := fixture.runtimeHost()
	published, rpcErr := publishForTest(runtimeHost, prepared.RunID,
		`{"type":"passed","id":"a"}`, `{"type":"finished","durationMs":12}`, `{"type":"passed","id":"b"}`)
	if rpcErr != nil || published.Accepted != 2 || published.Dropped != 1 {
		t.Fatalf("published = %+v, %v", published, rpcErr)
	}
	events := fixture.recorder.events(prepared.RunID)
	if len(events) != 3 || events[1].Type != "finished" || events[2].Type != "ended" || !*events[2].Finished || events[2].Reason != testRunEndFinished {
		t.Fatalf("events = %+v", events)
	}
	if testRunsLive(debuggerTestPlugin) {
		t.Fatal("a finished run is still live")
	}
	if _, rpcErr := publishForTest(runtimeHost, prepared.RunID, `{"type":"passed","id":"c"}`); rpcErr == nil {
		t.Fatal("publish after finished accepted")
	}
	// The plugin is told when the session ends, once.
	if len(fixture.recorder.endedNotices()) != 0 {
		t.Fatalf("told before the session ended: %v", fixture.recorder.endedNotices())
	}
	session, _ := debug.Lookup(result.SessionID)
	debug.StopSession(context.Background(), session, "test")
	waitFor(t, "tests.runEnded", func() bool { return len(fixture.recorder.endedNotices()) == 1 })
	waitFor(t, "run forgotten", func() bool { _, ok := testRunStateOf(prepared.RunID); return !ok })
}

func TestTestRunPublishRejectsForeignAndUnknownRuns(t *testing.T) {
	fixture := newTestRunFixture(t)
	prepared := fixture.prepareRun(t, false)
	other := &testRun{id: "run-other", pluginID: "other.plugin", providerID: testRunProvider, root: fixture.project, ids: map[string]struct{}{}}
	testRuns.mu.Lock()
	testRuns.runs[other.id] = other
	testRuns.mu.Unlock()
	runtimeHost := fixture.runtimeHost()
	for _, runID := range []string{"run-other", "run-forged", ""} {
		if _, rpcErr := publishForTest(runtimeHost, runID, `{"type":"started","id":"a"}`); rpcErr == nil {
			t.Errorf("%q: accepted", runID)
		}
	}
	if events := fixture.recorder.events("run-other"); len(events) != 0 {
		t.Fatalf("foreign run received events: %+v", events)
	}
	// The plugin's own run is unaffected.
	if result, rpcErr := publishForTest(runtimeHost, prepared.RunID, `{"type":"started","id":"a"}`); rpcErr != nil || result.Accepted != 1 {
		t.Fatalf("own run: %+v %v", result, rpcErr)
	}
	// A stale process generation is refused.
	if _, rpcErr := hostTestsPublish(runtimeHost, 2, json.RawMessage(`{"runId":"`+prepared.RunID+`","events":[]}`)); rpcErr == nil {
		t.Fatal("stale generation accepted")
	}
	// The method is hidden from plugins without debugRun.
	if declaresTestDebugRun(&processRuntime{}) {
		t.Fatal("gate open without debugRun")
	}
	if !declaresTestDebugRun(runtimeHost) {
		t.Fatal("gate closed with debugRun")
	}
}

func TestTestRunPublishLimitsAndValidation(t *testing.T) {
	fixture := newTestRunFixture(t)
	prepared := fixture.prepareRun(t, false)
	runtimeHost := fixture.runtimeHost()
	// Too many events in one call.
	many := make([]string, testRunPublishEventsMax+1)
	for index := range many {
		many[index] = `{"type":"started","id":"a"}`
	}
	if _, rpcErr := publishForTest(runtimeHost, prepared.RunID, many...); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("too many events: %v", rpcErr)
	}
	// Too many bytes.
	big := `{"type":"output","text":"` + strings.Repeat("x", testRunPublishMaxBytes) + `"}`
	if _, rpcErr := publishForTest(runtimeHost, prepared.RunID, big); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("too large: %v", rpcErr)
	}
	// Strict params.
	if _, rpcErr := hostTestsPublish(runtimeHost, 1, json.RawMessage(`{"runId":"`+prepared.RunID+`","events":[],"x":1}`)); rpcErr == nil {
		t.Fatal("unknown param accepted")
	}
	longTrace := strings.Repeat("é", testRunTraceMaxBytes)
	result, rpcErr := publishForTest(runtimeHost, prepared.RunID,
		`{"type":"tree","id":"s","label":"Suite","kind":"suite"}`,
		`{"type":"failed","id":"s#t","durationMs":3,"message":"boom\u001b[31m","expected":"","actual":"2","trace":`+jsonString(longTrace)+`}`,
		`{"type":"output","id":"s#t","text":"line\r\n"}`,
		`{"type":"unknown","id":"x"}`,
		`{"type":"tree","id":"x","label":"X","kind":"method"}`,
		`{"type":"tree","id":"x","label":"","kind":"test"}`,
		`{"type":"tree","id":"x","label":"two\nlines","kind":"test"}`,
		`{"type":"tree","id":"x","parentId":"x","label":"X","kind":"test"}`,
		`{"type":"tree","id":"x","label":"X","kind":"test","line":3}`,
		`{"type":"tree","id":"x","label":"X","kind":"test","path":"../x"}`,
		`{"type":"started","id":"x","message":"no"}`,
		`{"type":"started"}`,
		`{"type":"passed","id":"x","durationMs":-1}`,
		`{"type":"passed","id":" x"}`,
		`{"type":"output","text":""}`,
		`{"type":"finished","id":"x"}`,
		`"text"`,
	)
	if rpcErr != nil || result.Accepted != 3 || result.Dropped != 14 {
		t.Fatalf("result = %+v %v", result, rpcErr)
	}
	events := fixture.recorder.events(prepared.RunID)
	failed := events[1]
	if failed.Message != "boom" || failed.Expected == nil || *failed.Expected != "" || *failed.Actual != "2" || len(failed.Trace) > testRunTraceMaxBytes ||
		!strings.HasPrefix(failed.Trace, "éé") || events[2].Text != "line\n" {
		t.Fatalf("events = %+v", events)
	}
	// More than publishPerSecond calls in a second are refused.
	previous := testRunPublishLimiter
	testRunPublishLimiter = newHostRateLimiter(2, time.Second)
	t.Cleanup(func() { testRunPublishLimiter = previous })
	for range 2 {
		if _, rpcErr := publishForTest(runtimeHost, prepared.RunID, `{"type":"started","id":"s"}`); rpcErr != nil {
			t.Fatal(rpcErr)
		}
	}
	if _, rpcErr := publishForTest(runtimeHost, prepared.RunID, `{"type":"started","id":"s"}`); rpcErr == nil || rpcErr.Code != -32029 {
		t.Fatalf("rate limit: %v", rpcErr)
	}
	// The rate limit applies before the params are decoded.
	if _, rpcErr := hostTestsPublish(runtimeHost, 1, json.RawMessage(`not json`)); rpcErr == nil || rpcErr.Code != -32029 {
		t.Fatalf("rate limit before decode: %v", rpcErr)
	}
}

func TestTestRunNodeBudgetTruncates(t *testing.T) {
	fixture := newTestRunFixture(t)
	previous := testRunNodesMax
	testRunNodesMax = 3
	t.Cleanup(func() { testRunNodesMax = previous })
	prepared := fixture.prepareRun(t, false)
	runtimeHost := fixture.runtimeHost()
	result, rpcErr := publishForTest(runtimeHost, prepared.RunID,
		`{"type":"tree","id":"s","label":"S","kind":"suite"}`,
		`{"type":"tree","id":"s[1]","parentId":"s","label":"[1]","kind":"test"}`,
		`{"type":"tree","id":"s[2]","parentId":"s","label":"[2]","kind":"test"}`,
		`{"type":"tree","id":"s[3]","parentId":"s","label":"[3]","kind":"test"}`,
		`{"type":"passed","id":"s[4]"}`,
		`{"type":"passed","id":"s[1]"}`,
		`{"type":"output","text":"still forwarded"}`,
		`{"type":"output","id":"s[9]","text":"output of an unknown id over the cap"}`,
		`{"type":"output","id":"s[1]","text":"known id kept"}`,
	)
	if rpcErr != nil || result.Accepted != 7 || result.Dropped != 2 {
		t.Fatalf("result = %+v %v", result, rpcErr)
	}
	events := fixture.recorder.events(prepared.RunID)
	if last, before := events[len(events)-1], events[len(events)-2]; last.ID != "s[1]" || before.ID != "" || before.Text == "" {
		t.Fatalf("output events = %+v %+v", before, last)
	}
	truncated := 0
	for _, event := range fixture.recorder.events(prepared.RunID) {
		if event.Type == "truncated" {
			truncated++
		}
	}
	if truncated != 1 {
		t.Fatalf("truncated notices = %d", truncated)
	}
}

func TestTestRunSupersedesSameProviderAndProject(t *testing.T) {
	fixture := newTestRunFixture(t)
	first := fixture.prepareRun(t, false)
	second := fixture.prepareRun(t, true)
	if _, ok := testRunStateOf(first.RunID); ok {
		t.Fatal("the first run survived a new run of the same provider and project")
	}
	if state, ok := testRunStateOf(second.RunID); !ok || state != testRunPreparing {
		t.Fatal("the second run is not preparing")
	}
	events := fixture.recorder.events(first.RunID)
	if len(events) != 1 || events[0].Type != "ended" || events[0].Reason != testRunEndSuperseded {
		t.Fatalf("first events = %+v", events)
	}
	if notices := fixture.recorder.endedNotices(); len(notices) != 1 || notices[0] != first.RunID+"/"+testRunEndSuperseded {
		t.Fatalf("notices = %v", notices)
	}
	// The superseded run's prepared launch can no longer start.
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: first.PrepareID, Fingerprint: first.Launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	if _, err := debuggerStartForTest(fixture.manager, first.PreparedDebugLaunch, first.Launch.Fingerprint, nil); processLaunchErrorCode(err) != processLaunchErrNotFound {
		t.Fatalf("start of a superseded run: %v", err)
	}
	// Another project runs in parallel; runsPerPlugin bounds the total.
	for index := 1; index < testRunsPerPlugin; index++ {
		if _, err := fixture.manager.prepareTestRun(context.Background(), testRunPrepareRequest{PluginID: debuggerTestPlugin, ProviderID: testRunProvider, ProjectRoot: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.manager.prepareTestRun(context.Background(), testRunPrepareRequest{PluginID: debuggerTestPlugin, ProviderID: testRunProvider, ProjectRoot: t.TempDir()}); processLaunchErrorCode(err) != processLaunchErrLimit {
		t.Fatalf("over runsPerPlugin: %v", err)
	}
}

func TestTestRunStopExpireAndPluginStop(t *testing.T) {
	fixture := newTestRunFixture(t)
	// Stopping a preparing run closes it at once and tells the plugin.
	prepared := fixture.prepareRun(t, false)
	if !fixture.manager.stopTestRun(prepared.RunID, testRunEndStopped) || fixture.manager.stopTestRun(prepared.RunID, testRunEndStopped) {
		t.Fatal("stop must report a live run once")
	}
	if events := fixture.recorder.events(prepared.RunID); len(events) != 1 || events[0].Reason != testRunEndStopped || *events[0].Finished {
		t.Fatalf("events = %+v", events)
	}
	// A run whose session never starts expires.
	previous := testRunPrepareTTL
	testRunPrepareTTL = 20 * time.Millisecond
	t.Cleanup(func() { testRunPrepareTTL = previous })
	expiring := fixture.prepareRun(t, false)
	waitFor(t, "expiry", func() bool { _, ok := testRunStateOf(expiring.RunID); return !ok })
	if events := fixture.recorder.events(expiring.RunID); len(events) != 1 || events[0].Reason != testRunEndExpired {
		t.Fatalf("expired events = %+v", events)
	}
	testRunPrepareTTL = previous
	// A plugin stop or crash ends its runs without telling the (gone) process.
	live := fixture.prepareRun(t, false)
	notices := len(fixture.recorder.endedNotices())
	fixture.manager.runRecordLifecycleHooks(fixture.record)
	if _, ok := testRunStateOf(live.RunID); ok || testRunsLive(debuggerTestPlugin) {
		t.Fatal("run survived the plugin stop")
	}
	if events := fixture.recorder.events(live.RunID); len(events) != 1 || events[0].Reason != testRunEndPluginStopped || *events[0].Finished {
		t.Fatalf("plugin stop events = %+v", events)
	}
	if len(fixture.recorder.endedNotices()) != notices {
		t.Fatal("a stopped plugin was told tests.runEnded")
	}
}

func TestTestRunCommandsAndEventRegistered(t *testing.T) {
	if _, ok := registeredHostMethod(testRunPublishMethod); !ok {
		t.Fatal("host.tests.publish is not registered")
	}
	if capability, ok := hostEvents[testRunEndedEvent]; !ok || capability != api.CapabilityWorkspaceRead {
		t.Fatalf("tests.runEnded event = %q %v", capability, ok)
	}
	args, _ := json.Marshal(map[string]any{"payload": map[string]any{"runId": "run-missing"}})
	out, err := pluginHostTestRunStopHandler(context.Background(), "", string(args))
	if err != nil || !strings.Contains(out, `"stopped":false`) {
		t.Fatalf("stop = %s, %v", out, err)
	}
}

func TestTestRunUserStopUsesShortGrace(t *testing.T) {
	fixture := newTestRunFixture(t)
	previous := testRunStoppedGrace
	testRunStoppedGrace = 20 * time.Millisecond
	t.Cleanup(func() { testRunStoppedGrace = previous })
	executable, _ := os.Executable()
	fixture.setRunResult(executable)
	prepared := fixture.prepareRun(t, false)
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	if _, err := debuggerStartForTest(fixture.manager, prepared.PreparedDebugLaunch, prepared.Launch.Fingerprint, nil); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if !fixture.manager.stopTestRun(prepared.RunID, testRunEndStopped) {
		t.Fatal("stop of a running run reported false")
	}
	waitFor(t, "stopped run closed", func() bool { _, ok := testRunStateOf(prepared.RunID); return !ok })
	if elapsed := time.Since(started); elapsed > testRunGrace {
		t.Fatalf("stopped run took %v to close", elapsed)
	}
	events := fixture.recorder.events(prepared.RunID)
	if last := events[len(events)-1]; last.Type != "ended" || last.Reason != testRunEndStopped || *last.Finished {
		t.Fatalf("last = %+v", last)
	}
}

// A user stop of the run's session from the debug panel (debug_stop) ends
// the run like a stop from the Tests tab: short grace, reason "stopped".
func TestTestRunDebugPanelStopEndsAsStopped(t *testing.T) {
	fixture := newTestRunFixture(t)
	previous := testRunStoppedGrace
	testRunStoppedGrace = 20 * time.Millisecond
	t.Cleanup(func() { testRunStoppedGrace = previous })
	executable, _ := os.Executable()
	fixture.setRunResult(executable)
	prepared := fixture.prepareRun(t, true)
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	result, err := debuggerStartForTest(fixture.manager, prepared.PreparedDebugLaunch, prepared.Launch.Fingerprint, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	args, _ := json.Marshal(map[string]any{"sessionId": result.SessionID})
	if _, err := core.DispatchContext(context.Background(), "debug_stop", string(args)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped run closed", func() bool { _, ok := testRunStateOf(prepared.RunID); return !ok })
	if elapsed := time.Since(started); elapsed > testRunGrace {
		t.Fatalf("debug panel stop took %v to close the run", elapsed)
	}
	events := fixture.recorder.events(prepared.RunID)
	if last := events[len(events)-1]; last.Type != "ended" || last.Reason != testRunEndStopped || *last.Finished {
		t.Fatalf("last = %+v", last)
	}
}

// A new run that fails to prepare leaves the previous run of the same
// provider and project running: it is superseded only once the new run's
// launch is prepared.
func TestTestRunFailedPrepareKeepsPreviousRun(t *testing.T) {
	fixture := newTestRunFixture(t)
	first := fixture.prepareRun(t, false)
	fixture.setResult("tests.resolveRun", `{"request":"run","arguments":{}}`)
	if _, err := fixture.manager.prepareTestRun(context.Background(), testRunPrepareRequest{PluginID: debuggerTestPlugin, ProviderID: testRunProvider, ProjectRoot: fixture.project}); err == nil {
		t.Fatal("a bad resolve result prepared")
	}
	if state, ok := testRunStateOf(first.RunID); !ok || state != testRunPreparing {
		t.Fatalf("the first run did not survive a failed prepare: %v %v", state, ok)
	}
	fixture.setRunResult(fixture.python)
	second := fixture.prepareRun(t, false)
	if _, ok := testRunStateOf(first.RunID); ok {
		t.Fatal("the first run survived a prepared run of the same provider and project")
	}
	if state, ok := testRunStateOf(second.RunID); !ok || state != testRunPreparing {
		t.Fatal("the second run is not preparing")
	}
}

// A close that races with a publish emits its ended event after the
// publish's results, never before them.
func TestTestRunEndedFollowsRacingPublish(t *testing.T) {
	fixture := newTestRunFixture(t)
	prepared := fixture.prepareRun(t, false)
	recording := testRunEmit
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	testRunEmit = func(payload TestRunEventPayload) {
		if len(payload.Events) > 0 && payload.Events[0].Type == "started" {
			once.Do(func() { close(entered) })
			<-release
		}
		recording(payload)
	}
	published := make(chan *rpcError, 1)
	go func() {
		_, rpcErr := publishForTest(fixture.runtimeHost(), prepared.RunID, `{"type":"started","id":"a"}`)
		published <- rpcErr
	}()
	<-entered
	closed := make(chan struct{})
	go func() {
		fixture.manager.closeTestRun(prepared.RunID, testRunEndStopped, true)
		close(closed)
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	if rpcErr := <-published; rpcErr != nil {
		t.Fatal(rpcErr)
	}
	<-closed
	events := fixture.recorder.events(prepared.RunID)
	if len(events) != 2 || events[0].Type != "started" || events[1].Type != "ended" {
		t.Fatalf("events = %+v", events)
	}
}
