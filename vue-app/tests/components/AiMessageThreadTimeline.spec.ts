import { mount } from "@vue/test-utils";
import { expect, it, vi } from "vitest";
import { nextTick } from "vue";
import AiMessageThread from "@/components/ai/AiMessageThread.vue";
import type { AiTimelineDelegationChild } from "@/domain/ai/timeline-items";
import { AI_MESSAGE_FIRST_PAINT_SIZE } from "@/domain/ai/message-render-window";
import type { AiAssistantEvent, AiAssistantMessage } from "@/stores/ai-assistant";

const message: AiAssistantMessage = { id: "m1", role: "assistant", content: "Checking the workspace" };
const event = (id: string, toolName = "workspace.run_command", overrides: Partial<AiAssistantEvent> = {}): AiAssistantEvent => ({
  id, kind: "tool", title: toolName, toolName, detail: `result ${id}`,
  status: "completed", triggerMessageId: "m1", ...overrides,
});
const activity = new Proxy({}, { get: (_target, key) => String(key) });
const approvalLabels = {
  title: "Approve", tool: "Tool", resource: "Resource", summary: "Summary", parameters: "Parameters",
  risk: { read: "Read", network: "Network", write: "Write", dangerous: "Dangerous" },
  allowOnce: "Once", allowSession: "Session", allowSessionSameParameters: "Session, same call", allowGlobal: "Always",
  reject: "Reject", feedbackPlaceholder: "Reason", rejectWithFeedback: "Reject with note",
};

function thread(props = {}, attachTo?: HTMLElement) {
  return mount(AiMessageThread, { attachTo, props: {
    messages: [message], events: [], mode: "agentic", title: "", hint: "", running: false,
    permission: null, composerHasDraft: false, loadEarlierLabel: "Load earlier",
    activityLabels: activity, eventStatusLabels: activity, taskStepStatusLabels: activity,
    taskStepsLabel: "Steps", dismissTaskStepsLabel: "Dismiss steps", sourcesLabel: "Sources", artifactsLabel: "Artifacts",
    previewLabel: "Preview", collapseLabel: "Collapse", copyLabel: "Copy", copiedLabel: "Copied",
    copyResultLabel: "Copy result", copiedResultLabel: "Copied result", copyResultFailedLabel: "Copy failed",
    emptyMessageLabel: "Empty", metadataLabel: "Metadata", expandMessageLabel: "Expand", collapseMessageLabel: "Collapse",
    copiedMessageLabel: "Copied", copyMessageFailedLabel: "Copy failed", copyImageLabel: "Copy image",
    copiedImageLabel: "Copied image", copyImageFailedLabel: "Image copy failed",
    copyMessageLabel: "Copy reply", approvalLabels, ...props,
  } as never });
}

it("keeps the pinned prompt outside the scroll viewport and restores it on return", async () => {
  const stage = document.createElement("div");
  const header = document.createElement("div");
  const viewport = document.createElement("div");
  stage.append(header, viewport);
  stage.className = "ai-assistant-messages";
  viewport.className = "el-scrollbar__wrap";
  document.body.append(stage);
  const user: AiAssistantMessage = { id: "prompt", role: "user", content: "Check the workspace" };
  const wrapper = thread({ messages: [user, message], events: [event("thought", undefined, { kind: "thought" })], pinnedUserTarget: header }, viewport);
  try {
    const prompt = wrapper.get(".ai-message-bubble.is-user").element;
    const marker = wrapper.get('[data-ai-message-id="prompt"]').element;
    const body = wrapper.get<HTMLElement>(".ai-message-card__content").element;
    Object.defineProperty(body, "scrollHeight", { value: 24 });
    vi.spyOn(prompt, "getBoundingClientRect").mockImplementation(() => new DOMRect(0, 0, 120, 40));
    vi.spyOn(header, "getBoundingClientRect").mockImplementation(() => new DOMRect(0, 0, 300, header.contains(prompt) ? 40 : 0));
    vi.spyOn(marker, "getBoundingClientRect").mockImplementation(() => new DOMRect(0, (header.contains(prompt) ? 40 : 0) + 16 - viewport.scrollTop, 300, 40));
    await nextTick();
    expect(viewport.contains(prompt)).toBe(true);

    viewport.scrollTop = 25;
    viewport.dispatchEvent(new Event("scroll"));
    await nextTick();
    await nextTick();
    expect(header.contains(prompt)).toBe(true);
    expect(viewport.scrollTop).toBe(65);
    expect(viewport.contains(wrapper.get(".ai-timeline-thought button").element)).toBe(true);
    await wrapper.get(".ai-timeline-thought button").trigger("click");
    expect(wrapper.get(".ai-timeline-thought button").attributes("aria-expanded")).toBe("true");

    viewport.scrollTop = 45;
    viewport.dispatchEvent(new Event("scroll"));
    await nextTick();
    await nextTick();
    expect(viewport.contains(prompt)).toBe(true);
    expect(viewport.scrollTop).toBe(5);
  } finally {
    wrapper.unmount();
    stage.remove();
  }
});

it("places activity, text and native approval in one ordered timeline", async () => {
  const wrapper = thread({ events: [
    event("r1", "workspace.read_file"), event("r2", "workspace.read_file"), event("r3", "workspace.read_file"),
    event("loop-tool-shell"), event("edit", "workspace.write_file"), event("failed", undefined, { status: "failed" }),
    event("plan", undefined, { kind: "plan", detail: '{"steps":[{"title":"Run checks","status":"completed"}]}' }),
    event("thought", undefined, { kind: "thought" }), event("mode", undefined, { kind: "mode", detail: "Code" }),
  ], approvals: [{ id: "approval-1", conversationId: "c1", triggerMessageId: "m1", summary: "Run command",
    request: { toolCallId: "shell", toolName: "workspace.run_command", risk: "dangerous", resourceId: "shell", parameters: {} },
  }] });
  const rows = wrapper.findAll(".ai-message-bubble, .ai-timeline-summary, .ai-timeline-tool, .ai-timeline-approval, .ai-timeline-plan, .ai-timeline-thought, .ai-timeline-notice");
  expect(rows.map((row) => row.classes()[0])).toEqual([
    "ai-message-bubble", "ai-timeline-summary", "ai-timeline-tool", "ai-timeline-approval", "ai-timeline-tool",
    "ai-timeline-tool", "ai-timeline-plan", "ai-timeline-thought", "ai-timeline-notice",
  ]);
  expect(wrapper.findAll(".el-card")).toHaveLength(0);
  await wrapper.get('[data-approval-scope="once"]').trigger("click");
  expect(wrapper.emitted("decide-approval")).toEqual([["approval-1", { scope: "once" }]]);
  wrapper.unmount();
});

it("keeps retry and settings accessible on an inline error", async () => {
  const error: AiAssistantMessage = { ...message, presentation: "error", content: "Provider unavailable" };
  const wrapper = thread({ messages: [error], canRewind: true,
    errorLabels: { title: "Failed", hint: "Try again", retry: "Retry", settings: "Settings" },
  });
  const row = wrapper.get(".ai-timeline-error[role=alert]");
  expect(row.text()).toContain("Provider unavailable");
  const [retry, settings] = row.findAll("button");
  await retry?.trigger("click");
  await settings?.trigger("click");
  expect(wrapper.emitted("retry")).toEqual([[error]]);
  expect(wrapper.emitted("open-settings")).toEqual([[]]);
  wrapper.unmount();
});

it("answers an ACP permission inside the thread", async () => {
  const wrapper = thread({ permission: { requestId: "p1", serverId: "s1", sessionId: "c1",
    options: [{ optionId: "yes", name: "Allow", kind: "allow_once" }], toolCall: { title: "Read config" },
  } });
  expect(wrapper.get(".ai-timeline-approval").text()).toContain("Read config");
  await wrapper.get('[data-option-id="yes"]').trigger("click");
  expect(wrapper.emitted("answer-permission")).toEqual([["yes"]]);
  wrapper.unmount();
});

it("shows the question title and waiting status on a pending native tool row", () => {
  const wrapper = thread({
    events: [event("loop-tool-ask", "ask_user", { status: "running", title: "向你提问：Choice" })],
    questions: [{ id: "q1", conversationId: "c1", source: "native", toolCallId: "ask", questions: [] }],
  });
  try {
    expect(wrapper.get(".ai-timeline-tool .ai-timeline__title code").text()).toBe("向你提问：Choice");
    expect(wrapper.get(".ai-timeline-tool [role=status]").text()).toMatch(/等待你回答|Waiting for your answer/);
  } finally {
    wrapper.unmount();
  }
});

it("keeps time, token usage and copy on the plain task-summary reply", () => {
  const wrapper = thread({ messages: [{ ...message, presentation: "task-summary", createdAt: 1795500000000,
    tokenUsage: { inputTokens: 8, outputTokens: 3, cachedInputTokens: 0, cacheWriteTokens: 0,
      reasoningTokens: 0, requestCount: 1, attemptedRequestCount: 1, models: [] },
  }] });
  expect(wrapper.get(".ai-message-bubble").text()).toContain(message.content);
  expect(wrapper.get(".ai-message-bubble__token-usage").text()).toContain("11");
  expect(wrapper.find(".ai-message-bubble__footer time").exists()).toBe(true);
  expect(wrapper.find('button[aria-label="Copy reply"]').exists()).toBe(true);
  expect(wrapper.findAll(".el-card")).toHaveLength(0);
  wrapper.unmount();
});

it("bounds the first paint of a 300-call turn and loads older rows on demand", async () => {
  const wrapper = thread({ events: Array.from({ length: 300 }, (_, index) => event(String(index))) });
  const count = () => wrapper.findAll(".ai-timeline-tool").length;
  expect(count()).toBeGreaterThan(0);
  expect(count()).toBeLessThanOrEqual(AI_MESSAGE_FIRST_PAINT_SIZE);
  const before = count();
  await wrapper.get(".ai-message-thread__load-earlier").trigger("click");
  expect(count()).toBeGreaterThan(before);
  expect(count()).toBeLessThan(300);
  wrapper.unmount();
});

it("keeps a pending approval and its call visible outside the latest activity window", () => {
  const wrapper = thread({ events: Array.from({ length: 300 }, (_, index) => event(`loop-tool-${index}`)),
    approvals: [{ id: "approval-1", conversationId: "c1", triggerMessageId: "m1", summary: "Run command",
      request: { toolCallId: "0", toolName: "workspace.run_command", risk: "dangerous", resourceId: "shell", parameters: {} },
    }] });
  expect(wrapper.get(".ai-timeline-approval").element.previousElementSibling?.classList.contains("ai-timeline-tool")).toBe(true);
  expect(wrapper.findAll(".ai-timeline-tool").length).toBeLessThanOrEqual(AI_MESSAGE_FIRST_PAINT_SIZE + 1);
  wrapper.unmount();
});

it("puts turn metadata only on the final assistant message, including when it is an error", () => {
  const usage = { inputTokens: 8, outputTokens: 3, cachedInputTokens: 0, cacheWriteTokens: 0,
    reasoningTokens: 0, requestCount: 1, attemptedRequestCount: 1 };
  const wrapper = thread({
    messages: [message, { ...message, id: "first-final", content: "Finished the first turn", tokenUsage: usage },
      { id: "prompt", role: "user", content: "Run again" },
      { ...message, id: "second-start", tokenUsage: usage },
      { ...message, id: "second-error", presentation: "error", content: "Provider unavailable", tokenUsage: usage }],
    errorLabels: { title: "Failed", hint: "Try again", retry: "Retry", settings: "Settings" },
  });
  const replies = wrapper.findAll(".ai-message-bubble.is-assistant");
  expect(replies.map(reply => reply.find(".ai-message-bubble__footer").exists())).toEqual([false, true, false, true]);
  expect(replies[3]?.get(".ai-message-bubble__token-usage").text()).toContain("22");
  expect(replies[3]?.get(".ai-timeline-error").text()).toContain("Provider unavailable");
  expect(wrapper.get(".ai-message-bubble.is-user").find(".ai-message-bubble__footer").exists()).toBe(true);
  wrapper.unmount();
});

it("shows delegation progress, collapses on finish, and keeps manually opened reports visible", async () => {
  const messages = [{ id: "request", role: "user", content: "Delegate" }, message];
  const dispatch = event("loop-tool-call", "agent.run_parallel");
  const children: [AiTimelineDelegationChild, AiTimelineDelegationChild] = [
    { id: "a", title: "Read tests", selectedModel: "model-a", status: "completed", createdAt: 10,
      delegation: { parentId: "parent", source: "parallel", toolCallId: "call", turnMessageId: "request", result: "**Tests reviewed**" } },
    { id: "b", title: "Read docs", status: "in_progress", createdAt: 11,
      delegation: { parentId: "parent", source: "parallel", toolCallId: "call", turnMessageId: "request" } },
  ];
  const done: AiTimelineDelegationChild[] = [children[0], { ...children[1], status: "failed" }];
  const wrapper = thread({ messages, events: [dispatch], delegatedChildren: children,
    delegationModelLabels: { "model-a": "Demo model" }, delegationStatusLabels: activity });
  const group = () => wrapper.get(".ai-timeline-delegation");
  const header = () => group().get("button.ai-timeline__row");
  expect(group().element.previousElementSibling?.classList.contains("ai-timeline-tool")).toBe(true);
  expect(group().text()).toContain("1/2");
  expect(group().text()).toContain("Demo model");
  await wrapper.setProps({ delegatedChildren: done });
  expect(group().text()).toContain("1 个失败");
  expect(group().text()).not.toContain("Read tests");
  await header().trigger("click");
  await group().get("summary").trigger("click");
  expect(group().text()).toContain("Tests reviewed");
  await group().get('button[aria-label="打开子会话"]').trigger("click");
  expect(wrapper.emitted("open-delegation")).toEqual([["a"]]);
  await wrapper.setProps({ delegatedChildren: children });
  await wrapper.setProps({ delegatedChildren: done });
  expect(group().text()).toContain("Tests reviewed");
  wrapper.unmount();
});
