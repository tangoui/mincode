package browser

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInternalMCPBridgeRequiresLoopbackBearerAndAllowedTool(t *testing.T) {
	called := false
	bridge := &internalMCPBridge{
		leases: map[string]*internalMCPLeaseState{
			"secret-token": {ServerID: "agent-1", SessionID: "session-1"},
		},
		execute: func(name string, arguments map[string]any) (any, error) {
			called = true
			return map[string]any{"name": name}, nil
		},
	}
	requestBody, _ := json.Marshal(internalMCPBridgeRequest{ToolName: "browser.get_url", Arguments: map[string]any{}})

	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+internalMCPBridgePath, bytes.NewReader(requestBody))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Bearer wrong-token")
	response := httptest.NewRecorder()
	bridge.handleTool(response, request)
	if response.Code != http.StatusUnauthorized || called {
		t.Fatalf("expected unauthorized without execution, status=%d called=%v", response.Code, called)
	}

	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+internalMCPBridgePath, bytes.NewReader(requestBody))
	request.RemoteAddr = "10.0.0.2:54321"
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	bridge.handleTool(response, request)
	if response.Code != http.StatusUnauthorized || called {
		t.Fatalf("expected non-loopback rejection, status=%d called=%v", response.Code, called)
	}

	blockedBody, _ := json.Marshal(internalMCPBridgeRequest{ToolName: "browser.evaluate", Arguments: map[string]any{"unsafeConfirmed": true}})
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+internalMCPBridgePath, bytes.NewReader(blockedBody))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	bridge.handleTool(response, request)
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("expected unsafe tool rejection, status=%d called=%v", response.Code, called)
	}

	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+internalMCPBridgePath, bytes.NewReader(requestBody))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	bridge.handleTool(response, request)
	if response.Code != http.StatusOK || !called {
		t.Fatalf("expected authorized tool execution, status=%d called=%v", response.Code, called)
	}
}

func TestBindInternalMCPLeaseChecksOwner(t *testing.T) {
	bridge := browserMCPBridge
	bridge.mu.Lock()
	original := bridge.leases
	bridge.leases = map[string]*internalMCPLeaseState{"token": {ServerID: "owner"}}
	bridge.mu.Unlock()
	t.Cleanup(func() {
		bridge.mu.Lock()
		bridge.leases = original
		bridge.mu.Unlock()
	})
	if err := BindInternalMCPLease("token", "other", "session"); err == nil {
		t.Fatal("expected owner mismatch rejection")
	}
	if err := BindInternalMCPLease("token", "owner", "session"); err != nil {
		t.Fatalf("expected owner to bind lease: %v", err)
	}
}

func TestDispatchInternalMCPToolRoutesByPrefix(t *testing.T) {
	var browserCalled, computerCalled bool
	browserExecutor := func(name string, arguments map[string]any) (any, error) {
		browserCalled = true
		return name, nil
	}
	computerExecutor := func(name string, arguments map[string]any) (any, error) {
		computerCalled = true
		return name, nil
	}

	if _, err := dispatchInternalMCPTool("browser.get_url", nil, browserExecutor, computerExecutor); err != nil || !browserCalled || computerCalled {
		t.Fatalf("expected browser dispatch, err=%v browser=%v computer=%v", err, browserCalled, computerCalled)
	}
	browserCalled, computerCalled = false, false
	if _, err := dispatchInternalMCPTool("computer.get_status", nil, browserExecutor, computerExecutor); err != nil || browserCalled || !computerCalled {
		t.Fatalf("expected computer dispatch, err=%v browser=%v computer=%v", err, browserCalled, computerCalled)
	}
	if _, err := dispatchInternalMCPTool("shell.execute", nil, browserExecutor, computerExecutor); err == nil {
		t.Fatal("expected unsupported prefix rejection")
	}
}

func TestInternalMCPBridgeCarriesStructuredToolErrors(t *testing.T) {
	bridge := &internalMCPBridge{
		leases: map[string]*internalMCPLeaseState{"secret-token": {ServerID: "agent-1", SessionID: "session-1"}},
		execute: func(string, map[string]any) (any, error) {
			return nil, newToolError(errCodeDialogPending, "the page is showing a dialog", map[string]any{
				"dialog": pendingDialog{ID: "d1", TabID: "a", Kind: "confirm", Message: "Sure?"},
			})
		},
	}
	requestBody, _ := json.Marshal(internalMCPBridgeRequest{ToolName: "browser.click", Arguments: map[string]any{}})
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+internalMCPBridgePath, bytes.NewReader(requestBody))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	bridge.handleTool(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", response.Code)
	}
	var payload internalMCPBridgeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != errCodeDialogPending || payload.Error != "the page is showing a dialog" {
		t.Fatalf("expected structured code and message, got %+v", payload)
	}
	dialog, _ := payload.Details["dialog"].(map[string]any)
	if dialog["id"] != "d1" || dialog["kind"] != "confirm" {
		t.Fatalf("expected dialog details, got %#v", payload.Details)
	}

	// Plain errors keep the old shape.
	bridge.execute = func(string, map[string]any) (any, error) { return nil, errors.New("boom") }
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+internalMCPBridgePath, bytes.NewReader(requestBody))
	request.RemoteAddr = "127.0.0.1:54321"
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	bridge.handleTool(response, request)
	if body := response.Body.String(); strings.Contains(body, `"code"`) || !strings.Contains(body, `"error":"boom"`) {
		t.Fatalf("plain errors must not gain a code, got %s", body)
	}
}
