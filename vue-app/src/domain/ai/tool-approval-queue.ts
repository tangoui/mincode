import type { ToolAuthorizationRequest, ToolAuthorizationScope } from "./tool-authorization";

const MAX_APPROVAL_FEEDBACK_LENGTH = 500;

/** A granted scope approves the call; no scope rejects it, optionally with the user's correction. */
export interface ToolApprovalDecision {
  scope?: ToolAuthorizationScope;
  feedback?: string;
}

export interface ToolApprovalQueueItem {
  id: string;
  conversationId: string;
  triggerMessageId?: string;
  request: ToolAuthorizationRequest;
  summary: string;
}

interface PendingToolApprovalQueueItem extends ToolApprovalQueueItem {
  resolver: (decision: ToolApprovalDecision) => void;
}

export interface ToolApprovalTicket {
  id: string;
  decision: Promise<ToolApprovalDecision>;
}

export interface ToolApprovalQueue {
  /** An aborted signal rejects the approval (resolves `{}`) and removes its card. */
  enqueue(
    conversationId: string,
    triggerMessageId: string | undefined,
    request: ToolAuthorizationRequest,
    summary: string,
    signal?: AbortSignal
  ): ToolApprovalTicket;
  list(conversationId?: string): ToolApprovalQueueItem[];
  resolve(id: string, decision?: ToolApprovalDecision): void;
  rejectConversation(conversationId: string, feedback?: string): void;
  subscribe(listener: () => void): () => void;
  dispose(): void;
}

/** Feedback goes back to the model as a single line: trimmed, newlines flattened, capped in length. */
export function normalizeApprovalFeedback(text: unknown): string | undefined {
  if (typeof text !== "string") return undefined;
  const normalized = text.replace(/\r\n|[\r\n]/g, " ").trim().slice(0, MAX_APPROVAL_FEEDBACK_LENGTH).trimEnd();
  return normalized || undefined;
}

export function createToolApprovalQueue(): ToolApprovalQueue {
  const items: PendingToolApprovalQueueItem[] = [];
  const listeners = new Set<() => void>();
  let sequence = 0;
  let disposed = false;

  function notify() {
    for (const listener of listeners) listener();
  }

  function settle(item: PendingToolApprovalQueueItem, decision: ToolApprovalDecision = {}) {
    item.resolver(decision);
  }

  function visibleItems(conversationId?: string) {
    const visible: ToolApprovalQueueItem[] = [];
    const seenConversations = new Set<string>();
    for (const { resolver: _resolver, ...item } of items) {
      if (conversationId && item.conversationId !== conversationId) continue;
      if (seenConversations.has(item.conversationId)) continue;
      seenConversations.add(item.conversationId);
      visible.push(item);
    }
    return visible;
  }

  return {
    enqueue(conversationId, triggerMessageId, request, summary, signal) {
      sequence += 1;
      const id = `tool-approval-${sequence}`;
      if (disposed || signal?.aborted) return { id, decision: Promise.resolve({}) };
      const decision = new Promise<ToolApprovalDecision>((resolve) => {
        const onAbort = () => {
          const index = items.findIndex((item) => item.id === id);
          if (index < 0) return;
          const [item] = items.splice(index, 1);
          if (item) settle(item);
          notify();
        };
        signal?.addEventListener("abort", onAbort, { once: true });
        items.push({
          id,
          conversationId,
          triggerMessageId,
          request,
          summary,
          resolver: (outcome) => {
            signal?.removeEventListener("abort", onAbort);
            resolve(outcome);
          },
        });
        notify();
      });
      return { id, decision };
    },
    list(conversationId) {
      return visibleItems(conversationId);
    },
    // Any pending approval resolves by id: the UI shows one card per conversation,
    // but a remote (cloud) approver answers each request it received.
    resolve(id, decision) {
      const index = items.findIndex((item) => item.id === id);
      if (index < 0) return;
      const [item] = items.splice(index, 1);
      if (!item) return;
      settle(item, decision);
      notify();
    },
    rejectConversation(conversationId, feedback) {
      const rejected = items.filter((item) => item.conversationId === conversationId);
      if (!rejected.length) return;
      for (let index = items.length - 1; index >= 0; index -= 1) {
        if (items[index]?.conversationId === conversationId) items.splice(index, 1);
      }
      const text = normalizeApprovalFeedback(feedback);
      for (const item of rejected) settle(item, text ? { feedback: text } : {});
      notify();
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      const pending = items.splice(0);
      for (const item of pending) settle(item);
      notify();
      listeners.clear();
    },
  };
}
