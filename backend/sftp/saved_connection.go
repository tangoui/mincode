// 按连接 ID 读取 MinCode 已保存的 SSH 连接，供插件宿主的 host.ssh.files.* 无人值守地
// 打开 SFTP。凭据只在本包内解密，从不返回给调用方；调用方只拿到连接名、主机信息
// （用于授权弹窗）与资格判断。
//
// 数据来源（前端 stores/ssh-connections.ts 原样写入 app_kv，均为明文 JSON）：
//   - app_ssh_connections_v1  {version, profiles: [SshConnectionProfile]}
//   - app_ssh_credentials_v1  {version: 1, credentialsByProfileId: {<id>: {password?, passphrase?, ...}}}
//     其中每个值为 enc:v2: 机器密钥密文。
//
// 只允许不需要人工交互的连接：已保存密码，或无口令 / 口令已保存的私钥；不能有跳板机、
// keyboard-interactive、SSH Agent、一次性验证码；文件传输必须是 SFTP；主机指纹必须已在
// known_hosts 中确认。连接时使用严格的主机密钥校验，永不写 known_hosts。
package sftp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"

	appcrypto "mincode/app/backend/crypto"
	"mincode/app/backend/sshauth"
	"mincode/app/backend/storage"
)

const (
	savedProfilesKey    = "app_ssh_connections_v1"
	savedCredentialsKey = "app_ssh_credentials_v1"

	// savedDialTimeout bounds the TCP dial and the SSH handshake.
	savedDialTimeout = 30 * time.Second
)

// Reasons a saved connection cannot be used unattended.
const (
	ReasonInteractive       = "interactive"
	ReasonNoSavedCredential = "noSavedCredential"
	ReasonUnknownHostKey    = "unknownHostKey"
	ReasonJumpHost          = "jumpHost"
	ReasonNotSftp           = "notSftp"
)

// Error codes of SavedConnectionError.
const (
	SavedNotFound    = "notFound"
	SavedNotEligible = "notEligible"
	SavedHostKey     = "hostKey"
	SavedAuth        = "auth"
	SavedNetwork     = "network"
)

// SavedConnectionError classifies failures. Its message never contains
// credentials; the wrapped error may contain the host and is for logs only.
type SavedConnectionError struct {
	Code   string
	Reason string // set for SavedNotEligible
	Err    error
}

func (e *SavedConnectionError) Error() string {
	message := "saved SSH connection: " + e.Code
	if e.Reason != "" {
		message += " (" + e.Reason + ")"
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *SavedConnectionError) Unwrap() error { return e.Err }

// SavedErrorCode returns the SavedConnectionError code of err, or "".
func SavedErrorCode(err error) string {
	var saved *SavedConnectionError
	if errors.As(err, &saved) {
		return saved.Code
	}
	return ""
}

// SavedConnection is what callers may see of a saved connection. Host, Port
// and Username are for the host's own authorization prompt only.
type SavedConnection struct {
	ID       string
	Name     string
	Host     string
	Port     int
	Username string
	Eligible bool
	Reason   string
}

// savedAppKV reads one app_kv value (nil when missing); a seam for tests.
var savedAppKV = func(key string) ([]byte, error) {
	database, err := storage.ReadDB()
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = database.QueryRow("SELECT value FROM app_kv WHERE key = ?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

type savedProfile struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Host            string          `json:"host"`
	Port            int             `json:"port"`
	Username        string          `json:"username"`
	AuthType        string          `json:"authType"`
	KeyFile         string          `json:"keyFile"`
	SaveCredentials *bool           `json:"saveCredentials"`
	JumpHost        json.RawMessage `json:"jumpHost"`
	SocksProxy      *struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
	} `json:"socksProxy"`
	FileTransfer *struct {
		Protocol string `json:"protocol"`
	} `json:"fileTransfer"`
	AlgorithmPolicy string `json:"algorithmPolicy"`
}

type savedCredentials struct {
	Password      string
	Passphrase    string
	SocksPassword string
	TOTPURI       string
}

// savedRecord is a resolved profile with decrypted credentials; never leaves
// this package.
type savedRecord struct {
	public      SavedConnection
	profile     savedProfile
	credentials savedCredentials
}

func readSavedJSON(key string, target any) error {
	raw, err := savedAppKV(key)
	if err != nil {
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func loadSavedProfiles() ([]savedProfile, error) {
	var stored struct {
		Version  int               `json:"version"`
		Profiles []json.RawMessage `json:"profiles"`
	}
	if err := readSavedJSON(savedProfilesKey, &stored); err != nil {
		return nil, fmt.Errorf("read saved SSH connections: %w", err)
	}
	if stored.Version != 1 && stored.Version != 2 {
		return nil, nil
	}
	profiles := make([]savedProfile, 0, len(stored.Profiles))
	for _, raw := range stored.Profiles {
		var profile savedProfile
		// Malformed entries are skipped, as the frontend restore does.
		if json.Unmarshal(raw, &profile) != nil || profile.ID == "" || strings.TrimSpace(profile.Host) == "" ||
			strings.TrimSpace(profile.Username) == "" || profile.Port < 1 || profile.Port > 65535 {
			continue
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

// loadSavedCredentials returns the encrypted credential fields by profile ID.
func loadSavedCredentials() (map[string]map[string]string, error) {
	var stored struct {
		Version                int                       `json:"version"`
		CredentialsByProfileID map[string]map[string]any `json:"credentialsByProfileId"`
	}
	if err := readSavedJSON(savedCredentialsKey, &stored); err != nil {
		return nil, fmt.Errorf("read saved SSH credentials: %w", err)
	}
	out := map[string]map[string]string{}
	if stored.Version != 1 {
		return out, nil
	}
	for id, fields := range stored.CredentialsByProfileID {
		values := map[string]string{}
		for name, value := range fields {
			if text, ok := value.(string); ok && strings.HasPrefix(text, "enc:v2:") {
				values[name] = text
			}
		}
		out[id] = values
	}
	return out, nil
}

// decryptSaved decrypts one stored field; "" when absent or unreadable
// (ciphertext from another machine counts as no saved credential).
func decryptSaved(fields map[string]string, name string) (string, bool) {
	sealed, ok := fields[name]
	if !ok {
		return "", true
	}
	plain, err := appcrypto.DecryptFromStorage(sealed)
	if err != nil {
		return "", false
	}
	return plain, true
}

func resolveSaved(profile savedProfile, encrypted map[string]string) *savedRecord {
	host, username := normalizeSSHHostUser(profile.Host, profile.Username)
	record := &savedRecord{
		public:  SavedConnection{ID: profile.ID, Name: profile.Name, Host: host, Port: profile.Port, Username: username},
		profile: profile,
	}
	record.public.Reason = savedIneligibility(record, host, encrypted)
	record.public.Eligible = record.public.Reason == ""
	return record
}

func savedIneligibility(record *savedRecord, host string, encrypted map[string]string) string {
	profile := record.profile
	if profile.FileTransfer != nil && profile.FileTransfer.Protocol != "" && profile.FileTransfer.Protocol != "sftp" {
		return ReasonNotSftp
	}
	if len(profile.JumpHost) > 0 && string(profile.JumpHost) != "null" {
		return ReasonJumpHost
	}
	authType := strings.ToLower(strings.TrimSpace(profile.AuthType))
	if authType != "password" && authType != "key" {
		// agent may prompt or be absent when unattended; keyboard-interactive needs a person.
		return ReasonInteractive
	}
	if _, hasTOTP := encrypted["totpUri"]; hasTOTP {
		return ReasonInteractive
	}
	if profile.SaveCredentials != nil && !*profile.SaveCredentials {
		return ReasonNoSavedCredential
	}
	var ok bool
	credentials := &record.credentials
	for _, field := range []struct {
		name  string
		value *string
	}{{"password", &credentials.Password}, {"passphrase", &credentials.Passphrase}, {"socksPassword", &credentials.SocksPassword}} {
		if *field.value, ok = decryptSaved(encrypted, field.name); !ok {
			return ReasonNoSavedCredential
		}
	}
	switch authType {
	case "password":
		if credentials.Password == "" {
			return ReasonNoSavedCredential
		}
	case "key":
		if _, err := loadSSHSigner(profile.KeyFile, credentials.Passphrase); err != nil {
			return ReasonNoSavedCredential
		}
	}
	if profile.SocksProxy != nil && strings.TrimSpace(profile.SocksProxy.Username) != "" && credentials.SocksPassword == "" {
		return ReasonNoSavedCredential
	}
	if known, err := sshauth.HasKnownHost(host, profile.Port); err != nil || !known {
		return ReasonUnknownHostKey
	}
	return ""
}

func loadSavedRecords() ([]*savedRecord, error) {
	profiles, err := loadSavedProfiles()
	if err != nil {
		return nil, err
	}
	credentials, err := loadSavedCredentials()
	if err != nil {
		return nil, err
	}
	records := make([]*savedRecord, 0, len(profiles))
	for _, profile := range profiles {
		records = append(records, resolveSaved(profile, credentials[profile.ID]))
	}
	return records, nil
}

// ListSavedConnections lists every saved SSH connection with its eligibility
// for unattended use.
func ListSavedConnections() ([]SavedConnection, error) {
	records, err := loadSavedRecords()
	if err != nil {
		return nil, err
	}
	out := make([]SavedConnection, 0, len(records))
	for _, record := range records {
		out = append(out, record.public)
	}
	return out, nil
}

func findSavedRecord(connectionID string) (*savedRecord, error) {
	records, err := loadSavedRecords()
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.public.ID == connectionID {
			return record, nil
		}
	}
	return nil, &SavedConnectionError{Code: SavedNotFound}
}

// SavedConnectionByID returns one saved connection (SavedNotFound if absent).
func SavedConnectionByID(connectionID string) (SavedConnection, error) {
	record, err := findSavedRecord(connectionID)
	if err != nil {
		return SavedConnection{}, err
	}
	return record.public, nil
}

// OpenSavedSFTP opens an SFTP client on an eligible saved connection with
// strict host key checking. Cancelling ctx closes the connection; the
// returned func closes it too and may be called more than once.
func OpenSavedSFTP(ctx context.Context, connectionID string) (*sftp.Client, func(), error) {
	record, err := findSavedRecord(connectionID)
	if err != nil {
		return nil, nil, err
	}
	if !record.public.Eligible {
		return nil, nil, &SavedConnectionError{Code: SavedNotEligible, Reason: record.public.Reason}
	}
	profile := record.profile
	params := sshConnectParams{
		host: record.public.Host, port: profile.Port, username: record.public.Username,
		authType: profile.AuthType, password: record.credentials.Password, keyFile: profile.KeyFile,
		passphrase: record.credentials.Passphrase, algorithmPolicy: profile.AlgorithmPolicy,
		hostKeyCallback: sshauth.StrictHostKeyCallback(record.public.Host, profile.Port),
		timeout:         savedDialTimeout,
		ctx:             ctx,
	}
	if profile.SocksProxy != nil {
		socks := sshauth.SocksProxyConfig{Host: strings.TrimSpace(profile.SocksProxy.Host), Port: profile.SocksProxy.Port,
			Username: profile.SocksProxy.Username, Password: record.credentials.SocksPassword}
		if err := socks.Validate(); err != nil {
			return nil, nil, &SavedConnectionError{Code: SavedNetwork, Err: err}
		}
		params.socksProxy = &socks
	}
	sshClient, err := dialSSH(params)
	if err != nil {
		return nil, nil, classifySavedDialError(err)
	}
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return nil, nil, &SavedConnectionError{Code: SavedNetwork, Err: err}
	}
	var once sync.Once
	var stop func() bool
	closeAll := func() {
		once.Do(func() {
			if stop != nil {
				stop()
			}
			_ = client.Close()
			_ = sshClient.Close()
		})
	}
	stop = context.AfterFunc(ctx, closeAll)
	return client, closeAll, nil
}

func classifySavedDialError(err error) error {
	switch {
	case errors.Is(err, sshauth.ErrUnknownHostKey), errors.Is(err, sshauth.ErrHostKeyChanged):
		return &SavedConnectionError{Code: SavedHostKey, Err: err}
	case strings.Contains(err.Error(), "unable to authenticate"), strings.Contains(err.Error(), "加载私钥失败"),
		strings.Contains(err.Error(), "未提供可用的 SSH 认证凭据"):
		return &SavedConnectionError{Code: SavedAuth, Err: err}
	}
	// Everything else (refused, timeouts, resets, failed negotiation) is
	// reported as a network failure.
	return &SavedConnectionError{Code: SavedNetwork, Err: err}
}
