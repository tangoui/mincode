<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import type { ScrollbarInstance } from "element-plus";
import { Delete, Edit, RefreshLeft } from "@element-plus/icons-vue";
import type { PendingRowState } from "@/domain/database/grid-pending-changes";
import { calculateVirtualGridRange } from "@/domain/database/virtual-grid-range";
import DatabaseGridCellEditor from "./DatabaseGridCellEditor.vue";
import DatabaseGridCellTooltip from "./DatabaseGridCellTooltip.vue";
import type { DatabaseTemporalEditorKind } from "@/domain/database/temporal-editor";
import { useVueI18n } from "@/i18n/messages";

export interface ResultGridColumn {
  name: string;
  index: number;
  dataType?: string;
  comment?: string | null;
}

export interface ResultGridRow {
  row: unknown[];
  originalIndex: number;
}

export interface ResultGridEditingCell {
  rowIndex: number;
  column: string;
  value: string;
  editorKind: DatabaseTemporalEditorKind;
}

export interface ResultGridContextMenu {
  x: number;
  y: number;
  rowIndex: number;
  column: string;
  value: unknown;
}

export interface ResultGridHeaderContextMenu {
  x: number;
  y: number;
  column: string;
}

type SortDirection = "asc" | "desc";

const props = withDefaults(
  defineProps<{
    columns: ResultGridColumn[];
    rows: ResultGridRow[];
    fieldName: string;
    mode: "query" | "table";
    page?: number;
    pageSize?: number;
    primaryKeys?: string[];
    canEditRows?: boolean;
    editingCell?: ResultGridEditingCell;
    sort?: { column: string; direction: SortDirection };
    formatCell: (value: unknown) => string;
    reorderColumnLabel?: (column: string) => string;
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
    reorderColumnLabel: (column: string) => `Drag to reorder ${column}`,
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

const FIELD_WIDTH = 225;
const RECORD_WIDTH = 148;
const OVERSCAN_COLUMNS = 3;

const { t } = useVueI18n();

const scroller = ref<ScrollbarInstance>();
const scrollLeft = ref(0);
const viewportWidth = ref(0);
let resizeObserver: ResizeObserver | undefined;
let scrollRaf = 0;

function updateViewport() {
  const element = scroller.value?.wrapRef;
  if (!element) return;
  scrollLeft.value = element.scrollLeft;
  viewportWidth.value = element.clientWidth;
}

function handleScroll() {
  if (scrollRaf) return;
  scrollRaf = requestAnimationFrame(() => {
    scrollRaf = 0;
    const element = scroller.value?.wrapRef;
    if (!element) return;
    scrollLeft.value = element.scrollLeft;
  });
}

const columnRange = computed(() =>
  calculateVirtualGridRange({
    itemCount: props.rows.length,
    scrollOffset: scrollLeft.value,
    viewportSize: viewportWidth.value,
    itemSize: RECORD_WIDTH,
    frozenSize: FIELD_WIDTH,
    minimumOverscanItems: OVERSCAN_COLUMNS,
  })
);

const visibleRows = computed(() =>
  props.rows.slice(columnRange.value.start, columnRange.value.end).map((entry, offset) => ({
    ...entry,
    visualIndex: columnRange.value.start + offset,
    style: {
      left: `${FIELD_WIDTH + (columnRange.value.start + offset) * RECORD_WIDTH}px`,
      width: `${RECORD_WIDTH}px`,
    },
  }))
);
const surfaceWidth = computed(() => `${FIELD_WIDTH + props.rows.length * RECORD_WIDTH}px`);
const selectedRowIndexes = computed(() => new Set(props.selectedRowIndexes));
const primaryKeySet = computed(() => new Set(props.primaryKeys));

function recordNumber(entry: ResultGridRow & { visualIndex: number }) {
  if (props.mode === "query") return `#${entry.visualIndex + 1}`;
  return `#${(props.page - 1) * props.pageSize + entry.originalIndex + 1}`;
}

function isRowSelected(entry: ResultGridRow) {
  return selectedRowIndexes.value.has(entry.originalIndex);
}

const draggedColumn = ref<string>();
const dropTargetColumn = ref<string>();

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
  if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
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

onMounted(() => {
  const element = scroller.value?.wrapRef;
  if (!element) return;
  resizeObserver = new ResizeObserver(updateViewport);
  resizeObserver.observe(element);
  updateViewport();
});

onBeforeUnmount(() => {
  resizeObserver?.disconnect();
  if (scrollRaf) cancelAnimationFrame(scrollRaf);
});

watch(
  () => [props.rows.length, props.columns.length],
  () => void nextTick(updateViewport)
);

/** The value behind a cell, for the shared tooltip (cells carry the row's originalIndex). */
function tooltipValue(originalIndex: number, column: string) {
  const entry = props.rows.find((row) => row.originalIndex === originalIndex);
  const meta = props.columns.find((item) => item.name === column);
  return entry && meta ? entry.row[meta.index] : undefined;
}
</script>

<template>
  <el-scrollbar ref="scroller" class="db-virtual-grid" role="grid" @scroll="handleScroll">
    <div class="db-virtual-grid__surface" :style="{ width: surfaceWidth }">
      <div class="db-virtual-grid__row db-virtual-grid__header" role="row">
        <div class="db-virtual-grid__field db-virtual-grid__field--header" role="columnheader">
          <el-checkbox
            :model-value="allRowsSelected"
            :indeterminate="someRowsSelected && !allRowsSelected"
            :aria-label="selectAllRowsLabel"
            @click.stop
            @change="emit('toggle-all-row-selection', Boolean($event))"
          />
          <span>{{ fieldName }}</span>
        </div>
        <div
          v-for="entry in visibleRows"
          :key="entry.originalIndex"
          class="db-virtual-grid__record db-virtual-grid__record--header"
          :class="[{ 'is-selected': isRowSelected(entry) }, pendingRowClass(entry)]"
          role="columnheader"
          :style="entry.style"
        >
          <el-checkbox
            :model-value="isRowSelected(entry)"
            :aria-label="rowSelectionLabel(recordNumber(entry))"
            @click.stop
            @change="emit('toggle-row-selection', entry.originalIndex, Boolean($event))"
          />
          <span class="db-record-number">{{ recordNumber(entry) }}</span>
          <span
            v-if="pendingRow(entry)"
            class="db-grid-pending-mark"
            role="img"
            :title="pendingRowLabel(entry)"
            :aria-label="pendingRowLabel(entry)"
          />
          <el-button
            v-if="mode === 'table' && canEditRows"
            text
            native-type="button"
            class="db-record-edit"
            :icon="Edit"
            :disabled="pendingRow(entry)?.kind === 'delete'"
            :aria-label="t('common.edit')"
            :title="t('common.edit')"
            @click="emit('edit-row', entry.originalIndex)"
          />
          <el-button
            v-if="mode === 'table' && canEditRows"
            text
            native-type="button"
            class="db-record-delete"
            :icon="pendingRow(entry)?.kind === 'delete' ? RefreshLeft : Delete"
            :aria-label="pendingRow(entry)?.kind === 'delete' ? t('database.pending.restoreRow') : t('common.delete')"
            :title="pendingRow(entry)?.kind === 'delete' ? t('database.pending.restoreRow') : t('common.delete')"
            @click="emit('delete-row', entry.originalIndex)"
          />
        </div>
      </div>

      <div v-for="column in columns" :key="column.name" class="db-virtual-grid__row" role="row">
        <div
          class="db-virtual-grid__field"
          :class="{
            'is-primary-key': mode === 'table' && isPrimaryKey(column.name),
            'is-drop-target': dropTargetColumn === column.name && draggedColumn !== column.name,
          }"
          role="rowheader"
          @contextmenu.prevent="handleHeaderContextMenu($event, column.name)"
          @dragover="dragOverColumn($event, column.name)"
          @dragleave="dropTargetColumn === column.name && (dropTargetColumn = undefined)"
          @drop="dropColumn($event, column.name)"
        >
          <span
            class="db-field-drag-handle"
            draggable="true"
            :title="reorderColumnLabel(column.name)"
            :aria-label="reorderColumnLabel(column.name)"
            @dragstart="startColumnDrag($event, column.name)"
            @dragend="resetColumnDrag"
            ><svg viewBox="0 0 10 14" width="10" height="14" aria-hidden="true" focusable="false">
              <circle cx="2.5" cy="2" r="1.3" /><circle cx="7.5" cy="2" r="1.3" />
              <circle cx="2.5" cy="7" r="1.3" /><circle cx="7.5" cy="7" r="1.3" />
              <circle cx="2.5" cy="12" r="1.3" /><circle cx="7.5" cy="12" r="1.3" /></svg
          ></span>
          <div class="db-grid-column-heading">
            <button type="button" class="db-grid-sort" @click="emit('sort', column.name)">
              {{ column.name }}
              <span v-if="sort?.column === column.name">{{
                sort.direction === "asc" ? "↑" : "↓"
              }}</span>
            </button>
            <span v-if="column.dataType || column.comment" class="db-grid-column-meta">
              <small v-if="column.dataType">{{ column.dataType }}</small>
              <small v-if="column.comment" :title="column.comment">{{ column.comment }}</small>
            </span>
          </div>
        </div>
        <div
          v-for="entry in visibleRows"
          :key="entry.originalIndex"
          class="db-virtual-grid__record db-virtual-grid__cell"
          :class="[{
            'is-null': entry.row[column.index] == null,
            'is-editing': isEditingCell(entry, column.name),
            'is-selected': isRowSelected(entry),
            'is-pending-changed': isPendingCell(entry, column.name),
          }, pendingRowClass(entry)]"
          role="gridcell"
          :style="entry.style"
          @dblclick="handleCellDoubleClick(entry, column)"
          @contextmenu.prevent="handleCellContextMenu($event, entry, column)"
        >
          <DatabaseGridCellEditor
            v-if="isEditingCell(entry, column.name)"
            :value="editingCell?.value ?? ''"
            :editor-kind="editingCell?.editorKind ?? 'text'"
            :ariaLabel="column.name"
            @update-value="
              emit('update-cell-edit', {
                rowIndex: entry.originalIndex,
                column: column.name,
                value: $event,
                editorKind: editingCell?.editorKind ?? 'text',
              })
            "
            @commit="emit('commit-cell-edit', { rowIndex: entry.originalIndex, column: column.name })"
            @cancel="emit('cancel-cell-edit', { rowIndex: entry.originalIndex, column: column.name })"
          />
          <span
            v-else
            class="db-grid-cell-text"
            :data-cell-row="entry.originalIndex"
            :data-cell-column="column.name"
          >{{ formatCell(entry.row[column.index]) }}</span>
        </div>
      </div>
    </div>
    <DatabaseGridCellTooltip :host="scroller?.$el" :value-at="tooltipValue" :format-cell="formatCell" />
  </el-scrollbar>
</template>
