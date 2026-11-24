import type { AiQuestion, AiQuestionAnswer } from "./ask-user";

export type AiQuestionOutcome =
  | { kind: "answered"; answers: AiQuestionAnswer[] }
  | { kind: "skipped" }
  | { kind: "cancelled" }
  | { kind: "withdrawn" };

export interface AiQuestionQueueItem {
  id: string;
  conversationId: string;
  source: "native" | "acp";
  toolCallId?: string;
  triggerMessageId?: string;
  message?: string;
  asyncDelivery?: boolean;
  autoResolutionMs?: number;
  questions: AiQuestion[];
}

export interface AiQuestionQueue {
  enqueue(item: Omit<AiQuestionQueueItem, "id">, signal?: AbortSignal): { id: string; outcome: Promise<AiQuestionOutcome> };
  list(conversationId?: string): AiQuestionQueueItem[];
  resolve(id: string, outcome: AiQuestionOutcome): void;
  cancelConversation(conversationId: string): void;
  subscribe(listener: () => void): () => void;
  dispose(): void;
}

export function createQuestionQueue(): AiQuestionQueue {
  const pending = new Map<string, { item: AiQuestionQueueItem; settle: (outcome: AiQuestionOutcome) => void }>();
  const listeners = new Set<() => void>();
  let sequence = 0;
  let disposed = false;

  function notify() {
    for (const listener of listeners) listener();
  }

  function settle(id: string, outcome: AiQuestionOutcome): boolean {
    const entry = pending.get(id);
    if (!entry) return false;
    pending.delete(id);
    entry.settle(outcome);
    return true;
  }

  function resolve(id: string, outcome: AiQuestionOutcome) {
    if (settle(id, outcome)) notify();
  }

  return {
    enqueue(item, signal) {
      const id = `ai-question-${++sequence}`;
      if (disposed || signal?.aborted) return { id, outcome: Promise.resolve({ kind: "cancelled" }) };
      const outcome = new Promise<AiQuestionOutcome>((finish) => {
        const onAbort = () => resolve(id, { kind: "cancelled" });
        signal?.addEventListener("abort", onAbort, { once: true });
        pending.set(id, {
          item: { ...item, id },
          settle(value) {
            signal?.removeEventListener("abort", onAbort);
            finish(value);
          },
        });
      });
      notify();
      return { id, outcome };
    },
    list(conversationId) {
      return [...pending.values()].map(({ item }) => item)
        .filter((item) => conversationId === undefined || item.conversationId === conversationId);
    },
    resolve,
    cancelConversation(conversationId) {
      const ids = [...pending.values()].filter(({ item }) => item.conversationId === conversationId).map(({ item }) => item.id);
      for (const id of ids) settle(id, { kind: "cancelled" });
      if (ids.length) notify();
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      const hadPending = pending.size > 0;
      for (const id of pending.keys()) settle(id, { kind: "cancelled" });
      if (hadPending) notify();
      listeners.clear();
    },
  };
}
