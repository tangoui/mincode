/**
 * Pure model of the `tasks` contribution point (process plugins with the
 * `shell` capability). The Go host lists the tasks of one project
 * (plugin_host_tasks_list: static tasks whose `when.files` exist plus the
 * detected tasks it cached), resolves, re-validates and fingerprints a task at
 * every run (tasks_launch.go) and only starts it after the user confirmed the
 * exact spec for that project. Everything here is untrusted display data: the
 * WebView names a task only by `(pluginId, taskKey, projectRoot)`. Nothing here
 * touches IPC or Vue.
 */
import contributionContract from "../plugins/contribution-points.json" with { type: "json" };
import type { PointContribution } from "../plugins/contributions.ts";
import { resolvePluginTitle } from "../plugins/title.ts";
import type { PluginTaskRef } from "../terminal/types.ts";

export const TASKS_CONTRIBUTION_POINT = "tasks";

interface TasksContract {
  maxEntries: number;
  limits: { titleMaxRunes: number; detailMaxRunes: number; localesMax: number; detectedTasksMax: number; argsMax: number };
  lists: { groups: string[] };
}

const CONTRACT = (contributionContract as unknown as { points: Record<string, TasksContract> }).points.tasks;

export const TASK_MAX_ENTRIES = CONTRACT.maxEntries;
export const TASK_TITLE_MAX_RUNES = CONTRACT.limits.titleMaxRunes;
export const TASK_DETAIL_MAX_RUNES = CONTRACT.limits.detailMaxRunes;
export const TASK_DETECTED_MAX = CONTRACT.limits.detectedTasksMax;
export const TASK_GROUPS: readonly string[] = CONTRACT.lists.groups;

/** Palette command ids. */
export const TASK_RUN_COMMAND_ID = "tasks.run";
export const TASK_DETECT_COMMAND_ID = "tasks.detect";

export type PluginTaskErrorCode = "not_found" | "forbidden" | "untrusted" | "changed" | "unavailable" | "limit";
export type PluginTaskProviderState = "idle" | "ready" | "error";

export interface PluginTaskItem {
  pluginId: string;
  pluginName: string;
  /** `static:<id>` or `detected:<provider>:<id>`. */
  key: string;
  source: "static" | "detected";
  providerId?: string;
  title: string;
  titleLocalized?: Record<string, string>;
  detail?: string;
  group: string;
  /** Declared command and args (display only; the host resolves the launch). */
  command: string;
  args: string[];
}

export interface PluginTaskProvider {
  pluginId: string;
  pluginName: string;
  providerId: string;
  title: string;
  titleLocalized?: Record<string, string>;
  state: PluginTaskProviderState;
  detectedAt?: number;
  dropped?: number;
  error?: string;
}

export interface PluginTaskList {
  projectRoot: string;
  tasks: PluginTaskItem[];
  providers: PluginTaskProvider[];
}

export interface PluginTaskGroup {
  group: string;
  tasks: PluginTaskItem[];
}

const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const TASK_ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const DETECTED_ID_PATTERN = /^[a-z0-9][a-z0-9:._-]{0,63}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,3}$/;
const TOKEN_PATTERN = /^[A-Za-z0-9-]{8,64}$/;
const ERROR_CODES: readonly PluginTaskErrorCode[] = ["not_found", "forbidden", "untrusted", "changed", "unavailable", "limit"];
// eslint-disable-next-line no-control-regex
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/g;
// eslint-disable-next-line no-control-regex
const UNSAFE_CHAR = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/;

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function sanitizeText(value: unknown, maxRunes: number) {
  if (typeof value !== "string") return "";
  return Array.from(value.replace(UNSAFE_TEXT, "").trim()).slice(0, maxRunes).join("");
}

function parseTitleLocalized(value: unknown): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  let count = 0;
  for (const [locale, title] of Object.entries(record)) {
    if (count >= CONTRACT.limits.localesMax) break;
    const clean = sanitizeText(title, TASK_TITLE_MAX_RUNES);
    if (!LOCALE_PATTERN.test(locale) || !clean) continue;
    result[locale] = clean;
    count += 1;
  }
  return count > 0 ? result : undefined;
}

/** Splits a task key; undefined when malformed (mirrors parseTaskKey in Go). */
export function parsePluginTaskKey(key: unknown): { source: "static" | "detected"; providerId?: string; id: string } | undefined {
  if (typeof key !== "string") return undefined;
  const staticId = key.startsWith("static:") ? key.slice("static:".length) : undefined;
  if (staticId !== undefined) return TASK_ID_PATTERN.test(staticId) ? { source: "static", id: staticId } : undefined;
  if (!key.startsWith("detected:")) return undefined;
  const rest = key.slice("detected:".length);
  const index = rest.indexOf(":");
  if (index <= 0) return undefined;
  const providerId = rest.slice(0, index);
  const id = rest.slice(index + 1);
  return TASK_ID_PATTERN.test(providerId) && DETECTED_ID_PATTERN.test(id) ? { source: "detected", providerId, id } : undefined;
}

function parseTaskItem(value: unknown): PluginTaskItem | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const { pluginId, key, command } = record;
  if (typeof pluginId !== "string" || !PLUGIN_ID_PATTERN.test(pluginId)) return undefined;
  const parsedKey = parsePluginTaskKey(key);
  if (!parsedKey || typeof command !== "string" || !command || UNSAFE_CHAR.test(command)) return undefined;
  const args = Array.isArray(record.args) ? record.args : [];
  if (args.length > CONTRACT.limits.argsMax || !args.every((arg) => typeof arg === "string")) return undefined;
  const group = typeof record.group === "string" && TASK_GROUPS.includes(record.group) ? record.group : "other";
  const title = sanitizeText(record.title, TASK_TITLE_MAX_RUNES);
  const detail = sanitizeText(record.detail, TASK_DETAIL_MAX_RUNES);
  return {
    pluginId,
    pluginName: sanitizeText(record.pluginName, TASK_TITLE_MAX_RUNES) || pluginId,
    key: key as string,
    source: parsedKey.source,
    ...(parsedKey.providerId ? { providerId: parsedKey.providerId } : {}),
    title: title || parsedKey.id,
    titleLocalized: parseTitleLocalized(record.titleLocalized),
    ...(detail ? { detail } : {}),
    group,
    command,
    args: args as string[],
  };
}

function parseProvider(value: unknown): PluginTaskProvider | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const { pluginId, providerId } = record;
  if (typeof pluginId !== "string" || !PLUGIN_ID_PATTERN.test(pluginId)) return undefined;
  if (typeof providerId !== "string" || !TASK_ID_PATTERN.test(providerId)) return undefined;
  const state: PluginTaskProviderState = record.state === "ready" || record.state === "error" ? record.state : "idle";
  const detectedAt = typeof record.detectedAt === "number" && Number.isSafeInteger(record.detectedAt) ? record.detectedAt : undefined;
  const dropped = typeof record.dropped === "number" && Number.isSafeInteger(record.dropped) && record.dropped > 0 ? record.dropped : undefined;
  const error = sanitizeText(record.error, 512);
  return {
    pluginId,
    pluginName: sanitizeText(record.pluginName, TASK_TITLE_MAX_RUNES) || pluginId,
    providerId,
    title: sanitizeText(record.title, TASK_TITLE_MAX_RUNES) || providerId,
    titleLocalized: parseTitleLocalized(record.titleLocalized),
    state,
    ...(detectedAt ? { detectedAt } : {}),
    ...(dropped ? { dropped } : {}),
    ...(error ? { error } : {}),
  };
}

/** Parses plugin_host_tasks_list; malformed items are dropped, duplicates keep the first. */
export function parsePluginTaskList(value: unknown): PluginTaskList | undefined {
  const record = objectValue(value);
  if (!record || typeof record.projectRoot !== "string") return undefined;
  const maxTasks = TASK_MAX_ENTRIES * (1 + TASK_DETECTED_MAX);
  const seenTasks = new Set<string>();
  const tasks: PluginTaskItem[] = [];
  for (const item of (Array.isArray(record.tasks) ? record.tasks : []).slice(0, maxTasks)) {
    const task = parseTaskItem(item);
    const id = task ? `${task.pluginId}\u0000${task.key}` : "";
    if (!task || seenTasks.has(id)) continue;
    seenTasks.add(id);
    tasks.push(task);
  }
  const seenProviders = new Set<string>();
  const providers: PluginTaskProvider[] = [];
  for (const item of (Array.isArray(record.providers) ? record.providers : []).slice(0, maxTasks)) {
    const provider = parseProvider(item);
    const id = provider ? `${provider.pluginId}\u0000${provider.providerId}` : "";
    if (!provider || seenProviders.has(id)) continue;
    seenProviders.add(id);
    providers.push(provider);
  }
  return { projectRoot: record.projectRoot, tasks, providers };
}

/** Groups tasks in contract order (build, test, run, …); titles sorted inside a group. */
export function groupPluginTasks(tasks: readonly PluginTaskItem[], locale: string | readonly string[] = "en"): PluginTaskGroup[] {
  return TASK_GROUPS.map((group) => ({
    group,
    tasks: tasks
      .filter((task) => task.group === group)
      .sort(
        (left, right) =>
          pluginTaskTitle(left, locale).localeCompare(pluginTaskTitle(right, locale)) ||
          left.pluginId.localeCompare(right.pluginId) ||
          left.key.localeCompare(right.key),
      ),
  })).filter((entry) => entry.tasks.length > 0);
}

export function pluginTaskTitle(task: { title: string; titleLocalized?: Record<string, string> }, locale: string | readonly string[]) {
  return resolvePluginTitle({ title: task.title, titleLocalized: task.titleLocalized }, locale);
}

/** Stable identity of a task in one project (store keys, palette ids). */
export function pluginTaskIdentity(task: Pick<PluginTaskRef, "pluginId" | "taskKey">) {
  return `${task.pluginId}/${task.taskKey}`;
}

/** The `<code>:` prefix of a launch error from the host, if any. */
export function pluginTaskErrorCode(error: unknown): PluginTaskErrorCode | undefined {
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  for (const code of ERROR_CODES) {
    const index = message.indexOf(`${code}:`);
    if (index === 0 || (index > 0 && /[\s:]/.test(message[index - 1] ?? ""))) return code;
  }
  return undefined;
}

/** True when a process plugin active in the workspace contributes at least one task entry. */
export function hasTaskContributions(contributions: readonly PointContribution[]): boolean {
  return contributions.some(
    (contribution) =>
      contribution.runtime === "process" &&
      contribution.activeInWorkspace &&
      Array.isArray(contribution.point?.entries) &&
      contribution.point.entries.length > 0,
  );
}

/**
 * Restores a persisted pane reference; anything malformed is dropped. The
 * launch token is never restored, so a restored pane only re-attaches.
 */
export function restorePluginTaskRef(value: unknown): PluginTaskRef | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const { pluginId, taskKey, projectRoot } = record;
  if (typeof pluginId !== "string" || !PLUGIN_ID_PATTERN.test(pluginId)) return undefined;
  if (!parsePluginTaskKey(taskKey)) return undefined;
  if (typeof projectRoot !== "string" || !projectRoot || projectRoot.length > 4096 || UNSAFE_CHAR.test(projectRoot)) return undefined;
  const title = sanitizeText(record.title, TASK_TITLE_MAX_RUNES);
  return { pluginId, taskKey: taskKey as string, projectRoot, title: title || (taskKey as string) };
}

/** A fresh launch token (in-memory only; see services/plugin-task-service.ts). */
export function isPluginTaskLaunchToken(value: unknown): value is string {
  return typeof value === "string" && TOKEN_PATTERN.test(value);
}

export interface PluginTaskPreview {
  tasks: { title: string; command: string; args: string[] }[];
  providers: string[];
}

/** Install preview (manifest `contributes.tasks`): declared commands and providers, before any run. */
export function pluginTaskPreviewSummary(contributes: unknown): PluginTaskPreview {
  const entries = objectValue(contributes)?.tasks;
  const preview: PluginTaskPreview = { tasks: [], providers: [] };
  if (!Array.isArray(entries)) return preview;
  for (const value of entries.slice(0, TASK_MAX_ENTRIES)) {
    const entry = objectValue(value);
    if (!entry || typeof entry.id !== "string" || !TASK_ID_PATTERN.test(entry.id)) continue;
    const title = sanitizeText(entry.title, TASK_TITLE_MAX_RUNES) || entry.id;
    if (entry.kind === "provider") {
      preview.providers.push(title);
      continue;
    }
    if (entry.kind !== "static" || typeof entry.command !== "string" || !entry.command) continue;
    const args = Array.isArray(entry.args) && entry.args.every((arg) => typeof arg === "string") ? (entry.args as string[]) : [];
    preview.tasks.push({ title, command: entry.command, args });
  }
  return preview;
}
