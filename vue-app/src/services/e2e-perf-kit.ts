// Shared timing helpers for the opt-in native performance scenarios (-tags e2e builds only).
// WebKit has no Long Tasks API or heap stats, so frames > 50 ms are the jank signal;
// process memory is sampled by the harness and matched to each step's `at` time.

export interface PerfStep {
  step: string;
  ms: number;
  frames: number;
  maxFrameMs: number;
  p95FrameMs: number;
  longFrames: number;
  domNodes: number;
  /** Wall-clock end of the step, matched by the harness against its process-memory samples. */
  at: number;
  error?: string;
}

// rAF stops while the window is occluded (the user is working in other windows); a timer
// fallback keeps the scenario moving, and the gap still shows up as a long frame.
export const nextFrame = () =>
  new Promise<number>((resolve) => {
    const timer = setTimeout(() => resolve(performance.now()), 250);
    requestAnimationFrame((now) => {
      clearTimeout(timer);
      resolve(now);
    });
  });

export class FrameMonitor {
  private deltas: number[] = [];
  private last = 0;
  private running = false;
  start() {
    this.deltas = [];
    this.running = true;
    this.last = performance.now();
    const tick = (now: number) => {
      if (!this.running) return;
      this.deltas.push(now - this.last);
      this.last = now;
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  }
  stop() {
    this.running = false;
    const sorted = [...this.deltas].sort((a, b) => a - b);
    const p95 = sorted.length ? sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * 0.95))] : 0;
    return { frames: sorted.length, maxFrameMs: sorted.length ? sorted[sorted.length - 1] : 0, p95FrameMs: p95, longFrames: sorted.filter((d) => d > 50).length };
  }
}

export async function until<T>(read: () => T | undefined | false | null, label: string, timeoutMs: number): Promise<T> {
  const end = performance.now() + timeoutMs;
  while (performance.now() < end) {
    const value = read();
    if (value) return value;
    await Promise.race([nextFrame(), new Promise((resolve) => setTimeout(resolve, 50))]);
  }
  throw new Error(`Timed out waiting for ${label}`);
}

export const round = (n: number) => Math.round(n * 10) / 10;

export interface PerfLog { done(step: string): void }

/** Returns measure(step, action): runs the action, records frame stats, emits one `PERF {json}` step. */
export function createMeasure(log: PerfLog, results: PerfStep[] = []) {
  return async function measure(step: string, action: () => Promise<void>) {
    const monitor = new FrameMonitor();
    monitor.start();
    const started = performance.now();
    let error: string | undefined;
    try {
      await action();
      await nextFrame();
      await nextFrame();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : String(cause);
    }
    const ms = performance.now() - started;
    const frames = monitor.stop();
    const row: PerfStep = { step, ms: round(ms), frames: frames.frames, maxFrameMs: round(frames.maxFrameMs), p95FrameMs: round(frames.p95FrameMs), longFrames: frames.longFrames, domNodes: document.getElementsByTagName("*").length, at: Date.now(), error };
    results.push(row);
    // One JSON line per step; the harness collects these into the performance report.
    log.done(`PERF ${JSON.stringify(row)}`);
  };
}

/** Startup milestones of this page load, in ms since navigation start (WebKit navigation/paint timing). */
export function startupTimings() {
  const navigation = performance.getEntriesByType("navigation")[0] as PerformanceNavigationTiming | undefined;
  const paint = Object.fromEntries(performance.getEntriesByType("paint").map((entry) => [entry.name, round(entry.startTime)]));
  const scripts = performance.getEntriesByType("resource").filter((entry) => entry.name.endsWith(".js"));
  return {
    domInteractive: navigation ? round(navigation.domInteractive) : undefined,
    domContentLoaded: navigation ? round(navigation.domContentLoadedEventEnd) : undefined,
    load: navigation ? round(navigation.loadEventEnd) : undefined,
    firstPaint: paint["first-paint"],
    firstContentfulPaint: paint["first-contentful-paint"],
    scriptsBeforeScenario: scripts.length,
    scenarioStart: round(performance.now()),
  };
}
