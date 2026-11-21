import assert from "node:assert/strict";
import test from "node:test";

const { createAcpTextSegments } = await import("../src/domain/ai/acp-text-segments.ts");

function setup() {
  let next = 0;
  return createAcpTextSegments(() => `seg-${++next}`);
}

test("没有工具调用时，同一回复的正文始终写入同一条消息", () => {
  const segments = setup();
  assert.equal(segments.messageIdFor("c", "", undefined), "seg-1");
  assert.equal(segments.messageIdFor("c", "", "seg-1"), "seg-1");
  assert.equal(segments.messageIdFor("c", "agent-1", undefined), "agent-1");
  assert.equal(segments.messageIdFor("c", "agent-1", "agent-1"), "agent-1");
});

test("新的工具调用之后，正文另起一条消息；代理沿用同一 messageId 时也拆开", () => {
  const segments = setup();
  assert.equal(segments.messageIdFor("c", "agent-1", undefined), "agent-1");
  segments.breakAfterTool("c");
  const second = segments.messageIdFor("c", "agent-1", "agent-1");
  assert.equal(second, "seg-1");
  assert.equal(segments.messageIdFor("c", "agent-1", second), "seg-1", "之后的片段继续写入新段");
  segments.breakAfterTool("c");
  assert.equal(segments.messageIdFor("c", "", "seg-1"), "seg-2");
});

test("断点只影响对应会话，重置后从头开始", () => {
  const segments = setup();
  segments.breakAfterTool("a");
  assert.equal(segments.messageIdFor("b", "", "cur-b"), "cur-b");
  segments.reset("a");
  assert.equal(segments.messageIdFor("a", "", "cur-a"), "cur-a");
});

test("抽屉接线：只有新的工具调用才断段，新提示开始时重置", async () => {
  const { readFile } = await import("node:fs/promises");
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.match(drawer, /if \(!acpEventMessageIds\.get\(conversationId\)\?\.has\(id\)\) acpTextSegments\.breakAfterTool\(conversationId\);[\s\S]*?const triggerMessageId = acpEventAnchor\(conversationId, id\);/);
  assert.match(drawer, /acpCurrentMessageIds\.delete\(conversationId\);\n  acpTextSegments\.reset\(conversationId\);/);
  assert.match(drawer, /const messageId = acpTextSegments\.messageIdFor\(/);
});
