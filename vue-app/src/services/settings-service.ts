import {
  normalizeMcpServerForRuntime,
  type StoredMcpServer,
} from "@/domain/ai/mcp-servers";
import type { AiAnthropicAuth } from "@/domain/ai/anthropic-auth";
import { chooseNativeDirectory, chooseNativeFile, invokeBackend, listenAppEvent } from "@/services/desktop-bridge";

export interface AiModelFetchConfig {
  baseUrl: string;
  apiKey: string;
  apiFormat: "openai" | "openai-responses" | "anthropic";
  /** Anthropic-format credential header; omitted means x-api-key. */
  anthropicAuth?: AiAnthropicAuth;
}

export interface AiConnectionTestConfig extends AiModelFetchConfig {
  model: string;
}

export interface ProtocolStatus {
  id: string;
  state: string;
  toolCount?: number;
  resourceCount?: number;
  promptCount?: number;
  error?: string | null;
}

export interface UpdateInfo {
  available: boolean;
  version: string;
  currentVersion: string;
  notes: string;
  pubDate?: string;
  platform: string;
  downloadSize?: number;
  releaseURL?: string;
}

export type UpdateInstallResult =
  | { version: string; ready: true }
  | { version: string; ready: false; releaseURL: string };

interface UpdateProgress {
  kind: string;
  done?: number;
  total?: number;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function optionalNonNegativeNumber(value: unknown, field: string) {
  if (value === undefined || value === null) return undefined;
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    throw new Error(`Invalid update response field: ${field}`);
  }
  return value;
}

function parseUpdateInfo(value: unknown): UpdateInfo {
  if (!isRecord(value)) throw new Error("Invalid update response");
  if (typeof value.available !== "boolean") throw new Error("Invalid update response field: available");
  const version = value.version;
  const currentVersion = value.currentVersion;
  const notes = value.notes;
  const pubDate = value.pubDate;
  const platform = value.platform;
  for (const [field, fieldValue] of Object.entries({ version, currentVersion, notes, platform })) {
    if (typeof fieldValue !== "string") throw new Error(`Invalid update response field: ${field}`);
  }
  if (pubDate !== undefined && typeof pubDate !== "string") {
    throw new Error("Invalid update response field: pubDate");
  }
  if (value.releaseURL !== undefined && typeof value.releaseURL !== "string") {
    throw new Error("Invalid update response field: releaseURL");
  }
  return {
    available: value.available,
    version: version as string,
    currentVersion: currentVersion as string,
    notes: notes as string,
    pubDate,
    platform: platform as string,
    downloadSize: optionalNonNegativeNumber(value.downloadSize, "downloadSize"),
    releaseURL: value.releaseURL,
  };
}

function parseUpdateInstallResult(value: unknown): UpdateInstallResult {
  if (!isRecord(value)) throw new Error("Invalid update install response");
  if (typeof value.version === "string") {
    if (value.ready === true) return { version: value.version, ready: true };
    if (value.ready === false && typeof value.releaseURL === "string" && value.releaseURL) {
      return { version: value.version, ready: false, releaseURL: value.releaseURL };
    }
  }
  throw new Error("Invalid update install response fields");
}

export function setMinimizeToTray(value: boolean) { return invokeBackend("set_minimize_to_tray", { value }); }
export function setAutostart(value: boolean) { return invokeBackend("set_autostart", { value }); }
export function getAppVersion() { return invokeBackend<string>("get_app_version", {}); }
export function getRuntimePlatform() {
  return invokeBackend<{ os: string; arch: string }>("get_runtime_platform", {});
}
export function getAutostartEnabled() { return invokeBackend<boolean>("get_autostart_enabled", {}); }
export function loadProtocolStatuses() {
  return Promise.all([
    invokeBackend<ProtocolStatus[]>("mcp_list_status", {}).catch(() => []),
    invokeBackend<ProtocolStatus[]>("acp_list_status", {}).catch(() => []),
  ]);
}
export function readSettingsTextFile(path: string) { return invokeBackend<string>("local_fs_read_selected_text_file", { path }); }
export function getUserHomeDirectory() { return invokeBackend<string>("get_user_home_dir", {}); }
export function chooseSettingsJsonFile(title: string) { return chooseNativeFile(title, [{ DisplayName: "JSON", Pattern: "*.json" }]); }
export function chooseSettingsDirectory(title: string) { return chooseNativeDirectory(title); }
export function connectMcpServer(server: StoredMcpServer) {
  const runtimeServer = normalizeMcpServerForRuntime(server);
  return invokeBackend("mcp_connect", {
    serverId: runtimeServer.id,
    transport: runtimeServer.transport,
    command: runtimeServer.command,
    args: runtimeServer.args.trim() ? runtimeServer.args.trim().split(/\s+/) : [],
    env: runtimeServer.env,
    url: runtimeServer.url,
    headers: runtimeServer.headers,
  });
}
export function disconnectMcpServer(serverId: string) { return invokeBackend("mcp_disconnect", { serverId }); }
export function fetchAiModels(config: AiModelFetchConfig) { return invokeBackend<string[]>("ai_list_models", { config }); }
export function testAiConnection(config: AiConnectionTestConfig) { return invokeBackend<boolean>("ai_test_connection", { config }); }
export async function checkForUpdates() {
  return parseUpdateInfo(await invokeBackend<unknown>("check_update", {}));
}

export async function downloadAndStageUpdate(version: string) {
  return parseUpdateInstallResult(await invokeBackend<unknown>("download_and_install", { version }));
}

export function restartForUpdate() {
  return invokeBackend("restart_app");
}

export function listenUpdateProgress(handler: (event: UpdateProgress) => void) {
  return listenAppEvent<UpdateProgress>("app-update:progress", handler);
}
