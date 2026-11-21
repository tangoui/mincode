/**
 * Plugin AI context providers (contributes.aiContextProviders): the @mention
 * registry of process plugins active in this workspace. Nothing here fetches
 * context; the AI composer does that only after the user picks a mention.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { AiContextProviderIssue, PluginAiContextProvider } from "@/domain/ai/plugin-context";

export const useAiContextProvidersStore = defineStore("ai-context-providers", () => {
  const providers = ref<PluginAiContextProvider[]>([]);
  const issues = ref<AiContextProviderIssue[]>([]);

  function setContributions(next: PluginAiContextProvider[], nextIssues: AiContextProviderIssue[] = []) {
    providers.value = next;
    issues.value = nextIssues;
  }

  function byKey(key: string | undefined) {
    return key ? providers.value.find((provider) => provider.key === key) : undefined;
  }

  return { providers, issues, setContributions, byKey };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useAiContextProvidersStore, import.meta.hot));
}
