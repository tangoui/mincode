// AI 数据库 / Redis 工具接入 SQL 安全网。桌面桥、确认框与 i18n 被替换；
// 工具通过注入的 invoke（db_safety_check、执行命令）与 confirm 运行真实的 runWithSafety。
import "./ts-resolve.mjs";
import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";

const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/i18n/messages") {
      return { url: dataModule("export const useVueI18n = () => ({ t: (key) => key });"), shortCircuit: true };
    }
    const resolved = nextResolve(specifier, context);
    if (resolved.url.endsWith("/src/services/desktop-bridge.ts")) {
      return { url: dataModule('export const invokeBackend = async () => { throw new Error("tools must use dependencies.invoke"); };'), shortCircuit: true };
    }
    if (resolved.url.endsWith("/src/services/sql-safety-confirm.ts")) {
      return { url: dataModule('export const confirmSqlSafety = async () => { throw new Error("confirm must be injected"); };'), shortCircuit: true };
    }
    return resolved;
  },
});

const { runWithSafety, sqlSafetyArgs } = await import("../src/services/sql-safety-service.ts");
const { databaseCapabilities } = await import("../src/services/ai-tools/database-tools.ts");
const { redisCapabilities } = await import("../src/services/ai-tools/redis-tools.ts");
const { createCapabilityRegistry } = await import("../src/domain/ai/capability-registry.ts");
const { requiresAiToolApproval } = await import("../src/domain/ai/approval-policy.ts");
const { invalidateSqlSafetySettingsCaches } = await import("../src/services/sql-safety-settings-cache.ts");

function makeCheck(overrides = {}) {
  return {
    checkId: "chk-1",
    sessionId: "s1",
    database: "shop",
    statements: [{ index: 0, text: "UPDATE orders SET paid = 1", kind: "data", table: { schema: "", name: "orders" },
      estimate: { rows: 1, exact: true, source: "count" }, risk: "low", reasons: [], undoable: true }],
    risk: "low",
    confirmTarget: "",
    undoable: true,
    expiresAt: "2026-10-31T00:05:00Z",
    ...overrides,
  };
}
const highCheck = (overrides = {}) => makeCheck({ checkId: "chk-high", risk: "high", confirmTarget: "orders", ...overrides });
const safetyError = (body) => new Error(`sqlsafety:${JSON.stringify(body)}`);
const settings = (overrides = {}) => ({ enabled: true, highRiskRows: 1000, retentionDays: 7, maxSnapshotRows: 100000, maxTotalBytes: 1 << 30, ...overrides });

/** Fake host: replies per command (function or value), records calls and confirmations. */
function harness({ replies = {}, answers = [] } = {}) {
  const calls = [];
  const confirms = [];
  let clock = 0;
  const dependencies = {
    invoke: async (command, args) => {
      calls.push({ command, args });
      const reply = replies[command];
      if (reply === undefined && command === "db_safety_settings_get") return settings();
      return typeof reply === "function" ? reply(args, calls) : reply;
    },
    sqlSafety: {
      runWithSafety,
      sqlSafetyArgs,
      confirm: async (check, origin, notice) => {
        confirms.push({ check, origin, notice });
        return answers.length ? answers.shift() : { confirmText: check.confirmTarget };
      },
      now: () => clock,
    },
  };
  return {
    calls,
    confirms,
    dependencies,
    advance: (ms) => { clock += ms; },
    commands: (name) => calls.filter((call) => call.command === name),
  };
}

const resource = (metadata = {}) => ({ id: "database:conn", kind: "database-session", name: "conn", state: "available",
  metadata: { connectionId: "conn", sessionId: "s1", ...metadata } });

function capability(list, name) {
  const found = list.find((item) => item.tool.name === name);
  assert.ok(found, `missing ${name}`);
  return found;
}

/** Runs a capability through the registry the way the assistant does in the given approval mode. */
async function runThroughRegistry(item, input, mode, res = resource()) {
  const registry = createCapabilityRegistry();
  registry.register(item);
  const approvals = [];
  const { result } = await registry.execute(item.tool.name, input, {
    resources: { resolve: () => ({ status: "resolved", resource: res }) },
    operations: {
      start: ({ run }) => {
        let state = "running";
        let value;
        let error;
        const done = Promise.resolve().then(() => run(new AbortController().signal)).then(
          (out) => { state = "succeeded"; value = out; },
          (cause) => { state = "failed"; error = String(cause?.message ?? cause); },
        );
        return { done, snapshot: () => ({ id: "op", resourceId: res.id, state, result: value, error }), cancel() {} };
      },
    },
    authorization: {
      conversationId: "conv-1",
      consume: async (request) => {
        approvals.push(request);
        // full-access approves every tool call without asking; other modes stand for a user who approved.
        assert.equal(requiresAiToolApproval(mode, request.risk), mode !== "full-access");
        return true;
      },
    },
    conversationId: "conv-1",
  });
  return { result, approvals };
}

const executeContext = (input, res = resource(), conversationId = "conv-1") => ({
  input, resource: res, resourceId: res.id, signal: new AbortController().signal, conversationId,
});

// ── database.execute_query ──

test("execute_query: a high-risk write asks the user even in full-access mode, then runs with the typed target", async () => {
  const h = harness({
    replies: {
      db_execute_query: (args) => {
        if (!args.checkId) throw safetyError({ code: "checkRequired" });
        return { columns: [], rows: [], affected: 5000 };
      },
      db_safety_check: highCheck(),
    },
  });
  const item = capability(databaseCapabilities(h.dependencies), "database.execute_query");
  assert.equal(item.risk, "dangerous");
  const { result, approvals } = await runThroughRegistry(item, { query: "DELETE FROM orders", database: "shop" }, "full-access");
  assert.equal(approvals.length, 1);
  assert.equal(h.confirms.length, 1);
  assert.equal(h.confirms[0].origin, "ai");
  assert.equal(h.confirms[0].check.risk, "high");
  assert.deepEqual(h.commands("db_safety_check").map((call) => call.args),
    [{ sessionId: "s1", query: "DELETE FROM orders", database: "shop", source: "ai" }]);
  const executes = h.commands("db_execute_query").map((call) => call.args);
  assert.equal(executes.length, 2);
  assert.equal(executes[0].checkId, undefined);
  assert.equal(executes[1].checkId, "chk-high");
  assert.equal(executes[1].confirmText, "orders");
  assert.ok(executes.every((args) => args.source === "ai" && args.sessionId === "s1" && args.query === "DELETE FROM orders"));
  assert.equal(result.affected, 5000);
});

test("execute_query: declining the high-risk dialog fails the tool and never runs the write", async () => {
  const h = harness({
    answers: [undefined],
    replies: {
      db_execute_query: (args) => {
        if (!args.checkId) throw safetyError({ code: "checkRequired" });
        throw new Error("must not run with a token");
      },
      db_safety_check: highCheck(),
    },
  });
  const item = capability(databaseCapabilities(h.dependencies), "database.execute_query");
  await assert.rejects(runThroughRegistry(item, { query: "DELETE FROM orders" }, "full-access"), /User declined the high-risk statement/);
  assert.equal(h.confirms.length, 1);
  assert.deepEqual(h.commands("db_execute_query").map((call) => call.args.checkId), [undefined]);
});

test("execute_query: a low-risk write relies on the tool approval and shows no safety dialog", async () => {
  const h = harness({
    replies: {
      db_execute_query: (args) => {
        if (!args.checkId) throw safetyError({ code: "checkRequired" });
        return { affected: 1 };
      },
      db_safety_check: makeCheck(),
    },
  });
  const item = capability(databaseCapabilities(h.dependencies), "database.execute_query");
  const result = await item.execute(executeContext({ query: "UPDATE orders SET paid = 1 WHERE id = 3" }));
  assert.deepEqual(result, { affected: 1 });
  assert.equal(h.confirms.length, 0);
  assert.equal(h.commands("db_execute_query")[1].args.checkId, "chk-1");
  assert.equal(h.commands("db_execute_query")[1].args.confirmText, undefined);
});

test("execute_query: a read runs once without a check or token", async () => {
  const h = harness({ replies: { db_execute_query: { columns: ["id"], rows: [[1]] } } });
  const item = capability(databaseCapabilities(h.dependencies), "database.execute_query");
  assert.deepEqual(await item.execute(executeContext({ query: "SELECT 1" })), { columns: ["id"], rows: [[1]] });
  assert.equal(h.commands("db_safety_check").length, 0);
  assert.equal(h.commands("db_execute_query").length, 1);
  assert.equal(h.confirms.length, 0);
});

test("execute_query: a force-write (read refused as a write) always asks, and declining fails the tool", async () => {
  const forced = makeCheck({ checkId: "chk-force" });
  const h = harness({
    answers: [undefined],
    replies: { db_execute_query: () => { throw safetyError({ code: "checkRequired", check: forced }); } },
  });
  const item = capability(databaseCapabilities(h.dependencies), "database.execute_query");
  await assert.rejects(item.execute(executeContext({ query: "SELECT nextval('s')" })), /User declined/);
  assert.equal(h.confirms.length, 1);
  assert.equal(h.confirms[0].notice, "forceWrite");
  assert.equal(h.commands("db_execute_query").length, 1);
});

test("execute_query: a reconfirm after a low-risk run asks the user before rerunning", async () => {
  const raised = highCheck({ checkId: "chk-2" });
  const h = harness({
    replies: {
      db_execute_query: (args) => {
        if (!args.checkId) throw safetyError({ code: "checkRequired" });
        if (args.checkId === "chk-1") throw safetyError({ code: "reconfirm", check: raised });
        return { affected: 2000 };
      },
      db_safety_check: makeCheck(),
    },
  });
  const item = capability(databaseCapabilities(h.dependencies), "database.execute_query");
  assert.deepEqual(await item.execute(executeContext({ query: "UPDATE orders SET paid = 1" })), { affected: 2000 });
  assert.equal(h.confirms.length, 1);
  assert.equal(h.confirms[0].notice, "reconfirm");
  assert.deepEqual(h.commands("db_execute_query").map((call) => call.args.checkId), [undefined, "chk-1", "chk-2"]);
});

// ── single-row tools ──

test("single-row tools send source ai and no token below the threshold", async () => {
  const h = harness({ replies: { db_insert_row: 1, db_update_cell: 1, db_delete_row: 1 } });
  const list = databaseCapabilities(h.dependencies);
  await capability(list, "database.insert_row").execute(executeContext({ database: "shop", table: "orders", fields: { id: 1, paid: 0 } }));
  await capability(list, "database.update_row").execute(executeContext({ database: "shop", table: "orders", primaryKey: { id: 1 }, fields: { paid: 1 } }));
  await capability(list, "database.delete_row").execute(executeContext({ database: "shop", table: "orders", primaryKey: { id: 1 } }));
  for (const command of ["db_insert_row", "db_update_cell", "db_delete_row"]) {
    const [call] = h.commands(command);
    assert.equal(call.args.source, "ai", command);
    assert.equal(call.args.checkId, undefined, command);
  }
  assert.equal(h.confirms.length, 0);
});

test("single-row tools: the 1001st row of a conversation asks with the table as the confirm target, even in full-access", async () => {
  const h = harness({ replies: { db_update_cell: 1 } });
  const item = capability(databaseCapabilities(h.dependencies), "database.update_row");
  const input = { database: "shop", table: "orders", primaryKey: { id: 1 }, fields: { paid: 1 } };
  for (let i = 0; i < 1000; i++) await item.execute(executeContext(input));
  assert.equal(h.confirms.length, 0);
  await runThroughRegistry(item, input, "full-access");
  assert.equal(h.confirms.length, 1);
  const { check, origin } = h.confirms[0];
  assert.equal(origin, "ai");
  assert.equal(check.risk, "high");
  assert.equal(check.confirmTarget, "orders");
  assert.equal(check.statements[0].estimate.rows, 1001);
  assert.deepEqual(check.statements[0].reasons, ["overThreshold"]);
  // The dialog explains a running total, not one statement's exact count or undo support.
  assert.equal(h.confirms[0].notice, "aiRowThreshold");
  assert.equal(h.commands("db_update_cell").length, 1001);
  // The settings are read once and cached, not per call.
  assert.equal(h.commands("db_safety_settings_get").length, 1);
  // Confirmed: the next threshold's worth of rows runs without asking again.
  await item.execute(executeContext(input));
  assert.equal(h.confirms.length, 1);
});

test("single-row tools: saving the settings drops the cached threshold before the TTL", async () => {
  let current = settings({ highRiskRows: 1000 });
  const h = harness({ replies: { db_update_cell: 1, db_safety_settings_get: () => current } });
  const item = capability(databaseCapabilities(h.dependencies), "database.update_row");
  const input = { database: "shop", table: "orders", primaryKey: { id: 1 }, fields: { paid: 1 } };
  await item.execute(executeContext(input));
  await item.execute(executeContext(input));
  assert.equal(h.commands("db_safety_settings_get").length, 1);
  current = settings({ highRiskRows: 2 });
  invalidateSqlSafetySettingsCaches();
  await item.execute(executeContext(input));
  assert.equal(h.commands("db_safety_settings_get").length, 2);
  assert.equal(h.confirms.length, 1);
  assert.equal(h.confirms[0].check.statements[0].estimate.rows, 3);
});

test("single-row tools: declining past the threshold fails the tool without writing", async () => {
  const h = harness({ answers: [undefined], replies: { db_delete_row: 1, db_safety_settings_get: settings({ highRiskRows: 2 }) } });
  const item = capability(databaseCapabilities(h.dependencies), "database.delete_row");
  const input = { database: "shop", table: "orders", primaryKey: { id: 1 } };
  await item.execute(executeContext(input));
  await item.execute(executeContext(input));
  await assert.rejects(item.execute(executeContext(input)), /User declined the high-risk statement/);
  assert.equal(h.commands("db_delete_row").length, 2);
});

test("single-row tools: counts are per conversation and pacing the writes does not reset them", async () => {
  const h = harness({ replies: { db_insert_row: 1, db_safety_settings_get: settings({ highRiskRows: 2 }) } });
  const item = capability(databaseCapabilities(h.dependencies), "database.insert_row");
  const input = { database: "shop", table: "orders", fields: { id: 1 } };
  await item.execute(executeContext(input, resource(), "conv-a"));
  h.advance(10 * 60_000);
  await item.execute(executeContext(input, resource(), "conv-a"));
  await item.execute(executeContext(input, resource(), "conv-b"));
  assert.equal(h.confirms.length, 0);
  // A slow agent cannot stay under the threshold by waiting between writes.
  h.advance(10 * 60_000);
  await item.execute(executeContext(input, resource(), "conv-a"));
  assert.equal(h.confirms.length, 1);
  // Only the user's confirmation starts a new count.
  await item.execute(executeContext(input, resource(), "conv-a"));
  assert.equal(h.confirms.length, 1);
  await item.execute(executeContext(input, resource(), "conv-a"));
  assert.equal(h.confirms.length, 2);
});

test("single-row tools: the threshold does not apply while the safety net is off", async () => {
  const h = harness({ replies: { db_insert_row: 1, db_safety_settings_get: settings({ enabled: false, highRiskRows: 1 }) } });
  const item = capability(databaseCapabilities(h.dependencies), "database.insert_row");
  for (let i = 0; i < 3; i++) await item.execute(executeContext({ database: "shop", table: "orders", fields: { id: i } }));
  assert.equal(h.confirms.length, 0);
});

test("single-row tools: a reconfirm from the backend is confirmed and resubmitted with its token", async () => {
  const reconfirm = makeCheck({ checkId: "chk-row", undoable: false,
    statements: [{ ...makeCheck().statements[0], reasons: ["overSnapshotLimit"], undoable: false }] });
  const h = harness({
    replies: {
      db_update_cell: (args) => {
        if (!args.checkId) throw safetyError({ code: "reconfirm", check: reconfirm });
        return 1;
      },
    },
  });
  const item = capability(databaseCapabilities(h.dependencies), "database.update_row");
  assert.deepEqual(await item.execute(executeContext({ database: "shop", table: "orders", primaryKey: { id: 1 }, fields: { blob: "x" } })),
    { affected: { blob: 1 } });
  assert.equal(h.confirms.length, 1);
  assert.equal(h.confirms[0].notice, "reconfirm");
  assert.deepEqual(h.commands("db_update_cell").map((call) => call.args.checkId), [undefined, "chk-row"]);
});

// ── database.import_rows ──

test("import_rows: checks the import operation as ai and executes with the token", async () => {
  const h = harness({ replies: { db_safety_check: makeCheck({ checkId: "chk-import" }), db_import_data: 2 } });
  const item = capability(databaseCapabilities(h.dependencies), "database.import_rows");
  const input = { database: "shop", table: "orders", format: "json", data: '[{"id":1},{"id":2}]' };
  assert.equal(await item.execute(executeContext(input)), 2);
  assert.deepEqual(h.commands("db_safety_check").map((call) => call.args), [{ sessionId: "s1", database: "shop", source: "ai",
    operation: { kind: "import", table: "orders", format: "json", data: input.data } }]);
  assert.deepEqual(h.commands("db_import_data").map((call) => call.args), [{ sessionId: "s1", ...input, checkId: "chk-import" }]);
  assert.equal(h.confirms.length, 0);
});

test("import_rows: a high-risk import asks the user and declining imports nothing", async () => {
  const h = harness({ answers: [undefined], replies: { db_safety_check: highCheck(), db_import_data: 5000 } });
  const item = capability(databaseCapabilities(h.dependencies), "database.import_rows");
  await assert.rejects(item.execute(executeContext({ database: "shop", table: "orders", format: "csv", data: "id\n1" })), /User declined/);
  assert.equal(h.commands("db_import_data").length, 0);
});

// ── Redis ──

const redisResource = (metadata = {}) => ({ id: "redis:conn", kind: "redis-session", name: "cache", state: "available",
  metadata: { connectionId: "conn", sessionId: "r1", ...metadata } });

test("redis writes check the key operation as ai and execute with the token, without a dialog for one key", async () => {
  const h = harness({ replies: { db_safety_check: (args) => makeCheck({ checkId: `chk-${args.operation.action}` }) } });
  const list = redisCapabilities(h.dependencies);
  const cases = [
    ["redis.set", { key: "k", value: "v", dbIndex: 2 }, "redis_set_key", "set"],
    ["redis.add", { key: "k", keyType: "string", value: "v" }, "redis_add_key", "add"],
    ["redis.delete", { key: "k" }, "redis_delete_key", "delete"],
    ["redis.expire", { key: "k", ttlSeconds: 10 }, "redis_set_ttl", "ttl"],
    ["redis.persist", { key: "k" }, "redis_set_ttl", "ttl"],
    ["redis.set_hash", { key: "k", value: { a: "1" } }, "redis_set_key", "set"],
  ];
  for (const [name, input, command, action] of cases) {
    h.calls.length = 0;
    await capability(list, name).execute(executeContext(input, redisResource()));
    const [check] = h.commands("db_safety_check");
    assert.deepEqual(check.args, { sessionId: "r1", source: "ai",
      operation: { kind: "redis", action, dbIndex: input.dbIndex ?? 0, keys: ["k"] } }, name);
    const [executed] = h.commands(command);
    assert.equal(executed.args.checkId, `chk-${action}`, name);
    assert.equal(executed.args.key, "k", name);
  }
  assert.equal(h.confirms.length, 0);
});

test("redis: a high-risk key edit asks even in full-access and declining skips the command", async () => {
  const h = harness({ answers: [undefined], replies: { db_safety_check: highCheck({ confirmTarget: "CONFIRM" }) } });
  const item = capability(redisCapabilities(h.dependencies), "redis.delete");
  await assert.rejects(runThroughRegistry(item, { key: "k" }, "full-access", redisResource()), /User declined the high-risk statement/);
  assert.equal(h.confirms.length, 1);
  assert.equal(h.commands("redis_delete_key").length, 0);
});
