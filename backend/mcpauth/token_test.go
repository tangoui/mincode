package mcpauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func fixedNow(t *testing.T, at time.Time) {
	t.Helper()
	previous := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = previous })
}

func TestExchangeAndRefresh(t *testing.T) {
	allowHTTP(t)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fixedNow(t, at)
	var forms []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			t.Errorf("headers accept=%q content-type=%q", r.Header.Get("Accept"), r.Header.Get("Content-Type"))
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		forms = append(forms, r.PostForm)
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			writeJSON(t, w, 200, map[string]any{"access_token": "at-1", "token_type": "Bearer", "expires_in": 3600, "refresh_token": "rt-1", "scope": "mcp:connect"})
		case "refresh_token":
			writeJSON(t, w, 200, map[string]any{"access_token": "at-2", "token_type": "bearer", "expires_in": "120"})
		}
	}))
	defer server.Close()
	md := Metadata{TokenEndpoint: server.URL + "/token", Resource: "https://mcp.example.com/mcp"}
	client := Client{ID: "dyn-123", Public: true, RedirectURI: "http://127.0.0.1:5000/callback"}

	tokens, err := ExchangeCode(context.Background(), server.Client(), md, client, "the-code", "the-verifier", client.RedirectURI)
	if err != nil {
		t.Fatal(err)
	}
	want := Tokens{AccessToken: "at-1", RefreshToken: "rt-1", ExpiresAt: at.Add(time.Hour), Scope: "mcp:connect"}
	if tokens != want {
		t.Fatalf("tokens = %#v, want %#v", tokens, want)
	}
	form := forms[0]
	for key, value := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code", "code_verifier": "the-verifier",
		"redirect_uri": client.RedirectURI, "client_id": "dyn-123", "resource": md.Resource,
	} {
		if form.Get(key) != value {
			t.Fatalf("form %s = %q, want %q (form %v)", key, form.Get(key), value, form)
		}
	}

	refreshed, err := Refresh(context.Background(), server.Client(), md, client, "rt-1")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "at-2" || refreshed.RefreshToken != "rt-1" || !refreshed.ExpiresAt.Equal(at.Add(2*time.Minute)) {
		t.Fatalf("refreshed = %#v; old refresh token must be kept", refreshed)
	}
	form = forms[1]
	if form.Get("refresh_token") != "rt-1" || form.Get("resource") != md.Resource || form.Get("client_id") != "dyn-123" {
		t.Fatalf("refresh form = %v", form)
	}
}

func TestTokenErrorsNeverLeakSecrets(t *testing.T) {
	allowHTTP(t)
	status := http.StatusBadRequest
	body := map[string]any{"error": "invalid_grant", "error_description": "code the-code expired"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, status, body)
	}))
	defer server.Close()
	md := Metadata{TokenEndpoint: server.URL + "/token"}
	client := Client{ID: "cid", Public: true}

	_, err := ExchangeCode(context.Background(), server.Client(), md, client, "the-code", "the-verifier", "http://127.0.0.1:1/callback")
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_TOKEN_FAILED]") || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("exchange error = %v", err)
	}
	for _, secret := range []string{"the-code", "the-verifier"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks %q: %v", secret, err)
		}
	}
	_, err = Refresh(context.Background(), server.Client(), md, client, "rt-secret")
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REFRESH_REJECTED]") || strings.Contains(err.Error(), "rt-secret") {
		t.Fatalf("rejected refresh error = %v", err)
	}
	// GitHub answers 200 with an error object.
	status = http.StatusOK
	body = map[string]any{"error": "bad_verification_code", "error_description": "The code passed is incorrect or expired."}
	_, err = ExchangeCode(context.Background(), server.Client(), md, client, "the-code", "v", "http://127.0.0.1:1/callback")
	if err == nil || !strings.Contains(err.Error(), "bad_verification_code") {
		t.Fatalf("200 error object must fail, got %v", err)
	}
	status = http.StatusOK
	body = map[string]any{"access_token": "at", "token_type": "mac"}
	if _, err = ExchangeCode(context.Background(), server.Client(), md, client, "c", "v", "http://127.0.0.1:1/callback"); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_TOKEN_FAILED]") {
		t.Fatalf("non-bearer token type must fail, got %v", err)
	}
	status = http.StatusServiceUnavailable
	body = map[string]any{}
	if _, err = Refresh(context.Background(), server.Client(), md, client, "rt"); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_TOKEN_FAILED]") {
		t.Fatalf("server failure during refresh must not be reported as rejected, got %v", err)
	}
}

func TestConfidentialClientUsesBasicAuth(t *testing.T) {
	allowHTTP(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		_ = r.ParseForm()
		if !ok || user != "cid" || pass != "s3cret" || r.PostForm.Get("client_secret") != "" {
			t.Errorf("basic auth = %q/%q/%v form=%v", user, pass, ok, r.PostForm)
		}
		writeJSON(t, w, 200, map[string]any{"access_token": "at", "token_type": "Bearer"})
	}))
	defer server.Close()
	tokens, err := Refresh(context.Background(), server.Client(), Metadata{TokenEndpoint: server.URL}, Client{ID: "cid", Secret: "s3cret"}, "rt")
	if err != nil || !tokens.ExpiresAt.IsZero() || tokens.RefreshToken != "rt" {
		t.Fatalf("tokens=%#v err=%v", tokens, err)
	}
}

func TestTokenEndpointMustBeHTTPS(t *testing.T) {
	_, err := ExchangeCode(context.Background(), nil, Metadata{TokenEndpoint: "http://as.example/token"}, Client{ID: "x"}, "c", "v", "http://127.0.0.1:1/callback")
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_INSECURE_URL]") {
		t.Fatalf("http token endpoint must be refused, got %v", err)
	}
}
