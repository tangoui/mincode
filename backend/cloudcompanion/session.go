package cloudcompanion

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appcrypto "mincode/app/backend/crypto"
)

const sessionFileName = "cloud-session.enc"

func isMemberSession(session persistedSession) bool {
	return session.AuthProvider == "member" || session.AuthProvider == "buildadmin" // Keep previously persisted sessions readable.
}

var sessionMu sync.Mutex

type persistedSession struct {
	APIBaseURL   string    `json:"apiBaseUrl"`
	AccountID    int64     `json:"accountId"`
	Email        string    `json:"email"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	AuthProvider string    `json:"authProvider,omitempty"`
	// AccessTokenObtainedAt (Unix seconds) lets member calls reuse the access token until it
	// nears expiry. Older session files lack it (zero), which simply forces one refresh.
	AccessTokenObtainedAt int64 `json:"accessTokenObtainedAt,omitempty"`
}

type CloudStatus struct {
	Configured      bool                  `json:"configured"`
	SignedIn        bool                  `json:"signedIn"`
	Expired         bool                  `json:"expired,omitempty"`
	APIBaseURL      string                `json:"apiBaseUrl,omitempty"`
	AccountID       int64                 `json:"accountId,omitempty"`
	Email           string                `json:"email,omitempty"`
	ExpiresAt       *time.Time            `json:"expiresAt,omitempty"`
	Device          *PublicDeviceIdentity `json:"device,omitempty"`
	DeviceAvailable bool                  `json:"deviceAvailable"`
	// DeviceRegistrationError is the last failed POST /devices/register for this account and device
	// (for example the plan's device limit). Login and other features keep working regardless.
	DeviceRegistrationError string `json:"deviceRegistrationError,omitempty"`
	// DeviceRegistration is "pending", "failed" or "registered"; empty before any attempt.
	DeviceRegistration string `json:"deviceRegistration,omitempty"`
	// RegisteredNeedsLogin reports a completed registration that could not sign in on its own
	// (no refresh token and a login captcha is required); the UI should switch to login.
	RegisteredNeedsLogin bool `json:"registeredNeedsLogin,omitempty"`
}

func readSession() (persistedSession, error) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	return readSessionLocked()
}

func readSessionLocked() (persistedSession, error) {
	path, err := sessionPath()
	if err != nil {
		return persistedSession{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return persistedSession{}, err
	}
	plain, err := appcrypto.DecryptFromStorage(string(raw))
	if err != nil {
		return persistedSession{}, fmt.Errorf("解密云会话: %w", err)
	}
	var session persistedSession
	if err := json.Unmarshal([]byte(plain), &session); err != nil {
		return persistedSession{}, fmt.Errorf("解析云会话: %w", err)
	}
	return validatePersistedSession(session)
}

// replaceSessionIf atomically writes next only while the stored session still satisfies keep,
// so a token refresh never resurrects a session that was logged out or switched meanwhile.
func replaceSessionIf(keep func(persistedSession) bool, next persistedSession) (bool, error) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	current, err := readSessionLocked()
	if err != nil || !keep(current) {
		return false, nil
	}
	return true, writeSessionLocked(next)
}

func validatePersistedSession(session persistedSession) (persistedSession, error) {
	if session.APIBaseURL == "" || strings.TrimSpace(session.AccessToken) == "" || strings.TrimSpace(session.RefreshToken) == "" || (session.ExpiresAt.IsZero() && !isMemberSession(session)) {
		return persistedSession{}, errors.New("云会话数据不完整")
	}
	if session.AuthProvider != "" && !isMemberSession(session) {
		return persistedSession{}, errors.New("未知云会话类型")
	}
	if isMemberSession(session) && session.AccountID <= 0 {
		return persistedSession{}, errors.New("会员会话账户 ID 无效")
	}
	if session.AccountID < 0 || (session.Email != "" && strings.TrimSpace(session.Email) == "") {
		return persistedSession{}, errors.New("云会话账户数据无效")
	}
	baseURL, err := cloudAPIBaseURL(session.APIBaseURL)
	if err != nil || strings.TrimSpace(session.APIBaseURL) != session.APIBaseURL {
		return persistedSession{}, errors.New("云会话服务地址无效")
	}
	session.APIBaseURL = baseURL
	return session, nil
}

func writeSession(session persistedSession) error {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	return writeSessionLocked(session)
}

func writeSessionLocked(session persistedSession) error {
	path, err := sessionPath()
	if err != nil {
		return err
	}
	plain, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("编码云会话: %w", err)
	}
	ciphertext, err := appcrypto.EncryptForStorage(string(plain))
	if err != nil {
		return fmt.Errorf("加密云会话: %w", err)
	}
	return writeSecureFile(path, []byte(ciphertext))
}

func deleteSession() error {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	path, err := sessionPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除云会话: %w", err)
	}
	return nil
}

func sessionPath() (string, error) {
	root := identityRootDir()
	if root == "" {
		return "", errors.New("应用数据目录不可用")
	}
	return filepath.Join(root, identityDataDirectory, sessionFileName), nil
}

func writeSecureFile(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建安全存储目录: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".cloud-session-*.tmp")
	if err != nil {
		return fmt.Errorf("创建安全存储临时文件: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置安全存储权限: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入安全存储: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步安全存储: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭安全存储: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("替换安全存储: %w", err)
	}
	return nil
}

func statusFromSession(session persistedSession, identity deviceIdentity) CloudStatus {
	expiresAt := session.ExpiresAt
	registrationState, registrationError, refused, _ := relayRegistrationState(session, identity)
	return CloudStatus{
		Configured: true,
		SignedIn:   true,
		Expired:    !isMemberSession(session) && !cloudNow().Before(expiresAt),
		// Relay access uses the member token; only a fixed registration refusal (device limit, key
		// mismatch) makes the device unavailable. Legacy device-token sessions can no longer connect.
		DeviceAvailable:         isMemberSession(session) && !refused,
		DeviceRegistrationError: registrationError,
		DeviceRegistration:      registrationState,
		APIBaseURL:              session.APIBaseURL,
		AccountID:               session.AccountID,
		Email:                   session.Email,
		ExpiresAt: func() *time.Time {
			if isMemberSession(session) {
				return nil
			}
			return &expiresAt
		}(),
		Device: ptr(publicIdentity(identity)),
	}
}

func ptr[T any](value T) *T { return &value }

func statusWithoutSession() CloudStatus {
	identity, err := loadOrCreateIdentity()
	if err != nil {
		return CloudStatus{}
	}
	return CloudStatus{Configured: true, Device: ptr(publicIdentity(identity))}
}

func configuredServerURL(value string) (string, error) {
	return cloudAPIBaseURL(value)
}

func validateLogoutServerURL(sessionURL, requestedURL string) error {
	if strings.TrimSpace(requestedURL) == "" {
		return nil
	}
	baseURL, err := configuredServerURL(requestedURL)
	if err != nil {
		return err
	}
	if baseURL != sessionURL {
		return errors.New("登出地址与当前云会话不一致")
	}
	return nil
}
