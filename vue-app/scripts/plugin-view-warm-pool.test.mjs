import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import {
  DEFAULT_PLUGIN_VIEW_WARM_LIMIT,
  MAX_PLUGIN_VIEW_WARM_LIMIT,
  PLUGIN_VIEW_WARM_TTL_MS,
  expirePluginViewWarmEntries,
  nextPluginViewWarmExpiry,
  normalizePluginViewWarmLimit,
  parkPluginViewWarmEntry,
  takePluginViewWarmEntry,
  trimPluginViewWarmEntries,
} from "../src/domain/plugin-ui/warm-pool.ts";

const entry = (key, parkedAt, overrides = {}) => ({ key, workspaceId: "ws", pluginId: "todo", viewId: "main", parkedAt, ...overrides });

test("warm limit defaults to 0 (off), accepts 0–6 and is clamped", () => {
  // Off by default: a closed plugin view keeps nothing unless the user opts in.
  assert.equal(DEFAULT_PLUGIN_VIEW_WARM_LIMIT, 0);
  assert.equal(PLUGIN_VIEW_WARM_TTL_MS, 10 * 60_000);
  assert.equal(normalizePluginViewWarmLimit(undefined), 0);
  assert.equal(normalizePluginViewWarmLimit("abc"), 0);
  assert.equal(normalizePluginViewWarmLimit(-1), 0);
  assert.equal(normalizePluginViewWarmLimit(3), 3);
  assert.equal(normalizePluginViewWarmLimit(0), 0);
  assert.equal(normalizePluginViewWarmLimit(2.4), 2);
  assert.equal(normalizePluginViewWarmLimit(99), MAX_PLUGIN_VIEW_WARM_LIMIT);
});

test("parking evicts the least recently parked entries beyond the limit", () => {
  let entries = [];
  for (const [index, key] of ["a", "b", "c"].entries()) entries = parkPluginViewWarmEntry(entries, entry(key, index), 3).entries;
  const result = parkPluginViewWarmEntry(entries, entry("d", 10), 3);
  assert.deepEqual(result.entries.map((item) => item.key), ["b", "c", "d"]);
  assert.deepEqual(result.evicted.map((item) => item.key), ["a"]);
  // Limit 0 disables the pool: the parked entry itself is evicted.
  const off = parkPluginViewWarmEntry([], entry("x", 1), 0);
  assert.deepEqual(off.entries, []);
  assert.deepEqual(off.evicted.map((item) => item.key), ["x"]);
  // Re-parking the same key replaces it without evicting its (shared) frame.
  const replaced = parkPluginViewWarmEntry([entry("a", 1)], entry("a", 5), 3);
  assert.deepEqual(replaced.entries.map((item) => item.parkedAt), [5]);
  assert.deepEqual(replaced.evicted, []);
  // Lowering the limit trims oldest first.
  const trimmed = trimPluginViewWarmEntries([entry("a", 3), entry("b", 1), entry("c", 2)], 1);
  assert.deepEqual(trimmed.entries.map((item) => item.key), ["a"]);
  assert.deepEqual(trimmed.evicted.map((item) => item.key).sort(), ["b", "c"]);
});

test("reuse requires the same workspace, plugin and view and an unexpired entry", () => {
  const entries = [entry("tab", 1000)];
  const reused = takePluginViewWarmEntry(entries, { key: "tab", workspaceId: "ws", pluginId: "todo", viewId: "main" }, 2000);
  assert.equal(reused.entry?.key, "tab");
  assert.deepEqual(reused.entries, []);
  for (const target of [
    { key: "tab", workspaceId: "other", pluginId: "todo", viewId: "main" },
    { key: "tab", workspaceId: "ws", pluginId: "calendar", viewId: "main" },
    { key: "tab", workspaceId: "ws", pluginId: "todo", viewId: "other" },
  ]) {
    const mismatch = takePluginViewWarmEntry(entries, target, 2000);
    assert.equal(mismatch.entry, undefined);
    assert.equal(mismatch.discarded?.key, "tab");
    assert.deepEqual(mismatch.entries, []);
  }
  const expired = takePluginViewWarmEntry(entries, { key: "tab", workspaceId: "ws", pluginId: "todo", viewId: "main" }, 1000 + PLUGIN_VIEW_WARM_TTL_MS);
  assert.equal(expired.entry, undefined);
  assert.equal(expired.discarded?.key, "tab");
  const missing = takePluginViewWarmEntry(entries, { key: "none", workspaceId: "ws", pluginId: "todo", viewId: "main" }, 2000);
  assert.equal(missing.entry, undefined);
  assert.equal(missing.entries.length, 1);
});

test("entries expire after the TTL and the next expiry is scheduled from the oldest", () => {
  const entries = [entry("old", 0), entry("new", 60_000)];
  assert.equal(nextPluginViewWarmExpiry(entries, 30_000), PLUGIN_VIEW_WARM_TTL_MS - 30_000);
  assert.equal(nextPluginViewWarmExpiry([], 0), undefined);
  const result = expirePluginViewWarmEntries(entries, PLUGIN_VIEW_WARM_TTL_MS);
  assert.deepEqual(result.expired.map((item) => item.key), ["old"]);
  assert.deepEqual(result.entries.map((item) => item.key), ["new"]);
});

test("warm pool store discards entries on every revocation signal", async () => {
  const store = await readFile(new URL("../src/stores/plugin-view-warm-pool.ts", import.meta.url), "utf8");
  for (const event of ["PLUGIN_UI_REVOKED_EVENT", '"plugin:state-changed"', '"plugin:development-changed"', '"plugin:workspace-changed"']) {
    assert.match(store, new RegExp(`listenAppEvent<unknown>\\(${event.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`));
  }
  assert.match(store, /export { PLUGIN_UI_REVOKED_EVENT }/);
  const domain = await readFile(new URL("../src/domain/plugin-ui/warm-pool.ts", import.meta.url), "utf8");
  assert.match(domain, /export const PLUGIN_UI_REVOKED_EVENT = "plugin:ui-revoked"/);
  assert.match(store, /watch\(\(\) => workbench\.activeWorkspaceId, \(\) => discardInactiveWorkspaces\(\)\)/);
  assert.doesNotMatch(store, /localStorage|sessionStorage|ScopedPersistence|persistSettingsSnapshot/);
  const go = await readFile(new URL("../../backend/plugin/ui_contract.go", import.meta.url), "utf8");
  assert.match(go, /const pluginUIRevokedEvent = "plugin:ui-revoked"/);
  assert.match(go, /manager\.uiHost\.RevokePlugin\(pluginID\)\s*\n(?:\s*\/\/[^\n]*\n)*\s*core\.Emit\(pluginUIRevokedEvent/);
});
