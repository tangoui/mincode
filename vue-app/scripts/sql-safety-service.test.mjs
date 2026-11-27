// runWithSafety: check → confirm → execute, with reconfirm / force-write / partial
// handling. The desktop bridge, the confirmation dialog and i18n are stubbed;
// check, confirm and execute are injected per test.
import "./ts-resolve.mjs";
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";

const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
const desktopStub = `
  export const invokeBackend = async (command, args) => {
    globalThis.__sqlSafetyTest.invoked.push([command, args]);
    return globalThis.__sqlSafetyTest.reply(command, args);
  };
`;
const confirmStub = `
  export const confirmSqlSafety = async () => { throw new Error("default confirm must be injected in tests"); };
`;
const i18nStub = `
  export const useVueI18n = () => ({ t: (key, params) => (params ? key + " " + JSON.stringify(params) : key) });
`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/i18n/messages") return { url: dataModule(i18nStub), shortCircuit: true };
    const resolved = nextResolve(specifier, context);
    if (resolved.url.endsWith("/src/services/desktop-bridge.ts")) return { url: dataModule(desktopStub), shortCircuit: true };
    if (resolved.url.endsWith("/src/services/sql-safety-confirm.ts")) return { url: dataModule(confirmStub), shortCircuit: true };
    return resolved;
  },
});
globalThis.__sqlSafetyTest = { invoked: [], reply: () => undefined };

const { runWithSafety, sqlSafetyArgs, SqlSafetyPartialError } = await import("../src/services/sql-safety-service.ts");

function makeCheck(overrides = {}) {
  return {
    checkId: "chk-1",
    sessionId: "s1",
    database: "shop",
    statements: [{ index: 0, text: "UPDATE orders SET paid = 1 WHERE id = 3", kind: "data", table: { schema: "", name: "orders" },
      estimate: { rows: 1, exact: true, source: "count" }, risk: "low", reasons: [], undoable: true }],
    risk: "low",
    confirmTarget: "",
    undoable: true,
    expiresAt: "2026-10-31T00:05:00Z",
    ...overrides,
  };
}
const safetyError = (body) => new Error(`sqlsafety:${JSON.stringify(body)}`);

function harness({ checks = [makeCheck()], confirms = [{}], executes = [async () => "ok"] } = {}) {
  const calls = { check: [], confirm: [], execute: [] };
  return {
    calls,
    options: {
      sessionId: "s1",
      query: "UPDATE orders SET paid = 1 WHERE id = 3",
      database: "shop",
      source: "query",
      origin: "user",
      check: async (args) => { calls.check.push(args); return checks[calls.check.length - 1]; },
      confirm: async (...args) => { calls.confirm.push(args); return confirms[calls.confirm.length - 1]; },
      execute: async (token) => { calls.execute.push(token); return executes[calls.execute.length - 1](token); },
    },
  };
}

test("low risk: one check, one confirm, one execute with the issued token", async () => {
  const { calls, options } = harness();
  assert.equal(await runWithSafety(options), "ok");
  assert.deepEqual(calls.check, [{ sessionId: "s1", query: "UPDATE orders SET paid = 1 WHERE id = 3", database: "shop", source: "query" }]);
  assert.equal(calls.confirm.length, 1);
  assert.equal(calls.confirm[0][0].checkId, "chk-1");
  assert.equal(calls.confirm[0][1], "user");
  assert.deepEqual(calls.execute, [{ checkId: "chk-1" }]);
});

test("reconfirm: the second confirm receives the new check and execute uses its token", async () => {
  const raised = makeCheck({ checkId: "chk-2", risk: "high", confirmTarget: "orders", undoable: false,
    statements: [{ ...makeCheck().statements[0], risk: "high", reasons: ["overThreshold", "overSnapshotLimit"], undoable: false,
      estimate: { rows: 50000, exact: true, source: "count" } }] });
  const { calls, options } = harness({
    confirms: [{}, { confirmText: "orders" }],
    executes: [async () => { throw safetyError({ code: "reconfirm", check: raised }); }, async () => "done"],
  });
  assert.equal(await runWithSafety(options), "done");
  assert.equal(calls.check.length, 1);
  assert.equal(calls.confirm.length, 2);
  assert.equal(calls.confirm[1][0].checkId, "chk-2");
  assert.equal(calls.confirm[1][0].risk, "high");
  assert.equal(calls.confirm[1][2], "reconfirm");
  assert.deepEqual(calls.execute, [{ checkId: "chk-1" }, { checkId: "chk-2", confirmText: "orders" }]);
});

test("cancelling the confirmation returns undefined and never executes", async () => {
  const { calls, options } = harness({ confirms: [undefined] });
  assert.equal(await runWithSafety(options), undefined);
  assert.equal(calls.execute.length, 0);
});

test("read-only text executes without a token and without a confirmation", async () => {
  const { calls, options } = harness({ checks: [makeCheck({ checkId: "", risk: "none", undoable: false,
    statements: [{ index: 0, text: "SELECT 1", kind: "read", estimate: { rows: 0, exact: false, source: "none" }, risk: "none", undoable: false }] })] });
  assert.equal(await runWithSafety({ ...options, query: "SELECT 1" }), "ok");
  assert.equal(calls.confirm.length, 0);
  assert.deepEqual(calls.execute, [{ checkId: "" }]);
  assert.deepEqual(sqlSafetyArgs(calls.execute[0]), {});
  assert.deepEqual(sqlSafetyArgs({ checkId: "c", confirmText: "t" }), { checkId: "c", confirmText: "t" });
});

test("a read refused as a write (checkRequired with check) is confirmed, never retried silently", async () => {
  const forced = makeCheck({ checkId: "chk-force", undoable: false,
    statements: [{ ...makeCheck().statements[0], kind: "unknown", reasons: ["unparsed"], undoable: false, estimate: { rows: 0, exact: false, source: "none" } }] });
  const readOnly = makeCheck({ checkId: "", risk: "none", statements: [] });
  const { calls, options } = harness({
    checks: [readOnly],
    confirms: [{}],
    executes: [async () => { throw safetyError({ code: "checkRequired", check: forced }); }, async () => "written"],
  });
  assert.equal(await runWithSafety({ ...options, origin: "preconfirmed" }), "written");
  assert.equal(calls.confirm.length, 1);
  assert.equal(calls.confirm[0][0].checkId, "chk-force");
  assert.equal(calls.confirm[0][1], "preconfirmed");
  assert.equal(calls.confirm[0][2], "forceWrite");
  assert.deepEqual(calls.execute, [{ checkId: "" }, { checkId: "chk-force" }]);
});

test("cancelling a force-write confirmation stops without executing again", async () => {
  const forced = makeCheck({ checkId: "chk-force" });
  const { calls, options } = harness({
    checks: [makeCheck({ checkId: "", risk: "none", statements: [] })],
    confirms: [undefined],
    executes: [async () => { throw safetyError({ code: "checkRequired", check: forced }); }],
  });
  assert.equal(await runWithSafety(options), undefined);
  assert.equal(calls.execute.length, 1);
});

test("partial failure raises SqlSafetyPartialError with applied and failedIndex", async () => {
  const { options } = harness({ executes: [async () => { throw safetyError({ code: "partial", applied: 2, failedIndex: 2, message: "duplicate key" }); }] });
  await assert.rejects(runWithSafety(options), (error) => {
    assert.ok(error instanceof SqlSafetyPartialError);
    assert.equal(error.applied, 2);
    assert.equal(error.failedIndex, 2);
    assert.equal(error.detail, "duplicate key");
    assert.equal(error.failedIndexKnown, true);
    assert.match(error.message, /database\.safety\.partial .*"applied":2.*"index":3/);
    assert.match(error.message, /duplicate key/);
    return true;
  });
});

test("partial failure without failedIndex says the index is unknown", async () => {
  const { options } = harness({ executes: [async () => { throw safetyError({ code: "partial", applied: 0, message: "plugin error" }); }] });
  await assert.rejects(runWithSafety(options), (error) => {
    assert.ok(error instanceof SqlSafetyPartialError);
    assert.equal(error.failedIndexKnown, false);
    return true;
  });
});

test("reconfirm is followed at most three times", async () => {
  const again = () => { throw safetyError({ code: "reconfirm", check: makeCheck({ checkId: "chk-n" }) }); };
  const { calls, options } = harness({ confirms: [{}, {}, {}, {}, {}], executes: [again, again, again, again, again] });
  await assert.rejects(runWithSafety(options), /database\.safety\.reconfirm/);
  assert.equal(calls.execute.length, 4);
  assert.equal(calls.confirm.length, 4);
});

test("other structured codes become readable errors; ordinary errors pass through", async () => {
  const invalid = harness({ executes: [async () => { throw safetyError({ code: "checkInvalid" }); }] });
  await assert.rejects(runWithSafety(invalid.options), /database\.safety\.error\.checkInvalid/);
  const plain = new Error("no such table: orders");
  const ordinary = harness({ executes: [async () => { throw plain; }] });
  await assert.rejects(runWithSafety(ordinary.options), (error) => error === plain);
});

test("operation checks pass the operation and omit an empty database", async () => {
  const { calls, options } = harness();
  const operation = { kind: "redis", action: "delete", dbIndex: 0, keys: ["k"] };
  await runWithSafety({ ...options, query: undefined, database: null, operation, source: "grid", origin: "preconfirmed" });
  assert.deepEqual(calls.check[0], { sessionId: "s1", operation, source: "grid" });
  assert.equal(calls.confirm[0][1], "preconfirmed");
  assert.equal(calls.confirm[0][2], undefined);
});

test("the default check calls db_safety_check and validates its reply", async () => {
  globalThis.__sqlSafetyTest = { invoked: [], reply: () => makeCheck({ checkId: "", risk: "none", statements: [] }) };
  const executed = [];
  await runWithSafety({ sessionId: "s1", query: "SELECT 1", source: "query", origin: "user",
    confirm: async () => { throw new Error("must not confirm reads"); },
    execute: async (token) => { executed.push(token); } });
  assert.deepEqual(globalThis.__sqlSafetyTest.invoked, [["db_safety_check", { sessionId: "s1", query: "SELECT 1", source: "query" }]]);
  assert.deepEqual(executed, [{ checkId: "" }]);
  globalThis.__sqlSafetyTest.reply = () => ({ risk: "low" });
  await assert.rejects(runWithSafety({ sessionId: "s1", query: "DELETE FROM t", source: "query", origin: "user", execute: async () => undefined }));
});

// ── executeFirst: the query window runs reads without any check round trip. ──

test("executeFirst: a read executes once and never checks or confirms", async () => {
  const { calls, options } = harness();
  assert.equal(await runWithSafety({ ...options, query: "SELECT 1", executeFirst: true }), "ok");
  assert.equal(calls.check.length, 0);
  assert.equal(calls.confirm.length, 0);
  assert.deepEqual(calls.execute, [{ checkId: "" }]);
});

test("executeFirst: a write answered with checkRequired is checked, confirmed, then executed with the token", async () => {
  const { calls, options } = harness({
    executes: [async () => { throw safetyError({ code: "checkRequired" }); }, async () => "written"],
  });
  assert.equal(await runWithSafety({ ...options, executeFirst: true }), "written");
  assert.equal(calls.check.length, 1);
  assert.equal(calls.confirm.length, 1);
  assert.equal(calls.confirm[0][0].checkId, "chk-1");
  assert.equal(calls.confirm[0][2], undefined);
  assert.deepEqual(calls.execute, [{ checkId: "" }, { checkId: "chk-1" }]);
});

test("executeFirst: a force-write check always opens the dialog, without another check", async () => {
  const forced = makeCheck({ checkId: "chk-force" });
  const { calls, options } = harness({
    executes: [async () => { throw safetyError({ code: "checkRequired", check: forced }); }, async () => "written"],
  });
  assert.equal(await runWithSafety({ ...options, origin: "preconfirmed", executeFirst: true }), "written");
  assert.equal(calls.check.length, 0);
  assert.equal(calls.confirm[0][2], "forceWrite");
  assert.deepEqual(calls.execute, [{ checkId: "" }, { checkId: "chk-force" }]);
});

test("executeFirst: cancelling the confirmation never executes the write", async () => {
  const { calls, options } = harness({
    confirms: [undefined],
    executes: [async () => { throw safetyError({ code: "checkRequired" }); }],
  });
  assert.equal(await runWithSafety({ ...options, executeFirst: true }), undefined);
  assert.equal(calls.execute.length, 1);
});

test("executeFirst: the reconfirm loop still follows a raised check", async () => {
  const raised = makeCheck({ checkId: "chk-2", risk: "high", confirmTarget: "orders" });
  const { calls, options } = harness({
    confirms: [{}, { confirmText: "orders" }],
    executes: [
      async () => { throw safetyError({ code: "checkRequired" }); },
      async () => { throw safetyError({ code: "reconfirm", check: raised }); },
      async () => "done",
    ],
  });
  assert.equal(await runWithSafety({ ...options, executeFirst: true }), "done");
  assert.equal(calls.confirm[1][2], "reconfirm");
  assert.deepEqual(calls.execute.at(-1), { checkId: "chk-2", confirmText: "orders" });
});

test("executeFirst: ordinary errors of the first run pass through without a check", async () => {
  const plain = new Error("no such table: orders");
  const { calls, options } = harness({ executes: [async () => { throw plain; }] });
  await assert.rejects(runWithSafety({ ...options, executeFirst: true }), (error) => error === plain);
  assert.equal(calls.check.length, 0);
});

test("executeFirst: a reconfirm of the tokenless first run (single-row commands) is confirmed and resubmitted", async () => {
  const raised = makeCheck({ checkId: "chk-row", undoable: false });
  const { calls, options } = harness({
    executes: [async () => { throw safetyError({ code: "reconfirm", check: raised }); }, async () => 1],
  });
  assert.equal(await runWithSafety({ ...options, executeFirst: true }), 1);
  assert.equal(calls.check.length, 0);
  assert.equal(calls.confirm[0][2], "reconfirm");
  assert.deepEqual(calls.execute, [{ checkId: "" }, { checkId: "chk-row" }]);
});

test("a disconnected tool cannot submit a schema write after confirmation", async () => {
  const abort = new AbortController();
  const { promise: opened, resolve: markOpened } = Promise.withResolvers();
  const { promise: answer, resolve: confirm } = Promise.withResolvers();
  const { calls, options } = harness({ checks: [makeCheck({ risk: "high", confirmTarget: "orders" })] });
  const pending = runWithSafety({
    ...options,
    operation: { kind: "schemaSync" },
    signal: abort.signal,
    confirm: async (_check, _origin, _notice, _summary, signal) => {
      assert.equal(signal, abort.signal);
      markOpened();
      return answer;
    },
  });
  const rejected = assert.rejects(pending, { name: "AbortError" });
  await opened;
  abort.abort();
  confirm({ confirmText: "orders" });
  await rejected;
  assert.deepEqual(calls.execute, []);
});
