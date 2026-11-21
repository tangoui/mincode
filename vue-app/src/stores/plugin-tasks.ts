/**
 * Plugin tasks (contributes.tasks) for the project workspace: whether any
 * active process plugin contributes tasks (set by the contribution consumer)
 * and the host's task list for the open project. The store never holds a
 * launch spec; running always goes through services/plugin-task-service.ts.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { PluginTaskList } from "@/domain/tasks/plugin-tasks";
import { detectPluginTasks, listPluginTasks } from "@/services/plugin-task-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

export const usePluginTasksStore = defineStore("plugin-tasks", () => {
  /** True while an active process plugin contributes at least one task entry. */
  const available = ref(false);
  /** Bumped on every contribution snapshot so an open list refreshes. */
  const contributionRevision = ref(0);
  /** Project root of the mounted project workspace ("" when none). */
  const activeProjectRoot = ref("");
  const list = ref<PluginTaskList>();
  const loading = ref(false);
  const error = ref("");
  /** `<pluginId>/<providerId>` of providers currently detecting. */
  const detecting = ref<string[]>([]);
  let loadGeneration = 0;

  function setAvailable(value: boolean) {
    available.value = value;
    contributionRevision.value += 1;
    if (!value) list.value = undefined;
  }

  async function refresh(projectRoot = activeProjectRoot.value) {
    const generation = ++loadGeneration;
    if (!projectRoot || !available.value || !isDesktopRuntime()) {
      list.value = undefined;
      error.value = "";
      loading.value = false;
      return;
    }
    loading.value = true;
    try {
      const next = await listPluginTasks(projectRoot);
      if (generation !== loadGeneration) return;
      list.value = next;
      error.value = "";
    } catch (cause) {
      if (generation !== loadGeneration) return;
      list.value = undefined;
      error.value = cause instanceof Error ? cause.message : String(cause);
    } finally {
      if (generation === loadGeneration) loading.value = false;
    }
  }

  function setActiveProjectRoot(projectRoot: string) {
    if (activeProjectRoot.value === projectRoot) return;
    activeProjectRoot.value = projectRoot;
    list.value = undefined;
    error.value = "";
  }

  function isDetecting(pluginId: string, providerId: string) {
    return detecting.value.includes(`${pluginId}/${providerId}`);
  }

  /** Runs one provider's detection; the refreshed list carries the result or its error. */
  async function detect(pluginId: string, providerId: string) {
    const projectRoot = activeProjectRoot.value;
    const key = `${pluginId}/${providerId}`;
    if (!projectRoot || detecting.value.includes(key)) return undefined;
    detecting.value = [...detecting.value, key];
    try {
      return await detectPluginTasks(pluginId, providerId, projectRoot);
    } finally {
      detecting.value = detecting.value.filter((item) => item !== key);
      if (activeProjectRoot.value === projectRoot) await refresh(projectRoot);
    }
  }

  /** Detects with every provider of the open project (palette "Detect tasks"). */
  async function detectAll() {
    if (!list.value) await refresh();
    const providers = list.value?.providers ?? [];
    await Promise.allSettled(providers.map((provider) => detect(provider.pluginId, provider.providerId)));
  }

  return {
    available,
    contributionRevision,
    activeProjectRoot,
    list,
    loading,
    error,
    detecting,
    setAvailable,
    setActiveProjectRoot,
    refresh,
    isDetecting,
    detect,
    detectAll,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginTasksStore, import.meta.hot));
}
