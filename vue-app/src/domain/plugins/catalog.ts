import type { OfficialPlugin, PluginMeta } from "../../services/plugin-service";

/** Categories a plugin.json may declare; the host validates the same list (plugins/api/categories.go). */
export const manifestCategories = ["database", "spreadsheet", "web", "api", "network", "development", "productivity", "other"] as const;
export const pluginCategories = [
  "database", "spreadsheet", "web", "api", "network", "messaging", "development", "devops", "editor",
  "ai", "document", "productivity", "language", "theme", "entertainment", "other",
] as const;
export type ManifestCategory = typeof manifestCategories[number];
export type PluginCategory = typeof pluginCategories[number];
export type CatalogSource = "all" | "official" | "local";
export type CatalogStatus = "all" | "installed" | "available";
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
  metadata: PluginMeta | OfficialPlugin;
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
export function mergePluginCatalog(plugins: PluginMeta[], official: OfficialPlugin[]): CatalogEntry[] {
  const installed = new Map(plugins.map(plugin => [plugin.id, plugin]));
  const bundled = new Map(official.map(plugin => [plugin.id, plugin]));
  return [...new Set([...bundled.keys(), ...installed.keys()])].map(id => {
    const plugin = installed.get(id);
    const official = bundled.get(id);
    const metadata = (plugin ?? official)!;
    const category = metadata.category;
    return { id, plugin, official, metadata,
      category: isPluginCategory(category) ? category : "other",
      installed: !!plugin || official?.installed === true,
      source: official ? "official" : "local" };
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
function matchesStatus(entry: CatalogEntry, status: CatalogStatus): boolean {
  if (status === "all") return true;
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
      && (filter.source === "all" || entry.source === filter.source)
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
