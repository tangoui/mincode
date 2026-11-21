export type DbType =
  | "mysql"
  | "mariadb"
  | "tidb"
  | "oceanbase"
  | "polardb"
  | "tdsql"
  | "dolt"
  | "greatsql"
  | "gbase"
  | "gbase8t"
  | "gbase8c"
  | "goldendb"
  | "starrocks"
  | "doris"
  | "postgres"
  | "kingbase"
  | "gaussdb"
  | "opengauss"
  | "highgo"
  | "vastbase"
  | "opentenbase"
  | "sqlite"
  | "duckdb"
  | "sqlserver"
  | "oracle"
  | "dameng"
  | "yashandb"
  | "firebird"
  | "clickhouse"
  | "tdengine"
  | "mongodb"
  | "redis"
  | "influxdb"
  | "elasticsearch"
  | "opensearch"
  | "cassandra"
  | "neo4j"
  | "couchdb"
  | "meilisearch"
  | "trino"
  | "cockroachdb"
  | "yugabyte"
  | "greenplum"
  | "questdb"
  | "materialize"
  | "risingwave"
  | "pgvector"
  | "qdrant"
  | "milvus"
  | "weaviate"
  | "pinecone"
  | "chroma"
  | "presto"
  | "selectdb"
  | "databend"
  | "cloudberry"
  | "redshift"
  | "databricks"
  | "bigquery"
  | "dremio"
  | "kylin"
  | "pinot"
  | "snowflake"
  // Plugin-provided driver (contributes.databaseDrivers); the concrete driver
  // is DatabaseConnection.driver. Never offered by the built-in type picker.
  | "plugin";

/** Plugin driver reference; mirrors Go db.ExternalDriverRef. */
export interface DatabaseDriverRef {
  pluginId: string;
  driverId: string;
}

export type DatabaseTunnelConfig =
  | {
      type: "ssh";
      host: string;
      port: number;
      username: string;
      authType: "password" | "key";
      password?: string;
      keyPath?: string;
      passphrase?: string;
    }
  | {
      type: "socks5";
      host: string;
      port: number;
      username?: string;
      password?: string;
    }
  | {
      type: "http_connect";
      host: string;
      port: number;
      username?: string;
      password?: string;
    };

export interface DatabaseConnection {
  id: string;
  name: string;
  dbType: DbType;
  host: string;
  port: number;
  username?: string;
  password?: string;
  database?: string;
  keyspace?: string;
  filePath?: string;
  token?: string;
  org?: string;
  authSource?: string;
  ssl: boolean;
  savePassword: boolean;
  group?: string;
  tags?: string[];
  description?: string;
  favorite?: boolean;
  lastConnectedAt?: number;
  tunnel?: DatabaseTunnelConfig;
  /** Set only when dbType is "plugin"; T4b persists and edits it. */
  driver?: DatabaseDriverRef;
  /** Driver-declared connection options (plugin drivers only). */
  options?: Record<string, unknown>;
  createdAt: number;
  updatedAt: number;
}

export interface DatabaseConnectionConfig {
  dbType: DbType;
  host: string;
  port: number;
  username?: string;
  password?: string;
  database?: string;
  filePath?: string;
  token?: string;
  org?: string;
  authSource?: string;
  ssl: boolean;
  /** Plugin driver binding; mirrors Go DbConnectionConfig.driver. */
  driver?: DatabaseDriverRef;
  /** Saved connection id: files SQL safety records; also matches the credential grant for driver. */
  connectionId?: string;
  options?: Record<string, unknown>;
}

export interface SchemaTable {
  name: string;
  kind: string;
  rowCount?: number | null;
  sizeBytes?: number | null;
  createdAt?: string | null;
  updatedAt?: string | null;
  comment?: string | null;
}

export interface SchemaDatabase {
  name: string;
  tables: SchemaTable[];
}

export interface DatabaseSchemaInfo {
  databases: SchemaDatabase[];
  catalog?: string;
}

export type DatabaseSchemaSyncRisk = "safe" | "review" | "dangerous";

export interface DatabaseSchemaColumnSnapshot {
  name: string;
  dataType: string;
  nullable: boolean;
  defaultValue?: string | null;
  autoIncrement: boolean;
}

export interface DatabaseSchemaIndexSnapshot {
  name: string;
  columns: string[];
  unique: boolean;
}

export interface DatabaseSchemaTableSnapshot {
  name: string;
  columns: DatabaseSchemaColumnSnapshot[];
  primaryKey: string[];
  indexes: DatabaseSchemaIndexSnapshot[];
}

export interface DatabaseSchemaSnapshot {
  dialect: string;
  database: string;
  tables: DatabaseSchemaTableSnapshot[];
  fingerprint: string;
}

export type DatabaseSchemaChangeKind =
  | "create_table"
  | "drop_table"
  | "add_column"
  | "drop_column"
  | "alter_column"
  | "add_primary_key"
  | "drop_primary_key"
  | "create_index"
  | "drop_index";

export interface DatabaseSchemaChange {
  id: string;
  kind: DatabaseSchemaChangeKind;
  table: string;
  objectName: string;
  risk: DatabaseSchemaSyncRisk;
  defaultSelected: boolean;
  sourceTable?: DatabaseSchemaTableSnapshot;
  targetTable?: DatabaseSchemaTableSnapshot;
  sourceColumn?: DatabaseSchemaColumnSnapshot;
  targetColumn?: DatabaseSchemaColumnSnapshot;
  sourceIndex?: DatabaseSchemaIndexSnapshot;
  targetIndex?: DatabaseSchemaIndexSnapshot;
  sourceColumns?: string[];
  targetColumns?: string[];
}

export interface DatabaseSchemaComparison {
  source: DatabaseSchemaSnapshot;
  target: DatabaseSchemaSnapshot;
  changes: DatabaseSchemaChange[];
}

export interface DatabaseSchemaSyncStatement {
  changeId: string;
  risk: DatabaseSchemaSyncRisk;
  sql: string;
}

export interface DatabaseSchemaSyncPlan {
  dialect: string;
  database: string;
  sourceFingerprint: string;
  targetFingerprint: string;
  statements: DatabaseSchemaSyncStatement[];
}

export interface DatabaseSchemaSyncStatementExecution extends DatabaseSchemaSyncStatement {
  status: "running" | "succeeded" | "failed";
  error?: string | null;
}

export interface DatabaseSchemaSyncExecutionResult {
  completed: boolean;
  statements: DatabaseSchemaSyncStatementExecution[];
  target: DatabaseSchemaSnapshot;
  refreshError?: string | null;
}

export interface DatabaseSchemaSyncSelection {
  sourceConnectionId: string;
  sourceDatabase: string;
  targetConnectionId: string;
  targetDatabase: string;
}

export interface DatabaseQueryResult {
  columns: string[];
  rows: unknown[][];
  rowCount: number;
  affectedRows?: number;
  executionMs: number;
  notice?: string;
  nextPageToken?: string;
}

export interface DatabaseColumnInfo {
  name: string;
  dataType: string;
  nullable: boolean;
  isPrimaryKey: boolean;
  defaultValue?: string | null;
  comment?: string | null;
  autoIncrement: boolean;
}

export interface RedisKeyListItem {
  key: string;
  type: string;
  ttl: number;
}

export interface RedisKeyListResult {
  keys: RedisKeyListItem[];
  nextCursor: string;
  total: number;
}

export interface RedisKeyInfo {
  type: string;
  ttl: number;
  value: unknown;
  dbIndex: number;
  /** 值含非 UTF-8 字节；value 已被 JSON 替换为 U+FFFD，只能只读查看。 */
  binary?: boolean;
  /** binary 的 string 类型原始字节（十六进制），此时 value 为 null。 */
  valueHex?: string;
}

export const databaseDefaultPorts: Record<DbType, number> = {
  mysql: 3306,
  mariadb: 3306,
  tidb: 4000,
  oceanbase: 2881,
  polardb: 3306,
  tdsql: 3306,
  dolt: 3306,
  greatsql: 3306,
  gbase: 5258,
  gbase8t: 9088,
  gbase8c: 5432,
  goldendb: 3306,
  starrocks: 9030,
  doris: 9030,
  postgres: 5432,
  kingbase: 54321,
  gaussdb: 5432,
  opengauss: 5432,
  highgo: 5866,
  vastbase: 5432,
  opentenbase: 5432,
  sqlite: 0,
  duckdb: 0,
  sqlserver: 1433,
  oracle: 1521,
  dameng: 5236,
  yashandb: 1688,
  firebird: 3050,
  clickhouse: 8123,
  tdengine: 6041,
  mongodb: 27017,
  redis: 6379,
  influxdb: 8086,
  elasticsearch: 9200,
  opensearch: 9200,
  cassandra: 9042,
  neo4j: 7687,
  couchdb: 5984,
  meilisearch: 7700,
  trino: 8080,
  cockroachdb: 26257,
  yugabyte: 5433,
  greenplum: 5432,
  questdb: 8812,
  materialize: 6875,
  risingwave: 4513,
  pgvector: 5432,
  qdrant: 6333,
  milvus: 19530,
  weaviate: 8080,
  pinecone: 443,
  chroma: 8000,
  presto: 8080,
  selectdb: 9030,
  databend: 3307,
  cloudberry: 5432,
  redshift: 5439,
  databricks: 443,
  bigquery: 443,
  dremio: 9047,
  kylin: 7070,
  pinot: 8099,
  snowflake: 443,
  plugin: 0,
};

export interface DatabaseDriverInfo {
  label: string;
  available: boolean;
}

export const databaseDriverInfo: Record<DbType, DatabaseDriverInfo> = {
  mysql: { label: "MySQL", available: true },
  mariadb: { label: "MySQL", available: true },
  tidb: { label: "MySQL", available: true },
  oceanbase: { label: "MySQL", available: true },
  polardb: { label: "MySQL", available: true },
  tdsql: { label: "MySQL", available: true },
  dolt: { label: "MySQL", available: true },
  greatsql: { label: "MySQL", available: true },
  gbase: { label: "MySQL", available: true },
  gbase8t: { label: "MySQL", available: true },
  gbase8c: { label: "PostgreSQL", available: true },
  goldendb: { label: "MySQL", available: true },
  starrocks: { label: "MySQL", available: true },
  doris: { label: "MySQL", available: true },
  postgres: { label: "PostgreSQL", available: true },
  kingbase: { label: "PostgreSQL", available: true },
  gaussdb: { label: "PostgreSQL", available: true },
  opengauss: { label: "PostgreSQL", available: true },
  highgo: { label: "PostgreSQL", available: true },
  vastbase: { label: "PostgreSQL", available: true },
  opentenbase: { label: "PostgreSQL", available: true },
  sqlite: { label: "SQLite", available: true },
  duckdb: { label: "DuckDB", available: true },
  sqlserver: { label: "SQL Server", available: true },
  oracle: { label: "Oracle", available: true },
  dameng: { label: "DM", available: true },
  yashandb: { label: "YashanDB Go + C Client", available: true },
  firebird: { label: "Firebird", available: true },
  clickhouse: { label: "ClickHouse", available: true },
  tdengine: { label: "TDengine REST", available: true },
  mongodb: { label: "MongoDB", available: true },
  redis: { label: "Redis", available: true },
  influxdb: { label: "InfluxDB HTTP", available: true },
  elasticsearch: { label: "Elasticsearch REST", available: true },
  opensearch: { label: "Elasticsearch REST", available: true },
  cassandra: { label: "CQL", available: true },
  neo4j: { label: "Neo4j Bolt", available: true },
  couchdb: { label: "CouchDB REST", available: true },
  meilisearch: { label: "Meilisearch REST", available: true },
  trino: { label: "Trino", available: true },
  cockroachdb: { label: "PostgreSQL", available: true },
  yugabyte: { label: "PostgreSQL", available: true },
  greenplum: { label: "PostgreSQL", available: true },
  questdb: { label: "PostgreSQL", available: true },
  materialize: { label: "PostgreSQL", available: true },
  risingwave: { label: "PostgreSQL", available: true },
  pgvector: { label: "PostgreSQL / pgx", available: true },
  qdrant: { label: "Qdrant REST", available: true },
  milvus: { label: "Milvus REST", available: true },
  weaviate: { label: "Weaviate REST / GraphQL", available: true },
  pinecone: { label: "Pinecone REST", available: true },
  chroma: { label: "Chroma HTTP", available: true },
  presto: { label: "Trino / Presto HTTP", available: true },
  selectdb: { label: "MySQL", available: true },
  databend: { label: "MySQL", available: true },
  cloudberry: { label: "PostgreSQL / pgx", available: true },
  redshift: { label: "PostgreSQL / pgx", available: true },
  databricks: { label: "Databricks SQL REST", available: true },
  bigquery: { label: "BigQuery REST", available: true },
  dremio: { label: "Dremio REST", available: true },
  kylin: { label: "Kylin REST", available: true },
  pinot: { label: "Pinot SQL HTTP", available: true },
  snowflake: { label: "Snowflake SQL API", available: true },
  plugin: { label: "Plugin", available: false },
};

export const databaseTypeLabels: Record<DbType, string> = {
  mysql: "MySQL",
  mariadb: "MariaDB",
  tidb: "TiDB",
  oceanbase: "OceanBase",
  polardb: "PolarDB",
  tdsql: "TDSQL",
  dolt: "Dolt",
  greatsql: "GreatSQL",
  gbase: "南大通用 GBase 8a",
  gbase8t: "南大通用 GBase 8t",
  gbase8c: "南大通用 GBase 8c",
  goldendb: "GoldenDB",
  starrocks: "StarRocks",
  doris: "Apache Doris",
  postgres: "PostgreSQL",
  kingbase: "KingbaseES",
  gaussdb: "GaussDB",
  opengauss: "openGauss",
  highgo: "HighGo",
  vastbase: "Vastbase",
  opentenbase: "OpenTenBase",
  sqlite: "SQLite",
  duckdb: "DuckDB",
  sqlserver: "SQL Server",
  oracle: "Oracle",
  dameng: "达梦 DM",
  yashandb: "崖山 YashanDB",
  firebird: "Firebird",
  clickhouse: "ClickHouse",
  tdengine: "TDengine",
  mongodb: "MongoDB",
  redis: "Redis",
  influxdb: "InfluxDB",
  elasticsearch: "Elasticsearch",
  opensearch: "OpenSearch",
  cassandra: "Cassandra",
  neo4j: "Neo4j",
  couchdb: "CouchDB",
  meilisearch: "Meilisearch",
  trino: "Trino",
  cockroachdb: "CockroachDB",
  yugabyte: "YugabyteDB",
  greenplum: "Greenplum",
  questdb: "QuestDB",
  materialize: "Materialize",
  risingwave: "RisingWave",
  pgvector: "PostgreSQL / pgvector",
  qdrant: "Qdrant",
  milvus: "Milvus",
  weaviate: "Weaviate",
  pinecone: "Pinecone",
  chroma: "Chroma",
  presto: "PrestoSQL",
  selectdb: "SelectDB",
  databend: "Databend",
  cloudberry: "Apache Cloudberry",
  redshift: "Amazon Redshift",
  databricks: "Databricks SQL",
  bigquery: "Google BigQuery",
  dremio: "Dremio",
  kylin: "Apache Kylin",
  pinot: "Apache Pinot",
  snowflake: "Snowflake SQL API",
  plugin: "Plugin",
};
export interface DatabaseIndexInfo {
  name: string;
  columns: string[];
  unique: boolean;
}

export interface DatabaseForeignKeyInfo {
  name: string;
  columns: string[];
  referencedTable: string;
  referencedColumns: string[];
}

export interface DatabaseTriggerInfo {
  name: string;
  timing: string;
  event: string;
}

export interface DatabasePrimaryKeyInfo {
  columns: string[];
  constraintName?: string;
}

export interface DatabaseTableDetails {
  ddl: string;
  columns: DatabaseColumnInfo[];
  primaryKeys: string[];
  primaryKeyName?: string;
  indexes: DatabaseIndexInfo[];
  foreignKeys: DatabaseForeignKeyInfo[];
  triggers: DatabaseTriggerInfo[];
  loadErrors?: string[];
}

export interface DatabaseDiagramTable {
  name: string;
  columns: DatabaseColumnInfo[];
  foreignKeys: DatabaseForeignKeyInfo[];
}

export interface DatabaseDiagram {
  database: string;
  tables: DatabaseDiagramTable[];
  skippedTables: number;
}

export interface OdbcDriverStatus {
  key: string;
  driverName: string;
  installed: boolean;
  dllPath?: string | null;
}

export interface DatabaseInstallEnvironment {
  platform: string;
  pkgManager?: string | null;
  pkgManagerPath?: string | null;
}

export interface DatabaseQueryHistoryItem {
  id: string;
  connectionId: string;
  database: string;
  query: string;
  startedAt: number;
  executionMs: number;
  rowCount: number;
  success: boolean;
  error?: string;
}

export interface DatabaseSavedScript {
  id: string;
  name: string;
  sql: string;
  updatedAt: number;
}
