import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import AiAgentEventGroup from "@/components/ai/AiAgentEventGroup.vue";
import type { AiAssistantEvent } from "@/stores/ai-assistant";

// Every label is a plain string; the count labels keep their {count} placeholder.
const text = new Proxy({}, { get: (_target, key) => String(key) });
const labels = new Proxy({
  activity: text,
  eventStatuses: text,
  taskStepStatuses: text,
  eventCounts: { succeeded: "{count} ok", failed: "{count} failed", running: "{count} running" },
}, { get: (target, key) => (key in target ? target[key as keyof typeof target] : String(key)) });

const tool = (status: string): AiAssistantEvent => ({ id: "loop-tool-1", kind: "tool", title: "读取", detail: "a.go", status, triggerMessageId: "m1" });

function mountGroup(events: AiAssistantEvent[]) {
  return mount(AiAgentEventGroup, { props: { groupKey: "activity-1", title: "活动", events, labels: labels as never } });
}
const expanded = (wrapper: ReturnType<typeof mountGroup>) => wrapper.find(".el-collapse-item").classes().includes("is-active");

describe("activity group as a step line", () => {
  it("stays expanded while steps run and folds once they all succeed", async () => {
    const wrapper = mountGroup([tool("running")]);
    expect(expanded(wrapper)).toBe(true);
    await wrapper.setProps({ events: [tool("completed")] });
    expect(expanded(wrapper)).toBe(false);
  });

  it("keeps a group with a failed step open", async () => {
    const wrapper = mountGroup([tool("running")]);
    await wrapper.setProps({ events: [tool("failed")] });
    expect(expanded(wrapper)).toBe(true);
  });

  it("opens finished history folded", () => {
    expect(expanded(mountGroup([tool("completed")]))).toBe(false);
  });

  it("keeps the reader's own choice once they toggled it", async () => {
    const wrapper = mountGroup([tool("running")]);
    await wrapper.find(".el-collapse-item__header").trigger("click");
    expect(expanded(wrapper)).toBe(false);
    await wrapper.find(".el-collapse-item__header").trigger("click");
    expect(expanded(wrapper)).toBe(true);
    await wrapper.setProps({ events: [tool("completed")] });
    expect(expanded(wrapper)).toBe(true);
  });
});

describe("steps waiting for approval", () => {
  it("label the tool step that the pending approval card below belongs to", () => {
    const wrapper = mount(AiAgentEventGroup, {
      props: { groupKey: "g", title: "活动", events: [tool("running")], awaitingApprovalIds: ["loop-tool-1"], labels: labels as never },
    });
    const tag = wrapper.find(".ai-agent-event-group__item .el-tag");
    expect(tag.text()).toBe("awaiting_approval");
    expect(tag.classes()).toContain("el-tag--warning");
  });
});
