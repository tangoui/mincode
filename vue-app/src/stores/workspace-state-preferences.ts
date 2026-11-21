import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  resolveWorkspaceStateScope,
  restoreWorkspaceStatePreferences,
  updateWorkspaceStateIsolation,
  WORKSPACE_STATE_PREFERENCES_VERSION,
  type WorkspaceStatePreferencesSnapshot,
} from "@/domain/workspace/state-scope";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

export const WORKSPACE_STATE_PREFERENCES_KEY = "app_workspace_state_preferences_v1";

export const useWorkspaceStatePreferencesStore = defineStore(
  "workspace-state-preferences",
  () => {
    const snapshot = ref<WorkspaceStatePreferencesSnapshot>(
      restoreWorkspaceStatePreferences(undefined)
    );
    const hydrated = ref(false);
    let hydrationPromise: Promise<void> | undefined;

    const isolationEnabled = computed(() => snapshot.value.isolationEnabled);
    const sharedWorkspaceId = computed(() => snapshot.value.sharedWorkspaceId);

    function scopeIdFor(workspaceId: string) {
      return resolveWorkspaceStateScope(snapshot.value, workspaceId);
    }

    async function hydrate() {
      if (hydrated.value) return;
      if (hydrationPromise) return hydrationPromise;
      hydrationPromise = (async () => {
        const stored = await loadPersistedValue<unknown>(WORKSPACE_STATE_PREFERENCES_KEY);
        snapshot.value = restoreWorkspaceStatePreferences(stored);
        hydrated.value = true;
        if (JSON.stringify(stored) !== JSON.stringify(persistedPreferences())) void flushPersistence();
      })();
      try {
        await hydrationPromise;
      } finally {
        hydrationPromise = undefined;
      }
    }

    async function flushPersistence() {
      if (!hydrated.value) return;
      await savePersistedValue(WORKSPACE_STATE_PREFERENCES_KEY, persistedPreferences());
    }

    function persistedPreferences(): WorkspaceStatePreferencesSnapshot {
      return {
        version: WORKSPACE_STATE_PREFERENCES_VERSION,
        isolationEnabled: snapshot.value.isolationEnabled,
        sharedWorkspaceId: snapshot.value.sharedWorkspaceId,
      };
    }

    async function setIsolationEnabled(enabled: boolean, activeWorkspaceId: string) {
      snapshot.value = updateWorkspaceStateIsolation(
        snapshot.value,
        enabled,
        activeWorkspaceId
      );
      await flushPersistence();
    }

    return {
      hydrated,
      isolationEnabled,
      sharedWorkspaceId,
      hydrate,
      flushPersistence,
      scopeIdFor,
      setIsolationEnabled,
    };
  }
);

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useWorkspaceStatePreferencesStore, import.meta.hot));
}
