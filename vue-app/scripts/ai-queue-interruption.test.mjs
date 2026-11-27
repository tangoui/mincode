import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const drawerSource = await readFile(
  new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url),
  "utf8",
);

test("立即发送对重复中断请求保持幂等", () => {
  assert.match(drawerSource, /const queueInterruptions = new Map<string, Promise<void>>\(\)/);
  assert.match(drawerSource, /const existing = queueInterruptions\.get\(conversationId\);[\s\S]*?if \(existing\) return existing/);
  assert.match(drawerSource, /queueInterruptions\.delete\(conversationId\)/);
});

test("中断句柄丢失或取消超时后会恢复状态并继续队列", () => {
  assert.match(drawerSource, /QUEUE_INTERRUPT_GRACE_MS = 2_000/);
  assert.match(drawerSource, /if \(!stopped\) await recoverStalledQueueInterruption\(conversationId\)/);
  assert.match(drawerSource, /recoverStalledQueueInterruption[\s\S]*?setConversationStatus\(conversationId, "interrupted"\)[\s\S]*?await reportConversationRun\(conversationId, "cancelled"[\s\S]*?runNextQueuedMessage\(conversationId\)/);
});

test("旧任务收尾不会清理超时恢复后启动的新任务", () => {
  assert.match(drawerSource, /agenticAborts\.get\(conversationId\) === abortController/);
  assert.match(drawerSource, /acpPromptAborts\.get\(conversationId\) === abortController/);
});

test("被超时恢复取代的旧任务不会改写新任务的状态、历史和运行记录", () => {
  assert.match(drawerSource, /const superseded = \(\) => agenticAborts\.get\(conversationId\) !== abortController;/);
  assert.match(drawerSource, /const superseded = \(\) => acpPromptAborts\.get\(conversationId\) !== abortController;/);
  assert.match(drawerSource, /const saveLoopHistory = [\s\S]*?if \(superseded\(\)\) return;\s*assistant\.setConversationAgentHistory/);
  assert.match(drawerSource, /if \(superseded\(\)\) return;\s*saveLoopHistory\(result\.history\);/);
  assert.match(drawerSource, /catch \(error\) \{\s*streamTextBatcher\.flush\(\);\s*if \(superseded\(\)\) return;/);
  assert.match(drawerSource, /if \(superseded\(\)\) return;\s*if \(!slashCommand\) acpContextInjected\.add\(conversationId\);/);
  assert.match(drawerSource, /catch \(error\) \{\s*acpStreamTextBatcher\.flush\(\);\s*if \(superseded\(\)\) return;/);
});
