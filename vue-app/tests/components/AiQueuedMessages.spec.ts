import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import AiQueuedMessages from "@/components/ai/AiQueuedMessages.vue";

const labels = {
  title: "待发送消息",
  badge: "排队中",
  edit: "编辑",
  delete: "删除",
  sendNow: "立即发送",
  attachmentsOnly: (count: number) => `${count} 个附件`,
  copyImage: "复制图片",
  copiedImage: "已复制",
  copyImageFailed: "复制失败",
};

const queue = [
  { id: "q1", content: "顺便补上\n测试", createdAt: 1 },
  { id: "q2", content: "", createdAt: 2, attachments: [{ id: "f1", kind: "text" as const, name: "a.log", mimeType: "text/plain", size: 3, data: "" }] },
];

describe("queued messages above the composer", () => {
  it("shows one compact line per message and nothing when empty", () => {
    expect(mount(AiQueuedMessages, { props: { queue: [], labels } }).html()).not.toContain("ai-queued-messages");
    const wrapper = mount(AiQueuedMessages, { props: { queue, labels, editingId: "q2" } });
    const items = wrapper.findAll(".ai-queued-message");
    expect(items).toHaveLength(2);
    expect(items[0].find(".ai-queued-message__text").text()).toBe("顺便补上 测试");
    expect(items[1].find(".ai-queued-message__text").text()).toBe("1 个附件");
    expect(items[1].classes()).toContain("is-editing");
    expect(wrapper.find("section").attributes("aria-label")).toBe("待发送消息");
  });

  it("emits send-now, edit and delete with the message id", async () => {
    const wrapper = mount(AiQueuedMessages, { props: { queue, labels } });
    const buttons = wrapper.findAll(".ai-queued-message")[0].findAll("button");
    await buttons[0].trigger("click");
    await buttons[1].trigger("click");
    await buttons[2].trigger("click");
    expect(wrapper.emitted("send-now")?.[0]).toEqual(["q1"]);
    expect(wrapper.emitted("edit")?.[0]).toEqual(["q1"]);
    expect(wrapper.emitted("delete")?.[0]).toEqual(["q1"]);
  });
});
