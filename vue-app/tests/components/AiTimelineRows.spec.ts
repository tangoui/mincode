import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import AiAgentEventDetail from "@/components/ai/AiAgentEventDetail.vue";
import AiTimelinePlanRow from "@/components/ai/timeline/AiTimelinePlanRow.vue";
import AiTimelineThoughtRow from "@/components/ai/timeline/AiTimelineThoughtRow.vue";
import AiTimelineToolRow from "@/components/ai/timeline/AiTimelineToolRow.vue";
import AiTimelineToolSummaryRow from "@/components/ai/timeline/AiTimelineToolSummaryRow.vue";
import type { AiAssistantEvent } from "@/stores/ai-assistant";

type Labels = InstanceType<typeof AiTimelineToolRow>["$props"]["labels"];
const text = new Proxy<Record<string, string>>({}, { get: (_target, key) => String(key) });
const labels: Labels = {
  activity: text as Labels["activity"],
  eventStatuses: { awaiting_approval: "等待操作审批", failed: "失败", running: "运行中" },
  taskStepStatuses: text as Labels["taskStepStatuses"],
  taskSteps: "Steps", dismissTaskSteps: "dismissTaskSteps", sources: "Sources", artifacts: "Artifacts",
  preview: "Preview", collapse: "Collapse", copy: "Copy", copied: "Copied", parameters: "Parameters",
  copyResult: "Copy result", copiedResult: "Copied result", copyResultFailed: "Copy failed",
};

function tool(overrides: Partial<AiAssistantEvent> = {}): AiAssistantEvent {
  return {
    id: "command", kind: "tool", title: "Run command", toolName: "workspace.run_command",
    status: "running", detail: "current output", ...overrides,
  };
}

function mountTool(event = tool(), overrides = {}) {
  return mount(AiTimelineToolRow, { props: {
    event, labels, awaitingApproval: false, conversationRunning: true, ...overrides,
  } });
}

describe("inline tool row", () => {
  it("opens for live output and folds when the command succeeds", async () => {
    const row = mountTool();
    expect(row.find(".is-spinning").exists()).toBe(true);
    expect(row.get("button[aria-expanded]").attributes("aria-expanded")).toBe("true");
    expect(row.getComponent(AiAgentEventDetail).text()).toContain("current output");
    await row.setProps({ event: tool({ status: "completed" }) });
    expect(row.get("button[aria-expanded]").attributes("aria-expanded")).toBe("false");
    expect(row.findComponent(AiAgentEventDetail).exists()).toBe(false);
  });

  it("opens when the command fails and renders the reason as an error", async () => {
    const row = mountTool(tool({ detail: "" }));
    expect(row.get("button[aria-expanded]").attributes("aria-expanded")).toBe("false");
    await row.setProps({ event: tool({ status: "failed", detail: "Permission denied" }) });
    expect(row.get("button[aria-expanded]").attributes("aria-expanded")).toBe("true");
    expect(row.get(".ai-agent-event-detail.is-failed[role=alert]").text()).toContain("Permission denied");
  });

  it("respects a manual toggle even when the event fails", async () => {
    const row = mountTool();
    await row.get("button[aria-expanded]").trigger("click");
    await row.setProps({ event: tool({ status: "failed" }) });
    expect(row.get("button[aria-expanded]").attributes("aria-expanded")).toBe("false");
  });

  it("shows the full retained parameters and output in an inline expansion", async () => {
    const row = mountTool(tool({ status: "completed", toolInput: '{"command":"printf x","cwd":"/tmp/scratch"}' }));
    expect(row.get("button[aria-expanded]").attributes("aria-expanded")).toBe("false");
    await row.get("button[aria-expanded]").trigger("click");
    expect(row.getComponent(AiAgentEventDetail).text()).toContain("current output");
    expect(row.text()).toContain("/tmp/scratch");
    expect(row.find(".el-card, .el-collapse, .el-scrollbar").exists()).toBe(false);
  });

  it("shows the waiting approval label on the tool row", () => {
    const row = mountTool(tool(), { awaitingApproval: true });
    expect(row.text()).toContain("等待操作审批");
  });

  it.each([undefined, "running"])("marks legacy %s calls interrupted", (status) => {
    const row = mountTool(tool({ status }), { conversationRunning: false });
    expect(row.text()).toContain("已中断");
    expect(row.find(".is-spinning").exists()).toBe(false);
  });

  it("shows unified diff counts for edits", async () => {
    const event = tool({ title: "Write file", toolName: "workspace.write_file", status: "completed",
      detail: "--- a/demo.ts\n+++ b/demo.ts\n@@ -1 +1,2 @@\n-before\n+after\n+another" });
    const row = mountTool(event);
    expect(row.text()).toContain("+2");
    expect(row.text()).toContain("−1");
    await row.setProps({ event: { ...event, detail: "saved" } });
    expect(row.text()).not.toMatch(/\+2|−1/);
  });
});

describe("inline activity rows", () => {
  const events = Array.from({ length: 4 }, (_, index) => tool({
    id: `read-${index}`, toolName: "workspace.read_file", title: "Read file", status: "completed",
  }));

  it("expands a summary to show every member", async () => {
    const row = mount(AiTimelineToolSummaryRow, { props: { events, labels, running: false, conversationRunning: false } });
    expect(row.text()).toContain("4");
    expect(row.findAllComponents(AiTimelineToolRow)).toHaveLength(0);
    await row.get("button[aria-expanded]").trigger("click");
    expect(row.findAllComponents(AiTimelineToolRow)).toHaveLength(4);
  });

  it("shows a spinner and exploring text for live summaries", () => {
    const row = mount(AiTimelineToolSummaryRow, { props: { events, labels, running: true, conversationRunning: true } });
    expect(row.text()).toContain("正在查看");
    expect(row.find(".is-spinning").exists()).toBe(true);
  });

  it("shows the thought duration with its text collapsed", async () => {
    const event = tool({ kind: "thought", status: "completed", startedAt: 1000, finishedAt: 5000, detail: "trace the call" });
    const row = mount(AiTimelineThoughtRow, { props: { event, labels } });
    expect(row.text()).toContain("4");
    expect(row.text()).not.toContain("trace the call");
    await row.get("button[aria-expanded]").trigger("click");
    expect(row.text()).toContain("trace the call");
  });

  it("shows plan progress and supports folding and dismissal", async () => {
    const event = tool({ kind: "plan", detail: JSON.stringify({ steps: [
      { title: "Read source", status: "completed" }, { title: "Test change", status: "pending" },
    ] }) });
    const row = mount(AiTimelinePlanRow, { props: { event, labels } });
    expect(row.text()).toContain("1/2");
    expect(row.findAll("li")).toHaveLength(2);
    expect(row.find(".el-card").exists()).toBe(false);
    await row.get("button[aria-expanded]").trigger("click");
    expect(row.find("li").exists()).toBe(false);
    await row.get("button[aria-label=dismissTaskSteps]").trigger("click");
    expect(row.emitted("dismiss")).toEqual([[event.id]]);
  });
});
