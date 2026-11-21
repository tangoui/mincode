import assert from "node:assert/strict";
import test from "node:test";

import { executeToolBatches, interruptedToolBatchResults } from "../src/domain/ai/tool-execution-batch.ts";

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

test("runs adjacent read-only calls concurrently and preserves result order", async () => {
  const calls = [
    { id: "read-1", readOnly: true },
    { id: "read-2", readOnly: true },
    { id: "write", readOnly: false },
    { id: "read-3", readOnly: true },
    { id: "read-4", readOnly: true },
  ];
  let running = 0;
  let maximumRunning = 0;
  const started = [];

  const results = await executeToolBatches(
    calls,
    (call) => call.readOnly,
    async (call) => {
      started.push(call.id);
      running += 1;
      maximumRunning = Math.max(maximumRunning, running);
      await wait(5);
      running -= 1;
      return `result:${call.id}`;
    },
  );

  assert.equal(maximumRunning, 2);
  assert.deepEqual(started, ["read-1", "read-2", "write", "read-3", "read-4"]);
  assert.deepEqual(results, calls.map((call) => `result:${call.id}`));
});

test("keeps non-parallel calls serialized", async () => {
  const calls = [{ id: "one" }, { id: "two" }, { id: "three" }];
  let running = 0;
  let maximumRunning = 0;

  await executeToolBatches(
    calls,
    () => false,
    async (call) => {
      running += 1;
      maximumRunning = Math.max(maximumRunning, running);
      await wait(2);
      running -= 1;
      return call.id;
    },
  );

  assert.equal(maximumRunning, 1);
});

test("an interrupted batch keeps finished results in call order and marks the rest", () => {
  const calls = [{ id: "write-1" }, { id: "write-2" }, { id: "read-3" }];
  const finished = new Map([["read-3", "read ok"], ["write-1", "wrote file"]]);
  assert.deepEqual(
    interruptedToolBatchResults(calls, finished, "interrupted"),
    ["wrote file", "interrupted", "read ok"],
  );
});

test("the agent loop records a stopped batch before rethrowing", async () => {
  const { readFile } = await import("node:fs/promises");
  const loop = await readFile(new URL("../src/services/ai-agent-loop.ts", import.meta.url), "utf8");
  assert.match(loop, /finishedResults\.set\(call\.id, result\);/);
  assert.match(
    loop,
    /catch \(error\) \{[\s\S]*?interruptedToolBatchResults\(businessCalls, finishedResults, INTERRUPTED_TOOL_RESULT\)[\s\S]*?notifyHistoryChange\(\);\s*throw error;/,
  );
  assert.match(loop, /INTERRUPTED_TOOL_RESULT: AiLoopToolExecution = \{[\s\S]*?status: "cancelled"/);
});
