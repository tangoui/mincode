// App Store 域：主配置持久化的通用键值存储。
//
// 命令契约（入参 camelCase，无 payload 外层，故用 core.DecodeArgs）：
//
//	store_get         {key}                 -> any | null
//	store_get_many    {keys}                -> {key: any | null}（缺失键返回 null）
//	store_set         {key, value}          -> void
//	store_del         {key}                 -> void
//	store_del_many    {keys}                -> void
//	store_load_all    {}                    -> {key: value}
//	store_apply_batch {operations|sets,...} -> void
//	delete_all_ai operation clears every normalized AI workspace in the same transaction
package appstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mincode/app/backend/core"
	"mincode/app/backend/internal/applockpolicy"
	"mincode/app/backend/internal/backuppolicy"
	"mincode/app/backend/internal/filetemplatepolicy"
	"mincode/app/backend/internal/mcpauthpolicy"
	"mincode/app/backend/storage"
)

func init() {
	core.Register("store_get", storeGetHandler)
	core.Register("store_get_many", storeGetManyHandler)
	core.Register("store_set", storeSetHandler)
	core.Register("store_del", storeDelHandler)
	core.Register("store_del_many", storeDelManyHandler)
	core.Register("store_load_all", storeLoadAllHandler)
	core.Register("store_apply_batch", storeApplyBatchHandler)
}

const (
	maxBatchOperations = 10000
	maxBatchValueBytes = 32 * 1024 * 1024
)

var openStoreDB = storage.DB

// openStoreReadDB 用于纯查询，走只读连接池，不与写入排队。
var openStoreReadDB = storage.ReadDB

// reservedKeyError 拒绝由后端专属模块持有的键：文件模板库、锁屏口令校验记录
// （渲染进程既不能读到校验材料去离线爆破，也不能替换或回写它）、MCP OAuth 令牌库，
// 以及记住的备份密码。
func reservedKeyError(key string) error {
	switch key {
	case filetemplatepolicy.StorageKey:
		return errors.New("file_template_reserved_key")
	case applockpolicy.StorageKey:
		return errors.New("app_lock_reserved_key")
	case mcpauthpolicy.StorageKey:
		return errors.New("mcp_oauth_reserved_key")
	case backuppolicy.PasswordStorageKey:
		return errors.New("backup_password_reserved_key")
	}
	return nil
}

type storeKeyArgs struct {
	Key string `json:"key"`
}

func storeGetHandler(_, argsJSON string) (string, error) {
	var args storeKeyArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := reservedKeyError(args.Key); err != nil {
		return "", err
	}
	db, err := openStoreReadDB()
	if err != nil {
		return "", err
	}
	var value []byte
	err = db.QueryRow("SELECT value FROM app_kv WHERE key = ?", args.Key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return core.EncodeResult(nil)
	}
	if err != nil {
		return "", fmt.Errorf("读取应用配置 %q: %w", args.Key, err)
	}
	return string(value), nil
}

type storeGetManyArgs struct {
	Keys []string `json:"keys"`
}

// maxGetManyKeys 限制单次批量读取的键数，同时保持 IN 子句参数数低于 SQLite 默认上限。
const maxGetManyKeys = 500

// storeGetManyHandler 在一次 IPC 与一次查询内读取多个键，供前端合并启动期的分散读取。
func storeGetManyHandler(_, argsJSON string) (string, error) {
	var args storeGetManyArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if len(args.Keys) > maxGetManyKeys {
		return "", fmt.Errorf("批量读取应用配置键数 %d 超过上限 %d", len(args.Keys), maxGetManyKeys)
	}
	keys := make([]string, 0, len(args.Keys))
	seen := make(map[string]struct{}, len(args.Keys))
	for _, key := range args.Keys {
		if err := reservedKeyError(key); err != nil {
			return "", err
		}
		if key == "" {
			return "", errors.New("批量读取应用配置 key 不能为空")
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	values := make(map[string][]byte, len(keys))
	if len(keys) > 0 {
		db, err := openStoreReadDB()
		if err != nil {
			return "", err
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
		params := make([]any, len(keys))
		for i, key := range keys {
			params[i] = key
		}
		rows, err := db.Query("SELECT key, value FROM app_kv WHERE key IN ("+placeholders+")", params...)
		if err != nil {
			return "", fmt.Errorf("批量读取应用配置: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var value []byte
			if err := rows.Scan(&key, &value); err != nil {
				return "", fmt.Errorf("扫描应用配置: %w", err)
			}
			values[key] = value
		}
		if err := rows.Err(); err != nil {
			return "", fmt.Errorf("遍历应用配置: %w", err)
		}
	}
	// 值在写入时已校验为合法 JSON，直接拼接对象，与 store_load_all 一致。
	var out strings.Builder
	out.WriteByte('{')
	for i, key := range keys {
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return "", fmt.Errorf("编码应用配置 key: %w", err)
		}
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(encodedKey)
		out.WriteByte(':')
		if value := values[key]; len(value) > 0 {
			out.Write(value)
		} else {
			out.WriteString("null")
		}
	}
	out.WriteByte('}')
	return out.String(), nil
}

type storeSetArgs struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func storeSetHandler(_, argsJSON string) (string, error) {
	var args storeSetArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := reservedKeyError(args.Key); err != nil {
		return "", err
	}
	if len(args.Value) == 0 {
		args.Value = json.RawMessage("null")
	}
	db, err := openStoreDB()
	if err != nil {
		return "", err
	}
	if _, err := db.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, args.Key, []byte(args.Value)); err != nil {
		return "", fmt.Errorf("写入应用配置 %q: %w", args.Key, err)
	}
	return core.EncodeResult(nil)
}

func storeDelHandler(_, argsJSON string) (string, error) {
	var args storeKeyArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	if err := reservedKeyError(args.Key); err != nil {
		return "", err
	}
	db, err := openStoreDB()
	if err != nil {
		return "", err
	}
	if _, err := db.Exec("DELETE FROM app_kv WHERE key = ?", args.Key); err != nil {
		return "", fmt.Errorf("删除应用配置 %q: %w", args.Key, err)
	}
	return core.EncodeResult(nil)
}

type storeDelManyArgs struct {
	Keys []string `json:"keys"`
}

func storeDelManyHandler(_, argsJSON string) (string, error) {
	var args storeDelManyArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	operations := make([]storeBatchOperation, 0, len(args.Keys))
	for _, key := range args.Keys {
		operations = append(operations, storeBatchOperation{Type: "delete", Key: key})
	}
	if err := applyBatch(operations); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

type storeBatchOperation struct {
	Type  string          `json:"type"`
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type storeApplyBatchArgs struct {
	Operations []storeBatchOperation      `json:"operations"`
	Sets       map[string]json.RawMessage `json:"sets"`
	Deletes    []string                   `json:"deletes"`
	DeleteKeys []string                   `json:"deleteKeys"`
}

func storeApplyBatchHandler(_, argsJSON string) (string, error) {
	var args storeApplyBatchArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	operations := append([]storeBatchOperation(nil), args.Operations...)
	for key, value := range args.Sets {
		operations = append(operations, storeBatchOperation{Type: "set", Key: key, Value: value})
	}
	for _, key := range append(args.Deletes, args.DeleteKeys...) {
		operations = append(operations, storeBatchOperation{Type: "delete", Key: key})
	}
	if err := applyBatch(operations); err != nil {
		return "", err
	}
	return core.EncodeResult(nil)
}

func applyBatch(operations []storeBatchOperation) error {
	if err := validateBatch(operations); err != nil {
		return err
	}
	if len(operations) == 0 {
		return nil
	}
	db, err := openStoreDB()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("开始应用配置批处理: %w", err)
	}
	defer tx.Rollback()
	for _, operation := range operations {
		switch operation.Type {
		case "set":
			if _, err := tx.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, operation.Key, []byte(operation.Value)); err != nil {
				return fmt.Errorf("批量写入应用配置 %q: %w", operation.Key, err)
			}
		case "delete", "del":
			if _, err := tx.Exec("DELETE FROM app_kv WHERE key = ?", operation.Key); err != nil {
				return fmt.Errorf("批量删除应用配置 %q: %w", operation.Key, err)
			}
		case "delete_ai_scope":
			if _, err := tx.Exec("DELETE FROM ai_workspaces WHERE scope_id = ?", operation.Key); err != nil {
				return fmt.Errorf("批量删除 AI 工作区 %q: %w", operation.Key, err)
			}
			if _, err := tx.Exec(`DELETE FROM ai_attachments
				WHERE NOT EXISTS (SELECT 1 FROM ai_attachment_refs r WHERE r.attachment_hash = ai_attachments.hash)`); err != nil {
				return fmt.Errorf("批量清理未引用 AI 附件: %w", err)
			}
		case "delete_all_ai":
			if _, err := tx.Exec("DELETE FROM ai_workspaces"); err != nil {
				return fmt.Errorf("批量删除全部 AI 工作区: %w", err)
			}
			if _, err := tx.Exec(`DELETE FROM app_kv
				WHERE key = 'app_ai_conversations_v2'
					OR substr(key, 1, length('app_ai_conversations_v2__')) = 'app_ai_conversations_v2__'
					OR key = 'app_ai_session_v1'
					OR substr(key, 1, length('app_ai_session_v1__')) = 'app_ai_session_v1__'`); err != nil {
				return fmt.Errorf("批量删除退役 AI 快照: %w", err)
			}
			if _, err := tx.Exec(`DELETE FROM ai_attachments
				WHERE NOT EXISTS (SELECT 1 FROM ai_attachment_refs r WHERE r.attachment_hash = ai_attachments.hash)`); err != nil {
				return fmt.Errorf("批量清理全部 AI 历史的未引用附件: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交应用配置批处理: %w", err)
	}
	return nil
}

func validateBatch(operations []storeBatchOperation) error {
	if len(operations) > maxBatchOperations {
		return fmt.Errorf("应用配置批处理操作数 %d 超过上限 %d", len(operations), maxBatchOperations)
	}
	seen := make(map[string]string, len(operations))
	totalValueBytes := 0
	for _, operation := range operations {
		if err := reservedKeyError(operation.Key); err != nil {
			return err
		}
		if operation.Key == "" {
			return errors.New("应用配置批处理 key 不能为空")
		}
		if previousType, exists := seen[operation.Key]; exists {
			return fmt.Errorf("应用配置批处理 key %q 重复（%s 与 %s）", operation.Key, previousType, operation.Type)
		}
		seen[operation.Key] = operation.Type
		switch operation.Type {
		case "set":
			if len(operation.Value) == 0 || !json.Valid(operation.Value) {
				return fmt.Errorf("应用配置批处理 key %q 的 value 不是合法 JSON", operation.Key)
			}
			totalValueBytes += len(operation.Value)
			if totalValueBytes > maxBatchValueBytes {
				return fmt.Errorf("应用配置批处理 value 总大小超过 %d 字节", maxBatchValueBytes)
			}
		case "delete", "del", "delete_ai_scope", "delete_all_ai":
			if len(operation.Value) != 0 {
				return fmt.Errorf("应用配置批处理删除 key %q 不允许携带 value", operation.Key)
			}
		default:
			return fmt.Errorf("不支持的应用配置批处理操作: %q", operation.Type)
		}
	}
	return nil
}

// storeLoadAllQuery 排除仅供后端使用的键（云终端绑定、云操作去重记录、窗口几何），
// 它们不属于前端配置，也不应进入配置同步/导出。前缀用区间谓词表达（':' 的下一个字符是 ';'）。
const storeLoadAllQuery = `SELECT key, value FROM app_kv
	WHERE NOT (key >= 'cloud-operation:' AND key < 'cloud-operation;')
		AND NOT (key >= 'cloud-terminal:' AND key < 'cloud-terminal;')
		AND key <> 'desktop_window_state_v1'
 AND key <> '` + filetemplatepolicy.StorageKey + `'
 AND key <> '` + applockpolicy.StorageKey + `'
 AND key <> '` + mcpauthpolicy.StorageKey + `'
 AND key <> '` + backuppolicy.PasswordStorageKey + `'
	ORDER BY key`

func storeLoadAllHandler(_, _ string) (string, error) {
	db, err := openStoreReadDB()
	if err != nil {
		return "", err
	}
	rows, err := db.Query(storeLoadAllQuery)
	if err != nil {
		return "", fmt.Errorf("读取全部应用配置: %w", err)
	}
	defer rows.Close()
	// 值在写入时已校验为合法 JSON，这里直接拼接对象，避免对每个值再做一次校验与压缩。
	var out strings.Builder
	out.WriteByte('{')
	first := true
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return "", fmt.Errorf("扫描应用配置: %w", err)
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return "", fmt.Errorf("编码应用配置 key: %w", err)
		}
		if !first {
			out.WriteByte(',')
		}
		first = false
		out.Write(encodedKey)
		out.WriteByte(':')
		if len(value) == 0 {
			out.WriteString("null")
		} else {
			out.Write(value)
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("遍历应用配置: %w", err)
	}
	out.WriteByte('}')
	return out.String(), nil
}
