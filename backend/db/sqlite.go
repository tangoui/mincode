// SQLite 连接（纯 Go 驱动 modernc.org/sqlite，无需 CGO）。
package db

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

func buildSQLiteSession(cfg *DbConnectionConfig) (*dbSession, error) {
	path := ""
	if cfg.FilePath != nil {
		path = strings.TrimSpace(*cfg.FilePath)
	}
	var dsn string
	if path == "" || path == ":memory:" {
		dsn = ":memory:"
	} else {
		dsn = path
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("SQLite 打开失败: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite 单写者，避免锁竞争
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("SQLite 连接失败: %w", err)
	}
	return &dbSession{kind: kindSQL, db: db, dialect: "sqlite"}, nil
}
