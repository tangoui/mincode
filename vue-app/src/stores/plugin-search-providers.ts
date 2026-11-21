/**
 * Plugin search providers (contributes.searchProviders): the re-validated
 * prefix registry of process plugins active in this workspace. Nothing is
 * persisted; queries and results never leave the palette session.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { PluginSearchProvider, SearchProviderIssue } from "@/domain/plugins/search-providers";

export const usePluginSearchProvidersStore = defineStore("plugin-search-providers", () => {
  const providers = ref<PluginSearchProvider[]>([]);
  const issues = ref<SearchProviderIssue[]>([]);
  /** True once the consumer applied a snapshot. */
  const loaded = ref(false);

  function setContributions(next: PluginSearchProvider[], nextIssues: SearchProviderIssue[]) {
    providers.value = next;
    issues.value = nextIssues;
    loaded.value = true;
  }

  function clearContributions() {
    providers.value = [];
    issues.value = [];
  }

  return { providers, issues, loaded, setContributions, clearContributions };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginSearchProvidersStore, import.meta.hot));
}
