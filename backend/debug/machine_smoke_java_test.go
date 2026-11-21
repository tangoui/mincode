package debug

// Opt-in end-to-end smoke test of Java debugging through a real jdtls with
// the java-debug bundle (adapter kind "jdtls"):
//
//	MINCODE_DEBUG_MACHINE_SMOKE=1                enables the test
//	MINCODE_DEBUG_SMOKE_JAVA_HOME=<jdk>          JDK that runs the debuggee
//	                                             (passed as javaExec) and
//	                                             JAVA_HOME of the test
//	MINCODE_DEBUG_SMOKE_JAVA_DEBUG_JAR=<jar>     com.microsoft.java.debug.plugin
//	                                             jar (official-plugins/java-debug
//	                                             fetch-assets puts it in lib/)
//	jdtls on PATH                                e.g. brew install jdtls
//
// JAVA_HOME is deliberately the debuggee JDK, typically older than jdtls
// needs (Homebrew jdtls 1.63: Java 25): the host must drop it from jdtls's
// environment so the launcher picks its own JDK. jdtls gets no settings
// from the test: the host's initialize adds the installed JDKs as
// java.configuration.runtimes (default Java 21 when installed), so the bare
// folder compiles for a JDK the debuggee can run. HOME points at a temp dir,
// so jdtls's workspace data dir (the Homebrew launcher keeps it under
// $HOME/Library/Caches/jdtls) is never the user's. Like every
// built-in jdtls, it runs with a MinCode per-session -configuration, so the
// java-debug bundle never lands in the jdtls install's shared OSGi
// configuration; step 2 restarts jdtls without the bundle and asserts that
// vscode.java.startDebugSession is gone.
//
//	MINCODE_DEBUG_MACHINE_SMOKE=1 MINCODE_DEBUG_SMOKE_JAVA_HOME=/opt/homebrew/opt/openjdk@21 \
//	  MINCODE_DEBUG_SMOKE_JAVA_DEBUG_JAR=$PWD/../official-plugins/java-debug/lib/com.microsoft.java.debug.plugin-0.53.1.jar \
//	  go test ./backend/debug -run TestMachineSmokeJava -v -timeout 900s

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/lsp"
)

const (
	javaSmokeTimeout      = 10 * time.Minute
	javaSmokeReadyTimeout = 180 * time.Second
	javaSmokeOutput       = "add(2, 3) = 5"
)

// smokeJava returns the debuggee java executable and the java-debug jar,
// skipping the test unless every prerequisite is present.
func smokeJava(t *testing.T) (javaExec, jar string) {
	t.Helper()
	if os.Getenv("MINCODE_DEBUG_MACHINE_SMOKE") != "1" {
		t.Skip("set MINCODE_DEBUG_MACHINE_SMOKE=1 to run the Java machine smoke test")
	}
	javaHome := os.Getenv("MINCODE_DEBUG_SMOKE_JAVA_HOME")
	if javaHome == "" {
		t.Skip("set MINCODE_DEBUG_SMOKE_JAVA_HOME to the JDK that runs the debuggee (e.g. /opt/homebrew/opt/openjdk@21)")
	}
	javaExec = filepath.Join(javaHome, "bin", "java")
	if runtime.GOOS == "windows" {
		javaExec += ".exe"
	}
	if info, err := os.Stat(javaExec); err != nil || info.IsDir() {
		t.Skipf("MINCODE_DEBUG_SMOKE_JAVA_HOME has no %s: %v", javaExec, err)
	}
	if _, err := exec.LookPath("jdtls"); err != nil {
		t.Skipf("jdtls not found on PATH (e.g. brew install jdtls): %v", err)
	}
	jar = os.Getenv("MINCODE_DEBUG_SMOKE_JAVA_DEBUG_JAR")
	if jar == "" {
		t.Skip("set MINCODE_DEBUG_SMOKE_JAVA_DEBUG_JAR to the com.microsoft.java.debug.plugin jar (official-plugins/java-debug/lib after fetch-assets)")
	}
	jar, err := filepath.Abs(jar)
	if err == nil {
		_, err = os.Stat(jar)
	}
	if err != nil {
		t.Skipf("MINCODE_DEBUG_SMOKE_JAVA_DEBUG_JAR: %v", err)
	}
	return javaExec, jar
}

func TestMachineSmokeJava(t *testing.T) {
	javaExec, jar := smokeJava(t)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "src", "App.java")
	text, err := os.ReadFile(filepath.Join("testdata", "smoke-java", "src", "App.java"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, text, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir()) // jdtls workspace data dir
	// Deliberately the debuggee JDK: the host drops it for jdtls when it is
	// older than jdtls needs.
	t.Setenv("JAVA_HOME", os.Getenv("MINCODE_DEBUG_SMOKE_JAVA_HOME"))

	ctx, cancel := context.WithTimeout(context.Background(), javaSmokeTimeout)
	defer cancel()
	rec := captureEmits(t)
	t.Cleanup(func() {
		if t.Failed() {
			rec.logAll(t)
		}
	})

	// Registered first so it runs last: Shutdown waits for the killed jdtls
	// processes and their per-session configuration directories to go.
	initialConfigDirs := map[string]bool{}
	if matches, err := filepath.Glob(filepath.Join(os.TempDir(), "mincode-jdtls-config-*")); err == nil {
		for _, match := range matches {
			initialConfigDirs[match] = true
		}
	}
	configDirs := func() []string {
		matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "mincode-jdtls-config-*"))
		var created []string
		for _, match := range matches {
			if !initialConfigDirs[match] {
				created = append(created, match)
			}
		}
		return created
	}
	t.Cleanup(func() {
		lsp.Shutdown(context.Background())
		if left := configDirs(); len(left) != 0 {
			t.Errorf("per-session jdtls configuration directories left behind: %v", left)
		}
	})

	lsp.SetJdtlsBundleProvider(func() []string { return []string{jar} })
	t.Cleanup(func() { lsp.SetJdtlsBundleProvider(nil) })
	javaHome, spec := debuggeeJDK(t, javaExec)
	t.Logf("debuggee JDK: %s (Java %s), also JAVA_HOME", javaHome, spec)
	firstStart := time.Now()
	stopFirst := startJdtls(ctx, t, root, source, string(text))
	t.Logf("step 1: jdtls with java-debug ready after %s", time.Since(firstStart).Round(time.Millisecond))
	rawLogs, _ := lspDispatch(t, "project_lsp_logs", map[string]any{"root": root, "serverId": "jdtls"})
	var logs struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(rawLogs), &logs); err != nil {
		t.Fatalf("decode project_lsp_logs: %v (%s)", err, rawLogs)
	}
	for _, line := range strings.Split(logs.Output, "\n") {
		if strings.Contains(line, "lifecycle jdtls") {
			t.Logf("host: %s", line)
		}
	}
	if !strings.Contains(logs.Output, "lifecycle jdtls runtimes configured") {
		t.Fatalf("the host configured no java.configuration.runtimes; logs: %s", logs.Output)
	}

	if bundles, running := lsp.JdtlsSessionBundles(root); !running || !slices.Contains(bundles, jar) {
		t.Fatalf("jdtls bundles = %v (running %v), want %s", bundles, running, jar)
	}
	if dirs := configDirs(); len(dirs) != 1 {
		t.Fatalf("per-session jdtls configuration directories = %v, want exactly one", dirs)
	}

	// The launch arguments java-debug's resolve builds, with javaExec and
	// console set by the test (mainClass is explicit: a bare folder has no
	// project-wide main class lookup).
	var classpaths [2][]string
	jdtlsCommand(ctx, t, root, "vscode.java.resolveClasspath", []any{"App", ""}, &classpaths)
	t.Logf("resolveClasspath: modulePaths %v, classPaths %v", classpaths[0], classpaths[1])
	if len(classpaths[1]) == 0 {
		t.Fatalf("resolveClasspath returned no class path for App")
	}
	// The bare folder must be compiled for a Java the debuggee JDK runs.
	debuggeeMajor, _ := strconv.Atoi(strings.TrimPrefix(spec, "1."))
	classMajor := smokeClassFileMajor(t, classpaths[1], "App")
	t.Logf("App.class: class file version %d (Java %d), debuggee Java %d", classMajor, classMajor-44, debuggeeMajor)
	if classMajor-44 > debuggeeMajor {
		t.Fatalf("App.class targets Java %d, newer than the debuggee JDK (Java %d)", classMajor-44, debuggeeMajor)
	}
	var projectJava *string
	jdtlsCommand(ctx, t, root, "vscode.java.resolveJavaExecutable", []any{"App", ""}, &projectJava)
	if projectJava != nil {
		t.Logf("resolveJavaExecutable: %s", *projectJava)
	}

	for _, mode := range []struct {
		console string
		noDebug bool
	}{
		{"internalConsole", false}, {"integratedTerminal", false},
		{"internalConsole", true}, {"integratedTerminal", true},
	} {
		console := mode.console
		args := ""
		if mode.noDebug {
			args = "--wait" // keep the fixture alive until StopSession
		}
		var port int
		jdtlsCommand(ctx, t, root, "vscode.java.startDebugSession", []any{}, &port)
		t.Logf("startDebugSession: port %d", port)
		requireLoopbackOnly(t, port)

		s, err := ConnectAdapter(ctx, ConnectSpec{
			Port: port, OwnerPluginID: "machine-smoke", DebuggerType: "java", ProjectRoot: root,
		})
		if err != nil {
			t.Fatalf("ConnectAdapter: %v", err)
		}
		t.Cleanup(func() { s.Close("smoke cleanup") }) // before jdtls stops
		launchArgs, err := json.Marshal(map[string]any{
			"type":        "java",
			"mainClass":   "App",
			"modulePaths": nonNil(classpaths[0]),
			"classPaths":  classpaths[1],
			"javaExec":    javaExec,
			"cwd":         root,
			"console":     console,
			"noDebug":     mode.noDebug,
			"args":        args,
		})
		if err != nil {
			t.Fatal(err)
		}
		beginCtx, cancelBegin := context.WithTimeout(ctx, 60*time.Second)
		defer cancelBegin()
		t.Logf("launching with %s, noDebug=%v", console, mode.noDebug)
		if _, err := s.Begin(beginCtx, BeginParams{
			AdapterID:   "java",
			Request:     "launch",
			Arguments:   launchArgs,
			Breakpoints: map[string][]SourceBreakpoint{source: {{Line: 4}}},
		}); err != nil {
			t.Fatalf("Begin: %v", err)
		}

		if mode.noDebug {
			if console == "integratedTerminal" {
				terminalID, _ := runInTerminalEvent(t, rec, s.ID())
				waitTerminalOutput(t, terminalID, javaSmokeOutput)
			} else {
				rec.waitEvent(ctx, t, s.ID(), "output")
				if out := rec.output(s.ID()); !strings.Contains(out, javaSmokeOutput) {
					t.Fatalf("noDebug output %q, want %q", out, javaSmokeOutput)
				}
			}
			t.Logf("noDebug Begin completed; fixture is running")
			StopSession(ctx, s, "noDebug smoke completed")
			if infos := List(); len(infos) != 0 {
				t.Fatalf("List() after noDebug stop = %+v, want empty", infos)
			}
			continue
		}

		stopped := rec.waitEvent(ctx, t, s.ID(), "stopped")
		var stop struct {
			Reason   string `json:"reason"`
			ThreadID int    `json:"threadId"`
		}
		if err := json.Unmarshal(stopped.Body, &stop); err != nil {
			t.Fatalf("decode stopped: %v (%s)", err, stopped.Body)
		}
		if stop.Reason != "breakpoint" {
			t.Fatalf("stopped reason = %q, want breakpoint (%s)", stop.Reason, stopped.Body)
		}
		var trace struct {
			StackFrames []struct {
				ID     int    `json:"id"`
				Name   string `json:"name"`
				Line   int    `json:"line"`
				Source struct {
					Path string `json:"path"`
				} `json:"source"`
			} `json:"stackFrames"`
		}
		smokeRequest(ctx, t, s, "stackTrace", map[string]any{"threadId": stop.ThreadID, "startFrame": 0, "levels": 20}, &trace)
		if len(trace.StackFrames) == 0 || trace.StackFrames[0].Line != 4 {
			t.Fatalf("stackTrace = %+v, want top frame on line 4", trace.StackFrames)
		}
		t.Logf("top frame: %s at %s:%d", trace.StackFrames[0].Name, trace.StackFrames[0].Source.Path, trace.StackFrames[0].Line)
		if total := smokeLocal(ctx, t, s, trace.StackFrames[0].ID, "total"); total != "5" {
			t.Fatalf("variable total = %q, want \"5\"", total)
		}

		terminalID := ""
		if console == "integratedTerminal" {
			terminalID, _ = runInTerminalEvent(t, rec, s.ID())
			if _, err := dispatch(t, "attach_local_shell", map[string]any{"sessionId": terminalID}); err != nil {
				t.Fatalf("attach debuggee PTY: %v", err)
			}
		}
		smokeRequest(ctx, t, s, "continue", map[string]any{"threadId": stop.ThreadID}, nil)
		rec.waitEvent(ctx, t, s.ID(), "terminated")
		ended := rec.waitEvent(ctx, t, s.ID(), SessionEndedEvent)
		t.Logf("%s: %s", SessionEndedEvent, ended.Body)
		if out := rec.output(s.ID()); console == "internalConsole" && !strings.Contains(out, javaSmokeOutput) {
			t.Fatalf("debuggee output %q, want it to contain %q", out, javaSmokeOutput)
		}
		if infos := List(); len(infos) != 0 {
			t.Fatalf("List() after terminated = %+v, want empty", infos)
		}
	}

	// Step 2 (C1): jdtls persists installed bundles in its OSGi
	// configuration area. MinCode gives every built-in jdtls a per-session
	// -configuration, so once the bundle is no longer provided (disabled,
	// untrusted, uninstalled) a restarted jdtls must not have java-debug.
	stopFirst()
	lsp.SetJdtlsBundleProvider(func() []string { return nil })
	secondStart := time.Now()
	startJdtls(ctx, t, root, source, string(text))
	t.Logf("step 2: jdtls without bundles ready after %s", time.Since(secondStart).Round(time.Millisecond))
	if bundles, running := lsp.JdtlsSessionBundles(root); !running || len(bundles) != 0 {
		t.Fatalf("restarted jdtls bundles = %v (running %v), want none", bundles, running)
	}
	cmdCtx, cancelCmd := context.WithTimeout(ctx, 60*time.Second)
	defer cancelCmd()
	result, err := lsp.ExecuteBuiltinCommand(cmdCtx, root, "jdtls", "vscode.java.startDebugSession", json.RawMessage("[]"))
	if err == nil {
		t.Fatalf("vscode.java.startDebugSession succeeded without the bundle (result %s): java-debug persisted across jdtls sessions", result)
	}
	if !strings.Contains(err.Error(), "No delegateCommandHandler for vscode.java.startDebugSession") {
		t.Fatalf("want command-not-found without bundle, got %v", err)
	}
	t.Logf("step 2: vscode.java.startDebugSession without bundle: %v", err)
}

// startJdtls starts the built-in jdtls for root through the project_lsp_*
// commands with the initialize the frontend sends (no
// initializationOptions: the host adds bundles and runtimes), opens source
// and waits for ServiceReady. It answers the server's requests like the
// frontend (workspace/configuration with nulls, everything else with null;
// the requested sections are logged) and stops jdtls on cleanup or when the
// returned stop is called (idempotent).
func startJdtls(ctx context.Context, t *testing.T, root, source, text string) (stop func()) {
	t.Helper()
	var statusMu sync.Mutex
	var statuses []string // language/status params, for a failed wait
	core.SetEmitter(func(_ string, payload any) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return
		}
		var event struct {
			ServerID   string          `json:"serverId"`
			Generation uint64          `json:"generation"`
			Method     string          `json:"method"`
			Params     json.RawMessage `json:"params"`
			RequestID  *uint64         `json:"requestId"`
		}
		if json.Unmarshal(raw, &event) != nil || event.ServerID != "jdtls" {
			return
		}
		if event.Method == "language/status" {
			statusMu.Lock()
			statuses = append(statuses, string(event.Params))
			statusMu.Unlock()
		}
		if event.RequestID == nil {
			return
		}
		result := json.RawMessage("null")
		if event.Method == "workspace/configuration" {
			var params struct {
				Items []json.RawMessage `json:"items"`
			}
			_ = json.Unmarshal(event.Params, &params)
			t.Logf("jdtls workspace/configuration items: %s", event.Params)
			result, _ = json.Marshal(make([]any, len(params.Items)))
		}
		_, _ = lspDispatch(t, "project_lsp_reply", map[string]any{
			"root": root, "serverId": "jdtls", "generation": event.Generation, "requestId": *event.RequestID, "result": result,
		})
	})
	t.Cleanup(func() { core.SetEmitter(nil) })

	out, err := lspDispatch(t, "project_lsp_start", map[string]any{"root": root, "serverId": "jdtls"})
	if err != nil {
		t.Fatalf("project_lsp_start: %v", err)
	}
	var status struct {
		Generation uint64 `json:"generation"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil || status.Generation == 0 {
		t.Fatalf("project_lsp_start = %s (%v)", out, err)
	}
	gen := status.Generation
	var once sync.Once
	stop = func() {
		once.Do(func() {
			// Graceful first so jdtls saves nothing half-written, then stop.
			_, _ = lspDispatch(t, "project_lsp_request", map[string]any{"root": root, "serverId": "jdtls", "generation": gen, "method": "shutdown", "params": nil, "timeoutMs": 10000})
			_, _ = lspDispatch(t, "project_lsp_notify", map[string]any{"root": root, "serverId": "jdtls", "generation": gen, "method": "exit", "params": nil})
			if _, err := lspDispatch(t, "project_lsp_stop", map[string]any{"root": root, "serverId": "jdtls"}); err != nil {
				t.Errorf("project_lsp_stop: %v", err)
			}
		})
	}
	t.Cleanup(stop)

	rootURI := fileURL(root)
	if _, err := lspDispatch(t, "project_lsp_request", map[string]any{
		"root": root, "serverId": "jdtls", "generation": gen, "method": "initialize", "timeoutMs": 30000,
		"params": map[string]any{
			"processId":        nil,
			"clientInfo":       map[string]string{"name": "mincode"},
			"rootUri":          rootURI,
			"workspaceFolders": []any{map[string]string{"uri": rootURI, "name": filepath.Base(root)}},
			"capabilities": map[string]any{
				"workspace": map[string]any{"configuration": true, "workspaceFolders": true, "executeCommand": map[string]any{}},
			},
		},
	}); err != nil {
		t.Fatalf("jdtls initialize: %v", err)
	}
	if _, err := lspDispatch(t, "project_lsp_notify", map[string]any{"root": root, "serverId": "jdtls", "generation": gen, "method": "initialized", "params": map[string]any{}}); err != nil {
		t.Fatalf("jdtls initialized: %v", err)
	}
	if _, err := lspDispatch(t, "project_lsp_notify", map[string]any{
		"root": root, "serverId": "jdtls", "generation": gen, "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": fileURL(source), "languageId": "java", "version": 1, "text": text}},
	}); err != nil {
		t.Fatalf("jdtls didOpen: %v", err)
	}

	started := time.Now()
	readyCtx, cancel := context.WithTimeout(ctx, javaSmokeReadyTimeout)
	defer cancel()
	for {
		ready, running := lsp.JdtlsReady(root)
		if ready {
			break
		}
		if !running {
			logs, _ := lspDispatch(t, "project_lsp_logs", map[string]any{"root": root, "serverId": "jdtls"})
			t.Fatalf("jdtls exited before ServiceReady; logs: %s", logs)
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-readyCtx.Done():
			statusMu.Lock()
			seen := slices.Clone(statuses)
			statusMu.Unlock()
			t.Fatalf("jdtls not ServiceReady after %s (last status %q, statuses %v)", javaSmokeReadyTimeout, lsp.JdtlsStatus(root), seen)
		}
	}
	statusMu.Lock()
	seen := slices.Clone(statuses)
	statusMu.Unlock()
	t.Logf("jdtls ServiceReady after %s (statuses %v)", time.Since(started).Round(time.Millisecond), seen)
	return stop
}

// requireLoopbackOnly fails when java-debug's server accepts a connection on
// a non-loopback address of this machine: the shipped jar binds 127.0.0.1
// (official-plugins/java-debug/scripts/loopback-patch.mjs); upstream listens
// on every interface. Skipped when the machine has no non-loopback address.
func requireLoopbackOnly(t *testing.T, port int) {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("interface addresses: %v", err)
	}
	for _, addr := range addrs {
		ip, ok := addr.(*net.IPNet)
		if !ok || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.To4() == nil {
			continue
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip.IP.String(), strconv.Itoa(port)), 2*time.Second)
		if err == nil {
			conn.Close()
			t.Fatalf("java-debug accepted a connection on %s:%d; it must listen on loopback only", ip.IP, port)
		}
		t.Logf("java-debug refuses %s:%d (%v)", ip.IP, port, err)
		return
	}
	t.Log("no non-loopback IPv4 address; loopback-only check skipped")
}

// debuggeeJDK returns java.home and java.specification.version of javaExec.
func debuggeeJDK(t *testing.T, javaExec string) (home, spec string) {
	t.Helper()
	out, err := exec.Command(javaExec, "-XshowSettings:properties", "-version").CombinedOutput()
	if err != nil {
		t.Fatalf("%s -XshowSettings:properties: %v: %s", javaExec, err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
		switch {
		case !ok:
		case name == "java.home":
			home = value
		case name == "java.specification.version":
			spec = value
		}
	}
	if home == "" || spec == "" {
		t.Fatalf("%s reported no java.home / java.specification.version: %s", javaExec, out)
	}
	return home, spec
}

// smokeClassFileMajor returns the class file major version of class name
// in the first class path directory that has it.
func smokeClassFileMajor(t *testing.T, classPaths []string, name string) int {
	t.Helper()
	for _, dir := range classPaths {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(name, ".", "/"))+".class"))
		if err != nil || len(data) < 8 {
			continue
		}
		return int(data[6])<<8 | int(data[7])
	}
	t.Fatalf("%s.class not found in %v", name, classPaths)
	return 0
}

// jdtlsCommand runs a workspace/executeCommand in root's jdtls and decodes
// the result into out.
func jdtlsCommand(ctx context.Context, t *testing.T, root, command string, args []any, out any) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result, err := lsp.ExecuteBuiltinCommand(cmdCtx, root, "jdtls", command, raw)
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	if err := json.Unmarshal(result, out); err != nil {
		t.Fatalf("decode %s result: %v (%s)", command, err, result)
	}
}

func lspDispatch(t *testing.T, command string, args any) (string, error) {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("encode %s args: %w", command, err)
	}
	return core.Dispatch(command, string(b))
}

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// output returns the concatenated `output` event text of session id.
func (r *emitRecorder) output(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, ev := range r.events {
		if ev.SessionID != id || ev.Event != "output" {
			continue
		}
		var body struct {
			Output string `json:"output"`
		}
		if json.Unmarshal(ev.Body, &body) == nil {
			b.WriteString(body.Output)
		}
	}
	return b.String()
}
