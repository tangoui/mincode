package cloudcompanion

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	appcrypto "mincode/app/backend/crypto"
	"mincode/app/backend/storage"
	"mincode/app/backend/terminal"
)

const (
	sshProfilesKey    = "app_ssh_connections_v1"
	sshCredentialsKey = "app_ssh_credentials_v1"
)

type storedSSHProfiles struct {
	Profiles []storedSSHProfile `json:"profiles"`
}

type storedSSHProfile struct {
	ID                  string                            `json:"id"`
	Name                string                            `json:"name"`
	Host                string                            `json:"host"`
	Port                uint16                            `json:"port"`
	Username            string                            `json:"username"`
	AuthType            string                            `json:"authType"`
	KeyFile             string                            `json:"keyFile"`
	DefaultPath         string                            `json:"defaultPath"`
	AlgorithmPolicy     string                            `json:"algorithmPolicy"`
	KeepAlive           *storedKeepAlive                  `json:"keepAlive"`
	Reconnect           *storedReconnect                  `json:"reconnect"`
	JumpHost            *terminal.JumpHostPayload         `json:"jumpHost"`
	SocksProxy          *terminal.SocksProxyPayload       `json:"socksProxy"`
	PortForwards        []terminal.PortForwardRulePayload `json:"portForwards"`
	PostConnectCommands []string                          `json:"postConnectCommands"`
}

type storedKeepAlive struct {
	IntervalSeconds uint32 `json:"intervalSeconds"`
	MaxMisses       uint32 `json:"maxMisses"`
}

type storedReconnect struct {
	Enabled     bool   `json:"enabled"`
	MaxAttempts uint32 `json:"maxAttempts"`
	BaseDelayMs uint32 `json:"baseDelayMs"`
	MaxDelayMs  uint32 `json:"maxDelayMs"`
}

type storedSSHCredentials struct {
	CredentialsByProfileID map[string]map[string]string `json:"credentialsByProfileId"`
}

type PublicSSHProfile struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Host              string `json:"host"`
	Port              uint16 `json:"port"`
	Username          string `json:"username"`
	AuthType          string `json:"authType"`
	MobileSupported   bool   `json:"mobileSupported"`
	UnsupportedReason string `json:"unsupportedReason,omitempty"`
}

func listCloudSSHProfiles() ([]PublicSSHProfile, error) {
	profiles, err := loadSSHProfiles()
	if err != nil {
		return nil, err
	}
	result := make([]PublicSSHProfile, 0, len(profiles))
	for _, profile := range profiles {
		reason := mobileSSHUnsupportedReason(profile)
		result = append(result, PublicSSHProfile{
			ID: profile.ID, Name: profile.Name, Host: profile.Host,
			Port: profile.Port, Username: profile.Username, AuthType: profile.AuthType,
			MobileSupported: reason == "", UnsupportedReason: reason,
		})
	}
	return result, nil
}

func startCloudSSHProfile(profileID, sessionID string, cols, rows uint32) error {
	profiles, err := loadSSHProfiles()
	if err != nil {
		return err
	}
	var profile *storedSSHProfile
	for i := range profiles {
		if profiles[i].ID == profileID {
			profile = &profiles[i]
			break
		}
	}
	if profile == nil {
		return errors.New("SSH Profile 不存在")
	}
	if reason := mobileSSHUnsupportedReason(*profile); reason != "" {
		return errors.New(reason)
	}
	credentials, err := loadSSHCredentials(profileID)
	if err != nil {
		return err
	}
	payload := cloudSSHPayload(*profile, credentials, sessionID, cols, rows)
	return terminal.StartCloudSSH(payload)
}

// mobileSSHUnsupportedReason 只拒绝桌面端本身也不支持的认证方式。跳板机、SOCKS、端口转发、
// 登录后命令都由电脑端的 SSH 处理；交互式认证的验证码会转发到发起会话的手机（或用已保存的 TOTP 自动填写）。
// 端口转发监听在电脑上，手机无法直接访问这些端口。
func mobileSSHUnsupportedReason(profile storedSSHProfile) string {
	switch profile.AuthType {
	case "password", "key", "agent", "keyboard-interactive":
		return ""
	default:
		return "SSH 认证类型不受移动端支持"
	}
}

// cloudSSHPayload 用本机保存的 Profile 与凭据组装 SSH 启动参数，与桌面端自己发起 SSH 时一致
// （见 vue-app SshTerminal.vue 的 backendPayload）。不修改传入的 Profile。
func cloudSSHPayload(profile storedSSHProfile, credentials map[string]string, sessionID string, cols, rows uint32) terminal.StartSshShellPayload {
	payload := terminal.StartSshShellPayload{
		SessionID:           sessionID,
		Host:                profile.Host,
		Port:                profile.Port,
		Username:            profile.Username,
		AuthType:            profile.AuthType,
		KeyFile:             optionalPointer(profile.KeyFile),
		Password:            optionalPointer(credentials["password"]),
		Passphrase:          optionalPointer(credentials["passphrase"]),
		TotpURI:             optionalPointer(credentials["totpUri"]),
		AlgorithmPolicy:     profile.AlgorithmPolicy,
		InitialDirectory:    profile.DefaultPath,
		PortForwards:        profile.PortForwards,
		PostConnectCommands: profile.PostConnectCommands,
		Cols:                &cols,
		Rows:                &rows,
	}
	if profile.JumpHost != nil {
		jump := *profile.JumpHost
		jump.AlgorithmPolicy = profile.AlgorithmPolicy
		jump.Password = optionalPointer(credentials["jumpPassword"])
		jump.Passphrase = optionalPointer(credentials["jumpPassphrase"])
		payload.JumpHost = &jump
	}
	if profile.SocksProxy != nil {
		socks := *profile.SocksProxy
		socks.Password = optionalPointer(credentials["socksPassword"])
		payload.SocksProxy = &socks
	}
	if profile.KeepAlive != nil {
		payload.KeepAliveIntervalSeconds = profile.KeepAlive.IntervalSeconds
		payload.KeepAliveMaxMisses = profile.KeepAlive.MaxMisses
	}
	if profile.Reconnect != nil {
		payload.ReconnectEnabled = &profile.Reconnect.Enabled
		payload.ReconnectMaxAttempts = profile.Reconnect.MaxAttempts
		payload.ReconnectBaseDelayMs = profile.Reconnect.BaseDelayMs
		payload.ReconnectMaxDelayMs = profile.Reconnect.MaxDelayMs
	}
	return payload
}

func loadSSHProfiles() ([]storedSSHProfile, error) {
	raw, err := appKV(sshProfilesKey)
	if err != nil {
		return nil, err
	}
	var stored storedSSHProfiles
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("解析 SSH Profiles: %w", err)
	}
	return stored.Profiles, nil
}

func loadSSHCredentials(profileID string) (map[string]string, error) {
	raw, err := appKV(sshCredentialsKey)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var stored storedSSHCredentials
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("解析 SSH 凭据: %w", err)
	}
	result := map[string]string{}
	for key, cipher := range stored.CredentialsByProfileID[profileID] {
		plain, err := appcrypto.DecryptFromStorage(cipher)
		if err != nil {
			return nil, fmt.Errorf("解密 SSH 凭据: %w", err)
		}
		result[key] = plain
	}
	return result, nil
}

func appKV(key string) ([]byte, error) {
	db, err := storage.DB()
	if err != nil {
		return nil, err
	}
	var raw []byte
	if err := db.QueryRow("SELECT value FROM app_kv WHERE key = ?", key).Scan(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func optionalPointer(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
