/**
 * The redacted request model rendered by plugin HTTP code generators
 * (`httpCodeGenerators`, domain/http/code-template.ts). Plugin templates are
 * third-party text, so the model never carries a secret:
 *
 * - auth values become placeholders (`Bearer YOUR_TOKEN`,
 *   `Basic YOUR_BASE64_CREDENTIALS`, `YOUR_API_KEY`, `Bearer YOUR_ACCESS_TOKEN`);
 * - sensitive headers (contract `sensitiveHeaderNames`) and any header,
 *   query, form or JSON/XML/`key=value` body key containing a contract
 *   `sensitiveKeyFragments` entry become `YOUR_<KEY>`;
 * - credential-shaped values anywhere (JWTs, `Bearer …`, well-known token
 *   prefixes, private keys, URL userinfo) become `YOUR_SECRET`;
 * - file fields become `YOUR_FILE_PATH` and binary bodies are omitted;
 * - environment variables are never resolved: `{{name}}` references stay
 *   literal (a value that is exactly one reference is kept even when the
 *   key is sensitive, since it reveals nothing) and the `variables` list
 *   names them with `YOUR_<NAME>` values;
 * - control, bidirectional and zero-width characters are stripped.
 *
 * Nothing here reaches the plugin process; rendering happens in the WebView.
 */
import contributionContract from "../plugins/contribution-points.json" with { type: "json" };
import type { CodeItem, CodeModel } from "./code-template.ts";
import type { HttpBodyType, HttpRequestDraft } from "./request-collection.ts";

const lists = (contributionContract as { points: Record<string, { lists?: Record<string, string[]> }> }).points
  .httpCodeGenerators?.lists ?? {};
const SENSITIVE_HEADER_NAMES = new Set((lists.sensitiveHeaderNames ?? []).map((name) => name.toLowerCase()));
const SENSITIVE_KEY_FRAGMENTS = (lists.sensitiveKeyFragments ?? []).map((fragment) => fragment.toLowerCase());

const ENV_REFERENCE = /\{\{\s*([A-Za-z_][A-Za-z0-9_.-]*)\s*\}\}/g;
const WHOLE_ENV_REFERENCE = /^\{\{\s*[A-Za-z_][A-Za-z0-9_.-]*\s*\}\}$/;
const BODY_CONTENT_TYPES: Partial<Record<HttpBodyType, string>> = {
  json: "application/json",
  xml: "application/xml",
  text: "text/plain",
  html: "text/html",
  javascript: "application/javascript",
  "x-www-form-urlencoded": "application/x-www-form-urlencoded",
  graphql: "application/json",
};
const TEXT_BODY_TYPES: readonly HttpBodyType[] = ["json", "xml", "text", "html", "javascript", "raw"];

/** Credential shapes replaced anywhere in a value (order matters: longest first). */
const CREDENTIAL_PATTERNS: Array<[RegExp, string]> = [
  [/-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)/g, "YOUR_SECRET"],
  [/([a-z][a-z0-9+.-]*:\/\/)[^\s/?#@]+@/gi, "$1"],
  [/\b(Bearer|Basic|Token|Digest)\s+[A-Za-z0-9\-._~+/=]{8,}/gi, "$1 YOUR_SECRET"],
  [/\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]*/g, "YOUR_SECRET"],
  [/\b(?:sk|pk|rk)[-_](?:live|test|proj|ant)?[-_]?[A-Za-z0-9_-]{16,}/g, "YOUR_SECRET"],
  [/\bgh[pousr]_[A-Za-z0-9]{20,}/g, "YOUR_SECRET"],
  [/\bgithub_pat_[A-Za-z0-9_]{20,}/g, "YOUR_SECRET"],
  [/\bglpat-[A-Za-z0-9_-]{20,}/g, "YOUR_SECRET"],
  [/\bxox[abposr]-[A-Za-z0-9-]{10,}/g, "YOUR_SECRET"],
  [/\bAKIA[0-9A-Z]{16}\b/g, "YOUR_SECRET"],
  [/\bAIza[0-9A-Za-z_-]{30,}/g, "YOUR_SECRET"],
  [/\bnpm_[A-Za-z0-9]{30,}/g, "YOUR_SECRET"],
];

function isHiddenCodePoint(code: number, allowNewlines: boolean): boolean {
  if (allowNewlines && (code === 0x0a || code === 0x0d || code === 0x09)) return false;
  return (
    code < 0x20 ||
    (code >= 0x7f && code <= 0x9f) ||
    code === 0x2028 ||
    code === 0x2029 ||
    (code >= 0x202a && code <= 0x202e) ||
    (code >= 0x2066 && code <= 0x2069) ||
    (code >= 0x200b && code <= 0x200f) ||
    code === 0x2060 ||
    code === 0xfeff
  );
}

function stripHidden(value: string, allowNewlines = false): string {
  let result = "";
  for (const char of value) if (!isHiddenCodePoint(char.codePointAt(0) ?? 0, allowNewlines)) result += char;
  return result;
}

/** `X-Api-Key` → `YOUR_X_API_KEY`; empty names fall back to `YOUR_SECRET`. */
export function secretPlaceholder(key: string): string {
  const name = key
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .replace(/[^A-Za-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .toUpperCase()
    .slice(0, 48);
  return name ? `YOUR_${name}` : "YOUR_SECRET";
}

/** True for a key that names a secret (contract `sensitiveKeyFragments`, case-insensitive). */
export function isSensitiveKey(key: string): boolean {
  const lower = key.toLowerCase();
  return SENSITIVE_KEY_FRAGMENTS.some((fragment) => lower.includes(fragment));
}

/** True for a header name that names a secret: contract `sensitiveHeaderNames` or a `sensitiveKeyFragments` entry. */
export function isSensitiveHeader(name: string): boolean {
  return SENSITIVE_HEADER_NAMES.has(name.trim().toLowerCase()) || isSensitiveKey(name);
}

/** Replaces credential shapes and strips hidden characters; env references stay literal. */
export function redactCredentialText(value: string, allowNewlines = false): string {
  let text = stripHidden(value, allowNewlines);
  for (const [pattern, replacement] of CREDENTIAL_PATTERNS) text = text.replace(pattern, replacement);
  return text;
}

/** Whether a value holds one of the credential shapes redactCredentialText replaces (JWT, Bearer, token prefixes, URL userinfo, …). */
export function hasCredentialShape(value: string): boolean {
  // search() ignores the patterns' global flag and lastIndex.
  return CREDENTIAL_PATTERNS.some(([pattern]) => value.search(pattern) >= 0);
}

/** A value under a sensitive key: kept only when it is exactly one env reference. */
function sensitiveValue(key: string, value: string): string {
  const trimmed = value.trim();
  if (WHOLE_ENV_REFERENCE.test(trimmed)) return trimmed;
  return value === "" ? "" : secretPlaceholder(key);
}

function redactPair(key: string, value: string): CodeItem {
  const name = stripHidden(key.trim());
  return { name, value: isSensitiveKey(name) ? sensitiveValue(name, value) : redactCredentialText(value) };
}

function redactHeader(name: string, value: string): CodeItem {
  const header = stripHidden(name.trim());
  if (!isSensitiveHeader(header)) return { name: header, value: redactCredentialText(value) };
  const trimmed = value.trim();
  if (WHOLE_ENV_REFERENCE.test(trimmed)) return { name: header, value: trimmed };
  const scheme = /^(Bearer|Basic|Token|Digest)\s+(.*)$/i.exec(trimmed);
  if (scheme && WHOLE_ENV_REFERENCE.test(scheme[2]!.trim())) return { name: header, value: `${scheme[1]} ${scheme[2]!.trim()}` };
  if (scheme?.[1]?.toLowerCase() === "basic") return { name: header, value: "Basic YOUR_BASE64_CREDENTIALS" };
  if (scheme) return { name: header, value: `${scheme[1]} YOUR_TOKEN` };
  return { name: header, value: value === "" ? "" : secretPlaceholder(header) };
}

/**
 * Redacts sensitive keys inside a text body without reformatting it: JSON
 * `"key": "value"` / `"key": 123`, XML `<key>value</key>` and `key=value`
 * pairs, then credential shapes anywhere.
 */
export function redactBodyText(body: string): string {
  let text = body.replace(
    /("((?:[^"\\\n]|\\.)*)"\s*:\s*)("(?:[^"\\\n]|\\.)*"|-?\d[\d.eE+-]*|true|false)/g,
    (match, prefix: string, key: string, value: string) => {
      if (!isSensitiveKey(key)) return match;
      const inner = value.startsWith('"') ? value.slice(1, -1) : value;
      return `${prefix}"${sensitiveValue(key, inner)}"`;
    },
  );
  text = text.replace(/<([A-Za-z_][\w.-]*)>([^<]*)<\/\1>/g, (match, key: string, value: string) =>
    isSensitiveKey(key) ? `<${key}>${sensitiveValue(key, value)}</${key}>` : match,
  );
  text = text.replace(/(^|[?&\s])([A-Za-z0-9_.\-[\]]+)=([^&\s]*)/g, (match, lead: string, key: string, value: string) =>
    isSensitiveKey(key) ? `${lead}${key}=${sensitiveValue(key, value)}` : match,
  );
  return redactCredentialText(text, true);
}

/** encodeURIComponent that keeps `{{name}}` env references readable. */
function encodeQueryPart(value: string): string {
  return encodeURIComponent(value).replace(/%7B%7B/g, "{{").replace(/%7D%7D/g, "}}");
}

function decodeQueryPart(value: string): string {
  try {
    return decodeURIComponent(value.replace(/\+/g, " "));
  } catch {
    return value;
  }
}

function activeRows<T extends { key: string; enabled: boolean }>(rows: readonly T[] | undefined): T[] {
  return (rows ?? []).filter((row) => row.enabled && row.key.trim());
}

interface UrlParts {
  scheme: string;
  host: string;
  path: string;
  query: Array<[string, string]>;
}

/** Splits a URL that may hold `{{env}}` references; userinfo is dropped, the fragment ignored. */
function splitRequestUrl(raw: string): UrlParts {
  const withoutHash = raw.trim().split("#")[0] ?? "";
  const queryIndex = withoutHash.indexOf("?");
  const beforeQuery = queryIndex >= 0 ? withoutHash.slice(0, queryIndex) : withoutHash;
  const query = queryIndex >= 0
    ? withoutHash.slice(queryIndex + 1).split("&").filter(Boolean).map((pair): [string, string] => {
        const equals = pair.indexOf("=");
        return equals >= 0
          ? [decodeQueryPart(pair.slice(0, equals)), decodeQueryPart(pair.slice(equals + 1))]
          : [decodeQueryPart(pair), ""];
      })
    : [];
  const match = /^([A-Za-z][A-Za-z0-9+.-]*):\/\/([^/]*)(.*)$/.exec(beforeQuery);
  if (!match) return { scheme: "", host: "", path: beforeQuery, query };
  const authority = match[2]!;
  return { scheme: match[1]!.toLowerCase(), host: authority.slice(authority.lastIndexOf("@") + 1), path: match[3]!, query };
}

function sanitizedMethod(method: string): string {
  const upper = method.trim().toUpperCase();
  return /^[A-Z0-9!#$%&'*+.^_`|~-]{1,32}$/.test(upper) ? upper : "GET";
}

function collectVariables(values: readonly string[]): CodeItem[] {
  const names = new Set<string>();
  for (const value of values) for (const match of value.matchAll(ENV_REFERENCE)) names.add(match[1]!);
  return [...names].slice(0, 64).map((name) => ({ name, value: secretPlaceholder(name) }));
}

/** Builds the redacted model a plugin template renders. */
export function buildRedactedHttpCodeModel(request: HttpRequestDraft): Required<CodeModel> {
  const bodyType: HttpBodyType = request.bodyType ?? (request.body ? "raw" : "none");
  const parts = splitRequestUrl(request.url);

  const query = [
    ...parts.query.map(([key, value]) => redactPair(key, value)),
    ...activeRows(request.params).map((row) => redactPair(row.key, row.value)),
  ];
  if (request.authType === "api-key" && request.apiKeyAddTo === "query" && request.apiKeyName?.trim() && request.apiKeyValue) {
    query.push({ name: stripHidden(request.apiKeyName.trim()), value: "YOUR_API_KEY" });
  }

  const headers = activeRows(request.headers).map((row) => redactHeader(row.key, row.value));
  const hasHeader = (name: string) => headers.some((header) => header.name.toLowerCase() === name.toLowerCase());
  const addHeader = (name: string, value: string) => {
    if (name && !hasHeader(name)) headers.push({ name, value });
  };
  const token = request.authToken.trim();
  if (request.authType === "bearer" && token) addHeader("Authorization", WHOLE_ENV_REFERENCE.test(token) ? `Bearer ${token}` : "Bearer YOUR_TOKEN");
  if (request.authType === "basic" && (request.authUsername || request.authPassword || token)) {
    addHeader("Authorization", "Basic YOUR_BASE64_CREDENTIALS");
  }
  if (request.authType === "api-key" && request.apiKeyAddTo !== "query" && request.apiKeyName?.trim() && request.apiKeyValue) {
    addHeader(stripHidden(request.apiKeyName.trim()), "YOUR_API_KEY");
  }
  if (request.authType === "oauth2" && request.oauth2AccessToken) addHeader("Authorization", "Bearer YOUR_ACCESS_TOKEN");
  const defaultContentType = BODY_CONTENT_TYPES[bodyType];
  if (defaultContentType) addHeader("Content-Type", defaultContentType);

  let formFields: CodeItem[] = [];
  if (bodyType === "form-data" || bodyType === "x-www-form-urlencoded") {
    formFields = activeRows(request.formBody).map((row) =>
      bodyType === "form-data" && row.fieldType === "file"
        ? { name: stripHidden(row.key.trim()), value: "YOUR_FILE_PATH" }
        : redactPair(row.key, row.value),
    );
  }

  let body = "";
  const graphqlQuery = bodyType === "graphql" ? redactCredentialText(request.graphqlQuery ?? "", true) : "";
  const graphqlVariables = bodyType === "graphql" ? redactBodyText(request.graphqlVariables?.trim() ?? "") : "";
  if (TEXT_BODY_TYPES.includes(bodyType)) body = redactBodyText(request.body ?? "");
  else if (bodyType === "x-www-form-urlencoded") {
    body = formFields.map((field) => `${encodeQueryPart(field.name)}=${encodeQueryPart(field.value)}`).join("&");
  } else if (bodyType === "graphql") {
    let variables: unknown;
    if (graphqlVariables) {
      try {
        variables = JSON.parse(graphqlVariables);
      } catch {
        variables = graphqlVariables;
      }
    }
    body = JSON.stringify({ query: graphqlQuery, ...(variables === undefined ? {} : { variables }) });
  }

  const scheme = stripHidden(parts.scheme);
  const host = stripHidden(parts.host);
  const path = redactCredentialText(parts.path);
  const urlBase = scheme ? `${scheme}://${host}${path}` : path;
  const queryText = query.map((item) => `${encodeQueryPart(item.name)}=${encodeQueryPart(item.value)}`).join("&");
  const contentType = headers.find((header) => header.name.toLowerCase() === "content-type")?.value ?? "";

  const variables = collectVariables([
    request.url,
    ...activeRows(request.params).map((row) => `${row.key} ${row.value}`),
    ...activeRows(request.headers).map((row) => `${row.key} ${row.value}`),
    ...activeRows(request.formBody).map((row) => `${row.key} ${row.value}`),
    TEXT_BODY_TYPES.includes(bodyType) ? request.body ?? "" : "",
    graphqlQuery,
    graphqlVariables,
    request.authType === "bearer" ? token : "",
  ]);

  return {
    vars: {
      requestName: stripHidden(request.name ?? "").trim(),
      method: sanitizedMethod(request.method),
      url: queryText ? `${urlBase}?${queryText}` : urlBase,
      urlBase,
      scheme,
      host,
      path,
      body,
      bodyType,
      contentType,
      graphqlQuery,
      graphqlVariables,
    },
    lists: { query, headers, formFields, variables },
    flags: {
      hasQuery: query.length > 0,
      hasHeaders: headers.length > 0,
      hasBody: body !== "",
      hasJsonBody: body !== "" && (bodyType === "json" || bodyType === "graphql"),
      hasFormBody: formFields.length > 0,
      hasBinaryBody: bodyType === "binary",
      hasGraphql: bodyType === "graphql",
      hasVariables: variables.length > 0,
    },
  };
}
