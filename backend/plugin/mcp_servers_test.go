package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"mincode/app/backend/mcp"
	"mincode/app/plugins/api"
)

const mcpServerTestPlugin = "mcp.fixture"

func decodeMcpServerForTest(ctx *pointContext, raw string) (McpServerContribution, error) {
	value, _, err := decodeMcpServerEntry(ctx, 0, "", json.RawMessage(raw))
	if err != nil {
		return McpServerContribution{}, err
	}
	return value.(McpServerContribution), nil
}

func mcpServerTestContext(t *testing.T) *pointContext {
	t.Helper()
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/server")
	return &pointContext{
		root:     root,
		manifest: &Manifest{ID: mcpServerTestPlugin, APIVersion: 2, Capabilities: []api.Capability{api.CapabilityShell}},
		spec:     mcpServerSpec,
	}
}

func TestMcpServerDecode(t *testing.T) {
	ctx := mcpServerTestContext(t)
	server, err := decodeMcpServerForTest(ctx, `{"id":"demo","title":"Demo","titleLocalized":{"zh":"\u6f14\u793a"},"description":"Demo tools","descriptionLocalized":{"zh":"\u6f14\u793a\u5de5\u5177"},"command":"${pluginRoot}/bin/server","args":["--root=${projectRoot}"],"env":{"DEMO_LOG":"${pluginRoot}/log"},"platforms":["linux","darwin"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if server.Description != "Demo tools" || server.DescriptionLocalized["zh"] == "" || strings.Join(server.Platforms, ",") != "darwin,linux" || len(server.SpecFingerprint) != 64 {
		t.Fatalf("server = %+v", server)
	}
	bare, err := decodeMcpServerForTest(ctx, `{"id":"fs","title":"FS","command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","${projectRoot}"]}`)
	if err != nil || len(bare.Platforms) != 3 || bare.Description != "" {
		t.Fatalf("bare = %+v, %v", bare, err)
	}
}

func TestMcpServerDecodeRequiresProject(t *testing.T) {
	ctx := mcpServerTestContext(t)
	byDefault, err := decodeMcpServerForTest(ctx, `{"id":"d","title":"D","command":"npx"}`)
	if err != nil || !byDefault.RequiresProject {
		t.Fatalf("servers need a project by default: %+v, %v", byDefault, err)
	}
	free, err := decodeMcpServerForTest(ctx, `{"id":"d","title":"D","command":"${pluginRoot}/bin/server","args":["mcp","${pluginRoot}/x"],"requiresProject":false}`)
	if err != nil || free.RequiresProject {
		t.Fatalf("project-less = %+v, %v", free, err)
	}
	same, _ := decodeMcpServerForTest(ctx, `{"id":"d","title":"D","command":"${pluginRoot}/bin/server","args":["mcp","${pluginRoot}/x"]}`)
	if same.SpecFingerprint == free.SpecFingerprint {
		t.Fatal("the flag must be part of the spec fingerprint")
	}
}

func TestMcpServerDecodeRejects(t *testing.T) {
	ctx := mcpServerTestContext(t)
	cases := map[string]string{
		"bad id":             `{"id":"Demo","title":"D","command":"npx"}`,
		"no title":           `{"id":"d","command":"npx"}`,
		"no command":         `{"id":"d","title":"D"}`,
		"absolute command":   `{"id":"d","title":"D","command":"/usr/bin/npx"}`,
		"relative command":   `{"id":"d","title":"D","command":"./npx"}`,
		"missing executable": `{"id":"d","title":"D","command":"${pluginRoot}/bin/none"}`,
		"project command":    `{"id":"d","title":"D","command":"${projectRoot}/x"}`,
		"unknown variable":   `{"id":"d","title":"D","command":"npx","args":["${env:HOME}"]}`,
		"env expansion":      `{"id":"d","title":"D","command":"npx","env":{"TOKEN":"${env:TOKEN}"}}`,
		"denied env":         `{"id":"d","title":"D","command":"npx","env":{"NODE_OPTIONS":"x"}}`,
		"denied env prefix":  `{"id":"d","title":"D","command":"npx","env":{"MINCODE_X":"x"}}`,
		"cwd field":          `{"id":"d","title":"D","command":"npx","cwd":"${projectRoot}"}`,
		"languages field":    `{"id":"d","title":"D","command":"npx","languages":["go"]}`,
		"empty description":  `{"id":"d","title":"D","command":"npx","description":""}`,
		"long description":   `{"id":"d","title":"D","command":"npx","description":"` + strings.Repeat("a", 201) + `"}`,
		"bidi arg":           `{"id":"d","title":"D","command":"npx","args":["a` + "\u202e" + `"]}`,
		"bad platform":       `{"id":"d","title":"D","command":"npx","platforms":["plan9"]}`,
		"project-less arg":   `{"id":"d","title":"D","command":"npx","requiresProject":false,"args":["--root=${projectRoot}"]}`,
		"project-less env":   `{"id":"d","title":"D","command":"npx","requiresProject":false,"env":{"ROOT":"${projectRoot}"}}`,
		"non-bool project":   `{"id":"d","title":"D","command":"npx","requiresProject":"no"}`,
	}
	for name, raw := range cases {
		if _, err := decodeMcpServerForTest(ctx, raw); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

type mcpServerFixture struct {
	manager *pluginManager
	record  *pluginRecord
	root    string
	project string
	started []mcp.PluginStdioSpec
	live    map[string]string
	owners  []string
}

func newMcpServerFixture(t *testing.T) *mcpServerFixture {
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
		"id": mcpServerTestPlugin, "name": "MCP fixture", "version": "1.0.0", "apiVersion": 2,
		"category": "development", "capabilities": []string{"shell"},
		"runtime": map[string]any{"type": "process", "entry": "bin/plugin.mjs"},
		"contributes": map[string]any{
			"mcpServers": []any{map[string]any{
				"id": "demo", "title": "Demo MCP", "description": "Demo tools",
				"command": "${pluginRoot}/bin/server",
				"args":    []string{"${pluginRoot}/server/main.mjs", "--root=${projectRoot}"},
				"env":     map[string]string{"DEMO_ROOT": "${projectRoot}", "DEMO_MODE": "literal $HOME"},
			}, map[string]any{
				"id": "desk", "title": "Desktop MCP", "requiresProject": false,
				"command": "${pluginRoot}/bin/server", "args": []string{"mcp"},
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
		id: mcpServerTestPlugin, root: root, source: pluginSourceLocal, manifest: &parsed,
		enabled: true, state: pluginStateRunning, capabilities: append([]api.Capability(nil), parsed.Capabilities...),
	}
	fixture := &mcpServerFixture{
		manager: &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}},
		record:  record, root: root, project: t.TempDir(), live: map[string]string{},
	}
	previous := []any{mcpServerConnect, mcpServerLive, mcpServerDisconnect, mcpServerDisconnectOwner, mcpServerNow, terminalProfileLoginPath}
	liveKey := func(fp, dir string) string { return fp + "\x00" + dir }
	mcpServerConnect = func(spec mcp.PluginStdioSpec) (*mcp.McpConnectResult, bool, error) {
		if fixture.live[spec.ServerID] == liveKey(spec.Fingerprint, spec.Dir) {
			return &mcp.McpConnectResult{}, true, nil
		}
		if spec.BeforeExec != nil {
			if err := spec.BeforeExec(); err != nil {
				return nil, false, err
			}
		}
		fixture.started = append(fixture.started, spec)
		fixture.live[spec.ServerID] = liveKey(spec.Fingerprint, spec.Dir)
		return &mcp.McpConnectResult{Tools: []mcp.McpToolDef{{Name: "echo"}}}, false, nil
	}
	mcpServerLive = func(id, fp, dir string) bool { return fp != "" && fixture.live[id] == liveKey(fp, dir) }
	mcpServerDisconnect = func(id string) bool {
		_, ok := fixture.live[id]
		delete(fixture.live, id)
		return ok
	}
	mcpServerDisconnectOwner = func(pluginID string) []string {
		fixture.owners = append(fixture.owners, pluginID)
		fixture.live = map[string]string{}
		return nil
	}
	terminalProfileLoginPath = func() string { return "" }
	t.Cleanup(func() {
		mcpServerConnect = previous[0].(func(mcp.PluginStdioSpec) (*mcp.McpConnectResult, bool, error))
		mcpServerLive = previous[1].(func(string, string, string) bool)
		mcpServerDisconnect = previous[2].(func(string) bool)
		mcpServerDisconnectOwner = previous[3].(func(string) []string)
		mcpServerNow = previous[4].(func() time.Time)
		terminalProfileLoginPath = previous[5].(func() string)
		forgetMcpServerStarts(func(string) bool { return true })
	})
	return fixture
}

func (fixture *mcpServerFixture) request(project, fingerprint string) mcpServerRequest {
	return mcpServerRequest{PluginID: mcpServerTestPlugin, ServerID: "demo", ProjectRoot: project, Fingerprint: fingerprint}
}

func (fixture *mcpServerFixture) trust(t *testing.T) McpServerLaunch {
	t.Helper()
	launch, err := fixture.manager.prepareMcpServer(fixture.request(fixture.project, ""))
	if err != nil {
		t.Fatal(err)
	}
	if launch.Trusted {
		t.Fatal("a new MCP server must not be trusted")
	}
	trusted, err := fixture.manager.trustMcpServer(fixture.request(fixture.project, launch.Fingerprint))
	if err != nil || !trusted.Trusted {
		t.Fatalf("trust = %+v, %v", trusted, err)
	}
	return trusted
}

func TestMcpServerLaunchLifecycle(t *testing.T) {
	fixture := newMcpServerFixture(t)
	if _, err := fixture.manager.startMcpServer(fixture.request(fixture.project, "x")); processLaunchErrorCode(err) != processLaunchErrUntrusted {
		t.Fatalf("an unconfirmed start must be refused, got %v", err)
	}
	launch := fixture.trust(t)
	if launch.Cwd != "${projectRoot}" || launch.SessionServerID != "plugin:mcp.fixture/demo" || launch.Description != "Demo tools" || len(launch.PluginFiles) != 1 {
		t.Fatalf("launch = %+v", launch)
	}
	if !strings.Contains(strings.Join(launch.Argv, " "), "--root=${projectRoot}") || launch.Env["DEMO_ROOT"] != "${projectRoot}" {
		t.Fatalf("${projectRoot} must stay deferred in the confirmation: %+v", launch)
	}
	other := t.TempDir()
	otherLaunch, err := fixture.manager.prepareMcpServer(fixture.request(other, ""))
	if err != nil || !otherLaunch.Trusted || otherLaunch.Fingerprint != launch.Fingerprint {
		t.Fatalf("another project = %+v, %v", otherLaunch, err)
	}
	if _, err := fixture.manager.startMcpServer(fixture.request(fixture.project, "stale")); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("a stale fingerprint must be refused, got %v", err)
	}
	result, err := fixture.manager.startMcpServer(fixture.request(fixture.project, launch.Fingerprint))
	if err != nil || result.Reused || result.SessionServerID != "plugin:mcp.fixture/demo" || len(result.Tools) != 1 {
		t.Fatalf("start = %+v, %v", result, err)
	}
	spec := fixture.started[0]
	canonical, _ := canonicalProjectLaunchRoot(fixture.project, "MCP servers")
	if spec.Dir != canonical || spec.Args[len(spec.Args)-1] != "--root="+canonical || spec.OwnerPluginID != mcpServerTestPlugin {
		t.Fatalf("started spec = %+v", spec)
	}
	if !slices.Contains(spec.Env, "DEMO_ROOT="+canonical) || !slices.Contains(spec.Env, "DEMO_MODE=literal $HOME") {
		t.Fatalf("env = %v", spec.Env)
	}
	if spec.StderrMaxBytes != 65536 || spec.Valid == nil || !spec.Valid() || spec.BeforeExec == nil {
		t.Fatalf("limits = %+v", spec)
	}
	for range 10 {
		if result, err := fixture.manager.startMcpServer(fixture.request(fixture.project, launch.Fingerprint)); err != nil || !result.Reused {
			t.Fatalf("reuse = %+v, %v", result, err)
		}
	}
	if len(fixture.started) != 1 {
		t.Fatalf("reuse started %d processes", len(fixture.started))
	}
	// Another project replaces the session.
	if _, err := fixture.manager.startMcpServer(fixture.request(other, launch.Fingerprint)); err != nil || len(fixture.started) != 2 {
		t.Fatalf("other project start: %v (%d)", err, len(fixture.started))
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "server", "main.mjs"), []byte("// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	forgetProcessLaunchHash(filepath.Join(fixture.root, "server", "main.mjs"))
	if changed, err := fixture.manager.prepareMcpServer(fixture.request(fixture.project, "")); err != nil || changed.Trusted || !changed.PreviouslyTrusted {
		t.Fatalf("changed = %+v, %v", changed, err)
	}
}

func TestMcpServerWithoutProject(t *testing.T) {
	fixture := newMcpServerFixture(t)
	desk := func(project, fingerprint string) mcpServerRequest {
		return mcpServerRequest{PluginID: mcpServerTestPlugin, ServerID: "desk", ProjectRoot: project, Fingerprint: fingerprint}
	}
	if _, err := fixture.manager.prepareMcpServer(fixture.request("", "")); err == nil {
		t.Fatal("a server that needs a project must still refuse an empty projectRoot")
	}
	if _, err := fixture.manager.prepareMcpServer(desk(fixture.project, "")); processLaunchErrorCode(err) != processLaunchErrForbidden {
		t.Fatalf("a project-less server must refuse a projectRoot, got %v", err)
	}
	launch, err := fixture.manager.prepareMcpServer(desk("", ""))
	if err != nil || launch.Trusted || launch.RequiresProject || launch.ProjectRoot != "" || launch.Cwd != "${pluginRoot}" {
		t.Fatalf("prepare = %+v, %v", launch, err)
	}
	if _, err := fixture.manager.startMcpServer(desk("", launch.Fingerprint)); processLaunchErrorCode(err) != processLaunchErrUntrusted {
		t.Fatalf("an unconfirmed project-less start must be refused, got %v", err)
	}
	trusted, err := fixture.manager.trustMcpServer(desk("", launch.Fingerprint))
	if err != nil || !trusted.Trusted {
		t.Fatalf("trust = %+v, %v", trusted, err)
	}
	result, err := fixture.manager.startMcpServer(desk("", launch.Fingerprint))
	if err != nil || result.SessionServerID != "plugin:mcp.fixture/desk" {
		t.Fatalf("start = %+v, %v", result, err)
	}
	pluginRoot, _ := processLaunchPluginRoot(fixture.root)
	if spec := fixture.started[len(fixture.started)-1]; spec.Dir != pluginRoot || strings.Join(spec.Args, " ") != "mcp" {
		t.Fatalf("a project-less server runs in the plugin root: %+v", spec)
	}
	if again, err := fixture.manager.startMcpServer(desk("", launch.Fingerprint)); err != nil || !again.Reused {
		t.Fatalf("reuse = %+v, %v", again, err)
	}
}

func TestMcpServerRestartLimitAndStop(t *testing.T) {
	fixture := newMcpServerFixture(t)
	launch := fixture.trust(t)
	now := time.Unix(1_700_000_000, 0)
	mcpServerNow = func() time.Time { return now }
	crash := func() { fixture.live = map[string]string{} }
	for index := range 5 {
		if _, err := fixture.manager.startMcpServer(fixture.request(fixture.project, launch.Fingerprint)); err != nil {
			t.Fatalf("start %d: %v", index, err)
		}
		crash()
	}
	if _, err := fixture.manager.startMcpServer(fixture.request(fixture.project, launch.Fingerprint)); processLaunchErrorCode(err) != processLaunchErrLimit {
		t.Fatalf("the sixth start in the window must be refused, got %v", err)
	}
	now = now.Add(10*time.Minute + time.Second)
	if _, err := fixture.manager.startMcpServer(fixture.request(fixture.project, launch.Fingerprint)); err != nil {
		t.Fatalf("the window must expire: %v", err)
	}
	if stopped, err := fixture.manager.stopMcpServer(fixture.request(fixture.project, "")); err != nil || !stopped {
		t.Fatalf("stop = %v, %v", stopped, err)
	}
	if _, err := fixture.manager.stopMcpServer(mcpServerRequest{PluginID: "../x", ServerID: "demo"}); err == nil {
		t.Fatal("a malformed plugin id must be refused")
	}
}

func TestMcpServerGatesAndValidity(t *testing.T) {
	fixture := newMcpServerFixture(t)
	launch := fixture.trust(t)
	if _, err := fixture.manager.startMcpServer(fixture.request(fixture.project, launch.Fingerprint)); err != nil {
		t.Fatal(err)
	}
	valid := fixture.started[0].Valid
	fixture.record.mu.Lock()
	fixture.record.state = pluginStateError
	fixture.record.mu.Unlock()
	if !valid() {
		t.Fatal("a plugin process crash must not invalidate its MCP servers")
	}
	fixture.record.mu.Lock()
	fixture.record.state = pluginStateRunning
	fixture.record.enabled = false
	fixture.record.mu.Unlock()
	if valid() {
		t.Fatal("a disabled plugin must invalidate the session")
	}
	if _, err := fixture.manager.prepareMcpServer(fixture.request(fixture.project, "")); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("a disabled plugin must be refused, got %v", err)
	}
	fixture.manager.runRecordLifecycleHooks(fixture.record)
	if !slices.Contains(fixture.owners, mcpServerTestPlugin) {
		t.Fatalf("disable must stop the servers: %v", fixture.owners)
	}
	fixture.record.mu.Lock()
	fixture.record.enabled = true
	fixture.record.capabilities = nil
	fixture.record.mu.Unlock()
	if valid() {
		t.Fatal("a revoked shell capability must invalidate the session")
	}
	if _, err := fixture.manager.prepareMcpServer(fixture.request(fixture.project, "")); processLaunchErrorCode(err) != processLaunchErrForbidden {
		t.Fatalf("no shell must be forbidden, got %v", err)
	}
	fixture.record.mu.Lock()
	fixture.record.capabilities = []api.Capability{api.CapabilityShell}
	fixture.record.mu.Unlock()
	if _, err := fixture.manager.prepareMcpServer(mcpServerRequest{PluginID: mcpServerTestPlugin, ServerID: "nope", ProjectRoot: fixture.project}); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("an unknown server must be unavailable, got %v", err)
	}
	fixture.owners = nil
	fixture.manager.runRecordRemovalHooks(mcpServerTestPlugin, removalReasonUninstall)
	if !slices.Contains(fixture.owners, mcpServerTestPlugin) {
		t.Fatalf("uninstall must stop the servers: %v", fixture.owners)
	}
}
