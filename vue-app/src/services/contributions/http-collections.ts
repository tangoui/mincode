/**
 * Consumer of the `httpCollections` contribution point (declarative and
 * process plugins). The Go host reads and validates each
 * `mincode.http-collection` v1 file; the runtime (re-validation and the
 * templates store in http-collections-runtime.ts) loads only when a plugin
 * contributes collections. Registering data never starts a plugin process and
 * never sends a request.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh, load or apply, if any. */
export function httpCollectionsSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the httpCollections consumer; a failed refresh keeps the templates already registered. */
export function registerHttpCollectionContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "httpCollections",
    apply: lazyContributionApply(async () => {
      const module = await import("@/services/contributions/http-collections-runtime");
      lastSyncError = undefined;
      return module.applyHttpCollectionContributions;
    }, onError),
    onError,
  });
}
