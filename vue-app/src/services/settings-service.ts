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
}

export interface UpdateInstallResult {
  version: string;
  ready: true;
}

interface UpdateProgress {
  kind: string;
  done?: number;
  total?: number;
}

interface SettingsDirectoryEntry {
  name: string;
  path: string;
  isDir: boolean;
}

const MAX_SKILL_SCAN_DIRECTORIES = 10_000;
const MAX_DISCOVERED_SKILLS = 1_000;

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
  return {
    available: value.available,
    version: version as string,
    currentVersion: currentVersion as string,
    notes: notes as string,
    pubDate,
    platform: platform as string,
    downloadSize: optionalNonNegativeNumber(value.downloadSize, "downloadSize"),
  };
}

function parseUpdateInstallResult(value: unknown): UpdateInstallResult {
  if (!isRecord(value)) throw new Error("Invalid update install response");
  if (typeof value.version !== "string" || value.ready !== true) {
    throw new Error("Invalid update install response fields");
  }
  return { version: value.version, ready: true };
}

export function setMinimizeToTray(value: boolean) { return invokeBackend("set_minimize_to_tray", { value }); }
export function setAutostart(value: boolean) { return invokeBackend("set_autostart", { value }); }
export function getAppVersion() { return invokeBackend<string>("get_app_version", {}); }
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
export function chooseSettingsMarkdownFile(title: string) { return chooseNativeFile(title, [{ DisplayName: "Markdown", Pattern: "*.md" }]); }
export function chooseSettingsDirectory(title: string) { return chooseNativeDirectory(title); }
export async function findSettingsSkillFiles(root: string) {
  const directories = [""];
  const skillPaths: string[] = [];
  const normalizedRoot = root.replace(/[\\/]+$/, "") || root;

  for (let index = 0; index < directories.length; index += 1) {
    if (directories.length > MAX_SKILL_SCAN_DIRECTORIES) throw new Error("skill_scan_too_large");
    const dirPath = directories[index]!;
    const entries = await invokeBackend<SettingsDirectoryEntry[]>("local_fs_list_dir", {
      projectRoot: root,
      dirPath: dirPath || null,
    });
    for (const entry of entries) {
      if (entry.isDir) {
        directories.push(entry.path);
      } else if (entry.name.toLowerCase() === "skill.md") {
        skillPaths.push(`${normalizedRoot}/${entry.path}`);
        if (skillPaths.length > MAX_DISCOVERED_SKILLS) throw new Error("too_many_skills");
      }
    }
  }

  return skillPaths.sort((left, right) => left.localeCompare(right));
}
export function connectMcpServer(server: StoredMcpServer) {
  const runtimeServer = normalizeMcpServerForRuntime(server);
  return invokeBackend("mcp_connect", { serverId: runtimeServer.id, transport: runtimeServer.transport, command: runtimeServer.command,
    args: runtimeServer.args.trim() ? runtimeServer.args.trim().split(/\s+/) : [], env: runtimeServer.env, url: runtimeServer.url, headers: runtimeServer.headers,
    // "oauth" makes the backend send the host-held sign-in token (mcpauth).
    auth: runtimeServer.auth });
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
