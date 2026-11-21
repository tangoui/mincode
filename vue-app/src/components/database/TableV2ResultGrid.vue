<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { TableV2FixedDir } from "element-plus";
import type { Column } from "element-plus";
import { Delete, Edit, RefreshLeft } from "@element-plus/icons-vue";
import type { PendingRowState } from "@/domain/database/grid-pending-changes";
import DatabaseGridCellEditor from "./DatabaseGridCellEditor.vue";
import DatabaseGridCellTooltip from "./DatabaseGridCellTooltip.vue";
import { useVueI18n } from "@/i18n/messages";
import type {
  ResultGridColumn,
  ResultGridContextMenu,
  ResultGridEditingCell,
  ResultGridHeaderContextMenu,
  ResultGridRow,
} from "./VirtualResultGrid.vue";

type SortDirection = "asc" | "desc";

const props = withDefaults(
  defineProps<{
    columns: ResultGridColumn[];
    rows: ResultGridRow[];
    mode: "query" | "table";
    page?: number;
    pageSize?: number;
    primaryKeys?: string[];
    canEditRows?: boolean;
    editingCell?: ResultGridEditingCell;
    sort?: { column: string; direction: SortDirection };
    /** 冻结的前 N 个字段列（0 表示不冻结）。 */
    frozenCount?: number;
    formatCell: (value: unknown) => string;
    reorderColumnLabel?: (column: string) => string;
    resizeColumnLabel?: (column: string) => string;
    selectedRowIndexes?: number[];
    allRowsSelected?: boolean;
    someRowsSelected?: boolean;
    selectAllRowsLabel?: string;
    rowSelectionLabel?: (record: string) => string;
    /** 暂存修改的行状态（以 originalIndex 为键）。 */
    pendingRows?: ReadonlyMap<number, PendingRowState>;
  }>(),
  {
    page: 1,
    pageSize: 50,
    primaryKeys: () => [],
    canEditRows: false,
    sort: undefined,
    frozenCount: 0,
    reorderColumnLabel: (column: string) => `Drag to reorder ${column}`,
    resizeColumnLabel: (column: string) => `Resize ${column}`,
    selectedRowIndexes: () => [],
    allRowsSelected: false,
    someRowsSelected: false,
    selectAllRowsLabel: "Select all rows",
    rowSelectionLabel: (record: string) => `Select ${record}`,
  }
);

const emit = defineEmits<{
  sort: [column: string];
  "cell-detail": [payload: { column: string; value: unknown }];
  "begin-cell-edit": [payload: { rowIndex: number; column: string }];
  "update-cell-edit": [payload: ResultGridEditingCell];
  "commit-cell-edit": [payload: { rowIndex: number; column: string }];
  "cancel-cell-edit": [payload: { rowIndex: number; column: string }];
  "context-menu": [payload: ResultGridContextMenu];
  "header-context-menu": [payload: ResultGridHeaderContextMenu];
  "delete-row": [rowIndex: number];
  "edit-row": [rowIndex: number];
  "reorder-columns": [source: string, target: string];
  "toggle-row-selection": [rowIndex: number, selected: boolean];
  "toggle-all-row-selection": [selected: boolean];
}>();

const INDEX_KEY = "__index";
const ACTIONS_KEY = "__actions";
const SPACER_LEFT_KEY = "__spacer-left";
const SPACER_RIGHT_KEY = "__spacer-right";
const DEFAULT_INDEX_WIDTH = 66;
const DEFAULT_COLUMN_WIDTH = 178;
const MIN_INDEX_WIDTH = 52;
const MIN_COLUMN_WIDTH = 96;
const MAX_COLUMN_WIDTH = 720;
const HEADER_HEIGHT = 54;
const ROW_HEIGHT = 36;
const ACTIONS_WIDTH = 84;
const OVERSCAN_COLUMN_PIXELS = DEFAULT_COLUMN_WIDTH * 2;

const { t } = useVueI18n();

const container = ref<HTMLElement>();
const scrollLeft = ref(0);
const viewportWidth = ref(0);
let resizeObserver: ResizeObserver | undefined;
let scrollRaf = 0;
const draggedColumn = ref<string>();
const dropTargetColumn = ref<string>();
const indexWidth = ref(DEFAULT_INDEX_WIDTH);
const columnWidths = ref<Record<string, number>>({});
const resizingColumn = ref<string>();
let activeColumnResize:
  | { column?: string; startX: number; startWidth: number; minWidth: number }
  | undefined;
let resizeRaf = 0;

const selectedRowIndexes = computed(() => new Set(props.selectedRowIndexes));
const primaryKeySet = computed(() => new Set(props.primaryKeys));
const showActions = computed(() => props.mode === "table" && props.canEditRows);

function storedColumnWidth(column: string) {
  return columnWidths.value[column] ?? DEFAULT_COLUMN_WIDTH;
}

// 容器比所有列基础宽度更宽时，把剩余宽度均分给未手动调过宽的字段列，
// 让网格撑满可用宽度而不是在右侧留下死区；手动调过宽的列保持用户设定。
const effectiveColumnWidths = computed(() => {
  const widths: Record<string, number> = {};
  for (const column of props.columns) widths[column.name] = storedColumnWidth(column.name);
  const available =
    viewportWidth.value - indexWidth.value - (showActions.value ? ACTIONS_WIDTH : 0);
  const totalBase = props.columns.reduce((sum, column) => sum + widths[column.name], 0);
  let pool = available > 0 ? Math.max(0, available - totalBase) : 0;
  let targets = props.columns
    .filter((column) => columnWidths.value[column.name] === undefined)
    .map((column) => column.name);
  while (pool > 0 && targets.length) {
    const share = Math.floor(pool / targets.length);
    if (share <= 0) break;
    const nextTargets: string[] = [];
    for (const name of targets) {
      const add = Math.min(MAX_COLUMN_WIDTH - widths[name], share);
      widths[name] += add;
      pool -= add;
      if (widths[name] < MAX_COLUMN_WIDTH) nextTargets.push(name);
    }
    targets = nextTargets;
  }
  // 均分封顶后仍剩的宽度交给最后一个未手动调宽的列，保证有富余时表格始终撑满
  if (pool > 0) {
    for (let i = props.columns.length - 1; i >= 0; i--) {
      const name = props.columns[i].name;
      if (columnWidths.value[name] === undefined) {
        widths[name] += pool;
        break;
      }
    }
  }
  return widths;
});

function columnWidth(column: string) {
  return effectiveColumnWidths.value[column] ?? DEFAULT_COLUMN_WIDTH;
}

type PositionedColumn = ResultGridColumn & { left: number; right: number };

const gridLayout = computed(() => {
  let left = 0;
  const columns: PositionedColumn[] = props.columns.map((column) => {
    const width = columnWidth(column.name);
    const positioned = { ...column, left, right: left + width };
    left += width;
    return positioned;
  });
  return { columns, totalWidth: left };
});

const frozenColumnCount = computed(() =>
  Math.max(0, Math.min(props.frozenCount, props.columns.length))
);
const frozenWidth = computed(() => {
  let width = 0;
  const all = gridLayout.value.columns;
  for (let index = 0; index < frozenColumnCount.value; index += 1) {
    width += all[index].right - all[index].left;
  }
  return width;
});

// el-table-v2 只虚拟化行：用两根垫片列撑住总宽度，只挂载视口附近的字段列；
// 冻结列不参与窗口化，垫片宽度从冻结区末尾起算。
const columnWindow = computed(() => {
  const all = gridLayout.value.columns.slice(frozenColumnCount.value);
  if (viewportWidth.value <= 0 || all.length === 0) {
    return { visible: all, leftPad: 0, rightPad: 0 };
  }
  const start = Math.max(0, scrollLeft.value - OVERSCAN_COLUMN_PIXELS);
  const end = scrollLeft.value + viewportWidth.value + OVERSCAN_COLUMN_PIXELS;
  const visible = all.filter((column) => column.right >= start && column.left <= end);
  if (!visible.length) return { visible: all, leftPad: 0, rightPad: 0 };
  const last = visible[visible.length - 1];
  return {
    visible,
    leftPad: Math.max(0, visible[0].left - frozenWidth.value),
    rightPad: Math.max(0, gridLayout.value.totalWidth - last.right),
  };
});

const tableColumns = computed<Column<ResultGridRow>[]>(() => {
  const list: Column<ResultGridRow>[] = [
    {
      key: INDEX_KEY,
      title: "#",
      width: indexWidth.value,
      minWidth: MIN_INDEX_WIDTH,
      // 主表中的固定列占位单元格按非固定列参与 flex，不禁止收缩会被压到 minWidth，
      // 与左固定层宽度失配而遮住首列内容
      flexShrink: 0,
      fixed: TableV2FixedDir.LEFT,
    },
  ];
  const { visible, leftPad, rightPad } = columnWindow.value;
  // 冻结的字段列紧跟索引列，保证左固定列在数组起始处连续（el-table-v2 约束）；
  // EP 对非固定列默认 flexShrink:1，总宽超容器时会压缩列宽，
  // 使渲染宽度偏离垫片/窗口化计算，故全部列禁止收缩，宽度完全由本组件控制。
  for (let index = 0; index < frozenColumnCount.value; index += 1) {
    const column = gridLayout.value.columns[index];
    list.push({
      key: column.name,
      title: column.name,
      width: columnWidth(column.name),
      minWidth: MIN_COLUMN_WIDTH,
      flexShrink: 0,
      fixed: TableV2FixedDir.LEFT,
    });
  }
  if (leftPad > 0) {
    list.push({ key: SPACER_LEFT_KEY, title: "", width: leftPad, minWidth: 0, flexShrink: 0 });
  }
  for (const column of visible) {
    list.push({
      key: column.name,
      title: column.name,
      width: columnWidth(column.name),
      minWidth: MIN_COLUMN_WIDTH,
      flexShrink: 0,
    });
  }
  if (rightPad > 0) {
    list.push({ key: SPACER_RIGHT_KEY, title: "", width: rightPad, minWidth: 0, flexShrink: 0 });
  }
  if (showActions.value) {
    list.push({
      key: ACTIONS_KEY,
      title: "",
      width: ACTIONS_WIDTH,
      flexShrink: 0,
      fixed: TableV2FixedDir.RIGHT,
    });
  }
  return list;
});

function rowClass({ rowData, rowIndex }: { rowData: ResultGridRow; rowIndex: number }) {
  return [
    rowIndex % 2 === 1 ? "is-striped" : "",
    selectedRowIndexes.value.has(rowData.originalIndex) ? "is-selected" : "",
    pendingRowClass(rowData),
  ].filter(Boolean).join(" ");
}

function recordNumber(entry: ResultGridRow, rowIndex: number) {
  if (props.mode === "query") return `#${rowIndex + 1}`;
  return `#${(props.page - 1) * props.pageSize + entry.originalIndex + 1}`;
}

function isRowSelected(entry: ResultGridRow) {
  return selectedRowIndexes.value.has(entry.originalIndex);
}

function sortIndicator(column: string) {
  if (props.sort?.column !== column) return "";
  return props.sort.direction === "asc" ? "↑" : "↓";
}

function isPrimaryKey(column: string) {
  return primaryKeySet.value.has(column);
}

function pendingRow(entry: ResultGridRow) {
  return props.pendingRows?.get(entry.originalIndex);
}

function pendingRowClass(entry: ResultGridRow) {
  const state = pendingRow(entry);
  if (!state) return "";
  return `is-pending-${state.kind}${state.failed ? " is-pending-failed" : ""}`;
}

function isPendingCell(entry: ResultGridRow, column: string) {
  const state = pendingRow(entry);
  return state?.kind === "update" && state.columns.has(column);
}

function pendingRowLabel(entry: ResultGridRow) {
  const state = pendingRow(entry);
  if (!state) return "";
  const label = t(`database.pending.mark.${state.kind}`);
  return state.failed ? `${label} · ${t("database.pending.mark.failed")}` : label;
}

function canInlineEdit(column: string) {
  return props.mode === "table" && props.canEditRows && !isPrimaryKey(column);
}

function isEditingCell(entry: ResultGridRow, column: string) {
  return props.editingCell?.rowIndex === entry.originalIndex && props.editingCell.column === column;
}

function columnMeta(column: string) {
  return props.columns.find((item) => item.name === column);
}

function cellValue(entry: ResultGridRow, column: string) {
  const meta = columnMeta(column);
  return meta ? entry.row[meta.index] : undefined;
}

/** The value behind a cell, for the shared tooltip (cells carry the row's originalIndex). */
function tooltipValue(originalIndex: number, column: string) {
  const entry = props.rows.find((row) => row.originalIndex === originalIndex);
  return entry ? cellValue(entry, column) : undefined;
}

function handleCellDoubleClick(entry: ResultGridRow, column: ResultGridColumn) {
  if (canInlineEdit(column.name)) {
    emit("begin-cell-edit", { rowIndex: entry.originalIndex, column: column.name });
    return;
  }
  if (props.mode === "query") {
    emit("cell-detail", { column: column.name, value: entry.row[column.index] });
  }
}

function handleCellContextMenu(event: MouseEvent, entry: ResultGridRow, column: ResultGridColumn) {
  emit("context-menu", {
    x: event.clientX,
    y: event.clientY,
    rowIndex: entry.originalIndex,
    column: column.name,
    value: entry.row[column.index],
  });
}

function handleHeaderContextMenu(event: MouseEvent, column: string) {
  emit("header-context-menu", { x: event.clientX, y: event.clientY, column });
}

function resetColumnDrag() {
  draggedColumn.value = undefined;
  dropTargetColumn.value = undefined;
}

function startColumnDrag(event: DragEvent, column: string) {
  draggedColumn.value = column;
  event.dataTransfer?.setData("text/plain", column);
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = "move";
    const cell = (event.currentTarget as HTMLElement).closest(".db-tablev2-grid__header-content");
    if (cell) event.dataTransfer.setDragImage(cell, 20, 20);
  }
}

function dragOverColumn(event: DragEvent, target: string) {
  const source = draggedColumn.value || event.dataTransfer?.getData("text/plain");
  if (!source || source === target) return;
  event.preventDefault();
  dropTargetColumn.value = target;
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
}

function dropColumn(event: DragEvent, target: string) {
  event.preventDefault();
  const source = draggedColumn.value || event.dataTransfer?.getData("text/plain");
  if (source && source !== target) emit("reorder-columns", source, target);
  resetColumnDrag();
}

function applyColumnResize(width: number) {
  const resize = activeColumnResize;
  if (!resize) return;
  const nextWidth = Math.max(resize.minWidth, Math.min(MAX_COLUMN_WIDTH, Math.round(width)));
  if (resize.column) {
    columnWidths.value = { ...columnWidths.value, [resize.column]: nextWidth };
  } else {
    indexWidth.value = nextWidth;
  }
}

function handleColumnResizeMove(event: PointerEvent) {
  if (!activeColumnResize) return;
  const clientX = event.clientX;
  if (resizeRaf) cancelAnimationFrame(resizeRaf);
  resizeRaf = requestAnimationFrame(() => {
    resizeRaf = 0;
    if (!activeColumnResize) return;
    applyColumnResize(activeColumnResize.startWidth + clientX - activeColumnResize.startX);
  });
}

function stopColumnResize() {
  if (resizeRaf) {
    cancelAnimationFrame(resizeRaf);
    resizeRaf = 0;
  }
  activeColumnResize = undefined;
  resizingColumn.value = undefined;
  document.body.style.cursor = "";
  document.body.style.userSelect = "";
  window.removeEventListener("pointermove", handleColumnResizeMove);
  window.removeEventListener("pointerup", stopColumnResize);
  window.removeEventListener("pointercancel", stopColumnResize);
}

function startColumnResize(event: PointerEvent, column?: string) {
  event.preventDefault();
  event.stopPropagation();
  stopColumnResize();
  const startWidth = column ? columnWidth(column) : indexWidth.value;
  activeColumnResize = {
    column,
    startX: event.clientX,
    startWidth,
    minWidth: column ? MIN_COLUMN_WIDTH : MIN_INDEX_WIDTH,
  };
  resizingColumn.value = column ?? "#";
  document.body.style.cursor = "col-resize";
  document.body.style.userSelect = "none";
  window.addEventListener("pointermove", handleColumnResizeMove);
  window.addEventListener("pointerup", stopColumnResize);
  window.addEventListener("pointercancel", stopColumnResize);
}

function resizeColumnWithKeyboard(event: KeyboardEvent, column?: string) {
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
  event.preventDefault();
  const currentWidth = column ? columnWidth(column) : indexWidth.value;
  activeColumnResize = {
    column,
    startX: 0,
    startWidth: currentWidth,
    minWidth: column ? MIN_COLUMN_WIDTH : MIN_INDEX_WIDTH,
  };
  applyColumnResize(currentWidth + (event.key === "ArrowRight" ? 12 : -12));
  activeColumnResize = undefined;
}

function handleScroll(params: { scrollLeft: number; scrollTop: number }) {
  if (scrollRaf) return;
  scrollRaf = requestAnimationFrame(() => {
    scrollRaf = 0;
    scrollLeft.value = params.scrollLeft;
  });
}

onMounted(() => {
  if (!container.value) return;
  resizeObserver = new ResizeObserver(() => {
    viewportWidth.value = container.value?.clientWidth ?? 0;
  });
  resizeObserver.observe(container.value);
  viewportWidth.value = container.value.clientWidth;
});

onBeforeUnmount(() => {
  resizeObserver?.disconnect();
  if (scrollRaf) cancelAnimationFrame(scrollRaf);
  stopColumnResize();
});
</script>

<template>
  <div ref="container" class="db-tablev2-grid">
  <el-auto-resizer class="db-tablev2-grid__resizer">
    <template #default="{ height, width }">
      <el-table-v2
        v-if="height > 0 && width > 0"
        :columns="tableColumns"
        :data="rows"
        :width="width"
        :height="height"
        :row-height="ROW_HEIGHT"
        :header-height="HEADER_HEIGHT"
        row-key="originalIndex"
        :cache="8"
        :row-class="rowClass"
        :on-scroll="handleScroll"
        scrollbar-always-on
        fixed
        :aria-label="t('database.resultGrid')"
      >
        <template #header-cell="{ column }">
          <div
            v-if="column.key === SPACER_LEFT_KEY || column.key === SPACER_RIGHT_KEY"
            class="db-tablev2-grid__header-content db-tablev2-grid__spacer"
            aria-hidden="true"
          />
          <div
            v-else-if="column.key === INDEX_KEY"
            class="db-tablev2-grid__header-content db-tablev2-grid__index--header"
          >
            <el-checkbox
              :model-value="allRowsSelected"
              :indeterminate="someRowsSelected && !allRowsSelected"
              :aria-label="selectAllRowsLabel"
              @click.stop
              @change="emit('toggle-all-row-selection', Boolean($event))"
            />
            <span>#</span>
            <span
              class="db-tablev2-grid__resize-handle"
              role="separator"
              tabindex="0"
              :aria-label="resizeColumnLabel('#')"
              @pointerdown="startColumnResize($event)"
              @keydown="resizeColumnWithKeyboard($event)"
            />
          </div>
          <div
            v-else-if="column.key === ACTIONS_KEY"
            class="db-tablev2-grid__header-content db-tablev2-grid__actions-header"
          >
            <span>{{ t("database.actions") }}</span>
          </div>
          <div
            v-else
            class="db-tablev2-grid__header-content"
            :class="{
              'is-drop-target': dropTargetColumn === column.key && draggedColumn !== column.key,
              'is-dragging': draggedColumn === column.key,
              'is-resizing': resizingColumn === column.key,
            }"
            @contextmenu.prevent="handleHeaderContextMenu($event, String(column.key))"
            @dragover="dragOverColumn($event, String(column.key))"
            @dragleave="dropTargetColumn === column.key && (dropTargetColumn = undefined)"
            @drop="dropColumn($event, String(column.key))"
          >
            <span
              class="db-field-drag-handle"
              draggable="true"
              :title="reorderColumnLabel(String(column.key))"
              :aria-label="reorderColumnLabel(String(column.key))"
              @dragstart="startColumnDrag($event, String(column.key))"
              @dragend="resetColumnDrag"
              ><svg viewBox="0 0 10 14" width="10" height="14" aria-hidden="true" focusable="false">
                <circle cx="2.5" cy="2" r="1.3" /><circle cx="7.5" cy="2" r="1.3" />
                <circle cx="2.5" cy="7" r="1.3" /><circle cx="7.5" cy="7" r="1.3" />
                <circle cx="2.5" cy="12" r="1.3" /><circle cx="7.5" cy="12" r="1.3" /></svg
            ></span>
            <div class="db-grid-column-heading">
              <button type="button" class="db-grid-sort" @click="emit('sort', String(column.key))">
                {{ column.key }}
                <span v-if="sortIndicator(String(column.key))">{{ sortIndicator(String(column.key)) }}</span>
              </button>
              <span
                v-if="columnMeta(String(column.key))?.dataType || columnMeta(String(column.key))?.comment"
                class="db-grid-column-meta"
              >
                <small v-if="columnMeta(String(column.key))?.dataType">{{
                  columnMeta(String(column.key))?.dataType
                }}</small>
                <small
                  v-if="columnMeta(String(column.key))?.comment"
                  :title="columnMeta(String(column.key))?.comment ?? undefined"
                  >{{ columnMeta(String(column.key))?.comment }}</small
                >
              </span>
            </div>
            <span
              v-if="mode === 'table' && isPrimaryKey(String(column.key))"
              class="db-tablev2-grid__pk"
              >PK</span
            >
            <span
              class="db-tablev2-grid__resize-handle"
              role="separator"
              tabindex="0"
              :aria-label="resizeColumnLabel(String(column.key))"
              @pointerdown="startColumnResize($event, String(column.key))"
              @keydown="resizeColumnWithKeyboard($event, String(column.key))"
            />
          </div>
        </template>
        <template #cell="{ column, rowData, rowIndex }">
          <div
            v-if="column.key === SPACER_LEFT_KEY || column.key === SPACER_RIGHT_KEY"
            class="db-tablev2-grid__cell-content db-tablev2-grid__spacer"
            aria-hidden="true"
          />
          <div
            v-else-if="column.key === INDEX_KEY"
            class="db-tablev2-grid__cell-content db-tablev2-grid__index"
          >
            <el-checkbox
              :model-value="isRowSelected(rowData)"
              :aria-label="rowSelectionLabel(recordNumber(rowData, rowIndex))"
              @click.stop
              @change="emit('toggle-row-selection', rowData.originalIndex, Boolean($event))"
            />
            <span class="db-record-number">{{ recordNumber(rowData, rowIndex) }}</span>
            <span
              v-if="pendingRow(rowData)"
              class="db-grid-pending-mark"
              role="img"
              :title="pendingRowLabel(rowData)"
              :aria-label="pendingRowLabel(rowData)"
            />
          </div>
          <div
            v-else-if="column.key === ACTIONS_KEY"
            class="db-tablev2-grid__cell-content db-tablev2-grid__actions"
          >
            <el-button
              text
              native-type="button"
              class="db-record-edit"
              :icon="Edit"
              :disabled="pendingRow(rowData)?.kind === 'delete'"
              :aria-label="t('common.edit')"
              :title="t('common.edit')"
              @click="emit('edit-row', rowData.originalIndex)"
            />
            <el-button
              text
              native-type="button"
              class="db-record-delete"
              :icon="pendingRow(rowData)?.kind === 'delete' ? RefreshLeft : Delete"
              :aria-label="pendingRow(rowData)?.kind === 'delete' ? t('database.pending.restoreRow') : t('common.delete')"
              :title="pendingRow(rowData)?.kind === 'delete' ? t('database.pending.restoreRow') : t('common.delete')"
              @click="emit('delete-row', rowData.originalIndex)"
            />
          </div>
          <div
            v-else
            class="db-tablev2-grid__cell-content"
            :class="{
              'is-null': cellValue(rowData, String(column.key)) == null,
              'is-editing': isEditingCell(rowData, String(column.key)),
              'is-pending-changed': isPendingCell(rowData, String(column.key)),
            }"
            @dblclick="handleCellDoubleClick(rowData, columnMeta(String(column.key))!)"
            @contextmenu.prevent="handleCellContextMenu($event, rowData, columnMeta(String(column.key))!)"
          >
            <DatabaseGridCellEditor
              v-if="isEditingCell(rowData, String(column.key))"
              :value="editingCell?.value ?? ''"
              :editor-kind="editingCell?.editorKind ?? 'text'"
              :ariaLabel="String(column.key)"
              @update-value="
                emit('update-cell-edit', {
                  rowIndex: rowData.originalIndex,
                  column: String(column.key),
                  value: $event,
                  editorKind: editingCell?.editorKind ?? 'text',
                })
              "
              @commit="emit('commit-cell-edit', { rowIndex: rowData.originalIndex, column: String(column.key) })"
              @cancel="emit('cancel-cell-edit', { rowIndex: rowData.originalIndex, column: String(column.key) })"
            />
            <span
              v-else
              class="db-grid-cell-text"
              :data-cell-row="rowData.originalIndex"
              :data-cell-column="String(column.key)"
            >{{ formatCell(cellValue(rowData, String(column.key))) }}</span>
          </div>
        </template>
      </el-table-v2>
    </template>
  </el-auto-resizer>
  <DatabaseGridCellTooltip :host="container" :value-at="tooltipValue" :format-cell="formatCell" />
  </div>
</template>
