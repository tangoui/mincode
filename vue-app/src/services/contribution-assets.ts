/**
 * Frontend access to plugin contribution assets served by the host command
 * `plugin_host_contribution_assets` (see domain/plugins/contribution-assets.ts
 * and docs/architecture/plugin-system.md). Consumers pass the content-hash
 * keys found in the contributions snapshot; the loader coalesces requests
 * within 16 ms, batches at most 64 keys per call, follows `deferred` keys and
 * keeps bytes plus blob URLs in a 16 MiB LRU. Browser preview has no plugin
 * host, so every key is reported missing there.
 */
import {
  createContributionAssetLoader,
  type ContributionAsset,
  type ContributionAssetsRequest,
  type ContributionAssetsResponse,
  type ContributionAssetsResult,
} from "@/domain/plugins/contribution-assets";
import { invokeBackend, isDesktopRuntime } from "@/services/desktop-bridge";

export type { ContributionAsset, ContributionAssetsResult };

const loader = createContributionAssetLoader({
  invoke: (request: ContributionAssetsRequest) =>
    invokeBackend<ContributionAssetsResponse>("plugin_host_contribution_assets", { ...request }),
});

/** Fetches assets of one plugin's contribution point by content-hash key. */
export function fetchContributionAssets(
  pluginId: string,
  point: string,
  keys: readonly string[],
  options: { fresh?: boolean } = {},
): Promise<ContributionAssetsResult> {
  if (!isDesktopRuntime()) {
    return Promise.resolve({ assets: new Map(), missing: [...new Set(keys)], stale: [] });
  }
  if (options.fresh) loader.releasePlugin(pluginId);
  return loader.fetchAssets(pluginId, point, keys);
}

/** Returns one asset's canonical bytes and mime, or null when unavailable. */
export function contributionAssetBytes(pluginId: string, point: string, key: string): Promise<ContributionAsset | null> {
  if (!isDesktopRuntime()) return Promise.resolve(null);
  return loader.assetBytes(pluginId, point, key);
}

/**
 * Returns a cached blob URL for one asset, or null. The URL stays valid until
 * the entry is evicted or `releasePluginAssets` runs; do not revoke it.
 */
export function contributionAssetObjectURL(pluginId: string, point: string, key: string): Promise<string | null> {
  if (!isDesktopRuntime()) return Promise.resolve(null);
  return loader.objectURL(pluginId, point, key);
}

/** Drops cached bytes and revokes blob URLs of one plugin (disable, uninstall, reload). */
export function releasePluginAssets(pluginId: string): void {
  loader.releasePlugin(pluginId);
}
