/**
 * Pure model of the `dashboardWidgets` contribution point (process plugins
 * only). The Go host (backend/plugin/contrib_dashboard_widgets.go)
 * validates the entries; they are re-checked here. View widgets render the
 * plugin's own view in a sandboxed PluginViewFrame (variant "widget"); data
 * widgets call a `read`-risk action through plugin_host_dashboard_widget_data
 * and render plain text only. Hidden/ordered widgets are workspace-scoped
 * runtime state (`app_dashboard_plugin_widgets_v1`). Nothing here touches Vue
 * or IPC.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const DASHBOARD_WIDGETS_CONTRIBUTION_POINT = "dashboardWidgets";
export const DASHBOARD_WIDGETS_STORAGE_KEY = "app_dashboard_plugin_widgets_v1";

// Mirrors `points.dashboardWidgets` in plugins/api/contribution-points.json
// (vue-app/scripts/dashboard-widgets-contributions.test.mjs keeps them in sync).
export const DASHBOARD_WIDGET_MAX_ENTRIES = 4;
export const DASHBOARD_WIDGET_TITLE_MAX_RUNES = 48;
export const DASHBOARD_WIDGET_LOCALES_MAX = 16;
export const DASHBOARD_WIDGET_REFRESH_MIN_SECONDS = 60;
export const DASHBOARD_WIDGET_REFRESH_MAX_SECONDS = 3600;
export const DASHBOARD_WIDGET_ITEMS_MAX = 8;
export const DASHBOARD_WIDGET_LABEL_MAX_RUNES = 48;
export const DASHBOARD_WIDGET_VALUE_MAX_RUNES = 64;
export const DASHBOARD_WIDGET_HINT_MAX_RUNES = 256;
export const DASHBOARD_WIDGET_FOOTNOTE_MAX_RUNES = 128;
export const DASHBOARD_WIDGET_HOST_CARDS_MAX = 12;
export const DASHBOARD_WIDGET_LIVE_FRAMES_MAX = 6;
export const DASHBOARD_WIDGET_KINDS = ["view", "data"] as const;
export const DASHBOARD_WIDGET_SIZES = ["small", "medium"] as const;
export const DASHBOARD_WIDGET_SEVERITIES = ["normal", "success", "warning", "danger"] as const;
/** Layout records retained (order + hidden). */
export const DASHBOARD_WIDGET_LAYOUT_MAX_RECORDS = 256;

export type DashboardWidgetKind = (typeof DASHBOARD_WIDGET_KINDS)[number];
export type DashboardWidgetSize = (typeof DASHBOARD_WIDGET_SIZES)[number];
export type DashboardWidgetSeverity = (typeof DASHBOARD_WIDGET_SEVERITIES)[number];

const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const REFERENCE_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,3}$/;
// eslint-disable-next-line no-control-regex
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/g;

export interface DashboardWidget {
  /** `<pluginId>/<widgetId>`. */
  key: string;
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  kind: DashboardWidgetKind;
  view?: string;
  action?: string;
  size: DashboardWidgetSize;
  /** 0 means manual refresh only. */
  refreshSeconds: number;
  command?: string;
}

export interface DashboardWidgetLayout {
  version: 1;
  /** Widget keys in the user's order; unknown keys stay inert. */
  order: string[];
  hidden: string[];
}

export interface DashboardWidgetDataItem {
  label: string;
  value: string;
  severity: DashboardWidgetSeverity;
  hint?: string;
}

export interface DashboardWidgetData {
  items: DashboardWidgetDataItem[];
  footnote?: string;
  updatedAt?: string;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function cleanText(value: unknown, maxRunes: number): string {
  if (typeof value !== "string") return "";
  return Array.from(value.replace(UNSAFE_TEXT, "").trim()).slice(0, maxRunes).join("");
}

function localized(value: unknown): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  let count = 0;
  for (const [locale, text] of Object.entries(record)) {
    if (count >= DASHBOARD_WIDGET_LOCALES_MAX) break;
    const clean = cleanText(text, DASHBOARD_WIDGET_TITLE_MAX_RUNES);
    if (!LOCALE_PATTERN.test(locale) || !clean) continue;
    result[locale] = clean;
    count += 1;
  }
  return count > 0 ? result : undefined;
}

export function dashboardWidgetKey(pluginId: string, widgetId: string) {
  return `${pluginId}/${widgetId}`;
}

/** Re-validates one entry; undefined when anything is out of contract. */
export function parseDashboardWidgetEntry(value: unknown, plugin: { pluginId: string; pluginName: string }): DashboardWidget | undefined {
  const record = objectValue(value);
  if (!record || typeof record.id !== "string" || !ID_PATTERN.test(record.id)) return undefined;
  const kind = record.kind;
  if (kind !== "view" && kind !== "data") return undefined;
  const size = record.size === undefined ? "small" : record.size;
  if (size !== "small" && size !== "medium") return undefined;
  const widget: DashboardWidget = {
    key: dashboardWidgetKey(plugin.pluginId, record.id),
    pluginId: plugin.pluginId,
    pluginName: cleanText(plugin.pluginName, DASHBOARD_WIDGET_TITLE_MAX_RUNES) || plugin.pluginId,
    id: record.id,
    title: cleanText(record.title, DASHBOARD_WIDGET_TITLE_MAX_RUNES) || record.id,
    kind,
    size,
    refreshSeconds: 0,
  };
  const titleLocalized = localized(record.titleLocalized);
  if (titleLocalized) widget.titleLocalized = titleLocalized;
  if (kind === "view") {
    if (typeof record.view !== "string" || !REFERENCE_PATTERN.test(record.view) || record.action !== undefined) return undefined;
    widget.view = record.view;
  } else {
    if (typeof record.action !== "string" || !REFERENCE_PATTERN.test(record.action) || record.view !== undefined) return undefined;
    widget.action = record.action;
    const refresh = record.refreshSeconds;
    if (typeof refresh !== "number" || !Number.isInteger(refresh)) return undefined;
    if (refresh !== 0 && (refresh < DASHBOARD_WIDGET_REFRESH_MIN_SECONDS || refresh > DASHBOARD_WIDGET_REFRESH_MAX_SECONDS)) return undefined;
    widget.refreshSeconds = refresh;
  }
  if (record.command !== undefined) {
    if (typeof record.command !== "string" || !REFERENCE_PATTERN.test(record.command)) return undefined;
    widget.command = record.command;
  }
  return widget;
}

/**
 * Widgets from process plugins effective in this workspace, sorted by
 * pluginId then declaration order; at most four per plugin.
 */
export function resolveDashboardWidgets(contributions: readonly PointContribution[]): DashboardWidget[] {
  const result: DashboardWidget[] = [];
  const seen = new Set<string>();
  const sorted = [...contributions].sort((left, right) => left.pluginId.localeCompare(right.pluginId));
  for (const contribution of sorted) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    const entries = Array.isArray(contribution.point?.entries) ? contribution.point.entries : [];
    let count = 0;
    for (const entry of entries) {
      if (count >= DASHBOARD_WIDGET_MAX_ENTRIES) break;
      const widget = parseDashboardWidgetEntry(entry, contribution);
      if (!widget || seen.has(widget.key)) continue;
      seen.add(widget.key);
      count += 1;
      result.push(widget);
    }
  }
  return result;
}

export function dashboardWidgetTitle(widget: DashboardWidget, locale: string | readonly string[]) {
  return resolvePluginTitle({ title: widget.title, titleLocalized: widget.titleLocalized }, locale);
}

export function emptyDashboardWidgetLayout(): DashboardWidgetLayout {
  return { version: 1, order: [], hidden: [] };
}

function validLayoutKey(key: unknown): key is string {
  if (typeof key !== "string") return false;
  const index = key.lastIndexOf("/");
  return index > 0 && PLUGIN_ID_PATTERN.test(key.slice(0, index)) && ID_PATTERN.test(key.slice(index + 1));
}

function uniqueKeys(value: unknown): string[] {
  return Array.isArray(value) ? [...new Set(value.filter(validLayoutKey))].slice(-DASHBOARD_WIDGET_LAYOUT_MAX_RECORDS) : [];
}

export function normalizeDashboardWidgetLayout(value: unknown): DashboardWidgetLayout {
  const record = objectValue(value);
  if (!record || record.version !== 1) return emptyDashboardWidgetLayout();
  return { version: 1, order: uniqueKeys(record.order), hidden: uniqueKeys(record.hidden) };
}

/** Widgets in the user's order: ordered keys first, then new widgets in resolve order. */
export function orderDashboardWidgets(widgets: readonly DashboardWidget[], layout: DashboardWidgetLayout): DashboardWidget[] {
  const rank = new Map(layout.order.map((key, index) => [key, index]));
  return widgets
    .map((widget, index) => ({ widget, index }))
    .sort((left, right) => {
      const leftRank = rank.get(left.widget.key) ?? Number.MAX_SAFE_INTEGER;
      const rightRank = rank.get(right.widget.key) ?? Number.MAX_SAFE_INTEGER;
      return leftRank - rightRank || left.index - right.index;
    })
    .map((item) => item.widget);
}

/** Visible widgets (ordered, not hidden, at most `max`) plus the number that did not fit. */
export function arrangeDashboardWidgets(
  widgets: readonly DashboardWidget[],
  layout: DashboardWidgetLayout,
  max = DASHBOARD_WIDGET_HOST_CARDS_MAX,
): { shown: DashboardWidget[]; overflow: number } {
  const hidden = new Set(layout.hidden);
  const visible = orderDashboardWidgets(widgets, layout).filter((widget) => !hidden.has(widget.key));
  const limit = Math.max(0, max);
  return { shown: visible.slice(0, limit), overflow: Math.max(0, visible.length - limit) };
}

export function setDashboardWidgetHidden(layout: DashboardWidgetLayout, key: string, hidden: boolean): DashboardWidgetLayout {
  if (!validLayoutKey(key)) return layout;
  const next = layout.hidden.filter((item) => item !== key);
  if (hidden) next.push(key);
  return normalizeDashboardWidgetLayout({ ...layout, hidden: next });
}

/**
 * Moves one widget by `delta` positions within the current full order
 * (`widgets` is the resolved list); the resulting order is stored in full.
 */
export function moveDashboardWidget(
  widgets: readonly DashboardWidget[],
  layout: DashboardWidgetLayout,
  key: string,
  delta: number,
): DashboardWidgetLayout {
  const keys = orderDashboardWidgets(widgets, layout).map((widget) => widget.key);
  const from = keys.indexOf(key);
  if (from < 0 || !Number.isInteger(delta) || delta === 0) return layout;
  const to = Math.min(keys.length - 1, Math.max(0, from + delta));
  if (to === from) return layout;
  keys.splice(to, 0, ...keys.splice(from, 1));
  const known = new Set(keys);
  // Keep ordering hints for widgets that are currently unavailable (disabled plugins).
  const inert = layout.order.filter((item) => !known.has(item));
  return normalizeDashboardWidgetLayout({ ...layout, order: [...inert, ...keys] });
}

/** Re-validates a data widget result: plain text only, at most eight items. */
export function parseDashboardWidgetData(value: unknown): DashboardWidgetData | undefined {
  const record = objectValue(value);
  if (!record || !Array.isArray(record.items) || record.items.length > DASHBOARD_WIDGET_ITEMS_MAX) return undefined;
  const items: DashboardWidgetDataItem[] = [];
  for (const raw of record.items) {
    const item = objectValue(raw);
    if (!item) return undefined;
    const label = cleanText(item.label, DASHBOARD_WIDGET_LABEL_MAX_RUNES);
    const value = cleanText(typeof item.value === "number" || typeof item.value === "boolean" ? String(item.value) : item.value, DASHBOARD_WIDGET_VALUE_MAX_RUNES);
    if (!label) return undefined;
    const severity = (DASHBOARD_WIDGET_SEVERITIES as readonly string[]).includes(item.severity as string)
      ? (item.severity as DashboardWidgetSeverity)
      : "normal";
    const parsed: DashboardWidgetDataItem = { label, value, severity };
    const hint = cleanText(item.hint, DASHBOARD_WIDGET_HINT_MAX_RUNES);
    if (hint) parsed.hint = hint;
    items.push(parsed);
  }
  const data: DashboardWidgetData = { items };
  const footnote = cleanText(record.footnote, DASHBOARD_WIDGET_FOOTNOTE_MAX_RUNES);
  if (footnote) data.footnote = footnote;
  const updatedAt = cleanText(record.updatedAt, 64);
  if (updatedAt && !Number.isNaN(Date.parse(updatedAt))) data.updatedAt = updatedAt;
  return data;
}

/**
 * Least-recently-visible tracker for live widget frames. `touch` marks a
 * widget visible and returns the keys that must drop their frame so that at
 * most `max` frames stay alive.
 */
export function createLiveFrameTracker(max = DASHBOARD_WIDGET_LIVE_FRAMES_MAX) {
  const live: string[] = [];
  return {
    touch(key: string): string[] {
      const index = live.indexOf(key);
      if (index >= 0) live.splice(index, 1);
      live.push(key);
      return live.length > max ? live.splice(0, live.length - max) : [];
    },
    release(key: string) {
      const index = live.indexOf(key);
      if (index >= 0) live.splice(index, 1);
    },
    has(key: string) {
      return live.includes(key);
    },
    keys() {
      return [...live];
    },
  };
}

/**
 * Whether a data widget should refresh now: only while visible (card in view,
 * dashboard route active, document visible), never faster than 60 s, and only
 * when the widget declares automatic refresh.
 */
export function shouldRefreshDashboardWidget(input: {
  refreshSeconds: number;
  lastFetchedAt: number | undefined;
  now: number;
  visible: boolean;
}): boolean {
  if (!input.visible) return false;
  if (input.lastFetchedAt === undefined) return true;
  if (input.refreshSeconds <= 0) return false;
  const interval = Math.max(DASHBOARD_WIDGET_REFRESH_MIN_SECONDS, input.refreshSeconds) * 1000;
  return input.now - input.lastFetchedAt >= interval;
}

/** Install preview: widget count and the read actions data widgets run while visible. */
export function dashboardWidgetPreviewSummary(contributes: unknown): { count: number; actions: string[] } | undefined {
  const list = objectValue(contributes)?.dashboardWidgets;
  if (!Array.isArray(list) || list.length === 0) return undefined;
  const actions: string[] = [];
  let count = 0;
  for (const entry of list.slice(0, DASHBOARD_WIDGET_MAX_ENTRIES)) {
    const record = objectValue(entry);
    if (!record) continue;
    count += 1;
    if (record.kind === "data" && typeof record.action === "string" && REFERENCE_PATTERN.test(record.action) && !actions.includes(record.action)) {
      actions.push(record.action);
    }
  }
  return count ? { count, actions } : undefined;
}
