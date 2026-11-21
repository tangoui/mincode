/**
 * Frontend stage of the `jsonValidation` contribution point: re-validation of
 * entries the Go host already accepted (backend/plugin/
 * contrib_json_validation.go), the pure matcher that picks the schemas of one
 * JSON document, and the regex collection the Monaco stage probes before a
 * schema reaches the JSON worker. No DOM, Vue, IPC or Monaco here; the
 * runtime wiring lives in services/contributions/json-validation.ts and
 * services/monaco-json-schemas.ts.
 *
 * Snapshot entries carry only the schema's content-hash asset key; schema
 * bytes are fetched lazily through services/contribution-assets.ts.
 */
import { isContributionAssetKey } from "@/domain/plugins/contribution-assets";
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolvePluginTitle } from "@/domain/plugins/title";
import { globToRegExp } from "@/domain/files/filename-glob";
import type { RegexProbeCase } from "@/domain/editor/regex-safety";

export const JSON_VALIDATION_CONTRIBUTION_POINT = "jsonValidation";

/** Mirrors contribution-points.json jsonValidation limits (the host enforces them first). */
export const JSON_VALIDATION_LIMITS = {
  maxEntries: 16,
  fileMatchMax: 8,
  patternMaxLength: 128,
  titleMaxRunes: 64,
  schemaMaxDepth: 64,
  schemaRegexMax: 1024,
  schemaRegexMaxLength: 1024,
  schemasPerModel: 4,
} as const;

/** URI scheme of plugin schemas registered with Monaco's JSON defaults. */
export const PLUGIN_JSON_SCHEMA_URI_PREFIX = "mincode-plugin-schema://";

const PATTERN_ALPHABET = /^[A-Za-z0-9._\-*?/]+$/;
const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
// eslint-disable-next-line no-control-regex -- rejecting control characters is the point
const UNSAFE_TEXT_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069]/;

export interface JsonSchemaEntry {
  pluginId: string;
  pluginName: string;
  /** Index into contributes.jsonValidation. */
  entry: number;
  /** The schema's package path (the entry id). */
  id: string;
  title?: string;
  titleLocalized?: Record<string, string>;
  fileMatch: string[];
  /** Content-hash asset key of the canonical schema. */
  schema: string;
}

export interface JsonValidationIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  schema?: string;
  message: string;
}

/** The document a schema is matched against. */
export interface JsonSchemaTarget {
  /** Project-relative path with "/" separators; undefined for files outside every project. */
  relativePath?: string;
  basename: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isSafeText(value: string, maxRunes: number): boolean {
  const length = [...value].length;
  return length > 0 && length <= maxRunes && !UNSAFE_TEXT_PATTERN.test(value);
}

/**
 * Mirrors the host's fileMatch rules: a relative glob of letters, digits,
 * `. _ - * ? /` that ends in .json or .jsonc and names at least 3 literal
 * file-name characters (so `*.json` or `**\/*.json` cannot claim every JSON
 * file). Returns the problem, or undefined when the pattern is acceptable.
 */
export function jsonValidationPatternIssue(pattern: string): string | undefined {
  if (!pattern) return "fileMatch pattern is empty";
  if (pattern.length > JSON_VALIDATION_LIMITS.patternMaxLength) {
    return `fileMatch pattern exceeds ${JSON_VALIDATION_LIMITS.patternMaxLength} characters`;
  }
  if (!PATTERN_ALPHABET.test(pattern)) return `fileMatch pattern "${pattern}" may only use letters, digits, . _ - * ? and /`;
  const segments = pattern.split("/");
  if (segments.some((segment) => segment === "")) return `fileMatch pattern "${pattern}" must be a relative path without empty segments`;
  if (segments.some((segment) => segment === "." || segment === "..")) return `fileMatch pattern "${pattern}" must not use . or .. segments`;
  const base = segments[segments.length - 1].toLowerCase();
  const stem = base.endsWith(".jsonc") ? base.slice(0, -6) : base.endsWith(".json") ? base.slice(0, -5) : undefined;
  if (stem === undefined) return `fileMatch pattern "${pattern}" must end in .json or .jsonc`;
  if ([...stem.replace(/[*?]/g, "")].length < 3) {
    return `fileMatch pattern "${pattern}" is too broad: the file name needs at least 3 literal characters before .json`;
  }
  return undefined;
}

export type JsonValidationEntryResult =
  | { ok: true; entry: Omit<JsonSchemaEntry, "pluginId" | "pluginName" | "entry"> }
  | { ok: false; error: string };

/** Re-validates one snapshot entry `{id, title?, titleLocalized?, fileMatch, schema}`. */
export function validateJsonValidationEntry(raw: unknown): JsonValidationEntryResult {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!id || id.length > 256 || UNSAFE_TEXT_PATTERN.test(id)) return { ok: false, error: "entry needs a schema path" };
  if (!isContributionAssetKey(raw.schema)) return { ok: false, error: `schema "${id}" has no asset key` };
  if (!Array.isArray(raw.fileMatch) || raw.fileMatch.length < 1 || raw.fileMatch.length > JSON_VALIDATION_LIMITS.fileMatchMax) {
    return { ok: false, error: `schema "${id}" fileMatch needs 1-${JSON_VALIDATION_LIMITS.fileMatchMax} patterns` };
  }
  const fileMatch: string[] = [];
  const seen = new Set<string>();
  for (const value of raw.fileMatch) {
    if (typeof value !== "string") return { ok: false, error: `schema "${id}" fileMatch must contain strings` };
    const issue = jsonValidationPatternIssue(value);
    if (issue) return { ok: false, error: `schema "${id}" ${issue}` };
    const folded = value.toLowerCase();
    if (seen.has(folded)) return { ok: false, error: `schema "${id}" fileMatch repeats "${value}"` };
    seen.add(folded);
    fileMatch.push(value);
  }
  const entry: Omit<JsonSchemaEntry, "pluginId" | "pluginName" | "entry"> = { id, fileMatch, schema: raw.schema };
  if (raw.title !== undefined && raw.title !== "") {
    if (typeof raw.title !== "string" || !isSafeText(raw.title, JSON_VALIDATION_LIMITS.titleMaxRunes)) {
      return { ok: false, error: `schema "${id}" title is invalid` };
    }
    entry.title = raw.title;
  }
  if (isRecord(raw.titleLocalized)) {
    const titles: Record<string, string> = {};
    for (const [locale, title] of Object.entries(raw.titleLocalized)) {
      if (typeof title === "string" && isSafeText(title, JSON_VALIDATION_LIMITS.titleMaxRunes) && locale.length <= 32) {
        titles[locale] = title;
      }
    }
    if (Object.keys(titles).length) entry.titleLocalized = titles;
  }
  return { ok: true, entry };
}

/**
 * Validates every plugin's entries. Only plugins active in the current
 * workspace contribute (declarative plugins always are). Entries come back
 * ordered by pluginId, then entry order, so matching is deterministic.
 */
export function collectJsonValidationEntries(contributions: readonly PointContribution[]): {
  entries: JsonSchemaEntry[];
  issues: JsonValidationIssue[];
} {
  const entries: JsonSchemaEntry[] = [];
  const issues: JsonValidationIssue[] = [];
  const ordered = [...contributions].sort((a, b) => (a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0));
  for (const contribution of ordered) {
    if (!contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { schema: issue.id } : {}) });
    }
    const seen = new Set<string>();
    contribution.point.entries.slice(0, JSON_VALIDATION_LIMITS.maxEntries).forEach((raw, entry) => {
      const result = validateJsonValidationEntry(raw);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      if (seen.has(result.entry.id)) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, schema: result.entry.id, message: `duplicate jsonValidation schema "${result.entry.id}"` });
        return;
      }
      seen.add(result.entry.id);
      entries.push({ ...result.entry, pluginId: contribution.pluginId, pluginName: contribution.pluginName, entry });
    });
  }
  return { entries, issues };
}

/**
 * True when one fileMatch pattern matches the document. A pattern with "/"
 * is matched against the project-relative path (a leading `**\/` also matches
 * at the project root); otherwise against the basename. Files outside every
 * project only have a basename, so path patterns never match them.
 */
export function jsonSchemaPatternMatches(pattern: string, target: JsonSchemaTarget): boolean {
  if (!pattern.includes("/")) return globToRegExp(pattern).test(target.basename);
  const path = target.relativePath;
  if (!path) return false;
  const regex = globToRegExp(pattern);
  return regex.test(path) || (pattern.startsWith("**/") && regex.test(`/${path}`));
}

/** The schemas for one document: at most `schemasPerModel`, ordered by pluginId, then entry. */
export function matchJsonSchemas(target: JsonSchemaTarget, entries: readonly JsonSchemaEntry[]): JsonSchemaEntry[] {
  const matched: JsonSchemaEntry[] = [];
  for (const entry of entries) {
    if (!entry.fileMatch.some((pattern) => jsonSchemaPatternMatches(pattern, target))) continue;
    matched.push(entry);
    if (matched.length >= JSON_VALIDATION_LIMITS.schemasPerModel) break;
  }
  return matched;
}

/** Monaco schema URI of one plugin schema (never fetched: schema requests are disabled). */
export function pluginJsonSchemaUri(pluginId: string, key: string): string {
  return `${PLUGIN_JSON_SCHEMA_URI_PREFIX}${pluginId}/${key}.json`;
}

/**
 * The regexes the JSON worker would compile for a schema, in the form the
 * worker compiles them (vscode-json-languageservice `extendedRegExp`: a
 * leading "(?i)" becomes the i flag; the u flag is tried first). Mirrors the
 * host's positions: every string `pattern` value and every
 * `patternProperties` key. A regex no flag set can compile is skipped: the
 * worker ignores it. Returns undefined when a limit is exceeded.
 */
export function collectJsonSchemaRegexes(schema: unknown): RegexProbeCase[] | undefined {
  const cases: RegexProbeCase[] = [];
  const add = (source: string): boolean => {
    if (source.length > JSON_VALIDATION_LIMITS.schemaRegexMaxLength) return false;
    let body = source;
    let flags = "";
    if (body.startsWith("(?i)")) {
      body = body.slice(4);
      flags = "i";
    }
    for (const candidate of [`${flags}u`, flags]) {
      try {
        new RegExp(body, candidate);
        cases.push({ source: body, flags: candidate });
        break;
      } catch {
        // Try without the u flag, then skip (the worker ignores invalid patterns).
      }
    }
    return cases.length <= JSON_VALIDATION_LIMITS.schemaRegexMax;
  };
  const stack: Array<{ value: unknown; depth: number }> = [{ value: schema, depth: 0 }];
  while (stack.length) {
    const { value, depth } = stack.pop()!;
    if (depth > JSON_VALIDATION_LIMITS.schemaMaxDepth + 1) return undefined;
    if (Array.isArray(value)) {
      for (const item of value) stack.push({ value: item, depth: depth + 1 });
      continue;
    }
    if (!isRecord(value)) continue;
    for (const [key, child] of Object.entries(value)) {
      if (key === "pattern" && typeof child === "string" && !add(child)) return undefined;
      if (key === "patternProperties" && isRecord(child)) {
        for (const regex of Object.keys(child)) if (!add(regex)) return undefined;
      }
      stack.push({ value: child, depth: depth + 1 });
    }
  }
  return cases;
}

/** The entry's title for `locale`, else its schema path. */
export function jsonSchemaTitle(entry: Pick<JsonSchemaEntry, "title" | "titleLocalized" | "id">, locale: string | readonly string[]): string {
  return resolvePluginTitle({ title: entry.title ?? entry.id, titleLocalized: entry.titleLocalized }, locale);
}

/** Install preview: the number of schemas and every fileMatch pattern, from the raw manifest. */
export function jsonValidationPreviewSummary(contributes: unknown): { count: number; patterns: string[] } | null {
  const entries = isRecord(contributes) ? contributes[JSON_VALIDATION_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries) || entries.length === 0) return null;
  const patterns = new Set<string>();
  for (const entry of entries) {
    if (!isRecord(entry) || !Array.isArray(entry.fileMatch)) continue;
    for (const pattern of entry.fileMatch) {
      if (typeof pattern === "string" && pattern.trim()) patterns.add(pattern.trim().slice(0, JSON_VALIDATION_LIMITS.patternMaxLength));
    }
  }
  return { count: entries.length, patterns: [...patterns] };
}
