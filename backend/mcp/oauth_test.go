package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConnectOAuthWithoutTokenSourceFails(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(server.Close)
	args, _ := json.Marshal(connectArgs{ServerID: "oauth-6", Transport: "http", URL: server.URL, Auth: "oauth"})
	_, err := mcpConnectHandler("", string(args))
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_UNSUPPORTED]") || hits.Load() != 0 {
		t.Fatalf("err = %v, hits = %d", err, hits.Load())
	}
}

func TestUnauthorizedBearerChallengeReportsAuthRequired(t *testing.T) {
	for _, test := range []struct {
		challenge string
		bearer    bool
	}{
		{`Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`, true},
		{`Basic realm="x", bearer error="invalid_token"`, true},
		{`Basic realm="x"`, false},
	} {
		t.Run(test.challenge, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Header.Get("X-Api-Key") != "k" {
					t.Error("configured API key was not forwarded")
				}
				w.Header().Set("WWW-Authenticate", test.challenge)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			}))
			t.Cleanup(server.Close)
			args, _ := json.Marshal(connectArgs{
				ServerID: "plain", Transport: "http", URL: server.URL,
				Headers: map[string]string{"X-Api-Key": "k"},
			})
			_, err := mcpConnectHandler("", string(args))
			if err == nil || strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") != test.bearer || hits.Load() != 1 {
				t.Fatalf("err = %v, hits = %d", err, hits.Load())
			}
		})
	}
}
