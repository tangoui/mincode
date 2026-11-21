import type {
  HttpAuthType,
  HttpBodyField,
  HttpBodyFieldType,
  HttpBodyType,
  HttpEnvironment,
  HttpKeyValueRow,
  HttpRequestCollection,
  SavedHttpRequest,
} from "../request-collection";

export type IdFactory = (prefix: string) => string;
export type UnknownRecord = Record<string, unknown>;

export type ApiImportSource = "openapi" | "swagger" | "postman" | "apifox" | "apipost";

/** 导入结果：一棵目录树加上环境，统一转换后再写入集合。 */
export interface ImportedApiFolder {
  name: string;
  requests: SavedHttpRequest[];
  children: ImportedApiFolder[];
}

export interface ImportedApiCollection extends ImportedApiFolder {
  source: ApiImportSource;
  environments: HttpEnvironment[];
}

/** 只防御构造出的异常嵌套导致栈溢出，正常项目不会触及。 */
export const IMPORT_MAX_DEPTH = 256;

export interface ImportedAuth {
  type: HttpAuthType;
  token?: string;
  username?: string;
  password?: string;
  apiKeyName?: string;
  apiKeyValue?: string;
  apiKeyAddTo?: "header" | "query";
}

export interface ImportedRequestInput {
  name: string;
  method: string;
  url: string;
  params?: HttpKeyValueRow[];
  headers?: HttpKeyValueRow[];
  bodyType?: HttpBodyType;
  body?: string;
  formBody?: HttpBodyField[];
  graphqlQuery?: string;
  graphqlVariables?: string;
  auth?: ImportedAuth;
  documentation?: string;
}

export function isRecord(value: unknown): value is UnknownRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function text(value: unknown, fallback = ""): string {
  return typeof value === "string" ? value : fallback;
}

export function records(value: unknown): UnknownRecord[] {
  return Array.isArray(value) ? value.filter(isRecord) : [];
}

export function valueString(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return JSON.stringify(value);
}

/** 文本或对象统一成可编辑文本；对象按缩进 JSON 输出。 */
export function bodyText(value: unknown): string {
  if (value === undefined || value === null) return "";
  return typeof value === "string" ? value : JSON.stringify(value, null, 2);
}

export function keyValueRow(
  idFactory: IdFactory,
  prefix: string,
  key: string,
  value: unknown,
  enabled = true,
  description = ""
): HttpKeyValueRow {
  return { id: idFactory(prefix), key, value: valueString(value), enabled, description };
}

export function bodyField(
  idFactory: IdFactory,
  key: string,
  value: unknown,
  enabled: boolean,
  fieldType: HttpBodyFieldType = "text",
  description = ""
): HttpBodyField {
  // 文件字段只保留文件名：导入的文件路径来自别的机器，不能直接读取。
  return {
    id: idFactory("body"),
    key,
    value: fieldType === "file" ? "" : valueString(value),
    enabled,
    fieldType,
    description,
    ...(fieldType === "file" && valueString(value) ? { fileName: valueString(value).split(/[\\/]/).pop() } : {}),
  };
}

/** 各工具的 Content-Type / raw 语言映射到本地请求体类型。 */
export function rawBodyType(hint: string): HttpBodyType {
  const lower = hint.toLowerCase();
  if (lower.includes("json")) return "json";
  if (lower.includes("xml")) return "xml";
  if (lower.includes("html")) return "html";
  if (lower.includes("javascript")) return "javascript";
  if (lower.includes("text") || lower === "plain") return "text";
  return "raw";
}

export function createImportedRequest(
  input: ImportedRequestInput,
  idFactory: IdFactory,
  now: number
): SavedHttpRequest {
  const auth = input.auth ?? { type: "none" };
  const bodyType = input.bodyType ?? (input.body ? "raw" : "none");
  return {
    id: idFactory("saved-request"),
    name: input.name.trim() || `${input.method.toUpperCase()} ${input.url}`,
    method: (input.method || "GET").toUpperCase(),
    url: input.url,
    params: input.params ?? [],
    headers: input.headers ?? [],
    body: input.body ?? "",
    bodyType,
    formBody: input.formBody ?? [],
    binaryFilePath: "",
    binaryFileName: "",
    authType: auth.type,
    authToken: auth.token ?? "",
    authUsername: auth.username ?? "",
    authPassword: auth.password ?? "",
    apiKeyName: auth.apiKeyName ?? "",
    apiKeyValue: auth.apiKeyValue ?? "",
    apiKeyAddTo: auth.apiKeyAddTo ?? "header",
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
    graphqlQuery: input.graphqlQuery ?? "",
    graphqlVariables: input.graphqlVariables ?? "",
    documentation: input.documentation ?? "",
    environmentMode: "follow",
    updatedAt: now,
  };
}

export function emptyFolder(name: string): ImportedApiFolder {
  return { name, requests: [], children: [] };
}

/** 去掉空目录，并把只有一个同名根目录的情况展平。 */
export function pruneFolder(folder: ImportedApiFolder): ImportedApiFolder {
  const children = folder.children.map(pruneFolder).filter((child) => child.requests.length || child.children.length);
  return { ...folder, children };
}

export function countImportedRequests(folder: ImportedApiFolder): number {
  return folder.requests.length + folder.children.reduce((sum, child) => sum + countImportedRequests(child), 0);
}

export function environment(
  idFactory: IdFactory,
  name: string,
  vars: Array<[string, unknown, boolean?, string?]>
): HttpEnvironment {
  return {
    id: idFactory("environment"),
    name: name.trim() || "Imported",
    vars: vars
      .filter(([key]) => key.trim())
      .map(([key, value, enabled = true, description = ""]) =>
        keyValueRow(idFactory, "kv", key.trim(), value, enabled, description)
      ),
  };
}

/** `:id`、`{id}` 等路径变量统一成 MinCode 的 `{{id}}`，已有示例值时直接代入。 */
export function normalizePathVariables(url: string, values: ReadonlyMap<string, string> = new Map()) {
  const replace = (name: string) => values.get(name) || `{{${name}}}`;
  return url
    .replace(/(^|[^{])\{([A-Za-z_][\w.-]*)\}(?!\})/g, (_, prefix: string, name: string) => `${prefix}${replace(name)}`)
    .replace(/(\/):([A-Za-z_][\w-]*)/g, (_, slash: string, name: string) => `${slash}${replace(name)}`);
}

export function joinUrl(base: string, path: string) {
  if (!base) return path;
  if (/^(https?:)?\/\//i.test(path) || path.startsWith("{{")) return path;
  return `${base.replace(/\/+$/, "")}/${path.replace(/^\/+/, "")}`;
}

/** 导入结果转为集合树（顶级集合持有环境）。 */
export function importedFolderToCollection(folder: ImportedApiFolder, idFactory: IdFactory): HttpRequestCollection {
  return {
    id: idFactory("collection"),
    name: folder.name,
    requests: folder.requests,
    children: folder.children.map((child) => importedFolderToCollection(child, idFactory)),
  };
}
