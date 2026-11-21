import assert from "node:assert/strict";
import test from "node:test";
import {
  agentRunStepPercent,
  isActiveAgentRun,
  needsAgentRunAttention,
  normalizeAgentRun,
  prunableAgentRunIds,
  safeAgentRunText,
  shouldApplyAgentRun,
} from "../src/domain/ai/session-progress.ts";

function run(overrides = {}) {
  return {
    runId: "run-1",
    sessionId: "session-1",
    workspaceId: "workspace-1",
    workspaceScopeId: "workspace:1",
    source: "agentic",
    title: "Task",
    revision: 1,
    navigation: { type: "ai-conversation", conversationId: "conversation-1" },
    progress: {
      phase: "thinking",
      summary: "Working",
      createdAt: 10,
      updatedAt: 10,
    },
    ...overrides,
  };
}

test("normalizes a valid run and rejects payloads without stable identity", () => {
  assert.deepEqual(normalizeAgentRun(run()), run());
  assert.equal(normalizeAgentRun({ ...run(), runId: "" }), undefined);
  assert.equal(normalizeAgentRun({ ...run(), navigation: { type: "session" } }), undefined);
});

test("newer revisions win even when events arrive out of order", () => {
  const current = run({ revision: 3, progress: { ...run().progress, updatedAt: 30 } });
  assert.equal(shouldApplyAgentRun(current, run({ revision: 2 })), false);
  assert.equal(shouldApplyAgentRun(current, run({ revision: 4 })), true);
});

test("active and attention phases are structural, not inferred from copy", () => {
  assert.equal(isActiveAgentRun(run()), true);
  assert.equal(needsAgentRunAttention(run({ progress: { ...run().progress, phase: "waiting_approval" } })), true);
  assert.equal(isActiveAgentRun(run({ progress: { ...run().progress, phase: "failed" } })), false);
  assert.equal(needsAgentRunAttention(run({ progress: { ...run().progress, phase: "failed" } })), true);
});

test("safe progress text redacts credentials and truncates summaries", () => {
  const value = safeAgentRunText("Authorization: Bearer abcdefghijklmnop secret", 42);
  assert.doesNotMatch(value, /abcdefghijklmnop/);
  assert.ok(value.length <= 42);
});

test("pruning keeps running and attention runs and only the newest finished runs", () => {
  const finished = Array.from({ length: 5 }, (_, index) => run({
    runId: `done-${index}`,
    progress: { ...run().progress, phase: "completed", updatedAt: 100 + index },
  }));
  const failed = run({ runId: "failed", progress: { ...run().progress, phase: "failed", updatedAt: 1 } });
  const running = run({ runId: "running", progress: { ...run().progress, updatedAt: 1 } });
  const pruned = prunableAgentRunIds([running, failed, ...finished], 2);
  assert.deepEqual(pruned.sort(), ["done-0", "done-1", "done-2"]);
  assert.deepEqual(prunableAgentRunIds([running, failed, ...finished], 10), []);
});

test("normalizes step progress and derives a rounded percentage", () => {
  const withSteps = normalizeAgentRun(run({
    progress: { ...run().progress, stepsCompleted: 5, stepsTotal: 3 },
  }));
  assert.equal(withSteps?.progress.stepsCompleted, 3);
  assert.equal(withSteps?.progress.stepsTotal, 3);
  assert.equal(agentRunStepPercent(withSteps.progress), 100);
  assert.equal(agentRunStepPercent({ ...run().progress, stepsCompleted: 1, stepsTotal: 3 }), 33);

  const withoutSteps = normalizeAgentRun(run({
    progress: { ...run().progress, stepsCompleted: 2, stepsTotal: 0 },
  }));
  assert.equal(withoutSteps?.progress.stepsTotal, undefined);
  assert.equal(agentRunStepPercent(withoutSteps.progress), undefined);
});
