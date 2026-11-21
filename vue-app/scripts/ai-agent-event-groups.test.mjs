import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import {
  anchorUnassignedAiEvents,
  groupAiEventsForTimeline,
  summarizeAiAgentEventStatuses,
} from "../src/domain/ai/agent-event-groups.ts";

const source = await readFile(
  new URL("../src/domain/ai/agent-event-groups.ts", import.meta.url),
  "utf8",
);

function toolEvent(id, triggerMessageId, status = "completed") {
  return { id, kind: "tool", title: id, detail: "", status, triggerMessageId };
}

function eventAnchor(id) {
  return { id, role: "assistant", content: "", eventAnchor: true };
}

test("summarizes successful, failed, and running activity separately", () => {
  const summary = summarizeAiAgentEventStatuses([
    toolEvent("completed", undefined, "completed"),
    { id: "thought", kind: "thought", title: "Thought", detail: "" },
    toolEvent("failed", undefined, "failed"),
    toolEvent("cancelled", undefined, "cancelled"),
    toolEvent("running", undefined, "running"),
    { id: "pending", kind: "tool", title: "pending", detail: "" },
  ]);

  assert.deepEqual(summary, {
    succeeded: 2,
    failed: 2,
    running: 2,
  });
});

test("anchors only orphaned activity to the currently executing turn", () => {
  const messages = [
    { id: "previous-user", role: "user", content: "Previous request" },
    { id: "current-user", role: "user", content: "Current request" },
  ];
  const anchored = toolEvent("anchored", "previous-user");
  const legacy = { ...toolEvent("legacy"), afterMessageIndex: 0 };
  const orphaned = toolEvent("orphaned");
  const dangling = toolEvent("dangling", "removed-message");
  const events = [anchored, legacy, orphaned, dangling];

  const result = anchorUnassignedAiEvents(messages, events, "current-user");

  assert.equal(result[0], anchored);
  assert.equal(result[1], legacy);
  assert.deepEqual(result[2], {
    ...orphaned,
    triggerMessageId: "current-user",
    afterMessageIndex: undefined,
  });
  assert.deepEqual(result[3], {
    ...dangling,
    triggerMessageId: "current-user",
    afterMessageIndex: undefined,
  });
});

test("coalesces uninterrupted tool-only event anchors into one timeline card", () => {
  const messages = [
    eventAnchor("tool-anchor-1"),
    eventAnchor("tool-anchor-2"),
    eventAnchor("tool-anchor-3"),
  ];
  const groups = groupAiEventsForTimeline(messages, [
    toolEvent("read", "tool-anchor-1"),
    toolEvent("search", "tool-anchor-2"),
    toolEvent("write", "tool-anchor-3", "failed"),
  ]);

  assert.deepEqual(
    groups.map((group) => group.anchorIndexes),
    [[0, 1, 2]],
  );
  assert.deepEqual(
    groups[0].events.map((event) => event.id),
    ["read", "search", "write"],
  );
});

test("coalesces persisted silent assistant anchors that predate the eventAnchor flag", () => {
  const messages = [
    { id: "legacy-tool-anchor-1", role: "assistant", content: "" },
    { id: "legacy-tool-anchor-2", role: "assistant", content: "" },
  ];
  const groups = groupAiEventsForTimeline(messages, [
    toolEvent("read", "legacy-tool-anchor-1"),
    toolEvent("search", "legacy-tool-anchor-2"),
  ]);

  assert.deepEqual(
    groups.map((group) => group.anchorIndexes),
    [[0, 1]],
  );
  assert.deepEqual(
    groups[0].events.map((event) => event.id),
    ["read", "search"],
  );
});

test("merges later activity when the previous group is anchored to a visible reply", () => {
  const messages = [
    {
      id: "assistant-reply",
      role: "assistant",
      content: "I will inspect this.",
    },
    eventAnchor("tool-anchor"),
  ];
  const groups = groupAiEventsForTimeline(messages, [
    toolEvent("read", "assistant-reply"),
    toolEvent("write", "tool-anchor"),
  ]);

  assert.deepEqual(
    groups.map((group) => group.anchorIndexes),
    [[0, 1]],
  );
});

test("preserves a separate event card when a visible assistant reply interrupts tool calls", () => {
  const messages = [
    eventAnchor("tool-anchor-1"),
    { id: "assistant-reply", role: "assistant", content: "I found the cause." },
    eventAnchor("tool-anchor-2"),
  ];
  const groups = groupAiEventsForTimeline(messages, [
    toolEvent("read", "tool-anchor-1"),
    toolEvent("write", "tool-anchor-2"),
  ]);

  assert.deepEqual(
    groups.map((group) => group.anchorIndexes),
    [[0], [2]],
  );
});

test("coalesces thoughts, plans, and tool calls in one uninterrupted work process", () => {
  const messages = [
    eventAnchor("thought-anchor"),
    eventAnchor("plan-anchor"),
    eventAnchor("tool-anchor"),
  ];
  const groups = groupAiEventsForTimeline(messages, [
    {
      id: "thought",
      kind: "thought",
      title: "Thought",
      detail: "Reviewing",
      triggerMessageId: "thought-anchor",
    },
    {
      id: "plan",
      kind: "plan",
      title: "Plan",
      detail: "Implementing",
      triggerMessageId: "plan-anchor",
    },
    toolEvent("read", "tool-anchor"),
  ]);

  assert.deepEqual(
    groups.map((group) => group.anchorIndexes),
    [[0, 1, 2]],
  );
  assert.deepEqual(
    groups[0].events.map((event) => event.id),
    ["thought", "plan", "read"],
  );
});

test("continues to support legacy after-message anchors without cross-message merging", () => {
  const messages = [
    { id: "assistant-1", role: "assistant", content: "Response" },
    { id: "assistant-2", role: "assistant", content: "Next response" },
  ];
  const groups = groupAiEventsForTimeline(messages, [
    {
      id: "legacy-1",
      kind: "tool",
      title: "Read",
      detail: "",
      afterMessageIndex: 0,
    },
    {
      id: "legacy-2",
      kind: "tool",
      title: "Write",
      detail: "",
      afterMessageIndex: 1,
    },
  ]);

  assert.deepEqual(
    groups.map((group) => group.anchorIndexes),
    [[0], [1]],
  );
});

test("活动锚点使用一次构建的消息索引，避免事件与消息嵌套扫描", () => {
  assert.match(source, /function messageIndexById/);
  assert.match(source, /indexedMessages \?\? messageIndexById\(messages\)/);
  assert.doesNotMatch(source, /messages\.findIndex/);
});

test("删除临时回复时把它的事件改挂到替换它的消息上，其他事件不动", async () => {
  const { reanchorAiEvents } = await import("../src/domain/ai/agent-event-groups.ts");
  const events = [
    { id: "loop-reasoning", kind: "thought", title: "思考", triggerMessageId: "temp" },
    { id: "loop-tool-1", kind: "tool", title: "读取", triggerMessageId: "anchor" },
    { id: "loop-tool-2", kind: "tool", title: "写入", triggerMessageId: "temp" },
  ];
  const moved = reanchorAiEvents(events, "temp", "summary");
  assert.deepEqual(moved.map((event) => event.triggerMessageId), ["summary", "anchor", "summary"]);
  assert.equal(moved[1], events[1], "未改动的事件保持同一对象");
  assert.equal(reanchorAiEvents(events, "missing", "summary"), events, "没有可改挂的事件时返回原数组");
});
