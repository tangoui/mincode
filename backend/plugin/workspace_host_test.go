package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

// useWorkspaceRoot points host.workspace.* at a fresh canonical temp root.
func useWorkspaceRoot(t *testing.T) string {
	t.Helper()
	root, err := canonicalWorkspaceRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	previous := pluginWorkspaceRoot
	pluginWorkspaceRoot = func() string { return root }
	t.Cleanup(func() { pluginWorkspaceRoot = previous })
	return root
}

func writeWorkspaceFile(t *testing.T, root, relative, content string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func workspaceCall(t *testing.T, handler hostMethodHandler, params any) (map[string]any, *rpcError) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	result, rpcErr := handler(nil, raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	encoded, _ := json.Marshal(result)
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded, nil
}

func workspaceErrorCode(rpcErr *rpcError) string {
	if rpcErr == nil {
		return ""
	}
	if data, ok := rpcErr.Data.(map[string]string); ok {
		return data["code"]
	}
	return ""
}

func TestWorkspacePathPolicy(t *testing.T) {
	for _, tc := range []struct {
		path        string
		read, write bool
	}{
		{"src/main.go", true, true},
		{"", true, true},
		{".git/config", false, false},
		{"sub/.Git/hooks/pre-commit", false, false},
		{".env", false, false},
		{"config/id_rsa", false, false},
		{"node_modules/pkg/index.js", true, false},
		{".mincode/state.json", true, false},
		{"docs/.envrc", true, true},
	} {
		if got := !workspacePathDenied(tc.path, false); got != tc.read {
			t.Errorf("read %q = %v, want %v", tc.path, got, tc.read)
		}
		if got := !workspacePathDenied(tc.path, true); got != tc.write {
			t.Errorf("write %q = %v, want %v", tc.path, got, tc.write)
		}
	}
}

func TestCanonicalWorkspaceRoot(t *testing.T) {
	if root, err := canonicalWorkspaceRoot("  "); err != nil || root != "" {
		t.Fatalf("empty root = %q %v", root, err)
	}
	if _, err := canonicalWorkspaceRoot("relative/dir"); err == nil {
		t.Fatal("relative root accepted")
	}
	if _, err := canonicalWorkspaceRoot(string(filepath.Separator)); err == nil {
		t.Fatal("filesystem root accepted")
	}
	if _, err := canonicalWorkspaceRoot(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing root accepted")
	}
}

func TestWorkspaceMethodsRequireRoot(t *testing.T) {
	previous := pluginWorkspaceRoot
	pluginWorkspaceRoot = func() string { return "" }
	t.Cleanup(func() { pluginWorkspaceRoot = previous })
	if _, rpcErr := workspaceCall(t, hostWorkspaceRead, map[string]any{"path": "a.txt"}); workspaceErrorCode(rpcErr) != "no_root" {
		t.Fatalf("read without root = %+v", rpcErr)
	}
	result, rpcErr := workspaceCall(t, hostWorkspaceRoot, nil)
	if rpcErr != nil || result["root"] != nil {
		t.Fatalf("root = %v %+v", result, rpcErr)
	}
}

func TestWorkspaceListStatRead(t *testing.T) {
	root := useWorkspaceRoot(t)
	writeWorkspaceFile(t, root, "src/main.go", "package main\n")
	writeWorkspaceFile(t, root, ".git/config", "[remote]\n")
	writeWorkspaceFile(t, root, ".env", "TOKEN=x\n")
	writeWorkspaceFile(t, root, "README.md", strings.Repeat("r", 1000))

	listing, rpcErr := workspaceCall(t, hostWorkspaceList, map[string]any{})
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	var names []string
	for _, entry := range listing["entries"].([]any) {
		names = append(names, entry.(map[string]any)["path"].(string))
	}
	if strings.Join(names, ",") != "README.md,src" {
		t.Fatalf("root listing = %v", names)
	}
	if _, rpcErr := workspaceCall(t, hostWorkspaceList, map[string]any{"path": ".git"}); workspaceErrorCode(rpcErr) != "denied" {
		t.Fatalf("list .git = %+v", rpcErr)
	}

	stat, rpcErr := workspaceCall(t, hostWorkspaceStat, map[string]any{"path": "src/main.go", "sha256": true})
	digest := sha256.Sum256([]byte("package main\n"))
	if rpcErr != nil || stat["type"] != "file" || stat["size"].(float64) != 13 || stat["sha256"] != hex.EncodeToString(digest[:]) {
		t.Fatalf("stat = %v %+v", stat, rpcErr)
	}

	chunk, rpcErr := workspaceCall(t, hostWorkspaceRead, map[string]any{"path": "README.md", "offset": 990, "maxBytes": 50})
	if rpcErr != nil || chunk["eof"] != true || chunk["nextOffset"].(float64) != 1000 {
		t.Fatalf("read tail = %v %+v", chunk, rpcErr)
	}
	if data, _ := base64.StdEncoding.DecodeString(chunk["data"].(string)); string(data) != strings.Repeat("r", 10) {
		t.Fatalf("tail data = %q", data)
	}
	for path, code := range map[string]string{".env": "denied", ".git/config": "denied", "../outside": "invalid_path", "missing.txt": "not_found", "src": "not_file"} {
		if _, rpcErr := workspaceCall(t, hostWorkspaceRead, map[string]any{"path": path}); workspaceErrorCode(rpcErr) != code {
			t.Errorf("read %q = %+v, want %s", path, rpcErr, code)
		}
	}
}

func TestWorkspaceSymlinksStayInsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture")
	}
	root := useWorkspaceRoot(t)
	outside := t.TempDir()
	writeWorkspaceFile(t, outside, "secret.txt", "outside")
	writeWorkspaceFile(t, root, "real.txt", "inside")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := workspaceCall(t, hostWorkspaceRead, map[string]any{"path": "escape.txt"}); workspaceErrorCode(rpcErr) != "denied" {
		t.Fatalf("escaping symlink read = %+v", rpcErr)
	}
	if _, rpcErr := workspaceCall(t, hostWorkspaceRead, map[string]any{"path": "alias.txt"}); rpcErr != nil {
		t.Fatalf("inside symlink read = %+v", rpcErr)
	}
	data := base64.StdEncoding.EncodeToString([]byte("x"))
	if _, rpcErr := workspaceCall(t, hostWorkspaceWrite, map[string]any{"path": "alias.txt", "data": data}); workspaceErrorCode(rpcErr) != "denied" {
		t.Fatalf("write through symlink = %+v", rpcErr)
	}
	if content, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(content) != "outside" {
		t.Fatal("outside file changed")
	}
}

func TestWorkspaceWrite(t *testing.T) {
	root := useWorkspaceRoot(t)
	encode := func(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }
	empty := ""

	result, rpcErr := workspaceCall(t, hostWorkspaceWrite, map[string]any{"path": "gen/out/a.txt", "data": encode("one"), "createParents": true, "expectedSha256": empty})
	if rpcErr != nil || result["created"] != true {
		t.Fatalf("create = %v %+v", result, rpcErr)
	}
	if _, rpcErr := workspaceCall(t, hostWorkspaceWrite, map[string]any{"path": "gen/out/a.txt", "data": encode("two"), "expectedSha256": empty}); workspaceErrorCode(rpcErr) != "conflict" {
		t.Fatalf("create-only over existing = %+v", rpcErr)
	}
	stale := hex.EncodeToString(make([]byte, 32))
	if _, rpcErr := workspaceCall(t, hostWorkspaceWrite, map[string]any{"path": "gen/out/a.txt", "data": encode("two"), "expectedSha256": stale}); workspaceErrorCode(rpcErr) != "conflict" {
		t.Fatalf("stale sha = %+v", rpcErr)
	}
	result, rpcErr = workspaceCall(t, hostWorkspaceWrite, map[string]any{"path": "gen/out/a.txt", "data": encode("two"), "expectedSha256": result["sha256"]})
	if rpcErr != nil || result["created"] != false {
		t.Fatalf("replace = %v %+v", result, rpcErr)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "gen", "out", "a.txt")); string(content) != "two" {
		t.Fatalf("content = %q", content)
	}
	if _, rpcErr := workspaceCall(t, hostWorkspaceWrite, map[string]any{"path": "missing/dir/a.txt", "data": encode("x")}); workspaceErrorCode(rpcErr) != "not_found" {
		t.Fatalf("missing parent = %+v", rpcErr)
	}
	for _, denied := range []string{".git/hooks/pre-commit", "node_modules/x.js", "keys/.env", ".mincode/a"} {
		if _, rpcErr := workspaceCall(t, hostWorkspaceWrite, map[string]any{"path": denied, "data": encode("x"), "createParents": true}); workspaceErrorCode(rpcErr) != "denied" {
			t.Errorf("write %q = %+v", denied, rpcErr)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatal("denied write created a directory")
	}
	big := base64.StdEncoding.EncodeToString(make([]byte, workspaceWriteMaxBytes+1))
	if _, rpcErr := workspaceCall(t, hostWorkspaceWrite, map[string]any{"path": "big.bin", "data": big}); workspaceErrorCode(rpcErr) != "too_large" {
		t.Fatalf("oversized write = %+v", rpcErr)
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, "gen", "out", ".mincode-plugin-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestHostEventsSubscribe(t *testing.T) {
	runtimeHost := newProcessRuntime(t.TempDir(), Manifest{Capabilities: []api.Capability{api.CapabilityCommands}}, nil, nil)
	call := func(events ...string) (any, *rpcError) {
		raw, _ := json.Marshal(map[string]any{"events": events})
		return hostEventsSubscribe(runtimeHost, raw)
	}
	if _, rpcErr := call("workspace.nope"); rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("unknown event = %+v", rpcErr)
	}
	if _, rpcErr := call(hostEventFileSaved); rpcErr == nil || rpcErr.Code != -32001 {
		t.Fatalf("path event without workspace.read = %+v", rpcErr)
	}
	if _, rpcErr := call(hostEventWorkspaceChanged); rpcErr != nil || !runtimeHost.subscribedTo(hostEventWorkspaceChanged) {
		t.Fatalf("subscribe = %+v", rpcErr)
	}
	if _, rpcErr := call(); rpcErr != nil || runtimeHost.subscribedTo(hostEventWorkspaceChanged) {
		t.Fatal("empty subscription did not clear")
	}
	described := runtimeHost.describeEvents()
	for _, event := range described {
		if event["name"] == hostEventFileSaved && event["available"] != false {
			t.Fatalf("describe = %v", described)
		}
	}
}

func TestWorkspaceRelativeEventPath(t *testing.T) {
	root := useWorkspaceRoot(t)
	writeWorkspaceFile(t, root, "src/app.ts", "")
	if relative, inside := workspaceRelativeEventPath(root, filepath.Join(root, "src", "app.ts")); !inside || relative != "src/app.ts" {
		t.Fatalf("inside = %q %v", relative, inside)
	}
	if relative, inside := workspaceRelativeEventPath(root, filepath.Join(root, "src", "new.ts")); !inside || relative != "src/new.ts" {
		t.Fatalf("unsaved file = %q %v", relative, inside)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "other.ts"), filepath.Join(root, ".env"), filepath.Join(root, ".git", "HEAD"), "relative.ts", ""} {
		if _, inside := workspaceRelativeEventPath(root, path); inside {
			t.Errorf("%q announced", path)
		}
	}
	if _, inside := workspaceRelativeEventPath("", filepath.Join(root, "src", "app.ts")); inside {
		t.Fatal("announced without a root")
	}
}

func TestDescribeAPIListsStorageFilesAndWorkspace(t *testing.T) {
	runtimeHost := newProcessRuntime(t.TempDir(), Manifest{Capabilities: []api.Capability{api.CapabilityStorage, api.CapabilityWorkspaceRead}}, nil, nil)
	encoded, _ := json.Marshal(runtimeHost.describeAPI())
	var described struct {
		Methods []struct {
			Name      string `json:"name"`
			Available bool   `json:"available"`
		} `json:"methods"`
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(encoded, &described); err != nil {
		t.Fatal(err)
	}
	available := map[string]bool{}
	for _, method := range described.Methods {
		available[method.Name] = method.Available
	}
	for name, want := range map[string]bool{"host.storage.file.read": true, "host.workspace.read": true, "host.workspace.write": false, "host.events.subscribe": true} {
		if got, listed := available[name]; !listed || got != want {
			t.Errorf("%s listed=%v available=%v, want %v", name, listed, got, want)
		}
	}
	if len(described.Events) != len(hostEvents) {
		t.Fatalf("events = %v", described.Events)
	}
}

// TestHostEventsAndWorkspaceThroughNodeSDK runs a real process plugin on the
// public Node SDK: it subscribes through start({ events }), receives
// plugin.event, and reads and writes workspace files through the host.
func TestHostEventsAndWorkspaceThroughNodeSDK(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not available")
	}
	useTemporaryPluginData(t)
	root := useWorkspaceRoot(t)
	writeWorkspaceFile(t, root, "README.md", "hello workspace")
	sdk, err := filepath.Abs("../../packages/plugin-sdk/node/runtime.mjs")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal("file://" + filepath.ToSlash(sdk))
	marker := filepath.Join(t.TempDir(), "events.jsonl")
	markerJSON, _ := json.Marshal(marker)
	source := "#!/usr/bin/env node\nimport { appendFileSync } from 'node:fs';\nimport { start, createWorkspaceAPI } from " + string(quoted) + `;
start(host => ({
  copy: async () => {
    const workspace = createWorkspaceAPI(host);
    const text = await workspace.readText('README.md');
    await workspace.write('out/copy.md', text.toUpperCase(), { createParents: true, createOnly: true });
    return { root: await workspace.root() };
  },
}), {
  events: ['workspace.changed', 'workspace.rootChanged', 'editor.fileSaved'],
  onEvent: (event, data) => appendFileSync(` + string(markerJSON) + `, JSON.stringify({ event, data }) + '\n'),
});`
	packageRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(packageRoot, "plugin.mjs"), []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
	manifestData, _ := json.Marshal(map[string]any{
		"apiVersion": 2, "id": "events.test", "name": "Events Test", "version": "1.0.0",
		"category": "development", "platforms": []string{runtime.GOOS},
		"capabilities": []string{"workspace.read", "workspace.write"},
		"runtime":      map[string]any{"type": "process", "entry": "plugin.mjs"},
	})
	if err := os.WriteFile(filepath.Join(packageRoot, manifestFilename), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := readManifestStrict(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	runtimeHost := newProcessRuntime(packageRoot, manifest, nil, nil)
	if err := runtimeHost.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		runtimeHost.Stop(ctx)
	})
	waitFor := func(what string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !ready() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("event subscription", func() bool { return runtimeHost.subscribedTo(hostEventFileSaved) })

	manager := &pluginManager{records: map[string]*pluginRecord{}}
	record := &pluginRecord{id: "events.test", source: pluginSourceLocal, root: packageRoot, manifest: &manifest,
		enabled: true, scope: pluginScopeGlobal, state: pluginStateRunning, runtime: runtimeHost}
	manager.records[record.id] = record
	manager.order = append(manager.order, record.id)
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	scope, err := manager.BeginWorkspaceScope(context.Background(), "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.CommitWorkspaceScope("ws-1", scope.Revision, root); err != nil {
		t.Fatal(err)
	}
	manager.publishHostEvent(hostEventActiveFileChanged, map[string]any{"path": "README.md"})
	manager.publishHostEvent(hostEventFileSaved, map[string]any{"path": "README.md"})
	waitFor("event delivery", func() bool {
		data, _ := os.ReadFile(marker)
		return strings.Count(string(data), "\n") >= 3
	})
	delivered, _ := os.ReadFile(marker)
	lines := strings.Split(strings.TrimSpace(string(delivered)), "\n")
	sort.Strings(lines)
	rootJSON, _ := json.Marshal(root)
	want := []string{`{"event":"editor.fileSaved","data":{"path":"README.md"}}`, `{"event":"workspace.changed","data":{"workspaceId":"ws-1"}}`, `{"event":"workspace.rootChanged","data":{"root":` + string(rootJSON) + `}}`}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("delivered events = %v", lines)
	}

	result, err := runtimeHost.Invoke(context.Background(), "copy", `{}`)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if !strings.Contains(result, root) {
		t.Fatalf("copy result = %s", result)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "out", "copy.md")); string(content) != "HELLO WORKSPACE" {
		t.Fatalf("copied content = %q", content)
	}
	if _, err := runtimeHost.Invoke(context.Background(), "copy", `{}`); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("createOnly second write = %v", err)
	}
}
