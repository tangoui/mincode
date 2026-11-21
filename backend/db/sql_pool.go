package db

import (
	"database/sql"
	"time"
)

const (
	remoteSQLMaxOpen = 6
	remoteSQLMaxIdle = 2
)

const (
	remoteSQLConnMaxIdleTime = 10 * time.Minute
	remoteSQLConnMaxLifetime = time.Hour
)

func configureRemoteSQLPool(db *sql.DB) {
	if db == nil {
		return
	}
	db.SetMaxOpenConns(remoteSQLMaxOpen)
	db.SetMaxIdleConns(remoteSQLMaxIdle)
	db.SetConnMaxIdleTime(remoteSQLConnMaxIdleTime)
	db.SetConnMaxLifetime(remoteSQLConnMaxLifetime)
}

func configureSessionSQLPool(session *dbSession) {
	if session == nil || session.kind != kindSQL || session.db == nil {
		return
	}
	if session.dialect == "sqlite" || session.dialect == "duckdb" {
		return
	}
	configureRemoteSQLPool(session.db)
}
