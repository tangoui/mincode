package mcpauth

import (
	"net/url"
	"slices"
	"strings"
)

// Client is the OAuth client MinCode presents to an authorization server,
// either registered dynamically (RFC 7591) or taken from Preregistered.
// Secret is only set when a server insists on issuing one; it is stored
// encrypted with the tokens and never leaves the Go backend.
type Client struct {
	ID          string `json:"id"`
	Secret      string `json:"secret,omitempty"`
	Public      bool   `json:"public"`
	RedirectURI string `json:"redirectUri,omitempty"`
	// Device marks clients that must use the OAuth device authorization
	// grant (RFC 8628) instead of the loopback authorization-code flow.
	Device bool `json:"device,omitempty"`
}

const githubIssuer = "https://github.com/login/oauth"

// Preregistered maps an authorization server issuer to a client registered
// out of band, for servers without dynamic client registration. Read it only
// through PreregisteredFor, which also checks that the connector is one the
// client was registered for: a hostile connector can name any issuer in its
// protected-resource metadata, and tokens from servers that ignore RFC 8707
// (GitHub) are not audience-bound.
//
// GitHub (ruling R7): the MinCode OAuth App is not registered yet, so its
// client ID is an empty placeholder. Login must report [MCP_AUTH_UNSUPPORTED]
// while it is empty and the settings page points the user at a personal access
// token. Desktop apps cannot keep a client secret, so GitHub uses device flow.
var Preregistered = map[string]Client{
	githubIssuer: {ID: "", Public: true, Device: true},
}

// trustedResourceOrigins binds built-in authorization servers and
// preregistered clients to the resource origins (scheme://host, default port
// omitted) allowed to use them.
var trustedResourceOrigins = map[string][]string{
	githubIssuer: {"https://api.githubcopilot.com"},
}

// PreregisteredFor returns the preregistered client for md.Issuer when
// md.Resource belongs to an origin that client was registered for. Otherwise it
// returns [MCP_AUTH_UNSUPPORTED]. The client ID may still be an empty
// placeholder (see Preregistered); the caller reports that separately.
func PreregisteredFor(md Metadata) (Client, error) {
	issuer := normalizeIssuer(md.Issuer)
	client, ok := Preregistered[issuer]
	if !ok {
		return Client{}, authError("MCP_AUTH_UNSUPPORTED", "授权服务器没有可用的预注册客户端")
	}
	if !resourceOriginTrusted(issuer, md.Resource) {
		return Client{}, authError("MCP_AUTH_UNSUPPORTED", "预注册客户端不能用于这个连接器")
	}
	return client, nil
}

func normalizeIssuer(issuer string) string {
	return strings.TrimSuffix(issuer, "/")
}

// resourceOriginTrusted reports whether resource is an https URL whose origin
// is listed for issuer in trustedResourceOrigins.
func resourceOriginTrusted(issuer, resource string) bool {
	allowed := trustedResourceOrigins[issuer]
	parsed, err := url.Parse(resource)
	if len(allowed) == 0 || err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" {
		return false
	}
	return slices.Contains(allowed, "https://"+hostKey(parsed))
}

// builtinAuthorizationServers lists authorization servers that publish no
// RFC 8414 / OpenID discovery document. GitHub's protected-resource metadata
// names https://github.com/login/oauth, which serves neither. Discover only
// returns them for resources listed in trustedResourceOrigins.
var builtinAuthorizationServers = map[string]Metadata{
	githubIssuer: {
		Issuer:                      githubIssuer,
		AuthorizationEndpoint:       "https://github.com/login/oauth/authorize",
		TokenEndpoint:               "https://github.com/login/oauth/access_token",
		DeviceAuthorizationEndpoint: "https://github.com/login/device/code",
		CodeChallengeS256:           true,
	},
}
