package mcpauth

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Tokens is the token set for one connector. It lives only in the Go backend
// (encrypted in the token store) and is never returned to plugins or the UI.
type Tokens struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt"` // zero when the server sent no expires_in
	Scope        string    `json:"scope,omitempty"`
}

// maxExpiresIn bounds expires_in so a hostile value cannot overflow time math.
const maxExpiresIn = 10 * 365 * 24 * 60 * 60

type tokenResponse struct {
	AccessToken  string          `json:"access_token"`
	TokenType    string          `json:"token_type"`
	ExpiresIn    json.RawMessage `json:"expires_in"`
	RefreshToken string          `json:"refresh_token"`
	Scope        string          `json:"scope"`
	Error        any             `json:"error"`
}

// ExchangeCode redeems an authorization code with its PKCE verifier and the
// RFC 8707 resource indicator.
func ExchangeCode(ctx context.Context, client *http.Client, md Metadata, c Client, code, verifier, redirectURI string) (Tokens, error) {
	if code == "" || verifier == "" {
		return Tokens{}, authError("MCP_AUTH_TOKEN_FAILED", "缺少授权码或 PKCE 校验码")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
	}
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	return requestToken(ctx, client, md, c, form, false)
}

// Refresh redeems a refresh token. When the server does not rotate the refresh
// token the old one is kept. A 400/401 or an OAuth error object yields
// [MCP_AUTH_REFRESH_REJECTED]: the caller should mark the connector as needing
// a new login. Network and 5xx failures yield other codes and leave the
// stored login alone.
func Refresh(ctx context.Context, client *http.Client, md Metadata, c Client, refreshToken string) (Tokens, error) {
	if refreshToken == "" {
		return Tokens{}, authError("MCP_AUTH_REFRESH_REJECTED", "没有可用的刷新令牌")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	tokens, err := requestToken(ctx, client, md, c, form, true)
	if err != nil {
		return Tokens{}, err
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}
	return tokens, nil
}

func requestToken(ctx context.Context, client *http.Client, md Metadata, c Client, form url.Values, refresh bool) (Tokens, error) {
	status, body, err := postToken(ctx, client, md, c, form)
	if err != nil {
		return Tokens{}, err
	}
	rejectedCode := "MCP_AUTH_TOKEN_FAILED"
	if refresh {
		rejectedCode = "MCP_AUTH_REFRESH_REJECTED"
	}
	return parseTokenResponse(status, body, rejectedCode)
}

// postToken sends one token-endpoint request with the client's credentials
// and the RFC 8707 resource indicator. It never follows redirects.
func postToken(ctx context.Context, client *http.Client, md Metadata, c Client, form url.Values) (int, []byte, error) {
	if _, err := checkURL(md.TokenEndpoint, "令牌接口"); err != nil {
		return 0, nil, err
	}
	if c.ID == "" {
		return 0, nil, authError("MCP_AUTH_UNSUPPORTED", "缺少 OAuth 客户端 ID")
	}
	if md.Resource != "" {
		form.Set("resource", md.Resource)
	}
	if c.Secret == "" {
		form.Set("client_id", c.ID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, authError("MCP_AUTH_INSECURE_URL", "令牌接口地址无效")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// GitHub answers form-encoded unless JSON is requested explicitly.
	req.Header.Set("Accept", "application/json")
	if c.Secret != "" {
		// RFC 6749 §2.3.1: client_secret_basic with form-encoded credentials.
		req.SetBasicAuth(url.QueryEscape(c.ID), url.QueryEscape(c.Secret))
	}
	return do(secureClient(client, false), req, "令牌接口")
}

// parseTokenResponse turns a token-endpoint answer into Tokens. 400/401 and
// OAuth error objects (GitHub sends them with HTTP 200) yield rejectedCode.
func parseTokenResponse(status int, body []byte, rejectedCode string) (Tokens, error) {
	if status == http.StatusBadRequest || status == http.StatusUnauthorized {
		return Tokens{}, authError(rejectedCode, "令牌接口拒绝请求（%s）", withErrorCode(status, body))
	}
	if status != http.StatusOK {
		return Tokens{}, authError("MCP_AUTH_TOKEN_FAILED", "令牌接口返回 %s", withErrorCode(status, body))
	}
	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Tokens{}, authError("MCP_AUTH_TOKEN_FAILED", "令牌响应格式无效")
	}
	if parsed.Error != nil && parsed.Error != "" {
		// GitHub reports failures as HTTP 200 with an error object.
		return Tokens{}, authError(rejectedCode, "令牌接口拒绝请求（%s）", withErrorCode(status, body))
	}
	if parsed.AccessToken == "" {
		return Tokens{}, authError("MCP_AUTH_TOKEN_FAILED", "令牌响应缺少 access_token")
	}
	if parsed.TokenType != "" && !strings.EqualFold(parsed.TokenType, "bearer") {
		return Tokens{}, authError("MCP_AUTH_TOKEN_FAILED", "不支持的令牌类型")
	}
	tokens := Tokens{AccessToken: parsed.AccessToken, RefreshToken: parsed.RefreshToken, Scope: parsed.Scope}
	if seconds, ok := parseExpiresIn(parsed.ExpiresIn); ok && seconds > 0 {
		tokens.ExpiresAt = now().Add(time.Duration(seconds) * time.Second)
	}
	return tokens, nil
}

// parseExpiresIn accepts a JSON number or a numeric string; some servers send
// the latter.
func parseExpiresIn(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err != nil {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	}
	if math.IsNaN(number) || number <= 0 {
		return 0, false
	}
	return int64(math.Min(number, maxExpiresIn)), true
}
