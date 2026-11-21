/**
 * Pure model of the `schedules` contribution point (process plugins only).
 * The Go host (backend/plugin/contrib_schedules.go) validates the
 * entries and runs them (schedules_runner.go); the frontend only lists them
 * in the plugin manager and the install preview. Nothing here touches Vue
 * or IPC.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const SCHEDULES_CONTRIBUTION_POINT = "schedules";

// Mirrors `points.schedules` in plugins/api/contribution-points.json.
export const SCHEDULE_LIMITS = {
  maxEntries: 8,
  titleMaxRunes: 48,
  localesMax: 16,
  intervalMinMinutes: 15,
  intervalMaxMinutes: 1440,
} as const;

const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{1,127}$/;

export interface PluginSchedule {
  key: string;
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  action: string;
  intervalMinutes: number;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === "object" && !Array.isArray(value));
}

function title(value: unknown) {
  return typeof value === "string" && value.trim() && [...value.trim()].length <= SCHEDULE_LIMITS.titleMaxRunes ? value.trim() : "";
}

function interval(value: unknown) {
  return typeof value === "number" && Number.isInteger(value) && value >= SCHEDULE_LIMITS.intervalMinMinutes && value <= SCHEDULE_LIMITS.intervalMaxMinutes ? value : 0;
}

function localized(value: unknown): Record<string, string> | undefined {
  if (!isRecord(value)) return undefined;
  const out: Record<string, string> = {};
  for (const [locale, text] of Object.entries(value).slice(0, SCHEDULE_LIMITS.localesMax)) {
    const clean = title(text);
    if (clean) out[locale] = clean;
  }
  return Object.keys(out).length ? out : undefined;
}

export function parseScheduleEntry(entry: unknown, contribution: Pick<PointContribution, "pluginId" | "pluginName">): PluginSchedule | undefined {
  if (!isRecord(entry)) return undefined;
  const id = typeof entry.id === "string" ? entry.id.trim() : "";
  const name = title(entry.title);
  const action = typeof entry.action === "string" ? entry.action.trim() : "";
  const minutes = interval(entry.intervalMinutes);
  if (!ID_PATTERN.test(id) || !name || !action || !minutes) return undefined;
  const schedule: PluginSchedule = { key: `${contribution.pluginId}/${id}`, pluginId: contribution.pluginId, pluginName: contribution.pluginName, id, title: name, action, intervalMinutes: minutes };
  const titles = localized(entry.titleLocalized);
  if (titles) schedule.titleLocalized = titles;
  return schedule;
}

/** Schedules of enabled process plugins, keyed by plugin (inactive workspaces included: the list is informational). */
export function resolveSchedules(contributions: readonly PointContribution[]): PluginSchedule[] {
  const result: PluginSchedule[] = [];
  const seen = new Set<string>();
  for (const contribution of [...contributions].sort((left, right) => left.pluginId.localeCompare(right.pluginId))) {
    if (contribution.runtime !== "process" || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    const entries = Array.isArray(contribution.point?.entries) ? contribution.point.entries : [];
    for (const entry of entries.slice(0, SCHEDULE_LIMITS.maxEntries)) {
      const schedule = parseScheduleEntry(entry, contribution);
      if (!schedule || seen.has(schedule.key)) continue;
      seen.add(schedule.key);
      result.push(schedule);
    }
  }
  return result;
}

export function scheduleTitle(schedule: Pick<PluginSchedule, "title" | "titleLocalized">, locale: string | readonly string[]) {
  return resolvePluginTitle({ title: schedule.title, titleLocalized: schedule.titleLocalized }, locale);
}

/** Install preview: what runs in the background and how often. */
export function schedulePreviewSummary(contributes: unknown): { title: string; titleLocalized?: Record<string, string>; intervalMinutes: number }[] {
  const entries = isRecord(contributes) ? contributes[SCHEDULES_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, SCHEDULE_LIMITS.maxEntries).flatMap((entry) => {
    if (!isRecord(entry)) return [];
    const name = title(entry.title) || (typeof entry.id === "string" ? entry.id.slice(0, 32) : "");
    const minutes = interval(entry.intervalMinutes);
    if (!name || !minutes) return [];
    const titles = localized(entry.titleLocalized);
    return [titles ? { title: name, titleLocalized: titles, intervalMinutes: minutes } : { title: name, intervalMinutes: minutes }];
  });
}

/** "every 15 min" / "every 2 h" style interval, as [value, unit]. */
export function scheduleIntervalParts(minutes: number): [number, "minutes" | "hours"] {
  return minutes % 60 === 0 ? [minutes / 60, "hours"] : [minutes, "minutes"];
}
