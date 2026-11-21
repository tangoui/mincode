package acp

import (
	"testing"

	"mincode/app/backend/browser"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestInternalBrowserMCPRegistrationUsesPrivateStdioHelper(t *testing.T) {
	registration, err := newInternalBrowserMCPRegistration("agent-1", "session-1")
	if err != nil {
		t.Fatalf("create registration: %v", err)
	}
	t.Cleanup(func() { browser.RevokeInternalMCPLease(registration.lease.Token) })
	stdio := registration.server.Stdio
	if stdio == nil {
		t.Fatal("expected stdio MCP server")
	}
	if len(stdio.Args) != 1 || stdio.Args[0] != "--internal-browser-mcp" {
		t.Fatalf("unexpected helper args: %#v", stdio.Args)
	}
	for _, argument := range stdio.Args {
		if argument == registration.lease.Token {
			t.Fatal("bridge token must not be exposed in process arguments")
		}
	}
	environment := map[string]string{}
	for _, variable := range stdio.Env {
		environment[variable.Name] = variable.Value
	}
	if environment["MINCODE_INTERNAL_BROWSER_MCP_TOKEN"] != registration.lease.Token || environment["MINCODE_INTERNAL_BROWSER_MCP_URL"] != registration.lease.URL {
		t.Fatalf("missing private bridge environment: %#v", environment)
	}
}

func TestAppendInternalBrowserMCPPreservesExternalServers(t *testing.T) {
	external := acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{Name: "external", Command: "external", Args: []string{}, Env: []acpsdk.EnvVariable{}}}
	internal := internalBrowserMCPRegistration{server: acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{Name: internalBrowserMCPName}}}
	result := appendInternalBrowserMCP([]acpsdk.McpServer{external}, internal)
	if len(result) != 2 || result[0].Stdio == nil || result[0].Stdio.Name != "external" || result[1].Stdio == nil || result[1].Stdio.Name != internalBrowserMCPName {
		t.Fatalf("unexpected MCP server order: %#v", result)
	}
}
