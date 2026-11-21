import { acceptHMRUpdate, defineStore } from "pinia";
import { computed, ref } from "vue";
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolveSchedules, type PluginSchedule } from "@/domain/plugins/schedules";

/** contributes.schedules of installed plugins, for the plugin manager. The host runs them. */
export const usePluginSchedulesStore = defineStore("plugin-schedules", () => {
  const schedules = ref<PluginSchedule[]>([]);
  const byPlugin = computed(() => {
    const map = new Map<string, PluginSchedule[]>();
    for (const schedule of schedules.value) map.set(schedule.pluginId, [...(map.get(schedule.pluginId) ?? []), schedule]);
    return map;
  });

  function setContributions(contributions: readonly PointContribution[]) {
    schedules.value = resolveSchedules(contributions);
  }

  function clearContributions() {
    schedules.value = [];
  }

  return { schedules, byPlugin, setContributions, clearContributions };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginSchedulesStore, import.meta.hot));
}
