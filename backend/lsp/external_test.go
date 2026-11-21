package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"mincode/app/backend/core"
)

func externalTestSession(t *testing.T, root string) *session {
	t.Helper()
	canonical, err := canonicalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	spec := &ExternalServerSpec{
		ServerID: "plugin:acme.lsp/demo", OwnerPluginID: "acme.lsp", Name: "Demo",
		InitializationOptions: json.RawMessage(`{"lint":true}`),
		MessageMaxBytes:       1024,
	}
	return &session{root: canonical, definition: serverDefinition{ID: spec.ServerID}, pending: map[uint64]chan rpcReply{}, external: spec}
}

func TestProjectLSPStartRejectsPluginServers(t *testing.T) {
	root := t.TempDir()
	args, _ := json.Marshal(map[string]string{"root": root, "serverId": "plugin:acme.lsp/demo"})
	if _, err := startHandler("", string(args)); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") || !strings.Contains(err.Error(), "plugin_host_language_server_start") {
		t.Fatalf("project_lsp_start must refuse plugin servers, got %v", err)
	}
	install, _ := json.Marshal(map[string]string{"serverId": "plugin:acme.lsp/demo"})
	if _, err := installHandler("", string(install)); err == nil || !strings.HasPrefix(err.Error(), "forbidden:") {
		t.Fatalf("project_lsp_install must refuse plugin servers, got %v", err)
	}
	// A plugin id that is not running is simply not found.
	if _, err := findSession(messageArgs{Root: root, ServerID: "plugin:acme.lsp/demo"}); err == nil {
		t.Fatal("expected no live plugin session")
	}
	if _, err := findSession(messageArgs{Root: root, ServerID: "plugin:../x"}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("malformed plugin id must be rejected, got %v", err)
	}
	if _, err := StartExternal(root, ExternalServerSpec{ServerID: "plugin:other/demo", OwnerPluginID: "acme.lsp", Executable: "/bin/true"}); err == nil {
		t.Fatal("a server id of another owner must be rejected")
	}
	if _, err := StartExternal(root, ExternalServerSpec{ServerID: "plugin:acme.lsp/demo", OwnerPluginID: "acme.lsp", Executable: "node"}); err == nil {
		t.Fatal("an unresolved executable must be rejected")
	}
}

func TestExternalInitializeIsRewritten(t *testing.T) {
	root := t.TempDir()
	active := externalTestSession(t, root)
	params := json.RawMessage(`{"processId":1,"rootUri":"file:///","rootPath":"/","workspaceFolders":[{"uri":"file:///etc","name":"etc"}],"initializationOptions":{"evil":true},"capabilities":{"x":1}}`)
	out, err := confineExternalMessage(active, root, "initialize", params)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}
	// The client's spelling of the same directory is kept (a symlinked path).
	wantURI := fileURI(filepath.Clean(root))
	if string(fields["rootUri"]) != `"`+wantURI+`"` {
		t.Fatalf("rootUri = %s, want %s", fields["rootUri"], wantURI)
	}
	var folders []map[string]string
	_ = json.Unmarshal(fields["workspaceFolders"], &folders)
	if len(folders) != 1 || folders[0]["uri"] != wantURI {
		t.Fatalf("workspaceFolders = %s", fields["workspaceFolders"])
	}
	if string(fields["initializationOptions"]) != `{"lint":true}` {
		t.Fatalf("initializationOptions = %s", fields["initializationOptions"])
	}
	if string(fields["capabilities"]) != `{"x":1}` || string(fields["processId"]) != "1" {
		t.Fatalf("client fields must be kept: %s", out)
	}
	if _, err := confineExternalMessage(active, root, "initialize", json.RawMessage(`[1]`)); err == nil {
		t.Fatal("non-object initialize params must be refused")
	}
	// Built-in sessions pass through untouched.
	builtin := &session{root: active.root}
	if out, err := confineExternalMessage(builtin, root, "initialize", params); err != nil || string(out) != string(params) {
		t.Fatalf("built-in initialize changed: %s %v", out, err)
	}
}

func TestExternalURIsAreConfinedToTheRoot(t *testing.T) {
	root := t.TempDir()
	active := externalTestSession(t, root)
	inside := fileURI(filepath.Join(active.root, "src", "a.mc"))
	outside := fileURI(filepath.Join(filepath.Dir(active.root), "secret.txt"))
	open := func(uri string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "mcdemo", "version": 1, "text": ""}})
		return raw
	}
	if _, err := confineExternalMessage(active, root, "textDocument/didOpen", open(inside)); err != nil {
		t.Fatalf("inside URI refused: %v", err)
	}
	for _, uri := range []string{outside, "untitled:Untitled-1", "file://remote/share/a", fileURI(active.root) + "/../x"} {
		if _, err := confineExternalMessage(active, root, "textDocument/didOpen", open(uri)); err == nil {
			t.Fatalf("URI %q must be refused", uri)
		}
	}
	if _, err := confineExternalMessage(active, root, "textDocument/didClose", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a document notification without textDocument.uri must be refused")
	}
	if _, err := confineExternalMessage(active, root, "workspace/didChangeWorkspaceFolders", json.RawMessage(`{"event":{"added":[],"removed":[]}}`)); err == nil {
		t.Fatal("workspace folder changes must be refused")
	}
	nested, _ := json.Marshal(map[string]any{"textDocument": map[string]any{"uri": inside}, "items": []any{map[string]any{"targetUri": outside}}})
	if _, err := confineExternalMessage(active, root, "textDocument/definition", nested); err == nil {
		t.Fatal("a nested out-of-root URI must be refused")
	}
	fine, _ := json.Marshal(map[string]any{"textDocument": map[string]any{"uri": inside}, "position": map[string]int{"line": 0, "character": 0}})
	if _, err := confineExternalMessage(active, root, "textDocument/hover", fine); err != nil {
		t.Fatalf("hover refused: %v", err)
	}
	if runtime.GOOS != "windows" {
		// A symlinked spelling of the same project is accepted.
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(active.root, link); err != nil {
			t.Fatal(err)
		}
		if _, err := confineExternalMessage(active, link, "textDocument/didOpen", open(fileURI(filepath.Join(link, "a.mc")))); err != nil {
			t.Fatalf("symlinked root spelling refused: %v", err)
		}
	}
}

func TestExternalMessageCap(t *testing.T) {
	active := externalTestSession(t, t.TempDir())
	var sink bytes.Buffer
	active.stdin = &sink
	if err := active.notify("x", json.RawMessage(`"`+strings.Repeat("a", 2048)+`"`)); err == nil {
		t.Fatal("an outbound frame over the session cap must be refused")
	}
	var frame bytes.Buffer
	if err := writeMessage(&frame, map[string]string{"s": strings.Repeat("b", 2048)}); err != nil {
		t.Fatal(err)
	}
	if _, err := readMessageLimit(bufio.NewReader(bytes.NewReader(frame.Bytes())), active.messageLimit()); err == nil {
		t.Fatal("an inbound frame over the session cap must be refused")
	}
	buffer := &limitedBuffer{max: 8}
	_, _ = buffer.Write([]byte("0123456789abcdef"))
	if buffer.String() != "89abcdef" {
		t.Fatalf("stderr cap kept %q", buffer.String())
	}
}

func TestStopExternalForOwner(t *testing.T) {
	var mu sync.Mutex
	events := []serverEvent{}
	core.SetEmitter(func(name string, payload any) {
		if event, ok := payload.(serverEvent); ok && name == serverEventName {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	root := t.TempDir()
	mine := externalTestSession(t, root)
	mine.key = sessionKey(mine.root, mine.external.ServerID)
	other := externalTestSession(t, root)
	other.external = &ExternalServerSpec{ServerID: "plugin:other.lsp/demo", OwnerPluginID: "other.lsp"}
	other.definition.ID = other.external.ServerID
	other.key = sessionKey(other.root, other.external.ServerID)
	manager.Lock()
	manager.sessions[mine.key] = mine
	manager.sessions[other.key] = other
	manager.Unlock()
	t.Cleanup(func() {
		manager.Lock()
		delete(manager.sessions, mine.key)
		delete(manager.sessions, other.key)
		manager.Unlock()
	})
	stopped := StopExternalForOwner("acme.lsp", "disabled")
	if len(stopped) != 1 || stopped[0] != "plugin:acme.lsp/demo" || !mine.closed.Load() || other.closed.Load() {
		t.Fatalf("stopped %v, mine closed %v, other closed %v", stopped, mine.closed.Load(), other.closed.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].Method != "$/serverStopped" || !strings.Contains(string(events[0].Params), `"final":true`) {
		t.Fatalf("events = %+v", events)
	}
	if _, err := findSession(messageArgs{Root: root, ServerID: "plugin:acme.lsp/demo"}); err == nil {
		t.Fatal("a stopped plugin session must not be reachable")
	}
}

func TestStartExternalProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/cat")
	}
	core.SetEmitter(func(string, any) {})
	t.Cleanup(func() { core.SetEmitter(nil) })
	root := t.TempDir()
	refused := errors.New("changed: plugin file changed")
	if _, err := StartExternal(root, ExternalServerSpec{
		ServerID: "plugin:acme.lsp/demo", OwnerPluginID: "acme.lsp", Executable: "/bin/cat",
		BeforeExec: func() error { return refused },
	}); !errors.Is(err, refused) {
		t.Fatalf("BeforeExec refusal must stop the start, got %v", err)
	}
	t.Cleanup(func() { StopExternalForOwner("acme.lsp", "test") })
	spec := ExternalServerSpec{
		ServerID: "plugin:acme.lsp/demo", OwnerPluginID: "acme.lsp", Name: "Demo", Executable: "/bin/cat",
		Env: map[string]string{"MCDEMO_LOG": "warn"}, Fingerprint: "f1", MaxSessionsPerOwner: 1,
	}
	status, err := StartExternal(root, spec)
	if err != nil || !status.Running || status.ID != spec.ServerID {
		t.Fatalf("start: %+v %v", status, err)
	}
	again, err := StartExternal(root, spec)
	if err != nil || again.Generation != status.Generation {
		t.Fatalf("an identical start must reuse the session: %+v %v", again, err)
	}
	second := spec
	second.ServerID = "plugin:acme.lsp/other"
	if _, err := StartExternal(root, second); err == nil || !strings.HasPrefix(err.Error(), "limit:") {
		t.Fatalf("the per-plugin session limit must apply, got %v", err)
	}
	changed := spec
	changed.Fingerprint = "f2"
	replaced, err := StartExternal(root, changed)
	if err != nil || replaced.Generation == status.Generation {
		t.Fatalf("a changed launch must replace the session: %+v %v", replaced, err)
	}
	if got := core.MergeEnv([]string{"A=1", "MCDEMO_LOG=x"}, map[string]string{"MCDEMO_LOG": "warn"}); strings.Join(got, ",") != "A=1,MCDEMO_LOG=warn" {
		t.Fatalf("merged env = %v", got)
	}
}

func TestBuiltinLanguagesSkipsAuxiliaryServers(t *testing.T) {
	languages := BuiltinLanguages()
	if len(languages["go"]) == 0 || languages["go"][0] != "gopls" {
		t.Fatalf("go = %v", languages["go"])
	}
	for _, ids := range languages {
		for _, id := range ids {
			definition, _ := definitionByID(id)
			if definition.Auxiliary {
				t.Fatalf("auxiliary server %s listed", id)
			}
		}
	}
}

func TestInvalidExternalSessionIsRetired(t *testing.T) {
	var mu sync.Mutex
	events := []serverEvent{}
	core.SetEmitter(func(name string, payload any) {
		if event, ok := payload.(serverEvent); ok && name == serverEventName {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	root := t.TempDir()
	active := externalTestSession(t, root)
	valid := true
	active.external.Valid = func() bool { return valid }
	active.external.Fingerprint = "fp"
	active.key = sessionKey(active.root, active.external.ServerID)
	manager.Lock()
	manager.sessions[active.key] = active
	manager.Unlock()
	t.Cleanup(func() { StopExternalForOwner("acme.lsp", "test") })
	if !ExternalSessionLive(root, active.external.ServerID, "fp") || ExternalSessionLive(root, active.external.ServerID, "other") {
		t.Fatal("ExternalSessionLive must match the fingerprint")
	}
	if _, err := findSession(messageArgs{Root: root, ServerID: active.external.ServerID}); err != nil {
		t.Fatalf("a valid session must be found: %v", err)
	}
	valid = false
	if _, err := findSession(messageArgs{Root: root, ServerID: active.external.ServerID}); err == nil || !strings.HasPrefix(err.Error(), "unavailable:") {
		t.Fatalf("an invalid owner must retire the session, got %v", err)
	}
	if !active.closed.Load() || ExternalSessionLive(root, active.external.ServerID, "fp") {
		t.Fatal("the retired session must be closed and removed")
	}
	mu.Lock()
	if len(events) != 1 || !strings.Contains(string(events[0].Params), `"plugin-unavailable"`) {
		t.Fatalf("events = %+v", events)
	}
	mu.Unlock()

	again := externalTestSession(t, root)
	again.key = sessionKey(again.root, again.external.ServerID)
	manager.Lock()
	manager.sessions[again.key] = again
	manager.Unlock()
	if !StopExternal(root, again.external.ServerID, "restart") || !again.closed.Load() || StopExternal(root, again.external.ServerID, "restart") {
		t.Fatal("StopExternal must stop the live session once")
	}
	if StopExternal(root, "gopls", "restart") {
		t.Fatal("StopExternal must ignore built-in servers")
	}
}
