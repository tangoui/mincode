// Window event dispatched after a backup restore wrote MinCode's stored data
// (services/backup-host.ts). Stores that hold loaded copies of restored keys
// reload them; detail.keys lists every key that was set or deleted.
export const BACKUP_RESTORED_EVENT = "mincode:backup-restored";

export interface BackupRestoredDetail {
  keys: string[];
  /** Listeners hand in their reload; the restore keeps those keys frozen until it settles. */
  waitUntil?: (reload: Promise<unknown>) => void;
}

export function backupRestoredKeys(event: Event): string[] {
  const keys = (event as CustomEvent<Partial<BackupRestoredDetail> | undefined>).detail?.keys;
  return Array.isArray(keys) ? keys.filter((key): key is string => typeof key === "string") : [];
}

/**
 * Calls listener when a restore touched one of keys; a returned promise is
 * the store's reload, which the restore waits for. Returns the unsubscribe
 * function.
 */
export function onBackupRestored(keys: readonly string[], listener: () => Promise<unknown> | void): () => void {
  if (typeof window === "undefined" || typeof window.addEventListener !== "function") return () => undefined;
  const handle = (event: Event) => {
    if (!backupRestoredKeys(event).some((key) => keys.includes(key))) return;
    const reload = listener();
    if (reload) (event as CustomEvent<BackupRestoredDetail | undefined>).detail?.waitUntil?.(reload);
  };
  window.addEventListener(BACKUP_RESTORED_EVENT, handle);
  return () => window.removeEventListener(BACKUP_RESTORED_EVENT, handle);
}
