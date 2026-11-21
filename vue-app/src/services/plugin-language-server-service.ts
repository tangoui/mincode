/**
 * Plugin language servers (contributes.languageServers): the IPC boundary.
 *
 * The WebView names a server only by `(pluginId, serverId, projectRoot)`. The
 * Go host looks the entry up in its own snapshot, re-validates and resolves
 * it (backend/plugin/language_servers_launch.go) and refuses to
 * start it until the user confirmed exactly that fingerprint. One
 * confirmation covers every project (`${projectRoot}` stays deferred), and it
 * is required in every approval mode. AI tools have no path to these
 * commands (services/ai-tools/workspace-tools.ts rejects `plugin:` ids).
 */
import { parsePluginLspServerId } from "@/domain/projects/lsp";
import type { LspStartResult } from "@/services/lsp-service";
import { confirmPluginLaunch, type ResolvedProcessLaunch } from "@/services/plugin-launch-confirm";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export interface PluginLanguageServerLaunch extends ResolvedProcessLaunch {
  serverId: string;
  sessionServerId: string;
  languages: string[];
  initializationOptions?: unknown;
  projectRoot: string;
}

const FINGERPRINT_PATTERN = /^[0-9a-f]{64}$/;

function requireDesktop() {
  if (!isDesktopRuntime()) throw new Error("unavailable: plugin language servers need the MinCode desktop app");
}

function target(sessionId: string, projectRoot: string) {
  const parsed = parsePluginLspServerId(sessionId);
  if (!parsed) throw new Error(`forbidden: "${sessionId}" is not a plugin language server`);
  return { pluginId: parsed.pluginId, serverId: parsed.serverId, projectRoot };
}

function parseLaunch(raw: unknown, operation: string): PluginLanguageServerLaunch {
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
    Array.isArray(record.languages) &&
    Array.isArray(record.argv) &&
    record.argv.length > 0 &&
    record.argv.every((arg) => typeof arg === "string");
  if (!valid) throw new Error(`unavailable: ${operation} returned an invalid launch spec`);
  const env = typeof record.env === "object" && record.env !== null ? (record.env as Record<string, string>) : {};
  return {
    ...(record as unknown as PluginLanguageServerLaunch),
    env,
    trusted: record.trusted === true,
    previouslyTrusted: record.previouslyTrusted === true,
  };
}

/** Resolves the launch on the host (nothing is started). */
export async function preparePluginLanguageServer(sessionId: string, projectRoot: string): Promise<PluginLanguageServerLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_language_server_prepare", { payload: target(sessionId, projectRoot) });
  return parseLaunch(raw, "prepare");
}

/** Stores the user's confirmation of exactly `fingerprint`. */
export async function trustPluginLanguageServer(sessionId: string, projectRoot: string, fingerprint: string): Promise<PluginLanguageServerLaunch> {
  requireDesktop();
  const raw = await invokeBackend<unknown>("plugin_host_language_server_trust", {
    payload: { ...target(sessionId, projectRoot), fingerprint },
  });
  return parseLaunch(raw, "trust");
}

/**
 * Prepares a server and, when the host has no matching confirmation, shows
 * the resolved launch and asks the user. Resolves to a trusted launch or
 * undefined (cancelled). Only called from a user action in the dialog.
 */
export async function ensurePluginLanguageServerConfirmed(
  sessionId: string,
  projectRoot: string,
): Promise<PluginLanguageServerLaunch | undefined> {
  const launch = await preparePluginLanguageServer(sessionId, projectRoot);
  if (launch.trusted) return launch;
  if (!(await confirmPluginLaunch(launch, { changed: launch.previouslyTrusted }))) return undefined;
  return trustPluginLanguageServer(sessionId, projectRoot, launch.fingerprint);
}

/**
 * Starts a confirmed server, or returns its live session. Never prompts: an
 * unconfirmed or changed launch fails with `untrusted:` / `changed:` and the
 * dialog offers "Review and start".
 */
export async function startPluginLanguageServer(sessionId: string, projectRoot: string): Promise<LspStartResult> {
  const launch = await preparePluginLanguageServer(sessionId, projectRoot);
  if (!launch.trusted) {
    throw new Error(launch.previouslyTrusted ? "changed: the language server changed since you confirmed it" : "untrusted: the language server is not confirmed");
  }
  return invokeBackend<LspStartResult>("plugin_host_language_server_start", {
    payload: { ...target(sessionId, projectRoot), fingerprint: launch.fingerprint },
  });
}

/** The user's Restart: stops the session and clears the host restart count. */
export async function resetPluginLanguageServer(sessionId: string, projectRoot: string): Promise<boolean> {
  requireDesktop();
  const raw = await invokeBackend<{ stopped?: unknown }>("plugin_host_language_server_reset", { payload: target(sessionId, projectRoot) });
  return raw?.stopped === true;
}
