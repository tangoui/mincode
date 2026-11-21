// PostgreSQL 系连接（jackc/pgx v5 的 database/sql 兼容层 stdlib）。
package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func buildPostgresSession(cfg *DbConnectionConfig) (*dbSession, error) {
	connectDB := "postgres"
	if d := trimmedOptional(cfg.Database); d != nil {
		connectDB = *d
	}
	sslMode := "disable"
	if cfg.SSL {
		sslMode = "require"
	}
	host := cfg.Host
	if strings.TrimSpace(host) == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 5432
	}

	u := &url.URL{
		Scheme: "postgres",
		Host:   fmt.Sprintf("%s:%d", host, port),
		Path:   "/" + connectDB,
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
	q.Set("sslmode", sslMode)
	u.RawQuery = q.Encode()

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		return nil, fmt.Errorf("PostgreSQL 打开失败: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("PostgreSQL 连接失败: %w", err)
	}
	return &dbSession{
		kind:            kindSQL,
		db:              db,
		dialect:         "postgres",
		defaultDatabase: connectDB,
	}, nil
}
