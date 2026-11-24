import assert from "node:assert/strict";
import test from "node:test";
import {
  aiPermissionToolCallId,
  aiTimelineToolCallIds,
  buildAiTimelineItems,
} from "../src/domain/ai/timeline-items.ts";

const message = { id: "message-0", role: "assistant", content: "Checking files" };
const tool = (id, toolName = "read_file", status = "completed") => ({
  id, kind: "tool", title: toolName, toolName: `workspace.${toolName}`, status, detail: "",
});
const approval = (toolCallId) => ({
  id: `approval-${toolCallId}`, conversationId: "session", summary: "Check access",
  request: { toolCallId },
});
const permission = (toolCall) => ({
  requestId: "permission", serverId: "server", sessionId: "session", options: [], toolCall,
});
const group = (events, anchorIndex = 0, anchorIndexes = [anchorIndex]) => ({
  anchorIndex, anchorIndexes, events,
});
function build(events, overrides = {}) {
  return buildAiTimelineItems({
    messages: [{ message, index: 0 }],
    groupsByAnchor: new Map([[0, group(events)]]),
    activeEvents: [],
    anchoredApprovals: new Map(),
    activeApprovals: [],
    permission: null,
    continueAt: null,
    running: false,
    ...overrides,
  });
}
const kinds = (items) => items.map(({ kind }) => kind);

test("merges consecutive read-only tools", () => {
  const events = [tool("a"), tool("b"), tool("c"), tool("d", "search_code")];
  const items = build(events);
  assert.deepEqual(kinds(items), ["message", "toolSummary"]);
  assert.deepEqual(items[1].events, events);
  assert.equal(items[1].running, false);
});

test("running read-only stays in the same summary", () => {
  const complete = tool("a");
  const pending = tool("b", "read_file", "running");
  const [, summary] = build([complete, pending], { running: true });
  assert.equal(summary.kind, "toolSummary");
  assert.equal(summary.running, true);
  assert.equal(summary.key, build([complete])[1].key);
  assert.equal(build([complete, pending])[1].running, false);
});

test("failed read-only splits out", () => {
  const failed = tool("b", "read_file", "failed");
  const items = build([tool("a"), failed, tool("c")]);
  assert.deepEqual(kinds(items), ["message", "toolSummary", "tool", "toolSummary"]);
  assert.equal(items[1].events.length, 1);
  assert.equal(items[2].event, failed);
  assert.equal(items[3].events.length, 1);
});

test("non-read-only tools break merging", () => {
  const items = build([tool("a"), tool("b", "run_command"), tool("c")]);
  assert.deepEqual(kinds(items), ["message", "toolSummary", "tool", "toolSummary"]);
});

for (const kind of ["thought", "plan", "mode"]) {
  test(`${kind} breaks merging`, () => {
    const event = { ...tool("b"), kind };
    assert.deepEqual(kinds(build([tool("a"), event, tool("c")])), [
      "message", "toolSummary", kind, "toolSummary",
    ]);
  });
}

test("approval follows its tool row across silent anchors", () => {
  const request = approval("a");
  const event = tool("loop-tool-a", "run_command");
  const items = build([], {
    groupsByAnchor: new Map([[0, group([event], 0, [0, 1])]]),
    anchoredApprovals: new Map([[1, [request]]]),
  });
  assert.deepEqual(kinds(items), ["message", "tool", "approval"]);
  assert.equal(items[1].awaitingApproval, true);
  assert.equal(items[2].approval, request);
  assert.deepEqual(aiTimelineToolCallIds(request), ["loop-tool-a", "a"]);
});

test("awaiting read-only is not merged", () => {
  const items = build([tool("a"), tool("b"), tool("loop-tool-c"), tool("d")], {
    anchoredApprovals: new Map([[0, [approval("c")]]]),
  });
  assert.deepEqual(kinds(items), ["message", "toolSummary", "tool", "approval", "toolSummary"]);
  assert.equal(items[1].events.length, 2);
  assert.equal(items[2].awaitingApproval, true);
});

test("unmatched anchored approval goes to its group end", () => {
  const items = build([], {
    groupsByAnchor: new Map([[0, group([tool("a")], 0, [0, 1])]]),
    anchoredApprovals: new Map([[1, [approval("missing")]]]),
    activeEvents: [tool("b", "run_command")],
  });
  assert.deepEqual(kinds(items), ["message", "toolSummary", "approval", "tool"]);
});

test("unmatched active approval goes to the timeline end", () => {
  assert.deepEqual(kinds(build([tool("a")], {
    activeEvents: [tool("b", "run_command")], activeApprovals: [approval("missing")], running: true,
  })), ["message", "toolSummary", "tool", "approval", "thinking"]);
});

test("ACP permission follows a matching read-only tool", () => {
  const request = permission({ toolCallId: "c" });
  const items = build([tool("c"), tool("d")], { permission: request });
  assert.deepEqual(kinds(items), ["message", "tool", "permission", "toolSummary"]);
  assert.equal(items[1].awaitingApproval, true);
  assert.equal(items[2].permission, request);
  assert.equal(aiPermissionToolCallId(request), "c");
});

test("ACP permission without a string call id goes before thinking", () => {
  for (const toolCall of [undefined, { toolCallId: 23 }]) {
    const request = permission(toolCall);
    assert.equal(aiPermissionToolCallId(request), undefined);
    assert.deepEqual(kinds(build([tool("c")], { permission: request, running: true })), [
      "message", "toolSummary", "permission", "thinking",
    ]);
  }
});

test("continue follows its message and thinking comes last", () => {
  assert.deepEqual(kinds(build([], {
    messages: [{ message, index: 2 }], groupsByAnchor: new Map([[2, group([tool("a")], 2)]]),
    continueAt: 2, running: true,
  })), ["message", "continue", "toolSummary", "thinking"]);
});

test("keys are stable and a summary key comes from its first event", () => {
  const events = [tool("first"), tool("second")];
  const keys = build(events).map(({ key }) => key);
  assert.deepEqual(build(events).map(({ key }) => key), keys);
  assert.equal(build([events[0]])[1].key, keys[1]);
  assert.match(keys[1], /first/);
});

test("an activity group at a hidden message retains its order", () => {
  const items = build([], {
    messages: [{ message, index: 0 }, { message: { ...message, id: "later" }, index: 2 }],
    groupsByAnchor: new Map([[1, group([tool("hidden")], 1)], [2, group([tool("later")], 2)]]),
  });
  assert.deepEqual(kinds(items), ["message", "toolSummary", "message", "toolSummary"]);
  assert.equal(items[1].events[0].id, "hidden");
});
