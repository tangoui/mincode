import { APP_LOCK_STORAGE_KEY } from "@/domain/security/app-lock";
import {
  isConfigSyncStorageKey,
  isSecretFieldName,
  type ConfigSyncStorageMode,
} from "@/domain/settings/config-sync";
import {
  DEVICE_LOCAL_HISTORY_STORAGE_KEYS,
  isKnownSecureStorageKey,
  sanitizePortableValue,
} from "@/services/config-sync-persistence";
import {
  applyPersistedBatch,
  loadAllPersistedValues,
  type PersistedBatchSet,
} from "@/services/persistent-store";
import {
  loadSecurePersistedValue,
  prepareSecurePersistedValue,
} from "@/services/secure-persistent-store";
import {
  normalizeLegacyAiProfiles,
  SETTINGS_STORAGE_KEY,
  updateSettingsRuntimeSnapshot,
} from "@/services/settings-persistence";
import { decryptField, encryptField } from "@/services/field-crypto";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import { isRetiredAiHistoryStorageKey } from "@/services/ai-conversation-persistence";

export const CONFIG_EXPORT_FORMAT = "mincode-config";
export const CONFIG_EXPORT_VERSION = 2;

const SECURE_VALUE_PREFIX = "enc:v2:";

/**
 * 授权记录与锁屏口令只对当前设备有意义：导出授权等于分发许可证，导入锁屏配置
 * 则可能用别人的口令把本机用户挡在门外，两者都不进入配置文件。
 */
const NON_PORTABLE_KEYS = new Set(["app_license", "mincode_license", APP_LOCK_STORAGE_KEY]);

/** 这两个键内部逐字段加密，导出要解密成明文、导入要用本机密钥重新加密。 */
const FIELD_ENCRYPTED_KEYS = new Set(["app_ssh_credentials_v1", "app_db_connections"]);

export interface ConfigExportEntry {
  storage: ConfigSyncStorageMode;
  value: unknown;
}

export interface ConfigExportPayload {
  format: typeof CONFIG_EXPORT_FORMAT;
  version: typeof CONFIG_EXPORT_VERSION;
  exportedAt: string;
  includesSecrets: boolean;
  values: Record<string, ConfigExportEntry>;
}

export interface ParsedConfigImport {
  includesSecrets: boolean;
  values: Record<string, ConfigExportEntry>;
  /** 无法在本机还原的键（多为旧版导出里的本机密文），调用方需要如实告知用户。 */
  skippedKeys: string[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function isPortableConfigKey(key: string) {
  return isConfigSyncStorageKey(key)
    && !NON_PORTABLE_KEYS.has(key)
    && !DEVICE_LOCAL_HISTORY_STORAGE_KEYS.has(key)
    && !isRetiredAiHistoryStorageKey(key);
}

async function decryptSecretFields(value: unknown): Promise<unknown> {
  if (typeof value === "string") {
    if (!value.startsWith(SECURE_VALUE_PREFIX)) return value;
    return decryptField(value);
  }
  if (Array.isArray(value)) return Promise.all(value.map(decryptSecretFields));
  if (!isRecord(value)) return value;
  const entries = await Promise.all(
    Object.entries(value).map(async ([key, child]) => [key, await decryptSecretFields(child)] as const)
  );
  return Object.fromEntries(entries);
}

async function encryptSecretFields(value: unknown, fieldName = ""): Promise<unknown> {
  if (typeof value === "string") {
    if (!value || value.startsWith(SECURE_VALUE_PREFIX) || !isSecretFieldName(fieldName)) {
      return value;
    }
    return encryptField(value);
  }
  if (Array.isArray(value)) {
    return Promise.all(value.map((item) => encryptSecretFields(item, fieldName)));
  }
  if (!isRecord(value)) return value;
  const entries = await Promise.all(
    Object.entries(value).map(async ([key, child]) => [key, await encryptSecretFields(child, key)] as const)
  );
  return Object.fromEntries(entries);
}

function hasPlainSecret(value: unknown, fieldName = ""): boolean {
  if (typeof value === "string") {
    return Boolean(value) && !value.startsWith(SECURE_VALUE_PREFIX) && isSecretFieldName(fieldName);
  }
  if (Array.isArray(value)) return value.some((item) => hasPlainSecret(item, fieldName));
  if (!isRecord(value)) return false;
  return Object.entries(value).some(([key, child]) => hasPlainSecret(child, key));
}

/**
 * 组装配置导出文件。
 *
 * 整体密封的键会先解封成明文 JSON：密文由本机指纹派生的密钥加密，直接写进文件
 * 会让导出的配置在任何另一台机器上都无法读取。
 */
export async function buildConfigExport(options: {
  includeSecrets: boolean;
  exportedAt?: string;
  /** Backups: fail instead of exporting the browser copy when the native load fails. */
  strict?: boolean;
}): Promise<ConfigExportPayload> {
  const stored = await loadAllPersistedValues({ strict: options.strict === true });
  const values: Record<string, ConfigExportEntry> = {};

  for (const [key, rawValue] of Object.entries(stored)) {
    if (!isPortableConfigKey(key) || rawValue === null || rawValue === undefined) continue;
    const secure =
      isKnownSecureStorageKey(key)
      || (typeof rawValue === "string" && rawValue.startsWith(SECURE_VALUE_PREFIX));
    const logical = secure ? await loadSecurePersistedValue<unknown>(key) : rawValue;
    if (logical === null || logical === undefined) continue;

    let value: unknown = logical;
    if (!options.includeSecrets) {
      value = sanitizePortableValue(key, logical);
    } else if (isDesktopRuntime() && FIELD_ENCRYPTED_KEYS.has(key)) {
      value = await decryptSecretFields(logical);
    }
    values[key] = { storage: secure ? "secure" : "plain", value };
  }

  return {
    format: CONFIG_EXPORT_FORMAT,
    version: CONFIG_EXPORT_VERSION,
    exportedAt: options.exportedAt ?? new Date().toISOString(),
    includesSecrets: options.includeSecrets,
    values,
  };
}

function parseLegacyEntry(value: unknown): ConfigExportEntry | null {
  if (typeof value !== "string") return { storage: "plain", value };
  // 旧版导出直接落盘了本机密文，换台机器解不开，只能跳过而不是写入坏数据。
  if (value.startsWith(SECURE_VALUE_PREFIX)) return null;
  try {
    return { storage: "plain", value: JSON.parse(value) as unknown };
  } catch {
    return { storage: "plain", value };
  }
}

/** 解析导出文件，同时兼容旧版 `{app_*: value}` 扁平结构。 */
export function parseConfigImport(
  payload: unknown,
  invalidMessage: string,
  options: { allowEmpty?: boolean } = {}
): ParsedConfigImport {
  if (!isRecord(payload)) throw new Error(invalidMessage);
  const values: Record<string, ConfigExportEntry> = {};
  const skippedKeys: string[] = [];

  const typed = payload.format === CONFIG_EXPORT_FORMAT && isRecord(payload.values);
  if (typed && payload.version !== CONFIG_EXPORT_VERSION) throw new Error(invalidMessage);
  const source = typed ? (payload.values as Record<string, unknown>) : payload;

  for (const [key, entry] of Object.entries(source)) {
    if (!key.startsWith("app_")) continue;
    if (!isPortableConfigKey(key)) {
      skippedKeys.push(key);
      continue;
    }
    if (typed) {
      if (!isRecord(entry) || !("value" in entry)) throw new Error(invalidMessage);
      const storage = entry.storage === "secure" ? "secure" : "plain";
      values[key] = { storage, value: entry.value };
      continue;
    }
    const legacy = parseLegacyEntry(entry);
    if (legacy) values[key] = legacy;
    else skippedKeys.push(key);
  }

  // 只含插件数据的文件没有应用配置，由调用方在确认插件数据存在后放行。
  if (!Object.keys(values).length && !options.allowEmpty) throw new Error(invalidMessage);
  return {
    includesSecrets:
      payload.includesSecrets === true
      || Object.values(values).some((entry) => hasPlainSecret(entry.value)),
    values,
    skippedKeys,
  };
}

export interface PreparedConfigWrite {
  sets: PersistedBatchSet[];
  /** Keys in sets, in order. */
  keys: string[];
  /** The new app_settings_v2 value, when it is among the sets. */
  settingsSnapshot?: Record<string, unknown>;
}

/**
 * 写入前的全部准备：字段加密、对象密封与浏览器快照，调用方随后用一次
 * applyPersistedBatch 提交（配置导入与备份恢复共用）。AI 会话历史键被跳过。
 */
export async function prepareConfigValuesForWrite(
  values: Record<string, ConfigExportEntry>
): Promise<PreparedConfigWrite> {
  const sets: PersistedBatchSet[] = [];
  let settingsSnapshot: Record<string, unknown> | undefined;

  for (const [key, entry] of Object.entries(values)) {
    if (isRetiredAiHistoryStorageKey(key)) continue;
    const secure = entry.storage === "secure" || isKnownSecureStorageKey(key);
    const imported =
      isDesktopRuntime() && FIELD_ENCRYPTED_KEYS.has(key)
        ? await encryptSecretFields(entry.value)
        : entry.value;
    const value = key === SETTINGS_STORAGE_KEY && isRecord(imported)
      ? normalizeLegacyAiProfiles(imported)
      : imported;
    sets.push({
      key,
      value: secure ? await prepareSecurePersistedValue(value) : value,
      browserSnapshot: sanitizePortableValue(key, value),
    });
    if (key === SETTINGS_STORAGE_KEY && isRecord(value)) settingsSnapshot = value;
  }

  return { sets, keys: sets.map((set) => set.key), ...(settingsSnapshot ? { settingsSnapshot } : {}) };
}

/**
 * 把解析后的配置写回本机存储。
 *
 * 所有字段加密、对象密封与序列化都在首次写入前完成，并由一个原生事务提交。
 * AI 会话历史不是可移植配置，旧格式中的相关键会被明确跳过。
 */
export async function applyConfigImport(parsed: ParsedConfigImport): Promise<string[]> {
  if (!Object.keys(parsed.values).length) return [];
  const prepared = await prepareConfigValuesForWrite(parsed.values);
  await applyPersistedBatch({ sets: prepared.sets });
  if (prepared.settingsSnapshot) updateSettingsRuntimeSnapshot(prepared.settingsSnapshot);
  return prepared.keys;
}
