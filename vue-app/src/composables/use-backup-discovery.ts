import { onBeforeUnmount, ref, watch, type Ref } from "vue";
import { discoverBackupTargets, type BackupDiscovery } from "@/services/database-backup-service";
import { nativeErrorMessage } from "@/services/desktop-bridge";

/** Independent native sessions: never switches the workspace's active connection. */
export function useBackupDiscovery(connectionId: Ref<string>, database: Ref<string>, active: Ref<boolean>) {
  const result = ref<BackupDiscovery>();
  const loading = ref(false);
  const error = ref("");
  let controller: AbortController | undefined;
  let generation = 0;
  let loadedConnection = "";
  async function refresh() {
    controller?.abort();
    const version = ++generation;
    const id = connectionId.value;
    const selectedDatabase = database.value;
    if (id !== loadedConnection || !active.value) result.value = undefined;
    loadedConnection = id;
    error.value = "";
    loading.value = false;
    if (!active.value || !id) return;
    const current = new AbortController();
    controller = current;
    loading.value = true;
    try {
      // Always retain the full database list, even when opening an existing
      // job with one target. Object discovery must not collapse the picker.
      const root = await discoverBackupTargets(id, "", current.signal);
      if (version !== generation) return;
      result.value = root;
      if (selectedDatabase) {
        const details = await discoverBackupTargets(id, selectedDatabase, current.signal);
        if (version !== generation) return;
        const databases = new Map(root.schema.databases.map(item => [item.name, item]));
        for (const item of details.schema.databases) databases.set(item.name, item);
        result.value = { ...details, schema: { databases: [...databases.values()] } };
      }
    } catch (cause) {
      if (version === generation && !current.signal.aborted) error.value = nativeErrorMessage(cause);
    } finally {
      if (version === generation) loading.value = false;
    }
  }
  // Invalidate synchronously: a previous connection's response must not be
  // accepted in the tick between changing the draft and Vue's next flush.
  watch([connectionId, database, active], () => { void refresh(); }, { immediate: true, flush: "sync" });
  onBeforeUnmount(() => { generation++; controller?.abort(); });
  return { result, loading, error, refresh };
}
