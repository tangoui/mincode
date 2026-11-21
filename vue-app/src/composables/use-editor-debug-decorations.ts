/**
 * Debug decorations of a project text editor: breakpoint glyphs (with
 * condition / disabled / unverified states), the paused-line highlight and
 * arrow, and entry-point ▶ glyphs. Also turns glyph-margin clicks into
 * breakpoint actions and keeps breakpoints and ▶ on their code while it is
 * edited.
 *
 * Line tracking: every model gets invisible model-level marks at its stored
 * breakpoint lines and at the last entry-point lookup's lines (they survive
 * switching documents, unlike the editor's rendered decorations). Glyphs
 * render where the marks are now, clicks map the shown line back to the
 * stored one, and `captureMoves` reports `{ from, to }` against the stored
 * lines when the file is saved, so the store only changes with the file.
 *
 * One rule decides what an edit does to a mark whose line start lies inside
 * the replaced range of a change:
 * - a pure deletion (nothing inserted) removed the line: the mark collapses
 *   and the breakpoint is deleted (reported as `to: 0`);
 * - a replacement (typing over a selection, paste, format, undo / redo, save
 *   cleanup, external tools) keeps it: the mark is put on the line its text
 *   moved to within the replaced region (`mapLinesAcrossEdit`), using the
 *   line texts remembered per mark (no copy of the file is kept).
 * Replacing the whole content (`setValue`: reload, revert, discard) drops
 * the marks; they are rebuilt from the stored lines.
 *
 * Inline values (the selected frame's variables at the end of the lines
 * above the paused line) are transient `after` decorations of the rendered
 * collection: they are recomputed on each render from the shown text and are
 * never line marks.
 *
 * Monaco-free on purpose (the editor, model and enum values are passed in),
 * so the decoration logic can be tested without the Monaco runtime.
 */
import { normalizeLogMessage } from "@/domain/debug/logpoints";
import type { SourceBreakpoint } from "@/domain/debug/types";
import type { DebugBreakpointHoverDetails } from "@/domain/debug/session-model";

export type { DebugBreakpointHoverDetails };
import { computeInlineValues, INLINE_VALUES_LOOK_BACK, type InlineValue } from "@/domain/debug/inline-values";

export interface DebugEntryPointMarker {
  line: number;
  label: string;
}

/** The last result shown on an entry glyph. */
export type DebugEntryPointState = "running" | "passed" | "failed" | "errored" | "skipped" | "terminated";

export interface DebugDecorationInput {
  breakpoints: readonly SourceBreakpoint[];
  /** Line → verified, from running sessions; absent lines have no result. */
  verifiedLines?: Record<number, boolean>;
  /** Stored line → the adapter's details shown in the breakpoint's hover. */
  breakpointDetails?: Record<number, DebugBreakpointHoverDetails>;
  pausedLine?: number | null;
  /** Entry points of the last lookup; a new array means a new lookup. */
  entryPoints?: readonly DebugEntryPointMarker[];
  /**
   * The last result of the entry on a stored entry line (e.g. a test's
   * passed / failed), shown on its glyph; changing it is not a new lookup.
   */
  entryPointStates?: Readonly<Record<number, DebugEntryPointState>>;
  /** Variables of the paused frame (shown inline above `pausedLine`); null when not paused. */
  inlineVariables?: readonly { name: string; value: string }[] | null;
}

export interface DebugRange {
  startLineNumber: number;
  startColumn: number;
  endLineNumber: number;
  endColumn: number;
}

/** The subset of Monaco's IModelDeltaDecoration used here. */
export interface DebugDecoration {
  range: DebugRange;
  options: {
    description: string;
    stickiness: number;
    glyphMarginClassName?: string;
    glyphMarginHoverMessage?: { value: string };
    className?: string;
    isWholeLine?: boolean;
    after?: { content: string; inlineClassName?: string };
    showIfCollapsed?: boolean;
  };
}

export interface DebugDecorationLabels {
  breakpoint: (bp: SourceBreakpoint, verified: boolean | undefined, details?: DebugBreakpointHoverDetails) => string;
  paused: () => string;
  entryPoint: (label: string, state?: DebugEntryPointState) => string;
}

export interface DebugAnchor {
  x: number;
  y: number;
}

export interface DebugEditorMouseEvent {
  target: { type: number; position?: { lineNumber: number; column: number } | null; range?: { startLineNumber: number } | null };
  event: { leftButton: boolean; rightButton: boolean; posx: number; posy: number };
}

export interface DebugDisposable {
  dispose(): void;
}

/** The subset of Monaco's IModelContentChangedEvent used here (`range` is in pre-edit coordinates). */
export interface DebugContentChangedEvent {
  isFlush: boolean;
  changes: ReadonlyArray<{ range: DebugRange; text: string }>;
}

export interface DebugModelLike {
  isDisposed(): boolean;
  getLineCount(): number;
  getLineMaxColumn(line: number): number;
  getLineContent(line: number): string;
  deltaDecorations(oldIds: string[], next: DebugDecoration[]): string[];
  getDecorationRange(id: string): DebugRange | null;
  onDidChangeContent(listener: (event: DebugContentChangedEvent) => void): DebugDisposable;
}

export interface DebugEditorLike {
  getModel(): DebugModelLike | null;
  createDecorationsCollection(): { set(decorations: DebugDecoration[]): unknown; clear(): void };
  onMouseDown(listener: (event: DebugEditorMouseEvent) => void): DebugDisposable;
  onContextMenu(listener: (event: DebugEditorMouseEvent) => void): DebugDisposable;
}

export interface BreakpointMove {
  from: number;
  to: number;
}

/** What a glyph-margin line shows: its stored breakpoint line and entry-point lookup line, if any. */
export interface DebugGlyphLineInfo {
  breakpointLine: number | null;
  entryLine: number | null;
}

/** Keep a breakpoint lane without Monaco's five-digit minimum line-number gutter. */
export function debugEditorOptions(kind: string | undefined): {
  glyphMargin?: boolean;
  lineNumbersMinChars?: number;
  lineDecorationsWidth?: number;
} {
  return kind === "project"
    ? { glyphMargin: true, lineNumbersMinChars: 3, lineDecorationsWidth: 4 }
    : {};
}

/** Longest strictly increasing subsequence of pairs by `post` (pairs sorted by `pre`). */
function increasingByPost(pairs: Array<[number, number]>): Array<[number, number]> {
  const tails: number[] = [];
  const previous: number[] = new Array(pairs.length).fill(-1);
  pairs.forEach(([, post], index) => {
    let low = 0;
    let high = tails.length;
    while (low < high) {
      const mid = (low + high) >> 1;
      if (pairs[tails[mid]!]![1] < post) low = mid + 1;
      else high = mid;
    }
    if (low > 0) previous[index] = tails[low - 1]!;
    tails[low] = index;
  });
  const result: Array<[number, number]> = [];
  for (let index = tails.length ? tails[tails.length - 1]! : -1; index >= 0; index = previous[index]!) result.unshift(pairs[index]!);
  return result;
}

/**
 * Maps 1-based line numbers of `before` to `after` for an edit that kept
 * most lines (formatting, whitespace cleanup, undo of those). Lines compare
 * ignoring whitespace; `null` marks a line whose text is unknown (it never
 * matches). Matching: the common head and tail, then lines unique on both
 * sides in order. Any other line goes to the nearest line with its text
 * between its matched neighbours, else keeps its distance to the matched
 * line above it; the result stays between the neighbours and in the file.
 */
export function mapLinesAcrossEdit(before: readonly (string | null)[], after: readonly string[]): (line: number) => number {
  const pre = before.map((text, index) => (text === null ? `\u0000${index}` : text.replace(/\s+/g, "")));
  const post = after.map((text) => text.replace(/\s+/g, ""));
  let head = 0;
  while (head < pre.length && head < post.length && pre[head] === post[head]) head += 1;
  let tail = 0;
  while (tail < pre.length - head && tail < post.length - head && pre[pre.length - 1 - tail] === post[post.length - 1 - tail]) tail += 1;
  const count = (keys: string[], from: number, to: number) => {
    const counts = new Map<string, number>();
    for (let index = from; index < to; index += 1) counts.set(keys[index]!, (counts.get(keys[index]!) ?? 0) + 1);
    return counts;
  };
  const preCounts = count(pre, head, pre.length - tail);
  const postCounts = count(post, head, post.length - tail);
  const postIndex = new Map<string, number>();
  for (let index = head; index < post.length - tail; index += 1) postIndex.set(post[index]!, index);
  const unique: Array<[number, number]> = [];
  for (let index = head; index < pre.length - tail; index += 1) {
    const key = pre[index]!;
    if (key && preCounts.get(key) === 1 && postCounts.get(key) === 1) unique.push([index, postIndex.get(key)!]);
  }
  // 1-based anchors, with sentinels before the first and after the last line.
  const anchors: Array<[number, number]> = [[0, 0]];
  for (let index = 0; index < head; index += 1) anchors.push([index + 1, index + 1]);
  for (const [from, to] of increasingByPost(unique)) anchors.push([from + 1, to + 1]);
  for (let index = tail; index > 0; index -= 1) anchors.push([pre.length - index + 1, post.length - index + 1]);
  anchors.push([pre.length + 1, post.length + 1]);
  const lastLine = Math.max(post.length, 1);
  return (line: number) => {
    let below = 0;
    while (below + 1 < anchors.length && anchors[below + 1]![0] <= line) below += 1;
    const [fromPre, fromPost] = anchors[below]!;
    if (fromPre === line && line >= 1 && line <= pre.length) return Math.min(Math.max(fromPost, 1), lastLine);
    const nextPost = anchors[below + 1]?.[1] ?? post.length + 1;
    const estimate = Math.min(Math.max(fromPost + (line - fromPre), fromPost), nextPost - 1);
    const key = pre[line - 1];
    let best: number | null = null;
    if (key !== undefined) {
      for (let candidate = fromPost + 1; candidate < nextPost; candidate += 1) {
        if (post[candidate - 1] === key && (best === null || Math.abs(candidate - estimate) < Math.abs(best - estimate))) best = candidate;
      }
    }
    return Math.min(Math.max(best ?? estimate, 1), lastLine);
  };
}

function lineRange(line: number): DebugRange {
  return { startLineNumber: line, startColumn: 1, endLineNumber: line, endColumn: 1 };
}

function breakpointClass(bp: SourceBreakpoint, verified: boolean | undefined): string {
  if (normalizeLogMessage(bp.logMessage) !== undefined) {
    const state = !bp.enabled ? " debug-bp--disabled" : verified === false ? " debug-bp--unverified" : "";
    return `debug-bp debug-bp--logpoint${state}`;
  }
  if (!bp.enabled) return "debug-bp debug-bp--disabled";
  if (verified === false) return "debug-bp debug-bp--unverified";
  if (bp.condition || bp.hitCondition) return "debug-bp debug-bp--conditional";
  return "debug-bp";
}

/**
 * The editor decorations for one document. `displayLine` / `entryDisplayLine`
 * map a stored breakpoint line / lookup line to where its code is now (edits
 * not saved yet; below 1 means the line was deleted and nothing is shown).
 * Verified results and adapter details are keyed by stored lines (a line the
 * adapter moved a breakpoint to is only mentioned in its hover). When two
 * breakpoints show on one line, the one stored highest in the file is shown. An entry point on a line
 * that shows a breakpoint is left out (its run / debug items stay in the
 * glyph menu).
 */
export function buildDebugDecorations(
  input: DebugDecorationInput,
  stickiness: number,
  options: {
    displayLine?: (line: number) => number;
    entryDisplayLine?: (line: number) => number;
    labels?: DebugDecorationLabels;
    /** Inline values with the end column of their line. */
    inlineValues?: ReadonlyArray<InlineValue & { column: number }>;
  } = {},
): DebugDecoration[] {
  const displayLine = options.displayLine ?? ((line: number) => line);
  const entryDisplayLine = options.entryDisplayLine ?? ((line: number) => line);
  const labels = options.labels;
  const decorations: DebugDecoration[] = [];
  const breakpointLines = new Set<number>();
  for (const bp of [...input.breakpoints].sort((a, b) => a.line - b.line)) {
    const line = displayLine(bp.line);
    if (line < 1 || breakpointLines.has(line)) continue;
    const verified = input.verifiedLines?.[bp.line];
    breakpointLines.add(line);
    decorations.push({
      range: lineRange(line),
      options: {
        description: "debug-breakpoint",
        stickiness,
        glyphMarginClassName: breakpointClass(bp, verified),
        ...(labels
          ? { glyphMarginHoverMessage: { value: labels.breakpoint(bp, verified, input.breakpointDetails?.[bp.line]) } }
          : {}),
      },
    });
  }
  const entryLines = new Set<number>();
  for (const entry of input.entryPoints ?? []) {
    const line = entryDisplayLine(entry.line);
    if (line < 1 || breakpointLines.has(line) || entryLines.has(line)) continue;
    entryLines.add(line);
    const state = input.entryPointStates?.[entry.line];
    decorations.push({
      range: lineRange(line),
      options: {
        description: "debug-entry-point",
        stickiness,
        glyphMarginClassName: state ? `debug-entry debug-entry--${state}` : "debug-entry",
        ...(labels ? { glyphMarginHoverMessage: { value: labels.entryPoint(entry.label, state) } } : {}),
      },
    });
  }
  const paused = input.pausedLine;
  if (typeof paused === "number" && paused >= 1) {
    decorations.push({
      range: lineRange(paused),
      options: { description: "debug-paused-line", stickiness, className: "debug-paused-line", isWholeLine: true },
    });
    decorations.push({
      range: lineRange(paused),
      options: {
        description: "debug-paused-arrow",
        stickiness,
        glyphMarginClassName: "debug-paused-arrow",
        ...(labels ? { glyphMarginHoverMessage: { value: labels.paused() } } : {}),
      },
    });
  }
  for (const value of options.inlineValues ?? []) {
    decorations.push({
      range: { startLineNumber: value.line, startColumn: value.column, endLineNumber: value.line, endColumn: value.column },
      options: {
        description: "debug-inline-value",
        stickiness,
        after: { content: value.text, inlineClassName: "debug-inline-value" },
        showIfCollapsed: true,
      },
    });
  }
  return decorations;
}

/** One tracked line. */
interface Mark {
  id: string;
  /** The stored (or looked-up) line the mark stands for. */
  origin: number;
  /** Where the mark was after the last edit, and that line's text. */
  line: number;
  text: string;
  /** Started empty (last line without text), so a collapse cannot be told apart. */
  startedEmpty: boolean;
  /** Its line was deleted (sticky until the tracking is rebuilt). */
  deleted: boolean;
}

/** Marks of one kind in one model. */
interface Tracker {
  marks: Mark[];
  /** Entry trackers: the lookup they track (a new lookup resets them). */
  source?: unknown;
}

interface ModelTracking {
  breakpoints: Tracker;
  entries: Tracker;
  listener: DebugDisposable;
}

/** Line breaks in `text` (\r\n, \r and \n each count once). */
export function countLineBreaks(text: string): number {
  let count = 0;
  for (let index = 0; index < text.length; index += 1) {
    const code = text.charCodeAt(index);
    if (code === 10) count += 1;
    else if (code === 13) {
      count += 1;
      if (text.charCodeAt(index + 1) === 10) index += 1;
    }
  }
  return count;
}

/**
 * For the changes of one content event (non-overlapping, pre-edit ranges, in
 * any order), returns line → the line shift caused by the changes that end
 * strictly above it. Sorting plus prefix sums keep a many-change event
 * (replace-all, multi-cursor) at O(n log n) instead of O(n²).
 */
export function lineShifts(changes: ReadonlyArray<{ range: DebugRange; text: string }>): (line: number) => number {
  const byEnd = changes
    .map((change) => ({
      end: change.range.endLineNumber,
      delta: countLineBreaks(change.text) - (change.range.endLineNumber - change.range.startLineNumber),
    }))
    .sort((a, b) => a.end - b.end);
  const prefix = new Array<number>(byEnd.length + 1);
  prefix[0] = 0;
  for (let index = 0; index < byEnd.length; index += 1) prefix[index + 1] = prefix[index]! + byEnd[index]!.delta;
  return (line) => {
    // Count of changes whose end line is < line.
    let low = 0;
    let high = byEnd.length;
    while (low < high) {
      const mid = (low + high) >>> 1;
      if (byEnd[mid]!.end < line) low = mid + 1;
      else high = mid;
    }
    return prefix[low]!;
  };
}

function isEmptyRange(range: DebugRange): boolean {
  return range.startLineNumber === range.endLineNumber && range.startColumn === range.endColumn;
}

function before(lineA: number, columnA: number, lineB: number, columnB: number): boolean {
  return lineA < lineB || (lineA === lineB && columnA < columnB);
}

export interface EditorDebugDecorationsOptions {
  editor: DebugEditorLike;
  /** Debug decorations and glyph clicks are active (project editors only). */
  enabled: () => boolean;
  /** Path of the document the editor shows. */
  path: () => string;
  /** The model of an open path (for saving a document that is not shown). */
  modelFor: (path: string) => DebugModelLike | undefined;
  state: () => DebugDecorationInput;
  labels?: () => DebugDecorationLabels;
  /** Monaco `MouseTargetType.GUTTER_GLYPH_MARGIN`. */
  glyphMarginTarget: number;
  /** Monaco `TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges`. */
  stickiness: number;
  /** Toggle the breakpoint stored on `line` (or add one there). */
  onToggle: (line: number) => void;
  /** `line` is the entry point's lookup line. */
  onEntryClick: (line: number, anchor: DebugAnchor) => void;
  /** `line` is the shown line; `lineInfo(line)` resolves it. */
  onGlyphContextMenu: (line: number, anchor: DebugAnchor) => void;
  onMoved: (moves: BreakpointMove[], path: string) => void;
}

export function useEditorDebugDecorations(options: EditorDebugDecorationsOptions) {
  const tracking = new Map<DebugModelLike, ModelTracking>();
  let rendered: ReturnType<DebugEditorLike["createDecorationsCollection"]> | null = null;

  function forget(model: DebugModelLike) {
    tracking.get(model)?.listener.dispose();
    tracking.delete(model);
  }

  function trackingFor(model: DebugModelLike): ModelTracking {
    for (const known of [...tracking.keys()]) if (known.isDisposed()) forget(known);
    let entry = tracking.get(model);
    if (!entry) {
      const created: ModelTracking = {
        breakpoints: { marks: [] },
        entries: { marks: [] },
        listener: model.onDidChangeContent((event) => handleContentChange(model, event)),
      };
      tracking.set(model, created);
      entry = created;
    }
    return entry;
  }

  /**
   * A whole-line range including the line break, so a deletion of the line
   * collapses it (typing on the line or above it does not).
   */
  function markDecoration(line: number, model: DebugModelLike): DebugDecoration {
    const lineCount = Math.max(model.getLineCount(), 1);
    const start = Math.min(Math.max(line, 1), lineCount);
    const range =
      start < lineCount
        ? { startLineNumber: start, startColumn: 1, endLineNumber: start + 1, endColumn: 1 }
        : { startLineNumber: start, startColumn: 1, endLineNumber: start, endColumn: model.getLineMaxColumn(start) };
    return { range, options: { description: "debug-line-track", stickiness: options.stickiness } };
  }

  /** Puts marks on lines (new ones get `origins`, moved ones keep theirs). */
  function placeMarks(model: DebugModelLike, removed: string[], places: Array<{ origin: number; line: number }>): Mark[] {
    const decorations = places.map((place) => markDecoration(place.line, model));
    const ids = model.deltaDecorations(removed, decorations);
    return places.map((place, index) => {
      const line = decorations[index]!.range.startLineNumber;
      return {
        id: ids[index]!,
        origin: place.origin,
        line,
        text: model.getLineContent(line),
        startedEmpty: isEmptyRange(decorations[index]!.range),
        deleted: false,
      };
    });
  }

  /** Brings a tracker in line with the wanted lines, keeping the (moved) marks still wanted. */
  function reconcile(model: DebugModelLike, tracker: Tracker, lines: readonly number[]) {
    const wanted = new Set(lines);
    const kept: Mark[] = [];
    const removed: string[] = [];
    for (const mark of tracker.marks) {
      if (wanted.has(mark.origin) && !kept.some((item) => item.origin === mark.origin) && model.getDecorationRange(mark.id)) kept.push(mark);
      else removed.push(mark.id);
    }
    const added = [...wanted].filter((line) => !kept.some((item) => item.origin === line));
    if (removed.length || added.length) kept.push(...placeMarks(model, removed, added.map((line) => ({ origin: line, line }))));
    tracker.marks = kept;
  }

  /**
   * Applies one content change event to the model's marks: replaced regions
   * are remapped by text, pure deletions collapse marks (deleted), and the
   * remembered line / text of moved or touched marks is updated.
   */
  function handleContentChange(model: DebugModelLike, event: DebugContentChangedEvent) {
    const track = tracking.get(model);
    if (!track || model.isDisposed()) return;
    if (event.isFlush) {
      // setValue dropped every decoration; rebuild from the stored lines.
      forget(model);
      if (shownModel() === model) refresh();
      return;
    }
    const marks = [...track.breakpoints.marks, ...track.entries.marks].filter((mark) => !mark.deleted);
    // Nothing to remap: skip the per-change work (replace-all / multi-cursor on big files).
    if (!marks.length) return;
    const shiftOf = lineShifts(event.changes);
    const remapped = new Map<Mark, number>();
    let newlyDeleted = false;
    const touched: Array<[number, number]> = [];
    for (const change of event.changes) {
      const { range, text } = change;
      // Changes of one event do not overlap; earlier ones above shift this region.
      const shift = shiftOf(range.startLineNumber);
      const newLineCount = countLineBreaks(text) + 1;
      const postStart = range.startLineNumber + shift;
      touched.push([postStart, postStart + newLineCount - 1]);
      if (text === "") continue;
      const inside = marks.filter(
        (mark) =>
          !before(mark.line, 1, range.startLineNumber, range.startColumn) &&
          before(mark.line, 1, range.endLineNumber, range.endColumn)
      );
      if (!inside.length) continue;
      const regionBefore: (string | null)[] = new Array(range.endLineNumber - range.startLineNumber + 1).fill(null);
      for (const mark of marks) {
        const index = mark.line - range.startLineNumber;
        if (index >= 0 && index < regionBefore.length) regionBefore[index] = mark.text;
      }
      const regionAfter: string[] = [];
      for (let line = postStart; line < postStart + newLineCount && line <= model.getLineCount(); line += 1) {
        regionAfter.push(model.getLineContent(line));
      }
      const map = mapLinesAcrossEdit(regionBefore, regionAfter);
      for (const mark of inside) remapped.set(mark, postStart + map(mark.line - range.startLineNumber + 1) - 1);
    }
    for (const tracker of [track.breakpoints, track.entries]) {
      const moving = tracker.marks.filter((mark) => remapped.has(mark));
      if (moving.length) {
        const placed = placeMarks(
          model,
          moving.map((mark) => mark.id),
          moving.map((mark) => ({ origin: mark.origin, line: remapped.get(mark)! }))
        );
        tracker.marks = tracker.marks.map((mark) => (remapped.has(mark) ? placed[moving.indexOf(mark)]! : mark));
      }
      for (const mark of tracker.marks) {
        if (mark.deleted || remapped.has(mark)) continue;
        const range = model.getDecorationRange(mark.id);
        if (!range) continue;
        if (!mark.startedEmpty && isEmptyRange(range)) {
          mark.deleted = true;
          newlyDeleted = true;
          continue;
        }
        const line = range.startLineNumber;
        if (line !== mark.line || touched.some(([from, to]) => line >= from && line <= to)) {
          mark.line = line;
          mark.text = model.getLineContent(line);
        }
      }
    }
    if ((remapped.size || newlyDeleted) && shownModel() === model) refresh();
  }

  /** Stored line → current line (0 when its line was deleted). */
  function currentLines(model: DebugModelLike, tracker: Tracker): Map<number, number> {
    const lines = new Map<number, number>();
    for (const mark of tracker.marks) {
      const range = model.getDecorationRange(mark.id);
      if (!range) continue;
      lines.set(mark.origin, mark.deleted ? 0 : range.startLineNumber);
    }
    return lines;
  }

  function movesOf(lines: Map<number, number>): BreakpointMove[] {
    return [...lines].filter(([from, to]) => from !== to).map(([from, to]) => ({ from, to }));
  }

  function shownModel(): DebugModelLike | null {
    const model = options.editor.getModel();
    return model && !model.isDisposed() ? model : null;
  }

  /** Inline values of the shown text (only the look-back window is read). */
  function inlineValuesOf(model: DebugModelLike, state: DebugDecorationInput): Array<InlineValue & { column: number }> {
    const paused = state.pausedLine;
    const variables = state.inlineVariables;
    if (typeof paused !== "number" || paused < 1 || !variables?.length) return [];
    const last = Math.min(paused, model.getLineCount());
    const first = Math.max(1, paused - INLINE_VALUES_LOOK_BACK);
    const lines = Array.from({ length: last }, (_, index) => (index + 1 >= first ? model.getLineContent(index + 1) : ""));
    return computeInlineValues(lines, paused, [...variables]).map((value) => ({ ...value, column: model.getLineMaxColumn(value.line) }));
  }

  /** Re-renders the shown document's decorations (state changed, the document switched or was replaced). */
  function refresh() {
    const model = shownModel();
    if (!options.enabled() || !model) {
      rendered?.clear();
      return;
    }
    const state = options.state();
    const track = trackingFor(model);
    reconcile(model, track.breakpoints, state.breakpoints.map((bp) => bp.line));
    const entries = state.entryPoints ?? [];
    if (track.entries.source !== entries) {
      // A new lookup: its lines describe the content as it is now.
      model.deltaDecorations(track.entries.marks.map((mark) => mark.id), []);
      track.entries = { marks: [], source: entries };
    }
    reconcile(model, track.entries, entries.map((entry) => entry.line));
    const display = currentLines(model, track.breakpoints);
    const entryDisplay = currentLines(model, track.entries);
    const decorations = buildDebugDecorations(state, options.stickiness, {
      displayLine: (line) => display.get(line) ?? line,
      entryDisplayLine: (line) => entryDisplay.get(line) ?? line,
      labels: options.labels?.(),
      inlineValues: inlineValuesOf(model, state),
    });
    rendered ??= options.editor.createDecorationsCollection();
    rendered.set(decorations);
  }

  /**
   * Snapshot of the path's breakpoint moves, taken when its content is
   * written; calling the result (after the write succeeded) reports them
   * through `onMoved` and makes the moved lines the stored ones.
   */
  function captureMoves(path: string): (() => void) | undefined {
    const model = options.modelFor(path);
    if (!model || model.isDisposed()) return undefined;
    const track = tracking.get(model)?.breakpoints;
    if (!track) return undefined;
    const snapshot = currentLines(model, track);
    const moves = movesOf(snapshot);
    if (!moves.length) return undefined;
    return () => {
      if (tracking.get(model)?.breakpoints !== track || model.isDisposed()) return;
      for (const mark of track.marks) mark.origin = snapshot.get(mark.origin) ?? mark.origin;
      options.onMoved(moves, path);
    };
  }

  /** The stored breakpoint line and entry lookup line shown on `line`. */
  function lineInfo(line: number): DebugGlyphLineInfo {
    const model = shownModel();
    const track = model ? tracking.get(model) : undefined;
    const state = options.state();
    if (!model || !track) {
      return {
        breakpointLine: state.breakpoints.some((bp) => bp.line === line) ? line : null,
        entryLine: state.entryPoints?.some((entry) => entry.line === line) ? line : null,
      };
    }
    const shownOn = (lines: Map<number, number>) => {
      const origins = [...lines].filter(([, shown]) => shown === line).map(([origin]) => origin);
      return origins.length ? Math.min(...origins) : null;
    };
    return { breakpointLine: shownOn(currentLines(model, track.breakpoints)), entryLine: shownOn(currentLines(model, track.entries)) };
  }

  /**
   * The line to store a new breakpoint shown on `line` under. That is `line`
   * itself, unless a stored breakpoint on `line` now shows elsewhere (its
   * code moved in unsaved edits): then the pending moves are stored first,
   * so the two cannot be confused.
   */
  function newBreakpointLine(line: number): number {
    const model = shownModel();
    const track = model ? tracking.get(model)?.breakpoints : undefined;
    if (!model || !track) return line;
    const lines = currentLines(model, track);
    const shown = lines.get(line);
    if (shown === undefined || shown === line) return line;
    for (const mark of track.marks) mark.origin = lines.get(mark.origin) ?? mark.origin;
    options.onMoved(movesOf(lines), options.path());
    return line;
  }

  function glyphLine(event: DebugEditorMouseEvent): number | null {
    if (!options.enabled() || event.target.type !== options.glyphMarginTarget) return null;
    return event.target.position?.lineNumber ?? event.target.range?.startLineNumber ?? null;
  }

  const disposables: DebugDisposable[] = [
    options.editor.onMouseDown((event) => {
      const line = glyphLine(event);
      if (line === null || !event.event.leftButton) return;
      const anchor = { x: event.event.posx, y: event.event.posy };
      const info = lineInfo(line);
      if (info.breakpointLine !== null) options.onToggle(info.breakpointLine);
      else if (info.entryLine !== null) options.onEntryClick(info.entryLine, anchor);
      else options.onToggle(newBreakpointLine(line));
    }),
    options.editor.onContextMenu((event) => {
      const line = glyphLine(event);
      if (line === null) return;
      options.onGlyphContextMenu(line, { x: event.event.posx, y: event.event.posy });
    }),
  ];

  function dispose() {
    for (const disposable of disposables) disposable.dispose();
    for (const model of [...tracking.keys()]) forget(model);
    rendered?.clear();
    rendered = null;
  }

  return { refresh, captureMoves, lineInfo, newBreakpointLine, dispose };
}
