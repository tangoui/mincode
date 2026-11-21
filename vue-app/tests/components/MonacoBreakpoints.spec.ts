import { describe, expect, it, vi } from "vitest";
import {
  buildDebugDecorations,
  debugEditorOptions,
  mapLinesAcrossEdit,
  useEditorDebugDecorations,
  type DebugDecoration,
  type DebugBreakpointHoverDetails,
  type DebugDecorationInput,
  type DebugEditorLike,
  type DebugContentChangedEvent,
  type DebugEditorMouseEvent,
  type DebugModelLike,
  type DebugRange,
} from "@/composables/use-editor-debug-decorations";
import type { SourceBreakpoint } from "@/domain/debug/types";

// MonacoTextEditor is not mounted here: the Monaco runtime pulls in workers and
// every editor feature. These tests cover the decoration builder and the
// composable that MonacoTextEditor wires to the real editor, against a model
// double that applies edits like Monaco: a decoration point before the
// replaced range stays, one inside collapses to its start, one at or after
// its end moves past the inserted text; then a content event is emitted.

const GLYPH_MARGIN = 2;
const CONTENT_TEXT = 6;
const STICKINESS = 1;
const PATH = "/p/app.py";

function glyphClassesOn(decorations: DebugDecoration[], line: number): string[] {
  return decorations
    .filter((item) => item.range.startLineNumber === line && item.options.glyphMarginClassName)
    .map((item) => item.options.glyphMarginClassName!);
}

function glyphLines(decorations: DebugDecoration[], className: string): number[] {
  return decorations
    .filter((item) => item.options.glyphMarginClassName?.split(" ")[0] === className)
    .map((item) => item.range.startLineNumber);
}

function input(partial: Partial<DebugDecorationInput>): DebugDecorationInput {
  return { breakpoints: [], ...partial };
}

function numbered(lines: number): string[] {
  return Array.from({ length: lines }, (_, index) => `stmt${index + 1}`);
}

interface Point {
  line: number;
  column: number;
}

function before(a: Point, b: Point) {
  return a.line < b.line || (a.line === b.line && a.column < b.column);
}

class FakeModel implements DebugModelLike {
  content: string[];
  private next = 0;
  readonly ranges = new Map<string, DebugRange>();
  private readonly listeners: Array<(event: DebugContentChangedEvent) => void> = [];

  constructor(lines: number) {
    this.content = numbered(lines);
  }

  get lines() {
    return this.content.length;
  }

  isDisposed() {
    return false;
  }

  getLineCount() {
    return this.content.length;
  }

  getLineMaxColumn(line: number) {
    return this.content[line - 1]!.length + 1;
  }

  getLineContent(line: number) {
    return this.content[line - 1]!;
  }

  getLinesContent() {
    return [...this.content];
  }

  deltaDecorations(oldIds: string[], next: DebugDecoration[]) {
    for (const id of oldIds) this.ranges.delete(id);
    return next.map((item) => {
      const id = `d${(this.next += 1)}`;
      this.ranges.set(id, { ...item.range });
      return id;
    });
  }

  getDecorationRange(id: string) {
    const range = this.ranges.get(id);
    return range ? { ...range } : null;
  }

  onDidChangeContent(listener: (event: DebugContentChangedEvent) => void) {
    this.listeners.push(listener);
    return { dispose: () => this.listeners.splice(this.listeners.indexOf(listener), 1) };
  }

  /** Replaces the text between two points (end exclusive) with `text`. */
  edit(start: Point, end: Point, text: string) {
    this.apply(start, end, text);
    const event: DebugContentChangedEvent = {
      isFlush: false,
      changes: [{ range: { startLineNumber: start.line, startColumn: start.column, endLineNumber: end.line, endColumn: end.column }, text }],
    };
    for (const listener of [...this.listeners]) listener(event);
  }

  /**
   * One event with several non-overlapping edits (replace-all, multi-cursor):
   * applied bottom-up like Monaco, reported in descending pre-edit order.
   */
  editMany(edits: Array<{ start: Point; end: Point; text: string }>, wrap = (change: DebugContentChangedEvent["changes"][number]) => change) {
    const ordered = [...edits].sort((a, b) => b.start.line - a.start.line || b.start.column - a.start.column);
    for (const item of ordered) this.apply(item.start, item.end, item.text);
    const event: DebugContentChangedEvent = {
      isFlush: false,
      changes: ordered.map((item) =>
        wrap({
          range: { startLineNumber: item.start.line, startColumn: item.start.column, endLineNumber: item.end.line, endColumn: item.end.column },
          text: item.text,
        })
      ),
    };
    for (const listener of [...this.listeners]) listener(event);
  }

  private apply(start: Point, end: Point, text: string) {
    const inserted = text.split("\n");
    const prefix = this.content[start.line - 1]!.slice(0, start.column - 1);
    const suffix = this.content[end.line - 1]!.slice(end.column - 1);
    const newEnd: Point =
      inserted.length === 1
        ? { line: start.line, column: start.column + text.length }
        : { line: start.line + inserted.length - 1, column: inserted[inserted.length - 1]!.length + 1 };
    const map = (point: Point): Point => {
      if (before(point, start)) return point;
      if (before(point, end)) return { ...start };
      if (point.line === end.line) return { line: newEnd.line, column: newEnd.column + point.column - end.column };
      return { line: point.line + newEnd.line - end.line, column: point.column };
    };
    const lines = [...inserted];
    lines[0] = prefix + lines[0];
    lines[lines.length - 1] += suffix;
    this.content.splice(start.line - 1, end.line - start.line + 1, ...lines);
    for (const [id, range] of this.ranges) {
      const from = map({ line: range.startLineNumber, column: range.startColumn });
      const to = map({ line: range.endLineNumber, column: range.endColumn });
      this.ranges.set(id, { startLineNumber: from.line, startColumn: from.column, endLineNumber: to.line, endColumn: to.column });
    }
  }

  /** Inserts count lines at the start of line `at`. */
  insertLines(at: number, count: number) {
    this.edit({ line: at, column: 1 }, { line: at, column: 1 }, "inserted\n".repeat(count));
  }

  /** Selects line n with its line break and deletes it. */
  deleteLine(line: number) {
    this.edit({ line, column: 1 }, { line: line + 1, column: 1 }, "");
  }

  /** Joins line n onto line n - 1 (backspace at its start). */
  joinLine(line: number) {
    this.edit({ line: line - 1, column: this.getLineMaxColumn(line - 1) }, { line, column: 1 }, "");
  }

  /** One edit replacing the text of lines from..to (a formatter span, a cleanup, the undo of those). */
  replaceLines(from: number, to: number, lines: string[]) {
    this.edit({ line: from, column: 1 }, { line: to, column: this.getLineMaxColumn(to) }, lines.join("\n"));
  }

  /** setValue: Monaco drops every decoration of the model, then emits a flush. */
  setValue(lines: string[]) {
    this.content = [...lines];
    this.ranges.clear();
    for (const listener of [...this.listeners]) listener({ isFlush: true, changes: [] });
  }
}

function fakeEditor(model: FakeModel) {
  let rendered: DebugDecoration[] = [];
  const mouseDown: Array<(event: DebugEditorMouseEvent) => void> = [];
  const contextMenu: Array<(event: DebugEditorMouseEvent) => void> = [];
  const listen = <T,>(list: T[]) => (listener: T) => {
    list.push(listener);
    return { dispose: () => undefined };
  };
  const editor: DebugEditorLike = {
    getModel: () => model,
    createDecorationsCollection: () => ({
      set: (decorations: DebugDecoration[]) => {
        rendered = decorations;
      },
      clear: () => {
        rendered = [];
      },
    }),
    onMouseDown: listen(mouseDown),
    onContextMenu: listen(contextMenu),
  };
  const event = (type: number, line: number, button: "left" | "right" = "left"): DebugEditorMouseEvent => ({
    target: { type, position: { lineNumber: line, column: 1 } },
    event: { leftButton: button === "left", rightButton: button === "right", posx: 40, posy: 300 },
  });
  return {
    editor,
    rendered: () => rendered,
    click: (type: number, line: number) => mouseDown.forEach((listener) => listener(event(type, line))),
    rightClick: (type: number, line: number) => contextMenu.forEach((listener) => listener(event(type, line, "right"))),
    /** Monaco's setValue: the editor's decorations go too. */
    replaceContent: (lines: number) => {
      rendered = [];
      model.setValue(numbered(lines));
    },
  };
}

function setup(state: DebugDecorationInput, lines = 40) {
  const model = new FakeModel(lines);
  const fake = fakeEditor(model);
  const handlers = {
    toggle: vi.fn(),
    entryClick: vi.fn(),
    contextMenu: vi.fn(),
    moved: vi.fn(),
  };
  const current = { state };
  const decorations = useEditorDebugDecorations({
    editor: fake.editor,
    enabled: () => true,
    path: () => PATH,
    modelFor: () => model,
    state: () => current.state,
    glyphMarginTarget: GLYPH_MARGIN,
    stickiness: STICKINESS,
    onToggle: handlers.toggle,
    onEntryClick: handlers.entryClick,
    onGlyphContextMenu: handlers.contextMenu,
    onMoved: handlers.moved,
  });
  decorations.refresh();
  return { model, fake, handlers, decorations, current };
}

describe("editor debug decorations", () => {
  it("renders breakpoint glyphs by state", () => {
    const decorations = buildDebugDecorations(
      input({
        breakpoints: [
          { line: 2, enabled: true },
          { line: 4, enabled: true, condition: "x > 1" },
          { line: 6, enabled: false },
          { line: 8, enabled: true },
          { line: 10, enabled: true, hitCondition: "3" },
        ],
        verifiedLines: { 2: true, 8: false },
      }),
      STICKINESS,
    );
    expect(glyphClassesOn(decorations, 2)).toEqual(["debug-bp"]);
    expect(glyphClassesOn(decorations, 4)).toEqual(["debug-bp debug-bp--conditional"]);
    expect(glyphClassesOn(decorations, 6)).toEqual(["debug-bp debug-bp--disabled"]);
    expect(glyphClassesOn(decorations, 8)).toEqual(["debug-bp debug-bp--unverified"]);
    expect(glyphClassesOn(decorations, 10)).toEqual(["debug-bp debug-bp--conditional"]);
    expect(decorations.every((item) => item.options.stickiness === STICKINESS)).toBe(true);
  });

  it("renders logpoints with a distinct shape while retaining disabled and unverified modifiers", () => {
    const decorations = buildDebugDecorations(input({ breakpoints: [
      { line: 2, enabled: true, logMessage: "value={value}" },
      { line: 4, enabled: false, logMessage: "disabled" },
      { line: 6, enabled: true, logMessage: "unverified", condition: "x > 0" },
      { line: 8, enabled: true, logMessage: " \t " },
    ], verifiedLines: { 6: false } }), STICKINESS);
    expect(glyphClassesOn(decorations, 2)).toEqual(["debug-bp debug-bp--logpoint"]);
    expect(glyphClassesOn(decorations, 4)).toEqual(["debug-bp debug-bp--logpoint debug-bp--disabled"]);
    expect(glyphClassesOn(decorations, 6)).toEqual(["debug-bp debug-bp--logpoint debug-bp--unverified"]);
    expect(glyphClassesOn(decorations, 8)).toEqual(["debug-bp"]);
  });

  it("passes the adapter's details of a breakpoint to its hover label", () => {
    const breakpoint = vi.fn(
      (_bp: SourceBreakpoint, verified: boolean | undefined, details?: DebugBreakpointHoverDetails) =>
        `${verified}:${details?.message ?? ""}:${details?.actualLine ?? ""}`,
    );
    const decorations = buildDebugDecorations(
      input({
        breakpoints: [
          { line: 2, enabled: true },
          { line: 4, enabled: true },
          { line: 8, enabled: true },
        ],
        verifiedLines: { 2: true, 4: true, 8: false },
        // Line 2 was moved by the adapter onto line 4; line 4 keeps its own state.
        breakpointDetails: { 2: { actualLine: 4 }, 8: { message: "Class not loaded" } },
      }),
      STICKINESS,
      { labels: { breakpoint, paused: () => "", entryPoint: () => "" } },
    );
    const hovers = decorations.map((item) => item.options.glyphMarginHoverMessage?.value);
    expect(hovers).toEqual(["true::4", "true::", "false:Class not loaded:"]);
    expect(glyphClassesOn(decorations, 2)).toEqual(["debug-bp"]);
  });

  it("glyph click emits toggle-breakpoint with the line", () => {
    const { fake, handlers } = setup(input({}));
    fake.click(GLYPH_MARGIN, 7);
    expect(handlers.toggle).toHaveBeenCalledWith(7);
    fake.click(CONTENT_TEXT, 9);
    expect(handlers.toggle).toHaveBeenCalledTimes(1);
    fake.rightClick(GLYPH_MARGIN, 5);
    expect(handlers.contextMenu).toHaveBeenCalledWith(5, { x: 40, y: 300 });
  });

  it("moves breakpoints with edits", () => {
    const { model, fake, handlers, decorations } = setup(input({ breakpoints: [{ line: 10, enabled: true }] }));
    model.insertLines(1, 2);
    // The rendered glyph follows the edit before anything is saved.
    decorations.refresh();
    expect(glyphClassesOn(fake.rendered(), 12)).toEqual(["debug-bp"]);
    expect(handlers.moved).not.toHaveBeenCalled();
    decorations.captureMoves(PATH)?.();
    expect(handlers.moved).toHaveBeenCalledWith([{ from: 10, to: 12 }], PATH);
  });

  it("paused line gets highlight and arrow", () => {
    const decorations = buildDebugDecorations(input({ pausedLine: 5 }), STICKINESS);
    const paused = decorations.filter((item) => item.range.startLineNumber === 5);
    expect(paused.some((item) => item.options.className === "debug-paused-line" && item.options.isWholeLine)).toBe(true);
    expect(glyphClassesOn(decorations, 5)).toEqual(["debug-paused-arrow"]);
  });

  it("entry glyph hidden when a breakpoint is on the same line", () => {
    const decorations = buildDebugDecorations(
      input({
        breakpoints: [{ line: 3, enabled: true }],
        entryPoints: [
          { line: 3, label: "main" },
          { line: 9, label: "test_app" },
        ],
      }),
      STICKINESS,
    );
    expect(glyphClassesOn(decorations, 3)).toEqual(["debug-bp"]);
    expect(glyphClassesOn(decorations, 9)).toEqual(["debug-entry"]);

    const { fake, handlers } = setup(
      input({ breakpoints: [{ line: 3, enabled: true }], entryPoints: [{ line: 3, label: "main" }, { line: 9, label: "t" }] }),
    );
    fake.click(GLYPH_MARGIN, 9);
    expect(handlers.entryClick).toHaveBeenCalledWith(9, { x: 40, y: 300 });
    fake.click(GLYPH_MARGIN, 3);
    expect(handlers.toggle).toHaveBeenCalledWith(3);
  });

  it("restores glyphs after the content is replaced from outside", () => {
    const { fake } = setup(input({ breakpoints: [{ line: 10, enabled: true }], entryPoints: [{ line: 4, label: "main" }] }));
    fake.replaceContent(40);
    expect(glyphLines(fake.rendered(), "debug-bp")).toEqual([10]);
    expect(glyphLines(fake.rendered(), "debug-entry")).toEqual([4]);
  });

  it("a click in an edited file acts on the stored breakpoint and stores no moves", () => {
    const { model, fake, handlers, decorations, current } = setup(input({ breakpoints: [{ line: 10, enabled: true }] }));
    model.insertLines(1, 2);
    decorations.refresh();
    fake.click(GLYPH_MARGIN, 12);
    expect(handlers.toggle).toHaveBeenCalledWith(10);
    expect(handlers.moved).not.toHaveBeenCalled();
    // Discard: the content goes back and the (still stored) line 10 shows at 10.
    current.state = input({ breakpoints: [{ line: 10, enabled: true }] });
    fake.replaceContent(40);
    expect(glyphLines(fake.rendered(), "debug-bp")).toEqual([10]);
    expect(decorations.captureMoves(PATH)).toBeUndefined();
  });

  it("a right-click stores nothing and resolves the stored line", () => {
    const { model, fake, handlers, decorations } = setup(input({ breakpoints: [{ line: 10, enabled: true }] }));
    model.insertLines(1, 2);
    decorations.refresh();
    fake.rightClick(GLYPH_MARGIN, 12);
    expect(handlers.contextMenu).toHaveBeenCalledWith(12, { x: 40, y: 300 });
    expect(decorations.lineInfo(12)).toEqual({ breakpointLine: 10, entryLine: null });
    expect(handlers.moved).not.toHaveBeenCalled();
  });

  it("a new breakpoint on a stored line whose code moved stores the moves first", () => {
    const { model, fake, handlers, decorations } = setup(input({ breakpoints: [{ line: 10, enabled: true }] }));
    model.insertLines(1, 2);
    decorations.refresh();
    fake.click(GLYPH_MARGIN, 10);
    expect(handlers.moved).toHaveBeenCalledWith([{ from: 10, to: 12 }], PATH);
    expect(handlers.toggle).toHaveBeenCalledWith(10);
    expect(handlers.moved.mock.invocationCallOrder[0]).toBeLessThan(handlers.toggle.mock.invocationCallOrder[0]!);
  });

  it("entry glyphs follow edits until the next lookup", () => {
    const { model, fake, handlers, decorations, current } = setup(input({ entryPoints: [{ line: 3, label: "main" }] }));
    model.insertLines(1, 2);
    decorations.refresh();
    expect(glyphLines(fake.rendered(), "debug-entry")).toEqual([5]);
    fake.click(GLYPH_MARGIN, 5);
    expect(handlers.entryClick).toHaveBeenCalledWith(3, { x: 40, y: 300 });
    fake.click(GLYPH_MARGIN, 3);
    expect(handlers.toggle).toHaveBeenCalledWith(3);
    // Unrelated re-renders do not snap ▶ back; a new lookup replaces it.
    current.state = { ...current.state, pausedLine: 20 };
    decorations.refresh();
    expect(glyphLines(fake.rendered(), "debug-entry")).toEqual([5]);
    current.state = input({ entryPoints: [{ line: 5, label: "main" }] });
    decorations.refresh();
    expect(glyphLines(fake.rendered(), "debug-entry")).toEqual([5]);
    expect(decorations.lineInfo(5).entryLine).toBe(5);
  });

  it("deleting a breakpoint's line removes it", () => {
    const { model, fake, handlers, decorations } = setup(input({ breakpoints: [{ line: 10, enabled: true }, { line: 20, enabled: true }] }));
    model.deleteLine(10);
    decorations.refresh();
    expect(glyphLines(fake.rendered(), "debug-bp")).toEqual([19]);
    decorations.captureMoves(PATH)?.();
    expect(handlers.moved).toHaveBeenCalledWith([{ from: 10, to: 0 }, { from: 20, to: 19 }], PATH);
  });

  it("joining a breakpoint's line onto another keeps the surviving line's breakpoint", () => {
    const { model, fake, handlers, decorations } = setup(
      input({ breakpoints: [{ line: 5, enabled: true, condition: "own" }, { line: 6, enabled: true }] }),
    );
    model.joinLine(6);
    decorations.refresh();
    expect(glyphClassesOn(fake.rendered(), 5)).toEqual(["debug-bp debug-bp--conditional"]);
    expect(decorations.lineInfo(5).breakpointLine).toBe(5);
    decorations.captureMoves(PATH)?.();
    // The store keeps line 5's own breakpoint (debug-configs-store spec).
    expect(handlers.moved).toHaveBeenCalledWith([{ from: 6, to: 5 }], PATH);
  });

  it("save cleanup, its undo and the next save keep every breakpoint on its line", () => {
    const { model, fake, handlers, decorations } = setup(
      input({ breakpoints: [{ line: 1, enabled: true }, { line: 10, enabled: true }, { line: 40, enabled: true }] }),
    );
    const original = model.getLinesContent().map((line) => `${line}   `);
    model.setValue(original);
    // Save: whitespace cleanup is one whole-file replacement.
    model.replaceLines(1, model.lines, original.map((line) => line.trimEnd()));
    expect(glyphLines(fake.rendered(), "debug-bp")).toEqual([1, 10, 40]);
    // Ctrl+Z: the inverse is again a whole-file replacement.
    model.replaceLines(1, model.lines, original);
    expect(glyphLines(fake.rendered(), "debug-bp")).toEqual([1, 10, 40]);
    model.replaceLines(1, model.lines, original.map((line) => line.trimEnd()));
    expect(decorations.captureMoves(PATH)).toBeUndefined();
    expect(handlers.moved).not.toHaveBeenCalled();
  });

  it("formatting a span keeps the breakpoints inside it on their (moved) lines", () => {
    const { model, fake, handlers, decorations } = setup(
      input({ breakpoints: [{ line: 6, enabled: true }, { line: 12, enabled: true }, { line: 30, enabled: true }] }),
    );
    // Lines 5..15 are reindented and line 7 is split in two.
    const formatted = model.content.slice(4, 15).flatMap((line, index) => (index === 2 ? [`    ${line}`, "    split"] : [`    ${line}`]));
    model.replaceLines(5, 15, formatted);
    expect(glyphLines(fake.rendered(), "debug-bp")).toEqual([6, 13, 31]);
    // Undoing the format puts them back.
    model.replaceLines(5, 16, numbered(15).slice(4));
    expect(glyphLines(fake.rendered(), "debug-bp")).toEqual([6, 12, 30]);
    decorations.captureMoves(PATH)?.();
    expect(handlers.moved).not.toHaveBeenCalled();
  });

  it("paste with format keeps the neighbouring breakpoints", () => {
    const { model, fake, handlers, decorations } = setup(input({ breakpoints: [{ line: 6, enabled: true }, { line: 10, enabled: true }] }));
    model.edit({ line: 8, column: 1 }, { line: 8, column: 1 }, "pasted1\npasted2\n");
    // Format-on-paste reindents lines 5..14 in one span.
    model.replaceLines(5, 14, model.content.slice(4, 14).map((line) => `  ${line}`));
    expect(glyphLines(fake.rendered(), "debug-bp")).toEqual([6, 12]);
    decorations.captureMoves(PATH)?.();
    expect(handlers.moved).toHaveBeenCalledWith([{ from: 10, to: 12 }], PATH);
  });

  it("typing over a selected line keeps its breakpoint (a replacement)", () => {
    const { model, fake } = setup(input({ breakpoints: [{ line: 10, enabled: true }, { line: 20, enabled: true }] }));
    model.edit({ line: 10, column: 1 }, { line: 11, column: 1 }, "x");
    model.edit({ line: 18, column: 1 }, { line: 18, column: model.getLineMaxColumn(18) }, "replaced");
    expect(glyphLines(fake.rendered(), "debug-bp")).toEqual([10, 19]);
  });

  it("a line the user deleted stays deleted through later replacements", () => {
    const { model, handlers, decorations } = setup(input({ breakpoints: [{ line: 10, enabled: true }, { line: 20, enabled: true }] }));
    model.deleteLine(10);
    model.replaceLines(1, model.lines, model.getLinesContent());
    decorations.captureMoves(PATH)?.();
    expect(handlers.moved).toHaveBeenCalledWith([{ from: 10, to: 0 }, { from: 20, to: 19 }], PATH);
  });

  it("a many-change event with no breakpoints does no per-change work", () => {
    const { model } = setup(input({}), 3000);
    let reads = 0;
    const counted = (change: DebugContentChangedEvent["changes"][number]) => {
      const { range, text } = change;
      return {
        get range() {
          reads += 1;
          return range;
        },
        get text() {
          reads += 1;
          return text;
        },
      };
    };
    const edits = Array.from({ length: 2000 }, (_, index) => {
      const line = index + 1;
      return { start: { line, column: 1 }, end: { line, column: 5 }, text: "STMT" };
    });
    model.editMany(edits, counted);
    expect(reads).toBe(0);
  });

  it("a many-change event reads each change a bounded number of times", () => {
    const { model } = setup(input({ breakpoints: [{ line: 2999, enabled: true }] }), 3000);
    let reads = 0;
    const counted = (change: DebugContentChangedEvent["changes"][number]) => {
      const { range, text } = change;
      return {
        get range() {
          reads += 1;
          return range;
        },
        get text() {
          reads += 1;
          return text;
        },
      };
    };
    const edits = Array.from({ length: 2000 }, (_, index) => {
      const line = index + 1;
      return { start: { line, column: 1 }, end: { line, column: 5 }, text: "STMT" };
    });
    model.editMany(edits, counted);
    // Linear: a few reads per change, never one per pair of changes.
    expect(reads).toBeLessThan(2000 * 10);
  });

  it("remaps breakpoints across a many-change event that adds and removes lines", () => {
    const { model, fake, handlers, decorations } = setup(
      input({ breakpoints: [{ line: 5, enabled: true }, { line: 50, enabled: true }, { line: 150, enabled: true }, { line: 199, enabled: true }] }),
      200,
    );
    // Every line 1..100 that ends in 0 gets an extra line inserted after its text
    // (+9 lines above 150 and 199, +4 above 50), lines 120..129 each lose
    // their line break onto the next (-10 above 150 and 199), and line 50 itself
    // is rewritten in place.
    const edits: Array<{ start: Point; end: Point; text: string }> = [];
    for (let line = 10; line <= 100; line += 10) {
      if (line === 50) continue;
      edits.push({ start: { line, column: model.getLineMaxColumn(line) }, end: { line, column: model.getLineMaxColumn(line) }, text: "\nadded" });
    }
    edits.push({ start: { line: 50, column: 1 }, end: { line: 50, column: model.getLineMaxColumn(50) }, text: "stmt50 // edited" });
    for (let line = 121; line <= 139; line += 2) {
      edits.push({ start: { line, column: model.getLineMaxColumn(line) }, end: { line: line + 1, column: 1 }, text: "" });
    }
    model.editMany(edits);
    expect(model.getLineContent(5)).toBe("stmt5");
    expect(model.getLineContent(54)).toBe("stmt50 // edited");
    expect(model.getLineContent(149)).toBe("stmt150");
    expect(model.getLineContent(198)).toBe("stmt199");
    expect(glyphLines(fake.rendered(), "debug-bp")).toEqual([5, 54, 149, 198]);
    decorations.captureMoves(PATH)?.();
    expect(handlers.moved).toHaveBeenCalledWith(
      [
        { from: 50, to: 54 },
        { from: 150, to: 149 },
        { from: 199, to: 198 },
      ],
      PATH,
    );
  });

  it("maps lines across an edit ignoring whitespace, clamping unmatched ones", () => {
    const map = mapLinesAcrossEdit(["a", "b", "x", "c", "d"], ["a", "  b", "y", "z", "c", "d"]);
    expect([1, 2, 3, 4, 5].map(map)).toEqual([1, 2, 3, 5, 6]);
    const shrunk = mapLinesAcrossEdit(["a", "b", "c"], ["q"]);
    expect([1, 2, 3].map(shrunk)).toEqual([1, 1, 1]);
  });

  it("keeps a compact project gutter without disabling breakpoint clicks", () => {
    expect(debugEditorOptions("project")).toEqual({
      glyphMargin: true,
      lineNumbersMinChars: 3,
      lineDecorationsWidth: 4,
    });
    expect(debugEditorOptions("sql")).toEqual({});
    expect(debugEditorOptions(undefined)).toEqual({});
  });

  it("shows inline values after the lines above the paused line and clears them on resume", () => {
    const { model, fake, current, decorations } = setup(input({ breakpoints: [{ line: 3, enabled: true }] }), 5);
    model.content = ["import os", "x = 1", "y = x + 2", "print(y)", "z = 3"];
    current.state = input({
      breakpoints: [{ line: 3, enabled: true }],
      pausedLine: 4,
      inlineVariables: [
        { name: "x", value: "1" },
        { name: "y", value: "3" },
      ],
    });
    decorations.refresh();
    const inline = fake.rendered().filter((item) => item.options.after);
    expect(inline.map((item) => [item.range.startLineNumber, item.range.startColumn, item.options.after!.content])).toEqual([
      [2, model.getLineMaxColumn(2), "x = 1"],
      [3, model.getLineMaxColumn(3), "y = 3, x = 1"],
      [4, model.getLineMaxColumn(4), "y = 3"],
    ]);
    expect(inline.every((item) => item.options.after!.inlineClassName === "debug-inline-value" && item.options.showIfCollapsed)).toBe(true);
    // Breakpoints are still the only tracked marks: inline values are not line marks.
    expect(glyphLines(fake.rendered(), "debug-bp")).toEqual([3]);

    current.state = input({ breakpoints: [{ line: 3, enabled: true }], pausedLine: null, inlineVariables: null });
    decorations.refresh();
    expect(fake.rendered().some((item) => item.options.after)).toBe(false);
  });
});
