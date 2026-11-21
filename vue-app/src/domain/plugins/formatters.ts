/**
 * Frontend stage of the `formatters` contribution point: re-validation of
 * formatter entries the Go host already accepted
 * (backend/plugin/contrib_formatters.go), the machine-local
 * preference model, the deterministic provider selector and the minimal-edit
 * computation. No DOM, Vue, IPC or Monaco here; the runtime wiring lives in
 * services/contributions/formatters.ts, stores/plugin-formatters.ts,
 * services/plugin-formatter-service.ts and services/monaco-formatters.ts.
 *
 * Built-in formatters stay the default: a plugin formatter runs only when the
 * user picks it once ("Format Document With…") or selects it for a language.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const FORMATTERS_CONTRIBUTION_POINT = "formatters";

/**
 * Machine-local preference key. Deliberately not `app_`-prefixed: config sync
 * and export never carry it, so format-on-save is never enabled on another
 * machine.
 */
export const PLUGIN_FORMATTERS_STORAGE_KEY = "mincode_plugin_formatters_v1";

/** Mirrors contribution-points.json formatters limits (the host enforces them first). */
export const FORMATTER_LIMITS = {
  maxEntries: 8,
  titleMaxRunes: 64,
  languagesMax: 16,
  payloadMaxBytes: 262144,
  resultMaxBytes: 786432,
  timeoutMs: 10000,
  saveTimeoutMs: 3000,
  /** Languages kept in the preference record. */
  preferencesMax: 256,
} as const;

export const FORMATTER_ID_PATTERN = /^[a-z][a-z0-9-]{0,31}$/;
export const FORMATTER_LANGUAGE_PATTERN = /^[a-z][a-z0-9+#._-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const ACTION_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
// eslint-disable-next-line no-control-regex -- rejecting control characters is the point
const UNSAFE_TEXT_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069]/;

export interface PluginFormatter {
  pluginId: string;
  pluginName: string;
  id: string;
  /** "<pluginId>/<formatterId>", the preference value. */
  ref: string;
  title: string;
  titleLocalized?: Record<string, string>;
  languages: string[];
  action: string;
  range: boolean;
}

export interface FormatterIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  formatter?: string;
  message: string;
}

export interface FormatterLanguagePreference {
  /** A formatter ref; undefined selects the built-in (or language server) formatter. */
  formatter?: string;
  formatOnSave: boolean;
}

export interface FormatterPreferences {
  version: 1;
  byLanguage: Record<string, FormatterLanguagePreference>;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isSafeText(value: string, maxRunes: number): boolean {
  const length = [...value].length;
  return length > 0 && length <= maxRunes && !UNSAFE_TEXT_PATTERN.test(value);
}

export function formatterRef(pluginId: string, formatterId: string): string {
  return `${pluginId}/${formatterId}`;
}

/** Splits a formatter ref; undefined when it is not "<pluginId>/<formatterId>". */
export function parseFormatterRef(ref: unknown): { pluginId: string; formatterId: string } | undefined {
  if (typeof ref !== "string") return undefined;
  const slash = ref.lastIndexOf("/");
  const pluginId = ref.slice(0, slash);
  const formatterId = ref.slice(slash + 1);
  return slash > 0 && PLUGIN_ID_PATTERN.test(pluginId) && FORMATTER_ID_PATTERN.test(formatterId) ? { pluginId, formatterId } : undefined;
}

export type FormatterEntryResult =
  | { ok: true; formatter: Omit<PluginFormatter, "pluginId" | "pluginName" | "ref"> }
  | { ok: false; error: string };

/** Re-validates one snapshot entry `{id, title, titleLocalized?, languages, action, range}`. */
export function validateFormatterEntry(raw: unknown): FormatterEntryResult {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!FORMATTER_ID_PATTERN.test(id)) return { ok: false, error: "formatter id is invalid" };
  if (typeof raw.title !== "string" || !isSafeText(raw.title, FORMATTER_LIMITS.titleMaxRunes)) {
    return { ok: false, error: `formatter "${id}" title is invalid` };
  }
  if (!Array.isArray(raw.languages) || raw.languages.length < 1 || raw.languages.length > FORMATTER_LIMITS.languagesMax) {
    return { ok: false, error: `formatter "${id}" needs 1-${FORMATTER_LIMITS.languagesMax} languages` };
  }
  const languages: string[] = [];
  for (const language of raw.languages) {
    if (typeof language !== "string" || !FORMATTER_LANGUAGE_PATTERN.test(language)) return { ok: false, error: `formatter "${id}" has an invalid language` };
    if (languages.includes(language)) return { ok: false, error: `formatter "${id}" repeats language "${language}"` };
    languages.push(language);
  }
  if (typeof raw.action !== "string" || !ACTION_ID_PATTERN.test(raw.action)) return { ok: false, error: `formatter "${id}" action is invalid` };
  const formatter: Omit<PluginFormatter, "pluginId" | "pluginName" | "ref"> = {
    id,
    title: raw.title,
    languages,
    action: raw.action,
    range: raw.range === true,
  };
  if (isRecord(raw.titleLocalized)) {
    const titles: Record<string, string> = {};
    for (const [locale, title] of Object.entries(raw.titleLocalized)) {
      if (typeof title === "string" && isSafeText(title, FORMATTER_LIMITS.titleMaxRunes) && locale.length <= 32) titles[locale] = title;
    }
    if (Object.keys(titles).length) formatter.titleLocalized = titles;
  }
  return { ok: true, formatter };
}

/**
 * Validates every plugin's formatters. Only process plugins active in the
 * current workspace contribute. Formatters come back ordered by pluginId,
 * then entry order.
 */
export function collectFormatters(contributions: readonly PointContribution[]): {
  formatters: PluginFormatter[];
  issues: FormatterIssue[];
} {
  const formatters: PluginFormatter[] = [];
  const issues: FormatterIssue[] = [];
  const ordered = [...contributions].sort((a, b) => (a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0));
  for (const contribution of ordered) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { formatter: issue.id } : {}) });
    }
    const seen = new Set<string>();
    contribution.point.entries.slice(0, FORMATTER_LIMITS.maxEntries).forEach((raw, entry) => {
      const result = validateFormatterEntry(raw);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      if (seen.has(result.formatter.id)) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, formatter: result.formatter.id, message: `duplicate formatter id "${result.formatter.id}"` });
        return;
      }
      seen.add(result.formatter.id);
      formatters.push({
        ...result.formatter,
        pluginId: contribution.pluginId,
        pluginName: contribution.pluginName,
        ref: formatterRef(contribution.pluginId, result.formatter.id),
      });
    });
  }
  return { formatters, issues };
}

export function emptyFormatterPreferences(): FormatterPreferences {
  return { version: 1, byLanguage: {} };
}

/**
 * Normalizes a stored preference record. Unknown refs are kept (inert until
 * the plugin is available again); invalid languages or refs are dropped;
 * languages beyond `preferencesMax` are dropped. Keys are read with own-
 * property checks only, and the result has a null prototype.
 */
export function normalizeFormatterPreferences(raw: unknown): FormatterPreferences {
  const result = emptyFormatterPreferences();
  result.byLanguage = Object.create(null) as Record<string, FormatterLanguagePreference>;
  if (!isRecord(raw) || raw.version !== 1 || !isRecord(raw.byLanguage)) return result;
  let count = 0;
  for (const language of Object.keys(raw.byLanguage)) {
    if (count >= FORMATTER_LIMITS.preferencesMax) break;
    if (!FORMATTER_LANGUAGE_PATTERN.test(language)) continue;
    const value = raw.byLanguage[language];
    if (!isRecord(value)) continue;
    const formatter = parseFormatterRef(value.formatter) ? (value.formatter as string) : undefined;
    // Format on save only applies to a selected plugin formatter.
    const formatOnSave = formatter !== undefined && value.formatOnSave === true;
    if (!formatter && !formatOnSave) continue;
    result.byLanguage[language] = { ...(formatter ? { formatter } : {}), formatOnSave };
    count += 1;
  }
  return result;
}

/** Returns a new record with one language's preference replaced (undefined formatter = built-in). */
export function updateFormatterPreference(
  preferences: FormatterPreferences,
  language: string,
  change: Partial<FormatterLanguagePreference>,
): FormatterPreferences {
  const next = normalizeFormatterPreferences(preferences);
  if (!FORMATTER_LANGUAGE_PATTERN.test(language)) return next;
  const current = next.byLanguage[language] ?? { formatOnSave: false };
  const formatter = "formatter" in change ? change.formatter : current.formatter;
  const formatOnSave = formatter !== undefined && (change.formatOnSave ?? current.formatOnSave);
  const updated = normalizeFormatterPreferences({
    version: 1,
    byLanguage: { ...next.byLanguage, [language]: { formatter, formatOnSave } },
  });
  // Keep the edited language even when the record is full.
  if (formatter && !updated.byLanguage[language]) updated.byLanguage[language] = { formatter, formatOnSave };
  return updated;
}

/** The preference of one language, if any. */
export function formatterPreference(preferences: FormatterPreferences, language: string): FormatterLanguagePreference | undefined {
  return Object.prototype.hasOwnProperty.call(preferences.byLanguage, language) ? preferences.byLanguage[language] : undefined;
}

export function formattersForLanguage(formatters: readonly PluginFormatter[], language: string): PluginFormatter[] {
  return formatters.filter((formatter) => formatter.languages.includes(language));
}

/** The available plugin formatter selected for a language, if any. */
export function selectedFormatter(
  preferences: FormatterPreferences,
  formatters: readonly PluginFormatter[],
  language: string,
): PluginFormatter | undefined {
  const ref = formatterPreference(preferences, language)?.formatter;
  return ref ? formatters.find((formatter) => formatter.ref === ref && formatter.languages.includes(language)) : undefined;
}

/**
 * The deterministic choice among Monaco's formatter providers: the preferred
 * plugin provider when it is present, otherwise the first provider that is
 * not a plugin's (built-in or language server). A plugin provider the user
 * did not select is never chosen implicitly (undefined: nothing runs).
 */
export function chooseFormatterProvider<T>(
  providers: readonly T[],
  pluginRefOf: (provider: T) => string | undefined,
  preferredRef: string | undefined,
): T | undefined {
  if (preferredRef) {
    const preferred = providers.find((provider) => pluginRefOf(provider) === preferredRef);
    if (preferred) return preferred;
  }
  return providers.find((provider) => pluginRefOf(provider) === undefined);
}

/**
 * The smallest single replacement that turns `before` into `after` (common
 * prefix and suffix removed), in UTF-16 offsets of `before`; null when the
 * texts are equal. Surrogate pairs are never split.
 */
export function minimalEdit(before: string, after: string): { start: number; end: number; text: string } | null {
  if (before === after) return null;
  const limit = Math.min(before.length, after.length);
  let start = 0;
  while (start < limit && before.charCodeAt(start) === after.charCodeAt(start)) start += 1;
  if (start > 0 && isHighSurrogate(before.charCodeAt(start - 1))) start -= 1;
  let suffix = 0;
  while (
    suffix < limit - start &&
    before.charCodeAt(before.length - 1 - suffix) === after.charCodeAt(after.length - 1 - suffix)
  ) {
    suffix += 1;
  }
  if (suffix > 0 && isLowSurrogate(before.charCodeAt(before.length - suffix))) suffix -= 1;
  return { start, end: before.length - suffix, text: after.slice(start, after.length - suffix) };
}

function isHighSurrogate(code: number): boolean {
  return code >= 0xd800 && code <= 0xdbff;
}

function isLowSurrogate(code: number): boolean {
  return code >= 0xdc00 && code <= 0xdfff;
}

/** UTF-8 byte length without allocating the encoded bytes. */
export function utf8ByteLength(text: string): number {
  let bytes = 0;
  for (let index = 0; index < text.length; index += 1) {
    const code = text.charCodeAt(index);
    if (code < 0x80) bytes += 1;
    else if (code < 0x800) bytes += 2;
    else if (isHighSurrogate(code) && isLowSurrogate(text.charCodeAt(index + 1))) {
      bytes += 4;
      index += 1;
    } else bytes += 3;
  }
  return bytes;
}

/** The formatter's title for `locale`. */
export function formatterTitle(formatter: Pick<PluginFormatter, "title" | "titleLocalized">, locale: string | readonly string[]): string {
  return resolvePluginTitle(formatter, locale);
}

/** Install preview: every formatter's title and languages, from the raw manifest. */
export function formatterPreviewSummary(contributes: unknown): { title: string; languages: string[] }[] {
  const entries = isRecord(contributes) ? contributes[FORMATTERS_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, FORMATTER_LIMITS.maxEntries).flatMap((entry) => {
    if (!isRecord(entry)) return [];
    const title = typeof entry.title === "string" && entry.title.trim() ? entry.title.trim().slice(0, FORMATTER_LIMITS.titleMaxRunes) : typeof entry.id === "string" ? entry.id.slice(0, 32) : "";
    const languages = Array.isArray(entry.languages)
      ? entry.languages.filter((language): language is string => typeof language === "string").map((language) => language.slice(0, 32))
      : [];
    return title ? [{ title, languages: languages.slice(0, FORMATTER_LIMITS.languagesMax) }] : [];
  });
}
