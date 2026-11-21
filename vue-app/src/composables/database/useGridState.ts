import { computed, ref, type ComputedRef, type Ref } from "vue";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import type { QueryTab } from "@/domain/database/query-tab-snapshot";
import type { TableFilterModel } from "@/domain/database/table-search";

type WorkspaceObject = "database" | "query" | "table";

export type GridSortDirection = "asc" | "desc";
export type GridLayout = "horizontal" | "vertical";

export interface TableTab {
  id: string;
  connectionId: string;
  database: string;
  table: string;
}

export interface ResultGridState {
  filterText: string;
  filters: TableFilterModel;
  hiddenColumns: string[];
  columnOrder: string[];
  layout: GridLayout;
  /** 冻结的前 N 个可见字段列（0 表示不冻结）。 */
  frozenColumnCount: number;
  sort?: { column: string; direction: GridSortDirection };
}

export const EMPTY_FILTER_MODEL: TableFilterModel = { logic: "and", conditions: [] };

/**
 * 结果网格的持久化状态：列可见性/顺序、排序、布局与筛选文本，
 * 以网格 key（query tab 或 table tab）隔离，每个网格独立保存。
 */
export function useGridState(options: {
  activeObject: Ref<WorkspaceObject>;
  activeQueryTab: ComputedRef<QueryTab | undefined>;
  activeTableTab: ComputedRef<TableTab | undefined>;
  resultColumns: ComputedRef<string[]>;
}) {
  const databaseStore = useDatabaseWorkspaceStore();

  const resultGridStates = ref<Record<string, ResultGridState>>({});
  const resultColumnIndexByName = computed(() => {
    const indexes = new Map<string, number>();
    for (let index = 0; index < options.resultColumns.value.length; index += 1) {
      const column = options.resultColumns.value[index];
      if (!indexes.has(column)) indexes.set(column, index);
    }
    return indexes;
  });
  const activeGridKey = computed(() =>
    options.activeObject.value === "query"
      ? `query:${options.activeQueryTab.value?.id ?? "none"}`
      : `table:${options.activeTableTab.value?.id ?? "none"}`
  );
  const activeGridState = computed<ResultGridState>(
    () =>
      resultGridStates.value[activeGridKey.value] ?? {
        filterText: "",
        filters: EMPTY_FILTER_MODEL,
        hiddenColumns: [],
        columnOrder: [],
        layout: "horizontal",
        frozenColumnCount: 0,
      }
  );
  const gridFilterText = computed({
    get: () => activeGridState.value.filterText,
    set: (filterText: string) => updateActiveGridState({ filterText }),
  });
  const visibleColumnEntries = computed(() => {
    const hidden = new Set(activeGridState.value.hiddenColumns);
    const currentColumns = new Set(options.resultColumns.value);
    const seen = new Set<string>();
    const orderedNames = [
      ...activeGridState.value.columnOrder.filter((column) => currentColumns.has(column)),
      ...options.resultColumns.value,
    ].filter((column) => {
      if (seen.has(column)) return false;
      seen.add(column);
      return !hidden.has(column);
    });

    const columnInfoByName = new Map(
      options.activeObject.value === "table"
        ? databaseStore.tableColumns.map((column) => [column.name, column])
        : []
    );
    return orderedNames.map((name) => {
      const column = columnInfoByName.get(name);
      return {
        name,
        index: resultColumnIndexByName.value.get(name) ?? -1,
        dataType: column?.dataType,
        comment: column?.comment,
      };
    });
  });

  function updateActiveGridState(patch: Partial<ResultGridState>) {
    const current = resultGridStates.value[activeGridKey.value] ?? {
      filterText: "",
      filters: EMPTY_FILTER_MODEL,
      hiddenColumns: [],
      columnOrder: [],
      layout: "horizontal",
      frozenColumnCount: 0,
    };
    resultGridStates.value = {
      ...resultGridStates.value,
      [activeGridKey.value]: { ...current, ...patch },
    };
  }

  function setColumnVisibility(column: string, visible: boolean) {
    const hidden = new Set(activeGridState.value.hiddenColumns);
    if (visible) hidden.delete(column);
    else {
      if (visibleColumnEntries.value.length <= 1) return;
      hidden.add(column);
    }
    const sort =
      activeGridState.value.sort?.column === column && !visible
        ? undefined
        : activeGridState.value.sort;
    updateActiveGridState({ hiddenColumns: [...hidden], sort });
  }

  function showAllColumns() {
    updateActiveGridState({ hiddenColumns: [] });
  }

  function toggleGridLayout() {
    updateActiveGridState({
      layout: activeGridState.value.layout === "vertical" ? "horizontal" : "vertical",
    });
  }

  function reorderColumns(source: string, target: string) {
    if (
      source === target ||
      !options.resultColumns.value.includes(source) ||
      !options.resultColumns.value.includes(target)
    ) {
      return;
    }

    const currentColumns = new Set(options.resultColumns.value);
    const seen = new Set<string>();
    const ordered = [
      ...activeGridState.value.columnOrder.filter((column) => currentColumns.has(column)),
      ...options.resultColumns.value,
    ].filter((column) => {
      if (seen.has(column)) return false;
      seen.add(column);
      return true;
    });
    const sourceIndex = ordered.indexOf(source);
    if (sourceIndex < 0) return;

    const targetIndex = ordered.indexOf(target);
    if (targetIndex < 0) return;

    ordered.splice(sourceIndex, 1);
    const insertIndex = sourceIndex < targetIndex ? ordered.indexOf(target) + 1 : ordered.indexOf(target);
    ordered.splice(insertIndex, 0, source);
    updateActiveGridState({ columnOrder: ordered });
  }

  function toggleColumnSort(column: string) {
    const current = activeGridState.value.sort;
    const sort =
      !current || current.column !== column
        ? { column, direction: "asc" as const }
        : current.direction === "asc"
          ? { column, direction: "desc" as const }
          : undefined;
    updateActiveGridState({ sort });
  }

  /** 冻结前 N 个可见字段列；传入 0 取消冻结。 */
  function setFrozenColumnCount(count: number) {
    const next = Math.max(0, Math.min(Math.floor(count), visibleColumnEntries.value.length));
    updateActiveGridState({ frozenColumnCount: next });
  }

  return {
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
  };
}
