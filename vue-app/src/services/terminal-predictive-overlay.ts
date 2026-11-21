import type { OverlayCell, OverlayFrame, TerminalView } from "@/domain/terminal/predictive-echo/types";

/** 预测回显覆盖层：一块盖在 `.xterm-screen` 上的 2D canvas，只画引擎给出的那一行。 */

export interface OverlayStyle {
  fontFamily: string;
  fontSize: number;
  foreground: string;
  background: string;
  cursorColor: string;
  cursorStyle: "block" | "underline" | "bar";
}

export interface PredictiveOverlay {
  /** null = 清空 */
  draw(frame: OverlayFrame | null, view: TerminalView): void;
  /**
   * resize / 字体 / WebGL 重建后：重新计算格子尺寸，并按新尺寸重画最近一帧（draw(null) 后没有帧则不画）。
   * 尺寸与 dpr 不变时不重设 canvas 位图。从空覆盖层到有帧时若 dpr 变了，draw 自己也会量一次。
   */
  remeasure(): void;
  setStyle(style: OverlayStyle): void;
  dispose(): void;
}

const DIM_ALPHA = 0.5;
const UNDERLINE_THICKNESS = 1;
const CURSOR_UNDERLINE_THICKNESS = 2;
const CURSOR_BAR_WIDTH = 1;
/** 高于 xterm 画布，但必须低于 `.xterm-helpers`（z-index 5，输入法候选框所在层） */
const OVERLAY_Z_INDEX = "4";

export function cellRect(row: number, col: number, width: 1 | 2, cellW: number, cellH: number) {
  return { x: col * cellW, y: row * cellH, w: cellW * width, h: cellH };
}

export function createPredictiveOverlay(
  screen: HTMLElement,
  cols: () => number,
  rows: () => number,
  initialStyle: OverlayStyle,
  createCanvas: () => HTMLCanvasElement = () => document.createElement("canvas"),
): PredictiveOverlay {
  let style = initialStyle;
  let canvas: HTMLCanvasElement | null = null;
  let ctx: CanvasRenderingContext2D | null = null;
  let cssWidth = 0;
  let cellW = 0;
  let cellH = 0;
  let devicePixel = 1;
  let measuredDpr = 0;
  let bitmapWidth = -1;
  let bitmapHeight = -1;
  let lastRow: number | null = null;
  /** 最近一次非空帧：remeasure / setStyle 后要重画（引擎会去重相同帧，不会再发） */
  let lastFrame: OverlayFrame | null = null;
  let lastView: TerminalView | null = null;
  let disposed = false;

  function currentDpr() {
    return (typeof window !== "undefined" && window.devicePixelRatio) || 1;
  }

  function measure() {
    if (!canvas || !ctx) return;
    const dpr = currentDpr();
    measuredDpr = dpr;
    devicePixel = 1 / dpr;
    // 用 getBoundingClientRect 的小数尺寸，clientWidth 取整后格子会累积偏移
    const bounds = screen.getBoundingClientRect();
    cssWidth = bounds.width;
    const cssHeight = bounds.height;
    cellW = cssWidth / Math.max(1, cols());
    cellH = cssHeight / Math.max(1, rows());
    const width = Math.round(cssWidth * dpr);
    const height = Math.round(cssHeight * dpr);
    // 给 width/height 赋值（即使同值）会重建位图：尺寸与 dpr 都没变就不动
    if (width !== bitmapWidth || height !== bitmapHeight || canvas.width !== width || canvas.height !== height) {
      canvas.style.width = `${cssWidth}px`;
      canvas.style.height = `${cssHeight}px`;
      canvas.width = width;
      canvas.height = height;
      bitmapWidth = width;
      bitmapHeight = height;
    }
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, cssWidth, cssHeight);
    lastRow = null;
  }

  function ensureCanvas(): boolean {
    if (ctx) return true;
    const created = createCanvas();
    const context = created.getContext("2d");
    if (!context) return false;
    Object.assign(created.style, {
      position: "absolute",
      left: "0",
      top: "0",
      pointerEvents: "none",
      zIndex: OVERLAY_Z_INDEX,
    });
    screen.appendChild(created);
    canvas = created;
    ctx = context;
    measure();
    return true;
  }

  /** 上下各多擦一个设备像素，避免小数格子边缘的抗锯齿残影 */
  function clearRow(context: CanvasRenderingContext2D, row: number) {
    context.clearRect(0, row * cellH - devicePixel, cssWidth, cellH + 2 * devicePixel);
  }

  function fillCell(context: CanvasRenderingContext2D, row: number, col: number, width: 1 | 2, color: string) {
    const r = cellRect(row, col, width, cellW, cellH);
    context.fillStyle = color;
    context.fillRect(r.x, r.y, r.w, r.h);
  }

  function drawChar(context: CanvasRenderingContext2D, row: number, col: number, width: 1 | 2, char: string, color: string) {
    if (!char || char === " ") return;
    const r = cellRect(row, col, width, cellW, cellH);
    context.fillStyle = color;
    context.fillText(char, r.x + r.w / 2, r.y + r.h / 2);
  }

  /**
   * 预测字：背景取它盖住的那格，前景为默认色半透明；移位的原有格子带着原来的前景/背景
   * （补进来的空格用默认背景）。
   */
  function drawCell(context: CanvasRenderingContext2D, row: number, cell: OverlayCell, view: TerminalView) {
    const source = cell.predicted
      ? view.cell(row, cell.col, true)
      : cell.sourceCol !== undefined ? view.cell(row, cell.sourceCol, true) : null;
    fillCell(context, row, cell.col, cell.width, source?.bg ?? style.background);
    const dim = cell.predicted || source?.dim === true;
    context.globalAlpha = dim ? DIM_ALPHA : 1;
    const fg = cell.predicted ? style.foreground : source?.fg ?? style.foreground;
    drawChar(context, row, cell.col, cell.width, cell.char, fg);
    if (cell.predicted) {
      const r = cellRect(row, cell.col, cell.width, cellW, cellH);
      context.fillStyle = style.foreground;
      context.fillRect(r.x, r.y + r.h - UNDERLINE_THICKNESS, r.w, UNDERLINE_THICKNESS);
    }
    context.globalAlpha = 1;
  }

  function drawCursor(context: CanvasRenderingContext2D, frame: OverlayFrame, view: TerminalView) {
    const col = frame.cursorCol;
    const own = frame.cells.find((cell) => cell.col === col);
    const under = own ? null : view.cell(frame.row, col);
    const width: 1 | 2 = own ? own.width : under?.width === 2 ? 2 : 1;
    const r = cellRect(frame.row, col, width, cellW, cellH);
    context.fillStyle = style.cursorColor;
    if (style.cursorStyle === "block") {
      context.fillRect(r.x, r.y, r.w, r.h);
      // 块光标里的字用背景色反显，与 xterm 一致
      drawChar(context, frame.row, col, width, own ? own.char : under?.char ?? "", style.background);
    } else if (style.cursorStyle === "underline") {
      context.fillRect(r.x, r.y + r.h - CURSOR_UNDERLINE_THICKNESS, r.w, CURSOR_UNDERLINE_THICKNESS);
    } else {
      context.fillRect(r.x, r.y, CURSOR_BAR_WIDTH, r.h);
    }
  }

  /** 盖住真实光标，改由预测位置画光标；该格若没有覆盖层格子，按缓冲区原字重画（不让原字消失） */
  function hideRealCursor(context: CanvasRenderingContext2D, frame: OverlayFrame, view: TerminalView) {
    let col = view.cursorX;
    let under = view.cell(frame.row, col, true);
    if (under.width === 0 && col > 0) {
      col -= 1;
      under = view.cell(frame.row, col, true);
    }
    const width: 1 | 2 = under.width === 2 ? 2 : 1;
    fillCell(context, frame.row, col, width, under.bg ?? style.background);
    if (frame.cells.some((cell) => cell.col <= col + width - 1 && cell.col + cell.width - 1 >= col)) return;
    context.globalAlpha = under.dim ? DIM_ALPHA : 1;
    drawChar(context, frame.row, col, width, under.char, under.fg ?? style.foreground);
    context.globalAlpha = 1;
  }

  function render(context: CanvasRenderingContext2D, frame: OverlayFrame, view: TerminalView) {
    context.font = `${style.fontSize}px ${style.fontFamily}`;
    context.textBaseline = "middle";
    context.textAlign = "center";
    if (view.cursorY === frame.row) hideRealCursor(context, frame, view);
    for (const cell of frame.cells) drawCell(context, frame.row, cell, view);
    drawCursor(context, frame, view);
  }

  function hasCellSize() {
    return cellW > 0 && cellH > 0;
  }

  function paint(context: CanvasRenderingContext2D, frame: OverlayFrame, view: TerminalView) {
    if (lastRow !== null && lastRow !== frame.row) clearRow(context, lastRow);
    clearRow(context, frame.row);
    render(context, frame, view);
    lastRow = frame.row;
  }

  function repaintLast() {
    if (ctx && lastFrame && lastView && hasCellSize()) paint(ctx, lastFrame, lastView);
  }

  return {
    draw(frame, view) {
      if (disposed) return;
      if (!frame) {
        if (ctx && lastRow !== null) clearRow(ctx, lastRow);
        lastRow = null;
        lastFrame = null;
        lastView = null;
        return;
      }
      const fresh = lastFrame === null;
      lastFrame = frame;
      lastView = view;
      const created = !ctx;
      if (!ensureCanvas() || !ctx) return;
      // 空覆盖层后的第一帧且 dpr 变了：量一次（窗口换到另一块屏幕时列数没变，不会有 resize；
      // 尺寸/字体/主题/WebGL 变化由 remeasure 负责）
      if ((fresh && !created && currentDpr() !== measuredDpr) || !hasCellSize()) measure();
      // 屏幕还没有尺寸（隐藏中）：保留帧，等 remeasure 再画
      if (!hasCellSize()) return;
      paint(ctx, frame, view);
    },
    remeasure() {
      if (disposed) return;
      measure();
      repaintLast();
    },
    setStyle(next) {
      if (disposed) return;
      style = next;
      repaintLast();
    },
    dispose() {
      disposed = true;
      canvas?.remove();
      canvas = null;
      bitmapWidth = -1;
      bitmapHeight = -1;
      ctx = null;
      lastRow = null;
      lastFrame = null;
      lastView = null;
    },
  };
}
