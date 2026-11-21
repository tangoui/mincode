package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
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

const (
	debuggerTestPlugin     = "debug.fixture"
	debuggerFakeAdapterEnv = "DEBUGGER_FAKE_ADAPTER"
	debuggerFakeAdapterArg = "-test.run=^TestDebuggerFakeAdapterProcess$"
)

// TestDebuggerFakeAdapterProcess is not a test: started with
// DEBUGGER_FAKE_ADAPTER=stdio it is a debug adapter that answers every DAP
// request with success on stdin/stdout.
func TestDebuggerFakeAdapterProcess(t *testing.T) {
	if os.Getenv(debuggerFakeAdapterEnv) != "stdio" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	var seq int64
	send := func(message debug.Message) {
		seq++
		message.Seq = seq
		_ = debug.WriteMessage(os.Stdout, message)
	}
	ok := true
	for {
		request, err := debug.ReadMessage(reader, 4<<20)
		if err != nil {
			os.Exit(0)
		}
		if request.Type != "request" {
			continue
		}
		var body json.RawMessage
		if request.Command == "initialize" {
			body = json.RawMessage(`{"supportsConfigurationDoneRequest":true}`)
		}
		send(debug.Message{Type: "response", RequestSeq: request.Seq, Command: request.Command, Success: &ok, Body: body})
		switch request.Command {
		case "initialize":
			send(debug.Message{Type: "event", Event: "initialized"})
		case "disconnect":
			os.Exit(0)
		}
	}
}

func debuggerTestContext(t *testing.T) *pointContext {
	t.Helper()
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/adapter")
	return &pointContext{
		root:     root,
		manifest: &Manifest{ID: debuggerTestPlugin, APIVersion: 2, Capabilities: []api.Capability{api.CapabilityShell}},
		spec:     debuggerSpec,
		actions: map[string]ManifestUIAction{
			"debug.resolve":     {ID: "debug.resolve", Risk: "read"},
			"debug.entryPoints": {ID: "debug.entryPoints", Risk: "read"},
			"debug.check":       {ID: "debug.check", Risk: "read"},
			"debug.settings":    {ID: "debug.settings", Risk: "read", MaxPayloadBytes: 65536},
			"debug.smallRead":   {ID: "debug.smallRead", Risk: "read", MaxPayloadBytes: 16384},
			"debug.write":       {ID: "debug.write", Risk: "write"},
			"debug.ai":          {ID: "debug.ai", Risk: "read", AICallable: true},
		},
	}
}

func decodeDebuggerForTest(ctx *pointContext, raw string) (DebuggerContribution, error) {
	value, _, err := decodeDebuggerEntry(ctx, 0, "", json.RawMessage(raw))
	if err != nil {
		return DebuggerContribution{}, err
	}
	return value.(DebuggerContribution), nil
}

// debuggerEntryJSON returns a valid entry with adapter replaced.
func debuggerEntryJSON(adapter string, extra string) string {
	return `{"type":"python","title":"Python","languages":["python"],"adapter":` + adapter + `,"resolveAction":"debug.resolve"` + extra + `}`
}

func TestDebuggersDecodeValid(t *testing.T) {
	ctx := debuggerTestContext(t)
	debugger, err := decodeDebuggerForTest(ctx, `{
		"type":"python","title":" Python ","titleLocalized":{"zh":"Python 调试"},"languages":["python"],
		"adapter":{"kind":"stdio","command":"${resolved.python}","args":["-m","debugpy.adapter","--log=${projectRoot}/.log","${resolved.extra}"],"env":{"DEBUGPY_LOG":"${pluginRoot}/log"}},
		"resolveAction":"debug.resolve","entryPointsAction":"debug.entryPoints","checkAction":"debug.check",
		"configurationSchema":{ "launch" : {"type":"object"}, "attach":{"type":"object"} },
		"configurationSnippets":[{"label":"Current file","labelLocalized":{"zh":"当前文件"},"request":"launch","body":{"program":"${file}"}}],
		"remote":{"forward":"local","argsTemplate":"python -m debugpy --listen 0.0.0.0:${port} <script.py>"},
		"platforms":["linux","darwin"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if debugger.Title != "Python" || debugger.Adapter.Command != "${resolved.python}" || len(debugger.Adapter.Args) != 4 ||
		string(debugger.ConfigurationSchema) != `{"launch":{"type":"object"},"attach":{"type":"object"}}` ||
		len(debugger.ConfigurationSnippets) != 1 || debugger.Remote == nil || debugger.Remote.Forward != "local" ||
		strings.Join(debugger.Platforms, ",") != "darwin,linux" || len(debugger.SpecFingerprint) != 64 ||
		debugger.EntryPointsAction != "debug.entryPoints" || debugger.CheckAction != "debug.check" {
		t.Fatalf("debugger = %+v", debugger)
	}
	tcp, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(`{"kind":"tcp","command":"dlv","args":["dap","--listen=127.0.0.1:${port}"]}`, ""))
	if err != nil || tcp.Adapter.Kind != "tcp" || len(tcp.Platforms) != 3 || tcp.ConfigurationSnippets == nil {
		t.Fatalf("tcp = %+v, %v", tcp, err)
	}
	plugin, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(`{"kind":"stdio","command":"${pluginRoot}/bin/adapter"}`, ""))
	if err != nil || plugin.Adapter.Command != "${pluginRoot}/bin/adapter" {
		t.Fatalf("plugin adapter = %+v, %v", plugin, err)
	}
}

func TestDebuggersRejectActionNotRead(t *testing.T) {
	ctx := debuggerTestContext(t)
	adapter := `{"kind":"stdio","command":"debugpy-adapter"}`
	cases := map[string]string{
		`{"type":"python","title":"P","languages":["python"],"adapter":` + adapter + `,"resolveAction":"debug.write"}`:                                     `risk "read"`,
		`{"type":"python","title":"P","languages":["python"],"adapter":` + adapter + `,"resolveAction":"debug.ai"}`:                                        "aiCallable",
		`{"type":"python","title":"P","languages":["python"],"adapter":` + adapter + `,"resolveAction":"debug.missing"}`:                                   "contributes.actions",
		`{"type":"python","title":"P","languages":["python"],"adapter":` + adapter + `}`:                                                                   "resolveAction is required",
		`{"type":"python","title":"P","languages":["python"],"adapter":` + adapter + `,"resolveAction":"debug.resolve","entryPointsAction":"debug.write"}`: `risk "read"`,
		`{"type":"python","title":"P","languages":["python"],"adapter":` + adapter + `,"resolveAction":"debug.resolve","checkAction":"debug.ai"}`:          "aiCallable",
		`{"type":"python","title":"P","languages":["python"],"adapter":` + adapter + `,"resolveAction":"debug.resolve","checkAction":""}`:                  "contributes.actions",
	}
	for raw, want := range cases {
		if _, err := decodeDebuggerForTest(ctx, raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
}

func TestDebuggersRejectAbsoluteCommand(t *testing.T) {
	ctx := debuggerTestContext(t)
	for _, command := range []string{"/usr/bin/python3", "./python", "${projectRoot}/.venv/bin/python", "${pluginRoot}/bin/none", "${pluginRoot}/../x", "x${resolved.python}", "${resolved.Bad-Name}", "${resolved.}"} {
		if _, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(`{"kind":"stdio","command":"`+command+`"}`, "")); err == nil {
			t.Errorf("command %q: expected an error", command)
		}
	}
	_, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(`{"kind":"stdio","command":"/usr/bin/python3"}`, ""))
	if err == nil || !strings.Contains(err.Error(), "absolute paths are not allowed") {
		t.Fatalf("err = %v", err)
	}
}

func TestDebuggersRejectPortInStdio(t *testing.T) {
	ctx := debuggerTestContext(t)
	_, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(`{"kind":"stdio","command":"dlv","args":["--listen=127.0.0.1:${port}"]}`, ""))
	if err == nil || !strings.Contains(err.Error(), "${port}") {
		t.Fatalf("stdio ${port}: err = %v", err)
	}
	cases := map[string]string{
		"tcp without port": `{"kind":"tcp","command":"dlv","args":["dap"]}`,
		"port in env":      `{"kind":"tcp","command":"dlv","args":["--listen=127.0.0.1:${port}"],"env":{"DLV_PORT":"${port}"}}`,
		"resolved in env":  `{"kind":"stdio","command":"dlv","env":{"DLV":"${resolved.dlv}"}}`,
		"bad resolved arg": `{"kind":"stdio","command":"dlv","args":["${resolved.a-b}"]}`,
		"unknown variable": `{"kind":"stdio","command":"dlv","args":["${workspaceFolder}"]}`,
		"denied env":       `{"kind":"stdio","command":"dlv","env":{"PYTHONPATH":"x"}}`,
		"denied prefix":    `{"kind":"stdio","command":"dlv","env":{"DYLD_X":"x"}}`,
		"unknown field":    `{"kind":"stdio","command":"dlv","cwd":"${projectRoot}"}`,
		"no command":       `{"kind":"stdio"}`,
	}
	for name, adapter := range cases {
		if _, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, "")); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDebuggersRejectInvalidEntries(t *testing.T) {
	ctx := debuggerTestContext(t)
	adapter := `{"kind":"stdio","command":"dlv"}`
	cases := map[string]string{
		"bad type":          `{"type":"Python","title":"P","languages":["python"],"adapter":` + adapter + `,"resolveAction":"debug.resolve"}`,
		"no title":          `{"type":"python","title":" ","languages":["python"],"adapter":` + adapter + `,"resolveAction":"debug.resolve"}`,
		"no languages":      `{"type":"python","title":"P","languages":[],"adapter":` + adapter + `,"resolveAction":"debug.resolve"}`,
		"no adapter":        `{"type":"python","title":"P","languages":["python"],"resolveAction":"debug.resolve"}`,
		"schema member":     debuggerEntryJSON(adapter, `,"configurationSchema":{"run":{}}`),
		"schema not object": debuggerEntryJSON(adapter, `,"configurationSchema":{"launch":[]}`),
		"schema empty":      debuggerEntryJSON(adapter, `,"configurationSchema":{}`),
		"schema too large":  debuggerEntryJSON(adapter, `,"configurationSchema":{"launch":{"x":"`+strings.Repeat("a", 16400)+`"}}`),
		"snippet request":   debuggerEntryJSON(adapter, `,"configurationSnippets":[{"label":"A","request":"run","body":{}}]`),
		"snippet field":     debuggerEntryJSON(adapter, `,"configurationSnippets":[{"label":"A","request":"launch","body":{},"x":1}]`),
		"snippet too large": debuggerEntryJSON(adapter, `,"configurationSnippets":[{"label":"A","request":"launch","body":{"x":"`+strings.Repeat("a", 4100)+`"}}]`),
		"remote forward":    debuggerEntryJSON(adapter, `,"remote":{"forward":"both"}`),
		"remote control":    debuggerEntryJSON(adapter, `,"remote":{"forward":"local","argsTemplate":"a`+"\\u0007"+`"}`),
		"bad platform":      debuggerEntryJSON(adapter, `,"platforms":["plan9"]`),
		"unknown field":     debuggerEntryJSON(adapter, `,"cwd":"x"`),
	}
	for name, raw := range cases {
		if _, err := decodeDebuggerForTest(ctx, raw); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func writeDebuggerManifest(t *testing.T, capabilities []string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "plugin.mjs"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"id": debuggerTestPlugin, "name": "Debugger fixture", "version": "1.0.0", "apiVersion": 2,
		"category": "development", "capabilities": capabilities,
		"runtime": map[string]any{"type": "process", "entry": "bin/plugin.mjs"},
		"contributes": map[string]any{
			"actions": []any{
				map[string]any{"id": "debug.resolve", "risk": "read", "maxPayloadBytes": 65536},
				map[string]any{"id": "debug.entryPoints", "risk": "read", "maxPayloadBytes": 65536},
				map[string]any{"id": "debug.check", "risk": "read"},
				map[string]any{"id": "debug.settings", "risk": "read", "maxPayloadBytes": 65536},
			},
			"debuggers": []any{map[string]any{
				"type": "fake", "title": "Fake", "languages": []string{"python"},
				"adapter": map[string]any{
					"kind": "stdio", "command": "${resolved.python}",
					"args": []string{debuggerFakeAdapterArg},
					"env":  map[string]string{debuggerFakeAdapterEnv: "stdio"},
				},
				"resolveAction": "debug.resolve", "entryPointsAction": "debug.entryPoints", "checkAction": "debug.check",
				"settingsAction":      "debug.settings",
				"exceptionTypes":      map[string]any{"placeholder": "pkg.Failure", "descriptionLocalized": map[string]string{"zh": "只在这些类型处暂停"}},
				"hotCodeReplace":      map[string]any{"event": "codeReloaded", "request": "applyCode"},
				"functionBreakpoints": map[string]any{"placeholder": "pkg.Type.method"},
			}},
		},
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDebuggersRequireShellCapability(t *testing.T) {
	useTemporaryPluginData(t)
	if _, err := inspectInstallDirectory(writeDebuggerManifest(t, []string{"ui"})); err == nil || !strings.Contains(err.Error(), "requires capability shell") {
		t.Fatalf("without shell: err = %v", err)
	}
	preview, err := inspectInstallDirectory(writeDebuggerManifest(t, []string{"shell", "ui"}))
	if err != nil {
		t.Fatal(err)
	}
	if resolved := preview.Manifest.contribution(debuggerPointKey); resolved == nil || len(resolved.Entries) != 1 {
		t.Fatalf("debuggers = %+v", resolved)
	}
}

type debuggerFixture struct {
	manager *pluginManager
	record  *pluginRecord
	root    string
	project string
	python  string

	mu       sync.Mutex
	payloads map[string][]map[string]any
	results  map[string]string
	errs     map[string]error
	timeouts map[string][]time.Duration
}

func newDebuggerFixture(t *testing.T) *debuggerFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses POSIX executables")
	}
	useTemporaryPluginData(t)
	return newDebuggerFixtureAt(t, writeDebuggerManifest(t, []string{"shell", "ui"}))
}

// newDebuggerFixtureAt loads the plugin at root (written by
// writeDebuggerManifest, maybe extended) as a running record.
func newDebuggerFixtureAt(t *testing.T, root string) *debuggerFixture {
	t.Helper()
	preview, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	parsed := preview.Manifest
	record := &pluginRecord{
		id: debuggerTestPlugin, root: root, source: pluginSourceLocal, manifest: &parsed,
		enabled: true, state: pluginStateRunning, capabilities: append([]api.Capability(nil), parsed.Capabilities...),
	}
	fixture := &debuggerFixture{
		manager: &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}},
		record:  record, root: root, project: t.TempDir(),
		python:   writeTerminalProfileExecutable(t, t.TempDir(), "bin/python"),
		payloads: map[string][]map[string]any{},
		results:  map[string]string{},
		errs:     map[string]error{},
		timeouts: map[string][]time.Duration{},
	}
	fixture.setPython(fixture.python)
	previousInvoke := debuggerInvoke
	debuggerInvoke = func(manager *pluginManager, _ context.Context, call contributedActionCall) (json.RawMessage, error) {
		record, err := manager.record(call.PluginID)
		if err != nil {
			return nil, err
		}
		record.mu.Lock()
		err = call.Validate(record)
		record.mu.Unlock()
		if err != nil {
			return nil, err
		}
		var payload map[string]any
		if err := json.Unmarshal(call.Payload, &payload); err != nil {
			t.Errorf("payload %s: %v", call.Payload, err)
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.payloads[call.ActionID] = append(fixture.payloads[call.ActionID], payload)
		fixture.timeouts[call.ActionID] = append(fixture.timeouts[call.ActionID], call.Timeout)
		if err := fixture.errs[call.ActionID]; err != nil {
			return nil, err
		}
		return json.RawMessage(fixture.results[call.ActionID]), nil
	}
	t.Cleanup(func() {
		debuggerInvoke = previousInvoke
		debug.StopForOwner(debuggerTestPlugin, "test cleanup")
		forgetPreparedDebugLaunches(debuggerTestPlugin)
	})
	return fixture
}

func (fixture *debuggerFixture) setResult(action, result string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.results[action] = result
}

func (fixture *debuggerFixture) setPython(python string) {
	encoded, _ := json.Marshal(python)
	fixture.setResult("debug.resolve", `{"variables":{"python":`+string(encoded)+`},"request":"launch","arguments":{"program":"main.py"}}`)
}

func (fixture *debuggerFixture) prepare(t *testing.T) PreparedDebugLaunch {
	t.Helper()
	prepared, err := fixture.manager.prepareDebugger(context.Background(), debuggerPrepareRequest{
		PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project,
		Configuration: json.RawMessage(`{"name":"Current file","program":"main.py"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func (fixture *debuggerFixture) useTestBinaryAsAdapter(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixture.setPython(executable)
}

func debuggerStartForTest(manager *pluginManager, prepared PreparedDebugLaunch, fingerprint string, breakpoints map[string][]debug.SourceBreakpoint) (DebuggerStartResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return manager.startDebugger(ctx, debuggerStartRequest{PrepareID: prepared.PrepareID, Fingerprint: fingerprint, Breakpoints: breakpoints, ExceptionFilters: []string{"raised"}})
}

func TestPrepareExpandsResolvedVariable(t *testing.T) {
	fixture := newDebuggerFixture(t)
	prepared := fixture.prepare(t)
	launch := prepared.Launch
	if launch.Argv[0] != fixture.python || launch.Trusted || launch.PreviouslyTrusted || launch.Fingerprint == "" ||
		launch.Argv[1] != debuggerFakeAdapterArg || launch.Env[debuggerFakeAdapterEnv] != "stdio" {
		t.Fatalf("launch = %+v", launch)
	}
	if len(prepared.PrepareID) != 32 || prepared.Request != "launch" || string(prepared.Arguments) != `{"program":"main.py"}` || prepared.Kind != "stdio" {
		t.Fatalf("prepared = %+v", prepared)
	}
	payloads := fixture.payloads["debug.resolve"]
	if len(payloads) != 1 {
		t.Fatalf("payloads = %v", payloads)
	}
	payload := payloads[0]
	project, _ := payload["project"].(map[string]any)
	configuration, _ := payload["configuration"].(map[string]any)
	if payload["type"] != "fake" || project["root"] != prepared.ProjectRoot || configuration["program"] != "main.py" || payload["noDebug"] != false || payload["platform"] != runtime.GOOS {
		t.Fatalf("payload = %v", payload)
	}
	// A different interpreter is a different fingerprint.
	other := writeTerminalProfileExecutable(t, t.TempDir(), "python3")
	fixture.setPython(other)
	if again := fixture.prepare(t); again.Launch.Fingerprint == launch.Fingerprint || again.PrepareID == prepared.PrepareID {
		t.Fatalf("fingerprint did not change: %+v", again.Launch)
	}
	// A resolved executable inside the plugin directory is hashed.
	fixture.setPython(writeTerminalProfileExecutable(t, fixture.root, "bin/python"))
	if inside := fixture.prepare(t); !inside.Launch.PluginExecutable || len(inside.Launch.ExecutableSHA256) != 64 {
		t.Fatalf("plugin executable = %+v", inside.Launch)
	}
}

func TestPrepareConfirmsAWorkingDirectoryOutsideTheProject(t *testing.T) {
	fixture := newDebuggerFixture(t)
	withCwd := func(cwd string) PreparedDebugLaunch {
		t.Helper()
		encodedPython, _ := json.Marshal(fixture.python)
		arguments := `{"program":"main.py"}`
		if cwd != "" {
			encodedCwd, _ := json.Marshal(cwd)
			arguments = `{"program":"main.py","cwd":` + string(encodedCwd) + `}`
		}
		fixture.setResult("debug.resolve", `{"variables":{"python":`+string(encodedPython)+`},"request":"launch","arguments":`+arguments+`}`)
		return fixture.prepare(t)
	}
	base := withCwd("")
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: base.PrepareID, Fingerprint: base.Launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	// A cwd in the project is covered by the same confirmation.
	sub := filepath.Join(fixture.project, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{fixture.project, sub, "relative"} {
		if inside := withCwd(cwd); inside.Launch.Fingerprint != base.Launch.Fingerprint || !inside.Launch.Trusted {
			t.Errorf("cwd %q: launch %+v, want the confirmed fingerprint", cwd, inside.Launch)
		}
	}
	// One outside the project must be confirmed (it is shown there), each one on its own.
	outside := t.TempDir()
	first := withCwd(outside)
	if first.Launch.Fingerprint == base.Launch.Fingerprint || first.Launch.Trusted {
		t.Fatalf("outside cwd: launch %+v, want a new confirmation", first.Launch)
	}
	if other := withCwd(t.TempDir()); other.Launch.Fingerprint == first.Launch.Fingerprint {
		t.Fatalf("two outside cwds share a fingerprint")
	}
	// The prepared launch keeps the outside cwd it judged (start passes it to
	// Begin as ConfirmedOutsideCwd); an in-project cwd keeps none.
	realOutside, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	for prepared, want := range map[string]string{first.PrepareID: realOutside, withCwd(sub).PrepareID: ""} {
		entry, err := preparedDebugLaunch(prepared, false)
		if err != nil || entry.outsideCwd != want {
			t.Errorf("prepared outsideCwd = %+v, %v, want %q", entry, err, want)
		}
	}
}

func TestPrepareConfirmsAnOutsideCwdLinkByItsTarget(t *testing.T) {
	fixture := newDebuggerFixture(t)
	link := filepath.Join(t.TempDir(), "workdir")
	target, other := t.TempDir(), t.TempDir()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	withLinkCwd := func() PreparedDebugLaunch {
		t.Helper()
		encodedPython, _ := json.Marshal(fixture.python)
		encodedCwd, _ := json.Marshal(link)
		fixture.setResult("debug.resolve", `{"variables":{"python":`+string(encodedPython)+`},"request":"launch","arguments":{"program":"main.py","cwd":`+string(encodedCwd)+`}}`)
		return fixture.prepare(t)
	}
	first := withLinkCwd()
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: first.PrepareID, Fingerprint: first.Launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if entry, err := preparedDebugLaunch(first.PrepareID, false); err != nil || entry.outsideCwd != realTarget {
		t.Fatalf("prepared outsideCwd = %+v, %v, want the link target %q", entry, err, realTarget)
	}
	if same := withLinkCwd(); same.Launch.Fingerprint != first.Launch.Fingerprint || !same.Launch.Trusted {
		t.Fatalf("unchanged link: launch %+v, want the confirmed fingerprint", same.Launch)
	}
	// The link is re-pointed: a new directory, so a new confirmation.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if moved := withLinkCwd(); moved.Launch.Fingerprint == first.Launch.Fingerprint || moved.Launch.Trusted {
		t.Fatalf("re-pointed link: launch %+v, want a new confirmation", moved.Launch)
	}
}

func TestPrepareJudgesTheOutsideCwdOnceForFingerprintAndEntry(t *testing.T) {
	fixture := newDebuggerFixture(t)
	base := fixture.prepare(t)
	// The judgement changes between two calls (e.g. a directory swapped for a
	// link meanwhile): the stored value must be the one the fingerprint used.
	calls := 0
	previous := debuggerOutsideCwd
	debuggerOutsideCwd = func(string, json.RawMessage) string {
		calls++
		if calls == 1 {
			return "/elsewhere/first"
		}
		return ""
	}
	t.Cleanup(func() { debuggerOutsideCwd = previous })
	prepared := fixture.prepare(t)
	entry, err := preparedDebugLaunch(prepared.PrepareID, false)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || entry.outsideCwd != "/elsewhere/first" || prepared.Launch.Fingerprint == base.Launch.Fingerprint {
		t.Fatalf("calls %d, stored %q, fingerprint changed %v: want one judgement used for both", calls, entry.outsideCwd, prepared.Launch.Fingerprint != base.Launch.Fingerprint)
	}
}

func TestPrepareKeepsPortDeferred(t *testing.T) {
	fixture := newDebuggerFixture(t)
	fixture.record.mu.Lock()
	debugger := fixture.record.manifest.contributions[debuggerPointKey].Entries[0].(DebuggerContribution)
	debugger.Adapter = DebuggerAdapter{Kind: "tcp", Command: "${resolved.python}", Args: []string{"--listen=127.0.0.1:${port}", "--root=${projectRoot}"}, Env: map[string]string{}}
	debugger.SpecFingerprint = debuggerSpecFingerprint(debugger)
	fixture.record.manifest.contributions[debuggerPointKey].Entries[0] = debugger
	fixture.record.mu.Unlock()
	prepared := fixture.prepare(t)
	if prepared.Launch.Argv[1] != "--listen=127.0.0.1:${port}" || prepared.Launch.Argv[2] != "--root="+prepared.ProjectRoot || prepared.Kind != "tcp" {
		t.Fatalf("argv = %v", prepared.Launch.Argv)
	}
}

func TestPrepareRejectsResolvedPathNotExecutable(t *testing.T) {
	fixture := newDebuggerFixture(t)
	notExecutable := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(notExecutable, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, python := range map[string]string{
		"directory":      t.TempDir(),
		"missing":        filepath.Join(t.TempDir(), "python"),
		"relative":       "bin/python",
		"bare":           "python3",
		"not executable": notExecutable,
		"variable":       "${pluginRoot}/bin/plugin.mjs",
	} {
		fixture.setPython(python)
		_, err := fixture.manager.prepareDebugger(context.Background(), debuggerPrepareRequest{PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project})
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestPrepareRejectsUnknownResolvedName(t *testing.T) {
	fixture := newDebuggerFixture(t)
	fixture.setResult("debug.resolve", `{"variables":{"interpreter":"/usr/bin/python3"},"request":"launch","arguments":{}}`)
	_, err := fixture.manager.prepareDebugger(context.Background(), debuggerPrepareRequest{PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project})
	if err == nil || !strings.Contains(err.Error(), `"python"`) || contributionErrorCode(err) != contributionErrInvalidResult {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareRejectsBadResolveResult(t *testing.T) {
	fixture := newDebuggerFixture(t)
	for _, result := range []string{
		`{"variables":{"python":"/usr/bin/python3"},"request":"run","arguments":{}}`,
		`{"variables":{"python":"/usr/bin/python3"},"arguments":{}}`,
		`{"variables":{"python":"/usr/bin/python3"},"request":"launch","arguments":[]}`,
		`{"variables":{"python":"/usr/bin/python3"},"request":"launch"}`,
		`{"variables":{"python":"/usr/bin/python3"},"request":"launch","arguments":{},"extra":1}`,
		`{"variables":{"bad-name":"x"},"request":"launch","arguments":{}}`,
		`{"variables":{"python":"/usr/bin/python3\n"},"request":"launch","arguments":{}}`,
		`[]`,
	} {
		fixture.setResult("debug.resolve", result)
		_, err := fixture.manager.prepareDebugger(context.Background(), debuggerPrepareRequest{PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project})
		if contributionErrorCode(err) != contributionErrInvalidResult {
			t.Errorf("%s: err = %v", result, err)
		}
	}
	if _, err := fixture.manager.prepareDebugger(context.Background(), debuggerPrepareRequest{PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project, Configuration: json.RawMessage(`[1]`)}); contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Fatalf("array configuration: %v", err)
	}
}

func TestStartRequiresTrust(t *testing.T) {
	fixture := newDebuggerFixture(t)
	prepared := fixture.prepare(t)
	_, err := debuggerStartForTest(fixture.manager, prepared, prepared.Launch.Fingerprint, nil)
	if processLaunchErrorCode(err) != processLaunchErrUntrusted {
		t.Fatalf("err = %v", err)
	}
	if len(debug.List()) != 0 {
		t.Fatalf("sessions = %v", debug.List())
	}
}

func TestStartRefusesChangedFingerprint(t *testing.T) {
	fixture := newDebuggerFixture(t)
	first := fixture.prepare(t)
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: first.PrepareID, Fingerprint: "stale"}); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("trust with a wrong fingerprint: %v", err)
	}
	trusted, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: first.PrepareID, Fingerprint: first.Launch.Fingerprint})
	if err != nil || !trusted.Launch.Trusted {
		t.Fatalf("trust = %+v, %v", trusted, err)
	}
	// The plugin now resolves a different interpreter.
	fixture.setPython(writeTerminalProfileExecutable(t, t.TempDir(), "python"))
	second := fixture.prepare(t)
	if second.Launch.Trusted || !second.Launch.PreviouslyTrusted {
		t.Fatalf("second = %+v", second.Launch)
	}
	for _, fingerprint := range []string{first.Launch.Fingerprint, second.Launch.Fingerprint} {
		retry := fixture.prepare(t)
		_, err := debuggerStartForTest(fixture.manager, retry, fingerprint, nil)
		if processLaunchErrorCode(err) != processLaunchErrChanged {
			t.Fatalf("start with %s: err = %v", fingerprint, err)
		}
	}
	if len(debug.List()) != 0 {
		t.Fatalf("sessions = %v", debug.List())
	}
}

// start re-reads the stored confirmation instead of trusting what prepare
// saw: a launch prepared while its fingerprint was confirmed cannot start
// once that confirmation was evicted by newer ones.
func TestStartRereadsConfirmation(t *testing.T) {
	fixture := newDebuggerFixture(t)
	prepared := fixture.prepare(t)
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	stale := fixture.prepare(t)
	if !stale.Launch.Trusted {
		t.Fatalf("stale = %+v", stale.Launch)
	}
	for i := 0; i < processLaunchFingerprintsPerEntry[debuggerPointKey]; i++ {
		fixture.setPython(writeTerminalProfileExecutable(t, t.TempDir(), "python"))
		other := fixture.prepare(t)
		if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: other.PrepareID, Fingerprint: other.Launch.Fingerprint}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := debuggerStartForTest(fixture.manager, stale, stale.Launch.Fingerprint, nil); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("start of an evicted confirmation: %v", err)
	}
	if len(debug.List()) != 0 {
		t.Fatalf("sessions = %v", debug.List())
	}
}

// Run configurations of one debugger and project that resolve different
// interpreters keep their own confirmations: switching back to a reviewed
// one does not ask again, up to processLaunchFingerprintsPerEntry of them.
func TestDebuggerKeepsRecentConfirmationsPerProject(t *testing.T) {
	fixture := newDebuggerFixture(t)
	confirm := func(python string) PreparedDebugLaunch {
		t.Helper()
		fixture.setPython(python)
		prepared := fixture.prepare(t)
		if prepared.Launch.Trusted {
			return prepared
		}
		trusted, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint})
		if err != nil || !trusted.Launch.Trusted {
			t.Fatalf("trust %s = %+v, %v", python, trusted.Launch, err)
		}
		return trusted
	}
	a := fixture.python
	b := writeTerminalProfileExecutable(t, t.TempDir(), "python")
	confirm(a)
	confirm(b)
	for _, python := range []string{a, b, a} {
		fixture.setPython(python)
		if again := fixture.prepare(t); !again.Launch.Trusted || again.Launch.PreviouslyTrusted {
			t.Fatalf("switching back to %s asks again: %+v", python, again.Launch)
		}
	}
	// An interpreter never reviewed is still a changed launch.
	fixture.setPython(writeTerminalProfileExecutable(t, t.TempDir(), "python"))
	if fresh := fixture.prepare(t); fresh.Launch.Trusted || !fresh.Launch.PreviouslyTrusted {
		t.Fatalf("unreviewed interpreter = %+v", fresh.Launch)
	}
	// Only the newest processLaunchFingerprintsPerEntry stay: a's confirmation
	// is the oldest once that many newer ones were confirmed.
	keep := processLaunchFingerprintsPerEntry[debuggerPointKey]
	for i := 0; i < keep; i++ {
		confirm(writeTerminalProfileExecutable(t, t.TempDir(), "python"))
	}
	fixture.setPython(a)
	if evicted := fixture.prepare(t); evicted.Launch.Trusted {
		t.Fatalf("the oldest of %d+ confirmations was kept: %+v", keep, evicted.Launch)
	}
	// A plugin upgrade invalidates every kept confirmation (each fingerprint
	// covers the plugin version).
	fixture.setPython(b)
	confirm(b)
	fixture.record.mu.Lock()
	upgrade := *fixture.record.manifest
	upgrade.Version = "9.9.9"
	fixture.record.manifest = &upgrade
	fixture.record.mu.Unlock()
	if upgraded := fixture.prepare(t); upgraded.Launch.Trusted || !upgraded.Launch.PreviouslyTrusted {
		t.Fatalf("upgrade kept a confirmation: %+v", upgraded.Launch)
	}
}

func startTrustedDebugger(t *testing.T, fixture *debuggerFixture, breakpoints map[string][]debug.SourceBreakpoint) (PreparedDebugLaunch, DebuggerStartResult) {
	t.Helper()
	fixture.useTestBinaryAsAdapter(t)
	prepared := fixture.prepare(t)
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	result, err := debuggerStartForTest(fixture.manager, prepared, prepared.Launch.Fingerprint, breakpoints)
	if err != nil {
		t.Fatal(err)
	}
	return prepared, result
}

func TestPrepareIDSingleUse(t *testing.T) {
	fixture := newDebuggerFixture(t)
	source := filepath.Join(fixture.project, "main.py")
	if err := os.WriteFile(source, []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "other.py")
	if err := os.WriteFile(outside, []byte("print(2)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(fixture.project, "deleted.py")
	prepared, result := startTrustedDebugger(t, fixture, map[string][]debug.SourceBreakpoint{
		source: {{Line: 1}}, outside: {{Line: 1}}, stale: {{Line: 3}},
	})
	if result.SessionID == "" || !strings.Contains(string(result.Capabilities), "supportsConfigurationDoneRequest") {
		t.Fatalf("result = %+v", result)
	}
	if strings.Join(result.SkippedBreakpoints, ",") != strings.Join([]string{stale, outside}, ",") && strings.Join(result.SkippedBreakpoints, ",") != strings.Join([]string{outside, stale}, ",") {
		t.Fatalf("skipped = %v", result.SkippedBreakpoints)
	}
	sessions := debug.List()
	if len(sessions) != 1 || sessions[0].OwnerPluginID != debuggerTestPlugin || sessions[0].DebuggerType != "fake" || sessions[0].ProjectRoot != fixture.project {
		t.Fatalf("sessions = %+v", sessions)
	}
	if _, err := debuggerStartForTest(fixture.manager, prepared, prepared.Launch.Fingerprint, nil); processLaunchErrorCode(err) != processLaunchErrNotFound {
		t.Fatalf("second start: %v", err)
	}
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint}); processLaunchErrorCode(err) != processLaunchErrNotFound {
		t.Fatalf("trust after start: %v", err)
	}
	// A confirmed launch needs no new confirmation for the next prepare.
	if again := fixture.prepare(t); !again.Launch.Trusted {
		t.Fatalf("again = %+v", again.Launch)
	}
}

func TestPrepareIDExpires(t *testing.T) {
	fixture := newDebuggerFixture(t)
	now := time.Now()
	previous := debuggerNow
	debuggerNow = func() time.Time { return now }
	t.Cleanup(func() { debuggerNow = previous })
	prepared := fixture.prepare(t)
	now = now.Add(debuggerPrepareTTL + time.Second)
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint}); processLaunchErrorCode(err) != processLaunchErrNotFound {
		t.Fatalf("expired trust: %v", err)
	}
	// The cache keeps at most debuggerPreparedMax entries.
	for range debuggerPreparedMax + 3 {
		fixture.prepare(t)
	}
	debuggerPrepared.mu.Lock()
	count := len(debuggerPrepared.entries)
	debuggerPrepared.mu.Unlock()
	if count > debuggerPreparedMax {
		t.Fatalf("cached %d prepared launches", count)
	}
}

func TestStopPluginDebugSessionsKillsAdapter(t *testing.T) {
	fixture := newDebuggerFixture(t)
	startTrustedDebugger(t, fixture, nil)
	if len(debug.List()) != 1 {
		t.Fatalf("sessions = %v", debug.List())
	}
	pending := fixture.prepare(t)
	fixture.record.mu.Lock()
	fixture.record.enabled = false
	fixture.record.mu.Unlock()
	fixture.manager.runRecordLifecycleHooks(fixture.record)
	if sessions := debug.List(); len(sessions) != 0 {
		t.Fatalf("sessions after disable = %v", sessions)
	}
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: pending.PrepareID, Fingerprint: pending.Launch.Fingerprint}); processLaunchErrorCode(err) != processLaunchErrNotFound {
		t.Fatalf("prepared launch survived disable: %v", err)
	}
	if _, err := fixture.manager.prepareDebugger(context.Background(), debuggerPrepareRequest{PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project}); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("prepare while disabled: %v", err)
	}
}

func TestDebuggersListAndOwnership(t *testing.T) {
	fixture := newDebuggerFixture(t)
	items := fixture.manager.listDebuggers()
	if len(items) != 1 || items[0].PluginID != debuggerTestPlugin || items[0].Type != "fake" || !items[0].HasCheck || !items[0].HasEntryPoints || items[0].AdapterKind != "stdio" ||
		!items[0].HasSettings || items[0].ExceptionTypes == nil || items[0].ExceptionTypes.Placeholder != "pkg.Failure" || items[0].ExceptionTypes.DescriptionLocalized["zh"] != "只在这些类型处暂停" {
		t.Fatalf("items = %+v", items)
	}
	encoded, _ := json.Marshal(items[0])
	if !strings.Contains(string(encoded), `"hasSettings":true`) || !strings.Contains(string(encoded), `"exceptionTypes":{"placeholder":"pkg.Failure","descriptionLocalized":{"zh":"只在这些类型处暂停"}}`) ||
		!strings.Contains(string(encoded), `"hotCodeReplace":{"event":"codeReloaded","request":"applyCode"}`) || !strings.Contains(string(encoded), `"functionBreakpoints":{"placeholder":"pkg.Type.method"}`) {
		t.Fatalf("list item JSON = %s", encoded)
	}
	// A second plugin offering the same type is shadowed by the first.
	second := &pluginRecord{
		id: "debug.second", root: fixture.root, source: pluginSourceLocal, manifest: fixture.record.manifest,
		enabled: true, state: pluginStateRunning, capabilities: []api.Capability{api.CapabilityShell},
	}
	fixture.manager.records[second.id] = second
	fixture.manager.order = append(fixture.manager.order, second.id)
	if items := fixture.manager.listDebuggers(); len(items) != 1 || items[0].PluginID != debuggerTestPlugin {
		t.Fatalf("items = %+v", items)
	}
	if _, err := fixture.manager.prepareDebugger(context.Background(), debuggerPrepareRequest{PluginID: second.id, Type: "fake", ProjectRoot: fixture.project}); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("shadowed prepare: %v", err)
	}
	// Without shell nothing is listed.
	fixture.record.mu.Lock()
	fixture.record.capabilities = nil
	fixture.record.mu.Unlock()
	if items := fixture.manager.listDebuggers(); len(items) != 1 || items[0].PluginID != second.id {
		t.Fatalf("items without shell = %+v", items)
	}
}

func TestDebuggerEntryPointsAndCheck(t *testing.T) {
	fixture := newDebuggerFixture(t)
	source := filepath.Join(fixture.project, "main.py")
	if err := os.WriteFile(source, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.setResult("debug.entryPoints", `[{"line":12,"label":"Run main","configuration":{"request":"launch","program":"main.py"}},{"line":0,"label":"bad","configuration":{}},{"line":3,"label":"","configuration":{}},{"line":4,"label":"x","configuration":[]},{"line":2,"label":"First","configuration":{}}]`)
	points, err := fixture.manager.debuggerEntryPoints(context.Background(), debuggerEntryPointsRequest{PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project, File: source, LanguageID: "python"})
	if err != nil || len(points) != 2 || points[0].Line != 2 || points[1].Line != 12 || string(points[1].Configuration) != `{"request":"launch","program":"main.py"}` {
		t.Fatalf("points = %+v, %v", points, err)
	}
	payload := fixture.payloads["debug.entryPoints"][0]
	if payload["file"] != source || payload["languageId"] != "python" {
		t.Fatalf("payload = %v", payload)
	}
	if _, err := fixture.manager.debuggerEntryPoints(context.Background(), debuggerEntryPointsRequest{PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project, File: filepath.Join(t.TempDir(), "x.py"), LanguageID: "python"}); err == nil {
		t.Fatal("a file outside the project must be refused")
	}
	many := make([]string, 0, 70)
	for line := 1; line <= 70; line++ {
		many = append(many, `{"line":`+jsonInt(line)+`,"label":"L","configuration":{}}`)
	}
	fixture.setResult("debug.entryPoints", "["+strings.Join(many, ",")+"]")
	if points, err := fixture.manager.debuggerEntryPoints(context.Background(), debuggerEntryPointsRequest{PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project, File: source, LanguageID: "python"}); err != nil || len(points) != debuggerEntryPointsMax {
		t.Fatalf("capped points = %d, %v", len(points), err)
	}

	fixture.setResult("debug.check", `{"ready":false,"missing":[{"name":"debugpy","hint":"Install debugpy","installCommand":"python3 -m pip install debugpy"}]}`)
	check, err := fixture.manager.checkDebugger(context.Background(), debuggerCheckRequest{PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project})
	if err != nil || check.Ready || len(check.Missing) != 1 || check.Missing[0].InstallCommand != "python3 -m pip install debugpy" {
		t.Fatalf("check = %+v, %v", check, err)
	}
	for _, bad := range []string{`{"missing":[]}`, `{"ready":true,"missing":[{"name":"x"}]}`, `{"ready":false,"missing":[{"name":"x","installCommand":"a\nb"}]}`, `{"ready":true,"extra":1}`} {
		fixture.setResult("debug.check", bad)
		if _, err := fixture.manager.checkDebugger(context.Background(), debuggerCheckRequest{PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project}); contributionErrorCode(err) != contributionErrInvalidResult {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
}

// The run configuration the user is about to start reaches checkAction as
// payload.configuration (omitted without one); a non-object is refused.
func TestCheckForwardsConfiguration(t *testing.T) {
	fixture := newDebuggerFixture(t)
	fixture.setResult("debug.check", `{"ready":true,"missing":[]}`)
	check := func(configuration string) (DebuggerCheckResult, error) {
		return fixture.manager.checkDebugger(context.Background(), debuggerCheckRequest{
			PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project,
			Configuration: json.RawMessage(configuration),
		})
	}
	if result, err := check(`{"name":"Run","python":"/opt/py/bin/python","program":"main.py"}`); err != nil || !result.Ready {
		t.Fatalf("check = %+v, %v", result, err)
	}
	payload := fixture.payloads["debug.check"][0]
	want := map[string]any{"name": "Run", "python": "/opt/py/bin/python", "program": "main.py"}
	if !reflect.DeepEqual(payload["configuration"], want) {
		t.Fatalf("payload configuration = %#v, want %#v", payload["configuration"], want)
	}
	for _, absent := range []string{"", "null", " "} {
		if _, err := check(absent); err != nil {
			t.Fatalf("check without configuration %q: %v", absent, err)
		}
		last := fixture.payloads["debug.check"][len(fixture.payloads["debug.check"])-1]
		if _, ok := last["configuration"]; ok {
			t.Fatalf("configuration %q: payload = %v, want no configuration member", absent, last)
		}
	}
	calls := len(fixture.payloads["debug.check"])
	for _, bad := range []string{`[]`, `"x"`, `1`} {
		if _, err := check(bad); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("configuration %s: err = %v, want invalid request", bad, err)
		}
	}
	if len(fixture.payloads["debug.check"]) != calls {
		t.Fatal("an invalid configuration reached the plugin")
	}
}

// exceptionTypes (texts for the host's list) and settingsAction (the read
// action that receives the saved list) decode with the line text rules;
// settingsAction follows the checkAction rules and needs room for a full list.
func TestDebuggersDecodeExceptionTypesAndSettingsAction(t *testing.T) {
	ctx := debuggerTestContext(t)
	adapter := `{"kind":"stdio","command":"dlv"}`
	debugger, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, `,"settingsAction":" debug.settings ","exceptionTypes":{
		"placeholder":" pkg.Failure ","placeholderLocalized":{"zh":"包名.类名"},
		"description":"Pause only on these types","descriptionLocalized":{"zh":"只在这些类型处暂停"}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := DebuggerTextHints{
		Placeholder: "pkg.Failure", PlaceholderLocalized: map[string]string{"zh": "包名.类名"},
		Description: "Pause only on these types", DescriptionLocalized: map[string]string{"zh": "只在这些类型处暂停"},
	}
	if debugger.SettingsAction != "debug.settings" || debugger.ExceptionTypes == nil || !reflect.DeepEqual(*debugger.ExceptionTypes, want) {
		t.Fatalf("debugger = %+v (exceptionTypes %+v)", debugger, debugger.ExceptionTypes)
	}
	// Every text is optional; settingsAction alone is fine too.
	if empty, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, `,"settingsAction":"debug.settings","exceptionTypes":{}`)); err != nil || empty.ExceptionTypes == nil || !reflect.DeepEqual(*empty.ExceptionTypes, DebuggerTextHints{}) {
		t.Fatalf("empty exceptionTypes = %+v, %v", empty.ExceptionTypes, err)
	}
	if alone, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, `,"settingsAction":"debug.settings"`)); err != nil || alone.ExceptionTypes != nil {
		t.Fatalf("settingsAction alone = %+v, %v", alone, err)
	}
	if plain, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, "")); err != nil || plain.ExceptionTypes != nil || plain.SettingsAction != "" {
		t.Fatalf("plain = %+v, %v", plain, err)
	}
	if other, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, `,"settingsAction":"debug.settings","exceptionTypes":{"placeholder":"x"}`)); err != nil || other.SpecFingerprint == debugger.SpecFingerprint {
		t.Fatalf("exceptionTypes must be part of the spec fingerprint: %v", err)
	}
	cases := map[string]string{
		`,"settingsAction":"debug.missing"`:                                 "contributes.actions",
		`,"settingsAction":"debug.write"`:                                   `risk "read"`,
		`,"settingsAction":"debug.ai"`:                                      "aiCallable",
		`,"settingsAction":""`:                                              "contributes.actions",
		`,"settingsAction":"debug.smallRead"`:                               "maxPayloadBytes",
		`,"exceptionTypes":{}`:                                              "requires settingsAction",
		`,"settingsAction":"debug.settings","exceptionTypes":[]`:            "exceptionTypes",
		`,"settingsAction":"debug.settings","exceptionTypes":{"label":"x"}`: "unknown field",
		`,"settingsAction":"debug.settings","exceptionTypes":{"placeholder":"` + strings.Repeat("a", 201) + `"}`:                 "placeholder",
		`,"settingsAction":"debug.settings","exceptionTypes":{"placeholderLocalized":{"zh":"` + strings.Repeat("a", 201) + `"}}`: "placeholderLocalized",
		`,"settingsAction":"debug.settings","exceptionTypes":{"description":"` + strings.Repeat("a", 2001) + `"}`:                "description",
		`,"settingsAction":"debug.settings","exceptionTypes":{"descriptionLocalized":{"zh":"a\u0007"}}`:                          "descriptionLocalized",
		`,"settingsAction":"debug.settings","exceptionTypes":{"description":"a\nb"}`:                                             "description",
	}
	for extra, want := range cases {
		if _, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, extra)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", extra, err, want)
		}
	}
	// Limits at the boundary are accepted.
	if _, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, `,"settingsAction":"debug.settings","exceptionTypes":{"placeholder":"`+strings.Repeat("界", 200)+`","description":"`+strings.Repeat("界", 2000)+`"}`)); err != nil {
		t.Fatalf("texts at the limits: %v", err)
	}
}

func TestNormalizeDebuggerExceptionTypes(t *testing.T) {
	got, err := normalizeDebuggerExceptionTypes([]string{" pkg.Failure ", "pkg.Failure", "\tOuter$Inner", "$x._y9", "_", "pkg.Failure "})
	if err != nil || !reflect.DeepEqual(got, []string{"pkg.Failure", "Outer$Inner", "$x._y9", "_"}) {
		t.Fatalf("normalized = %#v, %v", got, err)
	}
	if got, err := normalizeDebuggerExceptionTypes([]string{}); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty = %#v, %v", got, err)
	}
	if _, err := normalizeDebuggerExceptionTypes(nil); err == nil {
		t.Fatal("a missing list must be refused")
	}
	maxItems := make([]string, 0, 65)
	for index := range 64 {
		maxItems = append(maxItems, fmt.Sprintf("pkg.T%d", index))
	}
	if got, err := normalizeDebuggerExceptionTypes(append(slices.Clone(maxItems), "pkg.T0")); err != nil || len(got) != 64 {
		t.Fatalf("64 unique items plus a duplicate = %d, %v", len(got), err)
	}
	long := "a" + strings.Repeat(".b", 127) // 255 runes
	if _, err := normalizeDebuggerExceptionTypes([]string{long + "c"}); err != nil {
		t.Fatalf("256 runes: %v", err)
	}
	for name, values := range map[string][]string{
		"too many":      append(slices.Clone(maxItems), "pkg.T64"),
		"too long":      {long + "cd"},
		"blank":         {"pkg.A", "  "},
		"inner space":   {"pkg. A"},
		"space between": {"pkg.A B"},
		"leading digit": {"1pkg.A"},
		"double dot":    {"pkg..A"},
		"trailing dot":  {"pkg.A."},
		"leading dot":   {".pkg"},
		"wildcard":      {"pkg.*"},
		"dash":          {"my-pkg.A"},
		"non ascii":     {"pkg.Ä"},
		"control":       {"pkg.A\u0000"},
	} {
		if _, err := normalizeDebuggerExceptionTypes(values); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v, want invalid request", name, err)
		}
	}
}

// applyDebuggerSettings forwards the normalized list (an empty list too) to
// settingsAction within settingsTimeoutMs and accepts only {} back.
func TestApplyDebuggerSettings(t *testing.T) {
	fixture := newDebuggerFixture(t)
	fixture.setResult("debug.settings", `{}`)
	apply := func(exceptionTypes []string) error {
		return fixture.manager.applyDebuggerSettings(context.Background(), debuggerSettingsRequest{
			PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: fixture.project, ExceptionTypes: exceptionTypes,
		})
	}
	if err := apply([]string{" pkg.Failure", "pkg.Failure", "pkg.Outer$Inner "}); err != nil {
		t.Fatal(err)
	}
	root, err := canonicalProjectLaunchRoot(fixture.project, "debuggers")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"type": "fake", "project": map[string]any{"root": root}, "exceptionTypes": []any{"pkg.Failure", "pkg.Outer$Inner"}}
	if payload := fixture.payloads["debug.settings"][0]; !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
	if timeouts := fixture.timeouts["debug.settings"]; len(timeouts) != 1 || timeouts[0] != 10*time.Second || debuggerSettingsTimeout != 10*time.Second {
		t.Fatalf("timeouts = %v", timeouts)
	}
	// Clearing the list still reaches the plugin, as an explicit [].
	if err := apply([]string{}); err != nil {
		t.Fatal(err)
	}
	last := fixture.payloads["debug.settings"][1]
	if list, ok := last["exceptionTypes"].([]any); !ok || len(list) != 0 {
		t.Fatalf("cleared payload = %#v, want exceptionTypes []", last)
	}
	calls := len(fixture.payloads["debug.settings"])
	for name, values := range map[string][]string{"missing": nil, "blank": {"  "}, "bad": {"pkg.A B"}, "too long": {strings.Repeat("a", 257)}} {
		if err := apply(values); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v, want invalid request", name, err)
		}
	}
	if len(fixture.payloads["debug.settings"]) != calls {
		t.Fatal("an invalid list reached the plugin")
	}
	for _, bad := range []string{`null`, `[]`, `"ok"`, `{"applied":true}`, `{} {}`} {
		fixture.setResult("debug.settings", bad)
		if err := apply([]string{}); contributionErrorCode(err) != contributionErrInvalidResult {
			t.Errorf("result %s: err = %v, want invalid result", bad, err)
		}
	}
	// Plugin errors and timeouts reach the WebView unwrapped: the message
	// starts with the error code, as for check.
	fixture.errs["debug.settings"] = newContributionError(contributionErrTimeout, "plugin %q did not answer in time", debuggerTestPlugin)
	if err := apply([]string{}); contributionErrorCode(err) != contributionErrTimeout || !strings.HasPrefix(err.Error(), "timeout: ") {
		t.Fatalf("timeout err = %v, want a message starting with \"timeout: \"", err)
	}
	fixture.errs["debug.settings"] = newContributionError(contributionErrUnavailable, "plugin %q is not running", debuggerTestPlugin)
	if err := apply([]string{}); err == nil || !strings.HasPrefix(err.Error(), "unavailable: ") {
		t.Fatalf("plugin err = %v, want a message starting with \"unavailable: \"", err)
	}
	delete(fixture.errs, "debug.settings")
	// Unknown debugger, or a project root that is not an existing directory.
	calls = len(fixture.payloads["debug.settings"])
	if err := fixture.manager.applyDebuggerSettings(context.Background(), debuggerSettingsRequest{PluginID: debuggerTestPlugin, Type: "other", ProjectRoot: fixture.project, ExceptionTypes: []string{}}); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("unknown type: %v", err)
	}
	for _, root := range []string{"", "relative/dir", filepath.Join(fixture.project, "missing")} {
		if err := fixture.manager.applyDebuggerSettings(context.Background(), debuggerSettingsRequest{PluginID: debuggerTestPlugin, Type: "fake", ProjectRoot: root, ExceptionTypes: []string{}}); err == nil {
			t.Errorf("project root %q: expected an error", root)
		}
	}
	if len(fixture.payloads["debug.settings"]) != calls {
		t.Fatal("a refused debugger or project reached the plugin")
	}
	// A debugger without settingsAction has nothing to apply.
	fixture.record.mu.Lock()
	entries := fixture.record.manifest.contributions[debuggerPointKey].Entries
	plain := entries[0].(DebuggerContribution)
	plain.SettingsAction, plain.ExceptionTypes = "", nil
	entries[0] = plain
	fixture.record.mu.Unlock()
	calls = len(fixture.payloads["debug.settings"])
	if err := apply([]string{"pkg.A"}); err != nil || len(fixture.payloads["debug.settings"]) != calls {
		t.Fatalf("without settingsAction: err = %v, calls %d → %d", err, calls, len(fixture.payloads["debug.settings"]))
	}
	if items := fixture.manager.listDebuggers(); len(items) != 1 || items[0].HasSettings || items[0].ExceptionTypes != nil {
		t.Fatalf("items = %+v", items)
	}
}

func TestDebuggerApplySettingsCommandRegistered(t *testing.T) {
	if !slices.Contains(core.Commands(), "plugin_host_debugger_apply_settings") {
		t.Fatal("plugin_host_debugger_apply_settings is not registered")
	}
	if result, err := core.EncodeResult(debuggerSettingsResult{}); err != nil || result != "{}" {
		t.Fatalf("result = %q, %v", result, err)
	}
}

func jsonInt(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// A project opened through a symlinked root keeps the frontend's spelling for
// the session, so breakpoints set later with that spelling are accepted.
func TestStartDebuggerKeepsRequestedRootSpelling(t *testing.T) {
	fixture := newDebuggerFixture(t)
	link := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(fixture.project, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	fixture.project = link
	source := filepath.Join(link, "main.py")
	if err := os.WriteFile(source, []byte("print(1)\nprint(2)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prepared, result := startTrustedDebugger(t, fixture, map[string][]debug.SourceBreakpoint{source: {{Line: 1}}})
	if len(result.SkippedBreakpoints) != 0 {
		t.Fatalf("skipped = %v", result.SkippedBreakpoints)
	}
	if prepared.ProjectRoot == link {
		t.Fatalf("prepared root %q is not canonical", prepared.ProjectRoot)
	}
	sessions := debug.List()
	if len(sessions) != 1 || sessions[0].ProjectRoot != link {
		t.Fatalf("sessions = %+v, want project root %q", sessions, link)
	}
	args, _ := json.Marshal(map[string]any{
		"sessionId": result.SessionID, "path": source, "breakpoints": []map[string]any{{"line": 2}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := core.DispatchContext(ctx, "debug_set_breakpoints", string(args)); err != nil {
		t.Fatalf("debug_set_breakpoints with the requested root spelling: %v", err)
	}
}

const jdtlsDebuggerCommand = "vscode.java.startDebugSession"

// writeJdtlsDebuggerManifest writes a plugin with a jdtls bundle declaring
// vscode.java.startDebugSession and a "java" debugger whose adapter is
// adapter (nil: the jdtls adapter running that command).
func writeJdtlsDebuggerManifest(t *testing.T, adapter map[string]any) string {
	t.Helper()
	root := t.TempDir()
	writeJdtlsBundleJar(t, root, "bin/plugin.mjs", "\n")
	writeJdtlsBundleJar(t, root, "lib/debug.jar", "jar-v1")
	if adapter == nil {
		adapter = map[string]any{"kind": "jdtls", "command": jdtlsDebuggerCommand}
	}
	manifest := map[string]any{
		"id": jdtlsBundleTestPlugin, "name": "Jdtls fixture", "version": "1.0.0", "apiVersion": 2,
		"category": "development", "capabilities": []string{"shell", "ui"},
		"runtime": map[string]any{"type": "process", "entry": "bin/plugin.mjs"},
		"contributes": map[string]any{
			"actions":      []any{map[string]any{"id": "debug.resolve", "risk": "read", "maxPayloadBytes": 65536}},
			"jdtlsBundles": []any{map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": jdtlsBundleTestCommands}},
			"debuggers": []any{map[string]any{
				"type": "java", "title": "Java", "languages": []string{"java"},
				"adapter": adapter, "resolveAction": "debug.resolve",
			}},
		},
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDebuggersAcceptJdtlsKindWithDeclaredCommand(t *testing.T) {
	useTemporaryPluginData(t)
	preview, err := inspectInstallDirectory(writeJdtlsDebuggerManifest(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	resolved := preview.Manifest.contribution(debuggerPointKey)
	if resolved == nil || len(resolved.Entries) != 1 {
		t.Fatalf("debuggers = %+v", resolved)
	}
	debugger := resolved.Entries[0].(DebuggerContribution)
	if debugger.Adapter.Kind != "jdtls" || debugger.Adapter.Command != jdtlsDebuggerCommand || len(debugger.Adapter.Args) != 0 || len(debugger.Adapter.Env) != 0 {
		t.Fatalf("adapter = %+v", debugger.Adapter)
	}
}

func TestDebuggersRejectJdtlsKindUndeclaredCommand(t *testing.T) {
	useTemporaryPluginData(t)
	_, err := inspectInstallDirectory(writeJdtlsDebuggerManifest(t, map[string]any{"kind": "jdtls", "command": "vscode.java.other"}))
	if err == nil || !strings.Contains(err.Error(), "jdtlsBundles") {
		t.Fatalf("undeclared command: err = %v", err)
	}
	// Without any jdtlsBundles the entry is rejected too.
	ctx := debuggerTestContext(t)
	if _, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(`{"kind":"jdtls","command":"vscode.java.startDebugSession"}`, "")); err == nil || !strings.Contains(err.Error(), "jdtlsBundles") {
		t.Fatalf("no bundles: err = %v", err)
	}
	if _, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(`{"kind":"jdtls","command":"${pluginRoot}/bin/adapter"}`, "")); err == nil {
		t.Fatal("a path as jdtls command was accepted")
	}
}

func TestDebuggersRejectJdtlsKindWithArgs(t *testing.T) {
	useTemporaryPluginData(t)
	for name, adapter := range map[string]map[string]any{
		"args": {"kind": "jdtls", "command": jdtlsDebuggerCommand, "args": []string{"x"}},
		"env":  {"kind": "jdtls", "command": jdtlsDebuggerCommand, "env": map[string]string{"A": "b"}},
	} {
		if _, err := inspectInstallDirectory(writeJdtlsDebuggerManifest(t, adapter)); err == nil || !strings.Contains(err.Error(), "jdtls adapter") {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	// An empty args list is the same as none.
	if _, err := inspectInstallDirectory(writeJdtlsDebuggerManifest(t, map[string]any{"kind": "jdtls", "command": jdtlsDebuggerCommand, "args": []string{}})); err != nil {
		t.Fatalf("empty args: %v", err)
	}
}

type jdtlsDebuggerFixture struct {
	*jdtlsBundleFixture
	mu      sync.Mutex
	calls   []jdtlsExecuteCall
	result  json.RawMessage
	resolve string
}

func newJdtlsDebuggerFixture(t *testing.T) *jdtlsDebuggerFixture {
	t.Helper()
	useTemporaryPluginData(t)
	bundles := newJdtlsBundleFixture(t)
	// Replace the bundle-only record with the debugger plugin.
	root := writeJdtlsDebuggerManifest(t, nil)
	preview, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	parsed := preview.Manifest
	bundles.record.mu.Lock()
	bundles.record.root, bundles.record.manifest = root, &parsed
	bundles.record.capabilities = append([]api.Capability(nil), parsed.Capabilities...)
	bundles.record.mu.Unlock()
	bundles.root = root
	fixture := &jdtlsDebuggerFixture{
		jdtlsBundleFixture: bundles,
		resolve:            `{"request":"launch","arguments":{"mainClass":"com.example.Main"}}`,
	}
	previousInvoke, previousExecute := debuggerInvoke, jdtlsExecuteCommand
	debuggerInvoke = func(manager *pluginManager, _ context.Context, call contributedActionCall) (json.RawMessage, error) {
		record, err := manager.record(call.PluginID)
		if err != nil {
			return nil, err
		}
		record.mu.Lock()
		err = call.Validate(record)
		record.mu.Unlock()
		if err != nil {
			return nil, err
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		return json.RawMessage(fixture.resolve), nil
	}
	jdtlsExecuteCommand = func(ctx context.Context, root, serverID, command string, args json.RawMessage) (json.RawMessage, error) {
		call := jdtlsExecuteCall{root: root, serverID: serverID, command: command, args: args}
		if deadline, ok := ctx.Deadline(); ok {
			call.deadline = time.Until(deadline)
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.calls = append(fixture.calls, call)
		return fixture.result, nil
	}
	t.Cleanup(func() {
		debuggerInvoke, jdtlsExecuteCommand = previousInvoke, previousExecute
		debug.StopForOwner(jdtlsBundleTestPlugin, "test cleanup")
		forgetPreparedDebugLaunches(jdtlsBundleTestPlugin)
	})
	return fixture
}

// load confirms the plugin's jars and runs a fake jdtls started with them.
func (fixture *jdtlsDebuggerFixture) load(t *testing.T) {
	t.Helper()
	fixture.trust(t, jdtlsBundleTestPlugin)
	fixture.running, fixture.session = true, fixture.manager.jdtlsBundlePaths()
}

func (fixture *jdtlsDebuggerFixture) prepare() (PreparedDebugLaunch, error) {
	return fixture.manager.prepareDebugger(context.Background(), debuggerPrepareRequest{
		PluginID: jdtlsBundleTestPlugin, Type: "java", ProjectRoot: fixture.project,
		Configuration: json.RawMessage(`{"name":"Main","mainClass":"com.example.Main"}`),
	})
}

func (fixture *jdtlsDebuggerFixture) prepareTrusted(t *testing.T) PreparedDebugLaunch {
	t.Helper()
	prepared, err := fixture.prepare()
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	return trusted
}

func (fixture *jdtlsDebuggerFixture) setPort(result string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.result = json.RawMessage(result)
}

func TestPrepareJdtlsRequiresLoadedBundle(t *testing.T) {
	fixture := newJdtlsDebuggerFixture(t)
	const want = "unavailable: java-debug is not loaded into the Java language service"
	// The list tells the frontend to run the bundle flow first.
	if items := fixture.manager.listDebuggers(); len(items) != 1 || items[0].AdapterKind != "jdtls" {
		t.Fatalf("items = %+v", items)
	}
	// Not confirmed (jdtls running).
	fixture.running = true
	if _, err := fixture.prepare(); err == nil || err.Error() != want {
		t.Fatalf("unconfirmed: err = %v", err)
	}
	// Confirmed, but jdtls is not running.
	fixture.running = false
	fixture.trust(t, jdtlsBundleTestPlugin)
	if _, err := fixture.prepare(); err == nil || err.Error() != want {
		t.Fatalf("not running: err = %v", err)
	}
	// Running without the jar (started before the confirmation).
	fixture.running, fixture.session = true, nil
	if _, err := fixture.prepare(); err == nil || err.Error() != want {
		t.Fatalf("not loaded: err = %v", err)
	}
	fixture.session = fixture.manager.jdtlsBundlePaths()
	prepared, err := fixture.prepare()
	if err != nil {
		t.Fatal(err)
	}
	launch := prepared.Launch
	if prepared.Kind != "jdtls" || strings.Join(launch.Argv, " ") != "jdtls workspace/executeCommand "+jdtlsDebuggerCommand ||
		len(launch.PluginFiles) != 1 || launch.PluginFiles[0].Relative != "lib/debug.jar" || len(launch.PluginFiles[0].SHA256) != 64 ||
		len(launch.Fingerprint) != 64 || launch.Trusted || launch.Cwd != prepared.ProjectRoot || launch.Env == nil {
		t.Fatalf("prepared = %+v", prepared)
	}
	if len(fixture.calls) != 0 {
		t.Fatalf("prepare ran jdtls commands: %+v", fixture.calls)
	}
	// Starting without confirmation is refused before any jdtls command runs.
	if _, err := debuggerStartForTest(fixture.manager, prepared, launch.Fingerprint, nil); processLaunchErrorCode(err) != processLaunchErrUntrusted {
		t.Fatalf("untrusted start: err = %v", err)
	}
	if len(fixture.calls) != 0 {
		t.Fatalf("untrusted start ran jdtls commands: %+v", fixture.calls)
	}
}

// fakeJavaDebugPort serves DAP on a loopback port (like java-debug inside
// jdtls) and returns the port.
func fakeJavaDebugPort(t *testing.T, observers ...func(debug.Message)) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		_ = listener.Close()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		var seq int64
		ok := true
		for {
			request, err := debug.ReadMessage(reader, 4<<20)
			if err != nil {
				return
			}
			if request.Type != "request" {
				continue
			}
			for _, observe := range observers {
				observe(request)
			}
			var body json.RawMessage
			if request.Command == "initialize" {
				body = json.RawMessage(`{"supportsConfigurationDoneRequest":true}`)
			}
			seq++
			_ = debug.WriteMessage(conn, debug.Message{Seq: seq, Type: "response", RequestSeq: request.Seq, Command: request.Command, Success: &ok, Body: body})
			if request.Command == "initialize" {
				seq++
				_ = debug.WriteMessage(conn, debug.Message{Seq: seq, Type: "event", Event: "initialized"})
			}
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestStartJdtlsConnectsToReturnedPort(t *testing.T) {
	fixture := newJdtlsDebuggerFixture(t)
	fixture.load(t)
	fixture.setPort(jsonInt(fakeJavaDebugPort(t)))
	prepared := fixture.prepareTrusted(t)
	// A start context longer than the command timeout shows the 60 s bound.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := fixture.manager.startDebugger(ctx, debuggerStartRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID == "" || string(result.Capabilities) != `{"supportsConfigurationDoneRequest":true}` {
		t.Fatalf("result = %+v", result)
	}
	sessions := debug.List()
	if len(sessions) != 1 || sessions[0].OwnerPluginID != jdtlsBundleTestPlugin || sessions[0].DebuggerType != "java" {
		t.Fatalf("sessions = %+v", sessions)
	}
	canonical, _ := filepath.EvalSymlinks(fixture.project)
	if len(fixture.calls) != 1 {
		t.Fatalf("calls = %+v", fixture.calls)
	}
	call := fixture.calls[0]
	if call.root != canonical || call.serverID != "jdtls" || call.command != jdtlsDebuggerCommand || string(call.args) != "[]" ||
		call.deadline <= 55*time.Second || call.deadline > 60*time.Second {
		t.Fatalf("call = %+v", call)
	}
	// A loaded set that no longer contains the jar refuses the next start.
	fixture.session = []string{filepath.Join(t.TempDir(), "other.jar")}
	if _, err := fixture.prepare(); err == nil || !strings.Contains(err.Error(), "java-debug is not loaded") {
		t.Fatalf("after reload without the jar: err = %v", err)
	}
}

func TestStartJdtlsRejectsBadPort(t *testing.T) {
	fixture := newJdtlsDebuggerFixture(t)
	fixture.load(t)
	for _, result := range []string{`"5005"`, `0`, `70000`, `5005.5`, `null`, `{"port":5005}`} {
		fixture.setPort(result)
		prepared := fixture.prepareTrusted(t)
		_, err := debuggerStartForTest(fixture.manager, prepared, prepared.Launch.Fingerprint, nil)
		if processLaunchErrorCode(err) != processLaunchErrUnavailable || !strings.Contains(err.Error(), "port") {
			t.Fatalf("result %s: err = %v", result, err)
		}
	}
	if len(debug.List()) != 0 {
		t.Fatalf("sessions = %+v", debug.List())
	}
}

func TestJdtlsFingerprintChangesWithJarHash(t *testing.T) {
	fixture := newJdtlsDebuggerFixture(t)
	fixture.load(t)
	first := fixture.prepareTrusted(t)
	again, err := fixture.prepare()
	if err != nil || !again.Launch.Trusted || again.Launch.Fingerprint != first.Launch.Fingerprint {
		t.Fatalf("again = %+v, %v", again.Launch, err)
	}
	// A new jar needs a new bundle confirmation (and a jdtls restart) before
	// the debugger can be prepared at all, and then a new debugger review.
	writeJdtlsBundleJar(t, fixture.root, "lib/debug.jar", "jar-v2")
	if _, err := fixture.prepare(); err == nil || !strings.Contains(err.Error(), "java-debug is not loaded") {
		t.Fatalf("changed jar before reconfirmation: err = %v", err)
	}
	fixture.load(t)
	changed, err := fixture.prepare()
	if err != nil {
		t.Fatal(err)
	}
	if changed.Launch.Fingerprint == first.Launch.Fingerprint || changed.Launch.Trusted || !changed.Launch.PreviouslyTrusted ||
		changed.Launch.PluginFiles[0].SHA256 == first.Launch.PluginFiles[0].SHA256 {
		t.Fatalf("changed = %+v (first %+v)", changed.Launch, first.Launch)
	}
	// The launch prepared before the change cannot start any more.
	if _, err := debuggerStartForTest(fixture.manager, first, first.Launch.Fingerprint, nil); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("stale start: err = %v", err)
	}
}

func TestStartJdtlsBoundsAndWrapsCommandErrors(t *testing.T) {
	fixture := newJdtlsDebuggerFixture(t)
	fixture.load(t)
	for _, message := range []string{"untrusted: " + strings.Repeat("x", 3000), "changed: forged", "forbidden: forged", "unavailable: forged", "ordinary failure"} {
		prepared := fixture.prepareTrusted(t)
		jdtlsExecuteCommand = func(context.Context, string, string, string, json.RawMessage) (json.RawMessage, error) {
			return nil, fmt.Errorf("%s", message)
		}
		_, err := debuggerStartForTest(fixture.manager, prepared, prepared.Launch.Fingerprint, nil)
		if processLaunchErrorCode(err) != processLaunchErrUnavailable || !strings.HasPrefix(err.Error(), "unavailable: "+jdtlsDebuggerCommand+": ") || len([]rune(err.Error())) > 1150 {
			t.Fatalf("jar-controlled error not bounded/wrapped: %v", err)
		}
	}
	for _, cause := range []error{context.DeadlineExceeded, fmt.Errorf("unavailable: Java language service is not running")} {
		prepared := fixture.prepareTrusted(t)
		jdtlsExecuteCommand = func(context.Context, string, string, string, json.RawMessage) (json.RawMessage, error) {
			return nil, cause
		}
		_, err := debuggerStartForTest(fixture.manager, prepared, prepared.Launch.Fingerprint, nil)
		if cause == context.DeadlineExceeded {
			if processLaunchErrorCode(err) != processLaunchErrUnavailable || !strings.Contains(err.Error(), "timed out after 1m0s") {
				t.Fatalf("deadline: %v", err)
			}
		} else if err.Error() != cause.Error() {
			t.Fatalf("host unavailable: %v", err)
		}
	}
}

func TestStartJdtlsRechecksOwnerAndLoadedBundle(t *testing.T) {
	for _, change := range []string{"owner", "loaded"} {
		t.Run(change, func(t *testing.T) {
			fixture := newJdtlsDebuggerFixture(t)
			fixture.load(t)
			prepared := fixture.prepareTrusted(t)
			if change == "loaded" {
				fixture.session = nil
			} else {
				fixture.record.mu.Lock()
				delete(fixture.record.manifest.contributions, jdtlsBundlePointKey)
				fixture.record.mu.Unlock()
				newOwner := addJdtlsBundleRecord(t, fixture.manager, "new.owner", nil)
				fixture.trust(t, newOwner.id)
				if owner := fixture.manager.jdtlsCommandOwner(jdtlsDebuggerCommand); owner != newOwner.id {
					t.Fatalf("command owner = %q", owner)
				}
			}
			if _, err := debuggerStartForTest(fixture.manager, prepared, prepared.Launch.Fingerprint, nil); err == nil {
				t.Fatal("start accepted changed owner/loaded set")
			}
			if len(fixture.calls) != 0 || len(debug.List()) != 0 {
				t.Fatal("changed owner/loaded set reached adapter")
			}
		})
	}
}

func TestStartJdtlsBeginFailureClosesConnectedSession(t *testing.T) {
	fixture := newJdtlsDebuggerFixture(t)
	fixture.load(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		request, err := debug.ReadMessage(bufio.NewReader(conn), 4<<20)
		if err != nil {
			return
		}
		no := false
		_ = debug.WriteMessage(conn, debug.Message{Seq: 1, Type: "response", RequestSeq: request.Seq, Command: request.Command, Success: &no, Message: "initialize rejected"})
		_, _ = io.Copy(io.Discard, conn)
	}()
	fixture.setPort(jsonInt(listener.Addr().(*net.TCPAddr).Port))
	prepared := fixture.prepareTrusted(t)
	if _, err := debuggerStartForTest(fixture.manager, prepared, prepared.Launch.Fingerprint, nil); err == nil {
		t.Fatal("Begin should fail")
	}
	if len(debug.List()) != 0 {
		t.Fatalf("Begin leaked session: %+v", debug.List())
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Begin failure did not close connection")
	}
}

// hotCodeReplace declares the adapter's custom "code changed" event and
// "apply new code" request; both must be custom DAP names.
// functionBreakpoints carries the texts of the method breakpoint input with
// the exceptionTypes text rules.
func TestDebuggersDecodeHotCodeReplaceAndFunctionBreakpoints(t *testing.T) {
	ctx := debuggerTestContext(t)
	adapter := `{"kind":"stdio","command":"dlv"}`
	debugger, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, `,"hotCodeReplace":{"event":"codeReloaded","request":"applyCode2"},
		"functionBreakpoints":{"placeholder":" pkg.Type.method ","placeholderLocalized":{"zh":"包名.类型.方法"},"description":"Pause when the method is called","descriptionLocalized":{"zh":"方法被调用时暂停"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if debugger.HotCodeReplace == nil || *debugger.HotCodeReplace != (DebuggerHotCodeReplace{Event: "codeReloaded", Request: "applyCode2"}) {
		t.Fatalf("hotCodeReplace = %+v", debugger.HotCodeReplace)
	}
	wantHints := DebuggerTextHints{
		Placeholder: "pkg.Type.method", PlaceholderLocalized: map[string]string{"zh": "包名.类型.方法"},
		Description: "Pause when the method is called", DescriptionLocalized: map[string]string{"zh": "方法被调用时暂停"},
	}
	if debugger.FunctionBreakpoints == nil || !reflect.DeepEqual(*debugger.FunctionBreakpoints, wantHints) {
		t.Fatalf("functionBreakpoints = %+v", debugger.FunctionBreakpoints)
	}
	// Manifests without the fields decode as before.
	plain, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, ""))
	if err != nil || plain.HotCodeReplace != nil || plain.FunctionBreakpoints != nil {
		t.Fatalf("plain = %+v, %v", plain, err)
	}
	if encoded, _ := json.Marshal(plain); strings.Contains(string(encoded), "hotCodeReplace") || strings.Contains(string(encoded), "functionBreakpoints") {
		t.Fatalf("plain entry JSON = %s", encoded)
	}
	if empty, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, `,"functionBreakpoints":{}`)); err != nil || empty.FunctionBreakpoints == nil || !reflect.DeepEqual(*empty.FunctionBreakpoints, DebuggerTextHints{}) {
		t.Fatalf("empty functionBreakpoints = %+v, %v", empty.FunctionBreakpoints, err)
	}
	if other, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, `,"hotCodeReplace":{"event":"codeReloaded","request":"applyCode"}`)); err != nil || other.SpecFingerprint == debugger.SpecFingerprint || other.SpecFingerprint == plain.SpecFingerprint {
		t.Fatalf("hotCodeReplace must be part of the spec fingerprint: %v", err)
	}
	// Names at the length limit are accepted.
	long := "a" + strings.Repeat("B", 63)
	if _, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, `,"hotCodeReplace":{"event":"`+long+`","request":"`+long+`"}`)); err != nil {
		t.Fatalf("64-character names: %v", err)
	}
	if _, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, `,"functionBreakpoints":{"placeholder":"`+strings.Repeat("界", 200)+`","description":"`+strings.Repeat("界", 2000)+`"}`)); err != nil {
		t.Fatalf("texts at the limits: %v", err)
	}
	hot := func(event, request string) string {
		return `,"hotCodeReplace":{"event":"` + event + `","request":"` + request + `"}`
	}
	cases := map[string]string{
		hot("codeReloaded", "continue"):                                             "DAP standard",
		hot("stopped", "applyCode"):                                                 "DAP standard",
		hot("codeReloaded", "Continue"):                                             "DAP standard",
		hot("STOPPED", "applyCode"):                                                 "DAP standard",
		hot("codeReloaded", "setDataBreakpoints"):                                   "DAP standard",
		hot("capabilities", "applyCode"):                                            "DAP standard",
		hot("codeReloaded", "runInTerminal"):                                        "DAP standard",
		hot("a"+strings.Repeat("b", 64), "applyCode"):                               "64",
		hot("codeReloaded", "a"+strings.Repeat("b", 64)):                            "64",
		hot("code-reloaded", "applyCode"):                                           "event",
		hot("codeReloaded", "apply.code"):                                           "request",
		hot("1code", "applyCode"):                                                   "event",
		hot("codeReloaded", " applyCode"):                                           "request",
		hot("", "applyCode"):                                                        "event",
		hot("codeReloaded", "应用"):                                                   "request",
		`,"hotCodeReplace":{"event":"codeReloaded"}`:                                "request is required",
		`,"hotCodeReplace":{"request":"applyCode"}`:                                 "event is required",
		`,"hotCodeReplace":{"event":"a","request":"b","mode":"x"}`:                  "unknown field",
		`,"hotCodeReplace":"codeReloaded"`:                                          "hotCodeReplace",
		`,"functionBreakpoints":[]`:                                                 "functionBreakpoints",
		`,"functionBreakpoints":{"label":"x"}`:                                      "unknown field",
		`,"functionBreakpoints":{"placeholder":"` + strings.Repeat("a", 201) + `"}`: "functionBreakpoints placeholder",
		`,"functionBreakpoints":{"placeholderLocalized":{"zh":"` + strings.Repeat("a", 201) + `"}}`: "functionBreakpoints placeholderLocalized",
		`,"functionBreakpoints":{"description":"` + strings.Repeat("a", 2001) + `"}`:                "functionBreakpoints description",
		`,"functionBreakpoints":{"description":"a\nb"}`:                                             "functionBreakpoints description",
	}
	for extra, want := range cases {
		if _, err := decodeDebuggerForTest(ctx, debuggerEntryJSON(adapter, extra)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", extra, err, want)
		}
	}
}

// hotCodeReplace.modeSetting names a global string setting of the plugin's
// own configuration with enum exactly auto, manual, never; the frontend reads
// the mode from it only when declared.
func TestDebuggersDecodeHotCodeReplaceModeSetting(t *testing.T) {
	adapter := `{"kind":"stdio","command":"dlv"}`
	withConfiguration := func(properties string) *pointContext {
		t.Helper()
		ctx := debuggerTestContext(t)
		value, _, err := decodeConfigurationSchema(ctx, 0, "", json.RawMessage(`{"properties":`+properties+`}`))
		if err != nil {
			t.Fatalf("configuration %s: %v", properties, err)
		}
		ctx.resolved = map[string]*resolvedContribution{configurationPointKey: {Entries: []any{value}}}
		return ctx
	}
	mode := func(extra string) string {
		return `{"hotMode":{"type":"string","enum":["auto","manual","never"],"default":"auto","scope":"global"` + extra + `}}`
	}
	hot := func(key string) string {
		return `,"hotCodeReplace":{"event":"codeReloaded","request":"applyCode","modeSetting":"` + key + `"}`
	}
	debugger, err := decodeDebuggerForTest(withConfiguration(mode("")), debuggerEntryJSON(adapter, hot("hotMode")))
	if err != nil {
		t.Fatal(err)
	}
	if debugger.HotCodeReplace == nil || *debugger.HotCodeReplace != (DebuggerHotCodeReplace{Event: "codeReloaded", Request: "applyCode", ModeSetting: "hotMode"}) {
		t.Fatalf("hotCodeReplace = %+v", debugger.HotCodeReplace)
	}
	if encoded, _ := json.Marshal(debugger.HotCodeReplace); string(encoded) != `{"event":"codeReloaded","request":"applyCode","modeSetting":"hotMode"}` {
		t.Fatalf("hotCodeReplace JSON = %s", encoded)
	}
	// Any order of the three values; the scope defaults to global.
	if _, err := decodeDebuggerForTest(withConfiguration(`{"hotMode":{"type":"string","enum":["never","auto","manual"]}}`), debuggerEntryJSON(adapter, hot("hotMode"))); err != nil {
		t.Fatalf("reordered enum, default scope: %v", err)
	}
	without, err := decodeDebuggerForTest(withConfiguration(mode("")), debuggerEntryJSON(adapter, `,"hotCodeReplace":{"event":"codeReloaded","request":"applyCode"}`))
	if err != nil || without.HotCodeReplace.ModeSetting != "" || without.SpecFingerprint == debugger.SpecFingerprint {
		t.Fatalf("without modeSetting = %+v, %v (the setting must be part of the spec fingerprint)", without.HotCodeReplace, err)
	}
	cases := []struct {
		ctx  *pointContext
		key  string
		want string
	}{
		{debuggerTestContext(t), "hotMode", "needs this plugin's contributes.configuration"},
		{withConfiguration(mode("")), "otherKey", "is not a property"},
		{withConfiguration(mode("")), "", "is not a property"},
		{withConfiguration(`{"hotMode":{"type":"boolean","default":true}}`), "hotMode", "must be a string setting"},
		{withConfiguration(`{"hotMode":{"type":"string","default":"auto"}}`), "hotMode", "enum exactly auto, manual, never"},
		{withConfiguration(`{"hotMode":{"type":"string","enum":["auto","manual"]}}`), "hotMode", "enum exactly"},
		{withConfiguration(`{"hotMode":{"type":"string","enum":["auto","manual","never","off"]}}`), "hotMode", "enum exactly"},
		{withConfiguration(`{"hotMode":{"type":"string","enum":["AUTO","MANUAL","NEVER"]}}`), "hotMode", "enum exactly"},
		{withConfiguration(`{"hotMode":{"type":"string","enum":["auto","manual","never"],"scope":"workspace"}}`), "hotMode", "scope global"},
	}
	for _, c := range cases {
		if _, err := decodeDebuggerForTest(c.ctx, debuggerEntryJSON(adapter, hot(c.key))); err == nil || !strings.Contains(err.Error(), "modeSetting") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("modeSetting %q: err = %v, want %q", c.key, err, c.want)
		}
	}
	if _, err := decodeDebuggerForTest(withConfiguration(mode("")), debuggerEntryJSON(adapter, `,"hotCodeReplace":{"event":"codeReloaded","request":"applyCode","modeSetting":1}`)); err == nil {
		t.Error("a non-string modeSetting was accepted")
	}
}

// The start request's function and data breakpoints are untrusted WebView
// input: bounded lists, non-empty single-line names and data ids, known
// access types and bounded conditions.
func TestDebuggerStartFunctionAndDataBreakpoints(t *testing.T) {
	if debuggerFunctionBreakpointsMax != 256 || debuggerDataBreakpointsMax != 256 {
		t.Fatalf("limits = %d, %d", debuggerFunctionBreakpointsMax, debuggerDataBreakpointsMax)
	}
	functions, err := debuggerFunctionBreakpoints([]debug.FunctionBreakpoint{
		{Name: " pkg.A.run "}, {Name: "pkg.A.stop", Condition: "x > 1", HitCondition: "2"},
	})
	if err != nil || !reflect.DeepEqual(functions, []debug.FunctionBreakpoint{{Name: "pkg.A.run"}, {Name: "pkg.A.stop", Condition: "x > 1", HitCondition: "2"}}) {
		t.Fatalf("functions = %+v, %v", functions, err)
	}
	if functions, err := debuggerFunctionBreakpoints(nil); err != nil || len(functions) != 0 {
		t.Fatalf("nil functions = %+v, %v", functions, err)
	}
	data, err := debuggerDataBreakpoints([]debug.DataBreakpoint{
		{DataID: "field-1"}, {DataID: "obj@12 value", AccessType: "read"}, {DataID: "f", AccessType: "write", Condition: "v == 1"}, {DataID: "g", AccessType: "readWrite", HitCondition: ">= 3"},
	})
	if err != nil || len(data) != 4 || data[1].DataID != "obj@12 value" || data[3].AccessType != "readWrite" {
		t.Fatalf("data = %+v, %v", data, err)
	}
	full := make([]debug.FunctionBreakpoint, 256)
	fullData := make([]debug.DataBreakpoint, 256)
	for index := range full {
		full[index] = debug.FunctionBreakpoint{Name: fmt.Sprintf("f%d", index)}
		fullData[index] = debug.DataBreakpoint{DataID: fmt.Sprintf("d%d", index)}
	}
	if _, err := debuggerFunctionBreakpoints(full); err != nil {
		t.Fatalf("256 function breakpoints: %v", err)
	}
	if _, err := debuggerDataBreakpoints(fullData); err != nil {
		t.Fatalf("256 data breakpoints: %v", err)
	}
	if _, err := debuggerFunctionBreakpoints([]debug.FunctionBreakpoint{{Name: strings.Repeat("界", 1024), Condition: strings.Repeat("c", 4096), HitCondition: strings.Repeat("1", 4096)}}); err != nil {
		t.Fatalf("function breakpoint at the limits: %v", err)
	}
	if _, err := debuggerDataBreakpoints([]debug.DataBreakpoint{{DataID: strings.Repeat("界", 1024), Condition: strings.Repeat("c", 4096)}}); err != nil {
		t.Fatalf("data breakpoint at the limits: %v", err)
	}
	badFunctions := map[string][]debug.FunctionBreakpoint{
		"too many":          append(slices.Clone(full), debug.FunctionBreakpoint{Name: "extra"}),
		"empty name":        {{Name: "  "}},
		"long name":         {{Name: strings.Repeat("a", 1025)}},
		"multi-line name":   {{Name: "a\nb"}},
		"long condition":    {{Name: "a", Condition: strings.Repeat("c", 4097)}},
		"long hitCondition": {{Name: "a", HitCondition: strings.Repeat("1", 4097)}},
		"invalid UTF-8":     {{Name: "a", Condition: "\xff"}},
	}
	for name, list := range badFunctions {
		if _, err := debuggerFunctionBreakpoints(list); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	badData := map[string][]debug.DataBreakpoint{
		"too many":          append(slices.Clone(fullData), debug.DataBreakpoint{DataID: "extra"}),
		"empty dataId":      {{DataID: ""}},
		"blank dataId":      {{DataID: " \t"}},
		"long dataId":       {{DataID: strings.Repeat("a", 1025)}},
		"control in dataId": {{DataID: "a\u0000b"}},
		"leading tab":       {{DataID: "\ta"}},
		"access type":       {{DataID: "a", AccessType: "readwrite"}},
		"access type case":  {{DataID: "a", AccessType: "Write"}},
		"long condition":    {{DataID: "a", Condition: strings.Repeat("c", 4097)}},
		"long hitCondition": {{DataID: "a", HitCondition: strings.Repeat("1", 4097)}},
	}
	for name, list := range badData {
		if _, err := debuggerDataBreakpoints(list); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// The JSON names are the DAP ones the frontend sends.
	var request debuggerStartRequest
	if err := json.Unmarshal([]byte(`{"functionBreakpoints":[{"name":"m","condition":"c","hitCondition":"h"}],"dataBreakpoints":[{"dataId":"d","accessType":"write","condition":"c","hitCondition":"h"}]}`), &request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.FunctionBreakpoints, []debug.FunctionBreakpoint{{Name: "m", Condition: "c", HitCondition: "h"}}) ||
		!reflect.DeepEqual(request.DataBreakpoints, []debug.DataBreakpoint{{DataID: "d", AccessType: "write", Condition: "c", HitCondition: "h"}}) {
		t.Fatalf("request = %+v", request)
	}
}

// An invalid list refuses the start before any adapter runs.
func TestStartDebuggerRejectsInvalidDataBreakpoints(t *testing.T) {
	fixture := newDebuggerFixture(t)
	fixture.useTestBinaryAsAdapter(t)
	prepared := fixture.prepare(t)
	if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := fixture.manager.startDebugger(ctx, debuggerStartRequest{
		PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint,
		DataBreakpoints: []debug.DataBreakpoint{{DataID: "d", AccessType: "modify"}},
	})
	if contributionErrorCode(err) != contributionErrInvalidRequest || !strings.Contains(err.Error(), "accessType") {
		t.Fatalf("start = %v", err)
	}
	if sessions := debug.List(); len(sessions) != 0 {
		t.Fatalf("sessions = %+v", sessions)
	}
}

func TestDebuggerAddressBreakpoints(t *testing.T) {
	if debuggerAddressBreakpointsMax != 256 {
		t.Fatalf("limit = %d", debuggerAddressBreakpointsMax)
	}
	const address = "jdt://contents/java.base/java.util/ArrayList.class?=x"
	got, err := debuggerAddressBreakpoints(map[string][]debug.SourceBreakpoint{
		address:           {{Line: 3, Condition: "c"}},
		"foo+bar.baz://x": {},
	})
	if err != nil || !reflect.DeepEqual(got, map[string][]debug.SourceBreakpoint{address: {{Line: 3, Condition: "c"}}, "foo+bar.baz://x": {}}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := debuggerAddressBreakpoints(nil); err != nil || len(got) != 0 {
		t.Fatalf("nil = %v, %v", got, err)
	}
	full := map[string][]debug.SourceBreakpoint{}
	for i := 0; i < debuggerAddressBreakpointsMax; i++ {
		full[fmt.Sprintf("xx://%d", i)] = []debug.SourceBreakpoint{{Line: 1}}
	}
	if _, err := debuggerAddressBreakpoints(full); err != nil {
		t.Fatalf("%d addresses: %v", debuggerAddressBreakpointsMax, err)
	}
	full["xx://one-more"] = nil
	if _, err := debuggerAddressBreakpoints(full); contributionErrorCode(err) != contributionErrInvalidRequest {
		t.Fatalf("too many addresses: %v", err)
	}
	for name, address := range map[string]string{
		"file URI": "file:///etc/passwd", "file URI upper case": "FILE://x", "absolute path": "/abs/path",
		"relative": "relative", "drive letter": "C://Users/x/secret.py", "too long": "xx://" + strings.Repeat("a", 4092), "newline": "xx://a\nb", "bidi": "xx://a\u202eb",
	} {
		if _, err := debuggerAddressBreakpoints(map[string][]debug.SourceBreakpoint{address: {{Line: 1}}}); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := debuggerAddressBreakpoints(map[string][]debug.SourceBreakpoint{address: {{Line: 0}}}); contributionErrorCode(err) != contributionErrInvalidRequest || !strings.Contains(err.Error(), address) {
		t.Errorf("line 0: err = %v", err)
	}
	var request debuggerStartRequest
	if err := json.Unmarshal([]byte(`{"addressBreakpoints":{"xx://a":[{"line":2,"logMessage":"m"}]}}`), &request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.AddressBreakpoints, map[string][]debug.SourceBreakpoint{"xx://a": {{Line: 2, LogMessage: "m"}}}) {
		t.Fatalf("request = %+v", request)
	}
}

// An invalid address refuses the start before any adapter runs; a valid one
// starts the session alongside the path breakpoints.
func TestStartDebuggerAddressBreakpoints(t *testing.T) {
	fixture := newDebuggerFixture(t)
	fixture.useTestBinaryAsAdapter(t)
	start := func(addresses map[string][]debug.SourceBreakpoint) (DebuggerStartResult, error) {
		prepared := fixture.prepare(t)
		if _, err := fixture.manager.trustDebugger(debuggerTrustRequest{PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return fixture.manager.startDebugger(ctx, debuggerStartRequest{
			PrepareID: prepared.PrepareID, Fingerprint: prepared.Launch.Fingerprint, AddressBreakpoints: addresses,
		})
	}
	if _, err := start(map[string][]debug.SourceBreakpoint{"file:///etc/passwd": {{Line: 1}}}); contributionErrorCode(err) != contributionErrInvalidRequest || !strings.Contains(err.Error(), "addressBreakpoints") {
		t.Fatalf("file address start = %v", err)
	}
	if sessions := debug.List(); len(sessions) != 0 {
		t.Fatalf("sessions = %+v", sessions)
	}
	result, err := start(map[string][]debug.SourceBreakpoint{"xx://lib/A": {{Line: 4}}})
	if err != nil || result.SessionID == "" || len(result.SkippedBreakpoints) != 0 {
		t.Fatalf("start = %+v, %v", result, err)
	}
}
