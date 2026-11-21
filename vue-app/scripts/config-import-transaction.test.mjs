import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const persistentStoreSource = await readFile(
  new URL("../src/services/persistent-store.ts", import.meta.url),
  "utf8"
);
const [settingsViewSource, configPortabilitySource] = await Promise.all([
  readFile(new URL("../src/views/SettingsWorkspaceView.vue", import.meta.url), "utf8"),
  readFile(new URL("../src/services/config-portability.ts", import.meta.url), "utf8"),
]);
const compiled = ts.transpileModule(
  persistentStoreSource
    .replace(
      'import { createSerializedTaskQueue } from "@/domain/async/serialized-task-queue";',
      "const createSerializedTaskQueue = () => (task) => task();"
    )
    .replace(
      'import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";',
      "const invokeBackend = async () => undefined; const isDesktopRuntime = () => false;"
    ),
  { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }
).outputText;
const persistentStore = await import(
  `data:text/javascript;base64,${Buffer.from(compiled).toString("base64")}`
);

test("config import requires a plain object containing only app_ keys", () => {
  for (const invalid of [null, [], "value", 1]) {
    assert.throws(
      () => persistentStore.preparePersistedImportBatch(invalid),
      /invalid_config_import_payload/
    );
  }
  assert.throws(
    () => persistentStore.preparePersistedImportBatch({ app_valid: 1, invalid: 2 }),
    /invalid_config_import_key/
  );
});

test("config import fully prepares legacy strings before one batch call", () => {
  assert.deepEqual(
    persistentStore.preparePersistedImportBatch({
      app_object: '{"enabled":true}',
      app_number: "42",
      app_text: "legacy",
      app_native_cipher: "enc:v2:device-bound",
    }),
    {
      sets: [
        {
          key: "app_object",
          value: { enabled: true },
          browserSnapshot: { enabled: true },
          writeBrowserSnapshot: true,
        },
        {
          key: "app_number",
          value: 42,
          browserSnapshot: 42,
          writeBrowserSnapshot: true,
        },
        {
          key: "app_text",
          value: "legacy",
          browserSnapshot: "legacy",
          writeBrowserSnapshot: true,
        },
        {
          key: "app_native_cipher",
          value: "enc:v2:device-bound",
          browserSnapshot: "enc:v2:device-bound",
          writeBrowserSnapshot: false,
        },
      ],
    }
  );
});

test("Settings import rejects retired AI history keys from portable configuration", () => {
  const importStart = settingsViewSource.indexOf("async function importConfig");
  const importEnd = settingsViewSource.indexOf("async function clearAiHistory", importStart);
  const importSource = settingsViewSource.slice(importStart, importEnd);

  assert.match(importSource, /parseConfigImport\(/);
  assert.match(importSource, /await applyConfigImport\(parsed\)/);
  assert.match(configPortabilitySource, /!isRetiredAiHistoryStorageKey\(key\)/);
  assert.doesNotMatch(configPortabilitySource, /saveAiConversationCollection/);
  assert.doesNotMatch(configPortabilitySource, /deleteAiScopeIds/);
});

test("Settings import restores plugin data before app configuration", () => {
  const importStart = settingsViewSource.indexOf("async function startImportConfig");
  const importEnd = settingsViewSource.indexOf("async function importConfig", importStart);
  const importSource = settingsViewSource.slice(importStart, importEnd);

  assert.ok(importSource.indexOf("await readConfigImportFile(path)") > 0);
  assert.ok(
    importSource.indexOf("await importConfigPluginData(path)")
      < importSource.indexOf("await applyConfigImport(parsed)")
  );
  // Plugin files can be large, so the desktop export writes them from the backend.
  assert.match(settingsViewSource, /writeConfigExportWithPluginData\(path, payload\)/);
  assert.match(configPortabilitySource, /"plugin_host_config_export_write"/);
  assert.match(configPortabilitySource, /"plugin_host_config_import_read"/);
  assert.match(configPortabilitySource, /"plugin_host_config_import_plugins"/);
  // Plugin-only files stay importable when the caller confirmed plugin data exists.
  assert.match(configPortabilitySource, /!Object\.keys\(values\)\.length && !options\.allowEmpty/);
});
