/**
 * Consumer of the `languages` contribution point (declarative and process
 * plugins; global scope). The Go host validates entries, the Monarch grammar
 * shape and the configuration allowlist and inlines both files;
 * `buildLanguageRegistry` compiles every regex and runs the static ReDoS
 * heuristics and resolves plugin-vs-plugin conflicts by pluginId. The Monaco
 * stage (services/monaco-languages.ts) enforces built-in precedence, probes
 * grammars in a worker and registers them. Registering data never starts a
 * plugin process and never loads Monaco.
 */
import {
  LANGUAGE_CONTRIBUTION_POINT,
  buildLanguageRegistry,
  type LanguagePluginIssue,
} from "@/domain/editor/language-contributions";
import { monacoLanguageIssues, setMonacoLanguages } from "@/services/monaco-languages";
import { registerContributionConsumer } from "@/services/plugin-contributions";

export type { LanguagePluginIssue } from "@/domain/editor/language-contributions";

let lastIssues: LanguagePluginIssue[] = [];
let lastSyncError: string | undefined;

/** Issues of the last applied snapshot plus Monaco-stage issues, for diagnostics. */
export function languagePluginIssues(): readonly LanguagePluginIssue[] {
  return [...lastIssues, ...monacoLanguageIssues()];
}

/** The error of the last failed refresh (plugin host unavailable), if any. */
export function languagePluginSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the languages consumer; a failed refresh keeps the languages already registered. */
export function registerLanguageContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: LANGUAGE_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const result = buildLanguageRegistry(contributions);
      lastIssues = result.issues;
      lastSyncError = undefined;
      setMonacoLanguages(result.languages);
      return () => setMonacoLanguages([]);
    },
    onError: (message) => {
      lastSyncError = message;
    },
  });
}
