import { computed, ref, watch, type ComputedRef, type Ref } from "vue";
import {
  createTableRowFilter,
  inferTableFilterFieldType,
  tableFilterOperatorNeedsValue,
  tableFilterOperators,
  type TableFilterCondition,
  type TableFilterFieldType,
  type TableFilterModel,
  type TableFilterOperator,
} from "@/domain/database/table-search";
import { createSearchableRowText } from "@/domain/database/grid-utils";
import { useDatabaseWorkspaceStore } from "@/stores/database-workspace";
import { EMPTY_FILTER_MODEL, type ResultGridState } from "./useGridState";

type WorkspaceObject = "database" | "query" | "table";

const LOCAL_GRID_FILTER_DELAY = 120;

/**
 * 网格筛选：类型化筛选条件模型（条件增删改/逻辑切换/按值过滤）
 * 与本地结果集过滤（防抖文本搜索 + 条件过滤）。
 * 表格模式下的服务端过滤通过回调交由分页/服务端搜索逻辑处理。
 */
export function useTableFilters(options: {
  activeObject: Ref<WorkspaceObject>;
  activeGridKey: ComputedRef<string>;
  activeGridState: ComputedRef<ResultGridState>;
  updateActiveGridState: (patch: Partial<ResultGridState>) => void;
  resultColumns: ComputedRef<string[]>;
  filterColumns: ComputedRef<string[]>;
  resultRows: ComputedRef<unknown[][]>;
  resultColumnIndexByName: ComputedRef<Map<string, number>>;
  canSearchEntireTable: ComputedRef<boolean>;
  formatCell: (value: unknown) => string;
  /** 表格模式下清空筛选时清除服务端搜索。 */
  onClearTableFilters: () => void;
  /** 表格模式下按值过滤时应用服务端筛选。 */
  onApplyTableFilters: () => void;
}) {
  const databaseStore = useDatabaseWorkspaceStore();
  let filterConditionSequence = 0;

  function filterColumnType(column: string): TableFilterFieldType {
    const index = options.resultColumnIndexByName.value.get(column) ?? -1;
    const metadata = databaseStore.tableColumns.find((item) => item.name === column);
    const sample = options.resultRows.value.find((row) => row[index] != null)?.[index];
    return inferTableFilterFieldType(metadata?.dataType, sample == null ? [] : [sample]);
  }
  function defaultFilterOperator(fieldType: TableFilterFieldType): TableFilterOperator {
    return tableFilterOperators(fieldType)[0];
  }
  function addFilterCondition(column = options.filterColumns.value[0] ?? "") {
    if (!column) return;
    const fieldType = filterColumnType(column);
    filterConditionSequence += 1;
    options.updateActiveGridState({
      filters: {
        ...options.activeGridState.value.filters,
        conditions: [
          ...options.activeGridState.value.filters.conditions,
          { id: `filter-${Date.now()}-${filterConditionSequence}`, column, fieldType, operator: defaultFilterOperator(fieldType), value: "", valueTo: "" },
        ],
      },
    });
  }
  function updateFilterCondition(id: string, patch: Partial<TableFilterCondition>) {
    const conditions = options.activeGridState.value.filters.conditions.map((condition) => {
      if (condition.id !== id) return condition;
      const next = { ...condition, ...patch };
      if (patch.column) {
        next.fieldType = filterColumnType(patch.column);
        next.operator = defaultFilterOperator(next.fieldType);
        next.value = "";
        next.valueTo = "";
      }
      if (patch.operator && !tableFilterOperatorNeedsValue(patch.operator)) {
        next.value = "";
        next.valueTo = "";
      }
      return next;
    });
    options.updateActiveGridState({
      filters: { ...options.activeGridState.value.filters, conditions },
    });
  }
  function removeFilterCondition(id: string) {
    options.updateActiveGridState({
      filters: {
        ...options.activeGridState.value.filters,
        conditions: options.activeGridState.value.filters.conditions.filter(
          (condition) => condition.id !== id
        ),
      },
    });
  }
  function setFilterLogic(logic: TableFilterModel["logic"]) {
    options.updateActiveGridState({
      filters: { ...options.activeGridState.value.filters, logic },
    });
  }
  function clearColumnFilters() {
    options.updateActiveGridState({ filters: EMPTY_FILTER_MODEL });
    if (options.activeObject.value === "table") options.onClearTableFilters();
  }
  /** null 表示单元格为 NULL，转为 IS NULL 条件而不是与字符串 "NULL" 比较。 */
  function filterByColumnValues(column: string, values: Array<string | null>) {
    const uniqueValues = [...new Set(values)];
    if (!uniqueValues.length) return;
    const fieldType = filterColumnType(column);
    const timestamp = Date.now();
    const conditions = uniqueValues.map((value) => {
      filterConditionSequence += 1;
      return {
        id: `filter-${timestamp}-${filterConditionSequence}`,
        column,
        fieldType,
        operator: value === null ? ("isNull" as const) : ("equals" as const),
        value: value ?? "",
      };
    });
    options.updateActiveGridState({
      filters: {
        logic: conditions.length > 1 ? "or" : "and",
        conditions,
      },
    });
    if (options.activeObject.value === "table") options.onApplyTableFilters();
  }
  function filterByColumnValue(column: string, value: string | null) {
    filterByColumnValues(column, [value]);
  }
  function applyColumnFilterOperator(
    column: string,
    operator: TableFilterOperator,
    value = "",
  ) {
    const inferredFieldType = filterColumnType(column);
    // 包含操作始终按显示文本比较；大小比较则保留数字和日期语义。
    const fieldType = operator === "contains" || operator === "notContains"
      ? "text"
      : inferredFieldType === "boolean" && (operator === "greaterThan" || operator === "lessThan")
        ? "text"
        : inferredFieldType;
    filterConditionSequence += 1;
    options.updateActiveGridState({
      filters: {
        logic: "and",
        conditions: [{
          id: `filter-${Date.now()}-${filterConditionSequence}`,
          column,
          fieldType,
          operator,
          value,
        }],
      },
    });
    if (options.activeObject.value === "table") options.onApplyTableFilters();
  }
  /** 排除筛选：多个值必须全部不满足，使用 AND 逻辑 + notEquals 操作符。 */
  function excludeByColumnValues(column: string, values: Array<string | null>) {
    const uniqueValues = [...new Set(values)];
    if (!uniqueValues.length) return;
    const fieldType = filterColumnType(column);
    const timestamp = Date.now();
    const conditions = uniqueValues.map((value) => {
      filterConditionSequence += 1;
      return {
        id: `filter-${timestamp}-${filterConditionSequence}`,
        column,
        fieldType,
        operator: value === null ? ("isNotNull" as const) : ("notEquals" as const),
        value: value ?? "",
      };
    });
    options.updateActiveGridState({
      filters: {
        logic: "and",
        conditions,
      },
    });
    if (options.activeObject.value === "table") options.onApplyTableFilters();
  }
  function excludeByColumnValue(column: string, value: string | null) {
    excludeByColumnValues(column, [value]);
  }

  const localGridFilterCriteria = ref({
    gridKey: options.activeGridKey.value,
    filterText: "",
    filters: EMPTY_FILTER_MODEL as TableFilterModel,
  });
  let localGridFilterTimer: ReturnType<typeof setTimeout> | undefined;
  const rowTextSearch = createSearchableRowText((value) => options.formatCell(value));

  function applyLocalGridFilterCriteria(criteria: typeof localGridFilterCriteria.value) {
    localGridFilterCriteria.value = criteria;
  }

  watch(
    () => ({
      gridKey: options.activeGridKey.value,
      filterText: options.canSearchEntireTable.value
        ? ""
        : options.activeGridState.value.filterText,
      filters:
        options.activeObject.value === "query"
          ? options.activeGridState.value.filters
          : EMPTY_FILTER_MODEL,
    }),
    (criteria) => {
      if (localGridFilterTimer !== undefined) {
        clearTimeout(localGridFilterTimer);
        localGridFilterTimer = undefined;
      }

      const switchedGrid = criteria.gridKey !== localGridFilterCriteria.value.gridKey;
      const clearedFilters =
        !criteria.filterText.trim() && criteria.filters.conditions.length === 0;
      if (switchedGrid || clearedFilters) {
        rowTextSearch.reset();
      }
      if (switchedGrid || options.activeObject.value !== "query" || clearedFilters) {
        applyLocalGridFilterCriteria(criteria);
        return;
      }

      localGridFilterTimer = setTimeout(() => {
        localGridFilterTimer = undefined;
        applyLocalGridFilterCriteria(criteria);
      }, LOCAL_GRID_FILTER_DELAY);
    },
    { immediate: true, flush: "sync" }
  );

  const localRowFilter = computed(() =>
    options.activeObject.value === "query"
      ? createTableRowFilter(
          options.resultColumns.value,
          localGridFilterCriteria.value.filters
        )
      : undefined
  );

  watch(options.resultRows, () => {
    rowTextSearch.reset();
  });

  /** 清理本地过滤的防抖定时器（组件卸载时调用）。 */
  function disposeTableFilters() {
    if (localGridFilterTimer !== undefined) clearTimeout(localGridFilterTimer);
  }

  return {
    filterColumnType,
    defaultFilterOperator,
    addFilterCondition,
    updateFilterCondition,
    removeFilterCondition,
    setFilterLogic,
    clearColumnFilters,
    filterByColumnValue,
    filterByColumnValues,
    applyColumnFilterOperator,
    excludeByColumnValue,
    excludeByColumnValues,
    localGridFilterCriteria,
    localRowFilter,
    rowTextSearch,
    applyLocalGridFilterCriteria,
    disposeTableFilters,
  };
}
