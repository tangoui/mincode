package dbimport

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"mincode/app/backend/core"
	appcrypto "mincode/app/backend/crypto"
	"mincode/app/backend/storage"
)

// connectionsStoreKey is the app_kv key the database workspace persists its
// connection list under (vue-app/src/stores/database-workspace.ts).
const connectionsStoreKey = "app_db_connections"

const (
	maxGroupBytes      = 128
	maxStoredConnBytes = 16 << 20
)

var (
	pluginIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	driverIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

	// encryptSecret and updateStoredConnections are swapped in tests.
	encryptSecret           = appcrypto.EncryptForStorage
	updateStoredConnections = updateStoredConnectionsInDB
)

// DriverRef binds a connection to a plugin database driver.
type DriverRef struct {
	PluginID string `json:"pluginId"`
	DriverID string `json:"driverId"`
}

type commitRow struct {
	ID     string     `json:"id"`
	Driver *DriverRef `json:"driver"`
}

type commitArgs struct {
	ImportID    string      `json:"importId"`
	Group       string      `json:"group"`
	KeepFolders bool        `json:"keepFolders"`
	Rows        []commitRow `json:"rows"`
}

// CommitResult lists the ids of the stored connections.
type CommitResult struct {
	Imported      int      `json:"imported"`
	ConnectionIDs []string `json:"connectionIds"`
}

type storedTunnel struct {
	Type       string `json:"type"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username,omitempty"`
	AuthType   string `json:"authType,omitempty"`
	KeyPath    string `json:"keyPath,omitempty"`
	Password   string `json:"password,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// storedConnection mirrors the persisted DatabaseConnection shape.
type storedConnection struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	DbType       string        `json:"dbType"`
	Host         string        `json:"host"`
	Port         int           `json:"port"`
	Username     string        `json:"username,omitempty"`
	Password     string        `json:"password,omitempty"`
	Database     string        `json:"database,omitempty"`
	FilePath     string        `json:"filePath,omitempty"`
	AuthSource   string        `json:"authSource,omitempty"`
	SSL          bool          `json:"ssl"`
	SavePassword bool          `json:"savePassword"`
	Group        string        `json:"group,omitempty"`
	Tags         []string      `json:"tags"`
	Tunnel       *storedTunnel `json:"tunnel,omitempty"`
	Driver       *DriverRef    `json:"driver,omitempty"`
	CreatedAt    int64         `json:"createdAt"`
	UpdatedAt    int64         `json:"updatedAt"`
}

func commitHandler(_, argsJSON string) (string, error) {
	var args commitArgs
	if err := core.DecodeArgs(argsJSON, &args); err != nil {
		return "", err
	}
	result, err := commitImport(args)
	if err != nil {
		return "", err
	}
	return core.EncodeResult(result)
}

func commitImport(args commitArgs) (CommitResult, error) {
	s, ok := claimSession(args.ImportID)
	if !ok {
		return CommitResult{}, errCode("session_expired", nil)
	}
	committed := false
	// A failed commit keeps the session so the user can retry; a claimed
	// session cannot be committed twice concurrently.
	defer func() {
		if !committed {
			restoreSession(args.ImportID, s)
		}
	}()
	group := strings.TrimSpace(args.Group)
	if len(group) > maxGroupBytes {
		return CommitResult{}, errCode("group_invalid", nil)
	}
	if len(args.Rows) == 0 || len(args.Rows) > maxCandidates {
		return CommitResult{}, errCode("selection_invalid", nil)
	}
	byID := make(map[string]*Candidate, len(s.preview.Rows))
	for i := range s.preview.Rows {
		byID[s.preview.Rows[i].ID] = &s.preview.Rows[i]
	}
	seen := make(map[string]bool, len(args.Rows))
	records := make([]storedConnection, 0, len(args.Rows))
	timestamp := now().UnixMilli()
	for _, row := range args.Rows {
		candidate := byID[row.ID]
		if candidate == nil || seen[row.ID] {
			return CommitResult{}, errCode("selection_invalid", nil)
		}
		seen[row.ID] = true
		record, err := buildRecord(candidate, row.Driver, targetGroup(group, candidate.Folder, args.KeepFolders), timestamp)
		if err != nil {
			return CommitResult{}, err
		}
		records = append(records, record)
	}
	ids := make([]string, 0, len(records))
	err := updateStoredConnections(func(existing []json.RawMessage) ([]json.RawMessage, error) {
		for _, record := range records {
			encoded, err := json.Marshal(record)
			if err != nil {
				return nil, err
			}
			existing = append(existing, encoded)
			ids = append(ids, record.ID)
		}
		return existing, nil
	})
	if err != nil {
		return CommitResult{}, errCode("store_failed", err)
	}
	committed = true
	return CommitResult{Imported: len(ids), ConnectionIDs: ids}, nil
}

func targetGroup(group, folder string, keepFolders bool) string {
	folder = strings.Trim(folder, "/ ")
	if !keepFolders || folder == "" {
		return group
	}
	combined := folder
	if group != "" {
		combined = group + "/" + folder
	}
	if len(combined) > maxGroupBytes {
		return group
	}
	return combined
}

func validDriverRef(ref *DriverRef) bool {
	return ref != nil && pluginIDPattern.MatchString(ref.PluginID) && driverIDPattern.MatchString(ref.DriverID)
}

func buildRecord(c *Candidate, driver *DriverRef, group string, timestamp int64) (storedConnection, error) {
	record := storedConnection{
		ID:         uuid.NewString(),
		Name:       c.Name,
		DbType:     c.DbType,
		Host:       c.Host,
		Port:       c.Port,
		Username:   c.Username,
		Database:   c.Database,
		FilePath:   c.FilePath,
		AuthSource: c.AuthSource,
		SSL:        c.SSL,
		Group:      group,
		Tags:       []string{},
		CreatedAt:  timestamp,
		UpdatedAt:  timestamp,
	}
	switch c.Status {
	case StatusReady:
		if driver != nil {
			if !validDriverRef(driver) {
				return storedConnection{}, errCode("driver_invalid", nil)
			}
			record.Driver = &DriverRef{PluginID: driver.PluginID, DriverID: driver.DriverID}
		}
	case StatusNeedsPlugin:
		if !validDriverRef(driver) || driver.DriverID != c.PluginDriverID {
			return storedConnection{}, errCode("driver_required", nil)
		}
		record.DbType = "plugin"
		record.Driver = &DriverRef{PluginID: driver.PluginID, DriverID: driver.DriverID}
	default:
		return storedConnection{}, errCode("unsupported_type", nil)
	}
	var err error
	if record.Password, err = encryptOptional(c.password); err != nil {
		return storedConnection{}, errCode("encrypt_failed", nil)
	}
	// Plugin drivers do not use MinCode tunnels, so such records keep none.
	if t := c.Tunnel; t != nil && record.Driver == nil && t.Host != "" && t.Port > 0 && (t.Type != "ssh" || t.Username != "") {
		tunnel := &storedTunnel{Type: t.Type, Host: t.Host, Port: t.Port, Username: t.Username}
		if t.Type == "ssh" {
			tunnel.AuthType = t.AuthType
			if tunnel.AuthType != "key" {
				tunnel.AuthType = "password"
			}
			tunnel.KeyPath = t.KeyPath
		}
		if tunnel.Password, err = encryptOptional(t.password); err != nil {
			return storedConnection{}, errCode("encrypt_failed", nil)
		}
		if tunnel.Passphrase, err = encryptOptional(t.passphrase); err != nil {
			return storedConnection{}, errCode("encrypt_failed", nil)
		}
		record.Tunnel = tunnel
	}
	// Secrets survive the next frontend persist only with savePassword on.
	record.SavePassword = record.Password != "" ||
		(record.Tunnel != nil && (record.Tunnel.Password != "" || record.Tunnel.Passphrase != ""))
	return record, nil
}

func encryptOptional(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	return encryptSecret(plain)
}

// updateStoredConnectionsInDB appends to the persisted connection list in
// one transaction. A value that is not a JSON array is never overwritten.
func updateStoredConnectionsInDB(update func([]json.RawMessage) ([]json.RawMessage, error)) error {
	db, err := storage.DB()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	var raw []byte
	err = tx.QueryRow("SELECT value FROM app_kv WHERE key = ?", connectionsStoreKey).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read connections: %w", err)
	}
	existing, err := decodeStoredList(raw)
	if err != nil {
		return err
	}
	next, err := update(existing)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(encoded) > maxStoredConnBytes {
		return errors.New("connection list too large")
	}
	if _, err := tx.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, connectionsStoreKey, encoded); err != nil {
		return fmt.Errorf("write connections: %w", err)
	}
	return tx.Commit()
}

func decodeStoredList(raw []byte) ([]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return []json.RawMessage{}, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("stored connection list is not an array")
	}
	return list, nil
}
