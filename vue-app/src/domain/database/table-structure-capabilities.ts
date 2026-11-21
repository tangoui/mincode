// 表结构元数据（DDL / 索引 / 外键 / 触发器 / 结构变更 SQL）能力判断。
// 与后端 backend/db/schema.go 的实际分发能力保持一致：
// - db_get_table_ddl 支持 sqlite / duckdb / mysql / postgres / firebird 方言；
// - db_list_indexes 支持 sqlite / duckdb / postgres / mysql / sqlserver / oracle / dm / firebird，
//   以及只读的 MongoDB 集合索引；
// - db_list_foreign_keys 支持 sqlite / duckdb / postgres / mysql / firebird；
// - db_list_triggers 支持 sqlite / postgres / mysql / firebird（DuckDB 不支持触发器）；
// - 结构变更 SQL 通过 db_execute_query 执行，仅 SQL 系会话可用。
// 其余非 SQL 家族（Redis / Cassandra / Neo4j / InfluxDB / Elasticsearch /
// CouchDB 等）一律返回全 false，前端据此跳过查询并隐藏对应标签页，
// 避免把后端的“该会话类型不支持…”当作结构加载失败展示给用户。
import { supportsDatabaseIndexMutations } from "./index-change-sql";
import type { DbType } from "./types";
import {
  type DatabaseDriverDescriptor,
} from "./driver-descriptor";

export interface TableStructureCapabilities {
  ddl: boolean;
  indexes: boolean;
  createIndexes: boolean;
  editIndexes: boolean;
  deleteIndexes: boolean;
  foreignKeys: boolean;
  triggers: boolean;
  schemaChangeSql: boolean;
}

// MySQL 协议兼容家族（后端统一按 mysql 方言处理）。
const mysqlFamilyTypes = new Set<DbType>([
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
]);

// PostgreSQL 协议兼容家族（后端统一按 postgres 方言处理）。
const postgresFamilyTypes = new Set<DbType>([
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
]);

const ddlCapableTypes = new Set<DbType>([
  ...mysqlFamilyTypes,
  ...postgresFamilyTypes,
  "sqlite",
  "duckdb",
  "firebird",
]);
const indexCapableTypes = new Set<DbType>([...ddlCapableTypes, "sqlserver", "oracle", "yashandb", "dameng"]);
// 索引变更仅开放已验证并有明确方言 SQL 生成策略的数据库类型。
// 可通过 db_execute_query 执行 SQL 的全部会话类型（含未实现结构内省的 SQL 方言）。
const sqlExecutableTypes = new Set<DbType>([...indexCapableTypes, "clickhouse", "tdengine", "trino"]);

const UNSUPPORTED_CAPABILITIES: TableStructureCapabilities = {
  ddl: false,
  indexes: false,
  createIndexes: false,
  editIndexes: false,
  deleteIndexes: false,
  foreignKeys: false,
  triggers: false,
  schemaChangeSql: false,
};

// tableStructureCapabilities 返回指定数据库类型支持的表结构能力。
export function tableStructureCapabilities(
  dbType?: DbType,
  descriptor?: DatabaseDriverDescriptor,
): TableStructureCapabilities {
  if (descriptor?.available === false) return UNSUPPORTED_CAPABILITIES;
  if (dbType === "mongodb") return { ...UNSUPPORTED_CAPABILITIES, indexes: true };
  if (!dbType || !sqlExecutableTypes.has(dbType)) return UNSUPPORTED_CAPABILITIES;
  const mysqlLike = mysqlFamilyTypes.has(dbType);
  const relationalIntrospectable =
    mysqlLike ||
    postgresFamilyTypes.has(dbType) ||
    dbType === "sqlite" ||
    dbType === "duckdb" ||
    dbType === "firebird";
  const triggerIntrospectable =
    mysqlLike || postgresFamilyTypes.has(dbType) || dbType === "sqlite" || dbType === "firebird";
  return {
    ddl: ddlCapableTypes.has(dbType),
    indexes: indexCapableTypes.has(dbType),
    createIndexes: supportsDatabaseIndexMutations(dbType),
    editIndexes: supportsDatabaseIndexMutations(dbType),
    deleteIndexes: supportsDatabaseIndexMutations(dbType),
    foreignKeys: relationalIntrospectable,
    triggers: triggerIntrospectable,
    schemaChangeSql: true,
  };
}
