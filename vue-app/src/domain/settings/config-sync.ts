import { decryptAes, encryptAes } from "@/domain/security/crypto";

export const CONFIG_SYNC_FORMAT = "mincode-config-sync";
export const CONFIG_SYNC_VERSION = 2;
export const CONFIG_SYNC_STORAGE_PREFIX = "app_";
export const CONFIG_SYNC_SELF_PREFIX = "app_config_sync_";

const LEGACY_CONFIG_SYNC_VERSION = 1;
const SETTINGS_STORAGE_KEY = "app_settings_v2";
const SIDEBAR_VISIBILITY_KEY = "app_sidebar_visibility_v1";
const WORKSPACE_STATE_PREFERENCES_KEY = "app_workspace_state_preferences_v1";

export type ConfigSyncAuthMode = "system" | "https-token" | "ssh";
export type ConfigSyncStorageMode = "plain" | "secure";

export interface ConfigSyncPersistedEntry {
  storage: ConfigSyncStorageMode;
  value: unknown;
}

export interface ConfigSyncSnapshot {
  format: typeof CONFIG_SYNC_FORMAT;
  version: typeof CONFIG_SYNC_VERSION;
  exportedAt: number;
  persistedValues: Record<string, ConfigSyncPersistedEntry>;
}

interface LegacyConfigSyncSnapshot {
  format: typeof CONFIG_SYNC_FORMAT;
  version: typeof LEGACY_CONFIG_SYNC_VERSION;
  exportedAt: number;
  settings: Record<string, unknown>;
  sidebarVisibility: Record<string, boolean>;
  workspaceStateIsolationEnabled: boolean;
}

export interface ConfigSyncRepository {
  remoteUrl: string;
  branch: string;
  authMode: ConfigSyncAuthMode;
  username: string;
  token: string;
  encryptionPassword: string;
  rememberPassword: boolean;
  lastSyncAt?: number;
  lastCommit?: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

const SECRET_FIELD_NAMES = new Set([
  "password",
  "passphrase",
  "token",
  "authtoken",
  "accesstoken",
  "refreshtoken",
  "apikey",
  "apikeyvalue",
  "clientsecret",
  "oauth2clientsecret",
  "oauth2password",
  "oauth2accesstoken",
  "privatekey",
  "totpuri",
  "licensekey",
  "authorization",
  "cookie",
]);

function normalizedFieldName(value: string) {
  return value.replace(/[^a-zA-Z0-9]/g, "").toLowerCase();
}

/** 判断某个字段名是否承载凭据，导出/导入需要据此决定脱敏或重新加密。 */
export function isSecretFieldName(value: string) {
  return SECRET_FIELD_NAMES.has(normalizedFieldName(value));
}

function cloneJsonValue<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function redactSecretValue(value: unknown): unknown {
  if (typeof value === "string") return "";
  if (Array.isArray(value)) return value.map(redactSecretValue);
  if (isRecord(value)) {
    return Object.fromEntries(Object.keys(value).map((key) => [key, redactSecretValue(value[key])]));
  }
  return value === undefined ? undefined : null;
}

/** Removes portable credential material while retaining the surrounding configuration shape. */
export function sanitizeConfigSyncValue(value: unknown): unknown {
  if (typeof value === "string") return value.startsWith("enc:v2:") ? "" : value;
  if (Array.isArray(value)) return value.map(sanitizeConfigSyncValue);
  if (!isRecord(value)) return value;
  return Object.fromEntries(
    Object.entries(value).map(([key, child]) => [
      key,
      SECRET_FIELD_NAMES.has(normalizedFieldName(key))
        ? redactSecretValue(child)
        : sanitizeConfigSyncValue(child),
    ])
  );
}

function arrayItemIdentity(value: unknown): string | undefined {
  if (!isRecord(value)) return undefined;
  for (const key of ["id", "key", "name"]) {
    const candidate = value[key];
    if (typeof candidate === "string" && candidate) return `${key}:${candidate}`;
  }
  return undefined;
}

/**
 * Remote snapshots are deliberately redacted. Merge only secret-shaped fields
 * from the current device so a repository edit cannot introduce credentials.
 */
export function mergeConfigSyncValueWithLocalSecrets(
  remoteValue: unknown,
  localValue: unknown,
  fieldName = ""
): unknown {
  if (SECRET_FIELD_NAMES.has(normalizedFieldName(fieldName))) {
    return localValue === undefined ? redactSecretValue(remoteValue) : cloneJsonValue(localValue);
  }
  if (Array.isArray(remoteValue)) {
    const localItems = Array.isArray(localValue) ? localValue : [];
    const localByIdentity = new Map(
      localItems.flatMap((item) => {
        const identity = arrayItemIdentity(item);
        return identity ? [[identity, item] as const] : [];
      })
    );
    return remoteValue.map((item, index) => {
      const identity = arrayItemIdentity(item);
      const localItem = identity ? localByIdentity.get(identity) : localItems[index];
      return mergeConfigSyncValueWithLocalSecrets(item, localItem);
    });
  }
  if (!isRecord(remoteValue)) return remoteValue;
  const localRecord = isRecord(localValue) ? localValue : {};
  return Object.fromEntries(
    Object.entries(remoteValue).map(([key, child]) => [
      key,
      mergeConfigSyncValueWithLocalSecrets(child, localRecord[key], key),
    ])
  );
}

export function isConfigSyncStorageKey(key: string) {
  return key !== "app_file_templates_custom_v1" && key.startsWith(CONFIG_SYNC_STORAGE_PREFIX) && !key.startsWith(CONFIG_SYNC_SELF_PREFIX);
}

function normalizePersistedEntries(value: unknown) {
  if (!isRecord(value)) throw new Error("config_sync_invalid_payload");
  const entries: Record<string, ConfigSyncPersistedEntry> = {};
  for (const [key, entry] of Object.entries(value)) {
    if (!isConfigSyncStorageKey(key) || !isRecord(entry)) {
      throw new Error("config_sync_invalid_payload");
    }
    if (entry.storage !== "plain" && entry.storage !== "secure") {
      throw new Error("config_sync_invalid_payload");
    }
    if (!("value" in entry)) throw new Error("config_sync_invalid_payload");
    entries[key] = { storage: entry.storage, value: entry.value };
  }
  return entries;
}

function normalizeLegacySnapshot(value: Record<string, unknown>): LegacyConfigSyncSnapshot {
  if (!isRecord(value.settings) || !isRecord(value.sidebarVisibility)) {
    throw new Error("config_sync_invalid_payload");
  }
  if (typeof value.workspaceStateIsolationEnabled !== "boolean") {
    throw new Error("config_sync_invalid_payload");
  }
  return {
    format: CONFIG_SYNC_FORMAT,
    version: LEGACY_CONFIG_SYNC_VERSION,
    exportedAt: typeof value.exportedAt === "number" ? value.exportedAt : 0,
    settings: value.settings,
    sidebarVisibility: Object.fromEntries(
      Object.entries(value.sidebarVisibility).filter(
        (entry): entry is [string, boolean] => typeof entry[1] === "boolean"
      )
    ),
    workspaceStateIsolationEnabled: value.workspaceStateIsolationEnabled,
  };
}

export function normalizeConfigSyncRepository(value: unknown): ConfigSyncRepository {
  const source = isRecord(value) ? value : {};
  const authMode = source.authMode;
  return {
    remoteUrl: typeof source.remoteUrl === "string" ? source.remoteUrl : "",
    branch: typeof source.branch === "string" && source.branch.trim() ? source.branch : "main",
    authMode:
      authMode === "https-token" || authMode === "ssh" || authMode === "system"
        ? authMode
        : "system",
    username: typeof source.username === "string" ? source.username : "",
    token: typeof source.token === "string" ? source.token : "",
    encryptionPassword:
      typeof source.encryptionPassword === "string" ? source.encryptionPassword : "",
    rememberPassword: source.rememberPassword === true,
    lastSyncAt: typeof source.lastSyncAt === "number" ? source.lastSyncAt : undefined,
    lastCommit: typeof source.lastCommit === "string" ? source.lastCommit : undefined,
  };
}

export function validateConfigSyncSnapshot(value: unknown): ConfigSyncSnapshot {
  if (!isRecord(value)) throw new Error("config_sync_invalid_payload");
  if (value.format !== CONFIG_SYNC_FORMAT) throw new Error("config_sync_unsupported_version");

  if (value.version === LEGACY_CONFIG_SYNC_VERSION) {
    const legacy = normalizeLegacySnapshot(value);
    return {
      format: CONFIG_SYNC_FORMAT,
      version: CONFIG_SYNC_VERSION,
      exportedAt: legacy.exportedAt,
      persistedValues: {
        [SETTINGS_STORAGE_KEY]: { storage: "secure", value: legacy.settings },
        [SIDEBAR_VISIBILITY_KEY]: { storage: "plain", value: legacy.sidebarVisibility },
        [WORKSPACE_STATE_PREFERENCES_KEY]: {
          storage: "plain",
          value: {
            version: 1,
            isolationEnabled: legacy.workspaceStateIsolationEnabled,
          },
        },
      },
    };
  }

  if (value.version !== CONFIG_SYNC_VERSION) {
    throw new Error("config_sync_unsupported_version");
  }
  return {
    format: CONFIG_SYNC_FORMAT,
    version: CONFIG_SYNC_VERSION,
    exportedAt: typeof value.exportedAt === "number" ? value.exportedAt : 0,
    persistedValues: normalizePersistedEntries(value.persistedValues),
  };
}

export async function encodeConfigSyncSnapshot(
  snapshot: ConfigSyncSnapshot,
  password: string
): Promise<string> {
  if (password.length < 8) throw new Error("config_sync_password_too_short");
  return encryptAes(JSON.stringify(snapshot), password);
}

export async function decodeConfigSyncSnapshot(
  payload: string,
  password: string
): Promise<ConfigSyncSnapshot> {
  const plain = await decryptAes(payload, password);
  return validateConfigSyncSnapshot(JSON.parse(plain) as unknown);
}
