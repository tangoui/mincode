package browser

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestInternalMCPConfigRequiresExactLoopbackBridgeURL(t *testing.T) {
	valid := "http://127.0.0.1:43123" + internalMCPBridgePath
	for _, rawURL := range []string{
		"http://127.0.0.1:43123@evil.example" + internalMCPBridgePath,
		"http://localhost:43123" + internalMCPBridgePath,
		"https://127.0.0.1:43123" + internalMCPBridgePath,
		"http://127.0.0.1:43123/other",
		valid + "?redirect=1",
	} {
		if err := validateInternalMCPBridgeURL(rawURL); err == nil {
			t.Fatalf("expected bridge URL to be rejected: %s", rawURL)
		}
	}
	if err := validateInternalMCPBridgeURL(valid); err != nil {
		t.Fatalf("expected generated loopback bridge URL to pass: %v", err)
	}

	for key, value := range map[string]string{
		internalMCPURLEnv:       valid,
		internalMCPTokenEnv:     "test-token",
		internalMCPServerIDEnv:  "agent-1",
		internalMCPSessionIDEnv: "session-1",
	} {
		previous, existed := os.LookupEnv(key)
		if err := os.Setenv(key, value); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(key, previous)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
	if _, err := internalMCPConfigFromEnvironment(); err != nil {
		t.Fatalf("expected complete generated environment to pass: %v", err)
	}
}

func TestInternalMCPStdioLifecycleAndToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(response).Encode(internalMCPBridgeResponse{Result: map[string]any{"url": "https://example.com"}})
	}))
	defer server.Close()

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"browser.get_url","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping","params":{}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	err := runInternalMCPStdio(strings.NewReader(input), &output, internalMCPStdioConfig{
		URL: server.URL, Token: "test-token", ServerID: "agent-1", SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("stdio server failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected four responses and no notification response, got %d: %s", len(lines), output.String())
	}
	var initialize map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &initialize); err != nil {
		t.Fatal(err)
	}
	result := initialize["result"].(map[string]any)
	if result["protocolVersion"] != internalMCPProtocol {
		t.Fatalf("unexpected protocol version: %v", result["protocolVersion"])
	}
	var toolCall map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &toolCall); err != nil {
		t.Fatal(err)
	}
	callResult := toolCall["result"].(map[string]any)
	if callResult["isError"] != false {
		t.Fatalf("expected successful tool call: %v", callResult)
	}
}

func TestMCPToolResultReturnsScreenshotsAsImages(t *testing.T) {
	for _, toolName := range []string{"browser.capture_view", "computer.capture_screen", "computer.open_app", "computer.open_url", "computer.execute"} {
		result := mcpToolResult(toolName, map[string]any{
			"data": "aGVsbG8=", "mimeType": "image/png", "width": 10, "height": 20, "tabId": "tab-1",
		})
		content := result["content"].([]map[string]any)
		if len(content) != 2 || content[1]["type"] != "image" || content[1]["data"] != "aGVsbG8=" {
			t.Fatalf("expected MCP image content for %s, got %#v", toolName, content)
		}
		if strings.Contains(content[0]["text"].(string), "aGVsbG8=") {
			t.Fatalf("base64 screenshot leaked into text metadata for %s", toolName)
		}
	}
}

func TestInternalMCPStdioSurfacesStructuredToolErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(response).Encode(internalMCPBridgeResponse{
			Error:   "the page is showing a dialog",
			Code:    errCodeDialogPending,
			Details: map[string]any{"dialog": map[string]any{"id": "d1", "kind": "confirm"}},
		})
	}))
	defer server.Close()

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser.click","arguments":{"snapshotId":"s","ref":"e1"}}}` + "\n"
	var output bytes.Buffer
	if err := runInternalMCPStdio(strings.NewReader(input), &output, internalMCPStdioConfig{
		URL: server.URL, Token: "test-token", ServerID: "agent-1",
	}); err != nil {
		t.Fatalf("stdio server failed: %v", err)
	}
	var response struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode %s: %v", output.String(), err)
	}
	if !response.Result.IsError || len(response.Result.Content) != 2 {
		t.Fatalf("expected an error result with text and structured JSON, got %s", output.String())
	}
	if response.Result.Content[0].Text != "[dialog_pending] the page is showing a dialog" {
		t.Fatalf("unexpected error text %q", response.Result.Content[0].Text)
	}
	var structured struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal([]byte(response.Result.Content[1].Text), &structured); err != nil {
		t.Fatalf("second content item must be JSON: %v", err)
	}
	if structured.Code != errCodeDialogPending || structured.Details["dialog"].(map[string]any)["id"] != "d1" {
		t.Fatalf("unexpected structured error %+v", structured)
	}
}

func TestInternalMCPStdioKeepsPlainToolErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(response).Encode(internalMCPBridgeResponse{Error: "boom"})
	}))
	defer server.Close()
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser.get_url","arguments":{}}}` + "\n"
	var output bytes.Buffer
	if err := runInternalMCPStdio(strings.NewReader(input), &output, internalMCPStdioConfig{URL: server.URL, Token: "t", ServerID: "agent-1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"content":[{"text":"boom","type":"text"}]`) {
		t.Fatalf("plain errors must stay a single text item, got %s", output.String())
	}
}
