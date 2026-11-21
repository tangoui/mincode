import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { registerHooks } from "node:module";
import { setImmediate } from "node:timers/promises";
import test from "node:test";

// Exercise the real scheduler/runtime while replacing model/network I/O only.
const loopStub = `
  export const harness = { run: undefined };
  export function runAiLoop(options) { return harness.run(options); }
`;
const loopUrl = `data:text/javascript,${encodeURIComponent(loopStub)}`;
const failoverUrl = `data:text/javascript,${encodeURIComponent(`
  export function streamAiTurnWithFailover() {
    throw new Error("Unexpected real model request in subagent runtime test");
  }
`)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/ai-agent-loop") {
      return { url: loopUrl, shortCircuit: true };
    }
    if (specifier === "@/services/ai-model-failover") {
      return { url: failoverUrl, shortCircuit: true };
    }
    return nextResolve(specifier, context);
  },
});
const { harness } = await import(loopUrl);
const { executeSubagentPlan } = await import("../src/services/ai-subagent-runtime.ts");

function gate() {
  let release;
  const promise = new Promise((resolve) => { release = resolve; });
  return { promise, release };
}

function dependencies(conversationId, controller, started) {
  return {
    capabilities: {
      listCapabilities: () => [
        "workspace.read_file", "workspace.write_file", "git.status", "git.commit",
        "agent.run_parallel", "agent.integrate_result", "mcp.load_tools", "terminal.execute",
      ].map((name) => ({ tool: { name }, risk: "read" })),
      get: () => ({ risk: "read" }),
    },
    config: { model: "test-model" },
    models: ["test-model"],
    conversationId,
    parentRunId: `parent-${conversationId}`,
    workspace: { workspaceId: "test", workspaceScopeId: "test", projectRoot: "/test" },
    signal: controller.signal,
    invoke: async () => { throw new Error("Read-only test unexpectedly called native I/O"); },
    createResources: () => ({}),
    executeTool: async (_call, _resources, _root, _signal, allowedTools) => {
      assert.equal(allowedTools.has("agent.run_parallel"), false);
      assert.equal(allowedTools.has("mcp.load_tools"), false);
      return { content: "test result" };
    },
    startRun: async (input) => {
      started.push(input);
      return { runId: `${input.sessionId}:${started.length}` };
    },
    updateRun: async (input) => input,
    applyRun: () => {},
  };
}

function plan(count = 6) {
  return {
    title: "independent analysis",
    tasks: Array.from({ length: count }, (_, index) => ({
      id: `task-${index}`, title: `Task ${index}`, prompt: "Inspect the project", mode: "read",
    })),
  };
}

test("two conversations each dispatch more than four subagents without shared slots", async () => {
  const executionGate = gate();
  const controllers = [new AbortController(), new AbortController()];
  const started = [];
  const calls = [];
  harness.run = async (options) => {
    calls.push(options);
    assert.deepEqual(options.tools.map((tool) => tool.name), ["workspace.read_file", "git.status"]);
    assert.match(options.messages[0].content, /Do not.*create other agents/);
    await options.executeTool({ name: "workspace.read_file" }, options.signal);
    await executionGate.promise;
    return { completed: true, outcome: "task_completed", summary: "analysis complete" };
  };
  const promises = controllers.map((controller, index) => executeSubagentPlan(
    plan(), dependencies(`conversation-${index}`, controller, started),
  ));
  try {
    await setImmediate();
    assert.equal(calls.length, 12);
    for (const index of [0, 1]) {
      const children = started.filter((input) => input.parentRunId === `parent-conversation-${index}`);
      assert.equal(children.length, 6);
      assert.ok(children.every((input) => input.navigation.conversationId === `conversation-${index}`));
    }
    executionGate.release();
    const results = await Promise.all(promises);
    assert.ok(results.every((result) => result.state === "succeeded"));
    assert.ok(results.every((result) => result.tasks.length === 6));
  } finally {
    controllers.forEach((controller) => controller.abort());
    executionGate.release();
    await Promise.all(promises);
  }
});

test("cancelling one conversation does not abort another conversation's subagents", async () => {
  const executionGate = gate();
  const controllers = [new AbortController(), new AbortController()];
  const started = [];
  const calls = [];
  harness.run = (options) => {
    calls.push(options);
    return new Promise((resolve, reject) => {
      const onAbort = () => reject(options.signal.reason);
      options.signal.addEventListener("abort", onAbort, { once: true });
      executionGate.promise.then(() => {
        options.signal.removeEventListener("abort", onAbort);
        resolve({ completed: true, outcome: "task_completed", summary: "analysis complete" });
      });
    });
  };
  const promises = controllers.map((controller, index) => executeSubagentPlan(
    plan(), dependencies(`conversation-${index}`, controller, started),
  ));
  try {
    await setImmediate();
    assert.equal(calls.length, 12);
    controllers[0].abort();
    const cancelled = await promises[0];
    assert.equal(cancelled.state, "cancelled");
    assert.ok(cancelled.tasks.every((task) => task.state === "cancelled"));
    const survivorCalls = calls.filter((_call, index) =>
      started[index].parentRunId === "parent-conversation-1",
    );
    assert.equal(survivorCalls.length, 6);
    assert.ok(survivorCalls.every((call) => !call.signal.aborted));
    executionGate.release();
    const succeeded = await promises[1];
    assert.equal(succeeded.state, "succeeded");
    assert.ok(succeeded.tasks.every((task) => task.state === "succeeded"));
  } finally {
    controllers.forEach((controller) => controller.abort());
    executionGate.release();
    await Promise.all(promises);
  }
});

test("delegated runs navigate to distinct child conversations and publish completed turns", async () => {
  const started = [];
  const turns = [];
  const deps = dependencies("parent", new AbortController(), started);
  deps.conversationForTask = (task) => `child-${task.id}`;
  deps.onTaskTurn = (id, message) => turns.push([id, message.content]);
  harness.run = async (options) => {
    options.onTurnComplete?.({ role: "assistant", content: "Checked configuration" });
    return { outcome: "task_completed", summary: "Configuration checked" };
  };
  const result = await executeSubagentPlan(plan(2), deps);
  assert.deepEqual(started.map((run) => run.navigation.conversationId), ["child-task-0", "child-task-1"]);
  assert.deepEqual(turns, [
    ["child-task-0", "Checked configuration"],
    ["child-task-1", "Checked configuration"],
  ]);
  assert.deepEqual(result.tasks.map((task) => task.summary), ["Configuration checked", "Configuration checked"]);
});

test("a read delegation without a project can return a direct answer", async () => {
  const deps = dependencies("parent", new AbortController(), []);
  deps.workspace.projectRoot = "";
  harness.run = async ({ tools }) => {
    assert.deepEqual(tools, []);
    return { completed: true, outcome: "response", summary: "Drafted the outline", usedTools: false };
  };
  const definition = plan(1);
  const result = await executeSubagentPlan(definition, deps);
  assert.equal(result.state, "succeeded");
  assert.equal(result.tasks[0].summary, "Drafted the outline");
});

test("each delegated task uses its chosen provider and model", async () => {
  const deps = dependencies("parent", new AbortController(), []);
  const calls = [];
  deps.requestForTask = (task) => task.model === "other::model-b"
    ? { config: { model: "model-b", baseUrl: "https://other.example" }, models: ["model-b"] }
    : undefined;
  harness.run = async ({ config }) => {
    calls.push([config.model, config.baseUrl]);
    return { outcome: "task_completed", summary: "Done" };
  };
  const definition = plan(2);
  definition.tasks[1].model = "other::model-b";
  const result = await executeSubagentPlan(definition, deps);
  assert.equal(result.state, "succeeded");
  assert.deepEqual(calls, [["test-model", undefined], ["model-b", "https://other.example"]]);
});

test("write subagents also exclude agent creation and tool-discovery capabilities", async () => {
  const controller = new AbortController();
  let observed = 0;
  harness.run = async (options) => {
    observed += 1;
    assert.deepEqual(options.tools.map((tool) => tool.name), [
      "workspace.read_file", "workspace.write_file", "git.status", "git.commit",
    ]);
    assert.match(options.messages[0].content, /Never.*create other agents/);
    await options.executeTool({ name: "workspace.write_file" }, options.signal);
    return { completed: true, outcome: "task_completed", summary: "write complete" };
  };
  const deps = dependencies("write-conversation", controller, []);
  deps.invoke = async (command, input) => {
    if (command === "git_create_agent_worktree") {
      return { path: `/test/${input.taskId}`, projectPath: `/test/${input.taskId}`, name: input.taskId,
        branch: `mincode/agent/${input.taskId}`, baseCommit: "b".repeat(40) };
    }
    if (command === "git_inspect_agent_worktree") {
      return { clean: true, commitsAhead: 1, changedFiles: ["test.ts"], head: "a".repeat(40) };
    }
    throw new Error(`Unexpected command: ${command}`);
  };
  const definition = plan(2);
  definition.tasks.forEach((task) => { task.mode = "write"; });
  const result = await executeSubagentPlan(definition, deps);
  assert.equal(result.state, "succeeded");
  assert.equal(observed, 2);
  assert.equal(result.integrationRequired, true);

  // Even an unadvertised hallucinated call must meet the dispatch-side allowlist.
  const drawer = await readFile(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
  assert.match(drawer, /if \(policy\.allowedTools && !policy\.allowedTools\.has\(call\.name\)\)/);
  assert.match(drawer, /Tool is not allowed for this subagent/);
});

test("each subagent loop gets its own system reminder source", async () => {
  const controller = new AbortController();
  const reminders = [];
  const received = [];
  harness.run = async (options) => {
    received.push(options.systemReminder);
    return { completed: true, outcome: "task_completed", summary: "done" };
  };
  const deps = {
    ...dependencies("conversation-reminders", controller, []),
    createSystemReminder: () => {
      const reminder = () => "E";
      reminders.push(reminder);
      return reminder;
    },
  };
  try {
    const result = await executeSubagentPlan(plan(2), deps);
    assert.equal(result.state, "succeeded");
    assert.equal(received.length, 2);
    assert.deepEqual(new Set(received), new Set(reminders));
    assert.equal(new Set(received).size, 2);
  } finally {
    controller.abort();
  }
});

test("a subagent stopped at its loop limit fails with the wrap-up summary in the message", async () => {
  const controller = new AbortController();
  harness.run = async () => ({
    completed: false, outcome: "loop_limit", summary: "Did A; B remains", loopCount: 3,
  });
  const updates = [];
  const deps = dependencies("conversation-loop-limit", controller, []);
  deps.updateRun = async (input) => { updates.push(input); return input; };
  try {
    const result = await executeSubagentPlan(plan(2), deps);
    assert.equal(result.state, "failed");
    const expected = "Subagent reached its loop limit before completion.\n\nDid A; B remains";
    for (const task of result.tasks) {
      assert.equal(task.state, "failed");
      assert.equal(task.summary, undefined);
      assert.equal(task.error, expected);
    }
    assert.ok(updates.some((update) => update.phase === "failed" && update.summary === expected));
  } finally {
    controller.abort();
  }
});
