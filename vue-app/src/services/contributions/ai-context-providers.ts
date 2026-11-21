/**
 * Consumer of the `aiContextProviders` contribution point (process plugins
 * active in this workspace). The runtime (ai-context-providers-runtime.ts)
 * loads only when a plugin contributes providers and stores the @mention
 * registry the AI composer offers; registering data never starts a plugin
 * process and never sends anything to a model. Providers are not AI tools:
 * only the user's explicit pick in the composer fetches context.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function aiContextProvidersSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the aiContextProviders consumer; a failed refresh keeps the providers already registered. */
export function registerAiContextProviderContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "aiContextProviders",
    apply: lazyContributionApply(async () => {
      const module = await import("@/services/contributions/ai-context-providers-runtime");
      lastSyncError = undefined;
      return module.applyAiContextProviderContributions;
    }, onError),
    onError,
  });
}
