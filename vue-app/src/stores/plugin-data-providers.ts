/**
 * Plugin data providers (contributes.sessionSources, scmCommitMessage,
 * fileDecorations): the re-validated entries of enabled process
 * plugins, written by services/contributions/data-providers-runtime.ts. Only
 * declarations live here; session lists and transcripts are loaded on demand
 * by the sessions page, decorations by the project tree, never persisted.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { PluginDecorationProvider } from "@/domain/plugins/file-decorations";
import type { PluginScmProvider } from "@/domain/plugins/scm-commit-message";
import type { PluginSessionSource } from "@/domain/plugins/session-sources";

export const usePluginDataProvidersStore = defineStore("plugin-data-providers", () => {
  /** Includes sources inactive in this workspace (activeInWorkspace false); the sessions page filters them out. */
  const sessionSources = ref<PluginSessionSource[]>([]);

  /**
   * Replaces the sources, keeping the previous object of every source whose
   * entry did not change, so views keyed on a source keep their state.
   */
  function setSessionSources(next: PluginSessionSource[]) {
    const previous = new Map(sessionSources.value.map((source) => [source.key, source]));
    sessionSources.value = next.map((source) => {
      const old = previous.get(source.key);
      return old && JSON.stringify(old) === JSON.stringify(source) ? old : source;
    });
  }

  /** Commit message providers, inactive ones included (activeInWorkspace false); the Git panel filters them out. */
  const scmProviders = ref<PluginScmProvider[]>([]);

  function setScmProviders(next: PluginScmProvider[]) {
    scmProviders.value = next;
  }

  /** File decoration providers in render order, inactive ones included (activeInWorkspace false); the project tree filters them out. */
  const decorationProviders = ref<PluginDecorationProvider[]>([]);

  function setDecorationProviders(next: PluginDecorationProvider[]) {
    // Keep the array when nothing changed so the project tree does not drop its decorations.
    if (JSON.stringify(next) !== JSON.stringify(decorationProviders.value)) decorationProviders.value = next;
  }

  return { sessionSources, setSessionSources, scmProviders, setScmProviders, decorationProviders, setDecorationProviders };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginDataProvidersStore, import.meta.hot));
}
