import { computed, ref, watch, type ComputedRef, type Ref } from "vue";
import { ElMessage } from "element-plus";
import type { DatabaseConnection } from "@/domain/database/types";
import {
  buildMongoTableFilterQuery,
  buildMongoTableSearchQuery,
  buildTableFilterCountSql,
  buildTableFilterSql,
  buildTableRowCountSql,
  buildTableSearchCountSql,
  buildTableSearchSql,
  lastPageFromTotal,
  supportsSqlTableSearch,
  tableQueryPageSize,
  type TableFilterModel,
} from "@/domain/database/table-search";
import { tableSearchColumns } from "@/domain/database/grid-utils";
import { useVueI18n } from "@/i18n/messages";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import type { TableTab } from "./useGridState";
import type { InlineCellEdit } from "./useRowMutations";

type WorkspaceObject = "database" | "query" | "table";

export type TableServerSearchState =
  | { kind: "search"; keyword: string; columns: string[] }
  | { kind: "filter"; filters: TableFilterModel };

/**
 * 表格分页与服务端搜索：整表搜索/类型化筛选走 SQL/Mongo 查询，
 * 翻页/跳页/页大小切换会优先复用服务端搜索状态；
 * 向量库（qdrant/pinecone）使用游标分页 token 与 pinecone namespace。
 */
export function useTablePagination(options: {
  activeObject: Ref<WorkspaceObject>;
  activeConnection: ComputedRef<DatabaseConnection | undefined>;
  activeTableTab: ComputedRef<TableTab | undefined>;
  selectedTable: ComputedRef<string | undefined>;
  resultColumns: ComputedRef<string[]>;
  resultRows: ComputedRef<unknown[][]>;
  canSearchEntireTable: ComputedRef<boolean>;
  gridFilterText: Ref<string>;
  inlineCellEdit: Ref<InlineCellEdit | undefined>;
  connectionIsConnected: (connection: DatabaseConnection) => boolean;
  connectionIsActive: (connection: DatabaseConnection) => boolean;
  desktopOnly: () => void;
}) {
  const { t } = useVueI18n();
  const databaseStore = useDatabaseWorkspaceStore();

  const targetPage = ref(1);
  const vectorPageTokens = ref<Record<string, string[]>>({});
  const pineconeNamespaces = ref<Record<string, string>>({});
  const pineconeNamespaceDraft = ref("");
  const tableServerSearches = ref<Record<string, TableServerSearchState>>({});
  // 分页状态在 store 里是全局单值，切换标签会被下一张表覆盖，因此按标签留档以便切回时还原。
  const tablePagePositions = ref<Record<string, number>>({});
  // 尾页页码的 COUNT 结果按“标签|页大小|搜索签名”缓存，避免重复点尾页时反复全表计数。
  const tableLastPages = ref<Record<string, number>>({});
  const countingLastPage = ref(false);

  function rememberTablePage(tabId: string, page: number) {
    tablePagePositions.value = { ...tablePagePositions.value, [tabId]: Math.max(1, page) };
  }

  function forgetTablePage(tabId: string) {
    const positions = { ...tablePagePositions.value };
    delete positions[tabId];
    tablePagePositions.value = positions;
    forgetTableLastPages(tabId);
  }

  /** 行数可能变化（刷新、增删行、导入、换筛选、关标签）时丢弃该标签缓存的尾页页码。 */
  function forgetTableLastPages(tabId: string) {
    const prefix = `${tabId}|`;
    if (!Object.keys(tableLastPages.value).some((key) => key.startsWith(prefix))) return;
    tableLastPages.value = Object.fromEntries(
      Object.entries(tableLastPages.value).filter(([key]) => !key.startsWith(prefix))
    );
  }

  const supportsCursorTablePreview = computed(() =>
    ["qdrant", "pinecone"].includes(options.activeConnection.value?.dbType ?? "")
  );
  const supportsOffsetTablePreview = computed(() => !supportsCursorTablePreview.value);
  const canNextPage = computed(() => {
    if (options.activeObject.value !== "table" || !databaseStore.activeSessionId) return false;
    if (supportsCursorTablePreview.value) return Boolean(databaseStore.result?.nextPageToken);
    return options.resultRows.value.length >= databaseStore.pageSize;
  });
  // 尾页要靠 COUNT(*)；游标库与 mongodb/elasticsearch 生成不了这条 SQL。
  const canLastPage = computed(() => {
    if (options.activeObject.value !== "table" || !databaseStore.activeSessionId) return false;
    const dbType = options.activeConnection.value?.dbType;
    if (!dbType || !supportsSqlTableSearch(dbType)) return false;
    // 当页不满一页说明已经到底，末页按钮不再可点。
    if (!canNextPage.value) return false;
    // 缓存过末页页码且已到达时也不再发 COUNT。
    const known = activeKnownLastPage();
    return known === undefined || databaseStore.page < known;
  });

  function lastPageKeyFor(tab: TableTab, pageSize: number) {
    const search = tableServerSearches.value[tab.id];
    return `${tab.id}|${pageSize}|${search ? JSON.stringify(search) : ""}`;
  }

  function activeKnownLastPage(): number | undefined {
    const tab = options.activeTableTab.value;
    if (!tab) return undefined;
    const search = tableServerSearches.value[tab.id];
    const pageSize = search ? tableQueryPageSize(databaseStore.pageSize) : databaseStore.pageSize;
    return tableLastPages.value[lastPageKeyFor(tab, pageSize)];
  }

  function vectorTablePageKey(tab: TableTab, namespace = "") {
    return `${tab.id}:${namespace}`;
  }

  function activePineconeNamespace(tab: TableTab) {
    return pineconeNamespaces.value[tab.id] ?? "";
  }

  function activeVectorMutationOptions() {
    const tab = options.activeTableTab.value;
    return {
      namespace:
        options.activeConnection.value?.dbType === "pinecone" && tab
          ? activePineconeNamespace(tab) || undefined
          : undefined,
    };
  }

  function removeTableServerSearch(tabId: string) {
    const searches = { ...tableServerSearches.value };
    delete searches[tabId];
    tableServerSearches.value = searches;
  }

  async function openTablePage(tab: TableTab, page: number, pageSize: number) {
    const connection = databaseStore.connections.find((item) => item.id === tab.connectionId);
    const cursorPagination = connection?.dbType === "qdrant" || connection?.dbType === "pinecone";
    const namespace = connection?.dbType === "pinecone" ? activePineconeNamespace(tab) : "";
    if (!cursorPagination) {
      const result = await databaseStore.openTable(tab.database, tab.table, page, pageSize);
      rememberTablePage(tab.id, page);
      return result;
    }

    const key = vectorTablePageKey(tab, namespace);
    const tokens = vectorPageTokens.value[key] ?? [""];
    const pageToken = tokens[page - 1];
    if (page > 1 && !pageToken) throw new Error(t("database.vectorPageTokenUnavailable"));
    const result = await databaseStore.openTable(tab.database, tab.table, page, pageSize, {
      pageToken: pageToken || undefined,
      namespace: namespace || undefined,
    });
    const nextTokens = tokens.slice(0, page);
    if (result?.nextPageToken) nextTokens[page] = result.nextPageToken;
    vectorPageTokens.value = { ...vectorPageTokens.value, [key]: nextTokens };
    rememberTablePage(tab.id, page);
    return result;
  }

  async function loadTableSearchPage(
    tab: TableTab,
    search: TableServerSearchState,
    requestedPage: number,
    requestedPageSize: number
  ) {
    const connection = databaseStore.connections.find((item) => item.id === tab.connectionId);
    if (!connection || !options.connectionIsConnected(connection)) {
      throw new Error(t("database.connectionNotConnected"));
    }
    if (!options.connectionIsActive(connection)) databaseStore.activateConnection(connection.id);

    const page = Math.max(1, requestedPage);
    // 与 SQL 构造器对 LIMIT 的上限保持一致，否则 OFFSET 按未夹的页大小计算会跳过行。
    const pageSize = tableQueryPageSize(Math.max(10, requestedPageSize));
    const offset = (page - 1) * pageSize;
    const query =
      search.kind === "filter"
        ? connection.dbType === "mongodb"
          ? buildMongoTableFilterQuery(tab.database, tab.table, search.filters, pageSize, offset)
          : buildTableFilterSql(connection.dbType, tab.database, tab.table, search.filters, pageSize, offset)
        : connection.dbType === "mongodb"
          ? buildMongoTableSearchQuery(
              tab.database,
              tab.table,
              search.columns,
              search.keyword,
              pageSize,
              offset
            )
          : buildTableSearchSql(
              connection.dbType,
              tab.database,
              tab.table,
              search.columns,
              search.keyword,
              pageSize,
              offset
            );
    await databaseStore.executeTableQuery(query, tab.database);
    databaseStore.page = page;
    databaseStore.pageSize = pageSize;
    rememberTablePage(tab.id, page);
  }

  async function searchAllTableRows() {
    if (options.activeObject.value !== "table" || !options.activeTableTab.value) return;
    if (!databaseStore.desktopRuntime) return options.desktopOnly();
    if (!options.canSearchEntireTable.value) {
      ElMessage.info(t("database.tableSearchUnavailable"));
      return;
    }

    const keyword = options.gridFilterText.value.trim();
    if (!keyword) {
      await clearTableServerSearch();
      return;
    }

    const tab = options.activeTableTab.value;
    const connection = databaseStore.connections.find((item) => item.id === tab.connectionId);
    const columns = tableSearchColumns(
      connection?.dbType,
      databaseStore.tableColumns,
      options.resultColumns.value
    );
    if (!columns.length) {
      ElMessage.warning(t("database.tableSearchColumnsUnavailable"));
      return;
    }

    const previousSearch = tableServerSearches.value[tab.id];
    const search: TableServerSearchState = { kind: "search", keyword, columns };
    tableServerSearches.value = { ...tableServerSearches.value, [tab.id]: search };
    forgetTableLastPages(tab.id);
    options.inlineCellEdit.value = undefined;

    try {
      await loadTableSearchPage(tab, search, 1, databaseStore.pageSize);
    } catch (cause) {
      if (previousSearch) {
        tableServerSearches.value = { ...tableServerSearches.value, [tab.id]: previousSearch };
      } else {
        removeTableServerSearch(tab.id);
      }
      ElMessage.error(String(cause));
    }
  }

  async function applyTableFilters(filters: TableFilterModel) {
    if (options.activeObject.value !== "table" || !options.activeTableTab.value) return;
    if (!databaseStore.desktopRuntime) return options.desktopOnly();
    if (!filters.conditions.length) return clearTableServerSearch();
    const tab = options.activeTableTab.value;
    const previousSearch = tableServerSearches.value[tab.id];
    const search: TableServerSearchState = {
      kind: "filter",
      filters: { logic: filters.logic, conditions: filters.conditions.map((condition) => ({ ...condition })) },
    };
    tableServerSearches.value = { ...tableServerSearches.value, [tab.id]: search };
    forgetTableLastPages(tab.id);
    options.inlineCellEdit.value = undefined;
    try {
      await loadTableSearchPage(tab, search, 1, databaseStore.pageSize);
    } catch (cause) {
      if (previousSearch) tableServerSearches.value = { ...tableServerSearches.value, [tab.id]: previousSearch };
      else removeTableServerSearch(tab.id);
      ElMessage.error(String(cause));
    }
  }

  async function clearTableServerSearch() {
    if (options.activeObject.value !== "table" || !options.activeTableTab.value) return;
    const tab = options.activeTableTab.value;
    const hadSearch = Boolean(tableServerSearches.value[tab.id]);
    removeTableServerSearch(tab.id);
    forgetTableLastPages(tab.id);
    options.inlineCellEdit.value = undefined;
    if (!hadSearch || !databaseStore.desktopRuntime) return;

    try {
      await openTablePage(tab, 1, databaseStore.pageSize);
    } catch (cause) {
      ElMessage.error(String(cause));
    }
  }

  /** 重新加载当前表标签所在页；表数据可能已在外部变化，先丢弃缓存的尾页页码。 */
  async function refreshTablePage() {
    const tab = options.activeTableTab.value;
    if (options.activeObject.value !== "table" || !tab) return;
    if (!databaseStore.desktopRuntime) return options.desktopOnly();
    forgetTableLastPages(tab.id);
    options.inlineCellEdit.value = undefined;
    const page = tablePagePositions.value[tab.id] ?? 1;
    try {
      const search = tableServerSearches.value[tab.id];
      if (search) await loadTableSearchPage(tab, search, page, databaseStore.pageSize);
      else await openTablePage(tab, page, databaseStore.pageSize);
    } catch (cause) {
      ElMessage.error(String(cause));
    }
  }

  function handleGridSearchSubmit() {
    if (options.activeObject.value === "table") void searchAllTableRows();
  }

  function handleGridSearchClear() {
    if (options.activeObject.value === "table") void clearTableServerSearch();
  }

  async function applyPineconeNamespace() {
    const tab = options.activeTableTab.value;
    if (!tab || options.activeConnection.value?.dbType !== "pinecone") return;
    const namespace = pineconeNamespaceDraft.value.trim();
    pineconeNamespaces.value = { ...pineconeNamespaces.value, [tab.id]: namespace };
    const keyPrefix = `${tab.id}:`;
    vectorPageTokens.value = Object.fromEntries(
      Object.entries(vectorPageTokens.value).filter(([key]) => !key.startsWith(keyPrefix))
    );
    try {
      await openTablePage(tab, 1, databaseStore.pageSize);
    } catch (cause) {
      ElMessage.error(String(cause));
    }
  }

  async function changePage(nextPage: number) {
    if (!options.selectedTable.value || nextPage < 1 || !databaseStore.desktopRuntime) return;
    try {
      const tab = options.activeTableTab.value;
      const search = tab ? tableServerSearches.value[tab.id] : undefined;
      if (tab && search) {
        await loadTableSearchPage(tab, search, nextPage, databaseStore.pageSize);
        return;
      }
      if (!tab) return;
      await openTablePage(tab, nextPage, databaseStore.pageSize);
    } catch (cause) {
      ElMessage.error(String(cause));
    }
  }

  function jumpToPage() {
    const page = Math.max(1, Math.trunc(targetPage.value));
    targetPage.value = page;
    void changePage(page);
  }

  /**
   * 末页页码只能靠一次精确 COUNT 得出——表预览的 rowCount 是当页行数，
   * 引擎估算值又不可靠。因此仅在用户点「尾页」时才发这一次查询，
   * 结果按标签/页大小/搜索签名缓存，同条件下再次点尾页直接复用。
   */
  async function goToLastPage() {
    if (countingLastPage.value || !canLastPage.value) return;
    if (!databaseStore.desktopRuntime) return options.desktopOnly();
    const tab = options.activeTableTab.value;
    if (!tab) return;

    const key = lastPageKeyFor(
      tab,
      tableServerSearches.value[tab.id]
        ? tableQueryPageSize(databaseStore.pageSize)
        : databaseStore.pageSize
    );
    let lastPage = tableLastPages.value[key];
    if (lastPage === undefined) {
      countingLastPage.value = true;
      try {
        const connection = databaseStore.connections.find((item) => item.id === tab.connectionId);
        if (!connection || !options.connectionIsConnected(connection)) {
          throw new Error(t("database.connectionNotConnected"));
        }
        if (!options.connectionIsActive(connection)) databaseStore.activateConnection(connection.id);

        const search = tableServerSearches.value[tab.id];
        const query = !search
          ? buildTableRowCountSql(connection.dbType, tab.database, tab.table)
          : search.kind === "filter"
            ? buildTableFilterCountSql(connection.dbType, tab.database, tab.table, search.filters)
            : buildTableSearchCountSql(
                connection.dbType,
                tab.database,
                tab.table,
                search.columns,
                search.keyword
              );

        // executeTableQuery 会把结果写进 runtime.result，网格会被这一格计数顶掉。
        const result = await databaseStore.executeQueryForConnection(
          tab.connectionId,
          query,
          tab.database
        );
        const total = toRowCount(result?.rows?.[0]?.[0]);
        if (total === undefined) throw new Error(t("database.lastPageCountUnavailable"));

        // 搜索/筛选分页的 LIMIT 被构造器夹过，除数必须是生效后的页大小。
        const pageSize = search ? tableQueryPageSize(databaseStore.pageSize) : databaseStore.pageSize;
        lastPage = lastPageFromTotal(total, pageSize);
        tableLastPages.value = { ...tableLastPages.value, [key]: lastPage };
      } catch (cause) {
        ElMessage.error(t("database.lastPageCountFailed", { message: String(cause) }));
        return;
      } finally {
        countingLastPage.value = false;
      }
    }
    if (lastPage !== databaseStore.page) await changePage(lastPage);
  }

  watch(
    () => options.activeTableTab.value?.id,
    () => {
      const tab = options.activeTableTab.value;
      pineconeNamespaceDraft.value = tab ? activePineconeNamespace(tab) : "";
    },
    { immediate: true }
  );

  watch(
    () => databaseStore.page,
    (page) => {
      targetPage.value = page;
    },
    { immediate: true }
  );

  async function changePageSize(size: number) {
    if (!databaseStore.desktopRuntime) return options.desktopOnly();
    if (!options.selectedTable.value) {
      databaseStore.pageSize = size;
      return;
    }
    try {
      const tab = options.activeTableTab.value;
      const search = tab ? tableServerSearches.value[tab.id] : undefined;
      if (tab && search) {
        await loadTableSearchPage(tab, search, 1, size);
        return;
      }
      if (!tab) return;
      await openTablePage(tab, 1, size);
    } catch (cause) {
      ElMessage.error(String(cause));
    }
  }

  return {
    targetPage,
    pineconeNamespaceDraft,
    tableServerSearches,
    tablePagePositions,
    supportsCursorTablePreview,
    supportsOffsetTablePreview,
    canNextPage,
    canLastPage,
    countingLastPage,
    activePineconeNamespace,
    activeVectorMutationOptions,
    removeTableServerSearch,
    forgetTablePage,
    forgetTableLastPages,
    refreshTablePage,
    openTablePage,
    loadTableSearchPage,
    searchAllTableRows,
    applyTableFilters,
    clearTableServerSearch,
    handleGridSearchSubmit,
    handleGridSearchClear,
    applyPineconeNamespace,
    changePage,
    jumpToPage,
    goToLastPage,
    changePageSize,
  };
}

// 部分驱动把 bigint 计数当字符串返回，不能直接当 number 用。
function toRowCount(value: unknown): number | undefined {
  if (value === null || value === undefined) return undefined;
  const count = typeof value === "bigint" ? Number(value) : Number(String(value).trim());
  if (!Number.isFinite(count) || count < 0) return undefined;
  return count;
}
