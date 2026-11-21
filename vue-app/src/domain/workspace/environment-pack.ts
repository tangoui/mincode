import type { DatabaseConnection, DatabaseTunnelConfig, DbType } from "../database/types.ts";
import { databaseDefaultPorts, databaseTypeLabels } from "../database/types.ts";
import { portableDriverOptions } from "../database/connection-portability.ts";
import { normalizeDatabaseDriverRef } from "../plugins/database-drivers.ts";
import type {
  HttpAuthType,
  HttpBodyField,
  HttpBodyFieldType,
  HttpBodyType,
  HttpKeyValueRow,
  HttpRequestCollection,
  SavedHttpRequest,
} from "../http/request-collection.ts";
import type { SshConnectionProfile, SshRuntimeCredentials } from "../ssh/types.ts";

const ENCRYPTED_PREFIX = "MINCODE_V1_ENC:";
const PBKDF2_ITERATIONS = 100_000;
const MAX_PACK_ITEMS = 2_000;
const MAX_ID_LENGTH = 2_048;
const MAX_TEXT_LENGTH = 32_768;
const MAX_BODY_LENGTH = 5 * 1024 * 1024;
const HTTP_AUTH_TYPES = new Set<HttpAuthType>([
  "none",
  "bearer",
  "basic",
  "api-key",
  "oauth2",
]);
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
const HTTP_BODY_FIELD_TYPES = new Set<HttpBodyFieldType>([
  "text",
  "integer",
  "number",
  "boolean",
  "array",
  "object",
  "file",
]);

export interface EnvironmentPackVariable {
  id: string;
  key: string;
  value: string;
  enabled: boolean;
}

export interface EnvironmentPackEnvironment {
  id: string;
  name: string;
  vars: EnvironmentPackVariable[];
}

export interface EnvironmentPackProject {
  root: string;
  name: string;
}

export interface EnvironmentPackHttpState {
  environments: EnvironmentPackEnvironment[];
  activeEnvId: string;
  collections: HttpRequestCollection[];
}

export interface EnvironmentPackData {
  version: 2;
  exportedAt: number;
  appName: "MinCode";
  items: {
    sshConnections: SshConnectionProfile[];
    sshCredentials?: Record<string, SshRuntimeCredentials>;
    dbConnections: DatabaseConnection[];
    httpCollections: HttpRequestCollection[];
    httpEnvironments: {
      environments: EnvironmentPackEnvironment[];
      activeEnvId: string;
    };
    localProjects: EnvironmentPackProject[];
  };
}

export interface EnvironmentPackPreview {
  ssh: { total: number; newCount: number; skipCount: number };
  database: { total: number; newCount: number; skipCount: number };
  http: { total: number; newCount: number; skipCount: number };
  projects: { total: number; newCount: number; skipCount: number };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function optionalString(value: unknown, maxLength = 32_768) {
  return typeof value === "string" && value.length <= maxLength ? value : undefined;
}

function validPort(value: unknown, fallback: number) {
  return Number.isInteger(value) && Number(value) >= 1 && Number(value) <= 65_535
    ? Number(value)
    : fallback;
}

function bytesToBase64(bytes: Uint8Array) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function base64ToBytes(value: string) {
  const binary = atob(value.replace(/\s+/g, ""));
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

async function deriveKey(password: string, salt: Uint8Array, usage: KeyUsage[]) {
  const material = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(password),
    "PBKDF2",
    false,
    ["deriveKey"]
  );
  return crypto.subtle.deriveKey(
    {
      name: "PBKDF2",
      salt: salt as BufferSource,
      iterations: PBKDF2_ITERATIONS,
      hash: "SHA-256",
    },
    material,
    { name: "AES-GCM", length: 256 },
    false,
    usage
  );
}

export async function encodeEnvironmentPack(data: EnvironmentPackData, password: string) {
  if (password.length < 8)
    throw new Error("Environment pack password must contain at least 8 characters");
  const salt = crypto.getRandomValues(new Uint8Array(16));
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const key = await deriveKey(password, salt, ["encrypt"]);
  const encrypted = await crypto.subtle.encrypt(
    { name: "AES-GCM", iv: iv as BufferSource },
    key,
    new TextEncoder().encode(JSON.stringify(data))
  );
  const cipher = new Uint8Array(encrypted);
  const combined = new Uint8Array(salt.length + iv.length + cipher.length);
  combined.set(salt, 0);
  combined.set(iv, salt.length);
  combined.set(cipher, salt.length + iv.length);
  return `${ENCRYPTED_PREFIX}${bytesToBase64(combined)}`;
}

export async function decodeEnvironmentPack(raw: string, password: string) {
  const value = raw.trim();
  if (!value.startsWith(ENCRYPTED_PREFIX)) {
    throw new Error("Unsupported environment pack format");
  }
  if (!password) throw new Error("Environment pack password is required");
  const combined = base64ToBytes(value.slice(ENCRYPTED_PREFIX.length));
  if (combined.length <= 28) throw new Error("Environment pack payload is incomplete");
  const salt = combined.slice(0, 16);
  const iv = combined.slice(16, 28);
  const cipher = combined.slice(28);
  const key = await deriveKey(password, salt, ["decrypt"]);
  let json: string;
  try {
    const plain = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: iv as BufferSource },
      key,
      cipher as BufferSource
    );
    json = new TextDecoder("utf-8", { fatal: true }).decode(plain);
  } catch {
    throw new Error("Environment pack password or file is invalid");
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(json) as unknown;
  } catch {
    throw new Error("Environment pack JSON is invalid");
  }
  return restoreEnvironmentPack(parsed);
}

function limitedArray(value: unknown, label: string) {
  if (!Array.isArray(value) || value.length > MAX_PACK_ITEMS) throw new Error(`Invalid ${label}`);
  return value;
}

function restoreSshCredentials(value: unknown): Record<string, SshRuntimeCredentials> {
  if (value === undefined) return {};
  if (!isRecord(value) || Object.keys(value).length > MAX_PACK_ITEMS) {
    throw new Error("Invalid SSH credentials");
  }
  const result: Record<string, SshRuntimeCredentials> = {};
  for (const [profileId, candidate] of Object.entries(value)) {
    if (!profileId || !isRecord(candidate)) continue;
    const restored = Object.fromEntries(
      (["password", "passphrase", "jumpPassword", "jumpPassphrase", "socksPassword", "totpUri"] as const)
        .map((key) => [key, optionalString(candidate[key])])
        .filter(
          (entry): entry is [string, string] => typeof entry[1] === "string" && Boolean(entry[1])
        )
    ) as SshRuntimeCredentials;
    if (Object.keys(restored).length) result[profileId] = restored;
  }
  return result;
}

function restoreTunnel(value: unknown): DatabaseTunnelConfig | undefined {
  if (
    !isRecord(value) ||
    (value.type !== "ssh" && value.type !== "socks5" && value.type !== "http_connect")
  ) {
    return undefined;
  }
  const host = optionalString(value.host, 2_048)?.trim();
  if (!host) return undefined;
  const port = validPort(value.port, value.type === "ssh" ? 22 : 1_080);
  if (value.type === "ssh") {
    const username = optionalString(value.username, 2_048)?.trim();
    if (!username) return undefined;
    return {
      type: "ssh",
      host,
      port,
      username,
      authType: value.authType === "key" ? "key" : "password",
      password: optionalString(value.password),
      keyPath: optionalString(value.keyPath),
      passphrase: optionalString(value.passphrase),
    };
  }
  return {
    type: value.type,
    host,
    port,
    username: optionalString(value.username, 2_048)?.trim() || undefined,
    password: optionalString(value.password),
  };
}

export function restoreDatabaseConnection(value: unknown): DatabaseConnection | null {
  if (
    !isRecord(value) ||
    typeof value.dbType !== "string" ||
    !(value.dbType in databaseTypeLabels)
  ) {
    return null;
  }
  const dbType = value.dbType as DbType;
  // 插件驱动连接连同驱动引用与非秘密选项一起恢复；授权保存在宿主，环境包里永远没有。
  // 引用非法（或 dbType "plugin" 缺引用）时整条无效，绝不静默改成内置驱动（E-15）。
  const hasDriver = value.driver !== undefined && value.driver !== null;
  const driver = hasDriver ? normalizeDatabaseDriverRef(value.driver) : undefined;
  if ((hasDriver && !driver) || (dbType === "plugin" && !driver)) return null;
  const name = optionalString(value.name, 2_048)?.trim();
  const id = optionalString(value.id, 2_048)?.trim();
  if (!name || !id) return null;
  const now = Date.now();
  return {
    id,
    name,
    dbType,
    host: optionalString(value.host, 8_192)?.trim() ?? "",
    port: validPort(value.port, databaseDefaultPorts[dbType]),
    username: optionalString(value.username),
    password: optionalString(value.password),
    database: optionalString(value.database),
    filePath: optionalString(value.filePath),
    token: optionalString(value.token),
    org: optionalString(value.org),
    authSource: optionalString(value.authSource),
    ssl: value.ssl === true,
    savePassword: value.savePassword === true,
    group: optionalString(value.group, 2_048)?.trim() || undefined,
    tags: Array.isArray(value.tags)
      ? [
          ...new Set(
            value.tags
              .filter((tag): tag is string => typeof tag === "string")
              .map((tag) => tag.trim())
              .filter(Boolean)
          ),
        ].slice(0, 64)
      : [],
    description: optionalString(value.description),
    favorite: value.favorite === true,
    lastConnectedAt: typeof value.lastConnectedAt === "number" ? value.lastConnectedAt : undefined,
    tunnel: restoreTunnel(value.tunnel),
    ...(driver ? { driver, options: portableDriverOptions(value.options) } : {}),
    createdAt: typeof value.createdAt === "number" ? value.createdAt : now,
    updatedAt: typeof value.updatedAt === "number" ? value.updatedAt : now,
  };
}

function restoreVariable(value: unknown): EnvironmentPackVariable | null {
  if (!isRecord(value) || typeof value.key !== "string" || typeof value.value !== "string")
    return null;
  return {
    id: optionalString(value.id, 2_048)?.trim() || crypto.randomUUID(),
    key: value.key,
    value: value.value,
    enabled: value.enabled !== false,
  };
}

function restoreEnvironment(value: unknown): EnvironmentPackEnvironment | null {
  if (!isRecord(value)) return null;
  const id = optionalString(value.id, 2_048)?.trim();
  const name = optionalString(value.name, 2_048)?.trim();
  if (!id || !name) return null;
  const vars = limitedArray(value.vars ?? [], "HTTP environment variables").map(restoreVariable);
  if (vars.some((item) => item === null)) return null;
  return { id, name, vars: vars as EnvironmentPackVariable[] };
}

function restoreProject(value: unknown): EnvironmentPackProject | null {
  if (!isRecord(value)) return null;
  const root =
    optionalString(value.root, 32_768)?.trim() || optionalString(value.rootPath, 32_768)?.trim();
  if (!root) return null;
  return {
    root,
    name: optionalString(value.name, 2_048)?.trim() || root.split(/[\\/]/).pop() || root,
  };
}

function restoreHttpKeyValueRow(value: unknown): HttpKeyValueRow | null {
  if (!isRecord(value)) return null;
  const id = optionalString(value.id, MAX_ID_LENGTH)?.trim();
  const key = optionalString(value.key, MAX_TEXT_LENGTH);
  const rowValue = optionalString(value.value, MAX_BODY_LENGTH);
  if (!id || key === undefined || rowValue === undefined) return null;
  return {
    id,
    key,
    value: rowValue,
    enabled: value.enabled !== false,
  };
}

function restoreHttpBodyField(value: unknown): HttpBodyField | null {
  const row = restoreHttpKeyValueRow(value);
  if (
    !row ||
    !isRecord(value) ||
    typeof value.fieldType !== "string" ||
    !HTTP_BODY_FIELD_TYPES.has(value.fieldType as HttpBodyFieldType)
  ) {
    return null;
  }
  const fileName = optionalString(value.fileName, MAX_TEXT_LENGTH);
  return {
    ...row,
    fieldType: value.fieldType as HttpBodyFieldType,
    ...(fileName === undefined ? {} : { fileName }),
  };
}

function restoreHttpRequest(value: unknown): SavedHttpRequest | null {
  if (!isRecord(value)) return null;
  const id = optionalString(value.id, MAX_ID_LENGTH)?.trim();
  const name = optionalString(value.name, MAX_ID_LENGTH)?.trim();
  const method = optionalString(value.method, 32)?.trim();
  const url = optionalString(value.url, MAX_TEXT_LENGTH);
  const body = optionalString(value.body, MAX_BODY_LENGTH);
  const documentation = optionalString(value.documentation, MAX_BODY_LENGTH);
  if (
    !id ||
    !name ||
    !method ||
    url === undefined ||
    body === undefined ||
    documentation === undefined
  ) {
    return null;
  }
  if (typeof value.authType !== "string" || !HTTP_AUTH_TYPES.has(value.authType as HttpAuthType)) {
    return null;
  }
  const bodyType = value.bodyType ?? (body ? "raw" : "none");
  if (typeof bodyType !== "string" || !HTTP_BODY_TYPES.has(bodyType as HttpBodyType)) return null;

  const params = limitedArray(value.params, "HTTP request parameters").map(restoreHttpKeyValueRow);
  const headers = limitedArray(value.headers, "HTTP request headers").map(restoreHttpKeyValueRow);
  const formBody = limitedArray(value.formBody ?? [], "HTTP request form fields").map(
    restoreHttpBodyField
  );
  if ([...params, ...headers, ...formBody].some((item) => item === null)) return null;

  return {
    id,
    name,
    method,
    url,
    params: params as HttpKeyValueRow[],
    headers: headers as HttpKeyValueRow[],
    body,
    bodyType: bodyType as HttpBodyType,
    formBody: formBody as HttpBodyField[],
    binaryFilePath: optionalString(value.binaryFilePath, MAX_TEXT_LENGTH) ?? "",
    binaryFileName: optionalString(value.binaryFileName, MAX_TEXT_LENGTH) ?? "",
    authType: value.authType as HttpAuthType,
    authToken: optionalString(value.authToken, MAX_BODY_LENGTH) ?? "",
    authUsername: optionalString(value.authUsername, MAX_TEXT_LENGTH) ?? "",
    authPassword: optionalString(value.authPassword, MAX_BODY_LENGTH) ?? "",
    apiKeyName: optionalString(value.apiKeyName, MAX_TEXT_LENGTH) ?? "",
    apiKeyValue: optionalString(value.apiKeyValue, MAX_BODY_LENGTH) ?? "",
    apiKeyAddTo: value.apiKeyAddTo === "query" ? "query" : "header",
    oauth2GrantType:
      value.oauth2GrantType === "client-credentials" ||
      value.oauth2GrantType === "password" ||
      value.oauth2GrantType === "implicit"
        ? value.oauth2GrantType
        : "authorization-code",
    oauth2AuthUrl: optionalString(value.oauth2AuthUrl, MAX_TEXT_LENGTH) ?? "",
    oauth2TokenUrl: optionalString(value.oauth2TokenUrl, MAX_TEXT_LENGTH) ?? "",
    oauth2ClientId: optionalString(value.oauth2ClientId, MAX_TEXT_LENGTH) ?? "",
    oauth2ClientSecret: optionalString(value.oauth2ClientSecret, MAX_BODY_LENGTH) ?? "",
    oauth2Scope: optionalString(value.oauth2Scope, MAX_TEXT_LENGTH) ?? "",
    oauth2CallbackUrl: optionalString(value.oauth2CallbackUrl, MAX_TEXT_LENGTH) ?? "",
    oauth2Username: optionalString(value.oauth2Username, MAX_TEXT_LENGTH) ?? "",
    oauth2Password: optionalString(value.oauth2Password, MAX_BODY_LENGTH) ?? "",
    oauth2AccessToken: optionalString(value.oauth2AccessToken, MAX_BODY_LENGTH) ?? "",
    graphqlQuery: optionalString(value.graphqlQuery, MAX_BODY_LENGTH) ?? "",
    graphqlVariables: optionalString(value.graphqlVariables, MAX_BODY_LENGTH) ?? "",
    documentation,
    environmentId: optionalString(value.environmentId, MAX_ID_LENGTH)?.trim() || undefined,
    updatedAt:
      typeof value.updatedAt === "number" && Number.isFinite(value.updatedAt)
        ? value.updatedAt
        : Date.now(),
  };
}

function restoreHttpCollection(value: unknown, nested = false): HttpRequestCollection | null {
  if (!isRecord(value)) return null;
  const id = optionalString(value.id, MAX_ID_LENGTH)?.trim();
  const name = optionalString(value.name, MAX_ID_LENGTH)?.trim();
  if (!id || !name) return null;
  const requests = limitedArray(value.requests ?? [], "HTTP collection requests").map(
    restoreHttpRequest
  );
  const children = limitedArray(value.children ?? [], "nested HTTP collections").map((child) =>
    restoreHttpCollection(child, true)
  );
  if ([...requests, ...children].some((item) => item === null)) return null;

  const collection: HttpRequestCollection = {
    id,
    name,
    requests: requests as SavedHttpRequest[],
    children: children as HttpRequestCollection[],
  };
  if (nested) return collection;

  const environments = limitedArray(value.environments ?? [], "HTTP collection environments").map(
    restoreEnvironment
  );
  if (environments.some((item) => item === null)) return null;
  collection.environments = environments as EnvironmentPackEnvironment[];
  const activeEnvironmentId = optionalString(value.activeEnvironmentId, MAX_ID_LENGTH)?.trim();
  collection.activeEnvironmentId = collection.environments.some(
    (environment) => environment.id === activeEnvironmentId
  )
    ? activeEnvironmentId
    : collection.environments[0]?.id;
  return collection;
}

function migrateLegacyHttpEnvironments(
  collections: HttpRequestCollection[],
  environments: EnvironmentPackEnvironment[],
  activeEnvironmentId: string
) {
  if (!environments.length) return collections;
  return collections.map((collection) => {
    if (collection.environments?.length) return collection;
    const migratedEnvironments = structuredClone(environments);
    return {
      ...collection,
      environments: migratedEnvironments,
      activeEnvironmentId: migratedEnvironments.some(
        (environment) => environment.id === activeEnvironmentId
      )
        ? activeEnvironmentId
        : migratedEnvironments[0]?.id,
    };
  });
}

export function restoreEnvironmentPack(value: unknown): EnvironmentPackData {
  if (!isRecord(value) || value.version !== 2 || !isRecord(value.items)) {
    throw new Error("Unsupported environment pack version");
  }
  const sshConnections = limitedArray(
    value.items.sshConnections ?? [],
    "SSH connections"
  ) as SshConnectionProfile[];
  const dbConnections = limitedArray(value.items.dbConnections ?? [], "database connections").map(
    restoreDatabaseConnection
  );
  if (dbConnections.some((item) => item === null))
    throw new Error("Invalid database connection in environment pack");
  const httpCollections = limitedArray(value.items.httpCollections ?? [], "HTTP collections").map(
    (collection) => restoreHttpCollection(collection)
  );
  if (httpCollections.some((item) => item === null)) {
    throw new Error("Invalid HTTP collection in environment pack");
  }
  const httpEnvironmentSource = isRecord(value.items.httpEnvironments)
    ? value.items.httpEnvironments
    : {};
  const environments = limitedArray(
    httpEnvironmentSource.environments ?? [],
    "HTTP environments"
  ).map(restoreEnvironment);
  if (environments.some((item) => item === null))
    throw new Error("Invalid HTTP environment in environment pack");
  const projects = limitedArray(value.items.localProjects ?? [], "local projects").map(
    restoreProject
  );
  if (projects.some((item) => item === null))
    throw new Error("Invalid local project in environment pack");
  const legacyActiveEnvironmentId =
    optionalString(httpEnvironmentSource.activeEnvId, MAX_ID_LENGTH)?.trim() ?? "";
  const restoredCollections = migrateLegacyHttpEnvironments(
    httpCollections as HttpRequestCollection[],
    environments as EnvironmentPackEnvironment[],
    legacyActiveEnvironmentId
  );
  return {
    version: 2,
    exportedAt: typeof value.exportedAt === "number" ? value.exportedAt : Date.now(),
    appName: "MinCode",
    items: {
      sshConnections,
      sshCredentials: restoreSshCredentials(value.items.sshCredentials),
      dbConnections: dbConnections as DatabaseConnection[],
      httpCollections: restoredCollections,
      // 旧字段仅保留文件格式兼容；环境已迁移到各顶级集合，运行时不得读取这里。
      httpEnvironments: {
        environments: [],
        activeEnvId: "",
      },
      localProjects: projects as EnvironmentPackProject[],
    },
  };
}

export function buildEnvironmentPackPreview(
  data: EnvironmentPackData,
  existing: {
    sshIds: Set<string>;
    databaseIds: Set<string>;
    httpIds: Set<string>;
    projectRoots: Set<string>;
  }
): EnvironmentPackPreview {
  const counts = <T>(items: T[], exists: (item: T) => boolean) => {
    const skipCount = items.filter(exists).length;
    return { total: items.length, newCount: items.length - skipCount, skipCount };
  };
  return {
    ssh: counts(data.items.sshConnections, (item) => existing.sshIds.has(item.id)),
    database: counts(data.items.dbConnections, (item) => existing.databaseIds.has(item.id)),
    http: counts(data.items.httpCollections, (item) => existing.httpIds.has(item.id)),
    projects: counts(data.items.localProjects, (item) => existing.projectRoots.has(item.root)),
  };
}
