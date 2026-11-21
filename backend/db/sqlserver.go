//go:build !lite

// SQL Server 连接（microsoft/go-mssqldb，database/sql 兼容）。
package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/microsoft/go-mssqldb"
)

func buildSQLServerSession(cfg *DbConnectionConfig) (*dbSession, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 1433
	}

	u := &url.URL{
		Scheme: "sqlserver",
		Host:   fmt.Sprintf("%s:%d", host, port),
	}
	user := derefOr(cfg.Username, "")
	if user != "" {
		if pass := derefOr(cfg.Password, ""); pass != "" {
			u.User = url.UserPassword(user, pass)
		} else {
			u.User = url.User(user)
		}
	}
	q := u.Query()
	if d := trimmedOptional(cfg.Database); d != nil {
		q.Set("database", *d)
	}
	if cfg.SSL {
		q.Set("encrypt", "true")
	} else {
		q.Set("encrypt", "disable")
	}
	u.RawQuery = q.Encode()

	db, err := sql.Open("sqlserver", u.String())
	if err != nil {
		return nil, fmt.Errorf("SQL Server 打开失败: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("SQL Server 连接失败: %w", err)
	}
	return &dbSession{
		kind:            kindSQL,
		db:              db,
		dialect:         "sqlserver",
		defaultDatabase: derefOr(trimmedOptional(cfg.Database), ""),
	}, nil
}
