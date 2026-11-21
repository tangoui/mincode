import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const threadPath = new URL("../src/components/ai/AiMessageThread.vue", import.meta.url);

test("持续追加的工作过程使用首个锚点作为稳定卡片 key", async () => {
  const source = await readFile(threadPath, "utf8");

  assert.match(source, /groupKey:\s*`activity-\$\{group\.anchorIndex\}`/);
  assert.doesNotMatch(source, /groupKey:\s*`activity-\$\{group\.anchorIndexes\.join\(/);
});
