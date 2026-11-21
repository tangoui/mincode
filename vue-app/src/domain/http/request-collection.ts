export type HttpParameterType =
  | "string"
  | "integer"
  | "number"
  | "boolean"
  | "array"
  | "object"
  | "file";

export interface HttpKeyValueRow {
  id: string;
  key: string;
  value: string;
  enabled: boolean;
  valueType?: HttpParameterType;
  description?: string;
  fileName?: string;
}

export type HttpAuthType = "none" | "bearer" | "basic" | "api-key" | "oauth2";
export type HttpApiKeyLocation = "header" | "query";
export type HttpOAuth2GrantType =
  | "authorization-code"
  | "client-credentials"
  | "password"
  | "implicit";
export type HttpBodyType =
  | "none"
  | "json"
  | "xml"
  | "text"
  | "html"
  | "javascript"
  | "form-data"
  | "x-www-form-urlencoded"
  | "graphql"
  | "binary"
  | "raw";
export type HttpBodyFieldType =
  | "text"
  | "integer"
  | "number"
  | "boolean"
  | "array"
  | "object"
  | "file";

export interface HttpBodyField extends HttpKeyValueRow {
  fieldType: HttpBodyFieldType;
  fileName?: string;
  contentType?: string;
}

export type HttpRequestEnvironmentMode = "follow" | "fixed";

export interface HttpRequestDraft {
  id: string;
  name: string;
  method: string;
  url: string;
  params: HttpKeyValueRow[];
  headers: HttpKeyValueRow[];
  body: string;
  bodyType?: HttpBodyType;
  formBody?: HttpBodyField[];
  binaryFilePath?: string;
  binaryFileName?: string;
  authType: HttpAuthType;
  authToken: string;
  authUsername?: string;
  authPassword?: string;
  apiKeyName?: string;
  apiKeyValue?: string;
  apiKeyAddTo?: HttpApiKeyLocation;
  oauth2GrantType?: HttpOAuth2GrantType;
  oauth2AuthUrl?: string;
  oauth2TokenUrl?: string;
  oauth2ClientId?: string;
  oauth2ClientSecret?: string;
  oauth2Scope?: string;
  oauth2CallbackUrl?: string;
  oauth2Username?: string;
  oauth2Password?: string;
  oauth2AccessToken?: string;
  graphqlQuery?: string;
  graphqlVariables?: string;
  documentation: string;
  /** 默认跟随所属顶级集合；固定模式仅在 environmentId 有效时生效。 */
  environmentMode?: HttpRequestEnvironmentMode;
  environmentId?: string;
}

export interface SavedHttpRequest extends Omit<HttpRequestDraft, "id" | "authToken"> {
  id: string;
  /** Kept optional while restoring collections saved before auth persistence existed. */
  authToken?: string;
  updatedAt: number;
}

export interface HttpEnvironment {
  id: string;
  name: string;
  vars: HttpKeyValueRow[];
}

export interface HttpRequestCollection {
  id: string;
  name: string;
  requests: SavedHttpRequest[];
  children?: HttpRequestCollection[];
  /** 仅顶级集合持有；子集合与请求共享所属顶级集合的环境。 */
  environments?: HttpEnvironment[];
  activeEnvironmentId?: string;
}

export const COMMON_HTTP_HEADER_NAMES = [
  "Accept",
  "Accept-Encoding",
  "Accept-Language",
  "Authorization",
  "Cache-Control",
  "Connection",
  "Content-Type",
  "Content-Length",
  "Content-Encoding",
  "Cookie",
  "Host",
  "If-Match",
  "If-Modified-Since",
  "If-None-Match",
  "Origin",
  "Pragma",
  "Referer",
  "User-Agent",
  "X-API-Key",
  "X-Request-ID",
] as const;

export interface HttpCollectionFilterResult {
  collections: HttpRequestCollection[];
  /** 命中链路上的集合 id，供 UI 在搜索时自动展开。 */
  expandedIds: Set<string>;
}

/**
 * 按集合名 / 请求名 / URL / 方法过滤集合树。空输入原样返回；
 * 集合名命中时保留整个子树，命中链路上的集合 id 进 expandedIds。
 */
export function filterHttpCollections(
  collections: HttpRequestCollection[],
  rawQuery: string
): HttpCollectionFilterResult {
  const query = rawQuery.trim().toLowerCase();
  if (!query) return { collections, expandedIds: new Set() };
  const expandedIds = new Set<string>();

  const requestMatches = (request: SavedHttpRequest) =>
    request.name.toLowerCase().includes(query) ||
    request.url.toLowerCase().includes(query) ||
    request.method.toLowerCase().includes(query);

  // 名称命中时整棵子树保留并全部展开。
  const expandBranch = (collection: HttpRequestCollection) => {
    for (const child of collection.children ?? []) {
      expandedIds.add(collection.id);
      expandBranch(child);
    }
  };

  function filterCollection(
    collection: HttpRequestCollection
  ): HttpRequestCollection | null {
    if (collection.name.toLowerCase().includes(query)) {
      // 名称命中：整棵子树保留；自身必须展开，否则命中内容藏在折叠态里不可见。
      expandedIds.add(collection.id);
      expandBranch(collection);
      return collection;
    }
    const requests = collection.requests.filter(requestMatches);
    const children: HttpRequestCollection[] = [];
    for (const child of collection.children ?? []) {
      const filtered = filterCollection(child);
      if (filtered) children.push(filtered);
    }
    if (requests.length === 0 && children.length === 0) return null;
    // 直属请求命中或有命中后代时同样要展开，保证命中项可见。
    expandedIds.add(collection.id);
    return { ...collection, requests, children };
  }

  const filtered: HttpRequestCollection[] = [];
  for (const collection of collections) {
    const result = filterCollection(collection);
    if (result) filtered.push(result);
  }
  return { collections: filtered, expandedIds };
}

export function filterHttpHeaderNames(query: string): string[] {
  const normalized = query.trim().toLowerCase();
  if (!normalized) return [...COMMON_HTTP_HEADER_NAMES];
  return COMMON_HTTP_HEADER_NAMES.filter((name) => name.toLowerCase().includes(normalized));
}

const HTTP_PARAMETER_TYPES = new Set<HttpParameterType>([
  "string",
  "integer",
  "number",
  "boolean",
  "array",
  "object",
  "file",
]);
const HTTP_AUTH_TYPES = new Set<HttpAuthType>(["none", "bearer", "basic", "api-key", "oauth2"]);
const HTTP_BODY_TYPES = new Set<HttpBodyType>([
  "none",
  "json",
  "xml",
  "text",
  "html",
  "javascript",
  "form-data",
  "x-www-form-urlencoded",
  "graphql",
  "binary",
  "raw",
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function restoreRows(value: unknown): HttpKeyValueRow[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((candidate): HttpKeyValueRow[] => {
    if (!isRecord(candidate) || typeof candidate.key !== "string" || typeof candidate.value !== "string") {
      return [];
    }
    return [{
      id: typeof candidate.id === "string" && candidate.id ? candidate.id : crypto.randomUUID(),
      key: candidate.key,
      value: candidate.value,
      enabled: candidate.enabled !== false,
      valueType: HTTP_PARAMETER_TYPES.has(candidate.valueType as HttpParameterType)
        ? candidate.valueType as HttpParameterType
        : "string",
      description: typeof candidate.description === "string" ? candidate.description : "",
      fileName: typeof candidate.fileName === "string" ? candidate.fileName : undefined,
    }];
  });
}

function restoreEnvironment(value: unknown): HttpEnvironment | undefined {
  if (!isRecord(value) || typeof value.id !== "string" || !value.id.trim() ||
      typeof value.name !== "string" || !value.name.trim()) return undefined;
  return { id: value.id.trim(), name: value.name.trim(), vars: restoreRows(value.vars) };
}

function restoreSavedRequest(value: unknown): SavedHttpRequest | undefined {
  if (!isRecord(value) || typeof value.id !== "string" || !value.id.trim() ||
      typeof value.name !== "string" || typeof value.method !== "string" ||
      typeof value.url !== "string" || typeof value.body !== "string" ||
      typeof value.documentation !== "string" ||
      !HTTP_AUTH_TYPES.has(value.authType as HttpAuthType)) return undefined;
  const bodyType = HTTP_BODY_TYPES.has(value.bodyType as HttpBodyType)
    ? value.bodyType as HttpBodyType
    : value.body ? "raw" : "none";
  return {
    id: value.id.trim(),
    name: value.name,
    method: value.method,
    url: value.url,
    params: restoreRows(value.params),
    headers: restoreRows(value.headers),
    body: value.body,
    bodyType,
    formBody: Array.isArray(value.formBody)
      ? value.formBody.flatMap((field): HttpBodyField[] => {
          if (!isRecord(field)) return [];
          const row = restoreRows([field])[0];
          if (!row) return [];
          const fieldType = ["text", "integer", "number", "boolean", "array", "object", "file"]
            .includes(String(field.fieldType)) ? field.fieldType as HttpBodyFieldType : "text";
          return [{ ...row, fieldType, contentType: typeof field.contentType === "string" ? field.contentType : undefined }];
        })
      : [],
    binaryFilePath: typeof value.binaryFilePath === "string" ? value.binaryFilePath : "",
    binaryFileName: typeof value.binaryFileName === "string" ? value.binaryFileName : "",
    authType: value.authType as HttpAuthType,
    authToken: typeof value.authToken === "string" ? value.authToken : "",
    authUsername: typeof value.authUsername === "string" ? value.authUsername : "",
    authPassword: typeof value.authPassword === "string" ? value.authPassword : "",
    apiKeyName: typeof value.apiKeyName === "string" ? value.apiKeyName : "",
    apiKeyValue: typeof value.apiKeyValue === "string" ? value.apiKeyValue : "",
    apiKeyAddTo: value.apiKeyAddTo === "query" ? "query" : "header",
    oauth2GrantType: ["authorization-code", "client-credentials", "password", "implicit"]
      .includes(String(value.oauth2GrantType)) ? value.oauth2GrantType as HttpOAuth2GrantType : "authorization-code",
    oauth2AuthUrl: typeof value.oauth2AuthUrl === "string" ? value.oauth2AuthUrl : "",
    oauth2TokenUrl: typeof value.oauth2TokenUrl === "string" ? value.oauth2TokenUrl : "",
    oauth2ClientId: typeof value.oauth2ClientId === "string" ? value.oauth2ClientId : "",
    oauth2ClientSecret: typeof value.oauth2ClientSecret === "string" ? value.oauth2ClientSecret : "",
    oauth2Scope: typeof value.oauth2Scope === "string" ? value.oauth2Scope : "",
    oauth2CallbackUrl: typeof value.oauth2CallbackUrl === "string" ? value.oauth2CallbackUrl : "",
    oauth2Username: typeof value.oauth2Username === "string" ? value.oauth2Username : "",
    oauth2Password: typeof value.oauth2Password === "string" ? value.oauth2Password : "",
    oauth2AccessToken: typeof value.oauth2AccessToken === "string" ? value.oauth2AccessToken : "",
    graphqlQuery: typeof value.graphqlQuery === "string" ? value.graphqlQuery : "",
    graphqlVariables: typeof value.graphqlVariables === "string" ? value.graphqlVariables : "",
    documentation: value.documentation,
    environmentMode:
      value.environmentMode === "fixed" ||
      (value.environmentMode === undefined && typeof value.environmentId === "string" && value.environmentId)
        ? "fixed"
        : "follow",
    environmentId: typeof value.environmentId === "string" && value.environmentId ? value.environmentId : undefined,
    updatedAt: typeof value.updatedAt === "number" && Number.isFinite(value.updatedAt) ? value.updatedAt : Date.now(),
  };
}

function defaultEnvironment(): HttpEnvironment {
  return { id: crypto.randomUUID(), name: "Default", vars: [] };
}

export function restoreHttpCollections(
  value: unknown,
  legacyEnvironments: HttpEnvironment[] = [],
  nested = false
): HttpRequestCollection[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((item): HttpRequestCollection[] => {
    if (!item || typeof item !== "object") return [];
    const source = item as Partial<HttpRequestCollection>;
    if (typeof source.id !== "string" || typeof source.name !== "string") return [];
    const environments = nested
      ? undefined
      : (Array.isArray(source.environments) && source.environments.length
          ? source.environments.flatMap((environment) => {
              const restored = restoreEnvironment(environment);
              return restored ? [restored] : [];
            })
          : legacyEnvironments.map((environment) => structuredClone(environment))
        );
    if (!nested && !environments?.length) environments?.push(defaultEnvironment());
    const activeEnvironmentId = environments?.some(
      (environment) => environment.id === source.activeEnvironmentId
    )
      ? source.activeEnvironmentId
      : environments?.[0]?.id;
    const availableEnvironments = nested ? legacyEnvironments : environments;
    const requests = (Array.isArray(source.requests) ? source.requests : []).flatMap((request) => {
      const restored = restoreSavedRequest(request);
      if (!restored) return [];
      if (
        restored.environmentMode !== "fixed" ||
        !restored.environmentId ||
        !availableEnvironments?.some((environment) => environment.id === restored.environmentId)
      ) {
        restored.environmentMode = "follow";
        restored.environmentId = undefined;
      }
      return [restored];
    });
    return [{
      id: source.id,
      name: source.name,
      requests,
      children: restoreHttpCollections(source.children, availableEnvironments, true),
      ...(nested ? {} : { environments, activeEnvironmentId }),
    }];
  });
}

export function findHttpCollection(
  collections: HttpRequestCollection[],
  collectionId: string
): HttpRequestCollection | undefined {
  for (const collection of collections) {
    if (collection.id === collectionId) return collection;
    const nested = findHttpCollection(collection.children ?? [], collectionId);
    if (nested) return nested;
  }
  return undefined;
}

export function findTopLevelHttpCollection(
  collections: HttpRequestCollection[],
  collectionId: string
): HttpRequestCollection | undefined {
  return collections.find(
    (collection) =>
      collection.id === collectionId || Boolean(findHttpCollection(collection.children ?? [], collectionId))
  );
}

export function resolveHttpRequestEnvironment(
  topLevelCollection: HttpRequestCollection | undefined,
  environmentId?: string,
  environmentMode: HttpRequestEnvironmentMode = environmentId ? "fixed" : "follow"
): HttpEnvironment | undefined {
  const environments = topLevelCollection?.environments ?? [];
  const fixedEnvironment = environmentMode === "fixed"
    ? environments.find((environment) => environment.id === environmentId)
    : undefined;
  return (
    fixedEnvironment ??
    environments.find((environment) => environment.id === topLevelCollection?.activeEnvironmentId) ??
    environments[0]
  );
}

export function flattenCollectionRequests(collection: HttpRequestCollection): SavedHttpRequest[] {
  return [
    ...collection.requests,
    ...(collection.children ?? []).flatMap((child) => flattenCollectionRequests(child)),
  ];
}

function detachCollection(
  collections: HttpRequestCollection[],
  collectionId: string
): HttpRequestCollection | undefined {
  const index = collections.findIndex((collection) => collection.id === collectionId);
  if (index >= 0) return collections.splice(index, 1)[0];
  for (const collection of collections) {
    const detached = detachCollection(collection.children ?? [], collectionId);
    if (detached) return detached;
  }
  return undefined;
}

export function moveHttpCollection(
  collections: HttpRequestCollection[],
  collectionId: string,
  targetCollectionId?: string
): boolean {
  const source = findHttpCollection(collections, collectionId);
  if (!source || collectionId === targetCollectionId) return false;
  if (targetCollectionId && findHttpCollection(source.children ?? [], targetCollectionId)) return false;
  const target = targetCollectionId ? findHttpCollection(collections, targetCollectionId) : undefined;
  if (targetCollectionId && !target) return false;
  const targetTopLevel = target ? findTopLevelHttpCollection(collections, target.id) : undefined;
  const sourceWasTopLevel = collections.includes(source);
  const detached = detachCollection(collections, collectionId);
  if (!detached) return false;
  if (target) {
    const environmentIdMap = new Map<string, string>();
    if (sourceWasTopLevel && detached.environments?.length && targetTopLevel) {
      const targetEnvironments = (targetTopLevel.environments ??= []);
      for (const environment of detached.environments) {
        const collision = targetEnvironments.find((item) => item.id === environment.id);
        if (!collision) {
          targetEnvironments.push(structuredClone(environment));
          environmentIdMap.set(environment.id, environment.id);
        } else if (JSON.stringify(collision) === JSON.stringify(environment)) {
          environmentIdMap.set(environment.id, collision.id);
        } else {
          const replacementId = crypto.randomUUID();
          targetEnvironments.push({ ...structuredClone(environment), id: replacementId });
          environmentIdMap.set(environment.id, replacementId);
        }
      }
    }
    detached.environments = undefined;
    detached.activeEnvironmentId = undefined;
    const validEnvironmentIds = new Set(targetTopLevel?.environments?.map((environment) => environment.id));
    for (const request of flattenCollectionRequests(detached)) {
      if (request.environmentMode !== "fixed" || !request.environmentId) continue;
      request.environmentId = environmentIdMap.get(request.environmentId) ?? request.environmentId;
      if (!validEnvironmentIds.has(request.environmentId)) {
        request.environmentMode = "follow";
        request.environmentId = undefined;
      }
    }
    (target.children ??= []).push(detached);
  } else {
    const environment = defaultEnvironment();
    detached.environments = [environment];
    detached.activeEnvironmentId = environment.id;
    for (const request of flattenCollectionRequests(detached)) {
      request.environmentMode = "follow";
      request.environmentId = undefined;
    }
    collections.push(detached);
  }
  return true;
}

export function moveSavedHttpRequest(
  collections: HttpRequestCollection[],
  requestId: string,
  targetCollectionId: string
): boolean {
  const target = findHttpCollection(collections, targetCollectionId);
  if (!target) return false;
  let detached: SavedHttpRequest | undefined;
  const visit = (items: HttpRequestCollection[]) => {
    for (const collection of items) {
      const index = collection.requests.findIndex((request) => request.id === requestId);
      if (index >= 0) {
        detached = collection.requests.splice(index, 1)[0];
        return true;
      }
      if (visit(collection.children ?? [])) return true;
    }
    return false;
  };
  if (!visit(collections) || !detached) return false;
  const targetTopLevel = findTopLevelHttpCollection(collections, target.id);
  if (
    detached.environmentMode === "fixed" &&
    (!detached.environmentId ||
      !targetTopLevel?.environments?.some((environment) => environment.id === detached?.environmentId))
  ) {
    detached.environmentMode = "follow";
    detached.environmentId = undefined;
  }
  target.requests.push(detached);
  return true;
}

export function createSavedHttpRequest(
  draft: HttpRequestDraft,
  id = draft.id,
  updatedAt = Date.now()
): SavedHttpRequest {
  return {
    id,
    name: draft.name,
    method: draft.method,
    url: draft.url,
    params: draft.params.map((row) => ({ ...row })),
    headers: draft.headers.map((row) => ({ ...row })),
    body: draft.body,
    bodyType: draft.bodyType ?? (draft.body ? "raw" : "none"),
    formBody: draft.formBody?.map((row) => ({ ...row })) ?? [],
    binaryFilePath: draft.binaryFilePath ?? "",
    binaryFileName: draft.binaryFileName ?? "",
    authType: draft.authType,
    authToken: draft.authToken,
    authUsername: draft.authUsername ?? "",
    authPassword: draft.authPassword ?? "",
    apiKeyName: draft.apiKeyName ?? "",
    apiKeyValue: draft.apiKeyValue ?? "",
    apiKeyAddTo: draft.apiKeyAddTo ?? "header",
    oauth2GrantType: draft.oauth2GrantType ?? "authorization-code",
    oauth2AuthUrl: draft.oauth2AuthUrl ?? "",
    oauth2TokenUrl: draft.oauth2TokenUrl ?? "",
    oauth2ClientId: draft.oauth2ClientId ?? "",
    oauth2ClientSecret: draft.oauth2ClientSecret ?? "",
    oauth2Scope: draft.oauth2Scope ?? "",
    oauth2CallbackUrl: draft.oauth2CallbackUrl ?? "",
    oauth2Username: draft.oauth2Username ?? "",
    oauth2Password: draft.oauth2Password ?? "",
    oauth2AccessToken: draft.oauth2AccessToken ?? "",
    graphqlQuery: draft.graphqlQuery ?? "",
    graphqlVariables: draft.graphqlVariables ?? "",
    documentation: draft.documentation,
    environmentMode: draft.environmentMode === "fixed" && draft.environmentId ? "fixed" : "follow",
    environmentId: draft.environmentMode === "fixed" ? draft.environmentId : undefined,
    updatedAt,
  };
}

export function uniqueCollectionName(name: string, existingNames: Iterable<string>): string {
  const taken = new Set(existingNames);
  if (!taken.has(name)) return name;
  for (let index = 2; ; index += 1) {
    const candidate = `${name} (${index})`;
    if (!taken.has(candidate)) return candidate;
  }
}
