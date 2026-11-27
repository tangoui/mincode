/**
 * SQL 安全网确认框（Element Plus MessageBox + SqlSafetyConfirmContent）。
 *
 * - none：直接通过；
 * - low：origin 为 preconfirmed（调用方已有专用确认界面）或 ai（AI 工具已经过工具审批）时直接
 *   通过，否则弹一次确认；
 * - high：详细列表 + 输入确认目标（表名 / 过程名 / 库名，或字面量 CONFIRM），比对规则与
 *   Go `ConfirmTargetMatches` 一致，任何 origin 都必须弹出；
 * - 带 notice（reconfirm / forceWrite）时无论风险与 origin 都弹出。
 * 安全网关闭时后端仍签发令牌（全部为低风险、不可撤回），此时显示通用确认文案，不显示留底信息。
 */
import { h } from "vue";
import { ElMessageBox, type ElMessageBoxOptions } from "element-plus";
import SqlSafetyConfirmContent from "@/components/database/safety/SqlSafetyConfirmContent.vue";
import {
  confirmTargetMatches,
  type SqlSafetyCheck,
  type SqlSafetyConfirmNotice,
  type SqlSafetyOrigin,
} from "@/domain/database/sql-safety";
import { useVueI18n } from "@/i18n/messages";
import { throwIfAborted } from "@/domain/async/abort";
import { invokeBackend } from "@/services/desktop-bridge";

/** 安全网是否开启；读取失败时按开启处理（只影响说明文案，不影响是否确认）。 */
async function sqlSafetyEnabled(): Promise<boolean> {
  try {
    const settings = await invokeBackend<{ enabled?: unknown }>("db_safety_settings_get", {});
    return settings?.enabled !== false;
  } catch {
    return true;
  }
}

function cancelled(cause: unknown) {
  return cause === "cancel" || cause === "close";
}

/**
 * 按风险与来源确认；返回 undefined 表示用户取消，高风险时返回用户输入的 confirmText。
 * summary：调用方给出的摘要行（如表格提交的“修改 N 行”），显示在语句列表之前。
 */
export async function confirmSqlSafety(
  check: SqlSafetyCheck,
  origin: SqlSafetyOrigin,
  notice?: SqlSafetyConfirmNotice,
  summary?: readonly string[],
  signal?: AbortSignal,
): Promise<{ confirmText?: string } | undefined> {
  throwIfAborted(signal);
  if (!notice && check.risk === "none") return {};
  if (!notice && check.risk === "low" && (origin === "preconfirmed" || origin === "ai")) return {};
  const { t } = useVueI18n();
  const enabled = await sqlSafetyEnabled();
  throwIfAborted(signal);
  let cancelBox: (() => void) | undefined;
  const onAbort = () => cancelBox?.();
  const message: ElMessageBoxOptions["message"] = ({ cancel }) => {
    cancelBox = cancel;
    if (signal?.aborted) queueMicrotask(cancel);
    return h(SqlSafetyConfirmContent, { check, enabled, notice, summary });
  };
  signal?.addEventListener("abort", onAbort, { once: true });
  const common = {
    confirmButtonText: t("database.safety.execute"),
    cancelButtonText: t("common.cancel"),
    distinguishCancelAndClose: true,
    closeOnClickModal: false,
    customClass: "app-sql-safety-box",
  };
  try {
    if (check.risk === "high") {
      const target = check.confirmTarget;
      const { value } = await ElMessageBox.prompt(message, t("database.safety.highRiskTitle"), {
        ...common,
        inputPlaceholder: t("database.safety.typePlaceholder", { name: target }),
        inputValidator: (input: string) => confirmTargetMatches(target, input) || t("database.safety.mismatch"),
      });
      return { confirmText: value };
    }
    await ElMessageBox.confirm(message, t(enabled ? "database.safety.lowRiskTitle" : "database.safety.title"), {
      ...common,
    });
    return {};
  } catch (cause) {
    throwIfAborted(signal);
    if (cancelled(cause)) return undefined;
    throw cause;
  } finally {
    signal?.removeEventListener("abort", onAbort);
  }
}
