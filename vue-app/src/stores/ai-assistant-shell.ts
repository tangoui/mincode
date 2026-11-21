import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { AiAssistantMode, useAiAssistantStore } from "@/stores/ai-assistant";

export type AiAssistantLayoutMode = "overlay" | "docked";
export type AiAssistantPlacement = "drawer" | "tab";
export type AiAssistantStore = ReturnType<typeof useAiAssistantStore>;

export const AI_ASSISTANT_LAYOUT_MODE_KEY = "app_ai_layout_mode";
export const AI_ASSISTANT_PLACEMENT_KEY = "app_ai_assistant_placement";
export const AI_ASSISTANT_WIDTH_KEY = "app_ai_drawer_width";

function readLocalValue(key: string) {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

/** savePersistedValue stores JSON (`"docked"`); older builds wrote the raw string. */
export function readLocalStringValue(key: string): string | null {
  const raw = readLocalValue(key);
  if (raw === null) return null;
  try {
    const parsed: unknown = JSON.parse(raw);
    return typeof parsed === "string" ? parsed : raw;
  } catch {
    return raw;
  }
}

let assistantStoreFactory: (() => AiAssistantStore) | undefined;
let assistantStoreLoad: Promise<AiAssistantStore> | undefined;

/** Called by the full store while it is created, whichever importer created it first. */
export function registerAiAssistantStore(factory: () => AiAssistantStore) {
  assistantStoreFactory = factory;
}

/** Returns the conversation store only when something already created it. */
export function peekAiAssistantStore(): AiAssistantStore | undefined {
  return assistantStoreFactory?.();
}

/** Loads the conversation store chunk on first use; the shell must not import it statically. */
export function loadAiAssistantStore(): Promise<AiAssistantStore> {
  if (assistantStoreFactory) return Promise.resolve(assistantStoreFactory());
  assistantStoreLoad ??= import("@/stores/ai-assistant")
    .then((module) => module.useAiAssistantStore())
    .catch((error: unknown) => {
      assistantStoreLoad = undefined;
      throw error;
    });
  return assistantStoreLoad;
}

// Startup shell state for the AI entry points. Conversations, persistence and
// agent progress live in the lazily loaded `ai-assistant` store, which shares
// these refs so both stores always observe the same values.
export const useAiAssistantShellStore = defineStore("ai-assistant-shell", () => {
  const open = ref(false);
  const fullscreen = ref(false);
  const layoutMode = ref<AiAssistantLayoutMode>(
    readLocalStringValue(AI_ASSISTANT_LAYOUT_MODE_KEY) === "docked" ? "docked" : "overlay",
  );
  const placement = ref<AiAssistantPlacement>(
    // 默认以顶部标签页打开；只有用户明确选过抽屉才用抽屉。
    readLocalStringValue(AI_ASSISTANT_PLACEMENT_KEY) === "drawer" ? "drawer" : "tab",
  );
  const width = ref(Number(readLocalValue(AI_ASSISTANT_WIDTH_KEY)) || 420);

  // Explicit return types keep this store's type independent of the full store.
  async function show(nextMode?: AiAssistantMode): Promise<void> {
    (await loadAiAssistantStore()).show(nextMode);
  }

  async function toggle(): Promise<void> {
    (await loadAiAssistantStore()).toggle();
  }

  function close() {
    open.value = false;
    fullscreen.value = false;
  }

  return { open, fullscreen, layoutMode, placement, width, show, toggle, close };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useAiAssistantShellStore, import.meta.hot));
}
