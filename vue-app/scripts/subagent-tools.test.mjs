import assert from "node:assert/strict";
import test from "node:test";
import { subagentCapabilities } from "../src/services/ai-tools/subagent-tools.ts";

function capabilities(overrides = {}) {
  return subagentCapabilities({
    runPlan: async (plan, context) => ({ plan, context }),
    integrateResult: async (result, context) => ({ result, context }),
    ...overrides,
  });
}

test("registers parallel execution and explicit integration as dangerous discovery tools", () => {
  const registered = capabilities();
  assert.deepEqual(registered.map((capability) => capability.tool.name), [
    "agent.run_parallel",
    "agent.integrate_result",
  ]);
  for (const capability of registered) {
    assert.equal(capability.module, "agent");
    assert.equal(capability.discovery, true);
    assert.equal(capability.target, "none");
    assert.equal(capability.mode, "write");
    assert.equal(capability.risk, "dangerous");
  }
  const integration = registered[1].tool.inputSchema;
  assert.deepEqual(integration.required, ["branch", "commitHash", "baseCommit"]);
  assert.equal(integration.additionalProperties, false);
  assert.equal(integration.properties.commitHash.pattern, "^[0-9a-fA-F]{40,64}$");
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

test("integration preview and execution preserve the exact reviewed result", async () => {
  let received;
  const [_, integration] = capabilities({
    integrateResult: async (result, context) => {
      received = { result, context };
      return { cleaned: true };
    },
  });
  const input = {
    branch: "mincode/agent/task-123",
    commitHash: "a".repeat(40),
    baseCommit: "b".repeat(40),
  };
  const preview = await integration.approvalPreview({ input });
  assert.match(preview, /mincode\/agent\/task-123/);
  assert.match(preview, /aaaaaaaaaaaa/);
  const signal = new AbortController().signal;
  const value = await integration.execute({
    input,
    signal,
    conversationId: "conversation-1",
    resourceId: "agent:global",
    resource: {
      id: "agent:global",
      kind: "global",
      name: "agent",
      state: "available",
      capabilities: ["agent.integrate_result"],
    },
  });
  assert.deepEqual(value, { cleaned: true });
  assert.deepEqual(received.result, input);
  assert.equal(received.context.conversationId, "conversation-1");
  assert.equal(received.context.signal, signal);
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
