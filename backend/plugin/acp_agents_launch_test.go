package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"mincode/app/plugins/api"
)

const acpAgentTestPlugin = "acp.fixture"

type acpAgentFixture struct {
	manager *pluginManager
	record  *pluginRecord
	root    string
	project string
	stopped []string
}

func newAcpAgentFixture(t *testing.T) *acpAgentFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	useTemporaryPluginData(t)
	root := t.TempDir()
	writeTerminalProfileExecutable(t, root, "bin/agent")
	for path, content := range map[string]string{"bin/plugin.mjs": "\n", "agent/main.mjs": "// agent\n"} {
		full := filepath.Join(root, filepath.FromSlash(path))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	otherOS := "linux"
	if runtime.GOOS == "linux" {
		otherOS = "darwin"
	}
	manifest := map[string]any{
		"id": acpAgentTestPlugin, "name": "ACP fixture", "version": "1.0.0", "apiVersion": 2,
		"category": "development", "capabilities": []string{"shell"},
		"runtime": map[string]any{"type": "process", "entry": "bin/plugin.mjs"},
		"contributes": map[string]any{
			"acpAgents": []any{map[string]any{
				"id": "demo", "title": "Demo agent", "description": "Demo ACP agent",
				"command": "${pluginRoot}/bin/agent",
				"args":    []string{"${pluginRoot}/agent/main.mjs", "--root=${projectRoot}"},
				"env":     map[string]string{"DEMO_ROOT": "${projectRoot}", "DEMO_HOME": "${pluginRoot}/home"},
			}, map[string]any{
				"id": "elsewhere", "title": "Other platform agent", "platforms": []string{otherOS},
				"command": "${pluginRoot}/bin/agent",
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
		id: acpAgentTestPlugin, root: root, source: pluginSourceLocal, manifest: &parsed,
		enabled: true, state: pluginStateRunning, capabilities: append([]api.Capability(nil), parsed.Capabilities...),
	}
	fixture := &acpAgentFixture{
		manager: &pluginManager{records: map[string]*pluginRecord{record.id: record}, order: []string{record.id}},
		record:  record, root: root, project: t.TempDir(),
	}
	previousDisconnect, previousLoginPath := acpAgentDisconnectOwner, terminalProfileLoginPath
	acpAgentDisconnectOwner = func(pluginID string) { fixture.stopped = append(fixture.stopped, pluginID) }
	terminalProfileLoginPath = func() string { return "" }
	t.Cleanup(func() {
		acpAgentDisconnectOwner = previousDisconnect
		terminalProfileLoginPath = previousLoginPath
	})
	return fixture
}

func (fixture *acpAgentFixture) request(agentID, fingerprint string) acpAgentRequest {
	return acpAgentRequest{PluginID: acpAgentTestPlugin, AgentID: agentID, ProjectRoot: fixture.project, Fingerprint: fingerprint}
}

func (fixture *acpAgentFixture) trust(t *testing.T) AcpAgentLaunch {
	t.Helper()
	launch, err := fixture.manager.prepareAcpAgent(fixture.request("demo", ""))
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := fixture.manager.trustAcpAgent(fixture.request("demo", launch.Fingerprint))
	if err != nil || !trusted.Trusted {
		t.Fatalf("trust = %+v, %v", trusted, err)
	}
	return trusted
}

func TestAcpAgentPrepareAndTrust(t *testing.T) {
	fixture := newAcpAgentFixture(t)
	launch, err := fixture.manager.prepareAcpAgent(fixture.request("demo", ""))
	if err != nil {
		t.Fatal(err)
	}
	if launch.Trusted || launch.PreviouslyTrusted || launch.AgentID != "demo" || launch.Point != "acpAgents" || launch.Title != "Demo agent" || launch.Description != "Demo ACP agent" {
		t.Fatalf("prepare = %+v", launch)
	}
	if launch.Cwd != "${projectRoot}" || !slices.Contains(launch.Argv, "--root=${projectRoot}") || launch.Env["DEMO_ROOT"] != "${projectRoot}" {
		t.Fatalf("${projectRoot} must stay deferred in the confirmation: %+v", launch)
	}
	if !launch.PluginExecutable || launch.ExecutableSHA256 == "" || len(launch.PluginFiles) != 1 {
		t.Fatalf("the plugin program and files must be hashed: %+v", launch)
	}
	if _, err := fixture.manager.trustAcpAgent(fixture.request("demo", "stale")); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("a stale fingerprint must be refused, got %v", err)
	}
	trusted := fixture.trust(t)
	if trusted.Fingerprint != launch.Fingerprint {
		t.Fatalf("trust changed the fingerprint: %+v", trusted)
	}
	again, err := fixture.manager.prepareAcpAgent(fixture.request("demo", ""))
	if err != nil || !again.Trusted {
		t.Fatalf("prepare after trust = %+v, %v", again, err)
	}
	// One confirmation covers every project.
	other := fixture.request("demo", "")
	other.ProjectRoot = t.TempDir()
	if otherLaunch, err := fixture.manager.prepareAcpAgent(other); err != nil || !otherLaunch.Trusted || otherLaunch.Fingerprint != launch.Fingerprint {
		t.Fatalf("another project = %+v, %v", otherLaunch, err)
	}
	// The confirmation is keyed by plugin id + agent id.
	store, err := readProcessLaunchTrust(acpAgentTestPlugin, acpAgentPointKey)
	if err != nil || store.Entries["demo"].Fingerprint != launch.Fingerprint {
		t.Fatalf("trust store = %+v, %v", store, err)
	}
	if _, err := fixture.manager.prepareAcpAgent(acpAgentRequest{PluginID: acpAgentTestPlugin, AgentID: "demo"}); processLaunchErrorCode(err) != processLaunchErrForbidden {
		t.Fatalf("an empty projectRoot must be refused, got %v", err)
	}
	if _, err := fixture.manager.prepareAcpAgent(fixture.request("nope", "")); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("an unknown agent must be unavailable, got %v", err)
	}
}

func TestAcpAgentFingerprintChangesWithExecutableHash(t *testing.T) {
	fixture := newAcpAgentFixture(t)
	launch := fixture.trust(t)
	executable := filepath.Join(fixture.root, "bin", "agent")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\necho changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	forgetProcessLaunchHash(executable)
	changed, err := fixture.manager.prepareAcpAgent(fixture.request("demo", ""))
	if err != nil || changed.Trusted || !changed.PreviouslyTrusted || changed.Fingerprint == launch.Fingerprint {
		t.Fatalf("changed program = %+v, %v", changed, err)
	}
	if _, _, _, _, err := fixture.manager.resolveTrustedAcpAgent(acpAgentTestPlugin, "demo", fixture.project); processLaunchErrorCode(err) != processLaunchErrUntrusted {
		t.Fatalf("a changed program must need a new confirmation, got %v", err)
	}
}

func TestAcpLaunchPluginRefusesUntrusted(t *testing.T) {
	fixture := newAcpAgentFixture(t)
	if _, _, _, _, err := fixture.manager.resolveTrustedAcpAgent(acpAgentTestPlugin, "demo", fixture.project); processLaunchErrorCode(err) != processLaunchErrUntrusted {
		t.Fatalf("an unconfirmed agent must be refused, got %v", err)
	}
	fixture.trust(t)
	command, args, env, beforeExec, err := fixture.manager.resolveTrustedAcpAgent(acpAgentTestPlugin, "demo", fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	pluginRoot, _ := processLaunchPluginRoot(fixture.root)
	canonical, _ := canonicalProjectLaunchRoot(fixture.project, "ACP agents")
	if command != filepath.Join(pluginRoot, "bin", "agent") {
		t.Fatalf("command = %q", command)
	}
	if len(args) != 2 || args[0] != filepath.Join(pluginRoot, "agent", "main.mjs") || args[1] != "--root="+canonical {
		t.Fatalf("args = %v", args)
	}
	if len(env) != 2 || env["DEMO_ROOT"] != canonical || env["DEMO_HOME"] != pluginRoot+"/home" {
		t.Fatalf("env = %v", env)
	}
	if beforeExec == nil || beforeExec() != nil {
		t.Fatal("a confirmed launch must pin its plugin files until the exec")
	}
	if _, _, _, _, err := fixture.manager.resolveTrustedAcpAgent(acpAgentTestPlugin, "demo", ""); processLaunchErrorCode(err) != processLaunchErrForbidden {
		t.Fatalf("an empty project must be refused, got %v", err)
	}
	if _, _, _, _, err := fixture.manager.resolveTrustedAcpAgent(acpAgentTestPlugin, "demo", "/"); err == nil {
		t.Fatal("the filesystem root must be refused")
	}
}

// The plugin's own process may swap its program between the resolve and the
// exec; the pre-exec check refuses the swapped file.
func TestAcpAgentLaunchRefusesProgramSwappedBeforeExec(t *testing.T) {
	fixture := newAcpAgentFixture(t)
	fixture.trust(t)
	_, _, _, beforeExec, err := fixture.manager.resolveTrustedAcpAgent(acpAgentTestPlugin, "demo", fixture.project)
	if err != nil || beforeExec == nil {
		t.Fatalf("resolve = %v (beforeExec nil: %v)", err, beforeExec == nil)
	}
	executable := filepath.Join(fixture.root, "bin", "agent")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\necho swapped and longer\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := beforeExec(); processLaunchErrorCode(err) != processLaunchErrChanged {
		t.Fatalf("a program swapped after the resolve must be refused, got %v", err)
	}
}

func TestAcpLaunchPluginRefusesDisabled(t *testing.T) {
	fixture := newAcpAgentFixture(t)
	fixture.trust(t)
	fixture.record.mu.Lock()
	fixture.record.enabled = false
	fixture.record.mu.Unlock()
	if _, _, _, _, err := fixture.manager.resolveTrustedAcpAgent(acpAgentTestPlugin, "demo", fixture.project); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("a disabled plugin must be unavailable, got %v", err)
	}
	if _, err := fixture.manager.prepareAcpAgent(fixture.request("demo", "")); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("prepare for a disabled plugin must be unavailable, got %v", err)
	}
	fixture.record.mu.Lock()
	fixture.record.enabled = true
	fixture.record.capabilities = nil
	fixture.record.mu.Unlock()
	if _, _, _, _, err := fixture.manager.resolveTrustedAcpAgent(acpAgentTestPlugin, "demo", fixture.project); processLaunchErrorCode(err) != processLaunchErrForbidden {
		t.Fatalf("a plugin without shell must be forbidden, got %v", err)
	}
}

func TestAcpAgentPlatformFilter(t *testing.T) {
	fixture := newAcpAgentFixture(t)
	if _, err := fixture.manager.prepareAcpAgent(fixture.request("elsewhere", "")); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("an agent for another platform must be unavailable, got %v", err)
	}
	if _, _, _, _, err := fixture.manager.resolveTrustedAcpAgent(acpAgentTestPlugin, "elsewhere", fixture.project); processLaunchErrorCode(err) != processLaunchErrUnavailable {
		t.Fatalf("launching an agent for another platform must be unavailable, got %v", err)
	}
}

func TestStopPluginAcpAgentsDisconnects(t *testing.T) {
	fixture := newAcpAgentFixture(t)
	fixture.trust(t)
	stopPluginAcpAgents(acpAgentTestPlugin)
	if !slices.Equal(fixture.stopped, []string{acpAgentTestPlugin}) {
		t.Fatalf("stopPluginAcpAgents = %v", fixture.stopped)
	}
	// A plugin process crash (error state) disconnects its agents.
	fixture.stopped = nil
	fixture.record.mu.Lock()
	fixture.record.state = pluginStateError
	fixture.record.mu.Unlock()
	fixture.manager.runRecordLifecycleHooks(fixture.record)
	if !slices.Contains(fixture.stopped, acpAgentTestPlugin) {
		t.Fatalf("an error must disconnect the agents: %v", fixture.stopped)
	}
	// A plain stop of a running plugin keeps them.
	fixture.stopped = nil
	fixture.record.mu.Lock()
	fixture.record.state = pluginStateStopped
	fixture.record.mu.Unlock()
	fixture.manager.runRecordLifecycleHooks(fixture.record)
	if len(fixture.stopped) != 0 {
		t.Fatalf("a plain stop must not disconnect the agents: %v", fixture.stopped)
	}
	fixture.record.mu.Lock()
	fixture.record.enabled = false
	fixture.record.mu.Unlock()
	fixture.manager.runRecordLifecycleHooks(fixture.record)
	if !slices.Contains(fixture.stopped, acpAgentTestPlugin) {
		t.Fatalf("disable must disconnect the agents: %v", fixture.stopped)
	}
	fixture.stopped = nil
	fixture.manager.runRecordRemovalHooks(acpAgentTestPlugin, removalReasonUninstall)
	if !slices.Contains(fixture.stopped, acpAgentTestPlugin) {
		t.Fatalf("uninstall must disconnect the agents: %v", fixture.stopped)
	}
	if _, found, _ := pluginKVGetRaw(hostPluginID, processLaunchTrustKey(acpAgentTestPlugin, acpAgentPointKey)); found {
		t.Fatal("uninstall must drop the ACP agent confirmations")
	}
}

type acpAgentLifecyclePlugin struct {
	startupErr, shutdownErr error
}

func (plugin *acpAgentLifecyclePlugin) Meta() api.Meta {
	return api.Meta{ID: "test.acp-lifecycle", Name: "ACP lifecycle", Version: "1.0.0"}
}
func (plugin *acpAgentLifecyclePlugin) OnStartup(context.Context) error  { return plugin.startupErr }
func (plugin *acpAgentLifecyclePlugin) OnShutdown(context.Context) error { return plugin.shutdownErr }
func (plugin *acpAgentLifecyclePlugin) Invoke(context.Context, string, string) (string, error) {
	return `{}`, nil
}

func recordAcpAgentStops(t *testing.T) *[]string {
	t.Helper()
	stopped := &[]string{}
	previous := acpAgentDisconnectOwner
	acpAgentDisconnectOwner = func(pluginID string) { *stopped = append(*stopped, pluginID) }
	t.Cleanup(func() { acpAgentDisconnectOwner = previous })
	return stopped
}

func TestStopPluginAcpAgentsOnActivationFailure(t *testing.T) {
	stopped := recordAcpAgentStops(t)
	plugin := &acpAgentLifecyclePlugin{startupErr: errors.New("startup failed")}
	manager, record := testManagerWithPlugin(plugin, true)
	if err := manager.startRecord(context.Background(), record); err == nil {
		t.Fatal("the activation must fail")
	}
	if record.state != pluginStateError || !slices.Contains(*stopped, record.id) {
		t.Fatalf("a failed activation must disconnect the agents: state %s, stopped %v", record.state, *stopped)
	}
}

func TestStopPluginAcpAgentsOnFailedStop(t *testing.T) {
	stopped := recordAcpAgentStops(t)
	plugin := &acpAgentLifecyclePlugin{}
	manager, record := testManagerWithPlugin(plugin, true)
	if err := manager.startRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := manager.stopRecord(context.Background(), record, false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 0 {
		t.Fatalf("a clean stop must keep the agents: %v", *stopped)
	}
	if err := manager.startRecord(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	plugin.shutdownErr = errors.New("shutdown failed")
	if err := manager.stopRecord(context.Background(), record, false); err == nil {
		t.Fatal("the stop must fail")
	}
	if record.state != pluginStateError || !slices.Contains(*stopped, record.id) {
		t.Fatalf("a failed stop must disconnect the agents: state %s, stopped %v", record.state, *stopped)
	}
}

// Upgrade disables (and persists) the plugin before its agents are stopped, so
// no launch passes the gates in between.
func TestStopPluginAcpAgentsOnUpgradeAfterDisable(t *testing.T) {
	manager, id, source := upgradeFixture(t)
	record, _ := manager.record(id)
	record.enabled = true
	record.state = pluginStateStopped
	if err := writePluginEnabled(id, true); err != nil {
		t.Fatal(err)
	}
	var enabledAtStop []bool
	previous := acpAgentDisconnectOwner
	acpAgentDisconnectOwner = func(pluginID string) {
		if pluginID == id {
			record.mu.Lock()
			enabledAtStop = append(enabledAtStop, record.enabled)
			record.mu.Unlock()
		}
	}
	t.Cleanup(func() { acpAgentDisconnectOwner = previous })
	if _, err := manager.UpgradeLocal(context.Background(), id, source); err != nil {
		t.Fatal(err)
	}
	if len(enabledAtStop) == 0 || slices.Contains(enabledAtStop, true) {
		t.Fatalf("upgrade must stop the agents after disabling the plugin: %v", enabledAtStop)
	}
}
