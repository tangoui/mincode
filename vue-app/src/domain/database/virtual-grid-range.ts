export interface VirtualGridRangeOptions {
  itemCount: number;
  scrollOffset: number;
  viewportSize: number;
  itemSize: number;
  frozenSize?: number;
  minimumOverscanItems: number;
  overscanViewportFactor?: number;
}

export interface VirtualGridRange {
  start: number;
  end: number;
}

/**
 * Calculates a safe virtualized slice around the viewport.
 *
 * Keeping at least a couple of viewports mounted prevents the browser from
 * briefly exposing the empty surface when a wheel/trackpad scroll advances
 * farther than Vue can patch the previous virtual slice in one frame.
 */
export function calculateVirtualGridRange({
  itemCount,
  scrollOffset,
  viewportSize,
  itemSize,
  frozenSize = 0,
  minimumOverscanItems,
  overscanViewportFactor = 2,
}: VirtualGridRangeOptions): VirtualGridRange {
  if (itemCount <= 0 || itemSize <= 0) return { start: 0, end: 0 };

  const normalizedViewportSize = Math.max(0, viewportSize);
  const visibleItems = Math.max(1, Math.ceil(normalizedViewportSize / itemSize));
  const overscan = Math.max(
    minimumOverscanItems,
    Math.ceil(visibleItems * overscanViewportFactor),
  );
  const contentOffset = Math.max(0, scrollOffset - frozenSize);
  const firstVisibleItem = Math.floor(contentOffset / itemSize);
  const lastVisibleItem = Math.ceil((contentOffset + normalizedViewportSize) / itemSize);
  const start = Math.min(
    itemCount - 1,
    Math.max(0, firstVisibleItem - overscan),
  );
  const end = Math.min(itemCount, Math.max(start + 1, lastVisibleItem + overscan));

  return { start, end };
}
