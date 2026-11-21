/**
 * Plugin ACP agents (contribution point `acpAgents`), pure logic.
 *
 * Trust boundary: the Go host (backend/plugin/contrib_acp_agents.go)
 * validates each entry at install; acp_agents_launch.go resolves and confirms
 * an agent by (pluginId, agentId, projectRoot) only, and acp_launch_plugin
 * starts it by the same reference. The WebView never sends a command, args or
 * env. This module re-checks the display fields of the snapshot and builds
 * the list the settings page and the AI assistant offer, without the launch.
 */
import contributionContract from "./contribution-points.json" with { type: "json" };
import type { PointContribution } from "./contributions.ts";
import { mcpServerPlatform } from "./mcp-servers.ts";

export const ACP_AGENTS_CONTRIBUTION_POINT = "acpAgents";

export type AcpAgentPlatform = "darwin" | "linux" | "windows";

export interface PluginAcpAgent {
  pluginId: string;
  pluginName: string;
  agentId: string;
  title: string;
  titleLocalized?: Record<string, string>;
  description?: string;
  descriptionLocalized?: Record<string, string>;
  platforms?: string[];
}

interface PointSpec {
  maxEntries: number;
  limits: Record<string, number>;
}

const spec = (contributionContract as unknown as { points: Record<string, PointSpec> }).points[ACP_AGENTS_CONTRIBUTION_POINT]!;
const MAX_ENTRIES = spec.maxEntries;
const TITLE_MAX_RUNES = spec.limits.titleMaxRunes ?? 64;
const DESCRIPTION_MAX_RUNES = spec.limits.descriptionMaxRunes ?? 200;
const LOCALES_MAX = spec.limits.localesMax ?? 16;
const PLATFORMS: readonly AcpAgentPlatform[] = ["darwin", "linux", "windows"];
const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,63}$/;
const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{1,127}$/;
const LOCALE_PATTERN = /^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8}){0,3}$/;
// eslint-disable-next-line no-control-regex -- rejecting control and bidi characters is the point
const UNSAFE_TEXT_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/;

/** Maps `navigator.platform` (or a Go GOOS) to a contract platform name. */
export function acpAgentPlatform(platform: string): AcpAgentPlatform {
  return mcpServerPlatform(platform);
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

/** Re-validates the display fields of one host-accepted entry; the launch fields are dropped. */
export function validateAcpAgentEntry(
  raw: unknown,
): { ok: true; agent: Omit<PluginAcpAgent, "pluginId" | "pluginName"> } | { ok: false; error: string } {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const agentId = typeof raw.id === "string" ? raw.id : "";
  if (!ID_PATTERN.test(agentId)) return { ok: false, error: `ACP agent id "${agentId}" is invalid` };
  if (!validLine(raw.title, TITLE_MAX_RUNES)) return { ok: false, error: "title is invalid" };
  const titleLocalized = localizedLines(raw.titleLocalized, TITLE_MAX_RUNES);
  if (titleLocalized === false) return { ok: false, error: "titleLocalized is invalid" };
  let description: string | undefined;
  if (raw.description !== undefined && raw.description !== null && raw.description !== "") {
    if (!validLine(raw.description, DESCRIPTION_MAX_RUNES)) return { ok: false, error: "description is invalid" };
    description = raw.description.trim();
  }
  const descriptionLocalized = localizedLines(raw.descriptionLocalized, DESCRIPTION_MAX_RUNES);
  if (descriptionLocalized === false) return { ok: false, error: "descriptionLocalized is invalid" };
  // The host always fills platforms; an absent list means every platform.
  const platforms = raw.platforms;
  if (
    platforms !== undefined &&
    platforms !== null &&
    (!Array.isArray(platforms) ||
      platforms.length === 0 ||
      !platforms.every((platform) => (PLATFORMS as readonly unknown[]).includes(platform)) ||
      new Set(platforms).size !== platforms.length)
  ) {
    return { ok: false, error: "platforms are invalid" };
  }
  return {
    ok: true,
    agent: {
      agentId,
      title: raw.title.trim(),
      ...(titleLocalized ? { titleLocalized } : {}),
      ...(description ? { description } : {}),
      ...(descriptionLocalized ? { descriptionLocalized } : {}),
      ...(Array.isArray(platforms) ? { platforms: [...(platforms as string[])] } : {}),
    },
  };
}

/**
 * The agents the user can pick: entries of process plugins active in this
 * workspace (the snapshot holds enabled plugins only) that run on platform,
 * ordered by plugin id then agent id. Invalid and duplicate entries are
 * skipped; the host already reports its own issues on the plugin page.
 */
export function collectAcpAgents(contributions: readonly PointContribution[], platform?: AcpAgentPlatform): PluginAcpAgent[] {
  const agents: PluginAcpAgent[] = [];
  for (const contribution of contributions) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    const seen = new Set<string>();
    for (const raw of contribution.point.entries.slice(0, MAX_ENTRIES)) {
      const result = validateAcpAgentEntry(raw);
      if (!result.ok || seen.has(result.agent.agentId)) continue;
      seen.add(result.agent.agentId);
      if (platform && result.agent.platforms && !result.agent.platforms.includes(platform)) continue;
      agents.push({ pluginId: contribution.pluginId, pluginName: contribution.pluginName, ...result.agent });
    }
  }
  const key = (agent: PluginAcpAgent) => `${agent.pluginId}/${agent.agentId}`;
  return agents.sort((a, b) => (key(a) < key(b) ? -1 : key(a) > key(b) ? 1 : 0));
}

/** Host error code of a `<code>: message` error (plugin_process_launch.go, acp.go). */
export function acpAgentErrorCode(error: unknown): string {
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : "";
  const match = /^([a-z_]+):/.exec(message.trim());
  return match?.[1] ?? "";
}

/** The agent is not confirmed, or changed since the user confirmed it: prepare and confirm again. */
export function isAcpAgentUntrustedError(error: unknown): boolean {
  return acpAgentErrorCode(error) === "untrusted";
}

/** The plugin, agent or platform is gone, or this build has no plugin agents. */
export function isAcpAgentUnavailableError(error: unknown): boolean {
  return acpAgentErrorCode(error) === "unavailable";
}
