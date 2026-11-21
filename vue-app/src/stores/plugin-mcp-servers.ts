/**
 * Plugin MCP servers (contributes.mcpServers): the registry set by the
 * contribution consumer and the user's list of servers enabled for the AI
 * assistant (global, `app_plugin_mcp_servers_v1`). The store never holds a
 * launch spec; starting always goes through services/plugin-mcp-server-service.ts,
 * and enabling one first asks the user to confirm its launch.
 */
import { computed, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  normalizeEnabledPluginMcpServers,
  PLUGIN_MCP_SERVERS_STORAGE_KEY,
  type McpServerIssue,
  type PluginMcpServer,
} from "@/domain/plugins/mcp-servers";
import { loadPersistedValue, savePersistedValue } from "@/services/persistent-store";

export const usePluginMcpServersStore = defineStore("plugin-mcp-servers", () => {
  const servers = ref<PluginMcpServer[]>([]);
  const issues = ref<McpServerIssue[]>([]);
  const enabledIds = ref<string[]>([]);
  let hydration: Promise<void> | undefined;

  /** Registry servers the user enabled for the assistant, in registry order. */
  const enabledServers = computed(() => {
    const enabled = new Set(enabledIds.value);
    return servers.value.filter((server) => enabled.has(server.sessionId));
  });

  function setContributions(next: PluginMcpServer[], nextIssues: McpServerIssue[] = []) {
    servers.value = next;
    issues.value = nextIssues;
  }

  function clearContributions() {
    setContributions([]);
  }

  function server(sessionId: string) {
    return servers.value.find((candidate) => candidate.sessionId === sessionId);
  }

  function isEnabled(sessionId: string) {
    return enabledIds.value.includes(sessionId);
  }

  function hydrate(): Promise<void> {
    hydration ??= loadPersistedValue<unknown>(PLUGIN_MCP_SERVERS_STORAGE_KEY)
      .then((value) => {
        enabledIds.value = normalizeEnabledPluginMcpServers(value);
      })
      .catch(() => undefined);
    return hydration;
  }

  /** Enabled ids of uninstalled plugins are kept, so a reinstall restores them (the host re-checks trust). */
  async function setEnabled(sessionId: string, enabled: boolean) {
    await hydrate();
    const next = enabled
      ? normalizeEnabledPluginMcpServers([...enabledIds.value, sessionId])
      : enabledIds.value.filter((id) => id !== sessionId);
    enabledIds.value = next;
    await savePersistedValue(PLUGIN_MCP_SERVERS_STORAGE_KEY, next);
  }

  return { servers, issues, enabledIds, enabledServers, setContributions, clearContributions, server, isEnabled, hydrate, setEnabled };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(usePluginMcpServersStore, import.meta.hot));
}
