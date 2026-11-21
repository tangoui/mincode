import type { DatabaseQueryResult, DbType } from "@/domain/database/types";

export type DatabaseCharsetTarget =
  | { kind: "database"; database: string }
  | { kind: "table"; database: string; table: string };

export interface DatabaseCharsetOption {
  characterSet: string;
  collations: string[];
}

export interface DatabaseCharsetSelection {
  characterSet: string;
  collation: string;
}

const mysqlCharsetChangeTypes = new Set<DbType>([
  "mysql",
  "mariadb",
  "tidb",
  "oceanbase",
  "polardb",
  "tdsql",
  "dolt",
  "greatsql",
]);

const sqlTokenPattern = /^[A-Za-z0-9_]+$/;

export function supportsDatabaseCharsetChange(dbType: DbType | undefined) {
  return Boolean(dbType && mysqlCharsetChangeTypes.has(dbType));
}

function quoteMysqlIdentifier(value: string) {
  return `\`${value.replace(/`/g, "``")}\``;
}

function requireSqlToken(value: string, label: string) {
  const token = value.trim();
  if (!sqlTokenPattern.test(token)) throw new Error(`${label} contains unsupported characters`);
  return token;
}

export function buildCharsetChangeSql(
  target: DatabaseCharsetTarget,
  selection: DatabaseCharsetSelection
) {
  const characterSet = requireSqlToken(selection.characterSet, "Character set");
  const collation = requireSqlToken(selection.collation, "Collation");
  const suffix = `CHARACTER SET ${characterSet} COLLATE ${collation}`;

  if (target.kind === "database") {
    return `ALTER DATABASE ${quoteMysqlIdentifier(target.database)} ${suffix};`;
  }

  return `ALTER TABLE ${quoteMysqlIdentifier(target.database)}.${quoteMysqlIdentifier(target.table)} CONVERT TO ${suffix};`;
}

function columnIndex(result: DatabaseQueryResult, name: string) {
  return result.columns.findIndex((column) => column.toLowerCase() === name.toLowerCase());
}

export function parseCharsetOptions(result: DatabaseQueryResult) {
  const charsetIndex = columnIndex(result, "characterSet");
  const collationIndex = columnIndex(result, "collation");
  if (charsetIndex < 0 || collationIndex < 0) return [];

  const collationsByCharset = new Map<string, Set<string>>();
  for (const row of result.rows) {
    const characterSet = String(row[charsetIndex] ?? "").trim();
    const collation = String(row[collationIndex] ?? "").trim();
    if (!sqlTokenPattern.test(characterSet) || !sqlTokenPattern.test(collation)) continue;
    const collations = collationsByCharset.get(characterSet) ?? new Set<string>();
    collations.add(collation);
    collationsByCharset.set(characterSet, collations);
  }

  return Array.from(collationsByCharset, ([characterSet, collations]) => ({
    characterSet,
    collations: Array.from(collations).sort((left, right) => left.localeCompare(right)),
  })).sort((left, right) => left.characterSet.localeCompare(right.characterSet));
}

export function parseCurrentCharset(result: DatabaseQueryResult): DatabaseCharsetSelection | undefined {
  const charsetIndex = columnIndex(result, "characterSet");
  const collationIndex = columnIndex(result, "collation");
  const row = result.rows[0];
  if (!row || charsetIndex < 0 || collationIndex < 0) return undefined;

  const characterSet = String(row[charsetIndex] ?? "").trim();
  const collation = String(row[collationIndex] ?? "").trim();
  if (!sqlTokenPattern.test(characterSet) || !sqlTokenPattern.test(collation)) return undefined;
  return { characterSet, collation };
}

export function charsetOptionsQuery() {
  return [
    "SELECT CHARACTER_SET_NAME AS characterSet, COLLATION_NAME AS collation",
    "FROM information_schema.COLLATIONS",
    "ORDER BY CHARACTER_SET_NAME, COLLATION_NAME",
  ].join("\n");
}

function sqlLiteral(value: string) {
  const bytes = new TextEncoder().encode(value);
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
  return `CONVERT(X'${hex}' USING utf8mb4)`;
}

export function currentCharsetQuery(target: DatabaseCharsetTarget) {
  if (target.kind === "database") {
    return [
      "SELECT DEFAULT_CHARACTER_SET_NAME AS characterSet, DEFAULT_COLLATION_NAME AS collation",
      "FROM information_schema.SCHEMATA",
      `WHERE SCHEMA_NAME = ${sqlLiteral(target.database)}`,
    ].join("\n");
  }

  return [
    "SELECT c.CHARACTER_SET_NAME AS characterSet, t.TABLE_COLLATION AS collation",
    "FROM information_schema.TABLES t",
    "JOIN information_schema.COLLATIONS c ON c.COLLATION_NAME = t.TABLE_COLLATION",
    `WHERE t.TABLE_SCHEMA = ${sqlLiteral(target.database)}`,
    `  AND t.TABLE_NAME = ${sqlLiteral(target.table)}`,
  ].join("\n");
}
