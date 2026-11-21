import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import {
  STATUS_BAR_MAX_ITEMS_PER_PLUGIN,
  STATUS_BAR_PRIORITY_MAX,
  STATUS_BAR_PRIORITY_MIN,
  STATUS_BAR_TEXT_MAX_RUNES,
  STATUS_BAR_TOOLTIP_MAX_RUNES,
  compareStatusBarItems,
  dynamicStatusBarItemIds,
  parseStatusBarRuntime,
  resolveStatusBarItems,
  sanitizeStatusBarText,
  truncateStatusBarText,
  visibleStatusBarItems,
} from "../src/domain/plugins/status-bar.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");

function contribution(pluginId, entries, runtime, extra = {}) {
  return {
    pluginId,
    pluginName: `Plugin ${pluginId}`,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { fingerprint: pluginId, entries, issues: [], ...(runtime === undefined ? {} : { runtime }) },
    ...extra,
  };
}

const host = { platform: "mac", view: "terminal" };

test("status bar limits mirror the contribution contract", async () => {
  const contract = JSON.parse(await readSource("../src/domain/plugins/contribution-points.json"));
  const spec = contract.points.statusBar;
  assert.equal(spec.status, "enabled");
  assert.equal(spec.maxEntries, STATUS_BAR_MAX_ITEMS_PER_PLUGIN);
  assert.equal(spec.limits.textMaxRunes, STATUS_BAR_TEXT_MAX_RUNES);
  assert.equal(spec.limits.tooltipMaxRunes, STATUS_BAR_TOOLTIP_MAX_RUNES);
  assert.equal(spec.limits.priorityMin, STATUS_BAR_PRIORITY_MIN);
  assert.equal(spec.limits.priorityMax, STATUS_BAR_PRIORITY_MAX);
});

test("items order left before right, then priority, then plugin id", () => {
  const items = resolveStatusBarItems([
    contribution("zeta", [
      { id: "r-low", text: "R low", alignment: "right", priority: 1 },
      { id: "l-mid", text: "L mid", priority: 5 },
    ]),
    contribution("alpha", [
      { id: "r-high", text: "R high", alignment: "right", priority: 9 },
      { id: "l-mid", text: "L mid alpha", priority: 5 },
      { id: "l-top", text: "L top", priority: 100 },
    ]),
  ], "en");
  const ordered = visibleStatusBarItems(items, {}, host).map((item) => item.key);
  assert.deepEqual(ordered, ["alpha:l-top", "alpha:l-mid", "zeta:l-mid", "alpha:r-high", "zeta:r-low"]);
  assert.equal(compareStatusBarItems(items[0], items[0]), 0);
});

test("text is truncated by code point and sanitized", () => {
  const long = "字".repeat(STATUS_BAR_TEXT_MAX_RUNES + 5);
  const truncated = truncateStatusBarText(long);
  assert.equal(Array.from(truncated).length, STATUS_BAR_TEXT_MAX_RUNES);
  assert.ok(truncated.endsWith("…"));
  assert.equal(truncateStatusBarText("😀".repeat(3), 2), "😀…");
  assert.equal(truncateStatusBarText("short"), "short");
  assert.equal(sanitizeStatusBarText("a\u202eb\nc\u2066d\u0007"), "abcd");
  assert.equal(sanitizeStatusBarText(42), "");

  const [item] = resolveStatusBarItems([
    contribution("p", [{ id: "a", text: "x".repeat(80), tooltip: "t\u202e\nline" }]),
  ], "en");
  assert.equal(Array.from(item.text).length, STATUS_BAR_TEXT_MAX_RUNES);
  assert.equal(item.tooltip, "t\nline");
});

test("runtime overrides apply only to dynamic items; pure-dynamic items wait for text", () => {
  const entries = [
    { id: "static", text: "Static", dynamic: false },
    { id: "pure", dynamic: true },
    { id: "branch", text: "main", dynamic: true, textLocalized: { zh: "主干" } },
    { id: "hidden", text: "Hidden", dynamic: true },
    { id: "bad id", text: "rejected" },
    { id: "no-text" },
  ];
  const before = resolveStatusBarItems([contribution("p", entries)], "zh-CN");
  assert.deepEqual(before.map((item) => [item.id, item.text]), [["static", "Static"], ["branch", "主干"], ["hidden", "Hidden"]]);

  const runtime = {
    items: {
      static: { text: "hacked" },
      pure: { text: "42%", busy: true, severity: "warning", tooltip: "Indexing" },
      branch: { severity: "fatal", busy: "yes" },
      hidden: { visible: false },
    },
  };
  const after = resolveStatusBarItems([contribution("p", entries, runtime)], "en");
  assert.deepEqual(after.map((item) => [item.id, item.text]), [["static", "Static"], ["pure", "42%"], ["branch", "main"]]);
  const pure = after.find((item) => item.id === "pure");
  assert.equal(pure.busy, true);
  assert.equal(pure.severity, "warning");
  assert.equal(pure.tooltip, "Indexing");
  const branch = after.find((item) => item.id === "branch");
  assert.equal(branch.severity, "normal");
  assert.equal(branch.busy, false);
  assert.deepEqual(parseStatusBarRuntime({ items: { "Bad Id": { text: "x" } } }), {});
  assert.deepEqual(parseStatusBarRuntime(null), {});
});

test("inactive, declarative and over-limit entries are dropped", () => {
  const many = Array.from({ length: 10 }, (_, index) => ({ id: `i${index}`, text: `I${index}` }));
  assert.equal(resolveStatusBarItems([contribution("p", many)], "en").length, STATUS_BAR_MAX_ITEMS_PER_PLUGIN);
  assert.equal(resolveStatusBarItems([contribution("p", many, undefined, { activeInWorkspace: false })], "en").length, 0);
  assert.equal(resolveStatusBarItems([contribution("p", many, undefined, { runtime: "declarative" })], "en").length, 0);
  assert.equal(resolveStatusBarItems([contribution("p", [{ id: "a", text: "A" }, { id: "a", text: "B" }])], "en").length, 1);
});

test("when is evaluated against plugin context plus platform and view", () => {
  const items = resolveStatusBarItems([
    contribution("p", [
      { id: "ready", text: "Ready", when: "p.ready" },
      { id: "mac", text: "Mac", when: "platform == \"mac\"" },
      { id: "db", text: "DB", when: "view == \"database\"" },
      { id: "spoof", text: "Spoof", when: "view == \"spoofed\"" },
      { id: "bad", text: "Bad", when: "a ||" },
    ]),
  ], "en");
  const visible = (contexts, hostContext = host) => visibleStatusBarItems(items, contexts, hostContext).map((item) => item.id);
  assert.deepEqual(visible({}), ["mac"]);
  assert.deepEqual(visible({ p: { "p.ready": true } }), ["mac", "ready"].sort((a, b) => (a < b ? -1 : 1)));
  assert.deepEqual(visible({}, { platform: "win", view: "database" }), ["db"]);
  // Host keys win over plugin-provided context values of the same name.
  assert.deepEqual(visible({ p: { view: "spoofed" } }), ["mac"]);
});

test("install preview lists dynamic item ids", () => {
  assert.deepEqual(dynamicStatusBarItemIds({ statusBar: [{ id: "a", dynamic: true }, { id: "b" }, { dynamic: true }] }), ["a"]);
  assert.deepEqual(dynamicStatusBarItemIds({ commands: [] }), []);
  assert.deepEqual(dynamicStatusBarItemIds(undefined), []);
});

test("状态栏只占右侧内容区，不抬高侧栏底部设置", async () => {
  const app = await readSource("../src/App.vue");
  const template = app.slice(app.indexOf("<template>"));
  const column = template.slice(template.indexOf('<div class="app-workspace-column"'), template.indexOf('<AppCommandPaletteHost'));
  assert.match(column, /<AppStatusBar/);
  assert.ok(column.indexOf('<AppStatusBar') > column.indexOf('</main>'));
  assert.equal(template.match(/<AppStatusBar/g)?.length, 1);
  assert.ok(template.indexOf('<AppSidebar') < template.indexOf('<main class="app-workspace-content"'));
});

test("popup windows never render the status bar", async () => {
  const app = await readSource("../src/App.vue");
  const template = app.slice(app.indexOf("<template>"));
  const popup = template.indexOf('<PopupTabWindow v-if="popupWindow" />');
  const frame = template.indexOf('<div v-else class="app-frame"');
  const bar = template.indexOf("<AppStatusBar");
  assert.ok(popup >= 0 && frame > popup, "popup branch precedes the main frame");
  assert.ok(bar > frame, "status bar is inside the main frame branch");
  assert.equal(template.indexOf("<AppStatusBar", bar + 1), -1, "status bar is mounted once");
  assert.match(app, /const AppStatusBar = defineAsyncComponent\(/);
  // The status bar item source keeps plugin text as text (no v-html).
  const component = await readSource("../src/components/layout/AppStatusBar.vue");
  assert.doesNotMatch(component, /v-html/);
  assert.match(component, /pluginContrib\.statusBar\.source/);
});
