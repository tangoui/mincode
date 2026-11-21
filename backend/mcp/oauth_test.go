package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeOAuth stands in for mcpauth: it keeps one stored token that may be
// expired, refreshes it on demand and records every call.
type fakeOAuth struct {
	mu             sync.Mutex
	token          string
	expired        bool
	next           []string // tokens handed out by successive refreshes
	refreshErr     error
	accessErr      error
	accessCalls    []string // serverID|url
	refreshCalls   int
	rejectedTokens []string
}

func (f *fakeOAuth) refreshLocked() (string, error) {
	f.refreshCalls++
	if f.refreshErr != nil {
		return "", f.refreshErr
	}
	if len(f.next) == 0 {
		return "", errors.New("[MCP_AUTH_REQUIRED] no refresh left")
	}
	f.token, f.next = f.next[0], f.next[1:]
	f.expired = false
	return f.token, nil
}

func (f *fakeOAuth) source() OAuthTokenSource {
	return OAuthTokenSource{
		AccessToken: func(_ context.Context, serverID, url string) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.accessCalls = append(f.accessCalls, serverID+"|"+url)
			if f.accessErr != nil {
				return "", f.accessErr
			}
			if f.expired {
				return f.refreshLocked()
			}
			return f.token, nil
		},
		RefreshAfterUnauthorized: func(_ context.Context, _, _ string, rejected string) (string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.rejectedTokens = append(f.rejectedTokens, rejected)
			return f.refreshLocked()
		},
	}
}

func useFakeOAuth(t *testing.T, f *fakeOAuth) {
	t.Helper()
	oauthMu.Lock()
	previous := oauthSource
	oauthSource = f.source()
	oauthMu.Unlock()
	t.Cleanup(func() {
		oauthMu.Lock()
		oauthSource = previous
		oauthMu.Unlock()
	})
}

// mcpServer answers JSON-RPC when the request carries an accepted bearer
// token and 401 otherwise. It records every Authorization header it saw.
type mcpServer struct {
	*httptest.Server
	mu        sync.Mutex
	seen      [][]string
	accept    func(auth string) bool
	challenge string
}

func newMCPServer(t *testing.T, accept func(auth string) bool, challenge string) *mcpServer {
	t.Helper()
	s := &mcpServer{accept: accept, challenge: challenge}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Header.Values("Authorization"))
		s.mu.Unlock()
		if !s.accept(r.Header.Get("Authorization")) {
			if s.challenge != "" {
				w.Header().Set("WWW-Authenticate", s.challenge)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var msg struct {
			ID     *uint64 `json:"id"`
			Method string  `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &msg)
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := map[string]any{}
		if msg.Method == "tools/list" {
			result["tools"] = []map[string]any{{"name": "echo"}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": result})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *mcpServer) headers() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.seen...)
}

func connectArgsJSON(t *testing.T, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestConnectRefreshesExpiredToken(t *testing.T) {
	fake := &fakeOAuth{token: "expired-token", expired: true, next: []string{"fresh-token"}}
	useFakeOAuth(t, fake)
	server := newMCPServer(t, func(auth string) bool { return auth == "Bearer fresh-token" }, "")
	url := server.URL + "/mcp"

	out, err := mcpConnectHandler("", connectArgsJSON(t, map[string]any{
		"serverId": "oauth-1", "transport": "http", "url": url, "auth": "oauth",
		// A stale user-entered header must be replaced, not sent alongside.
		"headers": map[string]string{"authorization": "Bearer user-entered", "X-Extra": "1"},
	}))
	t.Cleanup(func() { gateway.disconnect("oauth-1") })
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !strings.Contains(out, `"echo"`) {
		t.Fatalf("connect result = %s", out)
	}
	if fake.refreshCalls != 1 || len(fake.accessCalls) != 1 || fake.accessCalls[0] != "oauth-1|"+url {
		t.Fatalf("access calls %v, refreshes %d", fake.accessCalls, fake.refreshCalls)
	}
	for _, values := range server.headers() {
		if len(values) != 1 || values[0] != "Bearer fresh-token" {
			t.Fatalf("Authorization headers = %v, want only the refreshed token", server.headers())
		}
	}
}

func TestConnectRetriesOnceAfterUnauthorized(t *testing.T) {
	fake := &fakeOAuth{token: "revoked-token", next: []string{"rotated-token"}}
	useFakeOAuth(t, fake)
	server := newMCPServer(t, func(auth string) bool { return auth == "Bearer rotated-token" }, `Bearer error="invalid_token"`)

	_, err := mcpConnectHandler("", connectArgsJSON(t, map[string]any{
		"serverId": "oauth-2", "transport": "http", "url": server.URL + "/mcp", "auth": "oauth",
	}))
	t.Cleanup(func() { gateway.disconnect("oauth-2") })
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if len(fake.rejectedTokens) != 1 || fake.rejectedTokens[0] != "revoked-token" {
		t.Fatalf("rejected tokens = %v", fake.rejectedTokens)
	}
	seen := server.headers()
	if len(seen) < 2 || seen[0][0] != "Bearer revoked-token" || seen[1][0] != "Bearer rotated-token" {
		t.Fatalf("headers = %v", seen)
	}
}

func TestConnectStillUnauthorizedAfterRefreshReportsAuthRequired(t *testing.T) {
	fake := &fakeOAuth{token: "a", next: []string{"b", "c"}}
	useFakeOAuth(t, fake)
	server := newMCPServer(t, func(string) bool { return false }, `Bearer realm="x"`)

	_, err := mcpConnectHandler("", connectArgsJSON(t, map[string]any{
		"serverId": "oauth-3", "transport": "http", "url": server.URL + "/mcp", "auth": "oauth",
	}))
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") {
		t.Fatalf("err = %v, want [MCP_AUTH_REQUIRED]", err)
	}
	if fake.refreshCalls != 1 || len(server.headers()) != 2 {
		t.Fatalf("refresh once and retry once: refreshes %d, requests %v", fake.refreshCalls, server.headers())
	}
	if strings.Contains(err.Error(), "Bearer b") || strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestConnectRefreshFailureAfterUnauthorizedReportsAuthRequired(t *testing.T) {
	fake := &fakeOAuth{token: "a", refreshErr: errors.New("[MCP_AUTH_REQUIRED] 连接器需要登录")}
	useFakeOAuth(t, fake)
	server := newMCPServer(t, func(string) bool { return false }, "")

	_, err := mcpConnectHandler("", connectArgsJSON(t, map[string]any{
		"serverId": "oauth-4", "transport": "http", "url": server.URL + "/mcp", "auth": "oauth",
	}))
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") {
		t.Fatalf("err = %v", err)
	}
	if len(server.headers()) != 1 {
		t.Fatalf("no retry without a new token: %v", server.headers())
	}
}

func TestConnectOAuthNotSignedInDoesNotContactServer(t *testing.T) {
	fake := &fakeOAuth{accessErr: errors.New("[MCP_AUTH_REQUIRED] 连接器需要登录")}
	useFakeOAuth(t, fake)
	server := newMCPServer(t, func(string) bool { return true }, "")

	_, err := mcpConnectHandler("", connectArgsJSON(t, map[string]any{
		"serverId": "oauth-5", "transport": "http", "url": server.URL + "/mcp", "auth": "oauth",
	}))
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") {
		t.Fatalf("err = %v", err)
	}
	if len(server.headers()) != 0 {
		t.Fatalf("server contacted without a token: %v", server.headers())
	}
}

func TestConnectOAuthWithoutTokenSourceFails(t *testing.T) {
	oauthMu.Lock()
	previous := oauthSource
	oauthSource = OAuthTokenSource{}
	oauthMu.Unlock()
	t.Cleanup(func() {
		oauthMu.Lock()
		oauthSource = previous
		oauthMu.Unlock()
	})
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	_, err := mcpConnectHandler("", connectArgsJSON(t, map[string]any{
		"serverId": "oauth-6", "transport": "http", "url": server.URL, "auth": "oauth",
	}))
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_UNSUPPORTED]") || hits.Load() != 0 {
		t.Fatalf("err = %v, hits = %d", err, hits.Load())
	}
}

func TestUnauthorizedBearerChallengeReportsAuthRequired(t *testing.T) {
	for _, challenge := range []string{
		`Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`,
		`Basic realm="x", bearer error="invalid_token"`,
	} {
		server := newMCPServer(t, func(string) bool { return false }, challenge)
		_, err := mcpConnectHandler("", connectArgsJSON(t, map[string]any{
			"serverId": "plain-1", "transport": "http", "url": server.URL + "/mcp",
			"headers": map[string]string{"X-Api-Key": "k"},
		}))
		if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") {
			t.Fatalf("challenge %q: err = %v, want [MCP_AUTH_REQUIRED]", challenge, err)
		}
		if len(server.headers()) != 1 {
			t.Fatalf("non-oauth connections never retry: %v", server.headers())
		}
	}

	// Without a Bearer challenge a 401 stays an ordinary HTTP error.
	server := newMCPServer(t, func(string) bool { return false }, `Basic realm="x"`)
	_, err := mcpConnectHandler("", connectArgsJSON(t, map[string]any{
		"serverId": "plain-2", "transport": "http", "url": server.URL + "/mcp",
	}))
	if err == nil || strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("basic challenge: err = %v", err)
	}
}

func TestAuthRequiredOnLaterRequestOfOAuthSession(t *testing.T) {
	fake := &fakeOAuth{token: "a", next: []string{"b"}}
	useFakeOAuth(t, fake)
	var revoked atomic.Bool
	server := newMCPServer(t, func(auth string) bool { return !revoked.Load() && auth == "Bearer a" }, "Bearer")
	if _, err := mcpConnectHandler("", connectArgsJSON(t, map[string]any{
		"serverId": "oauth-7", "transport": "http", "url": server.URL + "/mcp", "auth": "oauth",
	})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gateway.disconnect("oauth-7") })
	revoked.Store(true)
	_, err := mcpCallToolHandler("", connectArgsJSON(t, map[string]any{"serverId": "oauth-7", "toolName": "echo", "arguments": map[string]any{}}))
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") {
		t.Fatalf("tool call err = %v", err)
	}
	if len(fake.rejectedTokens) != 1 || fake.rejectedTokens[0] != "a" {
		t.Fatalf("rejected = %v", fake.rejectedTokens)
	}
}
