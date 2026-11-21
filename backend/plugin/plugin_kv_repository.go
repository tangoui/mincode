package plugin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"mincode/app/backend/storage"
)

var openPluginDB = storage.DB

// openPluginReadDB 用于纯查询，走只读连接池，不与写入排队。
var openPluginReadDB = storage.ReadDB

func pluginKVGetRepository(pluginID, key string) (json.RawMessage, bool, error) {
	db, err := openPluginReadDB()
	if err != nil {
		return nil, false, err
	}
	var value []byte
	err = db.QueryRow("SELECT value FROM plugin_kv WHERE plugin_id = ? AND key = ?", pluginID, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("读取 plugin_kv %q:%q: %w", pluginID, key, err)
	}
	return json.RawMessage(value), true, nil
}

func pluginKVSetRepository(pluginID, key string, value json.RawMessage) error {
	db, err := openPluginDB()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("开始写入 plugin_kv %q:%q: %w", pluginID, key, err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM plugin_kv WHERE plugin_id = ? AND key = ?)", pluginID, key).Scan(&exists); err != nil {
		return fmt.Errorf("检查 plugin_kv %q:%q: %w", pluginID, key, err)
	}
	if exists == 0 {
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM plugin_kv WHERE plugin_id = ?", pluginID).Scan(&count); err != nil {
			return fmt.Errorf("统计 plugin_kv %q: %w", pluginID, err)
		}
		if count >= pluginKVMaxKeys {
			return fmt.Errorf("plugin_kv 每个插件最多保存 %d 个 key", pluginKVMaxKeys)
		}
	}
	if _, err := tx.Exec(`INSERT INTO plugin_kv(plugin_id, key, value) VALUES(?, ?, ?)
		ON CONFLICT(plugin_id, key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, pluginID, key, []byte(value)); err != nil {
		return fmt.Errorf("写入 plugin_kv %q:%q: %w", pluginID, key, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 plugin_kv %q:%q: %w", pluginID, key, err)
	}
	return nil
}

func pluginKVDeleteRepository(pluginID, key string) error {
	db, err := openPluginDB()
	if err != nil {
		return err
	}
	if _, err := db.Exec("DELETE FROM plugin_kv WHERE plugin_id = ? AND key = ?", pluginID, key); err != nil {
		return fmt.Errorf("删除 plugin_kv %q:%q: %w", pluginID, key, err)
	}
	return nil
}

func pluginKVDeleteNamespaceRepository(pluginID string) error {
	db, err := openPluginDB()
	if err != nil {
		return err
	}
	if _, err := db.Exec("DELETE FROM plugin_kv WHERE plugin_id = ?", pluginID); err != nil {
		return fmt.Errorf("删除 plugin_kv namespace %q: %w", pluginID, err)
	}
	return nil
}

func pluginKVListRepository(pluginID string) ([]string, error) {
	db, err := openPluginReadDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT key FROM plugin_kv WHERE plugin_id = ? ORDER BY key", pluginID)
	if err != nil {
		return nil, fmt.Errorf("列出 plugin_kv %q: %w", pluginID, err)
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("扫描 plugin_kv %q: %w", pluginID, err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 plugin_kv %q: %w", pluginID, err)
	}
	return keys, nil
}
