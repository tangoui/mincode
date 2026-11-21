package cloudcompanion

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"mincode/app/backend/db"
)

const databaseConnectionsKey = "app_db_connections"

type storedDatabaseConnection struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	DbType     string  `json:"dbType"`
	Host       string  `json:"host"`
	Port       int     `json:"port"`
	Username   *string `json:"username"`
	Password   *string `json:"password"`
	Database   *string `json:"database"`
	FilePath   *string `json:"filePath"`
	Token      *string `json:"token"`
	Org        *string `json:"org"`
	AuthSource *string `json:"authSource"`
	SSL        bool    `json:"ssl"`
	Tunnel     any     `json:"tunnel"`
	// Driver 是插件驱动绑定（{pluginId, driverId}）。只有桌面端已授权凭据下发的绑定
	// （db.ExternalDriverReleaseAllowed）才对移动端可见，连接经插件驱动只读打开。
	Driver  json.RawMessage `json:"driver,omitempty"`
	Options map[string]any  `json:"options,omitempty"`
}

// cloudDatabaseConnectionSupported 判断连接能否经云伴侣以只读方式打开：
// 隧道连接一律排除；插件驱动连接只有已授权凭据下发时可用，绝不回落到内置驱动。
func cloudDatabaseConnectionSupported(connection storedDatabaseConnection) bool {
	if connection.Tunnel != nil {
		return false
	}
	driver, bound, ok := cloudDatabaseDriver(connection)
	if !ok {
		return false
	}
	if !bound {
		return connection.DbType != "plugin"
	}
	return driverReleaseAllowed(driver, connection.ID)
}

// driverReleaseAllowed is replaceable in tests.
var driverReleaseAllowed = db.ExternalDriverReleaseAllowed

// cloudDatabaseDriver decodes the optional driver binding. ok is false for a
// malformed binding or a "plugin" type without one.
func cloudDatabaseDriver(connection storedDatabaseConnection) (driver db.ExternalDriverRef, bound, ok bool) {
	raw := strings.TrimSpace(string(connection.Driver))
	if raw == "" || raw == "null" {
		return db.ExternalDriverRef{}, false, connection.DbType != "plugin"
	}
	if json.Unmarshal(connection.Driver, &driver) != nil {
		return db.ExternalDriverRef{}, false, false
	}
	driver.PluginID, driver.DriverID = strings.TrimSpace(driver.PluginID), strings.TrimSpace(driver.DriverID)
	if driver.PluginID == "" || driver.DriverID == "" {
		return db.ExternalDriverRef{}, false, false
	}
	return driver, true, true
}

type PublicDatabaseConnection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	DbType   string `json:"dbType"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database,omitempty"`
}

type cloudDatabaseBinding struct {
	SessionID      string
	SourceDeviceID string
}

type cloudDatabaseManager struct {
	mu       sync.Mutex
	bindings map[string]cloudDatabaseBinding
}

var cloudDatabases = &cloudDatabaseManager{bindings: map[string]cloudDatabaseBinding{}}

func listCloudDatabaseConnections() ([]PublicDatabaseConnection, error) {
	connections, err := loadDatabaseConnections()
	if err != nil {
		return nil, err
	}
	result := make([]PublicDatabaseConnection, 0, len(connections))
	for _, connection := range connections {
		if !cloudDatabaseConnectionSupported(connection) {
			continue
		}
		result = append(result, PublicDatabaseConnection{
			ID: connection.ID, Name: connection.Name, DbType: connection.DbType,
			Host: connection.Host, Port: connection.Port, Database: pointerValue(connection.Database),
		})
	}
	return result, nil
}

func (m *cloudDatabaseManager) closeAll() {
	m.mu.Lock()
	bindings := make([]cloudDatabaseBinding, 0, len(m.bindings))
	for _, binding := range m.bindings {
		bindings = append(bindings, binding)
	}
	m.bindings = map[string]cloudDatabaseBinding{}
	m.mu.Unlock()
	for _, binding := range bindings {
		db.CloseCloudReadOnly(binding.SessionID)
	}
}

func (m *cloudDatabaseManager) handlePayload(event CloudPayloadEvent) (bool, error) {
	if !strings.HasPrefix(event.Channel, "database.") {
		return false, nil
	}
	operation := strings.TrimPrefix(event.Channel, "database.")
	if operation == "connections.list" {
		connections, err := listCloudDatabaseConnections()
		return true, sendDatabaseResponse(event, map[string]any{"connections": connections}, err)
	}
	sessionID := stringValue(event.Payload["sessionId"])
	if !strings.HasPrefix(sessionID, "cloud-db-") || len(sessionID) > 100 {
		return true, errors.New("云数据库 sessionId 无效")
	}
	var result map[string]any
	var operationErr error
	switch operation {
	case "session.connect":
		connectionID := stringValue(event.Payload["connectionId"])
		result, operationErr = m.connect(event.SourceDeviceID, sessionID, connectionID)
	case "schema.get":
		operationErr = m.authorize(event.SourceDeviceID, sessionID)
		if operationErr == nil {
			var actual string
			actual, operationErr = m.actualSessionID(sessionID)
			var schema any
			if operationErr == nil {
				schema, operationErr = db.CloudSchema(actual)
			}
			result = map[string]any{"schema": schema}
		}
	case "query.read":
		operationErr = m.authorize(event.SourceDeviceID, sessionID)
		if operationErr == nil {
			query := stringValue(event.Payload["query"])
			var actual string
			actual, operationErr = m.actualSessionID(sessionID)
			var queryResult any
			if operationErr == nil {
				queryResult, operationErr = db.CloudReadOnlyQuery(actual, query)
			}
			result = map[string]any{"result": queryResult}
		}
	case "redis.keys", "redis.key":
		// Redis 只读浏览：只扫描 key、读取单个 key，不执行任意命令。
		operationErr = m.authorize(event.SourceDeviceID, sessionID)
		if operationErr == nil {
			var actual string
			actual, operationErr = m.actualSessionID(sessionID)
			if operationErr == nil {
				dbIndex := int64(number(event.Payload["dbIndex"]))
				if operation == "redis.keys" {
					var keys any
					keys, operationErr = db.CloudRedisKeys(actual, dbIndex,
						stringValue(event.Payload["pattern"]),
						stringValue(event.Payload["cursor"]),
						int64(number(event.Payload["count"])))
					result = map[string]any{"keys": keys}
				} else {
					var key any
					// key 原样使用，不去掉首尾空格（Redis key 可以包含空格）。
					rawKey, _ := event.Payload["key"].(string)
					key, operationErr = db.CloudRedisKey(actual, dbIndex, rawKey)
					result = map[string]any{"key": key}
				}
			}
		}
	case "session.close":
		operationErr = m.authorize(event.SourceDeviceID, sessionID)
		if operationErr == nil {
			actual, actualErr := m.actualSessionID(sessionID)
			if actualErr != nil {
				operationErr = actualErr
			} else {
				db.CloseCloudReadOnly(actual)
			}
			if operationErr == nil {
				m.mu.Lock()
				delete(m.bindings, sessionID)
				m.mu.Unlock()
				result = map[string]any{"closed": true}
			}
		}
	default:
		operationErr = errors.New("不支持的云数据库操作")
	}
	return true, sendDatabaseResponse(event, result, operationErr)
}

func (m *cloudDatabaseManager) connect(sourceDeviceID, sessionID, connectionID string) (map[string]any, error) {
	m.mu.Lock()
	existing, exists := m.bindings[sessionID]
	m.mu.Unlock()
	if exists {
		if existing.SourceDeviceID != sourceDeviceID {
			return nil, errors.New("云数据库会话已绑定其他设备")
		}
		return map[string]any{"connected": true, "sessionId": sessionID}, nil
	}
	connections, err := loadDatabaseConnections()
	if err != nil {
		return nil, err
	}
	var selected *storedDatabaseConnection
	for i := range connections {
		if connections[i].ID == connectionID {
			selected = &connections[i]
			break
		}
	}
	if selected == nil || selected.Tunnel != nil {
		return nil, errors.New("数据库连接不存在或移动端不支持隧道连接")
	}
	if !cloudDatabaseConnectionSupported(*selected) {
		return nil, errors.New("移动端不支持该数据库连接，插件驱动连接需先在桌面端授权")
	}
	config := db.DbConnectionConfig{
		DbType: selected.DbType, Host: selected.Host, Port: selected.Port,
		Username: selected.Username, Password: selected.Password, Database: selected.Database,
		FilePath: selected.FilePath, Token: selected.Token, Org: selected.Org,
		SSL: selected.SSL, AuthSource: selected.AuthSource,
	}
	if driver, bound, _ := cloudDatabaseDriver(*selected); bound {
		config.Driver = &driver
		config.ConnectionID = selected.ID
		config.Options = selected.Options
	}
	createdSessionID, err := db.ConnectCloudReadOnly(db.CloudReadOnlyConnection{Config: config})
	if err != nil {
		return nil, err
	}
	// DB registry owns generated IDs; binding maps the phone-selected stable ID to it.
	m.mu.Lock()
	m.bindings[sessionID] = cloudDatabaseBinding{SessionID: createdSessionID, SourceDeviceID: sourceDeviceID}
	m.mu.Unlock()
	return map[string]any{"connected": true, "sessionId": sessionID}, nil
}

func (m *cloudDatabaseManager) authorize(sourceDeviceID, sessionID string) error {
	m.mu.Lock()
	binding, ok := m.bindings[sessionID]
	m.mu.Unlock()
	if !ok || binding.SourceDeviceID != sourceDeviceID {
		return errors.New("无权操作该云数据库会话")
	}
	return nil
}

func (m *cloudDatabaseManager) actualSessionID(sessionID string) (string, error) {
	m.mu.Lock()
	binding, ok := m.bindings[sessionID]
	m.mu.Unlock()
	if !ok {
		return "", errors.New("云数据库会话不存在")
	}
	return binding.SessionID, nil
}

func sendDatabaseResponse(event CloudPayloadEvent, result map[string]any, operationErr error) error {
	response := map[string]any{
		"requestId": event.RequestID,
		"success":   operationErr == nil,
	}
	for key, value := range result {
		response[key] = value
	}
	if operationErr != nil {
		response["error"] = operationErr.Error()
	}
	raw, _ := json.Marshal(response)
	if len(raw) > 60*1024 {
		response = map[string]any{
			"requestId": event.RequestID,
			"success":   false,
			"error":     "数据库结果超过移动端单帧限制，请缩小查询范围",
		}
	}
	_, err := sendEncryptedCloudPayload(
		event.SourceDeviceID,
		"database.response",
		event.RequestID,
		event.Sequence,
		response,
	)
	return err
}

func loadDatabaseConnections() ([]storedDatabaseConnection, error) {
	raw, err := appKV(databaseConnectionsKey)
	if errors.Is(err, sql.ErrNoRows) {
		return []storedDatabaseConnection{}, nil
	}
	if err != nil {
		return nil, err
	}
	var connections []storedDatabaseConnection
	if err := json.Unmarshal(raw, &connections); err != nil {
		return nil, fmt.Errorf("解析数据库连接: %w", err)
	}
	return connections, nil
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
