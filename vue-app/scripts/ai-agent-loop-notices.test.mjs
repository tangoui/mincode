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

const { runAiLoop } = await import("../src/services/ai-agent-loop.ts");

const config = { baseUrl: "http://localhost", apiKey: "k", model: "m", apiFormat: "openai" };
const tool = (name) => ({ name, description: name, inputSchema: { type: "object" } });

/** Each turn is a reply text or a list of tool calls ([name, argumentsJson]). */
function scriptedTurns(turns) {
  let turn = 0;
  return async (_config, _messages, _tools, onEvent) => {
    const next = turns[turn++];
    if (typeof next === "string") onEvent({ type: "chunk", text: next });
    else for (const [name, args] of next) onEvent({ type: "tool_call", id: `call-${turn}-${name}`, name, arguments: args ?? "{}" });
    onEvent({ type: "done" });
  };
}
const completion = (summary) => ["task_complete", JSON.stringify({ summary })];

async function runWithNotices(turns) {
  const notices = [];
  const result = await runAiLoop({
    config,
    messages: [{ role: "user", content: "do it" }],
    tools: [tool("a")],
    streamTurn: scriptedTurns(turns),
    executeTool: async (call) => ({ content: `${call.name} ok`, status: "completed" }),
    onLoopNotice: (kind) => notices.push(kind),
  });
  return { result, notices };
}

test("task_complete 与业务工具同一步调用时被退回，并通知界面", async () => {
  const { result, notices } = await runWithNotices([[["a"], completion("done")], [completion("done")]]);
  assert.deepEqual(notices, ["completion_rejected"]);
  assert.equal(result.outcome, "task_completed");
});

test("总结为空的 task_complete 被退回，并通知界面", async () => {
  const { notices } = await runWithNotices([[["a"]], [completion("")], [completion("ok")]]);
  assert.deepEqual(notices, ["completion_rejected"]);
});

test("只描述下一步而不调用工具时追加继续提示，并通知界面", async () => {
  const { result, notices } = await runWithNotices(["已找到相关代码，我接下来继续检查调用链。", "检查完成，没有问题。"]);
  assert.deepEqual(notices, ["continuation_nudged"]);
  assert.equal(result.outcome, "response");
});

test("正常完成时没有任何通知", async () => {
  const { notices } = await runWithNotices([[["a"]], [completion("ok")]]);
  assert.deepEqual(notices, []);
});

test("抽屉把循环通知写成系统步骤（模式事件），挂在当前位置", async () => {
  const { readFile } = await import("node:fs/promises");
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.match(drawer, /onLoopNotice: \(kind\) => \{[\s\S]*?if \(superseded\(\)\) return;[\s\S]*?ensureAssistantEventAnchor\(conversationId, eventTriggerMessageId\)[\s\S]*?kind: "mode",[\s\S]*?ai\.loopNotice\.completionRejected/);
});
