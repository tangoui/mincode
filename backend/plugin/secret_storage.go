package plugin

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	hostcrypto "mincode/app/backend/crypto"
)

const (
	pluginSecretMaxKeys       = 256
	pluginSecretMaxValueBytes = 64 * 1024
	pluginSecretBindingPrefix = "mincode-plugin-secret\x00"
)

var (
	pluginSecretEncrypt = hostcrypto.EncryptForStorage
	pluginSecretDecrypt = hostcrypto.DecryptFromStorage

	errPluginSecretUnreadable = errors.New("stored secret cannot be decrypted on this device; enter it again")
)

// The plaintext is prefixed with the owning plugin and key so a ciphertext
// copied to another plugin or key fails to open instead of leaking.
func pluginSecretBinding(pluginID, key string) string {
	return pluginSecretBindingPrefix + pluginID + "\x00" + key + "\x00"
}

func validatePluginSecretKey(pluginID, key string) (string, string, error) {
	id, err := validatePluginID(pluginID)
	if err != nil {
		return "", "", err
	}
	validatedKey, err := validatePluginKey(key)
	if err != nil {
		return "", "", errors.New("secret key must be 1-256 bytes without control characters or ':'")
	}
	return id, validatedKey, nil
}

func pluginSecretGet(pluginID, key string) (string, bool, error) {
	id, validatedKey, err := validatePluginSecretKey(pluginID, key)
	if err != nil {
		return "", false, err
	}
	db, err := openPluginDB()
	if err != nil {
		return "", false, err
	}
	var sealed string
	err = db.QueryRow("SELECT value FROM plugin_secrets WHERE plugin_id = ? AND key = ?", id, validatedKey).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read plugin secret %q: %w", validatedKey, err)
	}
	plain, err := pluginSecretDecrypt(sealed)
	if err != nil {
		return "", false, errPluginSecretUnreadable
	}
	binding := pluginSecretBinding(id, validatedKey)
	if !strings.HasPrefix(plain, binding) {
		return "", false, errPluginSecretUnreadable
	}
	return strings.TrimPrefix(plain, binding), true, nil
}

func pluginSecretSet(pluginID, key, value string) error {
	id, validatedKey, err := validatePluginSecretKey(pluginID, key)
	if err != nil {
		return err
	}
	if value == "" {
		return errors.New("secret value must not be empty; use host.secrets.delete")
	}
	if len(value) > pluginSecretMaxValueBytes || !utf8.ValidString(value) {
		return fmt.Errorf("secret value must be valid UTF-8 up to %d bytes", pluginSecretMaxValueBytes)
	}
	sealed, err := pluginSecretEncrypt(pluginSecretBinding(id, validatedKey) + value)
	if err != nil {
		return errors.New("encrypt plugin secret failed")
	}
	db, err := openPluginDB()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("write plugin secret %q: %w", validatedKey, err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM plugin_secrets WHERE plugin_id = ? AND key = ?)", id, validatedKey).Scan(&exists); err != nil {
		return fmt.Errorf("write plugin secret %q: %w", validatedKey, err)
	}
	if exists == 0 {
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM plugin_secrets WHERE plugin_id = ?", id).Scan(&count); err != nil {
			return fmt.Errorf("count plugin secrets: %w", err)
		}
		if count >= pluginSecretMaxKeys {
			return fmt.Errorf("a plugin can store at most %d secrets", pluginSecretMaxKeys)
		}
	}
	if _, err := tx.Exec(`INSERT INTO plugin_secrets(plugin_id, key, value) VALUES(?, ?, ?)
		ON CONFLICT(plugin_id, key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, id, validatedKey, sealed); err != nil {
		return fmt.Errorf("write plugin secret %q: %w", validatedKey, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("write plugin secret %q: %w", validatedKey, err)
	}
	return nil
}

func pluginSecretDelete(pluginID, key string) error {
	id, validatedKey, err := validatePluginSecretKey(pluginID, key)
	if err != nil {
		return err
	}
	db, err := openPluginDB()
	if err != nil {
		return err
	}
	if _, err := db.Exec("DELETE FROM plugin_secrets WHERE plugin_id = ? AND key = ?", id, validatedKey); err != nil {
		return fmt.Errorf("delete plugin secret %q: %w", validatedKey, err)
	}
	return nil
}

func pluginSecretList(pluginID string) ([]string, error) {
	id, err := validatePluginID(pluginID)
	if err != nil {
		return nil, err
	}
	db, err := openPluginDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT key FROM plugin_secrets WHERE plugin_id = ? ORDER BY key", id)
	if err != nil {
		return nil, fmt.Errorf("list plugin secrets: %w", err)
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("list plugin secrets: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list plugin secrets: %w", err)
	}
	return keys, nil
}

func pluginSecretDeleteNamespace(pluginID string) error {
	id, err := validatePluginID(pluginID)
	if err != nil {
		return err
	}
	db, err := openPluginDB()
	if err != nil {
		return err
	}
	if _, err := db.Exec("DELETE FROM plugin_secrets WHERE plugin_id = ?", id); err != nil {
		return fmt.Errorf("delete plugin secrets: %w", err)
	}
	return nil
}

// Keys under configurationSecretPrefix hold secret settings owned by the host
// (configuration_store.go). host.secrets.* must not read, overwrite, delete or
// list them: the settings page is their only writer and host.configuration.get
// their only plugin-facing reader.
var errPluginSecretReserved = errors.New("secret keys starting with " + configurationSecretPrefix + " are reserved for plugin settings")

func pluginSecretKeyReserved(key string) bool {
	return strings.HasPrefix(strings.TrimSpace(key), configurationSecretPrefix)
}

// pluginFacingSecretList is host.secrets.list: the reserved keys are hidden.
func pluginFacingSecretList(pluginID string) ([]string, error) {
	keys, err := pluginSecretList(pluginID)
	if err != nil {
		return nil, err
	}
	visible := make([]string, 0, len(keys))
	for _, key := range keys {
		if !pluginSecretKeyReserved(key) {
			visible = append(visible, key)
		}
	}
	return visible, nil
}
