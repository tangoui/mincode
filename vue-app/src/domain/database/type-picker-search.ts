import { normalizeDatabaseTypeSearch } from "./type-picker";
import { databaseDriverInfo, databaseTypeLabels, type DbType } from "./types";

/**
 * Extra search terms for the built-in database type picker, beyond the type id,
 * display label and driver label. MySQL and PostgreSQL also answer to
 * "custom …" searches because they are the entry for any wire-compatible
 * database that has no dedicated type.
 */
export const databaseTypeSearchKeywords: Partial<Record<DbType, readonly string[]>> = {
  mysql: ["Custom MySQL", "自定义 MySQL", "MySQL compatible", "MySQL 兼容"],
  postgres: ["Custom PostgreSQL", "自定义 PostgreSQL", "PostgreSQL compatible", "PostgreSQL 兼容"],
  dolt: ["DoltHub"],
  greatsql: ["万里数据库", "GreatDB"],
  opentenbase: ["TBase", "腾讯"],
};

/** Whether a built-in type matches a picker query; `categoryLabel` is the localized category name. */
export function databaseTypeMatchesSearch(dbType: DbType, query: string, categoryLabel = "") {
  const normalizedQuery = normalizeDatabaseTypeSearch(query);
  if (!normalizedQuery) return true;
  const searchText = normalizeDatabaseTypeSearch(
    [
      dbType,
      databaseTypeLabels[dbType],
      databaseDriverInfo[dbType].label,
      categoryLabel,
      ...(databaseTypeSearchKeywords[dbType] ?? []),
    ].join(" "),
  );
  return searchText.includes(normalizedQuery);
}
