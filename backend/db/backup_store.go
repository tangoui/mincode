package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"mincode/app/backend/storage"
)

// 备份任务包含本机路径，属于设备本地配置：键名刻意不使用 app_ 前缀，
// 以免进入配置导出 / 云同步。
const (
	backupJobsKey       = "db_backup_jobs_v1"
	backupHistoryKey    = "db_backup_history_v1"
	savedConnectionsKey = "app_db_connections"
)

// backupStore 持久化备份任务与历史；测试使用内存实现。
type backupStore interface {
	loadJobs() ([]BackupJob, error)
	saveJobs([]BackupJob) error
	loadHistory() (map[string][]BackupRun, error)
	saveHistory(map[string][]BackupRun) error
}

type kvBackupStore struct{}

func (kvBackupStore) loadJobs() ([]BackupJob, error) {
	var jobs []BackupJob
	if err := readAppKVJSON(backupJobsKey, &jobs); err != nil {
		return nil, fmt.Errorf("读取备份任务: %w", err)
	}
	if jobs == nil {
		jobs = []BackupJob{}
	}
	return jobs, nil
}

func (kvBackupStore) saveJobs(jobs []BackupJob) error {
	if err := writeAppKVJSON(backupJobsKey, jobs); err != nil {
		return fmt.Errorf("保存备份任务: %w", err)
	}
	return nil
}

func (kvBackupStore) loadHistory() (map[string][]BackupRun, error) {
	history := map[string][]BackupRun{}
	if err := readAppKVJSON(backupHistoryKey, &history); err != nil {
		return nil, fmt.Errorf("读取备份历史: %w", err)
	}
	if history == nil {
		history = map[string][]BackupRun{}
	}
	return history, nil
}

func (kvBackupStore) saveHistory(history map[string][]BackupRun) error {
	if err := writeAppKVJSON(backupHistoryKey, history); err != nil {
		return fmt.Errorf("保存备份历史: %w", err)
	}
	return nil
}

// readAppKVJSON 读取 app_kv 中的 JSON 值；键不存在时保持 dst 不变。
func readAppKVJSON(key string, dst any) error {
	database, err := storage.DB()
	if err != nil {
		return err
	}
	var raw []byte
	err = database.QueryRow("SELECT value FROM app_kv WHERE key = ?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func writeAppKVJSON(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	database, err := storage.DB()
	if err != nil {
		return err
	}
	_, err = database.Exec(`INSERT INTO app_kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`, key, data)
	return err
}

// backupSavedConnection 是前端持久化的连接记录（app_db_connections）中备份所需的字段。
type backupSavedConnection struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	DbType       string             `json:"dbType"`
	Host         string             `json:"host"`
	Port         int                `json:"port"`
	Username     *string            `json:"username"`
	Password     *string            `json:"password"`
	Database     *string            `json:"database"`
	FilePath     *string            `json:"filePath"`
	Token        *string            `json:"token"`
	Org          *string            `json:"org"`
	AuthSource   *string            `json:"authSource"`
	SSL          bool               `json:"ssl"`
	SavePassword *bool              `json:"savePassword"`
	Driver       *ExternalDriverRef `json:"driver,omitempty"`
	Options      map[string]any     `json:"options,omitempty"`
	Tunnel       json.RawMessage    `json:"tunnel,omitempty"`
}

// loadSavedConnection 按 ID 读取已保存连接；凭据仍为密文，由调用方在连接前解密。
func loadSavedConnection(connectionID string) (*backupSavedConnection, error) {
	var connections []backupSavedConnection
	if err := readAppKVJSON(savedConnectionsKey, &connections); err != nil {
		return nil, fmt.Errorf("读取已保存连接: %w", err)
	}
	for i := range connections {
		if connections[i].ID == connectionID {
			return &connections[i], nil
		}
	}
	return nil, fmt.Errorf("备份任务引用的连接不存在或已删除")
}
