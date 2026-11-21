import assert from "node:assert/strict";
import test from "node:test";

import {
  AI_CONTEXT_SUMMARY_MARKER,
  AI_CONTEXT_SUMMARY_PREAMBLE,
  compactAiLoopContext,
  estimateAiMessageTokens,
  estimateAiToolTokens,
} from "../src/domain/ai/context-compaction.ts";
import { readFile } from "node:fs/promises";

const config = { contextWindow: 1000, maxInputTokens: 800, maxOutputTokens: 100 };

test("compacts older turns and preserves system plus recent complete turns", () => {
  const messages = [
    { role: "system", content: "rules" },
    { role: "user", content: "old request ".repeat(100) },
    { role: "assistant", content: "old answer ".repeat(100) },
    { role: "tool", toolCallId: "tool-1", content: "old tool output ".repeat(100) },
    { role: "user", content: "new request ".repeat(100) },
    { role: "assistant", content: "new answer ".repeat(100) },
    { role: "user", content: "latest request ".repeat(100) },
  ];
  const result = compactAiLoopContext(messages, config);
  assert.equal(result.compacted, true);
  assert.ok(result.messages.some((message) => message.content.startsWith(AI_CONTEXT_SUMMARY_MARKER)));
  assert.equal(result.messages[0].content, "rules");
  assert.equal(result.messages.at(-1)?.content, messages.at(-1)?.content);
  assert.ok(result.removedMessages > 0);
});

test("does not compact short conversations", () => {
  const messages = [{ role: "system", content: "rules" }, { role: "user", content: "hello" }];
  const result = compactAiLoopContext(messages, config);
  assert.equal(result.compacted, false);
  assert.equal(result.estimatedTokens, estimateAiMessageTokens(messages));
  assert.deepEqual(result.messages, messages);
});

test("anchors the compaction event to the triggering user message", async () => {
  const drawer = await readFile(
    new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url),
    "utf8"
  );
  // 压缩事件发生在首个流式片段之前，eventTriggerMessageId 还是空的；
  // 必须回落到本轮用户消息，否则事件会在线程末尾渲染成一张孤儿“工作过程”卡。
  assert.match(
    drawer,
    /id: createId\("context-compaction"\)[\s\S]*?triggerMessageId: eventTriggerMessageId \?\? triggerMessageId,/,
  );
});

test("uses an economic input budget below a very large model context window", () => {
  const messages = [
    { role: "system", content: "rules" },
    { role: "user", content: "old request ".repeat(45000) },
    { role: "assistant", content: "old answer ".repeat(45000) },
    { role: "user", content: "recent request" },
    { role: "assistant", content: "recent answer" },
    { role: "user", content: "latest request" },
  ];
  const result = compactAiLoopContext(messages, {
    contextWindow: 1048576,
    maxInputTokens: 1000000,
    maxOutputTokens: 32768,
  });
  assert.equal(result.inputLimit, 128000);
  assert.equal(result.compacted, true);
});

test("counts selected tool schemas in the compaction budget", () => {
  const messages = [
    { role: "system", content: "rules" },
    { role: "user", content: "old request ".repeat(70) },
    { role: "assistant", content: "old answer ".repeat(70) },
    { role: "user", content: "recent request" },
    { role: "assistant", content: "recent answer" },
    { role: "user", content: "latest request" },
  ];
  const tools = [{
    name: "workspace.large_tool",
    description: "large schema ".repeat(180),
    inputSchema: { type: "object", properties: { query: { type: "string" } } },
  }];
  const overhead = estimateAiToolTokens(tools);
  const withoutTools = compactAiLoopContext(messages, config);
  const withTools = compactAiLoopContext(messages, config, overhead);

  assert.equal(withoutTools.compacted, false);
  assert.equal(withTools.compacted, true);
  // estimatedTokens describes the request that will actually be sent.
  assert.equal(withTools.estimatedTokens, estimateAiMessageTokens(withTools.messages) + overhead);
  assert.ok(withTools.estimatedTokens < estimateAiMessageTokens(messages) + overhead);
});

test("recompaction keeps only one generated summary preamble", () => {
  const repeatedPreamble = `${AI_CONTEXT_SUMMARY_PREAMBLE}\n\n${AI_CONTEXT_SUMMARY_PREAMBLE}\n\nEarlier facts`;
  const messages = [
    { role: "system", content: "rules" },
    { role: "system", content: `${AI_CONTEXT_SUMMARY_MARKER}\n${repeatedPreamble}` },
    { role: "user", content: "old request ".repeat(100) },
    { role: "assistant", content: "old answer ".repeat(100) },
    { role: "user", content: "recent request ".repeat(100) },
    { role: "assistant", content: "recent answer" },
    { role: "user", content: "latest request" },
  ];
  const result = compactAiLoopContext(messages, config);
  const summary = result.messages.find((message) => message.content.startsWith(AI_CONTEXT_SUMMARY_MARKER));

  assert.equal(result.compacted, true);
  assert.ok(summary);
  assert.equal(summary.content.split(AI_CONTEXT_SUMMARY_PREAMBLE).length - 1, 1);
  assert.match(summary.content, /Earlier facts/);
});

test("long runs compact older turns before every later model request", async () => {
  const { readFile } = await import("node:fs/promises");
  const [loop, drawer] = await Promise.all([
    readFile(new URL("../src/services/ai-agent-loop.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8"),
  ]);
  assert.match(
    loop,
    /if \(loopCount > 1\) \{\s*const compacted = options\.compactHistory\?\.\(history\);[\s\S]*?history\.splice\(0, history\.length, \.\.\.compacted\);/,
  );
  assert.match(
    drawer,
    /compactHistory: \(loopHistory\) => \{\s*const compaction = compactAiLoopContext\(loopHistory, requestConfig, estimateAiToolTokens\(currentTools\(\)\)\);/,
  );
});

test("context usage uses the same limit as compaction", async () => {
  const module = await import("../src/domain/ai/context-compaction.ts");
  const config = { baseUrl: "", apiKey: "", model: "m", contextWindow: 10_000, maxOutputTokens: 2_000 };
  const messages = [{ role: "user", content: "x".repeat(4_000) }];
  const usage = module.estimateAiContextUsage(messages, config);
  assert.equal(usage.inputLimit, 8_000);
  assert.equal(usage.estimatedTokens, module.estimateAiMessageTokens(messages));
  assert.ok(usage.ratio > 0 && usage.ratio < 1);
  assert.equal(module.AI_CONTEXT_COMPACTION_THRESHOLD, 0.78);
  const nearLimit = [{ role: "user", content: "x".repeat(40_000) }];
  assert.ok(module.estimateAiContextUsage(nearLimit, config).ratio >= module.AI_CONTEXT_COMPACTION_THRESHOLD);
});

test("summarizes older tool rounds when the latest turn alone is oversized", () => {
  const rounds = [];
  for (let index = 0; index < 8; index += 1) {
    rounds.push(
      { role: "assistant", content: "", toolCalls: [{ id: `call-${index}`, name: "read_file", arguments: "{}" }] },
      { role: "tool", toolCallId: `call-${index}`, content: `result ${index} `.repeat(60) },
    );
  }
  const messages = [{ role: "system", content: "rules" }, { role: "user", content: "fix it" }, ...rounds];
  const result = compactAiLoopContext(messages, config);
  assert.equal(result.compacted, true);
  assert.equal(result.messages[0].content, "rules");
  assert.ok(result.messages[1].content.startsWith(AI_CONTEXT_SUMMARY_MARKER));
  assert.match(result.messages[1].content, /Earlier steps of the latest user request/);
  assert.equal(result.messages[2].content, "fix it");
  assert.equal(result.messages[3].role, "assistant");
  const keptCalls = new Set(result.messages.flatMap((message) => message.toolCalls?.map((call) => call.id) ?? []));
  for (const message of result.messages.filter((item) => item.role === "tool")) {
    assert.ok(keptCalls.has(message.toolCallId), "every kept tool result keeps its call");
  }
  assert.equal(result.messages.at(-1)?.toolCallId, "call-7");
  assert.ok(result.estimatedTokens < estimateAiMessageTokens(messages));
  assert.equal(result.estimatedTokens, estimateAiMessageTokens(result.messages));
});

test("keeps mid-conversation system messages in their original position", () => {
  const messages = [
    { role: "system", content: "rules" },
    { role: "user", content: "old request ".repeat(100) },
    { role: "assistant", content: "old answer ".repeat(100) },
    { role: "system", content: "old note" },
    { role: "user", content: "new request ".repeat(100) },
    { role: "assistant", content: "new answer" },
    { role: "system", content: "recent note" },
    { role: "user", content: "latest request" },
  ];
  const result = compactAiLoopContext(messages, config);
  assert.equal(result.compacted, true);
  assert.deepEqual(result.messages.filter((message) => message.content === "rules").length, 1);
  assert.equal(result.messages[0].content, "rules");
  assert.match(result.messages[1].content, /System: old note/);
  const noteIndex = result.messages.findIndex((message) => message.content === "recent note");
  assert.equal(result.messages[noteIndex - 1].content, "new answer");
  assert.equal(result.messages[noteIndex + 1].content, "latest request");
});
