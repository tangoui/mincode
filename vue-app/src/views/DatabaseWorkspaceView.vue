<script setup lang="ts">
import DatabaseTransferHistory from "@/components/database/DatabaseTransferHistory.vue";
import { useWorkspaceStateScope } from "@/composables/use-workspace-state-scope";
import { importSelectedTableData } from "@/services/database-transfer-service";
import {
  computed,
  defineAsyncComponent,
  nextTick,
  onBeforeUnmount,
  onDeactivated,
  onMounted,
  ref,
  toRaw,
  watch,
} from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  Connection,
  Coin,
  Document,
  Download,
  EditPen,
  Grid,
  Plus,
  Refresh,
  Upload,
} from "@element-plus/icons-vue";
import DatabaseConnectionDialog from "@/components/database/DatabaseConnectionDialog.vue";
import DatabaseConnectionOverview from "@/components/database/DatabaseConnectionOverview.vue";
import DatabaseErDiagram from "@/components/database/DatabaseErDiagram.vue";
import DatabaseObjectBrowser from "@/components/database/DatabaseObjectBrowser.vue";
import DatabaseTableDetailsDrawer from "@/components/database/DatabaseTableDetailsDrawer.vue";
import DatabaseUtilityDialogs from "@/components/database/DatabaseUtilityDialogs.vue";
import RedisWorkspacePanel from "@/components/database/RedisWorkspacePanel.vue";
import type {
  ResultGridContextMenu,
  ResultGridHeaderContextMenu,
} from "@/components/database/VirtualResultGrid.vue";
import DatabaseResultPane from "@/components/database/results/DatabaseResultPane.vue";
import DatabaseGridToolbar from "@/components/database/results/DatabaseGridToolbar.vue";
import {
  databaseDefaultPorts,
  databaseTypeLabels,
  type DatabaseConnection,
  type DbType,
  type DatabaseDiagram,
  type DatabaseQueryHistoryItem,
  type DatabaseQueryResult,
  type DatabaseSavedScript,
  type DatabaseTableDetails,
  type SchemaTable,
} from "@/domain/database/types";
import {
  buildCharsetChangeSql,
  charsetOptionsQuery,
  currentCharsetQuery,
  parseCharsetOptions,
  parseCurrentCharset,
  type DatabaseCharsetOption,
  type DatabaseCharsetSelection,
  type DatabaseCharsetTarget,
} from "@/domain/database/charset-change";
import { buildSqlCompletionItems } from "@/domain/database/sql-completion";
import { formatSqlForEditor } from "@/domain/database/sql-format";
import { resolveSqlForExecution, type SqlEditorSelection } from "@/domain/database/sql-statements";
import {
  buildDatabaseQueryTemplate,
  databaseQueryEditorLanguage,
} from "@/domain/database/query-template";
import {
  isVectorDatabaseType,
  type VectorCollectionCreateRequest,
} from "@/domain/database/vector-database";
import {
  connectionConfigHasSecrets,
  parseConnectionConfig,
  portableConnection,
} from "@/domain/database/connection-portability";
import {
  confirmSecretExport,
  confirmSecretImport,
} from "@/composables/use-secret-export-confirm";
import {
  createTableTemplate,
  qualifiedTableName,
  schemaExportFilename,
  sqlFileTabName,
  tableDataFilename,
  tableDataFormatFromPath,
  tableDataFormatIsBinary,
  type TableDataFormat,
} from "@/domain/database/object-naming";
import type { InsertSqlMode } from "@/domain/database/insert-sql";
import {
  buildColumnAlterSql,
  supportsCopyAsAlterSql,
} from "@/domain/database/alter-sql";
import { safetyRecordTouchesTable, type SqlSafetyRecord } from "@/domain/database/sql-safety";
import { loadSupportedDatabaseTypes } from "@/services/database-capabilities";
import DatabaseConnectionSidebar from "@/components/database/sidebar/DatabaseConnectionSidebar.vue";
import DatabaseDatasourceHeader from "@/components/database/query/DatabaseDatasourceHeader.vue";
import DatabaseAiQueryDialog from "@/components/database/query/DatabaseAiQueryDialog.vue";
import { useQueryExecution } from "@/composables/database/useQueryExecution";
import { useQueryTabs } from "@/composables/database/useQueryTabs";
import { useGridState, type TableTab } from "@/composables/database/useGridState";
import { useTableFilters } from "@/composables/database/useTableFilters";
import { useRowMutations } from "@/composables/database/useRowMutations";
import { useGridPendingChanges } from "@/composables/database/useGridPendingChanges";
import { useInsertSqlModeDialog } from "@/composables/database/useInsertSqlModeDialog";
import { useTablePagination } from "@/composables/database/useTablePagination";
import { useAiDatabaseActions } from "@/composables/database/useAiDatabaseActions";
import { useVueI18n } from "@/i18n/messages";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { useLinkedNotesDrawer } from "@/stores/linked-notes-drawer";
import AppWorkspaceTabs from "@/components/ui/AppWorkspaceTabs.vue";
import AppContextMenu from "@/components/ui/AppContextMenu.vue";
import AppSplitPane from "@/components/ui/AppSplitPane.vue";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { registerUiSessionFlusher } from "@/services/ui-session";
import { registerCommand } from "@/services/command-registry";
import { useResizablePane } from "@/composables/use-resizable-pane";
import { useActiveViewCommands, useToggleSidebarCommand } from "@/composables/use-active-view-commands";
import { useWorkspaceScopedPersistence } from "@/composables/use-workspace-state-scope";
import {
  chooseNativeFile,
  chooseNativeSaveFile,
  copyText,
  invokeBackend,
} from "@/services/desktop-bridge";
import { saveBinaryFileToDisk, saveTextFileToDisk } from "@/services/file-download";
import { runWithSafety, sqlSafetyArgs } from "@/services/sql-safety-service";
import { confirmSqlSafety } from "@/services/sql-safety-confirm";
import { consumeDatabaseFiles, pendingDatabaseFiles } from "@/services/external-file-open";

// Rendered only for SQL query tabs; loading it lazily keeps Monaco out of this view's startup chunk.
const loadDatabaseQueryWorkspace = () => import("@/components/database/query/DatabaseQueryWorkspace.vue");
const DatabaseQueryWorkspace = defineAsyncComponent(loadDatabaseQueryWorkspace);
// Import from other tools (DBeaver, Navicat, …); only loaded when opened.
const DatabaseConnectionImportDialog = defineAsyncComponent(() => import("@/components/database/DatabaseConnectionImportDialog.vue"));
const connectionImportVisible = ref(false);
// Scheduled backup management; only loaded when opened.
const DatabaseBackupDialog = defineAsyncComponent(() => import("@/components/database/DatabaseBackupDialog.vue"));
const backupDialogVisible = ref(false);
// Rarely used dialogs: still rendered as before (state kept between opens), but loaded
// in their own chunks so the workspace chunk stays within its bundle budget.
const VectorCollectionDialog = defineAsyncComponent(() => import("@/components/database/VectorCollectionDialog.vue"));
const DatabaseSchemaSyncDialog = defineAsyncComponent(() => import("@/components/database/DatabaseSchemaSyncDialog.vue"));
const DatabaseCharsetDialog = defineAsyncComponent(() => import("@/components/database/DatabaseCharsetDialog.vue"));

defineOptions({ name: "DatabaseWorkspaceView" });

const { t } = useVueI18n();
const databaseStore = useDatabaseWorkspaceStore();
const linkedNotes = useLinkedNotesDrawer();
const transferScope = useWorkspaceStateScope();
const connectingConnectionIds = ref<Record<string, boolean>>({});
const connectionSidebar = ref<InstanceType<typeof DatabaseConnectionSidebar>>();

const queryTabDefaultName = (number: number) => t("database.queryTabName", { number });

const tableTabs = ref<TableTab[]>([]);
const loadingTableIds = new Set<string>();
const activeTableTabId = ref<string>();
const activeTableTab = computed(() =>
  tableTabs.value.find((tab) => tab.id === activeTableTabId.value)
);

const activeObject = ref<"database" | "query" | "table">("database");
// A table workspace opens directly on its data tab. SQL editors are created only
// when the user explicitly requests a new query.
const {
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
} = useQueryTabs({
  defaultNameFor: queryTabDefaultName,
  templateFor: currentTableQueryTemplate,
  onQueryActivated: () => {
    activeObject.value = "query";
  },
  onTabsEmpty: () => {
    if (tableTabs.value.length) {
      void activateTableTab(activeTableTabId.value ?? tableTabs.value[0]!.id);
    } else if (databaseStore.selectedDatabase) {
      activeObject.value = "database";
    }
  },
});
const activeDatabaseObjectTab = computed<string>({
  get: () => {
    if (activeObject.value === "database" && databaseStore.selectedDatabase) {
      return `database:${databaseStore.selectedDatabase}`;
    }
    if (activeObject.value === "query" && activeQueryTabId.value) {
      return `query:${activeQueryTabId.value}`;
    }
    if (activeObject.value === "table" && activeTableTabId.value) {
      return `table:${activeTableTabId.value}`;
    }
    return "";
  },
  set: (name) => {
    if (name.startsWith("database:")) {
      const database = name.slice("database:".length);
      if (databaseStore.activeSchema?.databases.some((item) => item.name === database)) {
        databaseStore.selectedDatabase = database;
        activeObject.value = "database";
      }
      return;
    }
    if (name.startsWith("query:")) {
      const tabId = name.slice("query:".length);
      const tab = queryTabs.value.find((item) => item.id === tabId);
      if (tab) {
        activeQueryTabId.value = tabId;
        tab.resultLastAccessedAt = Date.now();
        activeObject.value = "query";
      }
      return;
    }
    if (name.startsWith("table:")) {
      const tabId = name.slice("table:".length);
      if (tableTabs.value.some((tab) => tab.id === tabId)) void activateTableTab(tabId);
    }
  },
});
const connectionDialogVisible = ref(false);
const editingConnection = ref<DatabaseConnection>();
const savingConnection = ref(false);
const testingConnection = ref(false);
const vectorCollectionDialogVisible = ref(false);
const vectorCollectionSaving = ref(false);
const vectorCollectionTarget = ref<{ connection: DatabaseConnection; database: string }>();
const gridContextMenu = ref<ResultGridContextMenu>();
const gridHeaderContextMenu = ref<ResultGridHeaderContextMenu>();
const importInput = ref<HTMLInputElement>();
const executionPlanVisible = ref(false);
const executionPlan = ref<DatabaseQueryResult>();
const detailsVisible = ref(false);
const detailsLoading = ref(false);
const schemaChangeApplying = ref(false);
const charsetDialogVisible = ref(false);
const charsetDialogLoading = ref(false);
const charsetChangeApplying = ref(false);
const charsetTarget = ref<DatabaseCharsetTarget>();
const charsetConnectionId = ref("");
const charsetOptions = ref<DatabaseCharsetOption[]>([]);
const currentCharset = ref<DatabaseCharsetSelection>();
let charsetDialogRequestId = 0;
const schemaSyncVisible = ref(false);
const tableDetails = ref<DatabaseTableDetails>();
const tableDetailsTarget = ref<{ database: string; table: string }>();
let tableDetailsRequestId = 0;
const overviewSelectedObjectName = ref("");
const overviewSearchText = ref("");
const overviewDetails = ref<DatabaseTableDetails>();
const overviewDetailsLoading = ref(false);
let overviewDetailsRequestId = 0;
const erDiagramVisible = ref(false);
const erDiagramLoading = ref(false);
const erDiagram = ref<DatabaseDiagram>();
const diagnosticsVisible = ref(false);
const aiQueryDialogVisible = ref(false);
// Occasional dialogs load on first open and then stay mounted (close animation, focus return).
// The AI query dialog stays static: as a separate chunk the bundler moves shared AI modules into
// the entry chunk (+4 KiB there).
const DatabaseBulkUpdateDialog = defineAsyncComponent(() => import("@/components/database/results/DatabaseBulkUpdateDialog.vue"));
const DatabaseInsertSqlModeDialog = defineAsyncComponent(() => import("@/components/database/DatabaseInsertSqlModeDialog.vue"));
const openedDialogs = ref({ bulk: false, insertSql: false });
const queryEditorFontSize = ref(13);
const queryEditorExpanded = ref(false);
const queryEditorPercent = ref(34);
const queryEditorSizePersistence = useWorkspaceScopedPersistence<number>("app_db_query_editor_percent");
const queryRunMode = ref<"smart" | "all">("smart");
const {
  size: explorerWidth,
  collapsed: explorerCollapsed,
  hydrate: hydrateExplorerLayout,
  persist: persistExplorerLayout,
} = useResizablePane({
  sizeKey: "app_db_sidebar_width",
  collapsedKey: "app_db_sidebar_collapsed",
  defaultSize: 322,
  minSize: 220,
  maxSize: 500,
});

function persistExplorerWidth(value: number) {
  explorerWidth.value = value;
  void persistExplorerLayout();
}

function updateExplorerCollapsed(value: boolean) {
  explorerCollapsed.value = value;
  void persistExplorerLayout();
}
useToggleSidebarCommand(() => explorerCollapsed.value, updateExplorerCollapsed);
const savedScripts = ref<DatabaseSavedScript[]>(loadStoredArray("app_db_scripts"));
const queryHistory = ref<DatabaseQueryHistoryItem[]>(
  loadStoredArray("app_db_query_history")
);
const cellDetailVisible = ref(false);
const cellDetail = ref<{ column: string; value: unknown }>();

function loadStoredArray<T>(key: string): T[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(key) || "[]");
    return Array.isArray(value) ? (value as T[]) : [];
  } catch {
    return [];
  }
}
const DATABASE_CONNECTION_OVERVIEW_TAB = "__connection-overview__";
const selectedDatabaseConnectionTab = ref(DATABASE_CONNECTION_OVERVIEW_TAB);
const activeConnection = computed(() => databaseStore.activeConnection);
const activeDriverDescriptor = computed(() =>
  databaseStore.connectionDescriptor(activeConnection.value)
);
const activeCapabilities = computed(() =>
  activeConnection.value ? databaseStore.connectionCapabilities(activeConnection.value) : undefined
);
const activeDialectType = computed(() => activeConnection.value?.dbType);

interface DetectedFileDatabase {
  dbType: Extract<DbType, "sqlite" | "duckdb">;
  filePath: string;
  fileName: string;
  builtinDriver?: boolean;
}

function connectErrorMessage(cause: unknown) {
  const message = cause instanceof Error ? cause.message : String(cause);
  return message.includes("driver_required") ? t("database.driverUnavailable") : message;
}

function capabilitiesOf(connection: DatabaseConnection) {
  return databaseStore.connectionCapabilities(connection);
}
const activeDatabaseConnectionTab = computed<string>({
  get: () =>
    selectedDatabaseConnectionTab.value === DATABASE_CONNECTION_OVERVIEW_TAB ||
    connectedConnections.value.some((connection) => connection.id === selectedDatabaseConnectionTab.value)
      ? selectedDatabaseConnectionTab.value
      : DATABASE_CONNECTION_OVERVIEW_TAB,
  set: (connectionId) => {
    if (connectionId === DATABASE_CONNECTION_OVERVIEW_TAB) {
      selectedDatabaseConnectionTab.value = DATABASE_CONNECTION_OVERVIEW_TAB;
      return;
    }
    const connection = connectedConnections.value.find((item) => item.id === connectionId);
    if (connection) void activateConnection(connection);
  },
});
const activeResult = computed(() =>
  activeObject.value === "query" ? activeQueryTab.value?.result : databaseStore.result
);
const activeQueryRunning = computed(() => activeQueryTab.value?.status === "running");
const connectedConnections = computed(() => databaseStore.connectedConnections);
const selectedTable = computed(() =>
  activeObject.value === "table"
    ? activeTableTab.value?.table ?? databaseStore.selectedTable
    : databaseStore.selectedTable
);
const selectedDatabase = computed(() =>
  activeObject.value === "table"
    ? activeTableTab.value?.database ?? databaseStore.selectedDatabase
    : databaseStore.selectedDatabase
);
const overviewSchema = computed(() => {
  const connectionId = activeConnection.value?.id;
  return connectionId ? databaseStore.schemas[connectionId] : undefined;
});
const overviewDatabases = computed(
  () => overviewSchema.value?.databases.map((database) => database.name) ?? []
);
const overviewObjects = computed(
  () =>
    overviewSchema.value?.databases.find(
      (database) => database.name === databaseStore.selectedDatabase
    )?.tables ?? []
);
const queryTemplateTarget = computed(() => {
  const connection = activeConnection.value;
  const tableTab = activeTableTab.value;
  if (!connection || !tableTab || tableTab.connectionId !== connection.id) return;
  return {
    connection,
    database: tableTab.database,
    table: tableTab.table,
  };
});
const queryEditorLanguage = computed(() =>
  databaseQueryEditorLanguage(activeDialectType.value ?? "mysql")
);
const vectorQueryHintVisible = computed(
  () =>
    activeObject.value === "query" &&
    isVectorDatabaseType(activeConnection.value?.dbType),
);
const queryEditorPath = computed(() => {
  const extension =
    queryEditorLanguage.value === "json"
      ? "json"
      : queryEditorLanguage.value === "sql"
        ? "sql"
        : "txt";
  return `${activeQueryTab.value?.id ?? "query"}.${extension}`;
});
const sqlCompletions = computed(() =>
  buildSqlCompletionItems({
    schema: databaseStore.activeSchema,
    selectedDatabase: selectedDatabase.value,
    selectedTable: selectedTable.value,
    selectedTableColumns: databaseStore.tableColumns,
    resultColumns: activeQueryTab.value?.result?.columns,
  })
);
function selectedSqlForExecution() {
  const tab = activeQueryTab.value;
  const state = tab?.viewState;
  const selection: SqlEditorSelection | undefined = state
    ? {
        startLineNumber: state.selectionStartLineNumber,
        startColumn: state.selectionStartColumn,
        endLineNumber: state.positionLineNumber,
        endColumn: state.positionColumn,
      }
    : undefined;
  return resolveSqlForExecution(queryText.value, selection, queryRunMode.value === "all");
}

const { runQuery, cancelQuery, showExecutionPlan } = useQueryExecution({
  queryTabs,
  activeQueryTab,
  queryText,
  activeObject,
  activeConnection,
  selectedDatabase,
  selectedTable,
  queryHistory,
  desktopOnly,
  enforceQueryResultBudget,
  onExecutionPlanLoaded: (result) => {
    executionPlan.value = result;
    executionPlanVisible.value = true;
  },
});

function runEditorQuery(mode: "smart" | "all" = "smart") {
  queryRunMode.value = mode;
  void runQuery(selectedSqlForExecution());
}

function changeQueryEditorFontSize(delta: number) {
  queryEditorFontSize.value = Math.max(10, Math.min(24, queryEditorFontSize.value + delta));
  void savePersistedValue("app_db_query_editor_font_size", queryEditorFontSize.value);
}

async function importSqlFile(file: File) {
  try {
    const content = await file.text();
    if (queryText.value.trim()) newQuery(content, file.name.replace(/\.[^.]+$/, ""));
    else queryText.value = content;
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function copyEditorSql() {
  if (!queryText.value.trim()) return;
  await copyText(queryText.value);
  ElMessage.success(t("database.sqlCopied"));
}

const loadedResultRows = computed(() => activeResult.value?.rows ?? []);
const resultColumns = computed(() => activeResult.value?.columns ?? []);
// 表格修改先暂存：resultRows 是叠加了暂存修改（含新增行）的行，所有表格读写都基于它。
const pendingChanges = useGridPendingChanges({
  activeObject,
  activeTableTab,
  loadedRows: loadedResultRows,
  resultColumns,
  vectorMutationOptions: () => activeVectorMutationOptions(),
  onApplied: () => forgetActiveTableLastPages(),
});
const resultRows = pendingChanges.rows;
const filterColumns = computed(() => {
  if (activeObject.value !== "table") return resultColumns.value;
  return [...new Set([
    ...resultColumns.value,
    ...databaseStore.tableColumns.map((column) => column.name),
  ])];
});
const {
  resultGridStates,
  resultColumnIndexByName,
  activeGridKey,
  activeGridState,
  gridFilterText,
  visibleColumnEntries,
  updateActiveGridState,
  setColumnVisibility,
  showAllColumns,
  toggleGridLayout,
  reorderColumns,
  toggleColumnSort,
  setFrozenColumnCount,
} = useGridState({
  activeObject,
  activeQueryTab,
  activeTableTab,
  resultColumns,
});
const tableSearchField = ref<InstanceType<typeof DatabaseGridToolbar>>();
const selectedGridRows = ref<Record<string, number[]>>({});
const selectedRowIndexes = computed(() => selectedGridRows.value[activeGridKey.value] ?? []);
const selectedRowIndexSet = computed(() => new Set(selectedRowIndexes.value));
const allDisplayedRowsSelected = computed(
  () =>
    displayRows.value.length > 0 &&
    displayRows.value.every((entry) => selectedRowIndexSet.value.has(entry.originalIndex))
);
const someDisplayedRowsSelected = computed(
  () =>
    !allDisplayedRowsSelected.value &&
    displayRows.value.some((entry) => selectedRowIndexSet.value.has(entry.originalIndex))
);
const canSearchEntireTable = computed(
  () =>
    activeObject.value === "table" &&
    Boolean(activeCapabilities.value?.tableSearch)
);
const gridSearchPlaceholder = computed(() =>
  canSearchEntireTable.value ? t("database.searchAllTableDataHint") : t("database.filterResult")
);
const {
  addFilterCondition,
  updateFilterCondition,
  removeFilterCondition,
  setFilterLogic,
  clearColumnFilters,
  filterByColumnValue,
  filterByColumnValues,
  excludeByColumnValue,
  excludeByColumnValues,
  applyColumnFilterOperator,
  localGridFilterCriteria,
  localRowFilter,
  rowTextSearch,
  disposeTableFilters,
} = useTableFilters({
  activeObject,
  activeGridKey,
  activeGridState,
  updateActiveGridState,
  resultColumns,
  filterColumns,
  resultRows,
  resultColumnIndexByName,
  canSearchEntireTable,
  formatCell,
  onClearTableFilters: () => void clearTableServerSearch(),
  onApplyTableFilters: () => applyTableFilters(activeGridState.value.filters),
});
const resultGridSortCollator = new Intl.Collator(undefined, {
  numeric: true,
  sensitivity: "base",
});

const displayRows = computed(() => {
  // 普通表的搜索和类型化筛选由后端查询整表；SQL 查询结果只筛选当前结果集。
  const filterText = localGridFilterCriteria.value.filterText.trim().toLowerCase();
  const entries: Array<{ row: unknown[]; originalIndex: number }> = [];
  const matchesTypedFilters = localRowFilter.value;

  for (let originalIndex = 0; originalIndex < resultRows.value.length; originalIndex += 1) {
    const row = resultRows.value[originalIndex];
    if (matchesTypedFilters && !matchesTypedFilters(row)) continue;
    if (filterText && !rowTextSearch.search(row).includes(filterText)) continue;
    entries.push({ row, originalIndex });
  }

  const sort = activeGridState.value.sort;
  const columnIndex = resultColumnIndexByName.value.get(sort?.column ?? "") ?? -1;
  if (!sort || columnIndex < 0) return entries;

  const direction = sort.direction === "asc" ? 1 : -1;
  return entries
    .map((entry) => {
      const value = entry.row[columnIndex];
      return {
        entry,
        value,
        numericValue: typeof value === "number" ? value : Number.NaN,
        textValue: value == null ? "" : formatCell(value),
      };
    })
    .sort((left, right) => {
      if (left.value == null && right.value == null) return 0;
      if (left.value == null) return -direction;
      if (right.value == null) return direction;
      const compared =
        Number.isFinite(left.numericValue) && Number.isFinite(right.numericValue)
          ? left.numericValue - right.numericValue
          : resultGridSortCollator.compare(left.textValue, right.textValue);
      return compared * direction;
    })
    .map(({ entry }) => entry);
});
function updateSelectedGridRows(next: Set<number>) {
  selectedGridRows.value = {
    ...selectedGridRows.value,
    [activeGridKey.value]: [...next],
  };
}

function toggleGridRowSelection(rowIndex: number, selected: boolean) {
  const next = new Set(selectedRowIndexes.value);
  if (selected) next.add(rowIndex);
  else next.delete(rowIndex);
  updateSelectedGridRows(next);
}

function toggleAllGridRowSelection(selected: boolean) {
  const next = new Set(selectedRowIndexes.value);
  for (const entry of displayRows.value) {
    if (selected) next.add(entry.originalIndex);
    else next.delete(entry.originalIndex);
  }
  updateSelectedGridRows(next);
}

const resultRowCountText = computed(() => {
  const result = activeResult.value;
  if (!result) return `0 ${t("database.rows")}`;
  if (displayRows.value.length !== resultRows.value.length) {
    return t("database.filteredRows", {
      shown: displayRows.value.length,
      total: resultRows.value.length,
    });
  }
  return `${result.rowCount} ${t("database.rows")}`;
});
const canInsertRows = computed(
  () =>
    activeObject.value === "table" &&
    Boolean(
      databaseStore.activeSessionId &&
        selectedTable.value &&
        activeConnection.value &&
        activeCapabilities.value?.rowInsert &&
        !isReadOnlyMongoCollection.value
    )
);
const isReadOnlyMongoCollection = computed(
  () =>
    activeConnection.value?.dbType === "mongodb" &&
    selectedTable.value.trim().toLowerCase().startsWith("system.")
);
const canEditRows = computed(() =>
  Boolean(
    activeObject.value === "table" &&
      databaseStore.activeSessionId &&
      selectedTable.value &&
      activeConnection.value &&
      activeCapabilities.value?.rowEdit &&
      databaseStore.primaryKeys.length &&
      !isReadOnlyMongoCollection.value
  )
);
const insertSqlModeDialog = useInsertSqlModeDialog();
const {
  rowDialogVisible,
  rowDialogMode,
  rowForm,
  inlineCellEdit,
  canEditContextCell,
  beginInlineCellEdit,
  updateInlineCellEdit,
  cancelInlineCellEdit,
  commitInlineCellEdit,
  copyGridContextValue,
  copyGridContextRow,
  canCopyGridContextInsertSql,
  copyGridContextInsertSql,
  filterByGridContextValue,
  excludeByGridContextValue,
  filterGridContextByOperator,
  canMutateGridContextColumn,
  canSetGridContextNull,
  setGridContextCellNull,
  bulkUpdateTarget,
  openBulkUpdate,
  openGridContextBulkUpdate,
  closeBulkUpdate,
  applyBulkUpdate,
  viewGridContextCell,
  editGridContextCell,
  openInsertRow,
  duplicateGridContextRow,
  editRecord,
  saveRow,
  deleteRecord,
  deleteGridContextRow,
  canRevertGridContextChange,
  revertGridContextChange,
} = useRowMutations({
  activeObject,
  activeConnection,
  selectedTable,
  selectedDatabase,
  resultRows,
  resultColumns,
  canEditRows,
  canInsertRows,
  selectedRowIndexes,
  selectedRowIndexSet,
  formatCell,
  updateSelectedGridRows,
  vectorMutationOptions: () => activeVectorMutationOptions(),
  gridContextMenu,
  closeGridContextMenu,
  filterByColumnValue,
  filterByColumnValues,
  excludeByColumnValue,
  excludeByColumnValues,
  applyColumnFilterOperator,
  openCellDetail,
  desktopOnly,
  chooseInsertSqlMode: insertSqlModeDialog.chooseInsertSqlMode,
  pending: pendingChanges,
});
// Pre-flush: the first open mounts the dialog in the same render that shows it.
watch([bulkUpdateTarget, insertSqlModeDialog.visible], ([bulk, insertSql]) => {
  const opened = openedDialogs.value;
  opened.bulk ||= Boolean(bulk);
  opened.insertSql ||= insertSql;
});
const {
  targetPage,
  pineconeNamespaceDraft,
  tableServerSearches,
  tablePagePositions,
  supportsOffsetTablePreview,
  canNextPage,
  canLastPage,
  countingLastPage,
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
} = useTablePagination({
  activeObject,
  activeConnection,
  activeTableTab,
  selectedTable,
  resultColumns,
  resultRows,
  canSearchEntireTable,
  gridFilterText,
  inlineCellEdit,
  connectionIsConnected,
  connectionIsActive,
  desktopOnly,
});
/** 暂存修改所属的表标签。 */
const pendingTableTab = computed(() =>
  tableTabs.value.find((tab) => tab.id === pendingChanges.tabId.value)
);
/** 离开有待提交修改的表（翻页、刷新、换表、关闭标签、换连接）前确认；返回 false 表示中止。 */
function confirmLeavePendingTable(next?: { tabId?: string; connectionId?: string }) {
  const staged = pendingTableTab.value;
  if (!pendingChanges.total.value) return Promise.resolve(true);
  if (staged && next?.tabId === staged.id) return Promise.resolve(true);
  if (staged && next?.connectionId === staged.connectionId) return Promise.resolve(true);
  return pendingChanges.confirmLeave();
}
function guardPendingTable<A extends unknown[]>(action: (...args: A) => unknown) {
  return async (...args: A) => {
    if (await confirmLeavePendingTable()) await action(...args);
  };
}
const changePageGuarded = guardPendingTable(changePage);
const jumpToPageGuarded = guardPendingTable(jumpToPage);
const goToLastPageGuarded = guardPendingTable(goToLastPage);
const changePageSizeGuarded = guardPendingTable(changePageSize);
// 换 Pinecone 命名空间会换掉写入目标：先提交 / 放弃暂存（提交时也会核对命名空间）。
const applyPineconeNamespaceGuarded = guardPendingTable(applyPineconeNamespace);
function beforeLeaveObjectTab(name: string | number) {
  const next = String(name);
  // 切到查询 / 库概览不丢暂存（仍属于原表标签）；切到别的表才确认。
  if (!next.startsWith("table:")) return true;
  return confirmLeavePendingTable({ tabId: next.slice("table:".length) });
}
function beforeLeaveConnectionTab(name: string | number) {
  const next = String(name);
  if (next === DATABASE_CONNECTION_OVERVIEW_TAB) return true;
  return confirmLeavePendingTable({ connectionId: next });
}
function discardPendingChanges() {
  inlineCellEdit.value = undefined;
  pendingChanges.discard();
}
/** 撤回后：涉及的表丢弃元数据与尾页缓存，当前打开的就重载本页（暂存按主键叠加，不受影响）；暂存修改基于撤回前的数据，提醒用户。 */
function onSafetyUndone(record: SqlSafetyRecord) {
  for (const tab of tableTabs.value) {
    if (!safetyRecordTouchesTable(record, tab)) continue;
    databaseStore.invalidateTableMetadata(tab.connectionId, tab.database, tab.table);
    forgetTableLastPages(tab.id);
  }
  const staged = pendingTableTab.value;
  if (staged && pendingChanges.total.value && safetyRecordTouchesTable(record, staged)) {
    ElMessage.warning(t("database.safety.history.pendingBeforeUndo"));
  }
  const tab = activeTableTab.value;
  if (activeObject.value === "table" && tab && safetyRecordTouchesTable(record, tab)) void refreshTablePage();
}
// Cmd/Ctrl+S（database:submit-grid-changes，编辑器与对话框内不触发；SQL 编辑器里仍是执行查询）
// 提交当前表的暂存修改；正在编辑的单元格先写入暂存。
useActiveViewCommands(() => [
  registerCommand({
    id: "database:submit-grid-changes",
    source: "builtin",
    title: () => t("keybindings.command.database:submit-grid-changes"),
    subtitle: () => t("commandPalette.source.builtin"),
    when: "!editor.focus && !dialog.focus",
    showInPalette: false,
    execute: () => {
      if (inlineCellEdit.value) commitInlineCellEdit(inlineCellEdit.value);
      if (pendingChanges.count.value) void pendingChanges.submit();
    },
  }),
]);
const activeConnectionHistory = computed(() =>
  queryHistory.value.filter((item) => item.connectionId === activeConnection.value?.id)
);
const diagnosticHistory = computed(() =>
  activeConnectionHistory.value.filter((item) => !item.success || item.executionMs >= 500)
);
function connectionIsConnected(connection: DatabaseConnection) {
  return Boolean(databaseStore.sessionIds[connection.id]);
}

function connectionIsConnecting(connection: DatabaseConnection) {
  return Boolean(connectingConnectionIds.value[connection.id]);
}

const connectionGroupNames = computed(() =>
  [...new Set(databaseStore.connections.map((connection) => connection.group?.trim()))]
    .filter((group): group is string => Boolean(group))
    .sort((left, right) => left.localeCompare(right))
);
const connectionTagNames = computed(() =>
  [...new Set(databaseStore.connections.flatMap((connection) => connection.tags ?? []))]
    .map((tag) => tag.trim())
    .filter(Boolean)
    .sort((left, right) => left.localeCompare(right))
);

function connectionIsActive(connection: DatabaseConnection) {
  return connection.id === activeConnection.value?.id;
}

async function ensureConnectionActive(connection: DatabaseConnection) {
  if (!connectionIsConnected(connection)) {
    return connect(connection);
  }
  if (!connectionIsActive(connection)) databaseStore.activateConnection(connection.id);
  return true;
}

/** 树上会在右侧打开内容的操作（表、查询、库概览等）：激活连接并离开「连接概览」，切到该连接的标签。 */
async function showConnectionContent(connection: DatabaseConnection) {
  if (!(await ensureConnectionActive(connection))) return false;
  selectedDatabaseConnectionTab.value = connection.id;
  return true;
}

async function activateConnection(connection: DatabaseConnection) {
  if (connectionIsConnecting(connection)) return;
  if (!(await confirmLeavePendingTable({ connectionId: connection.id }))) return;
  const wasConnected = connectionIsConnected(connection);
  connectionSidebar.value?.setConnectionExpanded(connection, true);
  if (!(await ensureConnectionActive(connection))) return;
  selectedDatabaseConnectionTab.value = connection.id;

  if (!wasConnected) {
    // 先让连接状态和左侧对象树完成一帧，再挂载右侧大型对象列表，
    // 避免两块界面在同一个渲染帧内集中创建造成“卡一下”的体感。
    await nextTick();
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  }

  if (activeObject.value === "query") return;
  const tab = tableTabs.value.find((item) => item.connectionId === connection.id);
  if (activeObject.value === "table" && tab) {
    await activateTableTab(tab.id);
    return;
  }

  activeTableTabId.value = undefined;
  databaseStore.closeTable();
  const database = databaseStore.activeSchema?.databases[0]?.name ?? "";
  if (database) {
    // The overview already shows every object on the right. Keep the matching
    // sidebar database collapsed until the user opens it to avoid mounting the
    // same large table list twice during connection activation.
    openDatabaseOverview(connection, database, false);
  }
}

function openDatabaseOverview(
  connection: DatabaseConnection,
  database: string,
  expandDatabase = true
) {
  databaseStore.selectedDatabase = database;
  connectionSidebar.value?.setConnectionExpanded(connection, true);
  connectionSidebar.value?.setDatabaseExpanded(connection, database, expandDatabase);
  activeObject.value = "database";
  overviewSelectedObjectName.value = "";
  overviewSearchText.value = "";
  overviewDetails.value = undefined;
  overviewDetailsRequestId += 1;
  if (capabilitiesOf(connection).loadsTablesPerDatabase) {
    void databaseStore.loadTablesForDatabase(connection.id, database).catch((cause) => {
      ElMessage.error(String(cause));
    });
  }
}

function switchOverviewDatabase(database: string) {
  const connection = activeConnection.value;
  if (!connection || database === databaseStore.selectedDatabase) return;
  if (!overviewDatabases.value.includes(database)) return;
  openDatabaseOverview(connection, database, false);
}

async function toggleConnectionExpansion(connection: DatabaseConnection) {
  if (!connectionSidebar.value?.connectionIsExpanded(connection)) {
    await activateConnection(connection);
    return;
  }
  connectionSidebar.value?.setConnectionExpanded(connection, false);
}

async function toggleDatabaseExpansion(connection: DatabaseConnection, database: string) {
  if (!(await showConnectionContent(connection))) return;
  const expandDatabase = !connectionSidebar.value?.databaseIsExpanded(connection, database);
  openDatabaseOverview(connection, database, expandDatabase);
  if (expandDatabase && capabilitiesOf(connection).loadsTablesPerDatabase) {
    await databaseStore.loadTablesForDatabase(connection.id, database).catch((cause) => {
      ElMessage.error(String(cause));
    });
  }
}

async function selectConnectionTable(
  connection: DatabaseConnection,
  database: string,
  table: string
) {
  if (!(await showConnectionContent(connection))) return;
  await selectTable(database, table);
}

async function openConnectionScripts(connection: DatabaseConnection) {
  if (!(await ensureConnectionActive(connection))) return;
  connectionSidebar.value?.setConnectionExpanded(connection, true);
  connectionSidebar.value?.revealScriptsTab();
}

async function openExternalSqlFile(connection: DatabaseConnection) {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  if (!(await showConnectionContent(connection))) return;

  try {
    const path = await chooseNativeFile(t("database.openSqlFile"), [
      { DisplayName: t("database.sqlFiles"), Pattern: "*.sql" },
    ]);
    if (!path) return;

    const sql = await invokeBackend<string>("local_fs_read_selected_text_file", { path });
    if (!sql.trim()) {
      ElMessage.warning(t("database.sqlFileEmpty"));
      return;
    }
    newQuery(sql, sqlFileTabName(path, t("database.externalSqlFiles")));
    ElMessage.success(t("database.sqlFileOpened"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

function existingTableNamesFor(connection: DatabaseConnection, database: string) {
  return (
    (databaseStore.schemas[connection.id]?.databases ?? [])
      .find((item) => item.name === database)
      ?.tables.map((table) => table.name) ?? []
  );
}

async function openSchemaNewQuery(connection: DatabaseConnection, database: string) {
  if (!(await showConnectionContent(connection))) return;
  databaseStore.selectedDatabase = database;
  newQuery("", t("database.newQuery"));
}

async function openNewTableTemplate(connection: DatabaseConnection, database: string) {
  if (!(await showConnectionContent(connection))) return;
  databaseStore.selectedDatabase = database;
  newQuery(
    createTableTemplate(connection, database, existingTableNamesFor(connection, database)),
    t("database.newTable")
  );
}

async function copyObjectName(value: string) {
  try {
    await copyText(value);
    ElMessage.success(t("database.objectNameCopied"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function copyTableDdl(connection: DatabaseConnection, database: string, table: string) {
  if (!(await ensureConnectionActive(connection))) return;
  if (!capabilitiesOf(connection).schemaDdl) {
    ElMessage.info(t("database.schemaDdlUnavailable"));
    return;
  }
  try {
    const ddl = await invokeBackend<string>("db_get_table_ddl", {
      sessionId: databaseStore.activeSessionId,
      database,
      table,
    });
    if (!ddl?.trim()) {
      ElMessage.info(t("database.noDdl"));
      return;
    }
    await copyText(ddl);
    ElMessage.success(t("database.ddlCopied"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function openTableDetailsFromTree(
  connection: DatabaseConnection,
  database: string,
  table: string
) {
  if (!(await showConnectionContent(connection))) return;
  await selectTable(database, table);
  await showTableDetails();
}

async function exportSchema(
  connection: DatabaseConnection,
  database: string,
  tables: SchemaTable[]
) {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  if (!(await ensureConnectionActive(connection))) return;
  if (!capabilitiesOf(connection).schemaDdl) {
    ElMessage.info(t("database.schemaDdlUnavailable"));
    return;
  }

  const databaseTables =
    capabilitiesOf(connection).loadsTablesPerDatabase
      ? await databaseStore.loadTablesForDatabase(connection.id, database)
      : tables;
  const exportableTables = databaseTables.filter(({ kind }) =>
    ["table", "view", "collection"].includes(kind.toLowerCase())
  );
  if (!exportableTables.length) {
    ElMessage.info(t("database.schemaExportEmpty"));
    return;
  }

  try {
    const parts = [
      `-- ${t("database.schemaExportHeader", { name: database })}`,
      `-- ${t("database.schemaExportGenerated", { date: new Date().toISOString() })}`,
      "",
    ];
    for (const table of exportableTables) {
      try {
        const ddl = await invokeBackend<string>("db_get_table_ddl", {
          sessionId: databaseStore.activeSessionId,
          database,
          table: table.name,
        });
        parts.push(ddl?.trim() || `-- ${t("database.ddlUnavailable", { name: table.name })}`, "");
      } catch {
        parts.push(`-- ${t("database.ddlUnavailable", { name: table.name })}`, "");
      }
    }

    const path = await chooseNativeSaveFile(
      t("database.exportSchema"),
      schemaExportFilename(database),
      [{ DisplayName: t("database.sqlFiles"), Pattern: "*.sql" }]
    );
    if (!path) return;
    await invokeBackend<void>("local_fs_write_selected_text_file", {
      path,
      content: parts.join("\n"),
    });
    ElMessage.success(t("database.schemaExported", { name: database }));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

type TableDataExportLabelKey =
  | "database.csvFiles"
  | "database.jsonFiles"
  | "database.xlsxFiles"
  | "database.markdownFiles"
  | "database.sqlFiles"
  | "database.txtFiles";

interface TableDataExportMeta {
  /** 保存对话框过滤器的 i18n key */
  labelKey: TableDataExportLabelKey;
  pattern: string;
  browserMimeType: string;
}

const tableDataExportMeta: Record<TableDataFormat, TableDataExportMeta> = {
  csv: { labelKey: "database.csvFiles", pattern: "*.csv", browserMimeType: "text/csv" },
  json: { labelKey: "database.jsonFiles", pattern: "*.json", browserMimeType: "application/json" },
  xlsx: {
    labelKey: "database.xlsxFiles",
    pattern: "*.xlsx",
    browserMimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  },
  markdown: { labelKey: "database.markdownFiles", pattern: "*.md", browserMimeType: "text/markdown" },
  sql: { labelKey: "database.sqlFiles", pattern: "*.sql", browserMimeType: "application/sql" },
  txt: { labelKey: "database.txtFiles", pattern: "*.txt", browserMimeType: "text/plain" },
};

/** 导出 SQL（INSERT）格式时先让用户选择单条合并或每行独立语句；取消返回 undefined。 */
async function chooseExportSqlMode(format: TableDataFormat) {
  if (format !== "sql") return undefined;
  return insertSqlModeDialog.chooseInsertSqlMode();
}

/** 按格式将导出内容保存到磁盘；xlsx 为 base64 二进制，其余为文本。返回 false 表示用户取消。 */
async function saveExportedTableData(
  database: string,
  table: string,
  format: TableDataFormat,
  content: string
) {
  const meta = tableDataExportMeta[format];
  const options = {
    title: t("database.exportTableData"),
    filename: tableDataFilename(database, table, format),
    filters: [{ DisplayName: t(meta.labelKey), Pattern: meta.pattern }],
    browserMimeType: meta.browserMimeType,
  };
  if (tableDataFormatIsBinary(format)) {
    return saveBinaryFileToDisk({ ...options, contentBase64: content });
  }
  return saveTextFileToDisk({ ...options, content });
}

async function exportTableData(
  connection: DatabaseConnection,
  database: string,
  table: string,
  format: TableDataFormat
) {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  if (!(await ensureConnectionActive(connection))) return;
  if (!capabilitiesOf(connection).tableDataExport) {
    ElMessage.info(t("database.tableDataUnavailable"));
    return;
  }

  try {
    const mode = await chooseExportSqlMode(format);
    if (format === "sql" && !mode) return; // 用户取消不是错误
    const content = await invokeBackend<string>("db_export_table", {
      sessionId: databaseStore.activeSessionId,
      database,
      table,
      format,
      mode,
    });
    const saved = await saveExportedTableData(database, table, format, content);
    if (!saved) return;
    ElMessage.success(t("database.tableDataExported", { name: table }));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function importTableData(connection: DatabaseConnection, database: string, table: string) {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  if (!(await ensureConnectionActive(connection))) return;
  if (!capabilitiesOf(connection).tableDataImport) {
    ElMessage.info(t("database.tableDataUnavailable"));
    return;
  }

  const importSessionId = databaseStore.activeSessionId;
  const capturedScope = transferScope.capture();
  try {
    const path = await chooseNativeFile(t("database.importTableData"), [
      { DisplayName: t("database.csvFiles"), Pattern: "*.csv" },
      { DisplayName: t("database.jsonFiles"), Pattern: "*.json" },
    ]);
    if (!path) return;
    const format = tableDataFormatFromPath(path);
    if (!format) {
      ElMessage.warning(t("database.tableDataFormatUnsupported"));
      return;
    }
    const file = path.split(/[\\/]/).pop() || path;
    await ElMessageBox.confirm(
      t("database.importTableDataConfirm", { file, name: table }),
      t("database.importTableDataTitle"),
      {
        type: "warning",
        confirmButtonText: t("common.import"),
        cancelButtonText: t("common.cancel"),
      }
    );
    const count = await importSelectedTableData(importSessionId ?? "", database, table, format, path, capturedScope.scopeId);
    if (count === undefined || !transferScope.isCurrent(capturedScope)) return;
    await selectTable(database, table);
    ElMessage.success(t("database.tableDataImported", { count, name: table }));
  } catch (cause) {
    if (cause === "cancel" || cause === "close") return;
    ElMessage.error(String(cause));
  }
}

type DestructiveObjectAction = "drop_database" | "clear_database" | "drop_table" | "clear_table";

async function confirmDestructiveObjectOperation(
  connection: DatabaseConnection,
  action: DestructiveObjectAction,
  database: string,
  table?: string
) {
  if (!(await ensureConnectionActive(connection))) return;
  const target = table ? `${database}.${table}` : database;
  const actionKey = action.replace("_", "") as "dropdatabase" | "cleardatabase" | "droptable" | "cleartable";
  const isVectorObjectDelete =
    action === "drop_table" &&
    Boolean(table) &&
    isVectorDatabaseType(connection.dbType) &&
    connection.dbType !== "pgvector";
  const dialogTitle = isVectorObjectDelete
    ? t("database.deleteVectorObject", {
        kind:
          connection.dbType === "pinecone"
            ? t("database.vectorIndex")
            : t("database.vectorCollection"),
      })
    : t(`database.${actionKey}`);
  // 原有的“输入 库.表”确认：安全网关闭（检查为低风险）时仍使用它。
  async function promptTarget() {
    try {
      await ElMessageBox.prompt(
        t("database.destructiveObjectConfirm", { target }),
        dialogTitle,
        {
          type: "error",
          confirmButtonText: t("database.confirmDestructiveObject"),
          cancelButtonText: t("common.cancel"),
          inputPlaceholder: target,
          inputValidator: (input) => input === target || t("database.destructiveObjectConfirmationMismatch"),
          distinguishCancelAndClose: true,
        }
      );
      return {};
    } catch (cause) {
      if (cause === "cancel" || cause === "close") return undefined;
      throw cause;
    }
  }
  const sessionId = databaseStore.activeSessionId;
  if (!sessionId) {
    ElMessage.warning(t("database.connectionNotConnected"));
    return;
  }
  try {
    // 安全网开启时整表 / 整库操作为高风险：由安全确认框列出影响并要求输入目标名，
    // 输入通过即视为人工确认（后端仍按 库.表 校验 confirmation）。
    const done = await runWithSafety({
      sessionId,
      database,
      operation: { kind: "destructive", action, ...(table ? { table } : {}) },
      source: "query",
      origin: "user",
      confirm: (check, origin, notice) =>
        check.risk === "high" || notice ? confirmSqlSafety(check, origin, notice) : promptTarget(),
      execute: async (token) => {
        await invokeBackend<void>("db_destructive_operation", {
          sessionId,
          action,
          database,
          table,
          confirmation: target,
          ...sqlSafetyArgs(token),
        });
        return true;
      },
    });
    if (!done) return;
    closeAffectedTableTabs(connection.id, database, table);
    await databaseStore.refreshSchema();
    ElMessage.success(t("database.destructiveObjectCompleted", { target }));
  } catch (cause) {
    if (cause === "cancel" || cause === "close") return;
    ElMessage.error(String(cause));
  }
}

function openVectorCollectionDialog(connection: DatabaseConnection, database: string) {
  vectorCollectionTarget.value = { connection, database };
  vectorCollectionDialogVisible.value = true;
}

async function createVectorCollection(request: VectorCollectionCreateRequest) {
  const target = vectorCollectionTarget.value;
  if (!target || !(await ensureConnectionActive(target.connection))) return;
  vectorCollectionSaving.value = true;
  try {
    await invokeBackend<void>("db_vector_create_collection", {
      sessionId: databaseStore.activeSessionId,
      ...request,
    });
    await databaseStore.refreshSchema();
    vectorCollectionDialogVisible.value = false;
    ElMessage.success(t("database.vectorObjectCreated", { name: request.name }));
  } catch (cause) {
    ElMessage.error(String(cause));
  } finally {
    vectorCollectionSaving.value = false;
  }
}

async function openCharsetDialog(
  connection: DatabaseConnection,
  target: DatabaseCharsetTarget
) {
  if (!capabilitiesOf(connection).charsetChange) return;
  if (!(await ensureConnectionActive(connection))) return;

  const requestId = ++charsetDialogRequestId;
  charsetTarget.value = target;
  charsetConnectionId.value = connection.id;
  charsetOptions.value = [];
  currentCharset.value = undefined;
  charsetDialogVisible.value = true;
  charsetDialogLoading.value = true;
  try {
    const optionsResult = await databaseStore.executeQueryForConnection(
      connection.id,
      charsetOptionsQuery(),
      target.database
    );
    const currentResult = await databaseStore.executeQueryForConnection(
      connection.id,
      currentCharsetQuery(target),
      target.database
    );
    if (requestId !== charsetDialogRequestId) return;
    if (!optionsResult || !currentResult) throw new Error(t("database.charsetOptionsUnavailable"));
    charsetOptions.value = parseCharsetOptions(optionsResult);
    currentCharset.value = parseCurrentCharset(currentResult);
    if (!charsetOptions.value.length) throw new Error(t("database.charsetOptionsUnavailable"));
  } catch (cause) {
    if (requestId !== charsetDialogRequestId) return;
    charsetDialogVisible.value = false;
    ElMessage.error(String(cause));
  } finally {
    if (requestId === charsetDialogRequestId) charsetDialogLoading.value = false;
  }
}

async function applyCharsetChange(selection: DatabaseCharsetSelection) {
  const target = charsetTarget.value;
  const connectionId = charsetConnectionId.value;
  if (!target || !connectionId || charsetChangeApplying.value) return;
  const sql = buildCharsetChangeSql(target, selection);
  try {
    await ElMessageBox.confirm(
      `${t("database.charsetChangeConfirm")}\n\n${sql}`,
      t("database.changeCharset"),
      {
        type: "warning",
        confirmButtonText: t("database.executeCharsetChange"),
        cancelButtonText: t("common.cancel"),
        distinguishCancelAndClose: true,
      }
    );
    charsetChangeApplying.value = true;
    const changed = await databaseStore.executeQueryForConnection(connectionId, sql, target.database, {
      safety: { origin: "preconfirmed", source: "designer" },
    });
    if (!changed) return;
    if (databaseStore.activeConnectionId === connectionId) await databaseStore.refreshSchema();
    if (target.kind === "table") {
      if (
        activeObject.value === "table" &&
        activeTableTab.value?.database === target.database &&
        activeTableTab.value?.table === target.table
      ) {
        await databaseStore.openTable(
          target.database,
          target.table,
          databaseStore.page,
          databaseStore.pageSize
        );
      }
      if (
        detailsVisible.value &&
        tableDetailsTarget.value?.database === target.database &&
        tableDetailsTarget.value?.table === target.table
      ) {
        await loadTableDetails(target.database, target.table);
      }
    }
    charsetDialogVisible.value = false;
    ElMessage.success(t("database.charsetChangeApplied"));
  } catch (cause) {
    if (cause !== "cancel" && cause !== "close") ElMessage.error(String(cause));
  } finally {
    charsetChangeApplying.value = false;
  }
}

async function handleDatabaseMenuCommand(
  connection: DatabaseConnection,
  database: string,
  tables: SchemaTable[],
  command: unknown
) {
  switch (String(command)) {
    case "new-query":
      await openSchemaNewQuery(connection, database);
      return;
    case "new-table":
      await openNewTableTemplate(connection, database);
      return;
    case "new-vector-collection":
      if (await ensureConnectionActive(connection)) openVectorCollectionDialog(connection, database);
      return;
    case "copy-name":
      await copyObjectName(database);
      return;
    case "change-charset":
      await openCharsetDialog(connection, { kind: "database", database });
      return;
    case "export-schema":
      await exportSchema(connection, database, tables);
      return;
    case "import-sql":
      databaseStore.selectedDatabase = database;
      await openExternalSqlFile(connection);
      return;
    case "refresh":
      if (await ensureConnectionActive(connection)) await refreshSchema();
      return;
    case "clear-database":
      await confirmDestructiveObjectOperation(connection, "clear_database", database);
      return;
    case "drop-database":
      await confirmDestructiveObjectOperation(connection, "drop_database", database);
  }
}

async function handleTableMenuCommand(
  connection: DatabaseConnection,
  database: string,
  table: SchemaTable,
  command: unknown
) {
  const tableName = table.name;
  switch (String(command)) {
    case "open":
      await selectConnectionTable(connection, database, tableName);
      return;
    case "query":
      await openTableForInlineSearch(connection, database, tableName);
      return;
    case "copy-name":
      await copyObjectName(qualifiedTableName(connection, database, tableName));
      return;
    case "copy-ddl":
      await copyTableDdl(connection, database, tableName);
      return;
    case "change-charset":
      if (table.kind.toLowerCase() !== "table") return;
      await openCharsetDialog(connection, { kind: "table", database, table: tableName });
      return;
    case "export-csv":
      await exportTableData(connection, database, tableName, "csv");
      return;
    case "export-json":
      await exportTableData(connection, database, tableName, "json");
      return;
    case "export-xlsx":
      await exportTableData(connection, database, tableName, "xlsx");
      return;
    case "export-markdown":
      await exportTableData(connection, database, tableName, "markdown");
      return;
    case "export-sql":
      await exportTableData(connection, database, tableName, "sql");
      return;
    case "export-txt":
      await exportTableData(connection, database, tableName, "txt");
      return;
    case "import-data":
      await importTableData(connection, database, tableName);
      return;
    case "details":
      await openTableDetailsFromTree(connection, database, tableName);
      return;
    case "clear-table":
      await confirmDestructiveObjectOperation(connection, "clear_table", database, tableName);
      return;
    case "drop-table":
      await confirmDestructiveObjectOperation(connection, "drop_table", database, tableName);
  }
}

async function duplicateConnection(connection: DatabaseConnection) {
  try {
    const now = Date.now();
    const duplicate: DatabaseConnection = {
      ...structuredClone(toRaw(connection)),
      id: crypto.randomUUID(),
      name: `${connection.name} ${t("database.copySuffix")}`,
      lastConnectedAt: undefined,
      createdAt: now,
      updatedAt: now,
    };
    await databaseStore.saveConnection(duplicate);
    const groupName = duplicate.group?.trim() || t("database.ungroupedConnections");
    connectionSidebar.value?.setConnectionGroupExpanded(groupName, true);
    ElMessage.success(t("database.connectionDuplicated"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function copyConnectionInfo(connection: DatabaseConnection) {
  const lines: string[] = [
    `Name: ${connection.name}`,
    `Type: ${connection.dbType}`,
  ];
  if (connection.host) lines.push(`Host: ${connection.host}`);
  if (connection.port) lines.push(`Port: ${connection.port}`);
  if (connection.username) lines.push(`Username: ${connection.username}`);
  if (connection.password) lines.push(`Password: ${connection.password}`);
  if (connection.database) lines.push(`Database: ${connection.database}`);
  if (connection.token) lines.push(`Token: ${connection.token}`);
  if (connection.filePath) lines.push(`File: ${connection.filePath}`);
  if (connection.description) lines.push(`Description: ${connection.description}`);
  try {
    await copyText(lines.join("\n"));
    ElMessage.success(t("database.copyConnectionInfoSuccess"));
  } catch (cause) {
    ElMessage.error(t("database.copyFailed").replace("{message}", String(cause)));
  }
}

async function toggleConnectionFavorite(connection: DatabaseConnection) {
  try {
    await databaseStore.saveConnection({ ...connection, favorite: !connection.favorite });
    ElMessage.success(
      connection.favorite ? t("database.favoriteRemoved") : t("database.favoriteAdded")
    );
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function renameConnectionGroup(groupName: string) {
  if (groupName === t("database.ungroupedConnections")) return;
  try {
    const { value } = await ElMessageBox.prompt(
      t("database.renameGroupPrompt"),
      t("database.renameGroup"),
      {
        inputValue: groupName,
        inputValidator: (input) => input.trim().length > 0 || t("database.renameGroupRequired"),
        confirmButtonText: t("common.confirm"),
        cancelButtonText: t("common.cancel"),
      }
    );
    const nextName = value.trim();
    if (!nextName || nextName === groupName) return;

    const groupConnections = databaseStore.connections.filter(
      (connection) => connection.group?.trim() === groupName
    );
    await Promise.all(
      groupConnections.map((connection) =>
        databaseStore.saveConnection({ ...connection, group: nextName })
      )
    );
    await databaseStore.replaceConnectionGroupOrderName(groupName, nextName);
    connectionSidebar.value?.transferConnectionGroupCollapse(groupName, nextName);
    ElMessage.success(t("database.groupRenamed"));
  } catch (cause) {
    const reason = String(cause);
    if (reason !== "cancel" && reason !== "close") ElMessage.error(reason);
  }
}

async function handleConnectionMenuCommand(connection: DatabaseConnection, command: unknown) {
  switch (String(command)) {
    case "connect":
      await activateConnection(connection);
      return;
    case "disconnect":
      await disconnectConnection(connection);
      return;
    case "new-query":
      if (await showConnectionContent(connection)) newQuery();
      return;
    case "scripts":
      await openConnectionScripts(connection);
      return;
    case "open-sql":
      await openExternalSqlFile(connection);
      return;
    case "duplicate":
      await duplicateConnection(connection);
      return;
    case "copy-info":
      await copyConnectionInfo(connection);
      return;
    case "favorite":
      await toggleConnectionFavorite(connection);
      return;
    case "edit":
      editConnection(connection);
      return;
    case "notes-new":
    case "notes-view":
      await linkedNotes.open(
        { objectType: "database", objectId: connection.id, nameSnapshot: connection.name },
        { createNew: command === "notes-new" },
      );
      return;
    case "delete":
      await removeConnection(connection);
  }
}

async function openConnectionAudit(connection: DatabaseConnection) {
  if (!(await ensureConnectionActive(connection))) return;
  connectionSidebar.value?.setConnectionExpanded(connection, true);
  diagnosticsVisible.value = true;
}

function openNewConnection() {
  editingConnection.value = undefined;
  connectionDialogVisible.value = true;
}

function fileDatabaseConnectionName(fileName: string) {
  const name = fileName.replace(/\.(?:sqlite3?|duckdb|ddb|db)$/i, "").trim();
  return name || fileName;
}

let databaseFileOpenQueue: Promise<void> = Promise.resolve();

/**
 * 打开/拖入数据库文件。按顺序逐个处理，并等已保存的连接加载完成后再按文件路径查找已有连接：
 * 否则同一文件的并发打开（或启动时连接尚未加载）会各自新建连接并再次弹出凭据确认。
 */
function openDroppedDatabaseFiles(files: string[]) {
  const task = databaseFileOpenQueue.then(() => openDatabaseFilesInOrder(files));
  databaseFileOpenQueue = task.catch(() => undefined);
  return task;
}

async function openDatabaseFilesInOrder(files: string[]) {
  await databaseStore.initialize();
  for (const filePath of files) {
    try {
      const detected = await invokeBackend<DetectedFileDatabase>("db_detect_file_type", {
        filePath,
      });
      const existing = databaseStore.connections.find(
        (connection) => connection.filePath === detected.filePath,
      );
      const now = Date.now();
      const draft: DatabaseConnection = {
        id: crypto.randomUUID(),
        name: fileDatabaseConnectionName(detected.fileName),
        dbType: detected.dbType,
        host: "",
        port: databaseDefaultPorts[detected.dbType],
        filePath: detected.filePath,
        ssl: false,
        savePassword: false,
        createdAt: now,
        updatedAt: now,
      };
      if (!existing && detected.builtinDriver === false) {
        ElMessage.warning(t("database.driverUnavailable"));
        continue;
      }
      const connection = existing ?? draft;
      if (!existing) await databaseStore.saveConnection(connection);
      await activateConnection(connection);
    } catch (cause) {
      ElMessage.error(t("database.fileDropFailed", { reason: String(cause) }));
    }
  }
}
function editConnection(connection: DatabaseConnection) {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  editingConnection.value = connection;
  connectionDialogVisible.value = true;
}
async function removeConnection(connection: DatabaseConnection) {
  try {
    await ElMessageBox.confirm(
      t("database.deleteConnectionConfirm", { name: connection.name }),
      t("database.deleteConnection"),
      {
        type: "warning",
        confirmButtonText: t("common.delete"),
        cancelButtonText: t("common.cancel"),
      }
    );
  } catch {
    return;
  }

  try {
    await databaseStore.deleteConnection(connection.id);
    ElMessage.success(t("database.deleteConnection"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}
function desktopOnly() {
  ElMessage.warning(t("database.desktopRequired"));
}
function formatCell(value: unknown) {
  if (value == null) return "NULL";
  return typeof value === "object" ? JSON.stringify(value) : String(value);
}
function openCellDetail(column: string, value: unknown) {
  cellDetail.value = { column, value };
  cellDetailVisible.value = true;
}
function closeGridContextMenu() {
  gridContextMenu.value = undefined;
}
// A cell menu never outlives its grid or the view.
watch(activeGridKey, closeGridContextMenu);
onDeactivated(closeGridContextMenu);
function selectedRowCountForContext(context: ResultGridContextMenu) {
  if (selectedRowIndexes.value.length > 1) return selectedRowIndexes.value.length;
  return selectedRowIndexSet.value.has(context.rowIndex) ? selectedRowIndexes.value.length : 1;
}
function openGridContextMenu(payload: ResultGridContextMenu) {
  const menuWidth = 218;
  const menuHeight = 520;
  gridContextMenu.value = {
    ...payload,
    x: Math.max(8, Math.min(payload.x, window.innerWidth - menuWidth - 8)),
    y: Math.max(8, Math.min(payload.y, window.innerHeight - menuHeight - 8)),
  };
}
function openGridHeaderContextMenu(payload: ResultGridHeaderContextMenu) {
  const menuWidth = 218;
  const menuHeight = 380;
  gridHeaderContextMenu.value = {
    ...payload,
    x: Math.max(8, Math.min(payload.x, window.innerWidth - menuWidth - 8)),
    y: Math.max(8, Math.min(payload.y, window.innerHeight - menuHeight - 8)),
  };
}
function closeGridHeaderContextMenu() {
  gridHeaderContextMenu.value = undefined;
}
async function copyGridHeaderColumnName() {
  const menu = gridHeaderContextMenu.value;
  if (!menu) return;
  closeGridHeaderContextMenu();
  try {
    await copyText(menu.column);
    ElMessage.success(t("database.columnNameCopied"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}
async function copyGridHeaderAllColumnNames() {
  if (!resultColumns.value.length) return;
  closeGridHeaderContextMenu();
  try {
    await copyText(resultColumns.value.join(", "));
    ElMessage.success(t("database.columnNameCopied"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}
function gridHeaderColumnInfo() {
  const menu = gridHeaderContextMenu.value;
  if (!menu) return;
  return databaseStore.tableColumns.find((column) => column.name === menu.column);
}
function canCopyGridHeaderAlterSql() {
  return Boolean(
    gridHeaderContextMenu.value &&
    activeObject.value === "table" &&
    activeConnection.value &&
    selectedTable.value &&
    supportsCopyAsAlterSql(activeDialectType.value ?? activeConnection.value.dbType) &&
    gridHeaderColumnInfo()
  );
}
async function copyGridHeaderAlterSql() {
  const connection = activeConnection.value;
  const column = gridHeaderColumnInfo();
  const table = selectedTable.value;
  if (!connection || !column || !table) return;
  closeGridHeaderContextMenu();
  try {
    await copyText(
      buildColumnAlterSql({
        dbType: activeDialectType.value ?? connection.dbType,
        database: selectedDatabase.value,
        table,
        column,
      })
    );
    ElMessage.success(t("database.alterSqlCopied"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}
function hideGridHeaderColumn() {
  const menu = gridHeaderContextMenu.value;
  if (!menu) return;
  closeGridHeaderContextMenu();
  setColumnVisibility(menu.column, false);
}
function freezeGridHeaderUpToColumn() {
  const menu = gridHeaderContextMenu.value;
  if (!menu) return;
  const index = visibleColumnEntries.value.findIndex((column) => column.name === menu.column);
  closeGridHeaderContextMenu();
  if (index >= 0) setFrozenColumnCount(index + 1);
}
function unfreezeGridColumns() {
  closeGridHeaderContextMenu();
  setFrozenColumnCount(0);
}
function bulkUpdateGridHeaderColumn(mode: "value" | "generator") {
  const menu = gridHeaderContextMenu.value;
  if (!menu) return;
  closeGridHeaderContextMenu();
  openBulkUpdate(menu.column, resultRows.value.map((_, index) => index), mode);
}
function sortGridContextColumn(direction: "asc" | "desc") {
  const context = gridContextMenu.value;
  if (!context) return;
  updateActiveGridState({ sort: { column: context.column, direction } });
}
function clearGridSort() {
  updateActiveGridState({ sort: undefined });
}
function clearGridSelection() {
  updateSelectedGridRows(new Set());
}
function viewGridColumnDetails() {
  closeGridHeaderContextMenu();
  closeGridContextMenu();
  void showTableDetails();
}
function viewGridContextRowDetail() {
  const context = gridContextMenu.value;
  if (!context) return;
  closeGridContextMenu();
  editRecord(context.rowIndex);
}
function refreshActiveGrid() {
  closeGridHeaderContextMenu();
  closeGridContextMenu();
  if (activeObject.value === "query") {
    void runQuery();
    return;
  }
  void refreshSelectedTable();
}
/** 当前表标签按原页重新加载；selectTable 对已打开的表是空操作，不能用来刷新。 */
async function refreshSelectedTable() {
  if (!(await confirmLeavePendingTable())) return;
  if (activeObject.value === "table" && activeTableTab.value) {
    void refreshTablePage();
    return;
  }
  if (selectedTable.value) void selectTable(selectedDatabase.value, selectedTable.value);
}
function forgetActiveTableLastPages() {
  if (activeTableTab.value) forgetTableLastPages(activeTableTab.value.id);
}
function currentTableQueryTemplate() {
  const target = queryTemplateTarget.value;
  if (!target) return "";
  return buildDatabaseQueryTemplate({
    dbType: databaseStore.connectionDialectType(target.connection),
    database: target.database,
    table: target.table,
    limit: 100,
  });
}

async function regenerateQueryTemplate() {
  const tab = activeQueryTab.value;
  const template = currentTableQueryTemplate();
  if (!tab || !template) {
    ElMessage.info(t("database.queryTemplateNeedsTable"));
    return;
  }

  if (tab.sql.trim() && tab.sql.trim() !== template) {
    try {
      await ElMessageBox.confirm(
        t("database.regenerateQueryConfirm"),
        t("database.generateQuery"),
        {
          type: "warning",
          confirmButtonText: t("common.confirm"),
          cancelButtonText: t("common.cancel"),
        }
      );
    } catch {
      return;
    }
  }

  tab.sql = template;
  tab.result = undefined;
  tab.error = undefined;
  tab.status = "idle";
  ElMessage.success(t("database.queryTemplateGenerated"));
}

function openAiQueryDialog() {
  if (!activeQueryTab.value || !activeConnection.value) return;
  aiQueryDialogVisible.value = true;
}

async function applyAiQuery(query: string) {
  const tab = activeQueryTab.value;
  const trimmed = query.trim();
  if (!tab || !trimmed) return;

  if (tab.sql.trim() && tab.sql.trim() !== trimmed) {
    try {
      await ElMessageBox.confirm(
        t("database.aiQueryApplyConfirm"),
        t("database.aiGenerateQuery"),
        {
          type: "warning",
          confirmButtonText: t("common.confirm"),
          cancelButtonText: t("common.cancel"),
        }
      );
    } catch {
      return;
    }
  }

  tab.sql = trimmed;
  tab.result = undefined;
  tab.error = undefined;
  tab.status = "idle";
}
// 关闭含有用户编辑 SQL 的查询标签前确认；返回 false 表示用户取消，调用方必须中止关闭。
async function confirmDiscardQueryTabs(names: readonly string[]) {
  const tabIds = names
    .filter((name) => name.startsWith("query:"))
    .map((name) => name.slice("query:".length));
  const unsaved = queryTabsWithUnsavedSql(tabIds);
  if (!unsaved.length) return true;
  const firstTab = queryTabs.value.find((tab) => tab.id === unsaved[0]);
  try {
    await ElMessageBox.confirm(
      unsaved.length === 1
        ? t("database.closeQueryTabConfirm", { name: firstTab?.name ?? "" })
        : t("database.closeQueryTabsConfirm", { count: unsaved.length }),
      t("project.unsavedTitle"),
      {
        type: "warning",
        confirmButtonText: t("project.discard"),
        cancelButtonText: t("common.cancel"),
      }
    );
    return true;
  } catch {
    return false;
  }
}

async function removeDatabaseObjectTab(name: string | number) {
  if (typeof name !== "string") return;
  if (!(await confirmDiscardQueryTabs([name]))) return;
  if (name === `table:${pendingTableTab.value?.id}` && !(await confirmLeavePendingTable())) return;
  closeDatabaseObjectTab(name);
}

function closeDatabaseObjectTab(name: string) {
  if (name.startsWith("query:")) {
    closeQueryTab(name.slice("query:".length));
    return;
  }
  if (name.startsWith("table:")) closeTableTab(name.slice("table:".length));
}

function tableTabId(connectionId: string, database: string, table: string) {
  return `${connectionId}:${database}:${table}`;
}

function removeTableGridState(tabId: string) {
  const states = { ...resultGridStates.value };
  delete states[`table:${tabId}`];
  resultGridStates.value = states;
}

async function activateTableTab(tabId: string) {
  const tab = tableTabs.value.find((item) => item.id === tabId);
  if (!tab) return;
  if (!(await confirmLeavePendingTable({ tabId }))) return;
  const connection = databaseStore.connections.find((item) => item.id === tab.connectionId);
  if (!connection || !connectionIsConnected(connection)) return;

  if (!connectionIsActive(connection)) databaseStore.activateConnection(connection.id);
  activeTableTabId.value = tab.id;
  inlineCellEdit.value = undefined;
  activeObject.value = "table";
  const search = tableServerSearches.value[tab.id];
  // selectedDatabase/selectedTable 是全局单值，切到别的标签后必然不匹配，
  // 因此这里靠标签自己记录的页码还原，而不是回到第一页。
  const restoredPage = tablePagePositions.value[tab.id] ?? 1;
  const isLoadedTable =
    databaseStore.selectedDatabase === tab.database && databaseStore.selectedTable === tab.table;

  try {
    if (!isLoadedTable) {
      // Metadata and primary keys belong to the selected table, so hydrate them
      // before replacing the first page with the server-side search result.
      await openTablePage(tab, search ? 1 : restoredPage, databaseStore.pageSize);
    }
    if (search) await loadTableSearchPage(tab, search, restoredPage, databaseStore.pageSize);
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

function closeAffectedTableTabs(connectionId: string, database: string, table?: string) {
  const affected = tableTabs.value.filter(
    (tab) =>
      tab.connectionId === connectionId &&
      tab.database === database &&
      (!table || tab.table === table)
  );
  for (const tab of affected) closeTableTab(tab.id);
}

function closeTableTab(tabId: string) {
  const index = tableTabs.value.findIndex((tab) => tab.id === tabId);
  if (index < 0) return;
  const wasActive = activeTableTabId.value === tabId;
  tableTabs.value.splice(index, 1);
  pendingChanges.forgetTab(tabId);
  removeTableServerSearch(tabId);
  removeTableGridState(tabId);
  forgetTablePage(tabId);
  if (!wasActive) return;

  const next = tableTabs.value[index] ?? tableTabs.value[index - 1];
  if (next) {
    void activateTableTab(next.id);
    return;
  }

  activeTableTabId.value = undefined;
  databaseStore.closeTable();
  if (databaseStore.selectedDatabase) activeObject.value = "database";
  else if (queryTabs.value.length) activeObject.value = "query";
}

async function connect(connection: DatabaseConnection) {
  if (connectionIsConnecting(connection)) return false;
  if (!databaseStore.desktopRuntime) {
    desktopOnly();
    return false;
  }
  const supported = await loadSupportedDatabaseTypes();
  if (connection.driver && supported?.has(connection.dbType)) {
    ElMessage.warning(t("database.confirmBuiltinDriverHint"));
    editConnection(connection);
    return false;
  }
  if (!databaseStore.connectionDescriptor(connection)?.available || (supported && !supported.has(connection.dbType))) {
    ElMessage.warning(t("database.driverUnavailable"));
    return false;
  }
  connectingConnectionIds.value = {
    ...connectingConnectionIds.value,
    [connection.id]: true,
  };
  try {
    await databaseStore.connect(connection.id);
    ElMessage.success(`${connection.name} ${t("database.connected")}`);
    return true;
  } catch (cause) {
    ElMessage.error(connectErrorMessage(cause));
    return false;
  } finally {
    const next = { ...connectingConnectionIds.value };
    delete next[connection.id];
    connectingConnectionIds.value = next;
  }
}

async function connectFromOverview(connection: DatabaseConnection) {
  if (!(await connect(connection))) return;
  await activateConnection(connection);
}

async function removeDatabaseConnectionTab(name: string | number) {
  if (typeof name !== "string") return;
  const connection = connectedConnections.value.find((item) => item.id === name);
  if (!connection) return;
  if (pendingTableTab.value?.connectionId === connection.id && !(await confirmLeavePendingTable())) return;
  if (selectedDatabaseConnectionTab.value === connection.id) {
    selectedDatabaseConnectionTab.value = DATABASE_CONNECTION_OVERVIEW_TAB;
  }
  void disconnectConnection(connection);
}

async function disconnectConnection(connection: DatabaseConnection) {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  if (pendingTableTab.value?.connectionId === connection.id && !(await confirmLeavePendingTable())) return;
  try {
    const closingActiveTable = activeTableTab.value?.connectionId === connection.id;
    await databaseStore.disconnect(connection.id);

    connectionSidebar.value?.setConnectionExpanded(connection, false);
    const closingTableIds = tableTabs.value
      .filter((tab) => tab.connectionId === connection.id)
      .map((tab) => tab.id);
    tableTabs.value = tableTabs.value.filter((tab) => tab.connectionId !== connection.id);
    for (const tabId of closingTableIds) {
      pendingChanges.forgetTab(tabId);
      removeTableServerSearch(tabId);
      removeTableGridState(tabId);
      forgetTablePage(tabId);
    }
    if (closingActiveTable) {
      activeTableTabId.value = undefined;
      if (databaseStore.selectedDatabase) activeObject.value = "database";
      else if (queryTabs.value.length) activeObject.value = "query";
    }
    ElMessage.success(t("database.disconnected", { name: connection.name }));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function refreshSchema() {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  try {
    await databaseStore.refreshSchema();
    ElMessage.success(t("database.schemaRefreshed"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function openTableForInlineSearch(
  connection: DatabaseConnection,
  database: string,
  table: string
) {
  if (!(await showConnectionContent(connection))) return;
  await selectTable(database, table);
  await nextTick();
  tableSearchField.value?.focusSearchField();
}

async function selectTable(database: string, table: string) {
  if (databaseStore.isRedis || !activeConnection.value) return;
  const connectionId = activeConnection.value.id;
  const id = tableTabId(connectionId, database, table);
  if (!(await confirmLeavePendingTable({ tabId: id }))) return;
  const isCurrentTable =
    activeTableTabId.value === id &&
    databaseStore.selectedDatabase === database &&
    databaseStore.selectedTable === table;
  if (!tableTabs.value.some((tab) => tab.id === id)) {
    tableTabs.value.push({ id, connectionId, database, table });
  }
  activeTableTabId.value = id;
  inlineCellEdit.value = undefined;
  activeObject.value = "table";
  const tab = tableTabs.value.find((item) => item.id === id);
  if (!tab) return;
  if (isCurrentTable) return;
  if (loadingTableIds.has(id)) return;

  loadingTableIds.add(id);
  try {
    const search = tableServerSearches.value[id];
    if (search) {
      databaseStore.activateTableContext(database, table);
      await loadTableSearchPage(tab, search, 1, databaseStore.pageSize);
    } else {
      await openTablePage(tab, 1, databaseStore.pageSize);
    }
  } catch (cause) {
    ElMessage.error(String(cause));
  } finally {
    loadingTableIds.delete(id);
  }
}

async function testConnection(connection: DatabaseConnection) {
  testingConnection.value = true;
  try {
    const schema = await databaseStore.testConnection(connection);
    ElMessage.success(t("database.testSuccess", { count: schema.databases.length }));
  } catch (cause) {
    ElMessage.error(t("database.testFailed", { error: connectErrorMessage(cause) }));
  } finally {
    testingConnection.value = false;
  }
}
async function saveConnection(connection: DatabaseConnection) {
  savingConnection.value = true;
  try {
    await databaseStore.saveConnection(connection);
    const groupName = connection.group?.trim() || t("database.ungroupedConnections");
    connectionSidebar.value?.setConnectionGroupExpanded(groupName, true);
    connectionDialogVisible.value = false;
    editingConnection.value = undefined;
    ElMessage.success(t("database.connectionSaved"));
    if (databaseStore.desktopRuntime) await connect(connection);
  } catch (cause) {
    ElMessage.error(String(cause));
  } finally {
    savingConnection.value = false;
  }
}

async function exportConnectionConfig() {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  const choice = await confirmSecretExport();
  if (!choice) return;
  const includeSecrets = choice === "with-secrets";
  try {
    const path = await chooseNativeSaveFile(
      t("database.configExport"),
      `mincode-database-connections-${new Date().toISOString().slice(0, 10)}.json`,
      [{ DisplayName: t("database.configFiles"), Pattern: "*.json" }]
    );
    if (!path) return;
    const connections = databaseStore.connections.map((connection) =>
      portableConnection(connection, { includeSecrets })
    );
    await invokeBackend<void>("local_fs_write_selected_text_file", {
      path,
      content: JSON.stringify(
        {
          version: 1,
          exportedAt: new Date().toISOString(),
          includesSecrets: includeSecrets,
          connections,
        },
        null,
        2
      ),
    });
    ElMessage.success(t("database.configExported", { count: connections.length }));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}

async function importConnectionConfig() {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  try {
    const path = await chooseNativeFile(t("database.configImport"), [
      { DisplayName: t("database.configFiles"), Pattern: "*.json" },
    ]);
    if (!path) return;
    const content = await invokeBackend<string>("local_fs_read_selected_text_file", { path });
    const parsed: unknown = JSON.parse(content);
    const includeSecrets = connectionConfigHasSecrets(parsed);
    if (includeSecrets && !(await confirmSecretImport())) return;
    const connections = parseConnectionConfig(parsed, t("database.configInvalid"), {
      includeSecrets,
    });
    for (const connection of connections) {
      await databaseStore.saveConnection(connection);
    }
    ElMessage.success(t("database.configImported", { count: connections.length }));
  } catch (cause) {
    ElMessage.error(cause instanceof Error ? cause.message : String(cause));
  }
}

async function exportData(format: TableDataFormat) {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  if (!selectedTable.value) return ElMessage.warning(t("database.selectTableFirst"));
  try {
    const mode: InsertSqlMode | undefined = await chooseExportSqlMode(format);
    if (format === "sql" && !mode) return; // 用户取消不是错误
    const content = await databaseStore.exportTable(format, mode);
    // WKWebView 不支持 <a download> 触发下载，桌面端改为原生保存对话框写盘。
    const saved = await saveExportedTableData(
      databaseStore.selectedDatabase,
      selectedTable.value,
      format,
      content
    );
    if (!saved) return;
    ElMessage.success(t("database.tableDataExported", { name: selectedTable.value }));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}
function chooseImport() {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  if (!selectedTable.value) return ElMessage.warning(t("database.selectTableFirst"));
  importInput.value?.click();
}
async function importData(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  const format = file.name.toLowerCase().endsWith(".json") ? "json" : "csv";
  try {
    await ElMessageBox.confirm(
      t("database.importDataConfirm", { file: file.name, table: selectedTable.value }),
      t("database.import"),
      { type: "warning" }
    );
  } catch {
    input.value = "";
    return;
  }
  try {
    const affected = await databaseStore.importData(format, await file.text());
    if (affected === undefined) return;
    forgetActiveTableLastPages();
    ElMessage.success(t("database.importedRows", { count: affected }));
  } catch (cause) {
    ElMessage.error(String(cause));
  } finally {
    input.value = "";
  }
}
async function loadOverviewDetails(object: SchemaTable) {
  const database = databaseStore.selectedDatabase;
  if (!database || !databaseStore.desktopRuntime) return;
  const requestId = ++overviewDetailsRequestId;
  overviewSelectedObjectName.value = object.name;
  overviewDetailsLoading.value = true;
  overviewDetails.value = undefined;
  try {
    const details = await databaseStore.getTableDetails(database, object.name);
    if (requestId !== overviewDetailsRequestId) return;
    overviewDetails.value = details;
  } catch (cause) {
    if (requestId === overviewDetailsRequestId) ElMessage.error(String(cause));
    return;
  } finally {
    if (requestId === overviewDetailsRequestId) overviewDetailsLoading.value = false;
  }

  void databaseStore
    .getTableDetailsSupplement(database, object.name)
    .then((supplement) => {
      if (requestId !== overviewDetailsRequestId || !overviewDetails.value) return;
      overviewDetails.value = {
        ...overviewDetails.value,
        ...supplement,
        loadErrors: [
          ...(overviewDetails.value.loadErrors ?? []),
          ...(supplement.loadErrors ?? []),
        ],
      };
    })
    .catch((cause) => {
      if (requestId !== overviewDetailsRequestId || !overviewDetails.value) return;
      overviewDetails.value = {
        ...overviewDetails.value,
        loadErrors: [
          ...(overviewDetails.value.loadErrors ?? []),
          `${t("database.supplementDetailsFailed")}: ${String(cause)}`,
        ],
      };
    });
}

function selectOverviewObject(object: SchemaTable) {
  void loadOverviewDetails(object);
}

const overviewContextMenu = ref<{ object: SchemaTable; x: number; y: number }>();
function openOverviewContextMenu(payload: { object: SchemaTable; x: number; y: number }) {
  overviewContextMenu.value = payload;
}
function closeOverviewContextMenu() {
  overviewContextMenu.value = undefined;
}
async function runOverviewTableCommand(command: string) {
  const menu = overviewContextMenu.value;
  if (!menu || !activeConnection.value) return;
  overviewContextMenu.value = undefined;
  await handleTableMenuCommand(
    activeConnection.value,
    databaseStore.selectedDatabase,
    menu.object,
    command
  );
}

// 对象标签（库/查询/表）右键菜单：el-tabs 导航项 id 为 tab-{paneName}，
// 事件委托在标签条容器上统一解析，三类标签共用。
const tabContextMenu = ref<{ name: string; x: number; y: number }>();

function openObjectTabContextMenu(event: MouseEvent) {
  const item = (event.target as HTMLElement | null)?.closest?.(".el-tabs__item");
  if (!item || !item.id.startsWith("tab-")) return;
  const name = item.id.slice("tab-".length);
  if (!name) return;
  event.preventDefault();
  tabContextMenu.value = { name, x: event.clientX, y: event.clientY };
}

function closeObjectTabContextMenu() {
  tabContextMenu.value = undefined;
}

const tabContextMenuKind = computed<"database" | "query" | "table">(() => {
  const name = tabContextMenu.value?.name ?? "";
  if (name.startsWith("table:")) return "table";
  if (name.startsWith("query:")) return "query";
  return "database";
});

// 可关闭标签（查询 + 表）按标签条顺序展开，左/右/其他/全部语义都基于这个序列。
function closableObjectTabNames(): string[] {
  return [
    ...queryTabs.value.map((tab) => `query:${tab.id}`),
    ...tableTabs.value.map((tab) => `table:${tab.id}`),
  ];
}

const tabContextMenuIndex = computed(() =>
  closableObjectTabNames().indexOf(tabContextMenu.value?.name ?? "")
);
const tabContextMenuHasLeft = computed(() => tabContextMenuIndex.value > 0);
const tabContextMenuHasRight = computed(
  () =>
    tabContextMenuIndex.value >= 0 &&
    tabContextMenuIndex.value < closableObjectTabNames().length - 1
);

// 批量关闭时把当前激活的标签排到最后，避免中途反复触发邻位激活与数据加载。
async function closeObjectTabsByNames(names: string[]) {
  if (!(await confirmDiscardQueryTabs(names))) return;
  if (names.includes(`table:${pendingTableTab.value?.id}`) && !(await confirmLeavePendingTable())) return;
  const activeName = activeDatabaseObjectTab.value;
  const ordered = [...names.filter((name) => name !== activeName)];
  if (names.includes(activeName)) ordered.push(activeName);
  for (const name of ordered) closeDatabaseObjectTab(name);
}

function runObjectTabContextCommand(command: string) {
  const menu = tabContextMenu.value;
  if (!menu) return;
  tabContextMenu.value = undefined;
  const names = closableObjectTabNames();
  const index = names.indexOf(menu.name);
  switch (command) {
    case "refresh": {
      const tab = tableTabs.value.find((item) => `table:${item.id}` === menu.name);
      if (tab) void selectTable(tab.database, tab.table);
      return;
    }
    case "copy-name": {
      const tableTab = tableTabs.value.find((item) => `table:${item.id}` === menu.name);
      const queryTab = queryTabs.value.find((item) => `query:${item.id}` === menu.name);
      const value = tableTab
        ? `${tableTab.database}.${tableTab.table}`
        : (queryTab?.name ?? databaseStore.selectedDatabase);
      if (value) void copyObjectName(value);
      return;
    }
    case "close":
      void removeDatabaseObjectTab(menu.name);
      return;
    case "close-left":
      if (index > 0) void closeObjectTabsByNames(names.slice(0, index));
      return;
    case "close-right":
      if (index >= 0) void closeObjectTabsByNames(names.slice(index + 1));
      return;
    case "close-other":
      void closeObjectTabsByNames(names.filter((name) => name !== menu.name));
      return;
    case "close-all":
      void closeObjectTabsByNames(names);
  }
}

async function openOverviewObject(object: SchemaTable) {
  await selectTable(databaseStore.selectedDatabase, object.name);
}

async function refreshDatabaseOverview() {
  const selectedName = overviewSelectedObjectName.value;
  await refreshSchema();
  if (!selectedName) return;
  const object = overviewObjects.value.find((item) => item.name === selectedName);
  if (object) await loadOverviewDetails(object);
  else {
    overviewSelectedObjectName.value = "";
    overviewDetails.value = undefined;
  }
}

async function editOverviewObjectStructure(object: SchemaTable) {
  tableDetailsTarget.value = {
    database: databaseStore.selectedDatabase,
    table: object.name,
  };
  detailsVisible.value = true;
  await loadTableDetails(tableDetailsTarget.value.database, object.name);
}

function closeOverviewDetails() {
  overviewDetailsRequestId += 1;
  overviewSelectedObjectName.value = "";
  overviewDetails.value = undefined;
  overviewDetailsLoading.value = false;
}

async function loadTableDetails(database: string, table: string) {
  const requestId = ++tableDetailsRequestId;
  detailsLoading.value = true;
  tableDetails.value = undefined;
  try {
    const details = await databaseStore.getTableDetails(database, table);
    if (requestId !== tableDetailsRequestId) return;
    tableDetails.value = details;
  } catch (cause) {
    if (requestId === tableDetailsRequestId) ElMessage.error(String(cause));
    return;
  } finally {
    if (requestId === tableDetailsRequestId) detailsLoading.value = false;
  }

  void databaseStore
    .getTableDetailsSupplement(database, table)
    .then((supplement) => {
      if (requestId !== tableDetailsRequestId || !tableDetails.value) return;
      tableDetails.value = {
        ...tableDetails.value,
        ...supplement,
        loadErrors: [...(tableDetails.value.loadErrors ?? []), ...(supplement.loadErrors ?? [])],
      };
    })
    .catch((cause) => {
      if (requestId !== tableDetailsRequestId || !tableDetails.value) return;
      tableDetails.value = {
        ...tableDetails.value,
        loadErrors: [...(tableDetails.value.loadErrors ?? []), `${t("database.supplementDetailsFailed")}: ${String(cause)}`],
      };
    });
}

async function showTableDetails() {
  const database = selectedDatabase.value;
  const table = selectedTable.value;
  if (!databaseStore.desktopRuntime) return desktopOnly();
  if (!table) return ElMessage.warning(t("database.selectTableFirst"));
  tableDetailsTarget.value = { database, table };
  detailsVisible.value = true;
  await loadTableDetails(database, table);
}

async function applySchemaChange(sql: string, statements?: string[]) {
  if (!activeConnection.value || !tableDetailsTarget.value || schemaChangeApplying.value) return;
  const target = tableDetailsTarget.value;
  try {
    await ElMessageBox.confirm(
      `${t("database.schemaChangeConfirm")}\n\n${sql}`,
      t("database.executeSchemaChange"),
      {
        type: "warning",
        confirmButtonText: t("database.executeSchemaChange"),
        cancelButtonText: t("common.cancel"),
        distinguishCancelAndClose: true,
      }
    );
    schemaChangeApplying.value = true;
    const executionStatements = statements?.length ? statements : [sql];
    // 安全确认被取消时停止后续语句；之前的语句可能已生效，仍刷新结构后再返回。
    let completed = true;
    for (const statement of executionStatements) {
      const executed = await databaseStore.executeQuery(statement, target.database, {
        safety: { origin: "preconfirmed", source: "designer" },
      });
      if (!executed) {
        completed = false;
        break;
      }
    }
    await databaseStore.refreshSchema();
    if (
      activeObject.value === "table" &&
      activeTableTab.value?.database === target.database &&
      activeTableTab.value?.table === target.table
    ) {
      await databaseStore.openTable(
        target.database,
        target.table,
        databaseStore.page,
        databaseStore.pageSize
      );
    }
    await loadTableDetails(target.database, target.table);
    if (
      activeObject.value === "database" &&
      databaseStore.selectedDatabase === target.database &&
      overviewSelectedObjectName.value === target.table
    ) {
      const object = overviewObjects.value.find((item) => item.name === target.table);
      if (object) await loadOverviewDetails(object);
    }
    if (completed) ElMessage.success(t("database.schemaChangeApplied"));
  } catch (cause) {
    if (cause !== "cancel" && cause !== "close") ElMessage.error(String(cause));
  } finally {
    schemaChangeApplying.value = false;
  }
}

async function showErDiagram() {
  if (!databaseStore.desktopRuntime) return desktopOnly();
  if (!selectedDatabase.value) return ElMessage.warning(t("database.selectDatabaseFirst"));
  erDiagramVisible.value = true;
  erDiagramLoading.value = true;
  try {
    erDiagram.value = await databaseStore.getDatabaseDiagram(selectedDatabase.value);
  } catch (cause) {
    ElMessage.error(String(cause));
  } finally {
    erDiagramLoading.value = false;
  }
}

async function openDiagramTable(table: string) {
  erDiagramVisible.value = false;
  await selectTable(selectedDatabase.value, table);
}

async function saveCurrentScript() {
  const result = await ElMessageBox.prompt(
    t("database.scriptNamePrompt"),
    t("database.saveScriptTitle"),
    {
      inputValue: selectedTable.value
        ? selectedTable.value
        : t("database.defaultScriptName"),
      inputPattern: /\S+/,
      inputErrorMessage: t("database.scriptNameRequired"),
    }
  ).catch(() => undefined);
  if (!result) return;
  const script: DatabaseSavedScript = {
    id: crypto.randomUUID(),
    name: result.value.trim(),
    sql: queryText.value,
    updatedAt: Date.now(),
  };
  savedScripts.value = [script, ...savedScripts.value];
  void savePersistedValue("app_db_scripts", savedScripts.value);
  connectionSidebar.value?.revealScriptsTab();
  ElMessage.success(t("database.scriptSaved"));
}
function openScript(script: DatabaseSavedScript) {
  const existing = queryTabs.value.find(
    (tab) => tab.name === script.name && tab.sql === script.sql
  );
  if (existing) {
    activeQueryTabId.value = existing.id;
    existing.resultLastAccessedAt = Date.now();
    activeObject.value = "query";
    return;
  }
  newQuery(script.sql, script.name);
}
async function deleteScript(script: DatabaseSavedScript) {
  try {
    await ElMessageBox.confirm(
      t("database.deleteScriptConfirm", { name: script.name }),
      t("database.deleteScript"),
      { type: "warning" }
    );
    savedScripts.value = savedScripts.value.filter((item) => item.id !== script.id);
    void savePersistedValue("app_db_scripts", savedScripts.value);
  } catch {
    // 用户取消删除。
  }
}
function openHistory(item: DatabaseQueryHistoryItem) {
  if (queryText.value.trim()) newQuery(item.query, t("database.historyQueryName"));
  else queryText.value = item.query;
}
function clearEditor() {
  queryText.value = "";
}
function formatEditorSql() {
  queryText.value = formatSqlForEditor(queryText.value);
}
function clearHistory() {
  const connectionId = activeConnection.value?.id;
  queryHistory.value = queryHistory.value.filter((item) => item.connectionId !== connectionId);
  void savePersistedValue("app_db_query_history", queryHistory.value);
}

const { unregisterAiDatabaseWorkspaceActions } = useAiDatabaseActions({
  queryTabs,
  activeQueryTabId,
  tableTabs,
  activeTableTabId,
  activateConnection,
  selectConnectionTable,
  newQuery,
  closeQueryTab,
  closeTableTab,
  tableTabHasPendingChanges: (tabId) => pendingChanges.tabId.value === tabId && pendingChanges.total.value > 0,
  openNewConnection,
  editConnection,
  connectionDialogVisible,
  duplicateConnection,
  toggleConnectionFavorite,
  openTableDetails: async (database: string, table: string) => {
    tableDetailsTarget.value = { database, table };
    detailsVisible.value = true;
    await loadTableDetails(database, table);
  },
  detailsVisible,
  transferConnectionGroupCollapse: (previousName, nextName) =>
    connectionSidebar.value?.transferConnectionGroupCollapse(previousName, nextName),
});

watch(
  () => databaseStore.activeConnectionId,
  (connectionId) => {
    if (!connectionId || !databaseStore.sessionIds[connectionId]) return;
    connectionSidebar.value?.setConnectionExpandedById(connectionId, true);
  }
);

const unregisterQueryTabsFlusher = registerUiSessionFlusher(persistQueryTabs);

watch(
  pendingDatabaseFiles,
  (files) => {
    if (files.length) void openDroppedDatabaseFiles(consumeDatabaseFiles());
  },
  { flush: "post" },
);

// Load the query workspace chunk once the page has initialised, so the first "new query" does
// not wait for it. The SQL editor is CodeMirror and needs no warm-up (Monaco stays for JSON and
// plain-text query languages, loaded when one is shown).
let databaseViewUnmounted = false;
function warmQueryEditor() {
  if (databaseViewUnmounted) return;
  void loadDatabaseQueryWorkspace().catch(() => undefined);
}
onMounted(async () => {
  const [scripts, history, storedQueryTabs] = await Promise.all([
    loadPersistedValue<unknown>("app_db_scripts"),
    loadPersistedValue<unknown>("app_db_query_history"),
    loadQueryTabsSnapshot(),
    hydrateExplorerLayout(),
  ]);
  if (Array.isArray(scripts)) savedScripts.value = scripts as DatabaseSavedScript[];
  if (Array.isArray(history)) queryHistory.value = history as DatabaseQueryHistoryItem[];
  const [storedFontSize, storedEditorPercent] = await Promise.all([
    loadPersistedValue<unknown>("app_db_query_editor_font_size"),
    queryEditorSizePersistence.load(),
  ]);
  if (typeof storedFontSize === "number" && Number.isFinite(storedFontSize)) queryEditorFontSize.value = Math.max(10, Math.min(24, storedFontSize));
  if (typeof storedEditorPercent === "number" && Number.isFinite(storedEditorPercent)) queryEditorPercent.value = Math.max(20, Math.min(75, storedEditorPercent));
  hydrateQueryTabs(storedQueryTabs);
  await Promise.all([
    savePersistedValue("app_db_scripts", savedScripts.value),
    savePersistedValue("app_db_query_history", queryHistory.value),
    persistQueryTabs(),
    databaseStore.initialize(),
  ]);
  const droppedFiles = consumeDatabaseFiles();
  if (droppedFiles.length) await openDroppedDatabaseFiles(droppedFiles);
  warmQueryEditor();
});
onBeforeUnmount(() => {
  databaseViewUnmounted = true;
  closeGridContextMenu();
  disposeTableFilters();
  unregisterAiDatabaseWorkspaceActions();
  unregisterQueryTabsFlusher();
  void persistQueryTabs();
});
</script>

<template>
  <section
    id="database-workspace-drop-target"
    class="db-workspace"
    data-file-drop-target
    :data-file-drop-label="t('database.fileDropHint')"
  >
    <AppSplitPane
      class="db-workspace__split"
      :model-value="explorerWidth"
      :collapsed="explorerCollapsed"
      :collapsed-size="0"
      layout="horizontal"
      side="first"
      :min="220"
      :max="500"
      :second-min="360"
      collapsible
      refresh-panel-on-collapse
      :collapse-label="t('database.collapseSidebar')"
      :expand-label="t('database.expandSidebar')"
      :aria-label="t('database.resizeSidebar')"
      @resize-end="persistExplorerWidth"
      @update:collapsed="updateExplorerCollapsed"
    >
      <template #first>
        <DatabaseConnectionSidebar
          ref="connectionSidebar"
          :explorer-collapsed="explorerCollapsed"
          :active-connection="activeConnection"
          :connecting-connection-ids="connectingConnectionIds"
          :selected-database="selectedDatabase"
          :selected-table="selectedTable"
          :saved-scripts="savedScripts"
          :active-connection-history="activeConnectionHistory"
          @new-connection="openNewConnection"
          @import-config="importConnectionConfig"
          @import-connections="connectionImportVisible = true"
          @export-config="exportConnectionConfig"
          @refresh-schema="refreshSchema"
          @activate-connection="activateConnection"
          @toggle-connection-expansion="toggleConnectionExpansion"
          @toggle-database-expansion="toggleDatabaseExpansion"
          @connection-menu="handleConnectionMenuCommand"
          @database-menu="handleDatabaseMenuCommand"
          @table-menu="handleTableMenuCommand"
          @rename-group="renameConnectionGroup"
          @open-scripts="openConnectionScripts"
          @open-external-sql="openExternalSqlFile"
          @open-audit="openConnectionAudit"
          @select-table="selectConnectionTable"
          @save-current-script="saveCurrentScript"
          @open-script="openScript"
          @delete-script="deleteScript"
          @open-history="openHistory"
          @clear-history="clearHistory"
        />
      </template>

      <template #second>
        <div class="db-main">
          <DatabaseTransferHistory />
      <div class="db-connection-tabs">
        <span class="db-connection-tabs__label">{{ t("database.connectionLabel") }}</span>
        <AppWorkspaceTabs
          v-model="activeDatabaseConnectionTab"
          class="db-connection-tabs__list"
          :before-leave="beforeLeaveConnectionTab"
          @tab-remove="removeDatabaseConnectionTab"
        >
          <el-tab-pane :name="DATABASE_CONNECTION_OVERVIEW_TAB">
            <template #label>
              <el-icon><Grid /></el-icon>
              <span class="db-connection-tab__label">{{ t("database.connectionOverview") }}</span>
            </template>
          </el-tab-pane>
          <el-tab-pane
            v-for="connection in connectedConnections"
            :key="connection.id"
            :name="connection.id"
            closable
          >
            <template #label>
              <el-icon><Connection /></el-icon>
              <span class="db-connection-tab__label">{{ connection.name }}</span>
              <b>{{ databaseTypeLabels[connection.dbType].toUpperCase() }}</b>
            </template>
          </el-tab-pane>
        </AppWorkspaceTabs>
      </div>

      <DatabaseConnectionOverview
        v-if="activeDatabaseConnectionTab === DATABASE_CONNECTION_OVERVIEW_TAB"
        :connections="databaseStore.connections"
        :connected-connection-ids="Object.keys(databaseStore.sessionIds)"
        :connecting-connection-ids="connectingConnectionIds"
        :schemas="databaseStore.schemas"
        :group-order="databaseStore.connectionGroupOrder"
        @connect="connectFromOverview"
        @open="activateConnection"
        @new-connection="openNewConnection"
        @import-config="importConnectionConfig"
      />

      <template v-else-if="activeConnection">
        <DatabaseDatasourceHeader
          :connection="activeConnection"
          @schema-sync="schemaSyncVisible = true"
          @backup="backupDialogVisible = true"
          @new-connection="openNewConnection"
          @refresh="refreshSchema"
          @edit="editConnection"
          @undone="onSafetyUndone"
        />

        <RedisWorkspacePanel v-if="databaseStore.desktopRuntime && databaseStore.isRedis" />
        <template v-else>
          <div class="db-object-tabs" @contextmenu="openObjectTabContextMenu">
            <AppWorkspaceTabs
              v-model="activeDatabaseObjectTab"
              class="db-object-tabs__list"
              :before-leave="beforeLeaveObjectTab"
              @tab-remove="removeDatabaseObjectTab"
            >
              <el-tab-pane
                v-if="databaseStore.selectedDatabase"
                :name="`database:${databaseStore.selectedDatabase}`"
              >
                <template #label>
                  <el-icon><Coin /></el-icon>
                  <span class="db-object-tab__label">{{ databaseStore.selectedDatabase }}</span>
                </template>
              </el-tab-pane>
              <el-tab-pane
                v-for="tab in queryTabs"
                :key="tab.id"
                :name="`query:${tab.id}`"
                :label="tab.name"
                closable
              />
              <el-tab-pane
                v-for="tab in tableTabs"
                :key="tab.id"
                :name="`table:${tab.id}`"
                closable
              >
                <template #label>
                  <span
                    class="db-object-tab__table-indicator"
                    :title="`${tab.database}.${tab.table}`"
                  />
                  <span class="db-object-tab__label">{{ tab.table }}</span>
                </template>
              </el-tab-pane>
            </AppWorkspaceTabs>
            <el-button
              text
              native-type="button"
              class="db-object-tabs__new"
              :title="t('database.newQuery')"
              :aria-label="t('database.newQuery')"
              @click="newQuery()"
            >
              <el-icon><Plus /></el-icon>
            </el-button>
          </div>

          <DatabaseObjectBrowser
            v-if="activeObject === 'database'"
            v-model:search-text="overviewSearchText"
            :database="databaseStore.selectedDatabase"
            :databases="overviewDatabases"
            :objects="overviewObjects"
            :selected-name="overviewSelectedObjectName"
            :details="overviewDetails"
            :details-loading="overviewDetailsLoading"
            :db-type="activeDialectType"
            :driver-descriptor="activeDriverDescriptor"
            @refresh="refreshDatabaseOverview"
            @switch-database="switchOverviewDatabase"
            @select="selectOverviewObject"
            @open="openOverviewObject"
            @edit-structure="editOverviewObjectStructure"
            @close-details="closeOverviewDetails"
            @context-menu="openOverviewContextMenu"
          />
          <template v-else>
          <div class="db-data-toolbar">
            <el-button text native-type="button"
              :disabled="!selectedTable || !databaseStore.desktopRuntime"
              @click="refreshSelectedTable"
            >
              <el-icon><Refresh /></el-icon>{{ t("database.refresh") }}
            </el-button>
            <el-button text native-type="button" :disabled="!canInsertRows" @click="openInsertRow">
              <el-icon><Plus /></el-icon>{{ t("database.addRow") }}
            </el-button>
            <span class="db-toolbar-divider" />
            <el-button text native-type="button"
              :disabled="!selectedTable || !databaseStore.desktopRuntime"
              @click="chooseImport"
            >
              <el-icon><Upload /></el-icon>{{ t("database.import") }}
            </el-button>
            <el-button text native-type="button"
              :disabled="!selectedTable || !databaseStore.desktopRuntime"
              @click="showTableDetails"
            >
              <el-icon><EditPen /></el-icon>{{ t("database.basicInfo") }}
            </el-button>
            <el-button text native-type="button"
              :disabled="!selectedDatabase || !databaseStore.desktopRuntime"
              @click="showErDiagram"
            >
              <el-icon><Grid /></el-icon>{{ t("database.erDiagram") }}
            </el-button>
            <el-dropdown
              :disabled="!selectedTable || !databaseStore.desktopRuntime"
              @command="exportData"
              ><el-button text native-type="button" :disabled="!selectedTable || !databaseStore.desktopRuntime">
                <el-icon><Download /></el-icon>{{ t("database.export") }}</el-button
              ><template #dropdown
                ><el-dropdown-menu
                  ><el-dropdown-item command="csv">CSV</el-dropdown-item
                  ><el-dropdown-item command="json">JSON</el-dropdown-item
                  ><el-dropdown-item command="xlsx">Excel (XLSX)</el-dropdown-item
                  ><el-dropdown-item command="markdown">Markdown</el-dropdown-item
                  ><el-dropdown-item command="sql">SQL (INSERT)</el-dropdown-item
                  ><el-dropdown-item command="txt">TXT</el-dropdown-item></el-dropdown-menu
                ></template
              ></el-dropdown
            >
            <el-button text native-type="button"
              :disabled="!databaseStore.desktopRuntime"
              @click="showExecutionPlan"
            >
              ϟ {{ t("database.executionPlan") }}
            </el-button>
            <el-button text native-type="button" @click="saveCurrentScript">
              <el-icon><Document /></el-icon>{{ t("database.saveScript") }}
            </el-button>
            <DatabaseGridToolbar
              v-if="activeResult"
              v-model:filter-text="gridFilterText"
              :object="activeObject === 'table' ? 'table' : 'query'"
              :grid-state="activeGridState"
              :result-columns="resultColumns"
              :filter-columns="filterColumns"
              :visible-column-count="visibleColumnEntries.length"
              :query-running="databaseStore.queryRunning"
              :can-search-entire-table="canSearchEntireTable"
              :search-placeholder="gridSearchPlaceholder"
              @search-submit="handleGridSearchSubmit"
              @search-clear="handleGridSearchClear"
              @search-all-rows="searchAllTableRows"
              @add-condition="addFilterCondition()"
              @update-condition="(id, patch) => updateFilterCondition(id, patch)"
              @remove-condition="removeFilterCondition"
              @set-logic="setFilterLogic"
              @clear-filters="clearColumnFilters"
              @apply-filters="applyTableFilters(activeGridState.filters)"
              @toggle-layout="toggleGridLayout"
              @set-column-visibility="setColumnVisibility"
              @show-all-columns="showAllColumns"
              :pending-count="pendingChanges.count.value"
              :pending-submitting="pendingChanges.submitting.value"
              @submit-pending="pendingChanges.submit()"
              @discard-pending="discardPendingChanges"
            />
            <span class="db-data-toolbar__spacer" />
            <el-button text native-type="button" class="db-diagnostic-button" @click="diagnosticsVisible = true">
              ✣ {{ t("database.diagnostics")
              }}<b v-if="diagnosticHistory.length">{{ diagnosticHistory.length }}</b>
            </el-button>
          </div>

          <AppSplitPane
            class="db-query-split"
            layout="vertical"
            side="first"
            unit="percent"
            :model-value="queryEditorExpanded ? 100 : queryEditorPercent"
            :collapsed="activeObject !== 'query'"
            :collapsed-size="0"
            refresh-panel-on-collapse
            :min="queryEditorExpanded ? 100 : 20"
            :max="queryEditorExpanded ? 100 : 75"
            :second-min="queryEditorExpanded ? 0 : 160"
            :resizable="activeObject === 'query' && !queryEditorExpanded"
            :aria-label="t('database.resizeQueryResult')"
            @update:model-value="queryEditorPercent = $event"
            @resize-end="queryEditorSizePersistence.save($event)"
          >
            <template #first>
          <DatabaseQueryWorkspace
            v-if="activeObject === 'query'"
            :active-query-tab="activeQueryTab"
            v-model:query-text="queryText"
            :query-editor-path="queryEditorPath"
            :editor-language="queryEditorLanguage"
            :sql-completions="sqlCompletions"
            :vector-query-hint-visible="vectorQueryHintVisible"
            :query-running="databaseStore.queryRunning"
            :desktop-available="databaseStore.desktopRuntime"
            :editor-font-size="queryEditorFontSize"
            :editor-expanded="queryEditorExpanded"
            :query-history="activeConnectionHistory"
            :db-type="activeDialectType"
            @view-state="setQueryEditorViewState"
            @run="runEditorQuery"
            @cancel="cancelQuery"
            @explain="showExecutionPlan"
            @format="formatEditorSql"
            @font-size="changeQueryEditorFontSize"
            @clear="clearEditor"
            @toggle-expanded="queryEditorExpanded = !queryEditorExpanded"
            @open-history="openHistory"
            @clear-history="clearHistory"
            @copy="copyEditorSql"
            @save-script="saveCurrentScript"
            @import="importSqlFile"
            @regenerate="regenerateQueryTemplate"
            @ai-generate="openAiQueryDialog"
          />
            </template>
            <template #second>
          <DatabaseResultPane
            v-model:target-page="targetPage"
            v-model:pinecone-namespace-draft="pineconeNamespaceDraft"
            :object="activeObject === 'table' ? 'table' : 'query'"
            :active-query-tab="activeQueryTab"
            :selected-table="selectedTable || undefined"
            :active-result="activeResult"
            :result-rows="resultRows"
            :display-rows="displayRows"
            :columns="visibleColumnEntries"
            :grid-state="activeGridState"
            :format-cell="formatCell"
            :selected-row-indexes="selectedRowIndexes"
            :all-rows-selected="allDisplayedRowsSelected"
            :some-rows-selected="someDisplayedRowsSelected"
            :active-query-running="activeQueryRunning"
            :result-row-count-text="resultRowCountText"
            :page="databaseStore.page"
            :page-size="databaseStore.pageSize"
            :primary-keys="databaseStore.primaryKeys"
            :can-edit-rows="canEditRows"
            :can-insert-rows="canInsertRows"
            :editing-cell="inlineCellEdit"
            :can-next-page="canNextPage"
            :can-last-page="canLastPage"
            :last-page-loading="countingLastPage || databaseStore.queryRunning || databaseStore.loading"
            :supports-offset-table-preview="supportsOffsetTablePreview"
            :desktop-runtime="databaseStore.desktopRuntime"
            :is-pinecone="activeConnection?.dbType === 'pinecone'"
            :context-menu="gridContextMenu"
            :context-can-copy-insert-sql="canCopyGridContextInsertSql()"
            :context-can-edit-cell="canEditContextCell()"
            :context-can-mutate-column="canMutateGridContextColumn()"
            :context-can-set-null="canSetGridContextNull()"
            :context-selected-row-count="gridContextMenu ? selectedRowCountForContext(gridContextMenu) : 0"
            :header-context-menu="gridHeaderContextMenu"
            :header-can-copy-alter-sql="canCopyGridHeaderAlterSql()"
            :pending-rows="pendingChanges.rowStates.value"
            :context-can-revert="canRevertGridContextChange()"
            @sort="toggleColumnSort"
            @reorder-columns="reorderColumns"
            @toggle-row-selection="toggleGridRowSelection"
            @toggle-all-row-selection="toggleAllGridRowSelection"
            @cell-detail="({ column, value }: { column: string; value: unknown }) => openCellDetail(column, value)"
            @begin-cell-edit="beginInlineCellEdit"
            @update-cell-edit="updateInlineCellEdit"
            @commit-cell-edit="commitInlineCellEdit"
            @cancel-cell-edit="cancelInlineCellEdit"
            @context-menu="openGridContextMenu"
            @close-context-menu="closeGridContextMenu"
            @copy-context-value="copyGridContextValue"
            @copy-context-row="copyGridContextRow"
            @copy-context-insert-sql="copyGridContextInsertSql"
            @filter-context-value="filterByGridContextValue"
            @exclude-context-value="excludeByGridContextValue"
            @filter-context-operator="filterGridContextByOperator"
            @clear-grid-filters="clearColumnFilters"
            @sort-context-column="sortGridContextColumn"
            @clear-grid-sort="clearGridSort"
            @view-context-cell="viewGridContextCell"
            @view-column-details="viewGridColumnDetails"
            @view-context-row="viewGridContextRowDetail"
            @edit-context-cell="editGridContextCell"
            @set-context-null="setGridContextCellNull"
            @bulk-update-context="openGridContextBulkUpdate"
            @duplicate-context-row="duplicateGridContextRow"
            @delete-context-row="deleteGridContextRow"
            @revert-context-change="revertGridContextChange"
            @toggle-layout="toggleGridLayout"
            @clear-selection="clearGridSelection"
            @export-grid="exportData"
            @refresh-grid="refreshActiveGrid"
            @header-context-menu="openGridHeaderContextMenu"
            @close-header-context-menu="closeGridHeaderContextMenu"
            @copy-header-column-name="copyGridHeaderColumnName"
            @copy-header-all-column-names="copyGridHeaderAllColumnNames"
            @copy-header-alter-sql="copyGridHeaderAlterSql"
            @hide-header-column="hideGridHeaderColumn"
            @freeze-header-column="freezeGridHeaderUpToColumn"
            @unfreeze-columns="unfreezeGridColumns"
            @bulk-update-header="bulkUpdateGridHeaderColumn"
            @delete-row="deleteRecord"
            @edit-row="editRecord"
            @change-page="changePageGuarded"
            @jump-to-page="jumpToPageGuarded"
            @last-page="goToLastPageGuarded"
            @change-page-size="changePageSizeGuarded"
            @apply-namespace="applyPineconeNamespaceGuarded"
          />
            </template>
          </AppSplitPane>
        </template>
          </template>
      </template>

      <div v-else class="db-no-session">
        <el-empty :description="t('database.connectHint')"
          ><el-button type="primary" @click="openNewConnection">{{
            t("database.addConnection")
          }}</el-button></el-empty
        >
      </div>
        </div>
      </template>
    </AppSplitPane>
  </section>


  <AppContextMenu
    v-if="overviewContextMenu && activeConnection"
    :open="true"
    :x="overviewContextMenu.x"
    :y="overviewContextMenu.y"
    :width="210"
    popper-class="db-grid-context-menu"
    @close="closeOverviewContextMenu"
  >
    <el-dropdown-item @click="runOverviewTableCommand('open')">
      {{ t("database.open") }}
    </el-dropdown-item>
    <el-dropdown-item @click="runOverviewTableCommand('query')">
      {{ t("database.queryTable") }}
    </el-dropdown-item>
    <el-dropdown-item @click="runOverviewTableCommand('details')">
      {{ t("database.tableStructure") }}
    </el-dropdown-item>
    <el-dropdown-item divided @click="runOverviewTableCommand('copy-name')">
      {{ t("database.copyObjectName") }}
    </el-dropdown-item>
    <el-dropdown-item
      v-if="activeCapabilities?.schemaDdl"
      @click="runOverviewTableCommand('copy-ddl')"
    >
      {{ t("database.copyDdl") }}
    </el-dropdown-item>
    <el-dropdown-item
      v-if="
        overviewContextMenu.object.kind.toLowerCase() === 'table' &&
        activeCapabilities?.charsetChange
      "
      @click="runOverviewTableCommand('change-charset')"
    >
      {{ t("database.changeCharset") }}
    </el-dropdown-item>
    <template v-if="activeCapabilities?.tableDataExport">
      <el-dropdown-item divided @click="runOverviewTableCommand('export-csv')">
        {{ t("database.exportTableCsv") }}
      </el-dropdown-item>
      <el-dropdown-item @click="runOverviewTableCommand('export-json')">
        {{ t("database.exportTableJson") }}
      </el-dropdown-item>
      <el-dropdown-item @click="runOverviewTableCommand('export-xlsx')">
        {{ t("database.exportTableXlsx") }}
      </el-dropdown-item>
      <el-dropdown-item @click="runOverviewTableCommand('export-markdown')">
        {{ t("database.exportTableMarkdown") }}
      </el-dropdown-item>
      <el-dropdown-item @click="runOverviewTableCommand('export-sql')">
        {{ t("database.exportTableSql") }}
      </el-dropdown-item>
      <el-dropdown-item @click="runOverviewTableCommand('export-txt')">
        {{ t("database.exportTableTxt") }}
      </el-dropdown-item>
    </template>
    <el-dropdown-item
      v-if="activeCapabilities?.tableDataImport"
      :divided="!activeCapabilities?.tableDataExport"
      @click="runOverviewTableCommand('import-data')"
    >
      {{ t("database.importTableData") }}
    </el-dropdown-item>
    <template
      v-if="
        (overviewContextMenu.object.kind.toLowerCase() === 'table' ||
          isVectorDatabaseType(activeConnection.dbType)) &&
        (activeCapabilities?.clearTable || activeCapabilities?.dropTable)
      "
    >
      <el-dropdown-item
        v-if="
          activeCapabilities?.clearTable &&
          (!isVectorDatabaseType(activeConnection.dbType) || activeConnection.dbType === 'pgvector')
        "
        divided
        class="is-danger"
        @click="runOverviewTableCommand('clear-table')"
      >
        {{ t("database.cleartable") }}
      </el-dropdown-item>
      <el-dropdown-item
        v-if="activeCapabilities?.dropTable"
        :divided="
          !activeCapabilities?.clearTable ||
          (isVectorDatabaseType(activeConnection.dbType) && activeConnection.dbType !== 'pgvector')
        "
        class="is-danger"
        @click="runOverviewTableCommand('drop-table')"
      >
        {{
          isVectorDatabaseType(activeConnection.dbType) && activeConnection.dbType !== "pgvector"
            ? t("database.deleteVectorObject", {
                kind:
                  activeConnection.dbType === "pinecone"
                    ? t("database.vectorIndex")
                    : t("database.vectorCollection"),
              })
            : t("database.droptable")
        }}
      </el-dropdown-item>
    </template>
  </AppContextMenu>

  <AppContextMenu
    v-if="tabContextMenu"
    :open="true"
    :x="tabContextMenu.x"
    :y="tabContextMenu.y"
    :width="210"
    popper-class="db-grid-context-menu"
    @close="closeObjectTabContextMenu"
  >
    <el-dropdown-item
      v-if="tabContextMenuKind === 'table'"
      @click="runObjectTabContextCommand('refresh')"
    >
      {{ t("database.refresh") }}
    </el-dropdown-item>
    <el-dropdown-item
      :divided="tabContextMenuKind === 'table'"
      @click="runObjectTabContextCommand('copy-name')"
    >
      {{ t("database.copyObjectName") }}
    </el-dropdown-item>
    <template v-if="tabContextMenuKind !== 'database'">
      <el-dropdown-item divided @click="runObjectTabContextCommand('close')">
        {{ t("common.closeTab") }}
      </el-dropdown-item>
      <el-dropdown-item
        :disabled="!tabContextMenuHasLeft"
        @click="runObjectTabContextCommand('close-left')"
      >
        {{ t("tab.closeLeft") }}
      </el-dropdown-item>
      <el-dropdown-item
        :disabled="!tabContextMenuHasRight"
        @click="runObjectTabContextCommand('close-right')"
      >
        {{ t("tab.closeRight") }}
      </el-dropdown-item>
      <el-dropdown-item divided @click="runObjectTabContextCommand('close-other')">
        {{ t("tab.closeOther") }}
      </el-dropdown-item>
      <el-dropdown-item @click="runObjectTabContextCommand('close-all')">
        {{ t("tab.closeAll") }}
      </el-dropdown-item>
    </template>
  </AppContextMenu>

  <input
    ref="importInput"
    type="file"
    accept=".csv,.json,text/csv,application/json"
    hidden
    @change="importData"
  />
  <DatabaseCharsetDialog
    v-model="charsetDialogVisible"
    :target="charsetTarget"
    :options="charsetOptions"
    :current="currentCharset"
    :loading="charsetDialogLoading"
    :applying="charsetChangeApplying"
    @apply="applyCharsetChange"
  />
  <DatabaseConnectionDialog
    v-model="connectionDialogVisible"
    :initial="editingConnection"
    :group-options="connectionGroupNames"
    :tag-options="connectionTagNames"
    :saving="savingConnection"
    :testing="testingConnection"
    @save="saveConnection"
    @test="testConnection"
  />
  <DatabaseConnectionImportDialog v-if="connectionImportVisible" v-model="connectionImportVisible" />
  <VectorCollectionDialog
    v-model="vectorCollectionDialogVisible"
    :connection="vectorCollectionTarget?.connection"
    :saving="vectorCollectionSaving"
    @create="createVectorCollection"
  />
  <DatabaseSchemaSyncDialog
    v-model="schemaSyncVisible"
    :initial-connection-id="activeConnection?.id"
  />
  <DatabaseBackupDialog
    v-if="backupDialogVisible"
    v-model="backupDialogVisible"
    :initial-connection-id="activeConnection?.id"
    :initial-database="selectedDatabase"
  />
  <DatabaseInsertSqlModeDialog
    v-if="openedDialogs.insertSql"
    :model-value="insertSqlModeDialog.visible.value"
    :mode="insertSqlModeDialog.mode.value"
    :row-count="insertSqlModeDialog.rowCount.value"
    @update:model-value="insertSqlModeDialog.visible.value = $event"
    @update:mode="insertSqlModeDialog.mode.value = $event"
    @confirm="insertSqlModeDialog.confirmInsertSqlMode"
    @cancel="insertSqlModeDialog.cancelInsertSqlMode"
  />
  <DatabaseTableDetailsDrawer
    v-model="detailsVisible"
    :database="tableDetailsTarget?.database ?? selectedDatabase"
    :table="tableDetailsTarget?.table ?? selectedTable"
    :details="tableDetails"
    :loading="detailsLoading"
    :applying="schemaChangeApplying"
    :db-type="activeDialectType"
    :driver-descriptor="activeDriverDescriptor"
    @refresh="showTableDetails"
    @apply="applySchemaChange"
  />
  <DatabaseErDiagram
    v-model="erDiagramVisible"
    :diagram="erDiagram"
    :loading="erDiagramLoading"
    @refresh="showErDiagram"
    @select-table="openDiagramTable"
  />
  <DatabaseAiQueryDialog
    v-if="activeConnection"
    v-model="aiQueryDialogVisible"
    :db-type="activeDialectType ?? activeConnection.dbType"
    :database="queryTemplateTarget?.database"
    :table="queryTemplateTarget?.table"
    :desktop-available="databaseStore.desktopRuntime"
    @apply="applyAiQuery"
  />
  <DatabaseBulkUpdateDialog
    v-if="openedDialogs.bulk"
    :target="bulkUpdateTarget"
    :columns="databaseStore.tableColumns"
    @close="closeBulkUpdate"
    @apply="applyBulkUpdate"
  />
  <DatabaseUtilityDialogs
    v-model:row-visible="rowDialogVisible"
    v-model:cell-visible="cellDetailVisible"
    v-model:plan-visible="executionPlanVisible"
    v-model:diagnostics-visible="diagnosticsVisible"
    :columns="databaseStore.tableColumns"
    :row-form="rowForm"
    :row-mode="rowDialogMode"
    @update-row-field="(column, value) => (rowForm[column] = value)"
    :cell-detail="cellDetail"
    :execution-plan="executionPlan"
    :diagnostic-history="diagnosticHistory"
    @save-row="saveRow"
    @open-history="openHistory"
  />
</template>
