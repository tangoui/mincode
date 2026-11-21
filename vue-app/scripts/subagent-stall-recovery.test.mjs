import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";

async function source(path) {
  return readFile(new URL(path, import.meta.url), "utf8");
}

test("subagent requests use a bounded no-output timeout and preserve failover", async () => {
  const [runtime, failover, loop] = await Promise.all([
    source("../src/services/ai-subagent-runtime.ts"),
    source("../src/services/ai-model-failover.ts"),
    source("../src/services/ai-agent-loop.ts"),
  ]);
  assert.match(runtime, /SUBAGENT_MODEL_IDLE_TIMEOUT_MS\s*=\s*90_000/);
  assert.match(runtime, /idleTimeoutMs:\s*SUBAGENT_MODEL_IDLE_TIMEOUT_MS/);
  assert.match(failover, /options\.signal, options\.idleTimeoutMs/);
  assert.match(loop, /createAiStreamIdleWatchdog\(cancelProvider, idleTimeoutMs\)/);
});

test("subagent scheduler snapshots replace the static parallel-task presentation", async () => {
  const [runtime, drawer] = await Promise.all([
    source("../src/services/ai-subagent-runtime.ts"),
    source("../src/components/ai/AiAssistantDrawer.vue"),
  ]);
  assert.match(runtime, /onPlanUpdate\?\(snapshot:\s*SubagentPlanSnapshot\)/);
  assert.match(runtime, /onUpdate:\s*\(current\)\s*=>\s*dependencies\.onPlanUpdate\?\.\(current\)/);
  assert.match(drawer, /applySubagentPlanSnapshot\(conversationId, snapshot\)/);
  assert.match(drawer, /status:\s*subagentTaskPresentationStatus\(task\.state\)/);
});

test("subagent runtime has no shared concurrency pool and checks cancellation before setup", async () => {
  const runtime = await source("../src/services/ai-subagent-runtime.ts");
  assert.doesNotMatch(runtime, /globalSubagentExecutionPool|subagent-execution-pool|releaseGlobalSlot/);
  assert.match(runtime, /execute:\s*async \(task, execution\) => \{\s*throwIfExecutionAborted\(execution\.signal\)/);
});
