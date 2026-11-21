/**
 * The newer service surfaces (design "plugin services surfaces"): SFTP remote
 * files, database result sets, HTTP responses and terminal selections. Hosts
 * describe what their menu was opened on as a ServiceSurfaceTarget; which
 * services to offer is servicesForSurface (domain/plugins/services.ts), and
 * surfaceRunRequest builds the service request only when the user picks one,
 * so nothing is read, serialized or filtered while a menu is merely open.
 *
 * What a request carries (Go: service_commands.go checkHostServiceSources and
 * service_surface_context.go gateSurfaceContext, which decode strictly):
 * - sftpFile: `remoteFile {sessionId, path}` (the host reads the file through
 *   the session the user has open; no credential ever crosses) and
 *   `surfaceContext {remote}` or `{remoteOmitted: true}` for a path the
 *   context cannot hold;
 * - dbResult: `content {text}` with result.json (buildDbResultFile) and,
 *   when the grid knows its source, `surfaceContext {database}`;
 * - httpResponse: `content {text, contentType}` with the body as received and
 *   `surfaceContext {response, request?}`: headers filtered by
 *   redactResponseHeaders, the request URL by sanitizeMenuRequestUrl;
 * - terminalSelection: `input` (normalizeTerminalServiceText) and
 *   `surfaceContext {terminal: {kind}}`.
 * The host drops remote / request / database again for providers without the
 * matching capability; text the host would refuse (control and format
 * characters, oversize fields) is cleaned or left out here so that one odd
 * character never fails a whole call.
 *
 * Pure: nothing here touches IPC, Vue or the DOM. Only lazily loaded
 * components import it (it pulls in menu-targets.ts).
 */
import { hasCredentialShape, isSensitiveHeader } from "@/domain/http/code-generator-model";
import {
  menuTargetPassContext,
  sanitizeMenuRequestUrl,
  type DatabaseTreeMenuTarget,
  type SftpFileMenuTarget,
} from "@/domain/plugins/menu-targets";
import { SERVICE_LIMITS, normalizeTerminalServiceText, utf8ByteLength, type PluginServiceEntry, type ServiceGroup } from "@/domain/plugins/services";
import type { PluginServiceProviderRef, PluginServiceRunRequest } from "@/services/plugin-services";

export {
  httpResponseFileName,
  isTextResponseType,
  normalizeTerminalServiceText,
  servicesForSurface,
  type ServiceDisabledReason,
  type ServiceSurfaceGroup,
} from "@/domain/plugins/services";

export type FileMenuServiceTarget = { surface: "fileMenu"; files: { name: string; absolutePath: string }[] };

export type SftpFileServiceTarget = {
  surface: "sftpFile";
  /** The SFTP / FTP session the view has open. */
  sessionId: string;
  /** Size as listed; the host reads at most what it finds when it opens the file. */
  file: { name: string; size: number };
  remote: SftpFileMenuTarget;
};

export type DbResultServiceTarget = {
  surface: "dbResult";
  /** Visible columns in display order; `type` null when the grid does not know it. */
  columns: { name: string; type: string | null }[];
  /** Rows as shown (current page, sort and filter applied), cells in `columns` order; read only at click time. */
  rows: () => unknown[][];
  /** The grid's display text of a cell, used for values JSON cannot hold. */
  formatCell: (value: unknown) => string;
  /** More rows exist beyond this page. */
  hasMore: boolean;
  running: boolean;
  pendingChanges: boolean;
  view: { filtered: boolean; sorted: boolean; hiddenColumns: number };
  database?: DatabaseTreeMenuTarget;
};

export type HttpResponseServiceTarget = {
  surface: "httpResponse";
  response: { status: number; statusText: string; contentType: string; headers: [string, string][]; body: string; sizeBytes: number; elapsedMs: number };
  /** The request's credential values (auth, API key, OAuth, sensitive request headers): response headers echoing one are withheld. */
  secrets: string[];
  /**
   * The request this response answered; `url` is the address as sent and is
   * sanitized before it leaves the host. Absent when the host does not know
   * it: the context then has no `request`.
   */
  request?: { method: string; url: string };
};

export type TerminalSelectionServiceTarget = { surface: "terminalSelection"; kind: "local" | "ssh" | "docker"; selection: string };

/** What a host menu was opened on, for the services it offers. */
export type ServiceSurfaceTarget =
  | FileMenuServiceTarget
  | SftpFileServiceTarget
  | DbResultServiceTarget
  | HttpResponseServiceTarget
  | TerminalSelectionServiceTarget;

// ---------------------------------------------------------------------------
// Context text the host accepts (contextText in service_surface_context.go).

/** Field bound of the host's surface context strings (names, hosts, status text). */
const CONTEXT_FIELD_MAX_BYTES = 1024;
const CONTEXT_CONTENT_TYPE_MAX_BYTES = 256;
const CONTEXT_METHOD_MAX_BYTES = 32;
const HEADERS_REDACTED_MAX = 100000;
const ELAPSED_MS_MAX = 86400000;
/** Request credential values shorter than this are not looked for in response headers. */
const SECRET_MIN_LENGTH = 6;

/** Characters the host refuses in context text: control (Cc), format (Cf), U+2028 and U+2029. */
const REFUSED_TEXT = /[\p{Cc}\p{Cf}\u{2028}\u{2029}]/gu;
/** The same in a header value, where a tab is allowed. */
// eslint-disable-next-line no-control-regex -- removing control characters is the point
const REFUSED_HEADER_VALUE = /[\u0000-\u0008\u000a-\u001f\u007f-\u009f\p{Cf}\u{2028}\u{2029}]/gu;
const LONE_SURROGATE = /[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/g;

/** Cuts text to at most maxBytes of UTF-8 on a character boundary. */
function cutBytes(text: string, maxBytes: number): string {
  if (utf8ByteLength(text) <= maxBytes) return text;
  let result = "";
  let used = 0;
  for (const char of text) {
    const size = utf8ByteLength(char);
    if (used + size > maxBytes) break;
    result += char;
    used += size;
  }
  return result;
}

/** Text the host accepts in a context field: refused characters removed, cut to maxBytes. */
function contextText(value: unknown, maxBytes = CONTEXT_FIELD_MAX_BYTES): string {
  if (typeof value !== "string") return "";
  return cutBytes(value.replace(LONE_SURROGATE, "").replace(REFUSED_TEXT, ""), maxBytes);
}

/** Whether text is acceptable as is (a path or address is left out rather than altered). */
function cleanText(value: string, maxBytes: number): boolean {
  // search() ignores the global flag and lastIndex.
  return utf8ByteLength(value) <= maxBytes && value.search(LONE_SURROGATE) < 0 && value.search(REFUSED_TEXT) < 0;
}

function boundedInteger(value: number, min: number, max: number): number {
  return Number.isFinite(value) ? Math.min(max, Math.max(min, Math.trunc(value))) : min;
}

// ---------------------------------------------------------------------------
// Result sets.

function jsonCell(value: unknown, formatCell: (value: unknown) => string): unknown {
  if (value === undefined || value === null) return null;
  if (typeof value === "string" || typeof value === "boolean") return value;
  if (typeof value === "number") return Number.isFinite(value) ? value : formatCell(value);
  if (typeof value === "bigint") return value.toString();
  return formatCell(value);
}

/**
 * result.json (`mincode.dbResult` version 1) of what the grid shows: the
 * visible columns in display order and the loaded rows in grid order. Values
 * stay as the grid holds them (null, numbers, strings, booleans); undefined
 * becomes null, bigint a string, anything else the grid's formatCell text.
 * At most dbResultMaxRows rows and maxBytes bytes: rows past either are cut
 * whole and `truncated` is set. `hasMore` tells the plugin the result has
 * more pages than it got. When not even one row fits (or the columns alone
 * are over maxBytes) the result is `tooLarge` with no text: nothing is
 * handed over rather than an empty or over-limit file.
 */
export function buildDbResultFile(
  target: DbResultServiceTarget,
  maxBytes: number,
): { text: string; rowCount: number; truncated: boolean; tooLarge?: true } {
  const columns = target.columns.map((column) => ({ name: column.name, type: column.type ?? null }));
  const rows = target.rows();
  const head = `{"format":"mincode.dbResult","version":1,"columns":${JSON.stringify(columns)},"rows":[`;
  const tail = (rowCount: number, truncated: boolean) =>
    `],"rowCount":${rowCount},"truncated":${truncated},"hasMore":${target.hasMore},"view":${JSON.stringify({
      filtered: target.view.filtered,
      sorted: target.view.sorted,
      hiddenColumns: target.view.hiddenColumns,
    })}}`;
  const maxRows = SERVICE_LIMITS.dbResultMaxRows;
  // The largest tail any row count can need ("false" is the longer flag).
  const budget = maxBytes - utf8ByteLength(head) - utf8ByteLength(tail(maxRows, false));
  const parts: string[] = [];
  let used = 0;
  for (const row of budget > 0 ? rows : []) {
    if (parts.length >= maxRows) break;
    const cells = columns.map((_, index) => jsonCell(row[index], target.formatCell));
    const encoded = JSON.stringify(cells);
    const size = utf8ByteLength(encoded) + (parts.length ? 1 : 0);
    if (used + size > budget) break;
    parts.push(encoded);
    used += size;
  }
  if (rows.length > 0 && parts.length === 0) return { text: "", rowCount: 0, truncated: true, tooLarge: true };
  const truncated = parts.length < rows.length;
  return { text: head + parts.join(",") + tail(parts.length, truncated), rowCount: parts.length, truncated };
}

// ---------------------------------------------------------------------------
// HTTP response headers (design ruling 19 and 19a; Go filterHeaders).

/** Headers whose value is (or holds) a URL, whose query and fragment are removed. */
const URL_HEADERS = new Set(["location", "content-location", "link", "refresh", "origin", "referer"]);
/** Go's \s (RE2): ASCII space, tab, LF, FF, CR. */
const RE2_SPACE = "[\\t\\n\\f\\r ]";
const LINK_URL = /<([^>]*)>/g;
const REFRESH_URL = new RegExp(`(\\burl${RE2_SPACE}*=${RE2_SPACE}*)(['"]?)([^'"]*)(['"]?)${RE2_SPACE}*$`, "i");
const REFRESH_DELAY = new RegExp(`^${RE2_SPACE}*\\d+(?:\\.\\d+)?${RE2_SPACE}*$`);
const REFRESH_DELAY_PREFIX = new RegExp(`^${RE2_SPACE}*\\d+(?:\\.\\d+)?[\\t\\n\\f\\r ;,]*$`);
const SPECIAL_SCHEME_PREFIX = /^(?:https?|wss?|ftp|file):[/\\]*/i;
const OTHER_SCHEME_PREFIX = /^[A-Za-z][A-Za-z0-9+.-]*:\/\//;
const BAD_ESCAPE = /%(?![0-9A-Fa-f]{2})/;
// Characters Go's url.Parse refuses in a host (shouldEscape with encodeHost).
const BAD_HOST_CHAR = /[^A-Za-z0-9\-._~!$&'()*+,;=:[\]<>"%\u0080-￿]/;

/**
 * Whether an address carries userinfo in any WHATWG form; mirrors
 * requestURLHasUserinfo in service_surface_context.go (a detector only: the
 * request URL itself is cleaned by sanitizeMenuRequestUrl).
 */
export function urlHasUserinfo(raw: string): boolean {
  try {
    const parsed = new URL(raw);
    if (parsed.username || parsed.password) return true;
  } catch {
    // Not absolute: only the authority check below applies.
  }
  let rest = raw;
  const special = SPECIAL_SCHEME_PREFIX.exec(raw);
  if (special) rest = raw.slice(special[0].length);
  else if (OTHER_SCHEME_PREFIX.test(raw)) rest = raw.slice(raw.indexOf("://") + 3);
  else if (/^[/\\]{2}/.test(raw)) rest = raw.replace(/^[/\\]+/, "");
  else if (raw.startsWith("/")) return false;
  const end = rest.search(/[/?#\\]/);
  const authority = end >= 0 ? rest.slice(0, end) : rest;
  return authority.includes("@") || authority.toLowerCase().includes("%40");
}

/** An approximation of whether Go's url.Parse accepts an address (a header it cannot parse is dropped). */
function goURLParses(raw: string): boolean {
  // eslint-disable-next-line no-control-regex -- url.Parse refuses ASCII control characters
  if (/[\u0000-\u001f\u007f]/.test(raw) || raw.startsWith(":")) return false;
  const hash = raw.indexOf("#");
  if (hash >= 0 && BAD_ESCAPE.test(raw.slice(hash + 1))) return false;
  let rest = hash >= 0 ? raw.slice(0, hash) : raw;
  const query = rest.indexOf("?");
  if (query >= 0) rest = rest.slice(0, query);
  if (BAD_ESCAPE.test(rest)) return false;
  const scheme = /^[A-Za-z][A-Za-z0-9+.-]*:/.exec(rest);
  if (scheme) rest = rest.slice(scheme[0].length);
  else if (/^[^/]*:/.test(rest)) return false; // first path segment with a colon
  if (!rest.startsWith("//") || (!scheme && rest.startsWith("///"))) return true;
  const authorityEnd = rest.indexOf("/", 2);
  const authority = rest.slice(2, authorityEnd >= 0 ? authorityEnd : undefined);
  const host = authority.slice(authority.lastIndexOf("@") + 1);
  if (host.startsWith("[")) {
    const close = host.indexOf("]");
    if (close < 0) return false;
    const after = host.slice(close + 1);
    return after === "" || /^:\d*$/.test(after);
  }
  const colon = host.lastIndexOf(":");
  if (colon >= 0 && !/^\d*$/.test(host.slice(colon + 1))) return false;
  return !BAD_HOST_CHAR.test(colon >= 0 ? host.slice(0, colon) : host);
}

/** One URL in a header value without query and fragment; null when it does not parse or has userinfo. */
function stripHeaderURL(raw: string): { url: string; changed: boolean } | null {
  const url = raw.trim();
  if (!goURLParses(url) || urlHasUserinfo(url)) return null;
  const end = url.search(/[?#]/);
  return end >= 0 ? { url: url.slice(0, end), changed: true } : { url, changed: false };
}

/** A URL-valued header's value without queries and fragments; null drops the header. */
function sanitizeURLHeader(lowerName: string, value: string): { value: string; changed: boolean } | null {
  if (lowerName === "link") {
    const matches = [...value.matchAll(LINK_URL)];
    if (!matches.length || /[?#]/.test(value.replace(LINK_URL, ""))) return null;
    let out = "";
    let last = 0;
    let changed = false;
    for (const match of matches) {
      const start = (match.index ?? 0) + 1;
      const end = start + (match[1] ?? "").length;
      const stripped = stripHeaderURL(value.slice(start, end));
      if (!stripped) return null;
      changed ||= stripped.changed;
      out += value.slice(last, start) + stripped.url;
      last = end;
    }
    return { value: out + value.slice(last), changed };
  }
  if (lowerName === "refresh") {
    const match = REFRESH_URL.exec(value);
    if (!match) return REFRESH_DELAY.test(value) ? { value, changed: false } : null;
    if (!REFRESH_DELAY_PREFIX.test(value.slice(0, match.index))) return null;
    const start = match.index + (match[1] ?? "").length + (match[2] ?? "").length;
    const end = start + (match[3] ?? "").length;
    const stripped = stripHeaderURL(value.slice(start, end));
    if (!stripped) return null;
    return { value: value.slice(0, start) + stripped.url + value.slice(end), changed: stripped.changed };
  }
  const stripped = stripHeaderURL(value);
  if (!stripped) return null;
  return stripped.changed ? { value: stripped.url, changed: true } : { value, changed: false };
}

/**
 * Response headers a service may see (design ruling 19, 19a), filtered the
 * way the host filters them again, plus the two rules only the frontend can
 * apply:
 * 1. a sensitive name (contract sensitiveHeaderNames, set-cookie2) or
 * 2. a name containing a sensitiveKeyFragments entry,
 * 3. a value holding one of the request's credential values (6+ characters),
 * 4. a value with a credential shape (Bearer, JWT, token prefixes, …),
 * 5. a value over httpHeaderValueMaxBytes: the header is withheld;
 * 6. a URL-valued header (Location, Content-Location, Link `<…>`, Refresh
 *    `url=`, Origin, Referer) loses its query and fragment; one that does not
 *    parse, has userinfo, or (Link) a `?` / `#` outside `<…>`, or (Refresh)
 *    anything but a delay before `url=`, is withheld.
 * `redacted` counts the headers withheld or rewritten. Characters the host
 * refuses are removed first (a header left without a name is skipped, not
 * counted). At most httpResponseHeadersMax headers stay; `truncated` tells
 * whether more were dropped.
 */
export function redactResponseHeaders(
  headers: readonly [string, string][],
  secrets: readonly string[],
): { headers: [string, string][]; redacted: number; truncated: boolean } {
  const credentials = [...new Set(secrets.filter((secret) => typeof secret === "string" && secret.length >= SECRET_MIN_LENGTH))];
  const kept: [string, string][] = [];
  let redacted = 0;
  for (const [rawName, rawValue] of headers) {
    const name = contextText(rawName);
    if (!name || typeof rawValue !== "string") continue;
    const value = rawValue.replace(LONE_SURROGATE, "").replace(REFUSED_HEADER_VALUE, "");
    const lower = name.trim().toLowerCase();
    if (isSensitiveHeader(name) || lower === "set-cookie2"
      || utf8ByteLength(value) > SERVICE_LIMITS.httpHeaderValueMaxBytes
      || credentials.some((secret) => value.includes(secret))
      || hasCredentialShape(value)) {
      redacted++;
      continue;
    }
    if (URL_HEADERS.has(lower)) {
      const sanitized = sanitizeURLHeader(lower, value);
      if (!sanitized) {
        redacted++;
        continue;
      }
      if (sanitized.changed) redacted++;
      kept.push([name, sanitized.value]);
      continue;
    }
    kept.push([name, value]);
  }
  const max = SERVICE_LIMITS.httpResponseHeadersMax;
  return { headers: kept.slice(0, max), redacted, truncated: kept.length > max };
}

// ---------------------------------------------------------------------------
// Requests.

/**
 * The request of a surface target, plus a notice to show once (a cut result
 * set). With `refused` nothing may be started: the menu shows that message
 * instead (a result set none of whose rows fits, a response body over the
 * provider's limit).
 */
export type ServiceSurfaceRunRequest = PluginServiceRunRequest & { notice?: { key: string; count: number }; refused?: { key: string } };

/**
 * The provider a content call will run on, as the host picks it
 * (selectServiceProviderWhere): the only one offered, else the default. That
 * provider is pinned in the request so the host cannot pick another, and the
 * content is sized to its maxBytes (the host refuses content over the chosen
 * provider's maxBytes with too_large). With several providers and no default
 * the host asks the user, so the content is sized to the smallest maxBytes:
 * whichever is picked takes it.
 */
function contentProvider(group: ServiceGroup): { provider?: PluginServiceProviderRef; maxBytes: number } {
  const sizeOf = (entry: PluginServiceEntry) => entry.input.maxBytes ?? SERVICE_LIMITS.fileMaxBytes;
  const providers = group.providers;
  const chosen = providers.length === 1 ? providers[0] : providers.find((entry) => entry.isDefault);
  if (chosen) return { provider: { pluginId: chosen.pluginId, entryId: chosen.entryId }, maxBytes: sizeOf(chosen) };
  return { maxBytes: providers.length ? Math.min(...providers.map(sizeOf)) : SERVICE_LIMITS.fileMaxBytes };
}

/**
 * sftpFile context: the transfer endpoint and remote path, or
 * `{remoteOmitted: true}` when the path cannot be sent as is or the whole
 * context (JSON-escaped) would exceed surfaceContextMaxBytes.
 */
function sftpSurfaceContext(remote: SftpFileMenuTarget): Record<string, unknown> {
  const omitted = { remoteOmitted: true };
  const passed = menuTargetPassContext("sftp/file/context", remote) as { connection: SftpFileMenuTarget["connection"]; path: string };
  const path = passed.path;
  if (typeof path !== "string" || !path.startsWith("/") || !cleanText(path, SERVICE_LIMITS.remoteContextPathMaxBytes)) {
    return omitted;
  }
  const { id, name, protocol, host, port, username } = passed.connection;
  const context = {
    remote: {
      connection: {
        id: contextText(id),
        name: contextText(name),
        protocol: contextText(protocol),
        host: contextText(host),
        port: boundedInteger(port, 0, 65535),
        username: contextText(username),
      },
      path,
    },
  };
  return contextBytes(context) <= SERVICE_LIMITS.surfaceContextMaxBytes ? context : omitted;
}

/**
 * dbResult context: connection, database and table names. Over
 * surfaceContextMaxBytes the table, then the database name are dropped; when
 * even the connection does not fit there is no context (the host has no
 * omitted flag for it and accepts none).
 */
function databaseSurfaceContext(database: DatabaseTreeMenuTarget): Record<string, unknown> | undefined {
  const passed = menuTargetPassContext("database/tree/context", database) as {
    connection: DatabaseTreeMenuTarget["connection"];
    database?: string;
    table?: { name: string; kind: string };
  };
  const tableName = passed.table ? contextText(passed.table.name) : "";
  const databaseName = passed.database ? contextText(passed.database) : "";
  const connection = { id: contextText(passed.connection.id), name: contextText(passed.connection.name), kind: contextText(passed.connection.kind) };
  const candidates = [
    { connection, ...(databaseName ? { database: databaseName } : {}), ...(passed.table && tableName ? { table: { name: tableName, kind: contextText(passed.table.kind) } } : {}) },
    { connection, ...(databaseName ? { database: databaseName } : {}) },
    { connection },
  ];
  for (const candidate of candidates) {
    const context = { database: candidate };
    if (contextBytes(context) <= SERVICE_LIMITS.surfaceContextMaxBytes) return context;
  }
  return undefined;
}

/** The request line a plugin may see (with remote.context), or undefined when the host would refuse it. */
function requestContext(request: NonNullable<HttpResponseServiceTarget["request"]>): { method: string; url: string; redacted?: true } | undefined {
  const method = contextText(request.method.toUpperCase(), CONTEXT_METHOD_MAX_BYTES);
  const { url, redacted } = sanitizeMenuRequestUrl(request.url);
  if (!method || !url || url.trim() !== url || /[?#]/.test(url) || !cleanText(url, SERVICE_LIMITS.remoteContextPathMaxBytes) || urlHasUserinfo(url)) {
    return undefined;
  }
  return { method, url, ...(redacted ? { redacted: true as const } : {}) };
}

function contextBytes(value: unknown): number {
  return utf8ByteLength(JSON.stringify(value));
}

function httpSurfaceContext(target: HttpResponseServiceTarget): Record<string, unknown> {
  const { response } = target;
  const filtered = redactResponseHeaders(response.headers, target.secrets);
  const responseContext: Record<string, unknown> = {
    status: boundedInteger(response.status, 0, 999),
    statusText: contextText(response.statusText),
    contentType: contextText(response.contentType, CONTEXT_CONTENT_TYPE_MAX_BYTES),
    elapsedMs: Number.isFinite(response.elapsedMs) ? Math.min(ELAPSED_MS_MAX, Math.max(0, response.elapsedMs)) : 0,
    headers: filtered.headers,
    headersRedacted: Math.min(HEADERS_REDACTED_MAX, filtered.redacted),
    ...(filtered.truncated ? { headersTruncated: true } : {}),
  };
  const request = target.request ? requestContext(target.request) : undefined;
  const context: Record<string, unknown> = { response: responseContext, ...(request ? { request } : {}) };
  // The host refuses a context over surfaceContextMaxBytes: drop headers from the end first.
  const max = SERVICE_LIMITS.surfaceContextMaxBytes;
  while (contextBytes(context) > max && filtered.headers.length) {
    filtered.headers.pop();
    responseContext.headersTruncated = true;
  }
  if (contextBytes(context) > max) delete context.request;
  if (contextBytes(context) > max) responseContext.statusText = "";
  return context;
}

/**
 * The service request for a target, built when the user picks `group` (the
 * result set is serialized only now). Content (dbResult, httpResponse) is
 * sized to the provider that will run (contentProvider: pinned when it is
 * the only one or the default, else the smallest maxBytes of the group).
 * dbResult takes at most that and dbResultMaxBytes; a cut result adds a
 * `notice` the menu shows once, one with no row that fits is `refused` (no
 * content). A response body (UTF-8, as the host measures it) over the limit
 * is `refused` too: a response is never cut.
 */
export function surfaceRunRequest(target: ServiceSurfaceTarget, group: ServiceGroup): ServiceSurfaceRunRequest {
  const service = group.service;
  switch (target.surface) {
    case "fileMenu":
      return { service, surface: "fileMenu", paths: target.files.map((file) => file.absolutePath) };
    case "sftpFile":
      return {
        service,
        surface: "sftpFile",
        remoteFile: { sessionId: target.sessionId, path: target.remote.path },
        surfaceContext: sftpSurfaceContext(target.remote),
      };
    case "dbResult": {
      const { provider, maxBytes } = contentProvider(group);
      const file = buildDbResultFile(target, Math.min(SERVICE_LIMITS.dbResultMaxBytes, maxBytes));
      if (file.tooLarge) return { service, surface: "dbResult", refused: { key: "plugins.services.dbResultTooLarge" } };
      const database = target.database ? databaseSurfaceContext(target.database) : undefined;
      return {
        service,
        surface: "dbResult",
        ...(provider ? { provider } : {}),
        content: { text: file.text },
        ...(database ? { surfaceContext: database } : {}),
        ...(file.truncated ? { notice: { key: "plugins.services.dbResultTruncated", count: file.rowCount } } : {}),
      };
    }
    case "httpResponse": {
      const { provider, maxBytes } = contentProvider(group);
      if (utf8ByteLength(target.response.body) > Math.min(SERVICE_LIMITS.httpResponseMaxBytes, maxBytes)) {
        return { service, surface: "httpResponse", refused: { key: "plugins.services.responseTooLarge" } };
      }
      return {
        service,
        surface: "httpResponse",
        ...(provider ? { provider } : {}),
        content: { text: target.response.body, contentType: target.response.contentType },
        surfaceContext: httpSurfaceContext(target),
      };
    }
    case "terminalSelection":
      return {
        service,
        surface: "terminalSelection",
        input: normalizeTerminalServiceText(target.selection),
        surfaceContext: { terminal: { kind: target.kind } },
      };
  }
}
