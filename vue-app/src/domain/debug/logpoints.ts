// Stable reason returned by Go when an adapter did not advertise supportsLogPoints.
export const LOGPOINT_UNSUPPORTED_REASON = "unsupported: logpoints";

/** Blank means an ordinary breakpoint; preserve meaningful text verbatim. */
export function normalizeLogMessage(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}
