package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestHTTPConn(t *testing.T, target string, client *http.Client) *httpConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if client == nil {
		client = newMCPHTTPClient()
	}
	return &httpConn{
		serverID: "test",
		url:      target,
		headers:  map[string]string{"X-Api-Key": "secret-key", "Authorization": "Bearer secret"},
		client:   client,
		ctx:      ctx,
		cancel:   cancel,
	}
}

func TestMCPHTTPRejectsCrossHostRedirect(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
		var hits atomic.Int32
		var leaked atomic.Bool
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			body, _ := io.ReadAll(r.Body)
			if r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") != "" || strings.Contains(string(body), "tools/call") {
				leaked.Store(true)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		}))
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/mcp", status)
		}))

		conn := newTestHTTPConn(t, origin.URL+"/mcp", nil)
		_, err := conn.request("tools/call", map[string]any{"name": "x"})
		origin.Close()
		other.Close()
		if err == nil || !strings.Contains(err.Error(), "跨主机重定向") {
			t.Fatalf("status %d: cross-host redirect must fail, got %v", status, err)
		}
		if hits.Load() != 0 || leaked.Load() {
			t.Fatalf("status %d: redirect target must not receive the request (hits=%d leaked=%v)", status, hits.Load(), leaked.Load())
		}
	}
}

// connectHTTP is the production entry point; it must install the redirect policy
// itself rather than relying on callers to pass a hardened client.
func TestConnectHTTPRejectsCrossHostRedirect(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/mcp", http.StatusFound)
	}))
	defer origin.Close()

	conn, err := connectHTTP(context.Background(), "test", origin.URL+"/mcp", map[string]string{"X-Api-Key": "secret-key"}, "")
	if conn != nil {
		conn.transport.close()
	}
	if err == nil || !strings.Contains(err.Error(), "跨主机重定向") {
		t.Fatalf("connectHTTP must reject a cross-host redirect, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("redirect target must not be contacted, hits=%d", hits.Load())
	}
}

func TestMCPHTTPFollowsSameOriginRedirect(t *testing.T) {
	var gotKey atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/mcp", http.StatusTemporaryRedirect)
			return
		}
		gotKey.Store(r.Header.Get("X-Api-Key"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	}))
	defer srv.Close()

	conn := newTestHTTPConn(t, srv.URL+"/old", nil)
	res, err := conn.request("tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("same-origin redirect must succeed: %v", err)
	}
	if string(res) != `{"ok":true}` || gotKey.Load() != "secret-key" {
		t.Fatalf("unexpected result %s / key %v", res, gotKey.Load())
	}
}

func TestMCPHTTPRejectsHTTPSDowngradeRedirect(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer secure.Close()

	client := secure.Client()
	client.CheckRedirect = mcpCheckRedirect
	conn := newTestHTTPConn(t, secure.URL+"/mcp", client)
	_, err := conn.request("tools/list", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "降级") {
		t.Fatalf("https→http redirect must fail, got %v", err)
	}
	if plainHits.Load() != 0 {
		t.Fatal("plain http target must not be contacted")
	}
}

func TestMCPCheckRedirectPolicy(t *testing.T) {
	mk := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodPost, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	first := mk("https://mcp.example.com/mcp")
	cases := map[string]bool{
		"https://mcp.example.com/other":     true,
		"https://mcp.example.com:443/other": true,
		"http://mcp.example.com/mcp":        false,
		"https://mcp.example.com:8443/mcp":  false,
		"https://evil.example.net/mcp":      false,
		"http://169.254.169.254/latest":     false,
		"https://metadata.google.internal/": false,
	}
	for target, ok := range cases {
		err := mcpCheckRedirect(mk(target), []*http.Request{first})
		if (err == nil) != ok {
			t.Errorf("%s: allowed=%v err=%v", target, err == nil, err)
		}
	}
	via := make([]*http.Request, mcpMaxRedirects)
	for i := range via {
		via[i] = first
	}
	if err := mcpCheckRedirect(mk("https://mcp.example.com/again"), via); err == nil {
		t.Fatal("redirect chain must be bounded")
	}
}
