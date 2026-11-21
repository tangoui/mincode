import assert from "node:assert/strict";
import test from "node:test";
import {
  DEFAULT_AI_MAX_LOOPS,
  MAX_AI_MAX_LOOPS,
  MIN_AI_MAX_LOOPS,
  normalizeAiMaxLoops,
} from "../src/domain/ai/agent-loop-limit.ts";

test("AI agent loop limit defaults to 200", () => {
  assert.equal(DEFAULT_AI_MAX_LOOPS, 200);
  assert.equal(normalizeAiMaxLoops(undefined), 200);
  assert.equal(normalizeAiMaxLoops("invalid"), 200);
});

test("AI agent loop limit accepts settings values and clamps unsafe bounds", () => {
  assert.equal(normalizeAiMaxLoops("240"), 240);
  assert.equal(normalizeAiMaxLoops(240.8), 240);
  assert.equal(normalizeAiMaxLoops(0), MIN_AI_MAX_LOOPS);
  assert.equal(normalizeAiMaxLoops(MAX_AI_MAX_LOOPS + 1), MAX_AI_MAX_LOOPS);
});
