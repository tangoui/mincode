package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain doubles as a minimal stdio MCP server when re-executed with
// MINCODE_MCP_TEST_SERVER=1; it reports its cwd and DEMO env as the tool name.
func TestMain(m *testing.M) {
	if os.Getenv("MINCODE_MCP_TEST_SERVER") == "1" {
		runTestMcpServer()
		return
	}
	os.Exit(m.Run())
}

func runTestMcpServer() {
	cwd, _ := os.Getwd()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var msg struct {
			ID     *uint64 `json:"id"`
			Method string  `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil || msg.ID == nil {
			continue
		}
		var result any = map[string]any{}
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": mcpProtocolVersion, "capabilities": map[string]any{}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": filepath.Base(cwd) + "|" + os.Getenv("DEMO") + "|" + os.Getenv("SECRET_TOKEN")}}}
		case "resources/list":
			result = map[string]any{"resources": []any{}}
		case "prompts/list":
			result = map[string]any{"prompts": []any{}}
		}
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": result})
		fmt.Println(string(payload))
	}
}

func testPluginSpec(t *testing.T, serverID, fingerprint, dir string) PluginStdioSpec {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return PluginStdioSpec{
		ServerID: serverID, OwnerPluginID: "acme.demo", Executable: executable,
		Env:         PluginProcessEnv(map[string]string{"MINCODE_MCP_TEST_SERVER": "1", "DEMO": "literal-${env:SECRET_TOKEN}"}),
		Dir:         dir,
		Fingerprint: fingerprint,
	}
}

func TestPluginServerIDsAreReserved(t *testing.T) {
	if got := PluginServerID("acme.demo", "tools"); got != "plugin:acme.demo/tools" || !IsPluginServerID(got) {
		t.Fatalf("PluginServerID = %q", got)
	}
	for _, payload := range []string{
		`{"serverId":"plugin:acme.demo/tools","command":"true"}`,
	} {
		if _, err := mcpConnectStdioHandler("", payload); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("mcp_connect_stdio err = %v", err)
		}
		if _, err := mcpConnectHandler("", strings.Replace(payload, "{", `{"transport":"stdio",`, 1)); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("mcp_connect err = %v", err)
		}
	}
	if _, err := mcpConnectHTTPHandler("", `{"serverId":"plugin:acme.demo/tools","url":"http://127.0.0.1:1"}`); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("mcp_connect_http err = %v", err)
	}
	if DisconnectPlugin("user-server") {
		t.Fatal("DisconnectPlugin must ignore user server ids")
	}
}

func TestPluginEnvIsLiteral(t *testing.T) {
	t.Setenv("SECRET_TOKEN", "s3cret")
	env := strings.Join(PluginProcessEnv(map[string]string{"DEMO": "${env:SECRET_TOKEN}"}), "\n")
	if strings.Contains(env, "s3cret") || !strings.Contains(env, "DEMO=${env:SECRET_TOKEN}") || !strings.Contains(env, "NPM_CONFIG_LOGLEVEL=error") {
		t.Fatalf("env = %s", env)
	}
}

func TestConnectPluginLifecycle(t *testing.T) {
	t.Setenv("SECRET_TOKEN", "s3cret")
	t.Cleanup(gateway.disconnectAll)
	id := PluginServerID("acme.demo", "tools")
	dir := t.TempDir()

	if _, _, err := ConnectPlugin(PluginStdioSpec{ServerID: id}); !errors.Is(err, errPluginSpec) {
		t.Fatalf("incomplete spec err = %v", err)
	}
	foreign := testPluginSpec(t, PluginServerID("other.plugin", "tools"), "fp1", dir)
	if _, _, err := ConnectPlugin(foreign); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("foreign id err = %v", err)
	}

	refused := testPluginSpec(t, id, "fp1", dir)
	refused.BeforeExec = func() error { return errors.New("files changed") }
	if _, _, err := ConnectPlugin(refused); err == nil || err.Error() != "files changed" {
		t.Fatalf("BeforeExec err = %v", err)
	}
	if PluginSessionLive(id, "fp1", dir) {
		t.Fatal("refused start must not leave a session")
	}

	valid := true
	spec := testPluginSpec(t, id, "fp1", dir)
	spec.Valid = func() bool { return valid }
	result, reused, err := ConnectPlugin(spec)
	if err != nil || reused {
		t.Fatalf("connect = %v, reused %v", err, reused)
	}
	if len(result.Tools) != 1 || result.Tools[0].Description != filepath.Base(dir)+"|literal-${env:SECRET_TOKEN}|" {
		t.Fatalf("tools = %+v", result.Tools)
	}
	if !PluginSessionLive(id, "fp1", dir) || PluginSessionLive(id, "fp2", dir) || PluginSessionLive(id, "fp1", t.TempDir()) {
		t.Fatal("PluginSessionLive must match fingerprint and dir")
	}
	if _, reused, err := ConnectPlugin(spec); err != nil || !reused {
		t.Fatalf("same launch must reuse the session: %v, %v", err, reused)
	}
	if _, err := gateway.session(id); err != nil {
		t.Fatal(err)
	}

	valid = false
	if _, err := gateway.session(id); err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("invalid session err = %v", err)
	}
	if PluginSessionLive(id, "fp1", dir) {
		t.Fatal("an invalid session must be closed on use")
	}

	valid = true
	if _, _, err := ConnectPlugin(spec); err != nil {
		t.Fatal(err)
	}
	changed := testPluginSpec(t, id, "fp2", dir)
	if _, reused, err := ConnectPlugin(changed); err != nil || reused {
		t.Fatalf("a changed launch must replace the session: %v, %v", err, reused)
	}
	if !PluginSessionLive(id, "fp2", dir) {
		t.Fatal("replacement session missing")
	}
	stopped := DisconnectPluginOwner("acme.demo")
	if len(stopped) != 1 || stopped[0] != id || PluginSessionLive(id, "fp2", dir) {
		t.Fatalf("DisconnectPluginOwner = %v", stopped)
	}
	if DisconnectPlugin(id) {
		t.Fatal("nothing left to stop")
	}
}
