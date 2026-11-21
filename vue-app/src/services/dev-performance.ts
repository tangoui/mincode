export type DevPerformanceOutcome = "success" | "error";

export interface DevPerformanceRecord {
  name: string;
  durationMs: number;
  recordedAt: number;
  outcome: DevPerformanceOutcome;
}

export interface DevPerformanceInspector {
  entries: () => readonly DevPerformanceRecord[];
  clear: () => void;
}

const MAX_RECORDS = 200;
const records: DevPerformanceRecord[] = [];

function recordDevPerformance(
  name: string,
  startedAt: number,
  outcome: DevPerformanceOutcome
) {
  if (!import.meta.env.DEV) return;

  const durationMs = Math.max(0, performance.now() - startedAt);
  const record: DevPerformanceRecord = {
    name,
    durationMs,
    recordedAt: Date.now(),
    outcome,
  };
  records.push(record);
  if (records.length > MAX_RECORDS) records.splice(0, records.length - MAX_RECORDS);

  performance.measure(`mincode:${name}`, {
    start: startedAt,
    duration: durationMs,
    detail: { outcome },
  });
}

export function startDevPerformance() {
  return import.meta.env.DEV ? performance.now() : 0;
}

export function finishDevPerformance(
  name: string,
  startedAt: number,
  outcome: DevPerformanceOutcome = "success"
) {
  if (!import.meta.env.DEV) return;
  recordDevPerformance(name, startedAt, outcome);
}

export async function measureDevAsync<T>(name: string, operation: () => Promise<T>): Promise<T> {
  if (!import.meta.env.DEV) return operation();

  const startedAt = performance.now();
  try {
    const result = await operation();
    recordDevPerformance(name, startedAt, "success");
    return result;
  } catch (error) {
    recordDevPerformance(name, startedAt, "error");
    throw error;
  }
}

export function installDevPerformanceInspector() {
  if (!import.meta.env.DEV || typeof window === "undefined") return;

  const inspector: DevPerformanceInspector = {
    entries: () => records.map((record) => ({ ...record })),
    clear: () => {
      records.length = 0;
      performance.clearMeasures();
    },
  };
  window.__MINCODE_PERFORMANCE__ = inspector;
}
