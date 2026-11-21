import type { AppLocale } from "@/i18n/messages";
import type { PluginRisk } from "@/services/plugin-service";
import type { UiTheme } from "@/services/ui-theme";
import type { CustomEditorDocumentInfo } from "@/domain/plugin-ui/document-protocol";

export const PLUGIN_UI_PROTOCOL_VERSION = 1;
export const PLUGIN_UI_TASK_TIMEOUT_MS = 300_000;
export const PLUGIN_UI_MAX_TASKS = 8;
export const PLUGIN_UI_MAX_RETAINED_TASKS = 64;
export const PLUGIN_UI_TASK_RETENTION_MS = 300_000;
export const PLUGIN_UI_MAX_TASK_RESULT_BYTES = 256 * 1024;
export const PLUGIN_UI_MAX_TASK_PROGRESS_BYTES = 4 * 1024;
export const PLUGIN_UI_MAX_TASK_EXTRA_BYTES = 8 * 1024;
export const PLUGIN_UI_MAX_TASK_SNAPSHOT_BYTES = PLUGIN_UI_MAX_TASK_RESULT_BYTES + PLUGIN_UI_MAX_TASK_PROGRESS_BYTES + PLUGIN_UI_MAX_TASK_EXTRA_BYTES;
export const PLUGIN_UI_MAX_TASK_RESPONSE_BYTES = PLUGIN_UI_MAX_TASK_SNAPSHOT_BYTES + 1024;
/** 插件进程单任务门闩的保留 JSON-RPC 错误码；宿主桥接映射为 "busy" 供 UI 退避重试。 */
export const PLUGIN_PROCESS_BUSY_ERROR_CODE = -32029;

export interface PluginUITaskOwnership {
  expires: number;
  terminal: boolean;
  /** The page received the terminal snapshot (a get/cancel response or a forwarded push). */
  delivered?: boolean;
}

export function rememberPluginUITask(tasks: Map<string, PluginUITaskOwnership>, task: PluginUITaskSnapshot, now = Date.now()) {
  const previous = tasks.get(task.taskId);
  // Late running responses must neither reopen a slot nor extend terminal retention.
  if (previous?.terminal) return;
  const terminal = task.state !== "running";
  tasks.set(task.taskId, {
    terminal,
    expires: terminal ? now + PLUGIN_UI_TASK_RETENTION_MS : previous?.expires ?? now + PLUGIN_UI_TASK_TIMEOUT_MS + PLUGIN_UI_TASK_RETENTION_MS,
  });
}

/** Records that the page received this task's terminal snapshot; running tasks are never marked. */
export function markPluginUITaskDelivered(tasks: Map<string, PluginUITaskOwnership>, taskId: string) {
  const owned = tasks.get(taskId);
  if (owned?.terminal) owned.delivered = true;
}

/**
 * Frees retained-task slots for a new start like the backend does: finished
 * tasks whose terminal snapshot the page already received are dropped, oldest
 * finished first, until `reserved` more tasks fit under `limit`. Running and
 * undelivered terminal tasks are kept, so a result the page has not read is
 * never lost.
 */
export function evictDeliveredPluginUITasks(tasks: Map<string, PluginUITaskOwnership>, reserved: number, limit = PLUGIN_UI_MAX_RETAINED_TASKS) {
  let excess = tasks.size + reserved - limit + 1;
  if (excess <= 0) return;
  // Terminal expiry is finish time + retention, so it orders by finish time.
  const delivered = [...tasks].filter(([, task]) => task.terminal && task.delivered).sort(([, a], [, b]) => a.expires - b.expires);
  for (const [id] of delivered) {
    if (excess-- <= 0) break;
    tasks.delete(id);
  }
}

export interface PluginUITaskSnapshot {
  taskId: string;
  state: "running" | "succeeded" | "failed" | "cancelled";
  cancelRequested: boolean;
  outcomeUnconfirmed?: boolean;
  progress?: unknown;
  result?: unknown;
  error?: string;
}

export type PluginUITaskRequest =
  | { type: "mincode.plugin.task.start"; id: string; action: string; payload?: unknown; timeoutMs: number }
  | { type: "mincode.plugin.task.get" | "mincode.plugin.task.cancel"; id: string; taskId: string };

/** `measuredBytes` is the message size from measurePluginUIMessage, so the caller's single serialization is reused. */
export function parsePluginUITaskRequest(value: unknown, measuredBytes?: number): PluginUITaskRequest | undefined {
  const raw = recordValue(value);
  if (!raw || (measuredBytes ?? pluginUIMessageByteLength(raw, PLUGIN_UI_MAX_MESSAGE_BYTES)) > PLUGIN_UI_MAX_MESSAGE_BYTES) return undefined;
  const id = boundedText(raw.id, 128);
  if (!id) return undefined;
  if (raw.type === "mincode.plugin.task.start") {
    const action = boundedText(raw.action, 128);
    const timeoutMs = raw.timeoutMs === undefined ? PLUGIN_UI_TASK_TIMEOUT_MS : raw.timeoutMs;
    if (!action || typeof timeoutMs !== "number" || !Number.isSafeInteger(timeoutMs)
      || timeoutMs < 1 || timeoutMs > PLUGIN_UI_TASK_TIMEOUT_MS) return undefined;
    return { type: raw.type, id, action, payload: raw.payload, timeoutMs };
  }
  if (raw.type === "mincode.plugin.task.get" || raw.type === "mincode.plugin.task.cancel") {
    const taskId = boundedText(raw.taskId, 256);
    if (taskId) return { type: raw.type, id, taskId };
  }
  return undefined;
}

export function parsePluginUITaskSnapshot(value: unknown, expectedTaskId?: string): PluginUITaskSnapshot {
  const raw = recordValue(value);
  const { result, progress, ...extra } = raw ?? {};
  if (!raw || !boundedText(raw.taskId, 256) || (expectedTaskId !== undefined && raw.taskId !== expectedTaskId)
    || !["running", "succeeded", "failed", "cancelled"].includes(String(raw.state))
    || (raw.outcomeUnconfirmed !== undefined && typeof raw.outcomeUnconfirmed !== "boolean")
    || typeof raw.cancelRequested !== "boolean" || (raw.error !== undefined && typeof raw.error !== "string")
    || (result !== undefined && pluginUIMessageByteLength(result, PLUGIN_UI_MAX_TASK_RESULT_BYTES) > PLUGIN_UI_MAX_TASK_RESULT_BYTES)
    || (progress !== undefined && pluginUIMessageByteLength(progress, PLUGIN_UI_MAX_TASK_PROGRESS_BYTES) > PLUGIN_UI_MAX_TASK_PROGRESS_BYTES)
    || pluginUIMessageByteLength(extra, PLUGIN_UI_MAX_TASK_EXTRA_BYTES) > PLUGIN_UI_MAX_TASK_EXTRA_BYTES
    || pluginUIMessageByteLength(raw, PLUGIN_UI_MAX_TASK_SNAPSHOT_BYTES) > PLUGIN_UI_MAX_TASK_SNAPSHOT_BYTES) {
    throw new Error("Invalid plugin task snapshot");
  }
  return { taskId: raw.taskId as string, state: raw.state as PluginUITaskSnapshot["state"],
    outcomeUnconfirmed: raw.outcomeUnconfirmed as boolean | undefined,
    cancelRequested: raw.cancelRequested, progress: raw.progress, result: raw.result, error: raw.error as string | undefined };
}
export const PLUGIN_UI_MAX_IN_FLIGHT_REQUESTS = 8;
export const PLUGIN_UI_MAX_MESSAGE_BYTES = 256 * 1024;
export const PLUGIN_UI_MAX_CLIPBOARD_TEXT_BYTES = 128 * 1024;
export const PLUGIN_UI_MAX_CONFIRM_SUBJECT_LENGTH = 500;
/** ai.config: items per preview/apply request (each is checked again by parseSyncItems). */
export const PLUGIN_UI_MAX_AI_CONFIG_ITEMS = 500;
const PLUGIN_UI_MAX_AI_CONFIG_KEY_LENGTH = 512;
/** ai.config auth-status: sourceIds per request. */
export const PLUGIN_UI_MAX_AI_CONFIG_AUTH_SOURCES = 200;

export interface PluginUIContext {
  /** Base UI language (zh | en). */
  locale: AppLocale;
  /**
   * Active UI language tag when the user selected a language pack
   * (contributes.localizations), for example "ja"; absent otherwise. Plugins
   * without that language keep using `locale`.
   */
  localeTag?: string;
  /** A built-in theme id; a contributed plugin theme is sent as its kind ("light" or "dark"). */
  theme: UiTheme;
  /** Light/dark kind of the active theme. */
  themeKind?: "light" | "dark";
  /** Validated `--app-*` hex tokens of an active contributed theme (contributes.themes). */
  themeTokens?: Record<string, string>;
  /** Where the page is embedded: a workspace tab, a custom-editor document, a dashboard widget card, a floating window or a settings page. */
  surface?: PluginUISurface;
}

export type PluginUISurface = "workspace" | "document" | "widget" | "floating" | "settings";

const PLUGIN_UI_SURFACES: readonly PluginUISurface[] = ["workspace", "document", "widget", "floating", "settings"];

/** Bounded parse of a context `surface`; anything else is dropped. */
export function parsePluginUISurface(value: unknown): PluginUISurface | undefined {
  return PLUGIN_UI_SURFACES.includes(value as PluginUISurface) ? (value as PluginUISurface) : undefined;
}

export interface PluginUIReadyMessage {
  type: "mincode.plugin.ready";
  protocolVersion: number;
  sessionId: string;
  nonce: string;
  generation: number;
}

export interface PluginUIConnectMessage {
  type: "mincode.host.connect";
  protocolVersion: number;
  sessionId: string;
  nonce: string;
  generation: number;
  requestTimeoutMs?: number;
  /** features.aiConfig is a level: 1 preview/apply; 2 adds connector prompts, auth and login. */
  features?: { tasks: 1; taskEvents?: 1; viewSave?: 1; clipboardWrite?: 1; trustedConfirm?: 1; configuration?: 1; document?: 1; openRequest?: 1; calls?: 1; messages?: 1; binary?: 1; binaryDirect?: 1; immersive?: 1; keyboardCapture?: 1; floating?: 1; visibility?: 1; prefetch?: 1; aiConfig?: 1 | 2 };
  /** features.visibility: whether the page is currently shown; later changes arrive as `mincode.host.visibility`. */
  visible?: boolean;
  /** features.prefetch: the view's declared prefetch action; its outcome arrives once as `mincode.host.prefetch`. */
  prefetch?: { action: string };
  context: PluginUIContext;
  /** contributes.customEditors: the bound document (see document-protocol.ts). */
  document?: CustomEditorDocumentInfo;
  actions: Array<{
    id: string;
    risk: PluginRisk;
    maxPayloadBytes: number;
    resultHandling?: "save-text-file" | "open-url";
  }>;
}

export interface PluginUIInvokeRequest {
  type: "mincode.plugin.invoke";
  id: string;
  action: string;
  payload?: unknown;
}

/** plugin.call from a view: a method the plugin process registered (not a manifest action). */
export interface PluginUICallRequest {
  type: "mincode.plugin.call";
  id: string;
  method: string;
  params: unknown;
}

/** A free-form view message to the plugin process (plugin.ui.message), like VS Code's webview postMessage. */
export interface PluginUIMessageRequest {
  type: "mincode.plugin.message";
  id: string;
  message: unknown;
}

/** A binary frame from a view to its framed-transport plugin process. */
export interface PluginUIBinaryRequest {
  type: "mincode.plugin.binary";
  id: string;
  channel: string;
  data: ArrayBuffer;
}

/** A binary frame from the plugin process, pushed to its views (data is transferred). */
export interface PluginUIBinaryMessage {
  type: "mincode.host.binary";
  channel: string;
  data: ArrayBuffer;
}

export const PLUGIN_UI_MAX_BINARY_BYTES = 8 * 1024 * 1024;
const PLUGIN_UI_CHANNEL = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/;
const PLUGIN_UI_METHOD = /^[A-Za-z][A-Za-z0-9_]*(?:[./:-][A-Za-z0-9_]+)*$/;

export function isPluginUIChannel(value: unknown): value is string {
  return typeof value === "string" && PLUGIN_UI_CHANNEL.test(value);
}

/** Mirrors validPluginUIMethod in backend/plugin/ui_calls.go. */
export function isPluginUIMethod(value: unknown): value is string {
  return typeof value === "string" && value.length <= 128 && PLUGIN_UI_METHOD.test(value) && !/^(plugin|host|rpc)\./i.test(value);
}

export type PluginUIHostRequest =
  | { type: "mincode.plugin.clipboard.write-text"; id: string; text: string; sensitive: boolean }
  | { type: "mincode.plugin.confirm"; id: string; intent: "write" | "dangerous"; subject: string }
  // contributes.configuration: the host answers with non-secret values only.
  | { type: "mincode.plugin.configuration.get"; id: string; keys?: string[] }
  | { type: "mincode.plugin.configuration.open"; id: string }
  // views[].floating: show, hide or query one of the plugin's own floating views.
  | { type: "mincode.plugin.floating.show"; id: string; viewId: string }
  | { type: "mincode.plugin.floating.hide"; id: string; viewId: string }
  | { type: "mincode.plugin.floating.status"; id: string; viewId: string }
  // Only the page inside the floating window may resize it (within its declared size).
  | { type: "mincode.plugin.floating.resize"; id: string; width: number; height: number }
  // After a click in the floating window: open something in the main window (single-use click token).
  | { type: "mincode.plugin.floating.activate"; id: string; token: string; target: PluginUIFloatingTarget }
  // ai.config: items are validated again with parseSyncItems by the frame; apply always waits for the host confirmation.
  | { type: "mincode.plugin.ai-config.preview"; id: string; items: unknown[] }
  | { type: "mincode.plugin.ai-config.apply"; id: string; items: unknown[]; choices: PluginUIAiConfigChoice[] }
  // ai.config level 2: start the host sign-in of a connector this plugin installed; only a status comes back.
  | { type: "mincode.plugin.ai-config.login"; id: string; kind: "mcp"; sourceId: string }
  // ai.config level 2, read-only: the sign-in status of connectors this plugin installed; only statuses come back.
  | { type: "mincode.plugin.ai-config.auth-status"; id: string; sourceIds: string[] }
  // features.binaryDirect: a framed-transport view asks for its direct binary channel URL (single-use token).
  | { type: "mincode.plugin.direct.token"; id: string }
  // features.immersive: hide the MinCode chrome and make the window fullscreen while this view shows.
  | { type: "mincode.plugin.immersive.enter"; id: string }
  | { type: "mincode.plugin.immersive.exit"; id: string }
  // features.keyboardCapture: pause MinCode shortcuts so key combinations reach this view.
  | { type: "mincode.plugin.keyboard.capture"; id: string }
  | { type: "mincode.plugin.keyboard.release"; id: string };

/** One picked preview entry: `key` is the entry's sync key, `action` what to do with it. */
export interface PluginUIAiConfigChoice {
  key: string;
  action: "add" | "overwrite";
}

/** What a click in a floating window may open in the main window (checked again by the host). */
export type PluginUIFloatingTarget =
  | { kind: "app" }
  | { kind: "ai-runs" }
  | { kind: "view"; viewId: string }
  | { kind: "ai-run"; runId: string }
  | { kind: "ai-run-cancel"; runId: string };

/** Host push to the page inside a floating window after the user clicked it (not dragged). */
export interface PluginUIFloatingClickMessage {
  type: "mincode.host.floating.click";
  token: string;
  /** Click position in page CSS pixels (from the top-left of the view); omitted when unknown. */
  x?: number;
  y?: number;
}

/** Host push after one of the plugin's floating views opened or closed. */
export interface PluginUIFloatingMessage {
  type: "mincode.host.floating";
  viewId: string;
  visible: boolean;
}

/** Host push after the plugin's settings changed; never carries secret settings. */
export interface PluginUIConfigurationMessage {
  type: "mincode.host.configuration";
  keys: string[];
  values: Record<string, unknown>;
}

export const PLUGIN_UI_MAX_CONFIGURATION_KEYS = 256;

export interface PluginUIContextMessage {
  type: "mincode.host.context";
  context: PluginUIContext;
}

/** Most open requests queued in a frame until its session is ready. */
export const PLUGIN_UI_MAX_PENDING_OPEN_REQUESTS = 4;
/** Serialized size limit of one open request. */
export const PLUGIN_UI_MAX_OPEN_REQUEST_BYTES = 8 * 1024;

/**
 * Host push asking an open view to show something (a uriHandlers link, a
 * searchProviders result or a treeViews item). Only sent to frames that negotiated
 * `features.openRequest`; the values are untrusted input for the plugin.
 */
export interface PluginUIOpenRequest {
  requestId: string;
  /** "plugin": another plugin (or the same one) asked through host.view.open; `from` names it. */
  source: "uri" | "search" | "tree" | "plugin" | "external";
  from?: string;
  entry: string;
  path?: string;
  pathParams?: Record<string, string>;
  params?: Record<string, string | number | boolean>;
}

export interface PluginUIOpenRequestMessage extends PluginUIOpenRequest {
  type: "mincode.host.open-request";
}

function scalarRecord<T extends string | number | boolean>(value: unknown, accepts: (item: unknown) => item is T): Record<string, T> | undefined | false {
  if (value === undefined) return undefined;
  const raw = recordValue(value);
  if (!raw) return false;
  const out: Record<string, T> = {};
  for (const [key, item] of Object.entries(raw)) {
    if (!accepts(item) || key.length === 0 || key.length > 128) return false;
    out[key] = item;
  }
  return out;
}

const isString = (item: unknown): item is string => typeof item === "string";
const isScalar = (item: unknown): item is string | number | boolean =>
  typeof item === "string" || typeof item === "boolean" || (typeof item === "number" && Number.isFinite(item));

/** Bounded parse of an open request; undefined when malformed or over 8 KiB. */
export function parsePluginUIOpenRequest(value: unknown): PluginUIOpenRequest | undefined {
  const raw = recordValue(value);
  if (!raw || pluginUIMessageByteLength(raw) > PLUGIN_UI_MAX_OPEN_REQUEST_BYTES) return undefined;
  const requestId = boundedText(raw.requestId, 128);
  const entry = boundedText(raw.entry, 128);
  if (!requestId || !entry || (raw.source !== "uri" && raw.source !== "search" && raw.source !== "tree" && raw.source !== "plugin" && raw.source !== "external")) return undefined;
  if (raw.from !== undefined && (raw.source !== "plugin" || typeof raw.from !== "string" || !/^[a-z0-9][a-z0-9._-]{1,127}$/.test(raw.from))) return undefined;
  if (raw.path !== undefined && (typeof raw.path !== "string" || raw.path.length > 2048)) return undefined;
  const pathParams = scalarRecord(raw.pathParams, isString);
  const params = scalarRecord(raw.params, isScalar);
  if (pathParams === false || params === false) return undefined;
  const request: PluginUIOpenRequest = { requestId, source: raw.source, entry };
  if (typeof raw.from === "string") request.from = raw.from;
  if (typeof raw.path === "string") request.path = raw.path;
  if (pathParams) request.pathParams = pathParams;
  if (params) request.params = params;
  return request;
}

export interface PluginUIResponseMessage {
  type: "mincode.host.response";
  id: string;
  ok: boolean;
  result?: unknown;
  error?: {
    code: "invalid-request" | "not-allowed" | "payload-too-large" | "result-too-large" | "busy" | "cancelled" | "invoke-failed" | "host-unavailable" | "conflict";
    message: string;
    retryable?: boolean;
    details?: { kind?: string; tool?: string };
  };
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function boundedText(value: unknown, maxLength: number) {
  return typeof value === "string" && value.length > 0 && value.length <= maxLength ? value : undefined;
}

export function parsePluginUIReadyMessage(value: unknown): PluginUIReadyMessage | undefined {
  const raw = recordValue(value);
  if (!raw || raw.type !== "mincode.plugin.ready") return undefined;
  const sessionId = boundedText(raw.sessionId, 256);
  const nonce = boundedText(raw.nonce, 256);
  const generation = Number(raw.generation);
  const protocolVersion = Number(raw.protocolVersion);
  if (
    !sessionId
    || !nonce
    || !Number.isSafeInteger(generation)
    || generation < 1
    || protocolVersion !== PLUGIN_UI_PROTOCOL_VERSION
  ) return undefined;
  return { type: "mincode.plugin.ready", protocolVersion, sessionId, nonce, generation };
}

export function parsePluginUIInvokeRequest(value: unknown): PluginUIInvokeRequest | undefined {
  const raw = recordValue(value);
  if (!raw || raw.type !== "mincode.plugin.invoke") return undefined;
  const id = boundedText(raw.id, 128);
  const action = boundedText(raw.action, 128);
  if (!id || !action) return undefined;
  return { type: "mincode.plugin.invoke", id, action, payload: raw.payload };
}

export function parsePluginUICallRequest(value: unknown): PluginUICallRequest | undefined {
  const raw = recordValue(value);
  if (!raw || raw.type !== "mincode.plugin.call") return undefined;
  const id = boundedText(raw.id, 128);
  if (!id || !isPluginUIMethod(raw.method) || pluginUIMessageByteLength(raw, PLUGIN_UI_MAX_MESSAGE_BYTES) > PLUGIN_UI_MAX_MESSAGE_BYTES) return undefined;
  return { type: "mincode.plugin.call", id, method: raw.method, params: raw.params ?? null };
}

export function parsePluginUIMessageRequest(value: unknown): PluginUIMessageRequest | undefined {
  const raw = recordValue(value);
  if (!raw || raw.type !== "mincode.plugin.message" || raw.message === undefined || raw.message === null) return undefined;
  const id = boundedText(raw.id, 128);
  if (!id || pluginUIMessageByteLength(raw, PLUGIN_UI_MAX_MESSAGE_BYTES) > PLUGIN_UI_MAX_MESSAGE_BYTES) return undefined;
  return { type: "mincode.plugin.message", id, message: raw.message };
}

/** Payload of the "plugin:ui-message" event: a message the plugin process posted to its views. */
export interface PluginUIMessageEvent {
  pluginId: string;
  generation: number;
  /** Only views with this manifest view ID receive the message; empty means every open view. */
  viewId: string;
  message: unknown;
}

export function parsePluginUIMessageEvent(value: unknown): PluginUIMessageEvent | undefined {
  const raw = recordValue(value);
  const generation = Number(raw?.generation);
  const viewId = raw?.viewId ?? "";
  if (!raw || typeof raw.pluginId !== "string" || !raw.pluginId || typeof viewId !== "string" || viewId.length > 64
    || raw.message === undefined || raw.message === null || !Number.isSafeInteger(generation) || generation < 1) return undefined;
  return { pluginId: raw.pluginId, generation, viewId, message: raw.message };
}

export function parsePluginUIBinaryRequest(value: unknown): PluginUIBinaryRequest | undefined {
  const raw = recordValue(value);
  if (!raw || raw.type !== "mincode.plugin.binary" || !(raw.data instanceof ArrayBuffer)) return undefined;
  const id = boundedText(raw.id, 128);
  if (!id || !isPluginUIChannel(raw.channel) || raw.data.byteLength > PLUGIN_UI_MAX_BINARY_BYTES) return undefined;
  return { type: "mincode.plugin.binary", id, channel: raw.channel, data: raw.data };
}

const PLUGIN_UI_HOST_REQUEST_TYPES: ReadonlySet<unknown> = new Set([
  "mincode.plugin.clipboard.write-text",
  "mincode.plugin.confirm",
  "mincode.plugin.configuration.get",
  "mincode.plugin.configuration.open",
  "mincode.plugin.floating.show",
  "mincode.plugin.floating.hide",
  "mincode.plugin.floating.status",
  "mincode.plugin.floating.activate",
  "mincode.plugin.floating.resize",
  "mincode.plugin.ai-config.preview",
  "mincode.plugin.ai-config.apply",
  "mincode.plugin.ai-config.login",
  "mincode.plugin.ai-config.auth-status",
  "mincode.plugin.direct.token",
  "mincode.plugin.immersive.enter",
  "mincode.plugin.immersive.exit",
  "mincode.plugin.keyboard.capture",
  "mincode.plugin.keyboard.release",
]);

function parseAiConfigItems(value: unknown): unknown[] | undefined {
  return Array.isArray(value) && value.length <= PLUGIN_UI_MAX_AI_CONFIG_ITEMS ? [...value] : undefined;
}

/** At least one choice, no duplicate keys, and only add / overwrite. */
function parseAiConfigChoices(value: unknown): PluginUIAiConfigChoice[] | undefined {
  if (!Array.isArray(value) || value.length === 0 || value.length > PLUGIN_UI_MAX_AI_CONFIG_ITEMS) return undefined;
  const keys = new Set<string>();
  const choices: PluginUIAiConfigChoice[] = [];
  for (const item of value) {
    const choice = recordValue(item);
    const key = boundedText(choice?.key, PLUGIN_UI_MAX_AI_CONFIG_KEY_LENGTH);
    if (!choice || !key || keys.has(key) || (choice.action !== "add" && choice.action !== "overwrite")) return undefined;
    keys.add(key);
    choices.push({ key, action: choice.action });
  }
  return choices;
}

/** 1–200 non-empty, distinct sourceIds of at most 512 characters each. */
function parseAiConfigSourceIds(value: unknown): string[] | undefined {
  if (!Array.isArray(value) || value.length === 0 || value.length > PLUGIN_UI_MAX_AI_CONFIG_AUTH_SOURCES) return undefined;
  const seen = new Set<string>();
  for (const item of value) {
    const sourceId = boundedText(item, PLUGIN_UI_MAX_AI_CONFIG_KEY_LENGTH);
    if (!sourceId?.trim() || seen.has(sourceId)) return undefined;
    seen.add(sourceId);
  }
  return [...seen];
}

/**
 * The type is checked before the size, so invoke/task messages (the large ones) are not serialized
 * again here; `measuredBytes` reuses the caller's measurePluginUIMessage result.
 */
export function parsePluginUIHostRequest(value: unknown, measuredBytes?: number): PluginUIHostRequest | undefined {
  const raw = recordValue(value);
  const id = boundedText(raw?.id, 128);
  if (!raw || !id || !PLUGIN_UI_HOST_REQUEST_TYPES.has(raw.type)
    || (measuredBytes ?? pluginUIMessageByteLength(raw, PLUGIN_UI_MAX_MESSAGE_BYTES)) > PLUGIN_UI_MAX_MESSAGE_BYTES) return undefined;
  if (raw.type === "mincode.plugin.clipboard.write-text") {
    if (typeof raw.text !== "string" || utf8ByteLength(raw.text) > PLUGIN_UI_MAX_CLIPBOARD_TEXT_BYTES
      || (raw.sensitive !== undefined && typeof raw.sensitive !== "boolean")) return undefined;
    return { type: raw.type, id, text: raw.text, sensitive: raw.sensitive === true };
  }
  if (raw.type === "mincode.plugin.confirm") {
    const subject = boundedText(raw.subject, PLUGIN_UI_MAX_CONFIRM_SUBJECT_LENGTH);
    if (!subject || (raw.intent !== "write" && raw.intent !== "dangerous")) return undefined;
    return { type: raw.type, id, intent: raw.intent, subject };
  }
  if (raw.type === "mincode.plugin.configuration.get") {
    if (raw.keys === undefined) return { type: raw.type, id };
    if (!Array.isArray(raw.keys) || raw.keys.length > PLUGIN_UI_MAX_CONFIGURATION_KEYS
      || raw.keys.some((key) => typeof key !== "string" || key.length === 0 || key.length > 64)) return undefined;
    return { type: raw.type, id, keys: raw.keys as string[] };
  }
  if (raw.type === "mincode.plugin.configuration.open") return { type: raw.type, id };
  if (raw.type === "mincode.plugin.floating.show" || raw.type === "mincode.plugin.floating.hide" || raw.type === "mincode.plugin.floating.status") {
    const viewId = boundedText(raw.viewId, 128);
    if (!viewId || !PLUGIN_UI_VIEW_ID_PATTERN.test(viewId)) return undefined;
    return { type: raw.type, id, viewId };
  }
  if (raw.type === "mincode.plugin.floating.activate") {
    const token = boundedText(raw.token, 128);
    const target = parsePluginUIFloatingTarget(raw.target);
    return token && target ? { type: raw.type, id, token, target } : undefined;
  }
  if (raw.type === "mincode.plugin.floating.resize") {
    const size = (value: unknown) => Number.isSafeInteger(value) && (value as number) >= PLUGIN_UI_FLOATING_MIN_SIZE && (value as number) <= PLUGIN_UI_FLOATING_MAX_SIZE;
    if (!size(raw.width) || !size(raw.height)) return undefined;
    return { type: raw.type, id, width: raw.width as number, height: raw.height as number };
  }
  if (raw.type === "mincode.plugin.ai-config.preview") {
    const items = parseAiConfigItems(raw.items);
    return items ? { type: raw.type, id, items } : undefined;
  }
  if (raw.type === "mincode.plugin.ai-config.apply") {
    const items = parseAiConfigItems(raw.items);
    const choices = parseAiConfigChoices(raw.choices);
    return items && choices ? { type: raw.type, id, items, choices } : undefined;
  }
  if (raw.type === "mincode.plugin.ai-config.login") {
    const sourceId = boundedText(raw.sourceId, PLUGIN_UI_MAX_AI_CONFIG_KEY_LENGTH);
    return raw.kind === "mcp" && sourceId?.trim() ? { type: raw.type, id, kind: raw.kind, sourceId } : undefined;
  }
  if (raw.type === "mincode.plugin.ai-config.auth-status") {
    const sourceIds = parseAiConfigSourceIds(raw.sourceIds);
    return sourceIds ? { type: raw.type, id, sourceIds } : undefined;
  }
  if (raw.type === "mincode.plugin.direct.token" || raw.type === "mincode.plugin.immersive.enter" || raw.type === "mincode.plugin.immersive.exit"
    || raw.type === "mincode.plugin.keyboard.capture" || raw.type === "mincode.plugin.keyboard.release") return { type: raw.type, id };
  return undefined;
}

export function parsePluginUIFloatingTarget(value: unknown): PluginUIFloatingTarget | undefined {
  const raw = recordValue(value);
  if (!raw) return undefined;
  if (raw.kind === "app" || raw.kind === "ai-runs") return { kind: raw.kind };
  if (raw.kind === "view") {
    const viewId = boundedText(raw.viewId, 128);
    return viewId && PLUGIN_UI_VIEW_ID_PATTERN.test(viewId) ? { kind: "view", viewId } : undefined;
  }
  if ((raw.kind === "ai-run" || raw.kind === "ai-run-cancel") && typeof raw.runId === "string" && /^run-[0-9a-f]{32}$/.test(raw.runId)) {
    return { kind: raw.kind, runId: raw.runId };
  }
  return undefined;
}

/** Matches the host's contribution ID pattern for view IDs. */
const PLUGIN_UI_VIEW_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
/** Bounds of views[].floating sizes; the host also caps a resize at the declared size. */
export const PLUGIN_UI_FLOATING_MIN_SIZE = 48;
export const PLUGIN_UI_FLOATING_MAX_SIZE = 480;

export function serializePluginUIPayload(value: unknown): string {
  const serialized = JSON.stringify(value === undefined ? null : value);
  if (typeof serialized !== "string") return "null";
  return serialized;
}

const utf8Encoder = new TextEncoder();

export function utf8ByteLength(value: string): number {
  return utf8Encoder.encode(value).byteLength;
}

/** Bounded checks reuse one scratch buffer; larger limits fall back to utf8ByteLength. */
const UTF8_SCRATCH_MAX_BYTES = 1024 * 1024 + 4;
let utf8Scratch: Uint8Array | undefined;

/**
 * UTF-8 size of `value` for a `size > limit` check, without allocating an encoded copy per call:
 * exact when the size is at most `limit`, otherwise some number greater than `limit`.
 */
export function utf8ByteLengthUpTo(value: string, limit: number): number {
  // Every UTF-16 code unit encodes to at least one byte.
  if (value.length > limit) return value.length;
  if (!(limit >= 0) || limit + 4 > UTF8_SCRATCH_MAX_BYTES) return utf8ByteLength(value);
  if (!utf8Scratch || utf8Scratch.length < limit + 4) utf8Scratch = new Uint8Array(Math.max(limit + 4, 64 * 1024));
  // encodeInto stops before a code point (at most 4 bytes) that does not fit, so a partial read means more than `limit` bytes.
  const { read, written } = utf8Encoder.encodeInto(value, utf8Scratch.subarray(0, limit + 4));
  return read < value.length ? limit + 1 : written;
}

/** UTF-8 size of JSON.stringify(value); with `limit`, exact only up to it (see utf8ByteLengthUpTo). */
export function pluginUIMessageByteLength(value: unknown, limit?: number): number {
  try {
    const serialized = JSON.stringify(value);
    if (typeof serialized !== "string") return 0;
    return limit === undefined ? utf8ByteLength(serialized) : utf8ByteLengthUpTo(serialized, limit);
  } catch {
    return Number.POSITIVE_INFINITY;
  }
}

/** `"payload":` as JSON.stringify writes the member name. */
const PAYLOAD_MEMBER_PREFIX_BYTES = 10;

export interface PluginUIMeasuredMessage {
  /** UTF-8 size of JSON.stringify(message): exact up to the limit, otherwise greater than it; Infinity if unserializable. */
  bytes: number;
  /** For a plain message object with a payload: the payload exactly as serializePluginUIPayload returns it. */
  payload?: string;
  /** UTF-8 size of `payload`, with the same limit semantics as `bytes`. */
  payloadBytes?: number;
}

/**
 * Serializes an incoming bridge message once. For a plain object with a `payload` member, the payload
 * is serialized separately and reused as the invoke payload, and the message size is derived from it
 * plus the (small) remaining members, matching utf8(JSON.stringify(message)) byte for byte.
 */
export function measurePluginUIMessage(value: unknown, limit = PLUGIN_UI_MAX_MESSAGE_BYTES): PluginUIMeasuredMessage {
  const raw = recordValue(value);
  if (!raw || Object.getPrototypeOf(raw) !== Object.prototype || !Object.prototype.hasOwnProperty.call(raw, "payload")
    || raw.payload === undefined || typeof raw.toJSON === "function") {
    return { bytes: pluginUIMessageByteLength(value, limit) };
  }
  try {
    const { payload: payloadValue, ...rest } = raw;
    const restJSON = JSON.stringify(rest);
    const restBytes = utf8ByteLengthUpTo(restJSON, limit);
    const serialized = JSON.stringify(payloadValue);
    // A payload JSON drops (function/symbol) is omitted from the message; serializePluginUIPayload sends null.
    if (typeof serialized !== "string") return { bytes: restBytes, payload: "null", payloadBytes: 4 };
    const payloadBytes = utf8ByteLengthUpTo(serialized, limit);
    const separatorBytes = restJSON === "{}" ? 0 : 1;
    return { bytes: restBytes + separatorBytes + PAYLOAD_MEMBER_PREFIX_BYTES + payloadBytes, payload: serialized, payloadBytes };
  } catch {
    return { bytes: Number.POSITIVE_INFINITY };
  }
}
