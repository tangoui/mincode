package mcpauth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"
)

// authFixture is one httptest server acting as the MCP resource server
// (/mcp), its protected-resource metadata and the authorization server (/as).
type authFixture struct {
	t      *testing.T
	server *httptest.Server
	base   string

	// Authorization-server capabilities.
	registration bool
	device       bool
	s256         bool

	// tokenReply answers token requests; the default implementation issues
	// at-<n>/rt-<n> for code and refresh grants.
	tokenReply func(w http.ResponseWriter, form url.Values)
	// deviceReply answers device authorization requests.
	deviceReply func(w http.ResponseWriter, form url.Values)
	// acceptBearer is the token /mcp accepts; empty rejects everything.
	acceptBearer string

	mu            sync.Mutex
	registrations []map[string]any
	tokenForms    []url.Values
	deviceForms   []url.Values
	probes        []http.Header
	mcpAuth       []string
	issued        int
}

func newAuthFixture(t *testing.T, tls bool) *authFixture {
	t.Helper()
	f := &authFixture{t: t, registration: true, s256: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", f.handleMCP)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, 200, map[string]any{
			"resource":              f.base + "/mcp",
			"authorization_servers": []string{f.base + "/as"},
			"scopes_supported":      []string{"mcp:read", "mcp:write"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/as", func(w http.ResponseWriter, r *http.Request) {
		md := map[string]any{
			"issuer":                 f.base + "/as",
			"authorization_endpoint": f.base + "/as/authorize?prompt=consent",
			"token_endpoint":         f.base + "/as/token",
		}
		if f.registration {
			md["registration_endpoint"] = f.base + "/as/register"
		}
		if f.device {
			md["device_authorization_endpoint"] = f.base + "/as/device"
		}
		if f.s256 {
			md["code_challenge_methods_supported"] = []string{"plain", "S256"}
		}
		writeJSON(t, w, 200, md)
	})
	mux.HandleFunc("/as/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.registrations = append(f.registrations, body)
		n := len(f.registrations)
		f.mu.Unlock()
		writeJSON(t, w, 201, map[string]any{"client_id": "dyn-" + string(rune('0'+n))})
	})
	mux.HandleFunc("/as/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		f.mu.Lock()
		f.tokenForms = append(f.tokenForms, r.PostForm)
		reply := f.tokenReply
		f.mu.Unlock()
		if reply != nil {
			reply(w, r.PostForm)
			return
		}
		f.issue(w)
	})
	mux.HandleFunc("/as/device", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		f.mu.Lock()
		f.deviceForms = append(f.deviceForms, r.PostForm)
		reply := f.deviceReply
		f.mu.Unlock()
		if reply != nil {
			reply(w, r.PostForm)
			return
		}
		writeJSON(t, w, 200, map[string]any{
			"device_code": "device-code-secret", "user_code": "WDJB-MJHT",
			"verification_uri": f.base + "/login/device", "expires_in": 900, "interval": 1,
		})
	})
	if tls {
		f.server = httptest.NewTLSServer(mux)
	} else {
		f.server = httptest.NewServer(mux)
	}
	f.base = f.server.URL
	t.Cleanup(f.server.Close)
	return f
}

func (f *authFixture) mcpURL() string { return f.base + "/mcp" }

// issue answers with a fresh token pair at-<n>/rt-<n>.
func (f *authFixture) issue(w http.ResponseWriter) {
	f.mu.Lock()
	f.issued++
	n := f.issued
	f.mu.Unlock()
	suffix := string(rune('0' + n))
	writeJSON(f.t, w, 200, map[string]any{
		"access_token": "at-" + suffix, "refresh_token": "rt-" + suffix,
		"token_type": "Bearer", "expires_in": 3600,
	})
}

func (f *authFixture) handleMCP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.mcpAuth = append(f.mcpAuth, r.Header.Get("Authorization"))
	if r.Header.Get("Authorization") == "" {
		f.probes = append(f.probes, r.Header.Clone())
	}
	accept := f.acceptBearer
	f.mu.Unlock()
	if accept == "" || r.Header.Get("Authorization") != "Bearer "+accept {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", resource_metadata="`+f.base+`/.well-known/oauth-protected-resource/mcp"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var msg struct {
		ID *uint64 `json:"id"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &msg)
	if msg.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(f.t, w, 200, map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]any{"tools": []any{}}})
}

func (f *authFixture) forms() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.tokenForms...)
}

// useFixtureClient routes package HTTP through the fixture's client (which
// trusts its TLS certificate) and allows plain HTTP for non-TLS fixtures.
func useFixtureClient(t *testing.T, f *authFixture) {
	t.Helper()
	allowHTTP(t)
	previous := httpClient
	httpClient = f.server.Client()
	t.Cleanup(func() { httpClient = previous })
}

// trustPreregistered installs a preregistered client for the fixture's issuer,
// bound to the fixture's origin, for the duration of the test.
func trustPreregistered(t *testing.T, f *authFixture, client Client) {
	t.Helper()
	issuer := f.base + "/as"
	previousClient, hadClient := Preregistered[issuer]
	previousOrigins, hadOrigins := trustedResourceOrigins[issuer]
	Preregistered[issuer] = client
	trustedResourceOrigins[issuer] = []string{f.base}
	t.Cleanup(func() {
		if hadClient {
			Preregistered[issuer] = previousClient
		} else {
			delete(Preregistered, issuer)
		}
		if hadOrigins {
			trustedResourceOrigins[issuer] = previousOrigins
		} else {
			delete(trustedResourceOrigins, issuer)
		}
	})
}

func fastDevicePolling(t *testing.T) {
	t.Helper()
	previous := devicePollUnit
	devicePollUnit = time.Millisecond
	t.Cleanup(func() { devicePollUnit = previous })
}

func shortLoginTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	previous := loginTimeout
	loginTimeout = d
	t.Cleanup(func() { loginTimeout = previous })
}

// TestMain bounds every login in this package's tests so a regression that
// waits for a browser callback fails in seconds instead of five minutes.
func TestMain(m *testing.M) {
	loginTimeout = 5 * time.Second
	os.Exit(m.Run())
}
