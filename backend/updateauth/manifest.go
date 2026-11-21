// Package updateauth defines MinCode's signed update-manifest wire format.
package updateauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type Platform struct {
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	FileSize int64  `json:"file_size"`
}

type Manifest struct {
	Version     string              `json:"version"`
	VersionCode int64               `json:"version_code"`
	Notes       string              `json:"notes"`
	PubDate     *string             `json:"pub_date"`
	Platforms   map[string]Platform `json:"platforms"`
	Signature   string              `json:"signature,omitempty"`
}

func SigningPayload(manifest Manifest) ([]byte, error) {
	payload := struct {
		Version     string              `json:"version"`
		VersionCode int64               `json:"version_code"`
		Notes       string              `json:"notes"`
		PubDate     *string             `json:"pub_date"`
		Platforms   map[string]Platform `json:"platforms"`
	}{manifest.Version, manifest.VersionCode, manifest.Notes, manifest.PubDate, manifest.Platforms}
	return json.Marshal(payload)
}

func DecodeBase64(label, value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		decoded, err := encoding.DecodeString(value)
		if err == nil {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("%s不是有效的 Base64", label)
}

func Sign(manifest *Manifest, privateKeyValue string) error {
	privateKey, err := DecodeBase64("更新签名私钥", privateKeyValue)
	if err != nil {
		return err
	}
	if len(privateKey) == ed25519.SeedSize {
		privateKey = ed25519.NewKeyFromSeed(privateKey)
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("更新签名私钥长度无效")
	}
	payload, err := SigningPayload(*manifest)
	if err != nil {
		return fmt.Errorf("编码更新签名载荷: %w", err)
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(privateKey), payload))
	return nil
}

func Verify(manifest Manifest, publicKeyValue string) error {
	publicKey, err := DecodeBase64("更新签名公钥", publicKeyValue)
	if err != nil {
		return err
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("更新签名公钥长度无效")
	}
	if strings.TrimSpace(manifest.Signature) == "" {
		return fmt.Errorf("更新清单缺少签名")
	}
	signature, err := DecodeBase64("更新清单签名", manifest.Signature)
	if err != nil {
		return err
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("更新清单签名长度无效")
	}
	payload, err := SigningPayload(manifest)
	if err != nil {
		return fmt.Errorf("编码更新签名载荷: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return fmt.Errorf("更新清单签名验证失败")
	}
	return nil
}
