import assert from "node:assert/strict";
import test from "node:test";

const { createAiMessageExpansion } = await import("../src/domain/ai/message-expansion.ts");

test("本次运行中产生或正在流式的消息展开，历史消息默认折叠", () => {
  const expansion = createAiMessageExpansion(1_000);
  assert.equal(expansion.initiallyExpanded({ id: "new", createdAt: 1_500 }, false), true);
  assert.equal(expansion.initiallyExpanded({ id: "old", createdAt: 500 }, false), false);
  assert.equal(expansion.initiallyExpanded({ id: "live", createdAt: 500 }, true), true);
  assert.equal(expansion.initiallyExpanded({ createdAt: 500 }, false), true, "没有 id 无法记忆，保持展开");
});

test("用户手动展开或折叠后，重新挂载保持该选择", () => {
  const expansion = createAiMessageExpansion(1_000);
  expansion.remember("old", true);
  assert.equal(expansion.initiallyExpanded({ id: "old", createdAt: 500 }, false), true);
  expansion.remember("new", false);
  assert.equal(expansion.initiallyExpanded({ id: "new", createdAt: 1_500 }, false), false);
});

test("记忆有上限，最早的选择先被丢弃", () => {
  const expansion = createAiMessageExpansion(1_000, 2);
  expansion.remember("a", true);
  expansion.remember("b", true);
  expansion.remember("c", true);
  assert.equal(expansion.initiallyExpanded({ id: "a", createdAt: 0 }, false), false);
  assert.equal(expansion.initiallyExpanded({ id: "c", createdAt: 0 }, false), true);
});
