/**
 * Consumer of the `snippets` contribution point (declarative and process
 * plugins; a process plugin's snippets follow its workspace scope). The Go
 * host validates snippet files structurally; `buildSnippetRegistry` checks
 * body syntax (allowlisted variables, no regex transforms, no CLIPBOARD) and
 * the Monaco stage reports unknown languages. A snippet or file that fails is
 * skipped and reported, never thrown. Registering data never starts a plugin
 * process and never loads Monaco: providers attach once an editor loads it.
 */
import {
  SNIPPET_CONTRIBUTION_POINT,
  buildSnippetRegistry,
  type SnippetPluginIssue,
} from "@/domain/editor/snippets";
import { monacoSnippetIssues, setMonacoSnippets } from "@/services/monaco-snippets";
import { registerContributionConsumer } from "@/services/plugin-contributions";

export type { SnippetPluginIssue } from "@/domain/editor/snippets";

let lastIssues: SnippetPluginIssue[] = [];
let lastSyncError: string | undefined;

/**
 * Issues of the last applied snapshot plus Monaco-stage issues, for
 * diagnostics. Host-stage issues are also written to the plugin's log.
 */
export function snippetPluginIssues(): readonly SnippetPluginIssue[] {
  return [...lastIssues, ...monacoSnippetIssues()];
}

/** The error of the last failed refresh (plugin host unavailable), if any. */
export function snippetPluginSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the snippets consumer; a failed refresh keeps the snippets already registered. */
export function registerSnippetContributionConsumer(): () => void {
  return registerContributionConsumer({
    point: SNIPPET_CONTRIBUTION_POINT,
    apply: (contributions) => {
      const result = buildSnippetRegistry(contributions);
      lastIssues = result.issues;
      lastSyncError = undefined;
      setMonacoSnippets(result.byLanguage);
      return () => setMonacoSnippets(new Map());
    },
    onError: (message) => {
      lastSyncError = message;
    },
  });
}
