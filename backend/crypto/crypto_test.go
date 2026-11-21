package crypto

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
)

func useTemporaryKeyStore(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	originalUserDataDir := userDataDir

	userDataDir = func() string { return root }
	resetMachineKeyCache()
	t.Cleanup(func() {
		userDataDir = originalUserDataDir
		resetMachineKeyCache()
	})
	return root
}

func resetMachineKeyCache() {
	machineKeyMu.Lock()
	defer machineKeyMu.Unlock()
	machineKeyV2 = [32]byte{}
	machineKeyReady = false
}

func TestMachineKeyRetriesAfterDerivationFailure(t *testing.T) {
	root := useTemporaryKeyStore(t)

	userDataDir = func() string { return "" }
	if _, err := encryptValue("sensitive-value"); err == nil {
		t.Fatal("encrypt must fail while the key directory is unavailable")
	}

	userDataDir = func() string { return root }
	encrypted, err := encryptValue("sensitive-value")
	if err != nil {
		t.Fatalf("encrypt after key directory recovery: %v", err)
	}
	plain, err := decryptValue(encrypted)
	if err != nil || plain != "sensitive-value" {
		t.Fatalf("decrypt after recovery = %q, %v", plain, err)
	}

	// 成功后缓存：盐值目录再次失效也不影响已派生的密钥。
	userDataDir = func() string { return "" }
	if _, err := encryptValue("sensitive-value"); err != nil {
		t.Fatalf("cached key must survive later directory failures: %v", err)
	}
}

func TestMachineKeyConcurrentCallsDeriveSameKey(t *testing.T) {
	useTemporaryKeyStore(t)

	const callers = 16
	keys := make([][32]byte, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			keys[i], errs[i] = machineKey()
		}(i)
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if keys[i] != keys[0] {
			t.Fatalf("caller %d derived a different key", i)
		}
	}
}

func TestEncryptValueUsesRandomNonceAndRoundTrips(t *testing.T) {
	useTemporaryKeyStore(t)

	first, err := encryptValue("sensitive-value")
	if err != nil {
		t.Fatalf("encrypt first value: %v", err)
	}
	second, err := encryptValue("sensitive-value")
	if err != nil {
		t.Fatalf("encrypt second value: %v", err)
	}
	if first == second {
		t.Fatal("encrypting the same plaintext must use a fresh nonce")
	}
	if !strings.HasPrefix(first, encPrefixV2) {
		t.Fatalf("ciphertext prefix = %q, want %q", first, encPrefixV2)
	}

	plain, err := decryptValue(first)
	if err != nil {
		t.Fatalf("decrypt value: %v", err)
	}
	if plain != "sensitive-value" {
		t.Fatalf("plaintext = %q, want sensitive-value", plain)
	}
}

func TestDecryptValueRejectsTamperedCiphertext(t *testing.T) {
	useTemporaryKeyStore(t)

	encrypted, err := encryptValue("sensitive-value")
	if err != nil {
		t.Fatalf("encrypt value: %v", err)
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encrypted, encPrefixV2))
	if err != nil {
		t.Fatalf("decode ciphertext: %v", err)
	}
	payload[len(payload)-1] ^= 0xff
	tampered := encPrefixV2 + base64.StdEncoding.EncodeToString(payload)

	if _, err := decryptValue(tampered); err == nil {
		t.Fatal("tampered ciphertext must be rejected")
	}
}

func TestInstallSaltIsStableAndPrivate(t *testing.T) {
	useTemporaryKeyStore(t)

	first, err := loadOrCreateInstallSalt()
	if err != nil {
		t.Fatalf("create install salt: %v", err)
	}
	second, err := loadOrCreateInstallSalt()
	if err != nil {
		t.Fatalf("read install salt: %v", err)
	}
	if first != second {
		t.Fatal("install salt must remain stable")
	}

	info, err := os.Stat(saltPath())
	if err != nil {
		t.Fatalf("stat install salt: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("install salt permissions = %o, want 600", permissions)
	}
}

func TestDecryptValueRejectsShortCiphertext(t *testing.T) {
	useTemporaryKeyStore(t)

	short := encPrefixV2 + base64.StdEncoding.EncodeToString(make([]byte, 12))
	if _, err := decryptValue(short); err == nil {
		t.Fatal("short ciphertext must be rejected")
	}
}

func TestBatchFieldHandlersRoundTripAndIsolateFailures(t *testing.T) {
	useTemporaryKeyStore(t)

	encryptedJSON, err := encryptFieldsHandler("", `{"plains":["alpha","","enc:v2:already","beta"]}`)
	if err != nil {
		t.Fatalf("encrypt_fields: %v", err)
	}
	var encrypted []string
	if err := json.Unmarshal([]byte(encryptedJSON), &encrypted); err != nil {
		t.Fatal(err)
	}
	if len(encrypted) != 4 || encrypted[1] != "" || encrypted[2] != "enc:v2:already" {
		t.Fatalf("encrypt_fields = %#v", encrypted)
	}
	if !strings.HasPrefix(encrypted[0], encPrefixV2) || !strings.HasPrefix(encrypted[3], encPrefixV2) {
		t.Fatalf("encrypt_fields did not encrypt plain values: %#v", encrypted)
	}

	payload, _ := json.Marshal(map[string]any{
		"ciphers": []string{encrypted[0], "", "enc:v2:not-base64!", encrypted[3], "plain"},
	})
	decryptedJSON, err := decryptFieldsHandler("", string(payload))
	if err != nil {
		t.Fatalf("decrypt_fields: %v", err)
	}
	var decrypted []decryptFieldResult
	if err := json.Unmarshal([]byte(decryptedJSON), &decrypted); err != nil {
		t.Fatal(err)
	}
	if len(decrypted) != 5 {
		t.Fatalf("decrypt_fields length = %d", len(decrypted))
	}
	if decrypted[0] != (decryptFieldResult{Value: "alpha"}) || decrypted[3] != (decryptFieldResult{Value: "beta"}) {
		t.Fatalf("decrypt_fields values = %#v", decrypted)
	}
	if decrypted[1] != (decryptFieldResult{}) || decrypted[4] != (decryptFieldResult{Value: "plain"}) {
		t.Fatalf("decrypt_fields passthrough = %#v", decrypted)
	}
	if decrypted[2].Error == "" || decrypted[2].Value != "" {
		t.Fatalf("damaged field must report an isolated error: %#v", decrypted[2])
	}
}
