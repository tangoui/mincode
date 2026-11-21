export type AiOperationMode = "read" | "write";
export type AiOperationState =
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled";

export interface AiOperationSnapshot<T = unknown> {
  id: string;
  resourceId: string;
  capability: string;
  mode: AiOperationMode;
  state: AiOperationState;
  createdAt: number;
  startedAt?: number;
  finishedAt?: number;
  result?: T;
  error?: string;
}

export interface AiOperationRequest<T> {
  resourceId: string;
  capability: string;
  mode: AiOperationMode;
  run(signal: AbortSignal): Promise<T> | T;
}

export interface AiOperationHandle<T> {
  done: Promise<void>;
  snapshot(): AiOperationSnapshot<T>;
  cancel(): void;
}

interface OperationManagerOptions {
  createId?: () => string;
  now?: () => number;
}

interface InternalOperation<T> {
  snapshot: AiOperationSnapshot<T>;
  request: AiOperationRequest<T>;
  controller: AbortController;
  resolveDone: () => void;
}

function defaultId() {
  return typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
    ? crypto.randomUUID()
    : `operation-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function createOperationManager(options: OperationManagerOptions = {}) {
  const createId = options.createId ?? defaultId;
  const now = options.now ?? Date.now;
  const operations = new Map<string, InternalOperation<unknown>>();
  const writeQueues = new Map<string, InternalOperation<unknown>[]>();

  function finish(operation: InternalOperation<unknown>) {
    // 句柄持有 operation 引用即可读取最终快照；表里只保留未结束的操作，避免长会话无限增长。
    operations.delete(operation.snapshot.id);
    operation.resolveDone();
    if (operation.request.mode !== "write") return;
    const queue = writeQueues.get(operation.request.resourceId);
    if (!queue || queue[0] !== operation) return;
    queue.shift();
    if (!queue.length) writeQueues.delete(operation.request.resourceId);
    else run(queue[0]);
  }

  function run(operation: InternalOperation<unknown>) {
    if (operation.controller.signal.aborted) {
      operation.snapshot.state = "cancelled";
      operation.snapshot.finishedAt = now();
      finish(operation);
      return;
    }
    operation.snapshot.state = "running";
    operation.snapshot.startedAt = now();
    void Promise.resolve()
      .then(() => operation.request.run(operation.controller.signal))
      .then((result) => {
        if (operation.controller.signal.aborted) {
          operation.snapshot.state = "cancelled";
        } else {
          operation.snapshot.state = "succeeded";
          operation.snapshot.result = result;
        }
      })
      .catch((error: unknown) => {
        if (operation.controller.signal.aborted) {
          operation.snapshot.state = "cancelled";
        } else {
          operation.snapshot.state = "failed";
          operation.snapshot.error = error instanceof Error ? error.message : String(error);
        }
      })
      .finally(() => {
        operation.snapshot.finishedAt = now();
        finish(operation);
      });
  }

  return {
    start<T>(request: AiOperationRequest<T>): AiOperationHandle<T> {
      const id = createId();
      let resolveDone!: () => void;
      const done = new Promise<void>((resolve) => {
        resolveDone = resolve;
      });
      const operation: InternalOperation<T> = {
        snapshot: {
          id,
          resourceId: request.resourceId,
          capability: request.capability,
          mode: request.mode,
          state: "queued",
          createdAt: now(),
        },
        request,
        controller: new AbortController(),
        resolveDone,
      };
      operations.set(id, operation as InternalOperation<unknown>);

      if (request.mode === "write") {
        const queue = writeQueues.get(request.resourceId) ?? [];
        queue.push(operation as InternalOperation<unknown>);
        writeQueues.set(request.resourceId, queue);
        if (queue.length === 1) run(operation as InternalOperation<unknown>);
      } else {
        run(operation as InternalOperation<unknown>);
      }

      return {
        done,
        snapshot: () => ({ ...operation.snapshot }),
        cancel: () => {
          if (["succeeded", "failed", "cancelled"].includes(operation.snapshot.state)) return;
          operation.controller.abort(new Error("Operation cancelled"));
          if (operation.snapshot.state === "queued") {
            const queue = writeQueues.get(request.resourceId);
            const index = queue?.indexOf(operation as InternalOperation<unknown>) ?? -1;
            if (queue && index >= 0) queue.splice(index, 1);
            operation.snapshot.state = "cancelled";
            operation.snapshot.finishedAt = now();
            operations.delete(id);
            resolveDone();
          }
        },
      };
    },
    list(): AiOperationSnapshot[] {
      return [...operations.values()].map((operation) => ({ ...operation.snapshot }));
    },
  };
}

export const aiOperationManager = createOperationManager();
