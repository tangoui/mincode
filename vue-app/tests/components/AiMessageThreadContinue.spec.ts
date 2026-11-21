import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import AiMessageThread from "@/components/ai/AiMessageThread.vue";
import type { AiAssistantMessage } from "@/stores/ai-assistant";

const loopLimitMessage: AiAssistantMessage = {
  id: "m2",
  role: "assistant",
  content: "Stopped at the step limit (3 steps).\n\nDid A; B remains",
  presentation: "loop-limit",
};

function mountThread(messages: AiAssistantMessage[], canContinue: boolean) {
  return mount(AiMessageThread, {
    props: {
      messages,
      events: [],
      mode: "agentic",
      title: "",
      hint: "",
      running: false,
      canContinue,
      continueLabel: "Continue",
    } as never,
    shallow: false,
    global: { stubs: { AiMessageBubble: { template: "<div class='bubble' />" } } },
  });
}

function continueButton(wrapper: ReturnType<typeof mountThread>) {
  return wrapper.findAll("button").find((button) => button.text() === "Continue");
}

describe("step-limit continue action", () => {
  it("offers continue on the latest step-limit message and emits it", async () => {
    const wrapper = mountThread([{ id: "m1", role: "user", content: "do it" }, loopLimitMessage], true);
    const button = continueButton(wrapper);
    expect(button).toBeTruthy();
    expect(button!.attributes("disabled")).toBeUndefined();
    await button!.trigger("click");
    expect(wrapper.emitted("continue")).toHaveLength(1);
  });

  it("is disabled while the conversation cannot send", () => {
    const wrapper = mountThread([{ id: "m1", role: "user", content: "do it" }, loopLimitMessage], false);
    expect(continueButton(wrapper)!.attributes("disabled")).toBeDefined();
  });

  it("disappears once the conversation has moved on", () => {
    const wrapper = mountThread(
      [{ id: "m1", role: "user", content: "do it" }, loopLimitMessage, { id: "m3", role: "user", content: "Continue" }],
      true,
    );
    expect(continueButton(wrapper)).toBeUndefined();
  });
});
