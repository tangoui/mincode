import { isManifestCategory, isPluginCategory, manifestCategories, pluginHomepage, type ManifestCategory, type PluginCategory } from "@/domain/plugins/catalog";
import { parsePluginInstallPreview } from "@/domain/plugins/install-preview";
import { normalizeDevelopmentContributions, type DevelopmentContributionSummary } from "@/domain/plugins/development-summary";
import { parseContributionsSnapshot, type PluginContributionsSnapshot } from "@/domain/plugins/contributions";
import { parseCommandInputForm } from "@/domain/commands/input-schema";
import { WHEN_EXPRESSION_MAX_LENGTH, type PluginContextValue, type PluginContextValues } from "@/domain/commands/when";
import { parsePluginUITaskRequest, parsePluginUITaskSnapshot, utf8ByteLengthUpTo, PLUGIN_UI_MAX_MESSAGE_BYTES, type PluginUIFloatingTarget } from "@/domain/plugin-ui/protocol";
import { setPluginCommandTitles, setPluginViewTitles } from "@/services/plugin-titles";
import { invokeBackend } from "@/services/desktop-bridge";
import { parseFileDrop } from "@/domain/plugin-ui/file-drop.mjs";

export type PluginSource = "builtin" | "local" | "development";
/** `declarative` plugins contribute data only (keymaps) and never start a process. */
export type PluginRuntime = "go" | "process" | "declarative";
export type PluginState = "disabled" | "stopped" | "starting" | "running" | "stopping" | "error";
export type PluginRisk = "read" | "write" | "dangerous";
export type PluginScope = "global" | "workspace";

export interface PluginMeta {
  category?: PluginCategory;
  tags?: string[];
  homepage?: string;
  icon?: string;
  cover?: string;
  id: string;
  name: string;
  version: string;
  description: string;
  author?: string;
  enabled: boolean;
  scope: PluginScope;
  workspaces: string[];
  activeInWorkspace: boolean;
  source: PluginSource;
  runtime: PluginRuntime;
  state: PluginState;
  lastError?: string;
  capabilities: string[];
  apiVersion: number;
  /** Recorded from the marketplace at install; enabling needs a paid MinCode host plan. */
  requiresPaidHost?: boolean;
  /** Full trust holds right now (Go re-checks it on every confirmation). */
  fullTrust?: boolean;
}

export interface PluginCommandTarget {
  type: "view";
  viewId: string;
}

export interface PluginCommand {
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  description?: string;
  keywords?: string[];
  action?: string;
  target?: PluginCommandTarget;
  payload?: string;
  risk: PluginRisk;
  /**
   * The higher of `risk` and the risk of the action the command runs (Go
   * contributionCommandEffectiveRisk); "network" comes from the action.
   * Missing or unknown values read as "dangerous".
   */
  effectiveRisk: PluginRisk | "network";
  when?: string;
  inputSchema?: Record<string, unknown>;
}

export type PluginContextMap = Record<string, PluginContextValues>;

export interface PluginView {
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  icon?: string;
  enabled: boolean;
  activeInWorkspace: boolean;
  state: PluginState;
  /** views[].floating: shown only in a host floating window, never as a tab. */
  floating?: PluginFloatingSize;
}

export interface PluginFloatingSize {
  width: number;
  height: number;
}

export interface PluginUIAction {
  id: string;
  risk: PluginRisk;
  maxPayloadBytes: number;
  resultHandling?: "save-text-file" | "open-url";
}

export interface PluginUISession {
  mode: "asset" | "development";
  sessionId: string;
  pluginId: string;
  viewId: string;
  url: string;
  nonce: string;
  generation: number;
  requestTimeoutMs: number;
  actions: PluginUIAction[];
  /** views[].fileDrop as validated by the host; absent when the view accepts no drops. */
  fileDrop?: unknown;
  /** The plugin uses runtime.transport "framed", so the view may exchange binary frames with it. */
  binary: boolean;
  /**
   * views[].prefetch: a read-only, confirmation-free UI action the host invokes
   * through this session while the frame loads; `payload` is the exact JSON
   * string to forward. Absent when undeclared or when it fails revalidation.
   */
  prefetch?: { action: string; payload: string };
  /** The plugin's declared capabilities; the frame offers capability-gated page features (ai.config) from it. */
  capabilities: readonly string[];
}

export interface PluginLogEntry {
  time: string;
  level: string;
  message: string;
}

export type PluginDevelopmentRunnerStatus = "starting" | "running" | "stopping" | "stopped" | "error";

export interface PluginDevelopmentRunnerState {
  projectRoot: string;
  status: PluginDevelopmentRunnerStatus;
  error: string;
}

export interface PluginDevelopmentProject {
  path: string;
  manifestPath: string;
  packageRoot: string;
  pluginId: string;
  pluginName: string;
  description: string;
  version: string;
  kind: "source" | "package" | "unknown";
  status: "discovered" | "error";
  error: string;
}

// Matches pluginDevScanMaxItems in backend/plugin/development_workspace.go.
export const PLUGIN_DEVELOPMENT_SCAN_MAX_PROJECTS = 500;

export async function scanPluginDevelopmentProjects(root: string): Promise<PluginDevelopmentProject[]> {
  const response = await invokeBackend<unknown>("plugin_host_development_scan", { root });
  if (!Array.isArray(response) || response.length > PLUGIN_DEVELOPMENT_SCAN_MAX_PROJECTS) throw new Error("Invalid plugin project scan response");
  return response.map((value) => {
    const raw = objectValue(value);
    if (!raw || !text(raw.path) || !text(raw.manifestPath)
      || !["source", "package", "unknown"].includes(text(raw.kind))
      || !["discovered", "error"].includes(text(raw.status))) {
      throw new Error("Invalid plugin project descriptor");
    }
    return {
      path: text(raw.path), manifestPath: text(raw.manifestPath), packageRoot: text(raw.packageRoot),
      pluginId: text(raw.pluginId), pluginName: text(raw.pluginName), description: text(raw.description), version: text(raw.version),
      kind: raw.kind as PluginDevelopmentProject["kind"],
      status: raw.status as PluginDevelopmentProject["status"], error: text(raw.error),
    };
  });
}

function normalizeDevelopmentRunner(value: unknown): PluginDevelopmentRunnerState | undefined {
  const raw = objectValue(value);
  const projectRoot = text(raw?.projectRoot).trim();
  const status = text(raw?.status) as PluginDevelopmentRunnerStatus;
  if (!projectRoot || !["starting", "running", "stopping", "stopped", "error"].includes(status)) return undefined;
  return { projectRoot, status, error: text(raw?.error) };
}

export async function listPluginDevelopmentRunners(): Promise<PluginDevelopmentRunnerState[]> {
  const response = await invokeBackend<unknown>("plugin_host_development_runner_list", {});
  if (!Array.isArray(response) || response.length > 200) throw new Error("Invalid plugin development runner response");
  return response.flatMap((value) => normalizeDevelopmentRunner(value) ?? []);
}

export async function startPluginDevelopmentProject(projectRoot: string) {
  const state = normalizeDevelopmentRunner(await invokeBackend<unknown>("plugin_host_development_runner_start", { projectRoot }));
  if (!state) throw new Error("Invalid plugin development runner state");
  return state;
}

export function stopPluginDevelopmentProject(projectRoot: string) {
  return invokeBackend("plugin_host_development_runner_stop", { projectRoot });
}

export interface PluginDevelopmentManifestSummary {
  apiVersion: number;
  runtimeType: string;
  runtimeEntry: string;
  activation: string;
  platforms: string[];
  commandCount: number;
  viewCount: number;
  actionCount: number;
  /** Declarative plugins: number of contributes.keymaps entries. */
  keymapCount: number;
  /** Declarative plugins: skipped presets (reported, never fatal in development). */
  keymapIssues: string[];
  /** Every contribution point: valid entry count and skipped-entry reasons. */
  contributions: Record<string, DevelopmentContributionSummary>;
}

export interface PluginDevelopmentPairing {
  id: string;
  cliInstanceId: string;
  projectRoot: string;
  packageRoot: string;
  uiUrl?: string;
  pluginId: string;
  pluginName: string;
  version: string;
  capabilities: string[];
  manifest: PluginDevelopmentManifestSummary;
  protocolVersion: number;
  createdAt: string;
  expiresAt: string;
  status: "pending";
}

export interface PluginDevelopmentLog {
  level: "debug" | "info" | "warn" | "error";
  message: string;
  createdAt: string;
}

export interface PluginDevelopmentSession {
  id: string;
  pluginId: string;
  pluginName: string;
  projectRoot: string;
  packageRoot: string;
  uiUrl?: string;
  cliInstanceId: string;
  connectedAt: string;
  lastSeenAt: string;
  expiresAt: string;
  status: "connected";
  generation: number;
  protocolVersion: number;
  capabilities: string[];
  manifest: PluginDevelopmentManifestSummary;
  buildError?: string;
  logs: PluginDevelopmentLog[];
}

// Only accept bounded raster data supplied by the native plugin host.
export function normalizePluginCover(value: unknown): string | undefined {
  return typeof value === "string" && value.length <= 700000
    && /^data:image\/(png|jpeg|gif);base64,[A-Za-z0-9+/]+={0,2}$/.test(value)
    ? value : undefined;
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function text(value: unknown, fallback = "") {
  return typeof value === "string" ? value : fallback;
}

function localizedTitles(value: unknown) {
  const raw = objectValue(value);
  if (!raw) return undefined;
  const result: Record<string, string> = {};
  for (const [locale, title] of Object.entries(raw)) {
    if (typeof title === "string" && title.trim()) result[locale] = title.trim();
  }
  return Object.keys(result).length ? result : undefined;
}

function risk(value: unknown): PluginRisk {
  return value === "write" || value === "dangerous" ? value : "read";
}

const COMMAND_RISK_RANK = { read: 1, network: 2, write: 3, dangerous: 4 } as const;

function commandEffectiveRisk(value: unknown, declared: PluginRisk): PluginRisk | "network" {
  const effective = value === "read" || value === "network" || value === "write" || value === "dangerous" ? value : "dangerous";
  return COMMAND_RISK_RANK[effective] >= COMMAND_RISK_RANK[declared] ? effective : declared;
}

function exactRisk(value: unknown): PluginRisk | undefined {
  return value === "read" || value === "write" || value === "dangerous" ? value : undefined;
}

export function pluginUIParentOrigin() {
  if (window.location.origin && window.location.origin !== "null") return window.location.origin;
  const protocol = window.location.protocol.toLowerCase();
  const host = window.location.host;
  if (protocol === "tango:" && host) return `${protocol}//${host}`;
  throw new Error("MinCode application origin is unavailable");
}

function validatePluginUIURL(value: string, mode: PluginUISession["mode"]) {
  const parsed = new URL(value);
  if (
    parsed.protocol !== "http:"
    || parsed.hostname !== "127.0.0.1"
    || !parsed.port
    || parsed.username
    || parsed.password
    || (mode === "asset" && !parsed.pathname.startsWith("/plugin-ui/"))
  ) {
    throw new Error("Invalid plugin UI URL");
  }
  if (mode === "development") {
    const segments = parsed.pathname.split("/").filter(Boolean);
    if (segments.length !== 1 || !/^[a-f0-9]{48}$/.test(segments[0] ?? "")) {
      throw new Error("Invalid plugin development UI URL");
    }
  }
  return parsed.toString();
}

function pluginState(value: unknown, enabled = false): PluginState {
  const states: PluginState[] = ["disabled", "stopped", "starting", "running", "stopping", "error"];
  return states.includes(value as PluginState) ? value as PluginState : (enabled ? "stopped" : "disabled");
}

function normalizePlugin(value: unknown): PluginMeta | undefined {
  const raw = objectValue(value);
  if (!raw) return undefined;
  const id = text(raw.id).trim();
  const name = text(raw.name).trim() || id;
  if (!id || !name) return undefined;
  const enabled = raw.enabled === true;
  return {
    id,
    name,
    version: text(raw.version, "0.0.0"),
    description: text(raw.description),
    cover: normalizePluginCover(raw.cover),
    icon: text(raw.icon) || undefined,
    author: text(raw.author) || undefined,
    category: isPluginCategory(raw.category) ? raw.category : undefined,
    tags: Array.isArray(raw.tags) ? raw.tags.filter((tag): tag is string => typeof tag === "string") : [],
    homepage: pluginHomepage(raw.homepage),
    enabled,
    scope: raw.scope === "workspace" ? "workspace" : "global",
    workspaces: Array.isArray(raw.workspaces)
      ? raw.workspaces.filter((item): item is string => typeof item === "string" && item.trim() !== "")
      : [],
    activeInWorkspace: raw.activeInWorkspace === true,
    source: raw.source === "local" || raw.source === "development" ? raw.source : "builtin",
    runtime: raw.runtime === "process" || raw.runtime === "declarative" ? raw.runtime : "go",
    state: pluginState(raw.state, enabled),
    lastError: text(raw.lastError) || undefined,
    capabilities: Array.isArray(raw.capabilities)
      ? raw.capabilities.filter((item): item is string => typeof item === "string")
      : [],
    apiVersion: Number.isInteger(raw.apiVersion) ? Number(raw.apiVersion) : 2,
    ...(raw.fullTrust === true ? { fullTrust: true } : {}),
  };
}

export async function listPlugins(): Promise<PluginMeta[]> {
  const raw = await invokeBackend<unknown>("plugin_host_list", {});
  return Array.isArray(raw) ? raw.flatMap((item) => normalizePlugin(item) ?? []) : [];
}

/**
 * Contributions of enabled plugins for the requested points
 * (`plugin_host_contributions`); never starts a plugin process. Entries are
 * untrusted: each point consumer validates them before registering.
 */
export async function fetchPluginContributions(points: readonly string[]): Promise<PluginContributionsSnapshot> {
  return parseContributionsSnapshot(await invokeBackend<unknown>("plugin_host_contributions", { points: [...points] }));
}

let commandTitlesGeneration = 0;
let viewTitlesGeneration = 0;

export async function listPluginCommands(): Promise<PluginCommand[]> {
  const generation = ++commandTitlesGeneration;
  const raw = await invokeBackend<unknown>("plugin_host_commands", {});
  const commands: PluginCommand[] = (Array.isArray(raw) ? raw : []).flatMap((value) => {
    const item = objectValue(value);
    if (!item) return [];
    const pluginId = text(item.pluginId).trim();
    const pluginName = text(item.pluginName).trim();
    const id = text(item.id).trim();
    const title = text(item.title).trim();
    const action = text(item.action).trim() || undefined;
    const targetRaw = objectValue(item.target);
    const target = targetRaw?.type === "view" && text(targetRaw.viewId).trim()
      ? { type: "view" as const, viewId: text(targetRaw.viewId).trim() }
      : undefined;
    if (!pluginId || !pluginName || !id || !title || (!action && !target)) return [];
    const when = text(item.when).trim();
    const inputSchemaRaw = objectValue(item.inputSchema);
    const declaredRisk = risk(item.risk);
    return [{
      pluginId,
      pluginName,
      id,
      title,
      titleLocalized: localizedTitles(item.titleLocalized),
      action,
      target,
      description: text(item.description) || undefined,
      keywords: Array.isArray(item.keywords)
        ? item.keywords.filter((keyword): keyword is string => typeof keyword === "string")
        : undefined,
      payload: text(item.payload) || undefined,
      risk: declaredRisk,
      effectiveRisk: commandEffectiveRisk(item.effectiveRisk, declaredRisk),
      // Only action commands may carry visibility or parameter contracts.
      when: action && when && when.length <= WHEN_EXPRESSION_MAX_LENGTH ? when : undefined,
      inputSchema: action && inputSchemaRaw && parseCommandInputForm(inputSchemaRaw)
        ? inputSchemaRaw
        : undefined,
    }];
  });
  if (generation === commandTitlesGeneration) setPluginCommandTitles(commands);
  return commands;
}

export async function listPluginContexts(): Promise<PluginContextMap> {
  const raw = await invokeBackend<unknown>("plugin_host_contexts", {});
  const result: PluginContextMap = {};
  const root = objectValue(raw);
  if (!root) return result;
  for (const [pluginId, values] of Object.entries(root)) {
    const entry = objectValue(values);
    if (!entry) continue;
    const pairs = Object.entries(entry);
    if (pairs.length > 64) continue;
    const snapshot: PluginContextValues = {};
    for (const [key, value] of pairs) {
      if (value !== null && typeof value !== "string" && typeof value !== "number" && typeof value !== "boolean") {
        continue;
      }
      if (JSON.stringify(value).length > 1024) continue;
      snapshot[key] = value as PluginContextValue;
    }
    if (Object.keys(snapshot).length > 0) result[pluginId] = snapshot;
  }
  return result;
}

/**
 * Views that open as workspace tabs. Floating views (views[].floating) are left
 * out unless `includeFloating` is set; they only render in a floating window.
 */
export async function listPluginViews(options: { includeFloating?: boolean } = {}): Promise<PluginView[]> {
  const generation = ++viewTitlesGeneration;
  const raw = await invokeBackend<unknown>("plugin_host_views", {});
  const views: PluginView[] = (Array.isArray(raw) ? raw : []).flatMap((value) => {
    const item = objectValue(value);
    if (!item) return [];
    const pluginId = text(item.pluginId).trim();
    const pluginName = text(item.pluginName).trim();
    const id = text(item.id).trim();
    const title = text(item.title).trim();
    if (!pluginId || !pluginName || !id || !title) return [];
    return [{
      pluginId,
      pluginName,
      id,
      title,
      titleLocalized: localizedTitles(item.titleLocalized),
      icon: text(item.icon) || undefined,
      enabled: item.enabled === true,
      activeInWorkspace: item.activeInWorkspace === true,
      state: pluginState(item.state, item.enabled === true),
      floating: floatingSize(item.floating),
    }];
  });
  if (generation === viewTitlesGeneration) setPluginViewTitles(views);
  return options.includeFloating ? views : views.filter((view) => !view.floating);
}

function floatingSize(value: unknown): PluginFloatingSize | undefined {
  const item = objectValue(value);
  const width = Number(item?.width);
  const height = Number(item?.height);
  return Number.isSafeInteger(width) && Number.isSafeInteger(height) && width > 0 && height > 0 ? { width, height } : undefined;
}

/** backend event after a floating view window opened or closed. */
export const PLUGIN_FLOATING_CHANGED_EVENT = "plugin:floating-changed";

export interface PluginFloatingChange {
  pluginId: string;
  viewId: string;
  visible: boolean;
}

export function parsePluginFloatingChange(value: unknown): PluginFloatingChange | undefined {
  const item = objectValue(value);
  const pluginId = text(item?.pluginId).trim();
  const viewId = text(item?.viewId).trim();
  if (!pluginId || !viewId || typeof item?.visible !== "boolean") return undefined;
  return { pluginId, viewId, visible: item.visible };
}

/** Opens a floating view in its own window; the host validates the plugin and view. */
export async function showPluginFloatingView(pluginId: string, viewId: string): Promise<boolean> {
  await invokeBackend<unknown>("plugin_host_floating_show", { pluginId, viewId });
  return true;
}

export async function hidePluginFloatingView(pluginId: string, viewId: string): Promise<boolean> {
  await invokeBackend<unknown>("plugin_host_floating_hide", { pluginId, viewId });
  return false;
}

export async function pluginFloatingViewVisible(pluginId: string, viewId: string): Promise<boolean> {
  const raw = objectValue(await invokeBackend<unknown>("plugin_host_floating_status", { pluginId, viewId }));
  return raw?.visible === true;
}

/** Resizes an open floating window within its declared size, keeping its bottom-center in place. */
export async function resizePluginFloatingView(pluginId: string, viewId: string, width: number, height: number): Promise<void> {
  await invokeBackend<unknown>("plugin_host_floating_resize", { pluginId, viewId, width, height });
}

/** Opens target in the main window for a click in a floating view; the host re-validates it. */
export async function activatePluginFloatingView(pluginId: string, viewId: string, target: PluginUIFloatingTarget): Promise<void> {
  await invokeBackend<unknown>("plugin_host_floating_activate", { pluginId, viewId, target });
}

/** The plugin view a floating window (`?mode=plugin-floating`) renders, or undefined for other windows. */
export function pluginFloatingWindowTarget(search: string): { pluginId: string; viewId: string } | undefined {
  const query = new URLSearchParams(search);
  const pluginId = query.get("pluginId")?.trim() ?? "";
  const viewId = query.get("viewId")?.trim() ?? "";
  if (query.get("mode") !== "plugin-floating" || !/^[a-z0-9][a-z0-9._-]{1,127}$/.test(pluginId)
    || !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(viewId)) return undefined;
  return { pluginId, viewId };
}

/** Reopens floating views the user left visible; already open windows are kept. */
export async function restorePluginFloatingViews(): Promise<void> {
  await invokeBackend<unknown>("plugin_host_floating_restore", {});
}

/** Desktop-only permissions of a plugin with the "ai" capability (team-chat bots). */
export interface PluginAiGrants {
  chatApproval: boolean;
  workspaceAccess: boolean;
}

/** Plugins `id` may call without asking (host.plugins.invoke, read-only actions). */
export async function getPluginCallGrants(id: string): Promise<string[]> {
  const value = await invokeBackend<{ targets?: unknown } | null>("plugin_call_grants_get", { id });
  return Array.isArray(value?.targets) ? value.targets.filter((item): item is string => typeof item === "string") : [];
}

/** Revokes one target, or every target when `target` is empty. */
export async function revokePluginCallGrant(id: string, target = ""): Promise<string[]> {
  const value = await invokeBackend<{ targets?: unknown } | null>("plugin_call_grants_revoke", { id, target });
  return Array.isArray(value?.targets) ? value.targets.filter((item): item is string => typeof item === "string") : [];
}

export interface PluginDatabaseSchemaGrant {
  id: string;
  name: string;
  kind?: string;
}

function databaseSchemaGrants(value: { connections?: unknown } | null): PluginDatabaseSchemaGrant[] {
  if (!Array.isArray(value?.connections)) return [];
  return value.connections.flatMap((item) => {
    const raw = item && typeof item === "object" ? (item as Record<string, unknown>) : undefined;
    if (!raw || typeof raw.id !== "string") return [];
    const grant: PluginDatabaseSchemaGrant = { id: raw.id, name: typeof raw.name === "string" && raw.name ? raw.name : raw.id };
    if (typeof raw.kind === "string" && raw.kind) grant.kind = raw.kind;
    return [grant];
  });
}

/** Saved connections `id` may read the schema of without asking (host.database.*). */
export async function getPluginDatabaseSchemaGrants(id: string): Promise<PluginDatabaseSchemaGrant[]> {
  return databaseSchemaGrants(await invokeBackend<{ connections?: unknown } | null>("plugin_database_schema_grants_get", { id }));
}

/** Revokes one connection, or every connection when `connectionId` is empty. */
export async function revokePluginDatabaseSchemaGrant(id: string, connectionId = ""): Promise<PluginDatabaseSchemaGrant[]> {
  return databaseSchemaGrants(await invokeBackend<{ connections?: unknown } | null>("plugin_database_schema_grants_revoke", { id, connectionId }));
}

export async function getPluginAiGrants(id: string): Promise<PluginAiGrants> {
  const value = await invokeBackend<Partial<PluginAiGrants> | null>("plugin_ai_grants_get", { id });
  return { chatApproval: value?.chatApproval === true, workspaceAccess: value?.workspaceAccess === true };
}

export function setPluginAiGrants(id: string, grants: PluginAiGrants) {
  return invokeBackend<PluginAiGrants>("plugin_ai_grants_set", { id, ...grants });
}

export function setPluginEnabled(id: string, enabled: boolean) {
  return invokeBackend("plugin_host_set_enabled", { id, enabled });
}

export function setPluginScope(id: string, scope: PluginScope, workspaces: string[]) {
  return invokeBackend("plugin_host_set_scope", { id, scope, workspaces });
}

export interface PluginWorkspaceScope {
  workspaceId: string;
  root: string;
  revision: string;
  ready: boolean;
}

// Raw scope wire calls; only the lazy scope coordinator validates/adopts them.
export function beginPluginWorkspaceScope(workspaceId: string): Promise<unknown> {
  return invokeBackend<unknown>("plugin_host_workspace_scope_begin", { workspaceId });
}

export function commitPluginWorkspaceScope(workspaceId: string, expectedRevision: string, root: string): Promise<unknown> {
  return invokeBackend<unknown>("plugin_host_workspace_scope_commit", { workspaceId, expectedRevision, root });
}


export type PluginHostEditorEvent = "editor.activeFileChanged" | "editor.fileSaved" | "editor.selectionChanged";

/** 1-based editor selection; the cursor is at the end. */
export interface PluginHostSelection {
  startLine: number;
  startColumn: number;
  endLine: number;
  endColumn: number;
}

/** Publishes an editor event to subscribed process plugins; `path` is absolute or null. */
export function publishPluginHostEvent(event: PluginHostEditorEvent, path: string | null, selection?: PluginHostSelection) {
  return invokeBackend("plugin_host_publish_event", { event, path: path ?? "", ...(selection ? { selection } : {}) });
}

export function invokePlugin(id: string, action: string, payload = "") {
  return invokeBackend<unknown>("plugin_host_invoke", { id, action, payload });
}

export function reloadPlugin(id: string) {
  return invokeBackend("plugin_host_reload", { id });
}

export async function beginPluginUISession(pluginId: string, viewId: string): Promise<PluginUISession> {
  const raw = await invokeBackend<unknown>("plugin_host_ui_begin", {
    pluginId,
    viewId,
    parentOrigin: pluginUIParentOrigin(),
  });
  return parsePluginUISession(raw, pluginId, viewId);
}

/** Validates a `plugin_host_ui_begin`-shaped session (also returned by `plugin_host_ui_begin_document`). */
export function parsePluginUISession(raw: unknown, pluginId: string, viewId: string): PluginUISession {
  const item = objectValue(raw);
  const mode = item?.mode === "development" ? "development" : "asset";
  const sessionId = text(item?.sessionId).trim();
  const rawURL = text(item?.url).trim();
  const nonce = text(item?.nonce).trim();
  const generation = Number(item?.generation);
  const requestTimeoutMs = Number(item?.requestTimeoutMs);
  const responsePluginId = text(item?.pluginId).trim();
  const responseViewId = text(item?.viewId).trim();
  if (
    !item
    || !sessionId
    || !rawURL
    || !nonce
    || responsePluginId !== pluginId
    || responseViewId !== viewId
    || !Number.isSafeInteger(generation)
    || generation < 1
    || !Number.isSafeInteger(requestTimeoutMs)
    || requestTimeoutMs < 1_000
    || requestTimeoutMs > 5 * 60_000
  ) {
    throw new Error("Invalid plugin UI session response");
  }
  const url = validatePluginUIURL(rawURL, mode);
  const seenActions = new Set<string>();
  const actions = Array.isArray(item.actions)
    ? item.actions.flatMap((value) => {
        const action = objectValue(value);
        const id = text(action?.id).trim();
        const actionRisk = exactRisk(action?.risk);
        const maxPayloadBytes = Number(action?.maxPayloadBytes);
        const resultHandling: PluginUIAction["resultHandling"] = action?.resultHandling === "save-text-file"
          || action?.resultHandling === "open-url"
          ? action.resultHandling
          : undefined;
        if (
          !id
          || seenActions.has(id)
          || !actionRisk
          || (action?.resultHandling !== undefined && !resultHandling)
          || (resultHandling === "save-text-file" && actionRisk === "read")
          || !Number.isSafeInteger(maxPayloadBytes)
          || maxPayloadBytes < 1
          || maxPayloadBytes > 256 * 1024
        ) return [];
        seenActions.add(id);
        return [{ id, risk: actionRisk, maxPayloadBytes, resultHandling }];
      })
    : [];
  return {
    mode,
    sessionId,
    pluginId: responsePluginId,
    viewId: responseViewId,
    url,
    nonce,
    generation,
    requestTimeoutMs,
    actions,
    fileDrop: parseFileDrop(item.fileDrop),
    binary: item.binary === true,
    capabilities: parsePluginUISessionCapabilities(item.capabilities),
    ...parsePluginUIPrefetch(item.prefetch, actions),
  };
}

function parsePluginUISessionCapabilities(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  return raw.filter((capability): capability is string => typeof capability === "string" && capability.length > 0 && capability.length <= 64).slice(0, 64);
}

/** Revalidates views[].prefetch against the session's own actions: read risk, no host result handling, payload within limit. */
function parsePluginUIPrefetch(raw: unknown, actions: readonly PluginUIAction[]): { prefetch?: PluginUISession["prefetch"] } {
  const item = objectValue(raw);
  if (!item) return {};
  const actionId = text(item.action).trim();
  const payload = typeof item.payload === "string" ? item.payload : "null";
  const action = actions.find((candidate) => candidate.id === actionId);
  if (!action || action.risk !== "read" || action.resultHandling) return {};
  if (utf8ByteLengthUpTo(payload, action.maxPayloadBytes) > action.maxPayloadBytes) return {};
  try {
    JSON.parse(payload);
  } catch {
    return {};
  }
  return { prefetch: { action: action.id, payload } };
}

export function closePluginUISession(sessionId: string) {
  return invokeBackend("plugin_host_ui_close", { sessionId });
}

export async function startPluginUITask(sessionId: string, nonce: string, action: string, payload = "", timeoutMs = 300_000) {
  if (!sessionId || !nonce || typeof payload !== "string" || utf8ByteLengthUpTo(payload, PLUGIN_UI_MAX_MESSAGE_BYTES) > PLUGIN_UI_MAX_MESSAGE_BYTES || !parsePluginUITaskRequest({ type: "mincode.plugin.task.start", id: "service", action, timeoutMs })) {
    throw new Error("Invalid plugin task request");
  }
  return parsePluginUITaskSnapshot(await invokeBackend<unknown>("plugin_ui_task_start", { sessionId, nonce, action, payload, timeoutMs }));
}

export async function getPluginUITask(sessionId: string, nonce: string, taskId: string) {
  return pluginUITaskOperation("get", sessionId, nonce, taskId);
}

export async function cancelPluginUITask(sessionId: string, nonce: string, taskId: string) {
  return pluginUITaskOperation("cancel", sessionId, nonce, taskId);
}

/**
 * Tells the backend the view frame forwarded a pushed terminal snapshot to the
 * page, so the finished task stops holding one of the plugin's task slots.
 */
export async function acknowledgePluginUITask(sessionId: string, nonce: string, taskId: string) {
  if (!sessionId || !nonce || !parsePluginUITaskRequest({ type: "mincode.plugin.task.get", id: "service", taskId })) {
    throw new Error("Invalid plugin task request");
  }
  await invokeBackend<unknown>("plugin_ui_task_ack", { sessionId, nonce, taskId });
}

async function pluginUITaskOperation(operation: "get" | "cancel", sessionId: string, nonce: string, taskId: string) {
  if (!sessionId || !nonce || !parsePluginUITaskRequest({ type: `mincode.plugin.task.${operation}`, id: "service", taskId })) {
    throw new Error("Invalid plugin task request");
  }
  return parsePluginUITaskSnapshot(await invokeBackend<unknown>(`plugin_ui_task_${operation}`, { sessionId, nonce, taskId }), taskId);
}

export interface PluginUIInvocationErrorData {
  kind?: string;
  tool?: string;
  retryable?: boolean;
}

export class PluginUIInvocationError extends Error {
  code: number;
  data?: PluginUIInvocationErrorData;

  constructor(code: number, message: string, data?: PluginUIInvocationErrorData) {
    super(message);
    this.name = "PluginUIInvocationError";
    this.code = code;
    this.data = data;
  }
}

export async function invokePluginUI(sessionId: string, nonce: string, action: string, payload = "") {
  return unwrapPluginUIResult(await invokeBackend<unknown>("plugin_host_ui_invoke", { sessionId, nonce, action, payload }));
}

/** plugin.call: a method the plugin process registered for its views (no risk confirmation, never AI). */
export async function callPluginUIMethod(sessionId: string, nonce: string, method: string, params: unknown) {
  return unwrapPluginUIResult(await invokeBackend<unknown>("plugin_host_ui_call", { sessionId, nonce, method, params: params ?? null }));
}

/** Forwards one binary frame from a view to its framed-transport plugin process. */
export function sendPluginUIBinary(sessionId: string, nonce: string, channel: string, data: ArrayBuffer) {
  return invokeBackend<unknown>("plugin_host_ui_send_binary", { sessionId, nonce, channel, data: bytesToBase64(new Uint8Array(data)) });
}

/** Forwards one free-form message from a view to its plugin process (plugin.ui.message). */
export async function postPluginUIMessage(sessionId: string, nonce: string, message: unknown) {
  return unwrapPluginUIResult(await invokeBackend<unknown>("plugin_host_ui_post_message", { sessionId, nonce, message }));
}

/** Payload of the "plugin:ui-binary" event: a frame the plugin process sent to its views. */
export interface PluginUIBinaryEvent {
  pluginId: string;
  generation: number;
  channel: string;
  data: ArrayBuffer;
}

export function parsePluginUIBinaryEvent(value: unknown): PluginUIBinaryEvent | undefined {
  const raw = objectValue(value);
  const generation = Number(raw?.generation);
  if (!raw || typeof raw.pluginId !== "string" || typeof raw.channel !== "string" || typeof raw.data !== "string"
    || !Number.isSafeInteger(generation) || generation < 1) return undefined;
  try {
    return { pluginId: raw.pluginId, generation, channel: raw.channel, data: base64ToBytes(raw.data).buffer as ArrayBuffer };
  } catch {
    return undefined;
  }
}

function bytesToBase64(bytes: Uint8Array) {
  let binary = "";
  for (let index = 0; index < bytes.length; index += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(index, index + 0x8000));
  }
  return btoa(binary);
}

function base64ToBytes(value: string) {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
  return bytes;
}

function unwrapPluginUIResult(result: unknown) {
  const raw = objectValue(result);
  if (raw?.__mincodePluginError !== true) return result;
  const code = Number(raw.code);
  const message = text(raw.message).trim();
  const source = objectValue(raw.data);
  const data: PluginUIInvocationErrorData = {};
  if (typeof source?.kind === "string" && source.kind.length <= 128) data.kind = source.kind;
  if (typeof source?.tool === "string" && source.tool.length <= 128) data.tool = source.tool;
  if (typeof source?.retryable === "boolean") data.retryable = source.retryable;
  throw new PluginUIInvocationError(Number.isInteger(code) ? code : -32000, message || "Plugin action failed", Object.keys(data).length ? data : undefined);
}

function normalizeDevelopmentManifest(value: unknown): PluginDevelopmentManifestSummary | undefined {
  const raw = objectValue(value);
  if (!raw) return undefined;
  const apiVersion = Number(raw.apiVersion);
  const commandCount = Number(raw.commandCount);
  const viewCount = Number(raw.viewCount);
  const actionCount = Number(raw.actionCount);
  if (![apiVersion, commandCount, viewCount, actionCount].every((item) => Number.isSafeInteger(item) && item >= 0)) return undefined;
  return {
    apiVersion,
    runtimeType: text(raw.runtimeType).trim(),
    runtimeEntry: text(raw.runtimeEntry).trim(),
    activation: text(raw.activation).trim(),
    platforms: Array.isArray(raw.platforms) ? raw.platforms.filter((item): item is string => typeof item === "string") : [],
    commandCount,
    viewCount,
    actionCount,
    keymapCount: Number.isSafeInteger(Number(raw.keymapCount)) && Number(raw.keymapCount) >= 0 ? Number(raw.keymapCount) : 0,
    keymapIssues: Array.isArray(raw.keymapIssues)
      ? raw.keymapIssues.filter((item): item is string => typeof item === "string").slice(0, 16)
      : [],
    contributions: normalizeDevelopmentContributions(raw.contributions),
  };
}

function normalizeDevelopmentBase(value: unknown) {
  const raw = objectValue(value);
  if (!raw) return undefined;
  const id = text(raw.id).trim();
  const pluginId = text(raw.pluginId).trim();
  const pluginName = text(raw.pluginName).trim();
  const projectRoot = text(raw.projectRoot).trim();
  const packageRoot = text(raw.packageRoot).trim();
  const cliInstanceId = text(raw.cliInstanceId).trim();
  if (!id || !pluginId || !pluginName || !projectRoot || !packageRoot || !cliInstanceId) return undefined;
  return { raw, id, pluginId, pluginName, projectRoot, packageRoot, cliInstanceId };
}

export async function listPluginDevelopmentPairings(): Promise<PluginDevelopmentPairing[]> {
  const response = await invokeBackend<unknown>("plugin_host_development_pairings", {});
  return (Array.isArray(response) ? response : []).flatMap((value) => {
    const base = normalizeDevelopmentBase(value);
    const manifest = normalizeDevelopmentManifest(base?.raw.manifest);
    if (!base || !manifest || base.raw.status !== "pending" || base.raw.protocolVersion !== 1) return [];
    return [{
      id: base.id,
      cliInstanceId: base.cliInstanceId,
      projectRoot: base.projectRoot,
      packageRoot: base.packageRoot,
      uiUrl: text(base.raw.uiUrl).trim() || undefined,
      pluginId: base.pluginId,
      pluginName: base.pluginName,
      version: text(base.raw.version),
      capabilities: Array.isArray(base.raw.capabilities)
        ? base.raw.capabilities.filter((item): item is string => typeof item === "string")
        : [],
      manifest,
      protocolVersion: 1,
      createdAt: text(base.raw.createdAt),
      expiresAt: text(base.raw.expiresAt),
      status: "pending" as const,
    }];
  });
}

export async function listPluginDevelopmentSessions(): Promise<PluginDevelopmentSession[]> {
  const response = await invokeBackend<unknown>("plugin_host_development_sessions", {});
  return (Array.isArray(response) ? response : []).flatMap((value) => {
    const base = normalizeDevelopmentBase(value);
    const generation = Number(base?.raw.generation);
    const manifest = normalizeDevelopmentManifest(base?.raw.manifest);
    if (!base || !manifest || base.raw.status !== "connected" || base.raw.protocolVersion !== 1 || !Number.isSafeInteger(generation) || generation < 0) return [];
    return [{
      id: base.id,
      pluginId: base.pluginId,
      pluginName: base.pluginName,
      projectRoot: base.projectRoot,
      packageRoot: base.packageRoot,
      uiUrl: text(base.raw.uiUrl).trim() || undefined,
      cliInstanceId: base.cliInstanceId,
      connectedAt: text(base.raw.connectedAt),
      lastSeenAt: text(base.raw.lastSeenAt),
      expiresAt: text(base.raw.expiresAt),
      status: "connected" as const,
      generation,
      protocolVersion: 1,
      capabilities: Array.isArray(base.raw.capabilities)
        ? base.raw.capabilities.filter((item): item is string => typeof item === "string")
        : [],
      manifest,
      buildError: text(base.raw.buildError).trim() || undefined,
      logs: (Array.isArray(base.raw.logs) ? base.raw.logs : []).slice(-200).flatMap((value) => {
        const log = objectValue(value);
        const level = text(log?.level);
        const message = text(log?.message).trim();
        if (!log || !["debug", "info", "warn", "error"].includes(level) || !message || message.length > 4096) return [];
        return [{ level: level as PluginDevelopmentLog["level"], message, createdAt: text(log.createdAt) }];
      }),
    }];
  });
}

export function approvePluginDevelopmentPairing(id: string) {
  return invokeBackend("plugin_host_development_approve", { id });
}

export function rejectPluginDevelopmentPairing(id: string) {
  return invokeBackend("plugin_host_development_reject", { id });
}

export function disconnectPluginDevelopmentSession(id: string) {
  return invokeBackend("plugin_host_development_disconnect", { id });
}

export async function getPluginLogs(id: string): Promise<PluginLogEntry[]> {
  const raw = await invokeBackend<unknown>("plugin_host_logs", { id });
  if (!Array.isArray(raw)) return [];
  return raw.flatMap((value) => {
    const item = objectValue(value);
    if (!item || typeof item.message !== "string") return [];
    return [{ time: text(item.time), level: text(item.level, "info"), message: item.message }];
  });
}

export async function listPluginCategories(): Promise<ManifestCategory[]> {
  const raw = await invokeBackend<unknown>("plugin_host_categories", {});
  if (!Array.isArray(raw) || !raw.every(isManifestCategory) || raw.length !== manifestCategories.length || raw.some((category, index) => category !== manifestCategories[index])) {
    throw new Error("Invalid plugin categories response");
  }
  return raw;
}

export async function previewLocalPlugin(sourcePath: string) {
  return parsePluginInstallPreview(await invokeBackend<unknown>("plugin_host_install_preview", { sourcePath }));
}

export function installLocalPlugin(sourcePath: string) {
  return invokeBackend("plugin_host_install_local", { sourcePath });
}

export function upgradeLocalPlugin(id: string, sourcePath: string) {
 return invokeBackend("plugin_host_upgrade_local", { id, sourcePath });
}


export function uninstallPlugin(id: string, purgeData = false) {
  return invokeBackend("plugin_host_uninstall", { id, purgeData });
}

export function clearPluginData(id: string) {
  return invokeBackend("plugin_host_clear_data", { id });
}

/** 插件数据归档（zip）的清单：键值数据条数、插件文件数与字节数。插件密钥不在归档中。 */
export interface PluginDataArchiveManifest {
  pluginId: string;
  pluginName?: string;
  pluginVersion?: string;
  exportedAt: string;
  keys: number;
  files: number;
  fileBytes: number;
}

export function exportPluginDataArchive(id: string, path: string) {
  return invokeBackend<PluginDataArchiveManifest>("plugin_host_data_export_archive", { id, path });
}

export function inspectPluginDataArchive(path: string) {
  return invokeBackend<PluginDataArchiveManifest>("plugin_host_data_inspect_archive", { path });
}

export function importPluginDataArchive(id: string, path: string) {
  return invokeBackend<PluginDataArchiveManifest>("plugin_host_data_import_archive", { id, path });
}

export interface OfficialPlugin {
  bundled?: boolean;
  category?: PluginCategory;
  tags?: string[];
  author?: string;
  homepage?: string;
  icon?: string;
  cover?: string;
  id: string;
  name: string;
  version: string;
  description: string;
  capabilities: string[];
  supported: boolean;
  installed: boolean;
  installedVersion: string;
  updateAvailable: boolean;
}



export async function previewRemotePlugin(url: string) {
  return parsePluginInstallPreview(await invokeBackend<unknown>("plugin_host_remote_preview", { url }));
}
