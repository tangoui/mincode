package mcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// deviceGrantType is the RFC 8628 §3.4 token request grant type.
const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

const (
	defaultDeviceInterval  = 5   // RFC 8628 §3.2 default polling interval
	defaultDeviceExpiresIn = 900 // used when the server omits expires_in
	slowDownStep           = 5   // RFC 8628 §3.5: slow_down adds 5 seconds
	maxDeviceCodeBytes     = 4096
	maxUserCodeRunes       = 64
)

var (
	// devicePollUnit is one RFC 8628 "second"; tests shrink it.
	devicePollUnit = time.Second
	// deviceSleep waits between polls; tests wrap it to observe intervals.
	deviceSleep = sleepContext
)

type deviceAuthorizationResponse struct {
	DeviceCode      string          `json:"device_code"`
	UserCode        string          `json:"user_code"`
	VerificationURI string          `json:"verification_uri"`
	ExpiresIn       json.RawMessage `json:"expires_in"`
	Interval        json.RawMessage `json:"interval"`
}

// deviceLogin runs the OAuth device authorization grant (RFC 8628) for a
// public preregistered client: request a device code, show the user code
// (onDeviceCode, plus opening the verification page), then poll the token
// endpoint until the user approves, denies, the code expires or ctx ends.
// The device code never leaves this function.
func deviceLogin(ctx context.Context, md Metadata, client Client, open func(string) error, onDeviceCode func(userCode, verifyURL string)) (Tokens, error) {
	if md.DeviceAuthorizationEndpoint == "" {
		return Tokens{}, authError("MCP_AUTH_UNSUPPORTED", "授权服务器不支持设备授权")
	}
	if _, err := checkURL(md.DeviceAuthorizationEndpoint, "设备授权接口"); err != nil {
		return Tokens{}, err
	}
	if client.ID == "" {
		return Tokens{}, authError("MCP_AUTH_UNSUPPORTED", "缺少 OAuth 客户端 ID")
	}
	device, err := requestDeviceCode(ctx, md, client)
	if err != nil {
		return Tokens{}, err
	}
	interval := defaultDeviceInterval
	if seconds, ok := parseExpiresIn(device.Interval); ok {
		// A fractional interval truncates to 0: never busy-poll.
		interval = max(int(seconds), 1)
	}
	expiresIn := int64(defaultDeviceExpiresIn)
	if seconds, ok := parseExpiresIn(device.ExpiresIn); ok {
		expiresIn = seconds
	}
	pollCtx, cancel := context.WithTimeoutCause(ctx, time.Duration(expiresIn)*devicePollUnit, errDeviceCodeExpired)
	defer cancel()

	if onDeviceCode != nil {
		onDeviceCode(device.UserCode, device.VerificationURI)
	}
	if open != nil {
		// Best effort: the UI also shows the verification URI.
		_ = open(device.VerificationURI)
	}

	for {
		if err := deviceSleep(pollCtx, time.Duration(interval)*devicePollUnit); err != nil {
			return Tokens{}, devicePollContextError(pollCtx)
		}
		status, body, err := postToken(pollCtx, httpClient, md, client, url.Values{
			"grant_type":  {deviceGrantType},
			"device_code": {device.DeviceCode},
		})
		if err != nil {
			if pollCtx.Err() != nil {
				return Tokens{}, devicePollContextError(pollCtx)
			}
			return Tokens{}, err
		}
		switch oauthErrorCode(body) {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += slowDownStep
			continue
		case "expired_token":
			return Tokens{}, authError("MCP_AUTH_TIMEOUT", "设备验证码已过期，请重新登录")
		case "access_denied":
			return Tokens{}, authError("MCP_AUTH_DENIED", "授权已被拒绝")
		}
		return parseTokenResponse(status, body, "MCP_AUTH_TOKEN_FAILED")
	}
}

var errDeviceCodeExpired = errors.New("device code expired")

func devicePollContextError(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), errDeviceCodeExpired) {
		return authError("MCP_AUTH_TIMEOUT", "设备验证码已过期，请重新登录")
	}
	return contextError(ctx.Err(), "登录已取消")
}

func requestDeviceCode(ctx context.Context, md Metadata, client Client) (deviceAuthorizationResponse, error) {
	form := url.Values{"client_id": {client.ID}}
	if len(md.Scopes) > 0 {
		form.Set("scope", strings.Join(md.Scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.DeviceAuthorizationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return deviceAuthorizationResponse{}, authError("MCP_AUTH_INSECURE_URL", "设备授权接口地址无效")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	status, body, err := do(secureClient(httpClient, false), req, "设备授权接口")
	if err != nil {
		return deviceAuthorizationResponse{}, err
	}
	if status != http.StatusOK {
		return deviceAuthorizationResponse{}, authError("MCP_AUTH_DEVICE_FAILED", "设备授权请求失败（%s）", withErrorCode(status, body))
	}
	var device deviceAuthorizationResponse
	if err := json.Unmarshal(body, &device); err != nil {
		return deviceAuthorizationResponse{}, authError("MCP_AUTH_DEVICE_FAILED", "设备授权响应格式无效")
	}
	if code := oauthErrorCode(body); code != "" {
		return deviceAuthorizationResponse{}, authError("MCP_AUTH_DEVICE_FAILED", "设备授权请求失败（%s）", code)
	}
	if device.DeviceCode == "" || len(device.DeviceCode) > maxDeviceCodeBytes {
		return deviceAuthorizationResponse{}, authError("MCP_AUTH_DEVICE_FAILED", "设备授权响应缺少 device_code")
	}
	if !validUserCode(device.UserCode) {
		return deviceAuthorizationResponse{}, authError("MCP_AUTH_DEVICE_FAILED", "设备授权响应的验证码无效")
	}
	verify, err := checkURL(device.VerificationURI, "设备验证页面")
	if err != nil {
		return deviceAuthorizationResponse{}, err
	}
	device.VerificationURI = verify.String()
	return device, nil
}

// validUserCode accepts the short code the user types (RFC 8628 §6.1):
// printable, no whitespace or control characters, bounded length.
func validUserCode(code string) bool {
	if code == "" || len([]rune(code)) > maxUserCodeRunes {
		return false
	}
	for _, r := range code {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
