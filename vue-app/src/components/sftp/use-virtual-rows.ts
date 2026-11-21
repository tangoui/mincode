import { computed, nextTick, onBeforeUnmount, ref, watch, type Ref } from "vue";
import type { ScrollbarInstance } from "element-plus";
import { calculateVirtualGridRange } from "@/domain/database/virtual-grid-range";

/** Directories at or below this size render every row, exactly as before virtualization. */
export const SFTP_VIRTUAL_ROW_THRESHOLD = 150;

export interface VirtualRowWindowInput {
  count: number;
  scrollTop: number;
  viewportHeight: number;
  rowHeight: number;
  threshold?: number;
}

export interface VirtualRowWindow {
  start: number;
  end: number;
  paddingTop: number;
  paddingBottom: number;
}

export function computeVirtualRowWindow({
  count,
  scrollTop,
  viewportHeight,
  rowHeight,
  threshold = SFTP_VIRTUAL_ROW_THRESHOLD,
}: VirtualRowWindowInput): VirtualRowWindow {
  if (count <= threshold || rowHeight <= 0) {
    return { start: 0, end: count, paddingTop: 0, paddingBottom: 0 };
  }
  // Before the first layout the viewport is unknown; mount a bounded window instead of every row.
  const viewport = viewportHeight > 0 ? viewportHeight : rowHeight * 40;
  const { start, end } = calculateVirtualGridRange({
    itemCount: count,
    scrollOffset: Math.max(0, scrollTop),
    viewportSize: viewport,
    itemSize: rowHeight,
    minimumOverscanItems: 10,
    overscanViewportFactor: 1,
  });
  return { start, end, paddingTop: start * rowHeight, paddingBottom: (count - end) * rowHeight };
}

interface VirtualRowsOptions<T> {
  items: () => readonly T[];
  scroller: Ref<ScrollbarInstance | undefined>;
  /** Selector for a rendered row; used to measure the real row height after layout. */
  rowSelector: string;
  estimatedRowHeight: number;
  threshold?: number;
}

/**
 * Windowed rendering for an `el-scrollbar` list of fixed-height rows. Spacer
 * heights keep the scroll range identical to the fully rendered list.
 */
export function useVirtualRows<T>(options: VirtualRowsOptions<T>) {
  const scrollTop = ref(0);
  const viewportHeight = ref(0);
  const rowHeight = ref(options.estimatedRowHeight);
  let frame = 0;
  let observer: ResizeObserver | undefined;

  const range = computed(() =>
    computeVirtualRowWindow({
      count: options.items().length,
      scrollTop: scrollTop.value,
      viewportHeight: viewportHeight.value,
      rowHeight: rowHeight.value,
      threshold: options.threshold,
    })
  );
  // Split into primitives so scrolling inside the current window does not re-render the rows.
  const start = computed(() => range.value.start);
  const end = computed(() => range.value.end);
  const virtualized = computed(() => options.items().length > (options.threshold ?? SFTP_VIRTUAL_ROW_THRESHOLD));
  const rows = computed(() => options.items().slice(start.value, end.value));
  const paddingTop = computed(() => range.value.paddingTop);
  const paddingBottom = computed(() => range.value.paddingBottom);

  function readViewport() {
    const element = options.scroller.value?.wrapRef;
    if (!element) return;
    scrollTop.value = element.scrollTop;
    viewportHeight.value = element.clientHeight;
  }

  function measureRowHeight() {
    if (!virtualized.value) return;
    const element = options.scroller.value?.wrapRef;
    if (!element) return;
    // An inline rename input makes its row taller; the smallest sampled row is the regular height.
    const heights = Array.from(element.querySelectorAll<HTMLElement>(options.rowSelector))
      .slice(0, 8)
      .map((row) => row.offsetHeight)
      .filter((height) => height > 0);
    if (heights.length) rowHeight.value = Math.min(...heights);
  }

  function onScroll() {
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      readViewport();
    });
  }

  function scrollToIndex(index: number) {
    if (!virtualized.value || index < 0) return;
    const element = options.scroller.value?.wrapRef;
    if (!element) return;
    const top = index * rowHeight.value;
    const bottom = top + rowHeight.value;
    if (top < element.scrollTop) options.scroller.value?.setScrollTop(top);
    else if (bottom > element.scrollTop + element.clientHeight) {
      options.scroller.value?.setScrollTop(bottom - element.clientHeight);
    }
    readViewport();
  }

  watch(
    () => [options.items(), virtualized.value] as const,
    async () => {
      await nextTick();
      readViewport();
      measureRowHeight();
    },
    { flush: "post" }
  );

  // The scrollbar may mount after the owner (e.g. behind a loading state), so follow the element itself.
  watch(
    () => options.scroller.value?.wrapRef,
    (element) => {
      observer?.disconnect();
      observer = undefined;
      readViewport();
      measureRowHeight();
      if (element && typeof ResizeObserver !== "undefined") {
        observer = new ResizeObserver(() => readViewport());
        observer.observe(element);
      }
    },
    { flush: "post", immediate: true }
  );

  onBeforeUnmount(() => {
    if (frame) cancelAnimationFrame(frame);
    observer?.disconnect();
  });

  return { rows, paddingTop, paddingBottom, virtualized, onScroll, scrollToIndex };
}
