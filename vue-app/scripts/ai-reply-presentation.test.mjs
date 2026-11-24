import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";
import { readFile } from "node:fs/promises";

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


test("a reply that ends a turn which used tools reports usedTools; plain chat does not", async () => {
  const worked = await runAiLoop({
    config,
    messages: [{ role: "user", content: "fix it" }],
    tools: [tool("edit")],
    streamTurn: scriptedTurns([["edit"], "Changed drawer.css; not verified in the app."]),
    executeTool: async () => ({ content: "ok", status: "completed" }),
  });
  assert.equal(worked.outcome, "response");
  assert.equal(worked.usedTools, true);

  const chat = await runAiLoop({
    config,
    messages: [{ role: "user", content: "hi" }],
    tools: [tool("edit")],
    streamTurn: scriptedTurns(["Hello!"]),
    executeTool: async () => ({ content: "ok", status: "completed" }),
  });
  assert.equal(chat.outcome, "response");
  assert.equal(chat.usedTools, false);
});

test("work replies are shown as the task summary, plain chat stays a normal reply", async () => {
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.match(drawer, /result\.outcome === "response"[\s\S]{0,700}if \(result\.usedTools && lastAssistant\.content\.trim\(\)\) assistant\.setMessagePresentation\(conversationId, lastAssistant\.id, "task-summary"\)/);
  const store = await readFile(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8");
  assert.match(store, /function setMessagePresentation\(/);
});

test("the task summary follows the user's language", async () => {
  const loop = await readFile(new URL("../src/services/ai-agent-loop.ts", import.meta.url), "utf8");
  assert.match(loop, /Write the summary in the language the user wrote in/);
});

test("assistant replies and task summaries share the plain message body", async () => {
  const css = await readFile(new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url), "utf8");
  const bubble = await readFile(new URL("../src/components/ai/AiMessageBubble.vue", import.meta.url), "utf8");
  const rule = css.match(/\.ai-message-card\.is-assistant\s*\{([^}]*)\}/)?.[1] ?? "";
  assert.match(bubble, /:is="message\.role === 'assistant' \? 'div' : ElCard"/);
  assert.match(rule, /background: transparent/);
  assert.doesNotMatch(rule, /success|green/);
});
