import assert from "node:assert/strict";
import test from "node:test";

import {
  compactTokenCount,
  mergeAiTokenUsage,
  normalizeAiTokenUsage,
  unreportedAiTokenRequestCount,
  withAiTokenRequestStats,
} from "../src/domain/ai/token-usage.ts";

test("normalizes and merges provider token usage", () => {
  const first = normalizeAiTokenUsage({
    inputTokens: 1000,
    outputTokens: 100,
    cachedInputTokens: 800,
    requestCount: 1,
    attemptedRequestCount: 1,
    models: ["gpt-4.1"],
  });
  assert.ok(first);
  const merged = mergeAiTokenUsage(first, {
    inputTokens: 1200,
    outputTokens: 200,
    cachedInputTokens: 900,
    cacheWriteTokens: 0,
    reasoningTokens: 50,
    requestCount: 1,
    attemptedRequestCount: 1,
    models: ["gpt-4.1"],
  });
  assert.equal(merged.inputTokens, 2200);
  assert.equal(merged.cachedInputTokens, 1700);
  assert.equal(merged.requestCount, 2);
  assert.equal(merged.attemptedRequestCount, 2);
  assert.deepEqual(merged.models, ["gpt-4.1"]);
});

test("formats compact token counts", () => {
  assert.equal(compactTokenCount(999), "999");
  assert.equal(compactTokenCount(1200), "1.2K");
  assert.equal(compactTokenCount(12500), "13K");
});

test("tracks attempted requests independently from provider usage", () => {
  const usage = withAiTokenRequestStats(normalizeAiTokenUsage({
    inputTokens: 100,
    outputTokens: 20,
    requestCount: 1,
    models: ["primary"],
  }), 2, ["primary", "fallback"]);
  assert.ok(usage);
  assert.equal(usage.requestCount, 1);
  assert.equal(usage.attemptedRequestCount, 2);
  assert.equal(unreportedAiTokenRequestCount(usage), 1);
  assert.deepEqual(usage.models, ["primary", "fallback"]);
});

test("keeps legacy request counts fully reported", () => {
  const legacy = normalizeAiTokenUsage({ inputTokens: 10, requestCount: 3 });
  assert.ok(legacy);
  assert.equal(legacy.attemptedRequestCount, 3);
  assert.equal(unreportedAiTokenRequestCount(legacy), 0);

  const implicitSingleRequest = normalizeAiTokenUsage({ inputTokens: 10 });
  assert.ok(implicitSingleRequest);
  assert.equal(implicitSingleRequest.requestCount, 1);
  assert.equal(implicitSingleRequest.attemptedRequestCount, 1);
});

test("the answering model is kept through normalization and merges", async () => {
  const usageModule = await import("../src/domain/ai/token-usage.ts");
  const base = { inputTokens: 5, outputTokens: 3, requestCount: 1, attemptedRequestCount: 2, models: ["a", "b"] };
  assert.equal(usageModule.normalizeAiTokenUsage({ ...base, answeredBy: " b " }).answeredBy, "b");
  assert.equal(usageModule.normalizeAiTokenUsage({ ...base, answeredBy: 7 }).answeredBy, undefined);
  const merged = usageModule.mergeAiTokenUsage(usageModule.normalizeAiTokenUsage({ ...base, answeredBy: "a" }), usageModule.normalizeAiTokenUsage({ ...base, answeredBy: "b" }));
  assert.equal(merged.answeredBy, "b");
  assert.equal(usageModule.withAiTokenRequestStats({ ...base, answeredBy: "b" }, 3, ["c"]).answeredBy, "b");
  assert.equal(usageModule.aiReplyModel({ ...base, answeredBy: "b" }), "b");
  assert.equal(usageModule.aiReplyModel({ ...base, models: ["only"] }), "only", "legacy replies with one model");
  assert.equal(usageModule.aiReplyModel(base), undefined, "unknown when several models and none recorded");
});

test("the reported context size survives normalization, merges and request stats", async () => {
  const usageModule = await import("../src/domain/ai/token-usage.ts");
  const base = { inputTokens: 12_000, outputTokens: 30, requestCount: 1, attemptedRequestCount: 1 };
  assert.equal(usageModule.normalizeAiTokenUsage({ ...base, contextTokens: 12_000 }).contextTokens, 12_000);
  assert.equal(usageModule.normalizeAiTokenUsage({ ...base, contextTokens: -1 }).contextTokens, undefined);
  const merged = usageModule.mergeAiTokenUsage(
    usageModule.normalizeAiTokenUsage({ ...base, contextTokens: 9_000 }),
    usageModule.normalizeAiTokenUsage(base),
  );
  assert.equal(merged.contextTokens, 9_000, "a request without a context size keeps the previous one");
  assert.equal(usageModule.withAiTokenRequestStats({ ...base, contextTokens: 12_000 }, 2).contextTokens, 12_000);
});

test("the context meter uses the latest reported prompt size and the footer stays compact", async () => {
  const { readFile } = await import("node:fs/promises");
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  const bubble = await readFile(new URL("../src/components/ai/AiMessageBubble.vue", import.meta.url), "utf8");
  const css = await readFile(new URL("../src/styles/workspaces/ai/drawer.css", import.meta.url), "utf8");
  assert.match(drawer, /if \(usage\.inputTokens > 0\) replyTokenUsage = \{ \.\.\.replyTokenUsage, contextTokens: usage\.inputTokens \};/);
  assert.match(drawer, /reported\?\.tokenUsage\?\.contextTokens \?\? estimate\.estimatedTokens/);
  assert.match(bubble, /unreportedAiTokenRequestCount\(usage\) > 0\s*\?\s*t\("ai\.message\.tokenUsagePartial"/);
  assert.match(css, /\.ai-message-bubble__token-usage \{[^}]*font-size: 10px;[^}]*white-space: nowrap;/);
});
