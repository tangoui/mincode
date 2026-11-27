import type { QueryTab } from "@/domain/database/query-tab-snapshot";
import type { DatabaseQueryResult } from "@/domain/database/types";
import { useVueI18n } from "@/i18n/messages";
import { sendToAi } from "@/services/ai-handoff";
import { sanitizeAiResult } from "@/services/ai-tools/shared";

const { t } = useVueI18n();
const MAX_RESULT_ROWS = 100;

function queryContext(execution: NonNullable<QueryTab["lastExecution"]>) {
  return [
    execution.dbType ? t("ai.handoff.databaseType", { type: execution.dbType }) : "",
    `SQL\n${execution.sql}`,
  ].filter(Boolean).join("\n\n");
}

export function handoffDatabaseError(tab: QueryTab | undefined) {
  if (!tab?.lastExecution || !tab.error) return;
  return sendToAi({
    source: "database",
    title: t("ai.handoff.databaseError"),
    prompt: t("ai.handoff.databaseErrorPrompt"),
    content: `${queryContext(tab.lastExecution)}\n\n${t("ai.handoff.errorLabel")}\n${tab.error}`,
  });
}

export function handoffDatabaseResult(tab: QueryTab | undefined, result: DatabaseQueryResult | undefined) {
  if (!tab?.lastExecution || tab.status !== "success" || !result) return;
  const rows = result.rows.slice(0, MAX_RESULT_ROWS);
  const sample = sanitizeAiResult({ columns: result.columns, rows });
  const summary = t("ai.handoff.databaseSample", { count: rows.length, total: result.rowCount });
  return sendToAi({
    source: "database",
    title: t("ai.handoff.databaseResult"),
    prompt: t("ai.handoff.databaseResultPrompt"),
    content: `${queryContext(tab.lastExecution)}\n\n${summary}\n${JSON.stringify(sample, null, 2)}`,
  });
}
