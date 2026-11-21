package cloudcompanion

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

type encryptedPayload struct {
	Version    int    `json:"v"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	MAC        string `json:"mac"`
}

func deriveSharedKey(identity deviceIdentity, remote serverDevice) ([]byte, error) {
	privateBytes, err := base64.StdEncoding.DecodeString(identity.EncryptionPrivateKey)
	if err != nil {
		return nil, errors.New("云设备加密私钥无效")
	}
	remoteBytes, err := base64.StdEncoding.DecodeString(remote.EncryptionPublicKey)
	if err != nil {
		return nil, errors.New("远端设备加密公钥无效")
	}
	curve := ecdh.X25519()
	privateKey, err := curve.NewPrivateKey(privateBytes)
	if err != nil {
		return nil, errors.New("云设备加密私钥无效")
	}
	remoteKey, err := curve.NewPublicKey(remoteBytes)
	if err != nil {
		return nil, errors.New("远端设备加密公钥无效")
	}
	shared, err := privateKey.ECDH(remoteKey)
	if err != nil {
		return nil, fmt.Errorf("协商云设备共享密钥: %w", err)
	}
	key, err := hkdf.Key(
		sha256.New,
		shared,
		nil,
		string(cloudSharedKeyInfo(identity.DeviceID, remote.DeviceID)),
		32,
	)
	if err != nil {
		return nil, fmt.Errorf("派生云设备共享密钥: %w", err)
	}
	return key, nil
}

func encryptCloudPayload(
	payload map[string]any,
	key []byte,
	requestID string,
	channel string,
) (string, error) {
	plain, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("编码云业务数据: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成云密文 nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, plain, cloudPayloadAAD(requestID, channel))
	ciphertext := sealed[:len(sealed)-gcm.Overhead()]
	mac := sealed[len(sealed)-gcm.Overhead():]
	raw, err := json.Marshal(encryptedPayload{
		Version:    1,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
		MAC:        base64.StdEncoding.EncodeToString(mac),
	})
	if err != nil {
		return "", fmt.Errorf("编码云密文: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func decryptCloudPayload(
	encoded string,
	key []byte,
	requestID string,
	channel string,
) (map[string]any, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("云密文不是有效 Base64")
	}
	var payload encryptedPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Version != 1 {
		return nil, errors.New("云密文格式无效")
	}
	nonce, err := base64.StdEncoding.DecodeString(payload.Nonce)
	if err != nil {
		return nil, errors.New("云密文 nonce 无效")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(payload.Ciphertext)
	if err != nil {
		return nil, errors.New("云密文正文无效")
	}
	mac, err := base64.StdEncoding.DecodeString(payload.MAC)
	if err != nil {
		return nil, errors.New("云密文 MAC 无效")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() || len(mac) != gcm.Overhead() {
		return nil, errors.New("云密文参数长度无效")
	}
	sealed := append(append([]byte(nil), ciphertext...), mac...)
	plain, err := gcm.Open(nil, nonce, sealed, cloudPayloadAAD(requestID, channel))
	if err != nil {
		return nil, errors.New("云密文认证失败")
	}
	var result map[string]any
	if err := json.Unmarshal(plain, &result); err != nil || result == nil {
		return nil, errors.New("云密文业务数据无效")
	}
	return result, nil
}

func cloudSharedKeyInfo(localDeviceID, remoteDeviceID string) []byte {
	first, second := localDeviceID, remoteDeviceID
	if first > second {
		first, second = second, first
	}
	return []byte("mincode-cloud-e2ee-v1:" + first + ":" + second)
}

func cloudPayloadAAD(requestID, channel string) []byte {
	return []byte("mincode-cloud-envelope-v1\n" + requestID + "\n" + channel)
}
