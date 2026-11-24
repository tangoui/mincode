import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import AiMessageThread from "@/components/ai/AiMessageThread.vue";
import type { AiAssistantMessage } from "@/stores/ai-assistant";

function conversation(turns: number): AiAssistantMessage[] {
  return Array.from({ length: turns }, (_, turn) => [
    { id: `u${turn}`, role: "user", content: `Question ${turn}` } as AiAssistantMessage,
    { id: `a${turn}`, role: "assistant", content: `Answer ${turn}` } as AiAssistantMessage,
  ]).flat();
}

function mountThread(messages: AiAssistantMessage[]) {
  return mount(AiMessageThread, {
    props: { messages, events: [], mode: "agentic", title: "", hint: "", running: false, permission: null, composerHasDraft: false } as never,
    global: { stubs: { AiMessageBubble: { props: ["message"], template: "<div class='bubble' :data-id='message.id' />" } } },
  });
}

type ThreadApi = { fillRenderWindow(): boolean; revealMessage(id: string): boolean };

describe("AI thread render window on mount", () => {
  it("renders only the newest messages until the drawer asks for the rest", async () => {
    const wrapper = mountThread(conversation(100));
    // A switch to a long conversation renders one cheap first frame: its newest 16 messages.
    expect(wrapper.findAll(".bubble")).toHaveLength(16);
    expect(wrapper.findAll(".bubble").at(-1)?.attributes("data-id")).toBe("a99");
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(wrapper.findAll(".bubble")).toHaveLength(16);

    // Scrolling up towards the top fills the regular window of 80 in one step, reported like
    // "load earlier" so the drawer keeps the reading position.
    expect((wrapper.vm as unknown as ThreadApi).fillRenderWindow()).toBe(true);
    await wrapper.vm.$nextTick();
    expect(wrapper.findAll(".bubble")).toHaveLength(80);
    expect(wrapper.emitted("load-earlier")).toEqual([[64]]);
    // Filling again does nothing; older history stays behind "load earlier".
    expect((wrapper.vm as unknown as ThreadApi).fillRenderWindow()).toBe(false);
    expect(wrapper.find(".ai-message-thread__load-earlier").exists()).toBe(true);
    wrapper.unmount();
  });

  it("renders short conversations whole", () => {
    const wrapper = mountThread(conversation(5));
    expect(wrapper.findAll(".bubble")).toHaveLength(10);
    expect((wrapper.vm as unknown as ThreadApi).fillRenderWindow()).toBe(false);
    expect(wrapper.emitted("load-earlier")).toBeUndefined();
    wrapper.unmount();
  });

  it("still reveals an older message beyond the first paint", async () => {
    const wrapper = mountThread(conversation(100));
    expect((wrapper.vm as unknown as ThreadApi).revealMessage("u60")).toBe(true);
    await wrapper.vm.$nextTick();
    expect(wrapper.find("[data-id='u60']").exists()).toBe(true);
    wrapper.unmount();
  });
});
