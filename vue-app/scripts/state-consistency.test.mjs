import assert from "node:assert/strict";
import test from "node:test";

import { createLatestRequestGate } from "../src/domain/async/latest-request-gate.ts";
import {
  createLatestTaskRunner,
  createSerializedTaskQueue,
} from "../src/domain/async/serialized-task-queue.ts";

test("latest request gate rejects stale responses for switched and repeated targets", () => {
  let currentTarget = "repo-a";
  const gate = createLatestRequestGate(() => currentTarget);
  const first = gate.begin();
  const second = gate.begin();

  assert.equal(gate.isCurrent(first), false);
  assert.equal(gate.isCurrent(second), true);

  currentTarget = "repo-b";
  assert.equal(gate.isCurrent(second), false);
  const third = gate.begin();
  assert.equal(gate.isCurrent(third), true);

  gate.invalidate();
  assert.equal(gate.isCurrent(third), false);
});

test("serialized task queue preserves call order across delayed and failed tasks", async () => {
  const enqueue = createSerializedTaskQueue();
  const events = [];
  let releaseFirst;
  const firstBarrier = new Promise((resolve) => {
    releaseFirst = resolve;
  });

  const first = enqueue(async () => {
    events.push("first:start");
    await firstBarrier;
    events.push("first:end");
  });
  const second = enqueue(async () => {
    events.push("second");
    throw new Error("expected failure");
  });
  const secondRejected = assert.rejects(second, /expected failure/);
  const third = enqueue(async () => {
    events.push("third");
  });

  while (!events.length) await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(events, ["first:start"]);
  releaseFirst();
  await first;
  await secondRejected;
  await third;
  assert.deepEqual(events, ["first:start", "first:end", "second", "third"]);
});


test("latest task runner keeps one in-flight task and only the newest pending task", async () => {
  let releaseFirst;
  const firstGate = new Promise((resolve) => {
    releaseFirst = resolve;
  });
  const executed = [];
  const runLatest = createLatestTaskRunner();

  const first = runLatest(async () => {
    executed.push("first");
    await firstGate;
  });
  await Promise.resolve();
  const shared = runLatest(async () => {
    executed.push("stale");
  });
  const latest = runLatest(async () => {
    executed.push("latest");
  });

  assert.equal(first, shared);
  assert.equal(shared, latest);
  releaseFirst();
  await first;
  assert.deepEqual(executed, ["first", "latest"]);
});

test("latest task runner drains the newest pending task after a failure", async () => {
  let rejectFirst;
  const firstGate = new Promise((_, reject) => {
    rejectFirst = reject;
  });
  const executed = [];
  const runLatest = createLatestTaskRunner();
  const running = runLatest(async () => {
    executed.push("failed");
    await firstGate;
  });
  await Promise.resolve();
  const shared = runLatest(async () => {
    executed.push("latest");
  });

  rejectFirst(new Error("failed"));
  await assert.rejects(running, /failed/);
  await assert.rejects(shared, /failed/);
  assert.deepEqual(executed, ["failed", "latest"]);

  await runLatest(async () => {
    executed.push("recovered");
  });
  assert.deepEqual(executed, ["failed", "latest", "recovered"]);
});
