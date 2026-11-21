import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const drawer = await readFile(
  new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url),
  "utf8",
);

test("freezes running conversation order until the task settles", () => {
  assert.match(
    drawer,
    /const runningConversationSortTimes = reactive\(new Map<string, number>\(\)\);/,
  );
  assert.match(
    drawer,
    /if \(conversation\.running\)[\s\S]*?previousById\.get\(conversation\.id\)\?\.updatedAt \?\? conversation\.updatedAt/,
  );
  assert.match(
    drawer,
    /else \{\s*runningConversationSortTimes\.delete\(conversation\.id\);\s*\}/,
  );
});

test("uses the same stable time for sorting and date grouping", () => {
  assert.match(
    drawer,
    /function conversationSortTime\(conversation: AiConversation\)[\s\S]*?runningConversationSortTimes\.get\(conversation\.id\) \?\? conversation\.updatedAt/,
  );
  assert.match(
    drawer,
    /\.sort\(\(left, right\) => conversationSortTime\(right\) - conversationSortTime\(left\)\)/,
  );
  assert.match(drawer, /const anchor = tree\.anchor\(conversation\.id\) \?\? conversation;\s*[\s\S]*?const date = new Date\(conversationSortTime\(anchor\)\);/);
});
