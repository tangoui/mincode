import assert from "node:assert/strict";
import test from "node:test";
import { createToolApprovalQueue, normalizeApprovalFeedback } from "../src/domain/ai/tool-approval-queue.ts";

const request = { conversationId: "c1", toolName: "write", risk: "write" };

test("an approval enqueued for an already stopped run is rejected without a card", async () => {
  const queue = createToolApprovalQueue();
  const controller = new AbortController();
  controller.abort();
  const approval = queue.enqueue("c1", undefined, request, "summary", controller.signal);
  assert.deepEqual(await approval.decision, {});
  assert.deepEqual(queue.list("c1"), []);
});

test("stopping a run removes its pending approval card and rejects it", async () => {
  const queue = createToolApprovalQueue();
  const controller = new AbortController();
  let notified = 0;
  queue.subscribe(() => { notified += 1; });
  const approval = queue.enqueue("c1", undefined, request, "summary", controller.signal);
  assert.equal(queue.list("c1")[0]?.id, approval.id);
  controller.abort();
  assert.deepEqual(await approval.decision, {});
  assert.deepEqual(queue.list("c1"), []);
  assert.equal(notified, 2);
  queue.resolve(approval.id, { scope: "once" });
  assert.deepEqual(queue.list(), []);
});

test("approvals resolve by id, including a later one answered remotely", async () => {
  const queue = createToolApprovalQueue();
  const first = queue.enqueue("c1", undefined, request, "first");
  const second = queue.enqueue("c1", undefined, request, "second");
  assert.notEqual(first.id, second.id);
  assert.deepEqual(queue.list("c1").map((item) => item.id), [first.id]);
  queue.resolve(second.id, { scope: "session" });
  assert.deepEqual(await second.decision, { scope: "session" });
  assert.deepEqual(queue.list("c1").map((item) => item.id), [first.id]);
  queue.resolve(first.id, { scope: "once" });
  assert.deepEqual(await first.decision, { scope: "once" });
});

test("a resolved approval ignores a later abort", async () => {
  const queue = createToolApprovalQueue();
  const controller = new AbortController();
  const approval = queue.enqueue("c1", undefined, request, "summary", controller.signal);
  queue.resolve(approval.id, { scope: "once" });
  controller.abort();
  assert.deepEqual(await approval.decision, { scope: "once" });
});

test("rejecting a conversation and disposing settle every pending approval", async () => {
  const queue = createToolApprovalQueue();
  const a = queue.enqueue("c1", undefined, request, "a");
  const b = queue.enqueue("c2", undefined, request, "b");
  queue.rejectConversation("c1");
  assert.deepEqual(await a.decision, {});
  assert.deepEqual(queue.list().map((item) => item.conversationId), ["c2"]);
  queue.dispose();
  assert.deepEqual(await b.decision, {});
  const late = queue.enqueue("c3", undefined, request, "late");
  assert.deepEqual(await late.decision, {});
});

test("a rejection with feedback reaches every pending approval of the conversation", async () => {
  const queue = createToolApprovalQueue();
  const a = queue.enqueue("c1", undefined, request, "a");
  const b = queue.enqueue("c1", undefined, request, "b");
  const other = queue.enqueue("c2", undefined, request, "c");
  queue.rejectConversation("c1", "use the staging database instead");
  assert.deepEqual(await a.decision, { feedback: "use the staging database instead" });
  assert.deepEqual(await b.decision, { feedback: "use the staging database instead" });
  assert.equal(queue.list("c2")[0]?.id, other.id);
});

test("approval feedback is trimmed, single-line and capped at 500 characters", () => {
  assert.equal(normalizeApprovalFeedback("  \n "), undefined);
  assert.equal(normalizeApprovalFeedback("a\nb\r\nc"), "a b c");
  assert.equal(normalizeApprovalFeedback("x".repeat(600)).length, 500);
});

test("resolving with a scope keeps approvals and decline without text stays empty", async () => {
  const queue = createToolApprovalQueue();
  const a = queue.enqueue("c1", undefined, request, "a");
  queue.resolve(a.id, { scope: "once" });
  assert.deepEqual(await a.decision, { scope: "once" });
  const b = queue.enqueue("c1", undefined, request, "b");
  queue.resolve(b.id);
  assert.deepEqual(await b.decision, {});
});

test("tool approvals carry the run's abort signal and MCP calls re-check it", async () => {
  const { readSource: readFile } = await import("./read-source.mjs");
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  const authorize = drawer.match(/async function authorizeTool\([\s\S]*?\n}\n/)?.[0] ?? "";
  assert.match(authorize, /signal: AbortSignal,/);
  assert.match(authorize, /toolApprovalQueue\.enqueue\([\s\S]*?signal,\s*\)/);
  assert.match(authorize, /await approval\.decision;[\s\S]*?throwIfAborted\(signal\);[\s\S]*?"thinking"/);
  assert.equal((drawer.match(/await authorizeTool\([\s\S]*?signal,\s*\);/g) ?? []).length, 2);
  assert.match(drawer, /throwIfAborted\(signal\);\s*const result = safeMcpResult\(await callMcpToolCancellable<McpCallResult>\(\s*invokeBackend,[\s\S]*?signal,\s*\)\);/);
  const settingsTools = await readFile(new URL("../src/services/ai-tools/settings-tools.ts", import.meta.url), "utf8");
  assert.match(settingsTools, /execute: async \(\{ input, signal \}[\s\S]*?callMcpToolCancellable\([\s\S]*?signal,\s*\)\),/);
});

test("a cancellable MCP call sends mcp_cancel_tool with its id when the run stops", async () => {
  const { callMcpToolCancellable } = await import("../src/services/mcp-tool-call.ts");
  const calls = [];
  let finish;
  const invoke = (command, args) => {
    calls.push({ command, args });
    if (command === "mcp_call_tool") return new Promise((resolve) => { finish = resolve; });
    return Promise.resolve(true);
  };
  const controller = new AbortController();
  const pending = callMcpToolCancellable(invoke, { serverId: "s", toolName: "t", arguments: {} }, controller.signal);
  controller.abort();
  finish({ content: [] });
  await pending;
  const [call, cancel] = calls;
  assert.equal(call.command, "mcp_call_tool");
  assert.match(call.args.callId, /^mcp-call-/);
  assert.deepEqual(cancel, { command: "mcp_cancel_tool", args: { callId: call.args.callId } });

  calls.length = 0;
  const done = new AbortController();
  const settled = callMcpToolCancellable(invoke, { serverId: "s", toolName: "t", arguments: {} }, done.signal);
  finish({ content: [] });
  await settled;
  done.abort();
  assert.deepEqual(calls.map((item) => item.command), ["mcp_call_tool"], "no cancel after the call settled");
});

test("approval cards carry the tool call they authorize, so the thread can show them under that step", async () => {
  const queue = createToolApprovalQueue();
  queue.enqueue("c1", "anchor", { ...request, toolCallId: "call-7" }, "summary");
  assert.equal(queue.list("c1")[0]?.request.toolCallId, "call-7");
  queue.dispose();
});

test("the tool call id is display only: a remembered grant still matches another call", async () => {
  const { createToolAuthorization, createToolAuthorizationStore } = await import("../src/domain/ai/tool-authorization.ts");
  const store = createToolAuthorizationStore();
  const base = { conversationId: "c1", toolName: "read", resourceId: "fs:global", parameters: { path: "a" }, risk: "read" };
  const authorization = await createToolAuthorization("session", { ...base, toolCallId: "call-1" });
  assert.equal("toolCallId" in authorization, false, "the remembered grant does not store the call id");
  store.add(authorization);
  assert.equal(await store.consume({ ...base, toolCallId: "call-2" }), true);
});

test("both native and MCP approvals pass the model's call id", async () => {
  const { readFile } = await import("node:fs/promises");
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.equal(drawer.match(/^\s*toolCallId: call\.id,$/gm)?.length, 2);
});
