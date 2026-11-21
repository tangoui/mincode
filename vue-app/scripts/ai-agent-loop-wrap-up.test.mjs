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

const { runAiLoop, LOOP_LIMIT_WRAP_UP_PROMPT } = await import("../src/services/ai-agent-loop.ts");

const config = { baseUrl: "http://localhost", apiKey: "k", model: "m", apiFormat: "openai" };
const messages = [{ role: "user", content: "do the work" }];
const workTool = { name: "work", description: "work", inputSchema: { type: "object" } };
const executeTool = async () => ({ content: "ok", status: "completed" });
const FALLBACK_SUMMARY = "The task stopped after reaching the safety loop limit.";
/** The wrap-up request is the one whose last user message is the wrap-up prompt. */
const isWrapUp = (history) => history.findLast((message) => message.role === "user")?.content === LOOP_LIMIT_WRAP_UP_PROMPT;

test("the wrap-up prompt is the fixed English protocol string", () => {
  assert.equal(
    LOOP_LIMIT_WRAP_UP_PROMPT,
    "The step limit for this task has been reached. Tools are disabled. Reply with text only: (1) what was accomplished, (2) what remains unfinished, (3) recommended next steps.",
  );
});

test("hitting the loop limit asks once more for a text-only summary and returns it", async () => {
  const calls = [];
  const toolLists = [];
  const requests = [];
  const snapshots = [];
  const streamTurn = async (_config, history, tools, onEvent) => {
    calls.push(tools.length);
    toolLists.push(tools.map((tool) => tool.name));
    requests.push(history.map((message) => ({ role: message.role, content: message.content })));
    if (isWrapUp(history)) onEvent({ type: "chunk", text: "Did A; B remains; next do C." });
    else onEvent({ type: "tool_call", id: `t${calls.length}`, name: "work", arguments: "{}" });
    onEvent({ type: "done" });
  };
  const result = await runAiLoop({
    config,
    messages,
    tools: [workTool],
    executeTool,
    streamTurn,
    systemReminder: () => "REMINDER",
    onHistoryChange: (history) => snapshots.push(history),
    maxLoops: 3,
  });
  // One business tool plus the built-in task_complete tool per request. The wrap-up keeps
  // the same definitions: Anthropic rejects tool blocks in history without them.
  assert.deepEqual(calls, [2, 2, 2, 2]);
  assert.deepEqual(toolLists[3], toolLists[0]);
  assert.equal(result.completed, false);
  assert.equal(result.outcome, "loop_limit");
  assert.equal(result.summary, "Did A; B remains; next do C.");
  assert.equal(result.loopCount, 3);

  // The wrap-up request carries the prompt and the per-request reminder, which is not stored.
  assert.deepEqual(requests[3].slice(-2), [
    { role: "user", content: LOOP_LIMIT_WRAP_UP_PROMPT },
    { role: "system", content: "REMINDER" },
  ]);
  assert.deepEqual(result.history.slice(-2).map(({ role, content }) => ({ role, content })), [
    { role: "user", content: LOOP_LIMIT_WRAP_UP_PROMPT },
    { role: "assistant", content: "Did A; B remains; next do C." },
  ]);
  assert.equal(result.history.some((message) => message.role === "system"), false);
  assert.equal(result.history.at(-1).toolCalls, undefined);
  assert.deepEqual(snapshots.at(-1).slice(-1).map(({ role, content }) => ({ role, content })), [
    { role: "assistant", content: "Did A; B remains; next do C." },
  ]);
});

test("a failing wrap-up falls back to the previous summary", async () => {
  const calls = [];
  const streamTurn = async (_config, history, tools, onEvent) => {
    calls.push(tools.length);
    if (isWrapUp(history)) throw new Error("upstream unavailable");
    onEvent({ type: "tool_call", id: `t${calls.length}`, name: "work", arguments: "{}" });
    onEvent({ type: "done" });
  };
  const result = await runAiLoop({ config, messages, tools: [workTool], executeTool, streamTurn, maxLoops: 3 });
  assert.deepEqual(calls, [2, 2, 2, 2]);
  assert.equal(result.outcome, "loop_limit");
  assert.equal(result.completed, false);
  assert.equal(result.summary, FALLBACK_SUMMARY);
  // No dangling wrap-up request: the history still ends with the last tool result.
  assert.equal(result.history.some((message) => message.content === LOOP_LIMIT_WRAP_UP_PROMPT), false);
  assert.equal(result.history.at(-1).role, "tool");
});

test("a failing wrap-up keeps the last assistant text as the summary", async () => {
  let call = 0;
  const streamTurn = async (_config, history, _tools, onEvent) => {
    call += 1;
    if (isWrapUp(history)) throw new Error("upstream unavailable");
    onEvent({ type: "chunk", text: `step ${call}` });
    onEvent({ type: "tool_call", id: `t${call}`, name: "work", arguments: "{}" });
    onEvent({ type: "done" });
  };
  const result = await runAiLoop({ config, messages, tools: [workTool], executeTool, streamTurn, maxLoops: 2 });
  assert.equal(result.outcome, "loop_limit");
  assert.equal(result.summary, "step 2");
});

test("an aborted run does not send a wrap-up request", async () => {
  const controller = new AbortController();
  const calls = [];
  const streamTurn = async (_config, _history, tools, onEvent) => {
    calls.push(tools.length);
    onEvent({ type: "tool_call", id: `t${calls.length}`, name: "work", arguments: "{}" });
    onEvent({ type: "done" });
  };
  await assert.rejects(
    runAiLoop({
      config,
      messages,
      tools: [workTool],
      executeTool,
      streamTurn,
      signal: controller.signal,
      // Stop right after the third turn's tool results are recorded.
      onHistoryChange: (history) => {
        if (calls.length === 3 && history.at(-1).role === "tool") controller.abort();
      },
      maxLoops: 3,
    }),
    { name: "AbortError" },
  );
  assert.equal(calls.length, 3);
});

test("the wrap-up only collects text, ignoring any tool call the model still emits", async () => {
  const announced = [];
  const executed = [];
  const streamTurn = async (_config, history, _tools, onEvent) => {
    const wrapUp = isWrapUp(history);
    if (wrapUp) onEvent({ type: "chunk", text: "Summary." });
    onEvent({ type: "tool_call", id: wrapUp ? "late-call" : "work-call", name: "work", arguments: "{}" });
    onEvent({ type: "done" });
  };
  const result = await runAiLoop({
    config,
    messages,
    tools: [workTool],
    executeTool: async (call) => {
      executed.push(call.id);
      return executeTool();
    },
    streamTurn,
    onToolCall: (call) => announced.push(call.id),
    maxLoops: 1,
  });
  // The wrap-up still receives tool definitions, so a late tool call must not run or be announced.
  assert.deepEqual(announced, ["work-call"]);
  assert.deepEqual(executed, ["work-call"]);
  assert.equal(result.summary, "Summary.");
  assert.equal(result.history.at(-1).toolCalls, undefined);
});
