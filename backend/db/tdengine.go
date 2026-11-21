//go:build !lite

// TDengine 连接（taosdata/driver-go v3 taosRestful，纯 Go REST 通道，无需 CGO）。
//
// DSN 格式：user:password@http(host:port)/db，默认 REST 端口 6041。
// SSL 时使用 https(...)。TDengine 使用标准 SQL 子集，走 database/sql。
package db

import (
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"

	_ "github.com/taosdata/driver-go/v3/taosRestful"
)

// tdengineRestAddress 构造 TDengine REST 地址。默认 localhost 使用 IPv4 回环，
// 避免 Go 将它优先解析为 ::1，而本地 TDengine REST 服务仅监听 IPv4 的情况。
func tdengineRestAddress(cfg *DbConnectionConfig) string {
	host := strings.TrimSpace(cfg.Host)
	if host == "" || strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 6041
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func buildTDengineSession(cfg *DbConnectionConfig) (*dbSession, error) {
	scheme := "http"
	if cfg.SSL {
		scheme = "https"
	}
	user := derefOr(cfg.Username, "")
	pass := derefOr(cfg.Password, "")
	dbName := ""
	if d := trimmedOptional(cfg.Database); d != nil {
		dbName = *d
	}

	// user:password@http(host:port)/db
	dsn := fmt.Sprintf("%s:%s@%s(%s)/%s", user, pass, scheme, tdengineRestAddress(cfg), dbName)

	db, err := sql.Open("taosRestful", dsn)
	if err != nil {
		return nil, fmt.Errorf("TDengine 打开失败: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("TDengine 连接失败: %w", err)
	}
	return &dbSession{
		kind:            kindSQL,
		db:              db,
		dialect:         "tdengine",
		defaultDatabase: dbName,
	}, nil
}
