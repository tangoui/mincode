import { markRaw, type ComputedRef, type Ref } from "vue";
import { ElMessage } from "element-plus";
import {
  type DatabaseConnection,
  type DatabaseQueryHistoryItem,
  type DatabaseQueryResult,
} from "@/domain/database/types";
import { estimateQueryResultBytes } from "@/domain/database/query-result-budget";
import { buildTableExplainSourceSql } from "@/domain/database/query-template";
import type { QueryTab } from "@/domain/database/query-tab-snapshot";
import { savePersistedValue } from "@/services/persistent-store";
import { notifyApp } from "@/services/notifications";
import { useVueI18n } from "@/i18n/messages";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

type WorkspaceObject = "database" | "query" | "table";

/**
 * SQL 查询执行流：运行/取消/历史记录/长查询通知/执行计划。
 * 查询结果归属发起它的 tab（通过 tabId 捕获），切 tab 不会串写状态。
 */
export function useQueryExecution(options: {
  queryTabs: Ref<QueryTab[]>;
  activeQueryTab: ComputedRef<QueryTab | undefined>;
  queryText: Readonly<Ref<string>>;
  activeObject: Ref<WorkspaceObject>;
  activeConnection: ComputedRef<DatabaseConnection | undefined>;
  selectedDatabase: ComputedRef<string | undefined>;
  selectedTable: ComputedRef<string | undefined>;
  queryHistory: Ref<DatabaseQueryHistoryItem[]>;
  desktopOnly: () => void;
  enforceQueryResultBudget: () => void;
  /** 执行计划查询完成后由视图展示对话框。 */
  onExecutionPlanLoaded: (result: DatabaseQueryResult) => void;
}) {
  const { t } = useVueI18n();
  const databaseStore = useDatabaseWorkspaceStore();

  function addHistory(item: DatabaseQueryHistoryItem) {
    options.queryHistory.value = [item, ...options.queryHistory.value].slice(0, 200);
    void savePersistedValue("app_db_query_history", options.queryHistory.value);
  }

  function notifyLongQueryResult(success: boolean, startedAt: number) {
    const elapsedMs = Date.now() - startedAt;
    if (elapsedMs < 3000) return;
    notifyApp({
      source: "db",
      level: success ? "success" : "error",
      title: t(success ? "notifications.database.queryCompleted" : "notifications.database.queryFailed"),
      message: t("notifications.database.queryElapsed", { seconds: (elapsedMs / 1000).toFixed(1) }),
    });
  }

  async function runQuery(sql?: string) {
    const tab = options.activeQueryTab.value;
    const query = (sql ?? options.queryText.value).trim();
    if (!tab || !query) return;
    if (!databaseStore.desktopRuntime) return options.desktopOnly();
    if (databaseStore.queryRunning) {
      ElMessage.warning(t("database.queryAlreadyRunning"));
      return;
    }

    // Query results belong to the tab that launched them. Capture the tab ID before
    // awaiting the backend so switching tabs does not overwrite another tab's state.
    const tabId = tab.id;
    // 历史记录同理：执行期间切换连接/数据库不能把记录归到新的目标下。
    const historyConnectionId = options.activeConnection.value?.id ?? "";
    const historyDatabase = databaseStore.selectedDatabase;
    // The result's own source: plugin services name this connection, not whatever the sidebar shows later.
    const resultSource = historyConnectionId ? { connectionId: historyConnectionId, database: historyDatabase } : undefined;
    const startedAt = Date.now();
    const previousStatus = tab.status;
    tab.status = "running";
    tab.error = undefined;
    options.activeObject.value = "query";
    try {
      // 写语句由 SQL 安全网检查并确认（替代原先按关键字的确认框）。
      const result = await databaseStore.executeQuery(query, undefined, {
        safety: { origin: "user", source: "query" },
        onProgress(partialResult) {
          const runningTab = options.queryTabs.value.find((item) => item.id === tabId);
          if (!runningTab || runningTab.status !== "running") return;
          runningTab.result = markRaw(partialResult);
          runningTab.resultSource = resultSource;
          runningTab.resultEstimatedBytes = undefined;
          runningTab.resultReleased = false;
        },
      });
      if (!result) {
        // 用户取消了安全确认：未执行，不记历史。
        const cancelledTab = options.queryTabs.value.find((item) => item.id === tabId);
        if (cancelledTab?.status === "running") cancelledTab.status = previousStatus;
        return;
      }
      const completedTab = options.queryTabs.value.find((item) => item.id === tabId);
      if (completedTab) {
        completedTab.result = markRaw(result);
        completedTab.resultSource = resultSource;
        completedTab.resultEstimatedBytes = estimateQueryResultBytes(result);
        completedTab.resultLastAccessedAt = Date.now();
        completedTab.resultReleased = false;
        completedTab.status = "success";
        completedTab.lastExecutedAt = Date.now();
        options.enforceQueryResultBudget();
      }
      addHistory({
        id: crypto.randomUUID(),
        connectionId: historyConnectionId,
        database: historyDatabase,
        query,
        startedAt,
        executionMs: result.executionMs,
        rowCount: result.rowCount,
        success: true,
      });
      if (result.affectedRows != null) {
        ElMessage.success(t("database.statementExecuted", { count: result.affectedRows }));
      }
      if (result.notice) ElMessage.warning(result.notice);
      notifyLongQueryResult(true, startedAt);
    } catch (cause) {
      const message = String(cause);
      const completedTab = options.queryTabs.value.find((item) => item.id === tabId);
      if (completedTab) {
        completedTab.error = message;
        completedTab.status = "error";
        completedTab.lastExecutedAt = Date.now();
      }
      addHistory({
        id: crypto.randomUUID(),
        connectionId: historyConnectionId,
        database: historyDatabase,
        query,
        startedAt,
        executionMs: Date.now() - startedAt,
        rowCount: 0,
        success: false,
        error: message,
      });
      notifyLongQueryResult(false, startedAt);
      ElMessage.error(message);
    }
  }

  async function cancelQuery() {
    try {
      if (await databaseStore.cancelQuery()) ElMessage.info(t("database.cancelRequested"));
    } catch (cause) {
      ElMessage.error(String(cause));
    }
  }

  async function showExecutionPlan() {
    if (!databaseStore.desktopRuntime) return options.desktopOnly();
    const editorSql = options.queryText.value.trim().replace(/^EXPLAIN\s+/i, "");
    // 表数据视图没有编辑器 SQL 时，回退用当前表的 SELECT 作为来源查询，
    // 避免点击后静默无响应。
    const sourceSql =
      editorSql ||
      buildTableExplainSourceSql({
        dbType: options.activeConnection.value?.dbType ?? "mysql",
        database: options.selectedDatabase.value ?? "",
        table: options.selectedTable.value ?? "",
      });
    if (!sourceSql) return ElMessage.warning(t("database.executionPlanNoSource"));
    if (databaseStore.queryRunning) return ElMessage.warning(t("database.queryAlreadyRunning"));
    try {
      const result = await databaseStore.executeQuery(`EXPLAIN ${sourceSql}`);
      if (result) options.onExecutionPlanLoaded(result);
    } catch (cause) {
      ElMessage.error(String(cause));
    }
  }

  return {
    runQuery,
    cancelQuery,
    showExecutionPlan,
    addHistory,
    notifyLongQueryResult,
  };
}
