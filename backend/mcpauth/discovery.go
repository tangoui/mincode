package mcpauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Metadata is what MinCode needs from the protected resource and its
// authorization server. Issuer keys Preregistered; Resource is the RFC 8707
// resource indicator sent with authorization and token requests.
type Metadata struct {
	Issuer                      string   `json:"issuer,omitempty"`
	Resource                    string   `json:"resource,omitempty"`
	AuthorizationEndpoint       string   `json:"authorizationEndpoint,omitempty"`
	TokenEndpoint               string   `json:"tokenEndpoint,omitempty"`
	RegistrationEndpoint        string   `json:"registrationEndpoint,omitempty"`
	DeviceAuthorizationEndpoint string   `json:"deviceAuthorizationEndpoint,omitempty"`
	Scopes                      []string `json:"scopes,omitempty"`
	CodeChallengeS256           bool     `json:"codeChallengeS256,omitempty"`
}

type protectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

type authorizationServerMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	DeviceAuthorizationEndpoint   string   `json:"device_authorization_endpoint"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
}

// Discover resolves the authorization server for a remote MCP server that
// answered 401. It reads resource_metadata from the Bearer challenge
// (RFC 9728 §5.1), falling back to the well-known protected-resource URL, then
// fetches RFC 8414 metadata with OpenID discovery as the fallback. GitHub's
// authorization server publishes neither document and uses built-in endpoints.
func Discover(ctx context.Context, client *http.Client, serverURL string, wwwAuthenticate string) (Metadata, error) {
	server, err := checkURL(serverURL, "连接器")
	if err != nil {
		return Metadata{}, err
	}
	httpClient := secureClient(client, true)
	params := parseBearerParams(wwwAuthenticate)

	var prm protectedResourceMetadata
	if metadataURL := params["resource_metadata"]; metadataURL != "" {
		if _, err := checkURL(metadataURL, "受保护资源元数据"); err != nil {
			return Metadata{}, err
		}
		status, err := getJSON(ctx, httpClient, metadataURL, "受保护资源元数据", &prm)
		if err != nil {
			return Metadata{}, err
		}
		if status != http.StatusOK {
			return Metadata{}, authError("MCP_AUTH_DISCOVERY_FAILED", "受保护资源元数据返回 HTTP %d", status)
		}
	} else if err := firstJSON(ctx, httpClient, protectedResourceCandidates(server), "受保护资源元数据", &prm); err != nil {
		return Metadata{}, err
	}

	resource := canonicalResource(server)
	if prm.Resource != "" {
		if !resourceCovers(prm.Resource, server) {
			return Metadata{}, authError("MCP_AUTH_DISCOVERY_FAILED", "受保护资源元数据声明的资源与连接器地址不符")
		}
		resource = prm.Resource
	}
	if len(prm.AuthorizationServers) == 0 {
		return Metadata{}, authError("MCP_AUTH_DISCOVERY_FAILED", "受保护资源元数据没有列出授权服务器")
	}
	issuer := prm.AuthorizationServers[0]
	issuerURL, err := checkURL(issuer, "授权服务器")
	if err != nil {
		return Metadata{}, err
	}
	scopes := strings.Fields(params["scope"])
	if len(scopes) == 0 && len(prm.ScopesSupported) > 0 {
		scopes = slices.Clone(prm.ScopesSupported)
	}

	if builtin, ok := builtinAuthorizationServers[normalizeIssuer(issuer)]; ok {
		// Confused-deputy guard: only the connectors the built-in server
		// belongs to may use it (and MinCode's preregistered client with it).
		if !resourceOriginTrusted(builtin.Issuer, resource) {
			return Metadata{}, authError("MCP_AUTH_UNSUPPORTED", "该连接器声明的授权服务器不能在 MinCode 中登录")
		}
		builtin.Resource = resource
		builtin.Scopes = scopes
		return builtin, nil
	}

	var as authorizationServerMetadata
	if err := firstJSON(ctx, httpClient, authorizationServerCandidates(issuerURL), "授权服务器元数据", &as); err != nil {
		return Metadata{}, err
	}
	if as.Issuer == "" || strings.TrimSuffix(as.Issuer, "/") != strings.TrimSuffix(issuer, "/") {
		return Metadata{}, authError("MCP_AUTH_DISCOVERY_FAILED", "授权服务器元数据的 issuer 与授权服务器不符")
	}
	if as.AuthorizationEndpoint == "" || as.TokenEndpoint == "" {
		return Metadata{}, authError("MCP_AUTH_DISCOVERY_FAILED", "授权服务器元数据缺少授权或令牌接口")
	}
	for _, endpoint := range []struct{ value, what string }{
		{as.AuthorizationEndpoint, "授权接口"},
		{as.TokenEndpoint, "令牌接口"},
		{as.RegistrationEndpoint, "客户端注册接口"},
		{as.DeviceAuthorizationEndpoint, "设备授权接口"},
	} {
		if endpoint.value == "" {
			continue
		}
		if _, err := checkURL(endpoint.value, endpoint.what); err != nil {
			return Metadata{}, err
		}
	}
	return Metadata{
		Issuer:                      as.Issuer,
		Resource:                    resource,
		AuthorizationEndpoint:       as.AuthorizationEndpoint,
		TokenEndpoint:               as.TokenEndpoint,
		RegistrationEndpoint:        as.RegistrationEndpoint,
		DeviceAuthorizationEndpoint: as.DeviceAuthorizationEndpoint,
		Scopes:                      scopes,
		CodeChallengeS256:           slices.Contains(as.CodeChallengeMethodsSupported, "S256"),
	}, nil
}

// getJSON fetches a metadata document. A non-200 status is returned without
// an error so callers can try the next candidate.
func getJSON(ctx context.Context, client *http.Client, rawURL, what string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, authError("MCP_AUTH_INSECURE_URL", "%s地址无效", what)
	}
	req.Header.Set("Accept", "application/json")
	status, body, err := do(client, req, what)
	if err != nil || status != http.StatusOK {
		return status, err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return status, authError("MCP_AUTH_DISCOVERY_FAILED", "%s格式无效", what)
	}
	return status, nil
}

func firstJSON(ctx context.Context, client *http.Client, candidates []string, what string, out any) error {
	lastStatus := 0
	for _, candidate := range candidates {
		status, err := getJSON(ctx, client, candidate, what, out)
		if err != nil {
			return err
		}
		if status == http.StatusOK {
			return nil
		}
		lastStatus = status
	}
	return authError("MCP_AUTH_DISCOVERY_FAILED", "找不到%s（HTTP %d）", what, lastStatus)
}

func origin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// protectedResourceCandidates follows RFC 9728 §3.1: the well-known suffix
// goes between the host and the path. The root document is tried last, as the
// MCP authorization spec allows.
func protectedResourceCandidates(server *url.URL) []string {
	base := origin(server) + "/.well-known/oauth-protected-resource"
	path := server.EscapedPath()
	candidates := []string{}
	for _, p := range []string{path, strings.TrimSuffix(path, "/"), ""} {
		if p == "/" {
			p = ""
		}
		if !slices.Contains(candidates, base+p) {
			candidates = append(candidates, base+p)
		}
	}
	return candidates
}

// authorizationServerCandidates follows the MCP authorization spec order:
// RFC 8414 path insertion, OpenID path insertion, then OpenID path appending.
func authorizationServerCandidates(issuer *url.URL) []string {
	base := origin(issuer)
	path := strings.TrimSuffix(issuer.EscapedPath(), "/")
	if path == "" {
		return []string{
			base + "/.well-known/oauth-authorization-server",
			base + "/.well-known/openid-configuration",
		}
	}
	return []string{
		base + "/.well-known/oauth-authorization-server" + path,
		base + "/.well-known/openid-configuration" + path,
		base + path + "/.well-known/openid-configuration",
	}
}

// canonicalResource is the connector URL without query or fragment: connector
// URLs may carry API keys in the query, which must never become the RFC 8707
// resource value.
func canonicalResource(server *url.URL) string {
	return origin(server) + server.EscapedPath()
}

func hostKey(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" || (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return host
	}
	return host + ":" + port
}

// resourceCovers reports whether the resource identifier from the metadata
// names the connector URL itself or one of its parent paths on the same
// origin (RFC 9728 §3.3 forbids accepting metadata for another resource).
func resourceCovers(resource string, server *url.URL) bool {
	parsed, err := checkURL(resource, "资源")
	if err != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, server.Scheme) || hostKey(parsed) != hostKey(server) {
		return false
	}
	resourcePath := strings.TrimSuffix(parsed.EscapedPath(), "/")
	serverPath := strings.TrimSuffix(server.EscapedPath(), "/")
	return serverPath == resourcePath || strings.HasPrefix(serverPath, resourcePath+"/")
}

// parseBearerParams returns the auth-params of the first Bearer challenge in a
// WWW-Authenticate header (RFC 9110 §11.6.1). Parameters of other schemes are
// ignored; names are lower-cased and the first occurrence wins.
func parseBearerParams(header string) map[string]string {
	params := map[string]string{}
	inBearer := false
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' }
	i := 0
	for i < len(header) {
		for i < len(header) && (isSpace(header[i]) || header[i] == ',') {
			i++
		}
		start := i
		for i < len(header) && !isSpace(header[i]) && header[i] != ',' && header[i] != '=' {
			i++
		}
		token := header[start:i]
		if token == "" {
			if i < len(header) {
				i++ // stray '='
			}
			continue
		}
		j := i
		for j < len(header) && isSpace(header[j]) {
			j++
		}
		if j < len(header) && header[j] == '=' {
			i = j + 1
			for i < len(header) && isSpace(header[i]) {
				i++
			}
			var value string
			if i < len(header) && header[i] == '"' {
				i++
				var b strings.Builder
				for i < len(header) && header[i] != '"' {
					if header[i] == '\\' && i+1 < len(header) {
						i++
					}
					b.WriteByte(header[i])
					i++
				}
				i++ // closing quote
				value = b.String()
			} else {
				start := i
				for i < len(header) && header[i] != ',' && !isSpace(header[i]) {
					i++
				}
				value = header[start:i]
			}
			if key := strings.ToLower(token); inBearer {
				if _, seen := params[key]; !seen {
					params[key] = value
				}
			}
			continue
		}
		// A bare token starts a new challenge.
		if inBearer {
			break
		}
		inBearer = strings.EqualFold(token, "bearer")
		i = j
	}
	return params
}
