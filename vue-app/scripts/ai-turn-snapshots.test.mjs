import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { registerHooks } from "node:module";
import test from "node:test";

// The snapshot service reaches the backend only through desktop-bridge; stub it so
// the test can switch between a desktop and a browser runtime and record calls.
const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
const desktopBridgeStub = `
  const state = globalThis.__snapshotDesktop;
  export const isDesktopRuntime = () => state.runtime;
  export const invokeBackend = async (command, args) => {
    state.calls.push([command, args]);
    return state.reply(command, args);
  };
`;
globalThis.__snapshotDesktop = { runtime: false, calls: [], reply: () => undefined };
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/desktop-bridge") return { url: dataModule(desktopBridgeStub), shortCircuit: true };
    return nextResolve(specifier, context);
  },
});

const {
  createTurnSnapshotRecorder,
  isSideEffectRisk,
  mergeTurnSnapshots,
  normalizeTurnSnapshots,
  resolveTurnSnapshotRoot,
  retainTurnSnapshots,
} = await import("../src/domain/ai/turn-snapshots.ts");
const service = await import("../src/services/ai-snapshot-service.ts");

const capture = (tree, extra = {}) => ({ tree, head: "h1", branch: "main", skipped: [], ...extra });

/** A fake capture whose calls are recorded and whose results come from a queue per root. */
function fakeCapture(results = {}) {
  const calls = [];
  const fn = async (root) => {
    calls.push(root);
    const queue = results[root] ?? [];
    const next = queue.length > 1 ? queue.shift() : queue[0];
    if (next instanceof Error) throw next;
    if (typeof next === "function") return next();
    return next ?? capture(`${root}-tree-${calls.length}`);
  };
  return { fn, calls };
}

test("the before snapshot is captured once per turn and root, and only for side effects", async () => {
  assert.equal(isSideEffectRisk("read"), false);
  for (const risk of ["network", "write", "dangerous"]) assert.equal(isSideEffectRisk(risk), true);

  const { fn, calls } = fakeCapture({ "/p": [capture("t-before"), capture("t-after")] });
  const recorder = createTurnSnapshotRecorder({ capture: fn, timeoutMs: 3000 });
  await recorder.beforeSideEffect("u1", "/p");
  await recorder.beforeSideEffect("u1", "/p");
  // Two concurrent calls while the capture is in flight share it, too.
  await Promise.all([recorder.beforeSideEffect("u1", "/p"), recorder.beforeSideEffect("u1", "/p")]);
  assert.deepEqual(calls, ["/p"]);

  const [record] = await recorder.finish("u1");
  assert.equal(record.status, "captured");
  assert.deepEqual(record.before, { tree: "t-before", head: "h1", branch: "main" });
  assert.deepEqual(record.after, { tree: "t-after", head: "h1", branch: "main" });
  assert.deepEqual(calls, ["/p", "/p"]);

  // A turn that never reached a side effect records nothing and captures nothing.
  assert.deepEqual(await recorder.finish("u-read-only"), []);
  assert.deepEqual(calls, ["/p", "/p"]);
});

test("a slow capture is abandoned after 3 seconds and the turn is marked no_snapshot", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 1_000 });
  let resolveLate;
  const calls = [];
  const recorder = createTurnSnapshotRecorder({
    capture: (root) => {
      calls.push(root);
      return new Promise((resolve) => { resolveLate = resolve; });
    },
    timeoutMs: 3000,
  });
  let returned = false;
  const before = recorder.beforeSideEffect("u1", "/big").then(() => { returned = true; });
  await Promise.resolve();
  t.mock.timers.tick(2999);
  await Promise.resolve();
  assert.equal(returned, false, "still waiting inside the budget");
  t.mock.timers.tick(1);
  await before;
  assert.equal(returned, true, "the tool runs once the budget is spent");

  // A later side effect in the same turn does not start a second capture for that root.
  await recorder.beforeSideEffect("u1", "/big");
  assert.deepEqual(calls, ["/big"]);

  // The abandoned capture settles late; it must not turn the record into a snapshot.
  resolveLate(capture("late-tree"));
  await Promise.resolve();
  const records = await recorder.finish("u1");
  assert.equal(records.length, 1);
  assert.equal(records[0].status, "no_snapshot");
  assert.equal(records[0].before, undefined);
  assert.equal(records[0].after, undefined);
  assert.equal(records[0].startedAt, 1_000);
  assert.deepEqual(calls, ["/big"], "no after snapshot for a turn without a before snapshot");
});

test("a failed before capture is recorded as no_snapshot and the tool still runs", async () => {
  const { fn } = fakeCapture({ "/p": [new Error("disk full")] });
  const recorder = createTurnSnapshotRecorder({ capture: fn, timeoutMs: 3000 });
  await recorder.beforeSideEffect("u1", "/p");
  const [record] = await recorder.finish("u1");
  assert.equal(record.status, "no_snapshot");
});

test("finish captures the after snapshot even when the run was stopped", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 5_000 });
  const { fn, calls } = fakeCapture({
    "/p": [capture("t-before", { skipped: ["big.bin"] }), capture("t-after", { head: "h2", skipped: ["big.bin", "huge.iso"] })],
  });
  const recorder = createTurnSnapshotRecorder({ capture: fn, timeoutMs: 3000 });
  await recorder.beforeSideEffect("u1", "/p");
  t.mock.timers.tick(250);
  // The run is aborted here; the drawer's finally still calls finish.
  const [record] = await recorder.finish("u1");
  assert.deepEqual(calls, ["/p", "/p"]);
  assert.equal(record.status, "captured");
  assert.deepEqual(record.before, { tree: "t-before", head: "h1", branch: "main", skipped: ["big.bin"] });
  assert.deepEqual(record.after, { tree: "t-after", head: "h2", branch: "main", skipped: ["big.bin", "huge.iso"] });
  assert.equal(record.startedAt, 5_000);
  assert.equal(record.finishedAt, 5_250);

  // A stopped tool whose promise outlives the turn cannot reopen the finished record.
  await recorder.beforeSideEffect("u1", "/p");
  assert.deepEqual(await recorder.finish("u1"), []);
  assert.deepEqual(calls, ["/p", "/p"]);
});

test("finish waits for a before capture still in flight when the run stops", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let resolveBefore;
  let call = 0;
  const recorder = createTurnSnapshotRecorder({
    capture: () => (++call === 1 ? new Promise((resolve) => { resolveBefore = resolve; }) : Promise.resolve(capture("t-after"))),
    timeoutMs: 3000,
  });
  // The tool call is still waiting for its before snapshot when the run is stopped.
  void recorder.beforeSideEffect("u1", "/p");
  const finished = recorder.finish("u1");
  resolveBefore(capture("t-before"));
  const [record] = await finished;
  assert.equal(record.status, "captured");
  assert.equal(record.before.tree, "t-before");
  assert.equal(record.after.tree, "t-after");
});

test("a failed after capture leaves the turn without a usable snapshot", async () => {
  const { fn } = fakeCapture({ "/p": [capture("t-before"), new Error("gone")] });
  const recorder = createTurnSnapshotRecorder({ capture: fn, timeoutMs: 3000 });
  await recorder.beforeSideEffect("u1", "/p");
  const [record] = await recorder.finish("u1");
  assert.equal(record.status, "no_snapshot");
  assert.equal(record.before.tree, "t-before");
  assert.equal(record.after, undefined);
});

test("a hung after capture is abandoned so the turn can end", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let call = 0;
  const recorder = createTurnSnapshotRecorder({
    capture: () => (++call === 1 ? Promise.resolve(capture("t-before")) : new Promise(() => {})),
    timeoutMs: 3000,
    finishTimeoutMs: 20_000,
  });
  await recorder.beforeSideEffect("u1", "/p");
  const finished = recorder.finish("u1");
  await Promise.resolve();
  await Promise.resolve();
  t.mock.timers.tick(20_000);
  const [record] = await finished;
  assert.equal(record.status, "no_snapshot");
  assert.equal(record.after, undefined);
});

test("a non-git project is recorded as unsupported and never captures again", async () => {
  const { fn, calls } = fakeCapture({ "/plain": [{ ...capture(""), unsupported: "not_git" }] });
  const recorder = createTurnSnapshotRecorder({ capture: fn, timeoutMs: 3000 });
  await recorder.beforeSideEffect("u1", "/plain");
  await recorder.beforeSideEffect("u1", "/plain");
  const [record] = await recorder.finish("u1");
  assert.equal(record.status, "unsupported");
  assert.equal(record.before, undefined);
  assert.equal(record.after, undefined);
  assert.deepEqual(calls, ["/plain"], "no after snapshot for an unsupported root");

  // The next turn knows the root is unsupported without asking the backend.
  await recorder.beforeSideEffect("u2", "/plain");
  const [next] = await recorder.finish("u2");
  assert.equal(next.status, "unsupported");
  assert.deepEqual(calls, ["/plain"]);

  // A background baseline that finds a non-git root teaches the recorder too.
  const baseline = fakeCapture({ "/other": [{ ...capture(""), unsupported: "not_git" }] });
  const warmed = createTurnSnapshotRecorder({ capture: baseline.fn, timeoutMs: 3000 });
  await warmed.warmUp("/other");
  await warmed.beforeSideEffect("u3", "/other");
  assert.equal((await warmed.finish("u3"))[0].status, "unsupported");
  assert.deepEqual(baseline.calls, ["/other"]);
});

test("a background baseline never rejects and is captured once per root", async () => {
  const { fn, calls } = fakeCapture({ "/p": [new Error("not now"), capture("ok")] });
  const recorder = createTurnSnapshotRecorder({ capture: fn, timeoutMs: 3000 });
  await recorder.warmUp("/p");
  await recorder.warmUp("");
  assert.deepEqual(calls, ["/p"]);
  // A failed baseline may be retried later.
  await recorder.warmUp("/p");
  await recorder.warmUp("/p");
  assert.deepEqual(calls, ["/p", "/p"]);
});

test("writes to two roots in one turn produce one snapshot record per root", async () => {
  const { fn, calls } = fakeCapture();
  const recorder = createTurnSnapshotRecorder({ capture: fn, timeoutMs: 3000 });
  await recorder.beforeSideEffect("u1", "/a");
  await recorder.beforeSideEffect("u1", "/b");
  await recorder.beforeSideEffect("u1", "/a");
  // Another conversation's turn is recorded separately.
  await recorder.beforeSideEffect("other", "/a");
  const records = await recorder.finish("u1");
  assert.deepEqual(records.map((record) => [record.userMessageId, record.projectRoot, record.status]), [
    ["u1", "/a", "captured"],
    ["u1", "/b", "captured"],
  ]);
  assert.deepEqual(calls.sort(), ["/a", "/a", "/a", "/b", "/b"]);
  assert.equal((await recorder.finish("other")).length, 1);
});

test("records are keyed by the canonical root the backend reports", async () => {
  const canonical = (root) => ({ "/link/app": "/real/app", "/real/app/": "/real/app", "/real/app": "/real/app" })[root] ?? root;
  const calls = [];
  const recorder = createTurnSnapshotRecorder({
    capture: async (root) => {
      calls.push(root);
      return capture(`tree-${calls.length}`, { projectRoot: canonical(root) });
    },
    timeoutMs: 3000,
  });
  // The first spelling learns the canonical root; later spellings reuse its record.
  await recorder.beforeSideEffect("u1", "/link/app");
  await recorder.beforeSideEffect("u1", "/real/app/");
  await recorder.beforeSideEffect("u1", "/real/app");
  const records = await recorder.finish("u1");
  assert.deepEqual(records.map((record) => [record.projectRoot, record.status, record.before.tree]), [["/real/app", "captured", "tree-1"]]);
  assert.deepEqual(calls, ["/link/app", "/real/app"], "one before (unknown spelling) and one after (canonical root)");

  // A baseline teaches the canonical root before the turn starts.
  const warmCalls = [];
  const warmed = createTurnSnapshotRecorder({
    capture: async (root) => { warmCalls.push(root); return capture("t", { projectRoot: canonical(root) }); },
    timeoutMs: 3000,
  });
  await warmed.warmUp("/link/app");
  await warmed.beforeSideEffect("u2", "/link/app");
  await warmed.beforeSideEffect("u2", "/real/app");
  assert.deepEqual((await warmed.finish("u2")).map((record) => record.projectRoot), ["/real/app"]);
  assert.deepEqual(warmCalls, ["/link/app", "/real/app", "/real/app"]);
});

test("two unknown spellings captured concurrently collapse into the earliest record", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"], now: 100 });
  let resolveFirst;
  let call = 0;
  const recorder = createTurnSnapshotRecorder({
    capture: (root) => {
      call += 1;
      if (call === 1) return new Promise((resolve) => { resolveFirst = () => resolve(capture("late", { projectRoot: "/real/app" })); });
      return Promise.resolve(capture(`tree-${call}`, { projectRoot: "/real/app" }));
    },
    timeoutMs: 3000,
  });
  // The first spelling times out, the second captures; the earliest (no snapshot) wins.
  const first = recorder.beforeSideEffect("u1", "/link/app");
  await Promise.resolve();
  t.mock.timers.tick(3000);
  await first;
  t.mock.timers.tick(10);
  await recorder.beforeSideEffect("u1", "/real/app/");
  resolveFirst();
  await Promise.resolve();
  const records = await recorder.finish("u1");
  assert.equal(records.length, 1);
  assert.equal(records[0].projectRoot, "/real/app", "a late result still names the canonical root");
  assert.equal(records[0].status, "no_snapshot");
  assert.equal(records[0].startedAt, 100);
});

test("the snapshot root follows the resource, then the command directory, then the bound project", () => {
  const allowedRoots = ["/w/app", "/w/app/packages/lib", "/w/docs"];
  // A workspace or git resource names its project root.
  assert.equal(resolveTurnSnapshotRoot({ resourceRoot: "/w/docs", boundRoot: "/w/app", allowedRoots }), "/w/docs");
  // A resource outside the conversation's projects (a subagent's private worktree) is not snapshotted.
  assert.equal(resolveTurnSnapshotRoot({ resourceRoot: "/tmp/agent-worktree", boundRoot: "/w/app", allowedRoots }), undefined);
  // A command runs in the most specific project that contains its directory.
  assert.equal(resolveTurnSnapshotRoot({ cwd: "/w/app/packages/lib/src", boundRoot: "/w/app", allowedRoots }), "/w/app/packages/lib");
  assert.equal(resolveTurnSnapshotRoot({ cwd: "/w/application", boundRoot: "/w/docs", allowedRoots }), "/w/docs");
  // Outside every project, or with no target at all (MCP tools), the bound project is used.
  assert.equal(resolveTurnSnapshotRoot({ cwd: "/tmp", boundRoot: "/w/app", allowedRoots }), "/w/app");
  assert.equal(resolveTurnSnapshotRoot({ boundRoot: "/w/app", allowedRoots }), "/w/app");
  // Without a bound project (relayed conversations) nothing is snapshotted.
  assert.equal(resolveTurnSnapshotRoot({ boundRoot: "", allowedRoots }), undefined);
  assert.equal(resolveTurnSnapshotRoot({ cwd: "/tmp", allowedRoots: [] }), undefined);
  // Windows paths and trailing separators match their project.
  assert.equal(resolveTurnSnapshotRoot({ cwd: "C:\\w\\app\\src", allowedRoots: ["C:\\w\\app\\"] }), "C:\\w\\app\\");
});

test("stored turn snapshots are normalized, merged per turn and root, and trimmed on rewind", () => {
  const good = { userMessageId: "u1", projectRoot: "/p", status: "captured", before: { tree: "a", head: "h", branch: "main" }, after: { tree: "b", head: "h", branch: "main" }, startedAt: 1, finishedAt: 2 };
  assert.deepEqual(normalizeTurnSnapshots(undefined), []);
  assert.deepEqual(normalizeTurnSnapshots("bad"), []);
  const kept = [good];
  assert.equal(normalizeTurnSnapshots(kept), kept, "valid data keeps its identity");
  assert.deepEqual(normalizeTurnSnapshots([good, null, { userMessageId: "u2" }, { ...good, status: "weird" }, { ...good, userMessageId: "u3", before: { tree: 1 } }]), [
    good,
    { userMessageId: "u3", projectRoot: "/p", status: "no_snapshot", after: good.after, startedAt: 1, finishedAt: 2 },
  ]);

  const replaced = { ...good, status: "no_snapshot", after: undefined };
  const second = { ...good, projectRoot: "/q" };
  assert.deepEqual(mergeTurnSnapshots([good], [replaced, second]), [replaced, second]);

  assert.deepEqual(retainTurnSnapshots([good, second, { ...good, userMessageId: "u9" }], new Set(["u1"])), [good, second]);
});

test("the snapshot service calls the backend commands and reports a browser runtime as unsupported", async () => {
  const desktop = globalThis.__snapshotDesktop;
  desktop.runtime = false;
  desktop.calls = [];
  assert.deepEqual(await service.captureProjectSnapshot("/p"), { projectRoot: "/p", tree: "", head: "", branch: "", skipped: [], unsupported: "browser" });
  assert.deepEqual(await service.restoreProjectSnapshot({ projectRoot: "/p", target: "a", expected: "b", paths: ["x"] }), {
    restored: [], deleted: [], conflicts: [], unsupported: "browser",
  });
  for (const call of [
    () => service.diffProjectSnapshots("/p", "a", "b"),
    () => service.pruneProjectSnapshots("/p", ["a"]),
    () => service.projectSnapshotSize("/p"),
  ]) {
    await assert.rejects(call, (error) => error instanceof service.SnapshotUnsupportedError && error.reason === "browser");
  }
  assert.deepEqual(desktop.calls, []);

  desktop.runtime = true;
  desktop.reply = (command) => ({
    ai_snapshot_capture: { projectRoot: "/real/p", tree: "t", head: "h", branch: "main", skipped: null, unsupported: "" },
    ai_snapshot_diff: { changes: [{ path: "a.txt", status: "modified" }] },
    ai_snapshot_restore: { restored: ["a.txt"], deleted: null, conflicts: ["b.txt"] },
    ai_snapshot_prune: { bytes: 12 },
    ai_snapshot_size: { bytes: 34 },
  })[command];
  assert.deepEqual(await service.captureProjectSnapshot("/p"), { projectRoot: "/real/p", tree: "t", head: "h", branch: "main", skipped: [] });
  assert.deepEqual(await service.diffProjectSnapshots("/p", "a", "b"), [{ path: "a.txt", status: "modified" }]);
  assert.deepEqual(await service.restoreProjectSnapshot({ projectRoot: "/p", target: "a", expected: "b", paths: ["a.txt"] }), {
    restored: ["a.txt"], deleted: [], conflicts: ["b.txt"],
  });
  assert.equal(await service.pruneProjectSnapshots("/p", ["a"]), 12);
  assert.equal(await service.projectSnapshotSize("/p"), 34);
  assert.deepEqual(desktop.calls, [
    ["ai_snapshot_capture", { projectRoot: "/p" }],
    ["ai_snapshot_diff", { projectRoot: "/p", from: "a", to: "b" }],
    ["ai_snapshot_restore", { projectRoot: "/p", target: "a", expected: "b", paths: ["a.txt"] }],
    ["ai_snapshot_prune", { projectRoot: "/p", keep: ["a"] }],
    ["ai_snapshot_size", { projectRoot: "/p" }],
  ]);
  desktop.runtime = false;

  desktop.runtime = true;
  desktop.reply = () => ({ tree: "", head: "", branch: "", skipped: [], unsupported: "not_git" });
  assert.deepEqual(await service.captureProjectSnapshot("/plain"), { projectRoot: "/plain", tree: "", head: "", branch: "", skipped: [], unsupported: "not_git" });
  desktop.runtime = false;
});

// Drawer wiring: the source is a single large SFC, so its structure is pinned here.
const drawer = readFileSync(new URL("../src/components/ai/AiAssistantDrawer.vue", import.meta.url), "utf8");
const store = readFileSync(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8");

function functionBody(source, signature, terminator = "\n}\n") {
  const start = source.indexOf(signature);
  assert.notEqual(start, -1, `missing ${signature}`);
  const end = source.indexOf(terminator, start);
  assert.notEqual(end, -1, `unterminated ${signature}`);
  return source.slice(start, end);
}

const executeAgentTool = functionBody(drawer, "async function executeAgentTool(");
const mcpBranch = executeAgentTool.slice(
  executeAgentTool.indexOf("if (mcpTool) {"),
  executeAgentTool.indexOf("const capability = aiCapabilityRegistry.get(call.name);"),
);
const nativeBranch = executeAgentTool.slice(executeAgentTool.indexOf("const capability = aiCapabilityRegistry.get(call.name);"));

test("the drawer snapshots side-effect MCP and native calls only after they are approved", () => {
  assert.match(
    mcpBranch,
    /requiresAiToolApproval\(mcpMode, authorizationRequest\.risk\)[\s\S]*?authorizeTool\([\s\S]*?if \(!authorization\.approved\) \{[\s\S]*?\n {4}\}\n {4}await snapshotBeforeSideEffect\(conversationId, policy, authorizationRequest\.risk, \{\}\);\n {4}throwIfAborted\(signal\);\n {4}const result = safeMcpResult\(await callMcpToolCancellable/,
  );
  assert.match(
    nativeBranch,
    /requiresAiToolApproval\(callMode, callRisk\)[\s\S]*?authorizeTool\([\s\S]*?if \(!authorization\.approved\) \{[\s\S]*?\n {6}\}\n {6}await snapshotBeforeSideEffect\(conversationId, policy, callRisk, \{[\s\S]*?\}\);\n {6}throwIfAborted\(signal\);\n {6}authorized = true;\n {4}\}\n {4}let result: AiCapabilityExecutionResult;/,
  );
  // Exactly one snapshot hook per path, and none before the denials.
  assert.equal((executeAgentTool.match(/snapshotBeforeSideEffect\(/g) ?? []).length, 2);
  const hook = functionBody(drawer, "async function snapshotBeforeSideEffect(");
  assert.match(hook, /if \(!snapshotTurnId \|\| !isSideEffectRisk\(risk\)\) return;/);
  assert.match(hook, /await turnSnapshotRecorder\.beforeSideEffect\(snapshotTurnId, root\)/);
});

test("subagent tool calls snapshot under the parent turn", () => {
  const subagent = functionBody(drawer, "async function executeSubagentPlanForAi(");
  assert.match(subagent, /snapshotTurnId: activeSnapshotTurns\.get\(conversationId\)/);
});

test("each agentic run records its snapshots in the outer finally", () => {
  const send = drawer.slice(drawer.indexOf("const runProjectRoot = runContext.projectRoot;"));
  assert.match(send, /activeSnapshotTurns\.set\(conversationId, triggerMessageId\);/);
  assert.match(send, /snapshotTurnId: triggerMessageId/);
  assert.match(
    send,
    /\} finally \{\n {4}streamTextBatcher\.flush\(\);\n {4}streamTextBatcher\.dispose\(\);\n {4}\/\/ [^\n]*\n {4}await settleTurnSnapshots\(\);\n {4}\/\/ 中断超时兜底[^\n]*\n {4}if \(agenticAborts\.get\(conversationId\) === abortController\) \{\n {6}agenticAborts\.delete\(conversationId\);\n {6}subagentUsageCollectors\.delete\(conversationId\);\n {6}agenticSendGate\.release\(conversationId\);/,
  );
  // One record per turn: every exit shares the same memoized finish.
  assert.match(send, /const settleTurnSnapshots = \(\) => \(turnSnapshotsRecorded \?\?= recordTurnSnapshots\(conversationId, triggerMessageId\)\);/);
  assert.equal((send.match(/recordTurnSnapshots\(/g) ?? []).length, 1);
  const record = functionBody(drawer, "async function recordTurnSnapshots(");
  assert.match(record, /if \(activeSnapshotTurns\.get\(conversationId\) === userMessageId\) activeSnapshotTurns\.delete\(conversationId\);/);
  assert.match(record, /await turnSnapshotRecorder\.finish\(userMessageId\)/);
  assert.match(record, /assistant\.recordTurnSnapshots\(conversationId, records\)/);
});

// `running` comes from the run's phase; rewind, resend and edit-resend are only
// offered while it is false (canRewindActiveConversation, rewindConversationTo).
// The final phase is therefore reported only after the turn's record is stored.
test("the conversation stays running until the turn's snapshot record is stored", () => {
  const sendStart = drawer.indexOf("const runProjectRoot = runContext.projectRoot;");
  const send = drawer.slice(sendStart, drawer.indexOf("\n}\n", sendStart));
  const finalReports = [
    ...send.matchAll(/await reportConversationRun\(\n\s*conversationId,\n\s*"(?:completed|failed)",/g),
    ...send.matchAll(/const runUpdate = reportConversationRun\(/g),
  ];
  assert.equal(finalReports.length, 4, "task_completed, response, loop_limit and the error/stop path");
  for (const report of finalReports) {
    const before = send.slice(0, report.index);
    assert.match(before, /await settleTurnSnapshots\(\);\n\s*if \(superseded\(\)\) return;\n\s*$/, `final phase at ${report.index} must follow the snapshot record`);
  }
  // Every other phase report in the run is a non-final progress update.
  for (const match of send.matchAll(/reportConversationRun\(\s*conversationId,\s*"([a-z_]+)"/g)) {
    assert.ok(["thinking", "streaming", "tool_running", "completed", "failed"].includes(match[1]), match[1]);
  }
  assert.match(drawer, /const canRewindActiveConversation = computed\(\(\) =>\s*assistant\.mode !== "agent" && !assistant\.activeConversation\?\.running && !assistant\.awaitingResponse,\s*\);/);
  assert.match(functionBody(drawer, "async function resendFromMessage("), /if \(!canRewindActiveConversation\.value\) \{[\s\S]*?return;\n\s*\}\n\s*const conversationId = assistant\.activeConversationId;\n\s*if \(!\(await prepareRewind\(conversationId, messageId, onCancel\)\)\) return;\n\s*const removed = assistant\.rewindConversationTo/);
  assert.match(store, /function rewindConversationTo\(id: string, messageId: string\) \{\s*const conversation = conversationById\(id\);\s*if \(!conversation \|\| conversation\.running/);
  // Stop keeps working while the record is pending: the controller stays registered until finally.
  assert.ok(send.indexOf("agenticAborts.delete(conversationId)") > send.lastIndexOf("await settleTurnSnapshots();"));
});

test("a record that arrives after its turn was rewound is dropped", () => {
  const recordFn = functionBody(store, "  function recordTurnSnapshots(", "\n  }\n");
  assert.match(recordFn, /retainTurnSnapshots\(records, new Set\(conversation\.messages\.flatMap\(\(message\) => \(message\.id \? \[message\.id\] : \[\]\)\)\)\)/);
  assert.match(recordFn, /if \(!live\.length\) return false;/);
  assert.match(recordFn, /mergeTurnSnapshots\(conversation\.turnSnapshots \?\? \[\], live\)/);
  // The same helper, behaviourally: a record for a removed message does not survive.
  const record = { userMessageId: "gone", projectRoot: "/p", status: "captured" };
  assert.deepEqual(retainTurnSnapshots([record], new Set(["kept"])), []);
});

test("binding a conversation to a project starts a background baseline without waiting for it", () => {
  assert.match(drawer, /function warmUpProjectSnapshot\(projectRoot: string \| undefined\) \{\n {2}if \(projectRoot\?\.trim\(\)\) void turnSnapshotRecorder\.warmUp\(projectRoot\.trim\(\)\);\n\}/);
  assert.match(functionBody(drawer, "function workspaceContextForConversation("), /warmUpProjectSnapshot\(projectRoot\);/);
  assert.match(drawer, /watch\(\s*\(\) => assistant\.activeConversation\?\.projectRoot,\s*warmUpProjectSnapshot,\s*\{ immediate: true \},?\s*\)/);
  assert.match(drawer, /createTurnSnapshotRecorder\(\{\s*capture: captureProjectSnapshot,\s*timeoutMs: TURN_SNAPSHOT_TIMEOUT_MS,\s*\}\)/);
  assert.match(drawer, /const TURN_SNAPSHOT_TIMEOUT_MS = 3000;/);
});

test("turn snapshots persist with the conversation and old data is normalized", () => {
  assert.match(store, /turnSnapshots\?: TurnSnapshot\[\];/);
  assert.match(functionBody(store, "  function restoreConversationCollection(", "\n  }\n"), /turnSnapshots: normalizeTurnSnapshots\(conversation\.turnSnapshots\)/);
  assert.match(functionBody(store, "  function rewindConversationTo(", "\n  }\n"), /retainTurnSnapshots\(/);
  const recordFn = functionBody(store, "  function recordTurnSnapshots(", "\n  }\n");
  assert.match(recordFn, /mergeTurnSnapshots\(/);
  assert.match(recordFn, /markConversationDirty\(id, "meta"\)/);
});

test("turns still in flight, including ones taking their after snapshot, are listed", async () => {
  let releaseAfter;
  const afterGate = new Promise((resolve) => { releaseAfter = resolve; });
  const { fn } = fakeCapture({ "/p": [capture("t-before"), () => afterGate.then(() => capture("t-after"))] });
  const recorder = createTurnSnapshotRecorder({ capture: fn, timeoutMs: 3000, now: () => 42 });
  assert.deepEqual(recorder.inFlight(), []);
  await recorder.beforeSideEffect("u1", "/p");
  const running = recorder.inFlight();
  assert.equal(running.length, 1);
  assert.equal(running[0].userMessageId, "u1");
  assert.equal(running[0].startedAt, 42);
  assert.equal(running[0].after, undefined);
  // a copy: callers cannot change the recorder's record
  running[0].projectRoot = "/other";
  assert.equal(recorder.inFlight()[0].projectRoot, "/p");
  const finishing = recorder.finish("u1");
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(recorder.inFlight().length, 1, "still listed while the after snapshot is taken");
  releaseAfter();
  const [record] = await finishing;
  assert.equal(record.status, "captured");
  assert.deepEqual(recorder.inFlight(), []);
});
