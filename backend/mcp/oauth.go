package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// authOAuth is the connectArgs.Auth value for remote connectors that sign in
// through the host (mcpauth). Any other value keeps the configured headers.
const authOAuth = "oauth"

// OAuthTokenSource supplies bearer tokens for connectors marked auth "oauth".
// The backend package wires mcpauth in at start-up; this package never imports
// mcpauth so it stays free of the token store and browser dependencies, and
// tests can install a fake.
type OAuthTokenSource struct {
	// AccessToken returns a usable access token for serverID + url, refreshing
	// it first when it is about to expire. It returns an error containing
	// [MCP_AUTH_REQUIRED] when the connector is not (or no longer) signed in.
	AccessToken func(ctx context.Context, serverID, url string) (string, error)
	// RefreshAfterUnauthorized is called once after the server answered 401
	// to the rejected token. It returns a new token or [MCP_AUTH_REQUIRED].
	RefreshAfterUnauthorized func(ctx context.Context, serverID, url, rejected string) (string, error)
}

var (
	oauthMu     sync.RWMutex
	oauthSource OAuthTokenSource
)

// SetOAuthTokenSource installs the token source used by oauth connectors.
func SetOAuthTokenSource(source OAuthTokenSource) {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	oauthSource = source
}

func currentOAuthSource() (OAuthTokenSource, error) {
	oauthMu.RLock()
	source := oauthSource
	oauthMu.RUnlock()
	if source.AccessToken == nil || source.RefreshAfterUnauthorized == nil {
		return OAuthTokenSource{}, fmt.Errorf("[MCP_AUTH_UNSUPPORTED] 当前环境不支持连接器登录")
	}
	return source, nil
}

func authRequiredError(serverID string) error {
	return fmt.Errorf("[MCP_AUTH_REQUIRED] [mcp:%s] 连接器需要登录", serverID)
}

// hasBearerChallenge reports whether any WWW-Authenticate value offers the
// Bearer scheme (RFC 6750 §3), alone or among other challenges.
func hasBearerChallenge(values []string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			fields := strings.Fields(part)
			if len(fields) == 0 {
				continue
			}
			scheme := fields[0]
			if strings.Contains(scheme, "=") {
				continue // an auth-param of the previous challenge
			}
			if strings.EqualFold(scheme, "bearer") {
				return true
			}
		}
	}
	return false
}
