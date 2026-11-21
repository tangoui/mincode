/**
 * Windowed rendering for grouped lists of fixed-height rows inside one scrolling element
 * (Git change groups, SSH connection groups, …). Each group's rows sit in a container
 * marked `data-row-group="<key>"`; only the rows near the viewport are rendered and spacers
 * keep every group's height (see domain/ui/row-window). Windowing starts when all open groups
 * together exceed the threshold (many small groups add up); short lists render every row.
 */
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from "vue";
import { rowWindow, ROW_WINDOW_FALLBACK_ROWS, ROW_WINDOW_THRESHOLD, type RowWindow } from "@/domain/ui/row-window";

export interface GroupedRowWindowOptions {
  /** The element that scrolls (for el-scrollbar: its `wrapRef`). */
  scroller: () => HTMLElement | undefined;
  /** Row count of every rendered (open) group; a change re-measures the layout and the total decides windowing. */
  counts: () => Readonly<Record<string, number>>;
  /** Selector of a rendered row, used to measure the real row height. */
  rowSelector: string;
  estimatedRowHeight: number;
  threshold?: number;
  /** Total row budget across open groups of a windowed list before viewport measurement. Defaults to 60. */
  fallbackRows?: number;
}

export interface GroupWindow<T> extends RowWindow {
  rows: T[];
}

export const ROW_GROUP_ATTRIBUTE = "data-row-group";

export function useGroupedRowWindow(options: GroupedRowWindowOptions) {
  const threshold = options.threshold ?? ROW_WINDOW_THRESHOLD;
  const fallbackRows = options.fallbackRows ?? ROW_WINDOW_FALLBACK_ROWS;
  const scrollTop = ref(0);
  const viewportHeight = ref(0);
  const rowHeight = ref(options.estimatedRowHeight);
  /** Offset of each group's first row from the top of the scrolled content. */
  const groupTop = reactive<Record<string, number>>({});
  let frame = 0;
  let observer: ResizeObserver | undefined;

  function measure() {
    const element = options.scroller();
    if (!element) return;
    scrollTop.value = element.scrollTop;
    viewportHeight.value = element.clientHeight;
    // Rounded and written only on change: sub-pixel jitter must not re-render in a loop.
    const contentTop = element.getBoundingClientRect().top - element.scrollTop;
    for (const group of element.querySelectorAll<HTMLElement>(`[${ROW_GROUP_ATTRIBUTE}]`)) {
      const key = group.getAttribute(ROW_GROUP_ATTRIBUTE) ?? "";
      const top = Math.round(group.getBoundingClientRect().top - contentTop);
      if (groupTop[key] !== top) groupTop[key] = top;
    }
    const row = element.querySelector<HTMLElement>(options.rowSelector);
    if (row?.offsetHeight && row.offsetHeight !== rowHeight.value) rowHeight.value = row.offsetHeight;
  }

  function onScroll() {
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      measure();
    });
  }

  const windowed = computed(() => Object.values(options.counts()).reduce((sum, count) => sum + count, 0) > threshold);

  /** Rows of the groups listed before each group (counts() is in render order). */
  const rowsBefore = computed(() => {
    const before: Record<string, number> = {};
    let total = 0;
    for (const [group, count] of Object.entries(options.counts())) {
      before[group] = total;
      total += count;
    }
    return before;
  });

  /** The rows of a group to render and the spacer heights around them. */
  function windowOf<T>(group: string, rows: readonly T[]): GroupWindow<T> {
    if (!windowed.value) return { start: 0, end: rows.length, paddingTop: 0, paddingBottom: 0, rows: [...rows] };
    // Before the viewport is measured, one shared budget of rows goes to the first groups;
    // a per-group budget would mount every group's first rows (600 rows for ten groups).
    if (viewportHeight.value <= 0) {
      const end = Math.max(0, Math.min(rows.length, fallbackRows - (rowsBefore.value[group] ?? 0)));
      return { start: 0, end, paddingTop: 0, paddingBottom: (rows.length - end) * rowHeight.value, rows: rows.slice(0, end) };
    }
    const window = rowWindow({
      count: rows.length,
      groupTop: groupTop[group] ?? 0,
      rowHeight: rowHeight.value,
      scrollTop: scrollTop.value,
      viewportHeight: viewportHeight.value,
      threshold: 0,
    });
    return { ...window, rows: rows.slice(window.start, window.end) };
  }

  /** Scrolls the least distance that shows row `index` of `group` (it may not be rendered yet). */
  function scrollToRow(group: string, index: number) {
    const element = options.scroller();
    if (!element) return;
    measure();
    const top = (groupTop[group] ?? 0) + index * rowHeight.value;
    const bottom = top + rowHeight.value;
    if (top < element.scrollTop) element.scrollTop = top;
    else if (bottom > element.scrollTop + element.clientHeight) element.scrollTop = bottom - element.clientHeight;
    measure();
  }

  watch(options.counts, () => void nextTick(measure), { deep: true, flush: "post" });
  watch(
    options.scroller,
    (element, previous) => {
      previous?.removeEventListener("scroll", onScroll);
      observer?.disconnect();
      observer = undefined;
      if (!element) return;
      element.addEventListener("scroll", onScroll, { passive: true });
      if (typeof ResizeObserver !== "undefined") {
        observer = new ResizeObserver(measure);
        observer.observe(element);
      }
      void nextTick(measure);
    },
    { immediate: true, flush: "post" },
  );
  onBeforeUnmount(() => {
    options.scroller()?.removeEventListener("scroll", onScroll);
    observer?.disconnect();
    if (frame) cancelAnimationFrame(frame);
  });

  return { windowOf, scrollToRow, measure, onScroll };
}
