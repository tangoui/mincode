package cloudcompanion

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"mincode/app/backend/core"
)

func init() {
	core.Register("cloud_login", afterStartup(cloudLoginHandler))
	core.Register("cloud_login_captcha", cloudLoginCaptchaHandler)
	core.Register("cloud_login_captcha_check", cloudLoginCaptchaCheckHandler)
	core.Register("cloud_register", afterStartup(cloudRegisterHandler))
	core.Register("cloud_register_code", cloudRegisterCodeHandler)

	core.Register("cloud_status", afterStartup(cloudStatusHandler))
	core.Register("cloud_logout", afterStartup(cloudLogoutHandler))
	core.Register("cloud_device_identity", cloudDeviceIdentityHandler)
	core.Register("cloud_realtime_connect", afterStartup(cloudRealtimeConnectHandler))
	core.Register("cloud_realtime_disconnect", afterStartup(cloudRealtimeDisconnectHandler))
	core.Register("cloud_realtime_status", cloudRealtimeStatusHandler)
	core.Register("cloud_pairing_create", cloudPairingCreateHandler)
	core.Register("cloud_devices_list", cloudDevicesListHandler)
	core.Register("cloud_paired_devices", cloudPairedDevicesHandler)
	core.Register("cloud_device_revoke", afterStartup(cloudDeviceRevokeHandler))
	core.Register("cloud_realtime_ack", cloudRealtimeAckHandler)
	core.Register("cloud_realtime_send", cloudRealtimeSendHandler)
}

type loginArgs struct {
	Email       string `json:"email"`
	Credential  string `json:"password"`
	ServerURL   string `json:"serverUrl"`
	CaptchaID   string `json:"captchaId"`
	CaptchaInfo string `json:"captchaInfo"`
}

type logoutArgs struct {
	ServerURL string `json:"serverUrl"`
}

type createPairingArgs struct {
	Capabilities []string `json:"capabilities"`
}

type relaySendArgs struct {
	RequestID      string         `json:"requestId"`
	TargetDeviceID string         `json:"targetDeviceId"`
	Channel        string         `json:"channel"`
	Sequence       int64          `json:"sequence"`
	Payload        map[string]any `json:"payload"`
}

type relayAckArgs struct {
	RequestID string `json:"requestId"`
	RelayID   string `json:"relayId"`
}

type revokeDeviceArgs struct {
	DeviceID string `json:"deviceId"`
}

type memberRegisterArgs struct {
	Email        string `json:"email"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	Confirmation string `json:"passwordConfirmation"`
	Code         string `json:"code"`
	ServerURL    string `json:"serverUrl"`
}

type memberRegisterCodeArgs struct {
	Email       string `json:"email"`
	ServerURL   string `json:"serverUrl"`
	CaptchaID   string `json:"captchaId"`
	CaptchaInfo string `json:"captchaInfo"`
}

func cloudRegisterCodeHandler(_, argsJSON string) (string, error) {
	var args memberRegisterCodeArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	args.Email = strings.TrimSpace(args.Email)
	if args.Email == "" {
		return "", errors.New("请输入邮箱")
	}
	baseURL, err := configuredServerURL(args.ServerURL)
	if err != nil {
		return "", err
	}
	if err := consumeMemberCaptcha(baseURL, "register", args.CaptchaID, args.CaptchaInfo); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	if err := cloudPost(ctx, baseURL, "ems/send", url.Values{"email": {args.Email}, "event": {"user_register"}, "captchaId": {args.CaptchaID}, "captchaInfo": {args.CaptchaInfo}}, "", nil); err != nil {
		if message := registrationRefusal(err); message != "" {
			return "", errors.New("发送邮箱验证码失败：" + message)
		}
		return "", fmt.Errorf("发送邮箱验证码: %w", err)
	}
	return core.EncodeResult(true)
}

func cloudRegisterHandler(_, argsJSON string) (string, error) {
	var args memberRegisterArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	args.Email = strings.TrimSpace(args.Email)
	args.Username = strings.TrimSpace(args.Username)
	args.Code = strings.TrimSpace(args.Code)
	if args.Email == "" || args.Username == "" || args.Password == "" || args.Code == "" || args.Password != args.Confirmation {
		return "", errors.New("请填写完整注册信息并确认两次密码一致")
	}
	baseURL, err := configuredServerURL(args.ServerURL)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	var result cloudCheckInResponse
	// keep=1 asks the cloud to issue a refresh token with the new account, as login does.
	if err := cloudPost(ctx, baseURL, "user/checkIn", url.Values{"tab": {"register"}, "registerType": {"email"}, "email": {args.Email}, "username": {args.Username}, "password": {args.Password}, "captcha": {args.Code}, "keep": {"1"}}, "", &result); err != nil {
		if message := registrationRefusal(err); message != "" {
			return "", errors.New("注册失败：" + message)
		}
		return "", fmt.Errorf("云账号注册: %w", err)
	}
	if memberUserInfoComplete(result.UserInfo) {
		return saveMemberSession(baseURL, result.UserInfo)
	}
	// The account now exists even though this server returned no refresh token (older cloud
	// builds). Never end registration in an error: sign in once with the same credentials, or,
	// when login needs a captcha, tell the UI to switch to the login form.
	if settings, err := cloudCheckInSettingsRequest(ctx, baseURL); err == nil && !*settings.UserLoginCaptchaSwitch {
		var login cloudCheckInResponse
		loginFields := url.Values{"tab": {"login"}, "username": {args.Username}, "password": {args.Password}, "keep": {"1"}}
		if err := cloudPost(ctx, baseURL, "user/checkIn", loginFields, "", &login); err == nil && memberUserInfoComplete(login.UserInfo) {
			return saveMemberSession(baseURL, login.UserInfo)
		}
	}
	status := statusWithoutSession()
	status.RegisteredNeedsLogin = true
	status.Email = args.Email
	return core.EncodeResult(status)
}

// registrationReasons explains the cloud's fixed registration refusal reasons; unknown reasons keep the
// generic error.
var registrationReasons = map[string]string{
	"username_invalid": "用户名需以英文字母开头，3–16 位，只能包含字母、数字和下划线",
	"username_taken":   "该用户名已被注册，请换一个用户名",
	"password_invalid": "密码需为 6–32 位，且不能包含 & < > \" ' 或换行",
	"email_invalid":    "邮箱格式不正确",
	"email_taken":      "该邮箱已注册，请直接登录",
	"captcha_invalid":  "邮箱验证码错误或已过期",
	"too_frequent":     "验证码发送过于频繁，请 1 分钟后再试",
}

func registrationRefusal(err error) string {
	var refused *cloudRequestError
	if !errors.As(err, &refused) {
		return ""
	}
	var detail struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(refused.Data, &detail) != nil {
		return ""
	}
	return registrationReasons[detail.Reason]
}

func memberUserInfoComplete(user cloudUserInfo) bool {
	return user.ID > 0 && strings.TrimSpace(user.Token) != "" && strings.TrimSpace(user.RefreshToken) != ""
}

var memberCaptchaState = struct {
	sync.Mutex
	entries map[string]memberCaptchaEntry
}{entries: make(map[string]memberCaptchaEntry)}

type memberCaptchaEntry struct {
	baseURL              string
	width, height, steps int
	expires              time.Time
	verified             bool
	info                 string
	purpose              string
}

type memberCaptchaCheckArgs struct {
	ID      string `json:"id"`
	Info    string `json:"info"`
	Purpose string `json:"purpose"`
}

type memberCaptchaRequestArgs struct {
	Purpose   string `json:"purpose"`
	ServerURL string `json:"serverUrl"`
}

func cloudLoginCaptchaHandler(_, argsJSON string) (string, error) {
	var args memberCaptchaRequestArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if args.Purpose == "" {
		args.Purpose = "login"
	}
	if args.Purpose != "login" && args.Purpose != "register" {
		return "", errors.New("验证码用途无效")
	}
	baseURL, err := configuredServerURL(args.ServerURL)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	settings, err := cloudCheckInSettingsRequest(ctx, baseURL)
	if err != nil {
		return "", err
	}
	if args.Purpose == "login" && !*settings.UserLoginCaptchaSwitch {
		return core.EncodeResult(struct {
			Required bool `json:"required"`
		}{false})
	}
	idBytes := make([]byte, 24)
	if _, err := rand.Read(idBytes); err != nil {
		return "", errors.New("生成登录验证码失败")
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	captcha, err := memberCaptchaRequest(ctx, baseURL, id)
	if err != nil {
		return "", err
	}
	memberCaptchaState.Lock()
	for key, entry := range memberCaptchaState.entries {
		if time.Now().After(entry.expires) {
			delete(memberCaptchaState.entries, key)
		}
	}
	memberCaptchaState.entries[id] = memberCaptchaEntry{baseURL: baseURL, width: captcha.Width, height: captcha.Height, steps: len(captcha.Text), expires: time.Now().Add(5 * time.Minute), purpose: args.Purpose}
	memberCaptchaState.Unlock()
	return core.EncodeResult(struct {
		Required bool `json:"required"`
		memberClickCaptcha
	}{true, captcha})
}

func validMemberCaptchaInfo(info string, entry memberCaptchaEntry) bool {
	parts := strings.Split(info, ";")
	if len(parts) != 3 || parts[1] != fmt.Sprint(entry.width) || parts[2] != fmt.Sprint(entry.height) {
		return false
	}
	points := strings.Split(parts[0], "-")
	if len(points) != entry.steps {
		return false
	}
	for _, point := range points {
		var x, y int
		if n, _ := fmt.Sscanf(point, "%d,%d", &x, &y); n != 2 || fmt.Sprintf("%d,%d", x, y) != point || x < 0 || y < 0 || x >= entry.width || y >= entry.height {
			return false
		}
	}
	return true
}

func cloudLoginCaptchaCheckHandler(_, argsJSON string) (string, error) {
	var args memberCaptchaCheckArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	memberCaptchaState.Lock()
	entry, ok := memberCaptchaState.entries[args.ID]
	if ok {
		delete(memberCaptchaState.entries, args.ID)
	}
	memberCaptchaState.Unlock()
	if args.Purpose == "" {
		args.Purpose = "login"
	}
	if !ok || entry.purpose != args.Purpose || time.Now().After(entry.expires) || !validMemberCaptchaInfo(args.Info, entry) {
		return "", errors.New("验证码已过期或无效，请刷新重试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	// BuildAdmin reads unset with post("unset/b"), a PHP (bool) cast: the form string "false" is true and would
	// delete the challenge before checkIn reuses it. Only "0" or an empty value keeps it.
	if err := cloudPost(ctx, entry.baseURL, "common/checkClickCaptcha", url.Values{"id": {args.ID}, "info": {args.Info}, "unset": {"0"}}, "", nil); err != nil {
		return "", fmt.Errorf("验证登录验证码: %w", err)
	}
	entry.verified = true
	entry.info = args.Info
	memberCaptchaState.Lock()
	memberCaptchaState.entries[args.ID] = entry
	memberCaptchaState.Unlock()
	return core.EncodeResult(true)
}

func consumeMemberCaptcha(baseURL, purpose, id, info string) error {
	memberCaptchaState.Lock()
	entry, ok := memberCaptchaState.entries[id]
	if ok {
		delete(memberCaptchaState.entries, id)
	}
	memberCaptchaState.Unlock()
	if !ok || !entry.verified || entry.purpose != purpose || entry.baseURL != baseURL || time.Now().After(entry.expires) || entry.info != info || !validMemberCaptchaInfo(info, entry) {
		return errors.New("请先完成验证码验证")
	}
	return nil
}

func saveMemberSession(baseURL string, user cloudUserInfo) (string, error) {
	if !memberUserInfoComplete(user) {
		return "", errors.New("云账号响应缺少账号或令牌")
	}
	identity, err := loadOrCreateIdentity()
	if err != nil {
		return "", err
	}
	session := persistedSession{APIBaseURL: baseURL, AccountID: user.ID, Email: user.Email, AccessToken: user.Token, RefreshToken: user.RefreshToken, AuthProvider: "member", AccessTokenObtainedAt: memberTokenNow().Unix()}
	cloudRealtime.stop()
	if err := writeSession(session); err != nil {
		return "", err
	}
	// Registration failure is only recorded in the status and the log; the login itself succeeded.
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	if registerRelayDeviceAndRecord(ctx, session, identity) == nil {
		startRealtimeAfterRegistration()
	}
	return core.EncodeResult(statusFromSession(session, identity))
}

// startRealtimeAfterRegistration connects the relay once this desktop is registered; tests replace it.
var startRealtimeAfterRegistration = func() {
	if err := cloudRealtime.start(); err != nil {
		log.Printf("cloud realtime start failed: %v", err)
	}
}

func cloudLoginHandler(_, argsJSON string) (string, error) {
	var args loginArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	args.Email = strings.TrimSpace(args.Email)
	if args.Email == "" || args.Credential == "" {
		return "", errors.New("邮箱和密码不能为空")
	}
	baseURL, err := configuredServerURL(args.ServerURL)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	settings, err := cloudCheckInSettingsRequest(ctx, baseURL)
	if err != nil {
		return "", err
	}
	fields := url.Values{"tab": {"login"}, "username": {args.Email}, "password": {args.Credential}, "keep": {"1"}}
	if *settings.UserLoginCaptchaSwitch {
		if err := consumeMemberCaptcha(baseURL, "login", args.CaptchaID, args.CaptchaInfo); err != nil {
			return "", fmt.Errorf("登录: %w", err)
		}
		fields.Set("captchaId", args.CaptchaID)
		fields.Set("captchaInfo", args.CaptchaInfo)
	}
	var result cloudCheckInResponse
	if err := cloudPost(ctx, baseURL, "user/checkIn", fields, "", &result); err != nil {
		return "", fmt.Errorf("云账号登录: %w", err)
	}
	return saveMemberSession(baseURL, result.UserInfo)
}

func cloudStatusHandler(_, _ string) (string, error) {
	identity, identityErr := loadOrCreateIdentity()
	if identityErr != nil {
		return "", identityErr
	}
	session, err := readSession()
	if errors.Is(err, os.ErrNotExist) {
		return core.EncodeResult(CloudStatus{
			Configured: true,
			Device:     ptr(publicIdentity(identity)),
		})
	}
	if err != nil {
		return "", err
	}
	if isMemberSession(session) {
		ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
		defer cancel()
		// Reuses the cached access token; the refresh endpoint is called only near expiry.
		token, err := memberAccessToken(ctx, session, "")
		if err != nil {
			return "", err
		}
		session.AccessToken = token
		// Retry a failed device registration in the background (single-flight) so freeing a device slot
		// is picked up when the panel reopens; the status returned now reports it as pending.
		// Once it succeeds, connect the relay: a failed login-time registration never started it.
		// A manual disconnect cancels the retry, so its callback cannot undo that.
		if state, _, _, known := relayRegistrationState(session, identity); known && state == relayRegistrationFailed {
			registerRelayDeviceInBackground(session, identity, func(err error) {
				if err == nil {
					startRealtimeAfterRegistration()
				}
			})
		}
	}
	return core.EncodeResult(statusFromSession(session, identity))
}

func cloudLogoutHandler(_, argsJSON string) (string, error) {
	var args logoutArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	session, err := readSession()
	if errors.Is(err, os.ErrNotExist) {
		return core.EncodeResult(statusWithoutSession())
	}
	if err != nil {
		return "", err
	}
	if err := validateLogoutServerURL(session.APIBaseURL, args.ServerURL); err != nil {
		return "", err
	}
	cancelRelayRegistration()
	cloudRealtime.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if isMemberSession(session) {
		remoteErr := withMemberToken(ctx, session, func(token string) error {
			return cloudPost(ctx, session.APIBaseURL, "user/logout", url.Values{"refreshToken": {session.RefreshToken}}, token, nil)
		})
		// Never retain local credentials merely because remote revocation is unavailable.
		// Do not report remote logout as successful if the server could not be reached.
		if err := deleteSession(); err != nil {
			return "", errors.Join(fmt.Errorf("移除本地会员会话: %w", err), remoteErr)
		}
		if remoteErr != nil {
			return "", fmt.Errorf("本地已退出，但远程会话可能仍有效: %w", remoteErr)
		}
		return core.EncodeResult(statusWithoutSession())
	}
	// A legacy device-token session has no remote logout endpoint any more; only the local copy is removed.
	if err := deleteSession(); err != nil {
		return "", err
	}
	return core.EncodeResult(statusWithoutSession())
}

func cloudDeviceIdentityHandler(_, _ string) (string, error) {
	identity, err := loadOrCreateIdentity()
	if err != nil {
		return "", fmt.Errorf("读取云设备身份: %w", err)
	}
	return core.EncodeResult(publicIdentity(identity))
}

func cloudRealtimeConnectHandler(_, _ string) (string, error) {
	session, identity, err := authenticatedCloudContext()
	if err != nil {
		return "", err
	}
	// A device that is not registered cannot get a ticket: retry a failed registration first.
	if state, _, _, known := relayRegistrationState(session, identity); !known || state != relayRegistrationRegistered {
		ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
		defer cancel()
		if err := registerRelayDeviceAndRecord(ctx, session, identity); err != nil {
			return "", err
		}
	}
	if err := cloudRealtime.start(); err != nil {
		return "", err
	}
	return core.EncodeResult(cloudRealtime.snapshot())
}

func cloudRealtimeDisconnectHandler(_, _ string) (string, error) {
	// Startup registers in the background and then connects; cancel that first so an explicit
	// disconnect during startup is not undone by the registration callback.
	cancelRelayRegistration()
	cloudRealtime.stop()
	return core.EncodeResult(cloudRealtime.snapshot())
}

func cloudRealtimeStatusHandler(_, _ string) (string, error) {
	return core.EncodeResult(cloudRealtime.snapshot())
}

var (
	startupMu      sync.Mutex
	startupPending chan struct{}
)

// StartupInBackground runs Startup off the caller's goroutine so session decryption
// (machine-key derivation) and storage scans never delay window creation. The
// returned channel closes once Startup has finished.
func StartupInBackground(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	startupMu.Lock()
	startupPending = done
	startupMu.Unlock()
	go func() {
		defer close(done)
		if err := Startup(ctx); err != nil {
			log.Printf("cloud companion startup completed with errors: %v", err)
		}
	}()
	return done
}

// afterStartup 让会修改会话或实时连接的命令等后台 Startup 结束，避免与恢复中的连接交错。
func afterStartup(handler core.Handler) core.Handler {
	return func(cmd, argsJSON string) (string, error) {
		startupMu.Lock()
		pending := startupPending
		startupMu.Unlock()
		if pending != nil {
			<-pending
		}
		return handler(cmd, argsJSON)
	}
}

// Startup registers this desktop and restores the realtime connection when a member session exists.
func Startup(_ context.Context) error {
	if err := cloudTerminals.start(); err != nil {
		return err
	}
	session, err := readSession()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			core.Notify(core.AppNotificationPayload{
				Source: "system",
				Level:  "error",
				Code:   "cloud.sessionInvalid",
			})
		}
		return nil
	}
	// Legacy device-token sessions can no longer authenticate; the user has to sign in again.
	if !isMemberSession(session) {
		return nil
	}
	identity, err := loadOrCreateIdentity()
	if err != nil {
		return err
	}
	startupRelayRegistration(session, identity)
	return nil
}

// startupRelayRegistration registers the device in the background so commands gated on Startup (status,
// logout, connect, revoke) never wait on the network, then connects the relay. A refused registration
// (device limit, key mismatch) cannot connect; any other failure is transient and the realtime loop
// retries on its own.
func startupRelayRegistration(session persistedSession, identity deviceIdentity) {
	registerRelayDeviceInBackground(session, identity, func(err error) {
		if errors.Is(err, errRelayDeviceLimit) || errors.Is(err, errRelayKeyMismatch) {
			return
		}
		startRealtimeAfterRegistration()
	})
}

// Shutdown closes the realtime connection and background reconnect loop.
func Shutdown() {
	// Cancel first: a background registration that already finished has started realtime under the
	// registration lock, so the stop below always wins.
	cancelRelayRegistration()
	cloudRealtime.stop()
	cloudTerminals.stopAll()
	cloudDatabases.closeAll()
}

func cloudPairingCreateHandler(_, argsJSON string) (string, error) {
	var args createPairingArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	session, identity, err := authenticatedCloudContext()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// The cloud validates the capability vocabulary and always adds AI_CHAT; send [] rather than null.
	capabilities := args.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	var challenge pairingChallenge
	if err := authorizedCloudPost(ctx, session, identity, "/pairings", map[string]any{"capabilities": capabilities}, &challenge); err != nil {
		return "", err
	}
	if challenge.ChallengeID == "" || challenge.PairingSecret == "" || challenge.ExpiresAt <= time.Now().Unix() {
		return "", errors.New("MinCode Cloud 返回的配对挑战无效")
	}
	return core.EncodeResult(challenge)
}

func cloudDevicesListHandler(_, _ string) (string, error) {
	session, identity, err := authenticatedCloudContext()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var devices serverDeviceList
	if err := authorizedCloudGet(ctx, session, identity, "/devices", &devices); err != nil {
		return "", err
	}
	return core.EncodeResult(devices)
}

// cloudPairedDevicesHandler lists the phones paired with this desktop, with their capabilities and grant.
func cloudPairedDevicesHandler(_, _ string) (string, error) {
	session, identity, err := authenticatedCloudContext()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	devices := []pairedDevice{}
	if err := authorizedCloudGet(ctx, session, identity, "/paired-devices", &devices); err != nil {
		return "", err
	}
	if devices == nil {
		devices = []pairedDevice{}
	}
	return core.EncodeResult(devices)
}

func cloudDeviceRevokeHandler(_, argsJSON string) (string, error) {
	var args revokeDeviceArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	args.DeviceID = strings.TrimSpace(args.DeviceID)
	if len(args.DeviceID) < 16 {
		return "", errors.New("设备 ID 无效")
	}
	session, identity, err := authenticatedCloudContext()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Revoking this desktop itself revokes all of its grants; the device stays registered and signed in.
	if err := authorizedCloudPost(ctx, session, identity, "/devices/revoke", map[string]string{"device_id": args.DeviceID}, nil); err != nil {
		return "", err
	}
	cloudRealtime.forgetDevices()
	return core.EncodeResult(map[string]bool{"revoked": true})
}

func authenticatedCloudContext() (persistedSession, deviceIdentity, error) {
	identity, err := loadOrCreateIdentity()
	if err != nil {
		return persistedSession{}, deviceIdentity{}, err
	}
	session, err := readSession()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return persistedSession{}, deviceIdentity{}, errors.New("请先登录 MinCode Cloud")
		}
		return persistedSession{}, deviceIdentity{}, err
	}
	if !isMemberSession(session) {
		return persistedSession{}, deviceIdentity{}, errors.New("云会话已过期，请重新登录 MinCode Cloud")
	}
	return session, identity, nil
}

func cloudRealtimeAckHandler(_, argsJSON string) (string, error) {
	var args relayAckArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.RequestID) == "" || strings.TrimSpace(args.RelayID) == "" {
		return "", errors.New("云消息确认参数无效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := cloudRealtime.acknowledge(ctx, args.RequestID, args.RelayID); err != nil {
		return "", err
	}
	return core.EncodeResult(map[string]bool{"acknowledged": true})
}

func cloudRealtimeSendHandler(_, argsJSON string) (string, error) {
	var args relaySendArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := sendEncryptedCloudPayload(
		strings.TrimSpace(args.TargetDeviceID),
		strings.TrimSpace(args.Channel),
		strings.TrimSpace(args.RequestID),
		args.Sequence,
		args.Payload,
	)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func sendEncryptedCloudPayload(
	targetDeviceID string,
	channel string,
	requestID string,
	sequence int64,
	payload map[string]any,
) (map[string]any, error) {
	if requestID == "" || len(targetDeviceID) < 16 || channel == "" || payload == nil {
		return nil, errors.New("云消息发送参数无效")
	}
	session, identity, err := authenticatedCloudContext()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Reuses the realtime manager's paired-device cache: terminal output sends one frame per chunk.
	target, err := cloudRealtime.cloudDevice(ctx, session, identity, targetDeviceID)
	if errors.Is(err, errDeviceNotPaired) {
		return nil, errors.New("目标云设备不存在或未授权")
	}
	if err != nil {
		return nil, err
	}
	key, err := deriveSharedKey(identity, target)
	if err != nil {
		return nil, err
	}
	ciphertext, err := encryptCloudPayload(payload, key, requestID, channel)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) > 88*1024 {
		return nil, errors.New("云消息密文超过单帧上限")
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer waitCancel()
	accepted, err := cloudRealtime.sendAndWaitAccepted(waitCtx, requestID, map[string]any{
		"type":           "relay",
		"requestId":      requestID,
		"targetDeviceId": targetDeviceID,
		"channel":        channel,
		"sequence":       sequence,
		"timestamp":      time.Now().Unix(),
		"ciphertext":     ciphertext,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"queued":    true,
		"relayId":   accepted.RelayID,
		"duplicate": accepted.Duplicate,
	}, nil
}
