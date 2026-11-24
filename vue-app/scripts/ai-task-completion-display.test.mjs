import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const [dashboard, store, drawer] = await Promise.all([
  readFile(new URL("../src/views/DashboardView.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8"),
  readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
]);

test("环境检测卡片不显示无功能的设置齿轮", () => {
  assert.doesNotMatch(dashboard, /<Setting\s*\/>/);
  assert.doesNotMatch(dashboard, /import[\s\S]*\bSetting\b/);
});

test("任务完成时只展示 task_complete 总结，移除同轮临时普通回复", () => {
  assert.match(store, /function removeMessageFromConversation\(id: string, messageId: string, moveEventsTo\?: string\)/);
  assert.match(drawer, /completedTurnAssistantMessageId/);
  assert.match(drawer, /call\.name !== "task_complete"/);
  assert.match(drawer, /if \(hasBusinessToolCall\) completedTurnAssistantMessageId = undefined/);
  // 先新增总结再删除临时回复，临时回复上的事件改挂到总结上。
  assert.match(
    drawer,
    /const summaryMessageId = assistant\.addMessageToConversation\([\s\S]*?presentation: "task-summary"[\s\S]*?assistant\.removeMessageFromConversation\(conversationId, completedTurnAssistantMessageId, summaryMessageId\)/,
  );
});

test("撞上步数上限时展示上限说明加收尾总结，且总结只出现一次", () => {
  const branch = drawer.slice(
    drawer.indexOf('} else if (result.outcome === "loop_limit") {'),
    drawer.indexOf("} catch (error) {", drawer.indexOf('} else if (result.outcome === "loop_limit") {')),
  );
  assert.ok(branch.length > 0);
  assert.doesNotMatch(drawer, /t\("ai\.agentic\.loopLimit"\)/);
  assert.match(branch, /t\("ai\.agentic\.loopLimitReached", \{ steps: result\.loopCount \}\)/);
  assert.match(branch, /content: `\$\{t\("ai\.agentic\.loopLimitReached", \{ steps: result\.loopCount \}\)\}\\n\\n\$\{result\.summary\}`/);
  // 收尾回复已作为普通助手消息流式展示；拼接说明后的消息替代它，避免总结出现两次。
  assert.match(
    branch,
    /message\.id === completedTurnAssistantMessageId[\s\S]*?addMessageToConversation[\s\S]*?streamedSummary\.content\.trim\(\) === result\.summary\.trim\(\)[\s\S]*?removeMessageFromConversation\(conversationId, streamedSummary\.id, loopLimitMessageId\)/,
  );
  // 只有一条拼好的消息：分支里只新增这一条助手消息。
  assert.equal(branch.match(/addMessageToConversation\(/g)?.length, 1);
});

test("步数上限消息带持久化的 loop-limit 标记，并在线程里提供继续按钮", async () => {
  const [thread, zh, en] = await Promise.all([
    readFile(new URL("../src/components/ai/AiMessageThread.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/i18n/locales/zh.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/i18n/locales/en.ts", import.meta.url), "utf8"),
  ]);
  const branch = drawer.slice(
    drawer.indexOf('} else if (result.outcome === "loop_limit") {'),
    drawer.indexOf("} catch (error) {", drawer.indexOf('} else if (result.outcome === "loop_limit") {')),
  );
  // 标记随消息持久化，重新加载后按钮仍可由消息本身推导出来。
  assert.match(branch, /presentation: "loop-limit"/);
  assert.match(store, /export type AiAssistantMessagePresentation = "task-summary" \| "error" \| "loop-limit";/);
  assert.match(store, /value === "task-summary" \|\| value === "error" \|\| value === "loop-limit"/);
  // 只在最后一条消息上显示，运行中禁用。
  assert.match(
    thread,
    /const index = props\.messages\.length - 1;\s*const last = props\.messages\[index\];\s*return props\.continueLabel && last\?\.role === "assistant" && last\.presentation === "loop-limit" \? index : null;/,
  );
  assert.match(thread, /:disabled="!canContinue"[\s\S]*?@click="emit\('continue'\)"/);
  assert.match(drawer, /:can-continue="canContinueActiveConversation"/);
  assert.match(drawer, /:continue-label="t\('ai\.agentic\.continue'\)"/);
  assert.match(drawer, /@continue="continueAfterLoopLimit"/);
  // 继续走正常的 sendAgentic 路径，发一条本地化的后续消息。
  const handler = drawer.slice(drawer.indexOf("function continueAfterLoopLimit"), drawer.indexOf("function continueAfterLoopLimit") + 900);
  assert.match(handler, /if \(!canContinueActiveConversation\.value\) return;/);
  assert.match(handler, /sendAgentic\(\{\s*conversationId,\s*content: t\("ai\.agentic\.continuePrompt"\),\s*attachments: \[\],?\s*\}\)/);
  for (const locale of [zh, en]) {
    assert.match(locale, /"ai\.agentic\.continue":/);
    assert.match(locale, /"ai\.agentic\.continuePrompt":/);
    assert.match(locale, /"ai\.agentic\.loopLimitReached":\s*\n?\s*".*\{steps\}/);
  }
  assert.doesNotMatch(zh, /要继续，请发送“继续”/);
});
