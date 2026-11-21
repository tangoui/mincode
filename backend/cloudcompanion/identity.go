// Package cloudcompanion owns the desktop-side cloud identity and enrollment
// boundary. Remote relay execution is added after this identity layer is stable.
package cloudcompanion

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"mincode/app/backend/core"
	appcrypto "mincode/app/backend/crypto"
)

const identityFileName = "cloud-device-identity.enc"

var (
	identityMu      sync.Mutex
	identityRootDir = core.UserDataDir
)

type deviceIdentity struct {
	DeviceID             string `json:"deviceId"`
	SigningPublicKey     string `json:"signingPublicKey"`
	SigningPrivateKey    string `json:"signingPrivateKey"`
	EncryptionPublicKey  string `json:"encryptionPublicKey"`
	EncryptionPrivateKey string `json:"encryptionPrivateKey"`
}

type PublicDeviceIdentity struct {
	DeviceID            string `json:"deviceId"`
	SigningPublicKey    string `json:"signingPublicKey"`
	EncryptionPublicKey string `json:"encryptionPublicKey"`
}

func loadOrCreateIdentity() (deviceIdentity, error) {
	identityMu.Lock()
	defer identityMu.Unlock()

	path, err := identityPath()
	if err != nil {
		return deviceIdentity{}, err
	}
	if identity, err := readIdentity(path); err == nil {
		return identity, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return deviceIdentity{}, err
	}

	identity, err := generateIdentity()
	if err != nil {
		return deviceIdentity{}, err
	}
	if err := writeIdentity(path, identity); err != nil {
		return deviceIdentity{}, err
	}
	return identity, nil
}

func generateIdentity() (deviceIdentity, error) {
	signingPublic, signingPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return deviceIdentity{}, fmt.Errorf("生成设备签名密钥: %w", err)
	}
	encryptionPrivate, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return deviceIdentity{}, fmt.Errorf("生成设备加密密钥: %w", err)
	}
	randomID := make([]byte, 18)
	if _, err := rand.Read(randomID); err != nil {
		return deviceIdentity{}, fmt.Errorf("生成设备 ID: %w", err)
	}

	return deviceIdentity{
		DeviceID:             "desktop-" + base64.RawURLEncoding.EncodeToString(randomID),
		SigningPublicKey:     base64.StdEncoding.EncodeToString(signingPublic),
		SigningPrivateKey:    base64.StdEncoding.EncodeToString(signingPrivate),
		EncryptionPublicKey:  base64.StdEncoding.EncodeToString(encryptionPrivate.PublicKey().Bytes()),
		EncryptionPrivateKey: base64.StdEncoding.EncodeToString(encryptionPrivate.Bytes()),
	}, nil
}

func readIdentity(path string) (deviceIdentity, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return deviceIdentity{}, err
	}
	plain, err := appcrypto.DecryptFromStorage(string(raw))
	if err != nil {
		return deviceIdentity{}, fmt.Errorf("解密云设备身份: %w", err)
	}
	var identity deviceIdentity
	if err := json.Unmarshal([]byte(plain), &identity); err != nil {
		return deviceIdentity{}, fmt.Errorf("解析云设备身份: %w", err)
	}
	if err := validateIdentity(identity); err != nil {
		return deviceIdentity{}, err
	}
	return identity, nil
}

func writeIdentity(path string, identity deviceIdentity) error {
	plain, err := json.Marshal(identity)
	if err != nil {
		return fmt.Errorf("编码云设备身份: %w", err)
	}
	ciphertext, err := appcrypto.EncryptForStorage(string(plain))
	if err != nil {
		return fmt.Errorf("加密云设备身份: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建云设备身份目录: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".cloud-device-*.tmp")
	if err != nil {
		return fmt.Errorf("创建云设备身份临时文件: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置云设备身份权限: %w", err)
	}
	if _, err := tmp.WriteString(ciphertext); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入云设备身份: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步云设备身份: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭云设备身份: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("替换云设备身份: %w", err)
	}
	return nil
}

func validateIdentity(identity deviceIdentity) error {
	if len(identity.DeviceID) < 16 {
		return errors.New("云设备 ID 无效")
	}
	signingPublic, err := base64.StdEncoding.DecodeString(identity.SigningPublicKey)
	if err != nil || len(signingPublic) != ed25519.PublicKeySize {
		return errors.New("云设备签名公钥无效")
	}
	signingPrivate, err := base64.StdEncoding.DecodeString(identity.SigningPrivateKey)
	if err != nil || len(signingPrivate) != ed25519.PrivateKeySize {
		return errors.New("云设备签名私钥无效")
	}
	encryptionPublic, err := base64.StdEncoding.DecodeString(identity.EncryptionPublicKey)
	if err != nil || len(encryptionPublic) != 32 {
		return errors.New("云设备加密公钥无效")
	}
	encryptionPrivate, err := base64.StdEncoding.DecodeString(identity.EncryptionPrivateKey)
	if err != nil || len(encryptionPrivate) != 32 {
		return errors.New("云设备加密私钥无效")
	}
	return nil
}

func identityPath() (string, error) {
	root := identityRootDir()
	if root == "" {
		return "", errors.New("应用数据目录不可用")
	}
	return filepath.Join(root, identityDataDirectory, identityFileName), nil
}

func publicIdentity(identity deviceIdentity) PublicDeviceIdentity {
	return PublicDeviceIdentity{
		DeviceID:            identity.DeviceID,
		SigningPublicKey:    identity.SigningPublicKey,
		EncryptionPublicKey: identity.EncryptionPublicKey,
	}
}
