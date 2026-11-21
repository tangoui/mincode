//go:build lite

package db

import (
	"fmt"
	"strings"
)

var supportedDatabaseTypes = []string{"sqlite", "mysql", "postgres", "redis", "mongodb"}

func buildProfileSession(cfg *DbConnectionConfig) (*dbSession, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.DbType)) {
	case "sqlite", "sqlite3":
		return buildSQLiteSession(cfg)
	case "mysql":
		return buildMySQLSession(cfg)
	case "postgres", "postgresql":
		return buildPostgresSession(cfg)
	case "redis":
		return buildRedisSession(cfg)
	case "mongodb":
		return buildMongoSession(cfg)
	default:
		return nil, fmt.Errorf("Lite 版本不支持数据库类型: %s，请使用 Full 版本", cfg.DbType)
	}
}
