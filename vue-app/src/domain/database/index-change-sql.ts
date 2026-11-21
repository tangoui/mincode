import type { DatabaseIndexInfo, DbType } from "./types";

export type DatabaseIndexSqlDialect =
  | "mysql"
  | "postgres"
  | "sqlite"
  | "duckdb"
  | "sqlserver"
  | "oracle"
  | "dm"
  | "firebird";

export interface DatabaseIndexSqlTarget {
  dbType: DbType;
  database: string;
  table: string;
}

const DIALECT_BY_TYPE: Partial<Record<DbType, DatabaseIndexSqlDialect>> = {
  mysql: "mysql",
  mariadb: "mysql",
  tidb: "mysql",
  oceanbase: "mysql",
  polardb: "mysql",
  tdsql: "mysql",
  dolt: "mysql",
  greatsql: "mysql",
  postgres: "postgres",
  kingbase: "postgres",
  gaussdb: "postgres",
  opengauss: "postgres",
  highgo: "postgres",
  vastbase: "postgres",
  opentenbase: "postgres",
  gbase8c: "postgres",
  sqlite: "sqlite",
  duckdb: "duckdb",
  sqlserver: "sqlserver",
  oracle: "oracle",
  yashandb: "oracle",
  dameng: "dm",
  firebird: "firebird",
};

export function databaseIndexSqlDialect(dbType?: DbType): DatabaseIndexSqlDialect | undefined {
  return dbType ? DIALECT_BY_TYPE[dbType] : undefined;
}

export function supportsDatabaseIndexMutations(dbType?: DbType): boolean {
  return databaseIndexSqlDialect(dbType) !== undefined;
}

function quoteIdentifier(dialect: DatabaseIndexSqlDialect, value: string) {
  if (dialect === "mysql") return `\`${value.replace(/`/g, "``")}\``;
  if (dialect === "sqlserver") return `[${value.replace(/]/g, "]]")}]`;
  return `"${value.replace(/"/g, '""')}"`;
}

function qualifiedTable(dialect: DatabaseIndexSqlDialect, database: string, table: string) {
  const quotedTable = quoteIdentifier(dialect, table);
  if (dialect === "sqlite" || dialect === "firebird" || !database.trim()) return quotedTable;
  return `${quoteIdentifier(dialect, database)}.${quotedTable}`;
}

function qualifiedIndex(
  dialect: DatabaseIndexSqlDialect,
  database: string,
  index: string,
  purpose: "create" | "drop"
) {
  const quotedIndex = quoteIdentifier(dialect, index);
  if (!database.trim()) return quotedIndex;
  if (dialect === "oracle" || dialect === "dm") {
    return `${quoteIdentifier(dialect, database)}.${quotedIndex}`;
  }
  if (purpose === "drop" && (dialect === "postgres" || dialect === "duckdb")) {
    return `${quoteIdentifier(dialect, database)}.${quotedIndex}`;
  }
  return quotedIndex;
}

function createIndexStatement(
  dialect: DatabaseIndexSqlDialect,
  target: DatabaseIndexSqlTarget,
  index: DatabaseIndexInfo
) {
  const unique = index.unique ? "UNIQUE " : "";
  const columns = index.columns.map((column) => quoteIdentifier(dialect, column)).join(", ");
  return `CREATE ${unique}INDEX ${qualifiedIndex(dialect, target.database, index.name, "create")} ON ${qualifiedTable(dialect, target.database, target.table)} (${columns})`;
}

function dropIndexStatement(
  dialect: DatabaseIndexSqlDialect,
  target: DatabaseIndexSqlTarget,
  indexName: string
) {
  const index = qualifiedIndex(dialect, target.database, indexName, "drop");
  if (dialect === "mysql" || dialect === "sqlserver") {
    return `DROP INDEX ${index} ON ${qualifiedTable(dialect, target.database, target.table)}`;
  }
  return `DROP INDEX ${index}`;
}

function requireDialect(target: DatabaseIndexSqlTarget) {
  const dialect = databaseIndexSqlDialect(target.dbType);
  if (!dialect) throw new Error(`Unsupported index mutation database type: ${target.dbType}`);
  return dialect;
}

export function buildCreateIndexStatements(
  target: DatabaseIndexSqlTarget,
  index: DatabaseIndexInfo
): string[] {
  const dialect = requireDialect(target);
  return [createIndexStatement(dialect, target, index)];
}

export function buildDropIndexStatements(
  target: DatabaseIndexSqlTarget,
  indexName: string
): string[] {
  const dialect = requireDialect(target);
  return [dropIndexStatement(dialect, target, indexName)];
}

export function buildEditIndexStatements(
  target: DatabaseIndexSqlTarget,
  originalName: string,
  index: DatabaseIndexInfo
): string[] {
  const dialect = requireDialect(target);
  const table = qualifiedTable(dialect, target.database, target.table);
  const columns = index.columns.map((column) => quoteIdentifier(dialect, column)).join(", ");
  const unique = index.unique ? "UNIQUE " : "";

  // MySQL-family databases can replace the index in one ALTER TABLE statement,
  // avoiding a driver-dependent multi-statement query and reducing lock churn.
  if (dialect === "mysql") {
    return [
      `ALTER TABLE ${table} DROP INDEX ${quoteIdentifier(dialect, originalName)}, ADD ${unique}INDEX ${quoteIdentifier(dialect, index.name)} (${columns})`,
    ];
  }

  // SQL Server can replace an index definition in one statement when its name
  // remains unchanged. A rename still requires an explicit drop/create pair.
  if (dialect === "sqlserver" && originalName === index.name) {
    return [
      `CREATE ${unique}INDEX ${quoteIdentifier(dialect, index.name)} ON ${table} (${columns}) WITH (DROP_EXISTING = ON)`,
    ];
  }

  return [
    dropIndexStatement(dialect, target, originalName),
    createIndexStatement(dialect, target, index),
  ];
}

export function formatIndexChangeSql(statements: string[]): string {
  return statements.map((statement) => `${statement.replace(/;\s*$/, "")};`).join("\n");
}
