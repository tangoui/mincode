import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { registerHooks } from "node:module";
import test from "node:test";

// Run the real loop with model I/O replaced by a fake streamTurn; only the IPC
// transport modules (unused when streamTurn is injected) are stubbed out.
const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
const desktopBridgeStub = `
  export const invokeBackend = async () => undefined;
  export const listenAppEvent = () => () => {};
  export const isDesktopRuntime = () => false;
`;
const desktopStreamStub = `
  export const isStreamAvailable = () => false;
  export class JSONStream {}
  export const reportStreamDiagnostic = () => {};
`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/desktop-bridge") return { url: dataModule(desktopBridgeStub), shortCircuit: true };
    if (specifier === "@/services/desktop-stream") return { url: dataModule(desktopStreamStub), shortCircuit: true };
    return nextResolve(specifier, context);
  },
});

const { runAiLoop } = await import("../src/services/ai-agent-loop.ts");
const { createPlanModeReminders } = await import("../src/domain/ai/plan-mode-reminders.ts");

const config = { baseUrl: "http://localhost", apiKey: "k", model: "m", apiFormat: "openai" };
const tool = (name) => ({ name, description: name, inputSchema: { type: "object" } });

test("plan reminders repeat while in plan mode and announce leaving once", () => {
  let mode = "plan";
  const next = createPlanModeReminders(() => mode, { enter: "E", leave: "L" });
  assert.equal(next(), "E");
  assert.equal(next(), "E");
  mode = "agent";
  assert.equal(next(), "L");
  assert.equal(next(), undefined);
});

test("plan reminders stay silent when plan mode was never active", () => {
  let mode = "ask";
  const next = createPlanModeReminders(() => mode, { enter: "E", leave: "L" });
  assert.equal(next(), undefined);
  mode = "full-access";
  assert.equal(next(), undefined);
});

test("a reminder is sent with each request but never stored in the loop history", async () => {
  const seen = [];
  let turn = 0;
  const result = await runAiLoop({
    config,
    messages: [{ role: "system", content: "base" }, { role: "user", content: "look around" }],
    tools: [tool("a")],
    executeTool: async () => ({ content: "a ok", status: "completed" }),
    // First turn calls a tool so the loop makes a second request; the second replies with text.
    streamTurn: async (_config, messages, _tools, onEvent) => {
      seen.push(messages.map((message) => ({ ...message })));
      if (turn++ === 0) onEvent({ type: "tool_call", id: "call-a", name: "a", arguments: "{}" });
      else onEvent({ type: "chunk", text: "Here is the plan." });
      onEvent({ type: "done" });
    },
    systemReminder: () => "E",
  });
  assert.equal(seen.length, 2);
  for (const request of seen) {
    assert.deepEqual(request.at(-1), { role: "system", content: "E" });
    assert.equal(request.filter((m) => m.content === "E").length, 1);
  }
  assert.ok(!result.history.some((m) => m.content === "E"));
});

test("without a reminder the model receives the history unchanged", async () => {
  const seen = [];
  await runAiLoop({
    config,
    messages: [{ role: "user", content: "hi" }],
    tools: [],
    executeTool: async () => ({ content: "", status: "completed" }),
    streamTurn: async (_config, messages, _tools, onEvent) => {
      seen.push(messages.map((message) => ({ ...message })));
      onEvent({ type: "chunk", text: "hello" });
      onEvent({ type: "done" });
    },
    systemReminder: () => undefined,
  });
  assert.equal(seen.length, 1);
  assert.equal(seen[0].at(-1).role, "user");
});

test("one reminder source shared across turns announces leaving plan mode on the next turn", async () => {
  let mode = "plan";
  const reminder = createPlanModeReminders(() => mode, { enter: "E", leave: "L" });
  const runTurn = async (seen) => runAiLoop({
    config,
    messages: [{ role: "user", content: "next" }],
    tools: [],
    executeTool: async () => ({ content: "", status: "completed" }),
    streamTurn: async (_config, messages, _tools, onEvent) => {
      seen.push(messages.map((message) => ({ ...message })));
      onEvent({ type: "chunk", text: "ok" });
      onEvent({ type: "done" });
    },
    systemReminder: reminder,
  });
  const first = [];
  await runTurn(first);
  assert.equal(first[0].at(-1).content, "E");
  // The user switches modes between turns, then sends "go ahead".
  mode = "agent";
  const second = [];
  const result = await runTurn(second);
  assert.deepEqual(second[0].at(-1), { role: "system", content: "L" });
  assert.ok(!result.history.some((m) => m.content === "L"));
  const third = [];
  await runTurn(third);
  assert.equal(third[0].at(-1).role, "user");
});

test("the drawer keeps one main-loop reminder per conversation instead of one per send", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.match(drawer, /const planModeReminders = new Map<string, \(\) => string \| undefined>\(\);/);
  assert.match(
    drawer,
    /function conversationPlanModeReminder\(conversationId: string\) \{\s*let reminder = planModeReminders\.get\(conversationId\);/,
  );
  assert.match(drawer, /systemReminder: conversationPlanModeReminder\(conversationId\),/);
  assert.doesNotMatch(drawer, /systemReminder: planModeRemindersFor\(/, "a per-send instance loses the leave notice");
  // Sub-agents still get a fresh source per loop.
  assert.match(drawer, /createSystemReminder: \(\) => planModeRemindersFor\(conversationId\),/);
  // Deleting a conversation drops its reminder source.
  assert.match(drawer, /async function deleteConversation\([^]*?planModeReminders\.delete\(conversation\.id\);[^]*?\n\}/);
});
