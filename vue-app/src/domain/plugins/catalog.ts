import type { OfficialPlugin, PluginMeta } from "../../services/plugin-service";
import type { MarketPlugin } from "../../services/market-service";
import { marketCompatibility } from "./market-compatibility";
import { isNewerPluginVersion } from "./market-version";

/** Categories a plugin.json may declare; the host validates the same list (plugins/api/categories.go). */
export const manifestCategories = ["database", "spreadsheet", "web", "api", "network", "development", "productivity", "other"] as const;
/**
 * Catalog display categories in order: the manifest categories plus marketplace-only ones that the cloud
 * assigns to listings (MarketplaceWorkflow::CATEGORIES, backend/market validCategory).
 */
export const pluginCategories = [
  "database", "spreadsheet", "web", "api", "network", "messaging", "development", "devops", "editor",
  "ai", "document", "productivity", "language", "theme", "entertainment", "other",
] as const;
export type ManifestCategory = typeof manifestCategories[number];
export type PluginCategory = typeof pluginCategories[number];
export type CatalogSource = "all" | "official" | "local" | "cloud";
export type CatalogStatus = "all" | "installed" | "upgradable" | "available";
export interface CatalogFilter {
  category: PluginCategory | "all";
  source: CatalogSource;
  status: CatalogStatus;
  search: string;
}
export interface CatalogEntry {
  id: string;
  plugin?: PluginMeta;
  official?: OfficialPlugin;
  cloud?: MarketPlugin;
  metadata: PluginMeta | OfficialPlugin | MarketPlugin;
  category: PluginCategory;
  installed: boolean;
  source: Exclude<CatalogSource, "all">;
}
export function isPluginCategory(value: unknown): value is PluginCategory {
  return pluginCategories.some(category => category === value);
}
export function isManifestCategory(value: unknown): value is ManifestCategory {
  return manifestCategories.some(category => category === value);
}
export function mergePluginCatalog(plugins: PluginMeta[], official: OfficialPlugin[], cloud: MarketPlugin[] = []): CatalogEntry[] {
  const installed = new Map(plugins.map(plugin => [plugin.id, plugin]));
  const bundled = new Map(official.map(plugin => [plugin.id, plugin]));
  const products = new Map(cloud.map(product => [product.product_key, product]));
  return [...new Set([...bundled.keys(), ...installed.keys(), ...products.keys()])].map(id => {
    const plugin = installed.get(id);
    const official = bundled.get(id);
    const product = products.get(id);
    const metadata = (plugin ?? official ?? product)!;
    // The marketplace category is curated per listing and may be finer than the manifest's.
    const category = isPluginCategory(product?.category) ? product.category : metadata.category;
    return { id, plugin, official, cloud: product, metadata,
      category: isPluginCategory(category) ? category : "other",
      installed: !!plugin || official?.installed === true,
      // Product metadata never establishes official provenance or installability.
      source: official ? "official" : plugin ? "local" : "cloud" };
  });
}
/**
 * The catalog search box also takes an install source: an HTTPS URL, an absolute, home or relative path
 * (POSIX, Windows drive or UNC), or a bare .min/.zip package name. Returns the trimmed source, or "" for
 * ordinary search text. The backend still validates the source when previewing it.
 */
export function installSourceFromSearch(text: string): string {
  const value = text.trim();
  if (/^https:\/\/[^\s/]+/i.test(value)) return value;
  if (/^(?:\/|~[\\/]|\.{1,2}[\\/]|[A-Za-z]:[\\/]|\\\\)/.test(value)) return value;
  return /^[^\s\\/]+\.(?:min|zip)$/i.test(value) ? value : "";
}
/**
 * The market version an installed plugin can upgrade to on this MinCode, or null. Only market-installed
 * (local) plugins upgrade from the market; bundled and development copies are managed elsewhere.
 */
export function catalogUpgradeVersion(entry: CatalogEntry): string | null {
  const plugin = entry.plugin;
  if (!plugin || plugin.source !== "local" || !entry.cloud) return null;
  const offered = marketCompatibility(entry.cloud).offered;
  return isNewerPluginVersion(offered, plugin.version) ? offered : null;
}
/** Ids of the installed plugins the market can upgrade on this MinCode. */
export function upgradablePluginIds(entries: CatalogEntry[]): string[] {
  return entries.filter(entry => catalogUpgradeVersion(entry) !== null).map(entry => entry.id);
}
function matchesStatus(entry: CatalogEntry, status: CatalogStatus): boolean {
  if (status === "all") return true;
  if (status === "upgradable") return catalogUpgradeVersion(entry) !== null;
  return entry.installed === (status === "installed");
}
export function filterPluginCatalog(entries: CatalogEntry[], filter: CatalogFilter): CatalogEntry[] {
  // An install source typed into the search box is not a query.
  const query = installSourceFromSearch(filter.search) ? "" : filter.search;
  const terms = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
  return entries.filter(entry => {
    const meta = entry.metadata;
    const haystack = [meta.name, entry.id, meta.description, "author" in meta ? meta.author : "", ...(meta.tags ?? [])].join(" ").toLocaleLowerCase();
    return (filter.category === "all" || entry.category === filter.category)
      && (filter.source === "all" || entry.source === filter.source || (filter.source === "cloud" && !!entry.cloud))
      && matchesStatus(entry, filter.status)
      && terms.every(term => haystack.includes(term));
  });
}
// Facet counts respect search/source/status but not the selected category.
export function countPluginCategories(entries: CatalogEntry[], filter: CatalogFilter) {
  const matches = filterPluginCatalog(entries, { ...filter, category: "all" });
  return Object.fromEntries([ ["all", matches.length], ...pluginCategories.map(category => [category, matches.filter(entry => entry.category === category).length]) ]) as Record<PluginCategory | "all", number>;
}
export function pluginCanStart(plugin: PluginMeta) {
  return plugin.state !== "error";
}
export function pluginHomepage(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  try {
    // URL normalizes empty userinfo away, so reject it before parsing as well.
    const authority = value.trim().match(/^https?:\/\/([^/?#]*)/i)?.[1];
    if (!authority || authority.includes("@")) return undefined;
    const url = new URL(value);
    return ["https:", "http:"].includes(url.protocol) && !!url.hostname && !url.username && !url.password ? url.href : undefined;
  } catch { return undefined; }
}
