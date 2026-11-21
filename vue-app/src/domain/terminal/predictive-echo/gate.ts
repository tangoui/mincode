/**
 * 预测回显的开关与节流：决定此刻能预测几个字。
 *
 * - 模式：auto 按回显延迟中位数滞回开关（> 30ms 开，< 20ms 关，没有样本不预测）；
 *   always 忽略延迟；off 永不预测。
 * - 首字规则：本行必须已有一个字被服务器真实回显确认（markEchoConfirmed），任何模式下都适用。
 * - 猜错后进入试探模式：没有未确认预测时只允许 1 个字，确认后恢复连续预测。
 * - 10 秒内猜错 3 次暂停 5 秒，每触发一次暂停时长加倍，上限 60 秒；暂停结束后为试探模式。
 */

export type PredictiveEchoMode = "auto" | "always" | "off";

export interface PredictionGate {
  setMode(mode: PredictiveEchoMode): void;
  recordLatency(ms: number): void;          // 一次确认的按键→确认耗时
  medianLatency(): number | null;
  lineConfirmed(): boolean;                 // 首字规则标记
  markEchoConfirmed(): void;                // 有字被真实回显确认
  clearLine(): void;                        // 回车/控制键/清空/切屏/重连
  recordMiss(now: number): void;            // 猜错或超时
  enterTentative(): void;                   // 进入备用屏幕时调用
  /** 现在可以预测几个字：0 = 不预测，1 = 试探，Infinity = 连续 */
  allowance(now: number, pendingCount: number): number;
  timeoutMs(): number;                      // max(3×median, 1000)
}

const SAMPLE_WINDOW = 16;
const ENABLE_ABOVE_MS = 30;
const DISABLE_BELOW_MS = 20;
const MIN_TIMEOUT_MS = 1000;
const MISS_WINDOW_MS = 10_000;
const MISSES_TO_PAUSE = 3;
const BASE_PAUSE_MS = 5000;
const MAX_PAUSE_MS = 60_000;
/** 距上次猜错和上次暂停结束都已满这么久（干净运行），暂停时长回到 5 秒。 */
const PAUSE_RESET_AFTER_MS = 60_000;

export function createPredictionGate(initialMode: PredictiveEchoMode): PredictionGate {
  let mode = initialMode;

  // 环形数组：最近 16 次回显延迟
  const samples: number[] = [];
  let nextSample = 0;
  let median: number | null = null;
  /** auto 模式的滞回状态 */
  let enabled = false;

  let confirmed = false;
  let tentative = false;

  let recentMisses: number[] = [];
  let lastMissAt = Number.NEGATIVE_INFINITY;
  let pauseUntil = Number.NEGATIVE_INFINITY;
  let nextPauseMs = BASE_PAUSE_MS;
  /**
   * 暂停已开始、结束后尚未有人按 now 判定过。期间的回显都不是预测确认，
   * 不能结束试探；allowance / recordMiss 发现 now >= pauseUntil 时清除。
   */
  let pausePending = false;

  const settlePause = (now: number) => {
    if (pausePending && now >= pauseUntil) pausePending = false;
  };

  const computeMedian = (): number | null => {
    if (samples.length === 0) return null;
    const sorted = [...samples].sort((a, b) => a - b);
    const mid = sorted.length >> 1;
    return sorted.length % 2 === 1 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
  };

  return {
    setMode(next) {
      mode = next;
    },

    recordLatency(ms) {
      if (!Number.isFinite(ms) || ms < 0) return;
      if (samples.length < SAMPLE_WINDOW) samples.push(ms);
      else samples[nextSample] = ms;
      nextSample = (nextSample + 1) % SAMPLE_WINDOW;
      median = computeMedian();
      if (median !== null) {
        if (median > ENABLE_ABOVE_MS) enabled = true;
        else if (median < DISABLE_BELOW_MS) enabled = false;
      }
    },

    medianLatency() {
      return median;
    },

    lineConfirmed() {
      return confirmed;
    },

    markEchoConfirmed() {
      // 首字规则下本行第一个回显从来不是预测，不能算试探成功；
      // 暂停期间（及结束后尚未再预测）的回显同理。
      if (confirmed && !pausePending) tentative = false;
      confirmed = true;
    },

    clearLine() {
      confirmed = false;
    },

    recordMiss(now) {
      tentative = true;
      settlePause(now);
      // 暂停期间不应有预测；即便有迟到的猜错也不计入下一轮暂停
      if (now < pauseUntil) return;
      if (now - Math.max(lastMissAt, pauseUntil) >= PAUSE_RESET_AFTER_MS) {
        nextPauseMs = BASE_PAUSE_MS;
      }
      lastMissAt = now;
      recentMisses = recentMisses.filter((t) => now - t <= MISS_WINDOW_MS);
      recentMisses.push(now);
      if (recentMisses.length >= MISSES_TO_PAUSE) {
        pauseUntil = now + nextPauseMs;
        pausePending = true;
        nextPauseMs = Math.min(nextPauseMs * 2, MAX_PAUSE_MS);
        recentMisses = [];
      }
    },

    enterTentative() {
      tentative = true;
    },

    allowance(now, pendingCount) {
      settlePause(now);
      if (mode === "off" || !confirmed) return 0;
      if (mode === "auto" && !enabled) return 0;
      if (now < pauseUntil) return 0;
      if (tentative) return pendingCount === 0 ? 1 : 0;
      return Infinity;
    },

    timeoutMs() {
      return median === null ? MIN_TIMEOUT_MS : Math.max(3 * median, MIN_TIMEOUT_MS);
    },
  };
}
