/**
 * Pure model of the `statusBar` contribution point. The Go host
 * (backend/plugin/contrib_statusbar.go) validates declared items
 * and attaches dynamic overrides as the point's `runtime` field; entries are
 * still untrusted here, so every field is re-checked and text is truncated
 * to the contract bound. Nothing here touches IPC or Vue.
 */
import { evaluateWhen, type PluginContextValues } from "@/domain/commands/when";
import type { PointContribution } from "@/domain/plugins/contributions";

export const STATUS_BAR_CONTRIBUTION_POINT = "statusBar";

// Mirrors `points.statusBar` in plugins/api/contribution-points.json
// (vue-app/scripts/status-bar-contributions.test.mjs keeps them in sync).
export const STATUS_BAR_TEXT_MAX_RUNES = 48;
export const STATUS_BAR_TOOLTIP_MAX_RUNES = 256;
export const STATUS_BAR_PRIORITY_MIN = -1000;
export const STATUS_BAR_PRIORITY_MAX = 1000;
export const STATUS_BAR_MAX_ITEMS_PER_PLUGIN = 8;
const PRIORITY_MIN = STATUS_BAR_PRIORITY_MIN;
const PRIORITY_MAX = STATUS_BAR_PRIORITY_MAX;
const MAX_ITEMS_PER_PLUGIN = STATUS_BAR_MAX_ITEMS_PER_PLUGIN;
const ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
// Control characters, line/paragraph separators and bidi overrides/isolates.
// eslint-disable-next-line no-control-regex
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069]/g;
// eslint-disable-next-line no-control-regex
const UNSAFE_TOOLTIP = /[\u0000-\u0009\u000b-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069]/g;

export type StatusBarAlignment = "left" | "right";
export type StatusBarSeverity = "normal" | "warning" | "error";

export interface StatusBarItemDeclaration {
  id: string;
  text?: string;
  textLocalized?: Record<string, string>;
  tooltip?: string;
  tooltipLocalized?: Record<string, string>;
  icon?: string;
  command?: string;
  alignment: StatusBarAlignment;
  priority: number;
  when?: string;
  dynamic: boolean;
}

export interface StatusBarRuntimeOverride {
  text?: string;
  tooltip?: string;
  severity?: StatusBarSeverity;
  busy?: boolean;
  visible?: boolean;
}

/** One item ready to render (before `when` evaluation). */
export interface StatusBarItem {
  key: string;
  pluginId: string;
  pluginName: string;
  id: string;
  text: string;
  tooltip: string;
  icon?: string;
  /** Local command id; executed as `plugin:<pluginId>:<command>`. */
  command?: string;
  alignment: StatusBarAlignment;
  priority: number;
  severity: StatusBarSeverity;
  busy: boolean;
  when?: string;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

/** Truncates to `max` code points, marking the cut with an ellipsis. */
export function truncateStatusBarText(text: string, max = STATUS_BAR_TEXT_MAX_RUNES): string {
  const runes = Array.from(text);
  if (runes.length <= max) return text;
  return `${runes.slice(0, Math.max(0, max - 1)).join("")}…`;
}

/** Single-line, control- and bidi-free text within the contract bound. */
export function sanitizeStatusBarText(value: unknown): string {
  const text = stringValue(value)?.replace(UNSAFE_TEXT, "").trim() ?? "";
  return truncateStatusBarText(text);
}

export function sanitizeStatusBarTooltip(value: unknown): string {
  const text = stringValue(value)?.replace(UNSAFE_TOOLTIP, "").trim() ?? "";
  return truncateStatusBarText(text, STATUS_BAR_TOOLTIP_MAX_RUNES);
}

function localizedMap(value: unknown): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  for (const [locale, text] of Object.entries(record).slice(0, 16)) {
    if (typeof text === "string" && locale.trim()) result[locale] = text;
  }
  return Object.keys(result).length ? result : undefined;
}

export function parseStatusBarItemDeclaration(raw: unknown): StatusBarItemDeclaration | undefined {
  const record = objectValue(raw);
  const id = stringValue(record?.id) ?? "";
  if (!record || !ID_PATTERN.test(id)) return undefined;
  const priority = typeof record.priority === "number" && Number.isInteger(record.priority)
    ? Math.min(PRIORITY_MAX, Math.max(PRIORITY_MIN, record.priority))
    : 0;
  const text = stringValue(record.text);
  const dynamic = record.dynamic === true;
  if (!text && !dynamic) return undefined;
  const when = stringValue(record.when)?.trim();
  const command = stringValue(record.command)?.trim();
  const icon = stringValue(record.icon)?.trim();
  const textLocalized = localizedMap(record.textLocalized);
  const tooltipLocalized = localizedMap(record.tooltipLocalized);
  return {
    id,
    alignment: record.alignment === "right" ? "right" : "left",
    priority,
    dynamic,
    ...(text ? { text } : {}),
    ...(textLocalized ? { textLocalized } : {}),
    ...(typeof record.tooltip === "string" ? { tooltip: record.tooltip } : {}),
    ...(tooltipLocalized ? { tooltipLocalized } : {}),
    ...(icon ? { icon } : {}),
    ...(command ? { command } : {}),
    ...(when ? { when } : {}),
  };
}

/** Parses the host `runtime` overlay: `{ items: { <id>: override } }`. */
export function parseStatusBarRuntime(raw: unknown): Record<string, StatusBarRuntimeOverride> {
  const items = objectValue(objectValue(raw)?.items);
  const result: Record<string, StatusBarRuntimeOverride> = {};
  if (!items) return result;
  for (const [id, value] of Object.entries(items)) {
    const record = objectValue(value);
    if (!record || !ID_PATTERN.test(id)) continue;
    const override: StatusBarRuntimeOverride = {};
    if (typeof record.text === "string") override.text = record.text;
    if (typeof record.tooltip === "string") override.tooltip = record.tooltip;
    if (record.severity === "normal" || record.severity === "warning" || record.severity === "error") {
      override.severity = record.severity;
    }
    if (typeof record.busy === "boolean") override.busy = record.busy;
    if (typeof record.visible === "boolean") override.visible = record.visible;
    result[id] = override;
  }
  return result;
}

function localized(base: string | undefined, values: Record<string, string> | undefined, locale: string) {
  return values?.[locale] ?? values?.[locale.split("-")[0] ?? ""] ?? base;
}

/**
 * Merges declared items with their runtime overrides. A pure-dynamic item
 * (no declared text) stays hidden until the plugin sets text; `visible: false`
 * hides any dynamic item.
 */
export function resolveStatusBarItems(contributions: readonly PointContribution[], locale: string): StatusBarItem[] {
  const items: StatusBarItem[] = [];
  for (const contribution of contributions) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace) continue;
    const overrides = parseStatusBarRuntime(contribution.point.runtime);
    const seen = new Set<string>();
    for (const raw of contribution.point.entries.slice(0, MAX_ITEMS_PER_PLUGIN)) {
      const declaration = parseStatusBarItemDeclaration(raw);
      if (!declaration || seen.has(declaration.id)) continue;
      seen.add(declaration.id);
      const override = declaration.dynamic ? overrides[declaration.id] ?? {} : {};
      if (override.visible === false) continue;
      const text = sanitizeStatusBarText(override.text ?? localized(declaration.text, declaration.textLocalized, locale));
      if (!text) continue;
      const tooltip = sanitizeStatusBarTooltip(
        override.tooltip ?? localized(declaration.tooltip, declaration.tooltipLocalized, locale),
      );
      items.push({
        key: `${contribution.pluginId}:${declaration.id}`,
        pluginId: contribution.pluginId,
        pluginName: contribution.pluginName,
        id: declaration.id,
        text,
        tooltip,
        alignment: declaration.alignment,
        priority: declaration.priority,
        severity: override.severity ?? "normal",
        busy: override.busy === true,
        ...(declaration.icon ? { icon: declaration.icon } : {}),
        ...(declaration.command ? { command: declaration.command } : {}),
        ...(declaration.when ? { when: declaration.when } : {}),
      });
    }
  }
  return items;
}

/** Left before right, then higher priority first, then plugin id and item id. */
export function compareStatusBarItems(left: StatusBarItem, right: StatusBarItem): number {
  if (left.alignment !== right.alignment) return left.alignment === "left" ? -1 : 1;
  if (left.priority !== right.priority) return right.priority - left.priority;
  if (left.pluginId !== right.pluginId) return left.pluginId < right.pluginId ? -1 : 1;
  return left.id < right.id ? -1 : left.id > right.id ? 1 : 0;
}

export interface StatusBarHostContext {
  platform: string;
  view: string;
}

/**
 * Items whose `when` holds against the plugin's own context plus the host keys
 * `platform` and `view` (host keys win), in display order.
 */
export function visibleStatusBarItems(
  items: readonly StatusBarItem[],
  pluginContexts: Readonly<Record<string, PluginContextValues>>,
  host: StatusBarHostContext,
): StatusBarItem[] {
  return items
    .filter((item) => !item.when || evaluateWhen(item.when, {
      ...(pluginContexts[item.pluginId] ?? {}),
      platform: host.platform,
      view: host.view,
    }))
    .sort(compareStatusBarItems);
}

/** Dynamic item ids of an install-preview manifest (`contributes.statusBar`). */
export function dynamicStatusBarItemIds(contributes: unknown): string[] {
  const entries = objectValue(contributes)?.statusBar;
  if (!Array.isArray(entries)) return [];
  return entries.flatMap((entry) => {
    const record = objectValue(entry);
    const id = stringValue(record?.id);
    return record?.dynamic === true && id ? [id] : [];
  });
}
