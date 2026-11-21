export const UPDATE_PREFERENCES_KEY = "app_update_preferences_v1";
/** Leaves startup work (workspace restore, plugin activation) alone before the first check. */
export const UPDATE_CHECK_INITIAL_DELAY_MS = 30_000;
export const UPDATE_CHECK_INTERVAL_MS = 6 * 60 * 60 * 1000;
/** Re-evaluates the interval periodically so sleep/wake and long sessions still catch up. */
export const UPDATE_CHECK_TICK_MS = 30 * 60 * 1000;

export interface UpdatePreferences {
  version: 1;
  autoCheck: boolean;
  skippedVersion?: string;
  notifiedVersion?: string;
  lastCheckedAt?: number;
}

export interface UpdateAvailability {
  available: boolean;
  version: string;
}

function readVersion(value: unknown) {
  return typeof value === "string" && value.trim() ? value.trim().slice(0, 64) : undefined;
}

export function normalizeUpdatePreferences(value: unknown): UpdatePreferences {
  const record = typeof value === "object" && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
  const lastCheckedAt = typeof record.lastCheckedAt === "number" && Number.isFinite(record.lastCheckedAt) && record.lastCheckedAt > 0
    ? record.lastCheckedAt
    : undefined;
  return {
    version: 1,
    autoCheck: record.autoCheck !== false,
    skippedVersion: readVersion(record.skippedVersion),
    notifiedVersion: readVersion(record.notifiedVersion),
    lastCheckedAt,
  };
}

export function isAutoCheckDue(preferences: UpdatePreferences, now: number): boolean {
  if (!preferences.autoCheck) return false;
  const last = preferences.lastCheckedAt;
  // A clock moved backwards must not postpone checks indefinitely.
  return last === undefined || last > now || now - last >= UPDATE_CHECK_INTERVAL_MS;
}

/** The version worth flagging in the UI, unless the user chose to skip it. */
export function pendingUpdateVersion(info: UpdateAvailability | undefined, preferences: UpdatePreferences): string | undefined {
  if (!info?.available) return undefined;
  const version = readVersion(info.version);
  if (!version || version === preferences.skippedVersion) return undefined;
  return version;
}

/** Automatic checks notify once per version; manual checks always report their own result. */
export function shouldNotifyUpdate(info: UpdateAvailability | undefined, preferences: UpdatePreferences): boolean {
  const version = pendingUpdateVersion(info, preferences);
  return version !== undefined && version !== preferences.notifiedVersion;
}
