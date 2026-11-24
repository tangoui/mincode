import { mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import AiAgentEventDetail from "@/components/ai/AiAgentEventDetail.vue";
import AiTimelineNoticeRow from "@/components/ai/timeline/AiTimelineNoticeRow.vue";
import AiTimelinePlanRow from "@/components/ai/timeline/AiTimelinePlanRow.vue";
import AiTimelineThoughtRow from "@/components/ai/timeline/AiTimelineThoughtRow.vue";
import AiTimelineToolRow from "@/components/ai/timeline/AiTimelineToolRow.vue";
import AiTimelineToolSummaryRow from "@/components/ai/timeline/AiTimelineToolSummaryRow.vue";
import type { AiAssistantEvent } from "@/stores/ai-assistant";

import { copyText } from "@/services/desktop-bridge";

vi.mock("@/services/desktop-bridge", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/services/desktop-bridge")>(),
  copyText: vi.fn().mockResolvedValue(undefined),
}));

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

function mountTool(event = tool(), overrides = {}, attachTo?: HTMLElement) {
  return mount(AiTimelineToolRow, { attachTo, props: {
    event, labels, awaitingApproval: false, conversationRunning: true, ...overrides,
  } });
}

function observeOutputHeight() {
  let notify: (height: number) => void = () => {};
  vi.stubGlobal("ResizeObserver", class {
    constructor(private readonly callback: ResizeObserverCallback) {}

    observe(target: Element) {
      if (!target.closest(".ai-agent-event-detail")) return;
      notify = (height) => this.callback(
        [{ contentRect: new DOMRect(0, 0, 320, height) } as ResizeObserverEntry],
        this,
      );
    }

    unobserve() {}
    disconnect() {}
  });
  return (height: number) => notify(height);
}

afterEach(() => vi.unstubAllGlobals());

describe("inline tool row", () => {
  it("opens for live output and folds when the command succeeds", async () => {
    const row = mountTool();
    expect(row.find(".is-spinning").exists()).toBe(true);
    expect(row.find(".ai-timeline__shimmer").exists()).toBe(true);
    expect(row.get("button[aria-expanded]").attributes("aria-expanded")).toBe("true");
    expect(row.getComponent(AiAgentEventDetail).text()).toContain("current output");
    await row.setProps({ event: tool({ status: "completed" }) });
    expect(row.get("button[aria-expanded]").attributes("aria-expanded")).toBe("false");
    expect(row.findComponent(AiAgentEventDetail).exists()).toBe(false);
    expect(row.find(".ai-timeline__shimmer").exists()).toBe(false);
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

  it.each([
    { status: "completed", toolName: "workspace.run_command", tail: true },
    { status: "failed", toolName: "workspace.read_file", tail: true },
    { status: "completed", toolName: "workspace.read_file", toolInput: '{"path":"demo.ts"}', tail: false },
  ])("folds tall $status $toolName output with full copy and reversible expansion", async ({ tail, ...event }) => {
    const setHeight = observeOutputHeight();
    const detail = "content that wraps without any line breaks ".repeat(60).trim();
    const row = mountTool(tool({ detail, ...event }), {}, document.body);
    if (event.status === "completed") await row.get("button[aria-expanded]").trigger("click");
    const viewport = row.get<HTMLElement>(".ai-agent-event-detail__viewport");
    viewport.element.style.lineHeight = "15px";
    const toggle = () => row.get(".ai-timeline__output-toggle");
    setHeight(180);
    await row.vm.$nextTick();
    expect(row.find(".ai-timeline__output-toggle").exists()).toBe(false);
    setHeight(270);
    await row.vm.$nextTick();
    expect(viewport.text()).toBe(detail);
    expect(viewport.classes().includes("is-tail")).toBe(tail);
    expect(viewport.classes()).toContain("is-collapsed");
    expect(toggle().attributes("aria-expanded")).toBe("false");
    await row.get('button[aria-label="Copy result"]').trigger("click");
    expect(copyText).toHaveBeenLastCalledWith(detail);
    await toggle().trigger("click");
    expect(viewport.classes()).not.toContain("is-collapsed");
    expect(toggle().attributes("aria-expanded")).toBe("true");
    await toggle().trigger("click");
    expect(viewport.classes()).toContain("is-collapsed");
    setHeight(150);
    await row.vm.$nextTick();
    expect(row.find(".ai-timeline__output-toggle").exists()).toBe(false);
    row.unmount();
  });

  it("shows the waiting approval label on the tool row", () => {
    const row = mountTool(tool(), { awaitingApproval: true });
    expect(row.text()).toContain("等待操作审批");
    expect(row.find(".ai-timeline__shimmer").exists()).toBe(false);
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

  it("animates exploring text only while a summary is live", async () => {
    const row = mount(AiTimelineToolSummaryRow, { props: { events, labels, running: true, conversationRunning: true } });
    expect(row.get(".ai-timeline__shimmer").text()).toContain("正在查看");
    expect(row.find(".is-spinning").exists()).toBe(true);
    await row.setProps({ running: false });
    expect(row.find(".ai-timeline__shimmer, .is-spinning").exists()).toBe(false);
  });

  it("animates thought text and the pending notice only while running", async () => {
    const event = tool({ kind: "thought", startedAt: 1000 });
    const thought = mount(AiTimelineThoughtRow, { props: { event, labels, conversationRunning: true } });
    const notice = mount(AiTimelineNoticeRow, { props: { text: "Thinking", spinning: true } });
    expect(thought.find(".ai-timeline__shimmer").exists()).toBe(true);
    expect(notice.get(".ai-timeline__shimmer").text()).toBe("Thinking");
    await thought.setProps({ event: { ...event, status: "completed", finishedAt: 2000 } });
    await notice.setProps({ spinning: false });
    expect(thought.find(".ai-timeline__shimmer, .is-spinning").exists()).toBe(false);
    expect(notice.find(".ai-timeline__shimmer, .is-spinning").exists()).toBe(false);
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

describe("timeline colors and output toggle", () => {
  it("colors the status icon by state", async () => {
    const tone = (row: ReturnType<typeof mountTool>) => row.get(".ai-timeline__status-icon").attributes("data-tone");
    const row = mountTool();
    expect(tone(row)).toBe("running");
    await row.setProps({ event: tool({ status: "completed" }) });
    expect(tone(row)).toBe("success");
    await row.setProps({ event: tool({ status: "failed", detail: "boom" }) });
    expect(tone(row)).toBe("danger");
    await row.setProps({ event: tool({ status: "running" }), awaitingApproval: true });
    expect(tone(row)).toBe("warning");
    await row.setProps({ event: tool({ status: "running" }), awaitingApproval: false, conversationRunning: false });
    expect(tone(row)).toBe("muted");
  });

  it("colors the summary icon while exploring and when done", async () => {
    const events = [tool({ id: "read", toolName: "workspace.read_file", title: "Read file", status: "completed" })];
    const row = mount(AiTimelineToolSummaryRow, { props: { events, labels, running: true, conversationRunning: true } });
    expect(row.get(".ai-timeline__status-icon").attributes("data-tone")).toBe("running");
    await row.setProps({ running: false });
    expect(row.get(".ai-timeline__status-icon").attributes("data-tone")).toBe("success");
  });

  it("places the show-all toggle below the folded output with a chevron", async () => {
    const setHeight = observeOutputHeight();
    const detail = "content that wraps without any line breaks ".repeat(60).trim();
    const row = mountTool(tool({ detail, status: "completed" }), {}, document.body);
    await row.get("button[aria-expanded]").trigger("click");
    row.get<HTMLElement>(".ai-agent-event-detail__viewport").element.style.lineHeight = "15px";
    setHeight(270);
    await row.vm.$nextTick();
    const toggle = row.get(".ai-timeline__output-toggle");
    expect(toggle.element.previousElementSibling?.classList.contains("ai-agent-event-detail__viewport")).toBe(true);
    expect(toggle.find("svg").exists()).toBe(true);
    row.unmount();
  });

  it("styles status tones and the toggle with theme colors", async () => {
    const { readFile } = await import("node:fs/promises");
    const { resolve } = await import("node:path");
    const css = await readFile(resolve(process.cwd(), "src/styles/workspaces/ai/timeline.css"), "utf8");
    for (const [tone, color] of [["success", "success"], ["danger", "danger"], ["warning", "warning"], ["running", "primary"]]) {
      expect(css).toMatch(new RegExp(`\\.ai-timeline__status-icon\\[data-tone="${tone}"\\][^{]*\\{[^}]*color:\\s*var\\(--el-color-${color}\\)`));
    }
    expect(css).toMatch(/\.ai-timeline__output-toggle\s*\{[^}]*color:\s*var\(--el-color-primary\)/);
  });
});
