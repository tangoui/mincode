import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";
import { computed, markRaw, ref } from "vue";

const source = await readFile(
  new URL("../src/composables/database/useQueryExecution.ts", import.meta.url),
  "utf8"
);
const code = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

function mountExecution() {
  const pending = [];
  const databaseStore = {
    desktopRuntime: true,
    queryRunning: false,
    selectedDatabase: "orders_db",
    executeQuery() {
      const request = deferred();
      pending.push(request);
      return request.promise;
    },
  };
  const modules = {
    vue: { markRaw },
    "element-plus": {
      ElMessage: { success() {}, warning() {}, error() {} },
      ElMessageBox: { confirm: async () => undefined },
    },
    "@/domain/database/sql-mutation": { detectMutationKeyword: () => "" },
    "@/domain/database/query-result-budget": { estimateQueryResultBytes: () => 0 },
    "@/domain/database/query-template": { buildTableExplainSourceSql: () => "" },
    "@/services/persistent-store": { savePersistedValue: async () => undefined },
    "@/services/notifications": { notifyApp() {} },
    "@/i18n/messages": { useVueI18n: () => ({ t: (key) => key }) },
    "@/stores/database-workspace": { useDatabaseWorkspaceStore: () => databaseStore },
  };
  const exports = {};
  new Function("require", "exports", code)((name) => modules[name] ?? {}, exports);

  const queryTabs = ref([{ id: "query:1", name: "SQL 1", sql: "SELECT 1", status: "idle" }]);
  const activeConnectionValue = ref({ id: "conn-a", name: "A" });
  const queryHistory = ref([]);
  const execution = exports.useQueryExecution({
    queryTabs,
    activeQueryTab: computed(() => queryTabs.value[0]),
    queryText: computed(() => queryTabs.value[0].sql),
    activeObject: ref("query"),
    activeConnection: computed(() => activeConnectionValue.value),
    selectedDatabase: computed(() => databaseStore.selectedDatabase),
    selectedTable: computed(() => undefined),
    queryHistory,
    desktopOnly() {},
    enforceQueryResultBudget() {},
    onExecutionPlanLoaded() {},
  });
  return { execution, pending, databaseStore, activeConnectionValue, queryHistory, queryTabs };
}

test("query history keeps the connection and database the query started on", async () => {
  const { execution, pending, databaseStore, activeConnectionValue, queryHistory } = mountExecution();
  const running = execution.runQuery("SELECT * FROM orders");
  await Promise.resolve();
  assert.equal(pending.length, 1);

  // 长查询执行期间用户切到别的连接和库。
  activeConnectionValue.value = { id: "conn-b", name: "B" };
  databaseStore.selectedDatabase = "billing_db";
  pending[0].resolve({ columns: [], rows: [], rowCount: 3, executionMs: 12 });
  await running;

  assert.equal(queryHistory.value.length, 1);
  assert.equal(queryHistory.value[0].connectionId, "conn-a");
  assert.equal(queryHistory.value[0].database, "orders_db");
  assert.equal(queryHistory.value[0].success, true);
});

test("failed query history also keeps the original connection and database", async () => {
  const { execution, pending, databaseStore, activeConnectionValue, queryHistory } = mountExecution();
  const running = execution.runQuery("SELECT * FROM missing");
  await Promise.resolve();

  activeConnectionValue.value = { id: "conn-b", name: "B" };
  databaseStore.selectedDatabase = "billing_db";
  pending[0].reject(new Error("table missing"));
  await running;

  assert.equal(queryHistory.value.length, 1);
  assert.equal(queryHistory.value[0].connectionId, "conn-a");
  assert.equal(queryHistory.value[0].database, "orders_db");
  assert.equal(queryHistory.value[0].success, false);
});

test("a query result records the connection and database it ran on (plugin services name those)", async () => {
  const { execution, pending, databaseStore, activeConnectionValue, queryTabs } = mountExecution();
  const running = execution.runQuery("SELECT * FROM orders");
  await Promise.resolve();

  // The sidebar moves to another connection and database while the query runs.
  activeConnectionValue.value = { id: "conn-b", name: "B" };
  databaseStore.selectedDatabase = "billing_db";
  pending[0].resolve({ columns: ["id"], rows: [[1]], rowCount: 1, executionMs: 3 });
  await running;

  assert.deepEqual(queryTabs.value[0].resultSource, { connectionId: "conn-a", database: "orders_db" });
});
