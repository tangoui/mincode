/**
 * SQL 安全网操作记录与撤回的原生调用：列表、详情（前 20 行留底样例）、撤回预览、撤回。
 * 返回值在此校验后再交给界面。（删除连接时的 db_safety_purge_connection 在 database-workspace store。）
 */
import {
  parseSqlSafetyHistoryDetail,
  parseSqlSafetyHistoryList,
  parseSqlSafetyUndoPreview,
  parseSqlSafetyUndoResult,
  type SqlSafetyHistoryDetail,
  type SqlSafetyRecord,
  type SqlSafetyUndoMode,
  type SqlSafetyUndoPreview,
  type SqlSafetyUndoResult,
} from "@/domain/database/sql-safety";
import { invokeBackend } from "@/services/desktop-bridge";

export async function listSqlSafetyHistory(connectionId: string, limit?: number): Promise<SqlSafetyRecord[]> {
  const args: Record<string, unknown> = { connectionId };
  if (limit !== undefined) args.limit = limit;
  return parseSqlSafetyHistoryList(await invokeBackend<unknown>("db_safety_history_list", args));
}

export async function getSqlSafetyHistory(id: string): Promise<SqlSafetyHistoryDetail> {
  return parseSqlSafetyHistoryDetail(await invokeBackend<unknown>("db_safety_history_get", { id }));
}

export async function previewSqlSafetyUndo(sessionId: string, id: string): Promise<SqlSafetyUndoPreview> {
  return parseSqlSafetyUndoPreview(await invokeBackend<unknown>("db_safety_undo_preview", { sessionId, id }));
}

export interface SqlSafetyUndoArgs {
  sessionId: string;
  id: string;
  mode: SqlSafetyUndoMode;
  /** 用户看到的那次预览的令牌；数据在此后变化时后端返回 previewStale。 */
  previewToken: string;
  confirmText?: string;
}

export async function undoSqlSafety(args: SqlSafetyUndoArgs): Promise<SqlSafetyUndoResult> {
  return parseSqlSafetyUndoResult(await invokeBackend<unknown>("db_safety_undo", { ...args }));
}

/** 读取高风险行数阈值；读取失败返回 undefined（由后端的 confirmMismatch 兜底要求输入表名）。 */
export async function sqlSafetyHighRiskRows(): Promise<number | undefined> {
  try {
    const settings = await invokeBackend<{ highRiskRows?: unknown }>("db_safety_settings_get", {});
    return typeof settings?.highRiskRows === "number" ? settings.highRiskRows : undefined;
  } catch {
    return undefined;
  }
}

