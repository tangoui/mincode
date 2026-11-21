/**
 * Frontend stage of the `uriHandlers` contribution point: re-validation of
 * handler entries the Go host already accepted
 * (backend/plugin/contrib_uri_handlers.go), re-validation of
 * `plugin_host_uri_resolve` responses (uri_handler_resolve.go) and the pure
 * link gate that deduplicates links and bounds the confirmation queue. No
 * DOM, Vue or IPC here; the runtime lives in services/plugin-uri-links.ts,
 * services/contributions/uri-handlers.ts and stores/plugin-uri-handlers.ts.
 *
 * Every mincode://<pluginId>/<path> link is confirmed by the user in a host
 * dialog naming the plugin before anything opens, in every approval mode.
 */
import type { PointContribution } from "@/domain/plugins/contributions";

export const URI_HANDLERS_CONTRIBUTION_POINT = "uriHandlers";

/** Mirrors contribution-points.json uriHandlers limits (the host enforces them first). */
export const URI_HANDLER_LIMITS = {
  maxEntries: 16,
  titleMaxRunes: 64,
  pathMaxLength: 256,
  pathSegmentsMax: 8,
  pathParamMaxLength: 128,
  queryPropertiesMax: 16,
  queryValueMaxRunes: 1024,
  urlMaxLength: 2048,
  pendingLinksMax: 3,
  duplicateWindowMs: 5000,
} as const;

/** Mirrors contribution-points.json uriHandlers lists.reservedHosts. */
export const URI_HANDLER_RESERVED_HOSTS: ReadonlySet<string> = new Set([
  "settings", "ai", "workspace", "attachment", "plugin", "plugins", "open", "run",
  "auth", "oauth", "callback", "mincode", "app", "cloud", "license", "update",
]);

const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const HANDLER_ID_PATTERN = /^[a-z][a-z0-9-]{0,31}$/;
const LITERAL_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const PARAM_PATTERN = /^\{([a-z][a-zA-Z0-9]{0,31})\}$/;
const TARGET_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
// eslint-disable-next-line no-control-regex -- rejecting control characters is the point
const UNSAFE_TEXT_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/;

export interface PluginUriHandler {
  pluginId: string;
  pluginName: string;
  id: string;
  path: string;
  title?: string;
  titleLocalized?: Record<string, string>;
  target: { type: "view"; view: string } | { type: "command"; command: string };
}

export interface UriHandlerIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  handler?: string;
  message: string;
}

/** Validated `plugin_host_uri_resolve` response. */
export interface ResolvedPluginLink {
  pluginId: string;
  pluginName: string;
  pluginVersion: string;
  handlerId: string;
  title?: string;
  titleLocalized?: Record<string, string>;
  target:
    | { type: "view"; view: string; viewTitle: string }
    | { type: "command"; command: string; commandTitle: string };
  path: string;
  pathParams: Record<string, string>;
  params: Record<string, string | number | boolean>;
  payload?: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function safeLine(value: unknown, maxRunes: number): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  if (!trimmed || [...trimmed].length > maxRunes || UNSAFE_TEXT_PATTERN.test(trimmed)) return undefined;
  return trimmed;
}

function localized(value: unknown, maxRunes: number): Record<string, string> | undefined {
  if (!isRecord(value)) return undefined;
  const out: Record<string, string> = {};
  for (const [locale, title] of Object.entries(value).slice(0, 16)) {
    const line = safeLine(title, maxRunes);
    if (line && /^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8}){0,3}$/.test(locale)) out[locale] = line;
  }
  return Object.keys(out).length ? out : undefined;
}

/** Validates a handler path shape: /seg/{param}/..., 1-8 segments, unique params. */
export function parseUriHandlerPath(path: unknown): { literal?: string; param?: string }[] | undefined {
  if (typeof path !== "string" || path.length < 2 || path.length > URI_HANDLER_LIMITS.pathMaxLength || !path.startsWith("/")) return undefined;
  const parts = path.slice(1).split("/");
  if (parts.length > URI_HANDLER_LIMITS.pathSegmentsMax) return undefined;
  const params = new Set<string>();
  const segments: { literal?: string; param?: string }[] = [];
  for (const part of parts) {
    const param = PARAM_PATTERN.exec(part)?.[1];
    if (param) {
      if (params.has(param)) return undefined;
      params.add(param);
      segments.push({ param });
    } else if (LITERAL_PATTERN.test(part)) {
      segments.push({ literal: part });
    } else {
      return undefined;
    }
  }
  return segments;
}

/** Re-validates one host-accepted entry (defense in depth). */
export function validateUriHandlerEntry(raw: unknown): { ok: true; handler: Omit<PluginUriHandler, "pluginId" | "pluginName"> } | { ok: false; error: string } {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!HANDLER_ID_PATTERN.test(id)) return { ok: false, error: `link handler id "${id}" is invalid` };
  const segments = parseUriHandlerPath(raw.path);
  if (!segments) return { ok: false, error: `link handler "${id}" path is invalid` };
  const title = raw.title === undefined ? undefined : safeLine(raw.title, URI_HANDLER_LIMITS.titleMaxRunes);
  if (raw.title !== undefined && raw.title !== "" && !title) return { ok: false, error: `link handler "${id}" title is invalid` };
  const target = isRecord(raw.target) ? raw.target : undefined;
  let handlerTarget: PluginUriHandler["target"];
  if (target?.type === "view" && typeof target.view === "string" && TARGET_ID_PATTERN.test(target.view)) {
    handlerTarget = { type: "view", view: target.view };
  } else if (target?.type === "command" && typeof target.command === "string" && TARGET_ID_PATTERN.test(target.command)) {
    if (segments.some((segment) => segment.param)) return { ok: false, error: `link handler "${id}" command path must not contain parameters` };
    handlerTarget = { type: "command", command: target.command };
  } else {
    return { ok: false, error: `link handler "${id}" target is invalid` };
  }
  const titleLocalized = localized(raw.titleLocalized, URI_HANDLER_LIMITS.titleMaxRunes);
  return {
    ok: true,
    handler: { id, path: raw.path as string, ...(title ? { title } : {}), ...(titleLocalized ? { titleLocalized } : {}), target: handlerTarget },
  };
}

/** Link handlers of the active process plugins (reserved plugin ids never handle links). */
export function collectUriHandlers(contributions: readonly PointContribution[]): { handlers: PluginUriHandler[]; issues: UriHandlerIssue[] } {
  const handlers: PluginUriHandler[] = [];
  const issues: UriHandlerIssue[] = [];
  const ordered = [...contributions].sort((a, b) => (a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0));
  for (const contribution of ordered) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { handler: issue.id } : {}) });
    }
    if (URI_HANDLER_RESERVED_HOSTS.has(contribution.pluginId)) {
      if (contribution.point.entries.length) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry: -1, message: `plugin id "${contribution.pluginId}" is a reserved mincode:// host` });
      }
      continue;
    }
    const seen = new Set<string>();
    contribution.point.entries.slice(0, URI_HANDLER_LIMITS.maxEntries).forEach((raw, entry) => {
      const result = validateUriHandlerEntry(raw);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      if (seen.has(result.handler.id)) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, handler: result.handler.id, message: `duplicate link handler id "${result.handler.id}"` });
        return;
      }
      seen.add(result.handler.id);
      handlers.push({ ...result.handler, pluginId: contribution.pluginId, pluginName: contribution.pluginName });
    });
  }
  return { handlers, issues };
}

/** The plugin id a mincode:// link addresses, or undefined when it is not a plugin link. */
export function pluginLinkPluginId(url: string): string | undefined {
  const match = /^mincode:\/\/([^/?#]+)\//i.exec(url);
  const host = match?.[1]?.toLowerCase();
  if (!host || !PLUGIN_ID_PATTERN.test(host) || URI_HANDLER_RESERVED_HOSTS.has(host)) return undefined;
  return host;
}

/** Whether a string can be a plugin link at all (cheap check before calling the host). */
export function isPluginLinkCandidate(url: unknown): url is string {
  return typeof url === "string" && url.length > 0 && url.length <= URI_HANDLER_LIMITS.urlMaxLength && !UNSAFE_TEXT_PATTERN.test(url) && pluginLinkPluginId(url) !== undefined;
}

function stringRecord(value: unknown, maxRunes: number): Record<string, string> | undefined {
  if (value === undefined || value === null) return {};
  if (!isRecord(value)) return undefined;
  const out: Record<string, string> = {};
  for (const [key, item] of Object.entries(value)) {
    if (typeof item !== "string" || [...item].length > maxRunes || UNSAFE_TEXT_PATTERN.test(item)) return undefined;
    out[key] = item;
  }
  return out;
}

function scalarRecord(value: unknown): Record<string, string | number | boolean> | undefined {
  if (value === undefined || value === null) return {};
  if (!isRecord(value)) return undefined;
  const entries = Object.entries(value);
  if (entries.length > URI_HANDLER_LIMITS.queryPropertiesMax) return undefined;
  const out: Record<string, string | number | boolean> = {};
  for (const [key, item] of entries) {
    if (typeof item === "string") {
      if ([...item].length > URI_HANDLER_LIMITS.queryValueMaxRunes || UNSAFE_TEXT_PATTERN.test(item)) return undefined;
      out[key] = item;
    } else if (typeof item === "boolean" || (typeof item === "number" && Number.isFinite(item))) {
      out[key] = item;
    } else {
      return undefined;
    }
  }
  return out;
}

/** Validates a `plugin_host_uri_resolve` response; undefined when malformed. */
export function parseResolvedPluginLink(value: unknown): ResolvedPluginLink | undefined {
  if (!isRecord(value)) return undefined;
  const pluginId = typeof value.pluginId === "string" && PLUGIN_ID_PATTERN.test(value.pluginId) ? value.pluginId : "";
  const handlerId = typeof value.handlerId === "string" && HANDLER_ID_PATTERN.test(value.handlerId) ? value.handlerId : "";
  const path = typeof value.path === "string" && value.path.startsWith("/") && value.path.length <= URI_HANDLER_LIMITS.urlMaxLength && !UNSAFE_TEXT_PATTERN.test(value.path) ? value.path : "";
  const pathParams = stringRecord(value.pathParams, URI_HANDLER_LIMITS.pathParamMaxLength);
  const params = scalarRecord(value.params);
  if (!pluginId || !handlerId || !path || !pathParams || !params || !isRecord(value.target)) return undefined;
  const raw = value.target;
  let target: ResolvedPluginLink["target"];
  if (raw.type === "view" && typeof raw.view === "string" && TARGET_ID_PATTERN.test(raw.view)) {
    target = { type: "view", view: raw.view, viewTitle: safeLine(raw.viewTitle, 128) ?? raw.view };
  } else if (raw.type === "command" && typeof raw.command === "string" && TARGET_ID_PATTERN.test(raw.command)) {
    target = { type: "command", command: raw.command, commandTitle: safeLine(raw.commandTitle, 128) ?? raw.command };
  } else {
    return undefined;
  }
  if (value.payload !== undefined && (typeof value.payload !== "string" || value.payload.length > 65536)) return undefined;
  const title = safeLine(value.title, URI_HANDLER_LIMITS.titleMaxRunes);
  const titleLocalized = localized(value.titleLocalized, URI_HANDLER_LIMITS.titleMaxRunes);
  return {
    pluginId,
    pluginName: safeLine(value.pluginName, 128) ?? pluginId,
    pluginVersion: safeLine(value.pluginVersion, 64) ?? "",
    handlerId,
    ...(title ? { title } : {}),
    ...(titleLocalized ? { titleLocalized } : {}),
    target,
    path,
    pathParams,
    params,
    ...(typeof value.payload === "string" && value.payload ? { payload: value.payload } : {}),
  };
}

/**
 * Whether a second resolve still describes what the user confirmed (same
 * plugin version, handler, target and parameters). Titles may change.
 */
export function sameResolvedPluginLink(left: ResolvedPluginLink, right: ResolvedPluginLink): boolean {
  const canonical = (link: ResolvedPluginLink) =>
    JSON.stringify([
      link.pluginId,
      link.pluginVersion,
      link.handlerId,
      link.target.type,
      link.target.type === "view" ? link.target.view : link.target.command,
      link.path,
      Object.entries(link.pathParams).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)),
      Object.entries(link.params).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)),
      link.payload ?? "",
    ]);
  return canonical(left) === canonical(right);
}

export type PluginLinkOffer = "accepted" | "duplicate" | "overflow";

export interface PluginLinkGate {
  /** Offers a link: queued, ignored as a repeat within the window, or dropped because the queue is full. */
  offer(url: string): PluginLinkOffer;
  /** Takes the next queued link, or undefined when none waits. */
  take(): string | undefined;
  /** Number of links waiting (not counting the one being shown). */
  readonly waiting: number;
}

/**
 * Bounds incoming links: an identical link within `duplicateWindowMs` of the
 * previous identical one is ignored, and at most `pendingMax` links wait
 * behind the dialog on screen.
 */
export function createPluginLinkGate(options: {
  now?: () => number;
  duplicateWindowMs?: number;
  pendingMax?: number;
} = {}): PluginLinkGate {
  const now = options.now ?? (() => Date.now());
  const window = options.duplicateWindowMs ?? URI_HANDLER_LIMITS.duplicateWindowMs;
  const pendingMax = options.pendingMax ?? URI_HANDLER_LIMITS.pendingLinksMax;
  const lastSeen = new Map<string, number>();
  const queue: string[] = [];
  return {
    offer(url) {
      const at = now();
      for (const [key, seenAt] of lastSeen) if (at - seenAt >= window) lastSeen.delete(key);
      const previous = lastSeen.get(url);
      if (previous !== undefined && at - previous < window) return "duplicate";
      if (queue.length >= pendingMax) return "overflow";
      lastSeen.set(url, at);
      queue.push(url);
      return "accepted";
    },
    take() {
      return queue.shift();
    },
    get waiting() {
      return queue.length;
    },
  };
}

/** Install-preview summary: the link paths a manifest would handle. */
export function uriHandlerPreviewSummary(contributes: unknown): { path: string; target: string }[] {
  const entries = isRecord(contributes) ? contributes[URI_HANDLERS_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, URI_HANDLER_LIMITS.maxEntries).flatMap((entry) => {
    if (!isRecord(entry) || !parseUriHandlerPath(entry.path) || !isRecord(entry.target)) return [];
    const target = entry.target.type === "view" && typeof entry.target.view === "string"
      ? `view ${entry.target.view}`
      : entry.target.type === "command" && typeof entry.target.command === "string"
        ? `command ${entry.target.command}`
        : "";
    return target ? [{ path: entry.path as string, target }] : [];
  });
}
