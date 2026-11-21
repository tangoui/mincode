package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

// jdtlsExecuteCall records one call that reached lsp.ExecuteBuiltinCommand.
type jdtlsExecuteCall struct {
	root, serverID, command string
	args                    json.RawMessage
	deadline                time.Duration
}

type jdtlsExecuteFixture struct {
	*jdtlsBundleFixture
	calls  []jdtlsExecuteCall
	result json.RawMessage
	err    error
}

func newJdtlsExecuteFixture(t *testing.T) *jdtlsExecuteFixture {
	t.Helper()
	fixture := &jdtlsExecuteFixture{jdtlsBundleFixture: newJdtlsBundleFixture(t), result: json.RawMessage(`{"ok":true}`)}
	previous := jdtlsExecuteCommand
	jdtlsExecuteCommand = func(ctx context.Context, root, serverID, command string, args json.RawMessage) (json.RawMessage, error) {
		call := jdtlsExecuteCall{root: root, serverID: serverID, command: command, args: args}
		if deadline, ok := ctx.Deadline(); ok {
			call.deadline = time.Until(deadline)
		}
		fixture.calls = append(fixture.calls, call)
		return fixture.result, fixture.err
	}
	t.Cleanup(func() { jdtlsExecuteCommand = previous })
	return fixture
}

// load confirms the fixture plugin's jars and starts a fake jdtls with them.
func (fixture *jdtlsExecuteFixture) load(t *testing.T) {
	t.Helper()
	fixture.trust(t, jdtlsBundleTestPlugin)
	fixture.running, fixture.session = true, fixture.manager.jdtlsBundlePaths()
}

func jdtlsTestRuntime(manager *pluginManager, record *pluginRecord, capabilities ...api.Capability) *processRuntime {
	granted := map[api.Capability]struct{}{}
	for _, capability := range capabilities {
		granted[capability] = struct{}{}
	}
	runContext, cancel := context.WithCancel(context.Background())
	runtimeHost := &processRuntime{
		pluginID: record.id, root: record.root, manifest: *record.manifest, capabilities: granted,
		ownerManager: manager, ownerRecord: record,
		generation: 7, cmd: &exec.Cmd{}, runContext: runContext, runCancel: cancel,
	}
	record.mu.Lock()
	record.runtime = runtimeHost
	record.mu.Unlock()
	return runtimeHost
}

func (fixture *jdtlsExecuteFixture) runtime() *processRuntime {
	return jdtlsTestRuntime(fixture.manager, fixture.record, api.CapabilityShell)
}

func callJdtlsExecute(t *testing.T, runtimeHost *processRuntime, params any) (json.RawMessage, *rpcError) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var result any
	var rpcErr *rpcError
	answered := false
	runtimeHost.mu.Lock()
	generation := runtimeHost.generation
	runtimeHost.mu.Unlock()
	if !runtimeHost.serveRegisteredHostMethod(rpcEnvelope{Method: jdtlsExecuteCommandMethod, Params: raw, hostGeneration: generation}, func(value any, failure *rpcError) {
		answered, result, rpcErr = true, value, failure
	}) || !answered {
		t.Fatalf("%s is not registered", jdtlsExecuteCommandMethod)
	}
	if rpcErr != nil {
		return nil, rpcErr
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return encoded, nil
}

func wantJdtlsRPCError(t *testing.T, rpcErr *rpcError, code int, message string) {
	t.Helper()
	if rpcErr == nil || rpcErr.Code != code || !strings.Contains(rpcErr.Message, message) {
		t.Fatalf("error = %+v, want %d %q", rpcErr, code, message)
	}
}

func (fixture *jdtlsExecuteFixture) params(command string, args ...any) map[string]any {
	params := map[string]any{"root": fixture.project, "command": command}
	if args != nil {
		params["arguments"] = args
	}
	return params
}

func TestExecuteCommandRejectsUndeclared(t *testing.T) {
	fixture := newJdtlsExecuteFixture(t)
	fixture.load(t)
	runtimeHost := fixture.runtime()

	_, rpcErr := callJdtlsExecute(t, runtimeHost, fixture.params("vscode.java.other"))
	wantJdtlsRPCError(t, rpcErr, -32001, "command is not declared by this plugin")
	_, rpcErr = callJdtlsExecute(t, runtimeHost, fixture.params("bad command"))
	wantJdtlsRPCError(t, rpcErr, -32001, "command is not declared by this plugin")

	// Invalid parameters.
	declared := jdtlsBundleTestCommands[0]
	for name, params := range map[string]any{
		"relative root":  map[string]any{"root": "project", "command": declared},
		"empty root":     map[string]any{"root": "", "command": declared},
		"missing root":   map[string]any{"command": declared},
		"empty command":  map[string]any{"root": fixture.project, "command": ""},
		"object args":    map[string]any{"root": fixture.project, "command": declared, "arguments": map[string]any{}},
		"unknown field":  map[string]any{"root": fixture.project, "command": declared, "extra": 1},
		"missing folder": map[string]any{"root": filepath.Join(fixture.project, "missing"), "command": declared},
		"not an object":  []string{fixture.project},
	} {
		_, rpcErr := callJdtlsExecute(t, runtimeHost, params)
		if rpcErr == nil || rpcErr.Code != -32602 {
			t.Fatalf("%s: error = %+v, want -32602", name, rpcErr)
		}
	}

	// The shell capability is required; plugins without jdtlsBundles see no method.
	_, rpcErr = callJdtlsExecute(t, jdtlsTestRuntime(fixture.manager, fixture.record), fixture.params(declared))
	wantJdtlsRPCError(t, rpcErr, -32001, "shell capability not granted")
	plain := &processRuntime{pluginID: "plain.plugin", manifest: Manifest{ID: "plain.plugin", APIVersion: 2}, capabilities: map[api.Capability]struct{}{api.CapabilityShell: {}}, ownerManager: fixture.manager}
	_, rpcErr = callJdtlsExecute(t, plain, fixture.params(declared))
	wantJdtlsRPCError(t, rpcErr, -32601, "host method not found")

	// A command the caller declares in an entry that an earlier plugin's
	// ownership rejected is not routed to the caller.
	later := []any{
		map[string]any{"path": "${pluginRoot}/lib/debug.jar", "commands": []string{"later.only", declared}},
		map[string]any{"path": "${pluginRoot}/lib/extra.jar", "commands": []string{"later.extra"}},
	}
	laterRecord := addJdtlsBundleRecord(t, fixture.manager, "jdtls.later", later)
	fixture.trust(t, "jdtls.later")
	fixture.session = fixture.manager.jdtlsBundlePaths()
	laterRuntime := jdtlsTestRuntime(fixture.manager, laterRecord, api.CapabilityShell)
	for _, command := range []string{declared, "later.only"} {
		_, rpcErr = callJdtlsExecute(t, laterRuntime, fixture.params(command))
		wantJdtlsRPCError(t, rpcErr, -32001, "not available to this plugin")
	}
	if _, rpcErr = callJdtlsExecute(t, laterRuntime, fixture.params("later.extra")); rpcErr != nil {
		t.Fatalf("own accepted command: %+v", rpcErr)
	}

	if len(fixture.calls) != 1 || fixture.calls[0].command != "later.extra" {
		t.Fatalf("calls = %+v", fixture.calls)
	}
}

func TestExecuteCommandRequiresLoadedBundle(t *testing.T) {
	fixture := newJdtlsExecuteFixture(t)
	runtimeHost := fixture.runtime()
	declared := jdtlsBundleTestCommands[0]
	// An unconfirmed plugin's commands are refused, even with jdtls running.
	fixture.running = true
	_, rpcErr := callJdtlsExecute(t, runtimeHost, fixture.params(declared))
	wantJdtlsRPCError(t, rpcErr, -32001, "not available to this plugin")
	fixture.running = false
	fixture.trust(t, jdtlsBundleTestPlugin)

	// jdtls not running.
	_, rpcErr = callJdtlsExecute(t, runtimeHost, fixture.params(declared))
	wantJdtlsRPCError(t, rpcErr, -32000, "unavailable: Java language service is not running")
	// Running, but started before the jars were confirmed.
	fixture.running, fixture.session = true, nil
	_, rpcErr = callJdtlsExecute(t, runtimeHost, fixture.params(declared))
	wantJdtlsRPCError(t, rpcErr, -32000, "unavailable: restart the Java language service to load Jdtls fixture")
	// Running with another plugin's jar only.
	fixture.session = []string{filepath.Join(t.TempDir(), "other.jar")}
	_, rpcErr = callJdtlsExecute(t, runtimeHost, fixture.params(declared))
	wantJdtlsRPCError(t, rpcErr, -32000, "unavailable: restart the Java language service to load")
	if len(fixture.calls) != 0 {
		t.Fatalf("calls before the bundle was loaded = %+v", fixture.calls)
	}
	fixture.session = fixture.manager.jdtlsBundlePaths()
	if _, rpcErr = callJdtlsExecute(t, runtimeHost, fixture.params(declared)); rpcErr != nil {
		t.Fatalf("loaded bundle: %+v", rpcErr)
	}
}

func TestExecuteCommandForwards(t *testing.T) {
	fixture := newJdtlsExecuteFixture(t)
	fixture.load(t)
	runtimeHost := fixture.runtime()
	fixture.result = json.RawMessage(`{"classpaths":["/a","/b"]}`)
	result, rpcErr := callJdtlsExecute(t, runtimeHost, fixture.params(jdtlsBundleTestCommands[1], "com.example.Main", map[string]any{"scope": "test"}))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if string(result) != `{"classpaths":["/a","/b"]}` {
		t.Fatalf("result = %s", result)
	}
	canonical, err := filepath.EvalSymlinks(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.calls) != 1 {
		t.Fatalf("calls = %+v", fixture.calls)
	}
	call := fixture.calls[0]
	if call.root != canonical || call.serverID != "jdtls" || call.command != jdtlsBundleTestCommands[1] ||
		string(call.args) != `["com.example.Main",{"scope":"test"}]` {
		t.Fatalf("call = %+v (root want %s)", call, canonical)
	}
	if call.deadline <= 55*time.Second || call.deadline > 60*time.Second {
		t.Fatalf("deadline = %v, want 60 s", call.deadline)
	}
	// Omitted and null arguments reach lsp as no arguments; null results stay null.
	fixture.result = nil
	for _, params := range []map[string]any{fixture.params(jdtlsBundleTestCommands[0]), {"root": fixture.project, "command": jdtlsBundleTestCommands[0], "arguments": nil}} {
		result, rpcErr := callJdtlsExecute(t, runtimeHost, params)
		if rpcErr != nil || string(result) != "null" {
			t.Fatalf("result = %s, %+v", result, rpcErr)
		}
		if args := fixture.calls[len(fixture.calls)-1].args; len(args) != 0 {
			t.Fatalf("args = %s", args)
		}
	}
	// Errors from the language service.
	for _, test := range []struct {
		err     error
		code    int
		message string
	}{
		{context.DeadlineExceeded, -32603, "timed out"},
		{errors.New("unavailable: Java language service is not running"), -32000, "unavailable: Java language service is not running"},
		{errors.New("invalid_request: command arguments exceed size limit"), -32602, "size limit"},
		{errors.New("No delegateCommandHandler for vscode.java.resolveClasspath"), -32000, "No delegateCommandHandler"},
	} {
		fixture.err = test.err
		_, rpcErr := callJdtlsExecute(t, runtimeHost, fixture.params(jdtlsBundleTestCommands[1]))
		wantJdtlsRPCError(t, rpcErr, test.code, test.message)
	}
}

func TestExecuteCommandRefusedAfterDisable(t *testing.T) {
	fixture := newJdtlsExecuteFixture(t)
	fixture.load(t)
	runtimeHost := fixture.runtime()
	declared := jdtlsBundleTestCommands[0]
	if _, rpcErr := callJdtlsExecute(t, runtimeHost, fixture.params(declared)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	fixture.record.mu.Lock()
	fixture.record.enabled = false
	fixture.record.mu.Unlock()
	_, rpcErr := callJdtlsExecute(t, runtimeHost, fixture.params(declared))
	wantJdtlsRPCError(t, rpcErr, -32001, "disabled")
	// The record gate runs before the root is looked at.
	_, rpcErr = callJdtlsExecute(t, runtimeHost, map[string]any{"root": "relative", "command": declared})
	wantJdtlsRPCError(t, rpcErr, -32001, "disabled")
	if len(fixture.calls) != 1 {
		t.Fatalf("calls after disable = %d", len(fixture.calls))
	}
}

func TestExecuteCommandResultCap(t *testing.T) {
	fixture := newJdtlsExecuteFixture(t)
	fixture.load(t)
	runtimeHost := fixture.runtime()
	fixture.result = json.RawMessage(`"` + strings.Repeat("a", 1<<20) + `"`)
	_, rpcErr := callJdtlsExecute(t, runtimeHost, fixture.params(jdtlsBundleTestCommands[0]))
	wantJdtlsRPCError(t, rpcErr, -32603, "1 MiB")
	// A result at the limit fits the reply line.
	fixture.result = json.RawMessage(`"` + strings.Repeat("a", jdtlsCommandResultMaxBytes-2) + `"`)
	result, rpcErr := callJdtlsExecute(t, runtimeHost, fixture.params(jdtlsBundleTestCommands[0]))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	reply, _ := json.Marshal(hostReply(json.RawMessage(`"host-123456789"`), json.RawMessage(result), nil))
	if len(reply) >= pluginRPCMaxMessage {
		t.Fatalf("reply = %d bytes, limit %d", len(reply), pluginRPCMaxMessage)
	}
	// The reply escapes <, >, & (6 bytes each): a raw result well under the
	// cap whose encoding is not is refused with -32603, not -32005 later.
	fixture.result = json.RawMessage(`"` + strings.Repeat("<", jdtlsCommandResultMaxBytes/2) + `"`)
	_, rpcErr = callJdtlsExecute(t, runtimeHost, fixture.params(jdtlsBundleTestCommands[0]))
	wantJdtlsRPCError(t, rpcErr, -32603, "1 MiB")
	// At the cap after escaping it is accepted and the reply still fits.
	fixture.result = json.RawMessage(`"` + strings.Repeat("<", (jdtlsCommandResultMaxBytes-2)/6) + `"`)
	result, rpcErr = callJdtlsExecute(t, runtimeHost, fixture.params(jdtlsBundleTestCommands[0]))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	reply, _ = json.Marshal(hostReply(json.RawMessage(`"host-123456789"`), json.RawMessage(result), nil))
	if len(reply) >= pluginRPCMaxMessage {
		t.Fatalf("escaped reply = %d bytes, limit %d", len(reply), pluginRPCMaxMessage)
	}
	// Results are compacted like the reply.
	fixture.result = json.RawMessage(`{ "a" : [ 1, 2 ] }`)
	if result, rpcErr = callJdtlsExecute(t, runtimeHost, fixture.params(jdtlsBundleTestCommands[0])); rpcErr != nil || string(result) != `{"a":[1,2]}` {
		t.Fatalf("compacted = %s, %+v", result, rpcErr)
	}
}

func TestExecuteCommandRequiresCurrentRuntime(t *testing.T) {
	declared := jdtlsBundleTestCommands[0]
	refused := func(t *testing.T, fixture *jdtlsExecuteFixture, runtimeHost *processRuntime) {
		t.Helper()
		_, rpcErr := callJdtlsExecute(t, runtimeHost, fixture.params(declared))
		wantJdtlsRPCError(t, rpcErr, -32000, "unavailable: this plugin process is no longer running")
		if len(fixture.calls) != 0 {
			t.Fatalf("calls = %+v", fixture.calls)
		}
	}
	t.Run("replaced runtime", func(t *testing.T) {
		fixture := newJdtlsExecuteFixture(t)
		fixture.load(t)
		stale := fixture.runtime()
		fixture.runtime() // the upgraded plugin's runtime replaces it on the record
		refused(t, fixture, stale)
	})
	t.Run("exited process", func(t *testing.T) {
		fixture := newJdtlsExecuteFixture(t)
		fixture.load(t)
		runtimeHost := fixture.runtime()
		runtimeHost.mu.Lock()
		runtimeHost.cmd, runtimeHost.runContext = nil, nil
		runtimeHost.mu.Unlock()
		refused(t, fixture, runtimeHost)
	})
	t.Run("cancelled run context", func(t *testing.T) {
		fixture := newJdtlsExecuteFixture(t)
		fixture.load(t)
		runtimeHost := fixture.runtime()
		runtimeHost.runCancel()
		refused(t, fixture, runtimeHost)
	})
	t.Run("stopping", func(t *testing.T) {
		fixture := newJdtlsExecuteFixture(t)
		fixture.load(t)
		runtimeHost := fixture.runtime()
		runtimeHost.mu.Lock()
		runtimeHost.stopping = true
		runtimeHost.mu.Unlock()
		refused(t, fixture, runtimeHost)
	})
	t.Run("older generation", func(t *testing.T) {
		fixture := newJdtlsExecuteFixture(t)
		fixture.load(t)
		runtimeHost := fixture.runtime()
		raw, _ := json.Marshal(fixture.params(declared))
		var rpcErr *rpcError
		runtimeHost.serveRegisteredHostMethod(rpcEnvelope{Method: jdtlsExecuteCommandMethod, Params: raw, hostGeneration: 6}, func(_ any, failure *rpcError) { rpcErr = failure })
		wantJdtlsRPCError(t, rpcErr, -32000, "no longer running")
		if len(fixture.calls) != 0 {
			t.Fatalf("calls = %+v", fixture.calls)
		}
	})
	t.Run("stop cancels the command", func(t *testing.T) {
		fixture := newJdtlsExecuteFixture(t)
		fixture.load(t)
		runtimeHost := fixture.runtime()
		previous := jdtlsExecuteCommand
		t.Cleanup(func() { jdtlsExecuteCommand = previous })
		jdtlsExecuteCommand = func(ctx context.Context, _, _, _ string, _ json.RawMessage) (json.RawMessage, error) {
			runtimeHost.runCancel()
			<-ctx.Done()
			return nil, ctx.Err()
		}
		_, rpcErr := callJdtlsExecute(t, runtimeHost, fixture.params(declared))
		wantJdtlsRPCError(t, rpcErr, -32000, "stopping")
	})
}
