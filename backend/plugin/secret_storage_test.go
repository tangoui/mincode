package plugin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"mincode/app/plugins/api"
)

// useTestSecretCipher swaps the machine-bound cipher for a random AES-GCM key so
// tests never create an install salt in the real user data directory.
func useTestSecretCipher(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	previousEncrypt, previousDecrypt := pluginSecretEncrypt, pluginSecretDecrypt
	pluginSecretEncrypt = func(plain string) (string, error) {
		nonce := make([]byte, gcm.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return "", err
		}
		return "test:" + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
	}
	pluginSecretDecrypt = func(value string) (string, error) {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "test:"))
		if err != nil || len(raw) < gcm.NonceSize() {
			return "", errors.New("bad ciphertext")
		}
		plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
		return string(plain), err
	}
	t.Cleanup(func() { pluginSecretEncrypt, pluginSecretDecrypt = previousEncrypt, previousDecrypt })
}

func storedSecretCiphertext(t *testing.T, pluginID, key string) string {
	t.Helper()
	db, err := openPluginDB()
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRow("SELECT value FROM plugin_secrets WHERE plugin_id = ? AND key = ?", pluginID, key).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPluginSecretsRoundTripEncryptedAtRest(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	if err := pluginSecretSet("nats.plugin", "profile.p-1.password", "s3cr3t-value"); err != nil {
		t.Fatal(err)
	}
	stored := storedSecretCiphertext(t, "nats.plugin", "profile.p-1.password")
	if strings.Contains(stored, "s3cr3t-value") {
		t.Fatalf("secret stored in plaintext: %q", stored)
	}
	value, found, err := pluginSecretGet("nats.plugin", "profile.p-1.password")
	if err != nil || !found || value != "s3cr3t-value" {
		t.Fatalf("get = %q %v %v", value, found, err)
	}
	if err := pluginSecretSet("nats.plugin", "profile.p-1.password", "rotated"); err != nil {
		t.Fatal(err)
	}
	if value, _, _ := pluginSecretGet("nats.plugin", "profile.p-1.password"); value != "rotated" {
		t.Fatalf("overwrite = %q", value)
	}
	keys, err := pluginSecretList("nats.plugin")
	if err != nil || len(keys) != 1 || keys[0] != "profile.p-1.password" {
		t.Fatalf("list = %v %v", keys, err)
	}
	if err := pluginSecretDelete("nats.plugin", "profile.p-1.password"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := pluginSecretGet("nats.plugin", "profile.p-1.password"); found || err != nil {
		t.Fatalf("after delete found=%v err=%v", found, err)
	}
}

func TestPluginSecretsIsolatedAndBoundToOwner(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	if err := pluginSecretSet("a.plugin", "token", "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := pluginSecretGet("b.plugin", "token"); found || err != nil {
		t.Fatalf("cross-plugin read found=%v err=%v", found, err)
	}
	if keys, _ := pluginSecretList("b.plugin"); len(keys) != 0 {
		t.Fatalf("cross-plugin list = %v", keys)
	}
	// A ciphertext copied to another plugin or key must not open.
	sealed := storedSecretCiphertext(t, "a.plugin", "token")
	db, _ := openPluginDB()
	for _, target := range [][2]string{{"b.plugin", "token"}, {"a.plugin", "other"}} {
		if _, err := db.Exec("INSERT INTO plugin_secrets(plugin_id, key, value) VALUES(?, ?, ?)", target[0], target[1], sealed); err != nil {
			t.Fatal(err)
		}
		if _, found, err := pluginSecretGet(target[0], target[1]); found || !errors.Is(err, errPluginSecretUnreadable) {
			t.Fatalf("copied ciphertext %v opened: found=%v err=%v", target, found, err)
		}
	}
	if _, err := db.Exec("UPDATE plugin_secrets SET value = 'garbage' WHERE plugin_id = 'a.plugin' AND key = 'token'"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pluginSecretGet("a.plugin", "token"); !errors.Is(err, errPluginSecretUnreadable) {
		t.Fatalf("corrupt ciphertext err = %v", err)
	}
	if _, found, err := pluginSecretGet("a.plugin", "token"); found || err == nil {
		t.Fatal("corrupt ciphertext must not be reported as found")
	}
}

func TestPluginSecretsValidationAndLimits(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	for name, call := range map[string]func() error{
		"empty key":    func() error { return pluginSecretSet("x.plugin", " ", "v") },
		"colon key":    func() error { return pluginSecretSet("x.plugin", "a:b", "v") },
		"control key":  func() error { return pluginSecretSet("x.plugin", "a\nb", "v") },
		"long key":     func() error { return pluginSecretSet("x.plugin", strings.Repeat("k", 257), "v") },
		"empty value":  func() error { return pluginSecretSet("x.plugin", "k", "") },
		"invalid utf8": func() error { return pluginSecretSet("x.plugin", "k", "\xff") },
		"large value": func() error {
			return pluginSecretSet("x.plugin", "k", strings.Repeat("v", pluginSecretMaxValueBytes+1))
		},
		"bad plugin id": func() error { return pluginSecretSet("", "k", "v") },
	} {
		if err := call(); err == nil {
			t.Errorf("%s accepted", name)
		} else if strings.Contains(err.Error(), strings.Repeat("v", 64)) {
			t.Errorf("%s error echoes the value", name)
		}
	}
	if err := pluginSecretSet("x.plugin", "max", strings.Repeat("v", pluginSecretMaxValueBytes)); err != nil {
		t.Fatalf("max-size value rejected: %v", err)
	}
	for index := 1; index < pluginSecretMaxKeys; index++ {
		if err := pluginSecretSet("x.plugin", "k"+strings.Repeat("0", 3)+string(rune('a'+index%26))+strings.Repeat("x", index/26), "v"); err != nil {
			t.Fatalf("key %d: %v", index, err)
		}
	}
	if err := pluginSecretSet("x.plugin", "one-too-many", "v"); err == nil {
		t.Fatal("key limit not enforced")
	}
	if err := pluginSecretSet("x.plugin", "max", "updated"); err != nil {
		t.Fatalf("overwrite at limit rejected: %v", err)
	}
}

func TestPluginSecretsRPCRequiresCapability(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	call := func(capabilities []api.Capability, method, params string) rpcEnvelope {
		t.Helper()
		h := contextRuntime(t, capabilities, nil)
		replies := drainRuntimeWrites(h)
		h.handleHostRequest(1, rpcEnvelope{ID: json.RawMessage(`"1"`), Method: method, Params: json.RawMessage(params)})
		return <-replies
	}
	allowed := []api.Capability{api.CapabilitySecrets}
	for _, capabilities := range [][]api.Capability{nil, {api.CapabilityStorage}} {
		if r := call(capabilities, "host.secrets.get", `{"key":"k"}`); r.Error == nil || r.Error.Code != -32001 {
			t.Fatalf("capability %v: %+v", capabilities, r)
		}
	}
	if r := call(allowed, "host.secrets.get", `{"key":"k"}`); r.Error != nil || string(r.Result) != "null" {
		t.Fatalf("missing get: %+v", r)
	}
	if r := call(allowed, "host.secrets.set", `{"key":"k","value":"pw-123"}`); r.Error != nil || string(r.Result) != "true" {
		t.Fatalf("set: %+v", r)
	}
	if r := call(allowed, "host.secrets.get", `{"key":"k"}`); r.Error != nil || string(r.Result) != `"pw-123"` {
		t.Fatalf("get: %+v", r)
	}
	if r := call(allowed, "host.secrets.list", `{}`); r.Error != nil || string(r.Result) != `["k"]` {
		t.Fatalf("list: %+v", r)
	}
	for _, params := range []string{`{"key":"k"}`, `{"key":"k","value":42}`, `{"key":"k","value":null}`, `[1]`} {
		if r := call(allowed, "host.secrets.set", params); r.Error == nil || r.Error.Code != -32602 {
			t.Fatalf("set %s: %+v", params, r)
		}
	}
	if r := call(allowed, "host.secrets.delete", `{"key":"k"}`); r.Error != nil || string(r.Result) != "true" {
		t.Fatalf("delete: %+v", r)
	}
	if r := call(allowed, "host.secrets.get", `{"key":"k"}`); string(r.Result) != "null" {
		t.Fatalf("get after delete: %+v", r)
	}
	h := contextRuntime(t, allowed, nil)
	described, _ := json.Marshal(h.describeAPI())
	for _, method := range []string{"host.secrets.get", "host.secrets.set", "host.secrets.delete", "host.secrets.list"} {
		if !strings.Contains(string(described), `"name":"`+method+`"`) {
			t.Fatalf("describe missing %s: %s", method, described)
		}
	}
}

func TestPluginSecretsClearedWithPluginData(t *testing.T) {
	useTemporaryPluginData(t)
	useTestSecretCipher(t)
	id := "clear.plugin"
	manager := &pluginManager{records: map[string]*pluginRecord{id: {id: id}}}
	if err := pluginSecretSet(id, "password", "v"); err != nil {
		t.Fatal(err)
	}
	if err := pluginSecretSet("other.plugin", "password", "keep"); err != nil {
		t.Fatal(err)
	}
	if err := manager.ClearPluginData(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if keys, _ := pluginSecretList(id); len(keys) != 0 {
		t.Fatalf("secrets survived clear: %v", keys)
	}
	if value, _, _ := pluginSecretGet("other.plugin", "password"); value != "keep" {
		t.Fatal("clear removed another plugin's secrets")
	}
}
