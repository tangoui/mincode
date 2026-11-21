package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/lsp"
	"mincode/app/plugins/api"
)

const languageServerTestPlugin = "lsp.fixture"

func languageServerTestContext(t *testing.T) *pointContext {
	t.Helper()
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/server")
	return &pointContext{
		root:     root,
		manifest: &Manifest{ID: languageServerTestPlugin, APIVersion: 2, Capabilities: []api.Capability{api.CapabilityShell}},
		spec:     languageServerSpec,
	}
}

func decodeLanguageServerForTest(ctx *pointContext, raw string) (LanguageServerContribution, error) {
	value, _, err := decodeLanguageServerEntry(ctx, 0, "", json.RawMessage(raw))
	if err != nil {
		return LanguageServerContribution{}, err
	}
	return value.(LanguageServerContribution), nil
}

func TestLanguageServerDecode(t *testing.T) {
	ctx := languageServerTestContext(t)
	server, err := decodeLanguageServerForTest(ctx, `{"id":"demo","title":"Demo","titleLocalized":{"zh":"\u6f14\u793a"},"languages":["mcdemo","mcdemo.x"],"command":"${pluginRoot}/bin/server","args":["--stdio","--root=${projectRoot}"],"env":{"DEMO_LOG":"${pluginRoot}/log"},"initializationOptions":{ "b" : 1, "a" : [true, {"x": null}] },"platforms":["linux","darwin"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if string(server.InitializationOptions) != `{"b":1,"a":[true,{"x":null}]}` || strings.Join(server.Platforms, ",") != "darwin,linux" || len(server.SpecFingerprint) != 64 {
		t.Fatalf("server = %+v", server)
	}
	bare, err := decodeLanguageServerForTest(ctx, `{"id":"ts","title":"TS","languages":["typescript"],"command":"typescript-language-server","args":["--stdio"]}`)
	if err != nil || len(bare.Platforms) != 3 || bare.InitializationOptions != nil {
		t.Fatalf("bare = %+v, %v", bare, err)
	}
}

func TestLanguageServerDecodeRejects(t *testing.T) {
	ctx := languageServerTestContext(t)
	deep := strings.Repeat(`{"a":`, 17) + "1" + strings.Repeat("}", 17)
	okDeep := strings.Repeat(`{"a":`, 16) + "1" + strings.Repeat("}", 16)
	if _, err := decodeLanguageServerForTest(ctx, `{"id":"d","title":"D","languages":["go"],"command":"gopls","initializationOptions":`+okDeep+`}`); err != nil {
		t.Fatalf("16 levels must be accepted: %v", err)
	}
	cases := map[string]string{
		"bad id":             `{"id":"Demo","title":"D","languages":["go"],"command":"gopls"}`,
		"no languages":       `{"id":"d","title":"D","languages":[],"command":"gopls"}`,
		"bad language":       `{"id":"d","title":"D","languages":["Go"],"command":"gopls"}`,
		"duplicate language": `{"id":"d","title":"D","languages":["go","go"],"command":"gopls"}`,
		"too many languages": `{"id":"d","title":"D","languages":["a","b","c","d","e","f","g","h","i"],"command":"gopls"}`,
		"no command":         `{"id":"d","title":"D","languages":["go"]}`,
		"absolute command":   `{"id":"d","title":"D","languages":["go"],"command":"/usr/bin/gopls"}`,
		"relative command":   `{"id":"d","title":"D","languages":["go"],"command":"./gopls"}`,
		"missing executable": `{"id":"d","title":"D","languages":["go"],"command":"${pluginRoot}/bin/none"}`,
		"escaping command":   `{"id":"d","title":"D","languages":["go"],"command":"${pluginRoot}/../x"}`,
		"project command":    `{"id":"d","title":"D","languages":["go"],"command":"${projectRoot}/gopls"}`,
		"unknown variable":   `{"id":"d","title":"D","languages":["go"],"command":"gopls","args":["${workspaceFolder}"]}`,
		"denied env":         `{"id":"d","title":"D","languages":["go"],"command":"gopls","env":{"NODE_OPTIONS":"x"}}`,
		"denied env prefix":  `{"id":"d","title":"D","languages":["go"],"command":"gopls","env":{"DYLD_X":"x"}}`,
		"cwd field":          `{"id":"d","title":"D","languages":["go"],"command":"gopls","cwd":"${projectRoot}"}`,
		"bidi arg":           `{"id":"d","title":"D","languages":["go"],"command":"gopls","args":["a` + "\u202e" + `"]}`,
		"init not object":    `{"id":"d","title":"D","languages":["go"],"command":"gopls","initializationOptions":[1]}`,
		"init too deep":      `{"id":"d","title":"D","languages":["go"],"command":"gopls","initializationOptions":` + deep + `}`,
		"init too large":     `{"id":"d","title":"D","languages":["go"],"command":"gopls","initializationOptions":{"x":"` + strings.Repeat("a", 16400) + `"}}`,
		"init duplicate key": `{"id":"d","title":"D","languages":["go"],"command":"gopls","initializationOptions":{"a":1,"a":2}}`,
		"bad platform":       `{"id":"d","title":"D","languages":["go"],"command":"gopls","platforms":["plan9"]}`,
	}
	for name, raw := range cases {
		if _, err := decodeLanguageServerForTest(ctx, raw); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

type languageServerFixture struct {
	manager *pluginManager
	record  *pluginRecord
	root    string
	project string
	started []lsp.ExternalServerSpec
	live    map[string]string
	stopped []string
}

func newLanguageServerFixture(t *testing.T) *languageServerFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	useTemporaryPluginData(t)
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/server")
	for path, content := range map[string]string{"bin/plugin.mjs": "\n", "server/main.mjs": "// server\n"} {
		full := filepath.Join(root, filepath.FromSlash(path))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := map[string]any{
		"id": languageServerTestPlugin, "name": "LSP fixture", "version": "1.0.0", "apiVersion": 2,
		"category": "development", "capabilities": []string{"shell"},
		"runtime": map[string]any{"type": "process", "entry": "bin/plugin.mjs"},
		"contributes": map[string]any{
			"languageServers": []any{map[string]any{
				"id": "demo", "title": "Demo LSP", "languages": []string{"mcdemo"},
				"command":               "${pluginRoot}/bin/server",
				"args":                  []string{"${pluginRoot}/server/main.mjs", "--stdio", "--root=${projectRoot}"},
				"env":                   map[string]string{"DEMO_ROOT": "${projectRoot}"},
				"initializationOptions": map[string]any{"lint": true},
			}},
		},
	}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	preview, err := inspectInstallDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	parsed := preview.Manifest
	record := &pluginRecord{
		id: languageServerTestPlugin, root: root, source: pluginSourceLocal, manifest: &parsed,
		enabled: true, state: pluginStateRunning, capabilities: append([]api.Capability(nil), parsed.Capabilities...),
	}
	fixture := &languageServerFixture{
		manager: &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}},
		record:  record, root: root, project: t.TempDir(), live: map[string]string{},
	}
	previous := []any{languageServerStartExternal, languageServerExternalLive, languageServerStopExternal, languageServerStopForOwnerFunc, languageServerNow, terminalProfileLoginPath}
	languageServerStartExternal = func(root string, spec lsp.ExternalServerSpec) (lsp.ServerStatus, error) {
		if fp, ok := fixture.live[spec.ServerID]; ok && fp == spec.Fingerprint {
			return lsp.ServerStatus{ID: spec.ServerID, Running: true}, nil
		}
		if spec.BeforeExec != nil {
			if err := spec.BeforeExec(); err != nil {
				return lsp.ServerStatus{}, err
			}
		}
		fixture.started = append(fixture.started, spec)
		fixture.live[spec.ServerID] = spec.Fingerprint
		return lsp.ServerStatus{ID: spec.ServerID, Running: true}, nil
	}
	languageServerExternalLive = func(_, id, fp string) bool { return fixture.live[id] == fp && fp != "" }
	languageServerStopExternal = func(_, id, _ string) bool {
		_, ok := fixture.live[id]
		delete(fixture.live, id)
		return ok
	}
	languageServerStopForOwnerFunc = func(pluginID, reason string) []string {
		fixture.stopped = append(fixture.stopped, pluginID+":"+reason)
		fixture.live = map[string]string{}
		return nil
	}
	terminalProfileLoginPath = func() string { return "" }
	t.Cleanup(func() {
		languageServerStartExternal = previous[0].(func(string, lsp.ExternalServerSpec) (lsp.ServerStatus, error))
		languageServerExternalLive = previous[1].(func(string, string, string) bool)
		languageServerStopExternal = previous[2].(func(string, string, string) bool)
		languageServerStopForOwnerFunc = previous[3].(func(string, string) []string)
		languageServerNow = previous[4].(func() time.Time)
		terminalProfileLoginPath = previous[5].(func() string)
		forgetLanguageServerStarts(func(string) bool { return true })
	})
	return fixture
}

func (fixture *languageServerFixture) request(project, fingerprint string) languageServerRequest {
	return languageServerRequest{PluginID: languageServerTestPlugin, ServerID: "demo", ProjectRoot: project, Fingerprint: fingerprint}
}

func (fixture *languageServerFixture) trust(t *testing.T) LanguageServerLaunch {
	t.Helper()
	launch, err := fixture.manager.prepareLanguageServer(fixture.request(fixture.project, ""))
	if err != nil {
		t.Fatal(err)
	}
	if launch.Trusted {
		t.Fatal("a new language server must not be trusted")
	}
	trusted, err := fixture.manager.trustLanguageServer(fixture.request(fixture.project, launch.Fingerprint))
	if err != nil || !trusted.Trusted {
		t.Fatalf("trust = %+v, %v", trusted, err)
	}
	return trusted
}

func TestLanguageServerLaunchLifecycle(t *testing.T) {
	fixture := newLanguageServerFixture(t)
	if _, err := fixture.manager.startLanguageServer(fixture.request(fixture.project, "x")); processLaunchErrorCode(err) != processLaunchErrUntrusted {
		t.Fatalf("an unconfirmed start must be refused, got %v", err)
	}
	launch := fixture.trust(t)
	if launch.Cwd != "${projectRoot}" || launch.SessionServerID != "plugin:lsp.fixture/demo" || len(launch.PluginFiles) != 1 {
		t.Fatalf("launch = %+v", launch)
	}
	if !strings.Contains(strings.Join(launch.Argv, " "), "--root=${projectRoot}") || launch.Env["DEMO_ROOT"] != "${projectRoot}" {
		t.Fatalf("${projectRoot} must stay deferred in the confirmation: %+v", launch)
	}
	// One confirmation covers every project.
	other := t.TempDir()
	otherLaunch, err := fixture.manager.prepareLanguageServer(fixture.request(other, ""))
	if err != nil || !otherLaunch.Trusted || otherLaunch.Fingerprint != launch.Fingerprint {
		t.Fatalf("another project = %+v, %v", otherLaunch, err)
	}
	if _, err := fixture.manager.startLanguageServer(fixture.request(fixture.project, "stale")); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("a stale fingerprint must be refused, got %v", err)
	}
	if _, err := fixture.manager.startLanguageServer(fixture.request(fixture.project, launch.Fingerprint)); err != nil {
		t.Fatal(err)
	}
	spec := fixture.started[0]
	canonical, _ := canonicalLanguageServerRoot(fixture.project)
	if spec.Args[len(spec.Args)-1] != "--root="+canonical || spec.Env["DEMO_ROOT"] != canonical || string(spec.InitializationOptions) != `{"lint":true}` {
		t.Fatalf("started spec = %+v", spec)
	}
	if spec.MessageMaxBytes != 4194304 || spec.StderrMaxBytes != 65536 || spec.MaxSessionsPerOwner != 8 || spec.Valid == nil || !spec.Valid() {
		t.Fatalf("limits = %+v", spec)
	}
	// Reusing the live session is not a restart.
	for range 10 {
		if _, err := fixture.manager.startLanguageServer(fixture.request(fixture.project, launch.Fingerprint)); err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.started) != 1 {
		t.Fatalf("reuse started %d processes", len(fixture.started))
	}
	// A changed plugin file needs a new confirmation.
	if err := os.WriteFile(filepath.Join(fixture.root, "server", "main.mjs"), []byte("// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	forgetProcessLaunchHash(filepath.Join(fixture.root, "server", "main.mjs"))
	if changed, err := fixture.manager.prepareLanguageServer(fixture.request(fixture.project, "")); err != nil || changed.Trusted || !changed.PreviouslyTrusted {
		t.Fatalf("changed = %+v, %v", changed, err)
	}
}

func TestLanguageServerRestartLimit(t *testing.T) {
	fixture := newLanguageServerFixture(t)
	launch := fixture.trust(t)
	now := time.Unix(1_700_000_000, 0)
	languageServerNow = func() time.Time { return now }
	crash := func() { fixture.live = map[string]string{} }
	for index := range 5 {
		if _, err := fixture.manager.startLanguageServer(fixture.request(fixture.project, launch.Fingerprint)); err != nil {
			t.Fatalf("start %d: %v", index, err)
		}
		crash()
	}
	if _, err := fixture.manager.startLanguageServer(fixture.request(fixture.project, launch.Fingerprint)); processLaunchErrorCode(err) != processLaunchErrLimit {
		t.Fatalf("the sixth start in the window must be refused, got %v", err)
	}
	// Another project has its own budget.
	if _, err := fixture.manager.startLanguageServer(fixture.request(t.TempDir(), launch.Fingerprint)); err != nil {
		t.Fatal(err)
	}
	crash()
	now = now.Add(10*time.Minute + time.Second)
	if _, err := fixture.manager.startLanguageServer(fixture.request(fixture.project, launch.Fingerprint)); err != nil {
		t.Fatalf("the window must expire: %v", err)
	}
	for range 4 {
		crash()
		_, _ = fixture.manager.startLanguageServer(fixture.request(fixture.project, launch.Fingerprint))
	}
	crash()
	if _, err := fixture.manager.startLanguageServer(fixture.request(fixture.project, launch.Fingerprint)); processLaunchErrorCode(err) != processLaunchErrLimit {
		t.Fatalf("limit again, got %v", err)
	}
	if _, err := fixture.manager.resetLanguageServer(fixture.request(fixture.project, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.startLanguageServer(fixture.request(fixture.project, launch.Fingerprint)); err != nil {
		t.Fatalf("reset must clear the count: %v", err)
	}
	if stopped, err := fixture.manager.resetLanguageServer(fixture.request(fixture.project, "")); err != nil || !stopped {
		t.Fatalf("reset must stop the live session: %v %v", stopped, err)
	}
	if _, err := fixture.manager.resetLanguageServer(languageServerRequest{PluginID: "../x", ServerID: "demo", ProjectRoot: fixture.project}); err == nil {
		t.Fatal("a malformed plugin id must be refused")
	}
}

func TestLanguageServerGatesAndValidity(t *testing.T) {
	fixture := newLanguageServerFixture(t)
	launch := fixture.trust(t)
	if _, err := fixture.manager.startLanguageServer(fixture.request(fixture.project, launch.Fingerprint)); err != nil {
		t.Fatal(err)
	}
	valid := fixture.started[0].Valid
	// A crash of the plugin process keeps the server valid.
	fixture.record.mu.Lock()
	fixture.record.state = pluginStateError
	fixture.record.mu.Unlock()
	if !valid() {
		t.Fatal("a plugin process crash must not invalidate its language servers")
	}
	fixture.record.mu.Lock()
	fixture.record.state = pluginStateRunning
	fixture.record.root = t.TempDir()
	fixture.record.mu.Unlock()
	if valid() {
		t.Fatal("a new development generation (root) must invalidate the session")
	}
	fixture.record.mu.Lock()
	fixture.record.root = fixture.root
	fixture.record.enabled = false
	fixture.record.mu.Unlock()
	if valid() {
		t.Fatal("a disabled plugin must invalidate the session")
	}
	if _, err := fixture.manager.prepareLanguageServer(fixture.request(fixture.project, "")); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("a disabled plugin must be refused, got %v", err)
	}
	fixture.manager.runRecordLifecycleHooks(fixture.record)
	if len(fixture.stopped) != 1 || fixture.stopped[0] != "lsp.fixture:disabled" {
		t.Fatalf("stopped = %v", fixture.stopped)
	}
	fixture.record.mu.Lock()
	fixture.record.enabled = true
	fixture.record.capabilities = nil
	fixture.record.mu.Unlock()
	if valid() {
		t.Fatal("a revoked shell capability must invalidate the session")
	}
	if _, err := fixture.manager.prepareLanguageServer(fixture.request(fixture.project, "")); processLaunchErrorCode(err) != processLaunchErrForbidden {
		t.Fatalf("no shell must be forbidden, got %v", err)
	}
	fixture.record.mu.Lock()
	fixture.record.capabilities = []api.Capability{api.CapabilityShell}
	fixture.record.mu.Unlock()
	for _, bad := range []string{"", "relative", "/"} {
		if _, err := fixture.manager.prepareLanguageServer(fixture.request(bad, "")); err == nil {
			t.Errorf("project root %q must be refused", bad)
		}
	}
	if _, err := fixture.manager.prepareLanguageServer(languageServerRequest{PluginID: languageServerTestPlugin, ServerID: "nope", ProjectRoot: fixture.project}); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("an unknown server must be unavailable, got %v", err)
	}
	fixture.stopped = nil
	fixture.manager.runRecordRemovalHooks(languageServerTestPlugin, removalReasonUninstall)
	if len(fixture.stopped) != 1 || fixture.stopped[0] != "lsp.fixture:uninstall" {
		t.Fatalf("uninstall stopped = %v", fixture.stopped)
	}
}

func TestLanguageServerBeforeExecRefusesSwappedFiles(t *testing.T) {
	fixture := newLanguageServerFixture(t)
	launch := fixture.trust(t)
	previous := languageServerStartExternal
	languageServerStartExternal = func(root string, spec lsp.ExternalServerSpec) (lsp.ServerStatus, error) {
		// Swap the server script between verification and exec.
		path := filepath.Join(fixture.root, "server", "main.mjs")
		_ = os.Remove(path)
		_ = os.WriteFile(path, []byte("// evil\n"), 0o644)
		return previous(root, spec)
	}
	_, err := fixture.manager.startLanguageServer(fixture.request(fixture.project, launch.Fingerprint))
	if processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("a swapped file must be refused, got %v", err)
	}
	if len(fixture.started) != 0 {
		t.Fatal("nothing may start")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected error kind")
	}
}
