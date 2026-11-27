import assert from "node:assert/strict";
import test from "node:test";
import { createCapabilityRegistry } from "../src/domain/ai/capability-registry.ts";
import { terminalCapabilities } from "../src/services/ai-tools/terminal-tools.ts";

function fixture() {
  const capabilities = createCapabilityRegistry();
  const calls = [];
  const tools = terminalCapabilities({
    capabilities,
    listResources: () => [],
    invoke: async (command, args, options) => {
      calls.push({ command, args, signal: options?.signal });
      if (command === "terminal_control_read") return { text: "hello" };
      if (command === "terminal_control_wait") return { state: "waiting_input" };
      return { id: "pane-one", alive: true, state: "idle" };
    },
  });
  for (const tool of tools) capabilities.register(tool);
  const controller = new AbortController();
  const options = { signal: controller.signal };
  const execute = (name, args, consume) => capabilities.execute(`terminal.${name}`, args, {
    ...options,
    ...(consume ? { authorization: { conversationId: "chat", consume } } : {}),
  });
  return { capabilities, calls, execute, signal: controller.signal };
}

test("tool definitions expose the four pane controls with the required approval levels", () => {
  const { capabilities } = fixture();
  for (const [name, risk, required] of [
    ["split_pane", "write", ["direction"]],
    ["read_output", "read", ["pane"]],
    ["wait_idle", "read", ["pane"]],
    ["pane_info", "read", ["pane"]],
  ]) {
    const tool = capabilities.get(`terminal.${name}`);
    assert.equal(tool?.risk, risk);
    assert.deepEqual(tool.tool.inputSchema.required, required);
    assert.equal(tool.tool.inputSchema.additionalProperties, false);
  }
});

test("split sends the approved pane operation through terminal_control_split", async () => {
  const { execute, calls, signal } = fixture();
  const args = { pane: "pane-one", direction: "down", cwd: "/work", title: "Test", command: "echo hello" };
  await assert.rejects(execute("split_pane", args), /explicit authorization/);
  await assert.rejects(execute("split_pane", args, async () => false), /not authorized/);
  assert.equal(calls.length, 0);
  const result = await execute("split_pane", args, async ({ risk, parameters }) => risk === "write" && parameters === args);
  assert.equal(result.result.id, "pane-one");
  assert.deepEqual(calls, [{ command: "terminal_control_split", args, signal }]);
});

test("read, wait and info use their cancellable terminal control IPC with explicit pane IDs", async () => {
  const { calls, execute, signal } = fixture();
  assert.deepEqual(await execute("read_output", { pane: "pane-one", lines: 30 }), { result: { text: "hello" } });
  assert.deepEqual(await execute("wait_idle", { pane: "pane-one", timeoutSecs: 9 }), { result: { state: "waiting_input" } });
  await execute("pane_info", { pane: "pane-one" });
  assert.deepEqual(calls, [
    { command: "terminal_control_read", args: { pane: "pane-one", lines: 30 }, signal },
    { command: "terminal_control_wait", args: { pane: "pane-one", timeoutSecs: 9 }, signal },
    { command: "terminal_control_info", args: { pane: "pane-one" }, signal },
  ]);
});

test("invalid pane, direction, optional text and counts fail before the backend call", async () => {
  const { calls, execute } = fixture();
  for (const [name, args] of [
    ["split_pane", { direction: "left" }],
    ["split_pane", { direction: "right", cwd: 10 }],
    ["split_pane", { direction: "right", pane: "" }],
    ["read_output", { pane: "pane-one", lines: 4.5 }],
    ["read_output", { pane: "pane-one", lines: 5001 }],
    ["wait_idle", { pane: "pane-one", timeoutSecs: 0 }],
    ["wait_idle", { pane: "pane-one", timeoutSecs: 601 }],
    ["pane_info", { pane: " " }],
  ]) {
    await assert.rejects(execute(name, args, async () => true));
  }
  assert.equal(calls.length, 0);
});
