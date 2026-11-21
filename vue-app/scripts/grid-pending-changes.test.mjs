import assert from "node:assert/strict";
import test from "node:test";
import {
  afterPartialFailure,
  applyPendingChanges,
  createPendingChanges,
  dropApplied,
  editInsert,
  markFailed,
  pendingCount,
  primaryKeyRowKey,
  revertCell,
  revertRow,
  sameTarget,
  stageDelete,
  stageInsert,
  stageUpdate,
  submitPendingChanges,
  summary,
  toRowChanges,
} from "../src/domain/database/grid-pending-changes.ts";

const pk1 = { id: 1 };
const pk2 = { id: 2 };

test("staging the same cell twice keeps only the latest value and the first original", () => {
  let state = createPendingChanges();
  state = stageUpdate(state, pk1, "name", "b", "a");
  state = stageUpdate(state, pk1, "name", "c", "b");
  assert.equal(pendingCount(state), 1);
  assert.deepEqual(toRowChanges(state), [{ op: "update", primaryKey: { id: 1 }, values: { name: "c" } }]);
  assert.deepEqual(state.changes[0].original, { name: "a" });
});

test("staging a cell back to its original value removes it", () => {
  let state = stageUpdate(createPendingChanges(), pk1, "name", "b", "a");
  state = stageUpdate(state, pk1, "name", "a", "b");
  assert.equal(pendingCount(state), 0);
});

test("revertCell removes the column and the row once nothing is left", () => {
  let state = createPendingChanges();
  state = stageUpdate(state, pk1, "name", "b", "a");
  state = stageUpdate(state, pk1, "age", 3, 2);
  const key = primaryKeyRowKey(pk1);
  state = revertCell(state, key, "name");
  assert.deepEqual(toRowChanges(state), [{ op: "update", primaryKey: { id: 1 }, values: { age: 3 } }]);
  state = revertCell(state, key, "age");
  assert.equal(pendingCount(state), 0);
});

test("deleting a staged insert drops it without a delete", () => {
  let state = stageInsert(createPendingChanges(), { name: "no key" });
  state = revertRow(state, state.changes[0].key);
  assert.equal(pendingCount(state), 0);
  assert.deepEqual(toRowChanges(state), []);
});

test("deleting an original row whose duplicate is staged deletes the original and keeps the duplicate", () => {
  let state = stageInsert(createPendingChanges(), { id: 1, name: "copy" });
  state = stageDelete(state, pk1);
  assert.deepEqual(toRowChanges(state), [
    { op: "delete", primaryKey: { id: 1 } },
    { op: "insert", values: { id: 1, name: "copy" } },
  ]);
});

test("deleting a row replaces its staged update", () => {
  let state = stageUpdate(createPendingChanges(), pk1, "name", "b", "a");
  state = stageDelete(state, pk1);
  assert.deepEqual(toRowChanges(state), [{ op: "delete", primaryKey: { id: 1 } }]);
  // A deleted row takes no further edits.
  state = stageUpdate(state, pk1, "name", "c", "a");
  assert.deepEqual(toRowChanges(state), [{ op: "delete", primaryKey: { id: 1 } }]);
  state = stageDelete(state, pk1);
  assert.equal(pendingCount(state), 1);
});

test("toRowChanges orders delete, update, insert", () => {
  let state = createPendingChanges();
  state = stageInsert(state, { name: "new" });
  state = stageUpdate(state, pk1, "name", "b", "a");
  state = stageDelete(state, pk2);
  state = stageUpdate(state, { id: 3 }, "name", "z", "y");
  assert.deepEqual(toRowChanges(state).map((change) => change.op), ["delete", "update", "update", "insert"]);
  assert.deepEqual(summary(state), { updated: 2, inserted: 1, deleted: 1 });
});

test("markFailed marks the change at the toRowChanges index", () => {
  let state = createPendingChanges();
  state = stageInsert(state, { name: "new" });
  state = stageUpdate(state, pk1, "name", "b", "a");
  state = stageDelete(state, pk2);
  state = markFailed(state, 1);
  assert.equal(state.failedKey, primaryKeyRowKey(pk1));
  state = markFailed(state, 2);
  assert.equal(state.failedKey, state.changes.find((change) => change.op === "insert").key);
  assert.equal(markFailed(state, 7).failedKey, undefined);
});

test("dropApplied removes the applied prefix in submit order", () => {
  let state = createPendingChanges();
  state = stageInsert(state, { name: "new" });
  state = stageUpdate(state, pk1, "name", "b", "a");
  state = stageDelete(state, pk2);
  state = dropApplied(state, 2);
  assert.deepEqual(toRowChanges(state).map((change) => change.op), ["insert"]);
});

test("editInsert changes a staged insert's values", () => {
  let state = stageInsert(createPendingChanges(), { name: "new" });
  state = editInsert(state, state.changes[0].key, "name", "newer");
  assert.deepEqual(toRowChanges(state), [{ op: "insert", values: { name: "newer" } }]);
});

test("applyPendingChanges overlays updates, marks deletes and appends inserts", () => {
  let state = createPendingChanges();
  state = stageUpdate(state, pk1, "name", "b", "a");
  state = stageDelete(state, pk2);
  state = stageInsert(state, { id: 3, name: "c" });
  state = markFailed(state, 0);
  const rows = [[1, "a"], [2, "x"]];
  const overlay = applyPendingChanges(rows, ["id", "name"], ["id"], state);
  assert.deepEqual(overlay.rows, [[1, "b"], [2, "x"], [3, "c"]]);
  assert.deepEqual(rows[0], [1, "a"], "loaded rows are not mutated");
  assert.equal(overlay.rowKeys[0], primaryKeyRowKey(pk1));
  assert.equal(overlay.states.get(0)?.kind, "update");
  assert.ok(overlay.states.get(0)?.columns.has("name"));
  assert.equal(overlay.states.get(1)?.kind, "delete");
  assert.equal(overlay.states.get(1)?.failed, true);
  assert.equal(overlay.states.get(2)?.kind, "insert");
  assert.equal(overlay.rowKeys[2], state.changes.find((change) => change.op === "insert").key);
});

test("row keys do not depend on primary key column order", () => {
  assert.equal(primaryKeyRowKey({ a: 1, b: "x" }), primaryKeyRowKey({ b: "x", a: 1 }));
  assert.notEqual(primaryKeyRowKey({ a: 1 }), primaryKeyRowKey({ a: "1" }));
});

test("afterPartialFailure keeps a rolled-back group and marks the failed change", () => {
  let state = createPendingChanges();
  state = stageDelete(state, pk2);
  state = stageUpdate(state, pk1, "name", "b", "a");
  state = stageInsert(state, { name: "new" });
  const rolledBack = afterPartialFailure(state, 0, 1);
  assert.equal(pendingCount(rolledBack), 3);
  assert.equal(rolledBack.failedKey, primaryKeyRowKey(pk1));

  // In-order engines: the applied prefix took effect; the failed index shifts with it.
  const inOrder = afterPartialFailure(state, 1, 1);
  assert.deepEqual(toRowChanges(inOrder).map((change) => change.op), ["update", "insert"]);
  assert.equal(inOrder.failedKey, primaryKeyRowKey(pk1));

  // No failed index (plugin drivers): keep everything unchanged, mark nothing.
  assert.equal(afterPartialFailure(state, 0, undefined), state);
  assert.equal(afterPartialFailure(state, 2, undefined), state);
});

function threeChanges() {
  let state = createPendingChanges();
  state = stageDelete(state, pk2);
  state = stageUpdate(state, pk1, "name", "b", "a");
  return stageInsert(state, { name: "new" });
}

const noPartial = () => undefined;

test("submit clears staging once the backend succeeded, even when the refresh fails", async () => {
  const state = threeChanges();
  const order = [];
  const result = await submitPendingChanges(
    state,
    async (changes, committed) => {
      assert.deepEqual(changes, toRowChanges(state));
      committed();
      order.push("refresh");
      throw new Error("refresh failed");
    },
    noPartial,
    () => order.push("committed"),
  );
  assert.equal(result.outcome, "submitted");
  assert.equal(pendingCount(result.state), 0);
  assert.match(String(result.refreshError), /refresh failed/);
  assert.deepEqual(order, ["committed", "refresh"], "staging is cleared before the grid refreshes");
});

test("submit success, cancel and ordinary failure", async () => {
  const state = threeChanges();
  const ok = await submitPendingChanges(state, async () => ({ applied: 3 }), noPartial);
  assert.equal(ok.outcome, "submitted");
  assert.equal(pendingCount(ok.state), 0);

  const cancelled = await submitPendingChanges(state, async () => undefined, noPartial);
  assert.equal(cancelled.outcome, "cancelled");
  assert.equal(cancelled.state, state);

  const failed = await submitPendingChanges(state, async () => { throw new Error("boom"); }, noPartial);
  assert.equal(failed.outcome, "failed");
  assert.equal(failed.state, state);
});

test("submit partial failure keeps and marks per afterPartialFailure", async () => {
  const state = threeChanges();
  const result = await submitPendingChanges(
    state,
    async () => { throw new Error("partial"); },
    () => ({ applied: 1, failedIndex: 1 }),
  );
  assert.equal(result.outcome, "failed");
  assert.deepEqual(toRowChanges(result.state).map((change) => change.op), ["update", "insert"]);
  assert.equal(result.state.failedKey, primaryKeyRowKey(pk1));
});

test("staging targets match only when every field matches", () => {
  const target = { connectionId: "c", sessionId: "s", database: "d", table: "t", namespace: "a" };
  assert.equal(sameTarget(target, { ...target }), true);
  assert.equal(sameTarget(target, { ...target, namespace: "b" }), false);
  assert.equal(sameTarget(target, { ...target, sessionId: "s2" }), false);
  assert.equal(sameTarget(target, undefined), false);
});

test("mod+s submits grid changes while the inline cell input has focus", async () => {
  const { buildKeybindingContext } = await import("../src/domain/keybindings/context.ts");
  const { evaluateWhen } = await import("../src/domain/commands/when.ts");
  const { getKeybindingCommand } = await import("../src/domain/keybindings/catalog.ts");
  const { MINCODE_PRESET } = await import("../src/domain/keybindings/presets/mincode.ts");
  // The inline editor is an el-input inside the grid: a plain INPUT, outside Monaco and dialogs.
  const gridCell = { tagName: "DIV", closest: () => null };
  const input = { tagName: "INPUT", getAttribute: () => "text", closest: (selector) => (selector === ".db-tablev2-grid" ? gridCell : null) };
  const context = buildKeybindingContext({ target: input, platform: "mac" });
  assert.equal(context["editor.focus"], false);
  assert.equal(context["dialog.focus"], false);
  const rule = MINCODE_PRESET.rules.find((item) => item.command === "database:submit-grid-changes");
  assert.equal(rule.key, "mod+s");
  assert.equal(evaluateWhen(rule.when, context), true);
  assert.equal(getKeybindingCommand("database:submit-grid-changes").allowInInput, true);
});

test("namespace apply is guarded and AI cannot close a table tab with pending changes", async () => {
  const { readFile } = await import("node:fs/promises");
  const view = await readFile(new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url), "utf8");
  assert.match(view, /const applyPineconeNamespaceGuarded = guardPendingTable\(applyPineconeNamespace\)/);
  assert.match(view, /@apply-namespace="applyPineconeNamespaceGuarded"/);
  assert.match(view, /tableTabHasPendingChanges: \(tabId\) => pendingChanges\.tabId\.value === tabId && pendingChanges\.total\.value > 0/);
  const ai = await readFile(new URL("../src/composables/database/useAiDatabaseActions.ts", import.meta.url), "utf8");
  assert.match(ai, /if \(options\.tableTabHasPendingChanges\(tabId\)\) \{\s*throw new Error\(/);
  const composable = await readFile(new URL("../src/composables/database/useGridPendingChanges.ts", import.meta.url), "utf8");
  assert.match(composable, /if \(!current \|\| !sameTarget\(target, current\)\)/);
});
