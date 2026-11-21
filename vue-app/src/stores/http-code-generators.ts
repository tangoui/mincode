/**
 * HTTP code generators contributed by plugins (`httpCodeGenerators`
 * contribution point). Runtime data only, set by
 * services/contributions/http-code-generators.ts; nothing is persisted here.
 * Templates are fetched on demand when the user picks a generator.
 */
import { acceptHMRUpdate, defineStore } from "pinia";
import { shallowRef } from "vue";
import type { PluginHttpCodeGenerator, PluginHttpCodeGeneratorIssue } from "@/domain/plugins/http-code-generators";

export const useHttpCodeGeneratorsStore = defineStore("http-code-generators", () => {
  const generators = shallowRef<readonly PluginHttpCodeGenerator[]>([]);
  const issues = shallowRef<readonly PluginHttpCodeGeneratorIssue[]>([]);

  function setContributed(next: readonly PluginHttpCodeGenerator[], nextIssues: readonly PluginHttpCodeGeneratorIssue[] = []) {
    generators.value = next;
    issues.value = nextIssues;
  }

  return { generators, issues, setContributed };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useHttpCodeGeneratorsStore, import.meta.hot));
}
