import assert from "node:assert/strict";
import test from "node:test";
import {
  aiAgentEventDurationMs,
  aiAgentEventGroupDurationMs,
  applyAiAgentEventTiming,
  formatAiAgentEventDuration,
  normalizeAiEventTimestamp,
} from "../src/domain/ai/agent-event-timing.ts";

test("starts, continues and settles one streamed event without resetting its clock", () => {
  const started = applyAiAgentEventTiming(undefined, {
    kind: "tool",
    status: "running",
  }, 1_000);
  assert.deepEqual(started, {
    kind: "tool",
    status: "running",
    startedAt: 1_000,
    finishedAt: undefined,
  });

  const continued = applyAiAgentEventTiming(started, {
    kind: "tool",
    status: "running",
  }, 4_000);
  assert.equal(continued.startedAt, 1_000);
  assert.equal(continued.finishedAt, undefined);

  const settled = applyAiAgentEventTiming(continued, {
    kind: "tool",
    status: "completed",
  }, 6_500);
  assert.equal(settled.startedAt, 1_000);
  assert.equal(settled.finishedAt, 6_500);
  assert.equal(aiAgentEventDurationMs(settled, 9_000), 5_500);
});

test("keeps legacy history timeless while timing newly observed one-shot events", () => {
  const legacy = { kind: "tool", status: "completed" };
  assert.equal(aiAgentEventDurationMs(legacy, 9_000), undefined);
  assert.equal(aiAgentEventDurationMs(applyAiAgentEventTiming(undefined, legacy, 9_000), 9_000), 0);
  assert.equal(aiAgentEventGroupDurationMs([legacy], 9_000), undefined);
  assert.equal(formatAiAgentEventDuration(undefined), "");
});

test("computes live item and group duration from the earliest timed event", () => {
  const events = [
    { kind: "thought", status: "completed", startedAt: 1_000, finishedAt: 2_000 },
    { kind: "tool", status: "running", startedAt: 3_000 },
  ];
  assert.equal(aiAgentEventDurationMs(events[1], 8_900), 5_900);
  assert.equal(aiAgentEventGroupDurationMs(events, 8_900), 7_900);
  assert.equal(formatAiAgentEventDuration(7_900), "7s");
});

test("rejects invalid timestamps persisted by old or malformed clients", () => {
  assert.equal(normalizeAiEventTimestamp(0), undefined);
  assert.equal(normalizeAiEventTimestamp(Number.NaN), undefined);
  assert.equal(normalizeAiEventTimestamp("1000"), undefined);
  assert.equal(normalizeAiEventTimestamp(1000.9), 1000);
});

import fs from "node:fs";

const drawerSource = fs.readFileSync(
  new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url),
  "utf8",
);
const storeSource = fs.readFileSync(
  new URL("../src/stores/ai-assistant.ts", import.meta.url),
  "utf8",
);

test("native and ACP streaming paths emit running events through the timed store upsert", () => {
  assert.match(
    drawerSource,
    /const applyReasoningDelta[\s\S]*kind: "thought"[\s\S]*status: "running"[\s\S]*onReasoningDelta:[\s\S]*streamTextBatcher\.append\("reasoning"/,
  );
  assert.match(
    drawerSource,
    /const acpStreamTextBatcher[\s\S]*kind: "thought"[\s\S]*status: "running"[\s\S]*kind === "agent_thought_chunk"[\s\S]*acpStreamTextBatcher\.append/,
  );
  assert.match(
    drawerSource,
    /function upsertToolEvent[\s\S]*assistant\.upsertAgentEventForConversation/,
  );
});

test("the store persists timing and closes every running event on completion", () => {
  assert.match(storeSource, /startedAt\?: number;[\s\S]*finishedAt\?: number;/);
  assert.match(storeSource, /applyAiAgentEventTiming\(previous, merged\)/);
  assert.match(
    storeSource,
    /event\.status && UNSETTLED_AGENT_EVENT_STATUSES\.has\(event\.status\)[\s\S]*finishedAt/,
  );
});

test("reasoning events settle as soon as execution advances to output or tools", () => {
  assert.match(
    drawerSource,
    /createFrameTextBatcher\(\(kind, delta\)[\s\S]*?kind === "assistant"[\s\S]*?settleRunningAgentEvents\(conversationId, "completed", "thought"\)[\s\S]*?appendAgenticDelta[\s\S]*?onAssistantDelta:[\s\S]*?streamTextBatcher\.append\("assistant"/,
  );
  assert.match(
    drawerSource,
    /onToolCall:[\s\S]*?settleRunningAgentEvents\(conversationId, "completed", "thought"\)[\s\S]*?upsertToolEvent/,
  );
  assert.match(
    drawerSource,
    /onTurnComplete:[\s\S]*?settleRunningAgentEvents\(conversationId, "completed", "thought"\)/,
  );
  assert.match(
    drawerSource,
    /const acpStreamTextBatcher[\s\S]*?kind === "message"[\s\S]*?settleRunningAgentEvents\(conversationId, "completed", "thought"\)[\s\S]*?kind === "agent_message_chunk"[\s\S]*?acpStreamTextBatcher\.append/,
  );
  assert.match(
    drawerSource,
    /kind === "tool_call" \|\| kind === "tool_call_update"[\s\S]*?settleRunningAgentEvents\(conversationId, "completed", "thought"\)/,
  );
  assert.match(
    drawerSource,
    /sendAgentic\([\s\S]*?agenticSendGate\.claim\(conversationId\)[\s\S]*?settleRunningAgentEvents\(conversationId, "cancelled"\)/,
  );
  assert.match(
    drawerSource,
    /sendAgent\([\s\S]*?acpSendGate\.claim\(conversationId\)[\s\S]*?settleRunningAgentEvents\(conversationId, "cancelled"\)/,
  );
});

test("the store can settle one event kind without closing active tools", () => {
  assert.match(
    storeSource,
    /function settledAgentEvents\([\s\S]*?kind\?: AiAssistantEvent\["kind"\][\s\S]*?\(!kind \|\| event\.kind === kind\)/,
  );
  assert.match(
    storeSource,
    /function settleRunningAgentEvents\([\s\S]*?kind\?: AiAssistantEvent\["kind"\][\s\S]*?settledAgentEvents\(conversation\.agentEvents, status, Date\.now\(\), kind\)/,
  );
});
