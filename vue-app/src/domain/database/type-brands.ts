/**
 * Identity colors and glyph kinds for built-in database types. Pure data so the
 * type icon and the connection overview share one source.
 */
import type { DbType } from "./types";

export type DatabaseIconKind = "database" | "leaf" | "layers" | "star" | "nodes" | "bars" | "snowflake";

export interface DatabaseTypeBrand {
  color: string;
  icon: DatabaseIconKind;
}

const databaseTypeBrands: Partial<Record<DbType, DatabaseTypeBrand>> = {
  mysql: { color: "#4479a1", icon: "database" },
  mariadb: { color: "#087c88", icon: "database" },
  tidb: { color: "#d93f35", icon: "database" },
  oceanbase: { color: "#2878e8", icon: "database" },
  polardb: { color: "#2878e8", icon: "database" },
  tdsql: { color: "#2878e8", icon: "database" },
  dolt: { color: "#2a9d8f", icon: "database" },
  greatsql: { color: "#2b6cb0", icon: "database" },
  gbase: { color: "#c83f3f", icon: "database" },
  gbase8t: { color: "#c83f3f", icon: "database" },
  gbase8c: { color: "#c83f3f", icon: "database" },
  goldendb: { color: "#b47b16", icon: "database" },
  starrocks: { color: "#5865e8", icon: "star" },
  doris: { color: "#6259d9", icon: "star" },
  postgres: { color: "#336791", icon: "database" },
  kingbase: { color: "#c93d42", icon: "database" },
  gaussdb: { color: "#c83737", icon: "database" },
  opengauss: { color: "#d04444", icon: "database" },
  highgo: { color: "#c93d42", icon: "database" },
  vastbase: { color: "#3575cf", icon: "database" },
  opentenbase: { color: "#1f7ad4", icon: "database" },
  sqlite: { color: "#2485b5", icon: "database" },
  duckdb: { color: "#b68b00", icon: "database" },
  sqlserver: { color: "#ad4146", icon: "database" },
  oracle: { color: "#c74634", icon: "database" },
  dameng: { color: "#bd3b4c", icon: "database" },
  yashandb: { color: "#d45b37", icon: "database" },
  firebird: { color: "#d44444", icon: "database" },
  clickhouse: { color: "#a98100", icon: "bars" },
  tdengine: { color: "#2878e8", icon: "bars" },
  mongodb: { color: "#3c9745", icon: "leaf" },
  redis: { color: "#d34236", icon: "layers" },
  influxdb: { color: "#7664d9", icon: "nodes" },
  elasticsearch: { color: "#167d98", icon: "nodes" },
  opensearch: { color: "#2876b8", icon: "nodes" },
  cassandra: { color: "#2689ae", icon: "nodes" },
  neo4j: { color: "#2787c9", icon: "nodes" },
  couchdb: { color: "#d24747", icon: "nodes" },
  meilisearch: { color: "#ff4e62", icon: "nodes" },
  trino: { color: "#bd338f", icon: "nodes" },
  cockroachdb: { color: "#6843d9", icon: "nodes" },
  yugabyte: { color: "#db6949", icon: "nodes" },
  greenplum: { color: "#2a8a62", icon: "database" },
  questdb: { color: "#d84f4f", icon: "bars" },
  materialize: { color: "#705ed3", icon: "bars" },
  risingwave: { color: "#7265d3", icon: "bars" },
  pgvector: { color: "#336791", icon: "nodes" },
  qdrant: { color: "#cc3658", icon: "nodes" },
  milvus: { color: "#268fc2", icon: "nodes" },
  weaviate: { color: "#258e74", icon: "nodes" },
  pinecone: { color: "#4c5b70", icon: "nodes" },
  chroma: { color: "#d2673e", icon: "nodes" },
  presto: { color: "#4f7fd1", icon: "nodes" },
  selectdb: { color: "#6c59d1", icon: "database" },
  databend: { color: "#3979ca", icon: "database" },
  cloudberry: { color: "#8263ca", icon: "database" },
  redshift: { color: "#7c5bc2", icon: "bars" },
  databricks: { color: "#d84a37", icon: "layers" },
  bigquery: { color: "#4285d4", icon: "bars" },
  dremio: { color: "#3f9cab", icon: "database" },
  kylin: { color: "#31929d", icon: "database" },
  pinot: { color: "#d85b51", icon: "bars" },
  snowflake: { color: "#299cc4", icon: "snowflake" },
};

const fallbackBrand: DatabaseTypeBrand = { color: "#718096", icon: "database" };

/**
 * Identity colors of the official plugin drivers, keyed "pluginId/driverId".
 * Each is the main color of the driver's own icon (official-plugins/<id>/assets),
 * so the tinted tile matches the icon drawn on it.
 */
/** Brand for a database type; unknown or plugin-only types use a neutral fallback. */
export function databaseTypeBrand(dbType: string): DatabaseTypeBrand {
  return databaseTypeBrands[dbType as DbType] ?? fallbackBrand;
}

export function databaseConnectionBrand(connection: { dbType: string }): DatabaseTypeBrand {
  return databaseTypeBrand(connection.dbType);
}
