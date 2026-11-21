import { createPinia, setActivePinia } from "pinia";
import { describe, expect, it } from "vitest";
import { useAiAssistantStore } from "@/stores/ai-assistant";

describe("delegated conversations", () => {
  it("keeps the parent selected and associates the child's result with that parent", async () => {
    setActivePinia(createPinia());
    const store = useAiAssistantStore();
    const parent = store.createConversation("agentic");
    const child = store.createDelegatedConversation(parent.id, "Review configuration", "manual");
    expect(child).toBeDefined();
    expect(store.activeConversationId).toBe(parent.id);
    expect(store.conversations.map((item) => item.id)).toContain(child!.id);
    expect(child!.projectRoot).toBe(parent.projectRoot);
    store.setConversationStatus(child!.id, "completed");
    store.recordDelegatedConversationResult(child!.id, "Verify the timeout");
    expect(store.conversationById(child!.id)?.delegation).toEqual({
      parentId: parent.id,
      source: "manual",
      result: "Verify the timeout",
      resultStatus: "completed",
    });
    expect(store.delegatedConversationReports(parent.id)).toEqual([
      { title: "Review configuration", report: "Verify the timeout" },
    ]);
    expect(store.delegatedConversationReports(child!.id)).toEqual([]);
    expect(await store.switchConversation(child!.id)).toBe(true);
    expect(store.activeConversation?.delegation?.parentId).toBe(parent.id);
  });
});
