/**
 * Contributed terminal colour schemes (contributes.terminalColorSchemes) as
 * re-validated display data for the terminal settings. The terminal palette
 * itself is resolved from the registry in services/terminal-theme.ts.
 */
import { ref } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { TerminalColorSchemeIssue, TerminalColorSchemeRecord } from "@/domain/themes/terminal-color-schemes";

export const useTerminalColorSchemesStore = defineStore("terminal-color-schemes", () => {
  const schemes = ref<TerminalColorSchemeRecord[]>([]);
  const issues = ref<TerminalColorSchemeIssue[]>([]);
  /** True once the consumer applied a snapshot (so a missing scheme means its plugin went away). */
  const loaded = ref(false);

  function setSchemes(next: TerminalColorSchemeRecord[], nextIssues: TerminalColorSchemeIssue[]) {
    schemes.value = next;
    issues.value = nextIssues;
    loaded.value = true;
  }

  function markLoaded() {
    loaded.value = true;
  }

  function clearSchemes() {
    schemes.value = [];
    issues.value = [];
  }

  function schemeById(id: string) {
    return schemes.value.find((scheme) => scheme.id === id);
  }

  return { schemes, issues, loaded, setSchemes, markLoaded, clearSchemes, schemeById };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useTerminalColorSchemesStore, import.meta.hot));
}
