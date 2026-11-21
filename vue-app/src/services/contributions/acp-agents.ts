/**
 * Consumer of the `acpAgents` contribution point (process plugins with the
 * shell capability, enabled and active in this workspace). It publishes the
 * re-validated agents for this platform in `pluginAcpAgents` (with
 * `pluginAcpAgentsReady`, see acp-agents-state.ts), which the settings page
 * and the AI assistant read. The validation loads only when a plugin
 * contributes agents, keeping it out of the entry chunk. Listing never starts
 * anything: an agent starts only after the user confirmed it, through
 * services/plugin-acp-agent-service.ts.
 */
import { createAcpAgentsApply } from "@/services/contributions/acp-agents-state";
import { registerContributionConsumer } from "@/services/plugin-contributions";

export { pluginAcpAgents, pluginAcpAgentsReady } from "@/services/contributions/acp-agents-state";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function acpAgentsSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the acpAgents consumer; a failed refresh keeps the agents already listed. */
export function registerAcpAgentContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "acpAgents",
    // Same lazy loading as lazyContributionApply, but the list survives the
    // dispose/apply of a refresh (see acp-agents-state.ts).
    apply: createAcpAgentsApply(async () => {
      const { acpAgentPlatform, collectAcpAgents } = await import("@/domain/plugins/acp-agents");
      lastSyncError = undefined;
      const platform = typeof navigator === "undefined" ? undefined : acpAgentPlatform(navigator.platform);
      return (contributions) => collectAcpAgents(contributions, platform);
    }, onError),
    onError,
  });
}
