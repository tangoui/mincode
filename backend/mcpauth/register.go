package mcpauth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

const clientName = "MinCode"

type registrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope,omitempty"`
}

type registrationResponse struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// Register performs RFC 7591 dynamic client registration as a public client
// (token_endpoint_auth_method "none") bound to the loopback redirect URI.
// 401/403 means the server does not accept MinCode as a client
// ([MCP_AUTH_REGISTER_REFUSED]); the caller should offer a manual token instead.
func Register(ctx context.Context, client *http.Client, md Metadata, redirectURI string) (Client, error) {
	if md.RegistrationEndpoint == "" {
		return Client{}, authError("MCP_AUTH_UNSUPPORTED", "授权服务器不支持动态客户端注册")
	}
	if _, err := checkURL(md.RegistrationEndpoint, "客户端注册接口"); err != nil {
		return Client{}, err
	}
	if err := checkLoopbackRedirect(redirectURI); err != nil {
		return Client{}, err
	}
	payload, err := json.Marshal(registrationRequest{
		ClientName:              clientName,
		RedirectURIs:            []string{redirectURI},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		Scope:                   strings.Join(md.Scopes, " "),
	})
	if err != nil {
		return Client{}, authError("MCP_AUTH_REGISTER_FAILED", "编码注册请求失败")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.RegistrationEndpoint, bytes.NewReader(payload))
	if err != nil {
		return Client{}, authError("MCP_AUTH_INSECURE_URL", "客户端注册接口地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	status, body, err := do(secureClient(client, false), req, "客户端注册接口")
	if err != nil {
		return Client{}, err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Client{}, authError("MCP_AUTH_REGISTER_REFUSED", "授权服务器拒绝为 MinCode 注册客户端（%s）", withErrorCode(status, body))
	case status != http.StatusOK && status != http.StatusCreated:
		return Client{}, authError("MCP_AUTH_REGISTER_FAILED", "客户端注册失败（%s）", withErrorCode(status, body))
	}
	var registered registrationResponse
	if err := json.Unmarshal(body, &registered); err != nil || registered.ClientID == "" || len(registered.ClientID) > 1024 {
		return Client{}, authError("MCP_AUTH_REGISTER_FAILED", "客户端注册响应缺少 client_id")
	}
	return Client{
		ID:          registered.ClientID,
		Secret:      registered.ClientSecret,
		Public:      registered.ClientSecret == "",
		RedirectURI: redirectURI,
	}, nil
}

// checkLoopbackRedirect only allows the redirect URI shape StartLoopback
// produces: http://127.0.0.1:<port>/callback (RFC 8252 §7.3).
func checkLoopbackRedirect(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" ||
		parsed.Path != "/callback" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return authError("MCP_AUTH_INVALID_REDIRECT", "回调地址必须是本机回环地址 http://127.0.0.1:<端口>/callback")
	}
	return nil
}
