import { mount } from "@vue/test-utils";
import { expect, it } from "vitest";
import AiMessageThread from "@/components/ai/AiMessageThread.vue";
import type { AiAssistantEvent } from "@/stores/ai-assistant";

it("renders removed tools from persisted AI history as ordinary tool records", async () => {
  const tools = [
    { toolName: "browser_navigate", title: "Navigate browser" },
    { toolName: "git_status", title: "Git status" },
    { toolName: "project_read_file", title: "Read file" },
  ];
  const events = tools.map((tool): AiAssistantEvent => ({
    ...tool,
    id: tool.toolName,
    kind: "tool",
    detail: "Previous tool result",
    status: "completed",
    triggerMessageId: "history-answer",
  }));
  const messages = [{ id: "history-answer", role: "assistant", content: "Previous answer" }] as const;
  const props: InstanceType<typeof AiMessageThread>["$props"] = {
    messages: [...messages],
    events,
    permission: null,
    composerHasDraft: false,
    mode: "agentic",
    title: "History",
    hint: "",
    taskStepsLabel: "Task steps",
    dismissTaskStepsLabel: "Dismiss task steps",
    sourcesLabel: "Sources",
    artifactsLabel: "Artifacts",
    previewLabel: "Preview",
    collapseLabel: "Collapse",
    copyLabel: "Copy",
    copiedLabel: "Copied",
    copyResultLabel: "Copy result",
    copiedResultLabel: "Copied result",
    copyResultFailedLabel: "Copy failed",
    emptyMessageLabel: "Empty message",
    metadataLabel: "Metadata",
    expandMessageLabel: "Expand message",
    collapseMessageLabel: "Collapse message",
    copyMessageLabel: "Copy message",
    copiedMessageLabel: "Copied message",
    copyMessageFailedLabel: "Copy failed",
    copyImageLabel: "Copy image",
    copiedImageLabel: "Copied image",
    copyImageFailedLabel: "Copy failed",
    running: false,
    runningLabel: "Running",
    taskStepStatusLabels: { pending: "Pending", in_progress: "Running", completed: "Completed", failed: "Failed", cancelled: "Cancelled" },
    activityLabels: {
      analysis: "Analysis", plan: "Plan", modeChange: "Mode", readFile: "Read file", openFile: "Open file",
      searchCode: "Search code", searchFiles: "Search files", runTest: "Run test", codeCheck: "Check code",
      buildProject: "Build", gitOperation: "Git", runCommand: "Run command", writeFile: "Write file",
      createFile: "Create file", deleteFile: "Delete file", browserAction: "Browser", databaseAction: "Database",
      httpAction: "HTTP", remoteAction: "Remote", connectorCall: "Connector", systemAction: "System",
      genericTool: "Tool call", detail: "Show details", hideDetail: "Hide details", toolName: "Tool",
      result: "Result", runningDuration: "Running duration", totalDuration: "Total duration", duration: "Duration",
    },
    eventStatusLabels: { completed: "Completed" },
  };
  const wrapper = mount(AiMessageThread, { props });

  try {
    expect(wrapper.text()).toContain("Previous answer");
    const records = wrapper.findAll(".ai-timeline-tool");
    expect(records.map((record) => record.get(".ai-timeline__title strong").text())).toEqual(tools.map(() => "Tool call"));
    for (const [index, record] of records.entries()) {
      await record.get("button[aria-expanded]").trigger("click");
      expect(record.get(".ai-timeline__metadata code").text()).toBe(tools[index]?.toolName);
    }
  } finally {
    wrapper.unmount();
  }
});
