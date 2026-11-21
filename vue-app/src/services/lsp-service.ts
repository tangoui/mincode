import { invokeBackend, isDesktopRuntime, listenAppEvent } from "@/services/desktop-bridge";
import { lspRootKey } from "@/services/lsp-server-holds";
import type { LspServerInfo } from "@/domain/projects/lsp";

export type { LspServerInfo };

export interface LspStartResult {
  id: string;
  name: string;
  languages: string[];
  installed: boolean;
  executable?: string;
  heavy: boolean;
  auxiliary?: boolean;
  installHint: string;
  platform: string;
  running: boolean;
  generation?: number;
  error?: string;
}

export interface LspEvent {
  root: string;
  serverId: string;
  generation: number;
  method: string;
  params: unknown;
  requestId?: number;
}

export async function listLspServers(): Promise<LspServerInfo[]> {
  if (!isDesktopRuntime()) return [];
  return invokeBackend<LspServerInfo[]>("project_lsp_list_servers");
}

export async function startLspServer(root: string, serverId: string): Promise<LspStartResult> {
  return invokeBackend<LspStartResult>("project_lsp_start", { root, serverId });
}

export async function stopLspServer(root: string, serverId: string): Promise<void> {
  return invokeBackend<void>("project_lsp_stop", { root, serverId });
}

export async function sendLspRequest(
  root: string,
  serverId: string,
  generation: number,
  method: string,
  params: unknown,
  timeoutMs?: number,
  signal?: AbortSignal
): Promise<unknown> {
  const promise = invokeBackend("project_lsp_request", {
    root,
    serverId,
    generation,
    method,
    params,
    timeoutMs: timeoutMs ?? 0,
  });
  if (!signal) return promise;
  return Promise.race([
    promise,
    new Promise<never>((_, reject) => {
      if (signal.aborted) { reject(new DOMException("Aborted", "AbortError")); return; }
      signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
    }),
  ]);
}

export async function sendLspNotify(
  root: string,
  serverId: string,
  generation: number,
  method: string,
  params: unknown
): Promise<void> {
  return invokeBackend<void>("project_lsp_notify", {
    root,
    serverId,
    generation,
    method,
    params,
  });
}

export interface LspInstallResult {
  output: string;
  error?: string;
}

// Installed tools are machine-wide; listeners restart only their own active
// project's sessions. This is transient and never persisted as workspace data.
const installedListeners = new Set<(serverId: string) => void | Promise<void>>();

export function onLspServerInstalled(listener: (serverId: string) => void | Promise<void>): () => void {
  installedListeners.add(listener);
  return () => { installedListeners.delete(listener); };
}

export async function installLspServer(serverId: string): Promise<LspInstallResult> {
  const result = await invokeBackend<LspInstallResult>("project_lsp_install", { serverId });
  if (!result.error) {
    // Runtime handshake failures remain in the runtime state and logs; they
    // must not turn a successful package installation into an npm failure.
    await Promise.allSettled([...installedListeners].map(async (listener) => listener(serverId)));
  }
  return result;
}

export interface LspLogsResult {
  output: string;
  running: boolean;
}

export async function getLspServerLogs(root: string, serverId: string): Promise<LspLogsResult> {
  return invokeBackend<LspLogsResult>("project_lsp_logs", { root, serverId });
}

export async function replyLspRequest(
  root: string,
  serverId: string,
  generation: number,
  requestId: number,
  result: unknown,
): Promise<void> {
  return invokeBackend<void>("project_lsp_reply", { root, serverId, generation, requestId, result: result ?? null });
}

export function onLspEvent(handler: (event: LspEvent) => void): () => void {
  return listenAppEvent<LspEvent>("project-lsp:event", handler);
}

/** Payload of `project-lsp:restart-required` (backend/lsp/jdtls.go). */
export interface LspRestartRequiredEvent {
  serverId: string;
  reason: string;
}

/** A running built-in server must restart to apply a change (e.g. the confirmed jdtls bundles). */
export function onLspRestartRequired(handler: (event: LspRestartRequiredEvent) => void): () => void {
  return listenAppEvent<unknown>("project-lsp:restart-required", (payload) => {
    if (typeof payload !== "object" || payload === null) return;
    const { serverId, reason } = payload as Record<string, unknown>;
    if (typeof serverId !== "string" || !serverId) return;
    handler({ serverId, reason: typeof reason === "string" ? reason : "" });
  });
}

/**
 * Whether a built-in server serves a language in a project: "plugin" when the
 * language is assigned to a plugin language server, "disabled" when the
 * server is not enabled for the project, "unavailable" when the project's
 * language servers cannot run (inactive workspace, runtime not loadable).
 */
export type BuiltinServerAvailability = "available" | "plugin" | "disabled" | "unavailable";

/**
 * An open project's language servers as other features may drive them
 * (registered by useProjectLanguageServers, so callers need neither Monaco nor
 * the LSP runtime chunk until they use it).
 */
export interface ProjectLspHost {
  root(): string;
  active(): boolean;
  builtinServerAvailability(serverId: string, language: string): Promise<BuiltinServerAvailability>;
  /** Enables the built-in server in the project's language server settings; false when this view cannot. */
  enableBuiltinServer(serverId: string): boolean;
  /** Restarts the built-in server, or starts it when it is not running; resolves whether it runs afterwards. */
  restartBuiltinServer(serverId: string): Promise<boolean>;
}

// Live project views only (transient, never persisted).
const projectLspHosts = new Set<ProjectLspHost>();

export function registerProjectLspHost(host: ProjectLspHost): () => void {
  projectLspHosts.add(host);
  return () => { projectLspHosts.delete(host); };
}

/** The project view of root, preferring the active one. */
export function projectLspHostFor(root: string): ProjectLspHost | undefined {
  const key = lspRootKey(root);
  const matching = [...projectLspHosts].filter((host) => lspRootKey(host.root()) === key);
  return matching.find((host) => host.active()) ?? matching[0];
}

/** Read source/decompiled text for a JDT class-file URI from the built-in jdtls. */
export async function readJdtClassFileContents(root: string, uri: string): Promise<string> {
  if (!isDesktopRuntime()) throw new Error("unavailable: Java source is available in the desktop app only");
  return invokeBackend<string>("project_lsp_jdt_contents", { root, uri });
}
