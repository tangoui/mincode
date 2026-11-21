/**
 * Plugin terminal link rules (contributes.terminalLinks) that passed the
 * frontend checks: re-validation, compilation, the regex safety heuristics,
 * the empty-match check and the timed worker probe (services/regex-probe.ts,
 * once per pattern). Rules that fail are skipped and kept as frontend issues.
 * Terminals read `rules` on every link request, so enabling, disabling and
 * upgrades take effect immediately (services/terminal-plugin-links.ts).
 */
import { ref, shallowRef } from "vue";
import { acceptHMRUpdate, defineStore } from "pinia";
import type { PointContribution } from "@/domain/plugins/contributions";
import {
  compileTerminalLinkRule,
  resolveTerminalLinkRules,
  terminalLinkFlags,
  type CompiledTerminalLinkRule,
} from "@/domain/terminal/terminal-links";
import { probeRegexes } from "@/services/regex-probe";

export interface TerminalLinkIssue {
  /** `<pluginId>/<ruleId>`. */
  key: string;
  message: string;
}

const MAX_ISSUES = 64;

export const useTerminalLinksStore = defineStore("terminal-links", () => {
  const rules = shallowRef<CompiledTerminalLinkRule[]>([]);
  const issues = ref<TerminalLinkIssue[]>([]);
  let generation = 0;

  function recordIssue(key: string, message: string) {
    if (issues.value.some((issue) => issue.key === key && issue.message === message)) return;
    issues.value = [...issues.value, { key, message }].slice(-MAX_ISSUES);
  }

  /** Compiles and probes the rules; only rules that pass every check become active. */
  async function setContributions(contributions: readonly PointContribution[]) {
    const current = ++generation;
    issues.value = [];
    const compiled: CompiledTerminalLinkRule[] = [];
    for (const rule of resolveTerminalLinkRules(contributions)) {
      const result = compileTerminalLinkRule(rule);
      if ("issue" in result) recordIssue(rule.key, result.issue);
      else compiled.push(result.rule);
    }
    const probed = await Promise.all(
      compiled.map((rule) =>
        probeRegexes(`terminalLinks\u0000${terminalLinkFlags(rule)}\u0000${rule.pattern}`, [
          { source: rule.pattern, flags: terminalLinkFlags(rule) },
        ]),
      ),
    );
    if (current !== generation) return;
    rules.value = compiled.filter((rule, index) => {
      const result = probed[index];
      if (result === "ok") return true;
      recordIssue(rule.key, result === "timeout" ? "regex probe timed out" : "regex probe unavailable");
      return false;
    });
  }

  function clearContributions() {
    generation += 1;
    rules.value = [];
    issues.value = [];
  }

  return { rules, issues, setContributions, clearContributions, recordIssue };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useTerminalLinksStore, import.meta.hot));
}
