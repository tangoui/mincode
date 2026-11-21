package cloudcompanion

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestDecryptCloudPayloadMatchesMobileEnvelope(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	requestID := "request-1234567890"
	channel := "ai.chat.message"
	sealed := gcm.Seal(nil, nonce, []byte(`{"message":"hello"}`), cloudPayloadAAD(requestID, channel))
	ciphertext := sealed[:len(sealed)-gcm.Overhead()]
	mac := sealed[len(sealed)-gcm.Overhead():]
	payload, _ := json.Marshal(encryptedPayload{
		Version:    1,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
		MAC:        base64.StdEncoding.EncodeToString(mac),
	})
	encoded := base64.StdEncoding.EncodeToString(payload)

	decoded, err := decryptCloudPayload(encoded, key, requestID, channel)
	if err != nil {
		t.Fatal(err)
	}
	if decoded["message"] != "hello" {
		t.Fatalf("unexpected payload: %#v", decoded)
	}
	if _, err := decryptCloudPayload(encoded, key, requestID, "terminal.input"); err == nil {
		t.Fatal("changing channel must fail authentication")
	}
}

func TestCloudSharedKeyInfoIsSymmetric(t *testing.T) {
	left := string(cloudSharedKeyInfo("mobile-b", "desktop-a"))
	right := string(cloudSharedKeyInfo("desktop-a", "mobile-b"))
	if left != right {
		t.Fatalf("device order changed HKDF info: %q != %q", left, right)
	}
}

func TestDecryptCloudPayloadCrossLanguageVector(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	const encoded = "eyJjaXBoZXJ0ZXh0IjoiRko5WHJSaXV0aExUSnVJdnRzbUpKSkk4OHczTUQ0L0JqamVvODNObjdaND0iLCJtYWMiOiJNa1dtT0t4dUNNSE5pbG9OUXpCZkpRPT0iLCJub25jZSI6IkNnc01EUTRQRUJFU0V4UVYiLCJ2IjoxfQ=="
	payload, err := decryptCloudPayload(encoded, key, "request-1234567890", "ai.chat.message")
	if err != nil {
		t.Fatal(err)
	}
	if payload["message"] != "hello" || payload["sequence"] != float64(1) {
		t.Fatalf("unexpected vector payload: %#v", payload)
	}
}

func TestEncryptDecryptCloudPayloadRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(31 - i)
	}
	encoded, err := encryptCloudPayload(
		map[string]any{"message": "desktop hello", "sequence": float64(2)},
		key,
		"request-desktop-1234",
		"ai.chat.message",
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := decryptCloudPayload(encoded, key, "request-desktop-1234", "ai.chat.message")
	if err != nil {
		t.Fatal(err)
	}
	if payload["message"] != "desktop hello" || payload["sequence"] != float64(2) {
		t.Fatalf("unexpected roundtrip payload: %#v", payload)
	}
}
