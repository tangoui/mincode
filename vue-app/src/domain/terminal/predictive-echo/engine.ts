/**
 * 预测回显引擎：把可预测的按键排成待确认队列，生成覆盖层内容，并按 xterm 解析后的
 * 真实视图确认或纠错。纯 TS，不依赖 DOM/Vue。
 *
 * - 可预测：可打印字符（光标处插入；输入法一次提交的多个字整体预测，最多 32 个）、
 *   Backspace（删光标左边一格）、左右方向键。其他输入清空队列并清除首字标记，本次不预测。
 *   行模型（插入/删除/移位、覆盖层格子）在 line-model.ts。
 * - 每个预测记下按键时刻、预测前后的光标列，以及受影响格子的基线字符与预期字符；
 *   基线指"前面的预测都被确认后"这格应有的内容，所以部分回显后仍能逐个核对。
 * - serverParsed 只读光标所在行里与队列相关的格子：从队尾往前找视图已到达的预测
 *   状态并确认到那里（剩下的预测必须仍为基线）；否则队首仍为基线且光标未动就保留；
 *   其余一律按猜错清空。
 * - 队列为空时光标无输入地离开最近一次确认的位置（按绝对行比较），或光标左侧那格不再是
 *   确认的字（服务器换行、滚屏、同宽重画新提示符）→ 清除首字标记，密码提示不会沿用上一行的标记。
 * - 未被预测的可打印字符记为延迟样本：回显出现在光标左侧时记录延迟、确认本行首字。
 *   预测关闭时也采样（不画任何东西）。有样本未回显时按键不预测，避免预测位置与屏幕错开；
 *   打字比回显快时，样本回显确认本行首字的那一刻，把紧随其后、仍未回显的样本提升为预测。
 */

import { createPredictionGate, type PredictionGate, type PredictiveEchoMode } from "./gate.ts";
import { applyOp, classify, norm, overlayCells, type Op, type OpEffect, type Slot } from "./line-model.ts";
import type { OverlayFrame, TerminalView } from "./types.ts";

export interface PredictiveEcho {
  userInput(data: string, view: TerminalView, now: number, blocked: boolean): void; // blocked = 选区/失焦/隐藏/未就绪
  serverParsed(view: TerminalView, now: number): void;
  expire(now: number): void;                    // 组合式函数按 nextDeadline() 调用
  nextDeadline(): number | null;
  reset(): void;                                // resize/切屏/重连/隐藏：清空并 clearLine
  setMode(mode: PredictiveEchoMode): void;
  frame(): OverlayFrame | null;                 // 当前应画的内容；null = 清空覆盖层
}

interface Prediction extends OpEffect {
  op: Op;
  sentAt: number;
}

/** 未预测的可打印字符：等它回显出现在光标左侧时记一次延迟 */
/** absY = 发送时（逻辑光标）的缓冲区绝对行：滚屏后同一视口行上的新提示符不能被当成回显 */
interface Sample { char: string; width: 1 | 2; row: number; absY: number; expectX: number; sentAt: number }

const MAX_SAMPLES = 64;

function sameFrame(a: OverlayFrame | null, b: OverlayFrame | null): boolean {
  if (a === null || b === null) return a === b;
  if (a.row !== b.row || a.cursorCol !== b.cursorCol || a.cells.length !== b.cells.length) return false;
  return a.cells.every((c, i) => {
    const d = b.cells[i];
    return c.col === d.col && c.char === d.char && c.width === d.width
      && c.predicted === d.predicted && c.sourceCol === d.sourceCol;
  });
}

export function createPredictiveEcho(
  mode: PredictiveEchoMode,
  onFrame: (frame: OverlayFrame | null) => void,
  gate: PredictionGate = createPredictionGate(mode),
): PredictiveEcho {
  let queue: Prediction[] = [];
  let samples: Sample[] = [];
  let row = 0;
  /** 视图行（建立时）与执行完队列后的预测行；队列为空时为 null */
  let base: Slot[] | null = null;
  let line: Slot[] | null = null;
  let current: OverlayFrame | null = null;
  let lastAlternate = false;
  /**
   * 最近一次真实回显确认后的光标（绝对行）及其左侧那格的字。队列为空时光标离开这里、
   * 或那格的字变了，说明服务器换了行/打印了新提示符（含滚屏、同宽重画）。
   */
  let restX = -1;
  let restAbsY = -1;
  let restCol = -1;
  let restChar = "";

  /** 队列建立（或部分确认后重建）时的光标绝对行与队首左侧那格：保留预测前核对它们没被换掉 */
  let queueAbsY = -1;
  let anchorCol = -1;
  let anchorChar = "";

  /** 光标所在行 x 左侧那格（宽字符取左半格）的列，0 列时为 -1；读 1–2 格 */
  const leftCol = (view: TerminalView, x: number): number => {
    if (x === 0) return -1;
    return x >= 2 && view.cell(view.cursorY, x - 1).width === 0 ? x - 2 : x - 1;
  };
  const charAt = (view: TerminalView, col: number) => norm(view.cell(view.cursorY, col).char);

  const setRest = (view: TerminalView) => {
    restX = view.cursorX;
    restAbsY = view.cursorAbsY;
    restCol = leftCol(view, restX);
    if (restCol >= 0) restChar = charAt(view, restCol);
  };

  const setAnchor = (view: TerminalView, x: number) => {
    queueAbsY = view.cursorAbsY;
    anchorCol = leftCol(view, x);
    if (anchorCol >= 0) anchorChar = charAt(view, anchorCol);
  };

  /** 队列为空、本行已确认，但光标/字已不是确认时的样子 → 清除首字标记（读 1–2 格） */
  const checkRest = (view: TerminalView) => {
    if (queue.length > 0 || !gate.lineConfirmed()) return;
    const moved = view.cursorX !== restX || view.cursorAbsY !== restAbsY
      || (restCol >= 0 && charAt(view, restCol) !== restChar);
    if (moved) gate.clearLine();
  };

  const readLine = (view: TerminalView): Slot[] => {
    const slots: Slot[] = [];
    for (let col = 0; col < view.cols; col += 1) {
      const cell = view.cell(row, col);
      slots.push({ char: cell.char, width: cell.width, dim: cell.dim, src: col, predicted: false });
    }
    return slots;
  };

  const buildFrame = (): OverlayFrame | null => {
    if (queue.length === 0 || line === null || base === null) return null;
    return { row, cells: overlayCells(line, base), cursorCol: queue[queue.length - 1].after };
  };

  const emit = () => {
    const next = buildFrame();
    if (sameFrame(current, next)) return;
    current = next;
    onFrame(next);
  };

  const clearAll = () => {
    queue = [];
    samples = [];
    base = null;
    line = null;
  };

  const clearLine = () => {
    clearAll();
    gate.clearLine();
    emit();
  };

  const miss = (now: number) => {
    gate.recordMiss(now);
    clearLine();
  };

  const echoConfirmed = (sentAt: number, now: number) => {
    gate.recordLatency(now - sentAt);
    gate.markEchoConfirmed();
  };

  const pruneSamples = (now: number) => {
    const limit = gate.timeoutMs();
    let drop = 0;
    while (drop < samples.length && now - samples[drop].sentAt > limit) drop += 1;
    if (drop > 0) samples = samples.slice(drop);
  };

  const logicalCursor = (view: TerminalView): { x: number; y: number } => {
    if (samples.length > 0) {
      const last = samples[samples.length - 1];
      return { x: last.expectX, y: last.row };
    }
    if (queue.length > 0) return { x: queue[queue.length - 1].after, y: row };
    return { x: view.cursorX, y: view.cursorY };
  };

  const addSample = (op: Extract<Op, { kind: "char" }>, view: TerminalView, now: number) => {
    const { x, y } = logicalCursor(view);
    const last = samples[samples.length - 1];
    const absY = last ? last.absY : queue.length > 0 ? queueAbsY : view.cursorAbsY;
    samples.push({ char: op.char, width: op.width, row: y, absY, expectX: x + op.width, sentAt: now });
    if (samples.length > MAX_SAMPLES) samples.shift();
  };

  /** 切到/切出备用屏幕：清空并从试探模式开始 */
  const checkScreen = (view: TerminalView) => {
    if (view.alternate === lastAlternate) return;
    lastAlternate = view.alternate;
    clearLine();
    gate.enterTentative();
  };

  /** 视图是否已到达第 k 个预测执行完后的状态 */
  const reached = (view: TerminalView, p: Prediction): boolean => {
    const cursorOk = p.op.kind === "char" ? view.cursorX >= p.after : view.cursorX === p.after;
    if (!cursorOk || p.col < 0) return cursorOk;
    const cell = view.cell(row, p.col);
    const char = norm(cell.char);
    return p.dimOk ? char === " " || cell.dim : char === p.expected;
  };

  const atBaseline = (view: TerminalView, p: Prediction): boolean =>
    view.cursorX === p.before && (p.col < 0 || norm(view.cell(row, p.col).char) === p.baseline);

  const confirmThrough = (k: number, view: TerminalView, now: number) => {
    // 视图同时等于队首的基线（例如打字再删掉）时无法区分，确认但不记延迟
    const ambiguous = atBaseline(view, queue[0]);
    const done = queue.slice(0, k + 1);
    queue = queue.slice(k + 1);
    if (!ambiguous) for (const p of done) echoConfirmed(p.sentAt, now);
    if (queue.length === 0) {
      base = null;
      line = null;
      setRest(view);
    } else {
      // 以真实视图为准重建预测行，重放剩下的预测；基线/预期也按新视图重算
      // （例如 zsh 在下一格画出灰色建议，b 的基线就从空格变成建议字）
      base = readLine(view);
      line = base.slice();
      const replayed: Prediction[] = [];
      for (const p of queue) {
        const effect = applyOp(line, p.op, p.before);
        if (effect === null) break;
        replayed.push({ ...p, ...effect });
      }
      if (replayed.length < queue.length) clearAll();
      else queue = replayed;
      if (queue.length > 0) setAnchor(view, queue[0].before);
      // 剩下的预测必须仍是基线（例如预测 ab、服务器回 ax：光标已越过 b 的位置）
      if (queue.length > 0 && !atBaseline(view, queue[0])) {
        miss(now);
        return;
      }
    }
    emit();
  };

  const verify = (view: TerminalView, now: number) => {
    // 行变了（含滚屏后视口行相同的新行）→ 猜错
    if (view.cursorY !== row || view.cursorAbsY !== queueAbsY) {
      miss(now);
      return;
    }
    for (let k = queue.length - 1; k >= 0; k -= 1) {
      if (reached(view, queue[k])) {
        confirmThrough(k, view, now);
        return;
      }
    }
    // 保留还要求队首左侧那格未被换掉（同宽重画成新提示符时光标与基线都可能不变）
    if (atBaseline(view, queue[0]) && (anchorCol < 0 || charAt(view, anchorCol) === anchorChar)) return;
    miss(now);
  };

  /** 依次对 trial 执行前 count 个样本；某个样本不在同一行、不接着上一个或无法预测时停下 */
  const replaySamples = (trial: Slot[], count: number, cursor: number): Prediction[] => {
    const effects: Prediction[] = [];
    for (const s of samples.slice(0, count)) {
      if (s.row !== row || s.expectX - s.width !== cursor) break;
      const op: Op = { kind: "char", char: s.char, width: s.width };
      const effect = applyOp(trial, op, cursor);
      if (effect === null) break;
      effects.push({ op, sentAt: s.sentAt, ...effect });
      cursor = effect.after;
    }
    return effects;
  };

  /**
   * 打字比回显快时，本行首字刚被真实回显确认，后面已发出、尚未回显的字（同一行、
   * 紧接着首字）提升为预测；否则样本队列永远排不空，整行都不会预测。
   * 和按键时预测一样核对：视口在底部、额度（试探模式最多 1 个）、不折行；
   * 输入法一次提交的多个字整体提升或整体不提升。
   */
  const promoteSamples = (view: TerminalView, now: number) => {
    if (queue.length > 0 || samples.length === 0 || !view.atBottom || !gate.lineConfirmed()) return;
    const allowance = gate.allowance(now, 0);
    if (allowance === 0) return;
    row = view.cursorY;
    const slots = readLine(view);
    let effects = replaySamples(slots.slice(), Math.min(samples.length, allowance), view.cursorX);
    // 截掉被拆开的输入法组（同一次输入的样本 sentAt 相同）
    while (effects.length > 0 && effects.length < samples.length
      && samples[effects.length].sentAt === effects[effects.length - 1].sentAt) {
      const cut = effects[effects.length - 1].sentAt;
      effects = effects.filter((p) => p.sentAt !== cut);
    }
    if (effects.length === 0) return;
    base = slots;
    line = base.slice();
    queue = replaySamples(line, effects.length, view.cursorX);
    samples = samples.slice(queue.length);
    setAnchor(view, view.cursorX);
    emit();
  };

  const checkSamples = (view: TerminalView, now: number) => {
    for (let i = samples.length - 1; i >= 0; i -= 1) {
      const s = samples[i];
      if (view.cursorY !== s.row || view.cursorAbsY !== s.absY || view.cursorX !== s.expectX || s.expectX - s.width < 0) continue;
      if (norm(view.cell(s.row, s.expectX - s.width).char) !== s.char) continue;
      echoConfirmed(s.sentAt, now);
      samples = samples.slice(i + 1);
      setRest(view);
      promoteSamples(view, now);
      return;
    }
  };

  return {
    userInput(data, view, now, blocked) {
      checkScreen(view);
      checkRest(view); // serverParsed 可能被合批，按键前再查一次
      pruneSamples(now);
      const ops = classify(data, view.appCursorKeys);
      if (ops === null || blocked || !view.atBottom) {
        clearLine();
        return;
      }
      const allowance = gate.allowance(now, queue.length);
      // 试探模式一次只预测一个字：多字输入（输入法提交）只采样
      const canPredict = samples.length === 0 && queue.length < allowance
        && !(allowance === 1 && ops.length > 1);
      if (canPredict && queue.length === 0) {
        row = view.cursorY;
        setAnchor(view, view.cursorX);
        base = readLine(view);
        line = base.slice();
      }
      // 一次输入（含输入法一次提交的多个字）整体预测或整体不预测
      let effects: Prediction[] | null = null;
      if (canPredict && line !== null) {
        const trial = line.slice();
        let cursor = logicalCursor(view).x;
        effects = [];
        for (const op of ops) {
          const effect = applyOp(trial, op, cursor);
          if (effect === null) {
            effects = null;
            break;
          }
          effects.push({ op, sentAt: now, ...effect });
          cursor = effect.after;
        }
        if (effects !== null) line = trial;
      }
      if (effects === null) {
        if (queue.length === 0) {
          base = null;
          line = null;
        }
        if (ops[0].kind !== "char") clearLine();
        else for (const op of ops) if (op.kind === "char") addSample(op, view, now);
        return;
      }
      queue.push(...effects);
      emit();
    },

    serverParsed(view, now) {
      checkScreen(view);
      if (queue.length > 0) verify(view, now);
      if (samples.length > 0) {
        pruneSamples(now);
        checkSamples(view, now);
      }
      checkRest(view);
    },

    expire(now) {
      pruneSamples(now);
      if (queue.length > 0 && now >= queue[0].sentAt + gate.timeoutMs()) miss(now);
    },

    nextDeadline() {
      return queue.length > 0 ? queue[0].sentAt + gate.timeoutMs() : null;
    },

    reset() {
      clearLine();
    },

    setMode(next) {
      gate.setMode(next);
      if (next === "off" && queue.length > 0) {
        clearAll();
        emit();
      }
    },

    frame() {
      return current;
    },
  };
}
