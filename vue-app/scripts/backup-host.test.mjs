// Backup channel (backend/plugin/host_backup.go): the Go host asks
// the frontend to collect or write MinCode's stored data and to show the two
// host-owned dialogs (backup password, restore confirmation) over
// plugin:backup-request; the frontend answers through plugin_host_backup_reply.
import "./ts-resolve.mjs";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { registerHooks } from "node:module";
import test from "node:test";

// --- Stubs: the desktop bridge, Element Plus and the i18n registry. ---------------
const desktopStub = `
  const state = () => globalThis.__backupHostTest;
  export const isDesktopRuntime = () => true;
  export const canInvoke = () => true;
  export const listenAppEvent = () => () => undefined;
  export const nativeErrorMessage = (error) => String(error);
  export const invokeBackend = async (command, args = {}) => {
    const test = state();
    test.calls.push([command, JSON.parse(JSON.stringify(args))]);
    if (test.failOn === command) throw new Error("native failure with secret sk-live-123");
    if (command === "store_load_all") return test.stored;
    if (command === "store_get") return test.stored[args.key] ?? null;
    if (command === "unseal_object") return JSON.stringify(test.sealed[args.cipher]);
    if (command === "seal_object") return "enc:v2:sealed(" + args.json + ")";
    if (command === "encrypt_fields") return args.plains.map((plain) => (plain ? "enc:v2:" + plain : plain));
    if (command === "decrypt_fields") return args.ciphers.map((cipher) => ({ value: cipher.replace("enc:v2:", "") }));
    if (command === "store_apply_batch") {
      if (test.batchGate) await test.batchGate;
      return undefined;
    }
    return undefined;
  };
`;
const elementPlusStub = `
  const messages = () => globalThis.__backupHostTest.messages;
  export const ElMessage = Object.assign((options) => {
    const entry = { ...options, closed: false };
    messages().push(entry);
    return { close: () => (entry.closed = true) };
  }, {
    warning: (options) => messages().push({ type: "warning", ...(typeof options === "string" ? { message: options } : options) }),
    success: (options) => messages().push({ type: "success", ...(typeof options === "string" ? { message: options } : options) }),
    error: (options) => messages().push({ type: "error", ...(typeof options === "string" ? { message: options } : options) }),
  });
`;
const i18nStub = `
  export const useVueI18n = () => ({ t: (key, params) => (params ? key + " " + JSON.stringify(params) : key) });
`;
const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "element-plus") return { url: dataModule(elementPlusStub), shortCircuit: true };
    if (specifier === "@/i18n/messages") return { url: dataModule(i18nStub), shortCircuit: true };
    const resolved = nextResolve(specifier, context);
    if (resolved.url.endsWith("/src/services/desktop-bridge.ts")) return { url: dataModule(desktopStub), shortCircuit: true };
    return resolved;
  },
});

const storage = new Map();
globalThis.window = Object.assign(new EventTarget(), {
  localStorage: {
    getItem: (key) => (storage.has(key) ? storage.get(key) : null),
    setItem: (key, value) => storage.set(key, String(value)),
    removeItem: (key) => storage.delete(key),
    get length() {
      return storage.size;
    },
    key: (index) => [...storage.keys()][index] ?? null,
  },
});
globalThis.localStorage = globalThis.window.localStorage;

function reset(overrides = {}) {
  // A restore that needs a window reload leaves every write frozen; tests start unfrozen.
  if (persistentStore) persistentStore.clearPersistedWriteFreezesForTests();
  storage.clear();
  const state = { calls: [], stored: {}, sealed: {}, messages: [], failOn: "", batchGate: undefined, ...overrides };
  globalThis.__backupHostTest = state;
  return state;
}
let persistentStore;
reset();
persistentStore = await import("../src/services/persistent-store.ts");
const secureStore = await import("../src/services/secure-persistent-store.ts");

const requests = await import("../src/domain/backup/requests.ts");
const restoredEvent = await import("../src/domain/backup/restored-event.ts");
const backupHost = await import("../src/services/backup-host.ts");
const settingsPersistence = await import("../src/services/settings-persistence.ts");

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");
const origin = { id: "backup-1-1", pluginId: "mincode.backup", pluginName: "Backup" };
const event = (kind, payload, extra = {}) => ({ ...origin, kind, payload, ...extra });
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

// --- Parsing ------------------------------------------------------------------

test("parseBackupRequest accepts the four kinds", () => {
  assert.deepEqual(requests.parseBackupRequest(event("backupCollect", { includeSecrets: false })), {
    request: { ...origin, kind: "backupCollect", includeSecrets: false },
  });
  assert.deepEqual(requests.parseBackupRequest(event("backupPassword", { mode: "set" })), {
    request: { ...origin, kind: "backupPassword", mode: "set" },
  });
  const changes = [
    { section: "ssh", sectionTitle: "SSH", kind: "add", label: "prod" },
    { section: "", sectionTitle: "", kind: "more", label: "3" },
  ];
  assert.deepEqual(
    requests.parseBackupRequest(event("backupRestoreConfirm", { mode: "overwrite", pluginName: "Spoofed", changes })),
    { request: { ...origin, kind: "backupRestoreConfirm", mode: "overwrite", changes } },
  );
  const sets = { app_settings_v2: { storage: "secure", value: { theme: "dark" } }, app_x: { storage: "plain", value: null } };
  assert.deepEqual(requests.parseBackupRequest(event("backupApply", { sets, deletes: ["app_old"] })), {
    request: { ...origin, kind: "backupApply", sets, deletes: ["app_old"] },
  });
});

test("parseBackupRequest takes the plugin name from the event, never from the payload", () => {
  const parsed = requests.parseBackupRequest(
    event("backupRestoreConfirm", { mode: "merge", pluginName: "MinCode itself", changes: [] }),
  );
  assert.equal(parsed.request.pluginName, "Backup");
  assert.equal("pluginName" in parsed.request && parsed.request.pluginName, "Backup");
  const unnamed = requests.parseBackupRequest({ ...event("backupPassword", { mode: "open" }), pluginName: "" });
  assert.equal(unnamed.request.pluginName, "mincode.backup");
});

test("parseBackupRequest rejects malformed requests and keeps the id to answer", () => {
  const invalid = [
    [event("backupDelete", {}), "backup-1-1"],
    [{ ...event("backupCollect", { includeSecrets: true }), id: undefined }, undefined],
    [{ ...event("backupCollect", { includeSecrets: true }), id: "" }, undefined],
    [{ ...event("backupCollect", { includeSecrets: true }), id: 7 }, undefined],
    [event("backupCollect", { includeSecrets: "yes" }), "backup-1-1"],
    [event("backupCollect", null), "backup-1-1"],
    [event("backupPassword", { mode: "reset" }), "backup-1-1"],
    [event("backupRestoreConfirm", { mode: "replace", changes: [] }), "backup-1-1"],
    [event("backupRestoreConfirm", { mode: "merge", changes: {} }), "backup-1-1"],
    [event("backupRestoreConfirm", { mode: "merge", changes: [{ section: "ssh", sectionTitle: "", kind: "drop", label: "x" }] }), "backup-1-1"],
    [event("backupRestoreConfirm", { mode: "merge", changes: [{ section: "ssh", sectionTitle: "", kind: "add", label: 3 }] }), "backup-1-1"],
    [event("backupRestoreConfirm", { mode: "merge", changes: [{ section: "", sectionTitle: "", kind: "more", label: "many" }] }), "backup-1-1"],
    [event("backupApply", { sets: {}, deletes: "app_x" }), "backup-1-1"],
    [event("backupApply", { sets: [], deletes: [] }), "backup-1-1"],
    [event("backupApply", { sets: { app_x: { storage: "cloud", value: 1 } }, deletes: [] }), "backup-1-1"],
    [event("backupApply", { sets: { app_x: { storage: "plain" } }, deletes: [] }), "backup-1-1"],
    [event("backupApply", { sets: { "../etc": { storage: "plain", value: 1 } }, deletes: [] }), "backup-1-1"],
    [event("backupApply", { sets: {}, deletes: [1] }), "backup-1-1"],
    [event("backupApply", { sets: { app_x: { storage: "plain", value: 1 } }, deletes: ["app_x"] }), "backup-1-1"],
    [{ ...event("backupPassword", { mode: "set" }), pluginId: 3 }, "backup-1-1"],
    [null, undefined],
    ["backupCollect", undefined],
  ];
  for (const [input, id] of invalid) {
    assert.deepEqual(requests.parseBackupRequest(input), id ? { error: "invalid_request", id } : { error: "invalid_request" }, JSON.stringify(input));
  }
});

test("backupRequestCancelId reads only well-formed ids", () => {
  assert.equal(requests.backupRequestCancelId({ id: "backup-1-1" }), "backup-1-1");
  assert.equal(requests.backupRequestCancelId({ id: 1 }), undefined);
  assert.equal(requests.backupRequestCancelId(null), undefined);
});

test("backup passwords count code points and set mode asks twice", () => {
  assert.equal(requests.BACKUP_PASSWORD_MIN_LENGTH, 12);
  const emoji = "😀".repeat(12);
  assert.equal(emoji.length, 24);
  assert.equal(requests.backupPasswordIssue("set", "😀".repeat(11), "😀".repeat(11)), "tooShort");
  assert.equal(requests.backupPasswordIssue("set", "a".repeat(11) + "😀", "a".repeat(11) + "😀"), "");
  assert.equal(requests.backupPasswordIssue("set", emoji, emoji), "");
  assert.equal(requests.backupPasswordIssue("set", "correct horse battery", "correct horse batterY"), "mismatch");
  assert.equal(requests.backupPasswordIssue("set", "é".repeat(600), "é".repeat(600)), "tooLong");
  assert.equal(requests.backupPasswordIssue("open", "", ""), "empty");
  assert.equal(requests.backupPasswordIssue("open", "short", ""), "");
});

test("restore changes group by section then kind and make hidden text visible", () => {
  const grouped = requests.groupRestoreChanges([
    { section: "plugin:notes", sectionTitle: "Notes‮", kind: "update", label: "page" },
    { section: "ssh", sectionTitle: "SSH", kind: "remove", label: "old\u0007host" },
    { section: "settings", sectionTitle: "Settings", kind: "keep", label: "theme" },
    { section: "ssh", sectionTitle: "SSH", kind: "add", label: "new​host" },
    { section: "ssh", sectionTitle: "SSH", kind: "add", label: "second" },
    { section: "", sectionTitle: "", kind: "more", label: "42" },
  ]);
  assert.equal(grouped.more, 42);
  assert.deepEqual(grouped.sections.map((section) => section.section), ["settings", "ssh", "plugin:notes"]);
  const ssh = grouped.sections[1];
  assert.equal(ssh.core, true);
  assert.deepEqual(ssh.groups, [
    { kind: "add", labels: ["new\\u{200b}host", "second"] },
    { kind: "remove", labels: ["old\\u{7}host"] },
  ]);
  const plugin = grouped.sections[2];
  assert.equal(plugin.core, false);
  assert.equal(plugin.pluginId, "notes");
  assert.equal(plugin.sectionTitle, "Notes\\u{202e}");
});

// --- Collect ------------------------------------------------------------------

function storedDevice() {
  return {
    stored: {
      app_settings_v2: "enc:v2:settings",
      app_ssh_credentials_v1: { version: 1, credentialsByProfileId: { p1: { password: "enc:v2:hunter2" } } },
      app_db_connections: [{ id: "d1", name: "prod", host: "db", password: "enc:v2:dbpass" }],
      app_ssh_connections_v1: { version: 1, profiles: [{ id: "p1", name: "prod" }] },
      app_license: { key: "never" },
    },
    sealed: { "enc:v2:settings": { theme: "dark", aiProfiles: [{ name: "kimi", apiKey: "sk-1" }] } },
  };
}

test("collectBackupValues without secrets keeps every key and blanks the secrets", async () => {
  reset(storedDevice());
  const values = await backupHost.collectBackupValues(false);
  assert.ok(values.app_ssh_credentials_v1, "SSH credentials key stays so restore does not delete it");
  assert.ok(values.app_db_connections, "database connections key stays");
  assert.equal(values.app_ssh_credentials_v1.value.credentialsByProfileId.p1.password, "");
  assert.equal(values.app_db_connections.value[0].password, "");
  assert.equal(values.app_db_connections.value[0].host, "db");
  assert.equal(values.app_settings_v2.storage, "secure");
  assert.equal(values.app_settings_v2.value.aiProfiles[0].apiKey, "");
  assert.equal(values.app_license, undefined);
  assert.doesNotMatch(JSON.stringify(values), /hunter2|dbpass|sk-1/);
});

test("collectBackupValues with secrets decrypts field-encrypted keys", async () => {
  reset(storedDevice());
  const values = await backupHost.collectBackupValues(true);
  assert.equal(values.app_ssh_credentials_v1.value.credentialsByProfileId.p1.password, "hunter2");
  assert.equal(values.app_db_connections.value[0].password, "dbpass");
  assert.equal(values.app_settings_v2.value.aiProfiles[0].apiKey, "sk-1");
});

// --- Apply ----------------------------------------------------------------------

test("applyBackupValues writes sets and deletes in one native batch", async () => {
  const state = reset();
  const versionBefore = settingsPersistence.settingsSnapshotVersion.value;
  const restored = [];
  const listener = (e) => restored.push(restoredEvent.backupRestoredKeys(e));
  window.addEventListener(restoredEvent.BACKUP_RESTORED_EVENT, listener);
  let outcome;
  try {
    outcome = await backupHost.applyBackupValues(
      {
        app_settings_v2: { storage: "secure", value: { theme: "light" } },
        app_db_connections: { storage: "plain", value: [{ id: "d1", name: "prod", password: "dbpass" }] },
        app_sidebar_order_v2: { storage: "plain", value: ["ssh"] },
      },
      ["app_ssh_connection_group_order_v1"],
    );
  } finally {
    window.removeEventListener(restoredEvent.BACKUP_RESTORED_EVENT, listener);
  }
  const batches = state.calls.filter(([command]) => command === "store_apply_batch");
  assert.equal(batches.length, 1);
  const operations = batches[0][1].operations;
  assert.deepEqual(operations.map(({ type, key }) => [type, key]), [
    ["set", "app_settings_v2"],
    ["set", "app_db_connections"],
    ["set", "app_sidebar_order_v2"],
    ["delete", "app_ssh_connection_group_order_v1"],
  ]);
  assert.equal(operations[0].value, 'enc:v2:sealed({"theme":"light"})');
  assert.equal(operations[1].value[0].password, "enc:v2:dbpass");
  assert.equal(operations[2].value[0], "ssh");
  assert.equal(state.calls.filter(([command]) => command === "store_set" || command === "store_del").length, 0);
  assert.equal(settingsPersistence.settingsSnapshotVersion.value, versionBefore + 1);
  assert.equal(settingsPersistence.readSettingsSnapshot().theme, "light");
  assert.deepEqual(restored, [[
    "app_settings_v2",
    "app_db_connections",
    "app_sidebar_order_v2",
    "app_ssh_connection_group_order_v1",
  ]]);
  // Settings and layout cannot reload in place: every write stays frozen until the window reloads.
  assert.equal(outcome.reloadRequired, true);
  assert.equal(persistentStore.persistedWriteFreezeState().all, true);
});

test("a restore of SSH and database keys only reloads those stores and lifts its freeze after", async () => {
  const state = reset();
  let release;
  const reloads = [];
  const stop = restoredEvent.onBackupRestored(["app_ssh_connections_v1"], () => {
    reloads.push(persistentStore.persistedWriteFreezeState());
    return new Promise((resolve) => (release = resolve));
  });
  try {
    const applying = backupHost.applyBackupValues(
      { app_ssh_connections_v1: { storage: "plain", value: { version: 2, profiles: [] } } },
      ["app_db_connection_group_order"],
    );
    while (!release) await flush();
    // While the store reloads its keys stay frozen; others are writable.
    assert.deepEqual(reloads, [{ all: false, keys: ["app_db_connection_group_order", "app_ssh_connections_v1"] }]);
    await persistentStore.savePersistedValue("app_ssh_connections_v1", { stale: true });
    await persistentStore.savePersistedValue("app_sidebar_order_v2", ["ok"]);
    release();
    const outcome = await applying;
    assert.equal(outcome.reloadRequired, false);
  } finally {
    stop();
  }
  assert.deepEqual(persistentStore.persistedWriteFreezeState(), { all: false, keys: [] });
  assert.deepEqual(
    state.calls.filter(([command]) => command === "store_set").map(([, args]) => args.key),
    ["app_sidebar_order_v2"],
  );
});

test("a failed restore batch lifts the freeze", async () => {
  reset({ failOn: "store_apply_batch" });
  await assert.rejects(backupHost.applyBackupValues({ app_workspaces_v1: { storage: "plain", value: [] } }, []));
  assert.deepEqual(persistentStore.persistedWriteFreezeState(), { all: false, keys: [] });
});

test("frozen keys are skipped on every write path; the restore batch writes through", async () => {
  const state = reset();
  const lift = persistentStore.freezePersistedWrites(["app_frozen"]);
  try {
    await persistentStore.savePersistedValue("app_frozen", 1, { writeBrowserSnapshotInDesktop: true });
    await persistentStore.savePersistedValueWithBrowserSnapshot("app_frozen", 1, 1, { writeBrowserSnapshotInDesktop: true });
    await persistentStore.deletePersistedValue("app_frozen");
    await secureStore.saveSecurePersistedValue("app_frozen", { a: 1 }, { a: 1 });
    persistentStore.writePersistedBrowserSnapshot("app_frozen", 1);
    await persistentStore.applyPersistedBatch({
      sets: [{ key: "app_frozen", value: 1 }, { key: "app_free", value: 2 }],
      deleteKeys: ["app_frozen"],
    });
    assert.equal(storage.has("app_frozen"), false);
    assert.deepEqual(
      state.calls.map(([command, args]) => [command, args.key ?? args.operations?.map((op) => op.type + ":" + op.key)]),
      [["store_apply_batch", ["set:app_free"]]],
    );
    await persistentStore.applyPersistedBatch({ sets: [{ key: "app_frozen", value: 3 }] }, { ignoreWriteFreeze: true });
    assert.deepEqual(state.calls.at(-1), ["store_apply_batch", { operations: [{ type: "set", key: "app_frozen", value: 3 }] }]);
  } finally {
    lift();
  }
  await persistentStore.savePersistedValue("app_frozen", 4);
  assert.deepEqual(state.calls.at(-1), ["store_set", { key: "app_frozen", value: 4 }]);

  const liftAll = persistentStore.freezePersistedWrites("all");
  const before = state.calls.length;
  try {
    await persistentStore.savePersistedValue("app_anything", 1);
    await persistentStore.deletePersistedValue("app_other");
    await persistentStore.applyPersistedBatch({ sets: [{ key: "app_third", value: 1 }] });
  } finally {
    liftAll();
  }
  assert.equal(state.calls.length, before);
});

test("the SSH and database stores' own native writes check the freeze", async () => {
  const ssh = await readSource("../src/stores/ssh-connections.ts");
  assert.match(ssh, /isPersistedWriteFrozen\(CREDENTIALS_STORAGE_KEY\)[\s\S]*store_del[\s\S]*store_set/);
  const database = await readSource("../src/stores/database-workspace.ts");
  assert.match(database, /isPersistedWriteFrozen\(DATABASE_CONNECTIONS_KEY\)\) return;\n  localStorage\.setItem/);
  assert.match(database, /isPersistedWriteFrozen\(DATABASE_CONNECTIONS_KEY\)\) return;\n  await invokeBackend<void>\("store_set"/);
});

test("applyBackupValues refuses keys that are not MinCode's portable data", async () => {
  const state = reset();
  await assert.rejects(backupHost.applyBackupValues({ app_license: { storage: "plain", value: {} } }, []));
  await assert.rejects(backupHost.applyBackupValues({}, ["app_lock_v1"]));
  assert.equal(state.calls.length, 0);
});

test("applyBackupValues does not write when aborted before the batch", async () => {
  const state = reset();
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(
    backupHost.applyBackupValues({ app_x: { storage: "plain", value: 1 } }, [], { signal: controller.signal }),
    { name: "AbortError" },
  );
  assert.equal(state.calls.filter(([command]) => command === "store_apply_batch").length, 0);
});

// --- Request handling -----------------------------------------------------------

function handlerHarness({ queueSize = 20 } = {}) {
  const replies = [];
  const dialogs = [];
  const handler = backupHost.createBackupRequestHandler({
    reply: async (reply) => void replies.push(reply),
    showDialog: (request) => {
      if (dialogs.length >= queueSize) return false;
      dialogs.push(request);
      return true;
    },
    removeDialog: (id) => {
      const index = dialogs.findIndex((item) => item.id === id);
      if (index >= 0) dialogs.splice(index, 1);
    },
    showRestorePending: () => void replies.push("pending notice"),
    reloadWindow: (ok) => void replies.push(`window reload:${ok}`),
    setTimer: () => () => undefined,
  });
  return { replies, dialogs, handler };
}

test("malformed requests are answered invalid_request; id-less ones are dropped", async () => {
  reset();
  const { replies, handler } = handlerHarness();
  await handler.handle(event("backupDelete", {}));
  await handler.handle({ kind: "backupCollect", payload: { includeSecrets: true } });
  assert.deepEqual(replies, [{ id: "backup-1-1", error: "invalid_request" }]);
});

test("collect replies with the values", async () => {
  reset(storedDevice());
  const { replies, handler } = handlerHarness();
  await handler.handle(event("backupCollect", { includeSecrets: false }));
  assert.equal(replies.length, 1);
  assert.equal(replies[0].id, "backup-1-1");
  assert.ok(replies[0].result.values.app_db_connections);
});

test("collect failures reply with fixed text only", async () => {
  reset({ ...storedDevice(), failOn: "unseal_object" });
  const { replies, handler } = handlerHarness();
  await handler.handle(event("backupCollect", { includeSecrets: true }));
  assert.deepEqual(replies, [{ id: "backup-1-1", error: "collect_failed" }]);
});

test("apply replies ok, and failures reply apply_failed without the error text", async () => {
  reset();
  const { replies, handler } = handlerHarness();
  await handler.handle(event("backupApply", { sets: { app_x: { storage: "plain", value: 1 } }, deletes: [] }));
  assert.deepEqual(replies, [{ id: "backup-1-1", result: { ok: true } }, "pending notice"]);

  reset({ failOn: "store_apply_batch" });
  const failing = handlerHarness();
  await failing.handler.handle(event("backupApply", { sets: { app_x: { storage: "plain", value: 1 } }, deletes: [] }));
  assert.deepEqual(failing.replies, [{ id: "backup-1-1", error: "apply_failed" }]);
});

test("a workspace restore keeps writes frozen and waits for the finished event to reload", async () => {
  const state = reset();
  const log = [];
  const timers = [];
  const handler = backupHost.createBackupRequestHandler({
    reply: async (reply) => void log.push(reply),
    showDialog: () => true,
    removeDialog: () => undefined,
    showRestorePending: () => log.push("pending notice"),
    reloadWindow: (ok) => log.push(`window reload:${ok}`),
    setTimer: (callback, ms) => {
      const timer = { callback, ms, cancelled: false };
      timers.push(timer);
      return () => (timer.cancelled = true);
    },
  });
  await handler.handle(event("backupApply", { sets: { app_workspaces_v1: { storage: "plain", value: [] } }, deletes: [] }));
  // Step 4 answered, no reload yet: Go still applies plugin data and may roll back.
  // The user is told right away that edits made now are not saved.
  assert.deepEqual(log, [{ id: "backup-1-1", result: { ok: true } }, "pending notice"]);
  assert.equal(handler.reloadPending(), true);
  assert.equal(persistentStore.persistedWriteFreezeState().all, true);
  assert.deepEqual(timers.map(({ ms }) => ms), [backupHost.BACKUP_RESTORE_FINISHED_TIMEOUT_MS]);

  // A stale store flush is still skipped...
  await persistentStore.savePersistedValue("app_workspaces_v1", ["stale"]);
  // ...but the rollback (a restore batch of non-live keys) is written, and does not reload by itself.
  await handler.handle(event("backupApply", { sets: { app_workspaces_v1: { storage: "plain", value: ["old"] } }, deletes: ["app_sidebar_order_v2"] }, { id: "backup-1-2" }));
  const writes = state.calls.filter(([command]) => command === "store_apply_batch" || command === "store_set");
  assert.deepEqual(writes.map(([command, args]) => [command, args.operations.map((op) => op.type + ":" + op.key)]), [
    ["store_apply_batch", ["set:app_workspaces_v1"]],
    ["store_apply_batch", ["set:app_workspaces_v1", "delete:app_sidebar_order_v2"]],
  ]);
  assert.deepEqual(log.slice(2), [{ id: "backup-1-2", result: { ok: true } }], "one pending notice for the whole restore");
  assert.equal(timers.length, 1, "one safety net for the whole restore");

  handler.restoreFinished({ restoreId: "r1", ok: true });
  handler.restoreFinished({ restoreId: "r1", ok: false });
  assert.deepEqual(log.filter((entry) => String(entry).startsWith("window reload")), ["window reload:true"]);
  assert.equal(timers[0].cancelled, true);
  assert.equal(handler.reloadPending(), false);
  assert.equal(persistentStore.persistedWriteFreezeState().all, true, "the freeze lasts until the page is gone");
});

test("without a finished event the safety net reloads after 10 minutes, once", async () => {
  reset();
  const reloads = [];
  const timers = [];
  const handler = backupHost.createBackupRequestHandler({
    reply: async () => undefined,
    showDialog: () => true,
    removeDialog: () => undefined,
    showRestorePending: () => undefined,
    reloadWindow: (ok) => reloads.push(`reload:${ok}`),
    setTimer: (callback, ms) => {
      timers.push({ callback, ms });
      return () => undefined;
    },
  });
  assert.equal(backupHost.BACKUP_RESTORE_FINISHED_TIMEOUT_MS, 10 * 60 * 1000);
  await handler.handle(event("backupApply", { sets: { app_settings_v2: { storage: "secure", value: {} } }, deletes: [] }));
  assert.deepEqual(reloads, []);
  timers[0].callback();
  handler.restoreFinished({ ok: true });
  assert.deepEqual(reloads, ["reload:undefined"], "the safety net cannot confirm the outcome");
});

test("a failing step after the written batch still replies ok and keeps the reload pending", async () => {
  const state = reset();
  const { replies, handler } = handlerHarness();
  const dispatch = window.dispatchEvent;
  const warn = console.warn;
  const warnings = [];
  window.dispatchEvent = () => {
    throw new Error("listener blew up with sk-secret");
  };
  console.warn = (message) => warnings.push(message);
  try {
    await handler.handle(event("backupApply", { sets: { app_workspaces_v1: { storage: "plain", value: ["v"] } }, deletes: [] }));
  } finally {
    window.dispatchEvent = dispatch;
    console.warn = warn;
  }
  assert.equal(state.calls.filter(([command]) => command === "store_apply_batch").length, 1);
  assert.deepEqual(replies, [{ id: "backup-1-1", result: { ok: true } }, "pending notice"]);
  assert.equal(handler.reloadPending(), true);
  assert.equal(warnings.length, 1);
  assert.doesNotMatch(warnings[0], /sk-secret|"v"/);
  handler.restoreFinished({ ok: true });
  assert.deepEqual(replies.slice(2), ["window reload:true"]);
});

test("the reload notice follows ok in plugin:backup-restore-finished", async () => {
  assert.equal(backupHost.restoreReloadMessageKey(true), "pluginBackup.restored.reloading");
  assert.equal(backupHost.restoreReloadMessageKey(false), "pluginBackup.restored.undone");
  assert.equal(backupHost.restoreReloadMessageKey(undefined), "pluginBackup.restored.unconfirmed");
  const zh = await readSource("../src/i18n/locales/zh.ts");
  const en = await readSource("../src/i18n/locales/en.ts");
  assert.ok(zh.includes('"pluginBackup.restored.reloading": "已恢复数据，MinCode 即将重新加载"'));
  assert.ok(zh.includes('"pluginBackup.restored.undone": "恢复未完成，已撤回本次恢复，MinCode 即将重新加载"'));
  assert.ok(zh.includes('"pluginBackup.restored.pending": "正在完成恢复，期间的修改不会保存，完成后 MinCode 会自动重新加载"'));
  assert.ok(en.includes('"pluginBackup.restored.reloading": "Data restored. MinCode will reload now."'));
  assert.ok(en.includes(`"pluginBackup.restored.undone": "The restore didn't complete and was undone. MinCode will reload now."`));
  assert.ok(en.includes(`"pluginBackup.restored.pending": "Finishing the restore. Changes made now won't be saved; MinCode will reload when it's done."`));
});

test("the default host shows a persistent pending notice, closes it and reloads with the undone text", async () => {
  const state = reset();
  let reloaded = 0;
  window.location = { reload: () => (reloaded += 1) };
  await backupHost.handleBackupRequest(event("backupApply", { sets: { app_workspaces_v1: { storage: "plain", value: [] } }, deletes: [] }, { id: "backup-9-1" }));
  assert.deepEqual(state.calls.find(([command]) => command === "plugin_host_backup_reply")[1], { id: "backup-9-1", result: { ok: true } });
  const pending = state.messages.find((entry) => entry.message === "pluginBackup.restored.pending");
  assert.ok(pending, "pending notice shown when the reload starts waiting");
  assert.equal(pending.duration, 0);
  assert.equal(pending.showClose, false);
  assert.equal(pending.closed, false);
  backupHost.handleBackupRestoreFinished({ restoreId: "r", ok: false });
  assert.equal(pending.closed, true);
  const undone = state.messages.find((entry) => entry.message === "pluginBackup.restored.undone");
  assert.ok(undone);
  assert.equal(undone.showClose, false);
  assert.equal(reloaded, 0);
  await new Promise((resolve) => setTimeout(resolve, 1100));
  assert.equal(reloaded, 1);
});

test("an SSH-only restore whose store reload hangs unfreezes after the timeout and asks for a restart", async () => {
  reset();
  const stop = restoredEvent.onBackupRestored(["app_ssh_connections_v1"], () => new Promise(() => undefined));
  const notices = [];
  try {
    const outcome = await backupHost.applyBackupValues(
      { app_ssh_connections_v1: { storage: "plain", value: { version: 2, profiles: [] } } },
      [],
      { liveReloadTimeoutMs: 20, onLiveReloadTimeout: () => notices.push("restart") },
    );
    assert.equal(outcome.reloadRequired, false);
  } finally {
    stop();
  }
  assert.equal(backupHost.BACKUP_LIVE_RELOAD_TIMEOUT_MS, 30_000);
  assert.deepEqual(notices, ["restart"]);
  assert.deepEqual(persistentStore.persistedWriteFreezeState(), { all: false, keys: [] });
});

test("a finished event without a pending reload does nothing", async () => {
  reset();
  const { replies, handler } = handlerHarness();
  await handler.handle(event("backupApply", { sets: { app_ssh_connections_v1: { storage: "plain", value: { version: 2, profiles: [] } } }, deletes: [] }));
  handler.restoreFinished();
  assert.deepEqual(replies, [{ id: "backup-1-1", result: { ok: true } }]);
});

test("the host UI bridge forwards plugin:backup-restore-finished to the loaded backup host", async () => {
  const hostUi = await readSource("../src/services/plugin-host-ui.ts");
  assert.match(hostUi, /listenAppEvent\("plugin:backup-restore-finished"[\s\S]*?handleBackupRestoreFinished\(event\)/);
});

test("an SSH-only restore reloads in place without a window reload", async () => {
  reset();
  const { replies, handler } = handlerHarness();
  await handler.handle(event("backupApply", { sets: { app_ssh_connections_v1: { storage: "plain", value: { version: 2, profiles: [] } } }, deletes: ["app_ssh_credentials_v1"] }));
  assert.deepEqual(replies, [{ id: "backup-1-1", result: { ok: true } }]);
  assert.deepEqual(persistentStore.persistedWriteFreezeState(), { all: false, keys: [] });
});

test("a failed native load fails the collect instead of using the browser copy", async () => {
  reset({ ...storedDevice(), failOn: "store_load_all" });
  storage.set("app_sidebar_order_v2", JSON.stringify(["partial"]));
  const { replies, handler } = handlerHarness();
  await handler.handle(event("backupCollect", { includeSecrets: true }));
  assert.deepEqual(replies, [{ id: "backup-1-1", error: "collect_failed" }]);

  reset({ stored: null });
  const malformed = handlerHarness();
  await malformed.handler.handle(event("backupCollect", { includeSecrets: false }));
  assert.deepEqual(malformed.replies, [{ id: "backup-1-1", error: "collect_failed" }]);

  // Other callers keep the browser fallback.
  reset({ failOn: "store_load_all" });
  storage.set("app_sidebar_order_v2", JSON.stringify(["partial"]));
  assert.deepEqual(await persistentStore.loadAllPersistedValues(), { app_sidebar_order_v2: ["partial"] });
});

test("an apply cancelled before its batch starts writes nothing and does not reply", async () => {
  const state = reset();
  const { replies, handler } = handlerHarness();
  // The settings value is sealed first (an async native call) before the batch.
  const running = handler.handle(event("backupApply", { sets: { app_settings_v2: { storage: "secure", value: {} } }, deletes: [] }));
  handler.cancel({ id: "backup-1-1" });
  await running;
  assert.equal(state.calls.filter(([command]) => command === "store_apply_batch").length, 0);
  assert.deepEqual(replies, []);
});

test("an apply cancelled after its batch started still finishes and replies", async () => {
  let release;
  const state = reset({ batchGate: new Promise((resolve) => (release = resolve)) });
  const { replies, handler } = handlerHarness();
  const running = handler.handle(event("backupApply", { sets: { app_x: { storage: "plain", value: 1 } }, deletes: ["app_y"] }));
  while (!state.calls.some(([command]) => command === "store_apply_batch")) await flush();
  handler.cancel({ id: "backup-1-1" });
  release();
  await running;
  assert.equal(state.calls.filter(([command]) => command === "store_apply_batch").length, 1);
  assert.deepEqual(replies, [{ id: "backup-1-1", result: { ok: true } }, "pending notice"]);
});

test("dialogs are queued; answers and cancels close them", async () => {
  reset();
  const { replies, dialogs, handler } = handlerHarness();
  await handler.handle(event("backupPassword", { mode: "set" }));
  await handler.handle(event("backupRestoreConfirm", { mode: "merge", changes: [] }, { id: "backup-1-2" }));
  assert.deepEqual(dialogs.map((item) => item.id), ["backup-1-1", "backup-1-2"]);
  assert.deepEqual(replies, []);

  await handler.answer(dialogs[0], { password: "correct horse battery", remember: true });
  assert.deepEqual(replies, [{ id: "backup-1-1", result: { password: "correct horse battery", remember: true } }]);
  assert.deepEqual(dialogs.map((item) => item.id), ["backup-1-2"]);

  const confirm = dialogs[0];
  handler.cancel({ id: "backup-1-2" });
  assert.deepEqual(dialogs, []);
  await handler.answer(confirm, { confirmed: true });
  assert.equal(replies.length, 1, "a cancelled dialog is never answered");

  await handler.handle(event("backupPassword", { mode: "open" }, { id: "backup-1-3" }));
  await handler.answer(dialogs[0], { cancelled: true });
  await handler.handle(event("backupRestoreConfirm", { mode: "overwrite", changes: [] }, { id: "backup-1-4" }));
  await handler.answer(dialogs[0], { confirmed: false });
  assert.deepEqual(replies.slice(1), [
    { id: "backup-1-3", cancelled: true },
    { id: "backup-1-4", result: { confirmed: false } },
  ]);
});

test("a dialog the host cannot queue is answered as cancelled", async () => {
  reset();
  const { replies, dialogs, handler } = handlerHarness({ queueSize: 1 });
  await handler.handle(event("backupPassword", { mode: "set" }));
  await handler.handle(event("backupPassword", { mode: "set" }));
  assert.deepEqual(replies, [], "a repeated id is the same request");
  await handler.handle(event("backupPassword", { mode: "set" }, { id: "backup-1-2" }));
  assert.deepEqual(dialogs.map((item) => item.id), ["backup-1-1"]);
  assert.deepEqual(replies, [{ id: "backup-1-2", cancelled: true }]);
});

// --- Wiring, lazy loading and copy ------------------------------------------------

test("the host UI layer loads the backup host and its dialogs lazily", async () => {
  const hostUi = await readSource("../src/services/plugin-host-ui.ts");
  assert.match(hostUi, /listenAppEvent\("plugin:backup-request"/);
  assert.match(hostUi, /listenAppEvent\("plugin:backup-request-cancel"/);
  assert.match(hostUi, /import\("@\/services\/backup-host"\)/);
  assert.doesNotMatch(hostUi, /from "@\/services\/backup-host"/);
  const layer = await readSource("../src/components/plugins/PluginHostUiLayer.vue");
  assert.match(layer, /defineAsyncComponent\(\(\) => import\("@\/components\/plugins\/PluginBackupPasswordDialog\.vue"\)\)/);
  assert.match(layer, /defineAsyncComponent\(\(\) => import\("@\/components\/plugins\/PluginBackupRestoreConfirm\.vue"\)\)/);
  assert.doesNotMatch(layer, /^import (?!type )[^\n]*from "@\/services\/backup-host"/m);
  const confirm = await readSource("../src/components/plugins/PluginBackupRestoreConfirm.vue");
  assert.match(confirm, /visibleReviewText\(props\.request\.pluginName\)/);
  assert.doesNotMatch(confirm, /payload\.pluginName/);
});

test("onBackupRestored fires only for the keys a store holds", () => {
  const seen = [];
  const stop = restoredEvent.onBackupRestored(["app_ssh_connections_v1"], () => seen.push("ssh"));
  const dispatch = (keys) => window.dispatchEvent(new CustomEvent(restoredEvent.BACKUP_RESTORED_EVENT, { detail: { keys } }));
  dispatch(["app_settings_v2"]);
  dispatch(["app_settings_v2", "app_ssh_connections_v1"]);
  stop();
  dispatch(["app_ssh_connections_v1"]);
  assert.deepEqual(seen, ["ssh"]);
});

test("the SSH and database stores reload on mincode:backup-restored", async () => {
  const ssh = await readSource("../src/stores/ssh-connections.ts");
  assert.match(ssh, /onBackupRestored\(\[STORAGE_KEY, CREDENTIALS_STORAGE_KEY, GROUP_ORDER_STORAGE_KEY\]/);
  const database = await readSource("../src/stores/database-workspace.ts");
  assert.match(database, /onBackupRestored\(\[DATABASE_CONNECTIONS_KEY, DATABASE_CONNECTION_GROUP_ORDER_KEY\]/);
  // Workspaces and projects cannot reload in place: the host asks for a restart instead.
  const host = await readSource("../src/services/backup-host.ts");
  assert.match(host, /pluginBackup\.restored\.reloading/);
  assert.match(host, /window\.location\.reload\(\)/);
  assert.match(host, /export const handleBackupRestoreFinished = defaultHandler\.restoreFinished/);
});

test("backup capability is red (danger) and disclosed", async () => {
  const { pluginCapabilityTone } = await import("../src/domain/plugins/capability-tone.ts");
  assert.equal(pluginCapabilityTone("backup"), "red");
  const zh = await readSource("../src/i18n/locales/zh.ts");
  const en = await readSource("../src/i18n/locales/en.ts");
  assert.ok(zh.includes('"plugins.capability.backup": "加密导出和恢复 MinCode 全部数据（含可选密钥）"'));
  assert.ok(en.includes('"plugins.capability.backup": "Export and restore all MinCode data, encrypted (secrets optional)"'));
});

test("zh and en define the same pluginBackup keys, including every core section", async () => {
  const zh = await readSource("../src/i18n/locales/zh.ts");
  const en = await readSource("../src/i18n/locales/en.ts");
  const keyPattern = /"(pluginBackup\.[A-Za-z.]+)"/g;
  const zhKeys = [...zh.matchAll(keyPattern)].map((match) => match[1]).sort();
  const enKeys = [...en.matchAll(keyPattern)].map((match) => match[1]).sort();
  assert.ok(zhKeys.length > 20, `${zhKeys.length} keys`);
  assert.deepEqual(zhKeys, enKeys);
  const titles = {
    settings: ["设置、AI 模型与外观", "Settings, AI models and appearance"],
    ssh: ["SSH 连接", "SSH connections"],
    database: ["数据库连接", "Database connections"],
    workspaces: ["工作区与项目", "Workspaces and projects"],
    ai: ["AI 审批与界面偏好", "AI approvals and layout"],
  };
  for (const [section, [zhTitle, enTitle]] of Object.entries(titles)) {
    assert.ok(zh.includes(`"pluginBackup.section.${section}": "${zhTitle}"`), section);
    assert.ok(en.includes(`"pluginBackup.section.${section}": "${enTitle}"`), section);
  }
  for (const kind of ["add", "update", "remove", "keep"]) {
    assert.ok(zhKeys.includes(`pluginBackup.restore.kind.${kind}`), kind);
  }
});

test("a restore that could not be undone names the pre-restore backup", async () => {
  assert.deepEqual(backupHost.restoreReloadMessage(false, { rolledBack: false, preRestoreName: "mincode-backup-20261029T101500Z-0a1b2c3d.mcbak" }), {
    key: "pluginBackup.restored.notUndoneNamed",
    params: { name: "mincode-backup-20261029T101500Z-0a1b2c3d.mcbak" },
  });
  assert.deepEqual(backupHost.restoreReloadMessage(false, { rolledBack: false }), { key: "pluginBackup.restored.notUndone" });
  assert.deepEqual(backupHost.restoreReloadMessage(false, { rolledBack: true, preRestoreName: "x.mcbak" }), { key: "pluginBackup.restored.undone" });
  assert.deepEqual(backupHost.restoreReloadMessage(false), { key: "pluginBackup.restored.undone" });
  assert.deepEqual(backupHost.restoreReloadMessage(true, { rolledBack: false }), { key: "pluginBackup.restored.reloading" });
  // A name that is not plain text is left out.
  assert.deepEqual(backupHost.restoreReloadMessage(false, { rolledBack: false, preRestoreName: "a\nb" }), { key: "pluginBackup.restored.notUndone" });
  const zh = await readSource("../src/i18n/locales/zh.ts");
  const en = await readSource("../src/i18n/locales/en.ts");
  assert.ok(zh.includes('"pluginBackup.restored.notUndoneNamed": "恢复未完成，部分数据可能已改动。请用恢复前备份 {name} 还原。MinCode 即将重新加载"'));
  assert.ok(zh.includes('"pluginBackup.restored.notUndone": "恢复未完成，部分数据可能已改动。请用恢复前备份还原。MinCode 即将重新加载"'));
  assert.ok(en.includes(`"pluginBackup.restored.notUndoneNamed": "The restore didn't complete and some data may have changed. Restore the pre-restore backup {name}. MinCode will reload now."`));
  assert.ok(en.includes(`"pluginBackup.restored.notUndone": "The restore didn't complete and some data may have changed. Restore the pre-restore backup. MinCode will reload now."`));

  // The finished event's fields reach the reload notice.
  reset();
  const reloads = [];
  const handler = backupHost.createBackupRequestHandler({
    reply: async () => undefined,
    showDialog: () => true,
    removeDialog: () => undefined,
    showRestorePending: () => undefined,
    reloadWindow: (ok, detail) => reloads.push([ok, detail]),
    setTimer: () => () => undefined,
  });
  await handler.handle(event("backupApply", { sets: { app_workspaces_v1: { storage: "plain", value: [] } }, deletes: [] }));
  handler.restoreFinished({ restoreId: "r", ok: false, rolledBack: false, preRestoreName: "pre.mcbak" });
  assert.deepEqual(reloads, [[false, { rolledBack: false, preRestoreName: "pre.mcbak" }]]);
  // The user gets time to read (and copy) the name before the reload.
  assert.equal(backupHost.restoreReloadDelayMs(true), 1000);
  assert.ok(backupHost.restoreReloadDelayMs(false, { rolledBack: false }) >= 8000);
});

test("open mode says whether a password exists and does not replace it by default", async () => {
  assert.deepEqual(requests.parseBackupRequest(event("backupPassword", { mode: "open", hasPassword: true })), {
    request: { ...origin, kind: "backupPassword", mode: "open", hasPassword: true },
  });
  // Older hosts send no hasPassword: treated as a password being there (the safe default).
  assert.deepEqual(requests.parseBackupRequest(event("backupPassword", { mode: "open" })).request.hasPassword, true);
  assert.deepEqual(requests.parseBackupRequest(event("backupPassword", { mode: "open", hasPassword: "yes" })), { error: "invalid_request", id: "backup-1-1" });
  const open = (hasPassword) => ({ ...origin, kind: "backupPassword", mode: "open", hasPassword });
  assert.equal(requests.backupPasswordRememberDefault(open(true)), false);
  assert.equal(requests.backupPasswordRememberLabelKey(open(true)), "pluginBackup.password.rememberReplace");
  assert.equal(requests.backupPasswordRememberDefault(open(false)), true);
  assert.equal(requests.backupPasswordRememberLabelKey(open(false)), "pluginBackup.password.remember");
  assert.equal(requests.backupPasswordRememberHintKey(open(true), true), "pluginBackup.password.rememberReplaceHint");
  assert.equal(requests.backupPasswordRememberHintKey(open(true), false), "pluginBackup.password.openOnlyHint");
  assert.equal(requests.backupPasswordRememberHintKey(open(false), true), "pluginBackup.password.rememberHint");
  assert.equal(requests.backupPasswordRememberHintKey(open(false), false), "pluginBackup.password.openOnlyHint");
  const set = { ...origin, kind: "backupPassword", mode: "set" };
  assert.equal(requests.backupPasswordRememberHintKey(set, true), "pluginBackup.password.rememberHint");
  assert.equal(requests.backupPasswordRememberHintKey(set, false), "pluginBackup.password.sessionHint");
  assert.equal(requests.backupPasswordRememberDefault(set), true);
  assert.equal(requests.backupPasswordRememberLabelKey(set), "pluginBackup.password.remember");

  const dialog = await readSource("../src/components/plugins/PluginBackupPasswordDialog.vue");
  assert.match(dialog, /const remember = ref\(backupPasswordRememberDefault\(props\.request\)\)/);
  assert.match(dialog, /t\(backupPasswordRememberLabelKey\(request\)\)/);
  assert.match(dialog, /t\(backupPasswordRememberHintKey\(request, remember\)\)/);
  assert.match(dialog, /v-if="setting"[^>]*>\s*\{\{ t\("pluginBackup\.password\.managerHint"\) \}\}/);
  const zh = await readSource("../src/i18n/locales/zh.ts");
  const en = await readSource("../src/i18n/locales/en.ts");
  assert.ok(zh.includes('"pluginBackup.password.rememberReplace": "以后的备份也改用这个密码"'));
  assert.ok(en.includes('"pluginBackup.password.rememberReplace": "Also use this password for future backups"'));
  assert.ok(zh.includes('"pluginBackup.password.managerHint": "建议用密码管理器生成并保存这个密码"'));
  assert.ok(en.includes('"pluginBackup.password.managerHint": "Generate and keep this password in a password manager."'));
});

test("enabling a plugin that declares backup first shows a separate high-risk dialog", async () => {
  const manager = await readSource("../src/views/PluginManagerWorkspaceView.vue");
  const toggle = manager.slice(manager.indexOf("async function toggle("), manager.indexOf("async function retry("));
  const risk = toggle.indexOf('declaresBackupCapability(plugin) && !(await confirmBackupCapability())');
  assert.ok(risk > 0, "toggle asks about the backup capability");
  assert.ok(risk < toggle.indexOf('plugin.source === "local"'), "before the normal enable confirm");
  assert.match(toggle.slice(risk - 80, risk), /enabled && !previous &&/);
  assert.match(toggle.slice(risk, risk + 200), /plugin\.enabled = previous;\s*return;/, "cancel aborts enabling");
  const afterInstall = manager.slice(manager.indexOf("async function enableAfterInstall("), manager.indexOf("async function upgrade("));
  const askedAfterInstall = afterInstall.indexOf('declaresBackupCapability(plugin) && !(await confirmBackupCapability())');
  assert.ok(askedAfterInstall > 0 && askedAfterInstall < afterInstall.indexOf("setPluginEnabled("), "a fresh install asks before enabling too");
  const helper = manager.slice(manager.indexOf("function declaresBackupCapability("), manager.indexOf("async function toggle("));
  assert.match(helper, /Array\.isArray\(plugin\.capabilities\) && plugin\.capabilities\.includes\("backup"\)/, "a missing capabilities list means no backup capability");
  const dialog = manager.slice(manager.indexOf("async function confirmBackupCapability("));
  assert.match(dialog, /ElMessageBox\.confirm\(t\("plugins\.backupRisk\.body"\), t\("plugins\.backupRisk\.title"\), \{\s*type: "warning"/);
  const zh = await readSource("../src/i18n/locales/zh.ts");
  const en = await readSource("../src/i18n/locales/en.ts");
  assert.ok(zh.includes('"plugins.backupRisk.title": "高风险权限：数据备份"'));
  assert.ok(en.includes('"plugins.backupRisk.title": "High-risk permission: data backup"'));
  for (const text of [zh, en]) assert.match(text, /"plugins\.backupRisk\.body": "[^"]+"/);
});

test("the host UI layer mounts for a queued backup dialog, not only for prompts and diffs", async () => {
  const app = await readFile(new URL("../src/App.vue", import.meta.url), "utf8");
  const mount = app.match(/<PluginHostUiLayer v-if="([^"]+)"/);
  assert.ok(mount, "App.vue mounts PluginHostUiLayer conditionally");
  assert.match(mount[1], /pluginHostUi\.backupDialogs\.length/, "a backup password or restore dialog must mount the layer");
});
