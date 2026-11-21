package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/plugins/api"
)

func TestPluginHelperProcess(t *testing.T) {
	pluginID := os.Getenv("MINCODE_PLUGIN_ID")
	if pluginID != "runtime.test" && pluginID != "runtime.ignore-shutdown" && pluginID != "runtime.block-stdin" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request rpcEnvelope
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		response := rpcEnvelope{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case "plugin.initialize", "plugin.ping":
			response.Result = json.RawMessage(`true`)
		case "plugin.invoke":
			// Global shortcut tests read these lines from the plugin log.
			if bytes.Contains(request.Params, []byte(`globalShortcut`)) {
				fmt.Fprintln(os.Stderr, "helper invoke globalShortcut")
			}
			// Language feature slot tests: never answer, or answer as hover.
			if bytes.Contains(request.Params, []byte(`helper-hold`)) {
				continue
			}
			if bytes.Contains(request.Params, []byte(`helper-hover`)) {
				response.Result = json.RawMessage(`{"result":{"contents":["ok"]}}`)
				break
			}
			if bytes.Contains(request.Params, []byte(`helper-langfeat`)) {
				response.Result = helperLanguageFeatureResult(request.Params)
				break
			}
			if result, ok := helperSessionSourceResult(request.Params); ok {
				response.Result = result
				break
			}
			response.Result = json.RawMessage(`{"result":{"ok":true}}`)
		case globalShortcutActivationMethod:
			var activation struct {
				ID        string `json:"id"`
				Token     string `json:"token"`
				ExpiresAt int64  `json:"expiresAt"`
			}
			_ = json.Unmarshal(request.Params, &activation)
			fmt.Fprintf(os.Stderr, "helper activation %s %s %d\n", activation.ID, activation.Token, activation.ExpiresAt)
			response.Result = json.RawMessage(`{}`)
		case "plugin.shutdown":
			response.Result = json.RawMessage(`true`)
			_ = encoder.Encode(response)
			if pluginID == "runtime.test" {
				os.Exit(0)
			}
			continue
		default:
			response.Error = &rpcError{Code: -32601, Message: "unknown"}
		}
		_ = encoder.Encode(response)
		if pluginID == "runtime.block-stdin" {
			for {
				time.Sleep(time.Hour)
			}
		}
	}
	os.Exit(0)
}

// helperSessionSourceResult answers a session source, commit message or file
// decoration provider request: it logs the payload it received ("helper
// session payload <json>", "helper scm payload <json>", "helper decorations
// payload <json>") so tests can check exactly what the plugin saw, and
// returns one session, one message, a commit message or decorations.
func helperSessionSourceResult(params []byte) (json.RawMessage, bool) {
	var invoke struct {
		Payload string `json:"payload"`
	}
	if json.Unmarshal(params, &invoke) != nil {
		return nil, false
	}
	switch {
	case strings.Contains(invoke.Payload, `"request":"list"`):
		fmt.Fprintln(os.Stderr, "helper session payload "+invoke.Payload)
		return json.RawMessage(`{"result":{"sessions":[{"id":"s1","title":"One"}]}}`), true
	case strings.Contains(invoke.Payload, `"request":"messages"`):
		fmt.Fprintln(os.Stderr, "helper session payload "+invoke.Payload)
		return json.RawMessage(`{"result":{"messages":[{"role":"user","text":"hi"}]}}`), true
	case strings.HasPrefix(invoke.Payload, `{"provider":"conventional"`):
		// Commit message provider: log the payload, answer with CRLF line
		// ends and a bidi override the host must remove.
		fmt.Fprintln(os.Stderr, "helper scm payload "+invoke.Payload)
		return json.RawMessage(`{"result":{"message":"feat: x\r\n\r\nbody\u202e"}}`), true
	case strings.HasPrefix(invoke.Payload, `{"provider":"lint"`):
		// File decoration provider: log the payload, decorate a requested
		// name, a name that was not asked for and a sensitive name.
		fmt.Fprintln(os.Stderr, "helper decorations payload "+invoke.Payload)
		return json.RawMessage(`{"result":{"decorations":{"a.ts":{"badge":"E","color":"danger"},"c.ts":{"badge":"X"},".env":{"badge":"!"}}}}`), true
	}
	return nil, false
}

// helperLanguageFeatureResult answers a helper-langfeat language feature
// request with one kept item, one item the host drops and (for definition
// and references) a cross-file location kept only with workspace.read.
func helperLanguageFeatureResult(params []byte) json.RawMessage {
	r := `{"startLine":1,"startColumn":1,"endLine":1,"endColumn":5}`
	other := `{"startLine":2,"startColumn":1,"endLine":2,"endColumn":3}`
	var result string
	switch {
	case bytes.Contains(params, []byte(`\"feature\":\"definition\"`)):
		result = `{"locations":[{"range":` + r + `,"selectionRange":{"startLine":1,"startColumn":1,"endLine":1,"endColumn":3}},{"path":"src/a.yaml","range":` + other + `}]}`
	case bytes.Contains(params, []byte(`\"feature\":\"references\"`)):
		result = `{"locations":[{"range":` + r + `},{"path":"src/a.yaml","range":` + other + `}]}`
	case bytes.Contains(params, []byte(`\"feature\":\"codeLens\"`)):
		result = `{"lenses":[{"range":` + r + `,"title":"2 references"},{"range":` + r + `,"title":"Run","command":"run"},{"range":` + r + `,"title":"Steal","command":"other.plugin.run"}]}`
	case bytes.Contains(params, []byte(`\"feature\":\"inlayHints\"`)):
		result = `{"hints":[{"position":{"line":1,"column":5},"label":": T","kind":"type"},{"position":{"line":2,"column":1},"label":"x"}]}`
	case bytes.Contains(params, []byte(`\"feature\":\"documentSymbol\"`)):
		result = `{"symbols":[{"name":"task","kind":"function","range":` + r + `},{"name":"w","kind":"widget","range":` + r + `}]}`
	default:
		result = `{"ok":true}`
	}
	return json.RawMessage(`{"result":` + result + `}`)
}

type observedRPCWriter struct {
	io.WriteCloser
	entered chan struct{}
}

func (w *observedRPCWriter) Write(p []byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	return w.WriteCloser.Write(p)
}

func TestProcessRuntimeBlockedWriteCancellationAndStop(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestPluginHelperProcess$")
	cmd.Env = append(os.Environ(), "MINCODE_PLUGIN_ID=runtime.block-stdin")
	core.PrepareProcessTree(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.KillProcessTree(cmd) })
	// The helper acknowledges initialize and never reads stdin again.
	if _, err := io.WriteString(stdin, `{"jsonrpc":"2.0","id":"init","method":"plugin.initialize"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if !bufio.NewScanner(stdout).Scan() {
		t.Fatal("missing initialization reply")
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	writer := &observedRPCWriter{WriteCloser: stdin, entered: make(chan struct{}, 1)}
	h := newProcessRuntime("", Manifest{}, nil, nil)
	h.cmd, h.stdin, h.generation = cmd, stdin, 1
	h.runContext, h.runCancel = runCtx, runCancel
	h.pending = make(map[string]chan rpcEnvelope)
	h.done, h.heartbeatEnd = make(chan struct{}), make(chan struct{})
	h.writes = make(chan rpcWriteRequest)
	writerDone := make(chan struct{})
	go func() { writePluginRPC(runCtx, writer, h.writes); close(writerDone) }()
	go func() { h.processExited(1, cmd.Wait()) }()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := h.call(ctx, "plugin.invoke", json.RawMessage(`"`+strings.Repeat("x", 900*1024)+`"`))
		result <- err
	}()
	select {
	case <-writer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not enter pipe write")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("call: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("call cancellation blocked")
	}
	// A second call must time out waiting for the sole writer, not add writers.
	queuedCtx, queuedCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer queuedCancel()
	if _, err := h.call(queuedCtx, "plugin.ping", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued call: %v", err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stopCancel()
	stopped := make(chan error, 1)
	go func() { stopped <- h.Stop(stopCtx) }()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked behind write")
	}
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("writer leaked after Stop")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cmd != nil || len(h.pending) != 0 {
		t.Fatal("process or pending calls survived Stop")
	}
}

func TestProcessRuntimeForceStopGenerationWaitsForCleanup(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "canceled"}[canceled], func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=^TestPluginHelperProcess$")
			cmd.Env = append(os.Environ(), "MINCODE_PLUGIN_ID=runtime.block-stdin")
			core.PrepareProcessTree(cmd)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			h := newProcessRuntime("", Manifest{}, nil, nil)
			runCtx, runCancel := context.WithCancel(context.Background())
			h.cmd, h.stdin, h.generation = cmd, stdin, 1
			h.runContext, h.runCancel = runCtx, runCancel
			h.done, h.heartbeatEnd = make(chan struct{}), make(chan struct{})
			pending := make(chan rpcEnvelope, 1)
			h.pending = map[string]chan rpcEnvelope{"initialize": pending}
			waited, release, cleaned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() {
				err := cmd.Wait()
				close(waited)
				<-release
				h.processExited(1, err)
				close(cleaned)
			}()
			t.Cleanup(func() {
				_ = core.KillProcessTree(cmd)
				closeOnce(release)
				<-cleaned
				runCancel()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if canceled {
				cancel()
			}
			result := make(chan error, 1)
			go func() { result <- h.ForceStopGeneration(ctx, 1) }()
			select {
			case <-waited:
			case <-time.After(5 * time.Second):
				t.Fatal("captured process was not killed")
			}
			if runCtx.Err() == nil {
				t.Fatal("generation context was not canceled")
			}
			if canceled {
				select {
				case err := <-result:
					if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "termination unconfirmed") {
						t.Fatalf("force stop: %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("canceled confirmation wait blocked")
				}
			} else {
				select {
				case err := <-result:
					t.Fatalf("reported completion before pending cleanup: %v", err)
				default:
				}
			}
			close(release)
			<-cleaned
			if !canceled {
				if err := <-result; err != nil {
					t.Fatal(err)
				}
			}
			if h.cmd != nil || len(h.pending) != 0 {
				t.Fatal("runtime state survived confirmed exit")
			}
			select {
			case response := <-pending:
				if response.Error == nil {
					t.Fatal("pending initialization did not fail")
				}
			default:
				t.Fatal("pending initialization was not released")
			}
			if err := h.ForceStopGeneration(ctx, 1); err != nil {
				t.Fatalf("retired generation: %v", err)
			}
		})
	}
}

func TestProcessRuntimeRevalidatesBeforeWrite(t *testing.T) {
	for _, scenario := range []string{"generation", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			runCtx, stop := context.WithCancel(context.Background())
			defer stop()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := newProcessRuntime("", Manifest{}, nil, nil)
			h.generation, h.runContext, h.writes = 1, runCtx, make(chan rpcWriteRequest)
			result := make(chan error, 1)
			go func() { result <- h.writeContext(ctx, 1, rpcEnvelope{JSONRPC: "2.0", Method: "plugin.invoke"}) }()
			var request rpcWriteRequest
			select {
			case request = <-h.writes:
			case <-time.After(time.Second):
				t.Fatal("request not admitted")
			}
			// Pause after admission, before the writer's final validation.
			if scenario == "generation" {
				h.mu.Lock()
				h.generation = 2
				h.mu.Unlock()
			} else {
				cancel()
			}
			queue := make(chan rpcWriteRequest, 1)
			queue <- request
			var output bytes.Buffer
			done := make(chan struct{})
			go func() { writePluginRPC(runCtx, &output, queue); close(done) }()
			select {
			case err := <-result:
				if err == nil {
					t.Fatal("invalid request accepted")
				}
				if scenario == "generation" && !strings.Contains(err.Error(), "generation changed") {
					t.Fatal(err)
				}
				if scenario == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("request did not return")
			}
			stop()
			<-done
			if output.Len() != 0 {
				t.Fatal("invalid request reached pipe")
			}
		})
	}
}

func TestProcessRuntimeSaturatedCallbacksStillDrainStdout(t *testing.T) {
	h := newProcessRuntime("", Manifest{}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.generation, h.runContext = 1, ctx
	h.writes = make(chan rpcWriteRequest, pluginRPCWriteQueue)
	for i := 0; i < cap(h.hostRequests); i++ {
		h.hostRequests <- struct{}{}
	}
	reply := make(chan rpcEnvelope, 1)
	h.pending = map[string]chan rpcEnvelope{"invoke": reply}
	// No writer consumes the queue, modelling a child that does not read stdin.
	var stdout strings.Builder
	for i := 0; i < 1000; i++ {
		stdout.WriteString(`{"jsonrpc":"2.0","id":"callback","method":"host.info"}` + "\n")
	}
	stdout.WriteString(`{"jsonrpc":"2.0","id":"invoke","result":true}` + "\n")
	done := make(chan struct{})
	go func() { h.readStdout(1, strings.NewReader(stdout.String()), newRPCLineLimit()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stdout blocked behind busy responses")
	}
	select {
	case response := <-reply:
		if string(response.Result) != "true" {
			t.Fatalf("response: %+v", response)
		}
	default:
		t.Fatal("ordinary reply was not delivered")
	}
	if len(h.writes) != pluginRPCWriteQueue {
		t.Fatalf("queued replies: %d", len(h.writes))
	}
	request := <-h.writes
	var response rpcEnvelope
	if err := json.Unmarshal(request.data, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != -32004 || string(response.ID) != `"callback"` {
		t.Fatalf("busy response: %+v", response)
	}
	h.mu.Lock()
	h.generation = 2
	h.mu.Unlock()
	if request.validate() == nil {
		t.Fatal("stale busy reply accepted")
	}
	before := len(h.writes)
	h.respondBusy(1, rpcEnvelope{ID: json.RawMessage(`1`)})
	if len(h.writes) != before {
		t.Fatal("old generation queued a reply")
	}
}

func TestProcessRuntimeExitCallbackCarriesOriginalGeneration(t *testing.T) {
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h := newProcessRuntime("", Manifest{}, func(level, message string) {
		if level == "error" {
			close(entered)
			<-resume
		}
	}, func(error) { t.Error("legacy callback must not run when generation callback is set") })
	h.generation = 1
	h.done, h.heartbeatEnd = make(chan struct{}), make(chan struct{})
	var notified uint64
	h.SetExitHandler(func(generation uint64, err error) {
		notified = generation
		// Consumers may query the runtime: notification must not hold its lock.
		if h.Generation() != 2 {
			t.Error("reload did not occur before notification")
		}
		if err == nil {
			t.Error("missing exit error")
		}
	})
	go func() { h.processExited(1, nil); close(done) }()
	<-entered
	h.mu.Lock()
	h.generation = 2
	h.cmd = &exec.Cmd{}
	h.mu.Unlock()
	close(resume)
	<-done
	if notified != 1 {
		t.Fatalf("notified generation %d, want 1", notified)
	}
	h.processExited(1, nil)
	if h.cmd == nil {
		t.Fatal("old exit cleared reloaded process")
	}
}

// A crashed process clears cmd before its owner finishes storage/session hooks.
// Stop must join that owner, not treat cmd == nil as completed teardown.
func TestProcessRuntimeStopJoinsExitOwner(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint("force=", force), func(t *testing.T) {
			entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
			h := newProcessRuntime("", Manifest{}, nil, func(error) {
				close(entered)
				<-release
			})
			h.generation = 1
			h.done, h.heartbeatEnd = make(chan struct{}), make(chan struct{})
			go func() { h.processExited(1, nil); close(exited) }()
			t.Cleanup(func() { close(release); <-exited })
			<-entered
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var err error
			if force {
				err = h.ForceStopGeneration(ctx, 1)
			} else {
				err = h.Stop(ctx)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("stop returned before exit owner completed: %v", err)
			}
			select {
			case <-h.done:
				t.Error("generation teardown marked done before owner callback completed")
			default:
			}
		})
	}
}

func TestProcessRuntimeLegacyExitCallback(t *testing.T) {
	calls := 0
	h := newProcessRuntime("", Manifest{}, nil, func(error) { calls++ })
	h.processExited(0, nil)
	if calls != 1 {
		t.Fatalf("legacy callbacks: %d", calls)
	}
}

func TestSafeProcessRPCErrorPreservesAllowlistedData(t *testing.T) {
	err := safeProcessRPCError("plugin.invoke", &rpcError{
		Code: -32000, Message: "safe failure",
		Data: map[string]any{"kind": "business", "tool": "runner", "retryable": true, "secret": "hidden"},
	})
	var processErr *processRPCError
	if !errors.As(err, &processErr) {
		t.Fatalf("unexpected error type: %T", err)
	}
	if processErr.Code != -32000 || processErr.Message != "safe failure" {
		t.Fatalf("unexpected error: %+v", processErr)
	}
	if processErr.Data["kind"] != "business" || processErr.Data["tool"] != "runner" || processErr.Data["retryable"] != true {
		t.Fatalf("missing safe data: %#v", processErr.Data)
	}
	if _, exists := processErr.Data["secret"]; exists {
		t.Fatalf("unsafe data preserved: %#v", processErr.Data)
	}
}

func TestProcessRuntimeOversizedResponse(t *testing.T) {
	for _, response := range []rpcEnvelope{
		{Result: json.RawMessage(`"` + strings.Repeat("x", pluginRPCMaxMessage) + `"`)},
		{Error: &rpcError{Code: -32003, Message: strings.Repeat("x", pluginRPCMaxMessage)}},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		var output bytes.Buffer
		h := newProcessRuntime("", Manifest{}, nil, nil)
		h.runContext, h.writes = ctx, make(chan rpcWriteRequest)
		done := make(chan struct{})
		go func() { writePluginRPC(ctx, &output, h.writes); close(done) }()
		response.JSONRPC, response.ID = "2.0", json.RawMessage(`"request-42"`)
		err := h.write(0, response)
		cancel()
		<-done
		if err != nil {
			t.Fatal(err)
		}
		var got rpcEnvelope
		if err := json.Unmarshal(output.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if string(got.ID) != string(response.ID) || got.Error == nil || got.Error.Code != -32005 || len(got.Result) != 0 || output.Len() > 256 {
			t.Fatalf("unexpected response: %s", output.Bytes())
		}
	}
}

func TestProcessRuntimeShellOutputBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell output generator")
	}
	h := newProcessRuntime(t.TempDir(), Manifest{ID: "shell-budget"}, nil, nil)
	// NUL bytes exercise JSON's worst-case six-byte escaping on both streams.
	params, _ := json.Marshal(map[string]any{"command": "dd if=/dev/zero bs=1024 count=200 2>/dev/null; dd if=/dev/zero bs=1024 count=200 1>&2 2>/dev/null"})
	h.handleShellRequest(context.Background(), rpcEnvelope{Params: params}, func(value any, rpcErr *rpcError) {
		if rpcErr != nil {
			t.Fatalf("shell: %+v", rpcErr)
		}
		out := value.(execOutput)
		if !strings.Contains(out.Stdout, "stdout truncated") || !strings.Contains(out.Stderr, "stderr truncated") {
			t.Fatal("missing truncation markers")
		}
		result, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(rpcEnvelope{JSONRPC: "2.0", ID: json.RawMessage(`1`), Result: result})
		if err != nil || len(encoded) >= pluginRPCMaxMessage {
			t.Fatalf("shell response size %d: %v", len(encoded), err)
		}
	})
}

func TestProcessRuntimeJSONRPC(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	entry := "plugin-helper"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	entryPath := filepath.Join(root, entry)
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, data, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		APIVersion:   api.APIVersion,
		ID:           "runtime.test",
		Name:         "Runtime Test",
		Version:      "1.0.0",
		Capabilities: []api.Capability{api.CapabilityStorage},
		Runtime:      ProcessRuntimeManifest{Type: "process", Entry: entry, Args: []string{"-test.run=TestPluginHelperProcess"}},
	}
	manifest.Contributes.Actions = []ManifestUIAction{{ID: "run", UICallable: true, MaxPayloadBytes: 1024}}
	entered, resume := make(chan struct{}), make(chan struct{})
	var runtimeHost *processRuntime
	runtimeHost = newProcessRuntime(root, manifest, func(level, message string) {
		if strings.HasPrefix(message, "process started") && runtimeHost.Generation() == 3 {
			close(entered)
			<-resume
		}
	}, nil)
	defer func() { _ = runtimeHost.forceStop(context.Background()) }()

	if generation := runtimeHost.Generation(); generation != 0 {
		t.Fatalf("initial generation = %d, want 0", generation)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := runtimeHost.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if generation := runtimeHost.Generation(); generation != 1 {
		t.Fatalf("first generation = %d, want 1", generation)
	}
	result, err := runtimeHost.Invoke(ctx, "run", "")
	if err != nil {
		t.Fatal(err)
	}
	if result != `{"ok":true}` {
		t.Fatalf("unexpected result %s", result)
	}
	uiHost := newPluginUIHost()
	defer uiHost.Shutdown()
	record := &pluginRecord{id: manifest.ID, enabled: true, state: pluginStateRunning, runtime: runtimeHost, manifest: &manifest}
	manager := &pluginManager{records: map[string]*pluginRecord{record.id: record}, uiHost: uiHost}
	session, err := uiHost.Begin(record.id, "view", 1, root, "ui/index.html", "tango://app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := manager.InvokeUI(ctx, session.SessionID, session.Nonce, "run", ""); err != nil || got != `{"ok":true}` {
		t.Fatalf("real runtime UI invocation: %q, %v", got, err)
	}
	if err := runtimeHost.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if generation := runtimeHost.Generation(); generation != 1 {
		t.Fatalf("stopped generation = %d, want 1", generation)
	}
	if err := runtimeHost.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if generation := runtimeHost.Generation(); generation != 2 {
		t.Fatalf("restarted generation = %d, want 2", generation)
	}
	if _, err := runtimeHost.InvokeGeneration(ctx, 1, "run", ""); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("stale runtime generation accepted: %v", err)
	}
	if _, err := manager.InvokeUI(ctx, session.SessionID, session.Nonce, "run", ""); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale UI session accepted: %v", err)
	}
	runtimeHost.mu.Lock()
	replacement, replacementContext := runtimeHost.cmd, runtimeHost.runContext
	runtimeHost.mu.Unlock()
	if err := runtimeHost.ForceStopGeneration(ctx, 1); err != nil {
		t.Fatalf("stale generation cleanup: %v", err)
	}
	runtimeHost.mu.Lock()
	if runtimeHost.cmd != replacement || runtimeHost.stopping || replacementContext.Err() != nil {
		runtimeHost.mu.Unlock()
		t.Fatal("stale cleanup mutated replacement")
	}
	runtimeHost.mu.Unlock()
	if got, err := runtimeHost.InvokeGeneration(ctx, 2, "run", ""); err != nil || got != `{"ok":true}` {
		t.Fatalf("current generation invocation: %q, %v", got, err)
	}
	if err := runtimeHost.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	// Pause startup after publishing generation 3 but before initialization.
	// Its delayed initialization failure must not kill generation 4 or change
	// the replacement's stopping/handshake state.
	started := make(chan error, 1)
	go func() { started <- runtimeHost.Start(ctx) }()
	defer closeOnce(resume)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("startup did not reach barrier")
	}
	if err := runtimeHost.ForceStopGeneration(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if err := runtimeHost.Start(ctx); err != nil {
		t.Fatalf("replacement startup: %v", err)
	}
	close(resume)
	select {
	case err := <-started:
		if err == nil || !strings.Contains(err.Error(), "generation changed") {
			t.Fatalf("stale initialization: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stale initialization cleanup blocked")
	}
	if got, err := runtimeHost.InvokeGeneration(ctx, 4, "run", ""); err != nil || got != `{"ok":true}` {
		t.Fatalf("replacement after delayed startup cleanup: %q, %v", got, err)
	}
	if err := runtimeHost.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestProcessRuntimeStopKillsProcessWhenContextIsCanceled(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	entry := "plugin-helper"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	entryPath := filepath.Join(root, entry)
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, data, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		APIVersion:   api.APIVersion,
		ID:           "runtime.ignore-shutdown",
		Name:         "Runtime Stop Test",
		Version:      "1.0.0",
		Capabilities: []api.Capability{api.CapabilityStorage},
		Runtime:      ProcessRuntimeManifest{Type: "process", Entry: entry, Args: []string{"-test.run=TestPluginHelperProcess"}},
	}
	runtimeHost := newProcessRuntime(root, manifest, nil, nil)
	defer func() {
		_ = runtimeHost.forceStop(context.Background())
	}()
	startCtx, startCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer startCancel()
	if err := runtimeHost.Start(startCtx); err != nil {
		t.Fatal(err)
	}

	stopCtx, stopCancel := context.WithCancel(context.Background())
	stopCancel()
	if err := runtimeHost.Stop(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop error = %v, want context canceled", err)
	}
	runtimeHost.mu.Lock()
	running := runtimeHost.cmd != nil
	runtimeHost.mu.Unlock()
	if running {
		t.Fatal("plugin process is still registered as running after canceled Stop")
	}
}
