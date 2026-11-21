package mcpauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// browserCallback simulates the user approving in the browser: it checks the
// authorize URL and GETs the loopback redirect with the code and state.
func browserCallback(t *testing.T, code string, seen *url.Values) func(string) error {
	t.Helper()
	return func(authorizeURL string) error {
		parsed, err := url.Parse(authorizeURL)
		if err != nil {
			t.Fatalf("authorize URL: %v", err)
		}
		query := parsed.Query()
		*seen = query
		redirect := query.Get("redirect_uri")
		resp, err := http.Get(redirect + "?" + url.Values{"code": {code}, "state": {query.Get("state")}}.Encode())
		if err != nil {
			t.Errorf("callback: %v", err)
			return nil
		}
		resp.Body.Close()
		return nil
	}
}

func noDeviceCode(t *testing.T) func(string, string) {
	return func(string, string) { t.Error("code flow must not report a device code") }
}

func TestLoginWithDynamicRegistration(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	var authorize url.Values

	err := Login(context.Background(), "notion", f.mcpURL(), browserCallback(t, "the-code", &authorize), noDeviceCode(t))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// The unauthenticated probe found the challenge; it carried no credentials.
	if len(f.probes) == 0 {
		t.Fatal("Login must probe the connector for its Bearer challenge")
	}
	// Registration: public client bound to the loopback redirect.
	if len(f.registrations) != 1 {
		t.Fatalf("registrations = %d", len(f.registrations))
	}
	redirect := authorize.Get("redirect_uri")
	if err := checkLoopbackRedirect(redirect); err != nil {
		t.Fatalf("redirect_uri %q: %v", redirect, err)
	}
	if uris, _ := f.registrations[0]["redirect_uris"].([]any); len(uris) != 1 || uris[0] != redirect {
		t.Fatalf("registered redirect_uris = %v, want [%s]", f.registrations[0]["redirect_uris"], redirect)
	}
	// Authorize URL: every parameter the MCP authorization spec requires,
	// on top of the endpoint's own query.
	for key, want := range map[string]string{
		"response_type":         "code",
		"client_id":             "dyn-1",
		"code_challenge_method": "S256",
		"scope":                 "mcp:read mcp:write",
		"resource":              f.mcpURL(),
		"prompt":                "consent",
	} {
		if got := authorize.Get(key); got != want {
			t.Fatalf("authorize %s = %q, want %q (%v)", key, got, want, authorize)
		}
	}
	state := authorize.Get("state")
	if raw, err := base64.RawURLEncoding.DecodeString(state); err != nil || len(raw) < 32 {
		t.Fatalf("state %q is not 256 random bits", state)
	}
	// Token request: the code, its PKCE verifier and the resource.
	forms := f.forms()
	if len(forms) != 1 {
		t.Fatalf("token requests = %d", len(forms))
	}
	form := forms[0]
	sum := sha256.Sum256([]byte(form.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != authorize.Get("code_challenge") {
		t.Fatal("code_verifier does not match the code_challenge")
	}
	for key, want := range map[string]string{"grant_type": "authorization_code", "code": "the-code", "client_id": "dyn-1", "redirect_uri": redirect, "resource": f.mcpURL()} {
		if form.Get(key) != want {
			t.Fatalf("token form %s = %q, want %q", key, form.Get(key), want)
		}
	}

	record, ok, err := tokenStore.Get("notion", f.mcpURL())
	if err != nil || !ok {
		t.Fatalf("stored record: %v %v", ok, err)
	}
	if record.Tokens.AccessToken != "at-1" || record.Client.ID != "dyn-1" || record.Client.RedirectURI != redirect || record.NeedsLogin {
		t.Fatalf("record = %+v", record)
	}
	if status, _ := Status("notion", f.mcpURL()); status != StatusSignedIn {
		t.Fatalf("status = %q", status)
	}
	if token, err := AccessToken(context.Background(), "notion", f.mcpURL()); err != nil || token != "at-1" {
		t.Fatalf("AccessToken = %q, %v", token, err)
	}
}

func TestLoginReRegistersWhenRedirectURIChanged(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	stale := Record{
		ServerID: "notion", URL: f.mcpURL(),
		Metadata: Metadata{Issuer: f.base + "/as", RegistrationEndpoint: f.base + "/as/register", TokenEndpoint: f.base + "/as/token"},
		Client:   Client{ID: "old-client", Public: true, RedirectURI: "http://127.0.0.1:1/callback"},
		Tokens:   Tokens{AccessToken: "old"},
	}
	if err := tokenStore.Put(stale); err != nil {
		t.Fatal(err)
	}
	var authorize url.Values
	if err := Login(context.Background(), "notion", f.mcpURL(), browserCallback(t, "c", &authorize), noDeviceCode(t)); err != nil {
		t.Fatal(err)
	}
	if len(f.registrations) != 1 || authorize.Get("client_id") != "dyn-1" {
		t.Fatalf("a client bound to another redirect URI must not be reused (registrations %d, client %q)", len(f.registrations), authorize.Get("client_id"))
	}

	// The same redirect URI, issuer and registration endpoint may reuse it.
	md := stale.Metadata
	if _, ok := reusableClient(stale, true, md, "http://127.0.0.1:1/callback"); !ok {
		t.Fatal("same redirect URI should reuse the registered client")
	}
	for name, tc := range map[string]struct {
		record   Record
		md       Metadata
		redirect string
	}{
		"other redirect": {stale, md, "http://127.0.0.1:2/callback"},
		"other issuer":   {stale, Metadata{Issuer: "https://evil.example", RegistrationEndpoint: md.RegistrationEndpoint}, "http://127.0.0.1:1/callback"},
		"device client":  {Record{Metadata: md, Client: Client{ID: "x", RedirectURI: "http://127.0.0.1:1/callback", Device: true}}, md, "http://127.0.0.1:1/callback"},
		"no client id":   {Record{Metadata: md, Client: Client{RedirectURI: "http://127.0.0.1:1/callback"}}, md, "http://127.0.0.1:1/callback"},
	} {
		if _, ok := reusableClient(tc.record, true, tc.md, tc.redirect); ok {
			t.Fatalf("%s: client must be re-registered", name)
		}
	}
}

func TestLoginWithoutRegistrationOrPreregistrationIsUnsupported(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	f.registration = false
	useFixtureClient(t, f)
	opened := false
	err := Login(context.Background(), "x", f.mcpURL(), func(string) error { opened = true; return nil }, noDeviceCode(t))
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_UNSUPPORTED]") {
		t.Fatalf("err = %v", err)
	}
	if opened || len(f.forms()) != 0 {
		t.Fatal("nothing may be opened or exchanged")
	}
	if _, ok, _ := tokenStore.Get("x", f.mcpURL()); ok {
		t.Fatal("nothing may be stored")
	}
}

func TestLoginRefusesServerWithoutS256(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	f.s256 = false
	useFixtureClient(t, f)
	opened := false
	err := Login(context.Background(), "x", f.mcpURL(), func(string) error { opened = true; return nil }, noDeviceCode(t))
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_UNSUPPORTED]") {
		t.Fatalf("err = %v", err)
	}
	if opened || len(f.registrations) != 0 {
		t.Fatalf("refuse before registering or opening the browser (registrations %d)", len(f.registrations))
	}
}

func TestLoginPreregisteredClientWithoutIDIsUnsupported(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, true)
	f.registration = false
	f.device = true
	useFixtureClient(t, f)
	trustPreregistered(t, f, Client{ID: "", Public: true, Device: true})
	err := Login(context.Background(), "gh", f.mcpURL(), func(string) error { t.Error("must not open"); return nil },
		func(string, string) { t.Error("must not start device flow") })
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_UNSUPPORTED]") {
		t.Fatalf("err = %v", err)
	}
	if len(f.deviceForms) != 0 {
		t.Fatal("no device authorization request without a client ID")
	}

	// The built-in GitHub entry (ruling R7) is such a placeholder today.
	_, _, err = chooseClient(Metadata{Issuer: githubIssuer, Resource: "https://api.githubcopilot.com/mcp/"})
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_UNSUPPORTED]") {
		t.Fatalf("GitHub placeholder: err = %v", err)
	}
}

func TestChooseClientOnlyUsesPreregisteredForItsResource(t *testing.T) {
	previous, had := Preregistered["https://as.example"]
	Preregistered["https://as.example"] = Client{ID: "mincode-app", Public: true}
	t.Cleanup(func() {
		if had {
			Preregistered["https://as.example"] = previous
		} else {
			delete(Preregistered, "https://as.example")
		}
	})
	// No trusted origin for this issuer: a hostile connector naming it must
	// not obtain MinCode's client.
	md := Metadata{Issuer: "https://as.example", Resource: "https://evil.example/mcp"}
	if _, _, err := chooseClient(md); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_UNSUPPORTED]") {
		t.Fatalf("err = %v", err)
	}
	md.RegistrationEndpoint = "https://as.example/register"
	client, dynamic, err := chooseClient(md)
	if err != nil || !dynamic || client.ID != "" {
		t.Fatalf("fall back to dynamic registration, got %+v %v %v", client, dynamic, err)
	}
	// GitHub's issuer with a foreign resource is never served the GitHub client.
	if _, _, err := chooseClient(Metadata{Issuer: githubIssuer, Resource: "https://evil.example/mcp"}); err == nil {
		t.Fatal("GitHub client for a foreign resource")
	}
}

func TestLoginBrowserFailureAndTimeout(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	err := Login(context.Background(), "x", f.mcpURL(), func(string) error { return errors.New("no browser") }, noDeviceCode(t))
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_BROWSER]") {
		t.Fatalf("browser failure: %v", err)
	}

	shortLoginTimeout(t, 200*time.Millisecond)
	err = Login(context.Background(), "x", f.mcpURL(), func(string) error { return nil }, noDeviceCode(t))
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_TIMEOUT]") {
		t.Fatalf("timeout: %v", err)
	}
	if _, ok, _ := tokenStore.Get("x", f.mcpURL()); ok {
		t.Fatal("nothing may be stored")
	}
}

func TestLoginErrorsDoNotLeakSecrets(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	f.tokenReply = func(w http.ResponseWriter, form url.Values) {
		writeJSON(t, w, 400, map[string]any{"error": "invalid_grant", "error_description": "bad code " + form.Get("code") + " verifier " + form.Get("code_verifier")})
	}
	var authorize url.Values
	err := Login(context.Background(), "x", f.mcpURL(), browserCallback(t, "secret-code-value", &authorize), noDeviceCode(t))
	if err == nil {
		t.Fatal("expected failure")
	}
	verifier := f.forms()[0].Get("code_verifier")
	for _, secret := range []string{"secret-code-value", verifier, authorize.Get("state")} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks a secret: %v", err)
		}
	}
}

func TestLogoutCancelsLoginAndDeletesRecord(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	if err := tokenStore.Put(Record{ServerID: "x", URL: "https://other.example/mcp", Tokens: Tokens{AccessToken: "a"}}); err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Login(context.Background(), "x", f.mcpURL(), func(string) error { close(opened); return nil }, noDeviceCode(t))
	}()
	<-opened
	if err := Logout("x"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_CANCELLED]") {
		t.Fatalf("login after logout: %v", err)
	}
	if _, ok, _ := tokenStore.Get("x", "https://other.example/mcp"); ok {
		t.Fatal("logout must delete the stored login")
	}
}

func TestSecondLoginCancelsTheFirst(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	opened := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- Login(context.Background(), "x", f.mcpURL(), func(string) error { close(opened); return nil }, noDeviceCode(t))
	}()
	<-opened
	var authorize url.Values
	if err := Login(context.Background(), "x", f.mcpURL(), browserCallback(t, "c", &authorize), noDeviceCode(t)); err != nil {
		t.Fatal(err)
	}
	if err := <-first; err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_CANCELLED]") {
		t.Fatalf("first login: %v", err)
	}
	if status, _ := Status("x", f.mcpURL()); status != StatusSignedIn {
		t.Fatalf("status = %q", status)
	}
}

func storeLogin(t *testing.T, f *authFixture, serverID string, tokens Tokens) Record {
	t.Helper()
	record := Record{
		ServerID: serverID, URL: f.mcpURL(),
		Metadata: Metadata{Issuer: f.base + "/as", Resource: f.mcpURL(), TokenEndpoint: f.base + "/as/token"},
		Client:   Client{ID: "dyn-9", Public: true, RedirectURI: "http://127.0.0.1:1/callback"},
		Tokens:   tokens,
	}
	if err := tokenStore.Put(record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestAccessTokenRefreshesWhenLessThanAMinuteRemains(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fixedNow(t, at)

	if _, err := AccessToken(context.Background(), "x", f.mcpURL()); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") {
		t.Fatalf("signed out: %v", err)
	}

	storeLogin(t, f, "x", Tokens{AccessToken: "valid", RefreshToken: "rt-0", ExpiresAt: at.Add(61 * time.Second)})
	if token, err := AccessToken(context.Background(), "x", f.mcpURL()); err != nil || token != "valid" {
		t.Fatalf("valid token = %q, %v", token, err)
	}
	if len(f.forms()) != 0 {
		t.Fatal("no refresh while more than 60 s remain")
	}

	storeLogin(t, f, "x", Tokens{AccessToken: "expiring", RefreshToken: "rt-0", ExpiresAt: at.Add(59 * time.Second)})
	token, err := AccessToken(context.Background(), "x", f.mcpURL())
	if err != nil || token != "at-1" {
		t.Fatalf("refreshed token = %q, %v", token, err)
	}
	form := f.forms()[0]
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "rt-0" || form.Get("client_id") != "dyn-9" || form.Get("resource") != f.mcpURL() {
		t.Fatalf("refresh form = %v", form)
	}
	record, _, _ := tokenStore.Get("x", f.mcpURL())
	if record.Tokens.AccessToken != "at-1" || record.Tokens.RefreshToken != "rt-1" {
		t.Fatalf("refreshed tokens not stored: %+v", record.Tokens)
	}

	// Tokens without an expiry are used as-is until the server rejects them.
	storeLogin(t, f, "x", Tokens{AccessToken: "forever"})
	if token, err := AccessToken(context.Background(), "x", f.mcpURL()); err != nil || token != "forever" {
		t.Fatalf("no-expiry token = %q, %v", token, err)
	}
	// A different URL for the same server ID reads as signed out (R2).
	if _, err := AccessToken(context.Background(), "x", f.mcpURL()+"/other"); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") {
		t.Fatalf("other URL: %v", err)
	}
}

func TestRefreshFailureMarksNeedsLogin(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	f.tokenReply = func(w http.ResponseWriter, _ url.Values) {
		writeJSON(t, w, 400, map[string]any{"error": "invalid_grant"})
	}
	storeLogin(t, f, "x", Tokens{AccessToken: "expired", RefreshToken: "rt-0", ExpiresAt: time.Now().Add(-time.Minute)})

	_, err := AccessToken(context.Background(), "x", f.mcpURL())
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") {
		t.Fatalf("err = %v", err)
	}
	record, ok, _ := tokenStore.Get("x", f.mcpURL())
	if !ok || !record.NeedsLogin || record.Tokens.RefreshToken != "rt-0" {
		t.Fatalf("record must be kept and marked: ok=%v %+v", ok, record)
	}
	out, err := statusHandler("", `{"serverId":"x","url":"`+f.mcpURL()+`"}`)
	if err != nil || out != `{"status":"needs-login"}` {
		t.Fatalf("mcp_oauth_status = %s, %v", out, err)
	}
	// Once marked, no further refresh attempts until the user signs in again.
	if _, err := AccessToken(context.Background(), "x", f.mcpURL()); err == nil || len(f.forms()) != 1 {
		t.Fatalf("marked login must not refresh again (requests %d, err %v)", len(f.forms()), err)
	}
}

func TestRefreshNetworkFailureKeepsLogin(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	record := storeLogin(t, f, "x", Tokens{AccessToken: "expiring", RefreshToken: "rt-0", ExpiresAt: time.Now().Add(30 * time.Second)})
	record.Metadata.TokenEndpoint = "http://127.0.0.1:1/token" // nothing listens there
	if err := tokenStore.Put(record); err != nil {
		t.Fatal(err)
	}
	// Still valid for 30 s: use it rather than fail.
	if token, err := AccessToken(context.Background(), "x", f.mcpURL()); err != nil || token != "expiring" {
		t.Fatalf("token = %q, %v", token, err)
	}
	record.Tokens.ExpiresAt = time.Now().Add(-time.Second)
	if err := tokenStore.Put(record); err != nil {
		t.Fatal(err)
	}
	_, err := AccessToken(context.Background(), "x", f.mcpURL())
	if err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_NETWORK]") {
		t.Fatalf("expired + unreachable: %v", err)
	}
	if status, _ := Status("x", f.mcpURL()); status != StatusSignedIn {
		t.Fatalf("a network failure must not force a new login, status %q", status)
	}
}

func TestRefreshAfterUnauthorized(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	storeLogin(t, f, "x", Tokens{AccessToken: "rejected", RefreshToken: "rt-0", ExpiresAt: time.Now().Add(time.Hour)})

	token, err := RefreshAfterUnauthorized(context.Background(), "x", f.mcpURL(), "rejected")
	if err != nil || token != "at-1" {
		t.Fatalf("token = %q, %v", token, err)
	}
	// A concurrent request that was rejected with the old token gets the
	// already-refreshed one without a second refresh.
	token, err = RefreshAfterUnauthorized(context.Background(), "x", f.mcpURL(), "rejected")
	if err != nil || token != "at-1" || len(f.forms()) != 1 {
		t.Fatalf("token = %q, %v, refreshes %d", token, err, len(f.forms()))
	}

	// Without a refresh token (GitHub OAuth App tokens) a 401 means sign in again.
	storeLogin(t, f, "y", Tokens{AccessToken: "gh-token"})
	if _, err := RefreshAfterUnauthorized(context.Background(), "y", f.mcpURL(), "gh-token"); err == nil || !strings.Contains(err.Error(), "[MCP_AUTH_REQUIRED]") {
		t.Fatalf("err = %v", err)
	}
	if status, _ := Status("y", f.mcpURL()); status != StatusNeedsLogin {
		t.Fatalf("status = %q", status)
	}
}

func TestConcurrentAccessTokensRefreshOnce(t *testing.T) {
	tempStorage(t)
	f := newAuthFixture(t, false)
	useFixtureClient(t, f)
	storeLogin(t, f, "x", Tokens{AccessToken: "old", RefreshToken: "rt-0", ExpiresAt: time.Now().Add(-time.Second)})
	var wg sync.WaitGroup
	tokens := make([]string, 5)
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], _ = AccessToken(context.Background(), "x", f.mcpURL())
		}()
	}
	wg.Wait()
	for _, token := range tokens {
		if token != "at-1" {
			t.Fatalf("tokens = %v", tokens)
		}
	}
	if len(f.forms()) != 1 {
		t.Fatalf("refreshes = %d, want 1 (rotating refresh tokens break on a second use)", len(f.forms()))
	}
}
