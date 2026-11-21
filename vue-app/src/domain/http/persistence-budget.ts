import { retainWithinJsonBudget, truncateUtf8, utf8ByteLength } from "@/domain/persistence/byte-budget";

export const HTTP_HISTORY_MAX_ITEMS = 200;
export const HTTP_HISTORY_MAX_TOTAL_BYTES = 512 * 1024;
export const HTTP_HISTORY_MAX_URL_BYTES = 16 * 1024;

export interface HttpHistoryBudgetItem {
  id: string;
  method: string;
  url: string;
  status: number;
  elapsed: number;
  createdAt: number;
}

export function normalizeHttpHistoryForPersistence<T extends HttpHistoryBudgetItem>(items: readonly T[]) {
  return retainWithinJsonBudget(items, {
    maxItems: HTTP_HISTORY_MAX_ITEMS,
    maxTotalBytes: HTTP_HISTORY_MAX_TOTAL_BYTES,
    normalize: (item) => ({
      ...item,
      url: truncateUtf8(item.url, HTTP_HISTORY_MAX_URL_BYTES, "… (URL truncated)"),
    }),
  });
}

export function httpHistoryJsonBytes<T extends HttpHistoryBudgetItem>(items: readonly T[]) {
  return utf8ByteLength(JSON.stringify(normalizeHttpHistoryForPersistence(items)));
}
