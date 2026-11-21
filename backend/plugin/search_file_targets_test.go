package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/plugins/api"
)

// TestSearchFileHelperProcess is a search provider subprocess configured by
// files in its package root (its working directory): "negotiate" echoes the
// offered features, "result.json" is the action result, every payload is
// appended to "payloads", and "hold" parks the invoke (writing "entered")
// until "release" exists. Invokes run concurrently so shutdown still works.
func TestSearchFileHelperProcess(t *testing.T) {
	if os.Getenv("MINCODE_PLUGIN_ID") != "runtime.test" {
		return
	}
	trace := func(stage string) { fmt.Fprintf(os.Stderr, "search fixture %s at %d\n", stage, time.Now().UnixMicro()) }
	trace("boot")
	var mu sync.Mutex
	scanner, encoder := bufio.NewScanner(os.Stdin), json.NewEncoder(os.Stdout)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	send := func(response rpcEnvelope) {
		mu.Lock()
		defer mu.Unlock()
		if encoder.Encode(response) != nil {
			os.Exit(3)
		}
	}
	exists := func(name string) bool { _, err := os.Stat(name); return err == nil }
	for scanner.Scan() {
		var request rpcEnvelope
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		trace("received:" + request.Method)
		response := rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`true`)}
		switch request.Method {
		case "plugin.initialize":
			if delay, err := os.ReadFile("initialize-delay"); err == nil {
				duration, _ := time.ParseDuration(string(delay))
				time.Sleep(duration) // fault injection: slow fixture process startup
			}
			if exists("negotiate") {
				var params struct {
					Features map[string]int `json:"features"`
				}
				_ = json.Unmarshal(request.Params, &params)
				response.Result, _ = json.Marshal(map[string]any{"features": params.Features})
			}
		case "plugin.invoke":
			go func(response rpcEnvelope, params json.RawMessage) {
				var invoke struct {
					Payload string `json:"payload"`
				}
				_ = json.Unmarshal(params, &invoke)
				mu.Lock()
				file, err := os.OpenFile("payloads", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
				if err == nil {
					_, _ = file.WriteString(invoke.Payload + "\n")
					_ = file.Close()
				}
				mu.Unlock()
				if exists("hold") {
					_ = os.WriteFile("entered", []byte("yes"), 0600)
					for !exists("release") {
						time.Sleep(5 * time.Millisecond)
					}
				}
				result, _ := os.ReadFile("result.json")
				response.Result = json.RawMessage(`{"result":` + string(result) + `}`)
				trace("answer:plugin.invoke")
				send(response)
			}(response, request.Params)
			continue
		case "plugin.shutdown":
			send(response)
			os.Exit(0)
		}
		trace("answer:" + request.Method)
		send(response)
	}
	os.Exit(0)
}

const searchFileLegacyItems = `{"id":"v1","title":"View","target":{"type":"view","view":"view","args":{"key":"A"}}},
	{"id":"c1","title":"Cmd","target":{"type":"command","command":"run"}},
	{"id":"u1","title":"Url","target":{"type":"url","url":"https://example.com/a"}}`

var searchFileLegacyWant = []SearchResultItem{
	{ID: "v1", Title: "View", Target: SearchResultTarget{Type: "view", View: "view", Args: map[string]any{"key": "A"}}},
	{ID: "c1", Title: "Cmd", Target: SearchResultTarget{Type: "command", Command: "run"}},
	{ID: "u1", Title: "Url", Target: SearchResultTarget{Type: "url", URL: "https://example.com/a"}},
}

// searchFileManager is a running-capable search provider "issues" on action
// "run" backed by TestSearchFileHelperProcess.
func searchFileManager(t *testing.T, negotiate, workspaceRead bool) (*pluginManager, *pluginRecord) {
	t.Helper()
	manager, record := searchInvokeManager(t, SearchProviderContribution{ID: "issues", Prefix: "jira", Title: "Issues", Action: "run", MinQueryLength: 1})
	record.mu.Lock()
	record.manifest.Runtime.Args = []string{"-test.run=^TestSearchFileHelperProcess$"}
	record.manifest.Capabilities = nil
	if workspaceRead {
		record.manifest.Capabilities = []api.Capability{api.CapabilityWorkspaceRead}
	}
	record.runtime = manager.newRecordRuntime(record, record.root, *record.manifest)
	record.mu.Unlock()
	if negotiate {
		writeSearchFile(t, record.root, "negotiate", "yes")
	}
	created := time.Now()
	t.Cleanup(func() {
		assertNoContributionRequestsLeft(t)
		record.mu.Lock()
		defer record.mu.Unlock()
		t.Logf("search fixture elapsed=%s state=%s logs=%+v", time.Since(created), record.state, record.logs)
	})
	return manager, record
}

// readySearchFileProvider waits for the real initialize reply through the
// manager. Payload and in-flight cancellation tests are not startup-budget
// tests; their search still runs under the unchanged production 5 s deadline.
func readySearchFileProvider(t *testing.T, manager *pluginManager, record *pluginRecord) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pluginStartupTimeout)
	defer cancel()
	started := time.Now()
	if err := manager.ensureRunning(ctx, record.id); err != nil {
		t.Fatalf("search fixture initialization failed: %v", err)
	}
	t.Logf("search fixture initialize acknowledged after %s", time.Since(started))
}

func writeSearchFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func setSearchFileResult(t *testing.T, record *pluginRecord, items ...string) {
	t.Helper()
	writeSearchFile(t, record.root, "result.json", `{"items":[`+strings.Join(items, ",")+`]}`)
}

func searchFilePayloads(t *testing.T, record *pluginRecord) []string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(record.root, "payloads"))
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// commitSearchFileScope publishes a ready workspace scope for root.
func commitSearchFileScope(t *testing.T, manager *pluginManager, workspaceID, root string) workspaceScopeSnapshot {
	t.Helper()
	begun, err := manager.BeginWorkspaceScope(context.Background(), workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := manager.CommitWorkspaceScope(workspaceID, begun.Revision, root)
	if err != nil || !scope.Ready {
		t.Fatalf("commit = %+v, %v", scope, err)
	}
	return scope
}

func searchFileRequest(record *pluginRecord) searchRequest {
	return searchRequest{PluginID: record.id, ProviderID: "issues", RequestID: "search-file-0001", Query: "abc"}
}

func TestSearchFileOptInStrictLegacy(t *testing.T) {
	type legacyPayload struct {
		Provider string `json:"provider"`
		Query    string `json:"query"`
		Locale   string `json:"locale"`
	}
	fileItem := `{"id":"f1","title":"Main","target":{"type":"file","path":"src/main.go","line":12,"column":3}}`
	t.Run("not negotiated", func(t *testing.T) {
		manager, record := searchFileManager(t, false, true)
		root := t.TempDir()
		writeSearchFile(t, root, "src/main.go", "package main\n")
		commitSearchFileScope(t, manager, "ws-1", root)
		setSearchFileResult(t, record, searchFileLegacyItems, fileItem)
		response, err := manager.search(context.Background(), searchFileRequest(record))
		if err != nil {
			t.Fatal(err)
		}
		payloads := searchFilePayloads(t, record)
		if len(payloads) != 1 || payloads[0] != `{"provider":"issues","query":"abc","locale":"en"}` {
			t.Fatalf("legacy payload = %q", payloads)
		}
		var decoded legacyPayload
		if err := decodeStrictJSON([]byte(payloads[0]), &decoded, true); err != nil {
			t.Fatalf("strict legacy decode: %v", err)
		}
		if response.Dropped != 1 || response.FileScope != nil || response.ProviderRuntime != nil {
			t.Fatalf("legacy response = %+v", response)
		}
		assertSearchFileItems(t, response.Items, searchFileLegacyWant)
		encoded, _ := json.Marshal(response)
		if bytes.Contains(encoded, []byte("fileScope")) || bytes.Contains(encoded, []byte("providerRuntime")) || bytes.Contains(encoded, []byte("column")) {
			t.Fatalf("legacy response leaks new fields: %s", encoded)
		}
	})
	t.Run("negotiated", func(t *testing.T) {
		manager, record := searchFileManager(t, true, true)
		root := t.TempDir()
		writeSearchFile(t, root, "src/main.go", "package main\n")
		scope := commitSearchFileScope(t, manager, "ws-1", root)
		setSearchFileResult(t, record, searchFileLegacyItems, fileItem)
		response, err := manager.search(context.Background(), searchFileRequest(record))
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			legacyPayload
			Project struct {
				Name string `json:"name"`
				Root string `json:"root"`
			} `json:"project"`
		}
		payloads := searchFilePayloads(t, record)
		if err := decodeStrictJSON([]byte(payloads[0]), &decoded, true); err != nil || decoded.Project.Root != scope.Root || decoded.Project.Name != filepath.Base(scope.Root) || decoded.Provider != "issues" {
			t.Fatalf("negotiated payload = %s (%v)", payloads[0], err)
		}
		want := append(append([]SearchResultItem(nil), searchFileLegacyWant...), SearchResultItem{ID: "f1", Title: "Main", Target: SearchResultTarget{Type: "file", Path: "src/main.go", Line: 12, Column: 3}})
		assertSearchFileItems(t, response.Items, want)
		if response.Dropped != 0 || response.FileScope == nil || *response.FileScope != (searchFileScope{WorkspaceID: "ws-1", Root: scope.Root, Revision: scope.Revision}) {
			t.Fatalf("negotiated response = %+v", response)
		}
		record.mu.Lock()
		identity := record.runtime.identity(record.runtime.Generation())
		record.mu.Unlock()
		if response.ProviderRuntime == nil || *response.ProviderRuntime != identity || identity.Generation != "1" {
			t.Fatalf("providerRuntime = %+v, want %+v", response.ProviderRuntime, identity)
		}
	})
}

func assertSearchFileItems(t *testing.T, got, want []SearchResultItem) {
	t.Helper()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("items = %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestSearchFileBoundsAndPolicy(t *testing.T) {
	scope := searchTestScope()
	scope.fileRoot = t.TempDir()
	target := func(fields string) json.RawMessage { return json.RawMessage(`{"type":"file",` + fields + `}`) }
	segment := strings.Repeat("a", 200)
	path1024 := strings.Repeat(segment+"/", 6)[:1023] + "b"
	path1025 := path1024 + "c"
	if len(path1024) != 1024 {
		t.Fatalf("path length = %d", len(path1024))
	}
	accepted := map[string]SearchResultTarget{
		`"path":"` + path1024 + `"`:                       {Type: "file", Path: path1024},
		`"path":"a.go","line":1`:                          {Type: "file", Path: "a.go", Line: 1},
		`"path":"a.go","line":10000000`:                   {Type: "file", Path: "a.go", Line: 10_000_000},
		`"path":"a.go","line":1,"column":1`:               {Type: "file", Path: "a.go", Line: 1, Column: 1},
		`"path":"a.go","line":10000000,"column":10000000`: {Type: "file", Path: "a.go", Line: 10_000_000, Column: 10_000_000},
	}
	for fields, want := range accepted {
		got, err := normalizeSearchResultTarget(target(fields), scope)
		if err != nil || got.Path != want.Path || got.Line != want.Line || got.Column != want.Column || got.Type != "file" {
			t.Errorf("%s = %+v, %v", fields, got, err)
		}
	}
	for _, fields := range []string{
		`"path":"` + path1025 + `"`, `"path":"a.go","line":0`, `"path":"a.go","line":-1`, `"path":"a.go","line":1.5`,
		`"path":"a.go","line":10000001`, `"path":"a.go","column":3`, `"path":"a.go","line":1,"column":0`,
		`"path":"a.go","line":1,"column":10000001`, `"path":"a.go","line":1,"column":2.5`, `"path":"../a.go"`,
		`"path":"/etc/passwd"`, `"path":"a\\b.go"`, `"path":"C:/a.go"`, `"path":"a//b.go"`, `"path":"./a.go"`,
		`"path":""`, `"path":"a%2e.go"`, `"path":"a.go","url":"https://x.test"`,
	} {
		if got, err := normalizeSearchResultTarget(target(fields), scope); err == nil {
			t.Errorf("%s accepted as %+v", fields, got)
		}
	}
	// Columns stay search-only; other points never accept them.
	for _, raw := range []string{`{"type":"file","path":"a.go","line":1,"column":1}`, `{"type":"view","view":"main","column":1}`} {
		if _, err := normalizeTreeTarget(json.RawMessage(raw), scope); err == nil {
			t.Errorf("tree target %s accepted a column", raw)
		}
	}
	if _, err := normalizeSearchResultTarget(json.RawMessage(`{"type":"view","view":"main","column":1}`), scope); err == nil {
		t.Error("view target accepted a column")
	}
	// Without the admitted scope root, file targets are not accepted at all.
	noRoot := searchTestScope()
	if _, err := normalizeSearchResultTarget(target(`"path":"a.go"`), noRoot); err == nil {
		t.Error("file target accepted without a scope root")
	}

	// Filesystem policy at result time: the requested and the resolved path
	// are both filtered; a missing suffix may be shown.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeSearchFile(t, outside, "secret.txt", "x")
	writeSearchFile(t, root, "src/main.go", "package main\n")
	writeSearchFile(t, root, ".env", "TOKEN=x")
	writeSearchFile(t, root, "sub/.git/config", "x")
	for link, to := range map[string]string{"escape.txt": filepath.Join(outside, "secret.txt"), "dangling.txt": filepath.Join(root, "missing.txt"), "env-link.txt": filepath.Join(root, ".env"), "ok-link.go": filepath.Join(root, "src/main.go")} {
		if err := os.Symlink(to, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	policyScope := searchTestScope()
	policyScope.fileRoot = root
	item := searchFileItem
	raw := `{"items":[` + strings.Join([]string{
		item("ok", "src/main.go"), item("new", "src/new/file.go"), item("link", "ok-link.go"),
		item("env", ".env"), item("vcs", "sub/.git/config"), item("escape", "escape.txt"),
		item("dangling", "dangling.txt"), item("envlink", "env-link.txt"), item("pem", "keys/server.pem"),
	}, ",") + `]}`
	response, err := parseSearchResult(json.RawMessage(raw), policyScope)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, kept := range response.Items {
		ids = append(ids, kept.ID)
	}
	if strings.Join(ids, ",") != "ok,new,link" || response.Dropped != 6 {
		t.Fatalf("policy kept %v, dropped %d", ids, response.Dropped)
	}

	// End to end: no workspace.read, no ready root and no opt-in each drop
	// file items (counted) while other targets still work.
	fileItem := item("f1", "src/main.go")
	for _, scenario := range []string{"no workspace.read", "no root", "no opt-in"} {
		t.Run(scenario, func(t *testing.T) {
			manager, record := searchFileManager(t, scenario != "no opt-in", scenario != "no workspace.read")
			projectRoot := t.TempDir()
			writeSearchFile(t, projectRoot, "src/main.go", "package main\n")
			if scenario == "no root" {
				if _, err := manager.BeginWorkspaceScope(context.Background(), "ws-1"); err != nil {
					t.Fatal(err)
				}
			} else {
				commitSearchFileScope(t, manager, "ws-1", projectRoot)
			}
			setSearchFileResult(t, record, searchFileLegacyItems, fileItem)
			response, err := manager.search(context.Background(), searchFileRequest(record))
			if err != nil {
				t.Fatal(err)
			}
			assertSearchFileItems(t, response.Items, searchFileLegacyWant)
			if response.Dropped != 1 || response.FileScope != nil || response.ProviderRuntime != nil {
				t.Fatalf("response = %+v", response)
			}
			if payloads := searchFilePayloads(t, record); payloads[0] != `{"provider":"issues","query":"abc","locale":"en"}` {
				t.Fatalf("payload = %s", payloads[0])
			}
		})
	}
}

// normalizeTreeTarget validates a tree view item target with the tree limits.
func normalizeTreeTarget(raw json.RawMessage, scope searchTargetScope) (SearchResultTarget, error) {
	return normalizeContributionItemTarget(raw, scope, treeViewItemTargetLimits)
}

// R2: the opt-in project block never widens the search payload limit. When
// it cannot fit, the provider gets the legacy payload and no file results.
func TestSearchFilePayloadCapFallsBackToLegacy(t *testing.T) {
	manager, record := searchFileManager(t, true, true)
	// Keep payload assertions independent of cold fixture startup, even if
	// initialization alone takes longer than the production 5 s search cap.
	writeSearchFile(t, record.root, "initialize-delay", "5250ms")
	root := t.TempDir()
	writeSearchFile(t, root, "src/main.go", "package main\n")
	commitSearchFileScope(t, manager, "ws-1", root)
	request := searchFileRequest(record)
	request.Query = strings.Repeat("q", 100)
	legacy, err := encodeContributionJSON(searchPayload{Provider: "issues", Query: request.Query, Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	record.manifest.Contributes.Actions[0].MaxPayloadBytes = len(legacy) + 8
	record.mu.Unlock()
	setSearchFileResult(t, record, searchFileLegacyItems, searchFileItem("f1", "src/main.go"))
	readySearchFileProvider(t, manager, record)
	response, err := manager.search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if payloads := searchFilePayloads(t, record); payloads[0] != string(legacy) {
		t.Fatalf("payload = %s, want legacy %s", payloads[0], legacy)
	}
	assertSearchFileItems(t, response.Items, searchFileLegacyWant)
	if response.Dropped != 1 || response.FileScope != nil || response.ProviderRuntime != nil {
		t.Fatalf("response = %+v", response)
	}
	// The legacy payload itself is still bounded exactly as before.
	record.mu.Lock()
	record.manifest.Contributes.Actions[0].MaxPayloadBytes = len(legacy) - 1
	record.mu.Unlock()
	if _, err := manager.search(context.Background(), request); contributionErrorCode(err) != contributionErrTooLarge {
		t.Fatalf("oversized legacy payload err = %v", err)
	}
}

func searchFileItem(id, path string) string {
	return `{"id":"` + id + `","title":"t","target":{"type":"file","path":"` + path + `"}}`
}

// R7: a scope begin while a file-enabled search is in flight cancels it
// (plugin invoke phase and host prepare phase) and stamps no file items.
func TestSearchFileScopeBeginCancelsInFlight(t *testing.T) {
	t.Run("invoke", func(t *testing.T) {
		manager, record := searchFileManager(t, true, true)
		root := t.TempDir()
		writeSearchFile(t, root, "src/main.go", "package main\n")
		commitSearchFileScope(t, manager, "ws-1", root)
		setSearchFileResult(t, record, searchFileItem("f1", "src/main.go"))
		writeSearchFile(t, record.root, "hold", "yes")
		readySearchFileProvider(t, manager, record)
		type outcome struct {
			response searchResponse
			err      error
		}
		done := make(chan outcome, 1)
		joined := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(func() {
			cancel()
			writeSearchFile(t, record.root, "release", "yes")
			select {
			case <-joined:
			case <-time.After(3 * time.Second):
				t.Error("search fixture caller did not join")
			}
		})
		go func() {
			defer close(joined)
			response, err := manager.search(ctx, searchFileRequest(record))
			done <- outcome{response, err}
		}()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(record.root, "entered")); err == nil {
				break
			}
			select {
			case result := <-done:
				t.Fatalf("search completed before provider entry: %+v, %v", result.response, result.err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("search never reached the provider")
			}
			time.Sleep(5 * time.Millisecond)
		}
		if _, err := manager.BeginWorkspaceScope(context.Background(), "ws-2"); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-done:
			if contributionErrorCode(result.err) != contributionErrCancelled || len(result.response.Items) != 0 || result.response.FileScope != nil {
				t.Fatalf("in-flight search after scope begin = %+v, %v", result.response, result.err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("scope begin did not cancel the in-flight search")
		}
		writeSearchFile(t, record.root, "release", "yes")
	})
	t.Run("prepare", func(t *testing.T) {
		manager, record := searchFileManager(t, true, true)
		root := t.TempDir()
		writeSearchFile(t, root, "src/main.go", "package main\n")
		commitSearchFileScope(t, manager, "ws-1", root)
		setSearchFileResult(t, record, searchFileItem("f1", "src/main.go"))
		searchFileStageHook = func(stage string) {
			if stage == "prepared" {
				if _, err := manager.BeginWorkspaceScope(context.Background(), "ws-2"); err != nil {
					t.Error(err)
				}
			}
		}
		t.Cleanup(func() { searchFileStageHook = nil })
		response, err := manager.search(context.Background(), searchFileRequest(record))
		if contributionErrorCode(err) != contributionErrCancelled || len(response.Items) != 0 || response.FileScope != nil {
			t.Fatalf("search after begin during prepare = %+v, %v", response, err)
		}
		if _, err := os.Stat(filepath.Join(record.root, "payloads")); !os.IsNotExist(err) {
			t.Fatalf("cancelled search still reached the provider: %v", err)
		}
	})
	t.Run("result", func(t *testing.T) {
		manager, record := searchFileManager(t, true, true)
		root := t.TempDir()
		writeSearchFile(t, root, "src/main.go", "package main\n")
		commitSearchFileScope(t, manager, "ws-1", root)
		setSearchFileResult(t, record, searchFileItem("f1", "src/main.go"))
		searchFileStageHook = func(stage string) {
			if stage == "invoked" {
				if _, err := manager.BeginWorkspaceScope(context.Background(), "ws-2"); err != nil {
					t.Error(err)
				}
			}
		}
		t.Cleanup(func() { searchFileStageHook = nil })
		response, err := manager.search(context.Background(), searchFileRequest(record))
		if contributionErrorCode(err) != contributionErrCancelled || len(response.Items) != 0 || response.FileScope != nil {
			t.Fatalf("search answered after begin = %+v, %v", response, err)
		}
	})
}

func TestSearchFileClickRevalidate(t *testing.T) {
	manager, record := searchFileManager(t, true, true)
	root := t.TempDir()
	outside := t.TempDir()
	writeSearchFile(t, outside, "secret.txt", "x")
	writeSearchFile(t, root, "src/main.go", "package main\n")
	writeSearchFile(t, root, "src/locked.go", "package main\n")
	writeSearchFile(t, root, ".env", "TOKEN=x")
	if err := os.Symlink(filepath.Join(root, "src/main.go"), filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	commitSearchFileScope(t, manager, "ws-1", root)
	setSearchFileResult(t, record,
		`{"id":"f1","title":"Main","target":{"type":"file","path":"src/main.go","line":4,"column":2}}`,
		searchFileItem("link", "link.go"), searchFileItem("new", "src/new.go"), searchFileItem("dir", "src"), searchFileItem("locked", "src/locked.go"))
	search := func() searchResponse {
		t.Helper()
		response, err := manager.search(context.Background(), searchFileRequest(record))
		if err != nil || response.FileScope == nil || response.ProviderRuntime == nil || len(response.Items) != 5 {
			t.Fatalf("search = %+v, %v", response, err)
		}
		return response
	}
	click := func(response searchResponse, index int) (SearchResultTarget, error) {
		target := response.Items[index].Target
		request := searchFileTargetRequest{PluginID: record.id, ProviderID: "issues", FileScope: *response.FileScope, ProviderRuntime: *response.ProviderRuntime, Path: target.Path}
		if target.Line != 0 {
			line := target.Line
			request.Line = &line
		}
		if target.Column != 0 {
			column := target.Column
			request.Column = &column
		}
		return manager.validateSearchFileTarget(context.Background(), request)
	}
	response := search()
	got, err := click(response, 0)
	if err != nil || got.Type != "file" || got.Path != "src/main.go" || got.Line != 4 || got.Column != 2 || got.Args != nil || got.View != "" || got.Command != "" || got.URL != "" {
		t.Fatalf("valid click = %+v, %v", got, err)
	}
	if got, err := click(response, 1); err != nil || got.Path != "link.go" {
		t.Fatalf("in-root symlink click = %+v, %v", got, err)
	}
	// Validation stats the target and never reads it.
	if err := os.Chmod(filepath.Join(root, "src/locked.go"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "src/locked.go"), 0600) })
	if _, err := click(response, 4); err != nil {
		t.Fatalf("unreadable regular file click = %v", err)
	}
	// A missing suffix was shown, but a click needs an existing regular file.
	if _, err := click(response, 2); contributionErrorCode(err) != contributionErrNotFound {
		t.Fatalf("missing file click err = %v", err)
	}
	if _, err := click(response, 3); contributionErrorCode(err) != contributionErrForbidden {
		t.Fatalf("directory click err = %v", err)
	}
	// The symlink is swapped after the query: outside the root, to a
	// sensitive file and dangling are all refused.
	for _, to := range []string{filepath.Join(outside, "secret.txt"), filepath.Join(root, ".env"), filepath.Join(root, "gone.go")} {
		if err := os.Remove(filepath.Join(root, "link.go")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(to, filepath.Join(root, "link.go")); err != nil {
			t.Fatal(err)
		}
		if _, err := click(response, 1); contributionErrorCode(err) != contributionErrForbidden {
			t.Fatalf("swapped symlink to %s err = %v", to, err)
		}
	}
	if err := os.Remove(filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "src/main.go"), filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	// Request-side checks: the path policy, positions and the scope fields.
	malformed := []func(*searchFileTargetRequest){
		func(r *searchFileTargetRequest) { r.Path = "../x.go" },
		func(r *searchFileTargetRequest) { r.Path = ".git/config" },
		func(r *searchFileTargetRequest) { r.Path = "src/.env" },
		func(r *searchFileTargetRequest) { zero := 0; r.Line = &zero },
		func(r *searchFileTargetRequest) { r.Line = nil },
		func(r *searchFileTargetRequest) { big := 10_000_001; r.Column = &big },
		func(r *searchFileTargetRequest) { r.PluginID = "Bad Id" },
		func(r *searchFileTargetRequest) { r.ProviderRuntime.Generation = "01" },
	}
	for index, mutate := range malformed {
		line, column := 4, 2
		request := searchFileTargetRequest{PluginID: record.id, ProviderID: "issues", FileScope: *response.FileScope, ProviderRuntime: *response.ProviderRuntime, Path: "src/main.go", Line: &line, Column: &column}
		mutate(&request)
		if _, err := manager.validateSearchFileTarget(context.Background(), request); err == nil {
			t.Errorf("malformed request %d accepted", index)
		}
	}
	for _, args := range []string{
		`{"pluginId":"runtime.test","providerId":"issues","fileScope":{"workspaceId":"ws-1","root":"/x","revision":"1"},"providerRuntime":{"instance":"1","generation":"1"},"path":"a.go","line":1.5}`,
		`{"pluginId":"runtime.test","providerId":"issues","fileScope":{"workspaceId":"ws-1","root":"/x","revision":"1","extra":1},"providerRuntime":{"instance":"1","generation":"1"},"path":"a.go"}`,
		`nope`,
	} {
		if _, err := pluginHostValidateSearchFileTargetHandler(context.Background(), "", args); contributionErrorCode(err) != contributionErrInvalidRequest {
			t.Errorf("handler %s err = %v", args, err)
		}
	}

	// Switching away and back publishes a new revision: old results are stale.
	commitSearchFileScope(t, manager, "ws-2", outside)
	commitSearchFileScope(t, manager, "ws-1", root)
	if _, err := click(response, 0); contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("click after switching back err = %v", err)
	}
	fresh := search()
	if _, err := click(fresh, 0); err != nil {
		t.Fatalf("fresh click = %v", err)
	}

	// A replacement runtime restarts at generation 1 with a new instance.
	old := record.runtime
	if err := old.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	replacement := manager.newRecordRuntime(record, record.root, *record.manifest)
	if err := replacement.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	record.mu.Lock()
	record.runtime, record.state = replacement, pluginStateRunning
	record.mu.Unlock()
	if _, err := click(fresh, 0); contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("click across runtime replacement err = %v", err)
	}
	renewed := search()
	if renewed.ProviderRuntime.Generation != fresh.ProviderRuntime.Generation || renewed.ProviderRuntime.Instance == fresh.ProviderRuntime.Instance {
		t.Fatalf("replacement identity %+v vs %+v", renewed.ProviderRuntime, fresh.ProviderRuntime)
	}
	if _, err := click(renewed, 0); err != nil {
		t.Fatalf("click on the replacement = %v", err)
	}

	// Losing workspace.read or the provider contribution invalidates clicks.
	record.mu.Lock()
	record.manifest.Capabilities = nil
	record.mu.Unlock()
	if _, err := click(renewed, 0); contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("click without workspace.read err = %v", err)
	}
	record.mu.Lock()
	record.manifest.Capabilities = []api.Capability{api.CapabilityWorkspaceRead}
	contributions := record.manifest.contributions
	record.manifest.contributions = map[string]*resolvedContribution{}
	record.mu.Unlock()
	if _, err := click(renewed, 0); contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("click after provider removal err = %v", err)
	}
	record.mu.Lock()
	record.manifest.contributions = contributions
	record.mu.Unlock()
	if err := manager.SetEnabled(context.Background(), record.id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := click(renewed, 0); contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("click on a disabled plugin err = %v", err)
	}
	manager.mu.Lock()
	delete(manager.records, record.id)
	manager.mu.Unlock()
	if _, err := click(renewed, 0); contributionErrorCode(err) != contributionErrUnavailable {
		t.Fatalf("click after uninstall = %v", err)
	}

}
