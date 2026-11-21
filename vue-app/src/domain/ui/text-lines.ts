// Row index over a large read-only text (HTTP response bodies of several MB: hundreds of
// thousands of lines, or one multi-MB minified line). The text is never split into strings:
// typed arrays hold where each visual row starts and ends, so a viewer reads any row with one
// slice and renders only the rows in view. Lines longer than `columns` wrap into continuation
// rows, so no rendered row is wider than the view, however long the source line.

export interface RowIndex {
  /** Source offset where each row starts. */
  starts: Uint32Array;
  /** Source offset where each row ends (before the line break for a line's last row). */
  ends: Uint32Array;
  /** 0-based source line of each row. */
  lines: Uint32Array;
  /** 1 when the row continues the previous row's line (wrapped), else 0. */
  continues: Uint8Array;
}

/**
 * Indexes the visual rows of `text` wrapped at `columns` characters. Lines end at "\n" (a
 * preceding "\r" is not shown); a lone "\r" stays text. A final newline ends the last line
 * rather than starting an empty one, and empty text is one empty row. A wrapped row never
 * splits a surrogate pair. One pass over the text; pure, so it can run in a worker.
 */
export function indexRows(text: string, columns: number): RowIndex {
  const width = Math.max(1, Math.floor(columns));
  const length = text.length;
  let capacity = 1024;
  let starts = new Uint32Array(capacity);
  let ends = new Uint32Array(capacity);
  let lines = new Uint32Array(capacity);
  let continues = new Uint8Array(capacity);
  const grow = () => {
    capacity *= 2;
    const nextStarts = new Uint32Array(capacity); nextStarts.set(starts); starts = nextStarts;
    const nextEnds = new Uint32Array(capacity); nextEnds.set(ends); ends = nextEnds;
    const nextLines = new Uint32Array(capacity); nextLines.set(lines); lines = nextLines;
    const nextContinues = new Uint8Array(capacity); nextContinues.set(continues); continues = nextContinues;
  };
  let row = 0;
  let line = 0;
  let start = 0;
  for (;;) {
    let newline = text.indexOf("\n", start);
    const last = newline === -1;
    if (last) {
      if (start >= length && length > 0) break; // the final newline ended the last line
      newline = length;
    }
    const end = !last && newline > start && text.charCodeAt(newline - 1) === 13 ? newline - 1 : newline;
    let at = start;
    do {
      let next = end;
      if (end - at > width) {
        next = at + width;
        const code = text.charCodeAt(next - 1);
        if (code >= 0xd800 && code <= 0xdbff && next - 1 > at) next--;
      }
      if (row === capacity) grow();
      starts[row] = at;
      ends[row] = next;
      lines[row] = line;
      continues[row] = at === start ? 0 : 1;
      row++;
      at = next;
    } while (at < end);
    line++;
    if (last) break;
    start = newline + 1;
  }
  return { starts: starts.slice(0, row), ends: ends.slice(0, row), lines: lines.slice(0, row), continues: continues.slice(0, row) };
}

export function rowCount(index: RowIndex): number {
  return index.starts.length;
}

export function rowText(text: string, index: RowIndex, row: number): string {
  return text.slice(index.starts[row], index.ends[row]);
}

export interface ScrollWindowInput {
  count: number;
  rowHeight: number;
  scrollTop: number;
  viewportHeight: number;
  /** Rows kept above and below the viewport. */
  overscan: number;
  /** Tallest scrollable height; taller content is scaled into it. */
  maxHeight?: number;
}

export interface ScrollWindow {
  /** Rows [start, end) to render. */
  start: number;
  end: number;
  /** Top of the rendered block within the scrolled content, in px. */
  offset: number;
  /** Height of the scrolled content, in px. */
  height: number;
}

/** Rows rendered before the viewport is measured. */
const UNMEASURED_ROWS = 60;
/**
 * WebKit caps an element's height near 33.5M px; stay well under it. A 10 MB body of very
 * short lines (millions of rows) scrolls through a scaled range instead.
 */
export const MAX_SCROLL_HEIGHT = 15_000_000;

export function scrollWindow({ count, rowHeight, scrollTop, viewportHeight, overscan, maxHeight = MAX_SCROLL_HEIGHT }: ScrollWindowInput): ScrollWindow {
  const full = count * rowHeight;
  const height = Math.min(full, maxHeight);
  if (count <= 0 || rowHeight <= 0) return { start: 0, end: 0, offset: 0, height: 0 };
  if (viewportHeight <= 0) return { start: 0, end: Math.min(count, UNMEASURED_ROWS), offset: 0, height };
  const range = Math.max(0, height - viewportHeight);
  const top = Math.min(Math.max(0, scrollTop), range);
  // Position in the unscaled content: equal to `top` unless the content is scaled.
  const virtualTop = range > 0 && full > height ? (top * (full - viewportHeight)) / range : top;
  const first = Math.floor(virtualTop / rowHeight);
  const start = Math.max(0, first - overscan);
  const end = Math.min(count, Math.ceil((virtualTop + viewportHeight) / rowHeight) + overscan);
  // Place row `first` where the unscaled layout would show it relative to the viewport top.
  const offset = top - (virtualTop - start * rowHeight);
  return { start, end: Math.max(start, end), offset, height };
}

/** Most matches a search collects; a viewer reports "N+" beyond it. */
export const MAX_MATCHES = 10_000;

/**
 * Source offsets of the non-overlapping, case-insensitive matches of `query` in `text`,
 * at most `limit`. Lower-cases once and scans with indexOf, so a multi-MB body searches in
 * tens of milliseconds. Characters whose lower-case form has a different length (rare outside
 * a few scripts) can shift offsets; the viewer then highlights the nearest text.
 */
export function findMatches(text: string, query: string, limit = MAX_MATCHES): number[] {
  if (!query) return [];
  const haystack = text.toLowerCase();
  const needle = query.toLowerCase();
  const matches: number[] = [];
  for (let at = haystack.indexOf(needle); at !== -1 && matches.length < limit; at = haystack.indexOf(needle, at + needle.length)) {
    matches.push(at);
  }
  return matches;
}

/** Row that shows source offset `offset` (a line break belongs to the row before it). */
export function rowOfOffset(index: RowIndex, offset: number): number {
  const { starts } = index;
  let low = 0;
  let high = starts.length - 1;
  while (low < high) {
    const middle = (low + high + 1) >> 1;
    if (starts[middle]! <= offset) low = middle;
    else high = middle - 1;
  }
  return low;
}
