import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

async function source(path) {
  return readFile(new URL(path, import.meta.url), "utf8");
}

test("会话运行态只由活跃 AgentRun 投影", async () => {
  const [storeSource, drawerSource] = await Promise.all([
    source("../src/stores/ai-assistant.ts"),
    source("../src/components/ai/AiAssistantDrawer.vue"),
  ]);

  assert.match(storeSource, /const sessionProgress = useSessionProgressStore\(\)/);
  assert.match(storeSource, /for \(const run of sessionProgress\.activeRuns\)/);
  // 投影只在“哪些会话在运行 / 有哪些会话”变化时才遍历全部会话，而不是每个进度事件都遍历。
  assert.match(
    storeSource,
    /watch\(\s*\[runningConversationIdsKey, conversationIdsKey, globalOverviewConversationIdsKey\],[\s\S]*?flush: "sync"/,
  );
  assert.match(storeSource, /activeConversationIds\.add\(run\.navigation\.conversationId\)/);
  assert.match(storeSource, /syncRunningConversationIds\(activeConversationIds\)/);
  assert.doesNotMatch(storeSource, /setConversationRunning[,:()]/);
  assert.doesNotMatch(drawerSource, /setConversationRunning\(/);
});

test("AgentRun 权威返回值会立即写回 progress store", async () => {
  const drawerSource = await source("../src/components/ai/AiAssistantDrawer.vue");
  const beginStart = drawerSource.indexOf("async function beginConversationRun");
  const reportStart = drawerSource.indexOf("function reportConversationRun", beginStart);
  const beginSource = drawerSource.slice(beginStart, reportStart);
  const releaseStart = drawerSource.indexOf("function releaseConversationRun", reportStart);
  const reportSource = drawerSource.slice(reportStart, releaseStart);

  assert.match(beginSource, /const run = await startAgentRun[\s\S]*progress\.apply\(run\)/);
  assert.match(reportSource, /\.then\(\(run\) => \{[\s\S]*progress\.apply\(run\)/);
});

test("终态上报后由发送锁恢复队列，不受滞后的运行态投影阻塞", async () => {
  const drawerSource = await source("../src/components/ai/AiAssistantDrawer.vue");
  const recoveryStart = drawerSource.indexOf("async function recoverStalledQueueInterruption");
  const recoveryEnd = drawerSource.indexOf("async function interruptConversationAndRunQueue", recoveryStart);
  const recoverySource = drawerSource.slice(recoveryStart, recoveryEnd);

  assert.match(recoverySource, /await reportConversationRun\(conversationId, "cancelled"/);
  assert.doesNotMatch(recoverySource, /releaseConversationRun\(conversationId\)/);
  assert.ok(
    recoverySource.indexOf('await reportConversationRun(conversationId, "cancelled"')
      < recoverySource.indexOf("runNextQueuedMessage(conversationId)"),
  );
  assert.match(recoverySource, /if \(runQueue\) \{/);
  assert.doesNotMatch(recoverySource, /runQueue && !assistant\.conversationById/);
});
