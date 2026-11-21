package mcpauth

import (
	"reflect"
	"strings"
	"testing"
	"time"

	hostcrypto "mincode/app/backend/crypto"
	"mincode/app/backend/internal/mcpauthpolicy"
	"mincode/app/backend/storage"
)

func tempStorage(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_ = storage.Close()
	t.Cleanup(func() { _ = storage.Close() })
}

func rawStoreValue(t *testing.T) string {
	t.Helper()
	db, err := storage.DB()
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM app_kv WHERE key = ?`, mcpauthpolicy.StorageKey).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestKVStoreEncryptsAndRoundTrips(t *testing.T) {
	tempStorage(t)
	store := NewKVStore()
	if _, ok, err := store.Get("github", "https://api.githubcopilot.com/mcp/"); err != nil || ok {
		t.Fatalf("empty store Get = %v, %v", ok, err)
	}
	record := Record{
		ServerID: "github",
		URL:      "https://api.githubcopilot.com/mcp/",
		Metadata: Metadata{Issuer: "https://github.com/login/oauth", TokenEndpoint: "https://github.com/login/oauth/access_token", Scopes: []string{"repo"}, CodeChallengeS256: true},
		Client:   Client{ID: "cid", Secret: "client-secret-value", Public: false, RedirectURI: "http://127.0.0.1:1/callback", Device: true},
		Tokens:   Tokens{AccessToken: "access-token-value", RefreshToken: "refresh-token-value", ExpiresAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Scope: "repo"},
	}
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(Record{ServerID: "figma", URL: "https://mcp.figma.com/mcp", NeedsLogin: true}); err != nil {
		t.Fatal(err)
	}
	raw := rawStoreValue(t)
	for _, secret := range []string{"access-token-value", "refresh-token-value", "client-secret-value", "githubcopilot"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("stored value contains %q in clear text: %s", secret, raw)
		}
	}
	if !strings.Contains(raw, "enc:v2:") {
		t.Fatalf("stored value is not encrypted: %s", raw)
	}

	got, ok, err := NewKVStore().Get("github", record.URL)
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("round trip = %#v\nwant %#v", got, record)
	}
	if _, ok, err := store.Get("github", "https://other.example/mcp"); err != nil || ok {
		t.Fatalf("a changed URL must read as signed out, got %v, %v", ok, err)
	}
	if err := store.Delete("github"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get("github", record.URL); ok {
		t.Fatal("deleted record still present")
	}
	if figma, ok, err := store.Get("figma", "https://mcp.figma.com/mcp"); err != nil || !ok || !figma.NeedsLogin {
		t.Fatalf("other records must survive delete: %#v %v %v", figma, ok, err)
	}
	if err := store.Put(Record{URL: "https://x.example"}); err == nil {
		t.Fatal("record without server id must be refused")
	}
}

func TestKVStoreTreatsPlaintextOrForeignValueAsEmpty(t *testing.T) {
	tempStorage(t)
	db, err := storage.DB()
	if err != nil {
		t.Fatal(err)
	}
	plain := `"{\"github\":{\"serverId\":\"github\",\"url\":\"https://u\",\"tokens\":{\"accessToken\":\"planted\"}}}"`
	if _, err := db.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)`, mcpauthpolicy.StorageKey, plain); err != nil {
		t.Fatal(err)
	}
	store := NewKVStore()
	if _, ok, err := store.Get("github", "https://u"); err != nil || ok {
		t.Fatalf("unencrypted value must not be trusted: %v %v", ok, err)
	}
	// Ciphertext produced by another EncryptForStorage user (no binding prefix).
	foreign, err := hostcrypto.EncryptForStorage(`{"github":{"serverId":"github","url":"https://u","tokens":{"accessToken":"planted"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE app_kv SET value = ? WHERE key = ?`, `"`+foreign+`"`, mcpauthpolicy.StorageKey); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get("github", "https://u"); err != nil || ok {
		t.Fatalf("foreign ciphertext must not be trusted: %v %v", ok, err)
	}
	if err := store.Put(Record{ServerID: "a", URL: "https://a.example/mcp"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get("github", "https://u"); ok {
		t.Fatal("unreadable value must be replaced")
	}
}
