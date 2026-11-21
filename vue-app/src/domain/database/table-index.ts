import type { DatabaseColumnInfo, DatabaseIndexInfo } from "./types";

const DEFAULT_PRIMARY_INDEX_NAME = "PRIMARY";

export interface DatabaseDisplayIndexInfo extends DatabaseIndexInfo {
  primary: boolean;
}

function normalizeIdentifier(value: string) {
  return value.trim().toLocaleLowerCase();
}

function sameColumns(left: string[], right: string[]) {
  return (
    left.length === right.length &&
    left.every(
      (column, index) => normalizeIdentifier(column) === normalizeIdentifier(right[index] ?? "")
    )
  );
}

export function resolvePrimaryKeyColumns(
  primaryKeys: string[],
  columns: DatabaseColumnInfo[],
  fallbackPrimaryKeys: string[] = []
): string[] {
  if (primaryKeys.length) return primaryKeys;

  const columnPrimaryKeys = columns
    .filter((column) => column.isPrimaryKey)
    .map((column) => column.name);
  return columnPrimaryKeys.length ? columnPrimaryKeys : fallbackPrimaryKeys;
}

/**
 * Adds the primary-key index to table-details presentation data. Native index
 * discovery intentionally excludes primary-key constraints in most dialects
 * because schema-sync handles them separately; the structure dialog should
 * still show that index while preventing ordinary index edit/drop actions.
 */
export function indexesWithPrimaryKey(
  indexes: DatabaseIndexInfo[],
  primaryKeys: string[],
  primaryKeyName?: string
): DatabaseDisplayIndexInfo[] {
  const name = primaryKeyName?.trim() || DEFAULT_PRIMARY_INDEX_NAME;
  const decorated = indexes.map((index) => ({
    ...index,
    columns: [...index.columns],
    primary:
      primaryKeys.length > 0 &&
      (normalizeIdentifier(index.name) === normalizeIdentifier(name) ||
        (index.unique && sameColumns(index.columns, primaryKeys))),
  }));

  if (!primaryKeys.length || decorated.some((index) => index.primary)) return decorated;
  return [{ name, columns: [...primaryKeys], unique: true, primary: true }, ...decorated];
}
