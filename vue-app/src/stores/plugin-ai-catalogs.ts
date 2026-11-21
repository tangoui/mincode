/**
 * Plugin AI catalogs (contributes.aiCatalogs): the re-validated Discover
 * sources of process plugins active in this workspace. Nothing is persisted;
 * search results live only in the Discover tab and installed items become
 * ordinary user configuration.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { AiCatalogIssue, PluginAiCatalog } from "@/domain/plugins/ai-catalogs";

export const usePluginAiCatalogsStore = defineStore("plugin-ai-catalogs", () => {
  const catalogs = ref<PluginAiCatalog[]>([]);
  const issues = ref<AiCatalogIssue[]>([]);
  /** True once the consumer applied a snapshot. */
  const loaded = ref(false);

  function setContributions(next: PluginAiCatalog[], nextIssues: AiCatalogIssue[]) {
    catalogs.value = next;
    issues.value = nextIssues;
    loaded.value = true;
  }

  function clearContributions() {
    catalogs.value = [];
    issues.value = [];
  }

  return { catalogs, issues, loaded, setContributions, clearContributions };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginAiCatalogsStore, import.meta.hot));
}
