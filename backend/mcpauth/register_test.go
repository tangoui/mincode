package mcpauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestRegisterSendsPublicClient(t *testing.T) {
	allowHTTP(t)
	var body map[string]any
	var raw string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("method=%s content-type=%s", r.Method, r.Header.Get("Content-Type"))
		}
		data, _ := io.ReadAll(r.Body)
		raw = string(data)
		_ = json.Unmarshal(data, &body)
		writeJSON(t, w, http.StatusCreated, map[string]any{"client_id": "dyn-123", "token_endpoint_auth_method": "none"})
	}))
	defer server.Close()

	redirect := "http://127.0.0.1:53111/callback"
	md := Metadata{RegistrationEndpoint: server.URL + "/register", Scopes: []string{"mcp:connect"}}
	client, err := Register(context.Background(), server.Client(), md, redirect)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"token_endpoint_auth_method":"none"`) {
		t.Fatalf("body must request a public client: %s", raw)
	}
	if !reflect.DeepEqual(body["redirect_uris"], []any{redirect}) || body["client_name"] != "MinCode" {
		t.Fatalf("body = %#v", body)
	}
	if !reflect.DeepEqual(body["grant_types"], []any{"authorization_code", "refresh_token"}) || !reflect.DeepEqual(body["response_types"], []any{"code"}) {
		t.Fatalf("grant/response types = %#v", body)
	}
	want := Client{ID: "dyn-123", Public: true, RedirectURI: redirect}
	if client != want {
		t.Fatalf("client = %#v, want %#v", client, want)
	}
}

func TestRegisterRefusedAndFailures(t *testing.T) {
	allowHTTP(t)
	status := http.StatusForbidden
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, status, map[string]any{"error": "invalid_client_metadata", "error_description": "secret-ish detail"})
	}))
	defer server.Close()
	md := Metadata{RegistrationEndpoint: server.URL + "/register"}
	_, err := Register(context.Background(), server.Client(), md, "http://127.0.0.1:1234/callback")
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REGISTER_REFUSED]") {
		t.Fatalf("403 must be refused, got %v", err)
	}
	status = http.StatusBadRequest
	_, err = Register(context.Background(), server.Client(), md, "http://127.0.0.1:1234/callback")
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REGISTER_FAILED]") || !strings.Contains(err.Error(), "invalid_client_metadata") || strings.Contains(err.Error(), "secret-ish") {
		t.Fatalf("400 must fail with the OAuth error code only, got %v", err)
	}
	if _, err := Register(context.Background(), server.Client(), Metadata{}, "http://127.0.0.1:1234/callback"); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_UNSUPPORTED]") {
		t.Fatalf("missing registration endpoint must be unsupported, got %v", err)
	}
	if _, err := Register(context.Background(), server.Client(), md, "https://attacker.example/callback"); err == nil {
		t.Fatal("non-loopback redirect must be refused")
	}
}

func TestRegisterDoesNotFollowRedirects(t *testing.T) {
	allowHTTP(t)
	hit := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		writeJSON(t, w, 201, map[string]any{"client_id": "x"})
	}))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/register", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := Register(context.Background(), server.Client(), Metadata{RegistrationEndpoint: server.URL}, "http://127.0.0.1:1234/callback")
	if err == nil || hit {
		t.Fatalf("redirected registration must fail without reaching the target (hit=%v err=%v)", hit, err)
	}
}
