import { mount } from "@vue/test-utils";
import { afterEach, expect, it, vi } from "vitest";
import { h, nextTick, ref } from "vue";
import AiTimelineApprovalRow, { type InlineToolApprovalLabels } from "@/components/ai/timeline/AiTimelineApprovalRow.vue";
import type { ToolApprovalQueueItem } from "@/domain/ai/tool-approval-queue";

const labels: InlineToolApprovalLabels = {
  title: "Approve", tool: "Tool", resource: "Resource", summary: "Summary", parameters: "Parameters",
  risk: { read: "Read", network: "Network", write: "Write", dangerous: "Dangerous" },
  allowOnce: "Once", allowSession: "Session", allowSessionSameParameters: "Session, same call", allowGlobal: "Always",
  reject: "Reject", feedbackPlaceholder: "Tell the assistant what to do instead", rejectWithFeedback: "Reject with note",
};

function native(request: Partial<ToolApprovalQueueItem["request"]> = {}) {
  return { kind: "tool" as const, approval: {
    id: "approval-1", conversationId: "c1", summary: "Run the supplied command (redacted).",
    request: {
      conversationId: "c1", toolName: "workspace.run_command", resourceId: "project:/repo",
      parameters: { resourceId: "project:/repo", command: "printf 'first'\nprintf 'second'", timeoutSecs: 30 },
      risk: "dangerous" as const, ...request,
    },
  } };
}

const acp = { kind: "permission" as const, permission: {
  requestId: "p1", serverId: "server", sessionId: "session", toolCall: { title: "Read config" },
  options: [
    { optionId: "once", name: "Allow once", kind: "allow_once" },
    { optionId: "reject", name: "Deny this call", kind: "reject_once" },
  ],
} };

function row(source: ReturnType<typeof native> | typeof acp = native(), composerHasDraft = false, attachTo?: Element) {
  return mount(AiTimelineApprovalRow, { props: { source, labels, composerHasDraft }, attachTo });
}

afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

it("shows the summary and exact parameters as plain text", () => {
  const wrapper = row();
  expect(wrapper.text()).toContain("Run the supplied command (redacted).");
  const disclosure = wrapper.get("details");
  expect(disclosure.attributes("open")).toBeUndefined();
  const values = disclosure.findAll("[data-approval-parameter]");
  expect(values.map((value) => value.attributes("data-approval-parameter"))).toEqual(["command", "timeoutSecs"]);
  expect(values[0]?.text()).toBe("printf 'first'\nprintf 'second'");
  expect(values[1]?.text()).toBe("30");
  expect(wrapper.find(".el-card").exists()).toBe(false);
});

it("honors the tool risk rules and session grant wording", async () => {
  const bound = row();
  expect(bound.get('[data-approval-scope="session"]').text()).toBe("Session, same call");
  expect(bound.find('[data-approval-scope="global"]').exists()).toBe(false);
  const toolWide = row(native({ risk: "write", toolName: "workspace.save_project_file" }));
  expect(toolWide.get('[data-approval-scope="session"]').text()).toBe("Session");
  const readable = row(native({ risk: "read", toolName: "workspace.read_file" }));
  for (const scope of ["once", "session", "global"]) {
    await readable.get(`[data-approval-scope="${scope}"]`).trigger("click");
  }
  expect(readable.emitted("decide-tool")).toEqual([[{ scope: "once" }], [{ scope: "session" }], [{ scope: "global" }]]);
});

it("expands reject feedback, enforces the limit and supports an empty rejection", async () => {
  const wrapper = row();
  expect(wrapper.find("[data-approval-feedback] input").exists()).toBe(false);
  await wrapper.get('[data-approval-action="reject"]').trigger("click");
  const input = wrapper.get("[data-approval-feedback] input");
  expect(input.attributes("maxlength")).toBe("500");
  await input.setValue("  use pnpm  ");
  await wrapper.get('[data-approval-action="confirm-reject"]').trigger("click");
  await input.setValue("x".repeat(512));
  expect(wrapper.get("[data-approval-count]").text()).toBe("500 / 500");
  await wrapper.get('[data-approval-action="confirm-reject"]').trigger("click");
  await input.setValue("");
  await wrapper.get('[data-approval-action="confirm-reject"]').trigger("click");
  expect(wrapper.emitted("decide-tool")).toEqual([[{ feedback: "use pnpm" }], [{ feedback: "x".repeat(500) }], [{}]]);
});

it("shows every ACP option and emits cancel without selecting an option", async () => {
  const wrapper = row(acp);
  expect(wrapper.text()).toContain("Read config");
  expect(wrapper.findAll("[data-option-id]")).toHaveLength(2);
  expect(wrapper.get('[data-option-id="once"]').classes()).toContain("el-button--primary");
  expect(wrapper.get('[data-option-id="reject"]').classes()).not.toContain("el-button--primary");
  await wrapper.get('[data-option-id="once"]').trigger("click");
  await wrapper.get('[data-option-id="reject"]').trigger("click");
  await wrapper.get('[data-action="cancel"]').trigger("click");
  expect(wrapper.emitted("decide-permission")).toEqual([["once"], ["reject"], [undefined]]);
});

for (const source of [native(), acp]) {
  it(`scrolls ${source.kind} approval into view and leaves draft focus in the composer`, async () => {
    const composer = document.body.appendChild(document.createElement("textarea"));
    composer.focus();
    const scroll = vi.spyOn(HTMLElement.prototype, "scrollIntoView");
    const wrapper = row(source, true, document.body);
    await nextTick();
    expect(scroll).toHaveBeenCalledWith({ block: "nearest" });
    expect(document.activeElement).toBe(composer);
    expect(wrapper.classes()).toContain("is-attention");
    wrapper.unmount();
  });

  it(`focuses the first ${source.kind} action without a draft and restores focus on removal`, async () => {
    const panel = document.body.appendChild(document.createElement("aside"));
    panel.className = "ai-assistant-drawer__panel";
    const composer = panel.appendChild(document.createElement("textarea"));
    composer.focus();
    const visible = ref(true);
    const wrapper = mount({ render: () => visible.value
      ? h(AiTimelineApprovalRow, { source, labels, composerHasDraft: false })
      : null }, { attachTo: panel });
    await nextTick();
    expect(document.activeElement).toBe(wrapper.get("button").element);
    visible.value = false;
    await nextTick();
    expect(document.activeElement).toBe(composer);
    wrapper.unmount();
  });

  it(`preserves focus outside the AI panel when a ${source.kind} approval arrives`, async () => {
    const input = document.body.appendChild(document.createElement("input"));
    const panel = document.body.appendChild(document.createElement("aside"));
    panel.className = "ai-assistant-drawer__panel";
    input.focus();
    const wrapper = row(source, false, panel);
    try {
      await nextTick();
      expect(document.activeElement).toBe(input);
    } finally {
      wrapper.unmount();
    }
  });
}
