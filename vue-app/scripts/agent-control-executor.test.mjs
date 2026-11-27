import assert from "node:assert/strict";
import { setImmediate as tick } from "node:timers/promises";
import test from "node:test";
import { createCapabilityRegistry } from "../src/domain/ai/capability-registry.ts";
import { createResourceRegistry } from "../src/domain/ai/resource-registry.ts";
import { startAgentControlExecutor } from "../src/services/agent-control-executor.ts";

function fixture(t, resolveWriteRoot) {
  const capabilities = createCapabilityRegistry();
  const handlers = new Map();
  const commands = [];
  const panes = [];
  const snapshots = [];
  const contexts = [];
  const register = (name, execute, risk = "read") => capabilities.register({
    module: "sample", tool: { name, description: "", inputSchema: { type: "object" } },
    target: "none", mode: risk === "read" ? "read" : "write", risk, execute,
  });
  t.after(startAgentControlExecutor({
    capabilities, operations: {},
    listen: (event, handler) => {
      handlers.set(event, handler);
      return () => handlers.delete(event);
    },
    invoke: async (command, args) => { commands.push({ command, args }); },
    resolveProjectRoot: (origin) => origin.kind === "pane" ? origin.cwd : "/current",
    resources: async (root) => { contexts.push(root); return createResourceRegistry(); },
    resolveWriteRoot,
    beforeWrite: async (requestId, root) => { snapshots.push([requestId, root]); },
    afterWrite: async () => {},
    paneOperation: async (request) => {
      panes.push(request);
      return { id: "new", cwd: "/work", title: "Shell", alive: true, state: "working" };
    },
    message: (key) => key,
  }));
  const fire = (event, data) => handlers.get(event)(data);
  const call = (name, requestId = "call") => fire("agent-control:call", {
    name, requestId, arguments: {}, origin: { kind: "pane", cwd: "/work" },
  });
  const result = () => commands.filter(({ command }) => command === "agent_control_result").at(-1)?.args;
  return { register, fire, call, result, commands, panes, snapshots, contexts };
}

test("the ready list tracks plugin capabilities and excludes conversation tools", async (t) => {
  const { register, call, result, commands, contexts } = fixture(t);
  register("ask_user", () => assert.fail("excluded tool executed"));
  const unregister = register("plugin.sample", () => 42);
  await tick();
  assert.deepEqual(commands.at(-1).args.tools.map(({ name }) => name), ["plugin.sample"]);
  call("ask_user");
  await tick();
  assert.equal(result().isError, true);
  assert.deepEqual(contexts, []);
  unregister();
  await tick();
  assert.deepEqual(commands.at(-1).args.tools, []);
});

test("tool results redact sensitive fields, plaintext assignments and PEM blocks", async (t) => {
  const { register, call, result, snapshots, contexts } = fixture(t);
  register("sample.write", () => ({
    password: "saved secret with spaces",
    error: 'token="a token with spaces"',
    stdout: "password=probe-one probe-two\nAPI key: probe-plain",
    log: "Authorization: Bearer test-token\n-----BEGIN PRIVATE KEY-----\nfake-key\n-----END PRIVATE KEY-----",
  }), "write");
  call("sample.write");
  await tick();
  assert.equal(result().isError, false);
  const text = result().content[0].text;
  assert.match(text, /REDACTED/);
  assert.doesNotMatch(text, /saved secret|with spaces|test-token|fake-key|probe-one|probe-two|probe-plain/);
  assert.deepEqual(contexts, ["/work"]);
  assert.deepEqual(snapshots, [["call", "/work"]]);
});

test("text is truncated at the UTF-8 limit with a marker", async (t) => {
  const { register, call, result } = fixture(t);
  register("sample.long", () => "汉".repeat(200_000));
  call("sample.long");
  await tick();
  const text = result().content[0].text;
  assert.ok(Buffer.byteLength(text) <= 200_000);
  assert.match(text, /truncated/);
  assert.doesNotMatch(text, /\uFFFD/);
});

test("format a visual capture as MCP image content and bound decoded image size", async (t) => {
  const { register, call, result } = fixture(t);
  const image = Buffer.from("small probe").toString("base64");
  const stop = register("browser.capture_view", () => ({ data: image, mimeType: "image/png" }));
  call("browser.capture_view");
  await tick();
  assert.deepEqual(result().content[1], { type: "image", mimeType: "image/png", data: image });
  assert.doesNotMatch(result().content[0].text, new RegExp(image));
  stop();
  register("browser.capture_view", () => ({ data: "AAAA".repeat(1_800_000), mimeType: "image/png" }));
  call("browser.capture_view");
  await tick();
  assert.equal(result().content.length, 1);
  assert.match(result().content[0].text, /imageTooLarge/);
});

test("cancel interrupts an in-flight call and sends one cancellation result", async (t) => {
  const { register, fire, call, result, commands } = fixture(t);
  let signal;
  register("sample.pending", (context) => {
    signal = context.signal;
    return new Promise(() => {});
  });
  call("sample.pending");
  await tick();
  fire("agent-control:cancel", { requestId: "call" });
  await tick();
  assert.equal(signal.aborted, true);
  assert.equal(result().isError, true);
  assert.match(result().content[0].text, /cancelled/);
  assert.equal(commands.filter(({ command }) => command === "agent_control_result").length, 1);
});

test("split events call workspace layout operations and return PaneInfo", async (t) => {
  const { fire, commands, panes } = fixture(t);
  const request = { requestId: "split", op: "split", pane: "existing", direction: "right" };
  fire("agent-control:pane-op", request);
  await tick();
  assert.deepEqual(panes, [request]);
  const reply = commands.find(({ command }) => command === "agent_control_pane_result");
  assert.equal(reply.args.requestId, "split");
  assert.deepEqual(reply.args.pane, { id: "new", cwd: "/work", title: "Shell", alive: true, state: "working" });
});

test("an aborted SQL confirmation cannot authorize a later retry", async () => {
  const { createAiSqlSafety } = await import("../src/services/ai-tools/sql-safety.ts");
  const abort = new AbortController();
  let confirmationSignal;
  let wireSource;
  let complete;
  const safety = createAiSqlSafety(async () => undefined, {
    runWithSafety: async ({ confirm, execute, source }) => {
      wireSource = source;
      if (await confirm({ risk: "high" }, "ai")) return execute({ checkId: "test" });
    },
    sqlSafetyArgs: () => ({}),
    confirm: async (_check, _origin, _notice, _summary, signal) => {
      confirmationSignal = signal;
      return new Promise((resolve) => { complete = resolve; });
    },
  });
  const pending = safety.run({
    sessionId: "database", signal: abort.signal, externalAgent: true,
    execute: () => assert.fail("aborted SQL was submitted"),
  });
  const rejected = assert.rejects(pending, { name: "AbortError" });
  await tick();
  abort.abort();
  complete({ confirmText: "test" });
  await rejected;
  assert.equal(confirmationSignal, abort.signal);
  assert.equal(wireSource, "ai");
});


test("snapshots use the resolved write directory instead of the origin workspace", async (t) => {
  const { register, fire, snapshots } = fixture(t, (input, resource, root) => {
    assert.equal(root, "/work");
    assert.equal(resource.id, "sample:global");
    return input.cwd;
  });
  register("terminal.run_command", () => "written", "write");
  fire("agent-control:call", {
    requestId: "cwd", name: "terminal.run_command", arguments: { cwd: "/other" },
    origin: { kind: "pane", cwd: "/work" },
  });
  await tick();
  assert.deepEqual(snapshots, [["cwd", "/other"]]);
});

test("cancellation closes a shell whose start completes after the request ends", async () => {
  const { startAgentControlShell } = await import("../src/services/agent-control-terminal-launch.ts");
  const abort = new AbortController();
  const { promise, resolve } = Promise.withResolvers();
  const closed = [];
  const pending = startAgentControlShell(abort.signal, () => promise, async () => { closed.push(true); });
  const rejected = assert.rejects(pending, { name: "AbortError" });
  abort.abort();
  resolve("Shell started");
  await rejected;
  assert.deepEqual(closed, [true]);
});
