import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";
import { createCapabilityRegistry } from "../src/domain/ai/capability-registry.ts";
import { createQuestionQueue } from "../src/domain/ai/question-queue.ts";
import { availableAiCapabilityModules, selectAiCapabilityTools } from "../src/domain/ai/tool-selection.ts";
import { readSource } from "./read-source.mjs";

// The native registry imports the SQL confirmation dialog. This test exercises tool registration.
const hook = registerHooks({
  load(url, context, nextLoad) {
    if (url.endsWith("/SqlSafetyConfirmContent.vue")) {
      return { format: "module", shortCircuit: true, source: "export default {}" };
    }
    return nextLoad(url, context);
  },
});
const { registerNativeAiCapabilities } = await import("../src/services/ai-native-tools.ts");
hook.deregister();

const input = { questions: [{ header: "框架", question: "用哪个？" }] };
const toolOrigin = { toolCallId: "call-1", triggerMessageId: "message-1" };

function register(askUser) {
  const capabilities = createCapabilityRegistry();
  registerNativeAiCapabilities({ capabilities, askUser, listResources: () => [] });
  return capabilities;
}

test("ask_user is a read-only global capability and returns the answer text", async () => {
  const signal = new AbortController().signal;
  const capabilities = register(async (request) => {
    assert.equal(request.conversationId, "conv-1");
    assert.deepEqual(request.toolOrigin, toolOrigin);
    assert.equal(request.signal, signal);
    assert.equal(request.questions[0].input, "text");
    return "【框架】用哪个？ → Vue";
  });
  const capability = capabilities.get("ask_user");
  assert.equal(capability.target, "none");
  assert.equal(capability.risk, "read");
  assert.deepEqual(await capabilities.execute("ask_user", input, {
    conversationId: "conv-1", toolOrigin, signal,
  }), { result: "【框架】用哪个？ → Vue" });
});

test("invalid inputs and delegated calls never reach the question queue", async () => {
  const capabilities = register(() => assert.fail("unexpected prompt"));
  const execute = (args, origin) => capabilities.execute("ask_user", args, {
    conversationId: "conv-1", toolOrigin: origin,
  });
  await assert.rejects(execute({ questions: [] }, toolOrigin), /1.*4/);
  await assert.rejects(execute(input, { toolCallId: "child" }), {
    message: "子代理不能向用户提问，请把问题交回主会话",
  });
});

test("tool selection offers questions on task turns and excludes greetings", () => {
  const capabilities = register(async () => "").listCapabilities();
  const availableModules = availableAiCapabilityModules(capabilities, () => []);
  const select = (text) => selectAiCapabilityTools(capabilities, { text, availableModules })
    .some(({ name }) => name === "ask_user");
  assert.equal(select("帮我计划实现这个项目"), true);
  assert.equal(select("你好"), false);
});

test("aborting an executing ask_user settles the queued question and removes it", async () => {
  const queue = createQuestionQueue();
  const controller = new AbortController();
  const { promise: queued, resolve } = Promise.withResolvers();
  const capabilities = register(({ signal, conversationId, questions }) => {
    const { outcome } = queue.enqueue({ conversationId, questions, source: "native" }, signal);
    resolve(outcome);
    return outcome.then(() => signal.throwIfAborted());
  });
  const running = capabilities.execute("ask_user", input, {
    conversationId: "conv-1", toolOrigin, signal: controller.signal,
  });
  await new Promise((finish) => queue.subscribe(finish));
  assert.equal(queue.list("conv-1").length, 1);
  controller.abort();
  await assert.rejects(running, { name: "AbortError" });
  assert.deepEqual(await queued, { kind: "cancelled" });
  assert.deepEqual(queue.list(), []);
});

test("the drawer hooks native questions into the run signal and excludes them from parallel tool batches", async () => {
  const drawer = await readSource(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.match(drawer, /askUser: askNativeAiQuestion/);
  assert.match(drawer, /questionQueue\.enqueue\([\s\S]*?source: "native",[\s\S]*?}, signal\)/);
  assert.match(drawer, /canExecuteToolInParallel: \(call\) => call\.name !== ASK_USER_TOOL_NAME/);
  assert.match(drawer, /questionQueue\.cancelConversation\(conversationId\)/);
});
