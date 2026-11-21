import type { HttpBodyField, HttpEnvironment, HttpRequestCollection, SavedHttpRequest } from "../request-collection";
import {
  bodyField,
  bodyText,
  createImportedRequest,
  emptyFolder,
  environment,
  IMPORT_MAX_DEPTH,
  isRecord,
  keyValueRow,
  normalizePathVariables,
  pruneFolder,
  rawBodyType,
  records,
  text,
  valueString,
  type IdFactory,
  type ImportedApiCollection,
  type ImportedApiFolder,
  type ImportedAuth,
  type ImportedRequestInput,
  type UnknownRecord,
} from "./shared";
import { exportAuthValue, exportRows, splitUrlQuery } from "./export-safety";

const POSTMAN_SCHEMA_V21 = "https://schema.getpostman.com/json/collection/v2.1.0/collection.json";

export function isPostmanCollection(document: UnknownRecord) {
  const info = isRecord(document.info) ? document.info : undefined;
  return Boolean(info && Array.isArray(document.item) && /collection\/v2\.[01]\.0/.test(text(info.schema)));
}

/** Postman v2.0 的认证参数是对象，v2.1 是 [{key, value}] 数组。 */
function authParam(auth: UnknownRecord, type: string, key: string): string {
  const params = auth[type];
  if (Array.isArray(params)) {
    const entry = params.find((item) => isRecord(item) && item.key === key);
    return isRecord(entry) ? valueString(entry.value) : "";
  }
  return isRecord(params) ? valueString(params[key]) : "";
}

/** 返回 undefined 表示继承上级认证。 */
function importAuth(value: unknown): ImportedAuth | undefined {
  if (!isRecord(value)) return undefined;
  const type = text(value.type);
  switch (type) {
    case "inherit":
      return undefined;
    case "bearer":
      return { type: "bearer", token: authParam(value, "bearer", "token") };
    case "basic":
      return {
        type: "basic",
        username: authParam(value, "basic", "username"),
        password: authParam(value, "basic", "password"),
      };
    case "apikey":
      return {
        type: "api-key",
        apiKeyName: authParam(value, "apikey", "key"),
        apiKeyValue: authParam(value, "apikey", "value"),
        apiKeyAddTo: authParam(value, "apikey", "in") === "query" ? "query" : "header",
      };
    default:
      // noauth 以及暂不支持的 digest、oauth1、oauth2、hawk 等都按无认证导入。
      return { type: "none" };
  }
}

function description(value: unknown) {
  return isRecord(value) ? text(value.content) : text(value);
}

function importUrl(value: unknown, idFactory: IdFactory) {
  if (typeof value === "string") {
    const { base, query } = splitUrlQuery(value);
    return {
      url: normalizePathVariables(base),
      params: query.map(([key, item]) => keyValueRow(idFactory, "param", key, item)),
    };
  }
  if (!isRecord(value)) return { url: "", params: [] };
  let raw = text(value.raw);
  if (!raw) {
    const host = Array.isArray(value.host) ? value.host.join(".") : text(value.host);
    const path = Array.isArray(value.path) ? value.path.map(valueString).join("/") : text(value.path);
    const protocol = text(value.protocol);
    raw = `${protocol ? `${protocol}://` : ""}${host}${path ? `/${path}` : ""}`;
  }
  const { base, query } = splitUrlQuery(raw);
  const variables = new Map(
    records(value.variable)
      .filter((item) => text(item.key) && valueString(item.value))
      .map((item) => [text(item.key), valueString(item.value)])
  );
  const params = Array.isArray(value.query)
    ? records(value.query)
        .filter((item) => text(item.key))
        .map((item) =>
          keyValueRow(idFactory, "param", text(item.key), item.value, item.disabled !== true, description(item.description))
        )
    : query.map(([key, item]) => keyValueRow(idFactory, "param", key, item));
  return { url: normalizePathVariables(base, variables), params };
}

function importBody(value: unknown, idFactory: IdFactory): Partial<ImportedRequestInput> {
  if (!isRecord(value) || value.disabled === true) return { bodyType: "none" };
  const mode = text(value.mode);
  if (mode === "raw") {
    const options = isRecord(value.options) && isRecord(value.options.raw) ? value.options.raw : {};
    const raw = text(value.raw);
    let bodyType = rawBodyType(text(options.language, "text"));
    if (!text(options.language) && /^\s*[[{]/.test(raw)) bodyType = "json";
    return { bodyType, body: raw };
  }
  if (mode === "urlencoded" || mode === "formdata") {
    const formBody: HttpBodyField[] = records(value[mode])
      .filter((item) => text(item.key))
      .map((item) => {
        const isFile = mode === "formdata" && item.type === "file";
        const source = Array.isArray(item.src) ? item.src[0] : item.src;
        const field = bodyField(
          idFactory,
          text(item.key),
          isFile ? source : item.value,
          item.disabled !== true,
          isFile ? "file" : "text",
          description(item.description)
        );
        return text(item.contentType) ? { ...field, contentType: text(item.contentType) } : field;
      });
    return { bodyType: mode === "formdata" ? "form-data" : "x-www-form-urlencoded", formBody };
  }
  if (mode === "graphql" && isRecord(value.graphql)) {
    return {
      bodyType: "graphql",
      graphqlQuery: text(value.graphql.query),
      graphqlVariables: bodyText(value.graphql.variables),
    };
  }
  if (mode === "file") return { bodyType: "binary" };
  return { bodyType: "none" };
}

export function importPostman(
  document: UnknownRecord,
  idFactory: IdFactory,
  options: { now: number; environmentName: string }
): ImportedApiCollection {
  const info = isRecord(document.info) ? document.info : {};
  const walk = (items: unknown, folder: ImportedApiFolder, inherited: ImportedAuth | undefined, depth: number) => {
    if (depth > IMPORT_MAX_DEPTH) throw new Error("api_document_too_deep");
    for (const item of records(items)) {
      if (Array.isArray(item.item)) {
        const child = emptyFolder(text(item.name, "Folder"));
        walk(item.item, child, importAuth(item.auth) ?? inherited, depth + 1);
        folder.children.push(child);
        continue;
      }
      const request = typeof item.request === "string" ? { url: item.request, method: "GET" } : item.request;
      if (!isRecord(request)) continue;
      const { url, params } = importUrl(request.url, idFactory);
      folder.requests.push(
        createImportedRequest(
          {
            name: text(item.name),
            method: text(request.method, "GET"),
            url,
            params,
            headers: records(request.header)
              .filter((row) => text(row.key))
              .map((row) =>
                keyValueRow(idFactory, "header", text(row.key), row.value, row.disabled !== true, description(row.description))
              ),
            ...importBody(request.body, idFactory),
            auth: importAuth(request.auth) ?? inherited,
            documentation: description(request.description) || description(item.description),
          },
          idFactory,
          options.now
        )
      );
    }
  };
  const root = emptyFolder(text(info.name, "Postman Collection"));
  walk(document.item, root, importAuth(document.auth), 0);
  const variables = records(document.variable)
    .filter((item) => text(item.key))
    .map((item): [string, unknown, boolean, string] => [text(item.key), item.value, item.disabled !== true, description(item.description)]);
  return {
    ...pruneFolder(root),
    source: "postman",
    environments: variables.length ? [environment(idFactory, options.environmentName, variables)] : [],
  };
}

/** Postman 环境文件（_postman_variable_scope: environment / globals）。 */
export function isPostmanEnvironment(document: UnknownRecord) {
  return Array.isArray(document.values) && typeof document._postman_variable_scope === "string";
}

export function importPostmanEnvironment(document: UnknownRecord, idFactory: IdFactory): HttpEnvironment {
  return environment(
    idFactory,
    text(document.name, "Postman"),
    records(document.values)
      .filter((item) => text(item.key))
      .map((item): [string, unknown, boolean] => [text(item.key), item.value, item.enabled !== false])
  );
}

function exportAuth(request: SavedHttpRequest) {
  switch (request.authType) {
    case "bearer":
      return { type: "bearer", bearer: [{ key: "token", value: exportAuthValue(request.authToken), type: "string" }] };
    case "basic":
      return {
        type: "basic",
        basic: [
          { key: "username", value: request.authUsername ?? "", type: "string" },
          { key: "password", value: exportAuthValue(request.authPassword), type: "string" },
        ],
      };
    case "api-key":
      return {
        type: "apikey",
        apikey: [
          { key: "key", value: request.apiKeyName ?? "", type: "string" },
          { key: "value", value: exportAuthValue(request.apiKeyValue), type: "string" },
          { key: "in", value: request.apiKeyAddTo === "query" ? "query" : "header", type: "string" },
        ],
      };
    case "oauth2":
      return {
        type: "oauth2",
        oauth2: [{ key: "accessToken", value: exportAuthValue(request.oauth2AccessToken), type: "string" }],
      };
    default:
      return { type: "noauth" };
  }
}

const RAW_LANGUAGES: Record<string, string> = { json: "json", xml: "xml", html: "html", javascript: "javascript" };

function exportBody(request: SavedHttpRequest) {
  const bodyType = request.bodyType ?? (request.body ? "raw" : "none");
  const fields = (request.formBody ?? []).filter((field) => field.key.trim());
  switch (bodyType) {
    case "none":
      return undefined;
    case "x-www-form-urlencoded":
      return {
        mode: "urlencoded",
        urlencoded: fields.map((field) => ({
          key: field.key,
          value: field.value,
          ...(field.enabled ? {} : { disabled: true }),
          ...(field.description ? { description: field.description } : {}),
        })),
      };
    case "form-data":
      return {
        mode: "formdata",
        formdata: fields.map((field) => ({
          key: field.key,
          ...(field.fieldType === "file"
            ? { type: "file", src: field.fileName ?? "" }
            : { type: "text", value: field.value }),
          ...(field.enabled ? {} : { disabled: true }),
          ...(field.contentType ? { contentType: field.contentType } : {}),
          ...(field.description ? { description: field.description } : {}),
        })),
      };
    case "graphql":
      return { mode: "graphql", graphql: { query: request.graphqlQuery ?? "", variables: request.graphqlVariables ?? "" } };
    case "binary":
      return { mode: "file", file: {} };
    default:
      return {
        mode: "raw",
        raw: request.body,
        ...(RAW_LANGUAGES[bodyType] ? { options: { raw: { language: RAW_LANGUAGES[bodyType] } } } : {}),
      };
  }
}

/** Postman 官方导出同时带 raw 与 protocol/host/path；SDK 和部分导入器只读拆分后的字段。 */
function urlParts(url: string) {
  const base = splitUrlQuery(url).base;
  const match = /^(?:([a-z][a-z0-9+.-]*):\/\/)?([^/]*)(\/.*)?$/i.exec(base);
  if (!match || !match[2]) return {};
  const [, protocol, authority = "", path = ""] = match;
  const hostname = authority.startsWith("{{") ? authority : authority.split(":")[0]!;
  const port = authority.startsWith("{{") ? "" : authority.split(":")[1] ?? "";
  return {
    ...(protocol ? { protocol } : {}),
    host: hostname.startsWith("{{") ? [hostname] : hostname.split("."),
    ...(port ? { port } : {}),
    path: path.split("/").filter(Boolean),
  };
}

/** Postman URL 中的 {{var}} 主机与 `{{id}}` 路径变量原样保留，Apifox / Apipost 导入时同样识别。 */
function exportRequest(request: SavedHttpRequest) {
  const query = exportRows(request.params.filter((row) => row.key.trim()), false);
  const enabledQuery = query.filter((row) => row.enabled).map((row) => `${row.key}=${row.value}`);
  const raw = enabledQuery.length ? `${request.url}${request.url.includes("?") ? "&" : "?"}${enabledQuery.join("&")}` : request.url;
  const body = exportBody(request);
  return {
    name: request.name,
    request: {
      method: request.method.toUpperCase(),
      auth: exportAuth(request),
      header: exportRows(request.headers, true).map((row) => ({
        key: row.key,
        value: row.value,
        ...(row.enabled ? {} : { disabled: true }),
        ...(row.description ? { description: row.description } : {}),
      })),
      ...(body ? { body } : {}),
      url: {
        raw,
        ...urlParts(request.url),
        query: query.map((row) => ({
          key: row.key,
          value: row.value,
          ...(row.enabled ? {} : { disabled: true }),
          ...(row.description ? { description: row.description } : {}),
        })),
      },
      ...(request.documentation ? { description: request.documentation } : {}),
    },
  };
}

function exportFolder(collection: HttpRequestCollection): unknown[] {
  return [
    ...(collection.children ?? []).map((child) => ({ name: child.name, item: exportFolder(child) })),
    ...collection.requests.map(exportRequest),
  ];
}

export function exportPostmanCollection(collection: HttpRequestCollection, activeEnvironment?: HttpEnvironment) {
  const variables = exportRows(activeEnvironment?.vars ?? [], false).filter((row) => row.key.trim());
  return {
    info: { name: collection.name, schema: POSTMAN_SCHEMA_V21 },
    item: exportFolder(collection),
    ...(variables.length
      ? { variable: variables.map((row) => ({ key: row.key, value: row.value, ...(row.enabled ? {} : { disabled: true }) })) }
      : {}),
  };
}
