import assert from "node:assert/strict";
import test from "node:test";
import { applyTestRunEvents, createTestRunState, seedTestRunNodes, testNodeDisplayStatus, testRunTreeRows } from "../src/domain/testing/test-runs.ts";
import { findTestItem, pickTestItems, testsByFile } from "../src/domain/testing/test-discovery.ts";

const DISCOVERED = [
  {
    id: "Calc",
    label: "Calc",
    path: "src/Calc.test.ts",
    line: 3,
    children: [
      { id: "Calc#adds", label: "adds", path: "src/Calc.test.ts", line: 5 },
      { id: "Calc#subs", label: "subs", path: "src/Calc.test.ts", line: 9 },
    ],
  },
  { id: "Other", label: "Other", children: [{ id: "Other#x", label: "x", path: "src/Other.test.ts", line: 2 }] },
];

function run() {
  const state = createTestRunState({ runId: "run-01", pluginId: "acme", providerId: "unit", projectRoot: "/p", debug: false, testIds: [], startedAt: 1 });
  seedTestRunNodes(state, DISCOVERED);
  return state;
}

test("test tree: rows carry displayed statuses and durations; parents are listed", () => {
  const state = run();
  applyTestRunEvents(state, [
    { type: "started", id: "Calc#adds" },
    { type: "failed", id: "Calc#subs", durationMs: 7, message: "boom" },
  ]);
  const { rows, parentIds } = testRunTreeRows(state);
  assert.deepEqual(parentIds, ["Calc", "Other"]);
  assert.equal(rows[0].status, "running");
  assert.equal(rows[0].children[1].status, "failed");
  assert.equal(rows[0].children[1].durationMs, 7);
  assert.equal(rows[1].children[0].status, "pending");
  assert.equal(testNodeDisplayStatus(state, "Calc"), "running");
  applyTestRunEvents(state, [{ type: "passed", id: "Calc#adds" }]);
  assert.equal(testNodeDisplayStatus(state, "Calc"), "failed");
  assert.equal(testNodeDisplayStatus(state, "Calc#adds"), "passed");
  assert.equal(testNodeDisplayStatus(state, "missing"), undefined);
});

test("test discovery: tests by file and picked subtrees", () => {
  const byFile = testsByFile(DISCOVERED);
  assert.deepEqual(byFile.get("src/Calc.test.ts"), [
    { id: "Calc", label: "Calc", line: 3 },
    { id: "Calc#adds", label: "adds", line: 5 },
    { id: "Calc#subs", label: "subs", line: 9 },
  ]);
  assert.deepEqual(byFile.get("src/Other.test.ts"), [{ id: "Other#x", label: "x", line: 2 }]);
  assert.equal(pickTestItems(DISCOVERED, []).length, 2);
  assert.deepEqual(pickTestItems(DISCOVERED, ["Calc#subs", "Calc", "Other#x"]).map((item) => item.id), ["Calc", "Other#x"]);
  assert.equal(findTestItem(DISCOVERED, "Calc#adds")?.label, "adds");
  assert.equal(findTestItem(DISCOVERED, "nope"), undefined);
});
