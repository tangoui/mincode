// services/ai-external-sync.ts: reads the settings snapshot and the local
// secret-hash store, runs the domain logic, persists both, and bumps the
// revision the open Settings page watches.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";
import { settingsIntegrations } from "./settings-integrations-module.mjs";

const SECRETS_KEY = "ai_external_sync_secrets_v1";
const PLUGIN = "mincode.cc-switch-sync";
const moduleUrl = (path) => new URL(path, import.meta.url).href;
const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");
const transpile = (source) =>
  ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
  }).outputText;

// The real domain module, with settings-integrations swapped for its Node-loadable copy.
globalThis.__serviceTestSettingsIntegrations = settingsIntegrations;
const externalSync = await import(
  `data:text/javascript;base64,${Buffer.from(
    transpile(
      (await readSource("../src/domain/ai/external-sync.ts"))
        .replace(/from "@\/domain\/ai\/anthropic-auth"/g, `from "${moduleUrl("../src/domain/ai/anthropic-auth.ts")}"`)
        .replace(/from "@\/domain\/ai\/model-selection"/g, `from "${moduleUrl("../src/domain/ai/model-selection.ts")}"`)
        .replace(
          /import \{([^}]*)\} from "@\/domain\/ai\/settings-integrations";/,
          "const {$1} = globalThis.__serviceTestSettingsIntegrations;",
        ),
    ),
  ).toString("base64")}`
);

const serviceSource = await readSource("../src/services/ai-external-sync.ts");
let loads = 0;

/** Fresh service instance over in-memory doubles of the persistence modules. */
async function loadService({ snapshot = {}, store = {}, failPersist = null, onPersist = null } = {}) {
  const state = { snapshot, store, persisted: [], saved: [], runtimeUpdates: [], revisionListeners: [] };
  // shallowRef double that reports writes synchronously, standing in for a Vue watch.
  const shallowRef = (initial) => {
    let current = initial;
    return {
      get value() { return current; },
      set value(next) { current = next; for (const listener of state.revisionListeners) listener(next); },
    };
  };
  globalThis.__aiExternalSyncServiceMocks = {
    vue: { shallowRef },
    externalSync,
    settings: {
      hydrateSettingsSnapshot: async () => state.snapshot,
      readSettingsSnapshot: () => state.snapshot,
      updateSettingsRuntimeSnapshot: (value) => {
        state.runtimeUpdates.push(value);
        state.snapshot = structuredClone(value);
      },
      persistSettingsSnapshot: async (value, options) => {
        state.persisted.push({ value: structuredClone(value), options });
        state.snapshot = structuredClone(value);
        await onPersist?.(value, options);
        if (failPersist) throw failPersist;
      },
    },
    secure: {
      loadSecurePersistedValue: async (key) => state.store[key] ?? null,
      saveSecurePersistedValue: async (key, value, browserSnapshot, options) => {
        state.saved.push({ key, value, browserSnapshot, options });
        state.store[key] = value;
      },
    },
  };
  const source = serviceSource
    .replace(/import \{([^}]*)\} from "vue";/, "const {$1} = globalThis.__aiExternalSyncServiceMocks.vue;")
    .replace(
      /import \{([^}]*)\} from "@\/domain\/ai\/external-sync";/,
      "const {$1} = globalThis.__aiExternalSyncServiceMocks.externalSync;",
    )
    .replace(
      /import \{([^}]*)\} from "@\/services\/settings-persistence";/,
      "const {$1} = globalThis.__aiExternalSyncServiceMocks.settings;",
    )
    .replace(
      /import \{([^}]*)\} from "@\/services\/secure-persistent-store";/,
      "const {$1} = globalThis.__aiExternalSyncServiceMocks.secure;",
    );
  const compiled = `${transpile(source)}\n// load ${(loads += 1)}`;
  const service = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`);
  return { service, state };
}

function profileItem(apiKey = "sk-live") {
  return {
    kind: "profile",
    sourceId: "claude:a1",
    name: "gc2",
    sourceApp: "claude",
    profile: {
      baseUrl: "https://api.example.com",
      apiKey,
      model: "m1",
      apiFormat: "anthropic",
      anthropicAuth: "bearer",
      commonModels: ["m1"],
    },
  };
}

const existingProfile = { id: "p0", name: "Mine", apiKey: "keep", model: "x" };

test("apply persists lists and bumps the revision", async () => {
  const { service, state } = await loadService({
    snapshot: { uiTheme: "dark", aiProfiles: [existingProfile], mcpServers: [], aiSkills: [] },
  });
  const items = [profileItem()];
  const [entry] = await service.previewExternalSync(PLUGIN, items);
  assert.equal(entry.status, "new");
  assert.equal(service.aiSettingsExternalRevision.value, 0);

  const result = await service.applyExternalSync(PLUGIN, items, [{ key: entry.key, action: "add" }]);

  assert.deepEqual(result, { added: 1, overwritten: 0, failed: [] });
  assert.equal(state.persisted.length, 1);
  assert.deepEqual(state.persisted[0].options, { strict: true });
  const written = state.persisted[0].value;
  assert.equal(written.uiTheme, "dark", "other settings are kept");
  assert.equal(written.aiProfiles.length, 2);
  assert.deepEqual(written.aiProfiles[0], existingProfile);
  assert.equal(written.aiProfiles[1].apiKey, "sk-live");
  assert.equal(written.aiProfiles[1].syncSource.pluginId, PLUGIN);
  assert.equal(typeof written.aiProfiles[1].id, "string");
  assert.ok(written.aiProfiles[1].id);
  assert.equal(service.aiSettingsExternalRevision.value, 1);

  const secretWrite = state.saved.find((save) => save.key === SECRETS_KEY);
  assert.ok(secretWrite, "secret hashes are saved under the local-only key");
  assert.match(secretWrite.value[entry.key], /^[0-9a-f]{64}$/);
  assert.ok(!JSON.stringify(secretWrite).includes("sk-live"), "no secret value reaches the hash store");
  assert.ok(!JSON.stringify(secretWrite.browserSnapshot).includes(secretWrite.value[entry.key]),
    "secret hashes never reach browser storage");

  // A second read sees the synced item as unchanged.
  const [again] = await service.previewExternalSync(PLUGIN, items);
  assert.equal(again.status, "unchanged");
});

test("a failed settings save leaves the secret hashes and the revision alone", async () => {
  const failure = new Error("native store failed");
  const before = { aiProfiles: [existingProfile], mcpServers: [], aiSkills: [] };
  const { service, state } = await loadService({ snapshot: structuredClone(before), failPersist: failure });
  const items = [profileItem()];
  const [entry] = await service.previewExternalSync(PLUGIN, items);

  await assert.rejects(service.applyExternalSync(PLUGIN, items, [{ key: entry.key, action: "add" }]), failure);
  assert.equal(state.saved.filter((save) => save.key === SECRETS_KEY).length, 0);
  assert.equal(service.aiSettingsExternalRevision.value, 0);
  assert.deepEqual(state.snapshot, before, "the in-memory snapshot is rolled back");
});

test("invalid choices are reported and write nothing", async () => {
  const { service, state } = await loadService({ snapshot: { aiProfiles: [existingProfile] } });
  const result = await service.applyExternalSync(PLUGIN, [profileItem()], [{ key: "nope", action: "add" }]);
  assert.deepEqual(result, { added: 0, overwritten: 0, failed: [{ key: "nope", reason: "invalid-choice" }] });
  assert.equal(state.persisted.length, 0);
  assert.equal(state.saved.length, 0);
  assert.equal(service.aiSettingsExternalRevision.value, 0);
});

test("missing or malformed lists are read as empty", async () => {
  const { service } = await loadService({
    snapshot: { aiProfiles: "broken", mcpServers: [null, 3] },
    store: { [SECRETS_KEY]: ["not", "a", "record"] },
  });
  const [entry] = await service.previewExternalSync(PLUGIN, [profileItem()]);
  assert.equal(entry.status, "new");
});

test("secret hashes are not part of config sync", async () => {
  const persistence = await readSource("../src/services/config-sync-persistence.ts");
  assert.ok(!persistence.includes(SECRETS_KEY));
  const configSync = await readSource("../src/domain/settings/config-sync.ts");
  const prefix = configSync.match(/CONFIG_SYNC_STORAGE_PREFIX = "([^"]+)"/)?.[1];
  assert.equal(prefix, "app_");
  assert.ok(!SECRETS_KEY.startsWith(prefix), "config sync only collects app_ keys");
  assert.match(serviceSource, new RegExp(`"${SECRETS_KEY}"`));
});

/**
 * Stand-in for SettingsWorkspaceView: its own settings copy, a debounced save
 * that may be pending, the flush it registers, and its revision watcher.
 */
function standInPage(service, state) {
  const page = { settings: structuredClone(state.snapshot), pending: false };
  page.save = () => {
    page.pending = false;
    return state.persistPage(structuredClone(page.settings));
  };
  page.edit = (change) => {
    change(page.settings);
    page.pending = true;
  };
  service.registerSettingsFlush(async () => {
    if (page.pending) await page.save();
  });
  state.revisionListeners.push(() => {
    const lists = service.externallySyncedSettingsLists();
    page.settings = { ...page.settings, ...structuredClone(lists) };
    page.pending = false;
    void page.save();
  });
  return page;
}

function withPagePersistence(state) {
  // The page writes through the same (mocked) persistSettingsSnapshot.
  state.persistPage = (value) => globalThis.__aiExternalSyncServiceMocks.settings.persistSettingsSnapshot(value);
}

test("external update survives a pending settings save", async () => {
  const { service, state } = await loadService({
    snapshot: { uiTheme: "dark", aiProfiles: [existingProfile], mcpServers: [], aiSkills: [] },
  });
  withPagePersistence(state);
  const page = standInPage(service, state);
  page.edit((settings) => {
    settings.aiProfiles[0].model = "edited-on-page";
  });
  const items = [profileItem()];
  const [entry] = await service.previewExternalSync(PLUGIN, items);

  await service.applyExternalSync(PLUGIN, items, [{ key: entry.key, action: "add" }]);

  for (const [label, profiles] of [["snapshot", state.snapshot.aiProfiles], ["page", page.settings.aiProfiles]]) {
    assert.equal(profiles.length, 2, label);
    assert.equal(profiles[0].model, "edited-on-page", `${label} keeps the pending page edit`);
    assert.equal(profiles[1].syncSource?.pluginId, PLUGIN, `${label} holds the synced item`);
  }
  assert.equal(page.pending, false);
});

test("a page save landing during the apply write cannot undo the sync", async () => {
  let page;
  const { service, state } = await loadService({
    snapshot: { uiTheme: "dark", aiProfiles: [existingProfile], mcpServers: [], aiSkills: [] },
    onPersist: async (_value, options) => {
      if (!options?.strict) return;
      // The 300 ms page save fires while the apply's native write is pending.
      page.edit((settings) => {
        settings.uiTheme = "light";
      });
      await page.save();
    },
  });
  withPagePersistence(state);
  page = standInPage(service, state);
  const items = [profileItem()];
  const [entry] = await service.previewExternalSync(PLUGIN, items);

  const result = await service.applyExternalSync(PLUGIN, items, [{ key: entry.key, action: "add" }]);
  await Promise.resolve();

  assert.equal(result.added, 1);
  assert.equal(state.snapshot.uiTheme, "light", "the concurrent page edit is kept");
  assert.equal(state.snapshot.aiProfiles.length, 2, "the synced item is saved again after the page save");
  assert.equal(page.settings.aiProfiles[1].syncSource?.pluginId, PLUGIN);
});

test("an unregistered page is not flushed", async () => {
  const { service, state } = await loadService({ snapshot: { aiProfiles: [existingProfile] } });
  let flushed = 0;
  const unregister = service.registerSettingsFlush(async () => {
    flushed += 1;
  });
  unregister();
  const items = [profileItem()];
  const [entry] = await service.previewExternalSync(PLUGIN, items);
  await service.applyExternalSync(PLUGIN, items, [{ key: entry.key, action: "add" }]);
  assert.equal(flushed, 0);
  assert.equal(state.persisted.length, 1);
});

test("the Settings page registers its flush and assigns the published lists", async () => {
  const view = await readSource("../src/views/SettingsWorkspaceView.vue");
  assert.match(view, /onBeforeUnmount\(registerSettingsFlush\(flushPendingPersist\)\);/);
  const flush = view.slice(view.indexOf("async function flushPendingPersist"), view.indexOf("onBeforeUnmount(registerSettingsFlush"));
  assert.match(flush, /if \(!settingsHydrated\.value \|\| !persistTimer\) return;/);
  assert.match(flush, /await persistSettingsSnapshot\(\{ \.\.\.settings, locale: locale\.value \}\);/);
  const start = view.indexOf("watch(aiSettingsExternalRevision");
  const body = view.slice(start, view.indexOf("\n});", start));
  assert.match(body, /externallySyncedSettingsLists\(\)/);
  assert.match(body, /if \(!settingsHydrated\.value \|\| !lists\) return;/);
  for (const list of ["aiProfiles", "mcpServers", "aiSkills"]) {
    assert.match(body, new RegExp(`settings\\.${list} = external\\.${list};`));
  }
  assert.match(body, /persist\(\);/);
});

test("one review slot per plugin across frames", async () => {
  const { service } = await loadService();
  const release = service.claimExternalSyncReview(PLUGIN);
  assert.equal(typeof release, "function");
  assert.equal(service.claimExternalSyncReview(PLUGIN), null, "a second concurrent apply of the same plugin is busy");
  const other = service.claimExternalSyncReview("other.plugin");
  assert.equal(typeof other, "function", "other plugins are independent");
  release();
  release();
  const again = service.claimExternalSyncReview(PLUGIN);
  assert.equal(typeof again, "function", "released after the write");
  again();
  other();
});

test("pending Settings edits can be flushed before the review preview", async () => {
  const { service } = await loadService({ snapshot: { aiProfiles: [], mcpServers: [], aiSkills: [] } });
  let flushed = 0;
  const unregister = service.registerSettingsFlush(async () => { flushed += 1; });
  await service.flushPendingSettingsEdits();
  assert.equal(flushed, 1);
  unregister();
  await service.flushPendingSettingsEdits();
  assert.equal(flushed, 1);
});

test("a reviewed apply writes nothing whose entry changed since the review", async () => {
  const { service, state } = await loadService({ snapshot: { aiProfiles: [], mcpServers: [], aiSkills: [] } });
  const items = [profileItem()];
  const [shown] = await service.previewExternalSync(PLUGIN, items);
  // Another frame (or a Settings save) added it meanwhile: no longer new.
  await service.applyExternalSync(PLUGIN, items, [{ key: shown.key, action: "add" }]);
  const persisted = state.persisted.length;
  const result = await service.applyExternalSync(PLUGIN, items, [{ key: shown.key, action: "add" }], [shown]);
  assert.deepEqual(result, { added: 0, overwritten: 0, failed: [{ key: shown.key, reason: "changed-since-review" }] });
  assert.equal(state.persisted.length, persisted);
});

test("apply keeps a local prompted value when the plugin sends it empty", async () => {
  const { service, state } = await loadService({ snapshot: { aiProfiles: [], mcpServers: [], aiSkills: [] } });
  const item = {
    kind: "mcp",
    sourceId: "figma",
    name: "Figma",
    sourceApp: "ai-market",
    mcp: {
      transport: "stdio",
      command: "npx",
      args: ["-y", "figma-mcp@1.0.0"],
      url: "",
      env: { FIGMA_API_KEY: "" },
      headers: {},
      prompts: [{ target: "env", name: "FIGMA_API_KEY", label: "Figma API key", secret: true }],
    },
  };
  // First install: the value typed into the confirmation is written.
  const [added] = await service.previewExternalSync(PLUGIN, [item]);
  const typed = { [added.key]: { "env:FIGMA_API_KEY": "k1" } };
  const filled = externalSync.withPromptValues([item], PLUGIN, typed);
  const first = await service.applyExternalSync(PLUGIN, filled, [{ key: added.key, action: "add" }], [added]);
  assert.deepEqual(first, { added: 1, overwritten: 0, failed: [] });
  assert.equal(state.snapshot.mcpServers[0].env.FIGMA_API_KEY, "k1");
  assert.ok(!JSON.stringify(state.store[SECRETS_KEY]).includes("k1"), "the typed value is not hashed for the plugin");

  // An update from the plugin carries the prompted value empty again: the local one stays.
  const bumped = { ...item, mcp: { ...item.mcp, args: ["-y", "figma-mcp@1.1.0"] } };
  const [update] = await service.previewExternalSync(PLUGIN, [bumped]);
  assert.equal(update.status, "update");
  assert.deepEqual(update.changedFields, ["args"]);
  const unfilled = externalSync.withPromptValues([bumped], PLUGIN, {});
  const second = await service.applyExternalSync(PLUGIN, unfilled, [{ key: update.key, action: "overwrite" }], [update]);
  assert.deepEqual(second, { added: 0, overwritten: 1, failed: [] });
  assert.deepEqual(state.snapshot.mcpServers[0].env, { FIGMA_API_KEY: "k1" });
  assert.equal(state.snapshot.mcpServers[0].args, "-y figma-mcp@1.1.0");
});
