/**
 * Runtime of the `walkthroughs` contribution point consumer, loaded on
 * demand by services/contributions/walkthroughs.ts only when a plugin
 * contributes walkthroughs. It stores the re-validated walkthroughs for the
 * plugin details drawer and the dashboard "Get started" card; on cleanup the
 * cached assets of the contributing plugins are released.
 */
import type { PointContribution } from "@/domain/plugins/contributions";
import { releasePluginAssets } from "@/services/contribution-assets";
import { usePluginWalkthroughsStore } from "@/stores/plugin-walkthroughs";

/** Applies one snapshot of the point. Returns the cleanup of this snapshot. */
export function applyWalkthroughContributions(contributions: PointContribution[]): () => void {
  const store = usePluginWalkthroughsStore();
  store.setContributions(contributions);
  const pluginIds = new Set(store.walkthroughs.map((item) => item.pluginId));
  return () => {
    store.clearContributions();
    for (const pluginId of pluginIds) releasePluginAssets(pluginId);
  };
}
