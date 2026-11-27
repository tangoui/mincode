/**
 * SQL 安全网编排：check → confirm → execute。
 *
 * 所有非 AI 写入口（查询窗口、破坏性操作、结构同步、导入、Redis 改键、e2e）经此服务
 * 拿到 `db_safety_check` 签发的一次性令牌后再执行。执行被拒绝时：
 * - `reconfirm`（实际行数越界或无法留底，已回滚）：用新 Check 再确认一次后重试，最多 3 次；
 * - `checkRequired` 且带 Check（只读执行被数据库判为写入）：必须弹出确认，绝不静默重试；
 * - `partial`：抛出 `SqlSafetyPartialError`（已执行条数与失败序号）；
 * - 其他结构化错误转成可读消息，普通错误原样抛出。
 */
import {
  parseSqlSafetyCheck,
  parseSqlSafetyError,
  type SqlSafetyCheck,
  type SqlSafetyConfirmNotice,
  type SqlSafetyError,
  type SqlSafetyOrigin,
} from "@/domain/database/sql-safety";
import { useVueI18n } from "@/i18n/messages";
import { confirmSqlSafety } from "@/services/sql-safety-confirm";
import { invokeBackend } from "@/services/desktop-bridge";

export type { SqlSafetyOrigin, SqlSafetyConfirmNotice };

/** 执行命令携带的令牌；只读文本 checkId 为空串（执行时不带令牌）。 */
export interface SqlSafetyToken {
  checkId: string;
  confirmText?: string;
}

/** `db_safety_check` 的参数；operation.kind ∈ destructive | import | schemaSync | redis | rows。 */
export interface SqlSafetyCheckArgs {
  sessionId: string;
  query?: string;
  operation?: object;
  database?: string;
  source: string;
}

export type SqlSafetyConfirm = (
  check: SqlSafetyCheck,
  origin: SqlSafetyOrigin,
  notice?: SqlSafetyConfirmNotice,
  summary?: readonly string[],
  signal?: AbortSignal,
) => Promise<{ confirmText?: string } | undefined>;

export interface RunWithSafetyOptions<T> {
  sessionId: string;
  query?: string;
  operation?: object;
  database?: string | null;
  /** 后端来源：query ai designer import grid e2e。 */
  source: string;
  origin: SqlSafetyOrigin;
  signal?: AbortSignal;
  execute: (token: SqlSafetyToken) => Promise<T>;
  check?: (args: SqlSafetyCheckArgs) => Promise<SqlSafetyCheck>;
  confirm?: SqlSafetyConfirm;
  /**
   * 先不带令牌执行（查询窗口）：只读文本没有任何额外往返；后端答复 checkRequired 时才检查
   * （或用携带的强制写入 Check）→ 确认 → 带令牌执行。基于 operation 的调用总是写入，保持先检查。
   * 不需要令牌的单行写入也用它：答复 reconfirm 时确认其 Check 后带令牌重交。
   */
  executeFirst?: boolean;
}

/** 同一次执行最多跟随的 reconfirm / 强制写入确认次数。 */
const MAX_RECONFIRMS = 3;

/** 部分语句已生效后失败：前 `applied` 条已提交，序号 `failedIndex`（从 0 开始）失败，后续未执行。 */
export class SqlSafetyPartialError extends Error {
  readonly applied: number;
  readonly failedIndex: number;
  readonly detail: string;
  /** 后端是否给出了失败序号（为 false 时 failedIndex 只是按 applied 推断的）。 */
  readonly failedIndexKnown: boolean;

  constructor(message: string, applied: number, failedIndex: number, detail: string, failedIndexKnown = true) {
    // name stays "Error": callers show String(error), and the message is already user-facing.
    super(message);
    this.applied = applied;
    this.failedIndex = failedIndex;
    this.detail = detail;
    this.failedIndexKnown = failedIndexKnown;
  }
}

/** 执行命令参数里的令牌字段；只读（无令牌）时为空对象。 */
export function sqlSafetyArgs(token: SqlSafetyToken): { checkId?: string; confirmText?: string } {
  if (!token.checkId) return {};
  return token.confirmText === undefined
    ? { checkId: token.checkId }
    : { checkId: token.checkId, confirmText: token.confirmText };
}

/** 调用 `db_safety_check` 并校验返回值。 */
export async function checkSqlSafety(args: SqlSafetyCheckArgs): Promise<SqlSafetyCheck> {
  return parseSqlSafetyCheck(await invokeBackend<unknown>("db_safety_check", { ...args }));
}

function checkArgs<T>(options: RunWithSafetyOptions<T>): SqlSafetyCheckArgs {
  const args: SqlSafetyCheckArgs = { sessionId: options.sessionId, source: options.source };
  if (options.query !== undefined) args.query = options.query;
  if (options.database) args.database = options.database;
  if (options.operation !== undefined) args.operation = options.operation;
  return args;
}

function tokenFor(check: SqlSafetyCheck, answer: { confirmText?: string }): SqlSafetyToken {
  return answer.confirmText === undefined
    ? { checkId: check.checkId }
    : { checkId: check.checkId, confirmText: answer.confirmText };
}

function failureError(failure: SqlSafetyError): Error {
  const { t } = useVueI18n();
  if (failure.code === "partial") {
    const applied = failure.applied ?? 0;
    const failedIndex = failure.failedIndex ?? applied;
    const detail = failure.message ?? "";
    const summary = t("database.safety.partial", { applied, index: failedIndex + 1 });
    return new SqlSafetyPartialError(
      detail ? `${summary}: ${detail}` : summary, applied, failedIndex, detail, failure.failedIndex !== undefined);
  }
  if (failure.code === "reconfirm") return new Error(t("database.safety.reconfirm"));
  return new Error(t(`database.safety.error.${failure.code}`));
}

/**
 * check → confirm → execute（executeFirst 时先不带令牌执行，见选项说明）。用户取消返回
 * undefined（不执行）；只读文本（risk none 且无令牌）不弹确认，直接以空令牌执行。
 */
export async function runWithSafety<T>(options: RunWithSafetyOptions<T>): Promise<T | undefined> {
  const check = options.check ?? checkSqlSafety;
  const confirmation = options.confirm ?? confirmSqlSafety;
  const confirm: SqlSafetyConfirm = async (check, origin, notice) => {
    options.signal?.throwIfAborted();
    const answer = await confirmation(check, origin, notice, undefined, options.signal);
    options.signal?.throwIfAborted();
    return answer;
  };
  options.signal?.throwIfAborted();
  let token: SqlSafetyToken;
  if (options.executeFirst) {
    try {
      return await options.execute({ checkId: "" });
    } catch (cause) {
      const failure = parseSqlSafetyError(cause);
      if (!failure) throw cause;
      if (failure.code === "reconfirm" && failure.check) {
        // 不带令牌的单行写入（db_update_cell 等）无法留底时回滚并带回 Check：确认后带令牌重交。
        const answer = await confirm(failure.check, options.origin, "reconfirm");
        if (!answer) return undefined;
        return runWithToken(options, confirm, tokenFor(failure.check, answer));
      }
      if (failure.code !== "checkRequired") throw failureError(failure);
      // 强制写入（只读执行被数据库判为写入）：用携带的 Check，必须弹出确认。
      const required = failure.check ?? await check(checkArgs(options));
      if (!required.checkId) throw failureError(failure);
      const answer = await confirm(required, options.origin, failure.check ? "forceWrite" : undefined);
      if (!answer) return undefined;
      token = tokenFor(required, answer);
    }
  } else {
    const initial = await check(checkArgs(options));
    if (!initial.checkId && initial.risk === "none") {
      token = { checkId: "" };
    } else {
      const answer = await confirm(initial, options.origin);
      if (!answer) return undefined;
      token = tokenFor(initial, answer);
    }
  }
  return runWithToken(options, confirm, token);
}

/** 带令牌执行，跟随 reconfirm / 强制写入确认（最多 MAX_RECONFIRMS 次）。 */
async function runWithToken<T>(options: RunWithSafetyOptions<T>, confirm: SqlSafetyConfirm, token: SqlSafetyToken): Promise<T | undefined> {
  for (let reconfirms = 0; ; reconfirms++) {
    options.signal?.throwIfAborted();
    try {
      return await options.execute(token);
    } catch (cause) {
      const failure = parseSqlSafetyError(cause);
      if (!failure) throw cause;
      const next = failure.code === "reconfirm" || failure.code === "checkRequired" ? failure.check : undefined;
      if (!next || reconfirms >= MAX_RECONFIRMS) throw failureError(failure);
      const answer = await confirm(next, options.origin, failure.code === "reconfirm" ? "reconfirm" : "forceWrite");
      if (!answer) return undefined;
      token = tokenFor(next, answer);
    }
  }
}
