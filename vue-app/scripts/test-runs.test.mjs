import assert from "node:assert/strict";
import test from "node:test";
import {
  aggregateTestRunStatuses,
  applyTestRunEvents,
  createTestRunState,
  endTestRun,
  failedTestIds,
  parseTestRunEvent,
  parseTestRunEventPayload,
  seedTestRunNodes,
  summarizeTestRun,
  TEST_RUN_LIMITS,
} from "../src/domain/testing/test-runs.ts";
import { validateTestProviderEntry } from "../src/domain/plugins/test-providers.ts";

const RUN = "run-0123abcd";

function newRun() {
  return createTestRunState({ runId: RUN, pluginId: "java-debug", providerId: "junit", projectRoot: "/p", debug: false, testIds: [], startedAt: 1 });
}

test("test runs: payloads are re-validated, invalid events dropped and counted", () => {
  assert.equal(parseTestRunEventPayload({ runId: "x", pluginId: "p", events: [] }), null);
  assert.equal(parseTestRunEventPayload({ runId: RUN, pluginId: "", events: [] }), null);
  assert.equal(parseTestRunEventPayload({ runId: RUN, pluginId: "p", events: {} }), null);
  const payload = parseTestRunEventPayload({
    runId: RUN,
    pluginId: "java-debug",
    events: [
      { type: "tree", id: "s", label: " Suite ", kind: "suite", path: "src/S.java", line: 3 },
      { type: "tree", id: "s#a", parentId: "s#a", label: "a", kind: "test" },
      { type: "tree", id: "s#a", label: "a", kind: "method" },
      { type: "tree", id: "s#a", label: "a", kind: "test", line: 2 },
      { type: "tree", id: "s#a", label: "a", kind: "test", path: "../x" },
      { type: "started", id: " s" },
      { type: "failed", id: "s#a", durationMs: 2.4, message: "boom\u001b", expected: "", actual: "1", trace: "at S.a(S.java:3)" },
      { type: "passed", id: "s#b", durationMs: -1 },
      { type: "output", text: "" },
      { type: "ended", finished: "no", reason: "x" },
      { type: "unknown" },
      "text",
    ],
  });
  assert.equal(payload.dropped, 10);
  assert.deepEqual(payload.events[0], { type: "tree", id: "s", label: "Suite", kind: "suite", path: "src/S.java", line: 3 });
  assert.deepEqual(payload.events[1], { type: "failed", id: "s#a", durationMs: 2, message: "boom", expected: "", actual: "1", trace: "at S.a(S.java:3)" });
  assert.equal(parseTestRunEvent({ type: "trace", id: "x" }), undefined);
  const long = parseTestRunEvent({ type: "failed", id: "x", trace: "é".repeat(TEST_RUN_LIMITS.traceMaxBytes) });
  assert.ok(new TextEncoder().encode(long.trace).byteLength <= TEST_RUN_LIMITS.traceMaxBytes);
});

test("test runs: events merge into a tree in any order, repeated events are idempotent", () => {
  const state = newRun();
  applyTestRunEvents(state, [
    // A result before its tree node, and a child before its parent.
    { type: "passed", id: "s#b", durationMs: 3 },
    { type: "tree", id: "s#a", parentId: "s", label: "a()", kind: "test" },
    { type: "started", id: "s#a" },
    { type: "tree", id: "s", label: "Suite", kind: "suite", path: "src/S.java", line: 1 },
    { type: "tree", id: "s#b", parentId: "s", label: "b()", kind: "test" },
    { type: "failed", id: "s#a", message: "expected 1", expected: "1", actual: "2" },
    // Repeats: a late started never downgrades a result; the same tree event changes nothing.
    { type: "started", id: "s#b" },
    { type: "tree", id: "s#b", parentId: "s", label: "b()", kind: "test" },
    { type: "failed", id: "s#a", message: "expected 1", expected: "1", actual: "2" },
    { type: "output", id: "s#a", text: "log\n" },
  ]);
  assert.deepEqual(state.roots, ["s"]);
  assert.deepEqual(state.nodes.s.children, ["s#a", "s#b"]);
  assert.equal(state.nodes["s#b"].status, "passed");
  assert.equal(state.nodes["s#b"].durationMs, 3);
  assert.equal(state.nodes["s#a"].status, "failed");
  assert.equal(state.nodes["s#a"].expected, "1");
  assert.equal(state.nodes["s#a"].output, "log\n");
  assert.equal(state.output, "log\n");
  assert.equal(aggregateTestRunStatuses(state).get("s"), "failed");
  assert.deepEqual(summarizeTestRun(state), { total: 2, passed: 1, failed: 1, errored: 0, skipped: 0, terminated: 0, running: 0, pending: 0 });
  // A cycle is refused: s cannot move under its own child.
  applyTestRunEvents(state, [{ type: "tree", id: "s", parentId: "s#a", label: "Suite", kind: "suite" }]);
  assert.deepEqual(state.roots, ["s"]);
  assert.equal(state.nodes.s.parentId, undefined);
});

test("test runs: tree events keep the discovered label of a known node and name placeholders", () => {
  const state = seedTestRunNodes(newRun(), [{ id: "C", label: "CalcTest", children: [{ id: "C#passes", label: "passes()" }] }]);
  applyTestRunEvents(state, [
    // The runtime spells the same tests differently (no parentheses).
    { type: "tree", id: "C", label: "demo.CalcTest", kind: "suite" },
    { type: "tree", id: "C#passes", parentId: "C", label: "passes", kind: "test" },
    { type: "passed", id: "C#passes" },
    // A node first seen through a result, or created as a parent, has no label of its own yet.
    { type: "started", id: "D#runs" },
    { type: "tree", id: "D#runs", parentId: "D", label: "runs", kind: "test" },
    { type: "tree", id: "D", label: "OtherTest", kind: "suite" },
    // A dynamic node keeps the label it was first given.
    { type: "tree", id: "C#p[1]", parentId: "C#passes", label: "[1] 1", kind: "test" },
    { type: "tree", id: "C#p[1]", parentId: "C#passes", label: "[1] one", kind: "test" },
  ]);
  assert.equal(state.nodes["C"].label, "CalcTest");
  assert.equal(state.nodes["C#passes"].label, "passes()");
  assert.equal(state.nodes["C#passes"].status, "passed");
  assert.equal(state.nodes["D#runs"].label, "runs");
  assert.equal(state.nodes["D"].label, "OtherTest");
  assert.equal(state.nodes["D"].kind, "suite");
  assert.equal(state.nodes["C#p[1]"].label, "[1] 1");
});

test("test runs: dynamic nodes are capped with a truncation notice", () => {
  const state = newRun();
  const events = [{ type: "tree", id: "p", label: "param", kind: "suite" }];
  for (let index = 0; index < TEST_RUN_LIMITS.nodesMax + 50; index += 1) {
    events.push({ type: "tree", id: `p[${index}]`, parentId: "p", label: `[${index}]`, kind: "test" });
    events.push({ type: "passed", id: `p[${index}]` });
  }
  applyTestRunEvents(state, events);
  assert.equal(Object.keys(state.nodes).length, TEST_RUN_LIMITS.nodesMax);
  assert.equal(state.truncated, true);
  // Seeded discovered nodes do not use the runtime budget (the host counts only published ids).
  const seeded = seedTestRunNodes(newRun(), Array.from({ length: 2000 }, (_, index) => ({ id: `d${index}`, label: `d${index}` })));
  const dynamic = [{ type: "tree", id: "q", label: "param", kind: "suite" }];
  for (let index = 0; index < TEST_RUN_LIMITS.nodesMax - 1; index += 1) dynamic.push({ type: "tree", id: `q[${index}]`, parentId: "q", label: `[${index}]`, kind: "test" });
  applyTestRunEvents(seeded, dynamic);
  assert.equal(seeded.truncated, false);
  assert.equal(seeded.nodeCount, 2000 + TEST_RUN_LIMITS.nodesMax);
  applyTestRunEvents(seeded, [{ type: "tree", id: "q[extra]", parentId: "q", label: "extra", kind: "test" }]);
  assert.equal(seeded.truncated, true);
  // The host's own truncation notice sets the flag too.
  const other = applyTestRunEvents(newRun(), [{ type: "truncated" }]);
  assert.equal(other.truncated, true);
  // Run output keeps the newest characters.
  const noisy = newRun();
  applyTestRunEvents(noisy, [{ type: "output", text: "a".repeat(TEST_RUN_LIMITS.runOutputMaxChars) }, { type: "output", text: "END" }]);
  assert.equal(noisy.output.length, TEST_RUN_LIMITS.runOutputMaxChars);
  assert.ok(noisy.output.endsWith("END") && noisy.outputTruncated);
});

test("test runs: a session end without finished marks unfinished tests terminated", () => {
  const state = seedTestRunNodes(newRun(), [
    { id: "s", label: "Suite", children: [{ id: "s#a", label: "a" }, { id: "s#b", label: "b" }, { id: "s#c", label: "c" }] },
  ]);
  applyTestRunEvents(state, [
    { type: "passed", id: "s#a" },
    { type: "started", id: "s#b" },
    { type: "ended", finished: false, reason: "sessionEnded" },
    // Events after the end are ignored.
    { type: "passed", id: "s#c" },
  ]);
  assert.equal(state.phase, "ended");
  assert.equal(state.endReason, "sessionEnded");
  assert.deepEqual(["s#a", "s#b", "s#c"].map((id) => state.nodes[id].status), ["passed", "terminated", "terminated"]);
  assert.equal(aggregateTestRunStatuses(state).get("s"), "terminated");
  assert.equal(summarizeTestRun(state).terminated, 2);
  // A finished run leaves tests that never ran pending (not run), not terminated.
  const finished = seedTestRunNodes(newRun(), [{ id: "t", label: "t" }, { id: "u", label: "u" }]);
  applyTestRunEvents(finished, [{ type: "passed", id: "t" }, { type: "finished", durationMs: 9 }, { type: "ended", finished: true, reason: "finished" }]);
  assert.equal(finished.nodes.u.status, "pending");
  assert.equal(finished.durationMs, 9);
  // endTestRun is idempotent.
  endTestRun(finished, "stopped");
  assert.equal(finished.endReason, "finished");
});

test("test runs: failed ids are the most specific failures in tree order", () => {
  const state = seedTestRunNodes(newRun(), [
    { id: "A", label: "A", children: [{ id: "A#1", label: "1" }, { id: "A#2", label: "2" }] },
    { id: "B", label: "B", children: [{ id: "B#1", label: "1" }] },
    { id: "C", label: "C", children: [{ id: "C#1", label: "1" }] },
  ]);
  applyTestRunEvents(state, [
    { type: "failed", id: "A#2" },
    { type: "passed", id: "A#1" },
    { type: "errored", id: "A" },
    { type: "errored", id: "B" },
    { type: "passed", id: "C#1" },
    { type: "skipped", id: "C" },
  ]);
  assert.deepEqual(failedTestIds(state), ["A#2", "B"]);
  assert.deepEqual(failedTestIds(newRun()), []);
});

test("test runs: failed ids past the rerun limit collapse into their parents instead of being cut", () => {
  // Method A has 30 failing invocations, method B one later failure: with a limit of 10, A's invocations
  // collapse into A (rerun as a whole) and B is still rerun.
  const state = seedTestRunNodes(newRun(), [
    {
      id: "K",
      label: "K",
      children: [
        { id: "K#A", label: "A", children: Array.from({ length: 30 }, (_, index) => ({ id: `K#A[${index}]`, label: `${index}` })) },
        { id: "K#B", label: "B" },
        { id: "K#C", label: "C" },
      ],
    },
  ]);
  applyTestRunEvents(state, [
    ...Array.from({ length: 30 }, (_, index) => ({ type: "failed", id: `K#A[${index}]` })),
    { type: "failed", id: "K#A" },
    { type: "failed", id: "K#B" },
    { type: "passed", id: "K#C" },
  ]);
  assert.equal(failedTestIds(state).length, 31);
  assert.deepEqual(failedTestIds(state, 10), ["K#A", "K#B"]);
  assert.deepEqual(failedTestIds(state, 1), ["K"]);
});

test("test providers: debugRun is re-validated", () => {
  const provider = { id: "junit", title: "JUnit", discoverAction: "tests.discover", runAction: "tests.run", runRisk: "read" };
  assert.deepEqual(validateTestProviderEntry({ ...provider, debugRun: { debuggerType: "java", resolveRunAction: "tests.resolveRun" } }).value.debugRun, { debuggerType: "java" });
  assert.equal(validateTestProviderEntry(provider).value.debugRun, undefined);
  assert.equal(validateTestProviderEntry({ ...provider, debugRun: { debuggerType: "Java", resolveRunAction: "x" } }).ok, false);
  assert.equal(validateTestProviderEntry({ ...provider, debugRun: "java" }).ok, false);
});

test("test runs: chains deeper than depthMax are not re-parented (no cycle check walk without bound)", () => {
  const state = newRun();
  const events = [];
  for (let index = 1; index <= TEST_RUN_LIMITS.depthMax + 10; index += 1) {
    events.push({ type: "tree", id: `n${index}`, parentId: `n${index - 1}`, label: `n${index}`, kind: "suite" });
  }
  applyTestRunEvents(state, events);
  assert.equal(state.nodeCount, Object.keys(state.nodes).length);
  // Every node is reachable from a root.
  let reachable = 0;
  const visit = (id) => { reachable += 1; for (const child of state.nodes[id].children) visit(child); };
  for (const id of state.roots) visit(id);
  assert.equal(reachable, state.nodeCount);
  assert.ok(state.roots.length >= 2);
});
