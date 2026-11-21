/** Severity of a resource usage percentage for overview meters; undefined means no sample yet. */
export type UsageTone = "idle" | "normal" | "warning" | "danger";

export function usageTone(percent: number | undefined): UsageTone {
  if (percent === undefined || !Number.isFinite(percent)) return "idle";
  if (percent >= 90) return "danger";
  if (percent >= 75) return "warning";
  return "normal";
}
