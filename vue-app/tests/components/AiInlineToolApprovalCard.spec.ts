import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import AiInlineToolApprovalCard, { type InlineToolApprovalLabels } from "@/components/ai/AiInlineToolApprovalCard.vue";
import type { ToolApprovalQueueItem } from "@/domain/ai/tool-approval-queue";

const labels: InlineToolApprovalLabels = {
  title: "Approve",
  tool: "Tool",
  resource: "Resource",
  summary: "Summary",
  parameters: "Parameters",
  risk: { read: "Read", network: "Network", write: "Write", dangerous: "Dangerous" },
  allowOnce: "Once",
  allowSession: "Session",
  allowSessionSameParameters: "Session, same call",
  allowGlobal: "Always",
  reject: "Reject",
  feedbackPlaceholder: "Tell the assistant what to do instead",
  rejectWithFeedback: "Reject with note",
};

function approval(request: Partial<ToolApprovalQueueItem["request"]>): ToolApprovalQueueItem {
  return {
    id: "a1",
    conversationId: "c1",
    summary: "Run the supplied command in the bound project (command redacted).",
    request: {
      conversationId: "c1",
      toolName: "workspace.run_command",
      resourceId: "project:/repo",
      parameters: { resourceId: "project:/repo", command: "git status &&\nrm -rf build", timeoutSecs: 30 },
      risk: "dangerous",
      ...request,
    },
  };
}

describe("inline tool approval card", () => {
  it("shows the exact parameters the user is approving, unredacted and as plain text", () => {
    const wrapper = mount(AiInlineToolApprovalCard, { props: { approval: approval({}), labels } });
    const values = wrapper.findAll("[data-approval-parameter]");
    expect(values.map((value) => value.attributes("data-approval-parameter"))).toEqual(["command", "timeoutSecs"]);
    expect(values[0]!.text()).toBe("git status &&\nrm -rf build");
    expect(values[1]!.text()).toBe("30");
  });

  it("labels a parameter-bound session grant so it is not mistaken for allowing the whole tool", () => {
    const bound = mount(AiInlineToolApprovalCard, { props: { approval: approval({}), labels } });
    expect(bound.get('[data-approval-scope="session"]').text()).toBe("Session, same call");
    const toolWide = mount(AiInlineToolApprovalCard, {
      props: { approval: approval({ risk: "write", toolName: "workspace.save_project_file" }), labels },
    });
    expect(toolWide.get('[data-approval-scope="session"]').text()).toBe("Session");
  });

  it("rejecting with typed feedback emits it, and plain reject emits an empty decision", async () => {
    const wrapper = mount(AiInlineToolApprovalCard, { props: { approval: approval({}), labels } });
    await wrapper.find("[data-approval-feedback] input").setValue("  use pnpm  ");
    await wrapper.find('[data-approval-action="reject"]').trigger("click");
    expect(wrapper.emitted("decide")?.[0]).toEqual(["a1", { feedback: "use pnpm" }]);
    await wrapper.find("[data-approval-feedback] input").setValue("");
    await wrapper.find('[data-approval-action="reject"]').trigger("click");
    expect(wrapper.emitted("decide")?.[1]).toEqual(["a1", {}]);
  });

  it("limits the feedback to 500 characters and shows the count", () => {
    const wrapper = mount(AiInlineToolApprovalCard, { props: { approval: approval({}), labels } });
    expect(wrapper.get("[data-approval-feedback] input").attributes("maxlength")).toBe("500");
    expect(wrapper.find("[data-approval-feedback] .el-input__count").exists()).toBe(true);
  });

  it("allow buttons emit their scope without feedback", async () => {
    const wrapper = mount(AiInlineToolApprovalCard, { props: { approval: approval({ risk: "write" }), labels } });
    await wrapper.find('[data-approval-scope="once"]').trigger("click");
    expect(wrapper.emitted("decide")?.[0]).toEqual(["a1", { scope: "once" }]);
  });
});
