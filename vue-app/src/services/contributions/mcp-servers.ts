/**
 * Consumer of the `mcpServers` contribution point (process plugins with the
 * shell capability, active in this workspace). It stores the re-validated
 * registry the MCP settings panel and the AI assistant read. Registering data
 * never starts anything: a plugin MCP server starts only after the user
 * enabled and confirmed it, through plugin_host_mcp_server_start
 * (services/plugin-mcp-server-service.ts).
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function mcpServersSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the mcpServers consumer; a failed refresh keeps the servers already registered. */
export function registerMcpServerContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "mcpServers",
    apply: lazyContributionApply(async () => {
      const [{ collectMcpServers, mcpServerPlatform }, { usePluginMcpServersStore }] = await Promise.all([
        import("@/domain/plugins/mcp-servers"),
        import("@/stores/plugin-mcp-servers"),
      ]);
      lastSyncError = undefined;
      return (contributions) => {
        const store = usePluginMcpServersStore();
        const platform = typeof navigator === "undefined" ? undefined : mcpServerPlatform(navigator.platform);
        const result = collectMcpServers(contributions, platform);
        store.setContributions(result.servers, result.issues);
        void store.hydrate();
        return () => store.clearContributions();
      };
    }, onError),
    onError,
  });
}
