import type { DbType } from "@/domain/database/types";

const POSTGRES_LIKE_TYPES: ReadonlySet<DbType> = new Set([
  "postgres",
  "kingbase",
  "gaussdb",
  "opengauss",
  "highgo",
  "vastbase",
  "opentenbase",
  "cockroachdb",
  "yugabyte",
  "greenplum",
  "questdb",
  "materialize",
  "risingwave",
]);
const MYSQL_LIKE_TYPES: ReadonlySet<DbType> = new Set([
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
  "gbase8c",
  "goldendb",
  "starrocks",
  "doris",
]);

export type TableFilterLogic = "and" | "or";
export type TableFilterFieldType = "text" | "number" | "date" | "boolean";
export type TableFilterOperator =
  | "contains"
  | "notContains"
  | "equals"
  | "notEquals"
  | "startsWith"
  | "endsWith"
  | "greaterThan"
  | "greaterThanOrEqual"
  | "lessThan"
  | "lessThanOrEqual"
  | "before"
  | "after"
  | "between"
  | "isTrue"
  | "isFalse"
  | "isEmpty"
  | "isNotEmpty"
  | "isNull"
  | "isNotNull";

export interface TableFilterCondition {
  id: string;
  column: string;
  fieldType: TableFilterFieldType;
  operator: TableFilterOperator;
  value?: string;
  valueTo?: string;
}

export interface TableFilterModel {
  logic: TableFilterLogic;
  conditions: TableFilterCondition[];
}

const FILTER_OPERATORS: Record<TableFilterFieldType, readonly TableFilterOperator[]> = {
  text: [
    "contains",
    "notContains",
    "equals",
    "notEquals",
    "startsWith",
    "endsWith",
    "greaterThan",
    "lessThan",
    "isEmpty",
    "isNotEmpty",
    "isNull",
    "isNotNull",
  ],
  number: [
    "equals",
    "notEquals",
    "greaterThan",
    "greaterThanOrEqual",
    "lessThan",
    "lessThanOrEqual",
    "between",
    "isEmpty",
    "isNotEmpty",
    "isNull",
    "isNotNull",
  ],
  date: ["equals", "before", "after", "greaterThan", "lessThan", "between", "isEmpty", "isNotEmpty", "isNull", "isNotNull"],
  boolean: ["isTrue", "isFalse", "isEmpty", "isNotEmpty", "isNull", "isNotNull"],
};

export function tableFilterOperators(fieldType: TableFilterFieldType) {
  return FILTER_OPERATORS[fieldType];
}

export function tableFilterOperatorNeedsValue(operator: TableFilterOperator) {
  return !["isTrue", "isFalse", "isEmpty", "isNotEmpty", "isNull", "isNotNull"].includes(operator);
}

export function inferTableFilterFieldType(dataType?: string, values: unknown[] = []): TableFilterFieldType {
  const normalized = dataType?.trim().toLowerCase() ?? "";
  if (/bool|bit/.test(normalized)) return "boolean";
  if (/date|time|year/.test(normalized)) return "date";
  if (/int|decimal|numeric|number|real|double|float|money/.test(normalized)) return "number";
  const sample = values.find((value) => value != null);
  if (typeof sample === "boolean") return "boolean";
  if (typeof sample === "number" || typeof sample === "bigint") return "number";
  if (sample instanceof Date) return "date";
  return "text";
}

export function createTableRowFilter(
  columns: string[],
  model: TableFilterModel
): ((row: unknown[]) => boolean) | undefined {
  const columnIndexes = new Map<string, number>();
  for (let index = 0; index < columns.length; index += 1) {
    const column = columns[index];
    if (!columnIndexes.has(column)) columnIndexes.set(column, index);
  }
  const conditions = validFilterConditions(model.conditions).map((condition) =>
    compileFilterCondition(columnIndexes.get(condition.column) ?? -1, condition)
  );
  if (!conditions.length) return undefined;

  if (model.logic === "or") {
    return (row) => {
      for (const condition of conditions) {
        if (condition(row)) return true;
      }
      return false;
    };
  }
  return (row) => {
    for (const condition of conditions) {
      if (!condition(row)) return false;
    }
    return true;
  };
}

export function filterTableRows(
  rows: unknown[][],
  columns: string[],
  model: TableFilterModel
): unknown[][] {
  const matches = createTableRowFilter(columns, model);
  return matches ? rows.filter(matches) : rows;
}

export function buildTableFilterSql(
  dbType: DbType,
  database: string,
  table: string,
  model: TableFilterModel,
  limit: number,
  offset: number
): string {
  if (!supportsSqlTableSearch(dbType)) {
    throw new Error(`Table filtering is not supported for ${dbType}.`);
  }
  const where = filterWhereClause(dbType, model);
  const tableRef = quoteTable(dbType, database, table);
  const safeLimit = positiveInteger(limit, 100);
  const safeOffset = nonNegativeInteger(offset);
  if (dbType === "sqlserver") {
    return `SELECT * FROM ${tableRef} WHERE ${where} ORDER BY (SELECT NULL) OFFSET ${safeOffset} ROWS FETCH NEXT ${safeLimit} ROWS ONLY`;
  }
  return `SELECT * FROM ${tableRef} WHERE ${where} LIMIT ${safeLimit} OFFSET ${safeOffset}`;
}

export function buildMongoTableFilterQuery(
  database: string,
  collection: string,
  model: TableFilterModel,
  limit: number,
  skip: number
): string {
  const conditions = validFilterConditions(model.conditions);
  if (!conditions.length) throw new Error("Table filtering requires at least one complete condition.");
  const filters = conditions.map(buildMongoFilterCondition);
  return JSON.stringify({
    db: database.trim(),
    collection: collection.trim(),
    filter: { [model.logic === "or" ? "$or" : "$and"]: filters },
    limit: positiveInteger(limit, 100),
    skip: nonNegativeInteger(skip),
  });
}

/** Whether the connected database can run a generated all-column SQL search. */
export function supportsSqlTableSearch(dbType: DbType): boolean {
  return (
    MYSQL_LIKE_TYPES.has(dbType) ||
    POSTGRES_LIKE_TYPES.has(dbType) ||
    ["sqlite", "duckdb", "sqlserver", "clickhouse", "tdengine", "dameng"].includes(dbType)
  );
}

/** Whether the table toolbar can search all rows without opening a query editor. */
export function supportsTableSearch(dbType: DbType): boolean {
  return dbType === "mongodb" || supportsSqlTableSearch(dbType);
}

/**
 * 全表关键字搜索会把每列转成文本再 LIKE。MySQL 9.x 的 VECTOR 列不支持
 * CAST(列 AS CHAR)（运行时抛 Error 1210: Incorrect arguments to cast_as_char），
 * 因此这类列必须从搜索列清单中排除。
 */
export function isTextSearchableColumnType(dbType: DbType, dataType?: string): boolean {
  const normalized = dataType?.trim().toLowerCase() ?? "";
  if (MYSQL_LIKE_TYPES.has(dbType) && normalized.startsWith("vector")) return false;
  return true;
}

/**
 * Builds the JSON request expected by the MongoDB query backend for a
 * collection-wide text search. It deliberately does not emit SQL: table toolbar
 * searches stay in the active collection tab and MongoDB receives a JSON filter.
 */
export function buildMongoTableSearchQuery(
  database: string,
  collection: string,
  columns: string[],
  keyword: string,
  limit: number,
  skip: number
): string {
  const term = keyword.trim();
  const fields = [...new Set(columns.map((column) => column.trim()))].filter(
    (column) => column && column !== "_id"
  );
  if (!term || fields.length === 0) {
    throw new Error("MongoDB table search requires a keyword and at least one non-_id column.");
  }

  const filter = {
    $or: fields.map((field) => ({
      [field]: { $regex: escapeMongoRegex(term), $options: "i" },
    })),
  };
  return JSON.stringify({
    db: database.trim(),
    collection: collection.trim(),
    filter,
    limit: positiveInteger(limit, 100),
    skip: nonNegativeInteger(skip),
  });
}

function escapeMongoRegex(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Builds a read-only, paginated, all-column table search. Both table/column
 * identifiers and the search value are escaped, so a plain search term can
 * never be emitted as unquoted SQL.
 */
export function buildTableSearchSql(
  dbType: DbType,
  database: string,
  table: string,
  columns: string[],
  keyword: string,
  limit: number,
  offset: number
): string {
  if (!supportsSqlTableSearch(dbType)) {
    throw new Error(`Full-table search is not supported for ${dbType}.`);
  }

  const term = keyword.trim();
  const safeLimit = positiveInteger(limit, 100);
  const safeOffset = nonNegativeInteger(offset);
  const tableRef = quoteTable(dbType, database, table);
  const where = searchWhereClause(dbType, columns, term);

  if (dbType === "sqlserver") {
    return `SELECT * FROM ${tableRef} WHERE ${where} ORDER BY (SELECT NULL) OFFSET ${safeOffset} ROWS FETCH NEXT ${safeLimit} ROWS ONLY`;
  }
  return `SELECT * FROM ${tableRef} WHERE ${where} LIMIT ${safeLimit} OFFSET ${safeOffset}`;
}

function filterWhereClause(dbType: DbType, model: TableFilterModel) {
  const conditions = validFilterConditions(model.conditions);
  if (!conditions.length) throw new Error("Table filtering requires at least one complete condition.");
  return conditions
    .map((condition) => buildSqlFilterCondition(dbType, condition))
    .join(model.logic === "or" ? " OR " : " AND ");
}

function searchWhereClause(dbType: DbType, columns: string[], term: string) {
  const searchableColumns = [...new Set(columns.map((column) => column.trim()).filter(Boolean))];
  if (!term || searchableColumns.length === 0) {
    throw new Error("Table search requires a keyword and at least one column.");
  }
  const likeLiteral = quoteLikePattern(dbType, term, "%", "%");
  return searchableColumns
    .map((column) => `${textExpression(dbType, column)} ${likeOperator(dbType)} ${likeLiteral}`)
    .join(" OR ");
}

// SQL Server 的 COUNT(*) 返回 int，超过 2^31 行会直接报算术溢出。
function countExpression(dbType: DbType) {
  return dbType === "sqlserver" ? "COUNT_BIG(*)" : "COUNT(*)";
}

/** Counts every row in a table so the pager can resolve the last page. */
export function buildTableRowCountSql(dbType: DbType, database: string, table: string): string {
  if (!supportsSqlTableSearch(dbType)) {
    throw new Error(`Row counting is not supported for ${dbType}.`);
  }
  return `SELECT ${countExpression(dbType)} FROM ${quoteTable(dbType, database, table)}`;
}

/** Counts the filtered rows using the same WHERE clause as {@link buildTableFilterSql}. */
export function buildTableFilterCountSql(
  dbType: DbType,
  database: string,
  table: string,
  model: TableFilterModel
): string {
  if (!supportsSqlTableSearch(dbType)) {
    throw new Error(`Table filtering is not supported for ${dbType}.`);
  }
  const where = filterWhereClause(dbType, model);
  return `SELECT ${countExpression(dbType)} FROM ${quoteTable(dbType, database, table)} WHERE ${where}`;
}

/** Counts the matched rows using the same WHERE clause as {@link buildTableSearchSql}. */
export function buildTableSearchCountSql(
  dbType: DbType,
  database: string,
  table: string,
  columns: string[],
  keyword: string
): string {
  if (!supportsSqlTableSearch(dbType)) {
    throw new Error(`Full-table search is not supported for ${dbType}.`);
  }
  const where = searchWhereClause(dbType, columns, keyword.trim());
  return `SELECT ${countExpression(dbType)} FROM ${quoteTable(dbType, database, table)} WHERE ${where}`;
}

/**
 * The row count a generated search/filter page actually returns. The builders
 * clamp LIMIT, so the pager must divide the total by this — not by the raw page
 * size — or the last page lands short.
 */
export function tableQueryPageSize(pageSize: number): number {
  return positiveInteger(pageSize, 100);
}

export function lastPageFromTotal(total: number, pageSize: number): number {
  if (!Number.isFinite(total) || total <= 0) return 1;
  return Math.max(1, Math.ceil(total / Math.max(1, pageSize)));
}

function validFilterConditions(conditions: TableFilterCondition[]) {
  return conditions.filter(
    (condition) =>
      condition.column.trim() &&
      tableFilterOperators(condition.fieldType).includes(condition.operator) &&
      (!tableFilterOperatorNeedsValue(condition.operator) || Boolean(condition.value?.trim())) &&
      (condition.operator !== "between" || Boolean(condition.valueTo?.trim()))
  );
}

function compileFilterCondition(
  columnIndex: number,
  condition: TableFilterCondition
): (row: unknown[]) => boolean {
  if (columnIndex < 0) return () => false;

  if (condition.operator === "isNull") return (row) => row[columnIndex] == null;
  if (condition.operator === "isNotNull") return (row) => row[columnIndex] != null;
  if (condition.operator === "isEmpty") {
    return (row) => row[columnIndex] == null || row[columnIndex] === "";
  }
  if (condition.operator === "isNotEmpty") {
    return (row) => row[columnIndex] != null && row[columnIndex] !== "";
  }
  if (condition.operator === "isTrue") {
    return (row) => {
      const value = row[columnIndex];
      return value === true || value === 1 || value === "1" || value === "true";
    };
  }
  if (condition.operator === "isFalse") {
    return (row) => {
      const value = row[columnIndex];
      return value === false || value === 0 || value === "0" || value === "false";
    };
  }

  const expected = condition.value ?? "";
  if (condition.fieldType === "number") {
    const numericExpected = Number(expected);
    const numericExpectedTo = Number(condition.valueTo ?? "");
    return (row) => {
      const value = row[columnIndex];
      return value != null && value !== "" && compareFilterValues(
        Number(value),
        numericExpected,
        numericExpectedTo,
        condition.operator
      );
    };
  }
  if (condition.fieldType === "date") {
    const dateExpected = new Date(expected).getTime();
    const dateExpectedTo = new Date(condition.valueTo ?? "").getTime();
    return (row) => {
      const value = row[columnIndex];
      return value != null && value !== "" && compareFilterValues(
        new Date(String(value)).getTime(),
        dateExpected,
        dateExpectedTo,
        condition.operator
      );
    };
  }

  const target = expected.toLocaleLowerCase();
  return (row) => {
    const value = row[columnIndex];
    if (value == null || value === "") return false;
    const actual = String(value).toLocaleLowerCase();
    switch (condition.operator) {
      case "contains": return actual.includes(target);
      case "notContains": return !actual.includes(target);
      case "startsWith": return actual.startsWith(target);
      case "endsWith": return actual.endsWith(target);
      case "equals": return actual === target;
      case "notEquals": return actual !== target;
      case "greaterThan": return actual > target;
      case "lessThan": return actual < target;
      default: return false;
    }
  };
}

function compareFilterValues(
  actual: number,
  expected: number,
  expectedTo: number,
  operator: TableFilterOperator
) {
  if (!Number.isFinite(actual) || !Number.isFinite(expected)) return false;
  switch (operator) {
    case "equals": return actual === expected;
    case "notEquals": return actual !== expected;
    case "greaterThan":
    case "after": return actual > expected;
    case "greaterThanOrEqual": return actual >= expected;
    case "lessThan":
    case "before": return actual < expected;
    case "lessThanOrEqual": return actual <= expected;
    case "between": return Number.isFinite(expectedTo) && actual >= expected && actual <= expectedTo;
    default: return false;
  }
}

function buildSqlFilterCondition(dbType: DbType, condition: TableFilterCondition) {
  const column = quoteColumn(dbType, condition.column);
  if (condition.operator === "isNull") return `${column} IS NULL`;
  if (condition.operator === "isNotNull") return `${column} IS NOT NULL`;
  if (condition.operator === "isEmpty" || condition.operator === "isNotEmpty") {
    const isEmpty = condition.operator === "isEmpty";
    // 只有文本字段才比较空串：PostgreSQL 等对数值/日期列做 = '' 会直接报类型错误。
    if (condition.fieldType !== "text") return `${column} ${isEmpty ? "IS NULL" : "IS NOT NULL"}`;
    const text = MYSQL_LIKE_TYPES.has(dbType) ? column : textExpression(dbType, condition.column);
    return isEmpty ? `(${column} IS NULL OR ${text} = '')` : `(${column} IS NOT NULL AND ${text} <> '')`;
  }
  if (condition.operator === "isTrue") return `${column} = ${sqlBooleanLiteral(dbType, true)}`;
  if (condition.operator === "isFalse") return `${column} = ${sqlBooleanLiteral(dbType, false)}`;

  const value = condition.value ?? "";
  if (["contains", "notContains", "startsWith", "endsWith"].includes(condition.operator)) {
    const pattern = quoteLikePattern(
      dbType,
      value,
      condition.operator === "startsWith" ? "" : "%",
      condition.operator === "endsWith" ? "" : "%"
    );
    const comparison = `${textExpression(dbType, condition.column)} ${likeOperator(dbType)} ${pattern}`;
    return condition.operator === "notContains" ? `NOT (${comparison})` : comparison;
  }
  const literal = sqlFilterLiteral(dbType, condition.fieldType, value);
  if (condition.operator === "between") {
    return `${column} BETWEEN ${literal} AND ${sqlFilterLiteral(dbType, condition.fieldType, condition.valueTo ?? "")}`;
  }
  const operators: Partial<Record<TableFilterOperator, string>> = {
    equals: "=", notEquals: "<>", greaterThan: ">", greaterThanOrEqual: ">=",
    lessThan: "<", lessThanOrEqual: "<=", before: "<", after: ">",
  };
  return `${column} ${operators[condition.operator] ?? "="} ${literal}`;
}

function buildMongoFilterCondition(condition: TableFilterCondition): Record<string, unknown> {
  const field = condition.column;
  if (condition.operator === "isNull") return { [field]: null };
  if (condition.operator === "isNotNull") return { [field]: { $ne: null } };
  if (condition.operator === "isEmpty") return { [field]: { $in: [null, ""] } };
  if (condition.operator === "isNotEmpty") return { [field]: { $nin: [null, ""] } };
  if (condition.operator === "isTrue") return { [field]: true };
  if (condition.operator === "isFalse") return { [field]: false };
  const value = mongoFilterValue(condition.fieldType, condition.value ?? "");
  if (condition.operator === "contains") return { [field]: { $regex: escapeMongoRegex(String(value)), $options: "i" } };
  if (condition.operator === "notContains") return { [field]: { $not: { $regex: escapeMongoRegex(String(value)), $options: "i" } } };
  if (condition.operator === "startsWith") return { [field]: { $regex: `^${escapeMongoRegex(String(value))}`, $options: "i" } };
  if (condition.operator === "endsWith") return { [field]: { $regex: `${escapeMongoRegex(String(value))}$`, $options: "i" } };
  if (condition.operator === "between") {
    return { [field]: { $gte: value, $lte: mongoFilterValue(condition.fieldType, condition.valueTo ?? "") } };
  }
  const operators: Partial<Record<TableFilterOperator, string>> = {
    notEquals: "$ne", greaterThan: "$gt", greaterThanOrEqual: "$gte", lessThan: "$lt",
    lessThanOrEqual: "$lte", before: "$lt", after: "$gt",
  };
  return condition.operator === "equals" ? { [field]: value } : { [field]: { [operators[condition.operator] ?? "$eq"]: value } };
}

function mongoFilterValue(fieldType: TableFilterFieldType, value: string) {
  if (fieldType === "number") return Number(value);
  if (fieldType === "date") return { $date: value };
  return value;
}

const NUMERIC_FILTER_LITERAL = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i;

function sqlFilterLiteral(dbType: DbType, fieldType: TableFilterFieldType, value: string) {
  if (fieldType === "number") {
    // 原样输出校验过的文本：经 Number 往返会让 BIGINT 丢精度。
    const text = value.trim();
    if (!NUMERIC_FILTER_LITERAL.test(text)) throw new Error("Invalid numeric filter value.");
    return text.replace(/^\+/, "");
  }
  return quoteSqlString(dbType, value);
}

function sqlBooleanLiteral(dbType: DbType, value: boolean) {
  return POSTGRES_LIKE_TYPES.has(dbType) || dbType === "sqlite" || dbType === "duckdb"
    ? String(value).toUpperCase()
    : value ? "1" : "0";
}

// MySQL 系、ClickHouse、TDengine 的字符串字面量把反斜杠当转义符，必须一并转义。
function usesBackslashStringEscapes(dbType: DbType) {
  return MYSQL_LIKE_TYPES.has(dbType) || dbType === "clickhouse" || dbType === "tdengine";
}

function quoteSqlString(dbType: DbType, value: string) {
  const escaped = usesBackslashStringEscapes(dbType) ? value.replace(/\\/g, "\\\\") : value;
  return `'${escaped.replace(/'/g, "''")}'`;
}

function quoteColumn(dbType: DbType, column: string) {
  if (dbType === "sqlserver") return quoteSqlServerIdentifier(column);
  if (MYSQL_LIKE_TYPES.has(dbType) || dbType === "clickhouse" || dbType === "tdengine") {
    return quoteBacktickIdentifier(column);
  }
  return quoteDoubleIdentifier(column);
}

/** Escapes LIKE wildcards in the user value first, then adds the requested `%` anchors. */
function quoteLikePattern(dbType: DbType, value: string, prefix: "%" | "", suffix: "%" | "") {
  // TDengine 与旧版 ClickHouse 不支持 ESCAPE 子句，只能用默认的反斜杠转义通配符。
  if (dbType === "clickhouse" || dbType === "tdengine") {
    const escaped = value.replace(/[\\%_]/g, (character) => `\\${character}`);
    return quoteSqlString(dbType, `${prefix}${escaped}${suffix}`);
  }
  const escaped = value.replace(/[!%_]/g, (character) => `!${character}`);
  return `${quoteSqlString(dbType, `${prefix}${escaped}${suffix}`)} ESCAPE '!'`;
}

function positiveInteger(value: number, fallback: number) {
  if (!Number.isFinite(value)) return fallback;
  return Math.max(1, Math.min(5000, Math.floor(value)));
}

function nonNegativeInteger(value: number) {
  if (!Number.isFinite(value)) return 0;
  return Math.max(0, Math.floor(value));
}

function quoteTable(dbType: DbType, database: string, table: string) {
  if (dbType === "sqlserver") {
    const [schema, tableName] = table.includes(".") ? splitQualifiedName(table) : ["dbo", table];
    return `${quoteSqlServerIdentifier(database)}.${quoteSqlServerIdentifier(schema)}.${quoteSqlServerIdentifier(tableName)}`;
  }
  if (MYSQL_LIKE_TYPES.has(dbType) || dbType === "clickhouse" || dbType === "tdengine") {
    return database.trim()
      ? `${quoteBacktickIdentifier(database)}.${quoteBacktickIdentifier(table)}`
      : quoteBacktickIdentifier(table);
  }
  return database.trim()
    ? `${quoteDoubleIdentifier(database)}.${quoteDoubleIdentifier(table)}`
    : quoteDoubleIdentifier(table);
}

function textExpression(dbType: DbType, column: string) {
  if (dbType === "sqlserver") return `CAST(${quoteSqlServerIdentifier(column)} AS NVARCHAR(MAX))`;
  if (MYSQL_LIKE_TYPES.has(dbType)) return `CAST(${quoteBacktickIdentifier(column)} AS CHAR)`;
  if (dbType === "clickhouse") return `toString(${quoteBacktickIdentifier(column)})`;
  if (dbType === "tdengine") return `CAST(${quoteBacktickIdentifier(column)} AS NCHAR)`;
  return `CAST(${quoteDoubleIdentifier(column)} AS TEXT)`;
}

function likeOperator(dbType: DbType) {
  return POSTGRES_LIKE_TYPES.has(dbType) ? "ILIKE" : "LIKE";
}

function quoteDoubleIdentifier(value: string) {
  return `"${value.replace(/"/g, '""')}"`;
}

function quoteBacktickIdentifier(value: string) {
  return `\`${value.replace(/`/g, "``")}\``;
}

function quoteSqlServerIdentifier(value: string) {
  return `[${value.replace(/\]/g, "]]")}]`;
}

function splitQualifiedName(value: string): [string, string] {
  const index = value.indexOf(".");
  return [value.slice(0, index), value.slice(index + 1)];
}
