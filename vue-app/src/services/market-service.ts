import { isPluginCategory, pluginHomepage, type PluginCategory } from "@/domain/plugins/catalog";
import { parsePluginInstallPreview, type PluginInstallPreview } from "@/domain/plugins/install-preview";
import { PLUGIN_ICONS } from "@/domain/plugin-icons";
import { invokeBackend } from "@/services/desktop-bridge";

/** Untrusted, display-only cloud product metadata; not an installable release. */
export interface MarketPlugin {
  product_key: string;
  name: string;
  slug: string;
  description: string;
  homepage: string;
  icon: string;
  category: PluginCategory;
  tags: string[];
  author: string | null;
  /** Optional https cover published by the cloud; empty when absent or unusable. */
  cover_url: string;
  latest_published_version: string | null;
  updated_at: number;
  /** Enabling the plugin needs a paid MinCode host plan (annual or perpetual). */
  requires_paid_host: boolean;
  /** MinCode versions the latest release supports: minimum inclusive, maximum exclusive; "" = no bound. */
  min_host_version: string;
  max_host_version_exclusive: string;
  /** The latest release rated for this MinCode; "" when nothing is published. */
  host_compatibility: MarketHostCompatibility;
  /** Newest release this MinCode can run (may be older than the latest), or null when none fits. */
  compatible_version: string | null;
  /** Cloud popularity figures, display only; 0 when absent or malformed. */
  download_count: number;
  like_count: number;
}

export type MarketHostCompatibility = "" | "compatible" | "host_too_old" | "host_too_new";
const HOST_COMPATIBILITY: readonly string[] = ["", "compatible", "host_too_old", "host_too_new"];
const SEMVER = /^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$/;

/** A version string the Go side already validated; anything else reads as absent. */
function optionalVersion(value: unknown): string {
  return typeof value === "string" && value.length <= 64 && SEMVER.test(value) ? value : "";
}

const marketIcons = new Set(["", ...PLUGIN_ICONS]);

/**
 * The cover is decoration, so an unusable URL degrades to "no cover" instead of
 * rejecting the listing. Only https origins are rendered: the WebView blocks
 * mixed content, and fragments or credentials have no place in an image source.
 */
export function marketCoverUrl(value: unknown): string {
  if (typeof value !== "string" || !value || value.length > 500) return "";
  try {
    const url = new URL(value);
    return url.protocol === "https:" && url.hostname !== "" && url.username === "" && url.password === "" && url.hash === ""
      ? value : "";
  } catch {
    return "";
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// Mirrors the Go decoder: anything but a non-negative integer reads as zero rather than rejecting the entry.
function displayCount(value: unknown): number {
  return Number.isSafeInteger(value) && (value as number) >= 0 ? (value as number) : 0;
}

export function parseMarketPage(value: unknown, page: number, limit: number): MarketPlugin[] {
  if (!isRecord(value) || value.page !== page || value.limit !== limit || !Array.isArray(value.items) || value.items.length > limit) {
    throw new Error("Invalid market pagination response");
  }
  return value.items.map((item: unknown) => {
    if (!isRecord(item) || typeof item.product_key !== "string" || !/^[a-z0-9][a-zA-Z0-9._-]{1,127}$/.test(item.product_key)
      || typeof item.name !== "string" || !item.name.trim() || item.name.length > 160
      || typeof item.slug !== "string" || !/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,159}$/.test(item.slug)
      || typeof item.description !== "string" || item.description.length > 4096
      || typeof item.homepage !== "string" || item.homepage.length > 500 || (item.homepage !== "" && !pluginHomepage(item.homepage))
      || (item.icon != null && (typeof item.icon !== "string" || !marketIcons.has(item.icon)))
      || !isPluginCategory(item.category) || !Array.isArray(item.tags) || item.tags.length > 8
      || !item.tags.every((tag: unknown) => typeof tag === "string" && !!tag.trim() && tag.length <= 96 && [...tag].length <= 24)
      || (item.author != null && (typeof item.author !== "string" || !item.author.trim() || item.author.length > 120))
      || (item.latest_published_version != null && (typeof item.latest_published_version !== "string" || !/^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$/.test(item.latest_published_version) || item.latest_published_version.length > 64))
      || !Number.isSafeInteger(item.updated_at) || (item.updated_at as number) < 0
      || (item.requires_paid_host != null && typeof item.requires_paid_host !== "boolean")
      || (item.host_compatibility != null && (typeof item.host_compatibility !== "string" || !HOST_COMPATIBILITY.includes(item.host_compatibility)))
      // Mirrors the Go decoder: a present cover must be a string; only the
      // content of that string may degrade to "no cover".
      || (item.cover_url != null && typeof item.cover_url !== "string")) {
      throw new Error("Invalid market plugin metadata");
    }
    // Preserve only the allowlisted fields; never forward arbitrary server fields to the UI.
    return {
      product_key: item.product_key, name: item.name, slug: item.slug,
      description: item.description, homepage: item.homepage, icon: (item.icon as string | null) ?? "", category: item.category,
      tags: item.tags, author: (item.author as string | null) ?? null,
      cover_url: marketCoverUrl(item.cover_url),
      latest_published_version: (item.latest_published_version as string | null) ?? null, updated_at: item.updated_at,
      requires_paid_host: item.requires_paid_host === true,
      min_host_version: optionalVersion(item.min_host_version),
      max_host_version_exclusive: optionalVersion(item.max_host_version_exclusive),
      host_compatibility: (item.host_compatibility as MarketHostCompatibility | undefined) ?? "",
      compatible_version: optionalVersion(item.compatible_version) || null,
      download_count: displayCount(item.download_count),
      like_count: displayCount(item.like_count),
    } as MarketPlugin;
  });
}

export async function listMarketPlugins(isCurrent: () => boolean): Promise<{ items: MarketPlugin[]; truncated: boolean }> {
  const limit = 50;
  const items: MarketPlugin[] = [];
  for (let page = 1; page <= 12 && isCurrent(); page++) {
    const response = await invokeBackend<unknown>("market_plugins_list", { page, limit });
    if (!isCurrent()) break;
    const batch = parseMarketPage(response, page, limit);
    items.push(...batch);
    if (batch.length < limit) return { items, truncated: false };
  }
  return { items, truncated: isCurrent() && items.length === 600 };
}

/** A package downloaded with the signed-in cloud account and checked against the published size and SHA-256. */
export interface MarketInstallPreview {
  preview: PluginInstallPreview;
  version: string;
  sha256: string;
  versionChange: "install" | "upgrade";
  requiresPaidHost: boolean;
}

export async function previewMarketPluginInstall(productKey: string): Promise<MarketInstallPreview> {
  const value = await invokeBackend<unknown>("market_plugin_install_preview", { productKey });
  if (!isRecord(value) || typeof value.version !== "string" || typeof value.sha256 !== "string" || !/^[a-f0-9]{64}$/.test(value.sha256)
    || (value.versionChange !== "install" && value.versionChange !== "upgrade")
    || (value.requiresPaidHost != null && typeof value.requiresPaidHost !== "boolean")) {
    throw new Error("Invalid market install preview");
  }
  const preview = parsePluginInstallPreview(value.preview);
  if (preview.manifest.id !== productKey || preview.manifest.version !== value.version) {
    throw new Error("Market package does not match the requested plugin");
  }
  return { preview, version: value.version, sha256: value.sha256, versionChange: value.versionChange, requiresPaidHost: value.requiresPaidHost === true };
}
