import type { DbType } from "./types";

export interface InsertRowSqlInput {
  dbType: DbType;
  database?: string;
  table: string;
  columns: string[];
  columnTypes?: string[];
  row: unknown[];
}

/** batch：多行合并为一条 INSERT；rows：每行一条独立 INSERT。 */
export type InsertSqlMode = "batch" | "rows";

export interface InsertRowsSqlInput {
  dbType: DbType;
  database?: string;
  table: string;
  columns: string[];
  columnTypes?: string[];
  rows: unknown[][];
  mode?: InsertSqlMode;
}

const backtickIdentifierTypes = new Set<DbType>([
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
  "clickhouse",
  "tdengine",
]);

const sqlInsertTypes = new Set<DbType>([
  ...backtickIdentifierTypes,
  "gbase8c",
  "postgres",
  "kingbase",
  "gaussdb",
  "opengauss",
  "highgo",
  "vastbase",
  "opentenbase",
  "sqlite",
  "duckdb",
  "sqlserver",
  "oracle",
  "dameng",
  "yashandb",
  "firebird",
  "trino",
  "cockroachdb",
  "yugabyte",
  "greenplum",
  "questdb",
  "materialize",
  "risingwave",
  "cassandra",
]);

const postgresLiteralTypes = new Set<DbType>([
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

const mysqlTemporalTypes = new Set<DbType>([
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

const oracleLiteralTypes = new Set<DbType>(["oracle", "dameng", "yashandb"]);

export function supportsCopyAsInsertSql(dbType: DbType): boolean {
  return sqlInsertTypes.has(dbType);
}

function validateInsertTarget(input: { dbType: DbType; table: string; columns: string[] }): string {
  const table = input.table.trim();
  if (!supportsCopyAsInsertSql(input.dbType)) {
    throw new Error(`Copy as INSERT SQL is not supported for ${input.dbType}`);
  }
  if (!table) throw new Error("INSERT SQL requires a table name");
  if (!input.columns.length) throw new Error("INSERT SQL requires at least one column");
  return table;
}

function insertTarget(dbType: DbType, database: string | undefined, table: string, columns: string[]): string {
  const target = qualifiedTable(dbType, database, table);
  const columnList = columns.map((column) => quoteIdentifier(dbType, column)).join(", ");
  return `INSERT INTO ${target} (${columnList})`;
}

function rowValueTuple(input: { dbType: DbType; columns: string[]; columnTypes?: string[] }, row: unknown[]): string {
  if (input.columns.length !== row.length) {
    throw new Error("INSERT SQL column and value counts do not match");
  }
  const values = row
    .map((value, index) => sqlLiteral(input.dbType, value, input.columnTypes?.[index]))
    .join(", ");
  return `(${values})`;
}

export function buildInsertRowSql(input: InsertRowSqlInput): string {
  const table = validateInsertTarget(input);
  const prefix = insertTarget(input.dbType, input.database, table, input.columns);
  return `${prefix}\nVALUES ${rowValueTuple(input, input.row)};`;
}

/** 多行 INSERT 生成：batch 合并为一条语句，rows 每行一条独立语句。 */
export function buildInsertRowsSql(input: InsertRowsSqlInput): string {
  const table = validateInsertTarget(input);
  if (!input.rows.length) throw new Error("INSERT SQL requires at least one row");
  if ((input.mode ?? "rows") === "rows") {
    return input.rows
      .map((row) => `${insertTarget(input.dbType, input.database, table, input.columns)}\nVALUES ${rowValueTuple(input, row)};`)
      .join("\n");
  }
  const tuples = input.rows.map((row) => `  ${rowValueTuple(input, row)}`).join(",\n");
  return `${insertTarget(input.dbType, input.database, table, input.columns)}\nVALUES\n${tuples};`;
}

export function qualifiedTable(dbType: DbType, database: string | undefined, table: string): string {
  const normalizedDatabase = database?.trim() ?? "";
  if (dbType === "sqlite" || dbType === "firebird") return quoteIdentifier(dbType, table);

  const effectiveDatabase =
    normalizedDatabase || (dbType === "duckdb" ? "main" : dbType === "sqlserver" ? "dbo" : "");
  const quotedTable = quoteIdentifier(dbType, table);
  return effectiveDatabase
    ? `${quoteIdentifier(dbType, effectiveDatabase)}.${quotedTable}`
    : quotedTable;
}

export function quoteIdentifier(dbType: DbType, identifier: string): string {
  if (backtickIdentifierTypes.has(dbType)) {
    return `\`${replaceEvery(identifier, "`", "``")}\``;
  }
  if (dbType === "sqlserver") {
    return `[${replaceEvery(identifier, "]", "]]")}]`;
  }
  return `"${replaceEvery(identifier, '"', '""')}"`;
}

function sqlLiteral(dbType: DbType, value: unknown, columnType?: string): string {
  if (value === null || value === undefined) return "NULL";
  if (typeof value === "bigint") return value.toString();
  if (typeof value === "number") {
    return Number.isFinite(value) ? String(value) : quoteText(dbType, String(value));
  }
  if (typeof value === "boolean") {
    if (oracleLiteralTypes.has(dbType)) return value ? "1" : "0";
    return value ? "TRUE" : "FALSE";
  }
  if (typeof value === "string") return temporalLiteral(dbType, columnType, value);
  if (value instanceof Date) return temporalLiteral(dbType, columnType, value.toISOString());
  if (value instanceof Uint8Array) return binaryLiteral(dbType, value);
  if (dbType === "cassandra") return cqlCollectionLiteral(value);
  return quoteText(dbType, stringifyJson(value));
}

interface Rfc3339Parts {
  date: string;
  time: string;
  offset: string;
}

function temporalLiteral(dbType: DbType, columnType: string | undefined, value: string): string {
  const type = columnType?.trim().toLocaleLowerCase() ?? "";
  const parts = parseRfc3339(value);
  if (!parts || !isTemporalColumnType(type)) return quoteText(dbType, value);

  if (mysqlTemporalTypes.has(dbType)) {
    if (isDateOnlyType(type)) return quoteText(dbType, parts.date);
    return quoteText(dbType, `${parts.date} ${parts.time}`);
  }
  if (postgresLiteralTypes.has(dbType)) {
    return quoteText(
      dbType,
      hasTimeZone(type) ? `${parts.date} ${parts.time}${parts.offset}` : `${parts.date} ${parts.time}`
    );
  }
  if (dbType === "sqlserver") {
    const text = `${parts.date}T${parts.time}${hasTimeZone(type) ? parts.offset : ""}`;
    return quoteText(dbType, text);
  }
  if (oracleLiteralTypes.has(dbType)) {
    if (isDateOnlyType(type)) {
      return `TO_DATE('${parts.date} ${parts.time}', 'YYYY-MM-DD HH24:MI:SS')`;
    }
    if (hasTimeZone(type)) {
      return `TO_TIMESTAMP_TZ('${parts.date} ${parts.time} ${parts.offset}', 'YYYY-MM-DD HH24:MI:SS.FF TZH:TZM')`;
    }
    return `TO_TIMESTAMP('${parts.date} ${parts.time}', 'YYYY-MM-DD HH24:MI:SS.FF')`;
  }
  if (dbType === "trino") {
    if (isDateOnlyType(type)) return `DATE ${quoteText(dbType, parts.date)}`;
    const text = `${parts.date} ${parts.time}${hasTimeZone(type) ? ` ${parts.offset}` : ""}`;
    return `TIMESTAMP ${quoteText(dbType, text)}`;
  }
  if (dbType === "clickhouse") {
    const precision = type.match(/datetime64\s*\(\s*(\d+)/i)?.[1];
    const args = precision ? `${quoteText(dbType, value)}, ${precision}` : quoteText(dbType, value);
    return `parseDateTime${/datetime64/i.test(type) ? "64" : ""}BestEffort(${args})`;
  }
  if (dbType === "duckdb" || dbType === "firebird") {
    const suffix = hasTimeZone(type) ? parts.offset : "";
    return quoteText(dbType, isDateOnlyType(type) ? parts.date : `${parts.date} ${parts.time}${suffix}`);
  }
  if (dbType === "cassandra" && isDateOnlyType(type)) return quoteText(dbType, parts.date);
  return quoteText(dbType, value);
}

function parseRfc3339(value: string): Rfc3339Parts | undefined {
  const match = value.trim().match(
    /^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?)(Z|[+-]\d{2}:?\d{2})$/i
  );
  if (!match) return undefined;
  const offset = match[3].toUpperCase() === "Z" ? "+00:00" : normalizeOffset(match[3]);
  return { date: match[1], time: match[2], offset };
}

function normalizeOffset(offset: string): string {
  return offset.includes(":") ? offset : `${offset.slice(0, 3)}:${offset.slice(3)}`;
}

function isTemporalColumnType(type: string): boolean {
  return /\b(?:date|datetime|datetime2|datetime64|datetimeoffset|smalldatetime|timestamp|timestamptz)\b/i.test(type);
}

function isDateOnlyType(type: string): boolean {
  return /^date(?:\b|\s|\()/i.test(type);
}

function hasTimeZone(type: string): boolean {
  return /\b(?:with\s+(?:local\s+)?time\s+zone|timestamptz|datetimeoffset)\b/i.test(type);
}

function quoteText(dbType: DbType, value: string): string {
  let escaped = replaceEvery(value, "'", "''");
  if (backtickIdentifierTypes.has(dbType)) {
    escaped = replaceEvery(escaped, "\\", "\\\\");
  }
  const prefix = dbType === "sqlserver" ? "N" : "";
  return `${prefix}'${escaped}'`;
}

function binaryLiteral(dbType: DbType, value: Uint8Array): string {
  const hex = Array.from(value, (byte) => byte.toString(16).padStart(2, "0"))
    .join("")
    .toUpperCase();
  if (postgresLiteralTypes.has(dbType)) return `decode('${hex}', 'hex')`;
  if (dbType === "sqlserver") return `0x${hex}`;
  if (oracleLiteralTypes.has(dbType)) return `HEXTORAW('${hex}')`;
  if (dbType === "cassandra") return `0x${hex}`;
  if (dbType === "clickhouse") return `unhex('${hex}')`;
  return `X'${hex}'`;
}

function cqlCollectionLiteral(value: unknown): string {
  if (value === null || value === undefined) return "NULL";
  if (typeof value === "bigint") return value.toString();
  if (typeof value === "number") {
    return Number.isFinite(value) ? String(value) : `'${String(value)}'`;
  }
  if (typeof value === "boolean") return value ? "TRUE" : "FALSE";
  if (typeof value === "string") return `'${replaceEvery(value, "'", "''")}'`;
  if (value instanceof Date) return `'${value.toISOString()}'`;
  if (value instanceof Uint8Array) return binaryLiteral("cassandra", value);
  if (Array.isArray(value)) return `[${value.map(cqlCollectionLiteral).join(", ")}]`;
  if (isPlainObject(value)) {
    return `{${Object.entries(value)
      .map(([key, entry]) => `${cqlCollectionLiteral(key)}: ${cqlCollectionLiteral(entry)}`)
      .join(", ")}}`;
  }
  return `'${replaceEvery(stringifyJson(value), "'", "''")}'`;
}

function stringifyJson(value: unknown): string {
  const serialized = JSON.stringify(value, (_key, entry) =>
    typeof entry === "bigint" ? entry.toString() : entry
  );
  return serialized ?? String(value);
}

function replaceEvery(value: string, search: string, replacement: string): string {
  return value.split(search).join(replacement);
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (!value || typeof value !== "object") return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}
