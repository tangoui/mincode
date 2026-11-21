/**
 * Command snippets (contributes.commandSnippets) from plugins active in the
 * workspace, as re-validated data from the contribution consumer. The terminal
 * drawer filters them by terminal kind and platform; inserting always goes
 * through domain/terminal/command-snippets.ts `commandSnippetInsertionPlan`.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { PointContribution } from "@/domain/plugins/contributions";
import { resolveCommandSnippets, type CommandSnippet } from "@/domain/terminal/command-snippets";

export const useTerminalCommandSnippetsStore = defineStore("terminal-command-snippets", () => {
  const snippets = ref<CommandSnippet[]>([]);

  function setContributions(contributions: readonly PointContribution[]) {
    snippets.value = resolveCommandSnippets(contributions);
  }

  function clearContributions() {
    snippets.value = [];
  }

  return { snippets, setContributions, clearContributions };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useTerminalCommandSnippetsStore, import.meta.hot));
}
