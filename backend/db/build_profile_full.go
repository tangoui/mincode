//go:build !lite

package db

import (
	"fmt"
	"strings"
)

// supportedDatabaseTypes lists the built-in types of this build; the
// nocoredrivers profile drops excludedCoreDrivers.
var supportedDatabaseTypes = withoutExcludedCoreDrivers([]string{
	"mysql", "mariadb", "tidb", "oceanbase", "polardb", "tdsql", "gbase", "gbase8t", "gbase8c",
	"goldendb", "starrocks", "doris", "postgres", "kingbase", "gaussdb", "opengauss", "highgo",
	"vastbase", "dolt", "greatsql", "opentenbase", "sqlite", "duckdb", "sqlserver", "oracle", "dameng", "yashandb", "firebird",
	"clickhouse", "tdengine", "mongodb", "redis", "influxdb", "elasticsearch", "opensearch",
	"cassandra", "neo4j", "couchdb", "meilisearch", "trino", "cockroachdb", "yugabyte", "greenplum", "questdb",
	"materialize", "risingwave", "pgvector", "qdrant", "milvus", "weaviate", "pinecone", "chroma",
	"presto", "selectdb", "databend", "cloudberry", "redshift", "databricks", "bigquery", "dremio",
	"kylin", "pinot", "snowflake",
})

func withoutExcludedCoreDrivers(types []string) []string {
	out := make([]string, 0, len(types))
	for _, dbType := range types {
		if _, excluded := excludedCoreDrivers[dbType]; !excluded {
			out = append(out, dbType)
		}
	}
	return out
}

func buildProfileSession(cfg *DbConnectionConfig) (*dbSession, error) {
	dbType := strings.ToLower(strings.TrimSpace(cfg.DbType))
	switch dbType {
	case "mysql", "tidb", "oceanbase", "mariadb", "polardb", "tdsql", "dolt", "greatsql",
		"gbase", "gbase8t", "goldendb", "starrocks", "doris", "selectdb", "databend":
		return buildMySQLSession(cfg)
	case "postgres", "postgresql", "kingbase", "gaussdb", "opengauss",
		"highgo", "vastbase", "opentenbase", "gbase8c", "cockroachdb", "cockroach", "yugabyte", "yugabytedb",
		"questdb", "materialize", "risingwave", "greenplum", "cloudberry", "redshift":
		return buildPostgresSession(cfg)
	case "pgvector":
		return buildPgvectorSession(cfg)
	case "dm", "dameng":
		return buildDMSession(cfg)
	case "tdengine", "taos":
		return buildTDengineSession(cfg)
	case "influxdb", "influx":
		return buildInfluxSession(cfg)
	case "elasticsearch", "es", "opensearch":
		return buildElasticsearchSession(cfg)
	case "meilisearch", "meili":
		return buildMeilisearchSession(cfg)
	case "couchdb", "couch":
		return buildCouchDBSession(cfg)
	case "qdrant", "milvus", "weaviate", "pinecone", "chroma":
		return buildVectorHTTPSession(cfg, dbType)
	case "databricks", "bigquery", "dremio", "kylin", "pinot", "snowflake":
		return buildWarehouseHTTPSession(cfg, dbType)
	case "trino", "presto":
		return buildTrinoSession(cfg)
	case "cassandra", "scylla", "scylladb":
		return buildCassandraSession(cfg)
	case "neo4j":
		return buildNeo4jSession(cfg)
	case "sqlite", "sqlite3":
		return buildSQLiteSession(cfg)
	case "duckdb":
		return buildDuckDBSession(cfg)
	case "redis":
		return buildRedisSession(cfg)
	case "sqlserver", "mssql":
		return buildSQLServerSession(cfg)
	case "oracle":
		return buildOracleSession(cfg)
	case "yashandb", "yasdb":
		return buildYashanDBSession(cfg)
	case "firebird", "firebirdsql":
		return buildFirebirdSession(cfg)
	case "clickhouse":
		return buildClickHouseSession(cfg)
	case "mongodb":
		return buildMongoSession(cfg)
	default:
		return nil, fmt.Errorf("暂不支持的数据库类型: %s", cfg.DbType)
	}
}
