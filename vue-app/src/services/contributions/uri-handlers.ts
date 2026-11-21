/**
 * Consumer of the `uriHandlers` contribution point (process plugins active in
 * this workspace). It stores the re-validated handlers for display only:
 * links are resolved by the host and confirmed by the user in
 * services/plugin-uri-links-runtime.ts. The validation and store load only
 * when a plugin contributes handlers. Registering data never starts a plugin.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function uriHandlersSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the uriHandlers consumer; a failed refresh keeps the handlers already registered. */
export function registerUriHandlerContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "uriHandlers",
    apply: lazyContributionApply(async () => {
      const [{ collectUriHandlers }, { usePluginUriHandlersStore }] = await Promise.all([
        import("@/domain/plugins/uri-handlers"),
        import("@/stores/plugin-uri-handlers"),
      ]);
      return (contributions) => {
        const store = usePluginUriHandlersStore();
        const result = collectUriHandlers(contributions);
        store.setContributions(result.handlers, result.issues);
        return () => store.clearContributions();
      };
    }, onError),
    onError,
  });
}
