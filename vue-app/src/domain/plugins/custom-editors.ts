/**
 * Pure model of the `customEditors` contribution point (process plugins). The
 * Go host (backend/plugin/contrib_custom_editors.go) validates
 * entries; they are still untrusted here, so every field is re-checked with
 * the same rules and anything invalid is dropped. Nothing here touches IPC
 * or Vue.
 *
 * Resolution order for opening a file: user association > plugin `default`
 * editor (first by pluginId) > built-in text editor. Sensitive filenames are
 * never opened in a plugin editor automatically.
 */
import { globToRegExp } from "@/domain/files/filename-glob";
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const CUSTOM_EDITORS_CONTRIBUTION_POINT = "customEditors";

// Mirrors `points.customEditors` in plugins/api/contribution-points.json
// (vue-app/scripts/custom-editors.test.mjs keeps them in sync).
export const CUSTOM_EDITOR_MAX_ENTRIES = 16;
export const CUSTOM_EDITOR_PATTERNS_MAX = 16;
export const CUSTOM_EDITOR_PATTERN_MAX_LENGTH = 128;
export const CUSTOM_EDITOR_TITLE_MAX_RUNES = 64;
export const CUSTOM_EDITOR_LOCALES_MAX = 16;
export const CUSTOM_EDITOR_READ_MAX_BYTES = 64 * 1024 * 1024;
export const CUSTOM_EDITOR_WRITE_MAX_BYTES = 16 * 1024 * 1024;
export const CUSTOM_EDITOR_CHUNK_BYTES = 128 * 1024;
export const CUSTOM_EDITOR_OPEN_DOCUMENTS_PER_PLUGIN = 16;
export const CUSTOM_EDITOR_SENSITIVE_FILENAMES: readonly string[] = [
  ".env",
  ".env.local",
  "server.pem",
  "server.key",
  "id_rsa",
  "id_ed25519",
  "cert.p12",
  "cert.pfx",
  "vault.kdbx",
  ".npmrc",
  ".netrc",
  ".pgpass",
  "app.keystore",
  "app.jks",
  "credentials",
  "terraform.tfstate",
];

/** Association value that pins the built-in text editor. */
export const TEXT_EDITOR_ASSOCIATION = "text";

const ID_PATTERN = /^[a-z][a-z0-9-]{0,31}$/;
const VIEW_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const PATTERN_CHARACTERS = /^[A-Za-z0-9._\-/*?+@]+$/;
const REJECTED_PATTERNS = new Set(["*", "**", "**/*", "*.*", "*.?", ".*"]);
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,3}$/;
const ICON_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/;
// eslint-disable-next-line no-control-regex
const UNSAFE_TEXT = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/g;
// Defensive superset of the contract list: also never auto-open variants.
const SENSITIVE_SUFFIXES = [".pem", ".key", ".p12", ".pfx", ".kdbx", ".keystore", ".jks", ".tfstate"];
const SENSITIVE_PREFIXES = [".env.", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa"];

export type CustomEditorPriority = "default" | "option";
export type CustomEditorAccess = "read" | "readWrite";

export interface CustomEditorContribution {
  /** `<pluginId>.<id>`; the association value for this editor. */
  key: string;
  pluginId: string;
  pluginName: string;
  id: string;
  viewId: string;
  title: string;
  titleLocalized?: Record<string, string>;
  filenamePatterns: string[];
  priority: CustomEditorPriority;
  access: CustomEditorAccess;
  icon?: string;
}

export interface EditorAssociations {
  /** Pattern (`*.ext` or a basename, lowercase) → editor key or "text". */
  byPattern: Record<string, string>;
  /** Editor keys whose `default` priority the user confirmed once. */
  confirmedDefaults: string[];
}

export interface ResolvedEditors {
  /** Editor to open automatically; undefined means the built-in text editor. */
  default?: CustomEditorContribution;
  /** Where the default came from. */
  source: "user" | "plugin" | "builtin";
  /** True when a plugin default still needs the one-time host confirmation. */
  needsConfirmation: boolean;
  /** Every editor whose patterns match (for Open With…). */
  options: CustomEditorContribution[];
  /** True when the file is sensitive and was kept in the text editor. */
  sensitive: boolean;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function sanitizeText(value: string, maxRunes: number) {
  const clean = value.replace(UNSAFE_TEXT, "").trim();
  return Array.from(clean).slice(0, maxRunes).join("");
}

export function customEditorKey(pluginId: string, editorId: string) {
  return `${pluginId}.${editorId}`;
}

/** Splits `<pluginId>.<editorId>` (plugin ids may contain dots; editor ids cannot). */
export function parseCustomEditorKey(key: string): { pluginId: string; editorId: string } | undefined {
  const index = key.lastIndexOf(".");
  if (index <= 0) return undefined;
  const pluginId = key.slice(0, index);
  const editorId = key.slice(index + 1);
  return ID_PATTERN.test(editorId) && pluginId.length <= 128 ? { pluginId, editorId } : undefined;
}

/** The C1 filename-pattern rules (same as the Go host, CLI, and cloud). */
export function isValidCustomEditorPattern(pattern: unknown): pattern is string {
  if (typeof pattern !== "string" || pattern.length === 0 || pattern.length > CUSTOM_EDITOR_PATTERN_MAX_LENGTH) return false;
  if (!PATTERN_CHARACTERS.test(pattern) || pattern.startsWith("/") || pattern.includes("..")) return false;
  if (REJECTED_PATTERNS.has(pattern)) return false;
  const lastWildcard = Math.max(pattern.lastIndexOf("*"), pattern.lastIndexOf("?"));
  if (lastWildcard < 0) return true;
  const suffix = pattern.slice(lastWildcard + 1);
  return suffix.length >= 2 && suffix.includes(".") && !suffix.includes("/");
}

function normalizePath(path: string) {
  return path.replace(/\\/g, "/").replace(/^\.\/+/, "").replace(/^\/+/, "");
}

function basenameOf(path: string) {
  const normalized = normalizePath(path);
  return normalized.slice(normalized.lastIndexOf("/") + 1);
}

/** Case-insensitive match against the basename, or the relative path when the pattern has `/`. */
export function customEditorPatternMatches(pattern: string, relativePath: string) {
  const normalized = normalizePath(relativePath);
  const target = pattern.includes("/") ? normalized : basenameOf(normalized);
  return globToRegExp(pattern).test(target);
}

export function customEditorMatchesPath(editor: Pick<CustomEditorContribution, "filenamePatterns">, relativePath: string) {
  return editor.filenamePatterns.some((pattern) => customEditorPatternMatches(pattern, relativePath));
}

/** Sensitive files never open in a plugin editor unless the user explicitly picks one. */
export function isSensitiveFilename(path: string) {
  const name = basenameOf(path).toLowerCase();
  if (!name) return false;
  if (CUSTOM_EDITOR_SENSITIVE_FILENAMES.includes(name)) return true;
  return SENSITIVE_SUFFIXES.some((suffix) => name.endsWith(suffix))
    || SENSITIVE_PREFIXES.some((prefix) => name.startsWith(prefix));
}

function parseTitleLocalized(value: unknown): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  let count = 0;
  for (const [locale, title] of Object.entries(record)) {
    if (count >= CUSTOM_EDITOR_LOCALES_MAX) break;
    if (!LOCALE_PATTERN.test(locale) || typeof title !== "string") continue;
    const clean = sanitizeText(title, CUSTOM_EDITOR_TITLE_MAX_RUNES);
    if (!clean) continue;
    result[locale] = clean;
    count += 1;
  }
  return count > 0 ? result : undefined;
}

/** Re-validates one entry; returns undefined when anything is out of contract. */
export function parseCustomEditorEntry(
  value: unknown,
  plugin: { pluginId: string; pluginName: string },
): CustomEditorContribution | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const id = record.id;
  const viewId = record.viewId;
  if (typeof id !== "string" || !ID_PATTERN.test(id)) return undefined;
  if (typeof viewId !== "string" || !VIEW_ID_PATTERN.test(viewId)) return undefined;
  const patterns = record.filenamePatterns;
  if (!Array.isArray(patterns) || patterns.length < 1 || patterns.length > CUSTOM_EDITOR_PATTERNS_MAX) return undefined;
  if (!patterns.every(isValidCustomEditorPattern)) return undefined;
  const priority = record.priority === undefined ? "option" : record.priority;
  if (priority !== "default" && priority !== "option") return undefined;
  const access = record.access === undefined ? "read" : record.access;
  if (access !== "read" && access !== "readWrite") return undefined;
  const filenamePatterns = [...new Set(patterns as string[])];
  // Contract: a default editor must never claim a sensitive filename.
  if (priority === "default" && filenamePatterns.some((pattern) =>
    CUSTOM_EDITOR_SENSITIVE_FILENAMES.some((name) => customEditorPatternMatches(pattern, name)))) return undefined;
  const title = typeof record.title === "string" ? sanitizeText(record.title, CUSTOM_EDITOR_TITLE_MAX_RUNES) : "";
  const icon = typeof record.icon === "string" && ICON_PATTERN.test(record.icon) ? record.icon : undefined;
  return {
    key: customEditorKey(plugin.pluginId, id),
    pluginId: plugin.pluginId,
    pluginName: sanitizeText(plugin.pluginName, CUSTOM_EDITOR_TITLE_MAX_RUNES) || plugin.pluginId,
    id,
    viewId,
    title: title || id,
    titleLocalized: parseTitleLocalized(record.titleLocalized),
    filenamePatterns,
    priority,
    access,
    icon,
  };
}

export interface CustomEditorPreviewEntry {
  title: string;
  patterns: string[];
  priority: CustomEditorPriority;
  access: CustomEditorAccess;
}

/** Install preview (manifest `contributes.customEditors`): what each editor opens and whether it writes. */
export function customEditorPreviewSummary(contributes: unknown, pluginId = "plugin"): CustomEditorPreviewEntry[] {
  const entries = objectValue(contributes)?.customEditors;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, CUSTOM_EDITOR_MAX_ENTRIES).flatMap((entry) => {
    const editor = parseCustomEditorEntry(entry, { pluginId, pluginName: pluginId });
    return editor ? [{ title: editor.title, patterns: editor.filenamePatterns, priority: editor.priority, access: editor.access }] : [];
  });
}

/** Builds the validated editor list: process plugins active in the workspace, sorted by pluginId then id. */
export function resolveCustomEditors(contributions: readonly PointContribution[]): CustomEditorContribution[] {
  const editors: CustomEditorContribution[] = [];
  const seen = new Set<string>();
  const sorted = [...contributions].sort((left, right) => left.pluginId.localeCompare(right.pluginId));
  for (const contribution of sorted) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace) continue;
    const entries = Array.isArray(contribution.point?.entries) ? contribution.point.entries : [];
    let count = 0;
    for (const entry of entries) {
      if (count >= CUSTOM_EDITOR_MAX_ENTRIES) break;
      const editor = parseCustomEditorEntry(entry, contribution);
      if (!editor || seen.has(editor.key)) continue;
      seen.add(editor.key);
      editors.push(editor);
      count += 1;
    }
  }
  return editors.sort((left, right) => left.pluginId.localeCompare(right.pluginId) || left.id.localeCompare(right.id));
}

export function customEditorTitle(
  editor: Pick<CustomEditorContribution, "title" | "titleLocalized">,
  locale: string | readonly string[],
) {
  return resolvePluginTitle({ title: editor.title, titleLocalized: editor.titleLocalized }, locale);
}

/**
 * The association key the tab menu's "Set as default" writes: `*.ext`
 * (lowercase, last extension) or the lowercase basename when there is none.
 */
export function associationPatternForPath(path: string) {
  const name = basenameOf(path).toLowerCase();
  const dot = name.lastIndexOf(".");
  return dot > 0 && dot < name.length - 1 ? `*${name.slice(dot)}` : name;
}

export function emptyEditorAssociations(): EditorAssociations {
  return { byPattern: {}, confirmedDefaults: [] };
}

const MAX_ASSOCIATIONS = 256;
const ASSOCIATION_KEY_PATTERN = /^(?:\*\.[a-z0-9._+@-]{1,126}|[a-z0-9._+@-]{1,128})$/;

function isAssociationValue(value: unknown): value is string {
  return value === TEXT_EDITOR_ASSOCIATION || (typeof value === "string" && value.length <= 200 && parseCustomEditorKey(value) !== undefined);
}

/** Sanitizes persisted `app_editor_associations_v1`; unknown editor keys stay (inert until the plugin returns). */
export function normalizeEditorAssociations(value: unknown): EditorAssociations {
  const record = objectValue(value);
  const result = emptyEditorAssociations();
  const byPattern = objectValue(record?.byPattern);
  if (byPattern) {
    for (const [pattern, editor] of Object.entries(byPattern)) {
      if (Object.keys(result.byPattern).length >= MAX_ASSOCIATIONS) break;
      const key = pattern.toLowerCase();
      if (!ASSOCIATION_KEY_PATTERN.test(key) || key.includes("..") || !isAssociationValue(editor)) continue;
      result.byPattern[key] = editor;
    }
  }
  if (Array.isArray(record?.confirmedDefaults)) {
    for (const key of record.confirmedDefaults) {
      if (result.confirmedDefaults.length >= MAX_ASSOCIATIONS) break;
      if (typeof key === "string" && key !== TEXT_EDITOR_ASSOCIATION && isAssociationValue(key) && !result.confirmedDefaults.includes(key)) {
        result.confirmedDefaults.push(key);
      }
    }
  }
  return result;
}

/** The user association for a path: exact basename first, then `*.ext`. */
export function associationForPath(associations: EditorAssociations, path: string) {
  const name = basenameOf(path).toLowerCase();
  const exact = associations.byPattern[name];
  if (exact !== undefined) return { pattern: name, value: exact };
  const pattern = associationPatternForPath(path);
  const byExtension = associations.byPattern[pattern];
  return byExtension !== undefined ? { pattern, value: byExtension } : undefined;
}

/** Resolution order: user association > plugin default (first by pluginId) > built-in text. */
export function resolveEditorsForPath(
  editors: readonly CustomEditorContribution[],
  relativePath: string,
  associations: EditorAssociations = emptyEditorAssociations(),
): ResolvedEditors {
  const options = editors.filter((editor) => customEditorMatchesPath(editor, relativePath));
  const sensitive = isSensitiveFilename(relativePath);
  const builtin: ResolvedEditors = { source: "builtin", needsConfirmation: false, options, sensitive };
  if (sensitive) return builtin;
  const association = associationForPath(associations, relativePath);
  if (association?.value === TEXT_EDITOR_ASSOCIATION) return { ...builtin, source: "user" };
  if (association) {
    const chosen = options.find((editor) => editor.key === association.value);
    // Unknown or disabled editors are inert: fall through to plugin defaults.
    if (chosen) return { default: chosen, source: "user", needsConfirmation: false, options, sensitive };
  }
  const pluginDefault = options.find((editor) => editor.priority === "default");
  if (!pluginDefault) return builtin;
  return {
    default: pluginDefault,
    source: "plugin",
    needsConfirmation: !associations.confirmedDefaults.includes(pluginDefault.key),
    options,
    sensitive,
  };
}
