/**
 * Plugin walkthroughs (`walkthroughs` contribution point) and the user's
 * progress. Contributed walkthroughs are runtime data set by
 * services/contributions/walkthroughs.ts; only progress is persisted (global
 * key `app_plugin_walkthroughs_v1`, shape
 * `{ version: 1, completed: {"<pluginId>/<id>": stepIds[]}, dismissed: keys[] }`).
 * Nothing here opens a walkthrough: the user always opens one explicitly.
 */
import { acceptHMRUpdate, defineStore } from "pinia";
import { computed, ref, shallowRef } from "vue";
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  WALKTHROUGHS_STORAGE_KEY,
  emptyWalkthroughProgress,
  featuredWalkthroughs,
  markWalkthroughStepDone,
  normalizeWalkthroughProgress,
  resolveWalkthroughs,
  setWalkthroughDismissed,
  type Walkthrough,
  type WalkthroughProgress,
} from "@/domain/plugins/walkthroughs";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

export const usePluginWalkthroughsStore = defineStore("plugin-walkthroughs", () => {
  const walkthroughs = shallowRef<readonly Walkthrough[]>([]);
  const progress = ref<WalkthroughProgress>(emptyWalkthroughProgress());
  const hydrated = ref(false);
  let hydratePromise: Promise<void> | undefined;

  const byKey = computed(() => new Map(walkthroughs.value.map((item) => [item.key, item])));
  const featured = computed(() => featuredWalkthroughs(walkthroughs.value, progress.value));

  function setContributions(contributions: readonly PointContribution[]) {
    walkthroughs.value = resolveWalkthroughs(contributions);
  }

  function clearContributions() {
    walkthroughs.value = [];
  }

  function forPlugin(pluginId: string) {
    return walkthroughs.value.filter((item) => item.pluginId === pluginId);
  }

  function walkthroughByKey(key: string) {
    return byKey.value.get(key);
  }

  async function hydrate() {
    if (hydrated.value) return;
    hydratePromise ??= (async () => {
      progress.value = normalizeWalkthroughProgress(await loadPersistedValue<unknown>(WALKTHROUGHS_STORAGE_KEY));
      hydrated.value = true;
    })().finally(() => {
      hydratePromise = undefined;
    });
    return hydratePromise;
  }

  async function persist(next: WalkthroughProgress) {
    progress.value = next;
    await savePersistedValue(WALKTHROUGHS_STORAGE_KEY, next);
  }

  async function markStepDone(key: string, stepId: string) {
    await hydrate();
    const next = markWalkthroughStepDone(progress.value, key, stepId);
    if (next !== progress.value) await persist(next);
  }

  async function setDismissed(key: string, dismissed: boolean) {
    await hydrate();
    const next = setWalkthroughDismissed(progress.value, key, dismissed);
    if (next !== progress.value) await persist(next);
  }

  return {
    walkthroughs,
    progress,
    hydrated,
    featured,
    setContributions,
    clearContributions,
    forPlugin,
    walkthroughByKey,
    hydrate,
    markStepDone,
    setDismissed,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginWalkthroughsStore, import.meta.hot));
}
