import {
  databaseDefaultPorts,
  databaseTypeLabels,
  type DatabaseConnection,
  type DatabaseTunnelConfig,
  type DbType,
} from "./types.ts";
import {
  DATABASE_DRIVER_OPTION_STRING_MAX_LENGTH,
  DATABASE_DRIVER_OPTIONS_MAX,
  normalizeDatabaseDriverRef,
} from "../plugins/database-drivers.ts";

const PORTABLE_OPTION_KEY = /^[a-z][a-zA-Z0-9]{0,63}$/;

/**
 * 插件驱动选项的可移植形态：v1 没有秘密选项（E-4），只保留基本类型值，
 * 且键数受限；其余一律丢弃。导入后的值仍会在连接时由宿主按驱动声明再校验。
 */
export function portableDriverOptions(value: unknown): Record<string, string | number | boolean> | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const result: Record<string, string | number | boolean> = {};
  let count = 0;
  for (const [key, entry] of Object.entries(value as Record<string, unknown>)) {
    if (count >= DATABASE_DRIVER_OPTIONS_MAX) break;
    if (!PORTABLE_OPTION_KEY.test(key)) continue;
    if (
      typeof entry === "boolean" ||
      (typeof entry === "number" && Number.isFinite(entry)) ||
      (typeof entry === "string" && entry.length <= DATABASE_DRIVER_OPTION_STRING_MAX_LENGTH)
    ) {
      result[key] = entry;
      count += 1;
    }
  }
  return count ? result : undefined;
}

/**
 * 导出/导入连接配置时是否携带明文口令。
 * 导出文件是普通 JSON，携带口令等同于把凭据交给文件系统，调用方必须先获得用户确认。
 */
export interface ConnectionPortabilityOptions {
  includeSecrets?: boolean;
}

function secret(value: unknown) {
  return typeof value === "string" && value ? value : undefined;
}

export function portableTunnel(
  value: unknown,
  options: ConnectionPortabilityOptions = {}
): DatabaseTunnelConfig | undefined {
  if (!value || typeof value !== "object") return undefined;
  const tunnel = value as Record<string, unknown>;
  const host = typeof tunnel.host === "string" ? tunnel.host.trim() : "";
  const port = Number(tunnel.port);
  if (!host || !Number.isInteger(port) || port < 1 || port > 65535) return undefined;
  const withSecrets = options.includeSecrets === true;
  if (tunnel.type === "ssh") {
    const username = typeof tunnel.username === "string" ? tunnel.username.trim() : "";
    if (!username) return undefined;
    return {
      type: "ssh",
      host,
      port,
      username,
      authType: tunnel.authType === "key" ? "key" : "password",
      keyPath: typeof tunnel.keyPath === "string" ? tunnel.keyPath.trim() || undefined : undefined,
      password: withSecrets ? secret(tunnel.password) : undefined,
      passphrase: withSecrets ? secret(tunnel.passphrase) : undefined,
    };
  }
  if (tunnel.type !== "socks5" && tunnel.type !== "http_connect") return undefined;
  return {
    type: tunnel.type,
    host,
    port,
    username: typeof tunnel.username === "string" ? tunnel.username.trim() || undefined : undefined,
    password: withSecrets ? secret(tunnel.password) : undefined,
  };
}

export function portableConnection(
  connection: DatabaseConnection,
  options: ConnectionPortabilityOptions = {}
): DatabaseConnection {
  const withSecrets = options.includeSecrets === true;
  // 驱动绑定只导出 {pluginId, driverId} 引用；凭据释放授权保存在宿主，永不导出。
  const driver = normalizeDatabaseDriverRef(connection.driver);
  return {
    ...connection,
    driver,
    options: driver ? portableDriverOptions(connection.options) : undefined,
    password: withSecrets ? connection.password || undefined : undefined,
    token: withSecrets ? connection.token || undefined : undefined,
    savePassword: withSecrets ? connection.savePassword : false,
    tunnel: portableTunnel(connection.tunnel, options),
  };
}

/** 判断一份导出文件里是否真的带着明文口令，用于导入前提示用户。 */
export function connectionConfigHasSecrets(value: unknown): boolean {
  if (!value || typeof value !== "object") return false;
  const entries = (value as { connections?: unknown }).connections;
  if (!Array.isArray(entries)) return false;
  return entries.some((entry) => {
    if (!entry || typeof entry !== "object") return false;
    const raw = entry as Record<string, unknown>;
    const tunnel = (raw.tunnel ?? {}) as Record<string, unknown>;
    return Boolean(
      secret(raw.password) || secret(raw.token) || secret(tunnel.password) || secret(tunnel.passphrase)
    );
  });
}

export function parseConnectionConfig(
  value: unknown,
  invalidConfigMessage: string,
  options: ConnectionPortabilityOptions = {}
): DatabaseConnection[] {
  if (!value || typeof value !== "object") throw new Error(invalidConfigMessage);
  const entries = (value as { connections?: unknown }).connections;
  if (!Array.isArray(entries)) throw new Error(invalidConfigMessage);
  const withSecrets = options.includeSecrets === true;
  const now = Date.now();
  return entries.map((entry) => {
    if (!entry || typeof entry !== "object") throw new Error(invalidConfigMessage);
    const raw = entry as Record<string, unknown>;
    const dbType = raw.dbType as DbType;
    const name = typeof raw.name === "string" ? raw.name.trim() : "";
    if (!name || !(dbType in databaseTypeLabels)) throw new Error(invalidConfigMessage);
    // 插件驱动连接只携带驱动引用与非秘密选项；引用非法时整份文件视为无效，
    // 绝不把它静默降级成内置驱动（E-15）。dbType "plugin" 必须带合法驱动引用。
    const driver = raw.driver === undefined || raw.driver === null
      ? undefined
      : normalizeDatabaseDriverRef(raw.driver);
    if ((raw.driver !== undefined && raw.driver !== null && !driver) || (dbType === "plugin" && !driver)) {
      throw new Error(invalidConfigMessage);
    }
    const port = Number(raw.port);
    const password = withSecrets ? secret(raw.password) : undefined;
    const token = withSecrets ? secret(raw.token) : undefined;
    const tunnel = portableTunnel(raw.tunnel, options);
    const tunnelSecret = Boolean(
      tunnel && ("password" in tunnel ? tunnel.password : undefined)
    ) || Boolean(tunnel?.type === "ssh" && tunnel.passphrase);
    return {
      id: typeof raw.id === "string" && raw.id ? raw.id : crypto.randomUUID(),
      name,
      dbType,
      host: typeof raw.host === "string" ? raw.host.trim() : "",
      port: Number.isInteger(port) && port >= 1 && port <= 65535 ? port : databaseDefaultPorts[dbType],
      username: typeof raw.username === "string" ? raw.username : undefined,
      password,
      token,
      database: typeof raw.database === "string" ? raw.database : undefined,
      filePath: typeof raw.filePath === "string" ? raw.filePath : undefined,
      org: typeof raw.org === "string" ? raw.org : undefined,
      authSource: typeof raw.authSource === "string" ? raw.authSource : undefined,
      ssl: Boolean(raw.ssl),
      // 只有真的带回了口令才打开保存开关，否则导入的连接会以空口令覆盖本机已存的凭据。
      savePassword: Boolean(password || token || tunnelSecret),
      group: typeof raw.group === "string" ? raw.group : undefined,
      tags: Array.isArray(raw.tags) ? raw.tags.filter((tag): tag is string => typeof tag === "string") : [],
      description: typeof raw.description === "string" ? raw.description : undefined,
      favorite: Boolean(raw.favorite),
      tunnel,
      ...(driver ? { driver, options: portableDriverOptions(raw.options) } : {}),
      createdAt: typeof raw.createdAt === "number" ? raw.createdAt : now,
      updatedAt: now,
    };
  });
}
