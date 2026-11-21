package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
)

// ---------------------------------------------------------------------------
// protocol tests
// ---------------------------------------------------------------------------

func writeToReader(messages ...any) (*bufio.Reader, error) {
	buf := &bytes.Buffer{}
	for _, msg := range messages {
		if err := writeMessage(buf, msg); err != nil {
			return nil, err
		}
	}
	return bufio.NewReader(buf), nil
}

func TestReadWriteRoundtrip(t *testing.T) {
	msg := map[string]any{"jsonrpc": "2.0", "method": "test", "params": map[string]any{"key": "value"}}
	reader, err := writeToReader(msg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readMessage(reader)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(got, &result); err != nil {
		t.Fatal(err)
	}
	if result["method"] != "test" {
		t.Errorf("expected method 'test', got %v", result["method"])
	}
}

func TestReadMultipleMessages(t *testing.T) {
	reader, err := writeToReader(
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "first"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "second"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for i, expected := range []string{"first", "second"} {
		got, err := readMessage(reader)
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		var result map[string]any
		if err := json.Unmarshal(got, &result); err != nil {
			t.Fatalf("message %d unmarshal: %v", i, err)
		}
		if result["method"] != expected {
			t.Errorf("message %d: expected method %q, got %v", i, expected, result["method"])
		}
	}
}

func TestReadMultibyteUTF8(t *testing.T) {
	msg := map[string]any{"content": "日本語テスト 🎯"}
	reader, err := writeToReader(msg)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := readMessage(reader)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if result["content"] != "日本語テスト 🎯" {
		t.Errorf("unexpected content: %v", result["content"])
	}
}

func TestReadMissingContentLength(t *testing.T) {
	raw := "Content-Type: application/json\r\n\r\n{}"
	reader := bufio.NewReader(strings.NewReader(raw))
	_, err := readMessage(reader)
	if err == nil {
		t.Fatal("expected error for missing Content-Length")
	}
}

func TestReadInvalidContentLength(t *testing.T) {
	for _, header := range []string{"-1", "abc", fmt.Sprintf("%d", maxMessageBytes+1)} {
		raw := "Content-Length: " + header + "\r\n\r\n"
		reader := bufio.NewReader(strings.NewReader(raw))
		_, err := readMessage(reader)
		if err == nil {
			t.Errorf("expected error for Content-Length %s", header)
		}
	}
}

func TestReadInvalidJSON(t *testing.T) {
	body := []byte(`not-json`)
	var buf bytes.Buffer
	_, _ = fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)
	reader := bufio.NewReader(&buf)
	_, err := readMessage(reader)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestWriteExceedsLimit(t *testing.T) {
	huge := make([]byte, maxMessageBytes+1)
	err := writeMessage(io.Discard, json.RawMessage(huge))
	if err == nil {
		t.Fatal("expected error writing oversized message")
	}
}

// ---------------------------------------------------------------------------
// registry tests
// ---------------------------------------------------------------------------

func TestDefinitionByIDKnown(t *testing.T) {
	known := []string{
		"intelephense", "gopls", "ts-ls", "pyright", "rust-analyzer",
		"json-ls", "yaml-ls", "html-ls", "vue-ls", "css-ls", "bash-ls",
		"jdtls", "clangd", "lua-ls",
		"csharp-ls", "sqls", "r-languageserver", "ruby-lsp", "sourcekit-lsp", "perlnavigator",
		"marksman", "dockerfile-ls", "taplo",
		"graphql-lsp", "terraform-ls", "kotlin-ls", "svelte-ls", "prisma-ls",
		"dart-ls", "elixir-ls", "metals", "fsautocomplete",
		"zls", "haskell-ls", "ocamllsp", "julia-ls", "astro-ls", "protobuf-ls",
		"tailwind-ls", "angular-ls",
	}
	for _, id := range known {
		definition, ok := definitionByID(id)
		if !ok {
			t.Errorf("definitionByID(%q): not found", id)
			continue
		}
		if definition.ID != id {
			t.Errorf("definitionByID(%q): returned ID %q", id, definition.ID)
		}
		if definition.Command == "" {
			t.Errorf("definitionByID(%q): empty command", id)
		}
		if len(definition.Languages) == 0 {
			t.Errorf("definitionByID(%q): no languages", id)
		}
	}
}

func TestDefinitionByIDUnknown(t *testing.T) {
	_, ok := definitionByID("not-a-real-server")
	if ok {
		t.Error("expected false for unknown server ID")
	}
}

func TestRequireDefinitionUnknown(t *testing.T) {
	_, err := requireDefinition("injected-server")
	if err == nil {
		t.Fatal("expected error for unknown server ID")
	}
}

func TestHeavyServers(t *testing.T) {
	for _, definition := range serverDefinitions {
		if definition.ID == "jdtls" || definition.ID == "clangd" {
			if !definition.Heavy {
				t.Errorf("server %q should be marked Heavy", definition.ID)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// canonicalRoot tests
// ---------------------------------------------------------------------------

// Volar 拿不到 --tsdk 时会退回 require('typescript') 并直接启动失败，
// 因此这组用例锁住 tsdk 的解析行为。
func writeTypeScriptLib(t *testing.T, dir string) string {
	t.Helper()
	lib := filepath.Join(dir, "node_modules", "typescript", "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatalf("create lib dir: %v", err)
	}
	for _, name := range []string{"typescript.js", "tsserver.js"} {
		if err := os.WriteFile(filepath.Join(lib, name), []byte("// stub"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return lib
}

func TestFindTypeScriptLibInProjectRoot(t *testing.T) {
	root := t.TempDir()
	want := writeTypeScriptLib(t, root)
	if got := findTypeScriptLib(root); got != want {
		t.Errorf("findTypeScriptLib(%q) = %q, want %q", root, got, want)
	}
}

// monorepo：依赖提升到仓库根，子包目录里没有 node_modules。
func TestFindTypeScriptLibWalksUp(t *testing.T) {
	root := t.TempDir()
	want := writeTypeScriptLib(t, root)
	nested := filepath.Join(root, "packages", "app")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested dir: %v", err)
	}
	if got := findTypeScriptLib(nested); got != want {
		t.Errorf("findTypeScriptLib(%q) = %q, want %q", nested, got, want)
	}
}

// 只有目录、没有 typescript.js 时必须判否：传这种 --tsdk 会让 Volar 启动失败。
func TestIsTypeScriptLibRequiresEntryFile(t *testing.T) {
	dir := t.TempDir()
	if isTypeScriptLib(dir) {
		t.Error("empty directory must not be treated as a TypeScript lib")
	}
	if err := os.Mkdir(filepath.Join(dir, "typescript.js"), 0o755); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if isTypeScriptLib(dir) {
		t.Error("a directory named typescript.js must not count as the entry file")
	}
}

// Standalone Volar receives its SDK in initializationOptions, not CLI args.
func TestVueDefinitionSuppliesTsdk(t *testing.T) {
	definition, ok := definitionByID("vue-ls")
	if !ok {
		t.Fatal("vue-ls definition missing")
	}
	if definition.ExtraArguments != nil || definition.InstallHint != vueStandaloneInstall {
		t.Fatal("vue-ls must use pinned standalone installation and initializationOptions")
	}
	if len(definition.Languages) != 1 || definition.Languages[0] != "vue" {
		t.Errorf("vue-ls languages = %v, want [vue]", definition.Languages)
	}
}

// .vue 的主服务器只能是 Volar：主服务器声明 vue 会顶替 Volar，SFC 又会被
// 当成 HTML/内联 JS 校验。辅助型服务器（如 Tailwind，只叠加补全/悬停）例外，
// 多会话共存架构下它们与 Volar 并行接收同一文档，不互相顶替。
func TestNoOtherServerClaimsVue(t *testing.T) {
	for _, definition := range serverDefinitions {
		if definition.ID == "vue-ls" || definition.Auxiliary {
			continue
		}
		for _, language := range definition.Languages {
			if language == "vue" {
				t.Errorf("server %q must not claim the vue language", definition.ID)
			}
		}
	}
}

// 辅助型服务器存在的意义就是与主服务器共享语言；若它没被标记 Auxiliary，
// 前端会退回到单会话顶替行为，多会话共存无从谈起，这里把契约钉住。
func TestAuxiliaryServersMarked(t *testing.T) {
	for _, id := range []string{"tailwind-ls", "angular-ls"} {
		definition, ok := definitionByID(id)
		if !ok {
			t.Fatalf("%s definition missing", id)
		}
		if !definition.Auxiliary {
			t.Errorf("%s must be marked Auxiliary to coexist with primary servers", id)
		}
	}
}

// ---------------------------------------------------------------------------

func TestCanonicalRootEmpty(t *testing.T) {
	_, err := canonicalRoot("")
	if err == nil {
		t.Fatal("expected error for empty root")
	}
	_, err = canonicalRoot("   ")
	if err == nil {
		t.Fatal("expected error for whitespace-only root")
	}
}

func TestCanonicalRootNonexistent(t *testing.T) {
	_, err := canonicalRoot("/definitely/does/not/exist/12345")
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}
}

func TestCanonicalRootFile(t *testing.T) {
	t.TempDir() // ensure temp dir is created
	// create a temp file and pass it as root
	dir := t.TempDir()
	_, err := canonicalRoot(dir + "/nonexistentfile.txt")
	if err == nil {
		t.Fatal("expected error for file path instead of directory")
	}
}

func TestCanonicalRootValid(t *testing.T) {
	dir := t.TempDir()
	result, err := canonicalRoot(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty canonical path")
	}
}

// ---------------------------------------------------------------------------
// resolveProjectURI tests
// ---------------------------------------------------------------------------

func TestResolveProjectURIContained(t *testing.T) {
	root := t.TempDir()
	uri := "file://" + root + "/src/main.go"
	_, err := resolveProjectURI(root, uri)
	if err != nil {
		t.Errorf("unexpected error for contained path: %v", err)
	}
}

func TestResolveProjectURIEscape(t *testing.T) {
	root := t.TempDir()
	uri := "file://" + root + "/../etc/passwd"
	_, err := resolveProjectURI(root, uri)
	if err == nil {
		t.Fatal("expected error for path escaping project root")
	}
}

func TestResolveProjectURIParent(t *testing.T) {
	root := t.TempDir()
	uri := "file://" + root + "/.."
	_, err := resolveProjectURI(root, uri)
	if err == nil {
		t.Fatal("expected error for parent directory path")
	}
}

func TestResolveProjectURINoFileScheme(t *testing.T) {
	root := t.TempDir()
	_, err := resolveProjectURI(root, root+"/src/main.go")
	if err == nil {
		t.Fatal("expected error for non-file URI")
	}
	_, err = resolveProjectURI(root, "http://example.com/file.go")
	if err == nil {
		t.Fatal("expected error for http URI")
	}
}

// ---------------------------------------------------------------------------
// content-length byte-accuracy test
// ---------------------------------------------------------------------------

func TestContentLengthIsUTF8Bytes(t *testing.T) {
	// Multibyte character "日" is 3 bytes in UTF-8 but 1 rune.
	// Content-Length must count bytes, not runes.
	msg := map[string]string{"s": "日日日"}
	var buf bytes.Buffer
	if err := writeMessage(&buf, msg); err != nil {
		t.Fatal(err)
	}
	written := buf.String()
	// Extract the declared Content-Length from the header
	header := strings.Split(written, "\r\n\r\n")[0]
	var declared int
	_, err := fmt.Sscanf(strings.TrimPrefix(header, "Content-Length: "), "%d", &declared)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(strings.SplitN(written, "\r\n\r\n", 2)[1])
	if len(body) != declared {
		t.Errorf("Content-Length %d ≠ actual body bytes %d", declared, len(body))
	}
}

// ---------------------------------------------------------------------------
// readLoop routing tests
// ---------------------------------------------------------------------------

// readLoopMessages feeds a slice of raw LSP messages through a session's
// readLoop and returns the events that were emitted.
func captureEvents(t *testing.T, messages []any) []serverEvent {
	t.Helper()
	var (
		mu     sync.Mutex
		events []serverEvent
	)
	core.SetEmitter(func(name string, payload any) {
		if name != serverEventName {
			return
		}
		// payload arrives as serverEvent value; re-encode then decode to copy safely.
		b, _ := json.Marshal(payload)
		var evt serverEvent
		if err := json.Unmarshal(b, &evt); err == nil {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })

	var buf bytes.Buffer
	for _, m := range messages {
		if err := writeMessage(&buf, m); err != nil {
			t.Fatalf("writeMessage: %v", err)
		}
	}
	s := &session{
		definition: serverDefinition{ID: "test-ls"},
		pending:    map[uint64]chan rpcReply{},
	}
	s.readLoop(&buf)
	return events
}

// Notification (id absent, method present): must emit event with no RequestID.
func TestReadLoopNotification(t *testing.T) {
	msgs := []any{
		map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": "file:///a.go"}},
	}
	events := captureEvents(t, msgs)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Method != "textDocument/publishDiagnostics" {
		t.Errorf("wrong method: %q", events[0].Method)
	}
	if events[0].RequestID != nil {
		t.Errorf("notification must not set RequestID")
	}
}

// Server-to-client request (id + method both present): must emit event with RequestID set.
// The pending map must NOT have this id (it was never a client-initiated request).
func TestReadLoopServerRequest(t *testing.T) {
	id := uint64(99)
	msgs := []any{
		map[string]any{"jsonrpc": "2.0", "id": id, "method": "workspace/configuration", "params": map[string]any{"items": []any{}}},
	}
	events := captureEvents(t, msgs)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Method != "workspace/configuration" {
		t.Errorf("wrong method: %q", events[0].Method)
	}
	if events[0].RequestID == nil || *events[0].RequestID != id {
		t.Errorf("RequestID = %v, want %d", events[0].RequestID, id)
	}
}

// Response to a client request (id present, method absent): must be delivered
// to the waiting channel, not emitted as an event.
func TestReadLoopResponseDelivered(t *testing.T) {
	id := uint64(42)
	ch := make(chan rpcReply, 1)
	s := &session{
		definition: serverDefinition{ID: "test-ls"},
		pending:    map[uint64]chan rpcReply{id: ch},
	}
	var buf bytes.Buffer
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"ok": true}}
	if err := writeMessage(&buf, msg); err != nil {
		t.Fatal(err)
	}

	var emitted int
	core.SetEmitter(func(_ string, _ any) { emitted++ })
	defer core.SetEmitter(nil)

	s.readLoop(&buf)

	if emitted != 0 {
		t.Errorf("response must not emit an event, got %d events", emitted)
	}
	select {
	case reply := <-ch:
		if reply.err != nil {
			t.Errorf("unexpected error: %v", reply.err)
		}
	default:
		t.Error("response was not delivered to pending channel")
	}
}

// reply() must write a valid JSON-RPC 2.0 response frame.
func TestSessionReply(t *testing.T) {
	var buf bytes.Buffer
	s := &session{stdin: &buf}
	result := json.RawMessage(`{"applied":true}`)
	if err := s.reply(7, result); err != nil {
		t.Fatalf("reply: %v", err)
	}
	payload, err := readMessage(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	var got struct {
		Jsonrpc string          `json:"jsonrpc"`
		ID      uint64          `json:"id"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Jsonrpc != "2.0" || got.ID != 7 {
		t.Errorf("unexpected response: %+v", got)
	}
	if string(got.Result) != `{"applied":true}` {
		t.Errorf("result = %s", got.Result)
	}
}

// reply() with nil result must send "null", not an empty body.
func TestSessionReplyNullResult(t *testing.T) {
	var buf bytes.Buffer
	s := &session{stdin: &buf}
	if err := s.reply(1, nil); err != nil {
		t.Fatalf("reply: %v", err)
	}
	payload, err := readMessage(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["result"]) != "null" {
		t.Errorf("result = %s, want null", got["result"])
	}
}

// High-frequency notifications are coalesced (latest wins per key), unconsumed
// log notifications are dropped, and immediate events never overtake earlier
// coalesced ones.
func TestReadLoopCoalescesNotifications(t *testing.T) {
	id := uint64(7)
	msgs := []any{
		map[string]any{"jsonrpc": "2.0", "method": "$/progress", "params": map[string]any{"token": "t1", "value": map[string]any{"kind": "begin"}}},
		map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": "file:///a.go", "diagnostics": []any{1}}},
		map[string]any{"jsonrpc": "2.0", "method": "window/logMessage", "params": map[string]any{"type": 3, "message": "noise"}},
		map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": "file:///b.go", "diagnostics": []any{}}},
		map[string]any{"jsonrpc": "2.0", "method": "$/progress", "params": map[string]any{"token": "t1", "value": map[string]any{"kind": "end"}}},
		map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": "file:///a.go", "diagnostics": []any{2}}},
		map[string]any{"jsonrpc": "2.0", "id": id, "method": "workspace/configuration", "params": map[string]any{"items": []any{}}},
		map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": "file:///a.go", "diagnostics": []any{3}}},
	}
	events := captureEvents(t, msgs)
	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.Method+" "+string(event.Params))
	}
	want := []string{
		`$/progress {"token":"t1","value":{"kind":"end"}}`,
		`textDocument/publishDiagnostics {"diagnostics":[2],"uri":"file:///a.go"}`,
		`textDocument/publishDiagnostics {"diagnostics":[],"uri":"file:///b.go"}`,
		`workspace/configuration {"items":[]}`,
		`textDocument/publishDiagnostics {"diagnostics":[3],"uri":"file:///a.go"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Coalesced events are flushed by the timer when the stream stays open.
func TestCoalescedEventsFlushAfterWindow(t *testing.T) {
	flushed := make(chan serverEvent, 4)
	core.SetEmitter(func(name string, payload any) {
		if event, ok := payload.(serverEvent); ok && name == serverEventName {
			flushed <- event
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	s := &session{definition: serverDefinition{ID: "test-ls"}}
	for _, value := range []string{"1", "2"} {
		s.forward(serverEvent{Method: "$/progress", Params: json.RawMessage(`{"token":1,"value":` + value + `}`)})
	}
	select {
	case event := <-flushed:
		if string(event.Params) != `{"token":1,"value":2}` {
			t.Fatalf("flushed %s, want latest progress", event.Params)
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced progress never flushed")
	}
	select {
	case event := <-flushed:
		t.Fatalf("unexpected extra event %s", event.Params)
	case <-time.After(3 * eventCoalesceWindow):
	}
}
