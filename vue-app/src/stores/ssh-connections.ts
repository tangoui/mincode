import { computed, ref, watch } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type {
  FileTransferProtocol,
  SshAuthType,
  SshConnectionProfile,
  SshFileTransferConfig,
  SshJumpHostProfile,
  SshPortForwardRule,
  SshRuntimeCredentials,
} from "@/domain/ssh/types";
import { sanitizeSshProfile } from "@/domain/ssh/types";
import { normalizeConnectionGroupOrder } from "@/domain/connections/group-order";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";
import { decryptField, encryptField } from "@/services/field-crypto";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

const STORAGE_KEY = "app_ssh_connections_v1";
const CREDENTIALS_STORAGE_KEY = "app_ssh_credentials_v1";
const GROUP_ORDER_STORAGE_KEY = "app_ssh_connection_group_order_v1";
const PROFILE_STORAGE_VERSION = 2;
const PROFILE_PERSIST_DEBOUNCE_MS = 300;
const LEGACY_PROFILE_STORAGE_VERSION = 1;
const CREDENTIALS_STORAGE_VERSION = 1;
const AUTH_TYPES: ReadonlySet<SshAuthType> = new Set([
  "password",
  "key",
  "agent",
  "keyboard-interactive",
]);
const FILE_TRANSFER_PROTOCOLS: ReadonlySet<FileTransferProtocol> = new Set([
  "sftp",
  "ftp",
  "ftps-explicit",
]);

interface StoredProfiles {
  version: typeof PROFILE_STORAGE_VERSION | typeof LEGACY_PROFILE_STORAGE_VERSION;
  profiles: SshConnectionProfile[];
}

type EncryptedSshCredentials = Partial<Record<keyof SshRuntimeCredentials, string>>;

interface StoredCredentials {
  version: typeof CREDENTIALS_STORAGE_VERSION;
  credentialsByProfileId: Record<string, EncryptedSshCredentials>;
}

function compactCredentials(credentials: SshRuntimeCredentials): SshRuntimeCredentials {
  return Object.fromEntries(
    Object.entries(credentials).filter(([, value]) => typeof value === "string" && value.length > 0)
  ) as SshRuntimeCredentials;
}

function restoreEncryptedCredentials(value: unknown): EncryptedSshCredentials | null {
  if (!isRecord(value)) return null;
  const credentials: EncryptedSshCredentials = {};
  for (const key of [
    "password",
    "passphrase",
    "jumpPassword",
    "jumpPassphrase",
    "socksPassword",
    "ftpPassword",
    "totpUri",
  ] as const) {
    const secret = value[key];
    if (secret === undefined) continue;
    if (typeof secret !== "string" || !secret.startsWith("enc:v2:")) return null;
    credentials[key] = secret;
  }
  return credentials;
}

async function loadStoredCredentials(): Promise<Record<string, SshRuntimeCredentials>> {
  if (!isDesktopRuntime()) return {};
  const stored = await invokeBackend<unknown>("store_get", { key: CREDENTIALS_STORAGE_KEY });
  if (
    !isRecord(stored) ||
    stored.version !== CREDENTIALS_STORAGE_VERSION ||
    !isRecord(stored.credentialsByProfileId)
  ) {
    return {};
  }

  const entries = await Promise.all(
    Object.entries(stored.credentialsByProfileId).map(async ([profileId, encrypted]) => {
      try {
        const restored = restoreEncryptedCredentials(encrypted);
        if (!restored) return null;
        const credentials = await Promise.all(
          Object.entries(restored).map(async ([key, cipher]) => [
            key,
            await decryptField(cipher),
          ])
        );
        return [profileId, Object.fromEntries(credentials) as SshRuntimeCredentials] as const;
      } catch {
        // One damaged credential record must not hide every other saved SSH credential.
        return null;
      }
    })
  );
  return Object.fromEntries(
    entries.filter((entry): entry is NonNullable<typeof entry> => entry !== null)
  );
}

async function persistCredentials(credentialsByProfileId: Record<string, SshRuntimeCredentials>) {
  if (!isDesktopRuntime()) return;
  const entries = await Promise.all(
    Object.entries(credentialsByProfileId).map(async ([profileId, credentials]) => {
      const encrypted = await Promise.all(
        Object.entries(compactCredentials(credentials)).map(async ([key, plain]) => [
          key,
          await encryptField(plain),
        ])
      );
      return [profileId, Object.fromEntries(encrypted)] as const;
    })
  );

  if (!entries.length) {
    await invokeBackend<void>("store_del", { key: CREDENTIALS_STORAGE_KEY });
    return;
  }
  const snapshot: StoredCredentials = {
    version: CREDENTIALS_STORAGE_VERSION,
    credentialsByProfileId: Object.fromEntries(entries),
  };
  await invokeBackend<void>("store_set", { key: CREDENTIALS_STORAGE_KEY, value: snapshot });
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validPort(value: unknown): value is number {
  return Number.isInteger(value) && Number(value) >= 1 && Number(value) <= 65535;
}

function optionalString(value: unknown) {
  return typeof value === "string" && value.length <= 32_768 ? value : undefined;
}

function restorePostConnectCommands(value: unknown) {
  if (!Array.isArray(value) || value.length > 32) return [];
  return value
    .filter((command): command is string => typeof command === "string" && command.length <= 8192)
    .map((command) => command.trim())
    .filter(Boolean);
}

function restoreTags(value: unknown) {
  if (!Array.isArray(value) || value.length > 64) return [];
  return [
    ...new Set(
      value
        .filter((tag): tag is string => typeof tag === "string" && tag.trim().length > 0)
        .map((tag) => tag.trim())
    ),
  ];
}

function restoreForward(value: unknown): SshPortForwardRule | null {
  if (
    !isRecord(value) ||
    typeof value.id !== "string" ||
    !value.id ||
    (value.type !== "L" && value.type !== "R" && value.type !== "D") ||
    !validPort(value.bindPort) ||
    typeof value.targetHost !== "string" ||
    (value.type !== "D" && (!value.targetHost.trim() || !validPort(value.targetPort)))
  ) {
    return null;
  }
  return {
    id: value.id,
    type: value.type,
    bindPort: value.bindPort,
    targetHost: value.type === "D" ? "" : value.targetHost,
    targetPort: value.type === "D" ? 0 : Number(value.targetPort),
    label: optionalString(value.label),
  };
}

function restoreFileTransfer(
  value: unknown,
  profile: { host: string; port: number; username: string }
): SshFileTransferConfig | null {
  if (value === undefined) {
    return {
      enabled: true,
      protocol: "sftp",
      host: profile.host,
      port: profile.port,
      username: profile.username,
      viaCurrentSsh: false,
    };
  }
  if (
    !isRecord(value) ||
    typeof value.protocol !== "string" ||
    !FILE_TRANSFER_PROTOCOLS.has(value.protocol as FileTransferProtocol) ||
    typeof value.host !== "string" ||
    !value.host.trim() ||
    !validPort(value.port) ||
    typeof value.username !== "string" ||
    !value.username.trim()
  ) {
    return null;
  }
  const protocol = value.protocol as FileTransferProtocol;
  return {
    enabled: value.enabled !== false,
    protocol,
    host: value.host,
    port: value.port,
    username: value.username,
    viaCurrentSsh: protocol !== "sftp" && value.viaCurrentSsh === true,
    tlsServerName:
      protocol === "ftps-explicit" ? optionalString(value.tlsServerName)?.trim() || undefined : undefined,
  };
}

function restoreJumpHost(value: unknown): SshJumpHostProfile | undefined | null {
  if (value === undefined) return undefined;
  if (
    !isRecord(value) ||
    typeof value.host !== "string" ||
    !value.host.trim() ||
    !validPort(value.port) ||
    typeof value.username !== "string" ||
    !value.username.trim() ||
    (value.authType !== "password" && value.authType !== "key" && value.authType !== "agent")
  ) {
    return null;
  }
  return {
    host: value.host,
    port: value.port,
    username: value.username,
    authType: value.authType,
    keyFile: optionalString(value.keyFile),
  };
}

export function restoreSshProfile(value: unknown): SshConnectionProfile | null {
  if (
    !isRecord(value) ||
    typeof value.id !== "string" ||
    !value.id ||
    typeof value.name !== "string" ||
    typeof value.host !== "string" ||
    !value.host.trim() ||
    !validPort(value.port) ||
    typeof value.username !== "string" ||
    !value.username.trim() ||
    typeof value.authType !== "string" ||
    !AUTH_TYPES.has(value.authType as SshAuthType) ||
    (value.portForwards !== undefined && !Array.isArray(value.portForwards)) ||
    (Array.isArray(value.portForwards) && value.portForwards.length > 32) ||
    typeof value.createdAt !== "number" ||
    typeof value.updatedAt !== "number"
  ) {
    return null;
  }
  const portForwards = (value.portForwards ?? []).map(restoreForward);
  if (portForwards.some((rule) => rule === null)) return null;
  const jumpHost = restoreJumpHost(value.jumpHost);
  if (jumpHost === null) return null;
  const fileTransfer = restoreFileTransfer(value.fileTransfer, {
    host: value.host,
    port: value.port,
    username: value.username,
  });
  if (fileTransfer === null) return null;
  return {
    id: value.id,
    name: value.name,
    host: value.host,
    port: value.port,
    username: value.username,
    authType: value.authType as SshAuthType,
    keyFile: optionalString(value.keyFile),
    description: optionalString(value.description)?.trim() || undefined,
    defaultPath: optionalString(value.defaultPath)?.trim() || undefined,
    postConnectCommands: restorePostConnectCommands(value.postConnectCommands),
    group: optionalString(value.group)?.trim() || undefined,
    tags: restoreTags(value.tags),
    favorite: value.favorite === true,
    // Profiles saved before this preference existed should retain credentials by default.
    saveCredentials: value.saveCredentials !== false,
    portForwards: portForwards as SshPortForwardRule[],
    jumpHost,
    fileTransfer,
    algorithmPolicy: value.algorithmPolicy === "legacy" ? "legacy" : "modern",
    keepAlive: isRecord(value.keepAlive)
      ? {
          intervalSeconds: Number(value.keepAlive.intervalSeconds) || 0,
          maxMisses: Number(value.keepAlive.maxMisses) || 3,
        }
      : { intervalSeconds: 30, maxMisses: 3 },
    reconnect: isRecord(value.reconnect)
      ? {
          enabled: value.reconnect.enabled !== false,
          maxAttempts: Number(value.reconnect.maxAttempts) || 5,
          baseDelayMs: Number(value.reconnect.baseDelayMs) || 1000,
          maxDelayMs: Number(value.reconnect.maxDelayMs) || 15_000,
        }
      : { enabled: true, maxAttempts: 5, baseDelayMs: 1000, maxDelayMs: 15_000 },
    sshConfigSource: isRecord(value.sshConfigSource)
      ? {
          alias: optionalString(value.sshConfigSource.alias) ?? "",
          sourcePath: optionalString(value.sshConfigSource.sourcePath) ?? "",
          rootPath: optionalString(value.sshConfigSource.rootPath)?.trim() || undefined,
          syncedAt: Number(value.sshConfigSource.syncedAt) || value.updatedAt,
        }
      : undefined,
    createdAt: value.createdAt,
    updatedAt: value.updatedAt,
  };
}

export function restoreSshProfiles(value: unknown) {
  if (
    !isRecord(value) ||
    (value.version !== PROFILE_STORAGE_VERSION &&
      value.version !== LEGACY_PROFILE_STORAGE_VERSION) ||
    !Array.isArray(value.profiles)
  ) {
    return [];
  }
  return value.profiles.flatMap((profile) => {
    const restored = restoreSshProfile(profile);
    return restored ? [restored] : [];
  });
}

function readProfiles() {
  if (typeof window === "undefined") return [];
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    return raw ? restoreSshProfiles(JSON.parse(raw) as unknown) : [];
  } catch {
    return [];
  }
}

export const useSshConnectionsStore = defineStore("ssh-connections", () => {
  const profiles = ref<SshConnectionProfile[]>(readProfiles());
  const credentialsByProfileId = ref<Record<string, SshRuntimeCredentials>>({});
  const groupOrder = ref<string[]>([]);
  const hydrated = ref(false);
  const sortedProfiles = computed(() =>
    [...profiles.value].sort((a, b) => b.updatedAt - a.updatedAt || a.name.localeCompare(b.name))
  );
  let profilePersistenceQueue = Promise.resolve();
  let hydrationPromise: Promise<void> | null = null;
  let profilePersistTimer: ReturnType<typeof setTimeout> | undefined;
  let lastPersistedProfiles = "";

  function clearProfilePersistTimer() {
    if (profilePersistTimer === undefined) return;
    clearTimeout(profilePersistTimer);
    profilePersistTimer = undefined;
  }

  /** 立即持久化（显式保存路径使用），同时取消尚未触发的防抖写入。 */
  function persistProfiles() {
    clearProfilePersistTimer();
    const snapshot: StoredProfiles = {
      version: PROFILE_STORAGE_VERSION,
      profiles: profiles.value.map((profile) => ({ ...profile })),
    };
    lastPersistedProfiles = JSON.stringify(snapshot.profiles);
    profilePersistenceQueue = profilePersistenceQueue
      .catch(() => undefined)
      .then(() => savePersistedValue(STORAGE_KEY, snapshot));
    return profilePersistenceQueue;
  }

  function schedulePersistProfiles() {
    clearProfilePersistTimer();
    profilePersistTimer = setTimeout(() => {
      profilePersistTimer = undefined;
      // 显式保存已写入同一快照时跳过重复写入。
      if (JSON.stringify(profiles.value) === lastPersistedProfiles) return;
      void persistProfiles();
    }, PROFILE_PERSIST_DEBOUNCE_MS);
  }

  /** 退出/卸载前写入尚在防抖中的改动，并等待已排队的写入完成。 */
  function flushPersistence() {
    if (profilePersistTimer !== undefined && hydrated.value) return persistProfiles();
    return profilePersistenceQueue;
  }

  async function reorderGroups(order: string[]) {
    const normalized = normalizeConnectionGroupOrder(order);
    groupOrder.value = normalized;
    await savePersistedValue(GROUP_ORDER_STORAGE_KEY, normalized);
  }

  async function moveProfileToGroup(profileId: string, group?: string) {
    const profile = findProfile(profileId);
    if (!profile) throw new Error("SSH connection not found");

    const normalizedGroup = group?.trim() || undefined;
    if ((profile.group?.trim() || undefined) === normalizedGroup) return profile;

    const saved = saveProfile({ ...profile, group: normalizedGroup });
    await persistProfiles();
    return saved;
  }

  function saveProfile(profile: SshConnectionProfile) {
    const next = sanitizeSshProfile(profile);
    const index = profiles.value.findIndex((item) => item.id === next.id);
    if (index >= 0) profiles.value.splice(index, 1, next);
    else profiles.value.push(next);
    return next;
  }

  function removeProfile(profileId: string) {
    const index = profiles.value.findIndex((item) => item.id === profileId);
    if (index < 0) return false;
    profiles.value.splice(index, 1);
    if (hydrated.value) void persistProfiles().catch(() => {});
    void removeCredentials(profileId).catch(() => {});
    return true;
  }

  function credentialsFor(profileId: string) {
    return credentialsByProfileId.value[profileId];
  }

  function hasSavedCredentials(profileId: string) {
    return Boolean(credentialsFor(profileId) && Object.keys(credentialsFor(profileId)!).length);
  }

  async function saveCredentials(profileId: string, credentials: SshRuntimeCredentials) {
    if (!isDesktopRuntime()) return;
    const compacted = compactCredentials(credentials);
    if (!Object.keys(compacted).length) {
      await removeCredentials(profileId);
      return;
    }
    credentialsByProfileId.value = {
      ...credentialsByProfileId.value,
      [profileId]: compacted,
    };
    await persistCredentials(credentialsByProfileId.value);
  }

  async function removeCredentials(profileId: string) {
    if (!(profileId in credentialsByProfileId.value)) return;
    const remaining = Object.fromEntries(
      Object.entries(credentialsByProfileId.value).filter(([id]) => id !== profileId)
    ) as Record<string, SshRuntimeCredentials>;
    credentialsByProfileId.value = remaining;
    await persistCredentials(remaining);
  }

  function findProfile(profileId: string) {
    return profiles.value.find((item) => item.id === profileId);
  }

  async function hydrate() {
    if (hydrated.value) return;
    if (hydrationPromise) return hydrationPromise;

    hydrationPromise = (async () => {
      const [stored, storedGroupOrder] = await Promise.all([
        loadPersistedValue<StoredProfiles>(STORAGE_KEY),
        loadPersistedValue<unknown>(GROUP_ORDER_STORAGE_KEY),
      ]);
      profiles.value = stored ? restoreSshProfiles(stored) : [];
      groupOrder.value = normalizeConnectionGroupOrder(storedGroupOrder);

      try {
        credentialsByProfileId.value = await loadStoredCredentials();
      } catch {
        credentialsByProfileId.value = {};
      }
      hydrated.value = true;

      // Mirror the restored snapshot into the native store as well, migrating valid browser-only profiles.
      await persistProfiles();
    })().finally(() => {
      hydrationPromise = null;
    });

    return hydrationPromise;
  }

  watch(
    profiles,
    () => {
      if (!hydrated.value) return;
      schedulePersistProfiles();
    },
    { deep: true }
  );

  return {
    profiles,
    sortedProfiles,
    groupOrder,
    hydrated,
    hydrate,
    saveProfile,
    persistProfiles,
    flushPersistence,
    reorderGroups,
    moveProfileToGroup,
    removeProfile,
    findProfile,
    credentialsFor,
    hasSavedCredentials,
    saveCredentials,
    removeCredentials,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useSshConnectionsStore, import.meta.hot));
}
