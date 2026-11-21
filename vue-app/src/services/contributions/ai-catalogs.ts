/**
 * Consumer of the `aiCatalogs` contribution point (process plugins active in
 * this workspace). It stores the re-validated catalogs that Settings → AI →
 * Discover queries next to the built-in sources. The validation and store
 * load only when a plugin contributes catalogs, keeping them out of the entry
 * chunk. Registering data never starts a plugin process: a catalog runs only
 * when the user searches or adds an item in the Discover tab.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function aiCatalogsSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the aiCatalogs consumer; a failed refresh keeps the catalogs already registered. */
export function registerAiCatalogContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "aiCatalogs",
    apply: lazyContributionApply(async () => {
      const [{ collectAiCatalogs }, { usePluginAiCatalogsStore }] = await Promise.all([
        import("@/domain/plugins/ai-catalogs"),
        import("@/stores/plugin-ai-catalogs"),
      ]);
      return (contributions) => {
        const store = usePluginAiCatalogsStore();
        const result = collectAiCatalogs(contributions);
        store.setContributions(result.catalogs, result.issues);
        return () => store.clearContributions();
      };
    }, onError),
    onError,
  });
}
