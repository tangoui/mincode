/**
 * Plugin MCP servers (contributes.mcpServers): the IPC boundary.
 *
 * The WebView names a server only by `(pluginId, serverId, projectRoot)`, with
 * an empty projectRoot for a server that does not need a project. The
 * Go host looks the entry up in its own snapshot, re-validates and resolves
 * it (backend/plugin/mcp_servers_launch.go) and refuses to start it
 * until the user confirmed exactly that fingerprint. One confirmation covers
 * every project (`${projectRoot}` stays deferred), and it is required in
 * every approval mode. AI tools have no path to these commands, and the
 * gateway refuses `plugin:` ids from mcp_connect*.
 */
import { parsePluginMcpServerId, withMcpStepTimeout } from "@/domain/plugins/mcp-servers";
import { confirmPluginLaunch, type ResolvedProcessLaunch } from "@/services/plugin-launch-confirm";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export interface PluginMcpServerLaunch extends ResolvedProcessLaunch {
  serverId: string;
  sessionServerId: string;
  description?: string;
  descriptionLocalized?: Record<string, string>;
  projectRoot: string;
}

export interface PluginMcpTool {
  name: string;
  description?: string;
  inputSchema?: unknown;
}

export interface PluginMcpServerStartResult {
  sessionServerId: string;
  reused: boolean;
  tools: PluginMcpTool[];
}

const FINGERPRINT_PATTERN = /^[0-9a-f]{64}$/;

function requireDesktop() {
  if (!isDesktopRuntime()) throw new Error("unavailable: plugin MCP servers need the MinCode desktop app");
}

// The host decides whether the server needs projectRoot (mcpServerLaunchRoot
// picks it): it refuses an empty one for a project server and any for the rest.
function target(sessionId: string, projectRoot: string) {
  const parsed = parsePluginMcpServerId(sessionId);
  if (!parsed) throw new Error(`forbidden: "${sessionId}" is not a plugin MCP server`);
  return { pluginId: parsed.pluginId, serverId: parsed.serverId, projectRoot: projectRoot.trim() };
}

function parseLaunch(raw: unknown, operation: string): PluginMcpServerLaunch {
  const record = typeof raw === "object" && raw !== null && !Array.isArray(raw) ? (raw as Record<string, unknown>) : undefined;
  const valid =
    record &&
    typeof record.pluginId === "string" &&
    typeof record.serverId === "string" &&
    typeof record.sessionServerId === "string" &&
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
    ...(record as unknown as PluginMcpServerLaunch),
    env,
    trusted: record.trusted === true,
    previouslyTrusted: record.previouslyTrusted === true,
  };
}

function parseStart(raw: unknown, sessionId: string): PluginMcpServerStartResult {
  const record = typeof raw === "object" && raw !== null && !Array.isArray(raw) ? (raw as Record<string, unknown>) : {};
  if (record.sessionServerId !== sessionId) throw new Error("unavailable: start returned another MCP session");
  const tools = Array.isArray(record.tools)
    ? record.tools.filter((tool): tool is PluginMcpTool =>
        typeof tool === "object" && tool !== null && typeof (tool as { name?: unknown }).name === "string")
    : [];
  return { sessionServerId: sessionId, reused: record.reused === true, tools };
}

/** Resolves the launch on the host (nothing is started). */
export async function preparePluginMcpServer(sessionId: string, projectRoot: string): Promise<PluginMcpServerLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_mcp_server_prepare", { payload: target(sessionId, projectRoot) });
  return parseLaunch(raw, "prepare");
}

/** Stores the user's confirmation of exactly `fingerprint`. */
export async function trustPluginMcpServer(sessionId: string, projectRoot: string, fingerprint: string): Promise<PluginMcpServerLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_mcp_server_trust", {
    payload: { ...target(sessionId, projectRoot), fingerprint },
  });
  return parseLaunch(raw, "trust");
}

/**
 * Prepares a server and, when the host has no matching confirmation, shows
 * the resolved launch and asks the user. Resolves to a trusted launch or
 * undefined (cancelled). Only called from a user action.
 */
export async function ensurePluginMcpServerConfirmed(
  sessionId: string,
  projectRoot: string,
): Promise<PluginMcpServerLaunch | undefined> {
  const launch = await withMcpStepTimeout(preparePluginMcpServer(sessionId, projectRoot), "prepare");
  if (launch.trusted) return launch;
  if (!(await confirmPluginLaunch(launch, { changed: launch.previouslyTrusted }))) return undefined;
  return withMcpStepTimeout(trustPluginMcpServer(sessionId, projectRoot, launch.fingerprint), "trust");
}

/**
 * Starts a confirmed server in projectRoot, or reuses its live session, and
 * returns its tools. Never prompts: an unconfirmed or changed launch fails
 * with `untrusted:` / `changed:` so the caller can point to the settings.
 */
export async function startPluginMcpServer(sessionId: string, projectRoot: string): Promise<PluginMcpServerStartResult> {
  const launch = await preparePluginMcpServer(sessionId, projectRoot);
  if (!launch.trusted) {
    throw new Error(launch.previouslyTrusted ? "changed: the MCP server changed since you confirmed it" : "untrusted: the MCP server is not confirmed");
  }
  const raw = await invokeBackend<unknown>("plugin_host_mcp_server_start", {
    payload: { ...target(sessionId, projectRoot), fingerprint: launch.fingerprint },
  });
  return parseStart(raw, sessionId);
}

/** Stops the session (any project) and clears the host restart counts. */
export async function stopPluginMcpServer(sessionId: string): Promise<boolean> {
  requireDesktop();
  const parsed = parsePluginMcpServerId(sessionId);
  if (!parsed) throw new Error(`forbidden: "${sessionId}" is not a plugin MCP server`);
  const raw = await invokeBackend<{ stopped?: unknown }>("plugin_host_mcp_server_stop", {
    payload: { pluginId: parsed.pluginId, serverId: parsed.serverId },
  });
  return raw?.stopped === true;
}
