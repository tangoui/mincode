/**
 * When each SSH host was last opened, for the overview's "recently opened" list.
 * Kept apart from host profiles (key `app_ssh_recent_v1`, shape
 * `{ version: 1, openedAt: {[profileId]: epochMs}, clearedAt: epochMs }`) so it
 * never enters profile export or sync. `clearedAt` hides every record opened at
 * or before it without deleting the timestamps.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

export const SSH_RECENT_STORAGE_KEY = "app_ssh_recent_v1";
const SSH_RECENT_STORAGE_VERSION = 1;

interface StoredSshRecent {
  version: typeof SSH_RECENT_STORAGE_VERSION;
  openedAt: Record<string, number>;
  clearedAt: number;
}

function isValidTime(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0;
}

function restoreStoredRecent(value: unknown): { openedAt: Record<string, number>; clearedAt: number } {
  const empty = { openedAt: {}, clearedAt: 0 };
  if (!value || typeof value !== "object" || Array.isArray(value)) return empty;
  const stored = value as Partial<Record<keyof StoredSshRecent, unknown>>;
  if (stored.version !== SSH_RECENT_STORAGE_VERSION) return empty;
  const openedAt: Record<string, number> = {};
  if (stored.openedAt && typeof stored.openedAt === "object" && !Array.isArray(stored.openedAt)) {
    for (const [id, at] of Object.entries(stored.openedAt as Record<string, unknown>)) {
      if (id && isValidTime(at)) openedAt[id] = at;
    }
  }
  return { openedAt, clearedAt: isValidTime(stored.clearedAt) ? stored.clearedAt : 0 };
}

export const useSshRecentStore = defineStore("ssh-recent", () => {
  const openedAt = ref<Record<string, number>>({});
  const clearedAt = ref(0);
  const hydrated = ref(false);
  let hydratePromise: Promise<void> | undefined;

  function persist() {
    const snapshot: StoredSshRecent = {
      version: SSH_RECENT_STORAGE_VERSION,
      openedAt: { ...openedAt.value },
      clearedAt: clearedAt.value,
    };
    void savePersistedValue(SSH_RECENT_STORAGE_KEY, snapshot).catch(() => undefined);
  }

  function markOpened(profileId: string, at: number = Date.now()) {
    if (!profileId) return;
    openedAt.value = { ...openedAt.value, [profileId]: at };
    persist();
  }

  function prune(existingIds: ReadonlySet<string>) {
    const entries = Object.entries(openedAt.value);
    const kept = entries.filter(([id]) => existingIds.has(id));
    if (kept.length === entries.length) return;
    openedAt.value = Object.fromEntries(kept);
    persist();
  }

  function clearRecent(at: number = Date.now()) {
    clearedAt.value = at;
    persist();
  }

  async function hydrate() {
    if (hydrated.value) return;
    hydratePromise ??= (async () => {
      let loaded: unknown;
      try {
        loaded = await loadPersistedValue<unknown>(SSH_RECENT_STORAGE_KEY);
      } catch {
        loaded = null;
      }
      const stored = restoreStoredRecent(loaded);
      // Anything recorded while loading is newer than what was on disk, and the
      // save it triggered only carried the partial in-memory state: merge and re-save.
      const changedWhileLoading = Object.keys(openedAt.value).length > 0 || clearedAt.value > 0;
      openedAt.value = { ...stored.openedAt, ...openedAt.value };
      clearedAt.value = Math.max(stored.clearedAt, clearedAt.value);
      hydrated.value = true;
      if (changedWhileLoading) persist();
    })().finally(() => {
      hydratePromise = undefined;
    });
    return hydratePromise;
  }

  return { openedAt, clearedAt, hydrated, markOpened, prune, clearRecent, hydrate };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useSshRecentStore, import.meta.hot));
}
