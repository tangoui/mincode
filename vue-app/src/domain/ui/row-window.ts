// Windowed rendering for one group of a grouped, fixed-row-height list (Git changes, SSH
// connections, …). Only the rows near the viewport are mounted; spacers keep the group's
// height, so the scroll range is the same as with every row rendered.

/** Groups at or below this many rows render every row, exactly as before. */
export const ROW_WINDOW_THRESHOLD = 200;

/** Rows kept above and below the viewport. */
const OVERSCAN_ROWS = 8;
/** Rows mounted while the viewport is not measured yet. */
export const ROW_WINDOW_FALLBACK_ROWS = 60;

export interface RowWindowInput {
  count: number;
  /** Offset of the group's first row from the top of the scrolled content. */
  groupTop: number;
  rowHeight: number;
  scrollTop: number;
  viewportHeight: number;
  threshold?: number;
}

export interface RowWindow {
  start: number;
  end: number;
  paddingTop: number;
  paddingBottom: number;
}

export function rowWindow({
  count,
  groupTop,
  rowHeight,
  scrollTop,
  viewportHeight,
  threshold = ROW_WINDOW_THRESHOLD,
}: RowWindowInput): RowWindow {
  if (count <= threshold || rowHeight <= 0) return { start: 0, end: count, paddingTop: 0, paddingBottom: 0 };
  const window = (start: number, end: number): RowWindow => ({
    start,
    end,
    paddingTop: start * rowHeight,
    paddingBottom: (count - end) * rowHeight,
  });
  if (viewportHeight <= 0) return window(0, Math.min(count, ROW_WINDOW_FALLBACK_ROWS));
  // Viewport position relative to the group's first row.
  const top = scrollTop - groupTop;
  const bottom = top + viewportHeight;
  // A group out of view mounts nothing; its spacers keep the scroll range.
  if (bottom <= 0) return window(0, 0);
  if (top >= count * rowHeight) return window(count, count);
  const start = Math.max(0, Math.floor(top / rowHeight) - OVERSCAN_ROWS);
  const end = Math.min(count, Math.ceil(bottom / rowHeight) + OVERSCAN_ROWS);
  return window(start, end);
}
