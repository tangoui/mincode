package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMcpConnectRejectsLegacyPluginServerIDs(t *testing.T) {
	_, err := mcpConnectHandler("", `{"serverId":"plugin:acme.demo/tools","transport":"stdio"}`)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved MCP server ID, got %v", err)
	}
}

type testTransport struct{}

func (testTransport) callTool(context.Context, string, json.RawMessage) (*McpCallResult, error) {
	return nil, nil
}
func (testTransport) readResource(string) (*McpResourceContent, error) { return nil, nil }
func (testTransport) getPrompt(string, json.RawMessage) (*McpPromptResult, error) {
	return nil, nil
}
func (testTransport) close() {}

func TestMcpGatewayStaleAttemptCannotClearCurrentConnecting(t *testing.T) {
	gateway := newMcpGateway()
	firstRevision, firstCtx, _ := gateway.beginConnect("test")
	secondRevision, secondCtx, _ := gateway.beginConnect("test")

	select {
	case <-firstCtx.Done():
	default:
		t.Fatal("expected newer connection attempt to cancel the previous attempt")
	}
	gateway.failConnect("test", firstRevision)
	if len(gateway.connecting) != 1 {
		t.Fatal("stale failure cleared the current connection attempt")
	}
	select {
	case <-secondCtx.Done():
		t.Fatal("stale failure cancelled the current connection attempt")
	default:
	}

	if err := gateway.finishConnect("test", firstRevision, &mcpConnection{transport: testTransport{}}); err == nil {
		t.Fatal("expected stale connection attempt to be rejected")
	}
	if len(gateway.connecting) != 1 {
		t.Fatal("stale completion cleared the current connection attempt")
	}

	gateway.failConnect("test", secondRevision)
	if len(gateway.connecting) != 0 {
		t.Fatal("current failed connection attempt was not cleared")
	}
	select {
	case <-secondCtx.Done():
	default:
		t.Fatal("current failed connection attempt was not cancelled")
	}
}
