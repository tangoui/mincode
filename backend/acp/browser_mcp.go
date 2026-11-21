package acp

import (
	"fmt"
	"os"

	"mincode/app/backend/browser"

	acpsdk "github.com/coder/acp-go-sdk"
)

const internalBrowserMCPName = "MinCode Workbench"

type internalBrowserMCPRegistration struct {
	lease  browser.InternalMCPLease
	server acpsdk.McpServer
}

func newInternalBrowserMCPRegistration(serverID, sessionID string) (internalBrowserMCPRegistration, error) {
	executable, err := os.Executable()
	if err != nil {
		return internalBrowserMCPRegistration{}, fmt.Errorf("resolve MinCode executable for workbench MCP: %w", err)
	}
	lease, err := browser.NewInternalMCPLease(serverID, sessionID)
	if err != nil {
		return internalBrowserMCPRegistration{}, err
	}
	return internalBrowserMCPRegistration{
		lease: lease,
		server: acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
			Name:    internalBrowserMCPName,
			Command: executable,
			Args:    []string{"--internal-browser-mcp"},
			Env: []acpsdk.EnvVariable{
				{Name: "MINCODE_INTERNAL_BROWSER_MCP_URL", Value: lease.URL},
				{Name: "MINCODE_INTERNAL_BROWSER_MCP_TOKEN", Value: lease.Token},
				{Name: "MINCODE_INTERNAL_BROWSER_MCP_SERVER_ID", Value: lease.ServerID},
				{Name: "MINCODE_INTERNAL_BROWSER_MCP_SESSION_ID", Value: lease.SessionID},
			},
			Meta: map[string]any{"mincode.internal": true},
		}},
	}, nil
}

func appendInternalBrowserMCP(servers []acpsdk.McpServer, registration internalBrowserMCPRegistration) []acpsdk.McpServer {
	result := make([]acpsdk.McpServer, 0, len(servers)+1)
	result = append(result, servers...)
	result = append(result, registration.server)
	return result
}
