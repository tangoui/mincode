/**
 * Plugin HTTP collection templates (contribution point `httpCollections`),
 * pure logic.
 *
 * Trust boundary: the Go host (backend/plugin/contrib_http_collections.go)
 * reads and validates the `mincode.http-collection` v1 file; this module
 * re-checks the snapshot defensively (the "frontend" layer) and converts a
 * template into the user's own resources. Templates are untrusted
 * third-party data:
 * - the format has no auth fields; converted requests always get
 *   `authType: "none"` and empty credential fields;
 * - a sensitive header or key may only hold "" or one `{{variable}}`, and
 *   values shaped like credentials (JWT, PEM, AKIA, ghp_, sk-, ...) are
 *   rejected; URLs never carry user information;
 * - importing copies the template with fresh ids into the existing storage
 *   shape (no link back to the plugin); opening a request only creates an
 *   unsaved draft. Nothing here sends a request.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import type {
  HttpBodyType,
  HttpEnvironment,
  HttpKeyValueRow,
  HttpRequestCollection,
  SavedHttpRequest,
} from "@/domain/http/request-collection";

export const HTTP_COLLECTIONS_CONTRIBUTION_POINT = "httpCollections";

// Mirrors `points.httpCollections` in plugins/api/contribution-points.json
// (the host enforces them first).
const MAX_ENTRIES = 8;
const ID_PATTERN = /^[a-z0-9][a-z0-9-]{0,47}$/;
const TITLE_MAX_RUNES = 64;
const DESCRIPTION_MAX_RUNES = 256;
const REQUESTS_PER_COLLECTION = 200;
const REQUESTS_PER_PLUGIN = 500;
const FOLDERS_MAX = 64;
const FOLDER_DEPTH_MAX = 4;
const ENVIRONMENTS_MAX = 8;
const VARS_PER_ENVIRONMENT = 64;
const ROWS_MAX = 64;
const NAME_MAX_RUNES = 128;
const KEY_MAX_LENGTH = 256;
const VALUE_MAX_RUNES = 4096;
const URL_MAX_LENGTH = 2048;
const BODY_MAX_BYTES = 65536;
const DOCUMENTATION_MAX_BYTES = 16384;

export const HTTP_COLLECTION_METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"] as const;
export const HTTP_COLLECTION_BODY_TYPES = ["none", "json", "xml", "text", "x-www-form-urlencoded", "graphql"] as const;
const SENSITIVE_HEADER_NAMES = [
  "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "x-auth-token", "x-access-token", "x-csrf-token",
];
const SENSITIVE_KEY_FRAGMENTS = [
  "secret", "token", "password", "passwd", "pwd", "apikey", "api_key", "api-key", "auth", "credential", "session", "cookie",
  "privatekey", "private_key", "accesskey", "access_key", "signature",
];
const PLACEHOLDER = /^\{\{\s*[A-Za-z_][A-Za-z0-9_.-]*\s*\}\}$/;
const LEADING_VARIABLE = /^\{\{\s*[A-Za-z_][A-Za-z0-9_.-]*\s*\}\}/;
/** Credential shapes; the first eight also apply to prose (documentation, descriptions). */
const CREDENTIAL_PATTERNS = [
  /eyJ[\w-]+\.eyJ[\w-]+\./,
  /-----BEGIN [A-Z ]*PRIVATE KEY-----/,
  /AKIA[0-9A-Z]{16}/,
  /gh[pousr]_[A-Za-z0-9]{36}/,
  /github_pat_/,
  /xox[baprs]-/,
  /sk-[A-Za-z0-9]{20,}/,
  /AIza[0-9A-Za-z_-]{35}/,
  /Basic [A-Za-z0-9+/=]{12,}/,
  /Bearer [A-Za-z0-9._~+/-]{16,}/,
];
const PROSE_PATTERN_COUNT = 8;

export type PluginHttpMethod = (typeof HTTP_COLLECTION_METHODS)[number];
export type PluginHttpBodyType = (typeof HTTP_COLLECTION_BODY_TYPES)[number];

export interface PluginHttpRow {
  key: string;
  value: string;
  description?: string;
  enabled: boolean;
}

export interface PluginHttpRequestTemplate {
  name: string;
  method: PluginHttpMethod;
  url: string;
  params: PluginHttpRow[];
  headers: PluginHttpRow[];
  bodyType: PluginHttpBodyType;
  body: string;
  graphqlQuery?: string;
  graphqlVariables?: string;
  documentation?: string;
}

export interface PluginHttpFolderTemplate {
  name: string;
  requests: PluginHttpRequestTemplate[];
  children: PluginHttpFolderTemplate[];
}

export interface PluginHttpEnvironmentTemplate {
  name: string;
  vars: PluginHttpRow[];
}

export interface PluginHttpCollectionTemplate extends PluginHttpFolderTemplate {
  environments: PluginHttpEnvironmentTemplate[];
}

export interface PluginHttpCollectionCounts {
  requests: number;
  folders: number;
  environments: number;
}

export interface PluginHttpCollection {
  /** `<pluginId>.<id>`. */
  key: string;
  pluginId: string;
  pluginName: string;
  id: string;
  title: string;
  titleLocalized?: Record<string, string>;
  description: string;
  descriptionLocalized?: Record<string, string>;
  collection: PluginHttpCollectionTemplate;
  counts: PluginHttpCollectionCounts;
}

export interface PluginHttpCollectionIssue {
  pluginId: string;
  stage: "host" | "frontend";
  entry: number;
  id?: string;
  message: string;
}

export interface PluginHttpCollectionRegistry {
  collections: PluginHttpCollection[];
  issues: PluginHttpCollectionIssue[];
}

class TemplateError extends Error {}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : undefined;
}

function utf8Length(value: string) {
  return new TextEncoder().encode(value).length;
}

/** Controls, line separators, bidi and zero-width characters (tab/LF/CR allowed when multiline). */
function hasHiddenCharacters(value: string, multiline: boolean): boolean {
  for (const char of value) {
    const code = char.codePointAt(0) ?? 0;
    if (multiline && (code === 0x09 || code === 0x0a || code === 0x0d)) continue;
    if (
      code < 0x20 ||
      (code >= 0x7f && code <= 0x9f) ||
      code === 0x2028 ||
      code === 0x2029 ||
      (code >= 0x202a && code <= 0x202e) ||
      (code >= 0x2066 && code <= 0x2069) ||
      (code >= 0x200b && code <= 0x200f) ||
      code === 0xfeff
    ) {
      return true;
    }
  }
  return false;
}

/** Reports whether a value looks like a credential (prose skips the Basic/Bearer shapes). */
export function looksLikeHttpCredential(value: string, prose = false): boolean {
  const patterns = prose ? CREDENTIAL_PATTERNS.slice(0, PROSE_PATTERN_COUNT) : CREDENTIAL_PATTERNS;
  return patterns.some((pattern) => pattern.test(value));
}

/** A sensitive header name, or any key that contains a sensitive fragment (case-insensitive). */
export function isSensitiveHttpKey(key: string, header: boolean): boolean {
  const lower = key.trim().toLowerCase();
  if (header && SENSITIVE_HEADER_NAMES.includes(lower)) return true;
  return SENSITIVE_KEY_FRAGMENTS.some((fragment) => lower.includes(fragment));
}

/** An absolute http(s) URL, or one that starts with a `{{variable}}`, never with user information. */
export function isSafeTemplateUrl(url: string): boolean {
  if (!url || url.length > URL_MAX_LENGTH || /\s/.test(url) || looksLikeHttpCredential(url)) return false;
  const lower = url.toLowerCase();
  let rest: string;
  if (lower.startsWith("http://")) rest = url.slice(7);
  else if (lower.startsWith("https://")) rest = url.slice(8);
  else {
    const match = LEADING_VARIABLE.exec(url);
    if (!match) return false;
    rest = url.slice(match[0].length);
  }
  const authority = rest.split(/[/?#]/, 1)[0] ?? "";
  if (authority.includes("@")) return false;
  return !/^https?:\/\//i.test(url) || authority.length > 0;
}

function line(value: unknown, maxRunes: number, what: string): string {
  if (typeof value !== "string" || !value.trim() || [...value].length > maxRunes || hasHiddenCharacters(value, false)) {
    throw new TemplateError(`invalid ${what}`);
  }
  return value.trim();
}

function multiline(value: unknown, maxBytes: number, what: string, prose: boolean): string {
  if (value === undefined) return "";
  if (typeof value !== "string" || utf8Length(value) > maxBytes || hasHiddenCharacters(value, true)) {
    throw new TemplateError(`invalid ${what}`);
  }
  if (looksLikeHttpCredential(value, prose)) throw new TemplateError(`${what} looks like a credential`);
  return value;
}

function list(value: unknown, max: number, what: string): unknown[] {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > max) throw new TemplateError(`invalid ${what}`);
  return value;
}

function rows(value: unknown, max: number, header: boolean): PluginHttpRow[] {
  return list(value, max, "rows").map((candidate) => {
    const record = objectValue(candidate);
    if (!record || typeof record.key !== "string" || !record.key.trim() || utf8Length(record.key) > KEY_MAX_LENGTH || hasHiddenCharacters(record.key, false)) {
      throw new TemplateError("invalid row key");
    }
    const rowValue = record.value === undefined ? "" : record.value;
    if (typeof rowValue !== "string" || [...rowValue].length > VALUE_MAX_RUNES || hasHiddenCharacters(rowValue, false)) {
      throw new TemplateError("invalid row value");
    }
    if (looksLikeHttpCredential(rowValue)) throw new TemplateError("row value looks like a credential");
    if (isSensitiveHttpKey(record.key, header) && rowValue !== "" && !PLACEHOLDER.test(rowValue)) {
      throw new TemplateError("sensitive row must be empty or a single {{variable}}");
    }
    const row: PluginHttpRow = { key: record.key.trim(), value: rowValue, enabled: record.enabled !== false };
    if (typeof record.description === "string" && record.description) {
      if (hasHiddenCharacters(record.description, false) || looksLikeHttpCredential(record.description, true)) {
        throw new TemplateError("invalid row description");
      }
      row.description = record.description;
    }
    return row;
  });
}

interface WalkState {
  requests: number;
  folders: number;
}

function request(value: unknown): PluginHttpRequestTemplate {
  const record = objectValue(value);
  if (!record) throw new TemplateError("malformed request");
  if ("auth" in record || "authType" in record || "authToken" in record) throw new TemplateError("templates cannot carry auth");
  const method = typeof record.method === "string" ? record.method.trim().toUpperCase() : "";
  if (!(HTTP_COLLECTION_METHODS as readonly string[]).includes(method)) throw new TemplateError("invalid request method");
  const bodyType = record.bodyType === undefined ? "none" : record.bodyType;
  if (!(HTTP_COLLECTION_BODY_TYPES as readonly unknown[]).includes(bodyType)) throw new TemplateError("invalid request bodyType");
  const url = typeof record.url === "string" ? record.url.trim() : "";
  if (!isSafeTemplateUrl(url)) throw new TemplateError("invalid request URL");
  const result: PluginHttpRequestTemplate = {
    name: line(record.name, NAME_MAX_RUNES, "request name"),
    method: method as PluginHttpMethod,
    url,
    params: rows(record.params, ROWS_MAX, false),
    headers: rows(record.headers, ROWS_MAX, true),
    bodyType: bodyType as PluginHttpBodyType,
    body: multiline(record.body, BODY_MAX_BYTES, "request body", false),
  };
  const graphqlQuery = multiline(record.graphqlQuery, BODY_MAX_BYTES, "GraphQL query", false);
  const graphqlVariables = multiline(record.graphqlVariables, BODY_MAX_BYTES, "GraphQL variables", false);
  const documentation = multiline(record.documentation, DOCUMENTATION_MAX_BYTES, "documentation", true);
  if (graphqlQuery) result.graphqlQuery = graphqlQuery;
  if (graphqlVariables) result.graphqlVariables = graphqlVariables;
  if (documentation) result.documentation = documentation;
  return result;
}

function folder(value: unknown, depth: number, state: WalkState): PluginHttpFolderTemplate {
  const record = objectValue(value);
  if (!record) throw new TemplateError("malformed folder");
  const requests = list(record.requests, REQUESTS_PER_COLLECTION, "requests").map(request);
  state.requests += requests.length;
  if (state.requests > REQUESTS_PER_COLLECTION) throw new TemplateError("too many requests");
  const childValues = list(record.children, FOLDERS_MAX, "folders");
  if (childValues.length && depth >= FOLDER_DEPTH_MAX) throw new TemplateError("folders nested too deep");
  const children = childValues.map((child) => {
    state.folders += 1;
    if (state.folders > FOLDERS_MAX) throw new TemplateError("too many folders");
    return folder(child, depth + 1, state);
  });
  return { name: line(record.name, NAME_MAX_RUNES, "name"), requests, children };
}

/** Re-validates a host-normalized collection template; throws on anything unsafe. */
export function parseHttpCollectionTemplate(value: unknown): { collection: PluginHttpCollectionTemplate; counts: PluginHttpCollectionCounts } {
  const record = objectValue(value);
  if (!record) throw new TemplateError("malformed collection");
  const state: WalkState = { requests: 0, folders: 0 };
  const root = folder(record, 0, state);
  const environments = list(record.environments, ENVIRONMENTS_MAX, "environments").map((candidate) => {
    const environment = objectValue(candidate);
    if (!environment) throw new TemplateError("malformed environment");
    return { name: line(environment.name, NAME_MAX_RUNES, "environment name"), vars: rows(environment.vars, VARS_PER_ENVIRONMENT, false) };
  });
  return {
    collection: { ...root, environments },
    counts: { requests: state.requests, folders: state.folders, environments: environments.length },
  };
}

function localizedMap(value: unknown, maxRunes: number): Record<string, string> | undefined {
  const record = objectValue(value);
  if (!record) return undefined;
  const result: Record<string, string> = {};
  for (const [locale, text] of Object.entries(record).slice(0, 16)) {
    if (!locale.trim() || locale.length > 32) continue;
    if (typeof text === "string" && text.trim() && [...text].length <= maxRunes && !hasHiddenCharacters(text, false)) result[locale] = text.trim();
  }
  return Object.keys(result).length ? result : undefined;
}

function parseEntry(contribution: PointContribution, value: unknown): PluginHttpCollection | string {
  const record = objectValue(value);
  if (!record) return "malformed HTTP collection entry";
  const id = typeof record.id === "string" ? record.id : "";
  if (!ID_PATTERN.test(id)) return "invalid HTTP collection id";
  let parsed: ReturnType<typeof parseHttpCollectionTemplate>;
  let title: string;
  try {
    title = line(record.title, TITLE_MAX_RUNES, "title");
    parsed = parseHttpCollectionTemplate(record.collection);
  } catch (error) {
    return error instanceof TemplateError ? error.message : "invalid HTTP collection";
  }
  const description = typeof record.description === "string" && [...record.description].length <= DESCRIPTION_MAX_RUNES && !hasHiddenCharacters(record.description, false)
    ? record.description.trim()
    : "";
  const titleLocalized = localizedMap(record.titleLocalized, TITLE_MAX_RUNES);
  const descriptionLocalized = localizedMap(record.descriptionLocalized, DESCRIPTION_MAX_RUNES);
  return {
    key: `${contribution.pluginId}.${id}`,
    pluginId: contribution.pluginId,
    pluginName: contribution.pluginName.trim() || contribution.pluginId,
    id,
    title,
    ...(titleLocalized ? { titleLocalized } : {}),
    description,
    ...(descriptionLocalized ? { descriptionLocalized } : {}),
    collection: parsed.collection,
    counts: parsed.counts,
  };
}

/**
 * Normalizes the `httpCollections` snapshot. HTTP resources are global, so
 * every enabled plugin contributes regardless of the workspace. Invalid
 * entries are skipped and reported.
 */
export function buildPluginHttpCollectionRegistry(contributions: readonly PointContribution[]): PluginHttpCollectionRegistry {
  const collections: PluginHttpCollection[] = [];
  const issues: PluginHttpCollectionIssue[] = [];
  for (const contribution of contributions) {
    for (const issue of contribution.point.issues) {
      issues.push({ pluginId: contribution.pluginId, stage: "host", entry: issue.entry, message: issue.message, ...(issue.id ? { id: issue.id } : {}) });
    }
    const seen = new Set<string>();
    let total = 0;
    contribution.point.entries.slice(0, MAX_ENTRIES).forEach((value, entry) => {
      const parsed = parseEntry(contribution, value);
      const report = (message: string) => {
        const id = objectValue(value)?.id;
        issues.push({ pluginId: contribution.pluginId, stage: "frontend", entry, message, ...(typeof id === "string" && id ? { id } : {}) });
      };
      if (typeof parsed === "string") return report(parsed);
      if (seen.has(parsed.id)) return report("duplicate HTTP collection id");
      if (total + parsed.counts.requests > REQUESTS_PER_PLUGIN) return report("too many requests in total");
      seen.add(parsed.id);
      total += parsed.counts.requests;
      collections.push(parsed);
    });
  }
  return { collections, issues };
}

export type HttpTemplateIdFactory = (prefix: string) => string;

function userRows(template: readonly PluginHttpRow[], idFactory: HttpTemplateIdFactory, prefix: string): HttpKeyValueRow[] {
  return template.map((row) => ({
    id: idFactory(prefix),
    key: row.key,
    value: row.value,
    enabled: row.enabled,
    valueType: "string",
    description: row.description ?? "",
  }));
}

/** The request fields of a template request, in the shape of an unsaved draft (no auth). */
export function templateRequestToDraft(template: PluginHttpRequestTemplate, idFactory: HttpTemplateIdFactory) {
  return {
    name: template.name,
    method: template.method,
    url: template.url,
    params: userRows(template.params, idFactory, "param"),
    headers: userRows(template.headers, idFactory, "header"),
    body: template.body,
    bodyType: template.bodyType as HttpBodyType,
    authType: "none" as const,
    authToken: "",
    graphqlQuery: template.graphqlQuery ?? "",
    graphqlVariables: template.graphqlVariables ?? "",
    documentation: template.documentation ?? "",
  };
}

function savedRequest(template: PluginHttpRequestTemplate, idFactory: HttpTemplateIdFactory, now: number): SavedHttpRequest {
  const draft = templateRequestToDraft(template, idFactory);
  return {
    id: idFactory("saved-request"),
    ...draft,
    formBody: [],
    binaryFilePath: "",
    binaryFileName: "",
    authUsername: "",
    authPassword: "",
    apiKeyName: "",
    apiKeyValue: "",
    apiKeyAddTo: "header",
    oauth2GrantType: "authorization-code",
    oauth2AuthUrl: "",
    oauth2TokenUrl: "",
    oauth2ClientId: "",
    oauth2ClientSecret: "",
    oauth2Scope: "",
    oauth2CallbackUrl: "",
    oauth2Username: "",
    oauth2Password: "",
    oauth2AccessToken: "",
    environmentMode: "follow",
    updatedAt: now,
  };
}

function userFolder(template: PluginHttpFolderTemplate, idFactory: HttpTemplateIdFactory, now: number): HttpRequestCollection {
  return {
    id: idFactory("collection"),
    name: template.name,
    requests: template.requests.map((item) => savedRequest(item, idFactory, now)),
    children: template.children.map((child) => userFolder(child, idFactory, now)),
  };
}

/** Returns `name`, or `name (2)`, `name (3)`, ... when it is already taken. */
export function uniqueCollectionName(name: string, existingNames: Iterable<string>): string {
  const taken = new Set(existingNames);
  if (!taken.has(name)) return name;
  for (let index = 2; ; index += 1) {
    const candidate = `${name} (${index})`;
    if (!taken.has(candidate)) return candidate;
  }
}

/**
 * Converts a template into a new top-level collection in the existing storage
 * shape: fresh ids everywhere, no auth, environments owned by the collection
 * (the first one active), and no reference to the plugin.
 */
export function templateToHttpCollection(
  template: PluginHttpCollectionTemplate,
  idFactory: HttpTemplateIdFactory,
  existingNames: Iterable<string>,
  now = Date.now(),
  defaultEnvironmentName = "Default",
): HttpRequestCollection {
  const collection = userFolder(template, idFactory, now);
  const environments: HttpEnvironment[] = template.environments.map((environment) => ({
    id: idFactory("environment"),
    name: environment.name,
    vars: userRows(environment.vars, idFactory, "kv"),
  }));
  if (!environments.length) environments.push({ id: idFactory("environment"), name: defaultEnvironmentName, vars: [] });
  return {
    ...collection,
    name: uniqueCollectionName(template.name, existingNames),
    environments,
    activeEnvironmentId: environments[0]!.id,
  };
}

/** Install preview: the declared collection titles (the files are read by the host). */
export function httpCollectionPreviewSummary(contributes: unknown): { count: number; titles: string[] } | null {
  const entries = objectValue(contributes)?.[HTTP_COLLECTIONS_CONTRIBUTION_POINT];
  if (!Array.isArray(entries) || entries.length === 0) return null;
  const titles = entries
    .map((entry) => objectValue(entry)?.title)
    .filter((title): title is string => typeof title === "string" && Boolean(title.trim()))
    .map((title) => title.trim().slice(0, TITLE_MAX_RUNES));
  return { count: entries.length, titles };
}
