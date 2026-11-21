//go:build lite

package db

import (
	"context"
	"fmt"
)

func liteUnsupported(name string) error {
	return fmt.Errorf("Lite 版本不支持 %s，请使用 Full 版本", name)
}

// coreDriverUnavailable is the error of the DuckDB stubs in core_driver_stubs.go.
func coreDriverUnavailable(name string) error { return liteUnsupported(name) }

func cassandraExecuteQuery(context.Context, *dbSession, string) (*QueryResult, error) {
	return nil, liteUnsupported("Cassandra")
}
func cassandraSchema(context.Context, *dbSession) (*DbSchemaInfo, error) {
	return nil, liteUnsupported("Cassandra")
}
func cassandraColumns(context.Context, *dbSession, string, string) ([]ColumnInfo, error) {
	return nil, liteUnsupported("Cassandra")
}
func cassandraTableRows(context.Context, *dbSession, string, string, uint32) (*QueryResult, error) {
	return nil, liteUnsupported("Cassandra")
}
func cassandraPrimaryKeys(context.Context, *dbSession, string, string) (*PrimaryKeyInfo, error) {
	return nil, liteUnsupported("Cassandra")
}
func cassandraIndexes(context.Context, *dbSession, string, string) ([]DbIndexInfo, error) {
	return nil, liteUnsupported("Cassandra")
}
func cassandraUpdateCell(context.Context, *dbSession, string, string, map[string]any, string, any) (int64, error) {
	return 0, liteUnsupported("Cassandra")
}
func cassandraInsertRow(context.Context, *dbSession, string, string, map[string]any) (int64, error) {
	return 0, liteUnsupported("Cassandra")
}
func cassandraDeleteRow(context.Context, *dbSession, string, string, map[string]any) (int64, error) {
	return 0, liteUnsupported("Cassandra")
}

func neo4jExecuteQuery(context.Context, *dbSession, string) (*QueryResult, error) {
	return nil, liteUnsupported("Neo4j")
}
func neo4jSchema(context.Context, *dbSession) (*DbSchemaInfo, error) {
	return nil, liteUnsupported("Neo4j")
}
func neo4jTableRows(context.Context, *dbSession, string, uint32, uint32) (*QueryResult, error) {
	return nil, liteUnsupported("Neo4j")
}

func firebirdSchema(context.Context, *dbSession) (*DbSchemaInfo, error) {
	return nil, liteUnsupported("Firebird")
}
func firebirdColumns(context.Context, *dbSession, string) ([]ColumnInfo, error) {
	return nil, liteUnsupported("Firebird")
}
func firebirdPrimaryKeys(context.Context, *dbSession, string) (*PrimaryKeyInfo, error) {
	return nil, liteUnsupported("Firebird")
}
func firebirdIndexes(context.Context, *dbSession, string) ([]DbIndexInfo, error) {
	return nil, liteUnsupported("Firebird")
}
func firebirdForeignKeys(context.Context, *dbSession, string) ([]DbForeignKeyInfo, error) {
	return nil, liteUnsupported("Firebird")
}
func firebirdTriggers(context.Context, *dbSession, string) ([]DbTriggerInfo, error) {
	return nil, liteUnsupported("Firebird")
}
func firebirdTableDDL(context.Context, *dbSession, string) (string, error) {
	return "", liteUnsupported("Firebird")
}

func normalizeTrinoQuery(query string) string { return query }
func trinoSchema(context.Context, *dbSession) (*DbSchemaInfo, error) {
	return nil, liteUnsupported("Trino")
}
func trinoColumns(context.Context, *dbSession, string, string) ([]ColumnInfo, error) {
	return nil, liteUnsupported("Trino")
}
