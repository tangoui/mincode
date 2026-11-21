import assert from "node:assert/strict";
import test from "node:test";
import { subagentCapabilities } from "../src/services/ai-tools/subagent-tools.ts";

function capabilities(overrides = {}) {
  return subagentCapabilities({
    runPlan: async (plan, context) => ({ plan, context }),
    ...overrides,
  });
}

test("registers parallel analysis with read mode as the only task mode", () => {
  const registered = capabilities();
  assert.deepEqual(registered.map((capability) => capability.tool.name), [
    "agent.run_parallel",
  ]);
  for (const capability of registered) {
    assert.equal(capability.module, "agent");
    assert.equal(capability.discovery, true);
    assert.equal(capability.target, "none");
    assert.equal(capability.mode, "write");
    assert.equal(capability.risk, "dangerous");
  }
  assert.deepEqual(registered[0].tool.inputSchema.properties.tasks.items.properties.mode.enum, ["read"]);
});

test("parallel plans advertise automatic uncapped conversation-independent scheduling", () => {
  const [parallel] = capabilities();
  const concurrency = parallel.tool.inputSchema.properties.maxConcurrency;
  assert.equal(concurrency.minimum, 1);
  assert.equal(concurrency.maximum, undefined);
  assert.match(concurrency.description, /Omit.*automatically run all ready tasks/);
  assert.match(parallel.tool.description, /Subagents cannot create other agents/);
  assert.equal(parallel.tool.inputSchema.properties.tasks.minItems, 1);
  assert.equal(parallel.tool.inputSchema.properties.tasks.maxItems, 16);
});

test("a plan of read-only tasks resolves to read risk, any write task keeps it dangerous", async () => {
  const capability = capabilities().find((c) => c.tool.name === "agent.run_parallel");
  const read = { tasks: [{ id: "a", mode: "read" }, { id: "b", mode: "read" }] };
  const mixed = { tasks: [{ id: "a", mode: "read" }, { id: "b", mode: "write" }] };
  assert.equal(await capability.resolveRisk({ input: read }), "read");
  assert.equal(await capability.resolveRisk({ input: mixed }), "dangerous");
  assert.equal(await capability.resolveRisk({ input: {} }), "dangerous");
  assert.equal(await capability.resolveRisk({ input: { tasks: [] } }), "dangerous");
});
