import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

function readSource(path) {
  return readFile(new URL(`../${path}`, import.meta.url), "utf8");
}

// 运行中仍要能改模型：模型只在发送时读取一次，切换只影响下一条消息。
test("the built-in model picker stays usable while a conversation is running", async () => {
  const composer = await readSource("src/components/ai/AiPromptComposer.vue");
  const start = composer.indexOf('v-if="modelSelectionSupported"');
  assert.ok(start > 0, "model picker dropdown not found");
  const end = composer.indexOf("</el-dropdown>", start);
  const picker = composer.slice(start, end);

  assert.ok(
    !/:disabled="loading"/.test(picker),
    "the built-in model picker must not be disabled while loading",
  );
  assert.match(picker, /ai-assistant-model-menu__trigger/);
});

// 模型在发送瞬间解析并被请求闭包捕获，运行中切换不会改写进行中的请求。
test("the request plan resolves models once per send", async () => {
  const drawer = await readSource("src/components/ai/AiAssistantDrawer.vue");
  assert.match(drawer, /const \{ profile, config: requestConfig, attemptModels \} = aiRequestPlan\(conversationId\);/);
  assert.match(drawer, /models: attemptModels,/);
});
