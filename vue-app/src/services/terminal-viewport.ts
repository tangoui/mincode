// 终端尺寸的测量守卫。
//
// xterm 的 FitAddon 在容器尚未布局时不会失败，而是返回它自己的下限 2 列；再经
// clampTerminalDimensions 抬到 20，就成了一个看起来完全合法的宽度。这个 20 会被
// 写进 PTY，CLI 随后吐出的每一行都按 20 列硬换行 —— 硬换行一旦进了 scrollback
// 就再也无法回流，后来的 SIGWINCH 只能救回当前帧。所以宁可不 resize，也不能把
// 一个没测准的尺寸送给后端。

export interface TerminalViewportSize {
  width: number;
  height: number;
}

// 宽或高不是正有限数，就说明容器还没有布局：首帧 flex-basis 为 0 的 splitter 面板、
// 动画中的抽屉、display:none 的窗格都会落到这里。
export function isMeasuredViewport(size: TerminalViewportSize | null | undefined) {
  if (!size) return false;
  const { width, height } = size;
  return (
    Number.isFinite(width) && Number.isFinite(height) && width > 0 && height > 0
  );
}

export function hasTerminalLayout(host: HTMLElement | null | undefined) {
  if (!host || !host.isConnected) return false;
  // 必须和 FitAddon 用同一个基准：它读的是 getComputedStyle().width，永远解析为
  // content 宽度。clientWidth 含 padding —— .terminal-pane-host 的 padding: 6px 7px
  // 会让一个 0 宽面板报出 clientWidth === 14，足以骗过朴素的 >0 判断，而 FitAddon
  // 拿到的是 0，照样给出 2 列。display:none 时 width 解析为 "auto"，parseFloat 得
  // NaN，同样判否。
  const style = getComputedStyle(host);
  return isMeasuredViewport({
    width: Number.parseFloat(style.width),
    height: Number.parseFloat(style.height),
  });
}

// 等 host 第一次拿到真实布局。超时也 resolve：窗格可能长期不可见，让调用方退回
// 兜底尺寸，好过终端永远不启动。返回值表示等到的是不是真实测量。
export function waitForTerminalLayout(
  host: HTMLElement | null | undefined,
  timeoutMs = 2000,
) {
  return new Promise<boolean>((resolve) => {
    if (hasTerminalLayout(host)) {
      resolve(true);
      return;
    }
    if (!host || typeof ResizeObserver === "undefined") {
      resolve(false);
      return;
    }
    let settled = false;
    const finish = (measured: boolean) => {
      if (settled) return;
      settled = true;
      observer.disconnect();
      window.clearTimeout(timer);
      resolve(measured);
    };
    const observer = new ResizeObserver(() => {
      if (hasTerminalLayout(host)) finish(true);
    });
    const timer = window.setTimeout(() => finish(false), timeoutMs);
    observer.observe(host);
  });
}

// PTY 尺寸兜底。下限是最后一道保险，不该由没测准的容器触发 —— 那是调用方的责任，
// 调用前请先确认 hasTerminalLayout()。
export function clampTerminalDimensions(cols: number, rows: number) {
  return {
    cols: Math.max(TERMINAL_MIN_COLS, Math.min(512, cols || 120)),
    rows: Math.max(TERMINAL_MIN_ROWS, Math.min(256, rows || 32)),
  };
}

export const TERMINAL_MIN_COLS = 20;
export const TERMINAL_MIN_ROWS = 5;

/** The parts of an xterm Terminal and its FitAddon that sizing needs. */
export interface ResizableTerminal {
  readonly cols: number;
  readonly rows: number;
  resize(cols: number, rows: number): void;
  refresh(start: number, end: number): void;
}
export interface TerminalSizeProposer {
  proposeDimensions(): { cols: number; rows: number } | undefined;
}

/**
 * Fits the screen terminal to its container and returns the size the PTY must get,
 * which is always the size the screen now has; null leaves both untouched.
 *
 * FitAddon.fit() would shrink the screen to whatever a sliver of a container holds,
 * down to 2 columns (a pane mid-collapse during a tab switch or window resize passes
 * the width > 0 layout guard), while the PTY only gets the clamped 20. The shell then
 * redraws for a width the screen does not have and leaves 2-column fragments in the
 * scrollback. A proposal below the usable minimum is skipped; the next layout change
 * measures again.
 */
export function fitTerminalSize(terminal: ResizableTerminal, fitAddon: TerminalSizeProposer) {
  const proposed = fitAddon.proposeDimensions();
  if (!proposed || !Number.isFinite(proposed.cols) || !Number.isFinite(proposed.rows)) return null;
  if (proposed.cols < TERMINAL_MIN_COLS || proposed.rows < TERMINAL_MIN_ROWS) return null;
  const size = clampTerminalDimensions(proposed.cols, proposed.rows);
  if (terminal.cols !== size.cols || terminal.rows !== size.rows) {
    terminal.resize(size.cols, size.rows);
  } else {
    // Disposing WebGL in a hidden tab creates a DOM renderer whose initial glyph
    // measurements are zero. Repaint after showing even if the grid is unchanged;
    // resizing to the same grid is a no-op in xterm and leaves those spans stale.
    terminal.refresh(0, size.rows - 1);
  }
  return size;
}
