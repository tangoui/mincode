/**
 * E2E-only seam: records what the predictive echo overlay of each SSH terminal draws, so the
 * desktop smoke harness can time key → overlay draw and check that the overlay is empty when
 * it should be. Off by default: only the smoke runner, which loads only in `-tags e2e` builds,
 * turns it on, so in the product every draw makes one early-returning call.
 */
import type { OverlayFrame } from "@/domain/terminal/predictive-echo/types";

export interface PredictiveEchoDraw {
  /** performance.now() of the draw */
  at: number;
  /** predicted cells in the frame (shifted original cells not counted) */
  cells: number;
}

export interface PredictiveEchoProbeRecord {
  /** every non-null draw, oldest first */
  draws: PredictiveEchoDraw[];
  /** predicted cells currently shown; 0 after a null frame */
  cells: number;
  /** wrong guesses and timeouts the engine reported (gate.recordMiss) */
  misses: number;
}

let enabled = false;
const records = new Map<string, PredictiveEchoProbeRecord>();

export function enablePredictiveEchoProbe() {
  enabled = true;
}

export function predictiveEchoProbeEnabled() {
  return enabled;
}

/** Called by the binding whenever the engine counts a miss (wrong guess or timeout). */
export function recordPredictiveEchoMiss(sessionId: string) {
  if (!enabled) return;
  readPredictiveEchoProbe(sessionId).misses += 1;
}

/** Called by the predictive echo binding on every overlay frame (null = overlay cleared). */
export function recordPredictiveEchoDraw(sessionId: string, frame: OverlayFrame | null) {
  if (!enabled) return;
  const record = readPredictiveEchoProbe(sessionId);
  if (frame === null) {
    record.cells = 0;
    return;
  }
  let cells = 0;
  for (const cell of frame.cells) if (cell.predicted) cells += 1;
  record.cells = cells;
  record.draws.push({ at: performance.now(), cells });
}

/** The session's record, created empty if needed: a live object that later draws keep updating. */
export function readPredictiveEchoProbe(sessionId: string): PredictiveEchoProbeRecord {
  let record = records.get(sessionId);
  if (!record) {
    record = { draws: [], cells: 0, misses: 0 };
    records.set(sessionId, record);
  }
  return record;
}

export function clearPredictiveEchoProbe(sessionId: string) {
  records.delete(sessionId);
}
