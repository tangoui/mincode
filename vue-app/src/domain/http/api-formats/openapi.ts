import type { HttpEnvironment, HttpRequestCollection, SavedHttpRequest } from "../request-collection";
import {
  bodyField,
  bodyText,
  createImportedRequest,
  emptyFolder,
  environment,
  isRecord,
  keyValueRow,
  normalizePathVariables,
  pruneFolder,
  rawBodyType,
  records,
  text,
  type IdFactory,
  type ImportedApiCollection,
  type ImportedApiFolder,
  type ImportedAuth,
  type ImportedRequestInput,
  type UnknownRecord,
} from "./shared";
import { exportRows } from "./export-safety";

const HTTP_METHODS = ["get", "put", "post", "delete", "patch", "options", "head", "trace"];
const MAX_REF_DEPTH = 16;

export function isOpenApiDocument(document: UnknownRecord) {
  return typeof document.openapi === "string" || typeof document.swagger === "string";
}

/** 解析文档内的 #/ 引用；外部引用与循环引用原样返回。 */
function createResolver(document: UnknownRecord) {
  const resolve = (value: unknown, depth = 0): unknown => {
    if (!isRecord(value) || typeof value.$ref !== "string" || depth > MAX_REF_DEPTH) return value;
    const ref = value.$ref;
    if (!ref.startsWith("#/")) return value;
    let target: unknown = document;
    for (const part of ref.slice(2).split("/")) {
      const key = part.replace(/~1/g, "/").replace(/~0/g, "~");
      target = isRecord(target) ? target[key] : undefined;
    }
    return target === undefined ? value : resolve(target, depth + 1);
  };
  return resolve;
}

/** 从 schema 生成示例值：优先 example/default/enum，对象与数组递归展开。 */
function sampleFromSchema(schema: unknown, resolve: (value: unknown) => unknown, depth = 0): unknown {
  const value = resolve(schema);
  if (!isRecord(value) || depth > 8) return undefined;
  if ("example" in value) return value.example;
  if (Array.isArray(value.examples) && value.examples.length) return value.examples[0];
  if ("default" in value) return value.default;
  if (Array.isArray(value.enum) && value.enum.length) return value.enum[0];
  const composite = [value.allOf, value.oneOf, value.anyOf].find(Array.isArray) as unknown[] | undefined;
  if (composite) {
    if (value.allOf) {
      const merged: UnknownRecord = {};
      for (const part of composite) {
        const sample = sampleFromSchema(part, resolve, depth + 1);
        if (isRecord(sample)) Object.assign(merged, sample);
      }
      return merged;
    }
    return sampleFromSchema(composite[0], resolve, depth + 1);
  }
  const type = Array.isArray(value.type) ? value.type.find((item) => item !== "null") : value.type;
  if (type === "object" || isRecord(value.properties)) {
    const result: UnknownRecord = {};
    for (const [key, property] of Object.entries(isRecord(value.properties) ? value.properties : {})) {
      result[key] = sampleFromSchema(property, resolve, depth + 1) ?? null;
    }
    return result;
  }
  if (type === "array") {
    const item = sampleFromSchema(value.items, resolve, depth + 1);
    return item === undefined ? [] : [item];
  }
  if (type === "integer" || type === "number") return 0;
  if (type === "boolean") return false;
  if (type === "string") return "";
  return undefined;
}

function parameterExample(parameter: UnknownRecord, resolve: (value: unknown) => unknown) {
  if ("example" in parameter) return parameter.example;
  if (isRecord(parameter.examples)) {
    const first = resolve(Object.values(parameter.examples)[0]);
    if (isRecord(first) && "value" in first) return first.value;
  }
  // Swagger 2.0 的参数直接带 type/default/enum。
  return sampleFromSchema(parameter.schema ?? parameter, resolve) ?? "";
}

function mediaExample(media: UnknownRecord, resolve: (value: unknown) => unknown) {
  if ("example" in media) return media.example;
  if (isRecord(media.examples)) {
    const first = resolve(Object.values(media.examples)[0]);
    if (isRecord(first) && "value" in first) return first.value;
  }
  return sampleFromSchema(media.schema, resolve);
}

function importBody(
  content: UnknownRecord,
  resolve: (value: unknown) => unknown,
  idFactory: IdFactory
): Partial<ImportedRequestInput> {
  const entries = Object.entries(content);
  if (!entries.length) return { bodyType: "none" };
  const [contentType, mediaValue] = entries.find(([type]) => type.includes("json")) ?? entries[0]!;
  const media = isRecord(mediaValue) ? mediaValue : {};
  if (contentType.includes("form-data") || contentType.includes("x-www-form-urlencoded")) {
    const schema = resolve(media.schema);
    const example = mediaExample(media, resolve);
    const properties = isRecord(schema) && isRecord(schema.properties) ? schema.properties : {};
    const formBody = Object.entries(properties).map(([key, propertyValue]) => {
      const property = resolve(propertyValue);
      const isFile = isRecord(property) && (property.format === "binary" || property.type === "file");
      const sample = isRecord(example) ? example[key] : sampleFromSchema(property, resolve);
      return bodyField(
        idFactory,
        key,
        isFile ? "" : sample,
        true,
        isFile ? "file" : "text",
        isRecord(property) ? text(property.description) : ""
      );
    });
    return { bodyType: contentType.includes("form-data") ? "form-data" : "x-www-form-urlencoded", formBody };
  }
  if (contentType.includes("octet-stream")) return { bodyType: "binary" };
  const example = mediaExample(media, resolve);
  return { bodyType: rawBodyType(contentType), body: bodyText(example) };
}

/** Swagger 2.0 的 body / formData 参数转换成 OpenAPI 3 的 requestBody.content。 */
function swaggerBodyContent(parameters: UnknownRecord[], consumes: string[]): UnknownRecord | undefined {
  const body = parameters.find((parameter) => parameter.in === "body");
  if (body) return { [consumes.find((type) => type.includes("json")) ?? consumes[0] ?? "application/json"]: { schema: body.schema } };
  const form = parameters.filter((parameter) => parameter.in === "formData");
  if (!form.length) return undefined;
  const properties = Object.fromEntries(
    form.map((parameter) => [text(parameter.name), { ...parameter, format: parameter.type === "file" ? "binary" : parameter.format }])
  );
  const type = form.some((parameter) => parameter.type === "file") || consumes.some((item) => item.includes("form-data"))
    ? "multipart/form-data"
    : "application/x-www-form-urlencoded";
  return { [type]: { schema: { type: "object", properties } } };
}

function importSecurity(
  requirements: unknown,
  schemes: UnknownRecord,
  resolve: (value: unknown) => unknown
): ImportedAuth | undefined {
  if (!Array.isArray(requirements)) return undefined;
  if (!requirements.length) return { type: "none" };
  for (const requirement of requirements) {
    if (!isRecord(requirement)) continue;
    for (const name of Object.keys(requirement)) {
      const scheme = resolve(schemes[name]);
      if (!isRecord(scheme)) continue;
      const type = text(scheme.type).toLowerCase();
      const httpScheme = text(scheme.scheme).toLowerCase();
      if ((type === "http" && httpScheme === "bearer") || type === "oauth2" || type === "openidconnect") {
        return { type: "bearer", token: "{{token}}" };
      }
      if ((type === "http" && httpScheme === "basic") || type === "basic") {
        return { type: "basic", username: "{{username}}", password: "{{password}}" };
      }
      if (type === "apikey") {
        return {
          type: "api-key",
          apiKeyName: text(scheme.name, "X-API-Key"),
          apiKeyValue: "{{apiKey}}",
          apiKeyAddTo: scheme.in === "query" ? "query" : "header",
        };
      }
    }
  }
  return undefined;
}

/** 目录：Apifox 导出的 x-apifox-folder（a/b/c）优先，其次第一个 tag。 */
function folderPath(operation: UnknownRecord) {
  const folder = text(operation["x-apifox-folder"]);
  if (folder) return folder.split("/").map((part) => part.trim()).filter(Boolean);
  const tag = Array.isArray(operation.tags) ? text(operation.tags[0]) : "";
  return tag ? tag.split("/").map((part) => part.trim()).filter(Boolean) : [];
}

function folderFor(root: ImportedApiFolder, path: string[]) {
  let folder = root;
  for (const name of path) {
    let child = folder.children.find((item) => item.name === name);
    if (!child) {
      child = emptyFolder(name);
      folder.children.push(child);
    }
    folder = child;
  }
  return folder;
}

function serverUrls(document: UnknownRecord) {
  if (typeof document.swagger === "string") {
    const host = text(document.host);
    if (!host) return [text(document.basePath)].filter(Boolean);
    const schemes = Array.isArray(document.schemes) && document.schemes.length ? document.schemes.map(String) : ["https"];
    return [`${schemes[0]}://${host}${text(document.basePath)}`.replace(/\/$/, "")];
  }
  return records(document.servers).map((server) => {
    let url = text(server.url);
    const variables = isRecord(server.variables) ? server.variables : {};
    for (const [name, variable] of Object.entries(variables)) {
      url = url.split(`{${name}}`).join(isRecord(variable) ? text(variable.default) : "");
    }
    return url.replace(/\/$/, "");
  }).filter(Boolean);
}

export function importOpenApi(
  document: UnknownRecord,
  idFactory: IdFactory,
  options: { now: number; environmentName: string }
): ImportedApiCollection {
  const swagger = typeof document.swagger === "string";
  if (swagger ? !text(document.swagger).startsWith("2.") : !text(document.openapi).startsWith("3.")) {
    throw new Error("unsupported_openapi_version");
  }
  const resolve = createResolver(document);
  const info = isRecord(document.info) ? document.info : {};
  const components = isRecord(document.components) ? document.components : {};
  const schemes = swagger
    ? (isRecord(document.securityDefinitions) ? document.securityDefinitions : {})
    : (isRecord(components.securitySchemes) ? components.securitySchemes : {});
  const globalAuth = importSecurity(document.security, schemes, resolve);
  const globalConsumes = Array.isArray(document.consumes) ? document.consumes.map(String) : [];
  const servers = serverUrls(document);
  // 有服务器地址时用 {{baseUrl}}，并写入导入的环境，切换环境即可换服务器。
  const urlPrefix = servers.length ? "{{baseUrl}}" : "";
  const root = emptyFolder(text(info.title, "Imported API"));
  const paths = isRecord(document.paths) ? document.paths : {};

  for (const [path, pathValue] of Object.entries(paths)) {
    const pathItem = resolve(pathValue);
    if (!isRecord(pathItem)) continue;
    const shared = records(pathItem.parameters).map((item) => resolve(item)).filter(isRecord);
    for (const method of HTTP_METHODS) {
      const operation = pathItem[method];
      if (!isRecord(operation)) continue;
      const own = records(operation.parameters).map((item) => resolve(item)).filter(isRecord);
      const overridden = new Set(own.map((item) => `${text(item.in)}:${text(item.name)}`));
      const parameters = [...shared.filter((item) => !overridden.has(`${text(item.in)}:${text(item.name)}`)), ...own];
      const params = [];
      const headers = [];
      const pathValues = new Map<string, string>();
      for (const parameter of parameters) {
        const name = text(parameter.name);
        if (!name) continue;
        const example = parameterExample(parameter, resolve);
        const description = text(parameter.description);
        if (parameter.in === "query") params.push(keyValueRow(idFactory, "param", name, example, true, description));
        if (parameter.in === "header") headers.push(keyValueRow(idFactory, "header", name, example, true, description));
        if (parameter.in === "path" && example !== "" && example !== undefined) pathValues.set(name, String(example));
      }
      const content = swagger
        ? swaggerBodyContent(parameters, Array.isArray(operation.consumes) ? operation.consumes.map(String) : globalConsumes)
        : (() => {
            const requestBody = resolve(operation.requestBody);
            return isRecord(requestBody) && isRecord(requestBody.content) ? requestBody.content : undefined;
          })();
      const documentation = [text(operation.description)].filter(Boolean).join("\n\n");
      folderFor(root, folderPath(operation)).requests.push(
        createImportedRequest(
          {
            name: text(operation.summary, text(operation.operationId, `${method.toUpperCase()} ${path}`)),
            method,
            url: `${urlPrefix}${normalizePathVariables(path, pathValues)}`,
            params,
            headers,
            ...(content ? importBody(content, resolve, idFactory) : { bodyType: "none" as const }),
            auth: importSecurity(operation.security, schemes, resolve) ?? globalAuth,
            documentation,
          },
          idFactory,
          options.now
        )
      );
    }
  }
  const environments: HttpEnvironment[] = servers.map((server, index) =>
    environment(idFactory, servers.length > 1 ? `${options.environmentName} ${index + 1}` : options.environmentName, [["baseUrl", server]])
  );
  return { ...pruneFolder(root), source: swagger ? "swagger" : "openapi", environments };
}

export interface OpenApiExportDocument {
  openapi: string;
  info: { title: string; version: string };
  servers?: Array<{ url: string }>;
  tags?: Array<{ name: string }>;
  paths: Record<string, Record<string, unknown>>;
  components?: { securitySchemes: Record<string, unknown> };
}

function parseRequestUrl(raw: string, variables: ReadonlyMap<string, string>) {
  // {{baseUrl}}/users 这类地址用当前环境展开出服务器部分。
  const expanded = raw.replace(/^\{\{\s*([^{}\s]+)\s*\}\}/, (match, name: string) => variables.get(name) ?? match);
  const withoutQuery = expanded.split(/[?#]/)[0] ?? "";
  try {
    const url = new URL(withoutQuery);
    return { server: url.origin, path: decodeURI(url.pathname) || "/" };
  } catch {
    const leading = /^\{\{[^}]+\}\}/.exec(withoutQuery);
    const path = leading ? withoutQuery.slice(leading[0].length) : withoutQuery;
    return { server: leading ? leading[0] : "", path: path.startsWith("/") ? path : `/${path}` };
  }
}

const SCHEMA_TYPES: Record<string, string> = { integer: "integer", number: "number", boolean: "boolean", array: "array", object: "object" };

function schemaOf(value: string, valueType?: string) {
  return { type: SCHEMA_TYPES[valueType ?? ""] ?? "string", ...(value ? { example: value } : {}) };
}

function exportBody(request: SavedHttpRequest) {
  const bodyType = request.bodyType ?? (request.body ? "raw" : "none");
  const fields = (request.formBody ?? []).filter((field) => field.key.trim() && field.enabled);
  const formContent = (type: string) => ({
    content: {
      [type]: {
        schema: {
          type: "object",
          properties: Object.fromEntries(
            fields.map((field) => [
              field.key,
              field.fieldType === "file"
                ? { type: "string", format: "binary", ...(field.description ? { description: field.description } : {}) }
                : { ...schemaOf(field.value, field.fieldType), ...(field.description ? { description: field.description } : {}) },
            ])
          ),
        },
      },
    },
  });
  switch (bodyType) {
    case "none":
      return undefined;
    case "form-data":
      return formContent("multipart/form-data");
    case "x-www-form-urlencoded":
      return formContent("application/x-www-form-urlencoded");
    case "binary":
      return { content: { "application/octet-stream": { schema: { type: "string", format: "binary" } } } };
    case "graphql": {
      let variables: unknown;
      try {
        variables = JSON.parse(request.graphqlVariables || "{}");
      } catch {
        variables = request.graphqlVariables;
      }
      return {
        content: { "application/json": { schema: { type: "object" }, example: { query: request.graphqlQuery ?? "", variables } } },
      };
    }
    default: {
      if (!request.body.trim()) return undefined;
      const contentType = {
        json: "application/json",
        xml: "application/xml",
        html: "text/html",
        javascript: "application/javascript",
      }[bodyType as string] ?? "text/plain";
      let example: unknown = request.body;
      if (bodyType === "json" || bodyType === "raw") {
        try {
          example = JSON.parse(request.body);
          return { content: { "application/json": { schema: { type: "object" }, example } } };
        } catch {
          // 非 JSON 文本按原文导出。
        }
      }
      return { content: { [contentType]: { schema: { type: "string" }, example } } };
    }
  }
}

const SECURITY_SCHEMES: Record<string, { name: string; scheme: (request: SavedHttpRequest) => unknown }> = {
  bearer: { name: "bearerAuth", scheme: () => ({ type: "http", scheme: "bearer" }) },
  oauth2: { name: "bearerAuth", scheme: () => ({ type: "http", scheme: "bearer" }) },
  basic: { name: "basicAuth", scheme: () => ({ type: "http", scheme: "basic" }) },
  "api-key": {
    name: "apiKeyAuth",
    scheme: (request) => ({ type: "apiKey", name: request.apiKeyName || "X-API-Key", in: request.apiKeyAddTo === "query" ? "query" : "header" }),
  },
};

export function exportOpenApiDocument(
  collection: HttpRequestCollection,
  activeEnvironment?: HttpEnvironment
): OpenApiExportDocument {
  const variables = new Map(
    (activeEnvironment?.vars ?? []).filter((row) => row.enabled && row.key.trim()).map((row) => [row.key.trim(), row.value])
  );
  const servers = new Set<string>();
  const tags = new Set<string>();
  const paths: OpenApiExportDocument["paths"] = {};
  const securitySchemes: Record<string, unknown> = {};

  const visit = (folder: HttpRequestCollection, trail: string[]) => {
    for (const request of folder.requests) {
      const { server, path: rawPath } = parseRequestUrl(request.url, variables);
      if (server) servers.add(server);
      // {{id}} 路径变量导出为 OpenAPI 的 {id}。
      const pathNames: string[] = [];
      const path = rawPath.replace(/\{\{\s*([^{}\s]+)\s*\}\}/g, (_, name: string) => {
        pathNames.push(name);
        return `{${name}}`;
      });
      const tag = trail.join("/");
      if (tag) tags.add(tag);
      const parameters = [
        ...pathNames.map((name) => ({ name, in: "path", required: true, schema: { type: "string" } })),
        ...exportRows(request.params.filter((row) => row.key.trim()), false).map((row) => ({
          name: row.key,
          in: "query",
          required: false,
          ...(row.description ? { description: row.description } : {}),
          schema: schemaOf(row.value, row.valueType),
        })),
        ...exportRows(request.headers.filter((row) => row.key.trim()), true)
          .filter((row) => !["authorization", "content-type", "accept"].includes(row.key.trim().toLowerCase()))
          .map((row) => ({
            name: row.key,
            in: "header",
            required: false,
            ...(row.description ? { description: row.description } : {}),
            schema: schemaOf(row.value),
          })),
      ];
      const security = SECURITY_SCHEMES[request.authType];
      if (security) securitySchemes[security.name] = security.scheme(request);
      const requestBody = exportBody(request);
      const method = request.method.toLowerCase();
      paths[path] ??= {};
      // 同一路径同一方法只能有一个操作，后出现的保留在 x-mincode-duplicates 里不丢失。
      const operation = {
        summary: request.name,
        ...(request.documentation ? { description: request.documentation } : {}),
        ...(tag ? { tags: [tag], "x-apifox-folder": tag } : {}),
        ...(parameters.length ? { parameters } : {}),
        ...(requestBody ? { requestBody } : {}),
        ...(security ? { security: [{ [security.name]: [] }] } : {}),
        responses: { "200": { description: "OK" } },
      };
      if (paths[path]![method]) {
        const existing = paths[path]![method] as Record<string, unknown>;
        existing["x-mincode-duplicates"] = [...((existing["x-mincode-duplicates"] as unknown[]) ?? []), operation];
      } else {
        paths[path]![method] = operation;
      }
    }
    for (const child of folder.children ?? []) visit(child, [...trail, child.name]);
  };
  visit(collection, []);
  return {
    openapi: "3.0.3",
    info: { title: collection.name, version: "1.0.0" },
    ...(servers.size ? { servers: [...servers].map((url) => ({ url })) } : {}),
    ...(tags.size ? { tags: [...tags].map((name) => ({ name })) } : {}),
    paths,
    ...(Object.keys(securitySchemes).length ? { components: { securitySchemes } } : {}),
  };
}
