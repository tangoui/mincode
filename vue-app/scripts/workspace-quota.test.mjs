import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { evaluateQuota } from "../src/domain/workspace/quota.ts";

const workbench = await readFile(new URL("../src/stores/workbench.ts", import.meta.url), "utf8");
const limits = { max_devices: 1, max_active_workspaces: 3, max_windows_per_workspace: 6 };

test("quota blocks only increases beyond a positive limit", () => {
  assert.equal(evaluateQuota(limits, "workspace", 2).allowed, true);
  assert.deepEqual(evaluateQuota(limits, "workspace", 3), { allowed: false, resource: "workspace", current: 3, limit: 3 });
  assert.equal(evaluateQuota(limits, "workspace", 7).allowed, false, "existing excess stays but cannot grow");
  assert.equal(evaluateQuota(limits, "tab", 5).allowed, true);
  assert.equal(evaluateQuota(limits, "tab", 6).allowed, false);
});

test("zero means unlimited and missing limits never block", () => {
  const unlimited = { max_devices: 0, max_active_workspaces: 0, max_windows_per_workspace: 0 };
  assert.equal(evaluateQuota(unlimited, "workspace", 10_000).allowed, true);
  assert.equal(evaluateQuota(unlimited, "tab", 10_000).allowed, true);
  assert.equal(evaluateQuota(undefined, "workspace", 99).allowed, true);
  assert.equal(evaluateQuota(null, "tab", 99).allowed, true);
  assert.equal(evaluateQuota({ ...limits, max_windows_per_workspace: -1 }, "tab", 99).allowed, true);
});

test("enabled plugins use their own limit and older backends without it never block", () => {
  const withPlugins = { ...limits, max_enabled_plugins: 2 };
  assert.equal(evaluateQuota(withPlugins, "plugin", 1).allowed, true);
  assert.deepEqual(evaluateQuota(withPlugins, "plugin", 2), { allowed: false, resource: "plugin", current: 2, limit: 2 });
  assert.equal(evaluateQuota(withPlugins, "tab", 5).allowed, true, "plugin limit does not affect tabs");
  assert.equal(evaluateQuota(limits, "plugin", 99).allowed, true, "missing field means unlimited");
  assert.equal(evaluateQuota({ ...limits, max_enabled_plugins: 0 }, "plugin", 99).allowed, true);
});

test("a plan without plugins refuses every new plugin, even with no count limit", () => {
  const forbidden = { ...limits, max_enabled_plugins: 0, plugins_disabled: true };
  assert.deepEqual(evaluateQuota(forbidden, "plugin", 0), { allowed: false, resource: "plugin", current: 0, limit: 0, forbidden: true });
  assert.equal(evaluateQuota(forbidden, "tab", 1).allowed, true, "the plugin switch does not affect tabs");
  assert.equal(evaluateQuota({ ...limits, plugins_disabled: false }, "plugin", 99).allowed, true);
});

function body(name) {
  const start = workbench.indexOf(`function ${name}(`);
  assert.ok(start >= 0, `${name} exists`);
  const next = workbench.indexOf("\n  function ", start + 1);
  return workbench.slice(start, next < 0 ? undefined : next);
}

test("every workbench path that adds a tab or workspace checks the quota before mutating", () => {
  for (const name of ["createTab", "openView", "openPluginView", "duplicateTab"]) {
    const source = body(name);
    const guard = source.indexOf("admitTab(");
    assert.ok(guard > 0, `${name} checks the tab quota`);
    assert.ok(guard < source.search(/tabs\.value\.(push|splice)/), `${name} checks before adding`);
  }
  const move = body("moveTabToWorkspace");
  assert.equal(move.match(/admitTab\(workspaceId\)/g)?.length, 2, "both move branches check the target workspace");
  for (const name of ["createWorkspace", "restoreDeletedWorkspace"]) {
    const source = body(name);
    const guard = source.indexOf("admitWorkspace()");
    assert.ok(guard > 0 && guard < source.indexOf("workspaces.value.push"), `${name} checks before adding`);
  }
  assert.doesNotMatch(body("registerPopupTab"), /admit/, "re-registering a popped-out tab is not a new tab");
});

test("a view whose tab the quota refused is never routed to", async () => {
  const read = (path) => readFile(new URL(`../src/${path}`, import.meta.url), "utf8");
  const refusedGuard = /if \(!workbench\.openView\(([\w.]+)\) && \1 !== "dashboard"\) return;/;
  for (const path of ["components/layout/AppSidebar.vue", "composables/use-command-palette.ts", "views/DashboardView.vue"]) {
    const source = await read(path);
    assert.match(source, refusedGuard, `${path} stops before routing when the tab is refused`);
    const guard = source.search(refusedGuard);
    assert.ok(source.indexOf("router.push", guard) > guard, `${path} routes only after the guard`);
  }
  const app = await read("App.vue");
  assert.match(app, /if \(!workbench\.openView\(target\.view\) && target\.view !== "dashboard"\) return;/);
  assert.match(
    app,
    /!workbench\.openView\(routeName as WorkspaceView\) && routeName !== "dashboard"\) \{\s*void router\.replace\(workspaceTabRoute\(workbench\.activeTab\)\);/,
    "the route watcher falls back to the active tab when the quota refuses the routed view",
  );
  assert.match(app, /if \(!workbench\.openPluginView\(pluginId, viewId, [^)]*\)\)\) \{\s*void router\.replace\(workspaceTabRoute\(workbench\.activeTab\)\);/);
});
