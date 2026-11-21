import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { configurationFields } from "../src/domain/plugins/configuration.ts";
import {
  createDebouncedTrigger,
  isPluginSettingsCategoryListed,
  mergeConfigurationDrafts,
  reconcileConfigurationSelection,
} from "../src/domain/plugins/configuration-refresh.ts";
import { createLatestRequestGate } from "../src/domain/async/latest-request-gate.ts";

const source = (path) => readFile(new URL(path, import.meta.url), "utf8");

const fieldsOf = (properties) => configurationFields({ properties }, "en");

test("selection keeps a vanished plugin selected and reports it missing", () => {
  assert.deepEqual(reconcileConfigurationSelection(["a", "b"], ""), { selectedId: "a", missing: false });
  assert.deepEqual(reconcileConfigurationSelection([], ""), { selectedId: "", missing: false });
  assert.deepEqual(reconcileConfigurationSelection(["a", "b"], "b"), { selectedId: "b", missing: false });
  // Never silently switch to another plugin.
  assert.deepEqual(reconcileConfigurationSelection(["a"], "b"), { selectedId: "b", missing: true });
  assert.deepEqual(reconcileConfigurationSelection([], "b"), { selectedId: "b", missing: true });
  // An explicit navigation target wins when it exists, else the selection rules apply.
  assert.deepEqual(reconcileConfigurationSelection(["a", "dev"], "a", "dev"), { selectedId: "dev", missing: false });
  assert.deepEqual(reconcileConfigurationSelection(["a"], "a", "gone"), { selectedId: "a", missing: false });
});

test("unsaved edits survive a refresh while valid under the refreshed schema", () => {
  const before = fieldsOf({
    endpoint: { type: "string", scope: "global" },
    retries: { type: "integer", minimum: 0, maximum: 10, scope: "global" },
    mode: { type: "string", enum: ["fast", "safe"], scope: "global" },
    removed: { type: "string", scope: "global" },
  });
  const baseline = { endpoint: "https://a.test", retries: 3, mode: "safe", removed: "x" };
  const draft = { endpoint: "https://edited.test", retries: 8, mode: "fast", removed: "y" };

  // Same schema, another key changed on the host: edits kept, host value applied elsewhere.
  const same = mergeConfigurationDrafts(baseline, draft, { ...baseline, mode: "safe" }, before, before);
  assert.deepEqual(same.values, draft);
  assert.deepEqual(same.preserved.sort(), ["endpoint", "mode", "removed", "retries"]);

  // Development generation narrows retries, drops `removed`, turns mode into a boolean.
  const after = fieldsOf({
    endpoint: { type: "string", scope: "global" },
    retries: { type: "integer", minimum: 0, maximum: 5, scope: "global" },
    mode: { type: "boolean", scope: "global" },
    added: { type: "string", scope: "global" },
  });
  const next = { endpoint: "https://a.test", retries: 3, mode: false, added: "" };
  const merged = mergeConfigurationDrafts(baseline, draft, next, before, after);
  assert.deepEqual(merged.values, { endpoint: "https://edited.test", retries: 3, mode: false, added: "" });
  assert.deepEqual(merged.preserved, ["endpoint"]);

  // Untouched drafts always follow the host.
  const clean = mergeConfigurationDrafts(baseline, { ...baseline }, { ...baseline, retries: 4 }, before, before);
  assert.deepEqual(clean.values.retries, 4);
  assert.deepEqual(clean.preserved, []);
});

test("secret fields are never carried as drafts", () => {
  const fields = fieldsOf({ token: { type: "string", secret: true, scope: "global" } });
  const merged = mergeConfigurationDrafts({ token: "" }, { token: "typed" }, { token: "" }, fields, fields);
  assert.deepEqual(merged.preserved, []);
});

test("debounced trigger collapses bursts and can be cancelled", () => {
  const pending = new Map();
  let nextHandle = 0;
  const timers = {
    set: (callback) => {
      const handle = ++nextHandle;
      pending.set(handle, callback);
      return handle;
    },
    clear: (handle) => pending.delete(handle),
  };
  const flush = () => {
    for (const [handle, callback] of [...pending]) {
      pending.delete(handle);
      callback();
    }
  };
  let runs = 0;
  const trigger = createDebouncedTrigger(() => runs++, 50, timers);
  trigger.schedule();
  trigger.schedule();
  trigger.schedule();
  assert.equal(pending.size, 1);
  flush();
  assert.equal(runs, 1);
  trigger.schedule();
  trigger.cancel();
  flush();
  assert.equal(runs, 1);
});

test("out-of-order list refreshes apply only the latest response", async () => {
  let selected = "dev.plugin";
  const gate = createLatestRequestGate(() => 0);
  const applied = [];
  const load = async (response) => {
    const token = gate.begin();
    const ids = await response;
    if (!gate.isCurrent(token)) return false;
    const next = reconcileConfigurationSelection(ids, selected);
    selected = next.selectedId;
    applied.push({ ids, missing: next.missing });
    return true;
  };
  let resolveOld;
  const old = load(new Promise((resolve) => { resolveOld = resolve; }));
  // Newer refresh (development session detached) resolves first.
  assert.equal(await load(Promise.resolve(["other"])), true);
  resolveOld(["dev.plugin", "other"]);
  assert.equal(await old, false);
  assert.deepEqual(applied, [{ ids: ["other"], missing: true }]);
  assert.equal(selected, "dev.plugin");
});

test("settings state responses are bound to plugin and workspace", () => {
  let plugin = "a";
  let workspace = "ws-1";
  const gate = createLatestRequestGate(() => `${plugin}\n${workspace}`);
  const token = gate.begin();
  workspace = "ws-2";
  assert.equal(gate.isCurrent(token), false);
  const second = gate.begin();
  plugin = "b";
  assert.equal(gate.isCurrent(second), false);
});

test("plugin settings panel follows host events and cleans up", async () => {
  const panel = await source("../src/components/settings/PluginSettingsPanel.vue");
  assert.match(panel, /listenPluginSettingsInvalidated\(\(\) => refreshTrigger\.schedule\(\)\)/);
  assert.match(panel, /stopInvalidation\?\.\(\)/);
  assert.match(panel, /refreshTrigger\.cancel\(\)/);
  assert.match(panel, /pluginContrib\.configuration\.pluginUnavailable/);
  assert.match(panel, /meta\?\.source === "development"/);
  // Plugin-owned titles resolve over the locale chain (language pack tag, then base).
  assert.match(panel, /configurationFields\(state\.value\.schema, localeChain\.value\)/);
  assert.doesNotMatch(panel, /\blocale\.value\b/);
  const service = await source("../src/services/plugin-configuration-service.ts");
  for (const event of ["PLUGIN_CONTRIBUTIONS_CHANGED_EVENT", '"plugin:state-changed"', '"plugin:workspace-changed"']) {
    assert.ok(service.includes(event), `settings refresh listens to ${event}`);
  }
  for (const locale of ["zh", "en"]) {
    const messages = await source(`../src/i18n/locales/${locale}.ts`);
    for (const key of ["development", "developmentHint", "unavailable", "pluginUnavailable"]) {
      assert.ok(messages.includes(`"pluginContrib.configuration.${key}"`), `${locale} has ${key}`);
    }
  }
});

test("settings lists the plugins page only while a plugin declares settings", () => {
  assert.equal(isPluginSettingsCategoryListed("unknown", false), false);
  assert.equal(isPluginSettingsCategoryListed("none", false), false);
  assert.equal(isPluginSettingsCategoryListed("some", false), true);
  // A failed read keeps the page reachable so it can show the error.
  assert.equal(isPluginSettingsCategoryListed("error", false), true);
  // Never removed while it is the current page.
  for (const availability of ["unknown", "none", "some", "error"]) {
    assert.equal(isPluginSettingsCategoryListed(availability, true), true);
  }
});

test("settings navigation follows host events and cleans up", async () => {
  const composable = await source("../src/composables/use-plugin-settings-availability.ts");
  assert.match(composable, /listenPluginSettingsInvalidated\(\(\) => trigger\.schedule\(\)\)/);
  assert.match(composable, /stopInvalidation\?\.\(\)/);
  assert.match(composable, /trigger\.cancel\(\)/);
  assert.match(composable, /gate\.isCurrent\(token\)/);
  const view = await source("../src/views/SettingsWorkspaceView.vue");
  assert.match(view, /isPluginSettingsCategoryListed\(pluginSettings\.availability\.value, active\.value === "plugins"\)/);
  assert.match(view, /void pluginSettings\.refresh\(\)/);
});
