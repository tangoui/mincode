/**
 * Project problems: language-server diagnostics per project root, including
 * files that are not open (push diagnostics and workspace pulls). The project
 * LSP runtime writes it; the problems panel reads summaries. The registry
 * itself is not reactive; `revision` is bumped once per burst of updates.
 */
import { markRaw, ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import {
  clearServerProblems,
  createProblemRegistry,
  problemsFor,
  setProblems,
  summarizeProblems,
  type ProblemSummary,
} from "@/domain/projects/lsp-problems";
import type { LspDiagnostic } from "@/domain/projects/lsp-protocol";

const NOTIFY_DELAY_MS = 120;

export const useProjectProblemsStore = defineStore("project-problems", () => {
  const registry = markRaw(createProblemRegistry());
  const revision = ref(0);
  let notifyTimer: ReturnType<typeof setTimeout> | undefined;

  function changed() {
    if (notifyTimer) return;
    notifyTimer = setTimeout(() => {
      notifyTimer = undefined;
      revision.value += 1;
    }, NOTIFY_DELAY_MS);
  }

  function set(root: string, serverId: string, uri: string, diagnostics: readonly LspDiagnostic[]) {
    if (setProblems(registry, root, serverId, uri, diagnostics)) changed();
  }

  function clearServer(root: string, serverId: string) {
    if (clearServerProblems(registry, root, serverId)) changed();
  }

  function get(root: string, serverId: string, uri: string) {
    return problemsFor(registry, root, serverId, uri);
  }

  function summary(root: string): ProblemSummary {
    // Read revision so computed callers re-run after updates.
    void revision.value;
    return summarizeProblems(registry, root);
  }

  return { revision, set, clearServer, get, summary };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useProjectProblemsStore, import.meta.hot));
}
