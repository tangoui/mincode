// 数据库域公共类型：连接配置、统一查询结果、Schema 树、列信息。
// JSON 字段全部 camelCase，与前端 / Tauri 命令保持一致。
package db

import (
	"encoding/json"
	"strings"
)

// DbConnectionConfig 前端传来的数据库连接配置。
type DbConnectionConfig struct {
	DbType     string  `json:"dbType"`
	Host       string  `json:"host"`
	Port       int     `json:"port"`
	Username   *string `json:"username,omitempty"`
	Password   *string `json:"password,omitempty"`
	Database   *string `json:"database,omitempty"`
	FilePath   *string `json:"filePath,omitempty"`
	Token      *string `json:"token,omitempty"`
	Org        *string `json:"org,omitempty"`
	SSL        bool    `json:"ssl"`
	AuthSource *string `json:"authSource,omitempty"`
	// Driver binds the connection to a plugin-provided driver
	// (contributes.databaseDrivers); nil means the built-in driver for DbType.
	// Plugin-bound configs never fall back to a built-in driver.
	Driver *ExternalDriverRef `json:"driver,omitempty"`
	// ConnectionID is the saved connection's id (≤128), used to match the
	// user's credential grant for Driver and to file SQL safety records under
	// the connection (built-in drivers too).
	ConnectionID string `json:"connectionId,omitempty"`
	// Options are the driver-declared connection options; validated against
	// the driver's option schema by the plugin host before connect.
	Options map[string]any `json:"options,omitempty"`
}

// QueryResult 统一查询结果，兼容各 DB 类型。
type QueryResult struct {
	Columns       []string `json:"columns"`
	Rows          [][]any  `json:"rows"`
	RowCount      int      `json:"rowCount"`
	AffectedRows  *int64   `json:"affectedRows"`
	ExecutionMs   int64    `json:"executionMs"`
	Notice        *string  `json:"notice"`
	NextPageToken *string  `json:"nextPageToken,omitempty"`
}

// SchemaTable Schema 树中的一张表 / 视图 / 集合。
type SchemaTable struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	RowCount  *int64  `json:"rowCount,omitempty"`
	SizeBytes *int64  `json:"sizeBytes,omitempty"`
	CreatedAt *string `json:"createdAt,omitempty"`
	UpdatedAt *string `json:"updatedAt,omitempty"`
	Comment   *string `json:"comment,omitempty"`
}

// SchemaDatabase Schema 树中的一个数据库 / schema。
type SchemaDatabase struct {
	Name   string        `json:"name"`
	Tables []SchemaTable `json:"tables"`
}

// DbSchemaInfo 整个 Schema 信息（左侧对象树）。
type DbSchemaInfo struct {
	Databases []SchemaDatabase `json:"databases"`
	Catalog   *string          `json:"catalog,omitempty"`
}

// ColumnInfo 列信息。
type ColumnInfo struct {
	Name          string  `json:"name"`
	DataType      string  `json:"dataType"`
	Nullable      bool    `json:"nullable"`
	IsPrimaryKey  bool    `json:"isPrimaryKey"`
	DefaultValue  *string `json:"defaultValue"`
	Comment       *string `json:"comment,omitempty"`
	AutoIncrement bool    `json:"autoIncrement"`
}

// trimmedOptional 去空白后返回非空字符串指针，否则 nil。
func trimmedOptional(v *string) *string {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		return nil
	}
	return &s
}

// rawJSON 便捷别名。
type rawJSON = json.RawMessage
