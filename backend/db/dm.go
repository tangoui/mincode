//go:build !lite

// 达梦 DM 连接（gitee.com/chunanyong/dm，纯 Go，无需 dm-agent sidecar）。
//
// DSN 格式：dm://user:password@host:port
// 达梦 DM8 SQL 方言接近 Oracle（大写标识符、双引号），分页支持 LIMIT n OFFSET m。
package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "gitee.com/chunanyong/dm"
)

func buildDMSession(cfg *DbConnectionConfig) (*dbSession, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 5236
	}
	user := derefOr(cfg.Username, "")
	pass := derefOr(cfg.Password, "")

	// dm://user:password@host:port
	// 注意：chunanyong/dm 驱动的 parseDSN 只是对 "user:password@host" 做简单的
	// strings.SplitN(":", 2) 字符串切分，并不会对 userinfo 做 URL 反转义（即使驱动自身
	// BuildDSN() 会用 QueryEscape 编码，解析端却不对称地不 decode）。
	// 如果这里用 url.QueryEscape 编码，密码中的特殊字符（如 "!" -> "%21"）会被驱动
	// 原样当作密码的一部分，导致密码不匹配、连接报 "用户名或密码错误"。因此这里不做转义，
	// 直接原样拼接（要求用户名/密码不包含 ":" "@" "/" "?" 等 DSN 分隔符）。
	dsn := fmt.Sprintf("dm://%s:%s@%s:%d", user, pass, host, port)

	db, err := sql.Open("dm", dsn)
	if err != nil {
		return nil, fmt.Errorf("达梦打开失败: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("达梦连接失败: %w", err)
	}

	defaultDB := ""
	if d := trimmedOptional(cfg.Database); d != nil {
		defaultDB = *d
	}
	return &dbSession{
		kind:            kindSQL,
		db:              db,
		dialect:         "dm",
		defaultDatabase: defaultDB,
	}, nil
}
