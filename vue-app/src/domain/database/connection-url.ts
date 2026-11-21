import { databaseDefaultPorts, type DbType } from "./types.ts";

export interface ParsedDatabaseConnectionUrl {
  dbType: DbType;
  host: string;
  port: number;
  username: string;
  password: string;
  database: string;
  token: string;
  org: string;
  authSource: string;
  ssl: boolean;
}

const protocolTypes: Record<string, DbType> = {
  mysql: "mysql",
  mariadb: "mariadb",
  tidb: "tidb",
  oceanbase: "oceanbase",
  polardb: "polardb",
  tdsql: "tdsql",
  dolt: "dolt",
  greatsql: "greatsql",
  gbase: "gbase",
  gbase8t: "gbase8t",
  gbase8c: "gbase8c",
  goldendb: "goldendb",
  starrocks: "starrocks",
  doris: "doris",
  postgres: "postgres",
  postgresql: "postgres",
  kingbase: "kingbase",
  gaussdb: "gaussdb",
  opengauss: "opengauss",
  highgo: "highgo",
  vastbase: "vastbase",
  opentenbase: "opentenbase",
  sqlserver: "sqlserver",
  mssql: "sqlserver",
  oracle: "oracle",
  dameng: "dameng",
  dm: "dameng",
  yashandb: "yashandb",
  firebird: "firebird",
  clickhouse: "clickhouse",
  tdengine: "tdengine",
  taos: "tdengine",
  mongodb: "mongodb",
  "mongodb+srv": "mongodb",
  redis: "redis",
  rediss: "redis",
  influxdb: "influxdb",
  influx: "influxdb",
  elasticsearch: "elasticsearch",
  es: "elasticsearch",
  opensearch: "opensearch",
  cassandra: "cassandra",
  neo4j: "neo4j",
  bolt: "neo4j",
  "neo4j+s": "neo4j",
  "neo4j+ssc": "neo4j",
  couchdb: "couchdb",
  couch: "couchdb",
  meilisearch: "meilisearch",
  meili: "meilisearch",
  trino: "trino",
  cockroachdb: "cockroachdb",
  yugabyte: "yugabyte",
  greenplum: "greenplum",
  questdb: "questdb",
  materialize: "materialize",
  risingwave: "risingwave",
  pgvector: "pgvector",
  qdrant: "qdrant",
  milvus: "milvus",
  weaviate: "weaviate",
  pinecone: "pinecone",
  chroma: "chroma",
  presto: "presto",
  selectdb: "selectdb",
  databend: "databend",
  cloudberry: "cloudberry",
  redshift: "redshift",
  databricks: "databricks",
  bigquery: "bigquery",
  dremio: "dremio",
  kylin: "kylin",
  pinot: "pinot",
  snowflake: "snowflake",
};

const compatibleProtocolFamilies: DbType[][] = [
  [
    "mysql",
    "mariadb",
    "tidb",
    "oceanbase",
    "polardb",
    "tdsql",
    "dolt",
    "greatsql",
    "gbase",
    "gbase8t",
    "goldendb",
    "starrocks",
    "doris",
    "selectdb",
    "databend",
  ],
  [
    "postgres",
    "kingbase",
    "gaussdb",
    "opengauss",
    "highgo",
    "vastbase",
    "opentenbase",
    "gbase8c",
    "cockroachdb",
    "yugabyte",
    "greenplum",
    "questdb",
    "materialize",
    "risingwave",
    "pgvector",
    "cloudberry",
    "redshift",
  ],
  ["elasticsearch", "opensearch"],
];

const httpDatabaseTypes = new Set<DbType>([
  "influxdb",
  "elasticsearch",
  "opensearch",
  "couchdb",
  "meilisearch",
  "qdrant",
  "milvus",
  "weaviate",
  "pinecone",
  "chroma",
  "databricks",
  "bigquery",
  "dremio",
  "kylin",
  "pinot",
  "snowflake",
]);

/**
 * Parses a quick-connect URL. HTTP(S) URLs are ambiguous, so HTTP-based data
 * sources use the database type currently selected in the connection dialog.
 */
export function parseDatabaseConnectionUrl(
  value: string,
  selectedDbType?: DbType,
): ParsedDatabaseConnectionUrl | undefined {
  const source = value.trim();
  if (!source) return undefined;

  try {
    const parsed = new URL(source);
    const protocol = parsed.protocol.replace(/:$/, "").toLowerCase();
    const dbType = resolveDatabaseType(protocol, selectedDbType);
    if (!dbType || !parsed.hostname) return undefined;

    const parsedPort = parsed.port
      ? Number(parsed.port)
      : databaseDefaultPorts[dbType];
    if (!Number.isInteger(parsedPort) || parsedPort < 1 || parsedPort > 65535)
      return undefined;

    const username = decodeURIComponent(parsed.username);
    const password = decodeURIComponent(parsed.password);
    const token = readToken(dbType, parsed.searchParams, username, password);

    return {
      dbType,
      host: parsed.hostname,
      port: parsedPort,
      username: dbType === "influxdb" && token ? "" : username,
      password,
      database: parseDatabasePath(dbType, parsed.pathname),
      token,
      org: parseOrganization(dbType, parsed.searchParams),
      authSource: parseAuthSource(dbType, parsed.searchParams),
      ssl: inferConnectionSsl(dbType, protocol, parsed.searchParams),
    };
  } catch {
    return undefined;
  }
}

function resolveDatabaseType(
  protocol: string,
  selectedDbType?: DbType,
): DbType | undefined {
  if (
    (protocol === "http" || protocol === "https") &&
    selectedDbType &&
    httpDatabaseTypes.has(selectedDbType)
  ) {
    return selectedDbType;
  }

  const parsedDbType = protocolTypes[protocol];
  if (!parsedDbType) return undefined;
  if (!selectedDbType || parsedDbType === selectedDbType) return parsedDbType;

  const family = compatibleProtocolFamilies.find((types) =>
    types.includes(parsedDbType),
  );
  return family?.includes(selectedDbType) ? selectedDbType : parsedDbType;
}


function parseOrganization(dbType: DbType, params: URLSearchParams): string {
  if (dbType === "influxdb" || dbType === "chroma") {
    return params.get("org") ?? params.get("tenant") ?? "";
  }
  if (dbType === "databricks") return params.get("catalog") ?? "";
  if (dbType === "bigquery") return params.get("location") ?? "";
  if (dbType === "snowflake") return params.get("warehouse") ?? "";
  return "";
}

function parseAuthSource(dbType: DbType, params: URLSearchParams): string {
  if (dbType === "mongodb") return params.get("authSource") ?? "admin";
  if (dbType === "databricks" || dbType === "snowflake") {
    return params.get("schema") ?? "";
  }
  return "";
}

function readToken(
  dbType: DbType,
  params: URLSearchParams,
  username: string,
  password: string,
): string {
  const queryToken =
    params.get("apiKey") ?? params.get("apikey") ?? params.get("token") ?? "";
  if (queryToken) return queryToken;

  // InfluxDB commonly uses http://<token>@host:8086/bucket?org=<org>.
  if (dbType === "influxdb" && username && !password) return username;
  return "";
}

function parseDatabasePath(dbType: DbType, pathname: string): string {
  const decoded = decodeURIComponent(pathname);
  if (dbType === "firebird") {
    const withoutLeadingSlash = decoded.replace(/^\/+/, "");
    return withoutLeadingSlash.includes("/")
      ? `/${withoutLeadingSlash}`
      : withoutLeadingSlash;
  }
  return decoded.replace(/^\/+/, "").split("/")[0] ?? "";
}

function inferConnectionSsl(
  dbType: DbType,
  protocol: string,
  params: URLSearchParams,
): boolean {
  const tls = params.get("tls")?.toLowerCase();
  const ssl = params.get("ssl")?.toLowerCase();
  const sslMode = params.get("sslmode")?.toLowerCase();
  const wireCrypt = params.get("wire_crypt")?.toLowerCase();

  if (dbType === "firebird") {
    return (
      wireCrypt === undefined ||
      !["false", "0", "disable", "off"].includes(wireCrypt)
    );
  }
  return (
    protocol === "https" ||
    protocol === "rediss" ||
    protocol === "mongodb+srv" ||
    protocol === "neo4j+s" ||
    protocol === "neo4j+ssc" ||
    ssl === "true" ||
    sslMode === "require" ||
    tls === "true"
  );
}
