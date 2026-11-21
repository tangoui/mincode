import { onBeforeUnmount, ref, watch } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  HTTP_RESOURCES_VERSION,
  restoreHttpResources,
  type HttpEnvironment,
  type HttpResourcesSnapshot,
} from "@/domain/http/resources";
import type { HttpRequestCollection } from "@/domain/http/request-collection";
import {
  loadOrMigrateHttpResources,
  saveHttpResources,
} from "@/services/http-state-persistence";

export const useHttpResourcesStore = defineStore("http-resources", () => {
  const environments = ref<HttpEnvironment[]>([]);
  const collections = ref<HttpRequestCollection[]>([]);
  const hydrated = ref(false);
  let hydrationPromise: Promise<void> | undefined;
  let persistTimer: ReturnType<typeof setTimeout> | undefined;

  function snapshot(): HttpResourcesSnapshot {
    return {
      version: HTTP_RESOURCES_VERSION,
      environments: environments.value,
      collections: collections.value,
    };
  }

  async function hydrate() {
    if (hydrated.value) return;
    if (hydrationPromise) return hydrationPromise;
    hydrationPromise = (async () => {
      const restored = restoreHttpResources(await loadOrMigrateHttpResources());
      environments.value = restored.environments;
      collections.value = restored.collections;
      hydrated.value = true;
      await flushPersistence();
    })();
    try {
      await hydrationPromise;
    } finally {
      hydrationPromise = undefined;
    }
  }

  async function flushPersistence() {
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = undefined;
    if (!hydrated.value) return;
    await saveHttpResources(snapshot());
  }

  function schedulePersistence() {
    if (!hydrated.value) return;
    if (persistTimer) clearTimeout(persistTimer);
    persistTimer = setTimeout(() => void flushPersistence(), 300);
  }

  function removeResources(ids: { collectionIds?: readonly string[]; environmentIds?: readonly string[] }) {
    const collectionIds = new Set(ids.collectionIds ?? []);
    const environmentIds = new Set(ids.environmentIds ?? []);
    if (collectionIds.size) {
      collections.value = collections.value.filter((item) => !collectionIds.has(item.id));
    }
    if (environmentIds.size) {
      environments.value = environments.value.filter((item) => !environmentIds.has(item.id));
    }
  }

  function mergeResources(value: unknown) {
    const incoming = restoreHttpResources(value);
    const environmentIds = new Set(environments.value.map((item) => item.id));
    const collectionIds = new Set(collections.value.map((item) => item.id));
    const environmentAdditions = incoming.environments.filter(
      (item) => !environmentIds.has(item.id)
    );
    const collectionAdditions = incoming.collections.filter(
      (item) => !collectionIds.has(item.id)
    );
    environments.value.push(...environmentAdditions);
    collections.value.push(...collectionAdditions);
    return {
      environments: environmentAdditions.length,
      collections: collectionAdditions.length,
    };
  }

  watch([environments, collections], schedulePersistence, { deep: true });
  onBeforeUnmount(() => void flushPersistence());

  return {
    environments,
    collections,
    hydrated,
    hydrate,
    flushPersistence,
    mergeResources,
    removeResources,
  };
});

// Pinia store 实例会被缓存：模块热更新后 useStore 仍返回旧实例，
// 闭包里握着的还是旧函数，于是「代码已更新但行为没变」。
// 参见 https://pinia.vuejs.org/cookbook/hot-module-replacement.html
if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useHttpResourcesStore, import.meta.hot));
}
