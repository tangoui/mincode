import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { flushPromises, mount } from "@vue/test-utils";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { AI_CONVERSATION_PREFERENCES_KEY, useAiConversationPreferencesStore } from "@/stores/ai-conversation-preferences";
import AiConversationSidebarPosition from "@/components/ai/AiConversationSidebarPosition.vue";

vi.mock("@/services/persistent-store", () => ({ loadPersistedValue: vi.fn(), savePersistedValue: vi.fn().mockResolvedValue(undefined) }));
vi.mock("@/i18n/messages", () => ({ useVueI18n: () => ({ t: (key: string) => key }) }));
beforeEach(() => { vi.mocked(loadPersistedValue).mockReset(); });

describe("AI conversation sidebar position", () => {
  it("defaults old and invalid preferences to left without changing scope or group order", async () => {
    vi.mocked(loadPersistedValue).mockResolvedValueOnce({ version: 1, scopeMode: "workspace", sidebarPosition: "top", workspaceGroupOrder: ["one"] });
    const store = useAiConversationPreferencesStore();
    await store.hydrate();
    expect(store.sidebarPosition).toBe("left");
    expect(store.scopeMode).toBe("workspace");
    expect(store.workspaceGroupOrder).toEqual(["one"]);
  });
  it("persists a header toggle, synchronizes settings, and restores the choice in a new store", async () => {
    vi.mocked(loadPersistedValue).mockResolvedValueOnce({ version: 1, scopeMode: "global", sidebarPosition: "right", workspaceGroupOrder: [] });
    const header = mount(AiConversationSidebarPosition, { props: { compact: true } });
    const settings = mount(AiConversationSidebarPosition);
    await flushPromises();
    await header.get('button').trigger('click');
    await flushPromises();
    const store = useAiConversationPreferencesStore();
    expect(store.sidebarPosition).toBe("left");
    expect(settings.findAll("input")[0]!.element).toHaveProperty("checked", true);
    expect(savePersistedValue).toHaveBeenLastCalledWith(AI_CONVERSATION_PREFERENCES_KEY, expect.objectContaining({ sidebarPosition: "left", scopeMode: "global" }));
    const saved = vi.mocked(savePersistedValue).mock.calls.at(-1)?.[1];
    header.unmount(); settings.unmount();
    setActivePinia(createPinia());
    vi.mocked(loadPersistedValue).mockResolvedValueOnce(saved);
    const restored = useAiConversationPreferencesStore();
    await restored.hydrate();
    expect(restored.sidebarPosition).toBe("left");
  });
  it("settings can select right again and the header exposes the next action", async () => {
    vi.mocked(loadPersistedValue).mockResolvedValueOnce({ sidebarPosition: "left" });
    const header = mount(AiConversationSidebarPosition, { props: { compact: true } });
    const settings = mount(AiConversationSidebarPosition);
    await flushPromises();
    expect(header.get('button').attributes('aria-label')).toBe('ai.conversation.moveListRight');
    await settings.findAll("input")[1]!.setValue(true);
    await flushPromises();
    expect(useAiConversationPreferencesStore().sidebarPosition).toBe('right');
    expect(header.get('button').attributes('aria-label')).toBe('ai.conversation.moveListLeft');
    header.unmount(); settings.unmount();
  });
});
