/**
 * Targets, host `when` values and passContext objects of the git, database
 * tree, ssh, sftp, redis and http menus, plus the request URL sanitizer.
 * Loaded only through composables/use-plugin-menu-target.ts and
 * domain/plugins/service-surfaces.ts so it stays out of the entry chunk
 * (domain/plugins/menus.ts is imported by the app shell).
 * Pure: nothing here touches IPC, Vue or Monaco.
 *
 * Each target is the single object the user opened the menu on. Callers may
 * hand in richer objects (for example a whole saved profile); the passContext
 * builders copy only whitelisted fields, so credentials never reach a plugin.
 */
import type { PluginContextValues } from "@/domain/commands/when";
import {
  editorHostContext,
  extnameOf,
  menuPassContext,
  projectTreeHostContext,
  tabHostContext,
  terminalHostContext,
  type CoreMenuTargets,
  type PluginContextMenuId,
} from "@/domain/plugins/menus";

export interface GitChangeMenuTarget {
  projectName: string;
  /** Absolute repository root. */
  repositoryPath: string;
  /** Relative to the repository root, `/`-separated. */
  relativePath: string;
  /** Previous relative path of a rename or copy. */
  oldRelativePath?: string;
  /** backend/git status: modified, added, deleted, renamed, typechange, conflicted, untracked. */
  status: string;
  group: "staged" | "unstaged" | "untracked";
  /** Absolute path (also for deleted files; `status` tells them apart). */
  path: string;
}

export interface GitCommitMenuTarget {
  projectName: string;
  repositoryPath: string;
  hash: string;
  shortHash: string;
  /** Commit message; only its first line is passed. */
  subject: string;
  /** Author name only; an email is never passed. */
  author: string;
  /** Unix seconds. */
  time: number;
  parents: readonly string[];
  refs: readonly string[];
}

export interface DatabaseTreeMenuTarget {
  /** `kind` is the SQL editor's database.kind of the connection. */
  connection: { id: string; name: string; kind: string };
  connected: boolean;
  objectType: "connection" | "database" | "table";
  database?: string;
  table?: { name: string; kind: string };
}

export interface SshConnectionMenuTarget {
  connectionId: string;
  name: string;
  group?: string;
  host: string;
  port: number;
  username: string;
  status: "idle" | "connecting" | "ready";
  authType: string;
  hasJumpHost: boolean;
}

export interface SftpFileMenuTarget {
  /** The actual transfer endpoint (fileTransfer host and user when configured). */
  connection: { id: string; name: string; protocol: string; host: string; port: number; username: string };
  /** Remote absolute POSIX path. */
  path: string;
  name: string;
  kind: "file" | "directory";
  size?: number;
}

export interface RedisKeyMenuTarget {
  connection: { id: string; name: string };
  dbIndex: number;
  key: string;
  type: string;
  /** Seconds; negative when the key does not expire. */
  ttl: number;
}

export interface HttpRequestMenuTarget {
  collection: { id: string; name: string };
  /** `url` is the saved, unexpanded address; it is sanitized before it leaves the host. */
  request: { id: string; name: string; method: string; url: string };
}

/** The target type each rendered menu takes. */
export interface PluginMenuTargets extends CoreMenuTargets {
  "git/changes/context": GitChangeMenuTarget;
  "git/commit/context": GitCommitMenuTarget;
  "database/tree/context": DatabaseTreeMenuTarget;
  "ssh/connection/context": SshConnectionMenuTarget;
  "sftp/file/context": SftpFileMenuTarget;
  "redis/key/context": RedisKeyMenuTarget;
  "http/request/context": HttpRequestMenuTarget;
}
export type PluginMenuTargetOf<M extends PluginContextMenuId> = PluginMenuTargets[M];

// ---------------------------------------------------------------------------
// Host `when` values.

export function gitChangeHostContext(target: GitChangeMenuTarget): PluginContextValues {
  return { "git.group": target.group, "git.status": target.status, "git.extname": extnameOf(target.relativePath) };
}

export function gitCommitHostContext(target: GitCommitMenuTarget): PluginContextValues {
  return { "git.isMerge": target.parents.length > 1, "git.hasRefs": target.refs.length > 0 };
}

export function databaseTreeHostContext(target: DatabaseTreeMenuTarget): PluginContextValues {
  return {
    "database.kind": target.connection.kind,
    "database.objectType": target.objectType,
    "database.tableKind": target.objectType === "table" && target.table ? target.table.kind.toLowerCase() : "",
    "database.connected": target.connected,
  };
}

export function sshConnectionHostContext(target: SshConnectionMenuTarget): PluginContextValues {
  return { "ssh.status": target.status, "ssh.authType": target.authType, "ssh.hasJumpHost": target.hasJumpHost };
}

export function sftpFileHostContext(target: SftpFileMenuTarget): PluginContextValues {
  return {
    "sftp.kind": target.kind,
    "sftp.extname": target.kind === "directory" ? "" : extnameOf(target.path),
    "sftp.protocol": target.connection.protocol,
  };
}

export function redisKeyHostContext(target: RedisKeyMenuTarget): PluginContextValues {
  return { "redis.type": target.type.toLowerCase(), "redis.hasTtl": target.ttl >= 0 };
}

export function httpRequestHostContext(target: HttpRequestMenuTarget): PluginContextValues {
  return { "http.method": target.request.method.toUpperCase(), "http.scheme": sanitizeMenuRequestUrl(target.request.url).scheme };
}

/** Host `when` values of `menu` for its target (every rendered menu). */
export function menuHostContext<M extends PluginContextMenuId>(menu: M, target: PluginMenuTargetOf<M>): PluginContextValues {
  const menuId: PluginContextMenuId = menu;
  switch (menuId) {
    case "editor/context":
    case "sql/editor/context":
      return editorHostContext(target as CoreMenuTargets["editor/context"]);
    case "project/tree/context":
      return projectTreeHostContext(target as CoreMenuTargets["project/tree/context"]);
    case "workbench/tab/context":
      return tabHostContext(target as CoreMenuTargets["workbench/tab/context"]);
    case "terminal/context":
      return terminalHostContext(target as CoreMenuTargets["terminal/context"]);
    case "git/changes/context":
      return gitChangeHostContext(target as GitChangeMenuTarget);
    case "git/commit/context":
      return gitCommitHostContext(target as GitCommitMenuTarget);
    case "database/tree/context":
      return databaseTreeHostContext(target as DatabaseTreeMenuTarget);
    case "ssh/connection/context":
      return sshConnectionHostContext(target as SshConnectionMenuTarget);
    case "sftp/file/context":
      return sftpFileHostContext(target as SftpFileMenuTarget);
    case "redis/key/context":
      return redisKeyHostContext(target as RedisKeyMenuTarget);
    case "http/request/context":
      return httpRequestHostContext(target as HttpRequestMenuTarget);
  }
}

// ---------------------------------------------------------------------------
// Request URL sanitizing (http/request/context).

/** Schemes whose authority starts after any number of slashes (WHATWG "special" schemes). */
const SPECIAL_SCHEMES = new Set(["http", "https", "ws", "wss", "ftp"]);
const URL_SCHEME_PATTERN = /^([A-Za-z][A-Za-z0-9+.-]*):(\/*)/;
const AUTHORITY_END_PATTERN = /[/?#]/;
/** Token alphabet of a path segment; `%XX` counts as one character and is never decoded. */
const TOKEN_SEGMENT_PATTERN = /^(?:[A-Za-z0-9_\-.:~+=]|%[0-9A-Fa-f]{2})+$/;
const PERCENT_ESCAPE_PATTERN = /%[0-9A-Fa-f]{2}/g;
const VALID_PORT_PATTERN = /^(?:\d*|\{\{[^{}]*\}\})$/;
const CREDENTIAL_MIN_LENGTH = 24;
const REDACTED_SEGMENT = "{redacted}";

/**
 * A path segment that looks like a key or token: at least 24 characters of
 * `[A-Za-z0-9_\-.:~+=]` or `%XX` (each escape counts as one character) with
 * both letters and digits.
 */
function credentialShapedSegment(segment: string): boolean {
  if (!TOKEN_SEGMENT_PATTERN.test(segment)) return false;
  const literal = segment.replace(PERCENT_ESCAPE_PATTERN, "");
  const length = literal.length + (segment.length - literal.length) / 3;
  return length >= CREDENTIAL_MIN_LENGTH && /[A-Za-z]/.test(literal) && /\d/.test(literal);
}

/** Index of the first `/`, `?` or `#` at or after `from` (or the text length). */
function authorityEnd(text: string, from: number): number {
  const index = text.slice(from).search(AUTHORITY_END_PATTERN);
  return index >= 0 ? from + index : text.length;
}

/** The port of an authority (`host:port`, `[v6]:port`), or "" when it has none. */
function authorityPort(authority: string): string {
  const v6End = authority.lastIndexOf("]");
  const colon = authority.lastIndexOf(":");
  return colon > v6End ? authority.slice(colon + 1) : "";
}

/** Text after an `@` that could be a host: has `.`, `[`, `:` or `{{`, or is `localhost`. */
function looksLikeHost(text: string): boolean {
  return /[.[:]/.test(text) || text.includes("{{") || text.toLowerCase() === "localhost";
}

/**
 * An authority that is a host by itself and cannot be the start of a
 * userinfo: no `@`, a numeric (or `{{variable}}`) port, and a dot, an IPv6
 * literal, a variable or `localhost`. `u:12` or `myhost:8443` are not.
 */
function wellFormedHost(authority: string): boolean {
  if (authority.includes("@")) return false;
  const port = authorityPort(authority);
  if (!VALID_PORT_PATTERN.test(port)) return false;
  const host = port || authority.endsWith(":") ? authority.slice(0, authority.lastIndexOf(":")) : authority;
  return /[.[]/.test(host) || host.includes("{{") || host.toLowerCase() === "localhost";
}

/**
 * The request address a plugin may see:
 * 1. `\` is read as `/` (as WHATWG does for http/https); after an http(s)/ws/ftp
 *    scheme any number of slashes (also none) starts the authority, which is
 *    rebuilt as `scheme://`;
 * 2. the userinfo is removed before the query and fragment are cut, so an
 *    unencoded `/`, `?` or `#` in a password cannot hide it: when the text up to
 *    the first `/ ? #` is not a well-formed host (see wellFormedHost), the
 *    userinfo runs to the LAST `@` whose following text (up to `/ ? #`) looks
 *    like a host; otherwise to the last `@` before the first `/ ? #`. Removing
 *    it sets `redacted`. An authority with an invalid port that is left over
 *    becomes `{redacted}` as a whole. Known residuals (documented in the
 *    design spec and plugin-system.md, not fixed):
 *    - a user name with a dot and a numeric password reads as a well-formed
 *      host, so `user.name:12/34@host` passes through unchanged;
 *    - a dot-less single-label host after the `@` does not look like a host,
 *      so the userinfo passes through: `u:12/34@myhost` and `us/er@myhost`
 *      unchanged, `u:12#34@myhost` leaks `u:12` and `u:p/w@myhost` becomes
 *      `{redacted}/w@myhost`;
 *    - a dot-less host with a numeric port (`myhost:8443/users/@me.json`)
 *      reads like `u:12/34@me.json` and is over-dropped (`me.json` becomes
 *      the host); `myhost:8443/users/@me` stays as typed. With a dot, an IPv6
 *      literal, a variable or `localhost` the host is kept;
 * 3. the fragment and the query are cut; each path segment is cut at its
 *    first `;` (matrix parameters such as `;jsessionid=`), a segment
 *    containing `=` becomes `{redacted}` as a whole, and so does a
 *    credential-shaped segment (see credentialShapedSegment: webhook secrets,
 *    `bot<id>:<token>`, base64, percent-encoded tokens, also UUIDs);
 * 4. `{{variables}}` stay as typed and are never expanded; nothing is
 *    percent-decoded or punycode-converted (IDN hosts stay as typed).
 * `scheme` is `http` / `https`, or empty for variables, other schemes and
 * unrecognized input. `redacted` is set when userinfo, an authority or a
 * segment was removed or replaced. The length limit (urlMaxLength) is
 * enforced when the payload is built.
 */
export function sanitizeMenuRequestUrl(raw: string): { url: string; scheme: "http" | "https" | ""; redacted: boolean } {
  const text = raw.trim().replace(/\\/g, "/");

  let prefix = "";
  let scheme: "http" | "https" | "" = "";
  let body = text;
  let hasAuthority = true;
  const schemeMatch = URL_SCHEME_PATTERN.exec(text);
  const schemeName = schemeMatch?.[1]?.toLowerCase() ?? "";
  if (schemeMatch && (SPECIAL_SCHEMES.has(schemeName) || schemeMatch[2]!.length >= 2)) {
    prefix = `${schemeMatch[1]}://`;
    body = text.slice(schemeMatch[0].length);
    if (schemeName === "http" || schemeName === "https") scheme = schemeName;
  } else if (text.startsWith("//")) {
    prefix = "//";
    body = text.replace(/^\/+/, "");
  } else if (text.startsWith("/")) {
    // A path without a host.
    hasAuthority = false;
  }
  // Scheme-less input that does not start with a path still begins with a
  // host (HTTP clients add http://), so it has an authority too.

  let redacted = false;
  let authority = "";
  let rest = body;
  if (hasAuthority) {
    const leadEnd = authorityEnd(body, 0);
    const leadAt = body.lastIndexOf("@", leadEnd - 1);
    let start = leadAt >= 0 && leadAt < leadEnd ? leadAt + 1 : 0;
    if (!wellFormedHost(body.slice(start, authorityEnd(body, start)))) {
      for (let at = body.lastIndexOf("@"); at >= start; at = at > 0 ? body.lastIndexOf("@", at - 1) : -1) {
        if (looksLikeHost(body.slice(at + 1, authorityEnd(body, at + 1)))) {
          start = at + 1;
          break;
        }
      }
    }
    if (start > 0) redacted = true;
    const end = authorityEnd(body, start);
    authority = body.slice(start, end);
    rest = body.slice(end);
    if (!VALID_PORT_PATTERN.test(authorityPort(authority))) {
      authority = REDACTED_SEGMENT;
      redacted = true;
    }
  }

  const hash = rest.indexOf("#");
  if (hash >= 0) rest = rest.slice(0, hash);
  const query = rest.indexOf("?");
  if (query >= 0) rest = rest.slice(0, query);

  const path = rest
    .split("/")
    .map((part) => {
      const semicolon = part.indexOf(";");
      const segment = semicolon >= 0 ? part.slice(0, semicolon) : part;
      if (!segment.includes("=") && !credentialShapedSegment(segment)) return segment;
      redacted = true;
      return REDACTED_SEGMENT;
    })
    .join("/");
  return { url: prefix + authority + path, scheme, redacted };
}

// ---------------------------------------------------------------------------
// passContext objects. Every builder copies an explicit field whitelist.

function firstLine(text: string): string {
  const end = text.search(/\r?\n/);
  return end >= 0 ? text.slice(0, end) : text;
}

/** The author name without any email: ` <email>` is removed and a bare email becomes "". */
function authorName(author: string): string {
  const name = author.replace(/\s*<[^>]*>\s*$/, "").trim();
  return name.includes("@") ? "" : name;
}

/** The passContext object of any rendered menu (what the plugin receives as `context`). */
export function menuTargetPassContext<M extends PluginContextMenuId>(menu: M, target: PluginMenuTargetOf<M>): Record<string, unknown> {
  const menuId: PluginContextMenuId = menu;
  switch (menuId) {
    case "editor/context":
    case "sql/editor/context":
    case "project/tree/context":
    case "workbench/tab/context":
    case "terminal/context":
      return menuPassContext(menuId, target as CoreMenuTargets[typeof menuId]);
    case "git/changes/context": {
      const change = target as GitChangeMenuTarget;
      return {
        projectName: change.projectName,
        repositoryPath: change.repositoryPath,
        relativePath: change.relativePath,
        ...(change.oldRelativePath ? { oldRelativePath: change.oldRelativePath } : {}),
        status: change.status,
        group: change.group,
        path: change.path,
      };
    }
    case "git/commit/context": {
      const commit = target as GitCommitMenuTarget;
      return {
        projectName: commit.projectName,
        repositoryPath: commit.repositoryPath,
        hash: commit.hash,
        shortHash: commit.shortHash,
        subject: firstLine(commit.subject),
        author: authorName(commit.author),
        time: commit.time,
        parents: [...commit.parents],
        refs: [...commit.refs],
      };
    }
    case "database/tree/context": {
      const node = target as DatabaseTreeMenuTarget;
      return {
        connection: { id: node.connection.id, name: node.connection.name, kind: node.connection.kind },
        objectType: node.objectType,
        ...(node.objectType !== "connection" && node.database ? { database: node.database } : {}),
        ...(node.objectType === "table" && node.table ? { table: { name: node.table.name, kind: node.table.kind.toLowerCase() } } : {}),
      };
    }
    case "ssh/connection/context": {
      const ssh = target as SshConnectionMenuTarget;
      return {
        connectionId: ssh.connectionId,
        name: ssh.name,
        ...(ssh.group ? { group: ssh.group } : {}),
        host: ssh.host,
        port: ssh.port,
        username: ssh.username,
      };
    }
    case "sftp/file/context": {
      const file = target as SftpFileMenuTarget;
      const { id, name, protocol, host, port, username } = file.connection;
      return {
        connection: { id, name, protocol, host, port, username },
        path: file.path,
        name: file.name,
        kind: file.kind,
        ...(typeof file.size === "number" && Number.isFinite(file.size) ? { size: file.size } : {}),
      };
    }
    case "redis/key/context": {
      const key = target as RedisKeyMenuTarget;
      return {
        connection: { id: key.connection.id, name: key.connection.name },
        dbIndex: key.dbIndex,
        key: key.key,
        type: key.type.toLowerCase(),
        ttl: key.ttl,
      };
    }
    case "http/request/context": {
      const http = target as HttpRequestMenuTarget;
      const { url, redacted } = sanitizeMenuRequestUrl(http.request.url);
      return {
        collection: { id: http.collection.id, name: http.collection.name },
        request: {
          id: http.request.id,
          name: http.request.name,
          method: http.request.method.toUpperCase(),
          url,
          ...(redacted ? { redacted: true } : {}),
        },
      };
    }
  }
}
