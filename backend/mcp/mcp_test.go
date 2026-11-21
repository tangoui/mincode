package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type testTransport struct{}

func (testTransport) callTool(context.Context, string, json.RawMessage) (*McpCallResult, error) {
	return nil, nil
}
func (testTransport) readResource(string) (*McpResourceContent, error) { return nil, nil }
func (testTransport) getPrompt(string, json.RawMessage) (*McpPromptResult, error) {
	return nil, nil
}
func (testTransport) close() {}

func TestMCPReadConfigFileIsDisabled(t *testing.T) {
	_, err := mcpReadConfigFileHandler("", `{"path":"/tmp/mcp.json"}`)
	if err == nil || !strings.Contains(err.Error(), "直接读取已禁用") {
		t.Fatalf("expected disabled config read error, got %v", err)
	}
}

func TestMCPReadConfigFileRequiresPath(t *testing.T) {
	_, err := mcpReadConfigFileHandler("", `{"path":""}`)
	if err == nil || !strings.Contains(err.Error(), "路径不能为空") {
		t.Fatalf("expected empty path error, got %v", err)
	}
}

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
