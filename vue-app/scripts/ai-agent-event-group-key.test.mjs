import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const threadPath = new URL("../src/components/ai/AiMessageThread.vue", import.meta.url);

test("时间线使用事件的稳定 key 渲染活动行", async () => {
  const source = await readFile(threadPath, "utf8");

  assert.match(source, /v-for="item in visibleItems" :key="item\.key"/);
});
