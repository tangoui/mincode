// Opt-in keystroke latency probe (-tags e2e builds only): types into a real xterm pane through
// its helper textarea (the same keydown/keypress path a real key takes inside the WebView) and
// times each character from the key event to its echo arriving as shell-output, and to the next
// animation frame after it (≈ when it can be on screen).
import { listenAppEvent } from "@/services/desktop-bridge";
import { FrameMonitor, round, type PerfLog } from "@/services/e2e-perf-kit";
import { readPredictiveEchoProbe } from "@/services/e2e-predictive-echo-probe";

const pause = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
// Escape sequences end in letters; strip them so only echoed text is matched.
// eslint-disable-next-line no-control-regex -- matching ESC/BEL terminal control bytes is the point
const ESCAPES = /\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78]/g;

function keyEvent(type: "keydown" | "keypress" | "keyup", char: string) {
  const code = char.charCodeAt(0);
  const upper = char.toUpperCase().charCodeAt(0);
  const event = new KeyboardEvent(type, { key: char, code: `Key${char.toUpperCase()}`, bubbles: true, cancelable: true });
  Object.defineProperty(event, "keyCode", { get: () => (type === "keypress" ? code : upper) });
  Object.defineProperty(event, "which", { get: () => (type === "keypress" ? code : upper) });
  Object.defineProperty(event, "charCode", { get: () => (type === "keypress" ? code : 0) });
  return event;
}

function stats(values: number[]) {
  const sorted = [...values].sort((a, b) => a - b);
  const at = (q: number) => (sorted.length ? sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * q))] : 0);
  return { n: sorted.length, p50: round(at(0.5)), p90: round(at(0.9)), max: round(sorted[sorted.length - 1] ?? 0) };
}

/** Types `count` letters `intervalMs` apart into the pane's textarea; logs one `TYPING {json}` line. */
export async function measureTyping(log: PerfLog, label: string, sessionId: string, textarea: HTMLTextAreaElement, count: number, intervalMs: number) {
  const letters = "abcdefghijklmnopqrstuvwxyz";
  const pending: { char: string; sent: number }[] = [];
  const echo: number[] = [];
  const paint: number[] = [];
  const stop = listenAppEvent(`shell-output:${sessionId}`, (payload: unknown) => {
    const arrived = performance.now();
    const raw = typeof payload === "string" ? payload : String((payload as { data?: unknown })?.data ?? "");
    const text = raw.replace(ESCAPES, "");
    const matched: number[] = [];
    for (const char of text) {
      if (pending.length && char === pending[0].char) matched.push(pending.shift()!.sent);
    }
    if (!matched.length) return;
    for (const sent of matched) echo.push(arrived - sent);
    requestAnimationFrame(() => {
      const painted = performance.now();
      for (const sent of matched) paint.push(painted - sent);
    });
  });
  const frames = new FrameMonitor();
  frames.start();
  try {
    textarea.focus();
    for (let i = 0; i < count; i++) {
      const char = letters[i % letters.length];
      pending.push({ char, sent: performance.now() });
      typeChar(textarea, char);
      await pause(intervalMs);
    }
    const end = performance.now() + 3000;
    while (pending.length && performance.now() < end) await pause(20);
    await pause(100);
  } finally {
    stop();
  }
  const frame = frames.stop();
  const row = { label, intervalMs, lost: pending.length, echo: stats(echo), paint: stats(paint), maxFrameMs: round(frame.maxFrameMs), p95FrameMs: round(frame.p95FrameMs), longFrames: frame.longFrames };
  log.done(`TYPING ${JSON.stringify(row)}`);
  return row;
}

/** Non-printable keys the predictive echo scenario sends through xterm (keydown + keyup, like a real key). */
const SPECIAL_KEYS = {
  Enter: { key: "Enter", code: "Enter", keyCode: 13, ctrlKey: false },
  Escape: { key: "Escape", code: "Escape", keyCode: 27, ctrlKey: false },
  /** readline unix-line-discard: clears the typed line */
  CtrlU: { key: "u", code: "KeyU", keyCode: 85, ctrlKey: true },
} as const;

/** One printable key (keydown/keypress/keyup), unmeasured. */
/**
 * One key like the WebView delivers it: no keypress after a keydown the page cancelled (xterm
 * cancels the keydown it already sent as input). A synthetic keypress after it would send every
 * char twice.
 */
export function typeChar(textarea: HTMLTextAreaElement, char: string) {
  const down = keyEvent("keydown", char);
  textarea.dispatchEvent(down);
  if (!down.defaultPrevented) textarea.dispatchEvent(keyEvent("keypress", char));
  textarea.dispatchEvent(keyEvent("keyup", char));
}

export function pressKey(textarea: HTMLTextAreaElement, name: keyof typeof SPECIAL_KEYS) {
  const spec = SPECIAL_KEYS[name];
  for (const type of ["keydown", "keyup"] as const) {
    const event = new KeyboardEvent(type, { key: spec.key, code: spec.code, ctrlKey: spec.ctrlKey, bubbles: true, cancelable: true });
    Object.defineProperty(event, "keyCode", { get: () => spec.keyCode });
    Object.defineProperty(event, "which", { get: () => spec.keyCode });
    textarea.dispatchEvent(event);
  }
}

export interface PredictiveTypingRow {
  label: string;
  intervalMs: number;
  chars: number;
  /** keys whose echo never arrived (only when an echo is expected) */
  lost?: number;
  /** keys echoed by the server (when no echo is expected; should be 0) */
  echoed?: number;
  /** key → echo of the first char (never predicted: it waits for the real echo) */
  firstEcho: number | null;
  echo: ReturnType<typeof stats>;
  /** key → overlay draw, chars 2..N predicted inside their key handler: the draw runs there, so this is its cost */
  overlay: ReturnType<typeof stats>;
  /** key → the animation frame after the overlay draw (≈ on screen), same keys: the spec's "≤ one frame" target */
  overlayPaint: ReturnType<typeof stats>;
  /** 1-based indexes of the predicted keys (in their key handler, or promoted later) */
  predicted: number[];
  /** predicted keys that were typed before the line's first echo and drawn when it arrived (typing faster than the echo) */
  promoted: number[];
  /** chars 2..N whose echo arrived after the line's first echo but that were never predicted */
  unpredictedAfterFirstEcho: number;
  /** non-null overlay draws while typing (any cause) */
  draws: number;
  /** predicted cells still on the overlay after the echoes settled */
  finalCells: number;
  /** wrong guesses / timeouts the engine counted while typing and settling */
  misses: number;
  maxFrameMs: number;
  p95FrameMs: number;
  longFrames: number;
}

/**
 * Types `text` `intervalMs` apart into an SSH pane with the predictive echo probe on; logs one
 * `TYPING {json}` line. A key counts as predicted when the overlay draws more predicted cells
 * than before between that key and the next. With `expectEcho: false` (password prompt, vi
 * normal mode) it does not wait for echoes and reports how many chars were echoed instead.
 */
export async function measurePredictiveTyping(
  log: PerfLog, label: string, sessionId: string, textarea: HTMLTextAreaElement, text: string, intervalMs: number,
  options: { expectEcho?: boolean } = {},
): Promise<PredictiveTypingRow> {
  const expectEcho = options.expectEcho ?? true;
  const probe = readPredictiveEchoProbe(sessionId);
  const drawsAtStart = probe.draws.length;
  const missesAtStart = probe.misses;
  const pending: { char: string; sent: number; index: number }[] = [];
  const echo: number[] = [];
  /** key index (0-based) → arrival of its echo */
  const echoAt: (number | undefined)[] = [];
  let firstEcho: number | null = null;
  let echoed = 0;
  const stop = listenAppEvent(`shell-output:${sessionId}`, (payload: unknown) => {
    const arrived = performance.now();
    const raw = typeof payload === "string" ? payload : String((payload as { data?: unknown })?.data ?? "");
    for (const char of raw.replace(ESCAPES, "")) {
      if (!pending.length || char !== pending[0].char) continue;
      const key = pending.shift()!;
      echoed += 1;
      echo.push(arrived - key.sent);
      echoAt[key.index - 1] = arrived;
      if (key.index === 1) firstEcho = round(arrived - key.sent);
    }
  });
  const sentAt: number[] = [];
  const firstDrawIndex: number[] = [];
  const cellsBefore: number[] = [];
  /** key index → key → next animation frame, for keys that drew synchronously */
  const paintAfter: (number | undefined)[] = [];
  /** key index → a draw happened inside its key handler */
  const syncDraw: boolean[] = [];
  const frames = new FrameMonitor();
  frames.start();
  try {
    textarea.focus();
    for (let i = 0; i < text.length; i++) {
      const char = text[i];
      const sent = performance.now();
      sentAt.push(sent);
      firstDrawIndex.push(probe.draws.length);
      cellsBefore.push(probe.cells);
      pending.push({ char, sent, index: i + 1 });
      typeChar(textarea, char);
      // The binding predicts synchronously inside the key handler: a draw now is this key's.
      syncDraw.push(probe.draws.length > firstDrawIndex[i]);
      if (syncDraw[i]) requestAnimationFrame(() => { paintAfter[i] = performance.now() - sent; });
      await pause(intervalMs);
    }
    if (expectEcho) {
      const end = performance.now() + 3000;
      while (pending.length && performance.now() < end) await pause(20);
    }
    // Confirmations run after xterm parsed the echo; let the last ones land.
    await pause(expectEcho ? 300 : 800);
  } finally {
    stop();
  }
  const frame = frames.stop();
  // Draws are attributed by how many predicted cells they add (echo confirmations only ever lower
  // the count): within key i's window (before key i+1) an increase of n credits the latest n
  // uncredited keys up to i. A key-handler draw adds that key; the draw made when the line's
  // first echo arrives adds every key typed before it (typing faster than the echo).
  const keyDraw: (number | null)[] = sentAt.map(() => null);
  for (let i = 0; i < sentAt.length; i++) {
    const endIndex = i + 1 < firstDrawIndex.length ? firstDrawIndex[i + 1] : probe.draws.length;
    let before = cellsBefore[i];
    for (let index = firstDrawIndex[i]; index < endIndex; index++) {
      const draw = probe.draws[index];
      let added = draw.cells - before;
      before = draw.cells;
      for (let key = i; key >= 0 && added > 0; key--) {
        if (keyDraw[key] !== null || draw.at < sentAt[key]) continue;
        keyDraw[key] = draw.at - sentAt[key];
        added -= 1;
      }
    }
  }
  const predicted = keyDraw.flatMap((value, i) => (value === null ? [] : [i + 1]));
  const promoted = predicted.filter((index) => !syncDraw[index - 1]);
  const own = (value: number | null, i: number): value is number => i > 0 && value !== null && syncDraw[i];
  const overlay = keyDraw.filter(own);
  const overlayPaint = keyDraw.flatMap((value, i) => (own(value, i) && paintAfter[i] !== undefined ? [paintAfter[i]!] : []));
  const firstEchoAt = echoAt.reduce<number>((min, at) => (at === undefined ? min : Math.min(min, at)), Infinity);
  const unpredictedAfterFirstEcho = sentAt.filter((_, i) => i > 0 && keyDraw[i] === null && (echoAt[i] ?? Infinity) > firstEchoAt && Number.isFinite(firstEchoAt)).length;
  const row: PredictiveTypingRow = {
    label, intervalMs, chars: text.length,
    ...(expectEcho ? { lost: pending.length } : { echoed }),
    firstEcho, echo: stats(echo), overlay: stats(overlay), overlayPaint: stats(overlayPaint), predicted, promoted, unpredictedAfterFirstEcho,
    draws: probe.draws.length - drawsAtStart, finalCells: probe.cells, misses: probe.misses - missesAtStart,
    maxFrameMs: round(frame.maxFrameMs), p95FrameMs: round(frame.p95FrameMs), longFrames: frame.longFrames,
  };
  log.done(`TYPING ${JSON.stringify(row)}`);
  return row;
}
