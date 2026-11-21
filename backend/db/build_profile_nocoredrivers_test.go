//go:build !lite && nocoredrivers

package db

import (
	"context"
	"strings"
	"testing"
)

func TestCoreProfileExcludesUnavailableDrivers(t *testing.T) {
	available := make(map[string]struct{}, len(supportedDatabaseTypes))
	for _, dbType := range supportedDatabaseTypes {
		available[dbType] = struct{}{}
	}
	for _, dbType := range []string{"duckdb", "oracle", "yashandb", "clickhouse", "dameng"} {
		if _, ok := available[dbType]; ok {
			t.Fatalf("core profile still lists %q in supportedDatabaseTypes", dbType)
		}
	}
	for _, dbType := range []string{"sqlite", "mysql", "postgres", "sqlserver", "tdengine", "firebird"} {
		if _, ok := available[dbType]; !ok {
			t.Fatalf("core profile is missing %q", dbType)
		}
	}
}

func TestCoreProfileSavedConnectionsReportUnavailableDriver(t *testing.T) {
	path, service, user := t.TempDir()+"/saved.duckdb", "ORCL", "app"
	for _, cfg := range []*DbConnectionConfig{
		{DbType: "duckdb", FilePath: &path},
		{DbType: " Oracle ", Host: "db.example.test", Port: 1521, Database: &service},
		{DbType: "yashandb", Host: "db.example.test", Port: 1688, Username: &user},
		{DbType: "yasdb", Host: "db.example.test", Port: 1688, Username: &user},
		{DbType: "ClickHouse", Host: "db.example.test", Port: 9000, Database: &service},
		{DbType: "dameng", Host: "db.example.test", Port: 5236, Username: &user},
		{DbType: "dm", Host: "db.example.test", Port: 5236, Username: &user},
	} {
		_, err := buildProfileSession(cfg)
		if err == nil || !strings.HasPrefix(err.Error(), "driver_required: 此版本未内置 ") {
			t.Fatalf("buildProfileSession(%s) error = %v, want unavailable driver error", cfg.DbType, err)
		}
	}
	if _, err := duckdbSchema(context.Background(), nil); err == nil || !strings.HasPrefix(err.Error(), "driver_required:") {
		t.Fatalf("duckdbSchema error = %v", err)
	}
	if got := duckdbSchemaName(" "); got != "main" {
		t.Fatalf("duckdbSchemaName = %q", got)
	}
}
