/**
 * Consumer of the `walkthroughs` contribution point (declarative and process
 * plugins). The runtime (re-validation and the walkthroughs store in
 * walkthroughs-runtime.ts) loads only when a plugin contributes
 * walkthroughs. Registering data never opens anything: a walkthrough opens
 * only on an explicit user action. Step markdown and images are fetched
 * lazily as contribution assets; on dispose the cached assets of the
 * contributing plugins are released.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh, load or apply, if any. */
export function walkthroughsSyncError(): string | undefined {
  return lastSyncError;
}

export function registerWalkthroughContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "walkthroughs",
    apply: lazyContributionApply(async () => {
      const module = await import("@/services/contributions/walkthroughs-runtime");
      lastSyncError = undefined;
      return module.applyWalkthroughContributions;
    }, onError),
    onError,
  });
}
