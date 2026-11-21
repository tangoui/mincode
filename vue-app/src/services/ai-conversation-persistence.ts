import { createSerializedTaskQueue } from "@/domain/async/serialized-task-queue";
import { createConversationItemSaver } from "@/domain/ai/conversation-item-sync";
import { applyPersistedBatch } from "@/services/persistent-store";
import { canInvoke, invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

const enqueueAiPersistence = createSerializedTaskQueue();
const RETIRED_AI_STORAGE_PREFIXES = ["app_ai_conversations_v2", "app_ai_session_v1"] as const;

export function isRetiredAiHistoryStorageKey(key: string) {
  return RETIRED_AI_STORAGE_PREFIXES.some(
    (prefix) => key === prefix || key.startsWith(`${prefix}__`),
  );
}

export function loadAiWorkspaceOverview<T>(scopeId: string): Promise<T | null> {
  if (!canInvoke("ai_store_load_workspace")) return Promise.resolve(null);
  return enqueueAiPersistence(() => invokeBackend<T | null>("ai_store_load_workspace", { scopeId }));
}

export interface AiWorkspaceOverview<T> {
  scopeId: string;
  collection: T;
}

export function listAiWorkspaceOverviews<T>(): Promise<Array<AiWorkspaceOverview<T>>> {
  if (!canInvoke("ai_store_list_workspaces")) return Promise.resolve([]);
  return enqueueAiPersistence(() =>
    invokeBackend<Array<AiWorkspaceOverview<T>>>("ai_store_list_workspaces")
  );
}

export function loadAiConversationDetail<T>(scopeId: string, conversationId: string): Promise<T | null> {
  if (!canInvoke("ai_store_load_conversation")) return Promise.resolve(null);
  return enqueueAiPersistence(() => invokeBackend<T | null>("ai_store_load_conversation", {
    scopeId,
    conversationId,
  }));
}

// Detail saves send only the items that changed since the last save (see
// createConversationItemSaver); metadata-only saves never touch items.
const conversationItemSaver = createConversationItemSaver((args) =>
  invokeBackend<{ itemRevision?: number } | null>("ai_store_save_conversation", { ...args, replaceItems: true }));

export function saveAiConversationDetail<T extends { id: string }>(
  scopeId: string,
  conversation: T,
  position: number,
  replaceItems: boolean,
): Promise<void> {
  if (!canInvoke("ai_store_save_conversation")) return Promise.resolve();
  return enqueueAiPersistence(async () => {
    if (!replaceItems) {
      await invokeBackend<unknown>("ai_store_save_conversation", { scopeId, conversation, position, replaceItems });
      return;
    }
    await conversationItemSaver.save(scopeId, position, conversation as unknown as Record<string, unknown> & { id: string });
  });
}

export interface AiConversationLocation {
  scopeId: string;
  conversationId: string;
}

export function archiveAiConversations(conversations: AiConversationLocation[], archived: boolean): Promise<void> {
  if (!canInvoke("ai_store_save_workspace_meta")) return Promise.resolve();
  return enqueueAiPersistence(() => invokeBackend<void>("ai_store_save_workspace_meta", { archive: { conversations, archived } }));
}

export function deleteAiConversation(
  scopeId: string,
  conversationId: string,
  activeConversationId: string,
  conversationIds: string[],
  relatedConversations: AiConversationLocation[] = [],
): Promise<void> {
  if (!canInvoke("ai_store_delete_conversation")) return Promise.resolve();
  conversationItemSaver.forget(conversationId);
  for (const related of relatedConversations) conversationItemSaver.forget(related.conversationId);
  return enqueueAiPersistence(() => invokeBackend<void>("ai_store_delete_conversation", {
    scopeId,
    conversationId,
    activeConversationId,
    conversationIds,
    relatedConversations,
  }));
}

export function moveAiConversation<T>(input: {
  sourceScopeId: string;
  targetScopeId: string;
  conversationId: string;
  conversation: T;
  sourceActiveConversationId: string;
  sourceConversationIds: string[];
  targetConversationIds: string[];
}): Promise<void> {
  if (!canInvoke("ai_store_move_conversation")) return Promise.resolve();
  conversationItemSaver.forget(input.conversationId);
  return enqueueAiPersistence(() => invokeBackend<void>("ai_store_move_conversation", input));
}

export function replaceAiConversation<T>(
  scopeId: string,
  conversationId: string,
  conversation: T,
  relatedConversations: AiConversationLocation[] = [],
): Promise<void> {
  if (!canInvoke("ai_store_replace_conversation")) return Promise.resolve();
  conversationItemSaver.forget(conversationId);
  for (const related of relatedConversations) conversationItemSaver.forget(related.conversationId);
  return enqueueAiPersistence(() => invokeBackend<void>("ai_store_replace_conversation", {
    scopeId,
    conversationId,
    conversation,
    relatedConversations,
  }));
}

export interface AiMessageSearchMatch {
  scopeId: string;
  conversationId: string;
  title: string;
  messageId: string;
  role: string;
  snippet: string;
  createdAt?: number;
}

/**
 * Searches saved message text (case-insensitive). Queued behind pending saves so
 * recent messages are included. Empty scopeIds searches every workspace.
 */
export function searchAiConversationMessages(query: string, scopeIds: string[] = []): Promise<AiMessageSearchMatch[]> {
  if (!canInvoke("ai_store_search_messages")) return Promise.resolve([]);
  return enqueueAiPersistence(() => invokeBackend<AiMessageSearchMatch[]>("ai_store_search_messages", { query, scopeIds }));
}

export function saveAiWorkspaceConversationMeta(
  scopeId: string,
  activeConversationId: string,
  conversationIds: string[],
): Promise<void> {
  if (!canInvoke("ai_store_save_workspace_meta")) return Promise.resolve();
  return enqueueAiPersistence(() => invokeBackend<void>("ai_store_save_workspace_meta", {
    scopeId,
    activeConversationId,
    conversationIds,
  }));
}

/**
 * Browser-local mode keeps history in IndexedDB (services/browser-runtime/ai-store), which the
 * native batch does not reach: delete every stored conversation through the store commands.
 */
async function clearBrowserAiConversationHistory() {
  if (isDesktopRuntime() || !canInvoke("ai_store_list_workspaces") || !canInvoke("ai_store_delete_conversation")) return;
  const workspaces = await invokeBackend<Array<AiWorkspaceOverview<{ conversations?: Array<{ id?: unknown }> }>>>(
    "ai_store_list_workspaces",
  );
  for (const { scopeId, collection } of workspaces) {
    for (const { id } of collection.conversations ?? []) {
      if (typeof id !== "string" || !id) continue;
      conversationItemSaver.forget(id);
      await invokeBackend<void>("ai_store_delete_conversation", {
        scopeId,
        conversationId: id,
        activeConversationId: "",
        conversationIds: [],
      });
    }
  }
}

/**
 * Browser-local mode: permanently deleting a workspace must also remove its AI history from IndexedDB,
 * which the native `delete_ai_scope` batch (persistent-store) never reaches in the browser. The browser
 * `store_apply_batch` handler accepts exactly this operation. Desktop: no-op (the native batch does it).
 */
export function deleteBrowserAiScopeHistory(scopeId: string): Promise<void> {
  if (isDesktopRuntime() || !canInvoke("store_apply_batch")) return Promise.resolve();
  return enqueueAiPersistence(() => invokeBackend<void>("store_apply_batch", {
    operations: [{ type: "delete_ai_scope", key: scopeId }],
  }));
}

/** Clears current SQLite history and removes retired browser snapshots without reading them. */
export function clearAllAiConversationHistory(): Promise<void> {
  return enqueueAiPersistence(async () => {
    await applyPersistedBatch({ deleteAllAiHistory: true });
    await clearBrowserAiConversationHistory();
    try {
      const keys: string[] = [];
      for (let index = 0; index < window.localStorage.length; index += 1) {
        const key = window.localStorage.key(index);
        if (key && isRetiredAiHistoryStorageKey(key)) keys.push(key);
      }
      for (const key of keys) window.localStorage.removeItem(key);
    } catch {
      // Browser storage is optional; SQLite remains authoritative.
    }
  });
}
