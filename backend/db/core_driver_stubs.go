// DuckDB metadata stubs for builds without the built-in DuckDB driver (Lite
// and the nocoredrivers core profile). The error comes from the profile's
// coreDriverUnavailable, so a saved DuckDB connection never fails silently.
package db

import (
	"context"
	"strings"
)

func duckdbSchema(context.Context, *dbSession) (*DbSchemaInfo, error) {
	return nil, coreDriverUnavailable("DuckDB")
}
func duckdbColumns(context.Context, *dbSession, string, string) ([]ColumnInfo, error) {
	return nil, coreDriverUnavailable("DuckDB")
}
func duckdbPrimaryKeys(context.Context, *dbSession, string, string) (*PrimaryKeyInfo, error) {
	return nil, coreDriverUnavailable("DuckDB")
}
func duckdbIndexes(context.Context, *dbSession, string, string) ([]DbIndexInfo, error) {
	return nil, coreDriverUnavailable("DuckDB")
}
func duckdbForeignKeys(context.Context, *dbSession, string, string) ([]DbForeignKeyInfo, error) {
	return nil, coreDriverUnavailable("DuckDB")
}
func duckdbTableDDL(context.Context, *dbSession, string, string) (string, error) {
	return "", coreDriverUnavailable("DuckDB")
}

// duckdbSchemaName is shared with table_ops.go (plugin-driver sessions of
// dialect duckdb keep using it).
func duckdbSchemaName(schema string) string {
	if schema = strings.TrimSpace(schema); schema != "" {
		return schema
	}
	return "main"
}
