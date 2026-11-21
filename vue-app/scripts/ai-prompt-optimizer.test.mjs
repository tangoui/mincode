import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

const [service, composer, drawer] = await Promise.all([
  readFile(new URL("../src/services/ai-prompt-optimizer.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiPromptComposer.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
]);

test("提示词优化使用独立无工具流并限制短任务预算", () => {
  assert.match(service, /messages: promptOptimizationMessages\(prompt\)/);
  assert.match(service, /tools: \[\]/);
  assert.match(service, /reasoningEffort: undefined/);
  assert.match(service, /PROMPT_OPTIMIZER_MAX_OUTPUT_TOKENS = 1_600/);
  assert.match(service, /event\.type === "chunk"/);
  assert.match(service, /signal: options\.signal/);
});

test("优化系统提示保留原意且不读取附件和会话历史", () => {
  assert.match(service, /不改变用户原始意图/);
  assert.match(service, /不虚构缺失信息/);
  assert.match(service, /只输出优化后的提示词/);
  assert.match(service, /<original_prompt>/);
  assert.doesNotMatch(service, /attachments:/);
});

test("输入框提供优化、停止、对比和确认替换交互", () => {
  assert.match(composer, /"optimize-prompt": \[\]/);
  assert.match(composer, /"cancel-optimization": \[\]/);
  assert.match(composer, /optimizingPrompt \? VideoPause : MagicStick/);
  assert.match(drawer, /async function startPromptOptimization/);
  assert.match(drawer, /promptOptimizationController\?\.abort\(\)/);
  assert.match(drawer, /v-model="promptOptimizationVisible"/);
  assert.match(drawer, /function applyOptimizedPrompt[\s\S]*input\.value = promptOptimizationResult\.value\.trim\(\)/);
});

test("优化按钮位于右侧主操作区并排在发送按钮之前", () => {
  const toolsEnd = composer.indexOf('</div>\n      <div class="ai-assistant-composer__primary-actions">');
  const optimizeButton = composer.indexOf('class="ai-assistant-composer__optimize"');
  const sendButton = composer.indexOf('class="ai-assistant-composer__send"');

  assert.ok(toolsEnd > 0, "右侧主操作区不存在");
  assert.ok(optimizeButton > toolsEnd, "优化按钮仍位于左侧工具区");
  assert.ok(sendButton > optimizeButton, "优化按钮应位于发送按钮之前");
});
