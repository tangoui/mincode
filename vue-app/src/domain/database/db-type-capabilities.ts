import type { DatabaseConnection, DbType } from "./types";
import { mysqlLikeTypes, postgresLikeTypes } from "./object-naming";
import { supportsDatabaseCharsetChange } from "./charset-change";
import { schemaSyncDialectForDbType } from "./schema-sync";
import { supportsTableSearch } from "./table-search";
import {
  builtinDriverDescriptor,
  descriptorSupports,
  isPluginDriverConnection,
  type DatabaseDriverDescriptor,
} from "./driver-descriptor";

// 插件驱动参与结构同步所需的元数据功能，与 backend/db schemaSyncPluginFeatures 一致。
const schemaSyncPluginFeatures = [
  "metadata.tables",
  "metadata.columns",
  "metadata.primaryKeys",
  "metadata.indexes",
] as const;

// 各工作区能力的 dbType 白名单，集中维护避免视图间复制。
export const schemaDdlSupportedTypes = new Set<DbType>([
  "mysql",
  "mariadb",
  "tidb",
  "oceanbase",
  "polardb",
  "tdsql",
  "dolt",
  "greatsql",
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
  "sqlite",
  "duckdb",
  "firebird",
]);

export const tableTemplateSupportedTypes = new Set<DbType>([
  ...schemaDdlSupportedTypes,
  "sqlserver",
  "pgvector",
]);

export const destructiveDatabaseClearTypes = new Set<DbType>([
  "mysql",
  "mariadb",
  "tidb",
  "oceanbase",
  "polardb",
  "tdsql",
  "dolt",
  "greatsql",
  "postgres",
  "kingbase",
  "gaussdb",
  "opengauss",
  "highgo",
  "vastbase",
  "opentenbase",
  "gbase8c",
  "sqlite",
  "duckdb",
]);

export const destructiveDatabaseDropTypes = new Set<DbType>([
  "mysql",
  "mariadb",
  "tidb",
  "oceanbase",
  "polardb",
  "tdsql",
  "dolt",
  "greatsql",
  "postgres",
  "kingbase",
  "gaussdb",
  "opengauss",
  "highgo",
  "vastbase",
  "opentenbase",
  "gbase8c",
  "duckdb",
]);

export const destructiveTableOperationTypes = new Set<DbType>([
  ...destructiveDatabaseDropTypes,
  "gbase",
  "gbase8t",
  "goldendb",
  "starrocks",
  "doris",
  "cockroachdb",
  "yugabyte",
  "greenplum",
  "sqlite",
  "sqlserver",
  "oracle",
  "yashandb",
  "dameng",
  "clickhouse",
  "firebird",
  "pgvector",
  "qdrant",
  "milvus",
  "weaviate",
  "pinecone",
  "chroma",
]);

export const rowInsertSupportedTypes = new Set<DbType>([
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
  "cloudberry",
  "redshift",
  "sqlite",
  "duckdb",
  "sqlserver",
  "oracle",
  "yashandb",
  "dameng",
  "firebird",
  "mongodb",
  "pgvector",
  "qdrant",
  "milvus",
  "weaviate",
  "pinecone",
  "chroma",
]);

export const rowEditSupportedTypes = new Set<DbType>([...rowInsertSupportedTypes, "cassandra"]);

export const tableDataTransferSupportedTypes = new Set<DbType>([
  ...mysqlLikeTypes,
  ...postgresLikeTypes,
  "gbase",
  "gbase8t",
  "goldendb",
  "starrocks",
  "doris",
  "sqlite",
  "duckdb",
  "sqlserver",
  "oracle",
  "yashandb",
  "dameng",
  "firebird",
]);

type CapabilityConnection = Pick<DatabaseConnection, "dbType" | "driver">;

// 插件驱动连接必须带描述符判断；缺失时失败关闭，绝不按 dbType 套用内置白名单（E-15）。
export function supportsSchemaDdl(
  connection: CapabilityConnection,
  descriptor?: DatabaseDriverDescriptor,
) {
  return databaseConnectionCapabilities(connection, descriptor).schemaDdl;
}

export function supportsTableDataTransfer(
  connection: CapabilityConnection,
  descriptor?: DatabaseDriverDescriptor,
) {
  const capabilities = databaseConnectionCapabilities(connection, descriptor);
  return capabilities.tableDataExport || capabilities.tableDataImport;
}

// Types whose object list (with row counts, sizes and comments) loads per database through
// db_get_database_tables. MySQL-protocol types must be listed: their navigation schema holds
// database names only. Mirrors the session routing in backend/db/build_profile_full.go.
export const perDatabaseTableLoadTypes = new Set<DbType>([
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
  ...postgresLikeTypes,
  "sqlserver",
  "oracle",
  "yashandb",
  "dameng",
  "clickhouse",
  "duckdb",
  "sqlite",
  "mongodb",
]);

export function loadsTablesPerDatabase(
  connection: CapabilityConnection,
  descriptor?: DatabaseDriverDescriptor,
) {
  return databaseConnectionCapabilities(connection, descriptor)
    .loadsTablesPerDatabase;
}

/** 连接级能力：内置驱动沿用各白名单，插件驱动由描述符 features + 方言决定。 */
export interface DatabaseConnectionCapabilities {
  /** 传给方言相关辅助函数（引号、模板、编辑器）的类型。 */
  dialectType: DbType;
  pluginDriver: boolean;
  available: boolean;
  schemaDdl: boolean;
  tableTemplate: boolean;
  clearDatabase: boolean;
  dropDatabase: boolean;
  dropTable: boolean;
  clearTable: boolean;
  tableRows: boolean;
  rowInsert: boolean;
  rowEdit: boolean;
  rowDelete: boolean;
  tableDataExport: boolean;
  tableDataImport: boolean;
  loadsTablesPerDatabase: boolean;
  charsetChange: boolean;
  schemaSync: boolean;
  tableSearch: boolean;
  alwaysStream: boolean;
}

export function databaseConnectionCapabilities(
  connection: CapabilityConnection,
  descriptor?: DatabaseDriverDescriptor,
): DatabaseConnectionCapabilities {
  if (!isPluginDriverConnection(connection)) {
    const dbType = connection.dbType;
    const builtin = descriptor?.source === "builtin"
      ? descriptor
      : builtinDriverDescriptor(dbType);
    return {
      dialectType: dbType,
      pluginDriver: false,
      available: builtin.available,
      schemaDdl: schemaDdlSupportedTypes.has(dbType),
      tableTemplate: tableTemplateSupportedTypes.has(dbType),
      clearDatabase: destructiveDatabaseClearTypes.has(dbType),
      dropDatabase: destructiveDatabaseDropTypes.has(dbType),
      dropTable: destructiveTableOperationTypes.has(dbType),
      clearTable: destructiveTableOperationTypes.has(dbType),
      tableRows: true,
      rowInsert: rowInsertSupportedTypes.has(dbType),
      rowEdit: rowEditSupportedTypes.has(dbType),
      rowDelete: rowEditSupportedTypes.has(dbType),
      tableDataExport: tableDataTransferSupportedTypes.has(dbType),
      tableDataImport: tableDataTransferSupportedTypes.has(dbType),
      loadsTablesPerDatabase: perDatabaseTableLoadTypes.has(dbType),
      charsetChange: supportsDatabaseCharsetChange(dbType),
      schemaSync: dbType !== "redis",
      tableSearch: supportsTableSearch(dbType),
      alwaysStream: false,
    };
  }
  const plugin = descriptor?.source === "plugin" ? descriptor : undefined;
  if (!plugin) {
    return pluginCapabilitiesFromFeatures(connection.dbType, false, () => false);
  }
  return pluginCapabilitiesFromFeatures(
    plugin.dialectType,
    plugin.available,
    (feature) => descriptorSupports(plugin, feature),
  );
}

function pluginCapabilitiesFromFeatures(
  dialectType: DbType,
  available: boolean,
  has: (feature: Parameters<typeof descriptorSupports>[1]) => boolean,
): DatabaseConnectionCapabilities {
  const tableRows = has("tableRows");
  return {
    dialectType,
    pluginDriver: true,
    available,
    schemaDdl: has("metadata.ddl"),
    // 建表模板通过 db_execute_query 执行，只依赖方言。
    tableTemplate: available && tableTemplateSupportedTypes.has(dialectType),
    // 库级破坏性操作由驱动声明 destructive.* 决定；字符集修改仍不支持插件会话。
    clearDatabase: has("destructive.clearDatabase"),
    dropDatabase: has("destructive.dropDatabase"),
    dropTable: has("destructive.dropTable"),
    clearTable: has("destructive.clearTable"),
    tableRows,
    rowInsert: has("rows.insert"),
    rowEdit: has("rows.update"),
    rowDelete: has("rows.delete"),
    tableDataExport: tableRows,
    tableDataImport: has("rows.insert"),
    loadsTablesPerDatabase: has("metadata.tables"),
    charsetChange: false,
    // 结构同步需要方言可生成 DDL，且驱动能读取表、字段、主键和索引。
    schemaSync:
      schemaSyncDialectForDbType(dialectType) !== undefined &&
      schemaSyncPluginFeatures.every((feature) => has(feature)),
    tableSearch: tableRows && supportsTableSearch(dialectType),
    alwaysStream: true,
  };
}
