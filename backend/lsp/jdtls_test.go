package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
)

// fakeServerEnv makes the test binary act as a language server (TestMain).
const fakeServerEnv = "MINCODE_LSP_TEST_FAKE_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeServerEnv) == "jdtls" {
		runFakeJdtls()
		os.Exit(0)
	}
	// Keep initialize independent of the JDKs installed on the machine;
	// jdtls_runtimes_test.go covers discovery.
	discoverJdtlsRuntimes = func() []jdtlsRuntime { return nil }
	os.Exit(m.Run())
}

// runFakeJdtls answers initialize (echoing initializationOptions), reports
// language/status like jdtls 1.63 (Starting after initialize, Started after
// initialized, ServiceReady on the test-only test/importDone notification)
// and echoes workspace/executeCommand.
func runFakeJdtls() {
	reader := bufio.NewReader(os.Stdin)
	for {
		payload, err := readMessage(reader)
		if err != nil {
			return
		}
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(payload, &message) != nil {
			continue
		}
		respond := func(result any) {
			_ = writeMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
		}
		status := func(kind string) {
			_ = writeMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "method": "language/status", "params": map[string]string{"type": kind, "message": kind}})
		}
		switch message.Method {
		case "initialize":
			var params struct {
				InitializationOptions json.RawMessage `json:"initializationOptions"`
			}
			_ = json.Unmarshal(message.Params, &params)
			respond(map[string]any{"capabilities": map[string]any{}, "echo": params.InitializationOptions})
			status("Starting")
		case "initialized":
			status("Started")
		case "test/importDone":
			status("ServiceReady")
		case "workspace/executeCommand":
			respond(map[string]any{"echo": message.Params})
		case "java/classFileContents":
			var params struct {
				URI string `json:"uri"`
			}
			_ = json.Unmarshal(message.Params, &params)
			if strings.Contains(params.URI, "Missing") {
				respond(nil)
			} else {
				respond("// source of " + params.URI)
			}
		case "test/args":
			respond(os.Args[1:])
		case "shutdown":
			respond(nil)
		case "exit":
			return
		}
	}
}

// startFakeJdtls runs the test binary as the built-in jdtls session for root.
func startFakeJdtls(t *testing.T, root string) *session {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeServerEnv, "jdtls")
	primeLookPath(t, "jdtls", executable)
	core.SetEmitter(func(string, any) {})
	t.Cleanup(func() { core.SetEmitter(nil) })
	active, err := start(root, "jdtls")
	if err != nil {
		t.Fatalf("start fake jdtls: %v", err)
	}
	t.Cleanup(func() { _ = stop(root, "jdtls") })
	return active
}

func setBundleProvider(t *testing.T, provider func() []string) {
	t.Helper()
	SetJdtlsBundleProvider(provider)
	t.Cleanup(func() { SetJdtlsBundleProvider(nil) })
}

func initializeBundles(t *testing.T, out json.RawMessage) []string {
	t.Helper()
	var fields struct {
		InitializationOptions struct {
			Bundles []string `json:"bundles"`
		} `json:"initializationOptions"`
	}
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("initialize params: %v (%s)", err, out)
	}
	return fields.InitializationOptions.Bundles
}

// Ruling R3: only provider (user-confirmed) jars reach jdtls; client-supplied
// bundles are dropped and the drop is logged without paths.
func TestJdtlsInitializeSendsOnlyProviderBundles(t *testing.T) {
	setBundleProvider(t, func() []string { return []string{"/b.jar", "/a.jar", "/a.jar", ""} })
	s := &session{root: t.TempDir(), definition: serverDefinition{ID: "jdtls"}}
	out, err := s.prepareInitialize(json.RawMessage(`{"processId":7,"initializationOptions":{"bundles":["/c.jar","/b.jar"],"settings":{"java":{"home":"/jdk"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/a.jar", "/b.jar"}
	if got := initializeBundles(t, out); !reflect.DeepEqual(got, want) {
		t.Fatalf("bundles = %v, want %v", got, want)
	}
	if strings.Contains(string(out), "/c.jar") {
		t.Fatalf("client bundle reached jdtls: %s", out)
	}
	if !strings.Contains(string(out), `"processId":7`) || !strings.Contains(string(out), `"home":"/jdk"`) {
		t.Fatalf("other initialize fields lost: %s", out)
	}
	if got := s.loadedBundles(); !reflect.DeepEqual(got, want) {
		t.Fatalf("session bundles = %v, want %v", got, want)
	}
	if logs := s.logs(); !strings.Contains(logs, "client-supplied bundles dropped") || strings.Contains(logs, ".jar") {
		t.Fatalf("drop not logged or paths leaked: %s", logs)
	}
	// No client options at all.
	s = &session{root: t.TempDir(), definition: serverDefinition{ID: "jdtls"}}
	out, err = s.prepareInitialize(json.RawMessage(`{"processId":7}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := initializeBundles(t, out); !reflect.DeepEqual(got, want) {
		t.Fatalf("bundles = %v", got)
	}
	// Malformed client bundles are dropped like any other client bundles.
	s = &session{root: t.TempDir(), definition: serverDefinition{ID: "jdtls"}}
	out, err = s.prepareInitialize(json.RawMessage(`{"initializationOptions":{"bundles":"/x.jar"}}`))
	if err != nil || strings.Contains(string(out), "/x.jar") || !reflect.DeepEqual(initializeBundles(t, out), want) {
		t.Fatalf("malformed client bundles: %s %v", out, err)
	}
}

func TestJdtlsInitializeWithoutProviderUnchanged(t *testing.T) {
	SetJdtlsBundleProvider(nil)
	s := &session{root: t.TempDir(), definition: serverDefinition{ID: "jdtls"}}
	params := json.RawMessage(`{"processId":7,"initializationOptions":{"settings":{"java":{}}}}`)
	out, err := s.prepareInitialize(params)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, params) {
		t.Fatalf("initialize changed without a provider: %s", out)
	}
	if got := s.loadedBundles(); len(got) != 0 {
		t.Fatalf("session bundles = %v", got)
	}
	// Even without a provider, client bundles never reach jdtls.
	out, err = s.prepareInitialize(json.RawMessage(`{"processId":7,"initializationOptions":{"bundles":["/c.jar"],"settings":{}}}`))
	if err != nil || strings.Contains(string(out), "bundles") || !strings.Contains(string(out), `"settings":{}`) {
		t.Fatalf("client bundles kept without a provider: %s %v", out, err)
	}
	if got := s.loadedBundles(); len(got) != 0 {
		t.Fatalf("session bundles = %v", got)
	}
}

func TestOtherServersUnaffected(t *testing.T) {
	setBundleProvider(t, func() []string { return []string{"/a.jar"} })
	params := json.RawMessage(`{"processId":7,"initializationOptions":{"bundles":["/c.jar"]}}`)
	for _, id := range []string{"gopls", "rust-analyzer", "clangd", "pyright"} {
		s := &session{root: t.TempDir(), definition: serverDefinition{ID: id}}
		out, err := s.prepareInitialize(params)
		if err != nil || !bytes.Equal(out, params) {
			t.Fatalf("%s initialize changed: %s %v", id, out, err)
		}
		if s.loadedBundles() != nil {
			t.Fatalf("%s recorded bundles", id)
		}
	}
	// A plugin server whose spec happens to reuse the id is still untouched.
	s := &session{root: t.TempDir(), definition: serverDefinition{ID: "jdtls"}, external: &ExternalServerSpec{ServerID: "plugin:acme.java/jdtls"}}
	if out, err := s.prepareInitialize(params); err != nil || !bytes.Equal(out, params) {
		t.Fatalf("external initialize changed: %s %v", out, err)
	}
	// ts-ls keeps its own rewrite and gains no bundles.
	root := t.TempDir()
	writeTypeScriptLib(t, root)
	s = &session{root: root, definition: serverDefinition{ID: "ts-ls"}}
	out, err := s.prepareInitialize(params)
	if err != nil {
		t.Fatal(err)
	}
	if got := initializeBundles(t, out); !reflect.DeepEqual(got, []string{"/c.jar"}) {
		t.Fatalf("ts-ls bundles = %v", got)
	}
}

func TestExecuteBuiltinCommandUnavailableWhenNotRunning(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	_, err := ExecuteBuiltinCommand(ctx, root, "jdtls", "vscode.java.startDebugSession", nil)
	if err == nil || err.Error() != "unavailable: Java language service is not running" {
		t.Fatalf("err = %v", err)
	}
	if ready, running := JdtlsReady(root); ready || running {
		t.Fatalf("ready=%v running=%v for a stopped server", ready, running)
	}
	if status := JdtlsStatus(root); status != "" {
		t.Fatalf("status = %q for a stopped server", status)
	}
	if bundles, ok := JdtlsSessionBundles(root); ok || bundles != nil {
		t.Fatalf("bundles = %v ok=%v", bundles, ok)
	}
	for _, id := range []string{"plugin:acme.java/jdtls", "no-such-server", "gopls", "ts-ls", ""} {
		if _, err := ExecuteBuiltinCommand(ctx, root, id, "x", nil); err == nil || !strings.HasPrefix(err.Error(), "invalid_request:") {
			t.Fatalf("server %q: err = %v", id, err)
		}
	}
	if _, err := ExecuteBuiltinCommand(ctx, root, "jdtls", "", nil); err == nil || !strings.HasPrefix(err.Error(), "invalid_request:") {
		t.Fatalf("empty command: %v", err)
	}
	if _, err := ExecuteBuiltinCommand(ctx, root, "jdtls", "x", json.RawMessage(`{"a":1}`)); err == nil || !strings.HasPrefix(err.Error(), "invalid_request:") {
		t.Fatalf("non-array arguments: %v", err)
	}
}

func TestExecuteBuiltinCommandSendsWorkspaceExecuteCommand(t *testing.T) {
	setBundleProvider(t, func() []string { return []string{"/plugins/java-debug/a.jar"} })
	root := t.TempDir()
	active := startFakeJdtls(t, root)
	if bundles, ok := JdtlsSessionBundles(root); !ok || len(bundles) != 0 {
		t.Fatalf("before initialize: bundles=%v ok=%v", bundles, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := active.request(ctx, "initialize", json.RawMessage(`{"processId":null,"initializationOptions":{"bundles":["/client.jar"]}}`))
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	want := []string{"/plugins/java-debug/a.jar"}
	var initialized struct {
		Echo struct {
			Bundles []string `json:"bundles"`
		} `json:"echo"`
	}
	if err := json.Unmarshal(result, &initialized); err != nil || !reflect.DeepEqual(initialized.Echo.Bundles, want) {
		t.Fatalf("server received bundles %s (%v)", result, err)
	}
	if bundles, ok := JdtlsSessionBundles(root); !ok || !reflect.DeepEqual(bundles, want) {
		t.Fatalf("session bundles = %v ok=%v", bundles, ok)
	}
	// A ctx that is already done sends nothing.
	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := ExecuteBuiltinCommand(cancelled, root, "jdtls", "vscode.java.startDebugSession", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled ctx: %v", err)
	}
	if strings.Contains(active.logs(), "workspace/executeCommand") {
		t.Fatalf("pre-cancelled command was written:\n%s", active.logs())
	}
	// Caller deadlines beyond the 30 s request cap are honoured as given.
	long, cancelLong := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelLong()
	result, err = ExecuteBuiltinCommand(long, root, "jdtls", "vscode.java.startDebugSession", json.RawMessage(`[{"x":1}]`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var echoed struct {
		Echo struct {
			Command   string          `json:"command"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"echo"`
	}
	if err := json.Unmarshal(result, &echoed); err != nil || echoed.Echo.Command != "vscode.java.startDebugSession" || string(echoed.Echo.Arguments) != `[{"x":1}]` {
		t.Fatalf("echo = %s (%v)", result, err)
	}
	// Absent arguments are sent as an empty list.
	result, err = ExecuteBuiltinCommand(long, root, "jdtls", "java.project.getAll", nil)
	if err != nil || !strings.Contains(string(result), `"arguments":[]`) {
		t.Fatalf("no-argument execute = %s (%v)", result, err)
	}
	if err := stop(root, "jdtls"); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteBuiltinCommand(long, root, "jdtls", "x", nil); err == nil || !strings.HasPrefix(err.Error(), "unavailable:") {
		t.Fatalf("stopped session reachable: %v", err)
	}
	if _, ok := JdtlsSessionBundles(root); ok {
		t.Fatal("stopped session still reports bundles")
	}
}

func TestJdtlsReadyFromLanguageStatus(t *testing.T) {
	root := t.TempDir()
	active := startFakeJdtls(t, root)
	if ready, running := JdtlsReady(root); ready || !running {
		t.Fatalf("before initialize: ready=%v running=%v", ready, running)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := active.request(ctx, "initialize", json.RawMessage(`{"processId":null}`)); err != nil {
		t.Fatal(err)
	}
	if ready, _ := JdtlsReady(root); ready {
		t.Fatal("Starting must not count as ready")
	}
	waitStatus := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for JdtlsStatus(root) != want {
			if time.Now().After(deadline) {
				t.Fatalf("status = %q, want %q", JdtlsStatus(root), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitStatus("Starting")
	if err := active.notify("initialized", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	waitStatus("Started")
	if ready, running := JdtlsReady(root); ready || !running {
		t.Fatalf("Started (project still importing): ready=%v running=%v", ready, running)
	}
	if err := active.notify("test/importDone", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	waitStatus("ServiceReady")
	if ready, running := JdtlsReady(root); !ready || !running {
		t.Fatalf("ServiceReady: ready=%v running=%v", ready, running)
	}
	_ = stop(root, "jdtls")
	if ready, running := JdtlsReady(root); ready || running || JdtlsStatus(root) != "" {
		t.Fatalf("after stop: ready=%v running=%v status=%q", ready, running, JdtlsStatus(root))
	}
}

func TestJdtlsReadyStatusTypes(t *testing.T) {
	core.SetEmitter(func(string, any) {})
	t.Cleanup(func() { core.SetEmitter(nil) })
	feed := func(id string, kinds ...string) (bool, string) {
		var buf bytes.Buffer
		for _, kind := range kinds {
			_ = writeMessage(&buf, map[string]any{"jsonrpc": "2.0", "method": "language/status", "params": map[string]string{"type": kind}})
		}
		s := &session{definition: serverDefinition{ID: id}, pending: map[uint64]chan rpcReply{}}
		s.readLoop(&buf)
		return s.ready.Load(), s.status
	}
	if ready, status := feed("jdtls", "Starting", "ProjectStatus", "Started"); ready || status != "Started" {
		t.Fatalf("Started must not count as ready: ready=%v status=%q", ready, status)
	}
	if ready, status := feed("jdtls", "Started", "ServiceReady"); !ready || status != "ServiceReady" {
		t.Fatalf("ServiceReady: ready=%v status=%q", ready, status)
	}
	// Readiness is sticky; the status keeps tracking the latest report.
	if ready, status := feed("jdtls", "ServiceReady", "ProjectStatus"); !ready || status != "ProjectStatus" {
		t.Fatalf("after ServiceReady: ready=%v status=%q", ready, status)
	}
	// Server-supplied types are bounded to short identifiers.
	if _, status := feed("jdtls", "Started", strings.Repeat("A", 33), "bad type\n"); status != "Started" {
		t.Fatalf("invalid status recorded: %q", status)
	}
	if ready, status := feed("gopls", "ServiceReady"); ready || status != "" {
		t.Fatal("only jdtls tracks readiness")
	}
}

func TestNotifyJdtlsRestartRequired(t *testing.T) {
	var names []string
	var payloads []any
	core.SetEmitter(func(name string, payload any) {
		names = append(names, name)
		payloads = append(payloads, payload)
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	NotifyJdtlsRestartRequired()
	if len(names) != 1 || names[0] != "project-lsp:restart-required" {
		t.Fatalf("events = %v", names)
	}
	encoded, _ := json.Marshal(payloads[0])
	if string(encoded) != `{"serverId":"jdtls","reason":"bundles"}` {
		t.Fatalf("payload = %s", encoded)
	}
}

// C1: jdtls persists installed bundles in its OSGi configuration area, so the
// built-in jdtls must run with a private per-session -configuration directory
// that is removed when the session ends.
func TestJdtlsUsesPrivatePerSessionConfigurationRemovedOnClose(t *testing.T) {
	configArg := func(active *session) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := active.request(ctx, "test/args", nil)
		if err != nil {
			t.Fatalf("test/args: %v", err)
		}
		var args []string
		if err := json.Unmarshal(out, &args); err != nil {
			t.Fatalf("args %s: %v", out, err)
		}
		for i, arg := range args {
			if arg == "-configuration" && i+1 < len(args) {
				return args[i+1]
			}
		}
		t.Fatalf("jdtls command line %q has no -configuration", args)
		return ""
	}
	waitGone := func(dir string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("configuration directory %s not removed", dir)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	assertPrivateConfigDir := func(dir string) {
		t.Helper()
		if filepath.Clean(filepath.Dir(dir)) != filepath.Clean(os.TempDir()) {
			t.Fatalf("configuration %s is not directly under %s", dir, os.TempDir())
		}
		base := filepath.Base(dir)
		const prefix = "mincode-jdtls-config-"
		if !strings.HasPrefix(base, prefix) || len(base) == len(prefix) {
			t.Fatalf("configuration basename %q is not an unpredictable temp name", base)
		}
		if strings.HasPrefix(base, strconv.Itoa(os.Getpid())+"-") {
			t.Fatalf("configuration basename %q uses a pid prefix", base)
		}
		if filepath.Dir(dir) == filepath.Join(os.TempDir(), "mincode-jdtls-config") {
			t.Fatal("configuration uses the old shared parent")
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("configuration directory missing while running: %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("configuration path is not a directory: %s", dir)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("configuration mode = %o, want 700", info.Mode().Perm())
		}
	}

	root := t.TempDir()
	first := startFakeJdtls(t, root)
	dir := configArg(first)
	assertPrivateConfigDir(dir)
	if err := stop(root, "jdtls"); err != nil {
		t.Fatal(err)
	}
	waitGone(dir)

	// A restarted session gets a fresh directory, never the previous one.
	second := startFakeJdtls(t, root)
	again := configArg(second)
	assertPrivateConfigDir(again)
	if again == dir {
		t.Fatalf("restart reused configuration %s", dir)
	}
	// The process exiting on its own (crash) also removes it.
	if err := second.notify("exit", nil); err != nil {
		t.Fatal(err)
	}
	waitGone(again)

	// Shutdown removes it too.
	third := startFakeJdtls(t, root)
	last := configArg(third)
	assertPrivateConfigDir(last)
	Shutdown(context.Background())
	if _, err := os.Stat(last); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configuration %s survived Shutdown: %v", last, err)
	}
}

// C2: jdtls's built-in java.reloadBundles installs arbitrary jars; it is
// refused on the WebView bridge (project_lsp_request), on
// ExecuteBuiltinCommand and as a notification, before anything is written.
func TestJdtlsReloadBundlesRefusedOnEveryPath(t *testing.T) {
	root := t.TempDir()
	active := startFakeJdtls(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := active.request(ctx, "initialize", json.RawMessage(`{"processId":null}`)); err != nil {
		t.Fatal(err)
	}
	dispatch := func(command string, args any) error {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		_, err = core.Dispatch(command, string(raw))
		return err
	}
	for _, params := range []string{
		`{"command":"java.reloadBundles","arguments":[["/tmp/evil.jar"]]}`,
		`{"command":" Java.ReloadBundles ","arguments":[["/tmp/evil.jar"]]}`,
		// Duplicate keys: the re-encoded message is what was checked.
		`{"command":"java.project.getAll","command":"java.reloadBundles","arguments":[["/tmp/evil.jar"]]}`,
	} {
		err := dispatch("project_lsp_request", map[string]any{
			"root": root, "serverId": "jdtls", "generation": active.generation,
			"method": "workspace/executeCommand", "params": json.RawMessage(params),
		})
		if err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
			t.Fatalf("project_lsp_request %s: %v", params, err)
		}
	}
	if _, err := ExecuteBuiltinCommand(ctx, root, "jdtls", "java.reloadBundles", json.RawMessage(`[["/tmp/evil.jar"]]`)); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Fatalf("ExecuteBuiltinCommand java.reloadBundles: %v", err)
	}
	if err := dispatch("project_lsp_notify", map[string]any{
		"root": root, "serverId": "jdtls", "generation": active.generation,
		"method": "workspace/executeCommand", "params": json.RawMessage(`{"command":"java.reloadBundles","arguments":[["/tmp/evil.jar"]]}`),
	}); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Fatalf("executeCommand notification: %v", err)
	}
	if strings.Contains(active.logs(), "out workspace/executeCommand") {
		t.Fatalf("refused command was written:\n%s", active.logs())
	}
	// Other commands still pass, re-encoded from what was checked.
	result, err := ExecuteBuiltinCommand(ctx, root, "jdtls", "java.project.getAll", nil)
	if err != nil || !strings.Contains(string(result), `"command":"java.project.getAll"`) {
		t.Fatalf("java.project.getAll = %s (%v)", result, err)
	}
	out, err := core.Dispatch("project_lsp_request", mustJSON(t, map[string]any{
		"root": root, "serverId": "jdtls", "generation": active.generation,
		"method": "workspace/executeCommand", "params": json.RawMessage(`{"command":"java.project.getAll","arguments":[],"extra":1}`),
	}))
	if err != nil || strings.Contains(out, "extra") || !strings.Contains(out, `"command":"java.project.getAll"`) {
		t.Fatalf("project_lsp_request getAll = %s (%v)", out, err)
	}
}

// I1 (M1.2): an initialize *notification* would run jdtls's initialize
// handler without prepareInitialize's bundle filter.
func TestInitializeNotificationRefused(t *testing.T) {
	setBundleProvider(t, func() []string { return nil })
	root := t.TempDir()
	active := startFakeJdtls(t, root)
	raw := mustJSON(t, map[string]any{
		"root": root, "serverId": "jdtls", "generation": active.generation, "method": "initialize",
		"params": json.RawMessage(`{"processId":null,"initializationOptions":{"bundles":["/tmp/evil.jar"]}}`),
	})
	if _, err := core.Dispatch("project_lsp_notify", raw); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Fatalf("initialize notification: %v", err)
	}
	if err := active.notify("initialized", json.RawMessage(`{}`)); err == nil {
		t.Fatal("session.notify accepted initialized before initialize completed")
	}
	if err := active.notify("initialize", json.RawMessage(`{}`)); err == nil {
		t.Fatal("session.notify accepted initialize")
	}
	if strings.Contains(active.logs(), "out initialize") {
		t.Fatalf("initialize notification was written:\n%s", active.logs())
	}
	// Other servers too: no server takes initialize as a notification.
	other := &session{definition: serverDefinition{ID: "gopls"}}
	if _, err := other.guardOutgoing("initialize", nil, true); err == nil {
		t.Fatal("gopls initialize notification accepted")
	}
	if _, err := other.guardOutgoing("workspace/executeCommand", json.RawMessage(`{"command":"java.reloadBundles"}`), false); err != nil {
		t.Fatalf("non-jdtls executeCommand refused: %v", err)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
