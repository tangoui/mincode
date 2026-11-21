/**
 * HTTP collection templates contributed by plugins (`httpCollections`
 * contribution point). Runtime data only, set by
 * services/contributions/http-collections.ts; nothing is persisted here.
 * Imported collections live in the user's own http-resources store and keep
 * no link to the plugin.
 */
import { acceptHMRUpdate, defineStore } from "pinia";
import { shallowRef } from "vue";
import type { PluginHttpCollection, PluginHttpCollectionIssue } from "@/domain/http/plugin-collections";

export const useHttpPluginTemplatesStore = defineStore("http-plugin-templates", () => {
  const templates = shallowRef<readonly PluginHttpCollection[]>([]);
  const issues = shallowRef<readonly PluginHttpCollectionIssue[]>([]);

  function setContributed(next: readonly PluginHttpCollection[], nextIssues: readonly PluginHttpCollectionIssue[] = []) {
    templates.value = next;
    issues.value = nextIssues;
  }

  return { templates, issues, setContributed };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useHttpPluginTemplatesStore, import.meta.hot));
}
