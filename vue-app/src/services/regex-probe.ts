/**
 * Timed regex probe: the third ReDoS layer for plugin grammars. Runs every
 * regex of a grammar on adversarial input (domain/editor/regex-safety.ts
 * `runRegexProbe`) in a dedicated worker; if the worker does not answer within
 * the budget it is terminated and the grammar is treated as unsafe (the
 * language then stays registered without a tokenizer). Results are cached by
 * grammar fingerprint. The worker starts only when a probe is requested,
 * which happens after Monaco has loaded, never at application start.
 */
import type { RegexProbeCase } from "@/domain/editor/regex-safety";

export type RegexProbeResult = "ok" | "timeout" | "unavailable";
export type RegexProbeRunner = (cases: readonly RegexProbeCase[], budgetMs: number) => Promise<RegexProbeResult>;

/** Base budget per grammar, plus a small allowance per regex for large grammars. */
export const REGEX_PROBE_BUDGET_MS = 500;
const REGEX_PROBE_PER_CASE_MS = 0.5;
const REGEX_PROBE_MAX_BUDGET_MS = 3000;
const CACHE_LIMIT = 64;

let worker: Worker | undefined;
let nextId = 1;
let queue: Promise<unknown> = Promise.resolve();
let runner: RegexProbeRunner = runInWorker;
const cache = new Map<string, Promise<RegexProbeResult>>();

function spawn(): Worker | undefined {
  if (typeof Worker === "undefined") return undefined;
  worker ??= new Worker(new URL("./workers/regex-probe.worker.ts", import.meta.url), { type: "module" });
  return worker;
}

function kill() {
  worker?.terminate();
  worker = undefined;
}

function runInWorker(cases: readonly RegexProbeCase[], budgetMs: number): Promise<RegexProbeResult> {
  let target: Worker | undefined;
  try {
    target = spawn();
  } catch {
    target = undefined;
  }
  if (!target) return Promise.resolve("unavailable");
  const id = nextId++;
  const active = target;
  return new Promise<RegexProbeResult>((resolve) => {
    const finish = (result: RegexProbeResult) => {
      clearTimeout(timer);
      active.removeEventListener("message", onMessage);
      active.removeEventListener("error", onError);
      resolve(result);
    };
    const onMessage = (event: MessageEvent) => {
      const data = event.data as { id?: unknown } | undefined;
      if (data?.id === id) finish("ok");
    };
    const onError = () => {
      kill();
      finish("unavailable");
    };
    const timer = setTimeout(() => {
      kill();
      finish("timeout");
    }, budgetMs);
    active.addEventListener("message", onMessage);
    active.addEventListener("error", onError);
    active.postMessage({ id, cases });
  });
}

/**
 * Probes `cases` (one grammar and its configuration) once per `key`;
 * concurrent and repeated calls share the result. Probes run one at a time.
 */
export function probeRegexes(key: string, cases: readonly RegexProbeCase[]): Promise<RegexProbeResult> {
  if (cases.length === 0) return Promise.resolve("ok");
  const cached = cache.get(key);
  if (cached) return cached;
  const budget = Math.min(REGEX_PROBE_MAX_BUDGET_MS, REGEX_PROBE_BUDGET_MS + cases.length * REGEX_PROBE_PER_CASE_MS);
  const result = queue.then(() => runner(cases, budget));
  queue = result.catch(() => undefined);
  const settled = result.catch((): RegexProbeResult => "unavailable");
  cache.set(key, settled);
  if (cache.size > CACHE_LIMIT) cache.delete(cache.keys().next().value!);
  // An unavailable worker is not cached, so a later attach can retry.
  void settled.then((value) => {
    if (value === "unavailable" && cache.get(key) === settled) cache.delete(key);
  });
  return settled;
}

/** Replaces the probe runner (tests; the default runs in a worker) and clears the cache. */
export function setRegexProbeRunner(next?: RegexProbeRunner): void {
  runner = next ?? runInWorker;
  cache.clear();
  kill();
}
