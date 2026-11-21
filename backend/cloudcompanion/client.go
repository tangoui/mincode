package cloudcompanion

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"mincode/app/backend/core"
	"mincode/app/backend/misc"
)

const (
	cloudAPIPrefix        = "/api/mincode/v1"
	cloudMaxResponseBytes = 1024 * 1024
	cloudHTTPTimeout      = 20 * time.Second
	cloudRequestClockSkew = 5 * time.Minute
)

var cloudNow = time.Now

// cloudRequestError is a refused cloud request. Data keeps the response payload so callers can read
// fixed reason enums; the remote msg is untrusted and never surfaced.
type cloudRequestError struct {
	Status, Code int
	Data         json.RawMessage
}

func (e *cloudRequestError) Error() string {
	return fmt.Sprintf("云账号请求未成功（HTTP %d，code %d），请检查输入或稍后重试", e.Status, e.Code)
}

type cloudEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// cloudAPIError is a refused /api/mincode/v1 request. Message is the server's msg (the v1 endpoints answer
// fixed Chinese texts such as device-proof failures); Data keeps fixed reason enums like device_limit.
type cloudAPIError struct {
	Status, Code int
	Message      string
	Data         json.RawMessage
}

func (e *cloudAPIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("MinCode Cloud 请求失败（HTTP %d，code %d）", e.Status, e.Code)
}

// reason returns data.reason of the refusal, or "" when absent.
func (e *cloudAPIError) reason() string {
	var detail struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(e.Data, &detail) != nil {
		return ""
	}
	return detail.Reason
}

type cloudUserInfo struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Username     string `json:"username"`
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
}

type cloudCheckInResponse struct {
	UserInfo cloudUserInfo `json:"userInfo"`
}

type cloudCheckInSettings struct {
	UserLoginCaptchaSwitch *bool `json:"userLoginCaptchaSwitch"`
}

type memberClickCaptcha struct {
	ID     string   `json:"id"`
	Text   []string `json:"text"`
	Base64 string   `json:"base64"`
	Width  int      `json:"width"`
	Height int      `json:"height"`
}

type cloudRefreshResponse struct {
	Token string `json:"token"`
}

type serverDevice struct {
	DeviceID            string `json:"device_id"`
	DeviceType          string `json:"device_type"`
	Name                string `json:"name"`
	Platform            string `json:"platform"`
	OSVersion           string `json:"os_version"`
	DeviceModel         string `json:"device_model"`
	Architecture        string `json:"architecture"`
	SigningPublicKey    string `json:"signing_public_key"`
	EncryptionPublicKey string `json:"encryption_public_key"`
	AppVersion          string `json:"app_version"`
	FirstLoginAt        int64  `json:"first_login_at"`
	LastLoginAt         int64  `json:"last_login_at"`
	LastSeenAt          int64  `json:"last_seen_at"`
	LastIP              string `json:"last_ip"`
	LoginCount          int    `json:"login_count"`
}

// pairedDevice is one GET /paired-devices entry: a peer device with an active grant to this device.
type pairedDevice struct {
	serverDevice
	Capabilities []string `json:"capabilities"`
	GrantID      string   `json:"grant_id"`
}

type serverDeviceStats struct {
	Total   int `json:"total"`
	Desktop int `json:"desktop"`
	Mobile  int `json:"mobile"`
}

type serverDeviceList struct {
	Items []serverDevice    `json:"items"`
	Stats serverDeviceStats `json:"stats"`
}

func cloudAPIBaseURL(configured string) (string, error) {
	// Shared with the marketplace so artifact downloads always target the session origin.
	base := core.ResolveMinCodeCloudServer(configured)
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("MinCode Cloud 地址无效")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("MinCode Cloud 地址不能包含凭据、查询参数或片段")
	}
	if parsed.Scheme != "https" {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if parsed.Scheme != "http" || (!strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback())) {
			return "", errors.New("MinCode Cloud 仅允许 HTTPS；HTTP 仅限本机开发")
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func cloudEndpoint(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + cloudAPIPrefix + path
}

func cloudHTTPClient() *http.Client {
	return &http.Client{Timeout: cloudHTTPTimeout}
}

func cloudPostJSON(
	ctx context.Context,
	baseURL string,
	path string,
	payload any,
	bearerToken string,
	identity deviceIdentity,
	response any,
) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("编码云请求: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cloudEndpoint(baseURL, path), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建云请求: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	if err := signCloudRequest(req, body, bearerToken, identity); err != nil {
		return err
	}
	return cloudDoRequest(req, response)
}

func cloudGetJSON(
	ctx context.Context,
	baseURL string,
	path string,
	bearerToken string,
	identity deviceIdentity,
	response any,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cloudEndpoint(baseURL, path), nil)
	if err != nil {
		return fmt.Errorf("创建云请求: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	if err := signCloudRequest(req, nil, bearerToken, identity); err != nil {
		return err
	}
	return cloudDoRequest(req, response)
}

func signCloudRequest(req *http.Request, body []byte, token string, identity deviceIdentity) error {
	privateKey, err := base64.StdEncoding.DecodeString(identity.SigningPrivateKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("云设备签名私钥无效")
	}
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("生成云请求 nonce: %w", err)
	}
	timestamp := fmt.Sprintf("%d", cloudNow().Unix())
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	canonical := canonicalCloudRequest(req.Method, req.URL.EscapedPath(), timestamp, nonce, body, token)
	signature := ed25519.Sign(ed25519.PrivateKey(privateKey), []byte(canonical))
	req.Header.Set("X-MinCode-Device-Id", identity.DeviceID)
	req.Header.Set("X-MinCode-Timestamp", timestamp)
	req.Header.Set("X-MinCode-Nonce", nonce)
	req.Header.Set("X-MinCode-Signature", base64.StdEncoding.EncodeToString(signature))
	return nil
}

func canonicalCloudRequest(method, path, timestamp, nonce string, body []byte, token string) string {
	bodyHash := sha256.Sum256(body)
	tokenHash := sha256.Sum256([]byte(token))
	return strings.Join([]string{
		strings.ToUpper(method),
		path,
		timestamp,
		nonce,
		fmt.Sprintf("%x", bodyHash),
		fmt.Sprintf("%x", tokenHash),
	}, "\n")
}

func cloudPost(ctx context.Context, baseURL, path string, fields url.Values, token string, response any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/"+path, strings.NewReader(fields.Encode()))
	if err != nil {
		return fmt.Errorf("创建云账号请求: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.Header.Set("ba-user-token", token)
	}
	client := cloudHTTPClient()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return errors.New("云账号网络请求失败，请稍后重试")
	}
	defer res.Body.Close()
	raw, err := core.ReadAllLimited(res.Body, cloudMaxResponseBytes)
	if err != nil {
		return fmt.Errorf("读取云账号响应: %w", err)
	}
	var envelope cloudEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("云账号响应不是有效 JSON（HTTP %d）", res.StatusCode)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 || envelope.Code != 1 {
		// Remote messages are untrusted and may contain server branding or sensitive details.
		if isMemberAuthFailureCode(envelope.Code) {
			return errMemberTokenRejected
		}
		return &cloudRequestError{Status: res.StatusCode, Code: envelope.Code, Data: envelope.Data}
	}
	if response != nil {
		if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
			return errors.New("云账号响应缺少数据")
		}
		if err := json.Unmarshal(envelope.Data, response); err != nil {
			return fmt.Errorf("解析云账号响应: %w", err)
		}
	}
	return nil
}

func memberCaptchaRequest(ctx context.Context, baseURL, id string) (memberClickCaptcha, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/common/clickCaptcha?id="+url.QueryEscape(id), nil)
	if err != nil {
		return memberClickCaptcha{}, err
	}
	req.Header.Set("Accept", "application/json")
	client := cloudHTTPClient()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return memberClickCaptcha{}, errors.New("获取登录验证码失败，请稍后重试")
	}
	defer res.Body.Close()
	raw, err := core.ReadAllLimited(res.Body, cloudMaxResponseBytes)
	if err != nil {
		return memberClickCaptcha{}, fmt.Errorf("读取登录验证码: %w", err)
	}
	var envelope cloudEnvelope
	var captcha memberClickCaptcha
	if res.StatusCode != http.StatusOK || json.Unmarshal(raw, &envelope) != nil || envelope.Code != 1 || json.Unmarshal(envelope.Data, &captcha) != nil || captcha.ID != id || captcha.Width <= 0 || captcha.Height <= 0 || captcha.Width > 2000 || captcha.Height > 2000 || len(captcha.Text) == 0 || len(captcha.Text) > 10 {
		return memberClickCaptcha{}, errors.New("登录验证码响应无效")
	}
	parts := strings.SplitN(captcha.Base64, ",", 2)
	if len(parts) != 2 || (parts[0] != "data:image/png;base64" && parts[0] != "data:image/jpeg;base64" && parts[0] != "data:image/gif;base64") {
		return memberClickCaptcha{}, errors.New("登录验证码图片无效")
	}
	image, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || len(image) == 0 || len(image) > 512*1024 {
		return memberClickCaptcha{}, errors.New("登录验证码图片无效")
	}
	return captcha, nil
}

func cloudCheckInSettingsRequest(ctx context.Context, baseURL string) (cloudCheckInSettings, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/user/checkIn", nil)
	if err != nil {
		return cloudCheckInSettings{}, err
	}
	req.Header.Set("Accept", "application/json")
	client := cloudHTTPClient()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return cloudCheckInSettings{}, errors.New("读取云账号登录配置失败，请稍后重试")
	}
	defer res.Body.Close()
	raw, err := core.ReadAllLimited(res.Body, cloudMaxResponseBytes)
	if err != nil {
		return cloudCheckInSettings{}, err
	}
	var envelope cloudEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || res.StatusCode != http.StatusOK || envelope.Code != 1 {
		return cloudCheckInSettings{}, errors.New("无法确认云账号登录验证码配置")
	}
	var settings cloudCheckInSettings
	if err := json.Unmarshal(envelope.Data, &settings); err != nil || settings.UserLoginCaptchaSwitch == nil {
		return cloudCheckInSettings{}, errors.New("云账号登录配置响应无效")
	}
	return settings, nil
}

func cloudDoRequest(req *http.Request, response any) error {
	res, err := cloudHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("MinCode Cloud 网络请求失败: %w", err)
	}
	defer res.Body.Close()
	raw, err := core.ReadAllLimited(res.Body, cloudMaxResponseBytes)
	if err != nil {
		return fmt.Errorf("读取 MinCode Cloud 响应: %w", err)
	}
	var envelope cloudEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("MinCode Cloud 响应不是有效 JSON（HTTP %d）", res.StatusCode)
	}
	// The member token failure is checked first: BuildAdmin may pair it with a non-2xx status.
	if isMemberAuthFailureCode(envelope.Code) {
		return errMemberTokenRejected
	}
	// Success is exactly code 1; code 0 carries refusals such as {reason: "device_limit"}.
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices || envelope.Code != 1 {
		return &cloudAPIError{
			Status:  res.StatusCode,
			Code:    envelope.Code,
			Message: core.TakeChars(strings.TrimSpace(envelope.Msg), 240),
			Data:    envelope.Data,
		}
	}
	if response == nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, response); err != nil {
		return fmt.Errorf("解析 MinCode Cloud 业务响应: %w", err)
	}
	return nil
}

func desktopRegistrationPayload(identity deviceIdentity, name string) map[string]any {
	name = strings.TrimSpace(name)
	if name == "" {
		name, _ = os.Hostname()
	}
	if name == "" {
		name = "MinCode Desktop"
	}
	return map[string]any{
		"device_id":             identity.DeviceID,
		"device_type":           "desktop",
		"name":                  core.TakeChars(name, 100),
		"platform":              runtime.GOOS,
		"os_version":            desktopOSVersion(),
		"device_model":          name,
		"architecture":          runtime.GOARCH,
		"signing_public_key":    identity.SigningPublicKey,
		"encryption_public_key": identity.EncryptionPublicKey,
		"app_version":           misc.AppVersion,
	}
}

func desktopOSVersion() string {
	if runtime.GOOS == "darwin" {
		cmd, cancel := core.CommandWithTimeout(5*time.Second, "sw_vers", "-productVersion")
		defer cancel()
		if output, err := cmd.Output(); err == nil {
			return strings.TrimSpace(string(output))
		}
	}
	return runtime.GOOS
}

type pairingChallenge struct {
	Capabilities  []string     `json:"capabilities"`
	ChallengeID   string       `json:"challenge_id"`
	PairingSecret string       `json:"pairing_secret"`
	ExpiresAt     int64        `json:"expires_at"`
	Desktop       serverDevice `json:"desktop"`
}

// authorizedCloudPost sends a signed v1 POST with the member access token, refreshing the token and
// retrying once when the cloud rejects it (code 303/409).
func authorizedCloudPost(
	ctx context.Context,
	session persistedSession,
	identity deviceIdentity,
	path string,
	payload any,
	response any,
) error {
	return withMemberToken(ctx, session, func(token string) error {
		return cloudPostJSON(ctx, session.APIBaseURL, path, payload, token, identity, response)
	})
}

// authorizedCloudGet is the GET counterpart of authorizedCloudPost.
func authorizedCloudGet(
	ctx context.Context,
	session persistedSession,
	identity deviceIdentity,
	path string,
	response any,
) error {
	return withMemberToken(ctx, session, func(token string) error {
		return cloudGetJSON(ctx, session.APIBaseURL, path, token, identity, response)
	})
}

func requestTimestampValid(timestamp int64) bool {
	delta := cloudNow().Sub(time.Unix(timestamp, 0))
	return delta <= cloudRequestClockSkew && delta >= -cloudRequestClockSkew
}
