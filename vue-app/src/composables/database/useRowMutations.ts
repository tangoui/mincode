import { reactive, ref, type ComputedRef, type Ref } from "vue";
import { ElMessage } from "element-plus";
import type { GridPendingChanges } from "@/composables/database/useGridPendingChanges";
import { isInsertRowKey } from "@/domain/database/grid-pending-changes";
import type { ResultGridContextMenu, ResultGridEditingCell } from "@/components/database/VirtualResultGrid.vue";
import type { DatabaseConnection } from "@/domain/database/types";
import {
  databaseCellEditFieldKind,
  databaseRowFieldKind,
  databaseRowFieldValueFromRaw,
  DatabaseRowFieldValueError,
  initialDatabaseRowFieldValue,
  parseDatabaseRowFieldValue,
  type DatabaseRowFieldValue,
} from "@/domain/database/row-field";
import {
  databaseTemporalEditorKind,
  fromDatabaseTemporalEditorValue,
  toDatabaseTemporalEditorValue,
} from "@/domain/database/temporal-editor";
import {
  buildInsertRowsSql,
  supportsCopyAsInsertSql,
  type InsertSqlMode,
} from "@/domain/database/insert-sql";
import { DATABASE_VALUE_GENERATORS } from "@/domain/database/value-generator";
import { copyText } from "@/services/desktop-bridge";
import { useVueI18n } from "@/i18n/messages";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import type { TableFilterOperator } from "@/domain/database/table-search";

type WorkspaceObject = "database" | "query" | "table";

export interface InlineCellEdit extends ResultGridEditingCell {
  initialValue: string;
}

/** 批量修改/生成值对话框的目标：作用于所选行的某一列。 */
export interface BulkUpdateTarget {
  column: string;
  rowIndexes: number[];
  initialMode: "value" | "generator";
}

export type BulkUpdatePayload =
  | { mode: "value"; value: unknown }
  | { mode: "generator"; generatorId: string };

/**
 * 行编辑：单元格内联编辑、行编辑 dialog（新增/编辑/复制行）、删除行（含多选批量）、
 * 设为 NULL 与批量修改，以及右键菜单触发的复制/过滤/查看动作。所有写入只进暂存
 * （options.pending），由工具栏「提交」或 Cmd/Ctrl+S 一次提交。
 */
export function useRowMutations(options: {
  activeObject: Ref<WorkspaceObject>;
  activeConnection: ComputedRef<DatabaseConnection | undefined>;
  selectedTable: ComputedRef<string>;
  selectedDatabase: ComputedRef<string>;
  resultRows: ComputedRef<unknown[][]>;
  resultColumns: ComputedRef<string[]>;
  canEditRows: ComputedRef<boolean>;
  canInsertRows: ComputedRef<boolean>;
  selectedRowIndexes: ComputedRef<number[]>;
  selectedRowIndexSet: ComputedRef<Set<number>>;
  formatCell: (value: unknown) => string;
  updateSelectedGridRows: (next: Set<number>) => void;
  vectorMutationOptions: () => { namespace?: string };
  gridContextMenu: Ref<ResultGridContextMenu | undefined>;
  closeGridContextMenu: () => void;
  filterByColumnValue: (column: string, value: string | null) => void;
  filterByColumnValues: (column: string, values: Array<string | null>) => void;
  excludeByColumnValue: (column: string, value: string | null) => void;
  excludeByColumnValues: (column: string, values: Array<string | null>) => void;
  applyColumnFilterOperator: (column: string, operator: TableFilterOperator, value?: string) => void;
  openCellDetail: (column: string, value: unknown) => void;
  desktopOnly: () => void;
  /** 多行复制为 INSERT 时弹出模式选择；取消时 resolve undefined。 */
  chooseInsertSqlMode?: (rowCount: number) => Promise<InsertSqlMode | undefined>;
  /** 表格暂存（resultRows 已叠加暂存修改）。 */
  pending: GridPendingChanges;
}) {
  const { t } = useVueI18n();
  const databaseStore = useDatabaseWorkspaceStore();

  const rowDialogVisible = ref(false);
  const rowDialogMode = ref<"insert" | "edit">("insert");
  const editingRowPrimaryKey = ref<Record<string, unknown>>({});
  /** 行编辑框打开时的行号与行键（新增行以行键定位）。 */
  let editingRow: { index: number; key: string } | undefined;
  const rowForm = reactive<Record<string, DatabaseRowFieldValue>>({});
  const inlineCellEdit = ref<InlineCellEdit>();
  const pending = options.pending;

  function rowIsDeleted(rowIndex: number) {
    return pending.rowStateAt(rowIndex)?.kind === "delete";
  }

  /** 暂存一个单元格：新增行改它的字段，已有行按主键记修改（原值取当前显示值，同格只记第一次的原值）。 */
  function stageCell(rowIndex: number, column: string, value: unknown) {
    const row = options.resultRows.value[rowIndex];
    if (!row || rowIsDeleted(rowIndex)) return false;
    const key = pending.rowKeyAt(rowIndex);
    if (isInsertRowKey(key)) return pending.editInsert(key, column, value);
    const primaryKey = rowPrimaryKey(rowIndex);
    if (!primaryKey || Object.values(primaryKey).some((keyValue) => keyValue === undefined)) return false;
    const columnIndex = options.resultColumns.value.indexOf(column);
    return pending.stageUpdate(primaryKey, column, value, columnIndex >= 0 ? row[columnIndex] : undefined);
  }

  /** 选区里可暂存的行（跳过已标记删除的行）。 */
  function stageableRowIndexes(rowIndexes: number[]) {
    return rowIndexes.filter((index) => Boolean(options.resultRows.value[index]) && !rowIsDeleted(index));
  }

  function canEditContextCell() {
    const context = options.gridContextMenu.value;
    return Boolean(
      context && contextRowIndexes(context).length === 1 && options.canEditRows.value &&
      !databaseStore.primaryKeys.includes(context.column)
    );
  }

  function beginInlineCellEdit(payload: { rowIndex: number; column: string }) {
    if (!options.canEditRows.value) {
      if (databaseStore.desktopRuntime) ElMessage.warning(t("database.noPrimaryKeyEdit"));
      return;
    }
    if (databaseStore.primaryKeys.includes(payload.column) || rowIsDeleted(payload.rowIndex)) return;
    const columnIndex = options.resultColumns.value.indexOf(payload.column);
    const row = options.resultRows.value[payload.rowIndex];
    if (columnIndex < 0 || !row) return;
    const initialValue = options.formatCell(row[columnIndex]);
    const editorKind = databaseTemporalEditorKind(
      databaseStore.tableColumns.find((column) => column.name === payload.column)?.dataType,
      row[columnIndex],
    );
    inlineCellEdit.value = {
      ...payload,
      initialValue,
      editorKind,
      value: toDatabaseTemporalEditorValue(initialValue, editorKind),
    };
  }

  function updateInlineCellEdit(payload: ResultGridEditingCell) {
    const edit = inlineCellEdit.value;
    if (!edit || edit.rowIndex !== payload.rowIndex || edit.column !== payload.column) return;
    edit.value = payload.value;
  }

  function cancelInlineCellEdit(payload: { rowIndex: number; column: string }) {
    const edit = inlineCellEdit.value;
    if (edit && edit.rowIndex === payload.rowIndex && edit.column === payload.column) {
      inlineCellEdit.value = undefined;
    }
  }

  function commitInlineCellEdit(payload: { rowIndex: number; column: string }) {
    const edit = inlineCellEdit.value;
    if (!edit || edit.rowIndex !== payload.rowIndex || edit.column !== payload.column) return;
    const value = fromDatabaseTemporalEditorValue(
      edit.value,
      edit.editorKind,
      edit.initialValue,
    );
    if (value === edit.initialValue) {
      inlineCellEdit.value = undefined;
      return;
    }

    const row = options.resultRows.value[edit.rowIndex];
    if (!row) return;
    const columnMeta = databaseStore.tableColumns.find((column) => column.name === edit.column);
    const editedColumnIndex = options.resultColumns.value.indexOf(edit.column);
    let parsedValue: unknown;
    try {
      parsedValue = parseDatabaseRowFieldValue(
        databaseCellEditFieldKind(
          columnMeta?.dataType,
          edit.column,
          editedColumnIndex >= 0 ? row[editedColumnIndex] : undefined,
        ),
        value,
        columnMeta?.nullable ?? true,
      );
    } catch (cause) {
      ElMessage.error(
        cause instanceof DatabaseRowFieldValueError ? t(`database.${cause.reason}`) : String(cause)
      );
      return;
    }
    if (stageCell(edit.rowIndex, edit.column, parsedValue)) inlineCellEdit.value = undefined;
  }

  async function copyGridContextValue() {
    const context = options.gridContextMenu.value;
    if (!context) return;
    const columnIndex = options.resultColumns.value.indexOf(context.column);
    const values = contextRowIndexes(context).map((index) => {
      const row = options.resultRows.value[index];
      return row && columnIndex >= 0 ? options.formatCell(row[columnIndex]) : options.formatCell(context.value);
    });
    try {
      await navigator.clipboard.writeText(values.join("\n"));
      ElMessage.success(t("database.cellCopied"));
    } catch (cause) {
      ElMessage.error(String(cause));
    } finally {
      options.closeGridContextMenu();
    }
  }

  function contextRowIndexes(context: ResultGridContextMenu) {
    if (options.selectedRowIndexes.value.length > 1) {
      return options.selectedRowIndexes.value.filter((index) => Boolean(options.resultRows.value[index]));
    }
    return options.selectedRowIndexSet.value.has(context.rowIndex)
      ? options.selectedRowIndexes.value
      : [context.rowIndex];
  }

  function rowRecord(row: unknown[]) {
    return Object.fromEntries(
      options.resultColumns.value.map((column, index) => [column, row[index]])
    );
  }

  async function copyGridContextRow() {
    const context = options.gridContextMenu.value;
    if (!context) return;
    const rows = contextRowIndexes(context)
      .map((index) => options.resultRows.value[index])
      .filter((row): row is unknown[] => Boolean(row));
    if (!rows.length) return;
    try {
      const value = rows.length === 1 ? rowRecord(rows[0]) : rows.map(rowRecord);
      await navigator.clipboard.writeText(
        JSON.stringify(value, (_key, entry) => (typeof entry === "bigint" ? entry.toString() : entry), 2)
      );
      ElMessage.success(t("database.rowCopied"));
    } catch (cause) {
      ElMessage.error(String(cause));
    } finally {
      options.closeGridContextMenu();
    }
  }

  function canCopyGridContextInsertSql() {
    return Boolean(
      options.gridContextMenu.value &&
        options.activeObject.value === "table" &&
        options.activeConnection.value &&
        options.selectedTable.value &&
        supportsCopyAsInsertSql(options.activeConnection.value.dbType)
    );
  }

  async function copyGridContextInsertSql() {
    const context = options.gridContextMenu.value;
    const connection = options.activeConnection.value;
    const table = options.selectedTable.value;
    if (!context || !connection || !table || options.activeObject.value !== "table") return;
    const rows = contextRowIndexes(context)
      .map((index) => options.resultRows.value[index])
      .filter((row): row is unknown[] => Boolean(row));
    if (!rows.length) return;
    options.closeGridContextMenu();
    try {
      // 多行时让用户选择：每行一条独立 INSERT，或合并为单条批量 INSERT。
      let mode: InsertSqlMode = "rows";
      if (rows.length > 1 && options.chooseInsertSqlMode) {
        const chosen = await options.chooseInsertSqlMode(rows.length);
        if (!chosen) return; // 用户取消不是错误
        mode = chosen;
      }
      const sql = buildInsertRowsSql({
        dbType: connection.dbType,
        database: options.selectedDatabase.value,
        table,
        columns: options.resultColumns.value,
        columnTypes: options.resultColumns.value.map(
          (column) => databaseStore.tableColumns.find((item) => item.name === column)?.dataType ?? ""
        ),
        rows,
        mode,
      });
      await copyText(sql);
      ElMessage.success(t("database.insertSqlCopied"));
    } catch (cause) {
      ElMessage.error(String(cause));
    }
  }

  /** NULL 单元格返回 null，由过滤器转换成 IS NULL / IS NOT NULL 条件。 */
  function gridContextFilterValue(value: unknown): string | null {
    return value === null || value === undefined ? null : options.formatCell(value);
  }

  function filterByGridContextValue() {
    const context = options.gridContextMenu.value;
    if (!context) return;
    const columnIndex = options.resultColumns.value.indexOf(context.column);
    const values = contextRowIndexes(context)
      .map((index) => options.resultRows.value[index])
      .filter((row): row is unknown[] => Boolean(row))
      .map((row) => gridContextFilterValue(row[columnIndex]));
    if (values.length > 1) options.filterByColumnValues(context.column, values);
    else options.filterByColumnValue(context.column, gridContextFilterValue(context.value));
    options.closeGridContextMenu();
  }

  function excludeByGridContextValue() {
    const context = options.gridContextMenu.value;
    if (!context) return;
    const columnIndex = options.resultColumns.value.indexOf(context.column);
    const values = contextRowIndexes(context)
      .map((index) => options.resultRows.value[index])
      .filter((row): row is unknown[] => Boolean(row))
      .map((row) => gridContextFilterValue(row[columnIndex]));
    if (values.length > 1) options.excludeByColumnValues(context.column, values);
    else options.excludeByColumnValue(context.column, gridContextFilterValue(context.value));
    options.closeGridContextMenu();
  }

  function filterGridContextByOperator(operator: TableFilterOperator) {
    const context = options.gridContextMenu.value;
    if (!context) return;
    const value = operator === "isNull" || operator === "isNotNull"
      ? ""
      : options.formatCell(context.value);
    options.applyColumnFilterOperator(context.column, operator, value);
    options.closeGridContextMenu();
  }

  /** 右键列是否允许批量修改（表格模式 + 可编辑 + 非主键）。 */
  function canMutateGridContextColumn() {
    const context = options.gridContextMenu.value;
    return Boolean(
      context &&
      options.activeObject.value === "table" &&
      options.canEditRows.value &&
      !databaseStore.primaryKeys.includes(context.column)
    );
  }

  function canSetGridContextNull() {
    const context = options.gridContextMenu.value;
    if (!context || !canMutateGridContextColumn()) return false;
    const column = databaseStore.tableColumns.find((item) => item.name === context.column);
    return Boolean(column?.nullable);
  }

  function setGridContextCellNull() {
    const context = options.gridContextMenu.value;
    if (!context || !canSetGridContextNull()) return;
    const rowIndexes = stageableRowIndexes(contextRowIndexes(context));
    options.closeGridContextMenu();
    const staged = rowIndexes.filter((rowIndex) => stageCell(rowIndex, context.column, null)).length;
    if (staged) ElMessage.success(t("database.pending.stagedRows", { count: staged }));
  }

  const bulkUpdateTarget = ref<BulkUpdateTarget>();

  /** 表头/单元格菜单共用的批量修改入口。 */
  function openBulkUpdate(column: string, rowIndexes: number[], initialMode: "value" | "generator") {
    if (!options.canEditRows.value) return options.desktopOnly();
    if (!rowIndexes.length) return;
    bulkUpdateTarget.value = { column, rowIndexes, initialMode };
  }

  function openGridContextBulkUpdate(initialMode: "value" | "generator") {
    const context = options.gridContextMenu.value;
    if (!context || !canMutateGridContextColumn()) return;
    const rowIndexes = contextRowIndexes(context);
    options.closeGridContextMenu();
    openBulkUpdate(context.column, rowIndexes, initialMode);
  }

  function closeBulkUpdate() {
    bulkUpdateTarget.value = undefined;
  }

  function applyBulkUpdate(payload: BulkUpdatePayload) {
    const target = bulkUpdateTarget.value;
    if (!target) return;
    const generator =
      payload.mode === "generator"
        ? DATABASE_VALUE_GENERATORS.find((item) => item.id === payload.generatorId)
        : undefined;
    if (payload.mode === "generator" && !generator) return;
    bulkUpdateTarget.value = undefined;
    // 生成器模式每行生成一个新值（如 UUID），固定值模式所有行写入同一值。
    const staged = stageableRowIndexes(target.rowIndexes).filter((rowIndex) =>
      stageCell(rowIndex, target.column, generator ? generator.generate() : payload.mode === "value" ? payload.value : undefined)
    ).length;
    if (staged) ElMessage.success(t("database.pending.stagedRows", { count: staged }));
  }

  function viewGridContextCell() {
    const context = options.gridContextMenu.value;
    if (!context || contextRowIndexes(context).length > 1) return;
    options.openCellDetail(context.column, context.value);
    options.closeGridContextMenu();
  }

  function editGridContextCell() {
    const context = options.gridContextMenu.value;
    if (!context || contextRowIndexes(context).length > 1) return;
    options.closeGridContextMenu();
    beginInlineCellEdit(context);
  }

  function openInsertRow() {
    if (!options.canInsertRows.value) return options.desktopOnly();
    rowDialogMode.value = "insert";
    editingRowPrimaryKey.value = {};
    editingRow = undefined;
    for (const key of Object.keys(rowForm)) delete rowForm[key];
    for (const column of databaseStore.tableColumns) {
      if (column.autoIncrement) continue;
      const kind = databaseRowFieldKind(column.dataType, column.name);
      rowForm[column.name] = initialDatabaseRowFieldValue(kind, column.defaultValue);
    }
    rowDialogVisible.value = true;
  }

  function duplicateGridContextRow() {
    const context = options.gridContextMenu.value;
    if (!context) return;
    const indexes = contextRowIndexes(context);
    options.closeGridContextMenu();
    if (!options.canInsertRows.value) return options.desktopOnly();
    if (indexes.length > 1) {
      const columns = databaseStore.tableColumns.filter((column) => !column.autoIncrement);
      const fields = indexes.map((index) => {
        const row = options.resultRows.value[index];
        return Object.fromEntries(columns.flatMap((column) => {
          const columnIndex = options.resultColumns.value.indexOf(column.name);
          if (columnIndex < 0) return [];
          return [[column.name, row?.[columnIndex]]];
        }));
      });
      const staged = fields.filter((values) => pending.stageInsert(values)).length;
      if (staged) ElMessage.success(t("database.pending.stagedRows", { count: staged }));
      return;
    }
    const row = options.resultRows.value[context.rowIndex];
    if (!row) return;
    rowDialogMode.value = "insert";
    editingRowPrimaryKey.value = {};
    editingRow = undefined;
    for (const key of Object.keys(rowForm)) delete rowForm[key];
    for (const column of databaseStore.tableColumns) {
      if (column.autoIncrement) continue;
      const columnIndex = options.resultColumns.value.indexOf(column.name);
      const kind = databaseRowFieldKind(column.dataType, column.name);
      rowForm[column.name] =
        columnIndex >= 0
          ? databaseRowFieldValueFromRaw(kind, row[columnIndex])
          : initialDatabaseRowFieldValue(kind, column.defaultValue);
    }
    rowDialogVisible.value = true;
  }

  function editRecord(rowIndex: number) {
    if (!options.canEditRows.value) {
      if (databaseStore.desktopRuntime) ElMessage.warning(t("database.noPrimaryKeyDelete"));
      return;
    }
    const row = options.resultRows.value[rowIndex];
    if (!row || rowIsDeleted(rowIndex)) return;
    const key = pending.rowKeyAt(rowIndex);
    const primaryKeyValues = rowPrimaryKey(rowIndex) ?? {};
    if (!isInsertRowKey(key) && Object.values(primaryKeyValues).some((keyValue) => keyValue === undefined)) {
      ElMessage.error(t("database.primaryKeyValueMissing"));
      return;
    }
    rowDialogMode.value = "edit";
    editingRowPrimaryKey.value = primaryKeyValues;
    editingRow = { index: rowIndex, key };
    for (const key of Object.keys(rowForm)) delete rowForm[key];
    for (const column of databaseStore.tableColumns) {
      if (column.autoIncrement) continue;
      const columnIndex = options.resultColumns.value.indexOf(column.name);
      const kind = databaseRowFieldKind(column.dataType, column.name);
      rowForm[column.name] =
        columnIndex >= 0
          ? databaseRowFieldValueFromRaw(kind, row[columnIndex])
          : initialDatabaseRowFieldValue(kind, column.defaultValue);
    }
    rowDialogVisible.value = true;
  }

  /** 两个值显示相同（行编辑框只能编辑显示文本，显示相同的列视为未修改）。 */
  function sameDisplayedValue(left: unknown, right: unknown) {
    if (left == null || right == null) return left == null && right == null;
    return options.formatCell(left) === options.formatCell(right);
  }

  function saveRow() {
    const isEdit = rowDialogMode.value === "edit";
    const editing = isEdit ? editingRow : undefined;
    // 新增行（含编辑一个暂存的新增行）留空且有默认值的列不写，交给数据库默认值。
    const asInsert = !editing || isInsertRowKey(editing.key);
    let fields: Record<string, unknown>;
    try {
      fields = Object.fromEntries(
        databaseStore.tableColumns.flatMap((column) => {
          if (column.autoIncrement || !(column.name in rowForm)) return [];
          const rawValue = rowForm[column.name] ?? "";
          if (asInsert && column.defaultValue && rawValue === "") return [];
          const kind = databaseRowFieldKind(column.dataType, column.name);
          return [[column.name, parseDatabaseRowFieldValue(kind, rawValue, column.nullable)]];
        })
      );
    } catch (cause) {
      ElMessage.error(
        cause instanceof DatabaseRowFieldValueError ? t(`database.${cause.reason}`) : String(cause)
      );
      return;
    }
    if (!editing) {
      if (pending.stageInsert(fields)) rowDialogVisible.value = false;
      return;
    }
    if (isInsertRowKey(editing.key)) {
      for (const [column, value] of Object.entries(fields)) pending.editInsert(editing.key, column, value);
      rowDialogVisible.value = false;
      return;
    }
    const row = options.resultRows.value[editing.index];
    const primaryKey = editingRowPrimaryKey.value;
    for (const [column, value] of Object.entries(fields)) {
      if (databaseStore.primaryKeys.includes(column)) continue;
      const columnIndex = options.resultColumns.value.indexOf(column);
      const current = row && columnIndex >= 0 ? row[columnIndex] : undefined;
      if (sameDisplayedValue(value, current)) continue;
      if (!pending.stageUpdate(primaryKey, column, value, current)) return;
    }
    rowDialogVisible.value = false;
  }

  function rowPrimaryKey(rowIndex: number) {
    const row = options.resultRows.value[rowIndex];
    if (!row) return;
    return Object.fromEntries(
      databaseStore.primaryKeys.map((column) => {
        const columnIndex = options.resultColumns.value.indexOf(column);
        return [column, columnIndex >= 0 ? row[columnIndex] : undefined];
      })
    );
  }

  /**
   * 暂存删除（选中多行时作用于选区）：新增行直接丢弃，已有行按主键标记删除，已标记删除的跳过。
   * 点在一个已标记删除的行上（按钮显示「取消删除」）时只恢复这一行，不动选区。
   */
  function deleteRecord(rowIndex: number) {
    if (!options.canEditRows.value) {
      if (databaseStore.desktopRuntime) ElMessage.warning(t("database.noPrimaryKeyDelete"));
      return;
    }
    if (rowIsDeleted(rowIndex)) {
      pending.revertRow(pending.rowKeyAt(rowIndex));
      return;
    }
    const targetIndexes = (options.selectedRowIndexes.value.length > 1
      ? options.selectedRowIndexes.value
      : options.selectedRowIndexSet.value.has(rowIndex)
        ? options.selectedRowIndexes.value
        : [rowIndex]
    ).filter((index) => Boolean(options.resultRows.value[index]));
    if (!targetIndexes.length) return;
    const loaded = targetIndexes.filter((index) => !isInsertRowKey(pending.rowKeyAt(index)) && !rowIsDeleted(index));
    const primaryKeys = loaded
      .map(rowPrimaryKey)
      .filter((value): value is Record<string, unknown> => Boolean(value));
    if (primaryKeys.some((key) => Object.values(key).some((value) => value === undefined))) {
      ElMessage.error(t("database.primaryKeyValueMissing"));
      return;
    }
    // 先按行号取齐行键与主键，再修改暂存（修改后行号会变化）。
    const insertKeys = targetIndexes.map(pending.rowKeyAt).filter(isInsertRowKey);
    for (const key of insertKeys) pending.revertRow(key);
    for (const primaryKey of primaryKeys) pending.stageDelete(primaryKey);
    options.updateSelectedGridRows(new Set());
  }

  /** 撤销右键单元格的暂存：已修改的单元格回到原值，新增 / 删除的行整行撤销。 */
  function canRevertGridContextChange() {
    const context = options.gridContextMenu.value;
    return Boolean(context && pending.rowStateAt(context.rowIndex));
  }

  function revertGridContextChange() {
    const context = options.gridContextMenu.value;
    if (!context) return;
    options.closeGridContextMenu();
    const state = pending.rowStateAt(context.rowIndex);
    const key = pending.rowKeyAt(context.rowIndex);
    if (!state || !key) return;
    if (state.kind === "update") {
      if (state.columns.has(context.column)) pending.revertCell(key, context.column);
      else pending.revertRow(key);
      return;
    }
    pending.revertRow(key);
  }

  function deleteGridContextRow() {
    const context = options.gridContextMenu.value;
    if (!context) return;
    options.closeGridContextMenu();
    deleteRecord(context.rowIndex);
  }

  return {
    rowDialogVisible,
    rowDialogMode,
    editingRowPrimaryKey,
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
  };
}
