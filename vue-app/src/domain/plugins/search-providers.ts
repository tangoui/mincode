/**
 * Frontend stage of the `searchProviders` contribution point: re-validation
 * of provider entries the Go host already accepted
 * (backend/plugin/contrib_search_providers.go), the prefix registry
 * (a prefix belongs to the lowest plugin id that claims it), palette query
 * parsing (`<prefix>:<query>`) and re-validation of search results returned
 * by `plugin_host_search` (search_provider_invoke.go). No DOM, Vue or IPC
 * here; the runtime wiring lives in services/contributions/search-providers.ts,
 * stores/plugin-search-providers.ts, services/plugin-search-service.ts and
 * composables/use-command-palette.ts.
 *
 * Results are plain text. Targets are limited to the plugin's own view, one
 * of its own read action commands, an http(s) URL that is opened in the
 * system browser only when the user activates the row or, only when the host
 * stamped the response with its fileScope and providerRuntime (negotiated
 * `searchFileTargets`), a project-relative file position. A file row opens
 * only after services/plugin-search-file-targets.ts re-validated it against
 * the same scope and runtime in Go.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolvePluginTitle } from "@/domain/plugins/title";

export const SEARCH_PROVIDERS_CONTRIBUTION_POINT = "searchProviders";

/** Mirrors contribution-points.json searchProviders limits (the host enforces them first). */
export const SEARCH_PROVIDER_LIMITS = {
  maxEntries: 4,
  titleMaxRunes: 48,
  localesMax: 16,
  placeholderMaxRunes: 64,
  queryMaxRunes: 256,
  minQueryLengthMax: 8,
  itemsMax: 50,
  itemIdMaxLength: 64,
  itemTitleMaxRunes: 128,
  itemDetailMaxRunes: 256,
  argsMaxBytes: 1024,
  argsMaxProperties: 8,
  urlMaxLength: 2048,
  pathMaxLength: 1024,
  lineMax: 10_000_000,
  columnMax: 10_000_000,
  timeoutMs: 5000,
  debounceMs: 250,
} as const;

/** Mirrors contribution-points.json searchProviders lists.reservedPrefixes. */
export const SEARCH_PROVIDER_RESERVED_PREFIXES: ReadonlySet<string> = new Set([
  "http", "https", "ws", "wss", "ftp", "file", "files", "content", "data", "blob", "about", "javascript", "mailto",
  "cmd", "command", "commands", "view", "views", "tab", "tabs", "project", "projects", "workspace", "ssh", "sftp",
  "db", "database", "sql", "redis", "git", "terminal", "term", "ai", "mcp", "mincode", "plugin", "plugins",
  "settings", "help", "go", "open", "run",
]);

export const SEARCH_PROVIDER_PREFIX_PATTERN = /^[a-z][a-z0-9-]{1,15}$/;
const PROVIDER_ID_PATTERN = /^[a-z][a-z0-9-]{0,31}$/;
const PLUGIN_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const ACTION_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const ITEM_ID_PATTERN = /^[A-Za-z0-9._:-]{1,64}$/;
const VIEW_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const COMMAND_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
// eslint-disable-next-line no-control-regex -- rejecting control characters is the point
const UNSAFE_TEXT_PATTERN = /[\u0000-\u001f\u007f-\u009f\u2028\u2029\u202a-\u202e\u2066-\u2069\u200b-\u200f\ufeff]/;

export interface PluginSearchProvider {
  pluginId: string;
  pluginName: string;
  id: string;
  prefix: string;
  title: string;
  titleLocalized?: Record<string, string>;
  placeholder?: string;
  minQueryLength: number;
}

export interface SearchProviderIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  provider?: string;
  message: string;
}

/** A clean project-relative file position (1-based; a column needs a line). */
export interface SearchFileTarget {
  type: "file";
  path: string;
  line?: number;
  column?: number;
}

/** Host-filled workspace scope a file result belongs to (never plugin-written). */
export interface SearchFileScope {
  workspaceId: string;
  root: string;
  /** Decimal string, so it survives JS number precision. */
  revision: string;
}

/** Host-filled runtime instance and process generation that answered the query. */
export interface SearchProviderRuntime {
  instance: string;
  generation: string;
}

/** Everything a click needs to re-validate and open one file result. */
export interface SearchFileActivation {
  pluginId: string;
  providerId: string;
  fileScope: SearchFileScope;
  providerRuntime: SearchProviderRuntime;
  target: SearchFileTarget;
}

/** The published scope shape (services/plugin-workspace-scope.ts) compared against. */
export interface SearchFileCurrentScope extends SearchFileScope {
  ready: boolean;
}

export type SearchResultTarget =
  | { type: "view"; view: string; args?: Record<string, string | number | boolean> }
  | { type: "command"; command: string; args?: Record<string, string | number | boolean> }
  | { type: "url"; url: string }
  | SearchFileTarget;

export interface SearchResultItem {
  id: string;
  title: string;
  detail?: string;
  target: SearchResultTarget;
}

export interface SearchResultResponse {
  items: SearchResultItem[];
  dropped: number;
  truncated: boolean;
  /** Present (together with providerRuntime) only for a file-enabled query. */
  fileScope?: SearchFileScope;
  providerRuntime?: SearchProviderRuntime;
}

export interface ParsedProviderQuery {
  provider: PluginSearchProvider;
  /** The text after `<prefix>:`, trimmed. */
  query: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function runeCount(value: string) {
  return [...value].length;
}

function boundedLine(value: unknown, maxRunes: number, required: boolean): string | undefined | false {
  if (value === undefined && !required) return undefined;
  if (typeof value !== "string") return false;
  const trimmed = value.trim();
  if (!trimmed) return required ? false : undefined;
  if (runeCount(trimmed) > maxRunes || UNSAFE_TEXT_PATTERN.test(trimmed)) return false;
  return trimmed;
}

function localizedTitles(value: unknown): Record<string, string> | undefined | false {
  if (value === undefined) return undefined;
  if (!isRecord(value)) return false;
  const entries = Object.entries(value);
  if (entries.length > SEARCH_PROVIDER_LIMITS.localesMax) return false;
  const out: Record<string, string> = {};
  for (const [locale, title] of entries) {
    const line = boundedLine(title, SEARCH_PROVIDER_LIMITS.titleMaxRunes, true);
    if (!line || !/^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8}){0,3}$/.test(locale)) return false;
    out[locale] = line;
  }
  return entries.length ? out : undefined;
}

/** Re-validates one host-accepted entry (defense in depth). */
export function validateSearchProviderEntry(
  raw: unknown,
): { ok: true; provider: Omit<PluginSearchProvider, "pluginId" | "pluginName"> & { action: string } } | { ok: false; error: string } {
  if (!isRecord(raw)) return { ok: false, error: "entry must be an object" };
  const id = typeof raw.id === "string" ? raw.id : "";
  if (!PROVIDER_ID_PATTERN.test(id)) return { ok: false, error: `search provider id "${id}" is invalid` };
  const prefix = typeof raw.prefix === "string" ? raw.prefix : "";
  if (!SEARCH_PROVIDER_PREFIX_PATTERN.test(prefix)) return { ok: false, error: `prefix "${prefix}" is invalid` };
  if (SEARCH_PROVIDER_RESERVED_PREFIXES.has(prefix)) return { ok: false, error: `prefix "${prefix}" is reserved` };
  const title = boundedLine(raw.title, SEARCH_PROVIDER_LIMITS.titleMaxRunes, true);
  if (!title) return { ok: false, error: "title is invalid" };
  const titleLocalized = localizedTitles(raw.titleLocalized);
  if (titleLocalized === false) return { ok: false, error: "titleLocalized is invalid" };
  const placeholder = boundedLine(raw.placeholder, SEARCH_PROVIDER_LIMITS.placeholderMaxRunes, false);
  if (placeholder === false) return { ok: false, error: "placeholder is invalid" };
  const minQueryLength = raw.minQueryLength === undefined ? 1 : raw.minQueryLength;
  if (typeof minQueryLength !== "number" || !Number.isInteger(minQueryLength) || minQueryLength < 0 || minQueryLength > SEARCH_PROVIDER_LIMITS.minQueryLengthMax) {
    return { ok: false, error: "minQueryLength is invalid" };
  }
  const action = typeof raw.action === "string" ? raw.action : "";
  if (!ACTION_ID_PATTERN.test(action)) return { ok: false, error: "action is invalid" };
  return {
    ok: true,
    provider: {
      id,
      prefix,
      title,
      ...(titleLocalized ? { titleLocalized } : {}),
      ...(placeholder ? { placeholder } : {}),
      minQueryLength,
      action,
    },
  };
}

/**
 * Builds the prefix registry from the active process plugins. Plugins are
 * visited in ascending plugin id order, so on a prefix conflict between
 * plugins the lowest plugin id wins and the others are reported as issues.
 */
export function collectSearchProviders(contributions: readonly PointContribution[]): {
  providers: PluginSearchProvider[];
  issues: SearchProviderIssue[];
} {
  const providers: PluginSearchProvider[] = [];
  const issues: SearchProviderIssue[] = [];
  const owners = new Map<string, string>();
  const ordered = [...contributions].sort((a, b) => (a.pluginId < b.pluginId ? -1 : a.pluginId > b.pluginId ? 1 : 0));
  for (const contribution of ordered) {
    if (contribution.runtime !== "process" || !contribution.activeInWorkspace || !PLUGIN_ID_PATTERN.test(contribution.pluginId)) continue;
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { provider: issue.id } : {}) });
    }
    const seen = new Set<string>();
    contribution.point.entries.slice(0, SEARCH_PROVIDER_LIMITS.maxEntries).forEach((raw, entry) => {
      const result = validateSearchProviderEntry(raw);
      if (!result.ok) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message: result.error });
        return;
      }
      const { action: _action, ...provider } = result.provider;
      void _action;
      if (seen.has(provider.id)) {
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, provider: provider.id, message: `duplicate search provider id "${provider.id}"` });
        return;
      }
      seen.add(provider.id);
      const owner = owners.get(provider.prefix);
      if (owner) {
        issues.push({
          pluginId: contribution.pluginId,
          stage: "frontend",
          entry,
          provider: provider.id,
          message: `prefix "${provider.prefix}" is already used by plugin "${owner}"`,
        });
        return;
      }
      owners.set(provider.prefix, contribution.pluginId);
      providers.push({ ...provider, pluginId: contribution.pluginId, pluginName: contribution.pluginName });
    });
  }
  return { providers, issues };
}

/** The provider's title for a locale chain (plugin translations only). */
export function searchProviderTitle(provider: PluginSearchProvider, locale: string | readonly string[]): string {
  return resolvePluginTitle(provider, locale);
}

/**
 * Parses `<prefix>:<query>` typed in the palette's "all" scope. Only
 * registered prefixes match, so other text keeps its normal meaning.
 */
export function parseProviderQuery(value: string, providers: readonly PluginSearchProvider[]): ParsedProviderQuery | undefined {
  const trimmed = value.trimStart();
  const match = /^([a-z][a-z0-9-]{1,15}):/.exec(trimmed);
  if (!match) return undefined;
  const provider = providers.find((candidate) => candidate.prefix === match[1]);
  if (!provider) return undefined;
  const query = trimmed.slice(match[0].length).trim();
  return { provider, query: [...query].slice(0, SEARCH_PROVIDER_LIMITS.queryMaxRunes).join("") };
}

/** Whether the query may be sent (single line, long enough). */
export function providerQueryReady(parsed: ParsedProviderQuery): boolean {
  return runeCount(parsed.query) >= parsed.provider.minQueryLength && !UNSAFE_TEXT_PATTERN.test(parsed.query);
}

function scalarArgs(value: unknown): Record<string, string | number | boolean> | undefined | false {
  if (value === undefined) return undefined;
  if (!isRecord(value)) return false;
  const entries = Object.entries(value);
  if (entries.length > SEARCH_PROVIDER_LIMITS.argsMaxProperties) return false;
  const out: Record<string, string | number | boolean> = {};
  for (const [key, item] of entries) {
    if (!key || key.length > 64) return false;
    if (typeof item === "string" || typeof item === "boolean" || (typeof item === "number" && Number.isFinite(item))) out[key] = item;
    else return false;
  }
  if (new TextEncoder().encode(JSON.stringify(out)).byteLength > SEARCH_PROVIDER_LIMITS.argsMaxBytes) return false;
  return entries.length ? out : undefined;
}

/**
 * Validates an http(s) URL the same way the host did: no credentials, no
 * whitespace or backslashes, a host, at most urlMaxLength bytes. Returns the
 * normalized URL or undefined.
 */
export function normalizeSearchResultURL(value: unknown): string | undefined {
  if (typeof value !== "string" || !value || new TextEncoder().encode(value).byteLength > SEARCH_PROVIDER_LIMITS.urlMaxLength) return undefined;
  if (/[\s\\]/.test(value) || UNSAFE_TEXT_PATTERN.test(value) || !/^https?:\/\//i.test(value)) return undefined;
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return undefined;
  }
  if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || !parsed.hostname || parsed.username || parsed.password) return undefined;
  return parsed.href;
}

/** The URL host shown as the row subtitle so the user sees where it goes. */
export function searchResultURLHost(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return "";
  }
}

// Mirrors the host's path rules (validateContributionRelativePath and the
// language-location character set); the host stays authoritative.
// eslint-disable-next-line no-control-regex -- NUL is one of the refused characters
const FILE_PATH_REFUSED_PATTERN = /[\\:%?#\u0000．｡․。﹒／∕⁄⧸＼∖⧹﹨]/;
const LONE_SURROGATE_PATTERN = /[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/;
const FILE_TARGET_KEYS: ReadonlySet<string> = new Set(["type", "path", "line", "column"]);
const DECIMAL_ID_PATTERN = /^[1-9][0-9]{0,19}$/;

function boundedPosition(value: unknown, max: number): number | undefined | false {
  if (value === undefined) return undefined;
  return typeof value === "number" && Number.isInteger(value) && value >= 1 && value <= max ? value : false;
}

/**
 * Validates a `{type:"file", path, line?, column?}` target: a clean
 * slash-separated project-relative path of at most pathMaxLength UTF-8 bytes
 * and 1-based integer positions; a column needs a line. Returns undefined
 * for anything else, including extra fields.
 */
export function normalizeSearchFileTarget(value: unknown): SearchFileTarget | undefined {
  if (!isRecord(value) || value.type !== "file" || Object.keys(value).some((key) => !FILE_TARGET_KEYS.has(key))) return undefined;
  const path = value.path;
  if (typeof path !== "string" || !path || LONE_SURROGATE_PATTERN.test(path)
    || new TextEncoder().encode(path).byteLength > SEARCH_PROVIDER_LIMITS.pathMaxLength
    || UNSAFE_TEXT_PATTERN.test(path) || FILE_PATH_REFUSED_PATTERN.test(path) || /^[~$]/.test(path)
    || path.split("/").some((segment) => !segment || segment === "." || segment === "..")) {
    return undefined;
  }
  const line = boundedPosition(value.line, SEARCH_PROVIDER_LIMITS.lineMax);
  const column = boundedPosition(value.column, SEARCH_PROVIDER_LIMITS.columnMax);
  if (line === false || column === false || (column !== undefined && line === undefined)) return undefined;
  return { type: "file", path, ...(line !== undefined ? { line } : {}), ...(column !== undefined ? { column } : {}) };
}

function exactStringRecord(value: unknown, keys: readonly string[]): Record<string, string> | undefined {
  if (!isRecord(value) || Object.keys(value).length !== keys.length) return undefined;
  const out: Record<string, string> = {};
  for (const key of keys) {
    const item = value[key];
    if (typeof item !== "string") return undefined;
    out[key] = item;
  }
  return out;
}

/** The host-filled fileScope of a response, or undefined when absent or malformed. */
export function parseSearchFileScope(value: unknown): SearchFileScope | undefined {
  const record = exactStringRecord(value, ["workspaceId", "root", "revision"]);
  if (!record || !record.root || !DECIMAL_ID_PATTERN.test(record.revision)) return undefined;
  return { workspaceId: record.workspaceId, root: record.root, revision: record.revision };
}

/** The host-filled providerRuntime of a response, or undefined when absent or malformed. */
export function parseSearchProviderRuntime(value: unknown): SearchProviderRuntime | undefined {
  const record = exactStringRecord(value, ["instance", "generation"]);
  if (!record || !DECIMAL_ID_PATTERN.test(record.instance) || !DECIMAL_ID_PATTERN.test(record.generation)) return undefined;
  return { instance: record.instance, generation: record.generation };
}

/**
 * Whether a file result's scope is still the published one: same ready
 * workspace, canonical root and revision. Switching away and back changes
 * the revision, so older results never match again.
 */
export function searchFileScopeIsCurrent(scope: SearchFileScope, current: SearchFileCurrentScope | undefined): boolean {
  return Boolean(current?.ready) && current?.workspaceId === scope.workspaceId
    && current.root === scope.root && current.revision === scope.revision;
}

function parseTarget(value: unknown, fileEnabled: boolean): SearchResultTarget | undefined {
  if (!isRecord(value)) return undefined;
  if (value.type === "file") return fileEnabled ? normalizeSearchFileTarget(value) : undefined;
  const args = scalarArgs(value.args);
  if (args === false) return undefined;
  switch (value.type) {
    case "view":
      if (typeof value.view !== "string" || !VIEW_ID_PATTERN.test(value.view) || value.command !== undefined || value.url !== undefined) return undefined;
      return { type: "view", view: value.view, ...(args ? { args } : {}) };
    case "command":
      if (typeof value.command !== "string" || !COMMAND_ID_PATTERN.test(value.command) || value.view !== undefined || value.url !== undefined) return undefined;
      return { type: "command", command: value.command, ...(args ? { args } : {}) };
    case "url": {
      if (value.view !== undefined || value.command !== undefined || args) return undefined;
      const url = normalizeSearchResultURL(value.url);
      return url ? { type: "url", url } : undefined;
    }
    default:
      return undefined;
  }
}

/**
 * Re-validates a `plugin_host_search` response; invalid items are dropped and
 * counted. File items are kept only when the host stamped both fileScope and
 * providerRuntime; otherwise they are dropped like any invalid item.
 */
export function parseSearchResultResponse(value: unknown): SearchResultResponse {
  const response: SearchResultResponse = { items: [], dropped: 0, truncated: false };
  if (!isRecord(value) || !Array.isArray(value.items)) return response;
  const fileScope = parseSearchFileScope(value.fileScope);
  const providerRuntime = parseSearchProviderRuntime(value.providerRuntime);
  const fileEnabled = Boolean(fileScope && providerRuntime);
  if (fileScope && providerRuntime) {
    response.fileScope = fileScope;
    response.providerRuntime = providerRuntime;
  }
  response.dropped = typeof value.dropped === "number" && Number.isSafeInteger(value.dropped) && value.dropped > 0 ? value.dropped : 0;
  response.truncated = value.truncated === true || value.items.length > SEARCH_PROVIDER_LIMITS.itemsMax;
  const seen = new Set<string>();
  for (const raw of value.items.slice(0, SEARCH_PROVIDER_LIMITS.itemsMax)) {
    const item = isRecord(raw) ? raw : undefined;
    const id = typeof item?.id === "string" && ITEM_ID_PATTERN.test(item.id) ? item.id : "";
    const title = boundedLine(item?.title, SEARCH_PROVIDER_LIMITS.itemTitleMaxRunes, true);
    const detail = boundedLine(item?.detail, SEARCH_PROVIDER_LIMITS.itemDetailMaxRunes, false);
    const target = parseTarget(item?.target, fileEnabled);
    if (!id || !title || detail === false || !target || seen.has(id)) {
      response.dropped += 1;
      continue;
    }
    seen.add(id);
    response.items.push({ id, title, ...(detail ? { detail } : {}), target });
  }
  return response;
}

/** Install-preview summary: the prefixes a manifest would claim. */
export function searchProviderPreviewSummary(contributes: unknown): { prefix: string; title: string }[] {
  const entries = isRecord(contributes) ? contributes[SEARCH_PROVIDERS_CONTRIBUTION_POINT] : undefined;
  if (!Array.isArray(entries)) return [];
  return entries.slice(0, SEARCH_PROVIDER_LIMITS.maxEntries).flatMap((entry) => {
    if (!isRecord(entry) || typeof entry.prefix !== "string" || !SEARCH_PROVIDER_PREFIX_PATTERN.test(entry.prefix)) return [];
    const title = typeof entry.title === "string" ? entry.title.trim().slice(0, SEARCH_PROVIDER_LIMITS.titleMaxRunes) : "";
    return [{ prefix: entry.prefix, title: title || entry.prefix }];
  });
}
