import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { URL } from "node:url";
import {
  DASHBOARD_WIDGET_FOOTNOTE_MAX_RUNES,
  DASHBOARD_WIDGET_HINT_MAX_RUNES,
  DASHBOARD_WIDGET_HOST_CARDS_MAX,
  DASHBOARD_WIDGET_ITEMS_MAX,
  DASHBOARD_WIDGET_KINDS,
  DASHBOARD_WIDGET_LABEL_MAX_RUNES,
  DASHBOARD_WIDGET_LIVE_FRAMES_MAX,
  DASHBOARD_WIDGET_LOCALES_MAX,
  DASHBOARD_WIDGET_MAX_ENTRIES,
  DASHBOARD_WIDGET_REFRESH_MAX_SECONDS,
  DASHBOARD_WIDGET_REFRESH_MIN_SECONDS,
  DASHBOARD_WIDGET_SEVERITIES,
  DASHBOARD_WIDGET_SIZES,
  DASHBOARD_WIDGET_TITLE_MAX_RUNES,
  DASHBOARD_WIDGET_VALUE_MAX_RUNES,
  DASHBOARD_WIDGETS_STORAGE_KEY,
  arrangeDashboardWidgets,
  createLiveFrameTracker,
  dashboardWidgetPreviewSummary,
  emptyDashboardWidgetLayout,
  moveDashboardWidget,
  normalizeDashboardWidgetLayout,
  parseDashboardWidgetData,
  parseDashboardWidgetEntry,
  resolveDashboardWidgets,
  setDashboardWidgetHidden,
  shouldRefreshDashboardWidget,
} from "../src/domain/plugins/dashboard-widgets.ts";

const readSource = (path) => readFile(new URL(path, import.meta.url), "utf8");

function contribution(pluginId, entries, overrides = {}) {
  return {
    pluginId,
    pluginName: `${pluginId} name`,
    version: "1.0.0",
    source: "local",
    runtime: "process",
    activeInWorkspace: true,
    point: { entries, issues: [] },
    ...overrides,
  };
}

const dataWidget = (id, overrides = {}) => ({ id, title: `Data ${id}`, kind: "data", action: "dashboard.summary", size: "small", refreshSeconds: 120, ...overrides });
const viewWidget = (id, overrides = {}) => ({ id, title: `View ${id}`, kind: "view", view: "widget", size: "medium", refreshSeconds: 0, ...overrides });

test("常量与贡献点契约同步", async () => {
  for (const path of ["../../plugins/api/contribution-points.json", "../src/domain/plugins/contribution-points.json"]) {
    const contract = JSON.parse(await readSource(path)).points.dashboardWidgets;
    assert.equal(contract.status, "enabled");
    assert.deepEqual(contract.kinds, ["process"]);
    assert.equal(contract.maxEntries, DASHBOARD_WIDGET_MAX_ENTRIES);
    const { limits, lists } = contract;
    assert.equal(limits.titleMaxRunes, DASHBOARD_WIDGET_TITLE_MAX_RUNES);
    assert.equal(limits.localesMax, DASHBOARD_WIDGET_LOCALES_MAX);
    assert.equal(limits.refreshMinSeconds, DASHBOARD_WIDGET_REFRESH_MIN_SECONDS);
    assert.equal(limits.refreshMaxSeconds, DASHBOARD_WIDGET_REFRESH_MAX_SECONDS);
    assert.equal(limits.itemsMax, DASHBOARD_WIDGET_ITEMS_MAX);
    assert.equal(limits.labelMaxRunes, DASHBOARD_WIDGET_LABEL_MAX_RUNES);
    assert.equal(limits.valueMaxRunes, DASHBOARD_WIDGET_VALUE_MAX_RUNES);
    assert.equal(limits.hintMaxRunes, DASHBOARD_WIDGET_HINT_MAX_RUNES);
    assert.equal(limits.footnoteMaxRunes, DASHBOARD_WIDGET_FOOTNOTE_MAX_RUNES);
    assert.equal(limits.hostCardsMax, DASHBOARD_WIDGET_HOST_CARDS_MAX);
    assert.equal(limits.liveFramesMax, DASHBOARD_WIDGET_LIVE_FRAMES_MAX);
    assert.deepEqual(lists.kinds, [...DASHBOARD_WIDGET_KINDS]);
    assert.deepEqual(lists.sizes, [...DASHBOARD_WIDGET_SIZES]);
    assert.deepEqual(lists.severities, [...DASHBOARD_WIDGET_SEVERITIES]);
  }
});

test("条目再校验", () => {
  const plugin = { pluginId: "acme", pluginName: "Acme" };
  assert.equal(parseDashboardWidgetEntry(dataWidget("engine"), plugin).refreshSeconds, 120);
  assert.equal(parseDashboardWidgetEntry(viewWidget("live"), plugin).view, "widget");
  for (const value of [
    dataWidget("engine", { refreshSeconds: 30 }),
    dataWidget("engine", { refreshSeconds: 3601 }),
    dataWidget("engine", { view: "widget" }),
    viewWidget("live", { action: "x" }),
    viewWidget("live", { size: "large" }),
    dataWidget("engine", { kind: "chart" }),
    dataWidget("Bad Id"),
  ]) {
    assert.equal(parseDashboardWidgetEntry(value, plugin), undefined);
  }
  assert.equal(parseDashboardWidgetEntry(dataWidget("engine", { refreshSeconds: 0 }), plugin).refreshSeconds, 0);
});

test("resolve 只接受 process 且在当前工作区生效的插件，每插件最多 4 个", () => {
  const widgets = ["a", "b", "c", "d", "e"].map((id) => dataWidget(id));
  const resolved = resolveDashboardWidgets([
    contribution("zeta", [viewWidget("x")]),
    contribution("acme", widgets),
    contribution("decl", [dataWidget("y")], { runtime: "declarative" }),
    contribution("idle", [dataWidget("z")], { activeInWorkspace: false }),
  ]);
  assert.deepEqual(resolved.map((widget) => widget.key), ["acme/a", "acme/b", "acme/c", "acme/d", "zeta/x"]);
});

test("排列：顺序、隐藏与最多 12 张卡片", () => {
  const widgets = resolveDashboardWidgets(
    Array.from({ length: 4 }, (_, plugin) => contribution(`p${plugin}`, ["a", "b", "c", "d"].map((id) => dataWidget(id)))),
  );
  assert.equal(widgets.length, 16);
  let layout = emptyDashboardWidgetLayout();
  let arranged = arrangeDashboardWidgets(widgets, layout);
  assert.equal(arranged.shown.length, DASHBOARD_WIDGET_HOST_CARDS_MAX);
  assert.equal(arranged.overflow, 4);
  layout = setDashboardWidgetHidden(layout, "p0/a", true);
  layout = moveDashboardWidget(widgets, layout, "p3/d", -100);
  arranged = arrangeDashboardWidgets(widgets, layout);
  assert.equal(arranged.shown[0].key, "p3/d");
  assert.ok(!arranged.shown.some((widget) => widget.key === "p0/a"));
  assert.equal(arranged.overflow, 3);
  layout = setDashboardWidgetHidden(layout, "p0/a", false);
  assert.deepEqual(layout.hidden, []);
  assert.deepEqual(normalizeDashboardWidgetLayout({ version: 1, order: ["bad", "p0/a", "p0/a"], hidden: [1] }), { version: 1, order: ["p0/a"], hidden: [] });
  assert.deepEqual(normalizeDashboardWidgetLayout(null), emptyDashboardWidgetLayout());
});

test("数据结果：纯文本、最多 8 项", () => {
  const data = parseDashboardWidgetData({ items: [{ label: "Containers", value: 3, severity: "success", hint: "a\u202eb" }, { label: "Up", value: true, severity: "odd" }], footnote: "via socket" });
  assert.deepEqual(data.items[0], { label: "Containers", value: "3", severity: "success", hint: "ab" });
  assert.equal(data.items[1].severity, "normal");
  assert.equal(parseDashboardWidgetData({ items: Array.from({ length: 9 }, () => ({ label: "a", value: "b" })) }), undefined);
  assert.equal(parseDashboardWidgetData({ items: [{ value: "b" }] }), undefined);
  assert.equal(parseDashboardWidgetData([]), undefined);
});

test("活动 frame LRU 最多 6 个", () => {
  const tracker = createLiveFrameTracker();
  for (let index = 0; index < 6; index += 1) assert.deepEqual(tracker.touch(`p/w${index}`), []);
  tracker.touch("p/w0");
  assert.deepEqual(tracker.touch("p/w6"), ["p/w1"]);
  assert.equal(tracker.keys().length, DASHBOARD_WIDGET_LIVE_FRAMES_MAX);
  tracker.release("p/w0");
  assert.equal(tracker.has("p/w0"), false);
});

test("刷新只在可见时进行且间隔不低于 60 秒", () => {
  const base = { refreshSeconds: 60, now: 100_000, visible: true };
  assert.equal(shouldRefreshDashboardWidget({ ...base, lastFetchedAt: undefined }), true);
  assert.equal(shouldRefreshDashboardWidget({ ...base, lastFetchedAt: undefined, visible: false }), false);
  assert.equal(shouldRefreshDashboardWidget({ ...base, lastFetchedAt: 50_000 }), false);
  assert.equal(shouldRefreshDashboardWidget({ ...base, lastFetchedAt: 40_000 }), true);
  assert.equal(shouldRefreshDashboardWidget({ ...base, refreshSeconds: 0, lastFetchedAt: 0 }), false);
  assert.equal(shouldRefreshDashboardWidget({ ...base, refreshSeconds: 1, lastFetchedAt: 50_000 }), false);
});

test("安装预览摘要", () => {
  assert.deepEqual(dashboardWidgetPreviewSummary({ dashboardWidgets: [dataWidget("a"), viewWidget("b"), dataWidget("c")] }), { count: 3, actions: ["dashboard.summary"] });
  assert.equal(dashboardWidgetPreviewSummary({}), undefined);
});

test("接线：卡片无 v-html、frame 为 widget 变体、工作区删除键、拖放只用于页面", async () => {
  const card = await readSource("../src/components/dashboard/PluginWidgetCard.vue");
  assert.doesNotMatch(card, /v-html|innerHTML/);
  assert.match(card, /variant="widget"/);
  assert.match(card, /IntersectionObserver/);
  assert.match(card, /shouldRefreshDashboardWidget\(/);
  const frame = await readSource("../src/components/plugins/PluginViewFrame.vue");
  assert.match(frame, /!props\.document && !isWidget\.value/);
  assert.match(frame, /surface: "widget"/);
  const deletion = await readSource("../src/services/workspace-state-deletion.ts");
  assert.ok(deletion.includes(`"${DASHBOARD_WIDGETS_STORAGE_KEY}"`));
  const service = await readSource("../src/services/dashboard-widget-service.ts");
  assert.match(service, /plugin_host_dashboard_widget_data/);
  const dashboard = await readSource("../src/views/DashboardView.vue");
  assert.match(dashboard, /<PluginDashboardWidgets :active="routeActive" \/>/);
  assert.match(dashboard, /onDeactivated/);
});

test("隐藏时取消请求会复位 loading 并在重新可见时重新拉取", async () => {
  const card = await readSource("../src/components/dashboard/PluginWidgetCard.vue");
  const start = card.indexOf("function cancelRequest()");
  assert.ok(start >= 0);
  const body = card.slice(start, card.indexOf("\n}\n", start));
  assert.match(body, /if \(!request\) return;/);
  assert.match(body, /loading\.value = false;/);
  assert.match(body, /lastFetchedAt = undefined;/);
  const load = card.slice(card.indexOf("async function load("), card.indexOf("function refresh()"));
  assert.ok(load.indexOf("cancelRequest();") < load.indexOf("lastFetchedAt = Date.now();"));
});
