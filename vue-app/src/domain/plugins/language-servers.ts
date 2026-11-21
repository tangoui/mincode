/**
 * Plugin language servers (contribution point `languageServers`), pure logic.
 *
 * Trust boundary: the Go host (backend/plugin/contrib_language_servers.go)
 * validates each entry at install; language_servers_launch.go resolves,
 * confirms and starts a server by (pluginId, serverId, projectRoot) only. The
 * WebView never sends a command, args, env or initializationOptions. This
 * module re-checks the display fields of the snapshot (the "frontend" layer)
 * and builds the registry the project LSP runtime and dialog read.
 */
import contributionContract from "./contribution-points.json" with { type: "json" };
import type { PointContribution } from "./contributions.ts";
import { LSP_LANGUAGE_ID_PATTERN, pluginLspServerId } from "../projects/lsp.ts";

export const LANGUAGE_SERVERS_CONTRIBUTION_POINT = "languageServers";

interface PointSpec {
  maxEntries: number;
  limits: Record<string, number>;
}

const spec = (contributionContract as unknown as { points: Record<string, PointSpec> }).points[LANGUAGE_SERVERS_CONTRIBUTION_POINT]!;
const MAX_ENTRIES = spec.maxEntries;
const TITLE_MAX_RUNES = spec.limits.titleMaxRunes ?? 64;
const LOCALES_MAX = spec.limits.localesMax ?? 16;
const LANGUAGES_MAX = spec.limits.languagesMax ?? 8;
const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{1,127}$/;
const FINGERPRINT_PATTERN = /^[0-9a-f]{64}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8}){0,3}$/;
// eslint-disable-next-line no-control-regex -- rejecting control and bidi characters is the point
const UNSAFE_TEXT_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/;

export interface PluginLanguageServer {
  /** Session id `plugin:<pluginId>/<id>`. */
  sessionId: string;
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  languages: string[];
  /** Host hash of the manifest entry; changes when the plugin changes the server. */
  specFingerprint: string;
}

export interface LanguageServerIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  server?: string;
  message: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validLine(value: unknown, maxRunes: number): value is string {
  if (typeof value !== "string") return false;
  const trimmed = value.trim();
  return trimmed !== "" && [...trimmed].length <= maxRunes && !UNSAFE_TEXT_PATTERN.test(trimmed);
}

function localizedTitles(value: unknown): Record<string, string> | undefined | false {
  if (value === undefined) return undefined;
  if (!isRecord(value)) return false;
  const entries = Object.entries(value);
  if (entries.length > LOCALES_MAX) return false;
  const out: Record<string, string> = {};
  for (const [locale, title] of entries) {
    if (!LOCALE_PATTERN.test(locale) || !validLine(title, TITLE_MAX_RUNES)) return false;
    out[locale] = title.trim();
  }
  return entries.length ? out : undefined;
}

/** Re-validates the display fields of one host-accepted entry. */
export function validateLanguageServerEntry(
  raw: unknown,
): { ok: true; server: Omit<PluginLanguageServer, "sessionId" | "pluginId" | "pluginName"> } | { ok: false; error: string } {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!ID_PATTERN.test(id)) return { ok: false, error: `language server id "${id}" is invalid` };
  if (!validLine(raw.title, TITLE_MAX_RUNES)) return { ok: false, error: "title is invalid" };
  const titleLocalized = localizedTitles(raw.titleLocalized);
  if (titleLocalized === false) return { ok: false, error: "titleLocalized is invalid" };
  const languages = raw.languages;
  if (
    !Array.isArray(languages) ||
    languages.length === 0 ||
    languages.length > LANGUAGES_MAX ||
    !languages.every((language) => typeof language === "string" && LSP_LANGUAGE_ID_PATTERN.test(language)) ||
    new Set(languages).size !== languages.length
  ) {
    return { ok: false, error: "languages are invalid" };
  }
  const specFingerprint = typeof raw.specFingerprint === "string" ? raw.specFingerprint : "";
  if (!FINGERPRINT_PATTERN.test(specFingerprint)) return { ok: false, error: "specFingerprint is invalid" };
  return {
    ok: true,
    server: {
      id,
      title: raw.title.trim(),
      ...(titleLocalized ? { titleLocalized } : {}),
      languages: [...(languages as string[])],
      specFingerprint,
    },
  };
}

/**
 * Builds the registry from the active process plugins, ordered by session id
 * (so by plugin id). Declarative plugins and plugins outside this workspace
 * contribute nothing.
 */
export function collectLanguageServers(contributions: readonly PointContribution[]): {
  servers: PluginLanguageServer[];
  issues: LanguageServerIssue[];
} {
  const servers: PluginLanguageServer[] = [];
  const issues: LanguageServerIssue[] = [];
  for (const contribution of contributions) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { server: issue.id } : {}) });
    }
    const seen = new Set<string>();
    contribution.point.entries.slice(0, MAX_ENTRIES).forEach((raw, entry) => {
      const result = validateLanguageServerEntry(raw);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      if (seen.has(result.server.id)) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, server: result.server.id, message: `duplicate language server id "${result.server.id}"` });
        return;
      }
      seen.add(result.server.id);
      servers.push({
        ...result.server,
        sessionId: pluginLspServerId(contribution.pluginId, result.server.id),
        pluginId: contribution.pluginId,
        pluginName: contribution.pluginName,
      });
    });
  }
  servers.sort((a, b) => (a.sessionId < b.sessionId ? -1 : a.sessionId > b.sessionId ? 1 : 0));
  return { servers, issues };
}

/** Install preview: one `Title (lang, lang)` line per declared server. */
export function languageServerPreviewSummary(contributes: unknown): string[] {
  const entries = isRecord(contributes) ? contributes[LANGUAGE_SERVERS_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, MAX_ENTRIES).flatMap((entry) => {
    if (!isRecord(entry) || !validLine(entry.title, TITLE_MAX_RUNES)) return [];
    const languages = Array.isArray(entry.languages)
      ? entry.languages.filter((language): language is string => typeof language === "string" && LSP_LANGUAGE_ID_PATTERN.test(language)).slice(0, LANGUAGES_MAX)
      : [];
    return [languages.length ? `${entry.title.trim()} (${languages.join(", ")})` : entry.title.trim()];
  });
}

/** Host error code of a `<code>: message` error (plugin_process_launch.go). */
export function languageServerErrorCode(error: unknown): string {
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  const match = /^([a-z]+):/.exec(message.trim());
  return match?.[1] ?? "";
}
