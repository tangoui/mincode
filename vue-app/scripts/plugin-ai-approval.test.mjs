import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { pluginCapabilities } from "../src/services/ai-tools/plugin-tools.ts";
import { requiresAiToolApproval } from "../src/domain/ai/approval-policy.ts";

function setup(controller = new AbortController(), cancelOnPrepare = false) {
  const calls = [];
  const capability = pluginCapabilities({ invoke: async (command, args) => {
    calls.push({ command, args });
    if (command === "plugin_host_ai_prepare") {
      if (cancelOnPrepare) controller.abort();
      return { ticket: "private-ticket", risk: "read", permissions: [], description: "Search" };
    }
    return { result: "ok" };
  } }, true).find((item) => item.tool.name === "plugin.invoke");
  return { calls, capability, context: { input: { id: "kb", action: "search", payload: '{"query":"test"}' }, signal: controller.signal } };
}

test("plugin invocation uses the existing AI approval mode without modal confirmation", () => {
  const { capability } = setup();
  assert.equal(requiresAiToolApproval("ask", capability.risk), true);
  assert.equal(requiresAiToolApproval("agent", capability.risk), true);
  assert.equal(requiresAiToolApproval("full-access", capability.risk), false);
  const source = readFileSync(new URL('../src/services/ai-tools/plugin-tools.ts', import.meta.url), 'utf8');
  assert.doesNotMatch(source, /ElMessageBox|\.confirm\(/);
});

test("authorized execution preserves exact payload and private single-use ticket", async () => {
  const { calls, capability, context } = setup();
  assert.deepEqual(await capability.execute(context), { result: "ok" });
  assert.deepEqual(calls, [
    { command: "plugin_host_ai_prepare", args: { id: "kb", action: "search", payload: context.input.payload } },
    { command: "plugin_host_ai_execute", args: { ticket: "private-ticket", approved: true } },
  ]);
});

test("cancellation before preparation does not invoke the backend", async () => {
  const controller = new AbortController(); controller.abort();
  const { calls, capability, context } = setup(controller);
  await assert.rejects(capability.execute(context), /已取消/);
  assert.equal(calls.length, 0);
});

test("cancellation during preparation rejects the ticket instead of executing", async () => {
  const { calls, capability, context } = setup(new AbortController(), true);
  await assert.rejects(capability.execute(context), /已取消/);
  assert.deepEqual(calls[1], { command: "plugin_host_ai_execute", args: { ticket: "private-ticket", approved: false } });
});

test("approval follows the invoked action's declared risk", async () => {
  const actions = [
    { id: "search", aiCallable: true, risk: "read" },
    { id: "save", aiCallable: true, risk: "write" },
    { id: "drop", aiCallable: true, risk: "dangerous" },
    { id: "hidden", aiCallable: false, risk: "read" },
  ];
  const capability = pluginCapabilities({ invoke: async (command) => {
    assert.equal(command, "plugin_host_describe");
    return { actions };
  } }, true).find((item) => item.tool.name === "plugin.invoke");
  const risk = (action) => capability.resolveRisk({ input: { id: "kb", action } });
  assert.equal(await risk("search"), "read");
  assert.equal(await risk("save"), "write");
  assert.equal(await risk("drop"), "dangerous");
  assert.equal(await risk("hidden"), "dangerous", "actions AI cannot call stay at the ceiling");
  assert.equal(await risk("missing"), "dangerous");
  assert.equal(requiresAiToolApproval("agent", await risk("save")), false);
  assert.equal(requiresAiToolApproval("agent", await risk("drop")), true);
  assert.equal(requiresAiToolApproval("full-access", await risk("drop")), false);
  const drawer = readFileSync(new URL('../src/components/ai/AiAssistantDrawer.vue', import.meta.url), 'utf8');
  assert.match(drawer, /const callRisk = capability\.resolveRisk\s*\?\s*await capability\.resolveRisk/);
  assert.match(drawer, /const callMode = approvalModeForConversation\(aiApprovalMode\.value, conversation\?\.external\);/);
  assert.match(drawer, /requiresAiToolApproval\(callMode, callRisk\)/);
});
