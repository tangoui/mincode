import { mount } from "@vue/test-utils";
import { expect, it } from "vitest";
import AiMessageThread from "@/components/ai/AiMessageThread.vue";
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

function thread(props = {}) {
  return mount(AiMessageThread, { props: {
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
