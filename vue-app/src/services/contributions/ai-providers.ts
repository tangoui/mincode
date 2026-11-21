/**
 * Consumer of the `aiProviders` contribution point (declarative and process
 * plugins, always global). It stores the re-validated presets that Settings →
 * AI → Connection lists next to the built-in providers. The validation and
 * store load only when a plugin contributes presets, keeping them out of the
 * entry chunk. Registering data never starts a plugin process or sends a
 * request.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function aiProvidersSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the aiProviders consumer; a failed refresh keeps the presets already registered. */
export function registerAiProviderContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "aiProviders",
    apply: lazyContributionApply(async () => {
      const [{ collectAiProviders }, { usePluginAiProvidersStore }] = await Promise.all([
        import("@/domain/plugins/ai-providers"),
        import("@/stores/plugin-ai-providers"),
      ]);
      return (contributions) => {
        const store = usePluginAiProvidersStore();
        const result = collectAiProviders(contributions);
        store.setContributions(result.providers, result.issues);
        return () => store.clearContributions();
      };
    }, onError),
    onError,
  });
}
