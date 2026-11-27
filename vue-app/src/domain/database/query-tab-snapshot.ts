import type { DatabaseQueryResult, DbType } from "./types";
import type { ProjectEditorViewState } from "@/domain/projects/types";

export type QueryTabStatus = "idle" | "running" | "success" | "error";

export interface QueryTab {
  id: string;
  name: string;
  sql: string;
  result?: DatabaseQueryResult;
  /** Connection and database `result` was queried on (captured when the query started). */
  resultSource?: QueryResultSource;
  error?: string;
  lastExecution?: { sql: string; dbType?: DbType };
  status: QueryTabStatus;
  lastExecutedAt?: number;
  resultEstimatedBytes?: number;
  resultLastAccessedAt?: number;
  resultReleased?: boolean;
  viewState?: ProjectEditorViewState;
}

export interface QueryResultSource {
  connectionId: string;
  database: string;
}

export interface StoredQueryTab {
  id: string;
  name: string;
  sql: string;
  viewState?: ProjectEditorViewState;
}

export interface StoredQueryTabs {
  version: 1 | 2;
  tabs: StoredQueryTab[];
  activeId: string;
}

let queryTabSequence = 0;

export function createQueryTab(
  sql: string,
  defaultNameFor: (number: number) => string,
  name?: string
): QueryTab {
  queryTabSequence += 1;
  return {
    id: `query-${Date.now()}-${queryTabSequence}`,
    name: name?.trim() || defaultNameFor(queryTabSequence),
    sql,
    status: "idle",
  };
}

/**
 * 关闭 SQL 标签前判断其内容是否值得确认：空白或与创建时/当前模板一致的内容
 * 可以直接丢弃，其余视为用户编辑过的 SQL。
 */
export function queryTabSqlIsModified(
  sql: string,
  baselines: readonly (string | undefined)[]
): boolean {
  const trimmed = sql.trim();
  if (!trimmed) return false;
  return !baselines.some((baseline) => baseline !== undefined && baseline.trim() === trimmed);
}

export function restoreQueryTabs(
  value: unknown,
  defaultNameFor: (number: number) => string
): QueryTab[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item, index): QueryTab[] => {
    if (!item || typeof item !== "object") return [];
    const raw = item as Record<string, unknown>;
    if (typeof raw.sql !== "string") return [];
    return [
      {
        id: typeof raw.id === "string" && raw.id ? raw.id : `query-restored-${index}-${Date.now()}`,
        name:
          typeof raw.name === "string" && raw.name.trim()
            ? raw.name
            : defaultNameFor(index + 1),
        sql: raw.sql,
        status: "idle",
        viewState: restoreEditorViewState(raw.viewState),
      },
    ];
  });
}

export function restoreEditorViewState(value: unknown): ProjectEditorViewState | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const raw = value as Record<string, unknown>;
  const positiveInteger = (candidate: unknown, fallback = 1) =>
    Number.isInteger(candidate) && Number(candidate) > 0
      ? Math.min(Number(candidate), 10_000_000)
      : fallback;
  const boundedNumber = (candidate: unknown) =>
    typeof candidate === "number" && Number.isFinite(candidate) && candidate >= 0
      ? Math.min(candidate, 10_000_000)
      : 0;
  const lineNumber = positiveInteger(raw.lineNumber);
  const column = positiveInteger(raw.column);
  return {
    lineNumber,
    column,
    selectionStartLineNumber: positiveInteger(raw.selectionStartLineNumber, lineNumber),
    selectionStartColumn: positiveInteger(raw.selectionStartColumn, column),
    positionLineNumber: positiveInteger(raw.positionLineNumber, lineNumber),
    positionColumn: positiveInteger(raw.positionColumn, column),
    scrollTop: boundedNumber(raw.scrollTop),
    scrollLeft: boundedNumber(raw.scrollLeft),
  };
}

export function queryTabsSnapshot(tabs: readonly QueryTab[], activeId: string): StoredQueryTabs {
  return {
    version: 2,
    tabs: tabs.map(({ id, name, sql, viewState }) => ({
      id,
      name,
      sql,
      viewState,
    })),
    activeId,
  };
}
