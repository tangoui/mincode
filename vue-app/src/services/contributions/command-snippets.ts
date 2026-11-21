/**
 * Consumer of the `commandSnippets` contribution point. It only stores the
 * re-validated libraries for the terminal snippet drawer; nothing is inserted
 * or executed until the user picks a snippet (components/terminal/
 * TerminalCommandSnippetsPanel.vue).
 */
import { COMMAND_SNIPPETS_CONTRIBUTION_POINT } from "@/domain/terminal/command-snippets";
import { registerContributionConsumer } from "@/services/plugin-contributions";
import { useTerminalCommandSnippetsStore } from "@/stores/terminal-command-snippets";

export function registerCommandSnippetContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: COMMAND_SNIPPETS_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const store = useTerminalCommandSnippetsStore();
      store.setContributions(contributions);
      return () => store.clearContributions();
    },
  });
}
