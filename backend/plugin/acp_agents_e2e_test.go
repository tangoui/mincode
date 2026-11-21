package plugin

// End-to-end check of contributes.acpAgents through the same command handlers
// the desktop bridge dispatches: a fixture plugin (testdata/acp-agent-plugin) is
// installed and enabled, its agent is prepared and confirmed, acp_launch_plugin
// starts a real (minimal) ACP agent process from the plugin package, and
// disabling the plugin disconnects it.

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"mincode/app/backend/acp"
	"mincode/app/backend/core"
	"mincode/app/backend/license"
)

const (
	acpAgentE2EPlugin = "test.acp-agent-e2e"
	acpAgentE2EServer = "plugin:" + acpAgentE2EPlugin + "/demo"
)

// fakeAcpAgentRecord is what testdata/fake-acp-agent writes to its working
// directory after it starts (no environment values except the one it echoes).
type fakeAcpAgentRecord struct {
	PID       int      `json:"pid"`
	Cwd       string   `json:"cwd"`
	Args      []string `json:"args"`
	Project   string   `json:"project"`
	Inherited bool     `json:"inherited"`
}

// buildAcpAgentE2EPackage copies the fixture plugin and builds the fake ACP
// agent into its bin directory.
func buildAcpAgentE2EPackage(t *testing.T) string {
	t.Helper()
	pkg := filepath.Join(t.TempDir(), "acp-agent-plugin")
	if err := os.CopyFS(pkg, os.DirFS(filepath.Join("testdata", "acp-agent-plugin"))); err != nil {
		t.Fatalf("copy fixture plugin: %v", err)
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go tool is not on PATH")
	}
	build := exec.Command(goTool, "build", "-o", filepath.Join(pkg, "bin", "agent"), "./testdata/fake-acp-agent")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake ACP agent: %v\n%s", err, output)
	}
	return pkg
}

func acpAgentE2EDispatch(t *testing.T, command string, args any, out any) error {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := core.Dispatch(command, string(encoded))
	if err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal([]byte(result), out); err != nil {
			t.Fatalf("%s: decode %s: %v", command, result, err)
		}
	}
	return nil
}

func acpAgentE2EConnected(t *testing.T) bool {
	t.Helper()
	var status []struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := acpAgentE2EDispatch(t, "acp_list_status", map[string]any{}, &status); err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(status, func(entry struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}) bool {
		return entry.ID == acpAgentE2EServer && entry.State == "connected"
	})
}

func TestPluginAcpAgentEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture plugin names a POSIX executable (bin/agent)")
	}
	pkg := buildAcpAgentE2EPackage(t)
	useTemporaryPluginData(t)
	previousQuota := enabledPluginQuota
	enabledPluginQuota = func() (license.QuotaPolicy, string, bool) { return license.QuotaPolicy{}, "", false }
	// Initialize the singleton before temporarily routing host() to this manager.
	_ = host()
	previousManager := managerInst
	managerInst = newPluginManager()
	// Created before the cleanup below is registered, so the agent (which runs
	// in it) is disconnected before the directory is removed.
	project := t.TempDir()
	var disconnectedMu sync.Mutex
	var disconnected []acp.AcpDisconnectedPayload
	core.SetEmitter(func(name string, data any) {
		if payload, ok := data.(acp.AcpDisconnectedPayload); ok && name == acp.AcpDisconnectedEvent {
			disconnectedMu.Lock()
			disconnected = append(disconnected, payload)
			disconnectedMu.Unlock()
		}
	})
	t.Cleanup(func() {
		core.SetEmitter(nil)
		_ = acpAgentE2EDispatch(t, "acp_disconnect_all", map[string]any{}, nil)
		managerInst = previousManager
		enabledPluginQuota = previousQuota
	})
	// The agent inherits the user's environment; it reports only whether this
	// marker reached it.
	t.Setenv("MINCODE_E2E_ACP_INHERITED", "1")

	// Install and enable; the agent is listed for the frontend.
	if _, err := managerInst.InstallLocal(pkg); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := managerInst.SetEnabled(context.Background(), acpAgentE2EPlugin, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	snapshot := managerInst.Contributions([]string{acpAgentPointKey})
	if len(snapshot.Plugins) != 1 || len(snapshot.Plugins[0].Points[acpAgentPointKey].Entries) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	canonicalProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	launchArgs := map[string]any{"serverId": acpAgentE2EServer, "pluginId": acpAgentE2EPlugin, "agentId": "demo", "cwd": project}
	recordPath := filepath.Join(project, "fake-acp-agent.json")

	// Not confirmed yet: refused before any process starts.
	if err := acpAgentE2EDispatch(t, "acp_launch_plugin", launchArgs, nil); err == nil || !strings.HasPrefix(err.Error(), "untrusted:") {
		t.Fatalf("an unconfirmed agent must be refused with untrusted, got %v", err)
	}
	if _, err := os.Stat(recordPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no agent process may start before the confirmation: %v", err)
	}

	// Prepare (what the confirmation dialog shows), then trust.
	request := map[string]any{"pluginId": acpAgentE2EPlugin, "agentId": "demo", "projectRoot": project}
	var prepared AcpAgentLaunch
	if err := acpAgentE2EDispatch(t, "plugin_host_acp_agent_prepare", request, &prepared); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Trusted || prepared.Cwd != "${projectRoot}" || !prepared.PluginExecutable || prepared.ExecutableSHA256 == "" ||
		!slices.Contains(prepared.Argv, "--project=${projectRoot}") {
		t.Fatalf("prepare = %+v", prepared)
	}
	request["fingerprint"] = prepared.Fingerprint
	var trusted AcpAgentLaunch
	if err := acpAgentE2EDispatch(t, "plugin_host_acp_agent_trust", request, &trusted); err != nil || !trusted.Trusted {
		t.Fatalf("trust = %+v, %v", trusted, err)
	}

	// Launch by reference: the host resolves the command and the real agent
	// completes the ACP initialize handshake.
	var initResult struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := acpAgentE2EDispatch(t, "acp_launch_plugin", launchArgs, &initResult); err != nil {
		t.Fatalf("acp_launch_plugin: %v", err)
	}
	if initResult.ProtocolVersion != 1 || !acpAgentE2EConnected(t) {
		t.Fatalf("launch = %+v, connected %v", initResult, acpAgentE2EConnected(t))
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("the agent did not record its start: %v", err)
	}
	var started fakeAcpAgentRecord
	if err := json.Unmarshal(raw, &started); err != nil {
		t.Fatal(err)
	}
	if started.PID <= 0 || started.Cwd != canonicalProject || started.Project != canonicalProject || !started.Inherited ||
		!slices.Equal(started.Args, []string{"--project=" + canonicalProject}) {
		t.Fatalf("agent started with %+v (project %s)", started, canonicalProject)
	}

	// Disabling the plugin disconnects the agent and ends its process.
	if err := managerInst.SetEnabled(context.Background(), acpAgentE2EPlugin, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if acpAgentE2EConnected(t) {
		t.Fatal("disabling the plugin must disconnect its agent")
	}
	// The frontend is told which conversation lost its agent.
	disconnectedMu.Lock()
	events := slices.Clone(disconnected)
	disconnectedMu.Unlock()
	if len(events) != 1 || events[0].PluginID != acpAgentE2EPlugin || !slices.Equal(events[0].ServerIDs, []string{acpAgentE2EServer}) {
		t.Fatalf("acp:disconnected events = %+v", events)
	}
	if process, err := os.FindProcess(started.PID); err == nil {
		if signalErr := process.Signal(syscall.Signal(0)); signalErr == nil {
			t.Fatalf("the agent process %d must exit when its plugin is disabled", started.PID)
		}
	}
	// A disabled plugin's agent cannot start again.
	if err := acpAgentE2EDispatch(t, "acp_launch_plugin", launchArgs, nil); err == nil || !strings.HasPrefix(err.Error(), "unavailable:") {
		t.Fatalf("a disabled plugin's agent must be unavailable, got %v", err)
	}
}
