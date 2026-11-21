package backup

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"mincode/app/backend/internal/backuppolicy"
	"mincode/app/backend/storage"
)

func tempPasswordStorage(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	_ = storage.Close()
	t.Cleanup(func() { _ = storage.Close() })
}

func storedPasswordValue(t *testing.T) (string, bool) {
	t.Helper()
	db, err := storage.DB()
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	err = db.QueryRow(`SELECT value FROM app_kv WHERE key = ?`, backuppolicy.PasswordStorageKey).Scan(&raw)
	if err != nil {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("stored value is not a JSON string: %s", raw)
	}
	return value, true
}

func TestRememberPasswordRoundTrip(t *testing.T) {
	tempPasswordStorage(t)
	if backuppolicy.PasswordStorageKey != "backup_password_v1" {
		t.Fatalf("PasswordStorageKey = %q", backuppolicy.PasswordStorageKey)
	}
	if _, ok, err := RememberedPassword(); err != nil || ok {
		t.Fatalf("empty RememberedPassword = %v, %v", ok, err)
	}
	password := []byte("correct horse battery staple")
	if err := RememberPassword(password); err != nil {
		t.Fatal(err)
	}
	value, ok := storedPasswordValue(t)
	if !ok {
		t.Fatal("password was not stored")
	}
	if !strings.HasPrefix(value, "enc:v2:") {
		t.Fatalf("stored value %q is not enc:v2 ciphertext", value)
	}
	if strings.Contains(value, "horse") {
		t.Fatal("stored value contains the password in clear text")
	}
	got, ok, err := RememberedPassword()
	if err != nil || !ok || string(got) != string(password) {
		t.Fatalf("RememberedPassword = %q, %v, %v", got, ok, err)
	}
	if err := RememberPassword([]byte("another long passphrase")); err != nil {
		t.Fatal(err)
	}
	got, ok, err = RememberedPassword()
	if err != nil || !ok || string(got) != "another long passphrase" {
		t.Fatalf("replaced RememberedPassword = %q, %v, %v", got, ok, err)
	}
	if err := ForgetPassword(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := RememberedPassword(); err != nil || ok {
		t.Fatalf("RememberedPassword after Forget = %v, %v", ok, err)
	}
	if _, ok := storedPasswordValue(t); ok {
		t.Fatal("ForgetPassword left the row")
	}
	if err := ForgetPassword(); err != nil {
		t.Fatalf("ForgetPassword on empty store = %v", err)
	}
}

func TestRememberPasswordRejectsShortPassword(t *testing.T) {
	tempPasswordStorage(t)
	if err := RememberPassword([]byte("short")); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("RememberPassword(short) = %v, want ErrPasswordTooShort", err)
	}
	if _, ok := storedPasswordValue(t); ok {
		t.Fatal("short password was stored")
	}
}

func TestRememberedPasswordIgnoresUnreadableValue(t *testing.T) {
	tempPasswordStorage(t)
	db, err := storage.DB()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`"enc:v2:not-base64!"`, `{"x":1}`, `"plain password value"`} {
		if _, err := db.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, backuppolicy.PasswordStorageKey, raw); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := RememberedPassword(); err != nil || ok || got != nil {
			t.Fatalf("RememberedPassword(%s) = %q, %v, %v", raw, got, ok, err)
		}
	}
}
