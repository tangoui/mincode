package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mincode/app/backend/core"
	"mincode/app/backend/debug"
	"mincode/app/plugins/api"
)

// Streaming test runs (testProviders debugRun, host 0.3.0). A run is a debug
// session of the provider plugin's own debugger whose results the plugin
// streams back:
//
//  1. plugin_host_test_run_prepare {pluginId, providerId, projectRoot,
//     testIds, debug} registers a run under a new random runId, calls the
//     provider's resolveRunAction with {provider, project: {root}, testIds,
//     debug, runId} and turns its {variables?, request, arguments} result into
//     a prepared debug launch through the one debugger prepare path
//     (prepareDebuggerLaunch; debug false sets arguments.noDebug). It returns
//     the prepared launch plus runId. Once the launch is prepared, an older
//     run of the same plugin, provider and project that is still live is
//     stopped (reason "superseded"): one run per provider and project. A
//     prepare that fails leaves the older run alone.
//  2. The frontend confirms and starts it with the ordinary
//     plugin_host_debugger_trust / plugin_host_debugger_start (the one launch
//     path); start binds the new session to the run and refuses a run that
//     has ended. Test runs are never saved as run configurations.
//  3. While the run is live the plugin calls host.tests.publish {runId,
//     events}: only the run's own plugin, its current process generation, at
//     most publishMaxBytes / publishEventsMax / publishPerSecond; each event
//     is validated into the generic shapes below (an invalid event is dropped
//     and counted) and forwarded to the frontend as plugin:test-run-event
//     {runId, pluginId, events}. A run accepts at most runNodesMax distinct
//     test ids; events naming more are dropped and the frontend is told once
//     ({type: "truncated"}).
//  4. When the session ends the plugin gets the event tests.runEnded {runId,
//     reason} (plugin.event, sent to the run's plugin only) and the run keeps
//     accepting late results for runGraceMs; a finished event closes it at
//     once. A run closes with a final host event {type: "ended", finished,
//     reason}; the frontend marks tests without a result as terminated when
//     finished is false. Later events are refused.
//
// A live run (preparing, running, or in its grace period) is in-flight work
// for idle stop. A run still preparing when its prepared launch expires is
// closed ("expired"); a plugin stop or crash closes its runs ("pluginStopped",
// its process is gone, so no tests.runEnded). No AI path reaches any of it.
const (
	testRunFrontendEvent = "plugin:test-run-event"
	testRunPublishMethod = "host.tests.publish"
	testRunEndedEvent    = "tests.runEnded"

	testRunEndFinished      = "finished"
	testRunEndSessionEnded  = "sessionEnded"
	testRunEndSuperseded    = "superseded"
	testRunEndStopped       = "stopped"
	testRunEndPluginStopped = "pluginStopped"
	testRunEndExpired       = "expired"
	testRunEndFailed        = "failed"
)

var (
	testRunResolveTimeout   = time.Duration(testProviderSpec.Limit("resolveRunTimeoutMs")) * time.Millisecond
	testRunResolveMaxBytes  = int(testProviderSpec.Limit("resolveRunResultMaxBytes"))
	testRunPublishMaxBytes  = int(testProviderSpec.Limit("publishMaxBytes"))
	testRunPublishPerSecond = int(testProviderSpec.Limit("publishPerSecond"))
	testRunPublishEventsMax = int(testProviderSpec.Limit("publishEventsMax"))
	testRunTraceMaxBytes    = int(testProviderSpec.Limit("traceMaxBytes"))
	testRunOutputMaxBytes   = int(testProviderSpec.Limit("outputTextMaxBytes"))
	testRunNodesMax         = int(testProviderSpec.Limit("runNodesMax"))
	testRunsPerPlugin       = int(testProviderSpec.Limit("runsPerPlugin"))
	testRunEventTypes       = testProviderSpec.List("runEventTypes")
	testRunTreeKinds        = testProviderSpec.List("treeKinds")

	// testRunGrace is how long a run accepts late results after its session
	// ended; testRunPrepareTTL how long a run may wait for its session (the
	// prepared launch's lifetime plus a margin). Variables so tests can
	// shorten them.
	testRunGrace      = time.Duration(testProviderSpec.Limit("runGraceMs")) * time.Millisecond
	testRunPrepareTTL = debuggerPrepareTTL + 10*time.Second
	// testRunStoppedGrace is the grace of a run whose session the host
	// stopped (stopped, superseded).
	testRunStoppedGrace = 2 * time.Second

	// testRunPublishLimiter bounds host.tests.publish calls per plugin.
	testRunPublishLimiter = newHostRateLimiter(testRunPublishPerSecond, time.Second)

	// testRunEmit pushes one payload to the frontend; testRunNotifyEnded
	// tells the run's plugin that its session ended. Tests replace both.
	testRunEmit        = func(payload TestRunEventPayload) { core.Emit(testRunFrontendEvent, payload) }
	testRunNotifyEnded = func(manager *pluginManager, pluginID, runID, reason string) {
		manager.notifyTestRunEnded(pluginID, runID, reason)
	}
)

type testRunState int

const (
	testRunPreparing testRunState = iota
	testRunRunning
	// testRunEnding: the session ended; late results are accepted until the
	// grace timer closes the run.
	testRunEnding
	// testRunClosed: no more events; kept only until its session ends so the
	// plugin still gets tests.runEnded.
	testRunClosed
)

type testRun struct {
	id, pluginID, providerID, root string
	// seq orders runs by registration: a prepared run supersedes only older
	// runs of its provider and project.
	seq          uint64
	state        testRunState
	sessionID    string
	session      *debug.Session
	sessionEnded bool
	ids          map[string]struct{}
	truncated    bool
	runEndedSent bool
	// stopReason is the close reason of a run whose session the host or the
	// user stopped (superseded, stopped: stopTestRun, or debug_stop from the
	// debug panel); its session end then closes the run with it.
	stopReason string
	timer      *time.Timer
	// emitMu orders the run's frontend payloads: a publish takes it before
	// testRuns.mu is released, so a close that races with it emits "ended"
	// after the publish's results, never before.
	emitMu sync.Mutex
}

var testRuns = struct {
	mu   sync.Mutex
	runs map[string]*testRun
	seq  uint64
}{runs: map[string]*testRun{}}

// TestRunEvent is one validated event as the frontend receives it. The
// plugin types are tree, started, passed, failed, errored, skipped, output
// and finished; the host adds truncated and ended (Finished, Reason).
type TestRunEvent struct {
	Type       string  `json:"type"`
	ID         string  `json:"id,omitempty"`
	ParentID   string  `json:"parentId,omitempty"`
	Label      string  `json:"label,omitempty"`
	Kind       string  `json:"kind,omitempty"`
	Path       string  `json:"path,omitempty"`
	Line       int     `json:"line,omitempty"`
	DurationMs *int64  `json:"durationMs,omitempty"`
	Message    string  `json:"message,omitempty"`
	Expected   *string `json:"expected,omitempty"`
	Actual     *string `json:"actual,omitempty"`
	Trace      string  `json:"trace,omitempty"`
	Text       string  `json:"text,omitempty"`
	Finished   *bool   `json:"finished,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}

// TestRunEventPayload is the plugin:test-run-event payload.
type TestRunEventPayload struct {
	RunID    string         `json:"runId"`
	PluginID string         `json:"pluginId"`
	Events   []TestRunEvent `json:"events"`
}

type testRunPrepareRequest struct {
	PluginID    string   `json:"pluginId"`
	ProviderID  string   `json:"providerId"`
	ProjectRoot string   `json:"projectRoot"`
	TestIDs     []string `json:"testIds"`
	Debug       bool     `json:"debug"`
}

// PreparedTestRun is a prepared debug launch of a test run.
type PreparedTestRun struct {
	PreparedDebugLaunch
	RunID string `json:"runId"`
}

type testRunStopRequest struct {
	RunID string `json:"runId"`
}

// TestRunStopResult reports whether a live run was found.
type TestRunStopResult struct {
	Stopped bool `json:"stopped"`
}

func newTestRunID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", processLaunchError(processLaunchErrUnavailable, "create run id: %v", err)
	}
	return "run-" + hex.EncodeToString(buf[:]), nil
}

// normalizeTestRunIDs validates the requested test ids (an empty list runs
// all tests).
func normalizeTestRunIDs(ids []string) ([]string, error) {
	if ids == nil {
		ids = []string{}
	}
	if len(ids) > testProviderRunTestsMax {
		return nil, newContributionError(contributionErrInvalidRequest, "at most %d tests can run at once", testProviderRunTestsMax)
	}
	for index, id := range ids {
		if !validateContributionItemID(id, testProviderTestIDMaxLength) || slices.Contains(ids[:index], id) {
			return nil, newContributionError(contributionErrInvalidRequest, "testIds[%d] is invalid or repeated", index)
		}
	}
	return ids, nil
}

// testRunProvider returns the provider with its debugRun.
func (manager *pluginManager) testRunProvider(pluginID, providerID string) (TestProviderContribution, error) {
	if _, _, ok := enabledContributionPoint(testProviderPointKey); !ok {
		return TestProviderContribution{}, newContributionError(contributionErrUnavailable, "test providers are not enabled in this host")
	}
	record, err := manager.record(pluginID)
	if err != nil {
		return TestProviderContribution{}, newContributionError(contributionErrNotFound, "plugin %q not found", pluginID)
	}
	record.mu.Lock()
	provider, err := testProviderLocked(record, providerID)
	record.mu.Unlock()
	if err != nil {
		return TestProviderContribution{}, err
	}
	if provider.DebugRun == nil {
		return TestProviderContribution{}, newContributionError(contributionErrUnavailable, "test provider %q has no debugRun", providerID)
	}
	return provider, nil
}

// prepareTestRun registers a run and prepares its debug launch.
func (manager *pluginManager) prepareTestRun(ctx context.Context, request testRunPrepareRequest) (PreparedTestRun, error) {
	if _, err := normalizeTestProviderRequest(request.PluginID, request.ProviderID, ""); err != nil {
		return PreparedTestRun{}, err
	}
	testIDs, err := normalizeTestRunIDs(request.TestIDs)
	if err != nil {
		return PreparedTestRun{}, err
	}
	provider, err := manager.testRunProvider(request.PluginID, request.ProviderID)
	if err != nil {
		return PreparedTestRun{}, err
	}
	src, debugger, root, err := manager.debuggerSource(request.PluginID, provider.DebugRun.DebuggerType, request.ProjectRoot)
	if err != nil {
		return PreparedTestRun{}, err
	}
	run, err := manager.registerTestRun(request.PluginID, provider.ID, root)
	if err != nil {
		return PreparedTestRun{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"provider": provider.ID,
		"project":  map[string]string{"root": root},
		"testIds":  testIDs,
		"debug":    request.Debug,
		"runId":    run.id,
	})
	if err != nil {
		manager.closeTestRun(run.id, testRunEndFailed, true)
		return PreparedTestRun{}, newContributionError(contributionErrInvalidRequest, "encode payload: %v", err)
	}
	prepared, err := manager.prepareDebuggerLaunch(src, debugger, root, request.ProjectRoot, run.id, func() (debuggerResolveResult, error) {
		raw, err := debuggerInvoke(manager, ctx, contributedActionCall{
			PluginID: src.pluginID, Point: testProviderPointKey, EntryID: provider.ID, ActionID: provider.DebugRun.ResolveRunAction,
			RequestID: debuggerRequestID(), Payload: payload, Timeout: testRunResolveTimeout, MaxResultBytes: testRunResolveMaxBytes,
			Concurrency: debuggerActionConcurrency,
			Validate:    testProviderUnchanged(provider),
		})
		if err != nil {
			return debuggerResolveResult{}, err
		}
		result, err := parseDebuggerResolveResult(raw)
		if err != nil {
			return debuggerResolveResult{}, err
		}
		if !request.Debug {
			if result.Arguments, err = withNoDebugArgument(result.Arguments); err != nil {
				return debuggerResolveResult{}, newContributionError(contributionErrInvalidResult, "resolve result arguments: %v", err)
			}
		}
		return result, nil
	})
	if err != nil {
		manager.closeTestRun(run.id, testRunEndFailed, true)
		return PreparedTestRun{}, err
	}
	manager.supersedeOlderTestRuns(run)
	return PreparedTestRun{PreparedDebugLaunch: prepared, RunID: run.id}, nil
}

// withNoDebugArgument sets "noDebug": true in a DAP launch arguments object.
func withNoDebugArgument(arguments json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &object); err != nil || object == nil {
		return nil, fmt.Errorf("must be a JSON object")
	}
	object["noDebug"] = json.RawMessage("true")
	return json.Marshal(object)
}

// registerTestRun enforces runsPerPlugin and registers a new preparing run.
// Live runs of the same plugin, provider and project do not count: the new
// run supersedes them once it is prepared (supersedeOlderTestRuns).
func (manager *pluginManager) registerTestRun(pluginID, providerID, root string) (*testRun, error) {
	id, err := newTestRunID()
	if err != nil {
		return nil, err
	}
	testRuns.mu.Lock()
	live := 0
	for _, run := range testRuns.runs {
		if run.pluginID != pluginID || run.state == testRunClosed || run.providerID == providerID && run.root == root {
			continue
		}
		live++
	}
	if live >= testRunsPerPlugin {
		testRuns.mu.Unlock()
		return nil, processLaunchError(processLaunchErrLimit, "plugin %q already has %d test runs", pluginID, live)
	}
	testRuns.seq++
	run := &testRun{id: id, pluginID: pluginID, providerID: providerID, root: root, seq: testRuns.seq, ids: map[string]struct{}{}}
	run.timer = time.AfterFunc(testRunPrepareTTL, func() { manager.expireTestRun(id) })
	testRuns.runs[id] = run
	testRuns.mu.Unlock()
	return run, nil
}

// supersedeOlderTestRuns stops the live runs of run's plugin, provider and
// project registered before it (reason "superseded").
func (manager *pluginManager) supersedeOlderTestRuns(run *testRun) {
	superseded := []string{}
	testRuns.mu.Lock()
	for _, other := range testRuns.runs {
		if other.seq < run.seq && other.state != testRunClosed && other.pluginID == run.pluginID && other.providerID == run.providerID && other.root == run.root {
			superseded = append(superseded, other.id)
		}
	}
	testRuns.mu.Unlock()
	for _, old := range superseded {
		manager.stopTestRun(old, testRunEndSuperseded)
	}
}

// expireTestRun closes a run whose session never started.
func (manager *pluginManager) expireTestRun(runID string) {
	testRuns.mu.Lock()
	run := testRuns.runs[runID]
	preparing := run != nil && run.state == testRunPreparing
	testRuns.mu.Unlock()
	if preparing {
		manager.closeTestRun(runID, testRunEndExpired, true)
	}
}

// testRunAwaitingSession reports a preparing run of pluginID.
func testRunAwaitingSession(runID, pluginID string) bool {
	testRuns.mu.Lock()
	defer testRuns.mu.Unlock()
	run := testRuns.runs[runID]
	return run != nil && run.pluginID == pluginID && run.state == testRunPreparing
}

// bindTestRunSession binds a started session to its preparing run and
// watches the session's end. It reports false when the run has ended.
func (manager *pluginManager) bindTestRunSession(runID, pluginID string, session *debug.Session) bool {
	testRuns.mu.Lock()
	run := testRuns.runs[runID]
	if run == nil || run.pluginID != pluginID || run.state != testRunPreparing {
		testRuns.mu.Unlock()
		return false
	}
	run.state, run.session, run.sessionID = testRunRunning, session, session.ID()
	if run.timer != nil {
		run.timer.Stop()
		run.timer = nil
	}
	testRuns.mu.Unlock()
	go func() {
		<-session.Done()
		manager.testRunSessionEnded(runID)
	}()
	return true
}

// testRunSessionEnded tells the plugin and starts the grace period; a run
// already closed (finished) is forgotten after telling the plugin.
func (manager *pluginManager) testRunSessionEnded(runID string) {
	testRuns.mu.Lock()
	run := testRuns.runs[runID]
	if run == nil {
		testRuns.mu.Unlock()
		return
	}
	run.sessionEnded = true
	notify := !run.runEndedSent
	run.runEndedSent = true
	pluginID := run.pluginID
	switch run.state {
	case testRunClosed:
		delete(testRuns.runs, runID)
	case testRunPreparing, testRunRunning:
		run.state = testRunEnding
		if run.timer != nil {
			run.timer.Stop()
		}
		reason, grace := testRunEndSessionEnded, testRunGrace
		if run.stopReason != "" {
			// The host stopped the session (stop, superseded): wait less.
			reason, grace = run.stopReason, min(testRunGrace, testRunStoppedGrace)
		}
		run.timer = time.AfterFunc(grace, func() { manager.closeTestRun(runID, reason, false) })
	}
	testRuns.mu.Unlock()
	if notify {
		testRunNotifyEnded(manager, pluginID, runID, testRunEndSessionEnded)
	}
}

// closeTestRun closes a live run: the frontend gets {type: "ended"}, the
// plugin gets tests.runEnded (notify) unless it was told already, and the run
// is forgotten unless it waits for its session to end.
func (manager *pluginManager) closeTestRun(runID, reason string, notify bool) {
	testRuns.mu.Lock()
	run := testRuns.runs[runID]
	if run == nil || run.state == testRunClosed {
		testRuns.mu.Unlock()
		return
	}
	manager.closeTestRunLocked(run, reason, notify, nil)
}

// closeTestRunLocked is closeTestRun with testRuns.mu held; it unlocks.
// results (the publish that finished the run) are emitted before "ended".
func (manager *pluginManager) closeTestRunLocked(run *testRun, reason string, notify bool, results []TestRunEvent) {
	run.state = testRunClosed
	if run.timer != nil {
		run.timer.Stop()
		run.timer = nil
	}
	// A session still running keeps the run until it ends (tests.runEnded
	// then); otherwise the plugin is told now.
	waitForSession := run.session != nil && !run.sessionEnded && !run.runEndedSent
	sendEnded := notify && !waitForSession && !run.runEndedSent
	if sendEnded {
		run.runEndedSent = true
	}
	if !waitForSession {
		delete(testRuns.runs, run.id)
	}
	finished := reason == testRunEndFinished
	runID, pluginID := run.id, run.pluginID
	// Taken before testRuns.mu is released: a publish that passed its state
	// check emits first (it holds emitMu); later ones see the run closed.
	run.emitMu.Lock()
	testRuns.mu.Unlock()
	if len(results) > 0 {
		testRunEmit(TestRunEventPayload{RunID: runID, PluginID: pluginID, Events: results})
	}
	testRunEmit(TestRunEventPayload{RunID: runID, PluginID: pluginID, Events: []TestRunEvent{{Type: "ended", Finished: &finished, Reason: reason}}})
	run.emitMu.Unlock()
	if sendEnded {
		testRunNotifyEnded(manager, pluginID, runID, reason)
	}
}

// stopTestRun stops a live run: a bound session is stopped (the run then
// ends like any session end, with its grace period); a preparing run is
// closed at once. Unknown and closed runs report false.
func (manager *pluginManager) stopTestRun(runID, reason string) bool {
	testRuns.mu.Lock()
	run := testRuns.runs[runID]
	if run == nil || run.state == testRunClosed {
		testRuns.mu.Unlock()
		return false
	}
	session := run.session
	switch {
	case session == nil:
		manager.closeTestRunLocked(run, reason, true, nil)
	case run.state == testRunEnding:
		// The session already ended (the plugin was told): stop waiting for
		// late results.
		manager.closeTestRunLocked(run, reason, false, nil)
	default:
		run.stopReason = reason
		testRuns.mu.Unlock()
		go debuggerStopSession(context.Background(), session, "test run "+reason)
	}
	return true
}

// closePluginTestRuns closes every run of a stopped plugin; its process is
// gone, so it is not told.
func (manager *pluginManager) closePluginTestRuns(pluginID string) {
	testRuns.mu.Lock()
	ids := []string{}
	for id, run := range testRuns.runs {
		if run.pluginID == pluginID {
			ids = append(ids, id)
		}
	}
	testRuns.mu.Unlock()
	for _, id := range ids {
		testRuns.mu.Lock()
		run := testRuns.runs[id]
		if run == nil {
			testRuns.mu.Unlock()
			continue
		}
		if run.state == testRunClosed {
			delete(testRuns.runs, id)
			testRuns.mu.Unlock()
			continue
		}
		run.runEndedSent = true
		manager.closeTestRunLocked(run, testRunEndPluginStopped, false, nil)
		testRuns.mu.Lock()
		delete(testRuns.runs, id)
		testRuns.mu.Unlock()
	}
}

// testRunsLive reports a live (not closed) run of pluginID: in-flight work
// for idle stop.
func testRunsLive(pluginID string) bool {
	testRuns.mu.Lock()
	defer testRuns.mu.Unlock()
	for _, run := range testRuns.runs {
		if run.pluginID == pluginID && run.state != testRunClosed {
			return true
		}
	}
	return false
}

// notifyTestRunEnded sends plugin.event tests.runEnded to the run's plugin
// when it is running (never activating it), without waiting.
func (manager *pluginManager) notifyTestRunEnded(pluginID, runID, reason string) {
	record, err := manager.record(pluginID)
	if err != nil {
		return
	}
	record.mu.Lock()
	runtimeHost := record.runtime
	running := runtimeHost != nil && record.state == pluginStateRunning
	record.mu.Unlock()
	if !running {
		return
	}
	params, err := json.Marshal(map[string]any{"event": testRunEndedEvent, "data": map[string]string{"runId": runID, "reason": reason}})
	if err != nil {
		return
	}
	generation := runtimeHost.Generation()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hostEventTimeout)
		defer cancel()
		if _, err := runtimeHost.callGeneration(ctx, generation, hostEventMethod, params); err != nil {
			record.appendLog("warn", "event delivery failed ("+testRunEndedEvent+"): "+err.Error())
		}
	}()
}

// ---- host.tests.publish ----

type testRunPublishParams struct {
	RunID  *string           `json:"runId"`
	Events []json.RawMessage `json:"events"`
}

type testRunRawEvent struct {
	Type       string   `json:"type"`
	ID         *string  `json:"id"`
	ParentID   *string  `json:"parentId"`
	Label      *string  `json:"label"`
	Kind       *string  `json:"kind"`
	Path       *string  `json:"path"`
	Line       *int     `json:"line"`
	DurationMs *float64 `json:"durationMs"`
	Message    *string  `json:"message"`
	Expected   *string  `json:"expected"`
	Actual     *string  `json:"actual"`
	Trace      *string  `json:"trace"`
	Text       *string  `json:"text"`
}

// TestRunPublishResult counts the accepted and dropped events of one call.
type TestRunPublishResult struct {
	Accepted int `json:"accepted"`
	Dropped  int `json:"dropped"`
}

func init() {
	registerGenerationHostMethod(testRunPublishMethod, declaresTestDebugRun, api.CapabilityWorkspaceRead, hostTestsPublish)
	markHostMethodGateNotFound(testRunPublishMethod)
	registerRecordLifecycleHook(func(manager *pluginManager, record *pluginRecord) {
		manager.closePluginTestRuns(record.id)
	})
	core.RegisterCtx("plugin_host_test_run_prepare", pluginHostTestRunPrepareHandler)
	core.RegisterCtx("plugin_host_test_run_stop", pluginHostTestRunStopHandler)
	debug.OnStopRequested(markTestRunStopRequested)
}

// markTestRunStopRequested: the user stopped a session from the debug panel
// (debug_stop). A run bound to it then ends like a stop from the Tests tab
// (short grace, reason "stopped") instead of as a session that ended early.
func markTestRunStopRequested(sessionID string) {
	testRuns.mu.Lock()
	defer testRuns.mu.Unlock()
	for _, run := range testRuns.runs {
		if run.sessionID == sessionID && run.state == testRunRunning && run.stopReason == "" {
			run.stopReason = testRunEndStopped
			return
		}
	}
}

// declaresTestDebugRun is the publish gate: the runtime's manifest has a
// test provider with debugRun.
func declaresTestDebugRun(runtimeHost *processRuntime) bool {
	resolved := runtimeHost.manifest.contribution(testProviderPointKey)
	if resolved == nil {
		return false
	}
	for _, entry := range resolved.Entries {
		if provider, ok := entry.(TestProviderContribution); ok && provider.DebugRun != nil {
			return true
		}
	}
	return false
}

func testRunRPCError(code int, kind, message string) *rpcError {
	return &rpcError{Code: code, Message: message, Data: map[string]string{"code": kind}}
}

func hostTestsPublish(runtimeHost *processRuntime, generation uint64, raw json.RawMessage) (any, *rpcError) {
	// Cheap checks first: a stale generation and the rate limit refuse a call
	// before its params are decoded.
	runtimeHost.mu.Lock()
	current := generation == runtimeHost.generation
	runtimeHost.mu.Unlock()
	if !current {
		return nil, &rpcError{Code: -32001, Message: "plugin process generation changed"}
	}
	if !testRunPublishLimiter.allow(runtimeHost.pluginID) {
		return nil, testRunRPCError(-32029, contributionErrBusy, fmt.Sprintf("more than %d host.tests.publish calls per second", testRunPublishPerSecond))
	}
	if len(raw) > testRunPublishMaxBytes {
		return nil, testRunRPCError(-32602, contributionErrTooLarge, fmt.Sprintf("host.tests.publish params exceed %d bytes", testRunPublishMaxBytes))
	}
	var params testRunPublishParams
	if err := decodeStrictJSON(raw, &params, true); err != nil {
		return nil, testRunRPCError(-32602, contributionErrInvalidRequest, "invalid host.tests.publish params: "+err.Error())
	}
	if params.RunID == nil || *params.RunID == "" || params.Events == nil {
		return nil, testRunRPCError(-32602, contributionErrInvalidRequest, "runId and events are required")
	}
	if len(params.Events) > testRunPublishEventsMax {
		return nil, testRunRPCError(-32602, contributionErrTooLarge, fmt.Sprintf("at most %d events per call", testRunPublishEventsMax))
	}
	manager := runtimeHost.ownerManager
	if manager == nil {
		manager = host()
	}
	result, ok := manager.publishTestRunEvents(runtimeHost.pluginID, *params.RunID, params.Events)
	if !ok {
		return nil, testRunRPCError(-32001, contributionErrNotFound, "the test run is unknown, belongs to another plugin or has ended")
	}
	return result, nil
}

// publishTestRunEvents validates and forwards events of a live run owned by
// pluginID. It reports false for an unknown, foreign or closed run.
func (manager *pluginManager) publishTestRunEvents(pluginID, runID string, rawEvents []json.RawMessage) (TestRunPublishResult, bool) {
	testRuns.mu.Lock()
	run := testRuns.runs[runID]
	if run == nil || run.pluginID != pluginID || run.state == testRunClosed {
		testRuns.mu.Unlock()
		return TestRunPublishResult{}, false
	}
	result := TestRunPublishResult{}
	events := make([]TestRunEvent, 0, len(rawEvents))
	finished := false
	for _, rawEvent := range rawEvents {
		if finished {
			result.Dropped++
			continue
		}
		event, ok := parseTestRunEvent(rawEvent)
		if !ok {
			result.Dropped++
			continue
		}
		if !run.admitIDs(&event) {
			result.Dropped++
			if !run.truncated {
				run.truncated = true
				events = append(events, TestRunEvent{Type: "truncated"})
			}
			continue
		}
		events = append(events, event)
		result.Accepted++
		finished = event.Type == "finished"
	}
	if finished {
		// Close now (no other publish gets in); the results go out first,
		// then the ended event.
		manager.closeTestRunLocked(run, testRunEndFinished, true, events)
		return result, true
	}
	if len(events) == 0 {
		testRuns.mu.Unlock()
		return result, true
	}
	run.emitMu.Lock()
	testRuns.mu.Unlock()
	testRunEmit(TestRunEventPayload{RunID: runID, PluginID: pluginID, Events: events})
	run.emitMu.Unlock()
	return result, true
}

// admitIDs counts the event's new test ids against runNodesMax. An output
// event never takes budget: an id the run does not know yet is removed and
// its text kept, so run output never vanishes because of the node cap.
// Called with testRuns.mu held.
func (run *testRun) admitIDs(event *TestRunEvent) bool {
	if event.Type == "output" {
		if _, known := run.ids[event.ID]; !known {
			event.ID = ""
		}
		return true
	}
	fresh := []string{}
	for _, id := range []string{event.ID, event.ParentID} {
		if _, known := run.ids[id]; id != "" && !known && !slices.Contains(fresh, id) {
			fresh = append(fresh, id)
		}
	}
	if len(run.ids)+len(fresh) > testRunNodesMax {
		return false
	}
	for _, id := range fresh {
		run.ids[id] = struct{}{}
	}
	return true
}

// parseTestRunEvent validates one plugin event into its generic shape: the
// fields allowed by its type, ids and labels like discovered tests, messages
// cut to messageMaxRunes, traces, expected and actual values to
// traceMaxBytes, output text to outputTextMaxBytes.
func parseTestRunEvent(raw json.RawMessage) (TestRunEvent, bool) {
	var in testRunRawEvent
	if err := decodeStrictJSON(raw, &in, true); err != nil || !slices.Contains(testRunEventTypes, in.Type) {
		return TestRunEvent{}, false
	}
	out := TestRunEvent{Type: in.Type}
	present := map[string]bool{
		"id": in.ID != nil, "parentId": in.ParentID != nil, "label": in.Label != nil, "kind": in.Kind != nil,
		"path": in.Path != nil, "line": in.Line != nil, "durationMs": in.DurationMs != nil, "message": in.Message != nil,
		"expected": in.Expected != nil, "actual": in.Actual != nil, "trace": in.Trace != nil, "text": in.Text != nil,
	}
	var required, optional []string
	switch in.Type {
	case "tree":
		required, optional = []string{"id", "label", "kind"}, []string{"parentId", "path", "line"}
	case "started":
		required = []string{"id"}
	case "passed", "failed", "errored", "skipped":
		required, optional = []string{"id"}, []string{"durationMs", "message", "expected", "actual", "trace"}
	case "output":
		required, optional = []string{"text"}, []string{"id"}
	case "finished":
		optional = []string{"durationMs"}
	}
	for field, set := range present {
		if set && !slices.Contains(required, field) && !slices.Contains(optional, field) {
			return TestRunEvent{}, false
		}
	}
	for _, field := range required {
		if !present[field] {
			return TestRunEvent{}, false
		}
	}
	if in.ID != nil {
		if !validateContributionItemID(*in.ID, testProviderTestIDMaxLength) {
			return TestRunEvent{}, false
		}
		out.ID = *in.ID
	}
	if in.ParentID != nil {
		if !validateContributionItemID(*in.ParentID, testProviderTestIDMaxLength) || *in.ParentID == out.ID {
			return TestRunEvent{}, false
		}
		out.ParentID = *in.ParentID
	}
	if in.Label != nil {
		label, err := validateContributionLine(*in.Label, testProviderLabelMaxRunes, true)
		if err != nil {
			return TestRunEvent{}, false
		}
		out.Label = label
	}
	if in.Kind != nil {
		if !slices.Contains(testRunTreeKinds, *in.Kind) {
			return TestRunEvent{}, false
		}
		out.Kind = *in.Kind
	}
	if in.Path != nil {
		path, err := validateContributionRelativePath(*in.Path, testProviderPathMaxLength)
		if err != nil {
			return TestRunEvent{}, false
		}
		out.Path = path
	}
	if in.Line != nil {
		if *in.Line < 1 || *in.Line > testProviderLineMax || out.Path == "" {
			return TestRunEvent{}, false
		}
		out.Line = *in.Line
	}
	if in.DurationMs != nil {
		duration := *in.DurationMs
		if math.IsNaN(duration) || duration < 0 || duration > testProviderDurationMsMax {
			return TestRunEvent{}, false
		}
		rounded := int64(math.Round(duration))
		out.DurationMs = &rounded
	}
	if in.Message != nil {
		out.Message, _ = sanitizeContributionText(*in.Message, testProviderMessageMaxRunes)
	}
	if in.Expected != nil {
		value := sanitizeTestRunText(*in.Expected, testRunTraceMaxBytes)
		out.Expected = &value
	}
	if in.Actual != nil {
		value := sanitizeTestRunText(*in.Actual, testRunTraceMaxBytes)
		out.Actual = &value
	}
	if in.Trace != nil {
		out.Trace = sanitizeTestRunText(*in.Trace, testRunTraceMaxBytes)
	}
	if in.Text != nil {
		out.Text = sanitizeTestRunText(*in.Text, testRunOutputMaxBytes)
		if out.Text == "" {
			return TestRunEvent{}, false
		}
	}
	return out, true
}

// sanitizeTestRunText is sanitizeContributionText cut to maxBytes at a
// character boundary.
func sanitizeTestRunText(value string, maxBytes int) string {
	text, _ := sanitizeContributionText(value, maxBytes)
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.ToValidUTF8(text[:cut], "")
}

// ---- frontend commands ----

func pluginHostTestRunPrepareHandler(ctx context.Context, _, args string) (string, error) {
	var request testRunPrepareRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	prepared, err := host().prepareTestRun(ctx, request)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(prepared)
}

func pluginHostTestRunStopHandler(_ context.Context, _, args string) (string, error) {
	var request testRunStopRequest
	if err := core.DecodeArgsPayload(args, &request); err != nil {
		return "", err
	}
	if request.RunID == "" {
		return "", newContributionError(contributionErrInvalidRequest, "runId is required")
	}
	return core.EncodeResult(TestRunStopResult{Stopped: host().stopTestRun(request.RunID, testRunEndStopped)})
}
