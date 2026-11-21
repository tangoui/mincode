/**
 * Consumer of the `languageFeatures` contribution point (process plugins
 * active in this workspace). The validation and the Monaco stage
 * (services/monaco-language-features.ts) load only when a plugin contributes
 * providers. Registering data never starts a plugin process and never loads
 * Monaco: a provider is called only when the editor asks for one of its
 * kinds (hover, completion, definition, references, CodeLens, inlay hints,
 * document symbols, quick fixes) in one of its languages. Everything beyond
 * hover and completion (result parsing, location URIs, the passive debounce
 * and failure gate, the CodeLens command, the quick fix command) lives in
 * that lazily loaded stage, in domain/plugins/language-feature-results.ts
 * and in language-features-runtime.ts, never in the entry chunk.
 */
import type { LanguageFeatureIssue } from "@/domain/plugins/language-features";
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;
let lastIssues: readonly LanguageFeatureIssue[] = [];

/** The error of the last failed refresh or apply, if any. */
export function languageFeaturesSyncError(): string | undefined {
  return lastSyncError;
}

/** The entries the frontend re-validation rejected in the last snapshot (diagnostics). */
export function languageFeatureIssues(): readonly LanguageFeatureIssue[] {
  return lastIssues;
}

/** Registers the languageFeatures consumer; a failed refresh keeps the providers already registered. */
export function registerLanguageFeatureContributionConsumer(): () => void {
  let stage: Promise<typeof import("@/services/monaco-language-features")> | undefined;
  let wired = false;
  const onError = (message: string) => {
    lastSyncError = message;
  };
  const apply = lazyContributionApply(async () => {
    const [{ collectLanguageFeatures }, monaco, { useVueI18n }, { useLocalProjectStore }, { languageFeatureCommandRuntime }] = await Promise.all([
      import("@/domain/plugins/language-features"),
      (stage ??= import("@/services/monaco-language-features")),
      import("@/i18n/messages"),
      import("@/stores/local-project"),
      import("@/services/contributions/language-features-runtime"),
    ]);
    lastSyncError = undefined;
    if (!wired) {
      wired = true;
      monaco.setMonacoLanguageFeatureRuntime({
        locale: () => useVueI18n().localeChain.value[0] ?? "en",
        projectRoots: () => {
          try {
            return useLocalProjectStore().projects.map((project) => project.root);
          } catch {
            return [];
          }
        },
        ...languageFeatureCommandRuntime,
      });
    }
    return (contributions) => {
      const result = collectLanguageFeatures(contributions);
      lastIssues = result.issues;
      monaco.setMonacoLanguageFeatures(result.providers);
      return () => {
        lastIssues = [];
        monaco.setMonacoLanguageFeatures([]);
      };
    };
  }, onError);
  const unregister = registerContributionConsumer({
    point: "languageFeatures",
    apply,
    onError,
  });
  return () => {
    unregister();
    wired = false;
    void stage?.then((module) => module.stopMonacoLanguageFeatures(), () => undefined);
  };
}
