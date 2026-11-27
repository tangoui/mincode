import { mount } from "@vue/test-utils";
import { createPinia } from "pinia";
import { expect, it } from "vitest";
import { defineComponent } from "vue";
import { useAiAssistantStore } from "@/stores/ai-assistant";
import { useNotificationsStore } from "@/stores/notifications";

it("reading a conversation also reads its notifications, even when it was already read", () => {
  let assistant!: ReturnType<typeof useAiAssistantStore>;
  let notifications!: ReturnType<typeof useNotificationsStore>;
  const wrapper = mount(defineComponent({
    setup() {
      assistant = useAiAssistantStore();
      notifications = useNotificationsStore();
      return () => null;
    },
  }), { global: { plugins: [createPinia()] } });
  try {
    const read = assistant.createConversation("agentic");
    const other = assistant.createConversation("agentic");
    const target = (conversationId: string) => ({ kind: "ai-conversation" as const, conversationId, workspaceScopeId: assistant.activeWorkspaceScopeId });
    const first = notifications.push({ source: "ai", title: "AI 已完成：A", target: target(read.id) });
    const second = notifications.push({ source: "ai", title: "AI 需要你确认：A", target: target(read.id) });
    const untouched = notifications.push({ source: "ai", title: "AI 已完成：B", target: target(other.id) });
    expect(assistant.conversationById(read.id)?.unread).not.toBe(true);

    assistant.setConversationUnread(read.id, false);

    const byId = (id: string) => notifications.items.find((item) => item.id === id);
    expect(byId(first.id)?.readAt).toBeTruthy();
    expect(byId(second.id)?.readAt).toBeTruthy();
    expect(byId(untouched.id)?.readAt).toBeFalsy();
  } finally {
    wrapper.unmount();
  }
});
