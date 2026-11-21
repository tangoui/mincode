package mcpauth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func deviceFixture(t *testing.T) *authFixture {
	t.Helper()
	tempStorage(t)
	fastDevicePolling(t)
	f := newAuthFixture(t, true)
	f.registration = false
	f.s256 = false // device flow does not need PKCE support
	f.device = true
	useFixtureClient(t, f)
	trustPreregistered(t, f, Client{ID: "mincode-device-app", Public: true, Device: true})
	return f
}

// pollReplies answers device-code token polls with the given OAuth error codes
// (alternating HTTP 400 and GitHub-style HTTP 200), then issues tokens.
func pollReplies(t *testing.T, f *authFixture, codes ...string) {
	t.Helper()
	polls := 0
	f.tokenReply = func(w http.ResponseWriter, form url.Values) {
		if form.Get("grant_type") != deviceGrantType {
			t.Errorf("grant_type = %q", form.Get("grant_type"))
		}
		if polls < len(codes) {
			status := http.StatusBadRequest
			if polls%2 == 1 {
				status = http.StatusOK
			}
			code := codes[polls]
			polls++
			writeJSON(t, w, status, map[string]any{"error": code})
			return
		}
		f.issue(w)
	}
}

func TestLoginUsesDeviceFlowForPreregisteredDeviceClient(t *testing.T) {
	f := deviceFixture(t)
	pollReplies(t, f, "authorization_pending", "authorization_pending")
	var userCode, verifyURL, opened string
	err := Login(context.Background(), "github", f.mcpURL(),
		func(u string) error { opened = u; return nil },
		func(code, uri string) { userCode, verifyURL = code, uri })
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if userCode != "WDJB-MJHT" || verifyURL != f.base+"/login/device" || opened != verifyURL {
		t.Fatalf("user code %q, verify %q, opened %q", userCode, verifyURL, opened)
	}
	device := f.deviceForms[0]
	if device.Get("client_id") != "mincode-device-app" || device.Get("scope") != "mcp:read mcp:write" {
		t.Fatalf("device form = %v", device)
	}
	forms := f.forms()
	if len(forms) != 3 {
		t.Fatalf("polls = %d, want 3", len(forms))
	}
	for _, form := range forms {
		if form.Get("device_code") != "device-code-secret" || form.Get("client_id") != "mincode-device-app" {
			t.Fatalf("poll form = %v", form)
		}
	}
	record, ok, _ := tokenStore.Get("github", f.mcpURL())
	if !ok || record.Tokens.AccessToken != "at-1" || !record.Client.Device || record.Client.ID != "mincode-device-app" {
		t.Fatalf("record = %+v", record)
	}
	if len(f.registrations) != 0 {
		t.Fatal("preregistered client must not be registered dynamically")
	}
}

func TestDeviceFlowSlowDownAddsFiveSeconds(t *testing.T) {
	f := deviceFixture(t)
	pollReplies(t, f, "slow_down", "authorization_pending")
	var waits []time.Duration
	previous := deviceSleep
	deviceSleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return previous(ctx, d)
	}
	t.Cleanup(func() { deviceSleep = previous })
	if err := Login(context.Background(), "github", f.mcpURL(), func(string) error { return nil }, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	unit := devicePollUnit
	want := []time.Duration{1 * unit, 6 * unit, 6 * unit}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	}
}

func TestDeviceFlowEnds(t *testing.T) {
	for code, want := range map[string]string{
		"expired_token":  "[MCP_AUTH_TIMEOUT]",
		"access_denied":  "[MCP_AUTH_DENIED]",
		"invalid_client": "[MCP_AUTH_TOKEN_FAILED]",
	} {
		t.Run(code, func(t *testing.T) {
			f := deviceFixture(t)
			pollReplies(t, f, "authorization_pending", code, "authorization_pending")
			err := Login(context.Background(), "github", f.mcpURL(), func(string) error { return nil }, func(string, string) {})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want %s", err, want)
			}
			if len(f.forms()) != 2 {
				t.Fatalf("polling must stop at %s (polls %d)", code, len(f.forms()))
			}
			if strings.Contains(err.Error(), "device-code-secret") {
				t.Fatalf("error leaks the device code: %v", err)
			}
			if _, ok, _ := tokenStore.Get("github", f.mcpURL()); ok {
				t.Fatal("nothing may be stored")
			}
		})
	}
}

func TestDeviceFlowStopsAtOverallTimeout(t *testing.T) {
	f := deviceFixture(t)
	f.tokenReply = func(w http.ResponseWriter, _ url.Values) {
		writeJSON(t, w, 400, map[string]any{"error": "authorization_pending"})
	}
	shortLoginTimeout(t, 150*time.Millisecond)
	start := time.Now()
	err := Login(context.Background(), "github", f.mcpURL(), func(string) error { return nil }, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_TIMEOUT]") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("polling outlived the login timeout")
	}
}

func TestDeviceFlowStopsWhenDeviceCodeExpires(t *testing.T) {
	f := deviceFixture(t)
	f.deviceReply = func(w http.ResponseWriter, _ url.Values) {
		writeJSON(t, w, 200, map[string]any{
			"device_code": "dc", "user_code": "ABCD-EFGH", "verification_uri": f.base + "/login/device",
			"expires_in": 1, "interval": 1,
		})
	}
	f.tokenReply = func(w http.ResponseWriter, _ url.Values) {
		writeJSON(t, w, 400, map[string]any{"error": "authorization_pending"})
	}
	previous := devicePollUnit
	devicePollUnit = 20 * time.Millisecond // expires_in 1 => 20 ms, interval 1 => 20 ms
	t.Cleanup(func() { devicePollUnit = previous })
	err := Login(context.Background(), "github", f.mcpURL(), func(string) error { return nil }, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_TIMEOUT]") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeviceAuthorizationResponseIsValidated(t *testing.T) {
	for name, reply := range map[string]map[string]any{
		"missing device code":  {"user_code": "A", "verification_uri": "https://x.example/d"},
		"control in user code": {"device_code": "d", "user_code": "A\x1b[2J", "verification_uri": "https://x.example/d"},
		"insecure verify uri":  {"device_code": "d", "user_code": "A", "verification_uri": "javascript:alert(1)"},
	} {
		t.Run(name, func(t *testing.T) {
			f := deviceFixture(t)
			f.deviceReply = func(w http.ResponseWriter, _ url.Values) { writeJSON(t, w, 200, reply) }
			called := false
			err := Login(context.Background(), "github", f.mcpURL(), func(string) error { called = true; return nil }, func(string, string) { called = true })
			if err == nil || called {
				t.Fatalf("err = %v, callbacks called = %v", err, called)
			}
		})
	}
}

// A fractional interval ("interval": 0.5) truncates to zero seconds; polling
// must still wait at least one second between requests.
func TestDeviceFlowFractionalIntervalWaitsAtLeastOneSecond(t *testing.T) {
	f := deviceFixture(t)
	f.deviceReply = func(w http.ResponseWriter, _ url.Values) {
		writeJSON(t, w, 200, map[string]any{
			"device_code": "device-code-secret", "user_code": "WDJB-MJHT",
			"verification_uri": f.base + "/login/device", "expires_in": 900, "interval": 0.5,
		})
	}
	pollReplies(t, f, "authorization_pending")
	var waits []time.Duration
	previous := deviceSleep
	deviceSleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return previous(ctx, d)
	}
	t.Cleanup(func() { deviceSleep = previous })
	if err := Login(context.Background(), "github", f.mcpURL(), func(string) error { return nil }, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 || waits[0] != devicePollUnit || waits[1] != devicePollUnit {
		t.Fatalf("waits = %v, want two of %v", waits, devicePollUnit)
	}
}
