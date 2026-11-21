import { describe, expect, it, vi } from "vitest";
import type { OverlayFrame, TerminalView, ViewCell } from "@/domain/terminal/predictive-echo/types";
import { cellRect, createPredictiveOverlay, type OverlayStyle } from "@/services/terminal-predictive-overlay";

type Call = { op: string; args: unknown[]; alpha: number; fill: string };

function fakeContext() {
  const calls: Call[] = [];
  const state = { globalAlpha: 1, fillStyle: "", strokeStyle: "", font: "", textBaseline: "", textAlign: "" };
  const stack: (typeof state)[] = [];
  const record = (op: string) => (...args: unknown[]) => {
    calls.push({ op, args, alpha: state.globalAlpha, fill: String(state.fillStyle) });
  };
  const ctx = {
    get globalAlpha() { return state.globalAlpha; },
    set globalAlpha(value: number) { state.globalAlpha = value; calls.push({ op: "globalAlpha", args: [value], alpha: value, fill: state.fillStyle }); },
    get fillStyle() { return state.fillStyle; },
    set fillStyle(value: string) { state.fillStyle = value; },
    get strokeStyle() { return state.strokeStyle; },
    set strokeStyle(value: string) { state.strokeStyle = value; },
    get font() { return state.font; },
    set font(value: string) { state.font = value; },
    get textBaseline() { return state.textBaseline; },
    set textBaseline(value: string) { state.textBaseline = value; },
    get textAlign() { return state.textAlign; },
    set textAlign(value: string) { state.textAlign = value; },
    save: () => { stack.push({ ...state }); calls.push({ op: "save", args: [], alpha: state.globalAlpha, fill: state.fillStyle }); },
    restore: () => { Object.assign(state, stack.pop() ?? {}); calls.push({ op: "restore", args: [], alpha: state.globalAlpha, fill: state.fillStyle }); },
    setTransform: record("setTransform"),
    scale: record("scale"),
    clearRect: record("clearRect"),
    fillRect: record("fillRect"),
    fillText: record("fillText"),
    strokeRect: record("strokeRect"),
  };
  return { ctx, calls };
}

function setup() {
  const { ctx, calls } = fakeContext();
  // 记录位图尺寸赋值：给 canvas.width/height 赋值（即使同值）会清空并重建位图
  const bitmap = { width: 0, height: 0, sets: 0 };
  const canvas = {
    get width() { return bitmap.width; },
    set width(value: number) { bitmap.width = value; bitmap.sets += 1; },
    get height() { return bitmap.height; },
    set height(value: number) { bitmap.height = value; bitmap.sets += 1; },
    style: {} as Record<string, string>, getContext: vi.fn(() => ctx), remove: vi.fn(),
  };
  const createCanvas = vi.fn(() => canvas as unknown as HTMLCanvasElement);
  // clientWidth/clientHeight 故意给错的整数值：格子尺寸必须来自 getBoundingClientRect（小数）
  const rect = { width: 780, height: 170 };
  const getBoundingClientRect = vi.fn(() => ({ ...rect }));
  const screen = { clientWidth: 1, clientHeight: 1, appendChild: vi.fn(), getBoundingClientRect } as unknown as HTMLElement;
  const style: OverlayStyle = {
    fontFamily: "Menlo", fontSize: 13, foreground: "#ffffff", background: "#000000", cursorColor: "#00ff00", cursorStyle: "block",
  };
  const overlay = createPredictiveOverlay(screen, () => 100, () => 10, style, createCanvas);
  return { overlay, canvas, bitmap, createCanvas, screen, rect, calls, getBoundingClientRect };
}

function view(cells: Record<string, Partial<ViewCell>> = {}, cursor = { x: 4, y: 2 }, colorReads: string[] = []): TerminalView {
  return {
    cols: 100, rows: 10, cursorX: cursor.x, cursorY: cursor.y, cursorAbsY: cursor.y, alternate: false, atBottom: true, appCursorKeys: false,
    cell: (row, col, colors) => {
      const { fg, bg, ...rest } = cells[`${row}:${col}`] ?? {};
      if (!colors) return { char: " ", width: 1, dim: false, ...rest };
      colorReads.push(`${row}:${col}`);
      return { char: " ", width: 1, dim: false, ...rest, fg, bg };
    },
  };
}

const frame: OverlayFrame = {
  row: 2,
  cursorCol: 6,
  cells: [
    { col: 4, char: "a", width: 1, predicted: true },
    { col: 5, char: "x", width: 1, predicted: false, sourceCol: 4 },
  ],
};

describe("cellRect", () => {
  it("computes the cell position", () => {
    expect(cellRect(2, 3, 1, 7.8, 17)).toEqual({ x: 7.8 * 3, y: 34, w: 7.8, h: 17 });
    const r = cellRect(2, 3, 1, 7.8, 17);
    expect(r.x).toBeCloseTo(23.4);
    expect(cellRect(2, 3, 2, 7.8, 17).w).toBeCloseTo(15.6);
  });
});

describe("predictive overlay", () => {
  it("no canvas until first frame", () => {
    const { overlay, createCanvas, screen } = setup();
    overlay.draw(null, view());
    overlay.remeasure();
    expect(createCanvas).not.toHaveBeenCalled();
    expect(screen.appendChild).not.toHaveBeenCalled();
  });

  it("creates an absolute, click-through canvas sized by devicePixelRatio", () => {
    const { overlay, canvas, screen, calls } = setup();
    const previous = window.devicePixelRatio;
    Object.defineProperty(window, "devicePixelRatio", { value: 2, configurable: true });
    try {
      overlay.draw(frame, view());
    } finally {
      Object.defineProperty(window, "devicePixelRatio", { value: previous, configurable: true });
    }
    expect(screen.appendChild).toHaveBeenCalledWith(canvas);
    expect(canvas.style.position).toBe("absolute");
    expect(canvas.style.pointerEvents).toBe("none");
    // 必须低于 .xterm-helpers（z-index 5，输入法候选框所在层）
    expect(Number(canvas.style.zIndex)).toBeGreaterThan(0);
    expect(Number(canvas.style.zIndex)).toBeLessThan(5);
    expect(canvas.width).toBe(1560);
    expect(canvas.height).toBe(340);
    expect(canvas.style.width).toBe("780px");
    expect(calls.some((c) => (c.op === "scale" && c.args[0] === 2) || (c.op === "setTransform" && c.args[0] === 2))).toBe(true);
  });

  it("predicted cells drawn dim and underlined", () => {
    const { overlay, calls } = setup();
    overlay.draw(frame, view());
    const text = calls.find((c) => c.op === "fillText" && c.args[0] === "a");
    expect(text?.alpha).toBe(0.5);
    expect(text?.fill).toBe("#ffffff");
    // underline: a foreground fillRect inside cell (row 2, col 4) at its bottom edge, at half alpha
    const underline = calls.find((c) => c.op === "fillRect" && c.fill === "#ffffff" && c.alpha === 0.5
      && Math.abs((c.args[0] as number) - 31.2) < 0.01 && (c.args[1] as number) > 34 && (c.args[1] as number) < 51);
    expect(underline).toBeTruthy();
  });

  it("fills background before drawing and draws shifted cells at full opacity", () => {
    const { overlay, calls } = setup();
    overlay.draw(frame, view());
    const shiftedText = calls.findIndex((c) => c.op === "fillText" && c.args[0] === "x");
    expect(calls[shiftedText].alpha).toBe(1);
    const bg = calls.findIndex((c) => c.op === "fillRect" && c.fill === "#000000" && Math.abs((c.args[0] as number) - 39) < 0.01);
    expect(bg).toBeGreaterThanOrEqual(0);
    expect(bg).toBeLessThan(shiftedText);
  });

  it("draws shifted grey suggestion cells dimmed", () => {
    const { overlay, calls } = setup();
    overlay.draw(frame, view({ "2:4": { char: "x", dim: true } }));
    expect(calls.find((c) => c.op === "fillText" && c.args[0] === "x")?.alpha).toBe(0.5);
  });

  it("hides the real cursor and draws a predicted block cursor", () => {
    const { overlay, calls } = setup();
    overlay.draw({ row: 2, cursorCol: 6, cells: [] }, view({}, { x: 9, y: 2 }));
    expect(calls.some((c) => c.op === "fillRect" && c.fill === "#000000" && Math.abs((c.args[0] as number) - 70.2) < 0.01 && c.args[1] === 34)).toBe(true);
    expect(calls.some((c) => c.op === "fillRect" && c.fill === "#00ff00" && Math.abs((c.args[0] as number) - 46.8) < 0.01 && c.args[1] === 34)).toBe(true);
  });

  it("redraws the original char under the hidden real cursor when no overlay cell covers it", () => {
    const { overlay, calls } = setup();
    overlay.draw({ row: 2, cursorCol: 3, cells: [] }, view({ "2:4": { char: "q", dim: true } }, { x: 4, y: 2 }));
    const bg = calls.findIndex((c) => c.op === "fillRect" && c.fill === "#000000" && Math.abs((c.args[0] as number) - 31.2) < 0.01);
    const text = calls.findIndex((c) => c.op === "fillText" && c.args[0] === "q");
    expect(bg).toBeGreaterThanOrEqual(0);
    expect(text).toBeGreaterThan(bg);
    expect(calls[text].alpha).toBe(0.5);
  });

  it("clear erases previous row", () => {
    const { overlay, calls } = setup();
    overlay.draw(frame, view());
    calls.length = 0;
    overlay.draw(null, view());
    expect(calls.filter((c) => c.op === "clearRect")).toEqual([
      // 上下各多擦一个设备像素（dpr 1 → 1px）
      expect.objectContaining({ args: [0, 33, 780, 19] }),
    ]);
    expect(calls.some((c) => c.op === "fillText" || c.op === "fillRect")).toBe(false);
  });

  it("only clears the previous and current rows", () => {
    const { overlay, calls } = setup();
    overlay.draw(frame, view());
    calls.length = 0;
    overlay.draw({ ...frame, row: 5 }, view());
    const cleared = calls.filter((c) => c.op === "clearRect").map((c) => c.args[1]).sort();
    expect(cleared).toEqual([33, 84]);
  });

  it("remeasure recomputes cell size and clears; dispose removes the canvas", () => {
    const { overlay, canvas, rect, calls } = setup();
    overlay.draw(frame, view());
    rect.width = 1000;
    calls.length = 0;
    overlay.remeasure();
    expect(calls.some((c) => c.op === "clearRect")).toBe(true);
    calls.length = 0;
    overlay.draw(frame, view());
    expect(calls.some((c) => c.op === "fillText" && c.args[0] === "a" && Math.abs((c.args[1] as number) - 45) < 0.01)).toBe(true);
    overlay.dispose();
    expect(canvas.remove).toHaveBeenCalled();
    calls.length = 0;
    overlay.draw(frame, view());
    expect(calls).toEqual([]);
  });

  it("clears one device pixel above and below the row at the current dpr", () => {
    const { overlay, calls } = setup();
    const previous = window.devicePixelRatio;
    Object.defineProperty(window, "devicePixelRatio", { value: 2, configurable: true });
    try {
      overlay.draw(frame, view());
      calls.length = 0;
      overlay.draw(null, view());
    } finally {
      Object.defineProperty(window, "devicePixelRatio", { value: previous, configurable: true });
    }
    expect(calls.filter((c) => c.op === "clearRect").map((c) => c.args)).toEqual([[0, 33.5, 780, 18]]);
  });

  it("uses fractional bounding-rect sizes for cells", () => {
    const { overlay, rect, canvas, calls } = setup();
    rect.width = 785;
    overlay.draw(frame, view());
    expect(canvas.style.width).toBe("785px");
    // col 4 中心 = 4.5 × 7.85
    expect(calls.find((c) => c.op === "fillText" && c.args[0] === "a")?.args[1]).toBeCloseTo(35.325);
  });

  it("keeps the frame while the screen has no size and draws it once measurable", () => {
    const { overlay, rect, calls } = setup();
    rect.width = 0;
    rect.height = 0;
    overlay.draw(frame, view());
    expect(calls.some((c) => c.op === "fillText" || c.op === "fillRect")).toBe(false);
    rect.width = 780;
    rect.height = 170;
    overlay.remeasure();
    expect(calls.some((c) => c.op === "fillText" && c.args[0] === "a")).toBe(true);
  });

  it("measures again on draw when the cell size is still zero", () => {
    const { overlay, rect, calls } = setup();
    rect.width = 0;
    overlay.draw(frame, view());
    rect.width = 780;
    overlay.draw(frame, view());
    expect(calls.find((c) => c.op === "fillText" && c.args[0] === "a")?.args[1]).toBeCloseTo(35.1);
  });

  it("remeasure and setStyle redraw the last frame; draw(null) forgets it", () => {
    const { overlay, calls } = setup();
    overlay.draw(frame, view());
    calls.length = 0;
    overlay.remeasure();
    expect(calls.some((c) => c.op === "fillText" && c.args[0] === "a")).toBe(true);
    calls.length = 0;
    overlay.setStyle({ fontFamily: "Menlo", fontSize: 13, foreground: "#ff0000", background: "#111111", cursorColor: "#00ff00", cursorStyle: "bar" });
    const cleared = calls.findIndex((c) => c.op === "clearRect" && c.args[1] === 33);
    const text = calls.findIndex((c) => c.op === "fillText" && c.args[0] === "a");
    expect(cleared).toBeGreaterThanOrEqual(0);
    expect(text).toBeGreaterThan(cleared);
    expect(calls[text].fill).toBe("#ff0000");
    overlay.draw(null, view());
    calls.length = 0;
    overlay.remeasure();
    overlay.setStyle({ fontFamily: "Menlo", fontSize: 13, foreground: "#ffffff", background: "#000000", cursorColor: "#00ff00", cursorStyle: "block" });
    expect(calls.some((c) => c.op === "fillText" || c.op === "fillRect")).toBe(false);
  });

  it("wide predicted cells fill and underline two cells", () => {
    const { overlay, calls } = setup();
    overlay.draw({ row: 2, cursorCol: 6, cells: [{ col: 4, char: "中", width: 2, predicted: true }] }, view({}, { x: 4, y: 2 }));
    const at = (c: Call, x: number) => Math.abs((c.args[0] as number) - x) < 0.01;
    expect(calls.some((c) => c.op === "fillRect" && c.fill === "#000000" && at(c, 31.2) && Math.abs((c.args[2] as number) - 15.6) < 0.01 && c.args[3] === 17)).toBe(true);
    expect(calls.some((c) => c.op === "fillRect" && c.fill === "#ffffff" && c.alpha === 0.5 && at(c, 31.2) && Math.abs((c.args[2] as number) - 15.6) < 0.01 && (c.args[3] as number) < 17)).toBe(true);
    expect(calls.find((c) => c.op === "fillText" && c.args[0] === "中")?.args[1]).toBeCloseTo(39);
  });

  it("hides a real cursor on the right half of a wide char by covering the whole char", () => {
    const { overlay, calls } = setup();
    const cells = { "2:4": { char: "中", width: 2 as const }, "2:5": { char: "", width: 0 as const } };
    overlay.draw({ row: 2, cursorCol: 7, cells: [] }, view(cells, { x: 5, y: 2 }));
    const bg = calls.findIndex((c) => c.op === "fillRect" && c.fill === "#000000" && Math.abs((c.args[0] as number) - 31.2) < 0.01 && Math.abs((c.args[2] as number) - 15.6) < 0.01);
    const text = calls.findIndex((c) => c.op === "fillText" && c.args[0] === "中");
    expect(bg).toBeGreaterThanOrEqual(0);
    expect(text).toBeGreaterThan(bg);
    expect(calls[text].args[1]).toBeCloseTo(39);
  });

  it("repaints shifted cells and the cell under the real cursor with their own colours", () => {
    const { overlay, calls } = setup();
    const reads: string[] = [];
    const cells = {
      "2:4": { char: "x", fg: "#ff0000", bg: "#0000ff" },
      "2:9": { char: "q", fg: "#123456", bg: "#654321" },
    };
    overlay.draw(frame, view(cells, { x: 9, y: 2 }, reads));
    const at = (c: Call, x: number) => Math.abs((c.args[0] as number) - x) < 0.01;
    // 预测字 a 盖在 col 4：用它盖住的那格的背景，前景为默认色半透明
    expect(calls.some((c) => c.op === "fillRect" && c.fill === "#0000ff" && at(c, 31.2) && c.args[3] === 17)).toBe(true);
    const a = calls.find((c) => c.op === "fillText" && c.args[0] === "a");
    expect(a?.fill).toBe("#ffffff");
    expect(a?.alpha).toBe(0.5);
    // 右移到 col 5 的 x 带着原来的前景/背景
    expect(calls.some((c) => c.op === "fillRect" && c.fill === "#0000ff" && at(c, 39) && c.args[3] === 17)).toBe(true);
    expect(calls.find((c) => c.op === "fillText" && c.args[0] === "x")?.fill).toBe("#ff0000");
    // 真实光标下那格（col 9）按原配色重画
    expect(calls.some((c) => c.op === "fillRect" && c.fill === "#654321" && at(c, 70.2) && c.args[3] === 17)).toBe(true);
    expect(calls.find((c) => c.op === "fillText" && c.args[0] === "q")?.fill).toBe("#123456");
    expect(calls.some((c) => c.op === "fillRect" && c.fill === "#000000" && c.args[3] === 17)).toBe(false);
    // 只读要画的格子的颜色
    expect([...new Set(reads)].sort()).toEqual(["2:4", "2:9"]);
  });

  it("falls back to the default colours when a cell has none", () => {
    const { overlay, calls } = setup();
    overlay.draw(frame, view({ "2:4": { char: "x" } }));
    expect(calls.find((c) => c.op === "fillText" && c.args[0] === "x")?.fill).toBe("#ffffff");
    expect(calls.some((c) => c.op === "fillRect" && c.fill === "#000000" && Math.abs((c.args[0] as number) - 39) < 0.01)).toBe(true);
  });

  it("remeasure with an unchanged size and dpr keeps the canvas bitmap", () => {
    const { overlay, bitmap, rect } = setup();
    overlay.draw(frame, view());
    const sets = bitmap.sets;
    for (let i = 0; i < 5; i += 1) overlay.remeasure();
    expect(bitmap.sets).toBe(sets);
    rect.width = 800;
    overlay.remeasure();
    expect(bitmap.width).toBe(800);
    expect(bitmap.sets).toBeGreaterThan(sets);
  });

  it("measures again when a frame follows an empty overlay only if the dpr changed (window moved to another display)", () => {
    const { overlay, canvas, getBoundingClientRect } = setup();
    overlay.draw(frame, view());
    expect(canvas.width).toBe(780);
    // dpr 没变：空覆盖层后的下一帧不量（慢速打字时每个字都是这种情况）
    overlay.draw(null, view());
    const before = getBoundingClientRect.mock.calls.length;
    overlay.draw(frame, view());
    expect(getBoundingClientRect.mock.calls.length).toBe(before);
    overlay.draw(null, view());
    const previous = window.devicePixelRatio;
    Object.defineProperty(window, "devicePixelRatio", { value: 2, configurable: true });
    try {
      overlay.draw(frame, view());
    } finally {
      Object.defineProperty(window, "devicePixelRatio", { value: previous, configurable: true });
    }
    expect(canvas.width).toBe(1560);
    expect(canvas.height).toBe(340);
    // 后续帧（仍有帧显示）不再量
    const measured = getBoundingClientRect.mock.calls.length;
    overlay.draw({ ...frame, cursorCol: 7 }, view());
    expect(getBoundingClientRect.mock.calls.length).toBe(measured);
  });
});
