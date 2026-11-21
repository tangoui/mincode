/**
 * Consumer of the `treeViews` contribution point (process plugins active in
 * this workspace). It stores the re-validated views shown in the project
 * sidebar "Views" tab. The validation and store load only when a plugin
 * contributes views, keeping them out of the entry chunk. Registering data
 * never starts a plugin process: a view loads its items only when the user
 * opens the tab or expands an item.
 */
import { lazyContributionApply } from "@/services/contributions/lazy-consumer";
import { registerContributionConsumer } from "@/services/plugin-contributions";

let lastSyncError: string | undefined;

/** The error of the last failed refresh or apply, if any. */
export function treeViewsSyncError(): string | undefined {
  return lastSyncError;
}

/** Registers the treeViews consumer; a failed refresh keeps the views already registered. */
export function registerTreeViewContributionConsumer(): () => void {
  const onError = (message: string) => {
    lastSyncError = message;
  };
  return registerContributionConsumer({
    point: "treeViews",
    apply: lazyContributionApply(async () => {
      const [{ collectTreeViews }, { usePluginTreeViewsStore }] = await Promise.all([
        import("@/domain/plugins/tree-views"),
        import("@/stores/plugin-tree-views"),
      ]);
      lastSyncError = undefined;
      return (contributions) => {
        const store = usePluginTreeViewsStore();
        const result = collectTreeViews(contributions);
        store.setContributions(result.views, result.issues);
        return () => store.clearContributions();
      };
    }, onError),
    onError,
  });
}
