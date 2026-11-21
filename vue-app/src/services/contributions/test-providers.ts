/**
 * Consumer of the `testProviders` contribution point (process plugins active
 * in this workspace). It stores the re-validated providers shown in the
 * project sidebar "Tests" tab. Registering data never starts a plugin
 * process: discovery runs when the user opens the tab or refreshes, and tests
 * run only when the user clicks Run.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function testProvidersSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the testProviders consumer; a failed refresh keeps the providers already registered. */
export function registerTestProviderContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "testProviders",
    apply: lazyContributionApply(async () => {
      const [{ collectTestProviders }, { usePluginTestProvidersStore }] = await Promise.all([
        import("@/domain/plugins/test-providers"),
        import("@/stores/plugin-test-providers"),
      ]);
      lastSyncError = undefined;
      return (contributions) => {
        const store = usePluginTestProvidersStore();
        const result = collectTestProviders(contributions);
        store.setContributions(result.providers, result.issues);
        return () => store.clearContributions();
      };
    }, onError),
    onError,
  });
}
