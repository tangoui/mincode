import type { DatabaseConnection, DbType } from "./types";
import { mysqlLikeTypes, postgresLikeTypes } from "./object-naming";
import { supportsDatabaseCharsetChange } from "./charset-change";
import { supportsTableSearch } from "./table-search";
import { resolveConnectionDriver, type DatabaseDriverDescriptor } from "./driver-descriptor";


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

export interface DatabaseConnectionCapabilities {
  /** 传给方言相关辅助函数（引号、模板、编辑器）的类型。 */
  dialectType: DbType;
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
}

export function databaseConnectionCapabilities(
  connection: CapabilityConnection,
  descriptor?: DatabaseDriverDescriptor,
): DatabaseConnectionCapabilities {
  const dbType = connection.dbType;
  const available = (descriptor ?? resolveConnectionDriver(connection)).available;
  return {
    dialectType: dbType,
    available,
    schemaDdl: available && schemaDdlSupportedTypes.has(dbType),
    tableTemplate: available && tableTemplateSupportedTypes.has(dbType),
    clearDatabase: available && destructiveDatabaseClearTypes.has(dbType),
    dropDatabase: available && destructiveDatabaseDropTypes.has(dbType),
    dropTable: available && destructiveTableOperationTypes.has(dbType),
    clearTable: available && destructiveTableOperationTypes.has(dbType),
    tableRows: available,
    rowInsert: available && rowInsertSupportedTypes.has(dbType),
    rowEdit: available && rowEditSupportedTypes.has(dbType),
    rowDelete: available && rowEditSupportedTypes.has(dbType),
    tableDataExport: available && tableDataTransferSupportedTypes.has(dbType),
    tableDataImport: available && tableDataTransferSupportedTypes.has(dbType),
    loadsTablesPerDatabase: available && perDatabaseTableLoadTypes.has(dbType),
    charsetChange: available && supportsDatabaseCharsetChange(dbType),
    schemaSync: available && dbType !== "redis",
    tableSearch: available && supportsTableSearch(dbType),
  };
}
