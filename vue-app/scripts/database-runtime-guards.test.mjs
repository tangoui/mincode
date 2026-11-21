import assert from "node:assert/strict";
import { readSource as readFile } from "./read-source.mjs";
import test from "node:test";

import {
  DEFAULT_QUERY_RESULT_BUDGET,
  estimateQueryResultBytes,
  selectQueryResultsToRelease,
} from "../src/domain/database/query-result-budget.ts";
import { createRequestGenerationGate } from "../src/domain/database/request-generation.ts";
import { createDatabaseMetadataCache } from "../src/domain/database/metadata-cache.ts";
import {
  createQueryResultProgressBatcher,
  createQueryResultStream,
} from "../src/domain/database/query-result-stream.ts";

const result = (rows) => ({ columns: ["payload"], rows, rowCount: rows.length, executionMs: 1 });

test("query result stream preserves chunk order and protocol compatibility", async () => {
  const stream = createQueryResultStream("query-1");
  stream.append({ queryId: "query-1", sequence: 0, columns: ["id"], rows: [[1]], estimatedBytes: 64 });
  stream.append({ queryId: "query-1", sequence: 1, rows: [[2]], estimatedBytes: 64 });
  assert.deepEqual(await stream.finish({ queryId: "query-1", rowCount: 2, chunkCount: 2, executionMs: 5 }), {
    columns: ["id"], rows: [[1], [2]], rowCount: 2, executionMs: 5,
  });
});

test("query result stream snapshots isolate array shells while preserving accumulated rows", () => {
  const stream = createQueryResultStream("query-snapshot");
  stream.append({
    queryId: "query-snapshot",
    sequence: 0,
    columns: ["id"],
    rows: [[1]],
    estimatedBytes: 64,
  });

  const snapshot = stream.snapshot();
  snapshot.columns.push("mutated");
  snapshot.rows.push([999]);
  stream.append({
    queryId: "query-snapshot",
    sequence: 1,
    rows: [[2]],
    estimatedBytes: 64,
  });

  assert.deepEqual(snapshot, {
    columns: ["id", "mutated"],
    rows: [[1], [999]],
    rowCount: 1,
    executionMs: 0,
  });
  assert.deepEqual(stream.snapshot(3), {
    columns: ["id"],
    rows: [[1], [2]],
    rowCount: 2,
    executionMs: 3,
  });
});

test("query result stream permanently rejects after its transport fails", async () => {
  const stream = createQueryResultStream("query-disconnected");
  stream.append({
    queryId: "query-disconnected",
    sequence: 0,
    columns: ["id"],
    rows: [[1]],
    estimatedBytes: 64,
  });
  const transportError = new Error("Database result Stream disconnected before query completion");
  stream.fail(transportError);

  await assert.rejects(
    stream.finish({ queryId: "query-disconnected", rowCount: 2, chunkCount: 2, executionMs: 5 }),
    (error) => error === transportError,
  );
  assert.throws(() => stream.snapshot(), (error) => error === transportError);
});

function createFakeProgressScheduler() {
  const scheduled = [];
  return {
    scheduler(callback) {
      const entry = { callback, cancelled: false };
      scheduled.push(entry);
      return () => { entry.cancelled = true; };
    },
    flush() {
      const pending = scheduled.splice(0);
      for (const entry of pending) {
        if (!entry.cancelled) entry.callback();
      }
    },
    scheduledCount() {
      return scheduled.length;
    },
  };
}

test("query result progress batches multiple chunks into one scheduled snapshot", () => {
  const stream = createQueryResultStream("query-progress");
  const fake = createFakeProgressScheduler();
  const updates = [];
  const progress = createQueryResultProgressBatcher(stream, (value) => updates.push(value), fake.scheduler);

  stream.append({ queryId: "query-progress", sequence: 0, columns: ["id"], rows: [[1]], estimatedBytes: 64 });
  progress.schedule();
  stream.append({ queryId: "query-progress", sequence: 1, rows: [[2]], estimatedBytes: 64 });
  progress.schedule();

  assert.equal(fake.scheduledCount(), 1);
  fake.flush();
  assert.equal(updates.length, 1);
  assert.deepEqual(updates[0].rows, [[1], [2]]);
});

test("query result progress cancellation suppresses a pending partial update", async () => {
  const stream = createQueryResultStream("query-final");
  const fake = createFakeProgressScheduler();
  const updates = [];
  const progress = createQueryResultProgressBatcher(stream, (value) => updates.push(value), fake.scheduler);

  stream.append({ queryId: "query-final", sequence: 0, columns: ["id"], rows: [[1]], estimatedBytes: 64 });
  progress.schedule();
  const finalResult = await stream.finish({
    queryId: "query-final",
    rowCount: 1,
    chunkCount: 1,
    executionMs: 7,
  });
  progress.cancel();
  fake.flush();

  assert.equal(updates.length, 0);
  assert.equal(finalResult.executionMs, 7);
  assert.deepEqual(finalResult.rows, [[1]]);
});

test("query result stream waits for chunks delivered after the IPC summary", async () => {
  const stream = createQueryResultStream("query-late");
  const resultPromise = stream.finish({ queryId: "query-late", rowCount: 1, chunkCount: 1, executionMs: 2 });
  stream.append({ queryId: "query-late", sequence: 0, columns: ["id"], rows: [[1]], estimatedBytes: 64 });
  assert.equal((await resultPromise).rowCount, 1);
});

test("query result stream rejects stale, out-of-order, oversized, and released chunks", () => {
  const stream = createQueryResultStream("query-2", { maxRows: 2, maxBytes: 128 });
  assert.throws(() => stream.append({ queryId: "stale", sequence: 0, rows: [], estimatedBytes: 1 }));
  assert.throws(() => stream.append({ queryId: "query-2", sequence: 1, rows: [], estimatedBytes: 1 }));
  stream.append({ queryId: "query-2", sequence: 0, columns: ["id"], rows: [[1]], estimatedBytes: 64 });
  assert.throws(() => stream.append({ queryId: "query-2", sequence: 1, rows: [[2], [3]], estimatedBytes: 64 }));
  stream.release();
  assert.equal(stream.retainedRows(), 0);
  assert.throws(() => stream.append({ queryId: "query-2", sequence: 1, rows: [], estimatedBytes: 1 }));
});

test("query result estimation includes nested JSON/container overhead and stays bounded", () => {
  const nested = result([[{ values: Array.from({ length: 200 }, (_, index) => ({ index, ok: true })) }]]);
  const bytes = estimateQueryResultBytes(nested, { maxRowsToSample: 1, maxValueDepth: 4 });
  assert.ok(bytes > JSON.stringify(nested.rows).length);
  assert.ok(bytes <= DEFAULT_QUERY_RESULT_BUDGET.maxEstimatedResultBytes);
});

test("query result budget preserves active and recent results while releasing old LRU results", () => {
  const mib = 1024 * 1024;
  const releaseIds = selectQueryResultsToRelease(
    [
      { id: "active", estimatedBytes: 30 * mib, lastAccessedAt: 1, hasResult: true },
      { id: "recent", estimatedBytes: 20 * mib, lastAccessedAt: 40, hasResult: true },
      { id: "middle", estimatedBytes: 20 * mib, lastAccessedAt: 30, hasResult: true },
      { id: "old", estimatedBytes: 20 * mib, lastAccessedAt: 10, hasResult: true },
    ],
    "active",
  );
  assert.deepEqual(releaseIds, ["old", "middle"]);
});

test("request generation only allows the latest matching target to commit and finish loading", () => {
  const gate = createRequestGenerationGate();
  const oldRequest = gate.begin("scope-a", "session-a", "table:db.users");
  const currentRequest = gate.begin("scope-a", "session-a", "table:db.users");
  const otherTarget = gate.begin("scope-a", "session-a", "schema");

  assert.equal(gate.isCurrent(oldRequest), false);
  assert.equal(gate.isCurrent(currentRequest), true);
  assert.equal(gate.finish(oldRequest), false);
  assert.equal(gate.finish(currentRequest), true);
  assert.equal(gate.isCurrent(otherTarget), true);
});

test("request generation never reuses a completed generation while an older request is pending", () => {
  const gate = createRequestGenerationGate();
  const oldestRequest = gate.begin("scope-a", "session-a", "table:db.users");
  const completedRequest = gate.begin("scope-a", "session-a", "table:db.projects");

  assert.equal(gate.finish(completedRequest), true);
  const latestRequest = gate.begin("scope-a", "session-a", "table:db.logs");

  assert.equal(gate.isCurrent(oldestRequest), false);
  assert.equal(gate.isCurrent(latestRequest), true);
});

test("metadata cache deduplicates in-flight loads, expires values, and never retains failures", async () => {
  let now = 1_000;
  let loads = 0;
  const cache = createDatabaseMetadataCache({ ttlMs: 60_000, now: () => now });
  const key = { sessionId: "s1", database: "db", table: "users", kind: "columns" };
  const load = async () => {
    loads += 1;
    return [loads];
  };

  const [first, duplicate] = await Promise.all([cache.get(key, load), cache.get(key, load)]);
  assert.deepEqual(first, [1]);
  assert.deepEqual(duplicate, [1]);
  assert.equal(loads, 1);
  assert.deepEqual(await cache.get(key, load), [1]);

  now += 60_001;
  assert.deepEqual(await cache.get(key, load), [2]);
  cache.invalidate({ sessionId: "s1", database: "db", table: "users" });
  assert.deepEqual(await cache.get(key, load), [3]);

  const failedKey = { ...key, table: "broken" };
  await assert.rejects(cache.get(failedKey, async () => { throw new Error("temporary"); }));
  assert.deepEqual(await cache.get(failedKey, load), [4]);
});

test("metadata invalidation prevents an older in-flight load from replacing fresh data", async () => {
  const cache = createDatabaseMetadataCache();
  const key = { sessionId: "s1", database: "db", table: "users", kind: "columns" };
  let resolveStale;
  const stale = cache.get(key, () => new Promise((resolve) => { resolveStale = resolve; }));

  cache.invalidate({ sessionId: "s1" });
  assert.deepEqual(await cache.get(key, async () => ["fresh"]), ["fresh"]);
  resolveStale(["stale"]);
  assert.deepEqual(await stale, ["stale"]);

  let reloads = 0;
  assert.deepEqual(await cache.get(key, async () => { reloads += 1; return ["reloaded"]; }), ["fresh"]);
  assert.equal(reloads, 0);
});

test("selected database rows are staged for deletion and submitted as one batch refreshed once", async () => {
  const [mutations, store] = await Promise.all([
    readFile(new URL("../src/composables/database/useRowMutations.ts", import.meta.url), "utf8"),
    readFile(new URL("../src/stores/database-workspace.ts", import.meta.url), "utf8"),
  ]);
  const deleteRecord = mutations.match(
    /function deleteRecord[\s\S]*?\n  \}\n/
  )?.[0];
  const applyRowChanges = store.match(
    /async function applyRowChanges[\s\S]*?\n  \}\n/
  )?.[0];

  assert.ok(deleteRecord, "deleteRecord implementation must be present");
  assert.match(deleteRecord, /options\.selectedRowIndexSet\.value\.has\(rowIndex\)/);
  assert.match(deleteRecord, /pending\.stageDelete\(primaryKey\)/);
  assert.doesNotMatch(deleteRecord, /databaseStore\.(?:deleteRows|applyRowChanges)/);

  assert.ok(applyRowChanges, "applyRowChanges implementation must be present");
  assert.match(applyRowChanges, /"db_apply_changes"/);
  assert.match(applyRowChanges, /kind: "rows"/);
  // The backend binds the rows token to the vector namespace too: the check must name the one submitted.
  assert.match(applyRowChanges, /operation: \{ kind: "rows", table, changes, namespace: options\.namespace \}/);
  assert.equal((applyRowChanges.match(/openTableForScope\(/g) ?? []).length, 1);
});
test("query result stream counts received chunks so a stream with output is never re-run", () => {
  const stream = createQueryResultStream("query-r");
  assert.equal(stream.receivedChunks(), 0);
  stream.append({ queryId: "query-r", sequence: 0, columns: ["id"], rows: [], estimatedBytes: 8 });
  assert.equal(stream.receivedChunks(), 1);
});

test("a finished undo reloads the open grid of that table and warns about edits staged before it", async () => {
  const [view, header] = await Promise.all([
    readFile(new URL("../src/views/DatabaseWorkspaceView.vue", import.meta.url), "utf8"),
    readFile(new URL("../src/components/database/query/DatabaseDatasourceHeader.vue", import.meta.url), "utf8"),
  ]);
  assert.match(header, /@undone="emit\('undone', \$event\)"/);
  assert.match(view, /<DatabaseDatasourceHeader[\s\S]*?@undone="onSafetyUndone"[\s\S]*?\/>/);
  const handler = view.match(/function onSafetyUndone[\s\S]*?\n\}\n/)?.[0];
  assert.ok(handler, "onSafetyUndone must be present");
  assert.match(handler, /databaseStore\.invalidateTableMetadata\(/);
  assert.match(handler, /forgetTableLastPages\(tab\.id\)/);
  assert.match(handler, /pendingChanges\.total\.value[\s\S]*ElMessage\.warning\(t\("database\.safety\.history\.pendingBeforeUndo"\)\)/);
  // Reloads without the leave guard: staged edits stay overlaid by primary key.
  assert.match(handler, /void refreshTablePage\(\)/);
  assert.doesNotMatch(handler, /confirmLeavePendingTable/);
});
