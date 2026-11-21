import type { DbType } from "./types";

/** 选择器“最近使用”最多显示的数据库类型数。 */
export const RECENT_DATABASE_TYPE_LIMIT = 8;

export interface DatabaseTypePickerCategory<Id extends string = string> {
  id: Id;
  types: readonly DbType[];
}

export interface RecentConnectionLike {
  dbType: DbType;
  driver?: unknown;
  lastConnectedAt?: number;
  updatedAt?: number;
}

export function normalizeDatabaseTypeSearch(value: string) {
  return value.trim().toLocaleLowerCase().replace(/[\s_-]+/g, "");
}

/**
 * 按已保存连接推导“最近使用”的内置类型：最近连接时间优先，其次是修改时间；
 * 插件驱动的连接不计入，只保留当前可用的类型。
 */
export function recentDatabaseTypes(
  connections: readonly RecentConnectionLike[],
  isAvailable: (dbType: DbType) => boolean,
  limit = RECENT_DATABASE_TYPE_LIMIT,
): DbType[] {
  const latest = new Map<DbType, number>();
  for (const connection of connections) {
    if (connection.driver || !isAvailable(connection.dbType)) continue;
    const time = Math.max(connection.lastConnectedAt ?? 0, connection.updatedAt ?? 0);
    latest.set(connection.dbType, Math.max(latest.get(connection.dbType) ?? 0, time));
  }
  return [...latest.entries()]
    .sort((left, right) => right[1] - left[1])
    .slice(0, limit)
    .map(([dbType]) => dbType);
}

/** 搜索结果按分类分组；同一类型只出现在第一个匹配的分类里。 */
export function groupDatabaseTypeMatches<Id extends string>(
  categories: readonly DatabaseTypePickerCategory<Id>[],
  query: string,
  searchText: (dbType: DbType, category: DatabaseTypePickerCategory<Id>) => string,
): Array<{ category: DatabaseTypePickerCategory<Id>; types: DbType[] }> {
  const needle = normalizeDatabaseTypeSearch(query);
  if (!needle) return [];
  const seen = new Set<DbType>();
  const groups: Array<{ category: DatabaseTypePickerCategory<Id>; types: DbType[] }> = [];
  for (const category of categories) {
    const types = category.types.filter((dbType) => {
      if (seen.has(dbType)) return false;
      return normalizeDatabaseTypeSearch(searchText(dbType, category)).includes(needle);
    });
    for (const dbType of types) seen.add(dbType);
    if (types.length) groups.push({ category, types });
  }
  return groups;
}

/** 网格里的方向键移动：左右逐个，上下按列数跳行，越界时停在两端。 */
export function moveGridFocus(index: number, total: number, columns: number, key: string) {
  if (total <= 0) return -1;
  const current = index < 0 ? -1 : Math.min(index, total - 1);
  const step = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -Math.max(1, columns), ArrowDown: Math.max(1, columns) }[key];
  if (step === undefined) return current;
  if (current < 0) return step > 0 ? 0 : total - 1;
  return Math.min(total - 1, Math.max(0, current + step));
}
