package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/core"
)

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestProtocolRejectsShortWritesAndUnboundedHeaders(t *testing.T) {
	if err := writeFrameBytes(shortWriter{}, []byte(`{}`)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write: %v", err)
	}
	for _, input := range []string{
		"Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}",
		"X: " + strings.Repeat("a", 9000) + "\r\nContent-Length: 2\r\n\r\n{}",
	} {
		if _, err := readMessage(bufio.NewReader(strings.NewReader(input))); err == nil {
			t.Fatal("unsafe header accepted")
		}
	}
}

func TestStringServerRequestIDRoundtrip(t *testing.T) {
	var output bytes.Buffer
	s := &session{stdin: &output, pending: map[uint64]chan rpcReply{}}
	var event serverEvent
	core.SetEmitter(func(_ string, payload any) { event = payload.(serverEvent) })
	t.Cleanup(func() { core.SetEmitter(nil) })
	var input bytes.Buffer
	_ = writeMessage(&input, map[string]any{"jsonrpc": "2.0", "id": "server-config-1", "method": "workspace/configuration", "params": map[string]any{}})
	s.readLoop(&input)
	if event.RequestID == nil {
		t.Fatal("string request was dropped")
	}
	if err := s.reply(*event.RequestID, json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	raw, err := readMessage(bufio.NewReader(&output))
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]json.RawMessage
	_ = json.Unmarshal(raw, &message)
	if string(message["id"]) != `"server-config-1"` {
		t.Fatalf("wrong wire id: %s", raw)
	}
}

func TestProtocolFailureClosesAndNotifiesOnce(t *testing.T) {
	s := &session{pending: map[uint64]chan rpcReply{}}
	var stopped int
	core.SetEmitter(func(_ string, payload any) {
		if payload.(serverEvent).Method == "$/serverStopped" {
			stopped++
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	s.readLoop(strings.NewReader("Content-Length: broken\r\n\r\n"))
	s.fail(errors.New("second failure"))
	if !s.closed.Load() || stopped != 1 || s.failureText() == "" {
		t.Fatalf("closed=%v stopped=%d error=%s", s.closed.Load(), stopped, s.failureText())
	}
	if !strings.Contains(s.logs(), "error reading protocol frame") {
		t.Fatal(s.logs())
	}
}

func TestLifecycleAndMethodLogsWithoutPayload(t *testing.T) {
	var output bytes.Buffer
	s := &session{stdin: &output, pending: map[uint64]chan rpcReply{}}
	s.log("lifecycle process started; awaiting initialize")
	secret := json.RawMessage(`{"text":"SECRET_SOURCE","uri":"file:///private/secret"}`)
	if err := s.notify("textDocument/didOpen", secret); err != nil {
		t.Fatal(err)
	}
	if err := s.notify("evil\nSECRET_SOURCE", secret); err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	_ = writeMessage(&input, map[string]any{"method": "window/logMessage", "params": map[string]any{"message": "SECRET_SOURCE"}})
	s.readLoop(&input)
	logs := s.logs()
	for _, want := range []string{"awaiting initialize", "out textDocument/didOpen", "in window/logMessage", "[redacted-method]"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("missing %s: %s", want, logs)
		}
	}
	if strings.Contains(logs, "SECRET_SOURCE") || strings.Contains(logs, "private/secret") {
		t.Fatal("payload leaked")
	}
	for i := 0; i < 5000; i++ {
		s.logMethod("in", "textDocument/publishDiagnostics")
	}
	if len(s.trace.String()) > maxStderrBytes {
		t.Fatal("unbounded trace")
	}
}

func TestNeverStartedHasEmptyLogs(t *testing.T) {
	root := t.TempDir()
	args, _ := json.Marshal(startArgs{Root: root, ServerID: "gopls"})
	logs, err := logsHandler("", string(args))
	if err != nil || !strings.Contains(logs, `"output":""`) || !strings.Contains(logs, `"running":false`) {
		t.Fatalf("logs=%s err=%v", logs, err)
	}
}

func TestFailedStartRemainsObservable(t *testing.T) {
	root := t.TempDir()
	canonical, _ := canonicalRoot(root)
	command := filepath.Join(root, "missing-executable")
	definition := serverDefinition{ID: "test-missing-ls", Name: "Missing", Command: command}
	serverDefinitions = append(serverDefinitions, definition)
	t.Cleanup(func() {
		serverDefinitions = serverDefinitions[:len(serverDefinitions)-1]
		manager.Lock()
		delete(manager.sessions, sessionKey(canonical, definition.ID))
		manager.Unlock()
	})
	if _, err := start(root, definition.ID); err == nil {
		t.Fatal("missing executable started")
	}
	args, _ := json.Marshal(startArgs{Root: root, ServerID: definition.ID})
	logs, err := logsHandler("", string(args))
	if err != nil || !strings.Contains(logs, "process start failed") {
		t.Fatalf("logs=%s err=%v", logs, err)
	}
}

func TestExpandedEnvPreservesUserPathPriority(t *testing.T) {
	t.Setenv("PATH", filepath.Join(t.TempDir(), "custom-bin"))
	want := os.Getenv("PATH")
	for _, entry := range core.ExpandedEnv() {
		if strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "PATH="+want+string(os.PathListSeparator)) {
			t.Fatalf("user PATH priority changed: %s", entry)
		}
	}
}

func TestRegisteredLaunchModes(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range serverDefinitions {
		if seen[d.ID] || d.Command == "" || len(d.Languages) == 0 {
			t.Fatalf("invalid definition %+v", d)
		}
		seen[d.ID] = true
		for platform, c := range d.InstallOS {
			if c.Installable && c.InstallHint == "" {
				t.Fatalf("%s/%s lacks installer", d.ID, platform)
			}
		}
	}
	for id, want := range map[string]string{"taplo": "lsp stdio", "perlnavigator": "--stdio", "bash-ls": "start", "terraform-ls": "serve", "marksman": "server", "dart-ls": "language-server"} {
		d, _ := definitionByID(id)
		if strings.Join(d.Arguments, " ") != want {
			t.Fatalf("%s arguments=%v", id, d.Arguments)
		}
	}
	root := t.TempDir()
	modules := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(modules, 0755); err != nil {
		t.Fatal(err)
	}
	args := angularProbeArguments(root)
	if len(args) != 4 || args[1] != modules || args[3] != modules {
		t.Fatalf("angular probes=%v", args)
	}
}

// A real subprocess exercises framing, initialize, quiet stderr and exit cleanup
// without installing servers or depending on external tools.
func TestLSPSubprocess(t *testing.T) {
	if os.Getenv("MINCODE_LSP_TEST_HELPER") == "1" {
		reader := bufio.NewReader(os.Stdin)
		for {
			raw, err := readMessage(reader)
			if err != nil {
				return
			}
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(raw, &msg)
			if msg.Method == "exit" {
				os.Exit(0)
			}
			if len(msg.ID) > 0 {
				_ = writeMessage(os.Stdout, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"capabilities": map[string]any{}}})
			}
		}
	}
	core.SetEmitter(func(string, any) {})
	t.Cleanup(func() { core.SetEmitter(nil) })
	cmd := exec.Command(os.Args[0], "-test.run=^TestLSPSubprocess$")
	cmd.Env = append(os.Environ(), "MINCODE_LSP_TEST_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &session{cmd: cmd, stdin: stdin, stderr: &limitedBuffer{}, pending: map[uint64]chan rpcReply{}}
	cmd.Stderr = s.stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.close(errors.New("test cleanup")) })
	s.run(stdout)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = s.request(ctx, "initialize", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.logs(), "initialize response received") {
		t.Fatal(s.logs())
	}
	if err = s.notify("exit", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !s.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.closed.Load() {
		t.Fatal("exited process not retired")
	}
	if strings.Contains(s.failureText(), "%!w") {
		t.Fatal(s.failureText())
	}
}

// A whole frame whose body is not JSON is skipped; the session keeps reading
// and later responses still reach their requests, numeric or decimal-string id.
func TestInvalidBodySkippedAndStringResponseID(t *testing.T) {
	s := &session{pending: map[uint64]chan rpcReply{}}
	first, second := make(chan rpcReply, 1), make(chan rpcReply, 1)
	s.pending[1], s.pending[2] = first, second
	var input bytes.Buffer
	_ = writeFrameBytes(&input, []byte(`{not json`))
	_ = writeMessage(&input, map[string]any{"jsonrpc": "2.0", "id": 1, "result": "a"})
	_ = writeMessage(&input, map[string]any{"jsonrpc": "2.0", "id": "2", "result": "b"})
	s.readLoop(&input)
	for i, pending := range []chan rpcReply{first, second} {
		select {
		case reply := <-pending:
			if reply.err != nil {
				t.Fatalf("reply %d: %v", i+1, reply.err)
			}
		default:
			t.Fatalf("reply %d not delivered: %s", i+1, s.logs())
		}
	}
	if !strings.Contains(s.logs(), "invalid JSON payload skipped") {
		t.Fatal(s.logs())
	}
}
