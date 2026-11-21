import { shallowRef } from "vue";
import { RUNTIME_PERFORMANCE_SETTING_KEYS } from "@/domain/performance/runtime-limits";
import {
  loadSecurePersistedValue,
  saveSecurePersistedValue,
} from "@/services/secure-persistent-store";
import { storeBrowserAiKeys, withBrowserAiKeys } from "@/services/browser-ai-keys";
import { isDesktopRuntime } from "@/services/desktop-bridge";

export const SETTINGS_STORAGE_KEY = "app_settings_v2";

type SettingsRecord = Record<string, unknown>;
let runtimeSnapshot: SettingsRecord | null = null;
let hydrationPromise: Promise<SettingsRecord | null> | null = null;
/** Reactive counter incremented on every persist; use as a computed/watch dependency. */
export const settingsSnapshotVersion = shallowRef(0);

function isRecord(value: unknown): value is SettingsRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function normalizeLegacyAiProfiles(value: SettingsRecord): SettingsRecord {
  const profiles = value.aiProfiles;
  if (!Array.isArray(profiles) || !profiles.some((profile) => isRecord(profile) && profile.provider === "plugin")) {
    return value;
  }
  return {
    ...value,
    aiProfiles: profiles.map((profile: unknown) => {
      if (!isRecord(profile) || profile.provider !== "plugin") return profile;
      const { source: _source, ...saved } = profile;
      return { ...saved, provider: "custom" };
    }),
  };
}

/**
 * Browser-local mode keeps AI API keys in this tab's sessionStorage (services/browser-ai-keys), never
 * in the localStorage snapshot; they are merged back into the settings read here. Desktop unchanged.
 */
function withSessionAiKeys(value: SettingsRecord) {
  return isDesktopRuntime() ? value : withBrowserAiKeys(value);
}

function readBrowserSnapshot() {
  let snapshot: SettingsRecord = {};
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(SETTINGS_STORAGE_KEY) || "{}");
    if (isRecord(parsed)) snapshot = parsed;
  } catch {
    // Unreadable snapshot: start from defaults.
  }
  return normalizeLegacyAiProfiles(withSessionAiKeys(snapshot));
}

function blankRecordValues(value: unknown) {
  if (!isRecord(value)) return {};
  return Object.fromEntries(Object.keys(value).map((key) => [key, ""]));
}

function clonePersistable<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

export function sanitizeSettingsForSync(value: SettingsRecord): SettingsRecord {
  const snapshot = sanitizeSettingsForBrowser(value);
  for (const key of RUNTIME_PERFORMANCE_SETTING_KEYS) delete snapshot[key];
  return snapshot;
}

export function sanitizeSettingsForBrowser(value: SettingsRecord): SettingsRecord {
  const snapshot = clonePersistable(value);
  if (Array.isArray(snapshot.aiProfiles)) {
    snapshot.aiProfiles = snapshot.aiProfiles.map((profile) =>
      isRecord(profile) ? { ...profile, apiKey: "" } : profile
    );
  }
  if (Array.isArray(snapshot.mcpServers)) {
    snapshot.mcpServers = snapshot.mcpServers.map((server) =>
      isRecord(server)
        ? {
            ...server,
            env: blankRecordValues(server.env),
            headers: blankRecordValues(server.headers),
          }
        : server
    );
  }
  return snapshot;
}

export function readSettingsSnapshot<T extends SettingsRecord = SettingsRecord>(): T {
  return (runtimeSnapshot ?? readBrowserSnapshot()) as T;
}

export async function hydrateSettingsSnapshot<T extends SettingsRecord = SettingsRecord>() {
  if (runtimeSnapshot) return runtimeSnapshot as T;
  if (!hydrationPromise) {
    hydrationPromise = loadSecurePersistedValue<SettingsRecord>(SETTINGS_STORAGE_KEY)
      .then((stored) => {
        runtimeSnapshot = normalizeLegacyAiProfiles(withSessionAiKeys(stored ?? readBrowserSnapshot()));
        return runtimeSnapshot;
      })
      .finally(() => {
        hydrationPromise = null;
      });
  }
  return (await hydrationPromise) as T;
}

export function updateSettingsRuntimeSnapshot(value: SettingsRecord): void {
  runtimeSnapshot = clonePersistable(normalizeLegacyAiProfiles(value));
  settingsSnapshotVersion.value++;
}

/**
 * Debounced/automatic saves stay best-effort. Explicit user saves pass
 * `strict` so a native failure rejects instead of reporting success.
 */
export async function persistSettingsSnapshot(
  value: SettingsRecord,
  options: { strict?: boolean } = {}
) {
  const nextSnapshot = clonePersistable(normalizeLegacyAiProfiles(value));
  updateSettingsRuntimeSnapshot(nextSnapshot);
  if (!isDesktopRuntime()) storeBrowserAiKeys(nextSnapshot);
  await saveSecurePersistedValue(
    SETTINGS_STORAGE_KEY,
    nextSnapshot,
    sanitizeSettingsForBrowser(nextSnapshot),
    options
  );
}
