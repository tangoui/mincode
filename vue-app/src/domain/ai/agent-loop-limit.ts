export const DEFAULT_AI_MAX_LOOPS = 200;
export const MIN_AI_MAX_LOOPS = 1;
export const MAX_AI_MAX_LOOPS = 1000;

export function normalizeAiMaxLoops(value: unknown): number {
  const parsed = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(parsed)) return DEFAULT_AI_MAX_LOOPS;
  return Math.min(MAX_AI_MAX_LOOPS, Math.max(MIN_AI_MAX_LOOPS, Math.trunc(parsed)));
}
