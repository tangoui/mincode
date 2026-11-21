import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import test from "node:test";
import { createPinia, setActivePinia } from "pinia";

// The notification listener starts before the async hydrate() finishes; anything pushed in
// that window must survive hydration and be persisted together with the stored history.
const persistentStoreStub = `
  const state = () => globalThis.__notificationsHydrateTest;
  export const loadPersistedValue = (key) => state().load(key);
  export const savePersistedValue = async (key, value) => {
    state().saves.push({ key, value: JSON.parse(JSON.stringify(value)) });
  };
`;
const desktopBridgeStub = `
  export const isDesktopRuntime = () => false;
  export const listenAppEvent = () => () => undefined;
`;
const i18nStub = `
  export const useVueI18n = () => ({ t: (key) => key });
`;
const dataModule = (source) => `data:text/javascript,${encodeURIComponent(source)}`;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@/services/persistent-store") return { url: dataModule(persistentStoreStub), shortCircuit: true };
    if (specifier === "@/services/desktop-bridge") return { url: dataModule(desktopBridgeStub), shortCircuit: true };
    if (specifier === "@/i18n/messages") return { url: dataModule(i18nStub), shortCircuit: true };
    return nextResolve(specifier, context);
  },
});
const { useNotificationsStore } = await import("../src/stores/notifications.ts");

function stored(id, updatedAt, extra = {}) {
  return { id, source: "system", level: "info", title: `stored ${id}`, createdAt: updatedAt, updatedAt, ...extra };
}

function setup() {
  setActivePinia(createPinia());
  let resolveLoad;
  const state = {
    saves: [],
    loads: [],
    load: (key) => {
      state.loads.push(key);
      return new Promise((resolve) => {
        resolveLoad = resolve;
      });
    },
  };
  globalThis.__notificationsHydrateTest = state;
  return { state, store: useNotificationsStore(), resolveLoad: (value) => resolveLoad(value) };
}

test("hydrate keeps notifications pushed while loading and persists the merged list", async () => {
  const { state, store, resolveLoad } = setup();
  const hydrating = store.hydrate();
  const early = store.push({ source: "ssh", level: "warning", title: "early" });
  assert.equal(state.saves.length, 0, "nothing is persisted before hydration");

  resolveLoad([stored("old-1", 1_000), stored("old-2", 2_000, { readAt: 2_000 })]);
  await hydrating;

  assert.deepEqual(store.items.map((item) => item.id), [early.id, "old-2", "old-1"]);
  assert.equal(store.hydrated, true);
  assert.equal(store.unreadCount, 2);
  assert.equal(state.saves.length, 1);
  assert.deepEqual(state.saves[0].value.map((item) => item.id), [early.id, "old-2", "old-1"]);
});

test("in-memory notifications win over stored copies with the same id or progressKey", async () => {
  const { store, resolveLoad } = setup();
  const hydrating = store.hydrate();
  const progress = store.push({ title: "uploading", level: "progress", progressKey: "job-1", progress: { current: 5, total: 10 } });

  resolveLoad([
    stored(progress.id, 1_000, { title: "same id" }),
    stored("stale-progress", 2_000, { progressKey: "job-1", progress: { current: 1, total: 10 } }),
    stored("kept", 3_000),
  ]);
  await hydrating;

  assert.deepEqual(store.items.map((item) => item.id), [progress.id, "kept"]);
  assert.equal(store.items[0].title, "uploading");
  assert.deepEqual(store.items[0].progress, { current: 5, total: 10 });
});

test("the merged list respects the maximum notification count", async () => {
  const { state, store, resolveLoad } = setup();
  const hydrating = store.hydrate();
  const early = store.push({ title: "newest" });

  resolveLoad(Array.from({ length: 200 }, (_, index) => stored(`old-${index}`, 1_000 + index)));
  await hydrating;

  assert.equal(store.items.length, 200);
  assert.equal(store.items[0].id, early.id);
  assert.equal(store.items.at(-1).id, "old-1");
  assert.equal(state.saves.at(-1).value.length, 200);
});

test("hydrate without pending notifications restores storage without rewriting it", async () => {
  const { state, store, resolveLoad } = setup();
  const hydrating = store.hydrate();
  resolveLoad([stored("old-1", 1_000), stored("old-2", 2_000)]);
  await hydrating;

  assert.deepEqual(store.items.map((item) => item.id), ["old-2", "old-1"]);
  assert.equal(state.saves.length, 0);
  store.push({ title: "after" });
  assert.equal(state.saves.length, 1);
});
