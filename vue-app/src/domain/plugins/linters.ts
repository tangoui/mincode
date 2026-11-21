/**
 * Frontend stage of the `linters` contribution point: re-validation of linter
 * entries the Go host already accepted
 * (backend/plugin/contrib_linters.go), the machine-local preference
 * model and the defensive re-check of diagnostics the host already sanitised
 * (linter_invoke.go). No DOM, Vue, IPC or Monaco here; the runtime wiring
 * lives in services/contributions/linters.ts, stores/plugin-linters.ts,
 * services/plugin-linter-service.ts and services/monaco-linters.ts.
 *
 * Plugin linters are off by default: document text reaches a linter only for
 * languages where the user selected it, and while typing only when the user
 * also enabled "Lint while typing" and the linter declares the `type` trigger.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import contributionContract from "./contribution-points.json" with { type: "json" };
import { resolvePluginTitle } from "@/domain/plugins/title";

export const LINTERS_CONTRIBUTION_POINT = "linters";

/**
 * Machine-local preference key. Deliberately not `app_`-prefixed: config sync
 * and export never carry it, so document text is never sent to a plugin on
 * another machine without the user choosing it there.
 */
export const PLUGIN_LINTERS_STORAGE_KEY = "mincode_plugin_linters_v1";

/** Mirrors contribution-points.json linters limits (the host enforces them first). */
export const LINTER_LIMITS = {
  // Read from the contract so a raised limit never silently drops linters here.
  maxEntries: contributionContract.points.linters.maxEntries,
  detectFilesMax: contributionContract.points.linters.limits.detectFilesMax,
  detectPathMaxRunes: contributionContract.points.linters.limits.detectPathMaxRunes,
  titleMaxRunes: 64,
  languagesMax: 16,
  payloadMaxBytes: 262144,
  diagnosticsMax: 500,
  messageMaxRunes: 1024,
  codeMaxRunes: 64,
  sourceMaxRunes: 64,
  lineMax: 10_000_000,
  columnMax: 100_000,
  timeoutMs: 10000,
  debounceMinMs: 300,
  debounceMaxMs: 5000,
  debounceDefaultMs: 750,
  /** Languages kept in the preference record. */
  preferencesMax: 256,
} as const;

export const LINTER_TRIGGERS = ["open", "save", "type"] as const;
export type LinterTrigger = (typeof LINTER_TRIGGERS)[number];
export const LINTER_SEVERITIES = ["error", "warning", "info", "hint"] as const;
export type LinterSeverity = (typeof LINTER_SEVERITIES)[number];
export const LINTER_TAGS = ["unnecessary", "deprecated"] as const;
export type LinterTag = (typeof LINTER_TAGS)[number];

export const LINTER_ID_PATTERN = /^[a-z][a-z0-9-]{0,31}$/;
export const LINTER_LANGUAGE_PATTERN = /^[a-z][a-z0-9+#._-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const ACTION_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
// eslint-disable-next-line no-control-regex -- rejecting control characters is the point
const UNSAFE_TEXT_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/;
// eslint-disable-next-line no-control-regex -- the same set, newline allowed
const UNSAFE_MULTILINE_PATTERN = /[\u0000-\u0009\u000b-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/g;

/** contributes.configuration key syntax (the host checks the property itself). */
const CONFIGURATION_KEY_PATTERN = /^[a-z][a-zA-Z0-9]*(\.[a-z][a-zA-Z0-9]*){0,3}$/;

function isDetectPath(value: unknown): value is string {
  if (typeof value !== "string" || !value || [...value].length > LINTER_LIMITS.detectPathMaxRunes) return false;
  if (/[\\*?[\]{}:]/.test(value) || value.startsWith("/") || value.endsWith("/") || UNSAFE_TEXT_PATTERN.test(value)) return false;
  return value.split("/").every((segment) => segment !== "" && segment !== "." && segment !== ".." && segment.trim() === segment);
}

export interface PluginLinter {
  pluginId: string;
  pluginName: string;
  id: string;
  /** "<pluginId>/<linterId>", the preference value. */
  ref: string;
  title: string;
  titleLocalized?: Record<string, string>;
  languages: string[];
  action: string;
  triggers: LinterTrigger[];
  debounceMs: number;
  /** Project paths whose presence means the project uses this tool (the host asks once). */
  detect?: { files: string[] };
  /** The plugin's workspace-scoped boolean setting the tool needs because it runs project code. */
  enableSetting?: string;
}

export interface LinterIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  linter?: string;
  message: string;
}

export interface LinterLanguagePreference {
  /** A linter ref; undefined means no plugin linter for the language. */
  linter?: string;
  onType: boolean;
}

export interface LinterPreferences {
  version: 1;
  byLanguage: Record<string, LinterLanguagePreference>;
}

export interface LintDiagnostic {
  startLineNumber: number;
  startColumn: number;
  endLineNumber: number;
  endColumn: number;
  severity: LinterSeverity;
  message: string;
  code?: string;
  source?: string;
  tags?: LinterTag[];
}

export interface LintResult {
  diagnostics: LintDiagnostic[];
  dropped: number;
  truncated: boolean;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isSafeText(value: string, maxRunes: number): boolean {
  const length = [...value].length;
  return length > 0 && length <= maxRunes && !new RegExp(UNSAFE_TEXT_PATTERN.source).test(value);
}

export function linterRef(pluginId: string, linterId: string): string {
  return `${pluginId}/${linterId}`;
}

/** Splits a linter ref; undefined when it is not "<pluginId>/<linterId>". */
export function parseLinterRef(ref: unknown): { pluginId: string; linterId: string } | undefined {
  if (typeof ref !== "string") return undefined;
  const slash = ref.lastIndexOf("/");
  const pluginId = ref.slice(0, slash);
  const linterId = ref.slice(slash + 1);
  return slash > 0 && PLUGIN_ID_PATTERN.test(pluginId) && LINTER_ID_PATTERN.test(linterId) ? { pluginId, linterId } : undefined;
}

export type LinterEntryResult =
  | { ok: true; linter: Omit<PluginLinter, "pluginId" | "pluginName" | "ref"> }
  | { ok: false; error: string };

/** Re-validates one snapshot entry `{id, title, titleLocalized?, languages, action, triggers, debounceMs}`. */
export function validateLinterEntry(raw: unknown): LinterEntryResult {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!LINTER_ID_PATTERN.test(id)) return { ok: false, error: "linter id is invalid" };
  if (typeof raw.title !== "string" || !isSafeText(raw.title, LINTER_LIMITS.titleMaxRunes)) {
    return { ok: false, error: `linter "${id}" title is invalid` };
  }
  if (!Array.isArray(raw.languages) || raw.languages.length < 1 || raw.languages.length > LINTER_LIMITS.languagesMax) {
    return { ok: false, error: `linter "${id}" needs 1-${LINTER_LIMITS.languagesMax} languages` };
  }
  const languages: string[] = [];
  for (const language of raw.languages) {
    if (typeof language !== "string" || !LINTER_LANGUAGE_PATTERN.test(language)) return { ok: false, error: `linter "${id}" has an invalid language` };
    if (languages.includes(language)) return { ok: false, error: `linter "${id}" repeats language "${language}"` };
    languages.push(language);
  }
  if (typeof raw.action !== "string" || !ACTION_ID_PATTERN.test(raw.action)) return { ok: false, error: `linter "${id}" action is invalid` };
  if (!Array.isArray(raw.triggers) || raw.triggers.length < 1) return { ok: false, error: `linter "${id}" triggers are invalid` };
  const triggers: LinterTrigger[] = [];
  for (const trigger of raw.triggers) {
    if (!(LINTER_TRIGGERS as readonly unknown[]).includes(trigger) || triggers.includes(trigger as LinterTrigger)) {
      return { ok: false, error: `linter "${id}" triggers are invalid` };
    }
    triggers.push(trigger as LinterTrigger);
  }
  const debounceMs = raw.debounceMs;
  if (typeof debounceMs !== "number" || !Number.isInteger(debounceMs) || debounceMs < LINTER_LIMITS.debounceMinMs || debounceMs > LINTER_LIMITS.debounceMaxMs) {
    return { ok: false, error: `linter "${id}" debounceMs is invalid` };
  }
  const linter: Omit<PluginLinter, "pluginId" | "pluginName" | "ref"> = { id, title: raw.title, languages, action: raw.action, triggers, debounceMs };
  if (raw.detect !== undefined) {
    const files = isRecord(raw.detect) && Array.isArray(raw.detect.files) ? raw.detect.files : undefined;
    if (!files || files.length < 1 || files.length > LINTER_LIMITS.detectFilesMax || !files.every(isDetectPath) || new Set(files).size !== files.length) {
      return { ok: false, error: `linter "${id}" detect.files is invalid` };
    }
    linter.detect = { files: [...files] };
  }
  if (raw.enableSetting !== undefined) {
    if (typeof raw.enableSetting !== "string" || !CONFIGURATION_KEY_PATTERN.test(raw.enableSetting)) {
      return { ok: false, error: `linter "${id}" enableSetting is invalid` };
    }
    linter.enableSetting = raw.enableSetting;
  }
  if (isRecord(raw.titleLocalized)) {
    const titles: Record<string, string> = {};
    for (const [locale, title] of Object.entries(raw.titleLocalized)) {
      if (typeof title === "string" && isSafeText(title, LINTER_LIMITS.titleMaxRunes) && locale.length <= 32) titles[locale] = title;
    }
    if (Object.keys(titles).length) linter.titleLocalized = titles;
  }
  return { ok: true, linter };
}

/**
 * Validates every plugin's linters. Only process plugins active in the
 * current workspace contribute. Linters come back ordered by pluginId, then
 * entry order.
 */
export function collectLinters(contributions: readonly PointContribution[]): { linters: PluginLinter[]; issues: LinterIssue[] } {
  const linters: PluginLinter[] = [];
  const issues: LinterIssue[] = [];
  const ordered = [...contributions].sort((a, b) => (a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0));
  for (const contribution of ordered) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { linter: issue.id } : {}) });
    }
    const seen = new Set<string>();
    contribution.point.entries.slice(0, LINTER_LIMITS.maxEntries).forEach((raw, entry) => {
      const result = validateLinterEntry(raw);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      if (seen.has(result.linter.id)) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, linter: result.linter.id, message: `duplicate linter id "${result.linter.id}"` });
        return;
      }
      seen.add(result.linter.id);
      linters.push({ ...result.linter, pluginId: contribution.pluginId, pluginName: contribution.pluginName, ref: linterRef(contribution.pluginId, result.linter.id) });
    });
  }
  return { linters, issues };
}

export function emptyLinterPreferences(): LinterPreferences {
  return { version: 1, byLanguage: {} };
}

/**
 * Normalizes a stored preference record. Unknown refs are kept (inert until
 * the plugin is available again); invalid languages or refs are dropped;
 * languages beyond `preferencesMax` are dropped. The result has a null
 * prototype.
 */
export function normalizeLinterPreferences(raw: unknown): LinterPreferences {
  const result = emptyLinterPreferences();
  result.byLanguage = Object.create(null) as Record<string, LinterLanguagePreference>;
  if (!isRecord(raw) || raw.version !== 1 || !isRecord(raw.byLanguage)) return result;
  let count = 0;
  for (const language of Object.keys(raw.byLanguage)) {
    if (count >= LINTER_LIMITS.preferencesMax) break;
    if (!LINTER_LANGUAGE_PATTERN.test(language)) continue;
    const value = raw.byLanguage[language];
    if (!isRecord(value) || !parseLinterRef(value.linter)) continue;
    result.byLanguage[language] = { linter: value.linter as string, onType: value.onType === true };
    count += 1;
  }
  return result;
}

/** Returns a new record with one language's preference replaced (undefined linter = none). */
export function updateLinterPreference(preferences: LinterPreferences, language: string, change: Partial<LinterLanguagePreference>): LinterPreferences {
  const next = normalizeLinterPreferences(preferences);
  if (!LINTER_LANGUAGE_PATTERN.test(language)) return next;
  const current = next.byLanguage[language] ?? { onType: false };
  const linter = "linter" in change ? change.linter : current.linter;
  const onType = linter !== undefined && (change.onType ?? current.onType);
  const byLanguage = { ...next.byLanguage };
  delete byLanguage[language];
  const updated = normalizeLinterPreferences({ version: 1, byLanguage: linter ? { ...byLanguage, [language]: { linter, onType } } : byLanguage });
  if (linter && !updated.byLanguage[language]) updated.byLanguage[language] = { linter, onType };
  return updated;
}

export function linterPreference(preferences: LinterPreferences, language: string): LinterLanguagePreference | undefined {
  return Object.prototype.hasOwnProperty.call(preferences.byLanguage, language) ? preferences.byLanguage[language] : undefined;
}

export function lintersForLanguage(linters: readonly PluginLinter[], language: string): PluginLinter[] {
  return linters.filter((linter) => linter.languages.includes(language));
}

/** The available plugin linter selected for a language, if any. */
export function selectedLinter(preferences: LinterPreferences, linters: readonly PluginLinter[], language: string): PluginLinter | undefined {
  const ref = linterPreference(preferences, language)?.linter;
  return ref ? linters.find((linter) => linter.ref === ref && linter.languages.includes(language)) : undefined;
}

/** Whether `trigger` may run the selected linter for this language. */
export function linterRunsOn(linter: Pick<PluginLinter, "triggers">, preference: LinterLanguagePreference | undefined, trigger: LinterTrigger): boolean {
  if (!linter.triggers.includes(trigger)) return false;
  return trigger !== "type" || preference?.onType === true;
}

function clampInteger(value: unknown, max: number): number | undefined {
  return typeof value === "number" && Number.isInteger(value) && value >= 1 && value <= max ? value : undefined;
}

function clampLine(value: unknown, maxRunes: number): string | undefined {
  if (typeof value !== "string") return undefined;
  const text = value.trim();
  return text && isSafeText(text, maxRunes) ? text : undefined;
}

/**
 * Defensive re-check of one host-sanitised diagnostic; undefined drops it.
 * The message keeps newlines, has other hidden characters replaced and is cut
 * to `messageMaxRunes`.
 */
export function clampDiagnostic(raw: unknown): LintDiagnostic | undefined {
  if (!isRecord(raw)) return undefined;
  let startLineNumber = clampInteger(raw.startLineNumber, LINTER_LIMITS.lineMax);
  let startColumn = clampInteger(raw.startColumn, LINTER_LIMITS.columnMax);
  let endLineNumber = clampInteger(raw.endLineNumber, LINTER_LIMITS.lineMax);
  let endColumn = clampInteger(raw.endColumn, LINTER_LIMITS.columnMax);
  if (startLineNumber === undefined || startColumn === undefined || endLineNumber === undefined || endColumn === undefined) return undefined;
  if (endLineNumber < startLineNumber || (endLineNumber === startLineNumber && endColumn < startColumn)) {
    [startLineNumber, startColumn, endLineNumber, endColumn] = [endLineNumber, endColumn, startLineNumber, startColumn];
  }
  const severity = (LINTER_SEVERITIES as readonly unknown[]).includes(raw.severity) ? (raw.severity as LinterSeverity) : "warning";
  if (typeof raw.message !== "string") return undefined;
  let message = raw.message.replace(/\r\n/g, "\n").replace(UNSAFE_MULTILINE_PATTERN, " ").trim();
  const runes = [...message];
  if (runes.length > LINTER_LIMITS.messageMaxRunes) message = `${runes.slice(0, LINTER_LIMITS.messageMaxRunes - 1).join("")}…`;
  if (!message) return undefined;
  const diagnostic: LintDiagnostic = { startLineNumber, startColumn, endLineNumber, endColumn, severity, message };
  const code = clampLine(raw.code, LINTER_LIMITS.codeMaxRunes);
  if (code) diagnostic.code = code;
  const source = clampLine(raw.source, LINTER_LIMITS.sourceMaxRunes);
  if (source) diagnostic.source = source;
  if (Array.isArray(raw.tags)) {
    const tags = [...new Set(raw.tags.filter((tag): tag is LinterTag => (LINTER_TAGS as readonly unknown[]).includes(tag)))];
    if (tags.length) diagnostic.tags = tags;
  }
  return diagnostic;
}

/** Parses the plugin_host_lint response; malformed input yields no diagnostics. */
export function parseLintResult(raw: unknown): LintResult {
  if (!isRecord(raw) || !Array.isArray(raw.diagnostics)) return { diagnostics: [], dropped: 0, truncated: false };
  const diagnostics: LintDiagnostic[] = [];
  let dropped = typeof raw.dropped === "number" && Number.isInteger(raw.dropped) && raw.dropped > 0 ? raw.dropped : 0;
  for (const item of raw.diagnostics.slice(0, LINTER_LIMITS.diagnosticsMax)) {
    const diagnostic = clampDiagnostic(item);
    if (diagnostic) diagnostics.push(diagnostic);
    else dropped += 1;
  }
  return { diagnostics, dropped, truncated: raw.truncated === true || raw.diagnostics.length > LINTER_LIMITS.diagnosticsMax };
}

/** The Monaco marker owner of one linter; unique per plugin and linter. */
export function linterMarkerOwner(ref: string): string {
  return `mincode-plugin-linter:${ref}`;
}

/** The linter's title for `locale`. */
export function linterTitle(linter: Pick<PluginLinter, "title" | "titleLocalized">, locale: string | readonly string[]): string {
  return resolvePluginTitle(linter, locale);
}

/** Install preview: every linter's title, languages and whether it may run while typing, from the raw manifest. */
export function linterPreviewSummary(contributes: unknown): { title: string; languages: string[]; onType: boolean }[] {
  const entries = isRecord(contributes) ? contributes[LINTERS_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, LINTER_LIMITS.maxEntries).flatMap((entry) => {
    if (!isRecord(entry)) return [];
    const title = typeof entry.title === "string" && entry.title.trim() ? entry.title.trim().slice(0, LINTER_LIMITS.titleMaxRunes) : typeof entry.id === "string" ? entry.id.slice(0, 32) : "";
    const languages = Array.isArray(entry.languages)
      ? entry.languages.filter((language): language is string => typeof language === "string").map((language) => language.slice(0, 32))
      : [];
    const onType = Array.isArray(entry.triggers) && entry.triggers.includes("type");
    return title ? [{ title, languages: languages.slice(0, LINTER_LIMITS.languagesMax), onType }] : [];
  });
}
