/**
 * Plugin AI providers (contributes.aiProviders): the re-validated presets of
 * enabled plugins, offered in Settings → AI → Connection next to the built-in
 * presets. Nothing is persisted here; a chosen preset becomes an ordinary
 * saved profile that records its source and keeps working without the plugin.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { AiProviderIssue, PluginAiProvider } from "@/domain/plugins/ai-providers";

export const usePluginAiProvidersStore = defineStore("plugin-ai-providers", () => {
  const providers = ref<PluginAiProvider[]>([]);
  const issues = ref<AiProviderIssue[]>([]);
  /** True once the consumer applied a snapshot. */
  const loaded = ref(false);

  function setContributions(next: PluginAiProvider[], nextIssues: AiProviderIssue[]) {
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
  import.meta.hot.accept(acceptHMRUpdate(usePluginAiProvidersStore, import.meta.hot));
}
