/**
 * 表格暂存修改：把单元格修改、新增行、删除行先记在本地（属于一个表标签），叠加显示在表格上，
 * 再经 SQL 安全网（`db_apply_changes`）一次提交。
 *
 * - 提交：低风险一次确认（内容为修改摘要），高风险输入表名；成功清空暂存（表格已由 store 刷新）。
 * - `partial`：带失败序号时标记出错的那处修改；非事务库已生效的前 applied 处从暂存中移除；
 *   没有失败序号时整组保留，只显示原因。
 * - 离开（翻页、刷新、换表、关闭标签、换连接）前 `confirmLeave` 询问 提交 / 放弃 / 取消。
 */
import { computed, ref, shallowRef, type ComputedRef, type Ref } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  applyPendingChanges,
  createPendingChanges,
  editInsert,
  pendingCount,
  revertCell,
  revertRow,
  stageDelete,
  stageInsert,
  stageUpdate,
  sameTarget,
  submitPendingChanges,
  summary,
  type PendingChangesState,
  type PendingTarget,
  type PendingRowState,
} from "@/domain/database/grid-pending-changes";
import type { SqlSafetyCheck } from "@/domain/database/sql-safety";
import type { TableTab } from "@/composables/database/useGridState";
import { useVueI18n } from "@/i18n/messages";
import { confirmSqlSafety } from "@/services/sql-safety-confirm";
import { SqlSafetyPartialError } from "@/services/sql-safety-service";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";

const NO_ROW_STATES = new Map<number, PendingRowState>();

export function useGridPendingChanges(options: {
  activeObject: Ref<"database" | "query" | "table">;
  activeTableTab: ComputedRef<TableTab | undefined>;
  loadedRows: ComputedRef<unknown[][]>;
  resultColumns: ComputedRef<string[]>;
  vectorMutationOptions: () => { namespace?: string };
  /** 有修改生效后调用（如让尾页页码缓存失效）。 */
  onApplied?: () => void;
}) {
  const { t } = useVueI18n();
  const databaseStore = useDatabaseWorkspaceStore();
  const state = shallowRef<PendingChangesState>(createPendingChanges());
  /** 暂存所属的表标签。 */
  const tabId = ref<string>();
  const submitting = ref(false);

  const total = computed(() => pendingCount(state.value));
  /** 当前显示的是暂存所属的表。 */
  const active = computed(
    () => options.activeObject.value === "table" && Boolean(tabId.value) && tabId.value === options.activeTableTab.value?.id,
  );
  /** 当前表的待提交数（别的表的暂存不计）。 */
  const count = computed(() => (active.value ? total.value : 0));
  const overlay = computed(() =>
    count.value
      ? applyPendingChanges(options.loadedRows.value, options.resultColumns.value, databaseStore.primaryKeys, state.value)
      : undefined,
  );
  const rows = computed(() => overlay.value?.rows ?? options.loadedRows.value);
  const rowStates = computed(() => overlay.value?.states ?? NO_ROW_STATES);

  function rowKeyAt(rowIndex: number) {
    return overlay.value?.rowKeys[rowIndex] ?? "";
  }

  function rowStateAt(rowIndex: number) {
    return rowStates.value.get(rowIndex);
  }

  /** 暂存写入的目标（连接、会话、库、表、命名空间），第一处修改暂存时记录。 */
  let target: PendingTarget | undefined;

  function currentTarget(): PendingTarget | undefined {
    const tab = options.activeTableTab.value;
    const sessionId = databaseStore.activeSessionId;
    if (!tab || !sessionId || databaseStore.selectedDatabase !== tab.database || databaseStore.selectedTable !== tab.table) {
      return undefined;
    }
    return {
      connectionId: tab.connectionId,
      sessionId,
      database: tab.database,
      table: tab.table,
      namespace: options.vectorMutationOptions().namespace ?? "",
    };
  }

  /** 暂存归当前表标签；别的表（或同表的别的命名空间 / 会话）还有未提交的修改时拒绝。 */
  function claim() {
    const tab = options.activeTableTab.value;
    if (!tab || options.activeObject.value !== "table") return false;
    const current = currentTarget();
    if (!current) return false;
    if (total.value && (tabId.value !== tab.id || !sameTarget(target, current))) {
      ElMessage.warning(t(tabId.value !== tab.id ? "database.pending.otherTable" : "database.pending.targetChanged"));
      return false;
    }
    if (!total.value) target = current;
    tabId.value = tab.id;
    return true;
  }

  function update(next: (current: PendingChangesState) => PendingChangesState) {
    // 提交过程中暂存不变，提交结果才能对应到暂存的每一处修改。
    if (submitting.value || !claim()) return false;
    state.value = next(state.value);
    return true;
  }

  function discard() {
    state.value = createPendingChanges();
  }

  /** 暂存所属的表标签已关闭（未经 confirmLeave 的路径，如 AI 关闭标签、断开连接）。 */
  function forgetTab(id: string) {
    if (tabId.value === id) {
      discard();
      tabId.value = undefined;
    }
  }

  function errorText(cause: unknown) {
    return String(cause instanceof Error ? cause.message : cause);
  }

  function summaryLines(check: SqlSafetyCheck) {
    const counts = summary(state.value);
    const lines = [t("database.pending.summary", counts)];
    // 后端判定不能整组事务执行时会带上这两个原因：按顺序逐条写入，遇错即停，已生效的无法撤销。
    const inOrder = check.statements.some((statement) =>
      statement.reasons.some((reason) => reason === "noTransaction" || reason === "notUndoableStore"));
    if (inOrder) lines.push(t("database.pending.inOrder"));
    return lines;
  }

  /** 提交当前表的暂存；返回是否已全部提交（取消、失败为 false）。 */
  async function submit() {
    if (!count.value) return true;
    if (submitting.value) return false;
    const current = currentTarget();
    if (!current || !sameTarget(target, current)) {
      // 当前显示的不是暂存时的那个目标（换了库表、命名空间或重连了会话）：不能写到别处。
      ElMessage.warning(t(current ? "database.pending.targetChanged" : "database.pending.reopenTable"));
      return false;
    }
    submitting.value = true;
    try {
      const result = await submitPendingChanges(
        state.value,
        (changes, committed) => databaseStore.applyRowChanges(changes, {
          namespace: current.namespace || undefined,
          confirm: (check, origin, notice) => confirmSqlSafety(check, origin, notice, summaryLines(check)),
          onCommitted: committed,
        }),
        (cause) => cause instanceof SqlSafetyPartialError
          ? { applied: cause.applied, failedIndex: cause.failedIndexKnown ? cause.failedIndex : undefined }
          : undefined,
        // 后端已提交：先清空暂存再刷新表格，刷新失败也不能留下已生效的修改。
        discard,
      );
      state.value = result.state;
      if (result.outcome === "submitted") {
        options.onApplied?.();
        ElMessage.success(t("database.pending.submitted", { count: result.applied }));
        if (result.refreshError !== undefined) {
          ElMessage.warning(t("database.pending.refreshFailed", { error: errorText(result.refreshError) }));
        }
        return true;
      }
      if (result.outcome === "failed") {
        if (result.error instanceof SqlSafetyPartialError && result.error.applied > 0) options.onApplied?.();
        ElMessage.error(errorText(result.error));
      }
      return false;
    } finally {
      submitting.value = false;
    }
  }

  /**
   * 离开当前表的数据前确认：有待提交修改时询问 提交 / 放弃 / 取消。返回 true 表示可以继续
   * （已提交或已放弃）。
   */
  async function confirmLeave() {
    if (!total.value) return true;
    try {
      await ElMessageBox.confirm(
        t("database.pending.leaveMessage", { count: total.value }),
        t("database.pending.leaveTitle"),
        {
          type: "warning",
          confirmButtonText: t("database.pending.submit"),
          cancelButtonText: t("database.pending.discard"),
          distinguishCancelAndClose: true,
          closeOnClickModal: false,
        },
      );
    } catch (action) {
      if (action !== "cancel") return false;
      discard();
      return true;
    }
    if (!active.value) {
      // 暂存的表不在当前视图（例如在查询标签里打开别的表）：回到该表才能提交。
      ElMessage.warning(t("database.pending.reopenTable"));
      return false;
    }
    return submit();
  }

  return {
    state,
    tabId,
    count,
    total,
    active,
    submitting,
    rows,
    rowStates,
    rowKeyAt,
    rowStateAt,
    stageUpdate: (primaryKey: Record<string, unknown>, column: string, value: unknown, original: unknown) =>
      update((current) => stageUpdate(current, primaryKey, column, value, original)),
    editInsert: (key: string, column: string, value: unknown) => update((current) => editInsert(current, key, column, value)),
    stageInsert: (values: Record<string, unknown>) => update((current) => stageInsert(current, values)),
    stageDelete: (primaryKey: Record<string, unknown>) => update((current) => stageDelete(current, primaryKey)),
    revertCell: (key: string, column: string) => update((current) => revertCell(current, key, column)),
    revertRow: (key: string) => update((current) => revertRow(current, key)),
    discard,
    forgetTab,
    submit,
    confirmLeave,
  };
}

export type GridPendingChanges = ReturnType<typeof useGridPendingChanges>;
