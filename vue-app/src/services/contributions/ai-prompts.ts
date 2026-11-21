/**
 * Consumer of the `aiPrompts` contribution point (declarative and process
 * plugins; entries follow the plugin's workspace scope). The Go host
 * validates entries and reads the Markdown files; the runtime (re-validation
 * and the ai-plugin-prompts store in ai-prompts-runtime.ts) loads only when a
 * plugin contributes prompts. Registering data never starts a plugin process
 * and never sends anything to a model.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function aiPromptSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the aiPrompts consumer; a failed refresh keeps the entries already registered. */
export function registerAiPromptContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  const apply = lazyContributionApply(async () => {
    const module = await import("@/services/contributions/ai-prompts-runtime");
    lastSyncError = undefined;
    return module.applyAiPromptContributions;
  }, onError);
  return registerContributionConsumer({
    point: "aiPrompts",
    apply: (contributions) => {
      // An empty snapshot is a successful refresh too.
      if (contributions.length === 0) lastSyncError = undefined;
      return apply(contributions);
    },
    onError,
  });
}
