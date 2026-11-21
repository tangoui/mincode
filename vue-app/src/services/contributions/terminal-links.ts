/**
 * Consumer of the `terminalLinks` contribution point. It compiles and probes
 * the rules into the terminal-links store; every terminal pane reads the store
 * through its link provider (services/terminal-plugin-links.ts). Nothing opens
 * without a user click.
 */
import { TERMINAL_LINKS_CONTRIBUTION_POINT } from "@/domain/terminal/terminal-links";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { useTerminalLinksStore } from "@/stores/terminal-links";

export function registerTerminalLinkContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: TERMINAL_LINKS_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const store = useTerminalLinksStore();
      void store.setContributions(contributions);
      return () => store.clearContributions();
    },
  });
}
