/**
 * Shared re-validation helpers for the provider contribution points that
 * call a plugin action on demand (treeViews, languageFeatures,
 * testProviders). They mirror the Go host text rules
 * (backend/plugin/contribution_text.go, contribution_project.go,
 * contribution_targets.go): unsafe characters are rejected, never stripped.
 * No DOM, Vue or IPC here.
 */
import type { PointContribution } from "@/domain/plugins/contributions";

export const PROVIDER_ENTRY_ID_PATTERN = /^[a-z][a-z0-9-]{0,31}$/;
export const PROVIDER_PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
export const PROVIDER_ACTION_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const VIEW_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const COMMAND_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const ARG_KEY_PATTERN = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;
// eslint-disable-next-line no-control-regex -- rejecting control characters is the point
const UNSAFE_LINE_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/;
// eslint-disable-next-line no-control-regex -- rejecting control characters is the point
const UNSAFE_MULTILINE_PATTERN = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\u2060\ufeff]/;

const encoder = new TextEncoder();

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function utf8Bytes(value: string): number {
  return encoder.encode(value).byteLength;
}

function runeCount(value: string) {
  return [...value].length;
}

/** Whether a string contains characters never allowed in single-line contributed text. */
export function hasUnsafeLineText(value: string): boolean {
  return UNSAFE_LINE_PATTERN.test(value);
}

/**
 * A trimmed single line of at most maxRunes characters. Returns undefined
 * for an absent or blank optional value, false when invalid.
 */
export function boundedLine(value: unknown, maxRunes: number, required: boolean): string | undefined | false {
  if (value === undefined && !required) return undefined;
  if (typeof value !== "string") return false;
  const trimmed = value.trim();
  if (!trimmed) return required ? false : undefined;
  if (runeCount(trimmed) > maxRunes || UNSAFE_LINE_PATTERN.test(trimmed)) return false;
  return trimmed;
}

/** Multi-line text (\n and \t allowed, CRLF normalized, not trimmed); undefined when absent, false when invalid. */
export function boundedMultiline(value: unknown, maxRunes: number): string | undefined | false {
  if (value === undefined) return undefined;
  if (typeof value !== "string") return false;
  const normalized = value.replace(/\r\n/g, "\n");
  if (runeCount(normalized) > maxRunes || UNSAFE_MULTILINE_PATTERN.test(normalized)) return false;
  return normalized;
}

/** A plugin-chosen item id: non-empty, at most maxBytes, no surrounding whitespace or unsafe characters. */
export function isProviderItemId(value: unknown, maxBytes: number): value is string {
  return typeof value === "string" && value !== "" && value.trim() === value && utf8Bytes(value) <= maxBytes && !UNSAFE_LINE_PATTERN.test(value);
}

/** A clean project-relative path (no leading slash, backslash, colon, `.`/`..` segments). */
export function isProjectRelativePath(value: unknown, maxBytes: number): value is string {
  if (typeof value !== "string" || !value || utf8Bytes(value) > maxBytes || UNSAFE_LINE_PATTERN.test(value)) return false;
  if (/[\\:]/.test(value) || value.startsWith("/") || value.endsWith("/") || value.includes("//")) return false;
  return value.split("/").every((segment) => segment !== "." && segment !== "..");
}

/** Locale → title map (the host already trimmed and bounded it). */
export function localizedTitles(value: unknown, maxRunes: number, localesMax: number): Record<string, string> | undefined | false {
  if (value === undefined) return undefined;
  if (!isRecord(value)) return false;
  const entries = Object.entries(value);
  if (entries.length > localesMax) return false;
  const out: Record<string, string> = {};
  for (const [locale, title] of entries) {
    const line = boundedLine(title, maxRunes, true);
    if (!line || !locale.trim() || locale.length > 32 || UNSAFE_LINE_PATTERN.test(locale)) return false;
    out[locale] = line;
  }
  return entries.length ? out : undefined;
}

export type ScalarArgs = Record<string, string | number | boolean>;

/** View/command args: at most maxProperties scalar values within maxBytes of JSON. */
export function scalarArgs(value: unknown, maxProperties: number, maxBytes: number): ScalarArgs | undefined | false {
  if (value === undefined) return undefined;
  if (!isRecord(value)) return false;
  const entries = Object.entries(value);
  if (entries.length > maxProperties) return false;
  const out: ScalarArgs = {};
  for (const [key, item] of entries) {
    if (!ARG_KEY_PATTERN.test(key)) return false;
    if (typeof item === "string") {
      if (UNSAFE_LINE_PATTERN.test(item)) return false;
      out[key] = item;
    } else if (typeof item === "boolean" || (typeof item === "number" && Number.isFinite(item))) {
      out[key] = item;
    } else {
      return false;
    }
  }
  if (utf8Bytes(JSON.stringify(out)) > maxBytes) return false;
  return entries.length ? out : undefined;
}

/** An http(s) URL without credentials, whitespace or backslashes; returns the normalized URL. */
export function normalizeProviderURL(value: unknown, maxBytes: number): string | undefined {
  if (typeof value !== "string" || !value || utf8Bytes(value) > maxBytes) return undefined;
  if (/[\s\\]/.test(value) || UNSAFE_LINE_PATTERN.test(value) || !/^https?:\/\//i.test(value)) return undefined;
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return undefined;
  }
  if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || !parsed.hostname || parsed.username || parsed.password) return undefined;
  return parsed.href;
}

export type ProviderItemTarget =
  | { type: "file"; path: string; line?: number }
  | { type: "view"; view: string; args?: ScalarArgs }
  | { type: "command"; command: string; args?: ScalarArgs }
  | { type: "url"; url: string };

export interface ProviderTargetLimits {
  argsMaxProperties: number;
  argsMaxBytes: number;
  urlMaxLength: number;
  pathMaxLength: number;
  lineMax: number;
}

/** Re-validates an item target (file/view/command/url); undefined when invalid. */
export function parseProviderTarget(value: unknown, limits: ProviderTargetLimits): ProviderItemTarget | undefined {
  if (!isRecord(value)) return undefined;
  const keys = Object.keys(value);
  const only = (...allowed: string[]) => keys.every((key) => key === "type" || allowed.includes(key));
  switch (value.type) {
    case "file": {
      if (!only("path", "line") || !isProjectRelativePath(value.path, limits.pathMaxLength)) return undefined;
      if (value.line === undefined) return { type: "file", path: value.path };
      const line = value.line;
      if (typeof line !== "number" || !Number.isInteger(line) || line < 1 || line > limits.lineMax) return undefined;
      return { type: "file", path: value.path, line };
    }
    case "view": {
      const args = scalarArgs(value.args, limits.argsMaxProperties, limits.argsMaxBytes);
      if (!only("view", "args") || typeof value.view !== "string" || !VIEW_ID_PATTERN.test(value.view) || args === false) return undefined;
      return { type: "view", view: value.view, ...(args ? { args } : {}) };
    }
    case "command": {
      const args = scalarArgs(value.args, limits.argsMaxProperties, limits.argsMaxBytes);
      if (!only("command", "args") || typeof value.command !== "string" || !COMMAND_ID_PATTERN.test(value.command) || args === false) return undefined;
      return { type: "command", command: value.command, ...(args ? { args } : {}) };
    }
    case "url": {
      if (!only("url")) return undefined;
      const url = normalizeProviderURL(value.url, limits.urlMaxLength);
      return url ? { type: "url", url } : undefined;
    }
    default:
      return undefined;
  }
}

export interface ProviderPointIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  id?: string;
  message: string;
}

/**
 * Visits the active process plugins in ascending plugin id order, reports
 * host issues and validates at most maxEntries entries each; duplicate ids
 * within a plugin are reported.
 */
export function collectProviderEntries<T extends { id: string }>(
  contributions: readonly PointContribution[],
  maxEntries: number,
  validate: (raw: unknown) => { ok: true; value: T } | { ok: false; error: string },
  label: string,
): { entries: (T & { pluginId: string; pluginName: string })[]; issues: ProviderPointIssue[] } {
  const entries: (T & { pluginId: string; pluginName: string })[] = [];
  const issues: ProviderPointIssue[] = [];
  const ordered = [...contributions].sort((a, b) => (a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0));
  for (const contribution of ordered) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace || !PROVIDER_PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { id: issue.id } : {}) });
    }
    const seen = new Set<string>();
    contribution.point.entries.slice(0, maxEntries).forEach((raw, entry) => {
      const result = validate(raw);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      if (seen.has(result.value.id)) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, id: result.value.id, message: `duplicate ${label} id "${result.value.id}"` });
        return;
      }
      seen.add(result.value.id);
      entries.push({ ...result.value, pluginId: contribution.pluginId, pluginName: contribution.pluginName });
    });
  }
  return { entries, issues };
}

/** A non-negative safe integer count from a host response, else 0. */
export function responseCount(value: unknown): number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0 ? value : 0;
}

/** Install-preview titles of a point's entries (raw manifest, best effort). */
export function providerPreviewTitles(contributes: unknown, point: string, maxEntries: number, maxRunes: number): string[] {
  const entries = isRecord(contributes) ? contributes[point] : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, maxEntries).flatMap((entry) => {
    if (!isRecord(entry) || typeof entry.id !== "string" || !PROVIDER_ENTRY_ID_PATTERN.test(entry.id)) return [];
    const title = typeof entry.title === "string" ? [...entry.title.trim()].slice(0, maxRunes).join("") : "";
    return [title && !UNSAFE_LINE_PATTERN.test(title) ? title : entry.id];
  });
}
