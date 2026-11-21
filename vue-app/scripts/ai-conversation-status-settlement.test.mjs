import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const drawerSource = await readFile(
  new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url),
  "utf8",
);

function functionSource(startMarker, endMarker) {
  const start = drawerSource.indexOf(startMarker);
  const end = drawerSource.indexOf(endMarker, start + startMarker.length);
  assert.notEqual(start, -1, `missing ${startMarker}`);
  assert.notEqual(end, -1, `missing ${endMarker}`);
  return drawerSource.slice(start, end);
}

test("Agentic 最终状态在运行记录上报前本地收口", () => {
  const source = functionSource(
    "async function sendAgentic(",
    "async function startAgent(",
  );
  const settleIndex = source.indexOf(
    'assistant.setConversationStatus(conversationId, result.completed ? "completed" : "interrupted")',
  );
  const outcomeIndex = source.indexOf('if (result.outcome === "task_completed")');
  const reportIndex = source.indexOf("await reportConversationRun(", outcomeIndex);

  assert.notEqual(settleIndex, -1);
  assert.notEqual(outcomeIndex, -1);
  assert.notEqual(reportIndex, -1);
  assert.ok(
    settleIndex < outcomeIndex && settleIndex < reportIndex,
    "最终内容已产生时应先更新会话状态，不能被运行记录上报阻塞",
  );
});
