/**
 * Plugin MCP servers (contribution point `mcpServers`), pure logic.
 *
 * Trust boundary: the Go host (backend/plugin/contrib_mcp_servers.go)
 * validates each entry at install; mcp_servers_launch.go resolves, confirms
 * and starts a server by (pluginId, serverId, projectRoot) only. The WebView
 * never sends a command, args or env. This module re-checks the display
 * fields of the snapshot (the "frontend" layer), builds the registry the
 * settings panel and the AI assistant read, and normalizes the user's list of
 * servers enabled for the assistant.
 */
import contributionContract from "./contribution-points.json" with { type: "json" };
import type { PointContribution } from "./contributions.ts";
import { resolvePluginTitle } from "./title.ts";

export const MCP_SERVERS_CONTRIBUTION_POINT = "mcpServers";
/** Global user state: the plugin MCP session ids the user enabled for the AI assistant. */
export const PLUGIN_MCP_SERVERS_STORAGE_KEY = "app_plugin_mcp_servers_v1";

export type PluginMcpServerPlatform = "darwin" | "linux" | "windows";

interface PointSpec {
  maxEntries: number;
  limits: Record<string, number>;
}

const spec = (contributionContract as unknown as { points: Record<string, PointSpec> }).points[MCP_SERVERS_CONTRIBUTION_POINT]!;
const MAX_ENTRIES = spec.maxEntries;
const TITLE_MAX_RUNES = spec.limits.titleMaxRunes ?? 64;
const DESCRIPTION_MAX_RUNES = spec.limits.descriptionMaxRunes ?? 200;
const LOCALES_MAX = spec.limits.localesMax ?? 16;
const MAX_ENABLED = 64;
const PLATFORMS: readonly PluginMcpServerPlatform[] = ["darwin", "linux", "windows"];
const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{1,127}$/;
const SESSION_ID_PATTERN = /^plugin:[a-z0-9][a-z0-9._-]{1,127}\/[a-z0-9][a-z0-9-]{0,31}$/;
const FINGERPRINT_PATTERN = /^[0-9a-f]{64}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8}){0,3}$/;
// eslint-disable-next-line no-control-regex -- rejecting control and bidi characters is the point
const UNSAFE_TEXT_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/;

export interface PluginMcpServer {
  /** Gateway session id `plugin:<pluginId>/<id>`. */
  sessionId: string;
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  description?: string;
  descriptionLocalized?: Record<string, string>;
  platforms: PluginMcpServerPlatform[];
  /** False for a server that does not work on a project: it starts without one, in the plugin root. */
  requiresProject: boolean;
  /** Host hash of the manifest entry; changes when the plugin changes the server. */
  specFingerprint: string;
}

/** The project root a server starts with: none for a server that does not need a project. */
export function mcpServerLaunchRoot(server: Pick<PluginMcpServer, "requiresProject">, projectRoot: string) {
  return server.requiresProject ? projectRoot.trim() : "";
}

/** Whether the server can start for a conversation or settings view with this project root. */
export function mcpServerAvailable(server: Pick<PluginMcpServer, "requiresProject">, projectRoot: string) {
  return !server.requiresProject || Boolean(projectRoot.trim());
}

export interface McpServerIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  server?: string;
  message: string;
}

export function pluginMcpServerId(pluginId: string, serverId: string): string {
  return `plugin:${pluginId}/${serverId}`;
}

export function isPluginMcpServerId(id: string): boolean {
  return SESSION_ID_PATTERN.test(id);
}

/** Splits `plugin:<pluginId>/<serverId>`; undefined for anything else. */
export function parsePluginMcpServerId(id: string): { pluginId: string; serverId: string } | undefined {
  if (!isPluginMcpServerId(id)) return undefined;
  const rest = id.slice("plugin:".length);
  const slash = rest.lastIndexOf("/");
  return { pluginId: rest.slice(0, slash), serverId: rest.slice(slash + 1) };
}

/** Maps `navigator.platform` (or a Go GOOS) to a contract platform name. */
export function mcpServerPlatform(platform: string): PluginMcpServerPlatform {
  const value = platform.toLowerCase();
  if (value.startsWith("win")) return "windows";
  if (value.startsWith("mac") || value === "darwin" || value.startsWith("iphone") || value.startsWith("ipad")) return "darwin";
  return "linux";
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validLine(value: unknown, maxRunes: number): value is string {
  if (typeof value !== "string") return false;
  const trimmed = value.trim();
  return trimmed !== "" && [...trimmed].length <= maxRunes && !UNSAFE_TEXT_PATTERN.test(trimmed);
}

function localizedLines(value: unknown, maxRunes: number): Record<string, string> | undefined | false {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) return false;
  const entries = Object.entries(value);
  if (entries.length > LOCALES_MAX) return false;
  const out: Record<string, string> = {};
  for (const [locale, text] of entries) {
    if (!LOCALE_PATTERN.test(locale) || !validLine(text, maxRunes)) return false;
    out[locale] = text.trim();
  }
  return entries.length ? out : undefined;
}

/** Re-validates the display fields of one host-accepted entry. */
export function validateMcpServerEntry(
  raw: unknown,
): { ok: true; server: Omit<PluginMcpServer, "sessionId" | "pluginId" | "pluginName"> } | { ok: false; error: string } {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!ID_PATTERN.test(id)) return { ok: false, error: `MCP server id "${id}" is invalid` };
  if (!validLine(raw.title, TITLE_MAX_RUNES)) return { ok: false, error: "title is invalid" };
  const titleLocalized = localizedLines(raw.titleLocalized, TITLE_MAX_RUNES);
  if (titleLocalized === false) return { ok: false, error: "titleLocalized is invalid" };
  let description: string | undefined;
  if (raw.description !== undefined && raw.description !== "") {
    if (!validLine(raw.description, DESCRIPTION_MAX_RUNES)) return { ok: false, error: "description is invalid" };
    description = raw.description.trim();
  }
  const descriptionLocalized = localizedLines(raw.descriptionLocalized, DESCRIPTION_MAX_RUNES);
  if (descriptionLocalized === false) return { ok: false, error: "descriptionLocalized is invalid" };
  const platforms = raw.platforms;
  if (
    !Array.isArray(platforms) ||
    platforms.length === 0 ||
    !platforms.every((platform) => (PLATFORMS as readonly unknown[]).includes(platform)) ||
    new Set(platforms).size !== platforms.length
  ) {
    return { ok: false, error: "platforms are invalid" };
  }
  const specFingerprint = typeof raw.specFingerprint === "string" ? raw.specFingerprint : "";
  if (!FINGERPRINT_PATTERN.test(specFingerprint)) return { ok: false, error: "specFingerprint is invalid" };
  return {
    ok: true,
    server: {
      id,
      title: raw.title.trim(),
      ...(titleLocalized ? { titleLocalized } : {}),
      ...(description ? { description } : {}),
      ...(descriptionLocalized ? { descriptionLocalized } : {}),
      platforms: [...(platforms as PluginMcpServerPlatform[])],
      requiresProject: raw.requiresProject !== false,
      specFingerprint,
    },
  };
}

/**
 * Builds the registry from the active process plugins, ordered by session id
 * (so by plugin id). Declarative plugins, plugins outside this workspace and
 * entries for another platform contribute nothing.
 */
export function collectMcpServers(
  contributions: readonly PointContribution[],
  platform?: PluginMcpServerPlatform,
): { servers: PluginMcpServer[]; issues: McpServerIssue[] } {
  const servers: PluginMcpServer[] = [];
  const issues: McpServerIssue[] = [];
  for (const contribution of contributions) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { server: issue.id } : {}) });
    }
    const seen = new Set<string>();
    contribution.point.entries.slice(0, MAX_ENTRIES).forEach((raw, entry) => {
      const result = validateMcpServerEntry(raw);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      if (seen.has(result.server.id)) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, server: result.server.id, message: `duplicate MCP server id "${result.server.id}"` });
        return;
      }
      seen.add(result.server.id);
      if (platform && !result.server.platforms.includes(platform)) return;
      servers.push({
        ...result.server,
        sessionId: pluginMcpServerId(contribution.pluginId, result.server.id),
        pluginId: contribution.pluginId,
        pluginName: contribution.pluginName,
      });
    });
  }
  servers.sort((a, b) => (a.sessionId < b.sessionId ? -1 : a.sessionId > b.sessionId ? 1 : 0));
  return { servers, issues };
}

/** The persisted enabled list: unique, well-formed plugin session ids only. */
export function normalizeEnabledPluginMcpServers(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return [...new Set(value.filter((id): id is string => typeof id === "string" && isPluginMcpServerId(id)))].slice(0, MAX_ENABLED);
}

/** Title or description for a locale tag or chain (see resolvePluginTitle). */
export function localizedMcpServerText(text: string, localized: Record<string, string> | undefined, locale: string | readonly string[]): string {
  return resolvePluginTitle({ title: text, titleLocalized: localized }, locale);
}

/** Install preview: one title line per declared server. */
export function mcpServerPreviewSummary(contributes: unknown): string[] {
  const entries = isRecord(contributes) ? contributes[MCP_SERVERS_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, MAX_ENTRIES).flatMap((entry) =>
    isRecord(entry) && validLine(entry.title, TITLE_MAX_RUNES) ? [entry.title.trim()] : []);
}

/** Host error code of a `<code>: message` error (plugin_process_launch.go). */
/** Host round trips behind the connector switch; none of them waits for the user. */
export type McpServerHostStep = "prepare" | "trust" | "save";
export const MCP_SERVER_STEP_TIMEOUT_MS = 15_000;

/**
 * Rejects with `timeout: <step>` when a host step does not answer in time, so
 * the switch stops spinning and says which step stalled.
 */
export function withMcpStepTimeout<T>(
  promise: Promise<T>,
  step: McpServerHostStep,
  timeoutMs = MCP_SERVER_STEP_TIMEOUT_MS,
): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`timeout: ${step} did not answer within ${timeoutMs} ms`)), timeoutMs);
    promise.then(
      (value) => { clearTimeout(timer); resolve(value); },
      (error: unknown) => { clearTimeout(timer); reject(error); },
    );
  });
}

export function mcpServerErrorCode(error: unknown): string {
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  const match = /^([a-z_]+):/.exec(message.trim());
  return match?.[1] ?? "";
}
