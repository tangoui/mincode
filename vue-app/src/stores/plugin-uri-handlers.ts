/**
 * Plugin link handlers (contributes.uriHandlers): the re-validated handlers
 * of process plugins active in this workspace, for display. Links are always
 * resolved by the host (plugin_host_uri_resolve) and confirmed by the user;
 * this list never authorizes anything. Nothing is persisted.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { PluginUriHandler, UriHandlerIssue } from "@/domain/plugins/uri-handlers";

export const usePluginUriHandlersStore = defineStore("plugin-uri-handlers", () => {
  const handlers = ref<PluginUriHandler[]>([]);
  const issues = ref<UriHandlerIssue[]>([]);
  /** True once the consumer applied a snapshot. */
  const loaded = ref(false);

  function setContributions(next: PluginUriHandler[], nextIssues: UriHandlerIssue[]) {
    handlers.value = next;
    issues.value = nextIssues;
    loaded.value = true;
  }

  function clearContributions() {
    handlers.value = [];
    issues.value = [];
  }

  function forPlugin(pluginId: string) {
    return handlers.value.filter((handler) => handler.pluginId === pluginId);
  }

  return { handlers, issues, loaded, setContributions, clearContributions, forPlugin };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginUriHandlersStore, import.meta.hot));
}
