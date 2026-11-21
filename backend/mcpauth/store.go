package mcpauth

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"unicode"

	hostcrypto "mincode/app/backend/crypto"
	"mincode/app/backend/internal/mcpauthpolicy"
	"mincode/app/backend/storage"
)

// Record is one connector's login, keyed by server ID. It is only valid for
// the URL it was obtained for (ruling R2): a changed URL reads as signed out.
type Record struct {
	ServerID   string   `json:"serverId"`
	URL        string   `json:"url"`
	Metadata   Metadata `json:"metadata"`
	Client     Client   `json:"client"`
	Tokens     Tokens   `json:"tokens"`
	NeedsLogin bool     `json:"needsLogin,omitempty"`
}

// Store persists connector logins on this device only.
type Store interface {
	Get(serverID, url string) (Record, bool, error)
	Put(Record) error
	Delete(serverID string) error
}

const (
	// storeBinding prefixes the plaintext so ciphertext copied from another
	// EncryptForStorage user (for example a plugin secret) does not open here.
	storeBinding      = "mincode-mcp-oauth-v1\x00"
	encryptedPrefix   = "enc:"
	maxServerIDBytes  = 256
	maxRecordURLBytes = 4096
)

var (
	// storeMu serialises read-modify-write of the single app_kv row.
	storeMu     sync.Mutex
	openStoreDB = storage.DB
)

type kvStore struct{}

// NewKVStore returns the store backed by the reserved app_kv key
// mcp_oauth_v1. The whole map is encrypted with crypto.EncryptForStorage; the
// key is refused by the generic store commands and excluded from
// store_load_all, so it never reaches the renderer, plugins or config sync.
func NewKVStore() Store { return kvStore{} }

func (kvStore) Get(serverID, url string) (Record, bool, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	records, err := loadRecords()
	if err != nil {
		return Record{}, false, err
	}
	record, ok := records[serverID]
	if !ok || record.URL != url {
		return Record{}, false, nil
	}
	return record, true, nil
}

func (kvStore) Put(record Record) error {
	if err := validateServerID(record.ServerID); err != nil {
		return err
	}
	if record.URL == "" || len(record.URL) > maxRecordURLBytes {
		return authError("MCP_AUTH_STORE", "连接器地址无效")
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	records, err := loadRecords()
	if err != nil {
		return err
	}
	records[record.ServerID] = record
	return saveRecords(records)
}

func (kvStore) Delete(serverID string) error {
	if err := validateServerID(serverID); err != nil {
		return err
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	records, err := loadRecords()
	if err != nil {
		return err
	}
	if _, ok := records[serverID]; !ok {
		return nil
	}
	delete(records, serverID)
	return saveRecords(records)
}

func validateServerID(serverID string) error {
	if serverID == "" || len(serverID) > maxServerIDBytes || strings.IndexFunc(serverID, unicode.IsControl) >= 0 {
		return authError("MCP_AUTH_STORE", "连接器 ID 无效")
	}
	return nil
}

// loadRecords returns the stored map. A value that is not our ciphertext, does
// not decrypt on this device or does not parse is treated as empty: the logins
// it held are unusable, the connectors read as signed out and the next Put
// replaces it. Database errors are returned.
func loadRecords() (map[string]Record, error) {
	db, err := openStoreDB()
	if err != nil {
		return nil, authError("MCP_AUTH_STORE", "打开令牌库失败: %v", err)
	}
	var raw []byte
	err = db.QueryRow("SELECT value FROM app_kv WHERE key = ?", mcpauthpolicy.StorageKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, authError("MCP_AUTH_STORE", "读取令牌库失败: %v", err)
	}
	records := map[string]Record{}
	var sealed string
	if json.Unmarshal(raw, &sealed) != nil || !strings.HasPrefix(sealed, encryptedPrefix) {
		return records, nil
	}
	plain, err := hostcrypto.DecryptFromStorage(sealed)
	if err != nil || !strings.HasPrefix(plain, storeBinding) {
		return records, nil
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(plain, storeBinding)), &records) != nil {
		return map[string]Record{}, nil
	}
	return records, nil
}

func saveRecords(records map[string]Record) error {
	db, err := openStoreDB()
	if err != nil {
		return authError("MCP_AUTH_STORE", "打开令牌库失败: %v", err)
	}
	if len(records) == 0 {
		if _, err := db.Exec("DELETE FROM app_kv WHERE key = ?", mcpauthpolicy.StorageKey); err != nil {
			return authError("MCP_AUTH_STORE", "写入令牌库失败: %v", err)
		}
		return nil
	}
	plain, err := json.Marshal(records)
	if err != nil {
		return authError("MCP_AUTH_STORE", "编码令牌库失败")
	}
	sealed, err := hostcrypto.EncryptForStorage(storeBinding + string(plain))
	if err != nil {
		return authError("MCP_AUTH_STORE", "加密令牌库失败")
	}
	value, err := json.Marshal(sealed)
	if err != nil {
		return authError("MCP_AUTH_STORE", "编码令牌库失败")
	}
	if _, err := db.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, mcpauthpolicy.StorageKey, value); err != nil {
		return authError("MCP_AUTH_STORE", "写入令牌库失败: %v", err)
	}
	return nil
}
