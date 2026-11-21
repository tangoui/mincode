import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import { mergePluginCatalog, upgradablePluginIds } from "@/domain/plugins/catalog";
import { listMarketPlugins } from "@/services/market-service";
import { listPlugins } from "@/services/plugin-service";
import { isDesktopRuntime } from "@/services/desktop-bridge";

/** First background check after launch: off the startup path, like MinCode's own update check. */
export const PLUGIN_UPDATE_CHECK_DELAY_MS = 30_000;
export const PLUGIN_UPDATE_CHECK_INTERVAL_MS = 6 * 60 * 60 * 1000;

/**
 * Installed market plugins that have a newer compatible release, for the dot on
 * the plugin manager's activity bar entry. Checks run in the background; the
 * plugin manager publishes its own catalog after each refresh or upgrade.
 */
export const usePluginUpdatesStore = defineStore("plugin-updates", () => {
  const upgradableIds = ref<string[]>([]);
  const count = computed(() => upgradableIds.value.length);
  let startTimer: ReturnType<typeof setTimeout> | undefined;
  let intervalTimer: ReturnType<typeof setInterval> | undefined;
  let inflight: Promise<void> | undefined;

  function publish(ids: string[]) {
    upgradableIds.value = [...new Set(ids)];
  }

  /** Background check; failures (offline, plugin host not ready) keep the last known result. */
  async function refresh(): Promise<void> {
    if (inflight) return inflight;
    inflight = (async () => {
      try {
        const [installed, market] = await Promise.all([listPlugins(), listMarketPlugins(() => true)]);
        publish(upgradablePluginIds(mergePluginCatalog(installed, [], market.items)));
      } catch {
        // Silent: the dot only reflects what is known.
      }
    })();
    try {
      await inflight;
    } finally {
      inflight = undefined;
    }
  }

  function startAutoCheck() {
    if (startTimer || intervalTimer || !isDesktopRuntime()) return;
    startTimer = setTimeout(() => {
      startTimer = undefined;
      void refresh();
      intervalTimer = setInterval(() => void refresh(), PLUGIN_UPDATE_CHECK_INTERVAL_MS);
    }, PLUGIN_UPDATE_CHECK_DELAY_MS);
  }

  function stopAutoCheck() {
    if (startTimer) clearTimeout(startTimer);
    if (intervalTimer) clearInterval(intervalTimer);
    startTimer = undefined;
    intervalTimer = undefined;
  }

  return { upgradableIds, count, publish, refresh, startAutoCheck, stopAutoCheck };
});

if (import.meta.hot) import.meta.hot.accept(acceptHMRUpdate(usePluginUpdatesStore, import.meta.hot));
