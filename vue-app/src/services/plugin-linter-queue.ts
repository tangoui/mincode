/**
 * Per-plugin lint scheduling for the `linters` contribution point. The host
 * admits one lint call per plugin (concurrentPerPlugin 1), so the frontend
 * queues requests per plugin in FIFO order, keeps only the newest pending
 * request per model (an older pending one resolves as cancelled), and aborts
 * the in-flight request of a model when a newer version of that model is
 * queued. No IPC, Vue or Monaco imports: the transport is injected.
 */
import type { LintResult } from "@/domain/plugins/linters";
import type { ContributionError } from "@/services/plugin-contribution-requests";

export type LintOutcome = { ok: true; result: LintResult } | { ok: false; error: ContributionError };

/** Sends one request; must honour `signal` (resolve with code "cancelled" once aborted). */
export type LintTransport<Request> = (request: Request, signal: AbortSignal) => Promise<LintOutcome>;

interface Job<Request> {
  modelKey: string;
  request: Request;
  resolve(outcome: LintOutcome): void;
}

interface PluginQueue<Request> {
  running?: { modelKey: string; controller: AbortController };
  pending: Map<string, Job<Request>>;
}

const CANCELLED: LintOutcome = { ok: false, error: { code: "cancelled", detail: "" } };

export interface LintScheduler<Request> {
  /** Queues a lint of `modelKey` on `pluginId`; resolves with the outcome (never rejects). */
  schedule(pluginId: string, modelKey: string, request: Request): Promise<LintOutcome>;
  /** Cancels the pending and running requests of one model (all plugins). */
  cancelModel(modelKey: string): void;
  /** Cancels everything queued or running for one plugin. */
  cancelPlugin(pluginId: string): void;
  /** Number of queued (not running) requests; tests and diagnostics. */
  pendingCount(): number;
}

export function createLintScheduler<Request>(transport: LintTransport<Request>): LintScheduler<Request> {
  const queues = new Map<string, PluginQueue<Request>>();

  function queueOf(pluginId: string): PluginQueue<Request> {
    let queue = queues.get(pluginId);
    if (!queue) {
      queue = { pending: new Map() };
      queues.set(pluginId, queue);
    }
    return queue;
  }

  function pump(pluginId: string) {
    const queue = queues.get(pluginId);
    if (!queue || queue.running) return;
    const next = queue.pending.values().next();
    if (next.done) {
      queues.delete(pluginId);
      return;
    }
    const job = next.value;
    queue.pending.delete(job.modelKey);
    const controller = new AbortController();
    queue.running = { modelKey: job.modelKey, controller };
    void transport(job.request, controller.signal)
      .catch((): LintOutcome => ({ ok: false, error: { code: "unknown", detail: "lint request failed" } }))
      .then((outcome) => {
        job.resolve(controller.signal.aborted ? CANCELLED : outcome);
        if (queue.running?.controller === controller) queue.running = undefined;
        pump(pluginId);
      });
  }

  return {
    schedule(pluginId, modelKey, request) {
      return new Promise<LintOutcome>((resolve) => {
        const queue = queueOf(pluginId);
        queue.pending.get(modelKey)?.resolve(CANCELLED);
        queue.pending.delete(modelKey);
        if (queue.running?.modelKey === modelKey) queue.running.controller.abort();
        queue.pending.set(modelKey, { modelKey, request, resolve });
        pump(pluginId);
      });
    },
    cancelModel(modelKey) {
      for (const queue of queues.values()) {
        queue.pending.get(modelKey)?.resolve(CANCELLED);
        queue.pending.delete(modelKey);
        if (queue.running?.modelKey === modelKey) queue.running.controller.abort();
      }
    },
    cancelPlugin(pluginId) {
      const queue = queues.get(pluginId);
      if (!queue) return;
      for (const job of queue.pending.values()) job.resolve(CANCELLED);
      queue.pending.clear();
      queue.running?.controller.abort();
    },
    pendingCount() {
      let count = 0;
      for (const queue of queues.values()) count += queue.pending.size;
      return count;
    },
  };
}
