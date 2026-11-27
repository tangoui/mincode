import { mount } from "@vue/test-utils";
import { createPinia } from "pinia";
import { expect, it } from "vitest";
import { defineComponent } from "vue";
import { useAiAssistantStore } from "@/stores/ai-assistant";

it("inserts a tool-created session without changing the selected conversation or draft", () => {
  let store!: ReturnType<typeof useAiAssistantStore>;
  const wrapper = mount(defineComponent({
    setup() {
      store = useAiAssistantStore();
      return () => null;
    },
  }), { global: { plugins: [createPinia()] } });
  try {
    const current = store.createConversation("agentic");
    store.setDraft("Unsent message");

    const session = store.createBackgroundConversation("agent", {
      title: "Background review", cwd: "/project", agentId: "codex", fullAccess: true,
    });

    expect(store.activeConversationId).toBe(current.id);
    expect(store.draft).toBe("Unsent message");
    expect(store.open).toBe(false);
    expect(store.conversationById(session.id)).toMatchObject({
      mode: "agent", title: "Background review", projectRoot: "/project", requestedAgentId: "codex", fullAccess: true,
    });
  } finally {
    wrapper.unmount();
  }
});
