import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const {
  SNAPSHOT_STARTUP_PRUNE_INTERVAL_MS,
  normalizeSnapshotPruneTimes,
  snapshotRootsDueForPrune,
} = await import("../src/domain/ai/snapshot-retention.ts");

const store = readFileSync(new URL("../src/stores/ai-assistant.ts", import.meta.url), "utf8");
const DAY = 24 * 60 * 60 * 1000;

test("the startup sweep prunes each root at most once a day", () => {
  assert.equal(SNAPSHOT_STARTUP_PRUNE_INTERVAL_MS, DAY);
  const now = 10 * DAY;
  const times = { "/recent": now - DAY + 1, "/old": now - DAY, "/future": now + 5 * DAY };
  assert.deepEqual(
    snapshotRootsDueForPrune(["/recent", "/old", "/never", "/never", "", "/future"], times, now),
    ["/old", "/never", "/future"],
  );
  // a clock that went backwards must not block pruning forever
  assert.deepEqual(snapshotRootsDueForPrune(["/a"], { "/a": now - 10 }, now, 5), ["/a"]);
});

test("stored prune times are sanitised", () => {
  assert.deepEqual(normalizeSnapshotPruneTimes(null), {});
  assert.deepEqual(normalizeSnapshotPruneTimes([1, 2]), {});
  assert.deepEqual(normalizeSnapshotPruneTimes("x"), {});
  assert.deepEqual(
    normalizeSnapshotPruneTimes({ "/a": 5, "/b": "6", "/c": Number.NaN, "": 3, "/d": -1 }),
    { "/a": 5 },
  );
});

test("startup hydration schedules a rate-limited prune of every referenced root", () => {
  const hydrate = store.slice(store.indexOf("async function hydrate("), store.indexOf("function setDraft("));
  assert.match(hydrate, /hydrated\.value = true;[\s\S]*void scheduleStartupSnapshotPrune\(\);/);
  const startup = store.slice(store.indexOf("async function scheduleStartupSnapshotPrune"));
  const body = startup.slice(0, startup.indexOf("\n  }\n"));
  assert.match(body, /collectTurnSnapshotRecords\(\{ union: true, memoryOnlyOnError: true \}\)/);
  assert.match(body, /snapshotRootsDueForPrune\(/);
  assert.match(body, /scheduleSnapshotPrune\(due\)/);
});

test("a successful prune records its time; storage failures never throw", () => {
  const run = store.slice(store.indexOf("async function runPendingSnapshotPrunes"), store.indexOf("function scheduleSnapshotPrune"));
  assert.match(run, /await pruneSnapshotRoot\(\{[\s\S]*?\}\);\s*void markSnapshotRootPruned\(root\);/);
  // unsaved rewinds and clears must reach storage first, or the union keeps their trees
  assert.match(run, /try \{\s*await flushPersistence\(\);\s*\} catch \{/);
  const load = store.slice(store.indexOf("async function loadSnapshotPruneTimes"), store.indexOf("async function scheduleStartupSnapshotPrune"));
  assert.match(load, /try \{\s*snapshotPruneTimes = normalizeSnapshotPruneTimes\(await loadPersistedValue<unknown>\(AI_SNAPSHOT_PRUNE_TIMES_KEY\)\);\s*\} catch \{/);
  assert.match(load, /try \{\s*await savePersistedValue\(AI_SNAPSHOT_PRUNE_TIMES_KEY, \{ \.\.\.times \}\);\s*\} catch \{/);
});

test("rewinding and clearing a conversation prune the roots their records referenced", () => {
  const rewind = store.slice(store.indexOf("function rewindConversationTo"), store.indexOf("function recordTurnSnapshots"));
  assert.match(rewind, /const droppedRoots = conversation\.turnSnapshots\?\.map\(\(record\) => record\.projectRoot\) \?\? \[\];/);
  assert.match(rewind, /scheduleSnapshotPrune\(droppedRoots\);[\s\S]*return cloneMessage\(rewound\.removed\);/);
  const clear = store.slice(store.indexOf("function clearConversationMessages"), store.indexOf("function conversationIsVisible"));
  assert.match(clear, /const droppedRoots = conversation\.turnSnapshots\?\.map\(\(record\) => record\.projectRoot\) \?\? \[\];/);
  assert.match(clear, /scheduleSnapshotPrune\(droppedRoots\);/);
});
