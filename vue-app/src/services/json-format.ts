/**
 * Pretty-prints a large JSON response off the main thread. JSON.parse + stringify of a
 * multi-megabyte body blocks the UI for most of a second; a worker keeps the page
 * responsive while the "Format" button shows its loading state. Without worker support
 * (or if the worker fails) the same synchronous formatting runs instead.
 */
import { formatJsonResponseBody } from "@/domain/http/response-body";

export type JsonFormatRunner = (body: string) => Promise<string>;

let worker: Worker | undefined;
let nextId = 1;
let runner: JsonFormatRunner | undefined = typeof Worker === "undefined" ? undefined : runInWorker;

function runInWorker(body: string): Promise<string> {
  worker ??= new Worker(new URL("./workers/json-format.worker.ts", import.meta.url), { type: "module" });
  const active = worker;
  const id = nextId++;
  return new Promise<string>((resolve, reject) => {
    const cleanup = () => {
      active.removeEventListener("message", onMessage);
      active.removeEventListener("error", onError);
    };
    const onMessage = (event: MessageEvent) => {
      const data = event.data as { id?: unknown; text?: unknown } | undefined;
      if (data?.id !== id) return;
      cleanup();
      if (typeof data.text === "string") resolve(data.text);
      else reject(new Error("invalid worker reply"));
    };
    const onError = (event: ErrorEvent) => {
      cleanup();
      worker?.terminate();
      worker = undefined;
      reject(new Error(event.message || "json format worker failed"));
    };
    active.addEventListener("message", onMessage);
    active.addEventListener("error", onError);
    active.postMessage({ id, body });
  });
}

/** Replaces the off-thread runner (tests); undefined formats synchronously. */
export function setJsonFormatRunner(next: JsonFormatRunner | undefined) {
  runner = next;
}

export async function formatJsonOffThread(body: string): Promise<string> {
  if (!runner) return formatJsonResponseBody(body);
  try {
    return await runner(body);
  } catch {
    return formatJsonResponseBody(body);
  }
}
