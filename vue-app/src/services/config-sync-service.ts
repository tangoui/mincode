import {
  normalizeConfigSyncRepository,
  type ConfigSyncRepository,
} from "@/domain/settings/config-sync";
import {
  loadSecurePersistedValue,
  saveSecurePersistedValue,
} from "@/services/secure-persistent-store";
import { invokeBackend } from "@/services/desktop-bridge";

export const CONFIG_SYNC_REPOSITORY_KEY = "app_config_sync_repository_v1";

interface ConfigSyncConnectionResult {
  ok: boolean;
  default_branch: string;
  branch_exists: boolean;
  repository_empty: boolean;
}

interface ConfigSyncPullResult {
  exists: boolean;
  payload: string;
  commit: string;
  updated_at: number;
}

interface ConfigSyncPushResult {
  changed: boolean;
  commit: string;
}

function credentials(repository: ConfigSyncRepository) {
  return {
    mode: repository.authMode,
    username: repository.authMode === "https-token" ? repository.username : "",
    token: repository.authMode === "https-token" ? repository.token : "",
  };
}

function repositoryArgs(repository: ConfigSyncRepository) {
  return {
    remoteUrl: repository.remoteUrl.trim(),
    branch: repository.branch.trim() || "main",
    credentials: credentials(repository),
  };
}

export async function loadConfigSyncRepository(): Promise<ConfigSyncRepository> {
  return normalizeConfigSyncRepository(
    await loadSecurePersistedValue<unknown>(CONFIG_SYNC_REPOSITORY_KEY)
  );
}

export async function saveConfigSyncRepository(repository: ConfigSyncRepository): Promise<void> {
  const normalized = normalizeConfigSyncRepository(repository);
  if (!normalized.rememberPassword) normalized.encryptionPassword = "";
  await saveSecurePersistedValue(
    CONFIG_SYNC_REPOSITORY_KEY,
    normalized,
    { ...normalized, token: "", encryptionPassword: "" },
    { strict: true }
  );
}

export async function clearConfigSyncRepository(): Promise<void> {
  await saveConfigSyncRepository(normalizeConfigSyncRepository(undefined));
}

export function testConfigSyncConnection(repository: ConfigSyncRepository) {
  return invokeBackend<ConfigSyncConnectionResult>(
    "config_sync_test_connection",
    repositoryArgs(repository)
  );
}

export function pullConfigSyncSnapshot(repository: ConfigSyncRepository) {
  return invokeBackend<ConfigSyncPullResult>("config_sync_pull", repositoryArgs(repository));
}

export function pushConfigSyncSnapshot(repository: ConfigSyncRepository, payload: string) {
  return invokeBackend<ConfigSyncPushResult>("config_sync_push", {
    ...repositoryArgs(repository),
    payload,
  });
}
