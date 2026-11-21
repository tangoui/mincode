import assert from "node:assert/strict";
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

const { runAiLoop, REDIRECTED_TOOL_RESULT } = await import("../src/services/ai-agent-loop.ts");

const config = { baseUrl: "http://localhost", apiKey: "k", model: "m", apiFormat: "openai" };
const tool = (name) => ({ name, description: name, inputSchema: { type: "object" } });

/** Each entry is one model turn: a list of tool-call names, or a final text reply. */
function scriptedTurns(turns) {
  let turn = 0;
  return async (_config, _messages, _tools, onEvent) => {
    const next = turns[turn++];
    if (typeof next === "string") {
      onEvent({ type: "chunk", text: next });
    } else {
      for (const name of next) onEvent({ type: "tool_call", id: `call-${name}`, name, arguments: "{}" });
    }
    onEvent({ type: "done" });
  };
}

test("a redirecting decline skips the not-yet-run calls of the same batch", async () => {
  const executed = [];
  const result = await runAiLoop({
    config,
    messages: [{ role: "user", content: "do it" }],
    tools: [tool("a"), tool("b"), tool("c")],
    streamTurn: scriptedTurns([["a", "b", "c"], "Understood, switching approach."]),
    executeTool: async (call) => {
      executed.push(call.name);
      if (call.name === "b") return { content: "declined: x", status: "cancelled", redirect: true };
      return { content: `${call.name} ok`, status: "completed" };
    },
  });
  assert.deepEqual(executed, ["a", "b"]);
  const toolMessages = result.history.filter((m) => m.role === "tool");
  assert.equal(toolMessages.find((m) => m.toolCallId === "call-c")?.content, REDIRECTED_TOOL_RESULT);
  assert.equal(toolMessages.find((m) => m.toolCallId === "call-c")?.toolResult?.status, "cancelled");
  assert.equal(toolMessages.find((m) => m.toolCallId === "call-b")?.content, "declined: x");
  assert.equal(toolMessages.find((m) => m.toolCallId === "call-a")?.toolResult?.status, "completed");
  assert.equal(result.outcome, "response");
});

test("calls of a parallel group that already started keep their real results", async () => {
  const executed = [];
  const result = await runAiLoop({
    config,
    messages: [{ role: "user", content: "do it" }],
    tools: [tool("a"), tool("b"), tool("c")],
    streamTurn: scriptedTurns([["a", "b", "c"], "Done."]),
    canExecuteToolInParallel: (call) => call.name !== "c",
    executeTool: async (call) => {
      executed.push(call.name);
      if (call.name === "a") return { content: "declined: x", status: "cancelled", redirect: true };
      await new Promise((resolve) => setTimeout(resolve, 5));
      return { content: `${call.name} ok`, status: "completed" };
    },
  });
  assert.deepEqual(executed, ["a", "b"]);
  const toolMessages = result.history.filter((m) => m.role === "tool");
  assert.equal(toolMessages.find((m) => m.toolCallId === "call-b")?.content, "b ok");
  assert.equal(toolMessages.find((m) => m.toolCallId === "call-c")?.content, REDIRECTED_TOOL_RESULT);
});

test("the redirect flag stays out of the model history", async () => {
  const result = await runAiLoop({
    config,
    messages: [{ role: "user", content: "do it" }],
    tools: [tool("a")],
    streamTurn: scriptedTurns([["a"], "Ok."]),
    executeTool: async () => ({ content: "declined: x", status: "cancelled", redirect: true }),
  });
  const message = result.history.find((m) => m.toolCallId === "call-a");
  assert.equal("redirect" in message, false);
  assert.equal("redirect" in (message.toolResult ?? {}), false);
});

test("the tool card shows the skipped-call note in the user's language; the model still gets the constant", async () => {
  const { readFile } = await import("node:fs/promises");
  const [drawer, zh, en] = await Promise.all([
    readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8"),
  ]);
  const onToolResult = drawer.slice(drawer.indexOf("onToolResult: (call, result) => {"), drawer.indexOf("onHistoryChange: saveLoopHistory"));
  assert.match(onToolResult, /result\.content === REDIRECTED_TOOL_RESULT \? t\("ai\.agentic\.redirectedToolResult"\) : result\.content/);
  assert.match(zh, /"ai\.agentic\.redirectedToolResult": "未执行：用户已调整任务方向。"/);
  assert.match(en, /"ai\.agentic\.redirectedToolResult": "Not run: you redirected the task."/);
});
