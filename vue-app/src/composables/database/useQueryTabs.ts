import { computed, onScopeDispose, ref, watch } from "vue";
import {
  createQueryTab,
  queryTabSqlIsModified,
  queryTabsSnapshot,
  restoreQueryTabs,
  type QueryTab,
  type StoredQueryTabs,
} from "@/domain/database/query-tab-snapshot";
import { selectQueryResultsToRelease } from "@/domain/database/query-result-budget";
import { useWorkspaceScopedPersistence } from "@/composables/use-workspace-state-scope";
import type { ProjectEditorViewState } from "@/domain/projects/types";

const QUERY_TABS_STORE_KEY = "app_db_query_tabs_v1";
const QUERY_TABS_PERSIST_DELAY_MS = 300;

/**
 * SQL 查询 tab 的状态与生命周期：创建/激活/关闭、结果内存预算、
 * workspace 级持久化与 Monaco 视图状态恢复。
 * 视图相关的动作（切换 activeObject、最后一个 tab 关闭后的回退）
 * 通过回调注入，避免 composable 反向依赖视图状态。
 */
export function useQueryTabs(options: {
  defaultNameFor: (number: number) => string;
  /** 新建查询时预填的 SQL（通常来自当前表的查询模板）。 */
  templateFor: () => string;
  /** 查询 tab 被打开或激活时通知视图切换 activeObject。 */
  onQueryActivated: () => void;
  /** 最后一个查询 tab 关闭后的回退（切换到表/库视图）。 */
  onTabsEmpty: () => void;
}) {
  // A table workspace opens directly on its data tab. SQL editors are created only
  // when the user explicitly requests a new query.
  const queryTabs = ref<QueryTab[]>([]);
  const activeQueryTabId = ref("");
  const activeQueryTab = computed(
    () => queryTabs.value.find((tab) => tab.id === activeQueryTabId.value) ?? queryTabs.value[0]
  );
  const queryText = computed({
    get: () => activeQueryTab.value?.sql ?? "",
    set: (value: string) => {
      if (activeQueryTab.value) activeQueryTab.value.sql = value;
    },
  });
  let queryTabsHydrated = false;
  let persistTimer: ReturnType<typeof setTimeout> | undefined;
  // 记录新建时预填的 SQL，关闭时与之比较以判断用户是否编辑过；恢复的标签没有记录。
  const initialQuerySql = new Map<string, string>();

  const queryTabsPersistence = useWorkspaceScopedPersistence<StoredQueryTabs>(
    QUERY_TABS_STORE_KEY
  );

  function enforceQueryResultBudget() {
    const releaseIds = selectQueryResultsToRelease(
      queryTabs.value.map((tab) => ({
        id: tab.id,
        estimatedBytes: tab.resultEstimatedBytes ?? 0,
        lastAccessedAt: tab.resultLastAccessedAt ?? tab.lastExecutedAt ?? 0,
        hasResult: Boolean(tab.result),
      })),
      activeQueryTabId.value
    );
    for (const id of releaseIds) {
      const tab = queryTabs.value.find((item) => item.id === id);
      if (!tab) continue;
      tab.result = undefined;
      tab.resultEstimatedBytes = undefined;
      tab.resultReleased = true;
    }
  }

  function newQuery(sql?: string, name?: string) {
    const tab = createQueryTab(sql ?? options.templateFor(), options.defaultNameFor, name);
    initialQuerySql.set(tab.id, tab.sql);
    queryTabs.value.push(tab);
    activeQueryTabId.value = tab.id;
    options.onQueryActivated();
  }

  /** 返回关闭时会丢失用户编辑 SQL 的标签 id。 */
  function queryTabsWithUnsavedSql(tabIds: readonly string[]) {
    const template = options.templateFor();
    return tabIds.filter((tabId) => {
      const tab = queryTabs.value.find((item) => item.id === tabId);
      return Boolean(tab && queryTabSqlIsModified(tab.sql, [initialQuerySql.get(tabId), template]));
    });
  }

  function closeQueryTab(tabId: string) {
    const index = queryTabs.value.findIndex((tab) => tab.id === tabId);
    if (index < 0) return;
    const wasActive = activeQueryTabId.value === tabId;
    queryTabs.value.splice(index, 1);
    initialQuerySql.delete(tabId);
    if (wasActive) {
      activeQueryTabId.value = queryTabs.value[index]?.id ?? queryTabs.value[index - 1]?.id ?? "";
    }
    if (!queryTabs.value.length) options.onTabsEmpty();
  }

  function persistQueryTabs() {
    if (persistTimer) {
      clearTimeout(persistTimer);
      persistTimer = undefined;
    }
    return queryTabsPersistence.save(queryTabsSnapshot(queryTabs.value, activeQueryTabId.value));
  }

  function loadQueryTabsSnapshot() {
    return queryTabsPersistence.load();
  }

  /** 从持久化快照恢复 tab 列表，并开始后续变更的自动持久化。 */
  function hydrateQueryTabs(stored: StoredQueryTabs | null | undefined) {
    const restoredTabs = restoreQueryTabs(stored?.tabs, options.defaultNameFor);
    if (restoredTabs.length) {
      queryTabs.value = restoredTabs;
      activeQueryTabId.value = restoredTabs.some((tab) => tab.id === stored?.activeId)
        ? stored!.activeId
        : restoredTabs[0]!.id;
    }
    queryTabsHydrated = true;
  }

  function setQueryEditorViewState(path: string, state: ProjectEditorViewState) {
    const separatorIndex = path.lastIndexOf(".");
    const tabId = separatorIndex > 0 ? path.slice(0, separatorIndex) : path;
    const tab = queryTabs.value.find((candidate) => candidate.id === tabId);
    if (tab) tab.viewState = state;
  }

  // Only the persisted fields are tracked: a deep watch would walk every cell of every tab's
  // result on each keystroke and each streamed frame.
  watch(
    () => [activeQueryTabId.value, ...queryTabs.value.flatMap((tab) => [tab.id, tab.name, tab.sql, tab.viewState])],
    () => {
      if (!queryTabsHydrated) return;
      if (persistTimer) clearTimeout(persistTimer);
      persistTimer = setTimeout(() => void persistQueryTabs(), QUERY_TABS_PERSIST_DELAY_MS);
    }
  );
  onScopeDispose(() => {
    if (persistTimer) void persistQueryTabs();
  });

  return {
    queryTabs,
    activeQueryTabId,
    activeQueryTab,
    queryText,
    newQuery,
    closeQueryTab,
    queryTabsWithUnsavedSql,
    enforceQueryResultBudget,
    persistQueryTabs,
    loadQueryTabsSnapshot,
    hydrateQueryTabs,
    setQueryEditorViewState,
  };
}
