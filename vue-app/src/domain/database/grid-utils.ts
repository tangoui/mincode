import type { DbType } from "./types";
import { isTextSearchableColumnType } from "./table-search";

export interface ColumnMetadataLike {
  name: string;
  dataType?: string;
}

/**
 * 为行文本搜索提供带 WeakMap 缓存的匹配文本生成器。
 * 换结果集或切网格时调用 reset() 丢弃旧缓存。
 */
export function createSearchableRowText(formatCell: (value: unknown) => string) {
  let cache = new WeakMap<unknown[], string>();
  return {
    search(row: unknown[]) {
      const cached = cache.get(row);
      if (cached !== undefined) return cached;

      let text = "";
      for (const value of row) {
        if (text) text += "\0";
        text += formatCell(value).toLowerCase();
      }
      cache.set(row, text);
      return text;
    },
    reset() {
      cache = new WeakMap<unknown[], string>();
    },
  };
}

export function tableSearchColumns(
  dbType: DbType | undefined,
  metadataColumns: readonly ColumnMetadataLike[],
  resultColumns: readonly string[]
) {
  const namedMetadataColumns = metadataColumns.filter((column) => Boolean(column.name));
  if (!namedMetadataColumns.length) return resultColumns.filter(Boolean);
  // MySQL 9.x 的 VECTOR 列无法 CAST 成文本，全表搜索时直接排除（见 isTextSearchableColumnType）。
  return namedMetadataColumns
    .filter((column) => !dbType || isTextSearchableColumnType(dbType, column.dataType))
    .map((column) => column.name);
}
