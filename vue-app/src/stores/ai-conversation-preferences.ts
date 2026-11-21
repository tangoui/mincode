import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { appendWorkspaceGroupKeys, normalizeWorkspaceGroupOrder } from "@/domain/ai/workspace-group-order";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

export type AiConversationSidebarPosition = "left" | "right";

export type AiConversationScopeMode = "workspace" | "global";

interface AiConversationPreferencesSnapshot {
  version: 1;
  scopeMode: AiConversationScopeMode;
  sidebarPosition: AiConversationSidebarPosition;
  activeScopeId?: string;
  workspaceGroupOrder: string[];
}

export const AI_CONVERSATION_PREFERENCES_KEY = "app_ai_conversation_preferences_v1";

function normalizeSnapshot(value: unknown): AiConversationPreferencesSnapshot {
  const record = typeof value === "object" && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
  return {
    version: 1,
    // 默认左侧；只有用户明确选过右侧才放右侧。
    sidebarPosition: record.sidebarPosition === "right" ? "right" : "left",
    scopeMode: record.scopeMode === "workspace" ? "workspace" : "global",
    activeScopeId: typeof record.activeScopeId === "string" && record.activeScopeId.trim()
      ? record.activeScopeId.trim()
      : undefined,
    workspaceGroupOrder: normalizeWorkspaceGroupOrder(record.workspaceGroupOrder),
  };
}

export const useAiConversationPreferencesStore = defineStore(
  "ai-conversation-preferences",
  () => {
    const snapshot = ref<AiConversationPreferencesSnapshot>(normalizeSnapshot(undefined));
    const hydrated = ref(false);
    let hydrationPromise: Promise<void> | undefined;

    const scopeMode = computed(() => snapshot.value.scopeMode);
    // Like presentation placement, this is an intentional app-wide UI preference.
    const sidebarPosition = computed(() => snapshot.value.sidebarPosition);
    const globalMode = computed(() => scopeMode.value === "global");
    const activeScopeId = computed(() => snapshot.value.activeScopeId);
    const workspaceGroupOrder = computed(() => snapshot.value.workspaceGroupOrder);

    async function hydrate() {
      if (hydrated.value) return;
      if (hydrationPromise) return hydrationPromise;
      hydrationPromise = (async () => {
        snapshot.value = normalizeSnapshot(
          await loadPersistedValue<unknown>(AI_CONVERSATION_PREFERENCES_KEY),
        );
        hydrated.value = true;
      })();
      try {
        await hydrationPromise;
      } finally {
        hydrationPromise = undefined;
      }
    }

    async function saveSnapshot(value: unknown) {
      snapshot.value = normalizeSnapshot(value);
      if (!hydrated.value) hydrated.value = true;
      await savePersistedValue(AI_CONVERSATION_PREFERENCES_KEY, snapshot.value);
    }

    async function setScopeMode(scopeMode: AiConversationScopeMode) {
      await hydrate();
      await saveSnapshot({ ...snapshot.value, version: 1, scopeMode });
    }

    async function setSidebarPosition(sidebarPosition: AiConversationSidebarPosition) {
      await hydrate();
      await saveSnapshot({ ...snapshot.value, sidebarPosition });
    }

    async function setActiveScopeId(activeScopeId: string) {
      await hydrate();
      const normalized = activeScopeId.trim();
      if (!normalized || snapshot.value.activeScopeId === normalized) return;
      await saveSnapshot({ ...snapshot.value, activeScopeId: normalized });
    }

    async function ensureWorkspaceGroupOrder(keys: readonly string[]) {
      await hydrate();
      const nextOrder = appendWorkspaceGroupKeys(snapshot.value.workspaceGroupOrder, keys);
      if (nextOrder.length === snapshot.value.workspaceGroupOrder.length) return;
      await saveSnapshot({ ...snapshot.value, workspaceGroupOrder: nextOrder });
    }

    async function setWorkspaceGroupOrder(keys: readonly string[]) {
      await hydrate();
      const nextOrder = normalizeWorkspaceGroupOrder(keys);
      if (
        nextOrder.length === snapshot.value.workspaceGroupOrder.length
        && nextOrder.every((key, index) => key === snapshot.value.workspaceGroupOrder[index])
      ) return;
      await saveSnapshot({ ...snapshot.value, workspaceGroupOrder: nextOrder });
    }

    return {
      hydrated,
      scopeMode,
      globalMode,
      sidebarPosition,
      activeScopeId,
      workspaceGroupOrder,
      hydrate,
      setScopeMode,
      setSidebarPosition,
      setActiveScopeId,
      ensureWorkspaceGroupOrder,
      setWorkspaceGroupOrder,
    };
  },
);

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useAiConversationPreferencesStore, import.meta.hot));
}
