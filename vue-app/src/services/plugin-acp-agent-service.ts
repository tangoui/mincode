/**
 * Plugin ACP agents (contributes.acpAgents): the IPC boundary.
 *
 * The WebView names an agent only by `(pluginId, agentId)` plus the
 * conversation's project root. The Go host looks the entry up in its own
 * snapshot, re-validates and resolves it
 * (backend/plugin/acp_agents_launch.go) and acp_launch_plugin
 * refuses to start it until the user confirmed exactly that fingerprint. One
 * confirmation covers every project (`${projectRoot}` stays deferred), and it
 * is required in every approval mode. Failures carry the host codes
 * `untrusted:` / `unavailable:` (see isAcpAgentUntrustedError and
 * isAcpAgentUnavailableError in domain/plugins/acp-agents.ts).
 */
import type { ResolvedProcessLaunch } from "@/services/plugin-launch-confirm";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export interface PluginAcpAgentLaunch extends ResolvedProcessLaunch {
  agentId: string;
  description?: string;
  descriptionLocalized?: Record<string, string>;
  projectRoot: string;
}

/** acp_launch_plugin answers like acp_launch (the agent's initialize result). */
export interface AcpLaunchResult {
  agentCapabilities?: unknown;
  authMethods?: unknown[];
  protocolVersion?: number;
}

const FINGERPRINT_PATTERN = /^[0-9a-f]{64}$/;

function requireDesktop() {
  if (!isDesktopRuntime()) throw new Error("unavailable: plugin ACP agents need the MinCode desktop app");
}

function target(pluginId: string, agentId: string, projectRoot: string) {
  return { pluginId: pluginId.trim(), agentId: agentId.trim(), projectRoot: projectRoot.trim() };
}

function parseLaunch(raw: unknown, operation: string): PluginAcpAgentLaunch {
  const record = typeof raw === "object" && raw !== null && !Array.isArray(raw) ? (raw as Record<string, unknown>) : undefined;
  const valid =
    record &&
    typeof record.pluginId === "string" &&
    typeof record.agentId === "string" &&
    typeof record.projectRoot === "string" &&
    typeof record.cwd === "string" &&
    typeof record.fingerprint === "string" &&
    FINGERPRINT_PATTERN.test(record.fingerprint) &&
    Array.isArray(record.argv) &&
    record.argv.length > 0 &&
    record.argv.every((arg) => typeof arg === "string");
  if (!valid) throw new Error(`unavailable: ${operation} returned an invalid launch spec`);
  const env = typeof record.env === "object" && record.env !== null ? (record.env as Record<string, string>) : {};
  return {
    ...(record as unknown as PluginAcpAgentLaunch),
    env,
    // Go encodes an empty file list as null.
    pluginFiles: Array.isArray(record.pluginFiles) ? (record.pluginFiles as PluginAcpAgentLaunch["pluginFiles"]) : [],
    trusted: record.trusted === true,
    previouslyTrusted: record.previouslyTrusted === true,
  };
}

/** Resolves the agent's launch for projectRoot on the host (nothing is started). */
export async function prepareAcpAgent(pluginId: string, agentId: string, projectRoot: string): Promise<PluginAcpAgentLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_acp_agent_prepare", { payload: target(pluginId, agentId, projectRoot) });
  return parseLaunch(raw, "prepare");
}

/** Stores the user's confirmation of exactly `fingerprint` (from prepare). Only called after the user confirmed. */
export async function trustAcpAgent(
  pluginId: string,
  agentId: string,
  projectRoot: string,
  fingerprint: string,
): Promise<PluginAcpAgentLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_acp_agent_trust", {
    payload: { ...target(pluginId, agentId, projectRoot), fingerprint },
  });
  return parseLaunch(raw, "trust");
}

/**
 * Starts a confirmed agent for an ACP connection in cwd (the conversation's
 * project root). Never prompts: an unconfirmed or changed agent fails with
 * `untrusted:`, a gone one with `unavailable:`.
 */
export async function launchPluginAcpAgent({
  serverId,
  pluginId,
  agentId,
  cwd,
}: {
  serverId: string;
  pluginId: string;
  agentId: string;
  cwd: string;
}): Promise<AcpLaunchResult> {
  requireDesktop();
  const raw = await invokeBackend<AcpLaunchResult | null>("acp_launch_plugin", {
    serverId,
    pluginId: pluginId.trim(),
    agentId: agentId.trim(),
    cwd,
  });
  return raw ?? {};
}
