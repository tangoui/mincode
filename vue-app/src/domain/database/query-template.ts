import type { DbType } from "@/domain/database/types";
import { isVectorDatabaseType } from "@/domain/database/vector-database";

export type DatabaseQueryEditorLanguage = "sql" | "json" | "plaintext";

export interface DatabaseQueryTemplateOptions {
  dbType: DbType;
  database: string;
  table: string;
  limit?: number;
}

export const MYSQL_LIKE_TYPES = new Set<DbType>([
  "mysql",
  "mariadb",
  "tidb",
  "oceanbase",
  "polardb",
  "tdsql",
  "dolt",
  "greatsql",
  "clickhouse",
  "tdengine",
  "starrocks",
  "doris",
  "selectdb",
  "databend",
]);

const JSON_QUERY_TYPES = new Set<DbType>([
  "mongodb",
  "elasticsearch",
  "opensearch",
  "couchdb",
  "meilisearch",
]);

function positiveLimit(value: number | undefined) {
  return Math.max(1, Math.min(Math.floor(value ?? 100), 10_000));
}

function quoteDoubleIdentifier(value: string) {
  return `"${value.replace(/"/g, '""')}"`;
}

function quoteBacktickIdentifier(value: string) {
  return `\`${value.replace(/`/g, "``")}\``;
}

function quoteSquareIdentifier(value: string) {
  return "[" + value.replace(/\]/g, "]]") + "]";
}

function quoteCypherIdentifier(value: string) {
  return `\`${value.replace(/`/g, "``")}\``;
}

function jsonString(value: string) {
  return JSON.stringify(value);
}

function qualifiedSqlTable(dbType: DbType, database: string, table: string) {
  const needsDatabase =
    database.trim() && dbType !== "sqlite" && dbType !== "firebird";
  if (dbType === "sqlserver") {
    const schema = database.trim() || "dbo";
    return `${quoteSquareIdentifier(schema)}.${quoteSquareIdentifier(table)}`;
  }
  if (MYSQL_LIKE_TYPES.has(dbType)) {
    const tableRef = quoteBacktickIdentifier(table);
    return needsDatabase
      ? `${quoteBacktickIdentifier(database)}.${tableRef}`
      : tableRef;
  }
  const tableRef = quoteDoubleIdentifier(table);
  return needsDatabase
    ? `${quoteDoubleIdentifier(database)}.${tableRef}`
    : tableRef;
}

/**
 * Returns the editor syntax for a connection's executable query protocol.
 * MongoDB and the HTTP document stores receive JSON, while InfluxDB receives Flux.
 */
export function databaseQueryEditorLanguage(
  dbType: DbType,
): DatabaseQueryEditorLanguage {
  if (isVectorDatabaseType(dbType)) return "json";
  if (JSON_QUERY_TYPES.has(dbType)) return "json";
  if (dbType === "influxdb" || dbType === "redis") return "plaintext";
  return "sql";
}

/**
 * Builds a safe read-only starter query for the currently selected object.
 * The result matches the Go backend contract for non-SQL connections rather
 * than sending SQL to a JSON/Flux/Cypher/CQL endpoint.
 */
export function buildDatabaseQueryTemplate({
  dbType,
  database,
  table,
  limit: requestedLimit,
}: DatabaseQueryTemplateOptions): string {
  const safeDatabase = database.trim();
  const safeTable = table.trim();
  if (!safeTable) return "";
  const limit = positiveLimit(requestedLimit);

  if (isVectorDatabaseType(dbType)) {
    const template: Record<string, unknown> = {
      collection: safeTable,
      vector: [0.1, 0.2, 0.3],
      limit,
    };
    if (dbType === "pgvector") {
      template.vectorColumn = "";
      template.metric = "cosine";
    } else if (dbType === "milvus") {
      template.filter = "";
      template.outputFields = ["*"];
    } else {
      template.filter = {};
    }
    if (dbType === "pinecone") template.namespace = "";
    if (dbType === "weaviate") template.targetVector = "";
    template.includeVector = false;
    return JSON.stringify(template, null, 2);
  }

  switch (dbType) {
    case "mongodb":
      return JSON.stringify(
        {
          db: safeDatabase,
          collection: safeTable,
          filter: {},
          limit,
          skip: 0,
        },
        null,
        2,
      );
    case "elasticsearch":
    case "opensearch":
      return JSON.stringify(
        {
          index: safeTable,
          query: { match_all: {} },
          size: limit,
        },
        null,
        2,
      );
    case "couchdb":
      // CouchDB exposes each database as an object in the navigator's table slot.
      return JSON.stringify(
        {
          db: safeTable,
          selector: {},
          limit,
        },
        null,
        2,
      );
    case "meilisearch":
      // 空 q 为占位搜索，返回索引文档；filter/sort 等参数可按需补充。
      return JSON.stringify(
        {
          index: safeTable,
          q: "",
          limit,
        },
        null,
        2,
      );
    case "influxdb":
      return [
        `from(bucket: ${jsonString(safeDatabase)})`,
        "  |> range(start: -1h)",
        `  |> filter(fn: (r) => r._measurement == ${jsonString(safeTable)})`,
        `  |> limit(n: ${limit})`,
      ].join("\n");
    case "neo4j":
      return `MATCH (n:${quoteCypherIdentifier(safeTable)})\nRETURN n\nLIMIT ${limit};`;
    case "cassandra": {
      const keyspace = safeDatabase
        ? `${quoteDoubleIdentifier(safeDatabase)}.`
        : "";
      return `SELECT * FROM ${keyspace}${quoteDoubleIdentifier(safeTable)} LIMIT ${limit};`;
    }
    case "sqlserver":
      return `SELECT TOP (${limit}) *\nFROM ${qualifiedSqlTable(dbType, safeDatabase, safeTable)};`;
    case "oracle":
      return `SELECT *\nFROM ${qualifiedSqlTable(dbType, safeDatabase, safeTable)}\nFETCH FIRST ${limit} ROWS ONLY;`;
    case "firebird":
      return `SELECT *\nFROM ${qualifiedSqlTable(dbType, safeDatabase, safeTable)}\nROWS 1 TO ${limit};`;
    case "redis":
      return "";
    default:
      return `SELECT *\nFROM ${qualifiedSqlTable(dbType, safeDatabase, safeTable)}\nLIMIT ${limit};`;
  }
}

/**
 * 为没有编辑器 SQL 的表数据视图构造执行计划的来源查询。
 * 仅 SQL 系引擎可用；JSON/Flux/Cypher/CQL 等非 SQL 协议返回空串。
 */
export function buildTableExplainSourceSql({
  dbType,
  database,
  table,
  limit: requestedLimit,
}: DatabaseQueryTemplateOptions): string {
  const safeTable = table.trim();
  if (!safeTable) return "";
  if (
    isVectorDatabaseType(dbType) ||
    JSON_QUERY_TYPES.has(dbType) ||
    dbType === "influxdb" ||
    dbType === "redis" ||
    dbType === "neo4j" ||
    dbType === "cassandra"
  ) {
    return "";
  }

  const limit = positiveLimit(requestedLimit);
  if (dbType === "sqlserver") {
    return `SELECT TOP (${limit}) * FROM ${qualifiedSqlTable(dbType, database, safeTable)}`;
  }
  if (dbType === "oracle") {
    return `SELECT * FROM ${qualifiedSqlTable(dbType, database, safeTable)} FETCH FIRST ${limit} ROWS ONLY`;
  }
  if (dbType === "firebird") {
    return `SELECT * FROM ${qualifiedSqlTable(dbType, database, safeTable)} ROWS 1 TO ${limit}`;
  }
  return `SELECT * FROM ${qualifiedSqlTable(dbType, database, safeTable)} LIMIT ${limit}`;
}
