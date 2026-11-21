//go:build !lite

package db

import "fmt"

// excludedCoreDrivers maps the built-in types this profile leaves out to
// their display names.
var excludedCoreDrivers = map[string]string{
	"duckdb":     "DuckDB",
	"oracle":     "Oracle",
	"yashandb":   "YashanDB",
	"clickhouse": "ClickHouse",
}

// coreDriverUnavailable is prefixed with the driver_required code the
// frontend recognises (databaseDriverErrorCode).
func coreDriverUnavailable(name string) error {
	return fmt.Errorf("driver_required: 此版本未内置 %s 驱动", name)
}

func buildDuckDBSession(*DbConnectionConfig) (*dbSession, error) {
	return nil, coreDriverUnavailable("DuckDB")
}

func buildOracleSession(*DbConnectionConfig) (*dbSession, error) {
	return nil, coreDriverUnavailable("Oracle")
}

func buildYashanDBSession(*DbConnectionConfig) (*dbSession, error) {
	return nil, coreDriverUnavailable("YashanDB")
}

func buildClickHouseSession(*DbConnectionConfig) (*dbSession, error) {
	return nil, coreDriverUnavailable("ClickHouse")
}
