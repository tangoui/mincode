/**
 * AI 数据库 / Redis 工具接入 SQL 安全网。
 *
 * - 检查经工具同一通道（dependencies.invoke）调用 `db_safety_check`，来源 `ai`；
 * - 低风险沿用工具自身的 AI 审批（这些工具都是 dangerous），不再弹安全网确认；高风险以及带原因
 *   （reconfirm / forceWrite）的确认在任何审批模式（含全权限）都弹出宿主确认框，由用户输入确认
 *   目标，模型无法代答；用户取消时工具返回错误；
 * - 单行工具（insert_row / update_row / delete_row）按 会话 + 对话 累计行数（执行上下文没有 AI
 *   轮次 id）。计数不因时间流逝清零，只在用户确认后重新计数，放慢调用节奏无法绕过阈值；累计达到
 *   阈值（`db_safety_settings_get` 的 highRiskRows，缓存 30 秒，设置保存后立即失效）后，下一次调用
 *   先弹高风险确认（确认目标为表名，提示 aiRowThreshold 说明这是对话内的累计行数）。
 *
 * runWithSafety / 确认框由宿主注入（ai-native-tools），本模块不直接依赖 UI。
 * 错误消息与日志不含语句文本、键或值。
 */
import { throwIfAborted } from "@/domain/async/abort";
import { parseSqlSafetyCheck, type SqlSafetyCheck } from "@/domain/database/sql-safety";
import type {
  RunWithSafetyOptions,
  SqlSafetyConfirm,
  SqlSafetyConfirmNotice,
  SqlSafetyToken,
} from "@/services/sql-safety-service";
import { sqlSafetySettingsVersion } from "@/services/sql-safety-settings-cache";
import type { AiToolDependencies } from "./shared";

/** 宿主提供的安全网入口：sql-safety-service 的 runWithSafety / sqlSafetyArgs 与 confirmSqlSafety。 */
export interface AiSqlSafety {
  runWithSafety<T>(options: RunWithSafetyOptions<T>): Promise<T | undefined>;
  sqlSafetyArgs(token: SqlSafetyToken): { checkId?: string; confirmText?: string };
  confirm: SqlSafetyConfirm;
  /** 测试用时钟；默认 Date.now。 */
  now?(): number;
}

export const AI_DECLINED_HIGH_RISK = "User declined the high-risk statement";
const AI_DECLINED: Record<SqlSafetyConfirmNotice, string> = {
  reconfirm: "User declined to rerun the statement after it was rolled back for reconfirmation",
  forceWrite: "User declined to run the statement as a write after the database refused it as read-only",
  aiRowThreshold: AI_DECLINED_HIGH_RISK,
};

const SETTINGS_TTL_MS = 30_000;
/** 设置读取失败时按后端默认值（安全网开启、1000 行）判定。 */
const FALLBACK_SETTINGS = { enabled: true, highRiskRows: 1000 };

type RowWrite = "insert" | "update" | "delete";
const ROW_STATEMENT: Record<RowWrite, (table: string) => string> = {
  insert: (table) => `INSERT INTO ${table}`,
  update: (table) => `UPDATE ${table}`,
  delete: (table) => `DELETE FROM ${table}`,
};

export interface AiRowWrite {
  sessionId: string;
  database: string;
  table: string;
  write: RowWrite;
  conversationId?: string;
  externalAgent?: boolean;
  signal?: AbortSignal;
}

export type AiSafetyRunOptions<T> = Omit<RunWithSafetyOptions<T>, "source" | "origin" | "check" | "confirm">;

export interface AiSqlSafetyRunner {
  /** 经安全网执行；用户取消时抛出错误。 */
  run<T>(options: AiSafetyRunOptions<T>): Promise<T>;
  /** 执行命令参数里的令牌字段。 */
  tokenArgs(token: SqlSafetyToken): { checkId?: string; confirmText?: string };
  /**
   * 一次单行工具调用（计一行）：超过累计阈值时先确认，再运行 body。body 里的每条单行命令用
   * `run({ ..., executeFirst: true })`：不带令牌执行，答复 reconfirm 时确认后带令牌重交。
   */
  runRowWrite<T>(row: AiRowWrite, body: () => Promise<T>): Promise<T>;
}

export function createAiSqlSafety(
  invoke: AiToolDependencies["invoke"],
  sqlSafety: AiSqlSafety,
): AiSqlSafetyRunner {
  const now = () => sqlSafety.now?.() ?? Date.now();
  const rowCounts = new Map<string, { rows: number }>();
  let cachedSettings: { value: typeof FALLBACK_SETTINGS; at: number; version: number } | undefined;

  async function rowSettings() {
    const version = sqlSafetySettingsVersion();
    if (cachedSettings && cachedSettings.version === version && now() - cachedSettings.at < SETTINGS_TTL_MS) {
      return cachedSettings.value;
    }
    try {
      const value = await invoke<{ enabled?: unknown; highRiskRows?: unknown }>("db_safety_settings_get", {});
      const highRiskRows = value?.highRiskRows;
      const settings = {
        enabled: value?.enabled !== false,
        highRiskRows: typeof highRiskRows === "number" && Number.isSafeInteger(highRiskRows) && highRiskRows > 0
          ? highRiskRows
          : FALLBACK_SETTINGS.highRiskRows,
      };
      cachedSettings = { value: settings, at: now(), version };
      return settings;
    } catch {
      return FALLBACK_SETTINGS;
    }
  }

  async function run<T>(options: AiSafetyRunOptions<T>): Promise<T> {
    const { signal, ...request } = options;
    throwIfAborted(signal);
    let declined: string | undefined;
    const confirm: SqlSafetyConfirm = async (check, origin, notice) => {
      throwIfAborted(signal);
      if (!notice && check.risk !== "high") return {};
      const answer = await sqlSafety.confirm(check, origin, notice, undefined, signal);
      throwIfAborted(signal);
      if (!answer) declined = notice ? AI_DECLINED[notice] : AI_DECLINED_HIGH_RISK;
      return answer;
    };
    const result = await sqlSafety.runWithSafety<T>({
      ...request,
      source: "ai",
      origin: "ai",
      execute: (token) => {
        throwIfAborted(signal);
        return request.execute(token);
      },
      check: async (args) => {
        throwIfAborted(signal);
        return parseSqlSafetyCheck(await invoke<unknown>("db_safety_check", { ...args }, signal ? { signal } : undefined));
      },
      confirm,
    });
    if (declined) throw new Error(declined);
    return result as T;
  }

  /**
   * 累计行数的高风险确认；单行写入本身不经后端检查，确认目标同样是表名。estimate.rows 是对话内
   * 的累计行数（确认框按 aiRowThreshold 提示显示，不当作单条语句的精确行数，也不显示撤回支持）。
   */
  function rowThresholdCheck(row: AiRowWrite, rows: number): SqlSafetyCheck {
    return {
      checkId: "",
      sessionId: row.sessionId,
      database: row.database,
      statements: [{
        index: 0,
        text: ROW_STATEMENT[row.write](row.table),
        kind: "rows",
        table: { schema: "", name: row.table },
        estimate: { rows, exact: true, source: "count" },
        risk: "high",
        reasons: ["overThreshold"],
        undoable: false,
      }],
      risk: "high",
      confirmTarget: row.table,
      undoable: false,
      expiresAt: "",
    };
  }

  async function runRowWrite<T>(row: AiRowWrite, body: () => Promise<T>): Promise<T> {
    const settings = await rowSettings();
    throwIfAborted(row.signal);
    const key = `${row.sessionId}\u0000${row.externalAgent ? "external-agent" : row.conversationId ?? ""}`;
    let entry = rowCounts.get(key);
    if (!entry) {
      entry = { rows: 0 };
      rowCounts.set(key, entry);
    }
    if (settings.enabled && entry.rows >= settings.highRiskRows) {
      const answer = await sqlSafety.confirm(rowThresholdCheck(row, entry.rows + 1), "ai", "aiRowThreshold", undefined, row.signal);
      throwIfAborted(row.signal);
      if (!answer) throw new Error(AI_DECLINED_HIGH_RISK);
      entry.rows = 0;
    }
    entry.rows += 1;
    try {
      return await body();
    } catch (cause) {
      entry.rows = Math.max(0, entry.rows - 1);
      throw cause;
    }
  }

  return { run, tokenArgs: (token) => sqlSafety.sqlSafetyArgs(token), runRowWrite };
}
