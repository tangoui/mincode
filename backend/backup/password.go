package backup

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	hostcrypto "mincode/app/backend/crypto"
	"mincode/app/backend/internal/backuppolicy"
	"mincode/app/backend/storage"
)

// The remembered backup password is stored in app_kv under
// backuppolicy.PasswordStorageKey as a JSON string holding
// crypto.EncryptForStorage(password). The generic store commands refuse the
// key, so only this package reads or writes it. Errors never include the
// password.

// storedPasswordPrefix is the crypto.EncryptForStorage ciphertext prefix.
const storedPasswordPrefix = "enc:v2:"

// RememberPassword stores password, encrypted with the machine key, replacing
// any previously remembered one. Passwords shorter than MinPasswordRunes are
// refused with ErrPasswordTooShort.
func RememberPassword(password []byte) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	sealed, err := hostcrypto.EncryptForStorage(string(password))
	if err != nil {
		return errors.New("加密备份密码失败")
	}
	value, err := json.Marshal(sealed)
	if err != nil {
		return errors.New("编码备份密码失败")
	}
	db, err := storage.DB()
	if err != nil {
		return fmt.Errorf("保存备份密码: %w", err)
	}
	if _, err := db.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, backuppolicy.PasswordStorageKey, value); err != nil {
		return fmt.Errorf("保存备份密码: %w", err)
	}
	return nil
}

// RememberedPassword returns the remembered password. ok is false when none
// is stored, or when the stored value is not a ciphertext this device can
// decrypt (for example a database copied from another machine); the caller
// then asks for the password again. Database errors are returned.
func RememberedPassword() (password []byte, ok bool, err error) {
	db, err := storage.DB()
	if err != nil {
		return nil, false, fmt.Errorf("读取备份密码: %w", err)
	}
	var raw []byte
	err = db.QueryRow("SELECT value FROM app_kv WHERE key = ?", backuppolicy.PasswordStorageKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("读取备份密码: %w", err)
	}
	var sealed string
	// DecryptFromStorage passes plain text through unchanged; anything that
	// is not our ciphertext was not written by RememberPassword.
	if json.Unmarshal(raw, &sealed) != nil || !strings.HasPrefix(sealed, storedPasswordPrefix) {
		return nil, false, nil
	}
	plain, err := hostcrypto.DecryptFromStorage(sealed)
	if err != nil || plain == "" {
		return nil, false, nil
	}
	return []byte(plain), true, nil
}

// ForgetPassword deletes the remembered password. Forgetting when nothing is
// remembered is not an error.
func ForgetPassword() error {
	db, err := storage.DB()
	if err != nil {
		return fmt.Errorf("清除备份密码: %w", err)
	}
	if _, err := db.Exec("DELETE FROM app_kv WHERE key = ?", backuppolicy.PasswordStorageKey); err != nil {
		return fmt.Errorf("清除备份密码: %w", err)
	}
	return nil
}
