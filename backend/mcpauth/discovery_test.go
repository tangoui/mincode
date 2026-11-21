package mcpauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type requestLog struct {
	mu    sync.Mutex
	paths []string
}

func (l *requestLog) add(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, path)
}

func (l *requestLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.paths...)
}

// Figma shape: the 401 carries resource_metadata, the resource metadata names
// an authorization server that serves RFC 8414 metadata.
func TestDiscoverFromResourceMetadataHeader(t *testing.T) {
	allowHTTP(t)
	var log requestLog
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.URL.Path)
		switch r.URL.Path {
		case "/custom/resource-meta":
			writeJSON(t, w, 200, map[string]any{
				"resource":              server.URL + "/mcp",
				"authorization_servers": []string{server.URL + "/as"},
				"scopes_supported":      []string{"mcp:connect", "files:read"},
			})
		case "/.well-known/oauth-authorization-server/as":
			writeJSON(t, w, 200, map[string]any{
				"issuer":                           server.URL + "/as",
				"authorization_endpoint":           server.URL + "/as/authorize",
				"token_endpoint":                   server.URL + "/as/token",
				"registration_endpoint":            server.URL + "/as/register",
				"device_authorization_endpoint":    server.URL + "/as/device",
				"code_challenge_methods_supported": []string{"plain", "S256"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	header := `Bearer resource_metadata="` + server.URL + `/custom/resource-meta",scope="mcp:connect",error="invalid_token"`
	md, err := Discover(context.Background(), server.Client(), server.URL+"/mcp", header)
	if err != nil {
		t.Fatal(err)
	}
	want := Metadata{
		Issuer:                      server.URL + "/as",
		Resource:                    server.URL + "/mcp",
		AuthorizationEndpoint:       server.URL + "/as/authorize",
		TokenEndpoint:               server.URL + "/as/token",
		RegistrationEndpoint:        server.URL + "/as/register",
		DeviceAuthorizationEndpoint: server.URL + "/as/device",
		Scopes:                      []string{"mcp:connect"},
		CodeChallengeS256:           true,
	}
	if !reflect.DeepEqual(md, want) {
		t.Fatalf("metadata = %#v\nwant %#v", md, want)
	}
	if got := log.list(); got[0] != "/custom/resource-meta" {
		t.Fatalf("resource metadata from header must be fetched first, got %v", got)
	}
}

func TestDiscoverFallsBackToWellKnown(t *testing.T) {
	allowHTTP(t)
	var log requestLog
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.URL.Path)
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/v1/mcp":
			writeJSON(t, w, 200, map[string]any{
				"resource":              server.URL + "/v1/mcp",
				"authorization_servers": []string{server.URL + "/tenant"},
				"scopes_supported":      []string{"read", "write"},
			})
		case "/.well-known/openid-configuration/tenant":
			// RFC 8414 is missing (404 below), OIDC discovery answers.
			writeJSON(t, w, 200, map[string]any{
				"issuer":                           server.URL + "/tenant",
				"authorization_endpoint":           server.URL + "/tenant/authorize",
				"token_endpoint":                   server.URL + "/tenant/token",
				"code_challenge_methods_supported": []string{"S256"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	md, err := Discover(context.Background(), server.Client(), server.URL+"/v1/mcp?api_key=k", `Bearer realm="mcp"`)
	if err != nil {
		t.Fatal(err)
	}
	if md.AuthorizationEndpoint != server.URL+"/tenant/authorize" || md.TokenEndpoint != server.URL+"/tenant/token" {
		t.Fatalf("endpoints = %#v", md)
	}
	if md.RegistrationEndpoint != "" || !md.CodeChallengeS256 || md.Resource != server.URL+"/v1/mcp" {
		t.Fatalf("metadata = %#v", md)
	}
	if !reflect.DeepEqual(md.Scopes, []string{"read", "write"}) {
		t.Fatalf("scopes = %v", md.Scopes)
	}
	wantOrder := []string{
		"/.well-known/oauth-protected-resource/v1/mcp",
		"/.well-known/oauth-authorization-server/tenant",
		"/.well-known/openid-configuration/tenant",
	}
	if got := log.list(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("request order = %v, want %v", got, wantOrder)
	}
}

func TestDiscoverFallsBackToRootResourceMetadata(t *testing.T) {
	allowHTTP(t)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			writeJSON(t, w, 200, map[string]any{"authorization_servers": []string{server.URL}})
		case "/.well-known/oauth-authorization-server":
			writeJSON(t, w, 200, map[string]any{
				"issuer":                 server.URL,
				"authorization_endpoint": server.URL + "/authorize",
				"token_endpoint":         server.URL + "/token",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	md, err := Discover(context.Background(), server.Client(), server.URL+"/mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	if md.Resource != server.URL+"/mcp" || md.Issuer != server.URL || md.CodeChallengeS256 {
		t.Fatalf("metadata = %#v", md)
	}
}

type failingTransport struct{ hosts *requestLog }

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.hosts.add(r.URL.Host)
	return nil, errors.New("unexpected request")
}

// githubShapedServer serves GitHub-style protected-resource metadata (naming
// the GitHub authorization server) for whatever origin the request came to.
// The returned client routes api.githubcopilot.com to the test server and
// fails any request to github.com.
func githubShapedServer(t *testing.T) (*http.Client, *requestLog, string, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource/mcp/" {
			origin := "https://" + r.Host
			if r.Host != "api.githubcopilot.com" {
				origin = "http://" + r.Host
			}
			writeJSON(t, w, 200, map[string]any{
				"resource":              origin + "/mcp/",
				"authorization_servers": []string{"https://github.com/login/oauth"},
				"scopes_supported":      []string{"repo", "read:org"},
			})
			return
		}
		http.NotFound(w, r)
	}))
	target, _ := url.Parse(server.URL)
	base := server.Client().Transport
	githubHits := &requestLog{}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "github.com":
			return failingTransport{githubHits}.RoundTrip(r)
		case "api.githubcopilot.com":
			r = r.Clone(r.Context())
			r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
			r.Host = "api.githubcopilot.com"
		}
		return base.RoundTrip(r)
	})}
	return client, githubHits, server.URL, server.Close
}

func TestDiscoverGitHubUsesBuiltinEndpoints(t *testing.T) {
	allowHTTP(t)
	client, githubHits, _, stop := githubShapedServer(t)
	defer stop()

	md, err := Discover(context.Background(), client, "https://api.githubcopilot.com/mcp/", `Bearer error="invalid_request"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(githubHits.list()) != 0 {
		t.Fatalf("github.com must not be asked for RFC 8414 metadata: %v", githubHits.list())
	}
	want := Metadata{
		Issuer:                      "https://github.com/login/oauth",
		Resource:                    "https://api.githubcopilot.com/mcp/",
		AuthorizationEndpoint:       "https://github.com/login/oauth/authorize",
		TokenEndpoint:               "https://github.com/login/oauth/access_token",
		DeviceAuthorizationEndpoint: "https://github.com/login/device/code",
		Scopes:                      []string{"repo", "read:org"},
		CodeChallengeS256:           true,
	}
	if !reflect.DeepEqual(md, want) {
		t.Fatalf("metadata = %#v\nwant %#v", md, want)
	}
	c, err := PreregisteredFor(md)
	if err != nil || c.ID != "" || !c.Public || !c.Device {
		t.Fatalf("GitHub preregistered client = %#v, %v", c, err)
	}
}

// A connector on any other origin that names the GitHub authorization server
// must not get MinCode's GitHub client or endpoints (confused deputy).
func TestDiscoverGitHubRejectsForeignResource(t *testing.T) {
	allowHTTP(t)
	client, githubHits, hostileURL, stop := githubShapedServer(t)
	defer stop()

	// The same metadata served from 127.0.0.1 plays the hostile connector.
	_, err := Discover(context.Background(), client, hostileURL+"/mcp/", "")
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_UNSUPPORTED]") {
		t.Fatalf("foreign resource naming the GitHub AS must be unsupported, got %v", err)
	}
	if len(githubHits.list()) != 0 {
		t.Fatalf("github.com must not be contacted: %v", githubHits.list())
	}

	forged := Metadata{Issuer: githubIssuer, Resource: "https://evil.example/mcp/"}
	if _, err := PreregisteredFor(forged); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_UNSUPPORTED]") {
		t.Fatalf("preregistered client must be bound to its resource origins, got %v", err)
	}
	for _, md := range []Metadata{
		{Issuer: githubIssuer, Resource: "https://api.githubcopilot.com.evil.example/mcp/"},
		{Issuer: githubIssuer, Resource: "http://api.githubcopilot.com/mcp/"},
		{Issuer: githubIssuer, Resource: ""},
		{Issuer: "https://unknown.example", Resource: "https://api.githubcopilot.com/mcp/"},
	} {
		if _, err := PreregisteredFor(md); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_UNSUPPORTED]") {
			t.Fatalf("PreregisteredFor(%#v) must be unsupported, got %v", md, err)
		}
	}
	if _, err := PreregisteredFor(Metadata{Issuer: githubIssuer + "/", Resource: "https://API.githubcopilot.com:443/mcp/"}); err != nil {
		t.Fatalf("normalized GitHub resource must be accepted: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDiscoverRejectsUnsafeMetadata(t *testing.T) {
	allowHTTP(t)
	cases := map[string]func(base string) (prm, as map[string]any){
		"resource mismatch": func(base string) (map[string]any, map[string]any) {
			return map[string]any{"resource": "https://evil.example/mcp", "authorization_servers": []string{base}},
				map[string]any{"issuer": base, "authorization_endpoint": base + "/a", "token_endpoint": base + "/t"}
		},
		"issuer mismatch": func(base string) (map[string]any, map[string]any) {
			return map[string]any{"authorization_servers": []string{base}},
				map[string]any{"issuer": "https://other.example", "authorization_endpoint": base + "/a", "token_endpoint": base + "/t"}
		},
		"missing token endpoint": func(base string) (map[string]any, map[string]any) {
			return map[string]any{"authorization_servers": []string{base}},
				map[string]any{"issuer": base, "authorization_endpoint": base + "/a"}
		},
		"no authorization server": func(base string) (map[string]any, map[string]any) {
			return map[string]any{}, nil
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				prm, as := build(server.URL)
				switch r.URL.Path {
				case "/.well-known/oauth-protected-resource/mcp":
					writeJSON(t, w, 200, prm)
				case "/.well-known/oauth-authorization-server":
					writeJSON(t, w, 200, as)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			_, err := Discover(context.Background(), server.Client(), server.URL+"/mcp", "")
			if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_DISCOVERY_FAILED]") {
				t.Fatalf("want discovery failure, got %v", err)
			}
		})
	}
}

func TestDiscoverRequiresHTTPS(t *testing.T) {
	_, err := Discover(context.Background(), nil, "http://mcp.example.com/mcp", "")
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_INSECURE_URL]") {
		t.Fatalf("plain http must be refused, got %v", err)
	}
	allowHTTP(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, map[string]any{"authorization_servers": []string{"http://insecure.example"}})
	}))
	defer server.Close()
	allowHTTPForTest = false
	_, err = Discover(context.Background(), server.Client(), "https://"+strings.TrimPrefix(server.URL, "http://")+"/mcp", `Bearer resource_metadata="`+server.URL+`/meta"`)
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_INSECURE_URL]") {
		t.Fatalf("http resource_metadata must be refused, got %v", err)
	}
}

func TestDiscoverCapsResponseSize(t *testing.T) {
	allowHTTP(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resource":"` + strings.Repeat("a", 2<<20) + `"}`))
	}))
	defer server.Close()
	_, err := Discover(context.Background(), server.Client(), server.URL+"/mcp", `Bearer resource_metadata="`+server.URL+`/meta"`)
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_RESPONSE_TOO_LARGE]") {
		t.Fatalf("oversized metadata must be refused, got %v", err)
	}
}

func TestParseWWWAuthenticate(t *testing.T) {
	params := parseBearerParams(`Basic realm="x", Bearer realm="a, b", resource_metadata="https://x.example/m?a=\"q\"", scope="mcp:connect files"`)
	if params["resource_metadata"] != `https://x.example/m?a="q"` || params["scope"] != "mcp:connect files" || params["realm"] != "a, b" {
		t.Fatalf("params = %#v", params)
	}
	if got := parseBearerParams(`Basic resource_metadata="https://evil.example"`); got["resource_metadata"] != "" {
		t.Fatalf("non-bearer params must be ignored: %#v", got)
	}
}
