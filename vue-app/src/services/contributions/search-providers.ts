/**
 * Consumer of the `searchProviders` contribution point (process plugins
 * active in this workspace). It stores the re-validated prefix registry used
 * by the command palette (`<prefix>:<query>`). The validation and store load
 * only when a plugin contributes providers, keeping them out of the entry
 * chunk. Registering data never starts a plugin process: a provider runs
 * only when the user types its prefix.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function searchProvidersSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the searchProviders consumer; a failed refresh keeps the providers already registered. */
export function registerSearchProviderContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "searchProviders",
    apply: lazyContributionApply(async () => {
      const [{ collectSearchProviders }, { usePluginSearchProvidersStore }] = await Promise.all([
        import("@/domain/plugins/search-providers"),
        import("@/stores/plugin-search-providers"),
      ]);
      return (contributions) => {
        const store = usePluginSearchProvidersStore();
        const result = collectSearchProviders(contributions);
        store.setContributions(result.providers, result.issues);
        return () => store.clearContributions();
      };
    }, onError),
    onError,
  });
}
