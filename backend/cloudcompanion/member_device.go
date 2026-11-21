package cloudcompanion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"mincode/app/backend/core"
	"mincode/app/backend/misc"
)

// DeviceLimitError means the cloud refused to register this device because the
// member's plan already has its maximum number of active devices.
type DeviceLimitError struct {
	Current int64
	Limit   int64
}

func (e *DeviceLimitError) Error() string {
	return fmt.Sprintf("当前账号的设备数已达上限（%d/%d），本机按免费版额度使用；可在 设置 → 云账号 移除不用的设备", e.Current, e.Limit)
}

// MemberDevice is a registered device (desktop or phone) of the signed-in member, as reported by the cloud.
// Only desktops count toward the plan's device limit.
type MemberDevice struct {
	DeviceID    string `json:"deviceId"`
	DeviceType  string `json:"deviceType"`
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	AppVersion  string `json:"appVersion"`
	FirstSeenAt int64  `json:"firstSeenAt"`
	LastSeenAt  int64  `json:"lastSeenAt"`
	Current     bool   `json:"current"`
}

var memberDeviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

func init() {
	core.Register("cloud_member_devices", cloudMemberDevicesHandler)
	core.Register("cloud_member_device_remove", cloudMemberDeviceRemoveHandler)
}

func memberDeviceQuery(identity deviceIdentity) url.Values {
	name, _ := os.Hostname()
	return url.Values{
		"device_id":   {identity.DeviceID},
		"device_name": {core.TakeChars(strings.TrimSpace(name), 120)},
		"platform":    {runtime.GOOS + "/" + runtime.GOARCH},
		"app_version": {misc.AppVersion},
		// The cloud issues the schema 3 snapshot (with the plugin limit) only when asked.
		"schema_version": {"3"},
	}
}

func parseDeviceLimit(data json.RawMessage) error {
	var body struct {
		Reason  string `json:"reason"`
		Current int64  `json:"current"`
		Limit   int64  `json:"limit"`
	}
	if json.Unmarshal(data, &body) != nil || body.Reason != "device_limit" || body.Limit < 1 || body.Current < 0 {
		return nil
	}
	return &DeviceLimitError{Current: body.Current, Limit: body.Limit}
}

// memberRequest performs one authenticated member call against the session's own server.
func memberRequest(ctx context.Context, method, path string, form url.Values, result any) (persistedSession, error) {
	session, err := readSession()
	if errors.Is(err, os.ErrNotExist) {
		return persistedSession{}, errors.New("请先登录云账号")
	}
	if err != nil {
		return persistedSession{}, fmt.Errorf("读取云账号会话: %w", err)
	}
	if !isMemberSession(session) {
		return persistedSession{}, errors.New("当前会话不是云端会员账号")
	}
	err = withMemberToken(ctx, session, func(token string) error {
		if method == http.MethodPost {
			return cloudPost(ctx, session.APIBaseURL, strings.TrimPrefix(path, "/api/"), form, token, result)
		}
		status, envelope, err := memberGet(ctx, session.APIBaseURL, path, token, 256*1024)
		if err != nil {
			return err
		}
		if status != http.StatusOK || envelope.Code != 1 || json.Unmarshal(envelope.Data, result) != nil {
			return errors.New("云端响应无效或会话已失效")
		}
		return nil
	})
	if err != nil {
		return persistedSession{}, err
	}
	if !sameMemberSession(session) {
		return persistedSession{}, errors.New("云账号会话已切换，请重试")
	}
	return session, nil
}

func cloudMemberDevicesHandler(_, _ string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	var body struct {
		Items []struct {
			DeviceID    string `json:"device_id"`
			DeviceType  string `json:"device_type"`
			Name        string `json:"name"`
			Platform    string `json:"platform"`
			AppVersion  string `json:"app_version"`
			FirstSeenAt int64  `json:"first_seen_at"`
			LastSeenAt  int64  `json:"last_seen_at"`
		} `json:"items"`
	}
	if _, err := memberRequest(ctx, http.MethodGet, "/api/mincode/memberDevice/index", nil, &body); err != nil {
		return "", fmt.Errorf("获取设备列表: %w", err)
	}
	identity, _ := loadOrCreateIdentity()
	devices := make([]MemberDevice, 0, len(body.Items))
	for _, item := range body.Items {
		if !memberDeviceIDPattern.MatchString(item.DeviceID) {
			continue
		}
		devices = append(devices, MemberDevice{
			DeviceID: item.DeviceID, DeviceType: memberDeviceType(item.DeviceType, item.DeviceID), Name: core.TakeChars(item.Name, 120), Platform: core.TakeChars(item.Platform, 32),
			AppVersion: core.TakeChars(item.AppVersion, 64), FirstSeenAt: item.FirstSeenAt, LastSeenAt: item.LastSeenAt,
			Current: item.DeviceID == identity.DeviceID,
		})
	}
	return core.EncodeResult(devices)
}

// memberDeviceType normalizes the cloud's device_type to "desktop" or "mobile"; servers that predate the
// field are read from the device_id prefix.
func memberDeviceType(reported, deviceID string) string {
	switch reported {
	case "desktop", "mobile":
		return reported
	}
	if strings.HasPrefix(deviceID, "mobile-") {
		return "mobile"
	}
	return "desktop"
}

func cloudMemberDeviceRemoveHandler(_, argsJSON string) (string, error) {
	var args struct {
		DeviceID string `json:"deviceId"`
	}
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if !memberDeviceIDPattern.MatchString(args.DeviceID) {
		return "", errors.New("设备 ID 无效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	defer cancel()
	var result struct {
		Removed bool `json:"removed"`
	}
	if _, err := memberRequest(ctx, http.MethodPost, "/api/mincode/memberDevice/remove", url.Values{"device_id": {args.DeviceID}}, &result); err != nil {
		return "", fmt.Errorf("移除设备: %w", err)
	}
	return core.EncodeResult(result.Removed)
}

var (
	// errRelayDeviceLimit: the cloud refused POST /devices/register because the plan's desktop limit is reached.
	errRelayDeviceLimit = errors.New("relay device limit reached")
	// errRelayKeyMismatch: this device_id is registered with a different signing key.
	errRelayKeyMismatch = errors.New("relay device key mismatch")
)

// relayRegistrationError is a fixed registration refusal; it matches its sentinel with errors.Is.
type relayRegistrationError struct {
	kind    error
	message string
}

func (e *relayRegistrationError) Error() string { return e.message }
func (e *relayRegistrationError) Unwrap() error { return e.kind }

// registerRelayDevice upserts this desktop with POST /devices/register so it can pair and use the relay.
func registerRelayDevice(ctx context.Context, session persistedSession, identity deviceIdentity) error {
	if !isMemberSession(session) {
		return errors.New("请重新登录 MinCode Cloud")
	}
	var result struct {
		Device serverDevice `json:"device"`
	}
	err := authorizedCloudPost(ctx, session, identity, "/devices/register", desktopRegistrationPayload(identity, ""), &result)
	if err != nil {
		var refused *cloudAPIError
		if errors.As(err, &refused) {
			switch refused.reason() {
			case "device_limit":
				message := "当前账号的设备数已达上限，本机按免费版额度使用；可在 设置 → 云账号 移除不用的设备"
				var limit *DeviceLimitError
				if errors.As(parseDeviceLimit(refused.Data), &limit) {
					message = limit.Error()
				}
				return &relayRegistrationError{kind: errRelayDeviceLimit, message: message}
			case "key_mismatch":
				return &relayRegistrationError{kind: errRelayKeyMismatch, message: "本机设备密钥与云端登记的不一致，无法接入手机中继"}
			}
		}
		return fmt.Errorf("登记云设备: %w", err)
	}
	if result.Device.DeviceID != identity.DeviceID {
		return errors.New("MinCode Cloud 返回的设备登记无效")
	}
	return nil
}

// Registration states reported to the UI as CloudStatus.DeviceRegistration.
const (
	relayRegistrationPending    = "pending"
	relayRegistrationFailed     = "failed"
	relayRegistrationRegistered = "registered"
)

// relayRegistration remembers the registration outcome per (server, account, device) for the status shown
// to the UI, and owns the single in-flight background registration. A failed registration never blocks
// login, status or other features.
var relayRegistration struct {
	sync.Mutex
	key        string
	state      string
	message    string
	refused    bool
	generation uint64             // bumped by every attempt; stale results are discarded
	cancel     context.CancelFunc // cancels the in-flight background attempt, nil when idle
}

func relayRegistrationKey(session persistedSession, identity deviceIdentity) string {
	return fmt.Sprintf("%s|%d|%s", session.APIBaseURL, session.AccountID, identity.DeviceID)
}

// relayRegistrationState reports the recorded outcome for session: known is false before any attempt.
func relayRegistrationState(session persistedSession, identity deviceIdentity) (state, message string, refused, known bool) {
	relayRegistration.Lock()
	defer relayRegistration.Unlock()
	if relayRegistration.key != relayRegistrationKey(session, identity) {
		return "", "", false, false
	}
	return relayRegistration.state, relayRegistration.message, relayRegistration.refused, true
}

// beginRelayRegistrationLocked marks a new attempt for key as pending and returns its generation. A
// previous result for the same key stays visible (message, refused) until the attempt finishes.
func beginRelayRegistrationLocked(key string) uint64 {
	if relayRegistration.cancel != nil {
		relayRegistration.cancel()
		relayRegistration.cancel = nil
	}
	if relayRegistration.key != key {
		relayRegistration.message = ""
		relayRegistration.refused = false
	}
	relayRegistration.key = key
	relayRegistration.state = relayRegistrationPending
	relayRegistration.generation++
	return relayRegistration.generation
}

// finishRelayRegistrationLocked records err for generation unless a newer attempt superseded it.
func finishRelayRegistrationLocked(generation uint64, err error) bool {
	if relayRegistration.generation != generation {
		return false
	}
	relayRegistration.cancel = nil
	relayRegistration.message = ""
	relayRegistration.refused = errors.Is(err, errRelayDeviceLimit) || errors.Is(err, errRelayKeyMismatch)
	relayRegistration.state = relayRegistrationRegistered
	if err != nil {
		relayRegistration.message = core.TakeChars(err.Error(), 240)
		relayRegistration.state = relayRegistrationFailed
		log.Printf("cloud relay device registration failed: %s", relayRegistration.message)
	}
	return true
}

// registerRelayDeviceAndRecord registers the device synchronously (login, explicit connect) and records
// the outcome for the status. It supersedes any background attempt.
func registerRelayDeviceAndRecord(ctx context.Context, session persistedSession, identity deviceIdentity) error {
	relayRegistration.Lock()
	generation := beginRelayRegistrationLocked(relayRegistrationKey(session, identity))
	relayRegistration.Unlock()
	err := registerRelayDevice(ctx, session, identity)
	relayRegistration.Lock()
	finishRelayRegistrationLocked(generation, err)
	relayRegistration.Unlock()
	return err
}

// registerRelayDeviceInBackground starts one bounded background registration for session, so Startup and
// status never wait on the network. It is single-flight: while an attempt for the same account and device
// is pending it returns false and starts nothing. done, when set, runs with the result unless the attempt
// was superseded or cancelled; it runs under the registration lock so cancelRelayRegistration (Shutdown)
// either happens before it or observes its effects.
func registerRelayDeviceInBackground(session persistedSession, identity deviceIdentity, done func(error)) bool {
	key := relayRegistrationKey(session, identity)
	relayRegistration.Lock()
	if relayRegistration.key == key && relayRegistration.state == relayRegistrationPending {
		relayRegistration.Unlock()
		return false
	}
	generation := beginRelayRegistrationLocked(key)
	ctx, cancel := context.WithTimeout(context.Background(), cloudHTTPTimeout)
	relayRegistration.cancel = cancel
	relayRegistration.Unlock()
	go func() {
		defer cancel()
		err := registerRelayDevice(ctx, session, identity)
		relayRegistration.Lock()
		defer relayRegistration.Unlock()
		if ctx.Err() == context.Canceled {
			// Superseded or shut down: leave the state to whoever cancelled it.
			return
		}
		if finishRelayRegistrationLocked(generation, err) && done != nil {
			done(err)
		}
	}()
	return true
}

// cancelRelayRegistration stops an in-flight background registration (Shutdown, logout).
func cancelRelayRegistration() {
	relayRegistration.Lock()
	defer relayRegistration.Unlock()
	if relayRegistration.cancel != nil {
		relayRegistration.cancel()
		relayRegistration.cancel = nil
		if relayRegistration.state == relayRegistrationPending {
			relayRegistration.state = ""
		}
		relayRegistration.generation++
	}
}
