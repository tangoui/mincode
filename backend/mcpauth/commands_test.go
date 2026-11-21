package mcpauth

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/mcp"
)

type recordedEvent struct {
	name string
	data any
}

func captureEvents(t *testing.T) func() []recordedEvent {
	t.Helper()
	var mu sync.Mutex
	var events []recordedEvent
	core.SetEmitter(func(name string, data any) {
		mu.Lock()
		events = append(events, recordedEvent{name, data})
		mu.Unlock()
	})
	t.Cleanup(func() { core.SetEmitter(nil) })
	return func() []recordedEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedEvent(nil), events...)
	}
}

func useBrowser(t *testing.T, open func(string) error) {
	t.Helper()
	previous := browserOpener()
	SetBrowserOpener(open)
	t.Cleanup(func() { SetBrowserOpener(previous) })
}

func TestCommandsAreRegistered(t *testing.T) {
	registered := map[string]bool{}
	for _, name := range core.Commands() {
		registered[name] = true
	}
	for _, name := range []string{"mcp_oauth_login", "mcp_oauth_logout", "mcp_oauth_status", "mcp_oauth_access_token"} {
		if !registered[name] {
			t.Fatalf("%s is not registered", name)
		}
	}
}

func TestLoginCommandEmitsDeviceCode(t *testing.T) {
	f := deviceFixture(t)
	pollReplies(t, f, "authorization_pending")
	events := captureEvents(t)
	var opened []string
	useBrowser(t, func(u string) error { opened = append(opened, u); return nil })

	args := `{"serverId":"github","url":"` + f.mcpURL() + `"}`
	out, err := core.DispatchContext(context.Background(), "mcp_oauth_login", args)
	if err != nil || out != `{"status":"signed-in"}` {
		t.Fatalf("login = %s, %v", out, err)
	}
	got := events()
	if len(got) != 1 || got[0].name != DeviceCodeEvent {
		t.Fatalf("events = %+v", got)
	}
	payload, _ := json.Marshal(got[0].data)
	want := `{"serverId":"github","userCode":"WDJB-MJHT","verificationUri":"` + f.base + `/login/device"}`
	if string(payload) != want {
		t.Fatalf("event payload = %s, want %s", payload, want)
	}
	if strings.Contains(string(payload), "device-code-secret") {
		t.Fatal("event leaks the device code")
	}
	if len(opened) != 1 || opened[0] != f.base+"/login/device" {
		t.Fatalf("opened = %v", opened)
	}

	out, err = core.Dispatch("mcp_oauth_status", args)
	if err != nil || out != `{"status":"signed-in"}` {
		t.Fatalf("status = %s, %v", out, err)
	}
	out, err = core.DispatchContext(context.Background(), "mcp_oauth_access_token", args)
	if err != nil || out != `{"token":"at-1"}` {
		t.Fatalf("access token = %s, %v", out, err)
	}
	if _, err := core.Dispatch("mcp_oauth_logout", `{"serverId":"github"}`); err != nil {
		t.Fatal(err)
	}
	out, _ = core.Dispatch("mcp_oauth_status", args)
	if out != `{"status":"signed-out"}` {
		t.Fatalf("status after logout = %s", out)
	}
	if _, err := core.DispatchContext(context.Background(), "mcp_oauth_access_token", args); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") {
		t.Fatalf("access token after logout: %v", err)
	}
}

func TestLoginCommandReportsCancelled(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	opened := make(chan struct{})
	useBrowser(t, func(string) error { close(opened); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-opened; cancel() }()
	out, err := core.DispatchContext(ctx, "mcp_oauth_login", `{"serverId":"x","url":"`+f.mcpURL()+`"}`)
	if err != nil || out != `{"status":"cancelled"}` {
		t.Fatalf("login = %s, %v", out, err)
	}
}

func TestCommandsValidateArguments(t *testing.T) {
	tempStorage(t)
	for _, tc := range []struct{ cmd, args string }{
		{"mcp_oauth_login", `{"serverId":"","url":"https://x.example/mcp"}`},
		{"mcp_oauth_login", `{"serverId":"x","url":"http://x.example/mcp"}`},
		{"mcp_oauth_login", `{"serverId":"x","url":"file:///etc/passwd"}`},
		{"mcp_oauth_status", `{"serverId":"x\n","url":"https://x.example/mcp"}`},
		{"mcp_oauth_access_token", `{"serverId":"x","url":""}`},
		{"mcp_oauth_logout", `{"serverId":""}`},
	} {
		if _, err := core.DispatchContext(context.Background(), tc.cmd, tc.args); err == nil {
			t.Fatalf("%s %s must be rejected", tc.cmd, tc.args)
		}
	}
	out, err := core.Dispatch("mcp_oauth_status", `{"serverId":"x","url":"https://x.example/mcp"}`)
	if err != nil || out != `{"status":"signed-out"}` {
		t.Fatalf("status = %s, %v", out, err)
	}
}

// End to end through the real gateway: the stored token has expired, so
// mcp_connect refreshes it first and the MCP server sees only the new token.
func TestGatewayConnectRefreshesExpiredStoredToken(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	mcp.SetOAuthTokenSource(mcp.OAuthTokenSource{AccessToken: AccessToken, RefreshAfterUnauthorized: RefreshAfterUnauthorized})
	t.Cleanup(func() { mcp.SetOAuthTokenSource(mcp.OAuthTokenSource{}) })
	storeLogin(t, f, "linear", Tokens{AccessToken: "expired", RefreshToken: "rt-0", ExpiresAt: time.Now().Add(-time.Minute)})
	f.acceptBearer = "at-1"

	args, _ := json.Marshal(map[string]any{
		"serverId": "linear", "transport": "http", "url": f.mcpURL(), "auth": "oauth",
		"headers": map[string]string{"Authorization": "Bearer pasted"},
	})
	if _, err := core.Dispatch("mcp_connect", string(args)); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _, _ = core.Dispatch("mcp_disconnect", `{"serverId":"linear"}`) })
	for _, auth := range f.mcpAuth {
		if auth != "Bearer at-1" {
			t.Fatalf("MCP server saw %v", f.mcpAuth)
		}
	}
	if len(f.forms()) != 1 || f.forms()[0].Get("grant_type") != "refresh_token" {
		t.Fatalf("token requests = %v", f.forms())
	}
}
