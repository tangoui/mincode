import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import {
  createTerminalRecordingStore,
  LEGACY_TERMINAL_RECORDINGS_KEY,
  TERMINAL_RECORDINGS_INDEX_KEY,
  terminalRecordingKey,
} from "../src/domain/terminal/recording-store.ts";

const clone = (value) => JSON.parse(JSON.stringify(value));

function recording(id, createdAt = 1) {
  return { id, name: `rec ${id}`, createdAt, frames: [{ t: 0, d: `out-${id}` }] };
}

function createPersistence(initial = {}) {
  const values = new Map(Object.entries(initial));
  const loads = [];
  const batches = [];
  let failApply = false;
  return {
    values,
    loads,
    batches,
    failNextApply() {
      failApply = true;
    },
    persistence: {
      async load(key) {
        loads.push(key);
        return values.has(key) ? clone(values.get(key)) : null;
      },
      async apply({ sets, deleteKeys }) {
        if (failApply) {
          failApply = false;
          throw new Error("store failed");
        }
        batches.push({ sets: sets.map(({ key }) => key), deleteKeys: [...deleteKeys] });
        for (const { key, value } of sets) values.set(key, clone(value));
        for (const key of deleteKeys) values.delete(key);
      },
    },
  };
}

test("concurrent callers share one load", async () => {
  const storage = createPersistence({ [TERMINAL_RECORDINGS_INDEX_KEY]: [] });
  const published = [];
  const store = createTerminalRecordingStore(storage.persistence, (value) => published.push(value));
  const [first, second] = await Promise.all([store.ensureLoaded(), store.ensureLoaded()]);
  assert.equal(first, second);
  await store.ensureLoaded();
  assert.deepEqual(storage.loads, [TERMINAL_RECORDINGS_INDEX_KEY]);
  assert.equal(published.length, 1);
});

test("the legacy single key is migrated to an index plus one key per recording", async () => {
  const legacy = [recording("a"), recording("b"), { id: "bad", frames: "nope" }];
  const storage = createPersistence({ [LEGACY_TERMINAL_RECORDINGS_KEY]: legacy });
  const store = createTerminalRecordingStore(storage.persistence, () => {});
  const loaded = await store.ensureLoaded();
  assert.deepEqual(loaded.map((item) => item.id), ["a", "b"]);
  assert.equal(storage.values.has(LEGACY_TERMINAL_RECORDINGS_KEY), false);
  assert.deepEqual(storage.values.get(TERMINAL_RECORDINGS_INDEX_KEY), [
    { id: "a", name: "rec a", createdAt: 1 },
    { id: "b", name: "rec b", createdAt: 1 },
  ]);
  assert.deepEqual(storage.values.get(terminalRecordingKey("b")).frames, [{ t: 0, d: "out-b" }]);

  const reloaded = await createTerminalRecordingStore(storage.persistence, () => {}).ensureLoaded();
  assert.deepEqual(reloaded, loaded);
});

test("a failed migration keeps the legacy data readable", async () => {
  const storage = createPersistence({ [LEGACY_TERMINAL_RECORDINGS_KEY]: [recording("a")] });
  storage.failNextApply();
  const loaded = await createTerminalRecordingStore(storage.persistence, () => {}).ensureLoaded();
  assert.deepEqual(loaded.map((item) => item.id), ["a"]);
  assert.equal(storage.values.has(LEGACY_TERMINAL_RECORDINGS_KEY), true);
  const retried = await createTerminalRecordingStore(storage.persistence, () => {}).ensureLoaded();
  assert.deepEqual(retried.map((item) => item.id), ["a"]);
  assert.equal(storage.values.has(LEGACY_TERMINAL_RECORDINGS_KEY), false);
});

test("adding writes only the new recording and the index, evicting past the limit", async () => {
  const ids = Array.from({ length: 20 }, (_, index) => `r${index}`);
  const initial = { [TERMINAL_RECORDINGS_INDEX_KEY]: ids.map((id) => ({ id, name: id, createdAt: 1 })) };
  for (const id of ids) initial[terminalRecordingKey(id)] = recording(id);
  const storage = createPersistence(initial);
  let current = [];
  const store = createTerminalRecordingStore(storage.persistence, (value) => { current = value; });

  await store.add(recording("new", 2));
  assert.equal(current.length, 20);
  assert.equal(current[0].id, "new");
  assert.deepEqual(storage.batches, [{
    sets: [terminalRecordingKey("new"), TERMINAL_RECORDINGS_INDEX_KEY],
    deleteKeys: [terminalRecordingKey("r19")],
  }]);
  assert.equal(storage.values.has(terminalRecordingKey("r19")), false);
});

test("deleting removes the recording key and rewrites the index only", async () => {
  const storage = createPersistence({
    [TERMINAL_RECORDINGS_INDEX_KEY]: [{ id: "a", name: "a", createdAt: 1 }, { id: "b", name: "b", createdAt: 1 }],
    [terminalRecordingKey("a")]: recording("a"),
    [terminalRecordingKey("b")]: recording("b"),
  });
  let current = [];
  const store = createTerminalRecordingStore(storage.persistence, (value) => { current = value; });
  await store.remove("a");
  assert.deepEqual(current.map((item) => item.id), ["b"]);
  assert.deepEqual(storage.batches, [{
    sets: [TERMINAL_RECORDINGS_INDEX_KEY],
    deleteKeys: [terminalRecordingKey("a")],
  }]);
});

test("index entries whose recording key is missing or corrupt are skipped", async () => {
  const storage = createPersistence({
    [TERMINAL_RECORDINGS_INDEX_KEY]: [{ id: "a", name: "a", createdAt: 1 }, { id: "b", name: "b", createdAt: 1 }],
    [terminalRecordingKey("b")]: { ...recording("b"), frames: [{ t: -1, d: "x" }] },
  });
  const loaded = await createTerminalRecordingStore(storage.persistence, () => {}).ensureLoaded();
  assert.deepEqual(loaded, []);
});

test("terminal panes load recordings lazily from the shared cache", async () => {
  for (const path of ["LocalTerminal.vue", "SshTerminal.vue"]) {
    const source = await readFile(new URL(`../src/components/terminal/${path}`, import.meta.url), "utf8");
    assert.doesNotMatch(source, /loadTerminalRecordings|ref<SavedTerminalRecording/);
    assert.match(source, /watch\(playbackOpen, \(visible\) => \{\s*if \(visible\) void ensureTerminalRecordingsLoaded\(\);/);
  }
});

test("renaming rewrites the entry and the index but keeps the frames", async () => {
  const storage = createPersistence({
    [TERMINAL_RECORDINGS_INDEX_KEY]: [{ id: "a", name: "rec a", createdAt: 1 }],
    [terminalRecordingKey("a")]: recording("a"),
  });
  let current = [];
  const store = createTerminalRecordingStore(storage.persistence, (value) => { current = value; });

  await store.rename("a", "  生产环境  ");

  assert.equal(current[0].name, "生产环境");
  assert.equal(storage.values.get(terminalRecordingKey("a")).name, "生产环境");
  assert.deepEqual(storage.values.get(terminalRecordingKey("a")).frames, [{ t: 0, d: "out-a" }]);
  assert.equal(storage.values.get(TERMINAL_RECORDINGS_INDEX_KEY)[0].name, "生产环境");
  assert.deepEqual(storage.batches, [{
    sets: [terminalRecordingKey("a"), TERMINAL_RECORDINGS_INDEX_KEY],
    deleteKeys: [],
  }]);
});

test("renaming ignores blank drafts, unchanged names and unknown ids", async () => {
  const storage = createPersistence({
    [TERMINAL_RECORDINGS_INDEX_KEY]: [{ id: "a", name: "rec a", createdAt: 1 }],
    [terminalRecordingKey("a")]: recording("a"),
  });
  let current = [];
  const store = createTerminalRecordingStore(storage.persistence, (value) => { current = value; });

  await store.rename("a", "   ");
  await store.rename("a", "rec a");
  await store.rename("missing", "另一个名字");

  assert.equal(current[0].name, "rec a");
  assert.deepEqual(storage.batches, []);
});

test("renaming one recording leaves the others alone", async () => {
  const storage = createPersistence({
    [TERMINAL_RECORDINGS_INDEX_KEY]: [{ id: "a", name: "rec a", createdAt: 1 }, { id: "b", name: "rec b", createdAt: 2 }],
    [terminalRecordingKey("a")]: recording("a"),
    [terminalRecordingKey("b")]: recording("b", 2),
  });
  let current = [];
  const store = createTerminalRecordingStore(storage.persistence, (value) => { current = value; });

  await store.rename("b", "构建日志");

  assert.deepEqual(current.map((item) => [item.id, item.name]), [["a", "rec a"], ["b", "构建日志"]]);
  assert.equal(storage.values.get(terminalRecordingKey("a")).name, "rec a");
});
