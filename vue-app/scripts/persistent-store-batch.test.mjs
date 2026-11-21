import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

async function importTypeScriptSource(path, transform) {
  const source = transform(await readFile(new URL(path, import.meta.url), "utf8"));
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  return import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}#${Math.random()}`);
}

function createLocalStorage() {
  const values = new Map();
  return {
    get length() { return values.size; },
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => values.delete(key),
    setItem: (key, value) => values.set(key, value),
  };
}

globalThis.window = { localStorage: createLocalStorage() };
globalThis.localStorage = globalThis.window.localStorage;
globalThis.__persistentStoreBatchMocks = {
  desktop: true,
  calls: [],
  failure: null,
  blockers: [],
};

const persistentStore = await importTypeScriptSource(
  "../src/services/persistent-store.ts",
  (source) => source
    .replace(
      'import { createSerializedTaskQueue } from "@/domain/async/serialized-task-queue";',
      `const createSerializedTaskQueue = () => {
         let tail = Promise.resolve();
         return (task) => {
           const result = tail.catch(() => undefined).then(task);
           tail = result.then(() => undefined, () => undefined);
           return result;
         };
       };`
    )
    .replace(
      'import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";',
      `const isDesktopRuntime = () => globalThis.__persistentStoreBatchMocks.desktop;
       const invokeBackend = async (command, args) => {
         globalThis.__persistentStoreBatchMocks.calls.push([command, args]);
         const blocker = globalThis.__persistentStoreBatchMocks.blockers.shift();
         if (blocker) await blocker;
         if (globalThis.__persistentStoreBatchMocks.failure) {
           throw globalThis.__persistentStoreBatchMocks.failure;
         }
       };`
    )
);

globalThis.__persistentStoreBatchMocks.persistentStore = persistentStore;

const secureStore = await importTypeScriptSource(
  "../src/services/secure-persistent-store.ts",
  (source) => source
    .replace(
      /import \{[\s\S]*?\} from "@\/services\/persistent-store";/,
      `const {
         clearDesktopBrowserSnapshot,
         enqueuePersistedWrite,
         isPersistedWriteFrozen,
         writePersistedBrowserSnapshot: writeBrowserSnapshot,
         writesDesktopBrowserSnapshot,
       } = globalThis.__persistentStoreBatchMocks.persistentStore;`
    )
    .replace(
      'import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";',
      `const isDesktopRuntime = () => globalThis.__persistentStoreBatchMocks.desktop;
       const invokeBackend = async (command, args) => {
         globalThis.__persistentStoreBatchMocks.calls.push([command, args]);
         const blocker = globalThis.__persistentStoreBatchMocks.blockers.shift();
         if (blocker) await blocker;
         if (globalThis.__persistentStoreBatchMocks.failure) {
           throw globalThis.__persistentStoreBatchMocks.failure;
         }
         return command === "seal_object" ? "enc:v2:prepared" : undefined;
       };`
    )
);

function reset({ desktop = true } = {}) {
  globalThis.window.localStorage = createLocalStorage();
  globalThis.localStorage = globalThis.window.localStorage;
  globalThis.__persistentStoreBatchMocks.desktop = desktop;
  globalThis.__persistentStoreBatchMocks.calls = [];
  globalThis.__persistentStoreBatchMocks.failure = null;
  globalThis.__persistentStoreBatchMocks.blockers = [];
}

async function waitForCallCount(expected) {
  for (let attempt = 0; attempt < 10; attempt += 1) {
    if (globalThis.__persistentStoreBatchMocks.calls.length >= expected) return;
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.fail(`Timed out waiting for ${expected} persistence calls`);
}

test("desktop batch commits once before writing browser snapshots", async () => {
  reset();
  await persistentStore.applyPersistedBatch({
    sets: [
      { key: "app_sidebar_order_v2", value: { secret: "db" }, browserSnapshot: { secret: "" } },
      { key: "app_settings_v2", value: "enc:v2:cipher", browserSnapshot: { token: "" } },
    ],
    deleteKeys: ["app_old"],
  });

  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls, [[
    "store_apply_batch",
    { operations: [
      { type: "set", key: "app_sidebar_order_v2", value: { secret: "db" } },
      { type: "set", key: "app_settings_v2", value: "enc:v2:cipher" },
      { type: "delete", key: "app_old" },
    ] },
  ]]);
  assert.equal(localStorage.getItem("app_sidebar_order_v2"), '{"secret":""}');
  assert.equal(localStorage.getItem("app_settings_v2"), '{"token":""}');
  assert.doesNotMatch(localStorage.getItem("app_settings_v2"), /enc:v2:/);
});


test("desktop batch drops stale browser copies of keys without synchronous readers", async () => {
  reset();
  localStorage.setItem("app_batch_plain", '{"stale":true}');
  await persistentStore.applyPersistedBatch({
    sets: [{ key: "app_batch_plain", value: { fresh: true } }],
  });
  assert.equal(localStorage.getItem("app_batch_plain"), null);
});

test("batch sets default the browser snapshot to the persisted value", async () => {
  reset({ desktop: false });
  await persistentStore.applyPersistedBatch({
    sets: [{ key: "app_workspace_cleanup", value: { retained: true } }],
  });

  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls, []);
  assert.equal(localStorage.getItem("app_workspace_cleanup"), '{"retained":true}');
});

test("desktop batch can atomically clear every normalized AI workspace", async () => {
  reset();
  await persistentStore.applyPersistedBatch({
    deleteKeys: ["app_ai_conversations_v2"],
    deleteAllAiHistory: true,
  });

  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls, [[
    "store_apply_batch",
    { operations: [
      { type: "delete", key: "app_ai_conversations_v2" },
      { type: "delete_all_ai", key: "*" },
    ] },
  ]]);
});

test("desktop batch failure leaves every browser snapshot unchanged", async () => {
  reset();
  localStorage.setItem("app_plain", '{"before":true}');
  globalThis.__persistentStoreBatchMocks.failure = new Error("batch failed");

  await assert.rejects(
    persistentStore.applyPersistedBatch({
      sets: [{ key: "app_plain", value: { after: true }, browserSnapshot: { after: true } }],
    }),
    /batch failed/
  );
  assert.equal(localStorage.getItem("app_plain"), '{"before":true}');
});

test("invalid prepared input fails before native batch or snapshots", async () => {
  reset();
  await assert.rejects(
    persistentStore.applyPersistedBatch({
      sets: [{ key: "app_invalid", value: undefined, browserSnapshot: { ready: true } }],
    }),
    /persisted_value_not_serializable/
  );
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls, []);
  assert.equal(localStorage.length, 0);
});

test("browser preview writes snapshots without invoking native storage", async () => {
  reset({ desktop: false });
  await persistentStore.applyPersistedBatch({
    sets: [{ key: "app_preview", value: { secret: "value" }, browserSnapshot: { secret: "" } }],
  });
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls, []);
  assert.equal(localStorage.getItem("app_preview"), '{"secret":""}');
});

test("secure preparation seals without persistence and never snapshots cipher", async () => {
  reset();
  const cipher = await secureStore.prepareSecurePersistedValue({ token: "secret" });
  assert.equal(cipher, "enc:v2:prepared");
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls, [[
    "seal_object",
    { json: '{"token":"secret"}' },
  ]]);
  assert.equal(localStorage.length, 0);
});

test("secure save queues seal and native set as one operation before a later batch", async () => {
  reset();
  let releaseSeal;
  globalThis.__persistentStoreBatchMocks.blockers.push(
    new Promise((resolve) => { releaseSeal = resolve; })
  );

  const secureSave = secureStore.saveSecurePersistedValue(
    "app_settings_v2",
    { token: "secret" },
    { token: "" }
  );
  const laterBatch = persistentStore.applyPersistedBatch({
    sets: [{ key: "app_after", value: { order: 2 }, browserSnapshot: { order: 2 } }],
  });
  await waitForCallCount(1);
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls, [[
    "seal_object",
    { json: '{"token":"secret"}' },
  ]]);
  assert.equal(localStorage.getItem("app_settings_v2"), '{"token":""}');

  releaseSeal();
  await Promise.all([secureSave, laterBatch]);
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls.map(([command]) => command), [
    "seal_object",
    "store_set",
    "store_apply_batch",
  ]);
});

test("a batch blocks a later single-key save across different keys", async () => {
  reset();
  let releaseBatch;
  globalThis.__persistentStoreBatchMocks.blockers.push(
    new Promise((resolve) => { releaseBatch = resolve; })
  );

  const batch = persistentStore.applyPersistedBatch({
    sets: [{ key: "app_first", value: 1, browserSnapshot: 1 }],
  });
  const laterSave = persistentStore.savePersistedValue("app_second", 2);
  await waitForCallCount(1);
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls.map(([command]) => command), [
    "store_apply_batch",
  ]);

  releaseBatch();
  await Promise.all([batch, laterSave]);
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls.map(([command]) => command), [
    "store_apply_batch",
    "store_set",
  ]);
});

test("default secure save is best-effort: native failure keeps sanitized snapshot and resolves", async () => {
  reset();
  globalThis.__persistentStoreBatchMocks.failure = new Error("native failed");
  await secureStore.saveSecurePersistedValue(
    "app_secure",
    { token: "secret" },
    { token: "" }
  );
  assert.equal(localStorage.getItem("app_secure"), '{"token":""}');
  assert.doesNotMatch(localStorage.getItem("app_secure"), /secret|enc:v2:/);
});

test("strict secure save rejects on native failure, keeps the snapshot and does not stall the queue", async () => {
  reset();
  globalThis.__persistentStoreBatchMocks.failure = new Error("native failed");
  await assert.rejects(
    secureStore.saveSecurePersistedValue(
      "app_secure",
      { token: "secret" },
      { token: "" },
      { strict: true }
    ),
    /native failed/
  );
  assert.equal(localStorage.getItem("app_secure"), '{"token":""}');
  assert.doesNotMatch(localStorage.getItem("app_secure"), /secret|enc:v2:/);

  globalThis.__persistentStoreBatchMocks.failure = null;
  globalThis.__persistentStoreBatchMocks.calls = [];
  await secureStore.saveSecurePersistedValue(
    "app_secure",
    { token: "secret" },
    { token: "" },
    { strict: true }
  );
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls.map(([command]) => command), [
    "seal_object",
    "store_set",
  ]);
  assert.equal(localStorage.getItem("app_secure"), null);
});

test("desktop single-key save skips the browser snapshot and clears a stale copy", async () => {
  reset();
  localStorage.setItem("app_desktop_plain", '{"stale":true}');
  await persistentStore.savePersistedValue("app_desktop_plain", { fresh: true });
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls, [[
    "store_set",
    { key: "app_desktop_plain", value: { fresh: true } },
  ]]);
  assert.equal(localStorage.getItem("app_desktop_plain"), null);
});

test("desktop single-key native failure writes an emergency browser fallback", async () => {
  reset();
  globalThis.__persistentStoreBatchMocks.failure = new Error("native failed");
  await persistentStore.savePersistedValue("app_desktop_failure", { value: 1 });
  assert.equal(localStorage.getItem("app_desktop_failure"), '{"value":1}');

  globalThis.__persistentStoreBatchMocks.failure = null;
  await persistentStore.savePersistedValue("app_desktop_failure", { value: 2 });
  assert.equal(localStorage.getItem("app_desktop_failure"), null);
});

test("synchronously read keys keep desktop browser snapshots", async () => {
  reset();
  await persistentStore.savePersistedValue("app_workspaces_v1", [{ id: "w" }]);
  assert.equal(localStorage.getItem("app_workspaces_v1"), '[{"id":"w"}]');

  persistentStore.registerDesktopBrowserSnapshotKeys("app_test_pane_width");
  await persistentStore.savePersistedValue("app_test_pane_width", 240);
  assert.equal(localStorage.getItem("app_test_pane_width"), "240");

  await persistentStore.savePersistedValue("app_explicit_opt_in", true, {
    writeBrowserSnapshotInDesktop: true,
  });
  assert.equal(localStorage.getItem("app_explicit_opt_in"), "true");
});

test("desktop secure save of a key without synchronous readers never touches localStorage", async () => {
  reset();
  localStorage.setItem("app_secure_policy", '{"legacy":true}');
  await secureStore.saveSecurePersistedValue(
    "app_secure_policy",
    { token: "secret" },
    { token: "" }
  );
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls.map(([command]) => command), [
    "seal_object",
    "store_set",
  ]);
  assert.equal(localStorage.getItem("app_secure_policy"), null);
});

test("desktop settings save keeps only the sanitized browser snapshot", async () => {
  reset();
  await secureStore.saveSecurePersistedValue(
    "app_settings_v2",
    { uiTheme: "dark", apiKey: "secret" },
    { uiTheme: "dark", apiKey: "" }
  );
  assert.equal(localStorage.getItem("app_settings_v2"), '{"uiTheme":"dark","apiKey":""}');
});

test("browser preview secure save writes the sanitized snapshot only", async () => {
  reset({ desktop: false });
  await secureStore.saveSecurePersistedValue("app_secure_policy", { token: "secret" }, { token: "" });
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls, []);
  assert.equal(localStorage.getItem("app_secure_policy"), '{"token":""}');
});

test("desktop reads issued in the same tick share one store_get_many call", async () => {
  reset();
  localStorage.setItem("app_read_b", '{"fallback":true}');
  const results = await Promise.all([
    persistentStore.loadPersistedValue("app_read_a"),
    persistentStore.loadPersistedValue("app_read_b"),
    persistentStore.loadPersistedValue("app_read_a"),
  ]);

  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls, [
    ["store_get_many", { keys: ["app_read_a", "app_read_b"] }],
  ]);
  assert.deepEqual(results, [null, { fallback: true }, null]);

  reset();
  await persistentStore.loadPersistedValue("app_read_single");
  assert.deepEqual(globalThis.__persistentStoreBatchMocks.calls, [
    ["store_get", { key: "app_read_single" }],
  ]);
});

test("desktop batched read failures fall back to browser snapshots", async () => {
  reset();
  globalThis.__persistentStoreBatchMocks.failure = new Error("native store unavailable");
  localStorage.setItem("app_read_fallback", '"browser"');
  const results = await Promise.all([
    persistentStore.loadPersistedValue("app_read_fallback"),
    persistentStore.loadPersistedValue("app_read_missing"),
  ]);
  assert.deepEqual(results, ["browser", null]);
  assert.equal(globalThis.__persistentStoreBatchMocks.calls.length, 1);
});
