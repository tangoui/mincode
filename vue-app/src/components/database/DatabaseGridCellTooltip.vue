<script setup lang="ts">
/**
 * One hover tooltip shared by every cell of a result grid. Cells render plain text marked with
 * `.db-grid-cell-text` and `data-cell-row` / `data-cell-column`; this component listens on the
 * grid (event delegation), and when the hovered value is actually clipped it shows the readable,
 * bounded detail anchored to that cell. A tooltip component per cell made every scroll frame of
 * a wide result mount and destroy hundreds of poppers and ResizeObservers (~80–100 ms frames).
 */
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { CopyDocument } from "@element-plus/icons-vue";
import { formatDatabaseCellTooltip } from "@/domain/database/cell-tooltip";
import { copyText } from "@/services/desktop-bridge";
import { useVueI18n } from "@/i18n/messages";

const props = defineProps<{
  /** The grid element whose `.db-grid-cell-text` descendants get the tooltip. */
  host: HTMLElement | undefined;
  /** The raw value behind a cell, from its data attributes. */
  valueAt: (row: number, column: string) => unknown;
  formatCell: (value: unknown) => string;
}>();

const { t } = useVueI18n();
const anchor = ref<HTMLElement>();
const visible = ref(false);
const value = ref<unknown>();
const content = computed(() => formatDatabaseCellTooltip(value.value, props.formatCell) ?? "");
let showTimer: ReturnType<typeof setTimeout> | undefined;
let hideTimer: ReturnType<typeof setTimeout> | undefined;
let pointerInPopper = false;

const SHOW_AFTER_MS = 350;
const HIDE_AFTER_MS = 120;

function clearTimers() {
  if (showTimer) clearTimeout(showTimer);
  if (hideTimer) clearTimeout(hideTimer);
  showTimer = hideTimer = undefined;
}

function cellOf(target: EventTarget | null) {
  return target instanceof Element ? target.closest<HTMLElement>(".db-grid-cell-text") : null;
}

function show(cell: HTMLElement) {
  const row = Number(cell.dataset.cellRow);
  const column = cell.dataset.cellColumn;
  // Only a value the cell actually clips gets the detail.
  if (!Number.isInteger(row) || column === undefined || cell.scrollWidth <= cell.clientWidth + 1) return;
  const next = props.valueAt(row, column);
  if (!formatDatabaseCellTooltip(next, props.formatCell)) return;
  anchor.value = cell;
  value.value = next;
  visible.value = true;
}

function scheduleHide() {
  if (hideTimer) clearTimeout(hideTimer);
  hideTimer = setTimeout(() => {
    if (!pointerInPopper) visible.value = false;
  }, HIDE_AFTER_MS);
}

function onPointerOver(event: Event) {
  const cell = cellOf(event.target);
  if (!cell) return;
  if (cell === anchor.value && visible.value) {
    if (hideTimer) clearTimeout(hideTimer);
    return;
  }
  clearTimers();
  if (visible.value) visible.value = false;
  showTimer = setTimeout(() => show(cell), SHOW_AFTER_MS);
}

function onPointerOut(event: MouseEvent) {
  const cell = cellOf(event.target);
  if (!cell || cell.contains(event.relatedTarget as Node | null)) return;
  if (showTimer) clearTimeout(showTimer);
  showTimer = undefined;
  if (visible.value) scheduleHide();
}

// Keyboard users focus a cell's content; treat focus like hover.
function onFocusIn(event: Event) {
  const cell = cellOf(event.target);
  if (cell) show(cell);
}

function onScroll() {
  clearTimers();
  visible.value = false;
}

function listen(element: HTMLElement | undefined, add: boolean) {
  if (!element) return;
  if (add) {
    element.addEventListener("mouseover", onPointerOver);
    element.addEventListener("mouseout", onPointerOut);
    element.addEventListener("focusin", onFocusIn);
    element.addEventListener("wheel", onScroll, { passive: true });
    // Scroll does not bubble; capture it from the grid's inner scrolling window.
    element.addEventListener("scroll", onScroll, { capture: true, passive: true });
  } else {
    element.removeEventListener("mouseover", onPointerOver);
    element.removeEventListener("mouseout", onPointerOut);
    element.removeEventListener("focusin", onFocusIn);
    element.removeEventListener("wheel", onScroll);
    element.removeEventListener("scroll", onScroll, { capture: true });
  }
}

watch(() => props.host, (next, previous) => {
  listen(previous, false);
  listen(next, true);
}, { immediate: true });

onBeforeUnmount(() => {
  listen(props.host, false);
  clearTimers();
});

function onPopperEnter() {
  pointerInPopper = true;
  if (hideTimer) clearTimeout(hideTimer);
}

function onPopperLeave() {
  pointerInPopper = false;
  scheduleHide();
}

// 复制悬停浮层全文：桌面端经 宿主剪贴板通道，避免打包 WebView 写不进系统剪贴板
async function copyContent() {
  if (!content.value) return;
  try {
    await copyText(content.value);
    ElMessage.success(t("database.cellCopied"));
  } catch (cause) {
    ElMessage.error(String(cause));
  }
}
</script>

<template>
  <el-tooltip
    :visible="visible && Boolean(content) && Boolean(anchor)"
    virtual-triggering
    :virtual-ref="anchor"
    placement="top-start"
    popper-class="db-grid-cell-tooltip"
    :persistent="false"
  >
    <template #content>
      <div class="db-grid-cell-tooltip__body" @mouseenter="onPopperEnter" @mouseleave="onPopperLeave">
        <el-scrollbar class="db-grid-cell-tooltip__scroll" :max-height="320" :native="false">
          <pre class="db-grid-cell-tooltip__content">{{ content }}</pre>
        </el-scrollbar>
        <el-button
          class="db-grid-cell-tooltip__copy"
          text
          :icon="CopyDocument"
          :aria-label="t('common.copy')"
          :title="t('common.copy')"
          @click="copyContent"
        />
      </div>
    </template>
  </el-tooltip>
</template>
