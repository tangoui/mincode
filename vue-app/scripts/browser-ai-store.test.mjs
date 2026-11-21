import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";

// Browser-local `ai_store_*`: IndexedDB replacement of backend/aistore. The handlers run
// against an in-memory fake of the small transaction interface in services/browser-runtime/idb.ts;
// inputs and outputs must keep the Go command shapes used by services/ai-conversation-persistence.ts.

const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/dev-performance") {
      return { url: dataModule("export const measureDevAsync = (_name, run) => run();"), shortCircuit: true };
    }
    return nextResolve(specifier, context);
  },
});

/** Minimal Web Storage for localStorage / sessionStorage. */
function memoryStorage() {
  const values = new Map();
  return {
    getItem: (key) => (values.has(key) ? values.get(key) : null),
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: (key) => values.delete(key),
    clear: () => values.clear(),
    key: (index) => [...values.keys()][index] ?? null,
    get length() {
      return values.size;
    },
  };
}
// Browser-local mode: no window.tango; the run registry dispatches window events.
globalThis.window = Object.assign(new EventTarget(), { localStorage: memoryStorage(), sessionStorage: memoryStorage() });
globalThis.localStorage = globalThis.window.localStorage;
globalThis.sessionStorage = globalThis.window.sessionStorage;

const { AI_STORE_SCHEMA, __setAiStoreDatabaseForTests } = await import("../src/services/browser-runtime/ai-store.ts");
await import("../src/services/browser-runtime/agent-runs.ts");
const { getBrowserHandler } = await import("../src/services/browser-runtime/registry.ts");
const { isItemPatchConflict } = await import("../src/domain/ai/conversation-item-sync.ts");

/** In-memory stand-in for the IndexedDB wrapper: structured-clone values, all-or-nothing transactions. */
function memoryDatabase() {
  const stores = new Map(Object.keys(AI_STORE_SCHEMA).map((name) => [name, new Map()]));
  const keyFor = (store, value) => {
    const path = AI_STORE_SCHEMA[store];
    return Array.isArray(path) ? path.map((field) => value[field]) : value[path];
  };
  let transactions = 0;
  return {
    stores,
    transactionCount: () => transactions,
    async transaction(names, mode, run) {
      transactions += 1;
      const snapshot = new Map([...stores].map(([name, records]) => [name, new Map(records)]));
      const use = (store) => {
        assert.ok(names.includes(store), `store ${store} is not part of the transaction`);
        return stores.get(store);
      };
      const writable = () => assert.equal(mode, "readwrite", "write in a readonly transaction");
      const tx = {
        async get(store, key) {
          const value = use(store).get(JSON.stringify(key));
          return value === undefined ? undefined : structuredClone(value);
        },
        async getAll(store, scopeId) {
          return [...use(store).entries()]
            .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
            .map(([, value]) => value)
            .filter((value) => scopeId === undefined || keyFor(store, value)[0] === scopeId)
            .map((value) => structuredClone(value));
        },
        async put(store, value) {
          writable();
          use(store).set(JSON.stringify(keyFor(store, value)), structuredClone(value));
        },
        async delete(store, key) {
          writable();
          use(store).delete(JSON.stringify(key));
        },
      };
      try {
        return await run(tx);
      } catch (error) {
        for (const [name, records] of snapshot) stores.set(name, records);
        throw error;
      }
    },
  };
}

let database;
function reset() {
  database = memoryDatabase();
  __setAiStoreDatabaseForTests(database);
}

const call = (command, args = {}) => getBrowserHandler(command)(args);

function conversation(id, overrides = {}) {
  return {
    id,
    title: `Title ${id}`,
    workspaceId: "w1",
    messages: [{ id: `${id}-m1`, role: "user", content: "Hello World", createdAt: 1000 }],
    agentHistory: [{ role: "user", content: "Hello World" }],
    agentEvents: [],
    messageQueue: [],
    ...overrides,
  };
}

test("registers the AI store handlers and the AI-only store_apply_batch at import time", () => {
  for (const command of [
    "ai_store_load_workspace",
    "ai_store_list_workspaces",
    "ai_store_load_conversation",
    "ai_store_save_conversation",
    "ai_store_save_workspace_meta",
    "ai_store_delete_conversation",
    "ai_store_replace_conversation",
    "ai_store_move_conversation",
    "ai_store_search_messages",
    "store_apply_batch",
  ]) {
    assert.equal(typeof getBrowserHandler(command), "function", command);
  }
});

test("save then load: the conversation comes back in full with detailsLoaded, never aliasing the input", async () => {
  reset();
  const input = conversation("c1", { detailsLoaded: true, transient: () => "not JSON" });
  const result = await call("ai_store_save_conversation", { scopeId: "s1", conversation: input, position: 0, replaceItems: true });
  assert.deepEqual(result, {}, "no itemRevision: the browser store always takes full saves");
  input.messages[0].content = "mutated after save";

  const loaded = await call("ai_store_load_conversation", { scopeId: "s1", conversationId: "c1" });
  assert.equal(loaded.detailsLoaded, true);
  assert.equal(loaded.transient, undefined, "values are stored like JSON");
  assert.deepEqual(loaded.messages, [{ id: "c1-m1", role: "user", content: "Hello World", createdAt: 1000 }]);
  assert.deepEqual(loaded.agentHistory, [{ role: "user", content: "Hello World" }]);
  assert.equal(loaded.agentEvents, null, "empty item groups load as null, like Go");
  assert.equal(loaded.messageQueue, null);
  assert.equal(await call("ai_store_load_conversation", { scopeId: "s1", conversationId: "missing" }), null);
  assert.equal(await call("ai_store_load_conversation", { scopeId: "other", conversationId: "c1" }), null);
});

test("full saves drop attachment previewUrl like Go's blob externalization; metadata-only saves keep the items", async () => {
  reset();
  const attachment = { id: "a1", name: "shot.png", mimeType: "image/png", size: 10, kind: "image", data: "iVBORw0KGgo=", previewUrl: "blob:x" };
  await call("ai_store_save_conversation", {
    scopeId: "s1",
    conversation: conversation("c1", { messages: [{ id: "m", role: "user", content: "see", attachments: [attachment] }] }),
    position: 0,
    replaceItems: true,
  });
  let loaded = await call("ai_store_load_conversation", { scopeId: "s1", conversationId: "c1" });
  const { previewUrl: _previewUrl, ...withoutPreview } = attachment;
  assert.deepEqual(loaded.messages[0].attachments, [withoutPreview]);

  await call("ai_store_save_conversation", {
    scopeId: "s1",
    conversation: { id: "c1", title: "Renamed", messages: [], agentHistory: [], agentEvents: [], messageQueue: [] },
    position: 0,
    replaceItems: false,
  });
  loaded = await call("ai_store_load_conversation", { scopeId: "s1", conversationId: "c1" });
  assert.equal(loaded.title, "Renamed");
  assert.equal(loaded.messages.length, 1, "replaceItems=false leaves the stored items alone");
});

test("workspace overview lists conversations by position with item groups emptied", async () => {
  reset();
  assert.equal(await call("ai_store_load_workspace", { scopeId: "s1" }), null);
  await call("ai_store_save_conversation", { scopeId: "s1", conversation: conversation("b"), position: 1, replaceItems: true });
  await call("ai_store_save_conversation", { scopeId: "s1", conversation: conversation("a"), position: 0, replaceItems: true });
  const overview = await call("ai_store_load_workspace", { scopeId: "s1" });
  assert.equal(overview.version, 2);
  assert.equal(overview.activeConversationId, "");
  assert.deepEqual(overview.conversations.map((item) => item.id), ["a", "b"]);
  for (const item of overview.conversations) {
    assert.deepEqual(item.messages, []);
    assert.deepEqual(item.agentHistory, []);
    assert.deepEqual(item.agentEvents, []);
    assert.deepEqual(item.messageQueue, []);
    assert.equal(item.detailsLoaded, false);
    assert.equal(item.title, `Title ${item.id}`);
  }
});

test("workspace meta sets the active conversation and reorders; listing puts the latest workspace first", async () => {
  reset();
  await call("ai_store_save_conversation", { scopeId: "s1", conversation: conversation("a"), position: 0, replaceItems: true });
  await call("ai_store_save_conversation", { scopeId: "s1", conversation: conversation("b"), position: 1, replaceItems: true });
  await call("ai_store_save_conversation", { scopeId: "s2", conversation: conversation("z"), position: 0, replaceItems: true });
  assert.equal(await call("ai_store_save_workspace_meta", { scopeId: "s2", activeConversationId: "z", conversationIds: ["z"] }), null);
  await call("ai_store_save_workspace_meta", { scopeId: "s1", activeConversationId: "b", conversationIds: ["b", "", "a"] });

  const overview = await call("ai_store_load_workspace", { scopeId: "s1" });
  assert.equal(overview.activeConversationId, "b");
  assert.deepEqual(overview.conversations.map((item) => item.id), ["b", "a"]);

  const listed = await call("ai_store_list_workspaces");
  assert.deepEqual(listed.map((item) => item.scopeId), ["s1", "s2"]);
  assert.equal(listed[0].collection.version, 2);
  assert.equal(listed[0].collection.activeConversationId, "b");
  assert.deepEqual(listed[0].collection.conversations.map((item) => [item.id, item.detailsLoaded, item.messages.length]), [["b", false, 0], ["a", false, 0]]);
});

test("delete removes the conversation and its items, then applies the active id and order", async () => {
  reset();
  for (const [index, id] of ["a", "b", "c"].entries()) {
    await call("ai_store_save_conversation", { scopeId: "s1", conversation: conversation(id), position: index, replaceItems: true });
  }
  assert.equal(await call("ai_store_delete_conversation", { scopeId: "s1", conversationId: "b", activeConversationId: "c", conversationIds: ["c", "a"] }), null);
  const overview = await call("ai_store_load_workspace", { scopeId: "s1" });
  assert.equal(overview.activeConversationId, "c");
  assert.deepEqual(overview.conversations.map((item) => item.id), ["c", "a"]);
  assert.equal(await call("ai_store_load_conversation", { scopeId: "s1", conversationId: "b" }), null);
  assert.equal([...database.stores.get("items").keys()].some((key) => key.includes('"b"')), false, "items are deleted too");
});

test("replace swaps a conversation atomically, puts the new one first and makes it active", async () => {
  reset();
  await call("ai_store_save_conversation", { scopeId: "s1", conversation: conversation("old"), position: 0, replaceItems: true });
  await call("ai_store_save_conversation", { scopeId: "s1", conversation: conversation("keep"), position: 1, replaceItems: true });
  await call("ai_store_replace_conversation", { scopeId: "s1", conversationId: "old", conversation: conversation("new") });
  const overview = await call("ai_store_load_workspace", { scopeId: "s1" });
  assert.equal(overview.activeConversationId, "new");
  assert.deepEqual(overview.conversations.map((item) => item.id), ["new", "keep"]);
  assert.equal(await call("ai_store_load_conversation", { scopeId: "s1", conversationId: "old" }), null);
});

test("archives and removes a family across workspaces while preserving other conversations", async () => {
  reset();
  for (const [scopeId, id] of [["one", "parent"], ["two", "child"], ["two", "keep"]]) {
    await call("ai_store_save_conversation", { scopeId, conversation: conversation(id), position: 0, replaceItems: true });
  }
  const child = { scopeId: "two", conversationId: "child" };
  await call("ai_store_save_workspace_meta", { scopeId: "two", activeConversationId: "child", conversationIds: ["child", "keep"] });
  await call("ai_store_save_workspace_meta", {
    archive: { conversations: [{ scopeId: "one", conversationId: "parent" }, child], archived: true },
  });
  const saved = await call("ai_store_load_conversation", child);
  assert.equal(saved.archived, true);
  assert.equal(saved.messages.length, 1);
  await call("ai_store_replace_conversation", {
    scopeId: "one", conversationId: "parent", conversation: conversation("new"), relatedConversations: [child],
  });
  const overview = await call("ai_store_load_workspace", { scopeId: "two" });
  assert.equal(overview.activeConversationId, "keep");
  assert.deepEqual(overview.conversations.map((item) => item.id), ["keep"]);
  assert.equal(await call("ai_store_load_conversation", child), null);
});

test("move transfers a conversation between workspaces with Go's ordering and active ids", async () => {
  reset();
  await call("ai_store_save_conversation", { scopeId: "src", conversation: conversation("a"), position: 0, replaceItems: true });
  await call("ai_store_save_conversation", { scopeId: "src", conversation: conversation("b"), position: 1, replaceItems: true });
  await call("ai_store_save_conversation", { scopeId: "dst", conversation: conversation("x"), position: 0, replaceItems: true });
  const moved = conversation("a", { title: "Moved" });
  assert.equal(await call("ai_store_move_conversation", {
    sourceScopeId: "src",
    targetScopeId: "dst",
    conversationId: "a",
    conversation: moved,
    sourceActiveConversationId: "b",
    sourceConversationIds: ["b"],
    targetConversationIds: ["x"],
  }), null);
  const source = await call("ai_store_load_workspace", { scopeId: "src" });
  assert.equal(source.activeConversationId, "b");
  assert.deepEqual(source.conversations.map((item) => item.id), ["b"]);
  const target = await call("ai_store_load_workspace", { scopeId: "dst" });
  assert.equal(target.activeConversationId, "a");
  assert.deepEqual(target.conversations.map((item) => [item.id, item.title]), [["a", "Moved"], ["x", "Title x"]]);
  assert.equal((await call("ai_store_load_conversation", { scopeId: "dst", conversationId: "a" })).messages.length, 1);
});

test("a failed move changes nothing (one transaction)", async () => {
  reset();
  await call("ai_store_save_conversation", { scopeId: "src", conversation: conversation("a"), position: 0, replaceItems: true });
  await call("ai_store_save_conversation", { scopeId: "dst", conversation: conversation("a"), position: 0, replaceItems: true });
  const before = JSON.stringify([...database.stores].map(([name, records]) => [name, [...records]]));
  await assert.rejects(call("ai_store_move_conversation", {
    sourceScopeId: "src",
    targetScopeId: "dst",
    conversationId: "a",
    conversation: conversation("a"),
    sourceActiveConversationId: "",
    sourceConversationIds: [],
    targetConversationIds: ["a"],
  }), /目标工作区已存在同 ID 的 AI 会话/);
  assert.equal(JSON.stringify([...database.stores].map(([name, records]) => [name, [...records]])), before);
  await assert.rejects(call("ai_store_move_conversation", {
    sourceScopeId: "src", targetScopeId: "src", conversationId: "a", conversation: conversation("a"),
  }), /AI 会话迁移参数无效/);
});

test("search finds message text case-insensitively, with title, snippet and scope filter", async () => {
  reset();
  const long = `${"前".repeat(80)}Needle${"后".repeat(80)}`;
  await call("ai_store_save_conversation", {
    scopeId: "s1",
    conversation: conversation("c1", {
      messages: [
        { id: "m1", role: "user", content: "first NEEDLE here", createdAt: 5 },
        { id: "m2", role: "assistant", content: "needle failed", presentation: "error" },
        { id: "m3", role: "assistant", content: long },
        { role: "user", content: "needle without id" },
      ],
    }),
    position: 0,
    replaceItems: true,
  });
  await call("ai_store_save_conversation", {
    scopeId: "s2",
    conversation: conversation("c2", { messages: [{ id: "n1", role: "user", content: "another needle\n\tline" }] }),
    position: 0,
    replaceItems: true,
  });

  const all = await call("ai_store_search_messages", { query: " needle ", scopeIds: [] });
  assert.deepEqual(all.map((match) => [match.scopeId, match.conversationId, match.messageId]), [
    ["s2", "c2", "n1"],
    ["s1", "c1", "m1"],
    ["s1", "c1", "m3"],
  ], "newest conversation first, error cards and id-less messages skipped");
  assert.deepEqual(all[1], {
    scopeId: "s1",
    conversationId: "c1",
    title: "Title c1",
    messageId: "m1",
    role: "user",
    snippet: "first NEEDLE here",
    createdAt: 5,
  });
  assert.equal(all[0].snippet, "another needle line", "whitespace collapses to one line");
  assert.equal("createdAt" in all[0], false, "a missing createdAt is omitted like Go's omitempty");
  assert.equal(all[2].snippet, `…${"前".repeat(60)}Needle${"后".repeat(60)}…`);

  const scoped = await call("ai_store_search_messages", { query: "needle", scopeIds: ["s1"] });
  assert.deepEqual(scoped.map((match) => match.messageId), ["m1", "m3"]);
  assert.equal((await call("ai_store_search_messages", { query: "needle", limit: 1 })).length, 1);
  await assert.rejects(call("ai_store_search_messages", { query: "   " }), /AI 消息搜索关键字为空或过长/);
  await assert.rejects(call("ai_store_search_messages", { query: "x".repeat(201) }), /AI 消息搜索关键字为空或过长/);
});

test("validation errors match Go and item patches are always answered with the conflict error", async () => {
  reset();
  await assert.rejects(call("ai_store_load_workspace", { scopeId: " " }), /AI 存储 scopeId 不能为空/);
  await assert.rejects(call("ai_store_load_conversation", { scopeId: "s", conversationId: "" }), /AI 会话定位参数不能为空/);
  await assert.rejects(call("ai_store_save_conversation", { scopeId: "s", conversation: null }), /AI 会话保存参数无效/);
  await assert.rejects(call("ai_store_save_conversation", { scopeId: "s", conversation: [] }), /JSON 根节点不是对象/);
  await assert.rejects(call("ai_store_save_conversation", { scopeId: "s", conversation: { title: "x" } }), /AI 会话 id 不能为空/);
  await assert.rejects(call("ai_store_save_workspace_meta", { scopeId: "" }), /AI 存储 scopeId 不能为空/);
  await assert.rejects(call("ai_store_replace_conversation", { scopeId: "s", conversationId: "a" }), /AI 会话原子替换参数无效/);
  await assert.rejects(
    call("ai_store_save_conversation", { scopeId: "s", conversation: { id: "a" }, itemPatch: { baseRevision: 1, groups: {} } }),
    /AI 会话增量保存必须同时保存条目/,
  );
  await assert.rejects(
    call("ai_store_save_conversation", { scopeId: "s", conversation: { id: "a" }, replaceItems: true, itemPatch: { baseRevision: 1, groups: {} } }),
    (error) => isItemPatchConflict(error),
  );
  assert.equal(await call("ai_store_load_conversation", { scopeId: "s", conversationId: "a" }), null, "a rejected patch writes nothing");
});

test("ai-conversation-persistence reaches the browser store through invokeBackend in browser-local mode", async () => {
  reset();
  const persistence = await import("../src/services/ai-conversation-persistence.ts");
  const first = conversation("c1");
  await persistence.saveAiConversationDetail("s1", first, 0, true);
  await persistence.saveAiConversationDetail("s1", { ...first, messages: [...first.messages, { id: "m2", role: "assistant", content: "Hi" }] }, 0, true);
  await persistence.saveAiWorkspaceConversationMeta("s1", "c1", ["c1"]);

  const detail = await persistence.loadAiConversationDetail("s1", "c1");
  assert.deepEqual(detail.messages.map((message) => message.id), ["c1-m1", "m2"]);
  const overview = await persistence.loadAiWorkspaceOverview("s1");
  assert.equal(overview.activeConversationId, "c1");
  assert.deepEqual((await persistence.listAiWorkspaceOverviews()).map((item) => item.scopeId), ["s1"]);
  assert.deepEqual((await persistence.searchAiConversationMessages("hi")).map((match) => match.messageId), ["m2"]);

  await persistence.deleteAiConversation("s1", "c1", "", []);
  assert.equal(await persistence.loadAiConversationDetail("s1", "c1"), null);
});

test("clearing all AI history also empties the browser store", async () => {
  reset();
  const persistence = await import("../src/services/ai-conversation-persistence.ts");
  await call("ai_store_save_conversation", { scopeId: "s1", conversation: conversation("a"), position: 0, replaceItems: true });
  await call("ai_store_save_conversation", { scopeId: "s2", conversation: conversation("b"), position: 0, replaceItems: true });
  await persistence.clearAllAiConversationHistory();
  assert.equal(database.stores.get("conversations").size, 0);
  assert.equal(database.stores.get("items").size, 0);
  assert.deepEqual(await call("ai_store_search_messages", { query: "hello" }), []);
});

test("permanently deleting a workspace removes its browser AI history and workspace record", async () => {
  reset();
  const { deleteWorkspaceScopePersistence } = await import("../src/services/workspace-state-deletion.ts");
  const { isWorkspaceScopePermanentlyDeleted } = await import("../src/services/workspace-deleted-scopes.ts");
  await call("ai_store_save_conversation", { scopeId: "gone", conversation: conversation("a"), position: 0, replaceItems: true });
  await call("ai_store_save_conversation", { scopeId: "gone", conversation: conversation("b"), position: 1, replaceItems: true });
  await call("ai_store_save_workspace_meta", { scopeId: "gone", activeConversationId: "a", conversationIds: ["a", "b"] });
  await call("ai_store_save_conversation", { scopeId: "kept", conversation: conversation("c"), position: 0, replaceItems: true });

  await deleteWorkspaceScopePersistence(" gone ");

  assert.equal(isWorkspaceScopePermanentlyDeleted("gone"), true);
  assert.equal(await call("ai_store_load_workspace", { scopeId: "gone" }), null);
  assert.equal(await call("ai_store_load_conversation", { scopeId: "gone", conversationId: "a" }), null);
  assert.deepEqual((await call("ai_store_list_workspaces")).map((item) => item.scopeId), ["kept"]);
  assert.deepEqual((await call("ai_store_search_messages", { query: "hello" })).map((match) => match.scopeId), ["kept"]);
  assert.deepEqual([...database.stores.get("workspaces").keys()], [JSON.stringify("kept")]);
  assert.equal(database.stores.get("conversations").size, 1);
  assert.equal(database.stores.get("items").size, 1);
});

test("browser store_apply_batch accepts only delete_ai_scope and changes nothing otherwise", async () => {
  reset();
  await call("ai_store_save_conversation", { scopeId: "s1", conversation: conversation("a"), position: 0, replaceItems: true });
  for (const operation of [
    { type: "set", key: "app_settings_v2", value: {} },
    { type: "delete", key: "app_settings_v2" },
    { type: "delete_all_ai", key: "*" },
  ]) {
    await assert.rejects(
      call("store_apply_batch", { operations: [{ type: "delete_ai_scope", key: "s1" }, operation] }),
      /^Error: 不支持的应用配置批处理操作/,
    );
  }
  assert.notEqual(await call("ai_store_load_workspace", { scopeId: "s1" }), null);
  assert.equal(await call("store_apply_batch", { operations: [] }), null);
  assert.equal(await call("store_apply_batch", { operations: [{ type: "delete_ai_scope", key: "s1" }] }), null);
  assert.equal(await call("ai_store_load_workspace", { scopeId: "s1" }), null);
});

// ── API keys: this tab's sessionStorage only ─────────────────────────────────

test("browser-local settings keep AI keys in sessionStorage and never in localStorage", async () => {
  const { BROWSER_AI_KEYS_STORAGE_KEY, browserAiKey, setBrowserAiKey } = await import("../src/services/browser-ai-keys.ts");
  assert.equal(BROWSER_AI_KEYS_STORAGE_KEY, "mincode_browser_ai_keys_v1");
  const legacyProfile = {
    id: "p1",
    provider: "plugin",
    source: { pluginId: "demo.providers", providerId: "local" },
    baseUrl: "https://example.test/v1",
    model: "local-model",
    noToolModels: ["local-model"],
    apiKey: "",
  };
  localStorage.setItem("app_settings_v2", JSON.stringify({ aiProfiles: [legacyProfile, { id: "p2", apiKey: "" }] }));
  sessionStorage.setItem(BROWSER_AI_KEYS_STORAGE_KEY, JSON.stringify({ p1: "sk-session" }));

  const settings = await import("../src/services/settings-persistence.ts");
  assert.equal(settings.readSettingsSnapshot().aiProfiles[0].apiKey, "sk-session", "read before hydration");
  const hydrated = await settings.hydrateSettingsSnapshot();
  assert.deepEqual(hydrated.aiProfiles.map((profile) => profile.apiKey), ["sk-session", ""]);
  assert.deepEqual(hydrated.aiProfiles[0], {
    id: "p1",
    provider: "custom",
    baseUrl: "https://example.test/v1",
    model: "local-model",
    noToolModels: ["local-model"],
    apiKey: "sk-session",
  });

  await settings.persistSettingsSnapshot({ aiProfiles: [{ id: "p1", apiKey: "" }, { id: "p2", apiKey: "sk-two" }] });
  assert.deepEqual(
    JSON.parse(localStorage.getItem("app_settings_v2")).aiProfiles.map((profile) => profile.apiKey),
    ["", ""],
    "the localStorage snapshot stays redacted",
  );
  assert.equal(localStorage.getItem(BROWSER_AI_KEYS_STORAGE_KEY), null);
  assert.deepEqual(JSON.parse(sessionStorage.getItem(BROWSER_AI_KEYS_STORAGE_KEY)), { p2: "sk-two" }, "cleared and removed keys are dropped");
  assert.equal(settings.readSettingsSnapshot().aiProfiles[1].apiKey, "sk-two");

  assert.equal(browserAiKey("p2"), "sk-two");
  setBrowserAiKey("p3", "sk-three");
  assert.equal(browserAiKey("p3"), "sk-three");
  setBrowserAiKey("p3", "");
  assert.equal(browserAiKey("p3"), "");
  sessionStorage.setItem(BROWSER_AI_KEYS_STORAGE_KEY, "{not json");
  assert.equal(browserAiKey("p2"), "", "a corrupt entry reads as no keys");
});

// ── agent runs: in-memory registry ───────────────────────────────────────────

test("agent runs: start, update and settle like Go's registry, publishing progress as window events", async () => {
  const progress = [];
  const onProgress = (event) => progress.push(event.detail);
  window.addEventListener("agent-run:progress", onProgress);
  try {
    const run = await call("agent_run_start", {
      sessionId: "c1",
      workspaceId: "w1",
      workspaceScopeId: "s1",
      source: "agentic",
      title: "  Ask\nabout   api_key=sk-secret  ",
      phase: "thinking",
      summary: "Starting",
      navigation: { type: "ai-conversation", conversationId: "c1" },
    });
    assert.match(run.runId, /^run-[0-9a-f]{32}$/);
    assert.equal(run.title, "Ask about api_key=[REDACTED]");
    assert.equal(run.revision, 1);
    assert.equal(run.progress.phase, "thinking");
    assert.deepEqual(run.navigation, { type: "ai-conversation", conversationId: "c1" });

    const streaming = await call("agent_run_update", { runId: run.runId, phase: "streaming", summary: "Writing", stepsCompleted: 5, stepsTotal: 2 });
    assert.equal(streaming.revision, 2);
    assert.equal(streaming.progress.summary, "Writing");
    assert.deepEqual([streaming.progress.stepsCompleted, streaming.progress.stepsTotal], [2, 2]);
    const done = await call("agent_run_update", { runId: run.runId, phase: "completed" });
    assert.equal(done.progress.phase, "completed");
    assert.equal(typeof done.progress.completedAt, "number");
    assert.equal(done.progress.stepsTotal, undefined, "steps omitted on an update are cleared");
    const late = await call("agent_run_update", { runId: run.runId, phase: "streaming" });
    assert.equal(late.progress.phase, "completed", "a finished run no longer changes");

    assert.deepEqual(progress.map((item) => item.progress.phase), ["thinking", "streaming", "completed"]);
    assert.ok((await call("agent_run_list")).some((item) => item.runId === run.runId));
    assert.equal(await call("agent_run_remove", { runId: run.runId }), null);
    assert.equal((await call("agent_run_list")).some((item) => item.runId === run.runId), false);

    await assert.rejects(call("agent_run_start", { sessionId: "c", workspaceId: "w", workspaceScopeId: "s", source: "x", navigation: { type: "ai-conversation", conversationId: "c" } }), /unsupported agent run source/);
    await assert.rejects(call("agent_run_start", { sessionId: "c", workspaceId: "w", workspaceScopeId: "s", source: "agentic", phase: "completed", navigation: { type: "ai-conversation", conversationId: "c" } }), /invalid initial agent run phase/);
    await assert.rejects(call("agent_run_update", { runId: "missing", phase: "thinking" }), /agent run not found/);
  } finally {
    window.removeEventListener("agent-run:progress", onProgress);
  }
});

test("agent runs: a cancel request reaches the owner through a window event and cannot remove an active run", async () => {
  const requests = [];
  const onCancel = (event) => requests.push(event.detail);
  window.addEventListener("agent-run:cancel-requested", onCancel);
  try {
    const run = await call("agent_run_start", {
      sessionId: "c2", workspaceId: "w1", workspaceScopeId: "s1", source: "agentic", title: "t",
      navigation: { type: "ai-conversation", conversationId: "c2" },
    });
    assert.equal(run.progress.phase, "queued");
    await assert.rejects(call("agent_run_remove", { runId: run.runId }), /active agent run cannot be removed/);
    const cancelling = await call("agent_run_request_cancel", { runId: run.runId });
    assert.equal(cancelling.progress.phase, "cancelling");
    assert.equal(cancelling.progress.cancelRequested, true);
    assert.deepEqual(requests, [{ runId: run.runId, sessionId: "c2" }]);
    const cancelled = await call("agent_run_update", { runId: run.runId, phase: "cancelled" });
    assert.equal(cancelled.progress.phase, "cancelled");
  } finally {
    window.removeEventListener("agent-run:cancel-requested", onCancel);
  }
});

test("session-progress-service listens to the browser registry's window events in browser-local mode", async () => {
  const service = await import("../src/services/session-progress-service.ts");
  const runs = [];
  const cancels = [];
  const stopProgress = service.listenAgentRunProgress((run) => runs.push(run.runId));
  const stopCancel = service.listenAgentRunCancelRequested((payload) => cancels.push(payload));
  const run = await service.startAgentRun({
    sessionId: "c3", workspaceId: "w1", workspaceScopeId: "s1", source: "agentic", title: "t", phase: "thinking",
    navigation: { type: "ai-conversation", conversationId: "c3" },
  });
  await service.requestAgentRunCancel(run.runId);
  assert.deepEqual(runs, [run.runId, run.runId]);
  assert.deepEqual(cancels, [{ runId: run.runId, sessionId: "c3" }]);
  assert.ok((await service.listAgentRuns()).some((item) => item.runId === run.runId));
  stopProgress();
  stopCancel();
  await service.updateAgentRun({ runId: run.runId, phase: "cancelled" });
  assert.equal(runs.length, 2, "unsubscribed listeners get nothing");
});
