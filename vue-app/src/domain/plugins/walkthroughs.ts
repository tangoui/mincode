/**
 * Pure model of the `walkthroughs` contribution point (declarative and
 * process plugins). The Go host (backend/plugin/contrib_walkthroughs.go)
 * validates entries at install: step markdown and images become content-hash
 * asset keys, served by plugin_host_contribution_assets and re-validated on
 * every fetch. Entries are still untrusted here and are re-checked before they
 * are shown. Progress is global user state (`app_plugin_walkthroughs_v1`).
 * A walkthrough never opens automatically. Nothing here touches Vue or IPC.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const WALKTHROUGHS_CONTRIBUTION_POINT = "walkthroughs";
export const WALKTHROUGHS_STORAGE_KEY = "app_plugin_walkthroughs_v1";

// Mirrors `points.walkthroughs` in plugins/api/contribution-points.json
// (vue-app/scripts/walkthroughs-contributions.test.mjs keeps them in sync).
export const WALKTHROUGH_MAX_ENTRIES = 4;
export const WALKTHROUGH_STEPS_MAX = 16;
export const WALKTHROUGH_TITLE_MAX_RUNES = 64;
export const WALKTHROUGH_DESCRIPTION_MAX_RUNES = 256;
export const WALKTHROUGH_LOCALES_MAX = 16;
export const WALKTHROUGH_TEXT_MAX_RUNES = 2048;
export const WALKTHROUGH_IMAGE_ALT_MAX_RUNES = 256;
/** At most this many featured walkthroughs appear on the dashboard. */
export const WALKTHROUGH_FEATURED_MAX = 3;
/** Progress records retained (completed + dismissed). */
export const WALKTHROUGH_PROGRESS_MAX_RECORDS = 256;

const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const COMMAND_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,3}$/;
const ASSET_KEY_PATTERN = /^[0-9a-f]{64}$/;
const IMAGE_MIMES = new Set(["image/png", "image/svg+xml"]);
// eslint-disable-next-line no-control-regex
const UNSAFE_LINE = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/g;
// Multi-line text keeps \n (and \t) but drops every other control, bidi and zero-width character.
// eslint-disable-next-line no-control-regex
const UNSAFE_MULTILINE = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/g;

export interface WalkthroughStep {
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  /** Asset key of the step markdown (exclusive with `text`). */
  markdown?: string;
  markdownLocalized?: Record<string, string>;
  text?: string;
  /** Asset key of the step image; always paired with `imageAlt`. */
  image?: string;
  imageMime?: string;
  imageAlt?: string;
  /** One of the plugin's own commands (process plugins only). */
  command?: string;
}

export interface Walkthrough {
  /** `<pluginId>/<walkthroughId>`. */
  key: string;
  pluginId: string;
  pluginName: string;
  runtime: string;
  activeInWorkspace: boolean;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  description: string;
  descriptionLocalized?: Record<string, string>;
  featured: boolean;
  steps: WalkthroughStep[];
}

export interface WalkthroughProgress {
  version: 1;
  /** Completed step ids keyed by `<pluginId>/<walkthroughId>`; unknown keys stay inert. */
  completed: Record<string, string[]>;
  /** Walkthrough keys the user removed from the dashboard. */
  dismissed: string[];
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function limitRunes(value: string, maxRunes: number) {
  return Array.from(value).slice(0, maxRunes).join("");
}

function cleanLine(value: unknown, maxRunes: number): string {
  return typeof value === "string" ? limitRunes(value.replace(UNSAFE_LINE, "").trim(), maxRunes) : "";
}

function cleanMultiline(value: unknown, maxRunes: number): string {
  if (typeof value !== "string") return "";
  return limitRunes(value.replace(/\r\n?/g, "\n").replace(UNSAFE_MULTILINE, "").trim(), maxRunes);
}

function localized(value: unknown, maxRunes: number): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  let count = 0;
  for (const [locale, text] of Object.entries(record)) {
    if (count >= WALKTHROUGH_LOCALES_MAX) break;
    if (!LOCALE_PATTERN.test(locale)) continue;
    const clean = cleanLine(text, maxRunes);
    if (!clean) continue;
    result[locale] = clean;
    count += 1;
  }
  return count > 0 ? result : undefined;
}

function assetKeys(value: unknown): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  let count = 0;
  for (const [locale, key] of Object.entries(record)) {
    if (count >= WALKTHROUGH_LOCALES_MAX) break;
    if (!LOCALE_PATTERN.test(locale) || typeof key !== "string" || !ASSET_KEY_PATTERN.test(key)) continue;
    result[locale] = key;
    count += 1;
  }
  return count > 0 ? result : undefined;
}

export function walkthroughKey(pluginId: string, walkthroughId: string) {
  return `${pluginId}/${walkthroughId}`;
}

/** Re-validates one step; undefined when it is out of contract. */
export function parseWalkthroughStep(value: unknown, allowCommand: boolean): WalkthroughStep | undefined {
  const record = objectValue(value);
  if (!record || typeof record.id !== "string" || !ID_PATTERN.test(record.id)) return undefined;
  const hasMarkdown = typeof record.markdown === "string" && ASSET_KEY_PATTERN.test(record.markdown);
  const text = cleanMultiline(record.text, WALKTHROUGH_TEXT_MAX_RUNES);
  if (hasMarkdown === Boolean(text)) return undefined;
  const step: WalkthroughStep = { id: record.id, title: cleanLine(record.title, WALKTHROUGH_TITLE_MAX_RUNES) || record.id };
  const titleLocalized = localized(record.titleLocalized, WALKTHROUGH_TITLE_MAX_RUNES);
  if (titleLocalized) step.titleLocalized = titleLocalized;
  if (hasMarkdown) {
    step.markdown = record.markdown as string;
    const markdownLocalized = assetKeys(record.markdownLocalized);
    if (markdownLocalized) step.markdownLocalized = markdownLocalized;
  } else {
    step.text = text;
  }
  if (record.image !== undefined) {
    const alt = cleanLine(record.imageAlt, WALKTHROUGH_IMAGE_ALT_MAX_RUNES);
    if (typeof record.image !== "string" || !ASSET_KEY_PATTERN.test(record.image) || !alt) return undefined;
    if (typeof record.imageMime !== "string" || !IMAGE_MIMES.has(record.imageMime)) return undefined;
    step.image = record.image;
    step.imageMime = record.imageMime;
    step.imageAlt = alt;
  }
  if (record.command !== undefined) {
    if (!allowCommand || typeof record.command !== "string" || !COMMAND_ID_PATTERN.test(record.command)) return undefined;
    step.command = record.command;
  }
  return step;
}

/** Re-validates one walkthrough entry; undefined when anything is out of contract. */
export function parseWalkthroughEntry(
  value: unknown,
  plugin: { pluginId: string; pluginName: string; runtime: string; activeInWorkspace: boolean },
): Walkthrough | undefined {
  const record = objectValue(value);
  if (!record || typeof record.id !== "string" || !ID_PATTERN.test(record.id)) return undefined;
  if (!Array.isArray(record.steps) || record.steps.length === 0 || record.steps.length > WALKTHROUGH_STEPS_MAX) return undefined;
  const allowCommand = plugin.runtime === "process";
  const steps: WalkthroughStep[] = [];
  const seen = new Set<string>();
  for (const item of record.steps) {
    const step = parseWalkthroughStep(item, allowCommand);
    if (!step || seen.has(step.id)) return undefined;
    seen.add(step.id);
    steps.push(step);
  }
  return {
    key: walkthroughKey(plugin.pluginId, record.id),
    pluginId: plugin.pluginId,
    pluginName: cleanLine(plugin.pluginName, WALKTHROUGH_TITLE_MAX_RUNES) || plugin.pluginId,
    runtime: plugin.runtime,
    activeInWorkspace: plugin.activeInWorkspace,
    id: record.id,
    title: cleanLine(record.title, WALKTHROUGH_TITLE_MAX_RUNES) || record.id,
    titleLocalized: localized(record.titleLocalized, WALKTHROUGH_TITLE_MAX_RUNES),
    description: cleanLine(record.description, WALKTHROUGH_DESCRIPTION_MAX_RUNES),
    descriptionLocalized: localized(record.descriptionLocalized, WALKTHROUGH_DESCRIPTION_MAX_RUNES),
    featured: record.featured === true,
    steps,
  };
}

/**
 * All re-validated walkthroughs, deduplicated and sorted by pluginId then
 * declaration order; at most WALKTHROUGH_MAX_ENTRIES per plugin and one
 * featured walkthrough per plugin (later ones lose the flag).
 */
export function resolveWalkthroughs(contributions: readonly PointContribution[]): Walkthrough[] {
  const result: Walkthrough[] = [];
  const seen = new Set<string>();
  const sorted = [...contributions].sort((left, right) => left.pluginId.localeCompare(right.pluginId));
  for (const contribution of sorted) {
    if (!PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    const entries = Array.isArray(contribution.point?.entries) ? contribution.point.entries : [];
    let count = 0;
    let featured = false;
    for (const entry of entries) {
      if (count >= WALKTHROUGH_MAX_ENTRIES) break;
      const walkthrough = parseWalkthroughEntry(entry, contribution);
      if (!walkthrough || seen.has(walkthrough.key)) continue;
      if (walkthrough.featured) {
        if (featured) walkthrough.featured = false;
        featured = true;
      }
      seen.add(walkthrough.key);
      count += 1;
      result.push(walkthrough);
    }
  }
  return result;
}

export function walkthroughTitle(item: { title: string; titleLocalized?: Record<string, string> }, locale: string | readonly string[]) {
  return resolvePluginTitle({ title: item.title, titleLocalized: item.titleLocalized }, locale);
}

export function walkthroughDescription(walkthrough: Walkthrough, locale: string | readonly string[]) {
  if (!walkthrough.description) return "";
  return resolvePluginTitle({ title: walkthrough.description, titleLocalized: walkthrough.descriptionLocalized }, locale);
}

/** The markdown asset key for the locale chain: the first localized match, else the default. */
export function walkthroughMarkdownKey(step: WalkthroughStep, locale: string | readonly string[]): string | undefined {
  if (!step.markdown) return undefined;
  const chain = typeof locale === "string" ? [locale] : locale;
  const localizedKeys = step.markdownLocalized ?? {};
  for (const tag of chain) {
    for (const candidate of [tag, tag.split("-")[0] ?? ""]) {
      if (candidate && Object.prototype.hasOwnProperty.call(localizedKeys, candidate)) return localizedKeys[candidate];
    }
  }
  return step.markdown;
}

export function emptyWalkthroughProgress(): WalkthroughProgress {
  return { version: 1, completed: {}, dismissed: [] };
}

function validProgressKey(key: unknown): key is string {
  if (typeof key !== "string") return false;
  const index = key.lastIndexOf("/");
  return index > 0 && PLUGIN_ID_PATTERN.test(key.slice(0, index)) && ID_PATTERN.test(key.slice(index + 1));
}

/** Normalizes stored progress; malformed parts are dropped, at most 256 records are kept (newest last). */
export function normalizeWalkthroughProgress(value: unknown): WalkthroughProgress {
  const record = objectValue(value);
  if (!record || record.version !== 1) return emptyWalkthroughProgress();
  const completedEntries: Array<[string, string[]]> = [];
  for (const [key, steps] of Object.entries(objectValue(record.completed) ?? {})) {
    if (!validProgressKey(key) || !Array.isArray(steps)) continue;
    const ids = [...new Set(steps.filter((id): id is string => typeof id === "string" && ID_PATTERN.test(id)))].slice(0, WALKTHROUGH_STEPS_MAX);
    if (ids.length) completedEntries.push([key, ids]);
  }
  const dismissed = Array.isArray(record.dismissed) ? [...new Set(record.dismissed.filter(validProgressKey))] : [];
  const keptDismissed = dismissed.slice(-WALKTHROUGH_PROGRESS_MAX_RECORDS);
  const room = Math.max(0, WALKTHROUGH_PROGRESS_MAX_RECORDS - keptDismissed.length);
  const keptCompleted = room > 0 ? completedEntries.slice(-room) : [];
  return { version: 1, completed: Object.fromEntries(keptCompleted), dismissed: keptDismissed };
}

/** Marks one step done (idempotent); returns a new normalized progress. */
export function markWalkthroughStepDone(progress: WalkthroughProgress, key: string, stepId: string): WalkthroughProgress {
  if (!validProgressKey(key) || !ID_PATTERN.test(stepId)) return progress;
  const current = progress.completed[key] ?? [];
  if (current.includes(stepId)) return progress;
  const completed = { ...progress.completed };
  delete completed[key];
  completed[key] = [...current, stepId];
  return normalizeWalkthroughProgress({ ...progress, completed });
}

/** Adds or removes a walkthrough from the dashboard's dismissed list. */
export function setWalkthroughDismissed(progress: WalkthroughProgress, key: string, dismissed: boolean): WalkthroughProgress {
  if (!validProgressKey(key)) return progress;
  const next = progress.dismissed.filter((item) => item !== key);
  if (dismissed) next.push(key);
  return normalizeWalkthroughProgress({ ...progress, dismissed: next });
}

/** Step ids completed that still exist in the walkthrough (upgrades keep progress for surviving ids). */
export function completedWalkthroughSteps(walkthrough: Walkthrough, progress: WalkthroughProgress): Set<string> {
  const ids = new Set(walkthrough.steps.map((step) => step.id));
  return new Set((progress.completed[walkthrough.key] ?? []).filter((id) => ids.has(id)));
}

export function isWalkthroughComplete(walkthrough: Walkthrough, progress: WalkthroughProgress) {
  return completedWalkthroughSteps(walkthrough, progress).size === walkthrough.steps.length;
}

/**
 * Walkthroughs for the dashboard "Get started" card: featured, effective in
 * this workspace, neither dismissed nor completed; at most `max`.
 */
export function featuredWalkthroughs(
  walkthroughs: readonly Walkthrough[],
  progress: WalkthroughProgress,
  max = WALKTHROUGH_FEATURED_MAX,
): Walkthrough[] {
  const dismissed = new Set(progress.dismissed);
  return walkthroughs
    .filter((item) => item.featured && item.activeInWorkspace && !dismissed.has(item.key) && !isWalkthroughComplete(item, progress))
    .slice(0, Math.max(0, max));
}

/** Install preview: walkthrough titles and whether any step runs a plugin command. */
export function walkthroughPreviewSummary(contributes: unknown): { count: number; titles: string[]; commandSteps: number } | undefined {
  const list = objectValue(contributes)?.walkthroughs;
  if (!Array.isArray(list) || list.length === 0) return undefined;
  const titles: string[] = [];
  let commandSteps = 0;
  for (const entry of list.slice(0, WALKTHROUGH_MAX_ENTRIES)) {
    const record = objectValue(entry);
    if (!record) continue;
    titles.push(cleanLine(record.title, WALKTHROUGH_TITLE_MAX_RUNES) || cleanLine(record.id, 32) || "?");
    if (Array.isArray(record.steps)) {
      commandSteps += record.steps.filter((step) => typeof objectValue(step)?.command === "string").length;
    }
  }
  return titles.length ? { count: titles.length, titles, commandSteps } : undefined;
}
