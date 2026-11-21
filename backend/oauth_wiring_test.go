package backend

import (
	"strings"
	"testing"

	"mincode/app/backend/core"
	"mincode/app/backend/storage"
)

// An oauth connector that was never signed in must fail with
// [MCP_AUTH_REQUIRED] from mcpauth, not with the gateway's "sign-in
// unavailable" error that means the token source was never wired in.
func TestConnectorOAuthTokenSourceIsWired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_ = storage.Close()
	t.Cleanup(func() { _ = storage.Close() })

	_, err := core.Dispatch("mcp_connect", `{"serverId":"wiring-check","transport":"http","url":"https://mcp.example.invalid/mcp","auth":"oauth"}`)
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") {
		t.Fatalf("mcp_connect with auth oauth = %v, want [MCP_AUTH_REQUIRED]", err)
	}
}
