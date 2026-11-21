package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"mincode/app/backend/core"
)

type pluginLaunchCall struct {
	serverID, command, cwd string
	args                   []string
	env                    map[string]string
}

type pluginLaunchFixture struct {
	mu       sync.Mutex
	launches []pluginLaunchCall
	closed   []string
	resolved []string
}

// newPluginLaunchFixture swaps in a fresh gateway, a fake agent process and
// resolver for the test.
func newPluginLaunchFixture(t *testing.T, resolve PluginAgentResolver) *pluginLaunchFixture {
	t.Helper()
	fixture := &pluginLaunchFixture{}
	previousGateway, previousLaunch := gateway, launchAgentProcess
	pluginAgentResolverMu.Lock()
	previousResolver := pluginAgentResolver
	pluginAgentResolver = func(pluginID, agentID, projectRoot string) (string, []string, map[string]string, func() error, error) {
		fixture.mu.Lock()
		fixture.resolved = append(fixture.resolved, pluginID+"/"+agentID+"@"+projectRoot)
		fixture.mu.Unlock()
		return resolve(pluginID, agentID, projectRoot)
	}
	pluginAgentResolverMu.Unlock()
	gateway = newAcpGateway()
	launchAgentProcess = func(_ context.Context, serverID, command string, args []string, env map[string]string, cwd string, _ func() error) (*acpConnection, error) {
		fixture.mu.Lock()
		fixture.launches = append(fixture.launches, pluginLaunchCall{serverID: serverID, command: command, cwd: cwd, args: args, env: env})
		fixture.mu.Unlock()
		return &acpConnection{serverID: serverID, command: command, cancel: func() {
			fixture.mu.Lock()
			fixture.closed = append(fixture.closed, serverID)
			fixture.mu.Unlock()
		}}, nil
	}
	t.Cleanup(func() {
		gateway.disconnectAll()
		gateway, launchAgentProcess = previousGateway, previousLaunch
		pluginAgentResolverMu.Lock()
		pluginAgentResolver = previousResolver
		pluginAgentResolverMu.Unlock()
	})
	return fixture
}

func launchPluginArgs(serverID, pluginID, agentID, cwd string) string {
	raw, _ := json.Marshal(map[string]string{"serverId": serverID, "pluginId": pluginID, "agentId": agentID, "cwd": cwd})
	return string(raw)
}

func TestAcpLaunchPluginRefusesUntrusted(t *testing.T) {
	fixture := newPluginLaunchFixture(t, func(string, string, string) (string, []string, map[string]string, func() error, error) {
		return "", nil, nil, nil, errors.New(`untrusted: ACP agent "demo" has not been confirmed`)
	})
	_, err := acpLaunchPluginHandler("", launchPluginArgs("plugin:acme.demo/demo", "acme.demo", "demo", t.TempDir()))
	if err == nil || !strings.HasPrefix(err.Error(), "untrusted:") {
		t.Fatalf("an unconfirmed agent must be refused with untrusted, got %v", err)
	}
	if len(fixture.launches) != 0 || len(gateway.listStatus()) != 0 || len(gateway.owners) != 0 {
		t.Fatalf("nothing may start: %+v %v %v", fixture.launches, gateway.listStatus(), gateway.owners)
	}
}

func TestAcpLaunchPluginValidatesRequest(t *testing.T) {
	fixture := newPluginLaunchFixture(t, func(string, string, string) (string, []string, map[string]string, func() error, error) {
		return "/bin/agent", nil, nil, nil, nil
	})
	project := t.TempDir()
	for name, args := range map[string]string{
		"no server": launchPluginArgs("", "acme.demo", "demo", project),
		"no plugin": launchPluginArgs("s", "", "demo", project),
		"no agent":  launchPluginArgs("s", "acme.demo", "", project),
		"no cwd":    launchPluginArgs("s", "acme.demo", "demo", " "),
		"bad cwd":   launchPluginArgs("s", "acme.demo", "demo", filepath.Join(project, "missing")),
		"command":   `{"serverId":"s","pluginId":"acme.demo","agentId":"demo","cwd":"` + project + `","command":"/bin/sh"}`,
	} {
		if name == "command" {
			// A command in the request is ignored: the host resolves it.
			if _, err := acpLaunchPluginHandler("", args); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := fixture.launches[len(fixture.launches)-1].command; got != "/bin/agent" {
				t.Fatalf("the request command must be ignored, launched %q", got)
			}
			continue
		}
		if _, err := acpLaunchPluginHandler("", args); err == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
	pluginAgentResolverMu.Lock()
	pluginAgentResolver = nil
	pluginAgentResolverMu.Unlock()
	if _, err := acpLaunchPluginHandler("", launchPluginArgs("s", "acme.demo", "demo", project)); err == nil || !strings.HasPrefix(err.Error(), "unavailable:") {
		t.Fatalf("no resolver must be unavailable, got %v", err)
	}
}

func TestAcpLaunchPluginDisconnectsByOwner(t *testing.T) {
	fixture := newPluginLaunchFixture(t, func(pluginID, agentID, projectRoot string) (string, []string, map[string]string, func() error, error) {
		return "/plugins/" + pluginID + "/agent", []string{"--root=" + projectRoot}, map[string]string{"AGENT": agentID}, nil, nil
	})
	project := t.TempDir()
	resolvedProject, _ := resolveCwd(project)
	if _, err := acpLaunchPluginHandler("", launchPluginArgs("plugin:acme.one/a", "acme.one", "a", project)); err != nil {
		t.Fatal(err)
	}
	if _, err := acpLaunchPluginHandler("", launchPluginArgs("plugin:acme.one/b", "acme.one", "b", project)); err != nil {
		t.Fatal(err)
	}
	if _, err := acpLaunchPluginHandler("", launchPluginArgs("plugin:acme.two/a", "acme.two", "a", project)); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.launch(context.Background(), "custom:x", "/bin/custom", nil, nil, resolvedProject); err != nil {
		t.Fatal(err)
	}
	first := fixture.launches[0]
	if first.command != "/plugins/acme.one/agent" || first.cwd != resolvedProject || !slices.Equal(first.args, []string{"--root=" + resolvedProject}) || first.env["AGENT"] != "a" {
		t.Fatalf("launch = %+v", first)
	}
	if fixture.resolved[0] != "acme.one/a@"+resolvedProject {
		t.Fatalf("the resolver must get the validated cwd: %v", fixture.resolved)
	}
	DisconnectPluginAgents("acme.one")
	slices.Sort(fixture.closed)
	if !slices.Equal(fixture.closed, []string{"plugin:acme.one/a", "plugin:acme.one/b"}) {
		t.Fatalf("closed = %v", fixture.closed)
	}
	var live []string
	for _, status := range gateway.listStatus() {
		live = append(live, status.ID)
	}
	slices.Sort(live)
	if !slices.Equal(live, []string{"custom:x", "plugin:acme.two/a"}) {
		t.Fatalf("live = %v", live)
	}
	// A plain launch under a plugin's server id drops the ownership.
	if _, err := gateway.launch(context.Background(), "plugin:acme.two/a", "/bin/custom", nil, nil, resolvedProject); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.closed = nil
	fixture.mu.Unlock()
	DisconnectPluginAgents("acme.two")
	if len(fixture.closed) != 0 || len(gateway.listStatus()) != 2 {
		t.Fatalf("a connection no longer owned by the plugin must stay: closed = %v, live = %v", fixture.closed, gateway.listStatus())
	}
}

func TestStopPluginAcpAgentsCancelsConnecting(t *testing.T) {
	newPluginLaunchFixture(t, func(string, string, string) (string, []string, map[string]string, func() error, error) {
		return "/bin/agent", nil, nil, nil, nil
	})
	started := make(chan struct{})
	release := make(chan struct{})
	launchAgentProcess = func(ctx context.Context, serverID, command string, _ []string, _ map[string]string, _ string, _ func() error) (*acpConnection, error) {
		close(started)
		<-release
		return &acpConnection{serverID: serverID, command: command}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := acpLaunchPluginHandler("", launchPluginArgs("plugin:acme.one/a", "acme.one", "a", t.TempDir()))
		done <- err
	}()
	<-started
	DisconnectPluginAgents("acme.one")
	close(release)
	if err := <-done; err == nil {
		t.Fatal("a connection still starting when its plugin stops must be cancelled")
	}
	if len(gateway.listStatus()) != 0 || len(gateway.owners) != 0 {
		t.Fatalf("live = %v, owners = %v", gateway.listStatus(), gateway.owners)
	}
}

// The plugin's program may be swapped between the resolve and the exec: the
// resolver's pre-exec check runs right before cmd.Start() and refuses it.
func TestAcpLaunchPluginRefusesProgramSwappedBeforeExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	marker := filepath.Join(t.TempDir(), "started")
	checked := false
	newPluginLaunchFixture(t, func(string, string, string) (string, []string, map[string]string, func() error, error) {
		return "/bin/sh", []string{"-c", "touch " + marker}, nil, func() error {
			checked = true
			return errors.New(`changed: plugin file "bin/agent" changed before it could start; review the launch again`)
		}, nil
	})
	launchAgentProcess = launchAgent
	_, err := acpLaunchPluginHandler("", launchPluginArgs("plugin:acme.one/a", "acme.one", "a", t.TempDir()))
	if err == nil || !strings.HasPrefix(err.Error(), "untrusted:") || !checked {
		t.Fatalf("a swapped program must be refused with untrusted, got %v (checked %v)", err, checked)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("no process may start when the pre-exec check fails")
	}
	if len(gateway.listStatus()) != 0 || len(gateway.owners) != 0 {
		t.Fatalf("live = %v, owners = %v", gateway.listStatus(), gateway.owners)
	}
}

// A plugin stop while its launch is being resolved cancels the attempt before
// any process is spawned.
func TestAcpLaunchPluginCancelledDuringResolveNeverSpawns(t *testing.T) {
	fixture := newPluginLaunchFixture(t, func(pluginID, _, _ string) (string, []string, map[string]string, func() error, error) {
		DisconnectPluginAgents(pluginID)
		return "/bin/agent", nil, nil, nil, nil
	})
	if _, err := acpLaunchPluginHandler("", launchPluginArgs("plugin:acme.one/a", "acme.one", "a", t.TempDir())); err == nil {
		t.Fatal("a launch cancelled during the resolve must fail")
	}
	if len(fixture.launches) != 0 || len(gateway.listStatus()) != 0 || len(gateway.owners) != 0 {
		t.Fatalf("launches = %v, live = %v, owners = %v", fixture.launches, gateway.listStatus(), gateway.owners)
	}
}

// The frontend learns which conversations lost their agent from the
// acp:disconnected event: only the plugin's server ids, live or connecting.
func TestDisconnectPluginAgentsEmitsServerIds(t *testing.T) {
	newPluginLaunchFixture(t, func(string, string, string) (string, []string, map[string]string, func() error, error) {
		return "/bin/agent", nil, nil, nil, nil
	})
	var mu sync.Mutex
	var events []AcpDisconnectedPayload
	core.SetEmitter(func(name string, data any) {
		if payload, ok := data.(AcpDisconnectedPayload); ok && name == AcpDisconnectedEvent {
			mu.Lock()
			events = append(events, payload)
			mu.Unlock()
		}
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	project := t.TempDir()
	for _, launch := range [][3]string{{"conv-b", "acme.one", "b"}, {"conv-a", "acme.one", "a"}, {"conv-c", "acme.two", "a"}} {
		if _, err := acpLaunchPluginHandler("", launchPluginArgs(launch[0], launch[1], launch[2], project)); err != nil {
			t.Fatal(err)
		}
	}
	resolvedProject, _ := resolveCwd(project)
	if _, err := gateway.launch(context.Background(), "custom:x", "/bin/custom", nil, nil, resolvedProject); err != nil {
		t.Fatal(err)
	}
	DisconnectPluginAgents("acme.one")
	// A plugin with nothing running emits nothing.
	DisconnectPluginAgents("acme.none")
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].PluginID != "acme.one" || !slices.Equal(events[0].ServerIDs, []string{"conv-a", "conv-b"}) {
		t.Fatalf("events = %+v", events)
	}
	encoded, _ := json.Marshal(events[0])
	if string(encoded) != `{"serverIds":["conv-a","conv-b"],"pluginId":"acme.one"}` {
		t.Fatalf("payload = %s", encoded)
	}
}
