/**
 * Plugin status bar state (main window only renders it). Holds the latest
 * `statusBar` contributions (declared items plus dynamic runtime overrides)
 * and the in-memory plugin command contexts used for `when` evaluation.
 * Nothing here is persisted: dynamic status text is runtime state owned by
 * the plugin process and cleared by the host when the process ends.
 */
import { acceptHMRUpdate, defineStore } from "pinia";
import { shallowRef } from "vue";
import type { PluginContextValues } from "@/domain/commands/when";
import type { PointContribution } from "@/domain/plugins/contributions";

export const useStatusBarStore = defineStore("plugin-status-bar", () => {
  const contributions = shallowRef<readonly PointContribution[]>([]);
  const contexts = shallowRef<Readonly<Record<string, PluginContextValues>>>({});

  function setContributions(next: readonly PointContribution[]) {
    contributions.value = next;
  }

  function setContexts(next: Readonly<Record<string, PluginContextValues>>) {
    contexts.value = next;
  }

  return { contributions, contexts, setContributions, setContexts };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useStatusBarStore, import.meta.hot));
}
