import {
  CONFIG_SYNC_FORMAT,
  CONFIG_SYNC_VERSION,
  isConfigSyncStorageKey,
  mergeConfigSyncValueWithLocalSecrets,
  sanitizeConfigSyncValue,
  type ConfigSyncPersistedEntry,
  type ConfigSyncSnapshot,
} from "@/domain/settings/config-sync";
import {
  HTTP_RESOURCES_STORAGE_KEY,
  HTTP_STATE_STORAGE_KEY,
  sanitizeHttpStateForBrowser,
} from "@/services/http-state-persistence";
import {
  applyPersistedBatch,
  loadAllPersistedValues,
  type PersistedBatchSet,
} from "@/services/persistent-store";
import {
  loadSecurePersistedValue,
  prepareSecurePersistedValue,
} from "@/services/secure-persistent-store";
import { isRetiredAiHistoryStorageKey } from "@/services/ai-conversation-persistence";
import {
  parsePluginConfigurationSync,
  pluginConfigurationSyncEntry,
  PLUGIN_CONFIGURATION_SYNC_KEY,
} from "@/domain/plugins/configuration";
import {
  exportPluginConfigurationSync,
  importPluginConfigurationSync,
} from "@/services/plugin-configuration-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";
import {
  mergeSettingsWithLocalSecrets,
  sanitizeSettingsForSync,
  SETTINGS_STORAGE_KEY,
  updateSettingsRuntimeSnapshot,
} from "@/services/settings-persistence";

// 与 services/http-socketio-persistence.ts 保持一致；这里不导入以免把 Socket.IO 模块拉进同步链路。
const HTTP_SOCKETIO_EVENTS_STORAGE_KEY = "app_http_socketio_events_v1";
const LICENSE_STORAGE_KEY = "app_license";
// 与 domain/security/app-lock.ts 的 APP_LOCK_STORAGE_KEY 及后端 applockpolicy.StorageKey 保持一致；
// 该键由后端独占，store_* 会拒绝，因此同步快照中必须跳过。
const APP_LOCK_STORAGE_KEY = "app_lock_v1";
const SSH_CREDENTIALS_STORAGE_KEY = "app_ssh_credentials_v1";
const SECURE_VALUE_PREFIX = "enc:v2:";

const KNOWN_SECURE_STORAGE_KEYS = new Set([
  SETTINGS_STORAGE_KEY,
  "app_ai_approval_policy_v1",
  "app_ai_tool_authorizations_v1",
  HTTP_RESOURCES_STORAGE_KEY,
]);

export interface AppliedConfigSyncValues {
  persistedValues: Record<string, unknown>;
  skippedKeys: string[];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isHttpStorageKey(key: string) {
  return key === HTTP_RESOURCES_STORAGE_KEY
    || key === HTTP_SOCKETIO_EVENTS_STORAGE_KEY
    || key === HTTP_STATE_STORAGE_KEY
    || key.startsWith(`${HTTP_STATE_STORAGE_KEY}__workspace__`);
}

export function isKnownSecureStorageKey(key: string) {
  return KNOWN_SECURE_STORAGE_KEYS.has(key) || isHttpStorageKey(key);
}

/**
 * 「最近打开」是本机使用记录（SSH 主机最近打开时间、数据库最近列表清空时间），
 * 只存本机：不进入配置同步，也不进入配置导出（config-portability 复用此集合）。
 */
export const DEVICE_LOCAL_HISTORY_STORAGE_KEYS: ReadonlySet<string> = new Set([
  "app_ssh_recent_v1",
  "app_db_recent_cleared_at_v1",
]);

function isDeviceLocalStorageKey(key: string) {
  return !isConfigSyncStorageKey(key)
    || key === LICENSE_STORAGE_KEY
    || key === SSH_CREDENTIALS_STORAGE_KEY
    || DEVICE_LOCAL_HISTORY_STORAGE_KEYS.has(key)
    // 锁屏记录由后端独占，旧版同步快照里可能带着它：跳过而不是让整批写入被后端拒绝。
    || key === APP_LOCK_STORAGE_KEY;
}

export function sanitizePortableValue(key: string, value: unknown) {
  if (key === SETTINGS_STORAGE_KEY && isRecord(value)) {
    return sanitizeSettingsForSync(value);
  }
  if (isHttpStorageKey(key)) return sanitizeHttpStateForBrowser(value);
  return sanitizeConfigSyncValue(value);
}

function itemIdentity(value: unknown): string | undefined {
  if (!isRecord(value)) return undefined;
  for (const key of ["id", "key", "name"]) {
    const candidate = value[key];
    if (typeof candidate === "string" && candidate) return `${key}:${candidate}`;
  }
  return undefined;
}

/** Rehydrates deliberately blank HTTP fields from the same item on this device. */
function mergeRedactedHttpValue(remoteValue: unknown, localValue: unknown): unknown {
  if (remoteValue === "" && typeof localValue === "string") return localValue;
  if (Array.isArray(remoteValue)) {
    const localItems = Array.isArray(localValue) ? localValue : [];
    const localByIdentity = new Map(
      localItems.flatMap((item) => {
        const identity = itemIdentity(item);
        return identity ? [[identity, item] as const] : [];
      })
    );
    return remoteValue.map((item, index) => {
      const identity = itemIdentity(item);
      return mergeRedactedHttpValue(
        item,
        identity ? localByIdentity.get(identity) : localItems[index]
      );
    });
  }
  if (!isRecord(remoteValue)) return remoteValue;
  const localRecord = isRecord(localValue) ? localValue : {};
  return Object.fromEntries(
    Object.entries(remoteValue).map(([key, child]) => [
      key,
      mergeRedactedHttpValue(child, localRecord[key]),
    ])
  );
}

function usesSecureStorage(
  key: string,
  entry: ConfigSyncPersistedEntry,
  persistedValues: Record<string, unknown>
) {
  const rawValue = persistedValues[key];
  return isKnownSecureStorageKey(key)
    || entry.storage === "secure"
    || (typeof rawValue === "string" && rawValue.startsWith(SECURE_VALUE_PREFIX));
}

async function readLogicalValue(
  key: string,
  secure: boolean,
  persistedValues: Record<string, unknown>
) {
  if (secure) return loadSecurePersistedValue<unknown>(key);
  return persistedValues[key];
}

function mergeWithDeviceLocalSecrets(key: string, remoteValue: unknown, localValue: unknown) {
  let merged = mergeConfigSyncValueWithLocalSecrets(remoteValue, localValue);
  if (isHttpStorageKey(key)) merged = mergeRedactedHttpValue(merged, localValue);
  if (key === SETTINGS_STORAGE_KEY && isRecord(merged)) {
    merged = mergeSettingsWithLocalSecrets(merged, isRecord(localValue) ? localValue : {});
  }
  return merged;
}

export async function createConfigSyncSnapshot(): Promise<ConfigSyncSnapshot> {
  const stored = await loadAllPersistedValues();
  const entries = await Promise.all(
    Object.entries(stored).map(async ([key, rawValue]) => {
      if (isRetiredAiHistoryStorageKey(key) || isDeviceLocalStorageKey(key) || rawValue === undefined) return null;
      const secure = isKnownSecureStorageKey(key)
        || (typeof rawValue === "string" && rawValue.startsWith(SECURE_VALUE_PREFIX));
      const logicalValue = secure
        ? await loadSecurePersistedValue<unknown>(key)
        : rawValue;
      if (logicalValue === null || logicalValue === undefined) return null;
      return [
        key,
        {
          storage: secure ? "secure" : "plain",
          value: sanitizePortableValue(key, logicalValue),
        } satisfies ConfigSyncPersistedEntry,
      ] as const;
    })
  );

  const persistedValues: Record<string, ConfigSyncPersistedEntry> = Object.fromEntries(
    entries.filter((entry): entry is NonNullable<typeof entry> => entry !== null && entry[0] !== PLUGIN_CONFIGURATION_SYNC_KEY)
  );
  // Plugin settings are a synthetic entry owned by the Go host: only global,
  // non-secret values; secrets and workspace overrides are never synced.
  const pluginSettings = pluginConfigurationSyncEntry(await exportPluginConfigurationSync());
  if (pluginSettings) persistedValues[PLUGIN_CONFIGURATION_SYNC_KEY] = pluginSettings;

  return {
    format: CONFIG_SYNC_FORMAT,
    version: CONFIG_SYNC_VERSION,
    exportedAt: Date.now(),
    persistedValues,
  };
}

export async function applyConfigSyncSnapshot(
  snapshot: ConfigSyncSnapshot
): Promise<AppliedConfigSyncValues> {
  const stored = await loadAllPersistedValues();
  const applied: Record<string, unknown> = {};
  const skippedKeys: string[] = [];
  const preparedSets: PersistedBatchSet[] = [];
  let nextSettingsSnapshot: Record<string, unknown> | undefined;

  for (const [key, entry] of Object.entries(snapshot.persistedValues)) {
    if (key === PLUGIN_CONFIGURATION_SYNC_KEY) {
      // Applied through the Go host (which re-validates against each installed
      // plugin's schema), never persisted as a frontend store value.
      const data = entry.storage === "plain" ? parsePluginConfigurationSync(entry.value) : undefined;
      if (!data || !isDesktopRuntime()) {
        skippedKeys.push(key);
        continue;
      }
      try {
        await importPluginConfigurationSync(data);
      } catch {
        // Reported through skippedKeys like any other entry that was not applied.
        skippedKeys.push(key);
      }
      continue;
    }
    if (isRetiredAiHistoryStorageKey(key)) {
      skippedKeys.push(key);
      continue;
    }
    if (isDeviceLocalStorageKey(key)) {
      skippedKeys.push(key);
      continue;
    }
    const secure = usesSecureStorage(key, entry, stored);
    const localValue = await readLogicalValue(key, secure, stored);
    const mergedValue = mergeWithDeviceLocalSecrets(key, entry.value, localValue);

    const persistedValue = secure
      ? await prepareSecurePersistedValue(mergedValue)
      : mergedValue;
    preparedSets.push({
      key,
      value: persistedValue,
      browserSnapshot: sanitizePortableValue(key, mergedValue),
    });
    if (key === SETTINGS_STORAGE_KEY && isRecord(mergedValue)) {
      nextSettingsSnapshot = mergedValue;
    }
    applied[key] = mergedValue;
  }

  await applyPersistedBatch({ sets: preparedSets });
  if (nextSettingsSnapshot) updateSettingsRuntimeSnapshot(nextSettingsSnapshot);
  return { persistedValues: applied, skippedKeys };
}
