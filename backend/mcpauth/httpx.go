// Package mcpauth holds the OAuth building blocks for remote MCP connectors:
// authorization-server discovery (RFC 9728 / RFC 8414 / OpenID discovery),
// dynamic client registration (RFC 7591), PKCE (RFC 7636), the loopback
// redirect listener (RFC 8252), token exchange/refresh with resource indicators
// (RFC 8707) and the encrypted token store.
//
// Security rules for this package:
//   - every outbound request goes to an https URL (tests relax this through
//     allowHTTPForTest), redirects never downgrade to http, and requests that
//     carry credentials (registration, token) never follow redirects;
//   - every response body is capped at maxResponseBytes;
//   - error messages carry a stable "[MCP_AUTH_*]" code and never include
//     tokens, authorization codes, verifiers, client secrets or full URLs
//     (connector URLs may carry API keys in their query string).
package mcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	maxResponseBytes   = 1 << 20
	defaultHTTPTimeout = 30 * time.Second
	maxRedirects       = 5
)

// allowHTTPForTest lets tests talk to plain-HTTP httptest servers. It is never
// set in production code.
var allowHTTPForTest = false

// now is replaced in tests to make expiry arithmetic deterministic.
var now = time.Now

func authError(code, format string, args ...any) error {
	return fmt.Errorf("[%s] "+format, append([]any{code}, args...)...)
}

// checkURL validates an endpoint before any request is made. what names the
// endpoint in error text; the URL itself is never echoed.
func checkURL(raw, what string) (*url.URL, error) {
	if raw == "" {
		return nil, authError("MCP_AUTH_DISCOVERY_FAILED", "缺少%s地址", what)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Opaque != "" {
		return nil, authError("MCP_AUTH_INSECURE_URL", "%s地址无效", what)
	}
	if parsed.User != nil {
		return nil, authError("MCP_AUTH_INSECURE_URL", "%s地址不能包含用户信息", what)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !allowHTTPForTest {
			return nil, authError("MCP_AUTH_INSECURE_URL", "%s必须使用 https", what)
		}
	default:
		return nil, authError("MCP_AUTH_INSECURE_URL", "%s必须使用 https", what)
	}
	return parsed, nil
}

// secureClient copies the caller's client and installs the redirect policy:
// credential-bearing requests never follow redirects, metadata fetches follow
// a bounded number of redirects that must stay on https.
func secureClient(client *http.Client, followRedirects bool) *http.Client {
	var copied http.Client
	if client != nil {
		copied = *client
	}
	if copied.Timeout == 0 {
		copied.Timeout = defaultHTTPTimeout
	}
	inner := copied.CheckRedirect
	copied.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !followRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) >= maxRedirects {
			return authError("MCP_AUTH_DISCOVERY_FAILED", "重定向次数过多")
		}
		if _, err := checkURL(req.URL.String(), "重定向"); err != nil {
			return err
		}
		if inner != nil {
			return inner(req, via)
		}
		return nil
	}
	return &copied
}

// networkError strips the request URL from transport errors.
func networkError(what string, err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	if strings.HasPrefix(err.Error(), "[MCP_AUTH_") {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return authError("MCP_AUTH_CANCELLED", "请求%s已取消", what)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return authError("MCP_AUTH_TIMEOUT", "请求%s超时", what)
	}
	return authError("MCP_AUTH_NETWORK", "请求%s失败: %v", what, err)
}

func readLimited(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, authError("MCP_AUTH_NETWORK", "读取响应失败: %v", err)
	}
	if len(data) > maxResponseBytes {
		return nil, authError("MCP_AUTH_RESPONSE_TOO_LARGE", "响应超过 %d 字节", maxResponseBytes)
	}
	return data, nil
}

func do(client *http.Client, req *http.Request, what string) (int, []byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, networkError(what, err)
	}
	defer resp.Body.Close()
	data, err := readLimited(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

var oauthErrorCodePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// oauthErrorCode returns the RFC 6749 "error" code from a response body when it
// is a short identifier. error_description is never surfaced: it is free text
// chosen by the server and may echo request values.
func oauthErrorCode(body []byte) string {
	var payload struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	code, _ := payload.Error.(string)
	if !oauthErrorCodePattern.MatchString(code) {
		return ""
	}
	return code
}

func withErrorCode(status int, body []byte) string {
	if code := oauthErrorCode(body); code != "" {
		return fmt.Sprintf("HTTP %d (%s)", status, code)
	}
	return fmt.Sprintf("HTTP %d", status)
}
