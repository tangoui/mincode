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

test("工具调用生成中的进度交给界面，不进入模型历史", async () => {
  const progress = [];
  let turn = 0;
  const result = await runAiLoop({
    config,
    messages: [{ role: "user", content: "write it" }],
    tools: [tool("write_file")],
    streamTurn: async (_config, _messages, _tools, onEvent) => {
      turn += 1;
      if (turn === 1) {
        onEvent({ type: "tool_call_progress", index: 0, name: "write_file", bytes: 0 });
        onEvent({ type: "tool_call_progress", index: 0, name: "write_file", bytes: 2048 });
        onEvent({ type: "tool_call", id: "c1", name: "write_file", arguments: "{}" });
      } else {
        onEvent({ type: "chunk", text: "done" });
      }
      onEvent({ type: "done" });
    },
    executeTool: async () => ({ content: "ok", status: "completed" }),
    onToolCallProgress: (item) => progress.push(item),
  });
  assert.deepEqual(progress, [{ index: 0, name: "write_file", bytes: 0 }, { index: 0, name: "write_file", bytes: 2048 }]);
  assert.equal(JSON.stringify(result.history).includes("tool_call_progress"), false);
});

test("收尾的纯文本请求忽略工具进度", async () => {
  const { readFile } = await import("node:fs/promises");
  const loop = await readFile(new URL("../src/services/ai-agent-loop.ts", import.meta.url), "utf8");
  assert.match(loop, /event\.type === "tool_call_progress" && !textOnly/);
});

test("抽屉：进度生成占位步骤，最终调用、整步结束、出错和换模型时都清掉", async () => {
  const { readFile } = await import("node:fs/promises");
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.match(drawer, /onToolCallProgress: \(progress\) => \{[\s\S]*?id: `loop-tool-preparing-\$\{progress\.index\}`/);
  // 被接管的旧运行不碰会话：清理都在 superseded 检查之后；遗留占位由新运行开始时统一收口为已取消。
  assert.match(drawer, /onToolCall: \(call\) => \{\s*streamTextBatcher\.flush\(\);\s*if \(superseded\(\)\) return;\s*clearPreparingTools\(\);/);
  assert.match(drawer, /onTurnComplete: \(message\) => \{\s*streamTextBatcher\.flush\(\);\s*if \(superseded\(\)\) return;\s*clearPreparingTools\(\);/);
  assert.match(drawer, /\} catch \(error\) \{\s*streamTextBatcher\.flush\(\);\s*if \(superseded\(\)\) return;\s*clearPreparingTools\(\);/);
  assert.match(drawer, /onFallback: \(from, to\) => \{\s*if \(!superseded\(\)\) clearPreparingTools\(\);/);
});
