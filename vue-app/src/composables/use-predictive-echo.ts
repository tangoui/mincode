/**
 * 把 xterm 接到预测回显引擎与覆盖层：SshTerminal / SshDockerTerminal 共用。
 *
 * - `input` 在 onData 里、发送之前同步调用：引擎决定是否预测，覆盖层同步重画那一行。
 * - `write` 替代 xterm.write：所有已写入数据都解析完（在途写入计数归零）后才让引擎核对，
 *   一次重画被拆成多块时不会按中间状态判错。
 * - 尺寸变化时清空预测并重新量覆盖层；组件在重连、快照恢复、隐藏、换主题和 WebGL onSettled
 *   时调用 `reset()`，效果相同。
 * - 切屏、出现选区、失焦、滚离底部，以及有预测显示时的任何滚动（预测自己的回显从不滚屏）
 *   只清空预测、不量覆盖层：xterm 每滚一行、选区拖动每次鼠标移动都会触发，必须便宜。
 * - 覆盖层要画的格子才解析颜色（cell(row, col, true)），用 xterm 主题与 256 色调色板。
 * - 程序粘贴走 `paste(text)`（替代 xterm.paste）；DOM 粘贴由捕获阶段 paste 监听识别。
 * - 粘贴、未启用、有选区、未聚焦时输入按"不可预测"交给引擎（清空并清除首字标记）。
 * - 视图的格子按需从 buffer 读取（每行只取一次 IBufferLine，复用一个 IBufferCell），
 *   不拷贝整行；预测关闭时引擎不会产生帧，也就不会创建覆盖层或 canvas。
 * - 只用 xterm 公开 API。设置在创建时和 textarea 获得焦点时读取，不按键读取。
 */

import type { IBufferCell, IBufferLine, IDisposable, Terminal } from "@xterm/xterm";
import { getCurrentScope, onScopeDispose } from "vue";
import { createPredictiveEcho } from "@/domain/terminal/predictive-echo/engine";
import { createPredictionGate, type PredictiveEchoMode } from "@/domain/terminal/predictive-echo/gate";
import { buildCellPalette, resolveCellColors, type CellPalette } from "@/domain/terminal/predictive-echo/cell-colors";
import type { OverlayFrame, TerminalView, ViewCell } from "@/domain/terminal/predictive-echo/types";
import { predictiveEchoProbeEnabled, recordPredictiveEchoDraw, recordPredictiveEchoMiss } from "@/services/e2e-predictive-echo-probe";
import { createPredictiveOverlay, type OverlayStyle, type PredictiveOverlay } from "@/services/terminal-predictive-overlay";
import { readTerminalPreferences } from "@/services/terminal-preferences";

export interface PredictiveEchoBinding {
  /** 在 xterm.onData 里、queueShellInput 之前同步调用 */
  input(data: string): void;
  /** 替代 xterm.write(data)：解析完所有在途写入后让引擎核对 */
  write(data: string): void;
  /** 重连、快照恢复、隐藏、换主题、WebGL onSettled 时调用：清空预测并重新量覆盖层 */
  reset(): void;
  /** 替代 xterm.paste(text)：它同步触发的 onData 不预测 */
  paste(text: string): void;
  dispose(): void;
}

export interface PredictiveEchoOptions {
  terminal: () => Terminal | undefined;
  /** `.xterm-screen` */
  screen: () => HTMLElement | null;
  /** status === "ready" && 可见 && 允许输入 */
  enabled: () => boolean;
  style: () => OverlayStyle;
  /** 会话 id：e2e 构建里覆盖层每次绘制都报给 e2e-predictive-echo-probe（产品中该探针不启用） */
  probeId?: () => string;
}

/** 覆盖层样式取当前 xterm 的字体、主题和光标形状（主题未给颜色时用默认深色） */
export function terminalOverlayStyle(terminal: Terminal | null | undefined): OverlayStyle {
  const options = terminal?.options;
  const theme = options?.theme;
  const foreground = theme?.foreground ?? "#d4d4d4";
  return {
    fontFamily: options?.fontFamily ?? "monospace",
    fontSize: options?.fontSize ?? 13,
    foreground,
    background: theme?.background ?? "#1e1e1e",
    cursorColor: theme?.cursor ?? foreground,
    cursorStyle: options?.cursorStyle ?? "bar",
  };
}

const BRACKETED_PASTE_START = "\x1b[200~";
const EMPTY_CELL: ViewCell = { char: " ", width: 1, dim: false };

function readMode(): PredictiveEchoMode {
  return readTerminalPreferences().predictiveEcho;
}

function now() {
  return performance.now();
}

export function usePredictiveEcho(options: PredictiveEchoOptions): PredictiveEchoBinding {
  let mode = readMode();
  let attached: Terminal | undefined;
  let subscriptions: IDisposable[] = [];
  let removeDomListeners: (() => void) | null = null;
  let overlay: PredictiveOverlay | null = null;
  /** 当前终端的在途写入数；换终端时 generation 变化，旧回调作废 */
  let pendingWrites = 0;
  let generation = 0;
  /** 引擎回调 onFrame 时使用的视图（引擎同步回调，调用前设置） */
  let activeView: TerminalView | null = null;
  let pasteFlag = false;
  /** binding.paste 进行中：期间的 input 都来自粘贴 */
  let pasting = false;
  let pasteTimer: ReturnType<typeof setTimeout> | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let timerDeadline: number | null = null;
  let scratchCell: IBufferCell | undefined;
  /**
   * 当前终端的 onScroll 次数：单调的行号基准。xterm 每滚一行发一次 onScroll，回滚缓冲写满、
   * baseY 不再增加时也照发（BufferService.scroll 末尾）；用户滚动视口也会计数，多计只会让
   * 引擎的绝对行核对更保守。
   */
  let scrolled = 0;
  /** 格子颜色解析用的调色板：按 xterm options.theme 对象缓存，reset() 时丢弃 */
  let palette: CellPalette | null = null;
  let paletteTheme: unknown = null;
  let disposed = false;

  // 包一层 gate.recordMiss 把猜错报给 e2e 探针（引擎语义不变；产品中探针不启用，只多一次判断）
  const gate = createPredictionGate(mode);
  const recordMiss = gate.recordMiss.bind(gate);
  gate.recordMiss = (at) => {
    if (options.probeId && predictiveEchoProbeEnabled()) recordPredictiveEchoMiss(options.probeId());
    recordMiss(at);
  };
  const engine = createPredictiveEcho(mode, (frame) => paint(frame), gate);

  function cellPalette(term: Terminal): CellPalette {
    const theme = term.options.theme;
    if (palette && paletteTheme === theme) return palette;
    const style = options.style();
    palette = buildCellPalette(theme ?? {}, style.foreground, style.background);
    paletteTheme = theme;
    return palette;
  }

  function makeView(term: Terminal): TerminalView {
    const buffer = term.buffer.active;
    const viewportY = buffer.viewportY;
    const baseY = buffer.baseY;
    const cursorY = buffer.cursorY;
    let cachedRow = -1;
    let cachedLine: IBufferLine | undefined;
    return {
      cols: term.cols,
      rows: term.rows,
      cursorX: buffer.cursorX,
      cursorY,
      // 单调的行标识（不是缓冲区下标）：baseY 在回滚缓冲写满后不再增加，滚屏后会与旧行相同
      cursorAbsY: scrolled + cursorY,
      alternate: buffer.type === "alternate",
      atBottom: viewportY === baseY,
      appCursorKeys: term.modes.applicationCursorKeysMode,
      cell(row, col, colors) {
        if (row !== cachedRow) {
          cachedRow = row;
          cachedLine = buffer.getLine(viewportY + row);
        }
        const cell = cachedLine?.getCell(col, scratchCell);
        if (!cell) return EMPTY_CELL;
        scratchCell = cell;
        const width = cell.getWidth();
        const result: ViewCell = { char: cell.getChars() || " ", width: width === 0 || width === 2 ? width : 1, dim: cell.isDim() !== 0 };
        if (colors) Object.assign(result, resolveCellColors(cell, cellPalette(term)));
        return result;
      },
    };
  }

  function ensureOverlay(): PredictiveOverlay | null {
    if (overlay) return overlay;
    const term = attached;
    const screen = options.screen();
    if (!term || !screen) return null;
    overlay = createPredictiveOverlay(screen, () => term.cols, () => term.rows, options.style());
    return overlay;
  }

  function paint(frame: OverlayFrame | null) {
    const term = attached;
    if (disposed || !term) return;
    if (options.probeId && predictiveEchoProbeEnabled()) recordPredictiveEchoDraw(options.probeId(), frame);
    if (frame === null) {
      overlay?.draw(null, activeView ?? makeView(term));
      return;
    }
    ensureOverlay()?.draw(frame, activeView ?? makeView(term));
  }

  /** 用同一个视图调用引擎，引擎同步回调 onFrame 时复用它 */
  function withView(term: Terminal, run: (view: TerminalView) => void) {
    const view = makeView(term);
    activeView = view;
    try {
      run(view);
    } finally {
      activeView = null;
    }
    schedule();
  }

  function schedule() {
    const deadline = disposed ? null : engine.nextDeadline();
    if (deadline === timerDeadline) return;
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
    timerDeadline = deadline;
    if (deadline === null) return;
    timer = setTimeout(() => {
      timer = undefined;
      timerDeadline = null;
      if (disposed) return;
      engine.expire(now());
      schedule();
    }, Math.max(0, deadline - now()));
  }

  /** 只清空预测（不量覆盖层、不读 DOM）：滚动/选区/失焦/切屏这类高频事件用 */
  function clearPredictions() {
    if (disposed) return;
    engine.reset();
    schedule();
  }

  /** 清空预测并重新量覆盖层：resize、换主题、WebGL 重建、组件 reset() */
  function resetAll() {
    if (disposed) return;
    engine.reset();
    palette = null;
    if (overlay) {
      overlay.setStyle(options.style());
      overlay.remeasure();
    }
    schedule();
  }

  function atBottom(term: Terminal) {
    const buffer = term.buffer.active;
    return buffer.viewportY === buffer.baseY;
  }

  function bindDom(term: Terminal) {
    const textarea = term.textarea;
    const element = term.element;
    if (removeDomListeners || !textarea || !element) return;
    const onFocus = () => {
      const next = readMode();
      if (next === mode) return;
      mode = next;
      engine.setMode(next);
      schedule();
    };
    const onBlur = () => clearPredictions();
    // xterm 在粘贴事件处理里同步发出 onData；标记只给紧随其后的那次 input，
    // 用宏任务清除（原生事件的各监听器之间会清空微任务队列）
    const onPaste = () => {
      pasteFlag = true;
      if (pasteTimer !== undefined) clearTimeout(pasteTimer);
      pasteTimer = setTimeout(() => {
        pasteTimer = undefined;
        pasteFlag = false;
      }, 0);
    };
    textarea.addEventListener("focus", onFocus);
    textarea.addEventListener("blur", onBlur);
    element.addEventListener("paste", onPaste, true);
    removeDomListeners = () => {
      textarea.removeEventListener("focus", onFocus);
      textarea.removeEventListener("blur", onBlur);
      element.removeEventListener("paste", onPaste, true);
    };
  }

  function detach() {
    for (const subscription of subscriptions) subscription.dispose();
    subscriptions = [];
    removeDomListeners?.();
    removeDomListeners = null;
    overlay?.dispose();
    overlay = null;
    attached = undefined;
    pendingWrites = 0;
    scrolled = 0;
    generation += 1;
  }

  /** 终端在 onMounted 才创建：首次使用时绑定，换了终端实例就重新绑定 */
  function attach(): Terminal | undefined {
    if (disposed) return undefined;
    const term = options.terminal();
    if (term !== attached) {
      detach();
      engine.reset();
      if (!term) return undefined;
      attached = term;
      subscriptions = [
        term.onResize(() => resetAll()),
        // 有预测显示时任何滚动都清空：预测自己的回显从不滚屏（折行/回车不预测），
        // 停在底部的滚动说明输出把那一行推走了
        term.onScroll(() => {
          scrolled += 1;
          if (engine.frame() !== null || !atBottom(term)) clearPredictions();
        }),
        term.buffer.onBufferChange(() => clearPredictions()),
        term.onSelectionChange(() => {
          if (term.hasSelection()) clearPredictions();
        }),
      ];
    }
    if (term) bindDom(term);
    return term;
  }

  function input(data: string) {
    const term = attach();
    if (!term) return;
    const pasted = pasting || pasteFlag || data.startsWith(BRACKETED_PASTE_START);
    pasteFlag = false;
    const blocked = pasted
      || !options.enabled()
      || term.hasSelection()
      || document.activeElement !== term.textarea;
    withView(term, (view) => engine.userInput(data, view, now(), blocked));
  }

  function write(data: string) {
    const term = attach();
    if (!term) return;
    const writeGeneration = generation;
    pendingWrites += 1;
    try {
      term.write(data, () => {
        if (disposed || writeGeneration !== generation) return;
        pendingWrites -= 1;
        if (pendingWrites > 0) return;
        withView(term, (view) => engine.serverParsed(view, now()));
      });
    } catch (error) {
      if (writeGeneration === generation) pendingWrites -= 1;
      throw error;
    }
  }

  /** xterm.paste 同步发出 onData（组件在其中调用 input），期间置位粘贴标记 */
  function paste(text: string) {
    const term = attach();
    if (!term) return;
    pasting = true;
    try {
      term.paste(text);
    } finally {
      pasting = false;
    }
  }

  function reset() {
    attach();
    resetAll();
  }

  function dispose() {
    if (disposed) return;
    disposed = true;
    detach();
    if (timer !== undefined) clearTimeout(timer);
    if (pasteTimer !== undefined) clearTimeout(pasteTimer);
    timer = undefined;
    pasteTimer = undefined;
    timerDeadline = null;
  }

  if (getCurrentScope()) onScopeDispose(dispose);

  return { input, write, reset, paste, dispose };
}
