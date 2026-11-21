import { MYSQL_LIKE_TYPES } from "@/domain/database/query-template";
import type { DbType } from "@/domain/database/types";
import { isVectorDatabaseType } from "@/domain/database/vector-database";

/**
 * AI 自然语言生成查询的方言契约：为每种可执行查询协议给出
 * 与后端执行合同一致的输出格式说明，供一次性生成请求拼装提示词。
 */
export type DatabaseAiQueryFamily =
  | "sql-mysql"
  | "sql-standard"
  | "sql-sqlserver"
  | "sql-oracle"
  | "sql-firebird"
  | "mongodb"
  | "elasticsearch"
  | "couchdb"
  | "meilisearch"
  | "influxdb"
  | "neo4j"
  | "cassandra"
  | "vector"
  | "redis";

export interface DatabaseAiQueryGuide {
  family: DatabaseAiQueryFamily;
  /** 输出格式契约（写入 system prompt），只描述查询本身，不含上下文。 */
  instruction: string;
}

const SQL_ONLY = "只输出查询本身，不要解释、引号或 Markdown 代码块。";

function vectorInstruction(dbType: DbType): string {
  const extras: string[] = [];
  if (dbType === "pgvector") extras.push('"vectorColumn": "", "metric": "cosine"');
  else if (dbType === "milvus") extras.push('"filter": "", "outputFields": ["*"]');
  else if (dbType === "pinecone") extras.push('"namespace": ""');
  else if (dbType === "weaviate") extras.push('"targetVector": ""');
  else extras.push('"filter": {}');
  const extraText = extras.length ? `，并按集合需求包含 ${extras.join("、")}` : "";
  return `生成一个严格 JSON 对象的向量检索请求，形如 {"collection": "<集合名>", "vector": [数值数组], "limit": 100, "includeVector": false}${extraText}。${SQL_ONLY}`;
}

/**
 * 返回指定库类型的查询生成契约；与 buildDatabaseQueryTemplate 的可执行协议保持一致。
 */
export function databaseAiQueryGuide(dbType: DbType): DatabaseAiQueryGuide {
  if (isVectorDatabaseType(dbType)) {
    return { family: "vector", instruction: vectorInstruction(dbType) };
  }
  switch (dbType) {
    case "mongodb":
      return {
        family: "mongodb",
        instruction: `生成一个严格 JSON 对象的 MongoDB 查询，形如 {"db": "<数据库>", "collection": "<集合>", "filter": {<查询条件>}, "limit": 100, "skip": 0}。${SQL_ONLY}`,
      };
    case "elasticsearch":
    case "opensearch":
      return {
        family: "elasticsearch",
        instruction: `生成一个严格 JSON 对象的 ${dbType === "opensearch" ? "OpenSearch" : "Elasticsearch"} 查询，形如 {"index": "<索引>", "query": {<Query DSL>}, "size": 100}。${SQL_ONLY}`,
      };
    case "couchdb":
      return {
        family: "couchdb",
        instruction: `生成一个严格 JSON 对象的 CouchDB Mango 查询，形如 {"db": "<数据库>", "selector": {<选择器>}, "limit": 100}。${SQL_ONLY}`,
      };
    case "meilisearch":
      return {
        family: "meilisearch",
        instruction: `生成一个严格 JSON 对象的 Meilisearch 搜索请求，形如 {"index": "<索引>", "q": "<搜索词>", "filter": <可选过滤表达式>, "limit": 100}。${SQL_ONLY}`,
      };
    case "influxdb":
      return {
        family: "influxdb",
        instruction: `生成一段 Flux 脚本，形如 from(bucket: "<桶>") |> range(start: -1h) |> filter(...) |> limit(n: 100)。${SQL_ONLY}`,
      };
    case "neo4j":
      return {
        family: "neo4j",
        instruction: `生成一条 Cypher 查询，使用 MATCH/RETURN 并带 LIMIT 100，标签与属性名使用反引号。${SQL_ONLY}`,
      };
    case "cassandra":
      return {
        family: "cassandra",
        instruction: `生成一条 CQL SELECT 查询并带 LIMIT 100，标识符使用双引号。${SQL_ONLY}`,
      };
    case "redis":
      return {
        family: "redis",
        instruction: `生成单行 Redis 命令（如 SCAN、GET、HGETALL、ZRANGE），每行一条。${SQL_ONLY}`,
      };
    case "sqlserver":
      return {
        family: "sql-sqlserver",
        instruction: `生成一条 T-SQL SELECT 查询，标识符使用方括号，用 TOP (100) 限制行数。${SQL_ONLY}`,
      };
    case "oracle":
      return {
        family: "sql-oracle",
        instruction: `生成一条 Oracle SELECT 查询，标识符使用双引号，用 FETCH FIRST 100 ROWS ONLY 限制行数。${SQL_ONLY}`,
      };
    case "firebird":
      return {
        family: "sql-firebird",
        instruction: `生成一条 Firebird SELECT 查询，标识符使用双引号，用 ROWS 1 TO 100 限制行数。${SQL_ONLY}`,
      };
    default:
      if (MYSQL_LIKE_TYPES.has(dbType)) {
        return {
          family: "sql-mysql",
          instruction: `生成一条 MySQL 方言的 SELECT 查询，标识符使用反引号，默认带 LIMIT 100。${SQL_ONLY}`,
        };
      }
      return {
        family: "sql-standard",
        instruction: `生成一条标准 SQL SELECT 查询，标识符使用双引号，默认带 LIMIT 100。${SQL_ONLY}`,
      };
  }
}

/**
 * 清洗 AI 返回的查询文本：去除首尾空白与 Markdown 代码围栏。
 */
export function cleanDatabaseAiQueryOutput(value: string): string {
  const trimmed = value.trim();
  if (!trimmed.startsWith("```")) return trimmed;
  return trimmed
    .replace(/^```[^\n]*\n?/, "")
    .replace(/\n?```$/, "")
    .trim();
}
