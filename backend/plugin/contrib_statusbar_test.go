package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

const statusBarHelperPluginID = "statusbar.helper"

// TestStatusBarHelperProcess is the fake plugin used by the process test: the
// "update" / "reset" actions forward their payload to host.statusBar.* and
// return the host response; "crash" exits without a shutdown handshake.
func TestStatusBarHelperProcess(t *testing.T) {
	if os.Getenv("MINCODE_PLUGIN_ID") != statusBarHelperPluginID {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	encoder := json.NewEncoder(os.Stdout)
	hostCall := 0
	for scanner.Scan() {
		var request rpcEnvelope
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Method == "" {
			continue
		}
		response := rpcEnvelope{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case "plugin.initialize", "plugin.ping":
			response.Result = json.RawMessage(`true`)
		case "plugin.shutdown":
			response.Result = json.RawMessage(`true`)
			_ = encoder.Encode(response)
			os.Exit(0)
		case "plugin.invoke":
			var params struct {
				Action  string `json:"action"`
				Payload string `json:"payload"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.Action == "crash" {
				os.Exit(3)
			}
			hostCall++
			id := json.RawMessage(fmt.Sprintf(`"host-%d"`, hostCall))
			_ = encoder.Encode(rpcEnvelope{JSONRPC: "2.0", ID: id, Method: "host.statusBar." + params.Action, Params: json.RawMessage(params.Payload)})
			var hostResponse rpcEnvelope
			for scanner.Scan() {
				if json.Unmarshal(scanner.Bytes(), &hostResponse) == nil && string(hostResponse.ID) == string(id) && hostResponse.Method == "" {
					break
				}
			}
			result, _ := json.Marshal(map[string]any{"result": map[string]any{"result": hostResponse.Result, "error": hostResponse.Error}})
			response.Result = result
		default:
			response.Error = &rpcError{Code: -32601, Message: "unknown"}
		}
		_ = encoder.Encode(response)
	}
	os.Exit(0)
}

// statusBarNotifications records statusBarNotify calls for one test.
type statusBarNotifications struct {
	count atomic.Int64
}

func captureStatusBarNotify(t *testing.T, interval time.Duration) *statusBarNotifications {
	t.Helper()
	recorder := &statusBarNotifications{}
	previousNotify, previousInterval, previousNow := statusBarNotify, statusBarFlushInterval, statusBarNow
	statusBarNotify = func(string) { recorder.count.Add(1) }
	statusBarFlushInterval = interval
	t.Cleanup(func() {
		statusBarNotify, statusBarFlushInterval, statusBarNow = previousNotify, previousInterval, previousNow
		statusBarMu.Lock()
		for runtimeHost, state := range statusBarStates {
			stopStatusBarTimer(state)
			delete(statusBarStates, runtimeHost)
		}
		statusBarMu.Unlock()
	})
	return recorder
}

func statusBarManifestRoot(t *testing.T, items string, mutate func(*Manifest)) string {
	t.Helper()
	return writeTestManifest(t, func(manifest *Manifest) {
		manifest.APIVersion = api.ManifestAPIVersionV2
		manifest.Capabilities = []api.Capability{api.CapabilityCommands}
		manifest.Contributes.Points = map[string]json.RawMessage{statusBarPointKey: json.RawMessage(items)}
		if mutate != nil {
			mutate(manifest)
		}
	})
}

func statusBarTestManifest(t *testing.T, items string, mutate func(*Manifest)) Manifest {
	t.Helper()
	manifest, err := readManifest(statusBarManifestRoot(t, items, mutate))
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.contributionIssuesError(); err != nil {
		t.Fatal(err)
	}
	return manifest
}

const statusBarDefaultItems = `[
	{"id":"sync","text":"Synced","tooltip":"Last sync","icon":"connection","command":"example.run","alignment":"right","priority":10,"when":"example.ready"},
	{"id":"progress","dynamic":true},
	{"id":"branch","text":"main","dynamic":true}
]`

func TestStatusBarDecodeAndBounds(t *testing.T) {
	manifest := statusBarTestManifest(t, statusBarDefaultItems, nil)
	items := manifest.statusBarItems()
	if len(items) != 3 || items[0].Alignment != "right" || items[1].Alignment != "left" || items[1].Text != "" || !items[2].Dynamic {
		t.Fatalf("items = %+v", items)
	}
	encoded, _ := json.Marshal(items[1])
	if string(encoded) != `{"id":"progress","alignment":"left","priority":0,"dynamic":true}` {
		t.Fatalf("encoded = %s", encoded)
	}

	long := strings.Repeat("字", statusBarMaxTextRunes+1)
	cases := map[string]string{
		"unknown field":      `[{"id":"a","text":"x","color":"red"}]`,
		"bad id":             `[{"id":"A","text":"x"}]`,
		"missing text":       `[{"id":"a"}]`,
		"blank text":         `[{"id":"a","text":"  "}]`,
		"text too long":      `[{"id":"a","text":"` + long + `"}]`,
		"newline":            `[{"id":"a","text":"a\nb"}]`,
		"control":            `[{"id":"a","text":"a\u0007"}]`,
		"bidi override":      `[{"id":"a","text":"a\u202eb"}]`,
		"bidi isolate":       `[{"id":"a","text":"a\u2066b"}]`,
		"tooltip too long":   `[{"id":"a","text":"x","tooltip":"` + strings.Repeat("t", statusBarMaxTooltipRunes+1) + `"}]`,
		"tooltip bidi":       `[{"id":"a","text":"x","tooltip":"\u202e"}]`,
		"localized too long": `[{"id":"a","text":"x","textLocalized":{"zh":"` + long + `"}}]`,
		"bad icon":           `[{"id":"a","text":"x","icon":"rocket"}]`,
		"unknown command":    `[{"id":"a","text":"x","command":"other.run"}]`,
		"bad alignment":      `[{"id":"a","text":"x","alignment":"center"}]`,
		"priority low":       `[{"id":"a","text":"x","priority":-1001}]`,
		"priority high":      `[{"id":"a","text":"x","priority":1001}]`,
		"bad when":           `[{"id":"a","text":"x","when":"a ||"}]`,
		"duplicate":          `[{"id":"a","text":"x"},{"id":"a","text":"y"}]`,
	}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			manifest, err := readManifest(statusBarManifestRoot(t, items, nil))
			if err == nil {
				err = manifest.contributionIssuesError()
			}
			if err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// Tooltips may be multi-line; localized tooltips follow the same rules.
	statusBarTestManifest(t, `[{"id":"a","text":"x","tooltip":"line 1\nline 2","tooltipLocalized":{"zh":"第一行\n第二行"}}]`, nil)

	tooMany := make([]string, 0, 9)
	for index := 0; index < 9; index++ {
		tooMany = append(tooMany, fmt.Sprintf(`{"id":"i%d","text":"x"}`, index))
	}
	if _, err := readManifest(statusBarManifestRoot(t, "["+strings.Join(tooMany, ",")+"]", nil)); err == nil {
		t.Fatal("9 items accepted")
	}
	if _, err := readManifest(statusBarManifestRoot(t, `[{"id":"a","text":"x"}]`, func(manifest *Manifest) {
		manifest.APIVersion = api.APIVersion
	})); err == nil {
		t.Fatal("manifest apiVersion 1 accepted contributes.statusBar")
	}
}

func TestStatusBarHostMethodGating(t *testing.T) {
	captureStatusBarNotify(t, time.Hour)
	manifest := statusBarTestManifest(t, statusBarDefaultItems, nil)
	var result any
	var rpcErr *rpcError
	respond := func(value any, err *rpcError) { result, rpcErr = value, err }
	call := func(runtimeHost *processRuntime, method, params string) {
		result, rpcErr = nil, nil
		if !runtimeHost.serveRegisteredHostMethod(rpcEnvelope{Method: method, Params: json.RawMessage(params)}, respond) {
			t.Fatalf("%s not registered", method)
		}
	}

	withoutCapability := manifest
	withoutCapability.Capabilities = []api.Capability{api.CapabilityStorage}
	call(newProcessRuntime(t.TempDir(), withoutCapability, nil, nil), statusBarUpdateMethod, `{"id":"progress","text":"1%"}`)
	if rpcErr == nil || rpcErr.Code != -32001 || !strings.Contains(rpcErr.Message, "commands") {
		t.Fatalf("missing capability: %+v", rpcErr)
	}
	static := statusBarTestManifest(t, `[{"id":"a","text":"x"}]`, nil)
	call(newProcessRuntime(t.TempDir(), static, nil, nil), statusBarUpdateMethod, `{"id":"a","text":"y"}`)
	if rpcErr == nil || rpcErr.Code != -32001 {
		t.Fatalf("static-only plugin admitted: %+v", rpcErr)
	}
	runtimeHost := newProcessRuntime(t.TempDir(), manifest, nil, nil)
	if !statusBarGate(runtimeHost) || !strings.Contains(fmt.Sprint(runtimeHost.describeAPI()), statusBarUpdateMethod) {
		t.Fatalf("describe = %+v", runtimeHost.describeAPI())
	}
	for name, params := range map[string]string{
		"static item":   `{"id":"sync","text":"y"}`,
		"unknown item":  `{"id":"nope","text":"y"}`,
		"unknown field": `{"id":"progress","color":"red"}`,
		"long text":     `{"id":"progress","text":"` + strings.Repeat("x", statusBarMaxTextRunes+1) + `"}`,
		"newline":       `{"id":"progress","text":"a\nb"}`,
		"bidi":          `{"id":"progress","text":"\u202e"}`,
		"blank":         `{"id":"progress","text":""}`,
		"severity":      `{"id":"progress","severity":"fatal"}`,
		"not an object": `[]`,
	} {
		call(runtimeHost, statusBarUpdateMethod, params)
		if rpcErr == nil || rpcErr.Code != -32602 {
			t.Fatalf("%s: %+v", name, rpcErr)
		}
	}
	call(runtimeHost, statusBarUpdateMethod, `{"id":"progress","text":"42%","tooltip":"Indexing","severity":"warning","busy":true,"visible":true}`)
	if rpcErr != nil || result != true {
		t.Fatalf("update: %v %+v", result, rpcErr)
	}
	call(runtimeHost, statusBarResetMethod, `{"id":"sync"}`)
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("reset static item: %+v", rpcErr)
	}
	for _, params := range []string{``, `{}`, `{"id":"progress"}`} {
		call(runtimeHost, statusBarResetMethod, params)
		if rpcErr != nil {
			t.Fatalf("reset %q: %+v", params, rpcErr)
		}
	}
	runtimeHost.mu.Lock()
	runtimeHost.stopping = true
	runtimeHost.mu.Unlock()
	call(runtimeHost, statusBarUpdateMethod, `{"id":"progress","text":"1"}`)
	if rpcErr == nil || rpcErr.Code != -32001 {
		t.Fatalf("stopping runtime accepted: %+v", rpcErr)
	}
}

func statusBarRecord(manifest Manifest, runtimeHost *processRuntime) *pluginRecord {
	return &pluginRecord{
		id: manifest.ID, enabled: true, state: pluginStateRunning, runtime: runtimeHost,
		manifest: &manifest, source: pluginSourceLocal,
	}
}

func statusBarOverlay(t *testing.T, record *pluginRecord) map[string]StatusBarRuntimeItem {
	t.Helper()
	record.mu.Lock()
	defer record.mu.Unlock()
	overlay := statusBarRuntimeOverlay(record)
	if overlay == nil {
		return nil
	}
	return overlay.(StatusBarRuntime).Items
}

func TestStatusBarCoalescingRateLimitAndOverlay(t *testing.T) {
	notifications := captureStatusBarNotify(t, 40*time.Millisecond)
	manifest := statusBarTestManifest(t, statusBarDefaultItems, nil)
	runtimeHost := newProcessRuntime(t.TempDir(), manifest, nil, nil)
	record := statusBarRecord(manifest, runtimeHost)
	update := func(params string) {
		t.Helper()
		if _, rpcErr := handleStatusBarUpdate(runtimeHost, json.RawMessage(params)); rpcErr != nil {
			t.Fatal(rpcErr)
		}
	}

	// Latest wins; one immediate push plus one trailing push per interval.
	for index := 1; index <= 20; index++ {
		update(fmt.Sprintf(`{"id":"progress","text":"%d%%"}`, index))
	}
	if got := notifications.count.Load(); got != 1 {
		t.Fatalf("immediate pushes = %d", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for notifications.count.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(80 * time.Millisecond)
	if got := notifications.count.Load(); got != 2 {
		t.Fatalf("coalesced pushes = %d", got)
	}
	items := statusBarOverlay(t, record)
	if items["progress"].Text == nil || *items["progress"].Text != "20%" {
		t.Fatalf("overlay = %+v", items)
	}

	// Rate limit: 50 updates per window; the rest are silently dropped.
	now := time.Now().Add(time.Hour)
	statusBarNow = func() time.Time { return now }
	for index := 1; index <= statusBarRateLimit+10; index++ {
		if result, rpcErr := handleStatusBarUpdate(runtimeHost, json.RawMessage(fmt.Sprintf(`{"id":"branch","text":"b%d"}`, index))); rpcErr != nil || result != true {
			t.Fatalf("update %d: %v %+v", index, result, rpcErr)
		}
	}
	if text := statusBarOverlay(t, record)["branch"].Text; text == nil || *text != fmt.Sprintf("b%d", statusBarRateLimit) {
		t.Fatalf("rate-limited text = %v", text)
	}
	now = now.Add(statusBarRateWindow)
	update(`{"id":"branch","text":"fresh","visible":false,"busy":true,"severity":"error"}`)
	branch := statusBarOverlay(t, record)["branch"]
	if *branch.Text != "fresh" || *branch.Visible || !*branch.Busy || branch.Severity != "error" {
		t.Fatalf("branch = %+v", branch)
	}

	// The overlay follows the record's running runtime and generation only.
	record.state = pluginStateStopped
	if items := statusBarOverlay(t, record); items != nil {
		t.Fatalf("stopped overlay = %+v", items)
	}
	record.state = pluginStateRunning
	record.runtime = newProcessRuntime(t.TempDir(), manifest, nil, nil)
	if items := statusBarOverlay(t, record); items != nil {
		t.Fatalf("other runtime overlay = %+v", items)
	}
	record.runtime = runtimeHost
	runtimeHost.mu.Lock()
	runtimeHost.generation++
	runtimeHost.mu.Unlock()
	if items := statusBarOverlay(t, record); items != nil {
		t.Fatalf("stale generation overlay = %+v", items)
	}
	runtimeHost.mu.Lock()
	runtimeHost.generation--
	runtimeHost.mu.Unlock()

	// Reset one item, then all.
	if _, rpcErr := handleStatusBarReset(runtimeHost, json.RawMessage(`{"id":"branch"}`)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if items := statusBarOverlay(t, record); len(items) != 1 || items["progress"].Text == nil {
		t.Fatalf("after reset one = %+v", items)
	}
	if _, rpcErr := handleStatusBarReset(runtimeHost, nil); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if items := statusBarOverlay(t, record); items != nil {
		t.Fatalf("after reset all = %+v", items)
	}
}

func TestStatusBarLifecycleClearsRuntimeState(t *testing.T) {
	notifications := captureStatusBarNotify(t, time.Hour)
	manifest := statusBarTestManifest(t, statusBarDefaultItems, nil)
	runtimeHost := newProcessRuntime(t.TempDir(), manifest, nil, nil)
	record := statusBarRecord(manifest, runtimeHost)
	manager := &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}}
	if _, rpcErr := handleStatusBarUpdate(runtimeHost, json.RawMessage(`{"id":"progress","text":"busy"}`)); rpcErr != nil {
		t.Fatal(rpcErr)
	}

	snapshot := manager.Contributions([]string{statusBarPointKey})
	if len(snapshot.Plugins) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	point := snapshot.Plugins[0].Points[statusBarPointKey]
	if len(point.Entries) != 3 || point.Runtime == nil {
		t.Fatalf("point = %+v", point)
	}
	encoded, _ := json.Marshal(point.Runtime)
	if string(encoded) != `{"items":{"progress":{"text":"busy"}}}` {
		t.Fatalf("runtime = %s", encoded)
	}

	// A running record keeps its current runtime's state (context-only clears
	// such as a no-op hook run must not wipe it).
	before := notifications.count.Load()
	manager.clearRecordContext(record)
	if statusBarOverlay(t, record) == nil || notifications.count.Load() != before {
		t.Fatal("running runtime state cleared")
	}
	// Stop (disable, reload and uninstall stop through the same path).
	record.state = pluginStateStopping
	manager.clearRecordContext(record)
	record.state = pluginStateRunning
	if statusBarOverlay(t, record) != nil || notifications.count.Load() != before+1 {
		t.Fatalf("stop did not clear: notifications=%d", notifications.count.Load())
	}
	statusBarMu.Lock()
	remaining := len(statusBarStates)
	statusBarMu.Unlock()
	if remaining != 0 {
		t.Fatalf("states left = %d", remaining)
	}

	// A development generation switch replaces the runtime: the new runtime's
	// first update drops the previous runtime's state, and the overlay never
	// shows the old one.
	if _, rpcErr := handleStatusBarUpdate(runtimeHost, json.RawMessage(`{"id":"progress","text":"old"}`)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	replacement := newProcessRuntime(t.TempDir(), manifest, nil, nil)
	record.runtime = replacement
	if statusBarOverlay(t, record) != nil {
		t.Fatal("old generation state visible")
	}
	if _, rpcErr := handleStatusBarUpdate(replacement, json.RawMessage(`{"id":"branch","text":"new"}`)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	statusBarMu.Lock()
	_, oldKept := statusBarStates[runtimeHost]
	statusBarMu.Unlock()
	if oldKept {
		t.Fatal("previous runtime state kept after replacement update")
	}
	manager.clearRecordContext(record)
	if items := statusBarOverlay(t, record); len(items) != 1 || *items["branch"].Text != "new" {
		t.Fatalf("replacement overlay = %+v", items)
	}

	// Pure-dynamic items keep only declared data in the snapshot of a disabled
	// plugin: the plugin is not listed at all.
	record.enabled = false
	if snapshot := manager.Contributions([]string{statusBarPointKey}); len(snapshot.Plugins) != 0 {
		t.Fatalf("disabled plugin listed: %+v", snapshot)
	}
}

func TestStatusBarProcessRuntimeUpdateAndCrash(t *testing.T) {
	notifications := captureStatusBarNotify(t, 10*time.Millisecond)
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	entry := "plugin-helper"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, entry), data, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := statusBarTestManifest(t, statusBarDefaultItems, nil)
	manifest.ID = statusBarHelperPluginID
	manifest.Runtime = ProcessRuntimeManifest{Type: "process", Entry: entry, Args: []string{"-test.run=TestStatusBarHelperProcess"}}
	manifest.Contributes.Actions = []ManifestUIAction{{ID: "update", UICallable: true}, {ID: "reset", UICallable: true}, {ID: "crash", UICallable: true}}

	record := statusBarRecord(manifest, nil)
	manager := &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}}
	runtimeHost := manager.newRecordRuntime(record, root, manifest)
	record.runtime = runtimeHost
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := runtimeHost.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtimeHost.forceStop(context.Background()) }()

	invoke := func(action, payload string) string {
		t.Helper()
		result, err := runtimeHost.Invoke(ctx, action, payload)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := invoke("update", `{"id":"progress","text":"3 files"}`); !strings.Contains(got, `"result":true`) {
		t.Fatalf("update over stdio = %s", got)
	}
	if got := invoke("update", `{"id":"sync","text":"x"}`); !strings.Contains(got, `-32602`) {
		t.Fatalf("static update over stdio = %s", got)
	}
	snapshot := manager.Contributions([]string{statusBarPointKey})
	encoded, _ := json.Marshal(snapshot.Plugins[0].Points[statusBarPointKey].Runtime)
	if string(encoded) != `{"items":{"progress":{"text":"3 files"}}}` {
		t.Fatalf("runtime = %s", encoded)
	}
	if notifications.count.Load() < 1 {
		t.Fatal("update not announced")
	}

	// A crash ends the runtime; the exit handler clears the dynamic state.
	var once sync.Once
	crashed := make(chan struct{})
	go func() {
		for {
			record.mu.Lock()
			state := record.state
			record.mu.Unlock()
			if state == pluginStateError {
				once.Do(func() { close(crashed) })
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	_, _ = runtimeHost.Invoke(ctx, "crash", "")
	select {
	case <-crashed:
	case <-ctx.Done():
		t.Fatal("crash not observed")
	}
	record.mu.Lock()
	record.state = pluginStateRunning
	record.mu.Unlock()
	if items := statusBarOverlay(t, record); items != nil {
		t.Fatalf("state survived crash: %+v", items)
	}
}
