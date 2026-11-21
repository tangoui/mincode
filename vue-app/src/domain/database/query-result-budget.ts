import type { DatabaseQueryResult } from "./types";

export const DEFAULT_QUERY_RESULT_BUDGET = {
  maxTotalBytes: 64 * 1024 * 1024,
  maxFullResults: 3,
  maxEstimatedResultBytes: 16 * 1024 * 1024,
  maxRowsToSample: 128,
  maxValueDepth: 4,
} as const;

interface EstimateOptions {
  maxRowsToSample?: number;
  maxValueDepth?: number;
}

export interface QueryResultBudgetEntry {
  id: string;
  estimatedBytes: number;
  lastAccessedAt: number;
  hasResult: boolean;
}

const estimateValue = (value: unknown, depth: number): number => {
  if (value == null) return 8;
  if (typeof value === "string") return 24 + value.length * 2;
  if (typeof value === "number" || typeof value === "boolean") return 16;
  if (depth <= 0) return 64;
  if (Array.isArray(value)) {
    return 32 + value.reduce((total, item) => total + 8 + estimateValue(item, depth - 1), 0);
  }
  if (typeof value === "object") {
    return 48 + Object.entries(value).reduce(
      (total, [key, item]) => total + 24 + key.length * 2 + estimateValue(item, depth - 1),
      0,
    );
  }
  return 24;
};

export const estimateQueryResultBytes = (
  result: DatabaseQueryResult,
  options: EstimateOptions = {},
): number => {
  const maxRows = Math.max(1, options.maxRowsToSample ?? DEFAULT_QUERY_RESULT_BUDGET.maxRowsToSample);
  const depth = Math.max(0, options.maxValueDepth ?? DEFAULT_QUERY_RESULT_BUDGET.maxValueDepth);
  const sampledRows = result.rows.slice(0, maxRows);
  const sampledBytes = sampledRows.reduce((total, row) => total + estimateValue(row, depth), 0);
  const projectedRows = Math.max(result.rows.length, result.rowCount || 0);
  const projectedBytes = sampledRows.length
    ? Math.ceil((sampledBytes / sampledRows.length) * projectedRows)
    : 0;
  const columnsBytes = result.columns.reduce((total, column) => total + 32 + column.length * 2, 0);
  return Math.min(
    DEFAULT_QUERY_RESULT_BUDGET.maxEstimatedResultBytes,
    128 + columnsBytes + projectedBytes,
  );
};

export const selectQueryResultsToRelease = (
  entries: QueryResultBudgetEntry[],
  activeId: string,
  budget = DEFAULT_QUERY_RESULT_BUDGET,
): string[] => {
  const retained = entries.filter((entry) => entry.hasResult);
  let totalBytes = retained.reduce((total, entry) => total + entry.estimatedBytes, 0);
  let fullResults = retained.length;
  const candidates = retained
    .filter((entry) => entry.id !== activeId)
    .sort((left, right) => left.lastAccessedAt - right.lastAccessedAt);
  const released: string[] = [];
  for (const entry of candidates) {
    if (totalBytes <= budget.maxTotalBytes && fullResults <= budget.maxFullResults) break;
    released.push(entry.id);
    totalBytes -= entry.estimatedBytes;
    fullResults -= 1;
  }
  return released;
};