package mcpauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Connector login status values reported by Status and mcp_oauth_status.
const (
	StatusSignedOut  = "signed-out"
	StatusSignedIn   = "signed-in"
	StatusNeedsLogin = "needs-login"
)

// refreshSkew is how close to expiry an access token is refreshed first.
const refreshSkew = 60 * time.Second

var (
	// loginTimeout bounds a whole Login (discovery, browser or device
	// approval, token exchange). Tests shorten it.
	loginTimeout = 5 * time.Minute
	// tokenStore holds connector logins; tests use a temporary storage dir.
	tokenStore = NewKVStore()
	// httpClient is the base client for every OAuth request (nil uses the
	// defaults in secureClient). Tests point it at httptest servers.
	httpClient *http.Client
)

// Login signs serverID in to the remote MCP server at serverURL and stores the
// tokens for that serverID + URL pair:
//
//  1. probe the connector without credentials for its Bearer challenge and
//     discover the authorization server (RFC 9728 / RFC 8414);
//  2. take the preregistered client bound to this resource (PreregisteredFor),
//     otherwise register dynamically (RFC 7591); neither is
//     [MCP_AUTH_UNSUPPORTED];
//  3. device clients use the device authorization grant (RFC 8628), reporting
//     the user code through onDeviceCode; all others use the authorization
//     code grant with PKCE S256, state and resource through open and the
//     loopback redirect (RFC 8252).
//
// The whole login is bounded by loginTimeout. Starting another login for the
// same serverID, or Logout, cancels this one ([MCP_AUTH_CANCELLED]).
func Login(ctx context.Context, serverID, serverURL string, open func(url string) error, onDeviceCode func(userCode, verifyURL string)) error {
	if err := validateServerID(serverID); err != nil {
		return err
	}
	if _, err := checkURL(serverURL, "连接器"); err != nil {
		return err
	}
	if open == nil {
		return authError("MCP_AUTH_BROWSER", "无法打开系统浏览器")
	}
	ctx, cancelTimeout := context.WithTimeout(ctx, loginTimeout)
	defer cancelTimeout()
	ctx, end := beginLogin(ctx, serverID)
	defer end()

	md, err := Discover(ctx, httpClient, serverURL, probeChallenge(ctx, serverURL))
	if err != nil {
		return err
	}
	client, dynamic, err := chooseClient(md)
	if err != nil {
		return err
	}
	var tokens Tokens
	if client.Device {
		tokens, err = deviceLogin(ctx, md, client, open, onDeviceCode)
	} else {
		previous, hasPrevious, getErr := tokenStore.Get(serverID, serverURL)
		if getErr != nil {
			return getErr
		}
		client, tokens, err = codeLogin(ctx, md, client, dynamic, previous, hasPrevious, open)
	}
	if err != nil {
		return err
	}

	unlock := lockServer(serverID)
	defer unlock()
	// Logout cancels the login before taking this lock, so a login it
	// cancelled never writes after the delete.
	if err := ctx.Err(); err != nil {
		return contextError(err, "登录已取消")
	}
	return tokenStore.Put(Record{ServerID: serverID, URL: serverURL, Metadata: md, Client: client, Tokens: tokens})
}

// chooseClient returns the preregistered client for md, or dynamic=true when
// the authorization server supports registration. Preregistered clients are
// only ever looked up through PreregisteredFor, which binds them to their
// resource origins.
func chooseClient(md Metadata) (client Client, dynamic bool, err error) {
	if pre, err := PreregisteredFor(md); err == nil {
		if pre.ID == "" {
			// Ruling R7: the OAuth App is not registered yet.
			return Client{}, false, authError("MCP_AUTH_UNSUPPORTED", "MinCode 暂不支持登录这个连接器，请改用个人访问令牌")
		}
		return pre, false, nil
	}
	if md.RegistrationEndpoint == "" {
		return Client{}, false, authError("MCP_AUTH_UNSUPPORTED", "授权服务器不支持动态客户端注册，请改用手动填写令牌")
	}
	return Client{}, true, nil
}

// codeLogin runs the authorization code grant with PKCE through the loopback
// redirect and returns the client actually used (dynamic clients are bound to
// this login's redirect URI) and the tokens.
func codeLogin(ctx context.Context, md Metadata, client Client, dynamic bool, previous Record, hasPrevious bool, open func(string) error) (Client, Tokens, error) {
	if !md.CodeChallengeS256 {
		return Client{}, Tokens{}, authError("MCP_AUTH_UNSUPPORTED", "授权服务器不支持 PKCE（S256），无法安全登录")
	}
	if _, err := checkURL(md.AuthorizationEndpoint, "授权接口"); err != nil {
		return Client{}, Tokens{}, err
	}
	state := randomToken()
	loopback, redirectURI, err := StartLoopback(state)
	if err != nil {
		return Client{}, Tokens{}, err
	}
	defer loopback.Close()

	if dynamic {
		if reused, ok := reusableClient(previous, hasPrevious, md, redirectURI); ok {
			client = reused
		} else if client, err = Register(ctx, httpClient, md, redirectURI); err != nil {
			return Client{}, Tokens{}, err
		}
	}
	verifier, challenge := NewPKCE()
	authorizeURL, err := buildAuthorizeURL(md, client, redirectURI, challenge, state)
	if err != nil {
		return Client{}, Tokens{}, err
	}
	if err := open(authorizeURL); err != nil {
		return Client{}, Tokens{}, authError("MCP_AUTH_BROWSER", "无法打开系统浏览器")
	}
	code, err := loopback.Wait(ctx)
	if err != nil {
		return Client{}, Tokens{}, err
	}
	tokens, err := ExchangeCode(ctx, httpClient, md, client, code, verifier, redirectURI)
	if err != nil {
		return Client{}, Tokens{}, err
	}
	return client, tokens, nil
}

// reusableClient returns the previously registered dynamic client only when it
// was registered at the same authorization server for exactly this redirect
// URI. Dynamic clients are bound to one loopback redirect URI, and the
// loopback port changes on every login, so in practice this re-registers.
func reusableClient(previous Record, hasPrevious bool, md Metadata, redirectURI string) (Client, bool) {
	c := previous.Client
	if !hasPrevious || c.ID == "" || c.Device || c.RedirectURI == "" || c.RedirectURI != redirectURI ||
		md.RegistrationEndpoint == "" || previous.Metadata.RegistrationEndpoint != md.RegistrationEndpoint ||
		normalizeIssuer(previous.Metadata.Issuer) != normalizeIssuer(md.Issuer) {
		return Client{}, false
	}
	return c, true
}

func buildAuthorizeURL(md Metadata, client Client, redirectURI, challenge, state string) (string, error) {
	endpoint, err := checkURL(md.AuthorizationEndpoint, "授权接口")
	if err != nil {
		return "", err
	}
	query := endpoint.Query()
	query.Set("response_type", "code")
	query.Set("client_id", client.ID)
	query.Set("redirect_uri", redirectURI)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	if len(md.Scopes) > 0 {
		query.Set("scope", strings.Join(md.Scopes, " "))
	}
	if md.Resource != "" {
		query.Set("resource", md.Resource)
	}
	endpoint.RawQuery = query.Encode()
	endpoint.Fragment = ""
	return endpoint.String(), nil
}

// probeInitialize is an unauthenticated MCP initialize request; the 401 it
// draws carries the Bearer challenge with resource_metadata.
const probeInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"MinCode","version":"1.0.0"}}}`

// probeChallenge returns the WWW-Authenticate value of the connector's 401, or
// "" when there is none (Discover then falls back to the well-known URL).
func probeChallenge(ctx context.Context, serverURL string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(probeInitialize))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := secureClient(httpClient, false).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode != http.StatusUnauthorized {
		return ""
	}
	return strings.Join(resp.Header.Values("WWW-Authenticate"), ", ")
}

// AccessToken returns a usable access token for serverID + serverURL. A
// missing login, a login for another URL or one marked NeedsLogin is
// [MCP_AUTH_REQUIRED]. With less than refreshSkew left the token is refreshed
// first; a rejected refresh marks the login NeedsLogin (the record is kept)
// and is [MCP_AUTH_REQUIRED].
func AccessToken(ctx context.Context, serverID, serverURL string) (string, error) {
	unlock := lockServer(serverID)
	defer unlock()
	record, err := signedInRecord(serverID, serverURL)
	if err != nil {
		return "", err
	}
	expiresAt := record.Tokens.ExpiresAt
	if expiresAt.IsZero() || expiresAt.Sub(now()) >= refreshSkew {
		return record.Tokens.AccessToken, nil
	}
	token, err := refreshLocked(ctx, record)
	if err != nil && isTransient(err) && now().Before(expiresAt) {
		// The refresh did not get through (network); the current token is
		// still valid for a few seconds.
		return record.Tokens.AccessToken, nil
	}
	return token, err
}

// RefreshAfterUnauthorized is called after the MCP server answered 401 to
// rejected. It refreshes once; when another request already replaced the
// rejected token, the stored token is returned without a second refresh.
func RefreshAfterUnauthorized(ctx context.Context, serverID, serverURL, rejected string) (string, error) {
	unlock := lockServer(serverID)
	defer unlock()
	record, err := signedInRecord(serverID, serverURL)
	if err != nil {
		return "", err
	}
	if record.Tokens.AccessToken != rejected {
		return record.Tokens.AccessToken, nil
	}
	return refreshLocked(ctx, record)
}

func signedInRecord(serverID, serverURL string) (Record, error) {
	record, ok, err := tokenStore.Get(serverID, serverURL)
	if err != nil {
		return Record{}, err
	}
	if !ok || record.NeedsLogin || record.Tokens.AccessToken == "" {
		return Record{}, errAuthRequired()
	}
	return record, nil
}

// refreshLocked refreshes record's tokens; the caller holds lockServer.
// Network failures (the server was not reached or did not answer) leave the
// login untouched and are returned as-is. Every other failure means the
// stored grant is no longer usable: the record is kept, marked NeedsLogin,
// and the result is [MCP_AUTH_REQUIRED].
func refreshLocked(ctx context.Context, record Record) (string, error) {
	tokens, err := Refresh(ctx, httpClient, record.Metadata, record.Client, record.Tokens.RefreshToken)
	if err != nil {
		if isTransient(err) {
			return "", err
		}
		record.NeedsLogin = true
		if putErr := tokenStore.Put(record); putErr != nil {
			return "", putErr
		}
		return "", errAuthRequired()
	}
	record.Tokens = tokens
	record.NeedsLogin = false
	if err := tokenStore.Put(record); err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

func errAuthRequired() error {
	return authError("MCP_AUTH_REQUIRED", "连接器需要登录")
}

func isTransient(err error) bool {
	for _, code := range []string{"[MCP_AUTH_NETWORK]", "[MCP_AUTH_TIMEOUT]", "[MCP_AUTH_CANCELLED]"} {
		if strings.HasPrefix(err.Error(), code) {
			return true
		}
	}
	return false
}

// Status reports the login state of serverID + serverURL without network
// access: no login (or one for another URL) is signed-out.
func Status(serverID, serverURL string) (string, error) {
	record, ok, err := tokenStore.Get(serverID, serverURL)
	switch {
	case err != nil:
		return "", err
	case !ok || record.Tokens.AccessToken == "":
		return StatusSignedOut, nil
	case record.NeedsLogin:
		return StatusNeedsLogin, nil
	default:
		return StatusSignedIn, nil
	}
}

// Logout cancels a login in progress for serverID and deletes its stored
// tokens and client on this device.
func Logout(serverID string) error {
	if err := validateServerID(serverID); err != nil {
		return err
	}
	cancelLogin(serverID)
	unlock := lockServer(serverID)
	defer unlock()
	return tokenStore.Delete(serverID)
}

func contextError(err error, message string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return authError("MCP_AUTH_TIMEOUT", "登录超时")
	}
	return authError("MCP_AUTH_CANCELLED", "%s", message)
}

// ─── in-flight logins and per-server locks ──────────────────────────────────

type inflightLogin struct{ cancel context.CancelFunc }

var (
	loginsMu sync.Mutex
	logins   = map[string]*inflightLogin{}
	// serverLocks serialises token refresh and the final write of a login per
	// server ID, so rotating refresh tokens are never redeemed twice.
	serverLocks sync.Map
)

// beginLogin registers a cancellable login for serverID, cancelling the one
// already running. end must be called when the login finishes.
func beginLogin(parent context.Context, serverID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	entry := &inflightLogin{cancel: cancel}
	loginsMu.Lock()
	if previous := logins[serverID]; previous != nil {
		previous.cancel()
	}
	logins[serverID] = entry
	loginsMu.Unlock()
	return ctx, func() {
		loginsMu.Lock()
		if logins[serverID] == entry {
			delete(logins, serverID)
		}
		loginsMu.Unlock()
		cancel()
	}
}

func cancelLogin(serverID string) {
	loginsMu.Lock()
	if entry := logins[serverID]; entry != nil {
		entry.cancel()
		delete(logins, serverID)
	}
	loginsMu.Unlock()
}

func lockServer(serverID string) func() {
	value, _ := serverLocks.LoadOrStore(serverID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
